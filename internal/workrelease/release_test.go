package workrelease

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// Scope: the seat partition and the one-store sweep, at the package boundary.
// The two callers pin their own wiring elsewhere -- cmd/gc at the CLI boundary
// (cmd_session_close_addressed_work_test.go) and internal/api over real HTTP
// (session_close_work_release_test.go) -- and neither of those can tell whether
// a rule change here was correct or merely consistent, which is what this suite
// is for.
//
// Run: go test ./internal/workrelease/

const (
	testAlias   = "worker-2"
	testNamed   = "reviewer"
	testRuntime = "worker-cs-1"
)

func testSessionBead() beads.Bead {
	return beads.Bead{
		ID: "gc-seat",
		Metadata: map[string]string{
			"session_name":              testRuntime,
			"alias":                     testAlias,
			"configured_named_identity": testNamed,
			"alias_history":             "worker-9",
			"template":                  "worker",
		},
	}
}

func testIdentities() []string {
	return []string{"gc-seat", testRuntime, testNamed, testAlias, "worker-9"}
}

// TestSeatIdentityScopeCoversEveryIdentity pins the property that makes the
// split safe to extend: it is a FILTER, so the halves reassemble into exactly
// the list handed in. The failure it guards is a rewrite that re-derives the
// list from the session bead, where an identity the caller knows about but this
// package does not would be swept by neither half, silently.
func TestSeatIdentityScopeCoversEveryIdentity(t *testing.T) {
	ephemeral, durable := SeatIdentityScope(SeatFromBead(testSessionBead()), testIdentities())

	half := map[string]string{}
	for _, id := range ephemeral {
		half[id] = "ephemeral"
	}
	for _, id := range durable {
		if prior, dup := half[id]; dup {
			t.Fatalf("identity %q classified twice (%s and durable)", id, prior)
		}
		half[id] = "durable"
	}
	if len(half) != len(testIdentities()) {
		t.Fatalf("split has %d identities, want %d: ephemeral=%v durable=%v", len(half), len(testIdentities()), ephemeral, durable)
	}
	want := map[string]string{
		"gc-seat":   "ephemeral",
		testRuntime: "ephemeral",
		testAlias:   "durable",
		testNamed:   "durable",
		"worker-9":  "durable",
	}
	for id, expected := range want {
		if got := half[id]; got != expected {
			t.Errorf("identity %q classified %q, want %q", id, got, expected)
		}
	}
}

// TestTargetsSweepDurableIdentitiesOnlyWhenClaimed is the rule in one assertion:
// an ephemeral identity is swept in both statuses, a durable one only in
// in_progress. An open bead under a durable identity was never claimed, so there
// is nothing to release and no standing to rewrite the address.
func TestTargetsSweepDurableIdentitiesOnlyWhenClaimed(t *testing.T) {
	got := map[string][]string{}
	for _, target := range Targets(SeatFromBead(testSessionBead()), testIdentities(), SeatSurvives) {
		got[target.Assignee] = append(got[target.Assignee], target.Status)
	}
	for _, id := range []string{"gc-seat", testRuntime} {
		if len(got[id]) != 2 {
			t.Errorf("ephemeral identity %q swept in %v, want both statuses", id, got[id])
		}
	}
	for _, id := range []string{testAlias, testNamed, "worker-9"} {
		if len(got[id]) != 1 || got[id][0] != "in_progress" {
			t.Errorf("durable identity %q swept in %v, want in_progress only", id, got[id])
		}
	}
}

// TestTargetsSweepEverythingWhenTheSeatRetires is the carve-out: a config
// removal takes the seat's identities with it, so an address on one reaches
// nobody and must release like a claim.
func TestTargetsSweepEverythingWhenTheSeatRetires(t *testing.T) {
	for _, target := range Targets(SeatFromBead(testSessionBead()), testIdentities(), SeatRetired) {
		if target.Status == "open" && target.Assignee == testAlias {
			return
		}
	}
	t.Fatal("a retired seat's alias is never swept in open status; an address on it would strand")
}

