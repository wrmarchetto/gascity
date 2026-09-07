package main

// Scope: the guarded-release contract of workAssignment.ReleaseWorkBead, the
// write both session-retirement unclaim paths issue
// (releaseWorkFromClosedSessionBead and
// unclaimWorkAssignedToRetiredSessionInfo, cmd/gc/session_beads.go).
//
// Why this suite exists: both callers READ the work with
// OpenAssignedTo(target.Assignee, ...) and then WRITE with ReleaseWorkBead,
// and nothing between the two writes pins the assignee they matched on. A
// bead reassigned inside that window was released out from under its new
// holder, which -- with claim_routes putting two provider pools on one route
// -- hands one bead to a second pool while the first is still working it.
// These tests drive the window directly, because no suite that releases a
// bead nobody else touches can represent it.
//
// It delegates the byte-identity of the emitted UpdateOpts to
// work_assignment_write_test.go, and the equivalent CAS coverage for the
// orphan sweep to pool_session_name's own tests.
//
// Run: go test ./cmd/gc/ -run 'ReleaseWorkBead.*(Moved|StillMatches|Unsupported|OpenBead)'

import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// inProgressBead creates one work bead in store and drives it to in_progress
// under assignee, returning the stored snapshot. MemStore.Create forces
// status open and rewrites the id, so the claim is a second write and the
// caller must use the returned id.
func inProgressBead(t *testing.T, store beads.Store, assignee string) beads.Bead {
	t.Helper()
	created, err := store.Create(beads.Bead{Title: "work", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inProgress := "in_progress"
	if err := store.Update(created.ID, beads.UpdateOpts{
		Status:   &inProgress,
		Assignee: &assignee,
	}); err != nil {
		t.Fatalf("Update to in_progress: %v", err)
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "in_progress" || got.Assignee != assignee {
		t.Fatalf("setup did not take: status=%q assignee=%q", got.Status, got.Assignee)
	}
	return got
}

// TestReleaseWorkBeadRefusesWhenAssigneeMovedSinceSnapshot pins the invariant
// that a release names the holder it intends to displace: a bead whose
// assignee changed between the caller's read and this write must be left
// alone.
//
// The snapshot passed in deliberately disagrees with the store, which is the
// only way to represent the read-then-write window -- the caller cannot
// observe its own staleness, so the guard has to live in the write. Without
// it the new holder keeps working a bead this call has reopened for anyone,
// and the pool that picks it up next is a different provider's.
func TestReleaseWorkBeadRefusesWhenAssigneeMovedSinceSnapshot(t *testing.T) {
	store := beads.NewMemStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: store})

	// The store's truth: agent-2 claimed it after the caller's read.
	current := inProgressBead(t, store, "agent-2")

	// The caller's stale snapshot: it matched agent-1 and still believes it.
	stale := current
	stale.Assignee = "agent-1"

	if err := wa.ReleaseWorkBead(stale, ""); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}

	got, err := store.Get(current.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Assignee != "agent-2" {
		t.Fatalf("assignee = %q, want agent-2 retained: the release displaced a holder it never matched", got.Assignee)
	}
	if got.Status != "in_progress" {
		t.Fatalf("status = %q, want in_progress retained: a reopened bead is claimable by another pool", got.Status)
	}
}

// TestReleaseWorkBeadRefusalWritesNothing pins that a refused release emits
// NO write at all, not merely no status/assignee change.
//
// This exists because the sibling test above survived a mutation that made
// the refusal branch unreachable: control then fell through to the
// metadata-only follow-up write, which leaves status and assignee untouched
// and so was invisible to a state assertion. That write is not benign -- it
// clears gc.session_affinity and gc.continuation_group, and can stamp a
// run_target fallback, on a bead a DIFFERENT live session is holding.
//
// recordingWriteWorkStore records Updates without delegating, while
// ReleaseIfCurrent is promoted from the embedded MemStore and really applies,
// so an emitted write is observable here in a way it is not through a plain
// store.
func TestReleaseWorkBeadRefusalWritesNothing(t *testing.T) {
	rec := newRecordingWriteWorkStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: rec})

	current := heldBeadInRecordingStore(t, rec, "agent-2", nil)
	stale := current
	stale.Assignee = "agent-1"

	if err := wa.ReleaseWorkBead(stale, "worker"); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}
	if len(rec.updates) != 0 {
		t.Fatalf("a refused release emitted %d write(s), want none: %#v", len(rec.updates), rec.updates)
	}
	if len(rec.metaSets) != 0 {
		t.Fatalf("a refused release emitted %d SetMetadata call(s), want none: %#v", len(rec.metaSets), rec.metaSets)
	}
}

// TestReleaseWorkBeadReleasesWhenAssigneeStillMatches is the positive control
// for the test above: with an agreeing snapshot the release must still fire,
// clear the assignee, reopen the bead, and stamp the same affinity clears the
// unconditional path stamps. Without this, a guard that refused everything
// would satisfy the refusal test and silently strand every retired session's
// work.
func TestReleaseWorkBeadReleasesWhenAssigneeStillMatches(t *testing.T) {
	store := beads.NewMemStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: store})

	current := inProgressBead(t, store, "agent-1")
	if err := wa.ReleaseWorkBead(current, ""); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}

	got, err := store.Get(current.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want cleared", got.Assignee)
	}
	if got.Status != "open" {
		t.Fatalf("status = %q, want open -- an in_progress bead is invisible to every work_query tier", got.Status)
	}
	for _, key := range beadmeta.SessionAffinityMetadataKeys {
		if v := got.Metadata[key]; v != "" {
			t.Fatalf("metadata[%q] = %q, want cleared by the guarded path too", key, v)
		}
	}
}

