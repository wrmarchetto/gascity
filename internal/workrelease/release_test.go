package workrelease

import (
	"bytes"
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