// TestFromEndedSessionReleasesClaimsAndKeepsSeatAddresses drives the sweep over
// a real store, with every row differing only in (assignee, status), so a build
// that treats two rows alike cannot pass.
func TestFromEndedSessionReleasesClaimsAndKeepsSeatAddresses(t *testing.T) {
	store := beads.NewMemStore()
	sessionBead := testSessionBead()

	mk := func(title, assignee, status string) beads.Bead {
		t.Helper()
		b, err := store.Create(beads.Bead{Title: title, Type: "task", Status: "open", Assignee: assignee})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		if status != "open" {
			if err := store.Update(b.ID, beads.UpdateOpts{Status: &status}); err != nil {
				t.Fatalf("set %q status: %v", title, err)
			}
		}
		return b
	}
	addressed := mk("addressed to the seat", testAlias, "open")
	claimed := mk("claimed on the seat", testAlias, "in_progress")
	ownID := mk("addressed to the session bead", "gc-seat", "open")
	// Mail carries its recipient in assignee and has no claim semantics: clearing
	// it destroys the wisp's only route to an inbox (ra-59207).
	mail, err := store.Create(beads.Bead{Title: "mail", Type: "message", Status: "open", Assignee: "gc-seat"})
	if err != nil {
		t.Fatalf("create mail: %v", err)
	}

	var stderr bytes.Buffer
	released, failed := FromEndedSession(store, sessionBead, testIdentities(), "worker", SeatSurvives, &stderr)
	if failed != 0 {
		t.Fatalf("failed = %d, want 0; stderr=%s", failed, stderr.String())
	}
	if released != 2 {
		t.Errorf("released = %d, want 2 (the seat claim and the session-bead address)", released)
	}

	check := func(b beads.Bead, wantAssignee, why string) {
		t.Helper()
		got, err := store.Get(b.ID)
		if err != nil {
			t.Fatalf("get %s: %v", b.ID, err)
		}
		if got.Assignee != wantAssignee {
			t.Errorf("%q: Assignee = %q, want %q (%s)", b.Title, got.Assignee, wantAssignee, why)
		}
	}
	check(addressed, testAlias, "an open bead under a seat identity was never claimed")
	check(claimed, "", "a claim releases in full so any slot on the route can take it")
	check(ownID, "", "no future session bears this session bead's ID")
	check(mail, "gc-seat", "a mail wisp's assignee is its inbox route, not a claim")

	// The released claim carried no route, so the fallback must be stamped or it
	// lands open, unassigned and unrouted.
	gotClaimed, err := store.Get(claimed.ID)
	if err != nil {
		t.Fatalf("get claimed: %v", err)
	}
	if gotClaimed.Status != "open" {
		t.Errorf("claimed status = %q, want open", gotClaimed.Status)
	}
	if route := gotClaimed.Metadata["gc.run_target"]; route != "worker" {
		t.Errorf("claimed gc.run_target = %q, want %q", route, "worker")
	}
}

// --- the release window ----------------------------------------------------

// racedStore serves a STALE List -- the rows as they stood before a concurrent
// re-claim -- over a live store that has already moved on, which is the only
// way to drive FromEndedSession's read-then-write window from outside. The
// window is real and unreachable with a single store: List and Update both hit
// the same map, so a live MemStore can never disagree with itself.
//
// It REFUSES a List carrying any field beyond the (Assignee, Status) pair
// FromEndedSession queries on. A fake that answered every query would hand a
// pass to a rewrite that started filtering on something else -- the rows it
// got back would still look right, and the staleness under test would silently
// stop being staged.
type racedStore struct {
	beads.Store
	stale  []beads.Bead
	writes []racedWrite
	casErr error
	noCAS  bool
	t      *testing.T
}

type racedWrite struct {
	id   string
	opts beads.UpdateOpts
}