// TestReleaseWorkBeadOpenBeadTakesUnconditionalPath pins the deliberate
// absence of a guard for open-status beads. ReleaseIfCurrent's contract
// covers in_progress assignments only, so an open bead parked on a pool name
// has no CAS available and must keep the unconditional write -- the same
// carve-out releasePoolAssignmentIfCurrent makes at
// cmd/gc/pool_session_name.go. Narrowing this to a CAS would strand every
// open-status parked bead a retiring session leaves behind.
func TestReleaseWorkBeadOpenBeadTakesUnconditionalPath(t *testing.T) {
	store := beads.NewMemStore()
	wa := workAssignmentForStore(beads.WorkStore{Store: store})

	created, err := store.Create(beads.Bead{Title: "parked", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	parked := "pool-name"
	if err := store.Update(created.ID, beads.UpdateOpts{Assignee: &parked}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	item, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if err := wa.ReleaseWorkBead(item, ""); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Assignee != "" {
		t.Fatalf("assignee = %q, want cleared on the unconditional open-bead path", got.Assignee)
	}
}

// releaseUnsupportedStore is a store whose ReleaseIfCurrent reports the verb
// unsupported, so the guarded path must fall back to the unconditional write
// rather than silently declining to release. It REFUSES any other
// conditional-release call shape by failing the test, so a guard that reached
// it with an unexpected expectation cannot pass unnoticed.
type releaseUnsupportedStore struct {
	*beads.MemStore
	t              *testing.T
	wantExpected   string
	conditionalHit int
}

func (s *releaseUnsupportedStore) ReleaseIfCurrent(_, expectedAssignee string) (bool, error) {
	s.t.Helper()
	if expectedAssignee != s.wantExpected {
		s.t.Fatalf("ReleaseIfCurrent expected assignee = %q, want %q -- the guard must name the snapshot's holder", expectedAssignee, s.wantExpected)
	}
	s.conditionalHit++
	return false, beads.ErrConditionalReleaseUnsupported
}

// TestReleaseWorkBeadUnsupportedConditionalFallsBack pins that a store which
// cannot do the CAS still gets its work released. A bd backend predating the
// conditional verb would otherwise leave every retired session's work
// in_progress forever, which reads as a stuck queue rather than as a missing
// capability.
func TestReleaseWorkBeadUnsupportedConditionalFallsBack(t *testing.T) {
	backing := beads.NewMemStore()
	store := &releaseUnsupportedStore{MemStore: backing, t: t, wantExpected: "agent-1"}
	wa := workAssignmentForStore(beads.WorkStore{Store: store})

	current := inProgressBead(t, store, "agent-1")
	if err := wa.ReleaseWorkBead(current, ""); err != nil {
		t.Fatalf("ReleaseWorkBead: %v", err)
	}
	if store.conditionalHit == 0 {
		t.Fatal("the guarded path never attempted ReleaseIfCurrent, so the fallback proves nothing")
	}
	got, err := store.Get(current.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "open" || got.Assignee != "" {
		t.Fatalf("status=%q assignee=%q, want the unconditional fallback to have released", got.Status, got.Assignee)
	}
}

// releaseErroringStore reports an operational fault from the CAS. Unlike an
// unsupported verb, this must NOT fall back: a store that could not answer
// leaves ownership unresolved, and releasing anyway is the double-hold this
// guard exists to prevent.
type releaseErroringStore struct {
	*beads.MemStore
	err error
}

func (s *releaseErroringStore) ReleaseIfCurrent(string, string) (bool, error) {
	return false, s.err
}

// TestReleaseWorkBeadConditionalErrorDoesNotRelease pins that an unresolved
// CAS fails CLOSED. Treating a store fault as "not currently held" is how a
// transient error becomes a second holder.
func TestReleaseWorkBeadConditionalErrorDoesNotRelease(t *testing.T) {
	backing := beads.NewMemStore()
	wantErr := errors.New("store unreachable")
	store := &releaseErroringStore{MemStore: backing, err: wantErr}
	wa := workAssignmentForStore(beads.WorkStore{Store: store})

	current := inProgressBead(t, store, "agent-1")
	err := wa.ReleaseWorkBead(current, "")
	if err == nil {
		t.Fatal("ReleaseWorkBead returned nil on an unresolved CAS; the caller cannot distinguish released from unknown")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want it to wrap %v", err, wantErr)
	}
	got, gerr := store.Get(current.ID)
	if gerr != nil {
		t.Fatalf("Get: %v", gerr)
	}
	if got.Status != "in_progress" || got.Assignee != "agent-1" {
		t.Fatalf("status=%q assignee=%q, want the holder retained after an unresolved CAS", got.Status, got.Assignee)
	}
}
