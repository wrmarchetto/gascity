package main

import (
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// recordingWriteWorkStore records every Update/SetMetadata call the
// work-assignment WRITE façade emits while delegating to a MemStore for real
// effects. It proves the façade emits byte-identical bead writes to the raw ops
// it replaces (same UpdateOpts pointers/values, same metadata patches).
type recordingWriteWorkStore struct {
	*beads.MemStore
	listQueries []beads.ListQuery
	updates     []recordedUpdate
	metaSets    []recordedMetaSet
}

type recordedUpdate struct {
	id   string
	opts beads.UpdateOpts
}

type recordedMetaSet struct {
	id    string
	key   string
	value string
}

func newRecordingWriteWorkStore() *recordingWriteWorkStore {
	return &recordingWriteWorkStore{MemStore: beads.NewMemStore()}
}

func (s *recordingWriteWorkStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	s.listQueries = append(s.listQueries, q)
	return s.MemStore.List(q)
}

// Update/SetMetadata record the exact op and report success WITHOUT delegating
// to the MemStore. The byte-identity asserts only need the captured op; the
// MemStore.Create path rewrites bead IDs to gc-N, so delegating would force the
// tests to round-trip generated IDs. Recording-only keeps the asserts pinned to
// the literal beads the façade was asked to write.
func (s *recordingWriteWorkStore) Update(id string, opts beads.UpdateOpts) error {
	s.updates = append(s.updates, recordedUpdate{id: id, opts: opts})
	return nil
}

func (s *recordingWriteWorkStore) SetMetadata(id, key, value string) error {
	s.metaSets = append(s.metaSets, recordedMetaSet{id: id, key: key, value: value})
	return nil
}

// heldBeadInRecordingStore creates a bead in rec's embedded MemStore and
// really claims it under assignee, returning the snapshot a caller would have
// enumerated. The metadata is stamped on the returned snapshot only, which is
// where workrelease.Metadata reads the routing keys from.
//
// The release path arbitrates an in_progress+assigned bead through the store's
// ReleaseIfCurrent CAS, so a literal bead that was never stored makes the CAS
// refuse and the façade emit nothing. rec.Update deliberately does not
// delegate to the MemStore, so the claim is driven on the MemStore directly.
func heldBeadInRecordingStore(t *testing.T, rec *recordingWriteWorkStore, assignee string, metadata map[string]string) beads.Bead {
	t.Helper()
	created, err := rec.Create(beads.Bead{Title: "held", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inProgress := "in_progress"
	if err := rec.MemStore.Update(created.ID, beads.UpdateOpts{Status: &inProgress, Assignee: &assignee}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	item, err := rec.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if item.Status != "in_progress" || item.Assignee != assignee {
		t.Fatalf("setup did not take: status=%q assignee=%q", item.Status, item.Assignee)
	}
	item.Metadata = metadata
	return item
}

func derefStr(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestWorkAssignmentOpenAssignedToBasic_ByteIdenticalQuery asserts the no-flags
// List variant (used by releaseWorkFromClosedSessionBead) emits exactly
// {Assignee,Status} — no Live, no TierMode — matching the raw probe.
func TestWorkAssignmentOpenAssignedToBasic_ByteIdenticalQuery(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	if _, err := wa.OpenAssignedToBasic("agent-1", "in_progress"); err != nil {
		t.Fatalf("OpenAssignedToBasic: %v", err)
	}
	want := beads.ListQuery{Assignee: "agent-1", Status: "in_progress"}
	if len(rec.listQueries) != 1 || !reflect.DeepEqual(rec.listQueries[0], want) {
		t.Fatalf("List query mismatch:\n got %#v\n want %#v", rec.listQueries, want)
	}
}

// TestWorkAssignmentReleaseWorkBead_OpenStaysOpen asserts that releasing an
// already-open bead emits Update{Assignee:"", Metadata:<clearedAffinity>} with
// NO Status change (status reset is only for in_progress), byte-identical to the
// raw release op.
func TestWorkAssignmentReleaseWorkBead_OpenStaysOpen(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	item := beads.Bead{ID: "w-open", Status: "open", Assignee: "agent-1"}
	if err := wa.ReleaseWorkBead(item, ""); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}
	if len(rec.updates) != 1 {
		t.Fatalf("expected 1 Update, got %d: %#v", len(rec.updates), rec.updates)
	}
	got := rec.updates[0]
	if got.id != "w-open" {
		t.Fatalf("Update id = %q, want w-open", got.id)
	}
	if derefStr(got.opts.Assignee) != "" {
		t.Fatalf("Assignee = %q, want empty-string clear", derefStr(got.opts.Assignee))
	}
	if got.opts.Status != nil {
		t.Fatalf("Status should be nil for an already-open bead, got %q", *got.opts.Status)
	}
	wantMeta := clearedSessionAffinityMetadata()
	if !reflect.DeepEqual(got.opts.Metadata, wantMeta) {
		t.Fatalf("Metadata mismatch:\n got %#v\n want %#v", got.opts.Metadata, wantMeta)
	}
}

// TestWorkAssignmentReleaseWorkBead_InProgressGoesThroughTheGuardedPath
// asserts an in_progress release performs the status/assignee swap through the
// store's conditional verb and emits the affinity clears as a separate
// metadata-only write.
//
// It no longer asserts a single byte-identical Update carrying
// status+assignee: an in_progress bead assigned to a named holder is exactly
// the shape ReleaseIfCurrent arbitrates, so that Update is the op this path
// deliberately stopped emitting (ci-23nak7). The unconditional single-Update
// contract is still pinned for the shapes the CAS cannot cover, by
// _OpenStaysOpen and the two run-target-fallback tests above.
func TestWorkAssignmentReleaseWorkBead_InProgressGoesThroughTheGuardedPath(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	// The CAS is promoted from the embedded MemStore, so the bead has to
	// really be there and really be held for it to win. rec.Update does not
	// delegate, so the claim is driven on the MemStore directly.
	created, err := rec.Create(beads.Bead{Title: "held", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inProgress, holder := "in_progress", "agent-1"
	if err := rec.MemStore.Update(created.ID, beads.UpdateOpts{Status: &inProgress, Assignee: &holder}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	item, err := rec.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := wa.ReleaseWorkBead(item, ""); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}

	// The CAS did the swap.
	got, err := rec.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if got.Status != "open" {
		t.Fatalf("Status = %q, want open", got.Status)
	}
	if got.Assignee != "" {
		t.Fatalf("Assignee = %q, want cleared", got.Assignee)
	}

	// The façade emitted exactly the metadata-only follow-up write, carrying
	// no status/assignee of its own.
	if len(rec.updates) != 1 {
		t.Fatalf("expected 1 metadata Update, got %d: %#v", len(rec.updates), rec.updates)
	}
	meta := rec.updates[0]
	if meta.id != created.ID {
		t.Fatalf("Update id = %q, want %q", meta.id, created.ID)
	}
	if meta.opts.Status != nil || meta.opts.Assignee != nil {
		t.Fatalf("metadata write must carry neither status nor assignee, got status=%v assignee=%q", meta.opts.Status, derefStr(meta.opts.Assignee))
	}
	if !reflect.DeepEqual(meta.opts.Metadata, clearedSessionAffinityMetadata()) {
		t.Fatalf("Metadata mismatch:\n got %#v\n want %#v", meta.opts.Metadata, clearedSessionAffinityMetadata())
	}
}

// TestWorkAssignmentReleaseWorkBead_RunTargetFallbackApplied asserts the
// run_target fallback (used by the retire/unclaim path) is written only when the
// bead has neither run_target nor routed_to, byte-identical to the raw op.
func TestWorkAssignmentReleaseWorkBead_RunTargetFallbackApplied(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	item := heldBeadInRecordingStore(t, rec, "agent-1", nil)
	if err := wa.ReleaseWorkBead(item, "worker"); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}
	got := rec.updates[0]
	if got.opts.Metadata[beadmeta.RunTargetMetadataKey] != "worker" {
		t.Fatalf("run_target fallback = %q, want worker", got.opts.Metadata[beadmeta.RunTargetMetadataKey])
	}
}

// TestWorkAssignmentReleaseWorkBead_RunTargetFallbackSkippedWhenRouted asserts
// the fallback is NOT applied when run_target/routed_to are already present.
func TestWorkAssignmentReleaseWorkBead_RunTargetFallbackSkippedWhenRouted(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	item := heldBeadInRecordingStore(t, rec, "agent-1",
		map[string]string{beadmeta.RoutedToMetadataKey: "existing"})
	if err := wa.ReleaseWorkBead(item, "worker"); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}
	got := rec.updates[0]
	if _, ok := got.opts.Metadata[beadmeta.RunTargetMetadataKey]; ok {
		t.Fatalf("run_target fallback must be skipped when routed_to present, got %#v", got.opts.Metadata)
	}
}

// TestWorkAssignmentReassignWorkBead_ByteIdentical asserts reassign emits only
// Update{Assignee:&new}, byte-identical to the raw retire-reassign op.
func TestWorkAssignmentReassignWorkBead_ByteIdentical(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	if err := wa.ReassignWorkBead("w-1", "new-session"); err != nil {
		t.Fatalf("ReassignWorkBead: %v", err)
	}
	if len(rec.updates) != 1 {
		t.Fatalf("expected 1 Update, got %d", len(rec.updates))
	}
	got := rec.updates[0]
	if got.id != "w-1" || derefStr(got.opts.Assignee) != "new-session" {
		t.Fatalf("reassign = id %q assignee %q, want w-1/new-session", got.id, derefStr(got.opts.Assignee))
	}
	if got.opts.Status != nil || got.opts.Metadata != nil {
		t.Fatalf("reassign must not touch Status/Metadata, got %#v", got.opts)
	}
}

// TestWorkAssignmentClearDetachedProbe_ByteIdentical asserts the detached-probe
// clear emits SetMetadata(id, gc.detached, "") — the empty-string clear contract.
func TestWorkAssignmentClearDetachedProbe_ByteIdentical(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	if err := wa.ClearDetachedProbe("w-1"); err != nil {
		t.Fatalf("ClearDetachedProbe: %v", err)
	}
	want := recordedMetaSet{id: "w-1", key: beadmeta.DetachedMetadataKey, value: ""}
	if len(rec.metaSets) != 1 || rec.metaSets[0] != want {
		t.Fatalf("SetMetadata mismatch:\n got %#v\n want %#v", rec.metaSets, want)
	}
}

// TestWorkAssignmentWrite_NilStoreSafe asserts the write methods tolerate a nil
// underlying store the same way the raw ops did (no panic, no write).
func TestWorkAssignmentWrite_NilStoreSafe(t *testing.T) {
	wa := workAssignmentForStore(beads.WorkStore{Store: nil})
	if err := wa.ReleaseWorkBead(beads.Bead{ID: "x"}, ""); err != nil {
		t.Fatalf("nil store ReleaseWorkBead: %v", err)
	}
	if err := wa.ReassignWorkBead("x", "y"); err != nil {
		t.Fatalf("nil store ReassignWorkBead: %v", err)
	}
	if err := wa.ClearDetachedProbe("x"); err != nil { // must not panic
		t.Fatalf("nil store ClearDetachedProbe: %v", err)
	}
	if _, err := wa.OpenAssignedToBasic("a", "open"); err != nil {
		t.Fatalf("nil store OpenAssignedToBasic: %v", err)
	}
}
