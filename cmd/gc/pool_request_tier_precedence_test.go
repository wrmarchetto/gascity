// cmd/gc/pool_request_tier_precedence_test.go
//
// Pins which session request keeps the slot when max_active_sessions binds and
// a resume-like request competes with a "new" one: the resume-like tier wins,
// however urgent the new request's driving bead is.
//
// The suite exists because that rule was load-bearing but unexpressed. Until
// ci-qbhi4g the cap comparator sorted BeadPriorityRank FIRST and compared tier
// only as a tie-break, and resume-like tiers won anyway for one incidental
// reason: the three "new"-tier construction sites leave BeadPriorityRank at the
// Go zero value, which beadPriorityRank reserves for "no driving bead" and
// which sorts below every declared priority. Populate that field -- the
// obvious next change, and the one ci-qbhi4g was filed to make -- and the
// precedence silently inverts. Nothing would have gone red.
//
// The rule is not a preference. Shedding a wake-known-identity request is
// DESTRUCTIVE on the same tick: releaseOrphanedPoolAssignments
// (cmd/gc/pool_session_name.go) runs later in the same beadReconcileTick, and
// for a slot-named assignee it clears the assignee and reverts in_progress to
// open, because no re-homed session bead exists to make ownership.ownsWork
// true. Shedding a "new" request costs one tick of latency and nothing else.
//
// So every test here drives the new request at a STRICTLY MORE URGENT rank
// than its resume-like rival. Equal ranks would pass under either ordering and
// are exactly the shape that agreed with the defect.
//
// What this suite CANNOT represent: no producer emits a "new" request with a
// nonzero rank today, so the hand-built requests below pin a state production
// cannot reach. That is the point -- they are the guard that makes populating
// the field safe -- but they are not evidence about current production
// behavior. TestComputePoolDesiredStates_ResumeReservesCapBeforeNewDemand is,
// and it pins a DIFFERENT mechanism: the demand loop, not the comparator.
//
//	go test ./cmd/gc/ -run 'OutranksMoreUrgentNew|TierPrecedence|ReservesCapBeforeNewDemand'
package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// rankP0 and rankP4 name the two ends of the declared 0-4 priority range in
// the unit the comparator actually reads. They are written as the literals
// beadPriorityRank produces rather than computed by calling it: a test that
// derives its expectation from the implementation cannot notice the
// implementation changing.
const (
	rankP0 = 5 // most urgent declared priority
	rankP4 = 1 // least urgent declared priority
)

// TestApplyNestedCaps_ResumeOutranksMoreUrgentNew is the invariant in its
// plainest form: one slot, a P4 resume against a P0 new request, and the
// resume must keep it. The new request is listed first so a comparator that
// fell through to slice order would also fail.
func TestApplyNestedCaps_ResumeOutranksMoreUrgentNew(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "", intPtr(1), 0)},
	}
	requests := []SessionRequest{
		{Template: "claude", Tier: "new", BeadPriorityRank: rankP0, WorkBeadID: "w-new"},
		{Template: "claude", Tier: "resume", SessionBeadID: "sess-live", BeadPriorityRank: rankP4, WorkBeadID: "w-resume"},
	}

	result := applyNestedCaps(cfg, requests, nil, nil)

	if len(result) != 1 || len(result[0].Requests) != 1 {
		t.Fatalf("expected 1 template with 1 request (cap=1), got %#v", result)
	}
	if got := result[0].Requests[0].Tier; got != "resume" {
		t.Errorf("admitted tier = %q, want resume -- a resume must not be shed for more urgent new work", got)
	}
}

// TestAcceptedNestedCapUsageTierPrecedenceMatchesApplyNestedCaps pins that the
// two admission paths cannot disagree about precedence.
//
// acceptedNestedCapUsage exists to pre-spend the caps on resume requests
// before new demand is sized, and production calls it with resume-only input
// (computePoolDesiredStates), so its tier comparison never fires there. That
// makes it exactly the copy a later edit would fix in one place and not the
// other. The assertion is on the ACCEPTED SET, not on a shared helper name, so
// it still holds if the two are re-unified differently.
func TestAcceptedNestedCapUsageTierPrecedenceMatchesApplyNestedCaps(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "", intPtr(1), 0)},
	}
	requests := []SessionRequest{
		{Template: "claude", Tier: "new", BeadPriorityRank: rankP0, WorkBeadID: "w-new"},
		{Template: "claude", Tier: "resume", SessionBeadID: "sess-live", BeadPriorityRank: rankP4, WorkBeadID: "w-resume"},
	}

	usage := acceptedNestedCapUsage(newNestedCapLimits(cfg), requests)

	if len(usage.requests) != 1 {
		t.Fatalf("accepted = %d, want 1 (cap=1)", len(usage.requests))
	}
	if got := usage.requests[0].Tier; got != "resume" {
		t.Errorf("accepted tier = %q, want resume -- must match applyNestedCaps", got)
	}
}

// TestComputePoolDesiredStates_ResumeReservesCapBeforeNewDemand pins the
// SECOND, independent mechanism that enforces the same rule, and it is the one
// that actually fires in production.
//
// computePoolDesiredStates seeds nestedCapUsage from the accepted resume
// requests and then sizes new demand against the remaining headroom
// (capNewDemandCount). With the cap already fully spent by a resume, no new
// request is constructed at all -- so the comparator never sees the
// contention, and no amount of urgency on the ready bead can change that.
//
// This is why the comparator tests above must hand-build their input, and why
// this test cannot substitute for them: the two mechanisms express one rule
// and either could be changed alone.
func TestComputePoolDesiredStates_ResumeReservesCapBeforeNewDemand(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", intPtr(1), 0)},
	}
	// The resume is driven by the LEAST urgent declared priority and the
	// ready demand by the most urgent, so the reservation is doing the work
	// rather than the priorities agreeing with it.
	work := []beads.Bead{workBead("w-resume", "rig/claude", "sess-live", "in_progress", 4)}
	sessions := []beads.Bead{sessionBead("sess-live", "open")}
	demand := map[string]scaleCheckDemand{
		"rig/claude": {
			Count:       1,
			WorkBeadIDs: []string{"w-ready-p0"},
			StoreRefs:   map[string]string{"w-ready-p0": "city"},
		},
	}

	result := ComputePoolDesiredStatesWithDemandTraced(
		cfg, work, sessionInfosFromBeads(sessions), map[string]int{"rig/claude": 1}, demand, nil)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	reqs := result[0].Requests
	if len(reqs) != 1 {
		t.Fatalf("len(requests) = %d, want 1 (cap=1), got %#v", len(reqs), reqs)
	}
	if reqs[0].Tier != "resume" || reqs[0].WorkBeadID != "w-resume" {
		t.Errorf("admitted %q/%q, want resume/w-resume -- the resume must hold the only slot", reqs[0].Tier, reqs[0].WorkBeadID)
	}
}
