// cmd/gc/pool_request_priority_rank_test.go
//
// Pins the direction of the pool's session-request ranking: which request
// keeps a session when max_active_sessions binds.
//
// The suite exists because the ordering was inverted and nothing caught it.
// Beads number priority with P0 most urgent, and applyNestedCaps sorted the
// raw number DESCENDING, so a P4 request was admitted under a cap before a P0
// one and a bead whose Priority was nil outranked every populated value
// (ci-7vyl6k). The tests that covered this seam pinned the tie-break at equal
// priority and used out-of-range literals (5, 10) for the priorities
// themselves, which is exactly the shape that agrees with a wrong direction.
//
// So every expectation here is stated in the DECLARED unit -- a bead priority
// in 0-4, P0 most urgent (internal/formula/types.go validates the range) --
// and is driven through ComputePoolDesiredStates from real beads wherever the
// question is about priority. Only the encoding test names rank integers.
//
// What produces a nil Priority IS settled and is pinned here: it is the native
// store's encoding of P2 (internal/beads/native_dolt_store.go), not a missing
// value. The first draft of this suite assumed the opposite and pinned a nil as
// LEAST urgent; that would have ranked every P2 bead in the city below any P3.
//
// What this suite CANNOT represent: these beads are hand-built, so the nil-ness
// is asserted rather than produced by a real store round trip. A test that
// drives beadPriorityRank from a bead written at priority 2 through an actual
// NativeDoltStore and an actual BdStore -- proving the two agree -- is the gap,
// and it belongs beside the store, not here.
//
//	go test ./cmd/gc/ -run 'PriorityRank|NilPriority|CapAdmits|CapTreats'

package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// workBeadNoPriority builds an assigned work bead whose Priority is nil -- the
// state every P2 bead arrives in from the native store, which returns nil for
// issue.Priority == 2 and for no other input. workBead cannot express it: it
// always takes an int and always sets the pointer.
// Every bead in this suite is in_progress and routed to the single "claude"
// pool template, so neither is a parameter -- the linter rejects one that
// never varies, and a fixed value here is also one fewer thing for a reader to
// check when the question is purely about ordering.
func workBeadNoPriority(id, assignee string) beads.Bead {
	b := workBead(id, "claude", assignee, "in_progress", 0)
	b.Priority = nil
	return b
}

// TestBeadPriorityRankIsMonotonicInUrgency pins the rank encoding itself:
// higher rank is more urgent, the whole declared 0-4 range is distinct, and a
// nil Priority ranks exactly where P2 ranks. The literals are written out
// rather than recomputed from the implementation's constants, because a test
// that derives its expectation from the same constant cannot notice that
// constant changing.
//
// Rank 0 appears nowhere in the table on purpose: it is reserved for a request
// with no driving bead, and beadPriorityRank must never return it.
func TestBeadPriorityRankIsMonotonicInUrgency(t *testing.T) {
	cases := []struct {
		name string
		bead beads.Bead
		want int
	}{
		{"nil (the native store's P2)", workBeadNoPriority("b", "s"), 3},
		{"P4", workBead("b", "claude", "s", "in_progress", 4), 1},
		{"P3", workBead("b", "claude", "s", "in_progress", 3), 2},
		{"P2", workBead("b", "claude", "s", "in_progress", 2), 3},
		{"P1", workBead("b", "claude", "s", "in_progress", 1), 4},
		{"P0", workBead("b", "claude", "s", "in_progress", 0), 5},
	}
	for _, tc := range cases {
		if got := beadPriorityRank(tc.bead); got != tc.want {
			t.Errorf("beadPriorityRank(%s) = %d, want %d", tc.name, got, tc.want)
		}
	}
	// Out-of-range values are data gc does not understand. They clamp to the
	// nearest declared priority rather than being read literally, so a stray
	// large number cannot rank below a request with no bead and a negative one
	// cannot rank above P0.
	if got := beadPriorityRank(workBead("b", "claude", "s", "in_progress", 10)); got != 1 {
		t.Errorf("beadPriorityRank(10) = %d, want 1 (clamped to P4)", got)
	}
	if got := beadPriorityRank(workBead("b", "claude", "s", "in_progress", -1)); got != 5 {
		t.Errorf("beadPriorityRank(-1) = %d, want 5 (clamped to P0)", got)
	}
	// The reserved rank. A request with no driving bead never calls this
	// function -- its field stays the Go zero value -- so nothing the function
	// can return may collide with it.
	for p := -2; p <= 10; p++ {
		if got := beadPriorityRank(workBead("b", "claude", "s", "in_progress", p)); got == 0 {
			t.Errorf("beadPriorityRank(%d) = 0, which is reserved for a request with no bead", p)
		}
	}
	if got := beadPriorityRank(workBeadNoPriority("b", "s")); got == 0 {
		t.Error("beadPriorityRank(nil) = 0, which is reserved for a request with no bead")
	}
}

