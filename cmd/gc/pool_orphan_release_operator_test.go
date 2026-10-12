package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// Scope: releaseOrphanedPoolAssignments must leave a bead assigned to the
// reserved operator identity alone. The pool-name address case is pinned in
// pool_orphan_release_addressee_test.go and everything else about the sweep in
// pool_session_name_test.go.
//
// Why it exists (ci-qq99de). An operator act, ci-x0t427, was assigned to
// "human" and routed to a session-backed agent. The controller's orphan sweep
// cleared the assignee about 5 s after each write, measured in the hq store's
// Dolt history (16:46:53 -> 16:46:58Z, 17:36:52 -> 17:36:58Z) and matched by
// "released orphaned pool work: ci-x0t427" in the supervisor log at both
// instants. "human" is reserved by session.ValidateAlias, so no session can
// EVER bear it, and the sweep read that permanent absence as a dead holder.
// The bead fell off the operator's queue with nothing reported.
//
// Run: go test ./cmd/gc/ -run 'OrphanReleaseOperator'

// TestOrphanReleaseOperatorAssigneeSurvives is the regression. Both rows carry
// a route, which is what makes the sweep consider them at all, and the
// in_progress row reaches the CAS release path where the open row reaches the
// recheck path, so a guard placed inside only one write path fails one row.
func TestOrphanReleaseOperatorAssigneeSurvives(t *testing.T) {
	store := beads.NewMemStore()
	cfg := &config.City{Agents: []config.Agent{
		{Name: "worker", MinActiveSessions: intPtr(0), MaxActiveSessions: intPtr(3)},
	}}

	mk := func(title, status, assignee string) beads.Bead {
		t.Helper()
		b, err := store.Create(beads.Bead{
			Title:    title,
			Type:     "task",
			Status:   status,
			Assignee: assignee,
			Metadata: map[string]string{"gc.routed_to": "worker"},
		})
		if err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
		return b
	}

	open := mk("operator act, open", "open", "human")
	inProgress := mk("operator act, in progress", "in_progress", "human")
	// The control row: an identity nothing bears and nothing reserves. It
	// MUST still be released, so a fix that disables the sweep, or that
	// spares every unknown assignee, cannot pass.
	dead := mk("dead slot", "open", "worker-gone-42")

	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, "", nil,
		[]beads.Bead{open, inProgress, dead}, nil, nil, nil,
	)
	if len(released) != 1 || released[0].ID != dead.ID {
		t.Errorf("released = %v, want only %s; the operator's queue is not a dead session", released, dead.ID)
	}
	for _, b := range []beads.Bead{open, inProgress} {
		got, err := store.Get(b.ID)
		if err != nil {
			t.Fatalf("get %s: %v", b.ID, err)
		}
		if got.Assignee != "human" {
			t.Errorf("%q: Assignee = %q, want %q -- clearing it drops the bead off the operator's queue silently", b.Title, got.Assignee, "human")
		}
		if got.Status != b.Status {
			t.Errorf("%q: Status = %q, want %q", b.Title, got.Status, b.Status)
		}
	}
	got, err := store.Get(dead.ID)
	if err != nil {
		t.Fatalf("get %s: %v", dead.ID, err)
	}
	if got.Assignee != "" {
		t.Errorf("dead slot: Assignee = %q, want cleared", got.Assignee)
	}
}