func (s *racedStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	bare := q
	bare.Assignee = ""
	bare.Status = ""
	if !reflect.DeepEqual(bare, beads.ListQuery{}) {
		s.t.Fatalf("racedStore.List: unscripted query %#v; the fake stages staleness for (Assignee, Status) only", q)
	}
	var out []beads.Bead
	for _, b := range s.stale {
		if q.Assignee != "" && b.Assignee != q.Assignee {
			continue
		}
		if q.Status != "" && b.Status != q.Status {
			continue
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *racedStore) Update(id string, opts beads.UpdateOpts) error {
	s.writes = append(s.writes, racedWrite{id: id, opts: opts})
	return s.Store.Update(id, opts)
}

func (s *racedStore) ReleaseIfCurrent(id, expectedAssignee string) (bool, error) {
	if s.noCAS {
		return false, beads.ErrConditionalReleaseUnsupported
	}
	if s.casErr != nil {
		return false, s.casErr
	}
	releaser, ok := s.Store.(beads.ConditionalAssignmentReleaser)
	if !ok {
		s.t.Fatal("racedStore: backing store has no ReleaseIfCurrent; the CAS arm cannot be reached")
	}
	return releaser.ReleaseIfCurrent(id, expectedAssignee)
}

// heldByNewHolder stages the race: one in_progress bead the ending seat held
// at snapshot time and a live store where a different session already holds
// it. Returns the store wrapper and the bead's id.
func heldByNewHolder(t *testing.T, newHolder string) (*racedStore, string) {
	t.Helper()
	live := beads.NewMemStore()
	b, err := live.Create(beads.Bead{Title: "raced claim", Type: "task", Status: "open", Assignee: newHolder})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	inProgress := "in_progress"
	if err := live.Update(b.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
		t.Fatalf("claim for the new holder: %v", err)
	}
	// The snapshot FromEndedSession is working from: same id, the OLD holder.
	stale := beads.Bead{
		ID: b.ID, Title: "raced claim", Type: "task",
		Status: "in_progress", Assignee: testAlias,
	}
	return &racedStore{Store: live, stale: []beads.Bead{stale}, t: t}, b.ID
}

// TestFromEndedSessionRefusesToDisplaceANewHolder pins the defect ci-q5spdz
// names: the sweep enumerated work by assignee and wrote it unconditionally,
// so a bead re-claimed inside that window was reopened out from under its new
// holder. Because claim_routes puts two provider pools on one route
// (packs/lab/agents/engineer-codex), the reopened bead then goes to the OTHER
// pool while the first is still working it -- the same shape ci-23nak7 fixed
// in ReleaseWorkBead and ci-2hk in the closed-session path.
//
// The assertion is on the NEW holder being retained, not on some error being
// returned: FromEndedSession is best-effort by contract and returns no error,
// so a test keyed on an error can never see this.
//
// Run: go test ./internal/workrelease/ -run Displace
func TestFromEndedSessionRefusesToDisplaceANewHolder(t *testing.T) {
	store, id := heldByNewHolder(t, "worker-3")

	var stderr bytes.Buffer
	released, failed := FromEndedSession(store, testSessionBead(), testIdentities(), "worker", SeatSurvives, &stderr)
	if failed != 0 {
		t.Errorf("failed = %d, want 0; a refusal is not a fault: stderr=%s", failed, stderr.String())
	}
	// Counted as released for the reason ReleaseWorkBead counts it: the tally
	// is release ATTEMPTS that did not fault, and its only consumer refuses to
	// report success on `failed`. A third counter would touch every caller for
	// a reporting nicety no path reads.
	if released != 1 {
		t.Errorf("released = %d, want 1 (one attempt, no fault)", released)
	}

	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if got.Assignee != "worker-3" {
		t.Errorf("Assignee = %q, want %q: the sweep displaced a holder it never enumerated", got.Assignee, "worker-3")
	}
	if got.Status != "in_progress" {
		t.Errorf("Status = %q, want in_progress: the new holder's claim was reopened", got.Status)
	}
}

// TestFromEndedSessionRefusalWritesNothing is the case ci-23nak7's own
// mutation sweep needed a second pass to find, restated here because the same
// hole exists on this path. A refusal that falls through to the metadata-only
// follow-up write leaves status and assignee untouched, so a state assertion
// cannot see it -- and that write is NOT benign: it clears
// gc.session_affinity and gc.continuation_group, and can stamp a run_target
// fallback, on a bead a different live session is holding right now.
//
// So this asserts on writes EMITTED, not on resulting state.
func TestFromEndedSessionRefusalWritesNothing(t *testing.T) {
	store, _ := heldByNewHolder(t, "worker-3")

	var stderr bytes.Buffer
	FromEndedSession(store, testSessionBead(), testIdentities(), "worker", SeatSurvives, &stderr)

	if len(store.writes) != 0 {
		t.Fatalf("writes = %#v, want none: a refused release must emit no write at all", store.writes)
	}
}

// TestFromEndedSessionCASFaultDoesNotDegradeToAnUnconditionalWrite pins the
// direction a fault must fail in. An unresolved CAS means ownership is
// UNKNOWN, and treating unknown as "not held" is exactly how a transient
// backend error becomes a second holder. The contract forbids returning an
// error, so the fault is counted in `failed` -- which is the signal a
// stranded-worker repair reads to refuse to report success.
func TestFromEndedSessionCASFaultDoesNotDegradeToAnUnconditionalWrite(t *testing.T) {
	store, id := heldByNewHolder(t, "worker-3")
	store.casErr = errors.New("backend unavailable")

	var stderr bytes.Buffer
	released, failed := FromEndedSession(store, testSessionBead(), testIdentities(), "worker", SeatSurvives, &stderr)
	if failed != 1 {
		t.Errorf("failed = %d, want 1: an unresolved CAS is a fault, not a refusal", failed)
	}
	if released != 0 {
		t.Errorf("released = %d, want 0", released)
	}
	if len(store.writes) != 0 {
		t.Fatalf("writes = %#v, want none: a CAS fault must not degrade to the unconditional write", store.writes)
	}
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if got.Assignee != "worker-3" {
		t.Errorf("Assignee = %q, want %q", got.Assignee, "worker-3")
	}
	if !bytes.Contains(stderr.Bytes(), []byte("backend unavailable")) {
		t.Errorf("stderr = %q, want the backend error named", stderr.String())
	}
}

// TestFromEndedSessionKeepsTheUnconditionalWriteWhereNoCASApplies pins the
// deliberate absence. ReleaseIfCurrent's contract covers in_progress
// assignments only, and a store may not offer the verb at all, so narrowing
// the guard to every shape would strand exactly the beads this sweep exists to
// recover: an open bead parked on a seat identity, and any bead in a store
// with no conditional verb. Both keep the single unconditional Update, and
// this is the case that fails if a fix refuses instead of falling back.
func TestFromEndedSessionKeepsTheUnconditionalWriteWhereNoCASApplies(t *testing.T) {
	t.Run("store-without-the-verb-still-releases", func(t *testing.T) {
		store, id := heldByNewHolder(t, testAlias)
		store.noCAS = true

		var stderr bytes.Buffer
		released, failed := FromEndedSession(store, testSessionBead(), testIdentities(), "worker", SeatSurvives, &stderr)
		if failed != 0 || released != 1 {
			t.Fatalf("(released, failed) = (%d, %d), want (1, 0); stderr=%s", released, failed, stderr.String())
		}
		got, err := store.Get(id)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.Assignee != "" || got.Status != "open" {
			t.Errorf("(Assignee, Status) = (%q, %q), want (cleared, open): the fallback write did not run", got.Assignee, got.Status)
		}
	})

	t.Run("open-bead-on-a-retired-alias-has-no-CAS-to-use", func(t *testing.T) {
		live := beads.NewMemStore()
		b, err := live.Create(beads.Bead{Title: "parked open", Type: "task", Status: "open", Assignee: testAlias})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		stale := beads.Bead{ID: b.ID, Title: "parked open", Type: "task", Status: "open", Assignee: testAlias}
		store := &racedStore{Store: live, stale: []beads.Bead{stale}, t: t}

		var stderr bytes.Buffer
		released, failed := FromEndedSession(store, testSessionBead(), testIdentities(), "worker", SeatRetired, &stderr)
		if failed != 0 || released != 1 {
			t.Fatalf("(released, failed) = (%d, %d), want (1, 0); stderr=%s", released, failed, stderr.String())
		}
		if len(store.writes) != 1 {
			t.Fatalf("writes = %#v, want exactly one unconditional Update", store.writes)
		}
		if store.writes[0].opts.Status != nil {
			t.Errorf("an already-open bead's release must not restate status, got %q", *store.writes[0].opts.Status)
		}
	})
}