// TestPoolCapAdmitsP0OverP4 is the defect in its plainest form. Two resumable
// beads, one session to give: the P0 must get it. The P4 bead is listed first
// so a comparator that fell back to slice order would also fail.
func TestPoolCapAdmitsP0OverP4(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "", intPtr(1), 0)},
	}
	work := []beads.Bead{
		workBead("w-p4", "claude", "s-p4", "in_progress", 4),
		workBead("w-p0", "claude", "s-p0", "in_progress", 0),
	}
	sessions := []beads.Bead{sessionBead("s-p4", "open"), sessionBead("s-p0", "open")}

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads(sessions), nil)

	if len(result) != 1 || len(result[0].Requests) != 1 {
		t.Fatalf("expected 1 template with 1 request (cap=1), got %#v", result)
	}
	if got := result[0].Requests[0].WorkBeadID; got != "w-p0" {
		t.Errorf("admitted work bead = %q, want w-p0 -- a cap must shed the LEAST urgent request", got)
	}
}

// TestPoolCapTreatsNilPriorityAsP2 pins the half that actually fired in the
// field, in the direction the store's encoding actually justifies.
//
// A nil Priority is the native store's P2, so under a binding cap it must beat
// a P3 and lose to a P1 -- exactly as an explicit 2 would. The first draft of
// this fix ranked nil below every declared priority; the P3 case below is the
// one that caught it, and it is not hypothetical: every open bead in the hq
// store was P2 (hence nil) while astoria-zephyr held an open P3.
func TestPoolCapTreatsNilPriorityAsP2(t *testing.T) {
	cases := []struct {
		name  string
		rival beads.Bead
		want  string
	}{
		// The rejected "nil ranks last" reading fails this row.
		{"beats P3", workBead("w-rival", "claude", "s-rival", "in_progress", 3), "w-nil"},
		// ... and passes this one, which is why the P3 row has to exist.
		{"loses to P1", workBead("w-rival", "claude", "s-rival", "in_progress", 1), "w-rival"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.City{
				Agents: []config.Agent{poolAgent("claude", "", intPtr(1), 0)},
			}
			work := []beads.Bead{
				workBeadNoPriority("w-nil", "s-nil"),
				tc.rival,
			}
			sessions := []beads.Bead{sessionBead("s-nil", "open"), sessionBead("s-rival", "open")}

			result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads(sessions), nil)

			if len(result) != 1 || len(result[0].Requests) != 1 {
				t.Fatalf("expected 1 template with 1 request (cap=1), got %#v", result)
			}
			if got := result[0].Requests[0].WorkBeadID; got != tc.want {
				t.Errorf("admitted work bead = %q, want %q -- a nil Priority must rank exactly as P2", got, tc.want)
			}
		})
	}
}

// TestPoolCapRanksNilIdenticallyToExplicitP2 pins backend invariance. The same
// P2 bead arrives as nil from NativeDoltStore and as 2 from BdStore, so the two
// must rank identically or which store constructor fired decides which session
// survives a cap.
func TestPoolCapRanksNilIdenticallyToExplicitP2(t *testing.T) {
	if nilRank, explicit := beadPriorityRank(workBeadNoPriority("b", "s")),
		beadPriorityRank(workBead("b", "claude", "s", "in_progress", 2)); nilRank != explicit {
		t.Fatalf("rank(nil) = %d, rank(P2) = %d -- the same bead must rank the same through either store", nilRank, explicit)
	}
}

// TestAcceptedNestedCapUsageRanksLikeApplyNestedCaps pins the second copy of
// the comparator. acceptedNestedCapUsage decides how much cap headroom is
// already spoken for, and it re-sorts the same requests independently of
// applyNestedCaps. The two disagreeing is invisible from either one's own
// tests: usage would reserve the slot for one request while the accept walk
// hands it to another.
func TestAcceptedNestedCapUsageRanksLikeApplyNestedCaps(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "", intPtr(1), 0)},
	}
	// Ranks, not priorities: this function takes requests, and the point is
	// that both comparators agree on which of the two is more urgent.
	requests := []SessionRequest{
		{Template: "claude", Tier: "resume", SessionBeadID: "s-low", BeadPriorityRank: 1},
		{Template: "claude", Tier: "resume", SessionBeadID: "s-high", BeadPriorityRank: 5},
	}

	usage := acceptedNestedCapUsage(newNestedCapLimits(cfg), requests)
	if !usage.seenSessionBead["s-high"] {
		t.Error("acceptedNestedCapUsage reserved the cap for the lower-ranked request")
	}
	if usage.seenSessionBead["s-low"] {
		t.Error("acceptedNestedCapUsage accepted both requests under a cap of 1")
	}

	accepted := applyNestedCaps(cfg, requests, nil, nil)
	if len(accepted) != 1 || len(accepted[0].Requests) != 1 {
		t.Fatalf("expected 1 template with 1 request (cap=1), got %#v", accepted)
	}
	if got := accepted[0].Requests[0].SessionBeadID; got != "s-high" {
		t.Errorf("applyNestedCaps admitted %q, want s-high -- the two comparators must agree", got)
	}
}
