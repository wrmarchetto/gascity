// Scope: how many NEW-tier session requests one ready bead earns when two pool
// templates read the same shared claim route, and one of them is sized from a
// custom scale_check that reports a bare count.
//
// Why this suite exists separately from pool_desired_state_test.go: that suite
// drives one template at a time, so every assertion in it is satisfied by a
// per-template count. The defect this file pins is only visible ACROSS
// templates -- each template's own arithmetic is correct and the sum is wrong.
// Measured on the running city 2026-09-12 (ci-fp7pzt): bead as-74qq, created
// 00:11:10Z on route astoria-sel4/lab.engineer, produced
// template_tick_summary pool_desired=1 work_requested=true for BOTH
// astoria-sel4/lab.engineer and astoria-sel4/lab.engineer-codex in the same
// tick at 00:11:31.005Z. The Claude slot claimed it at 00:11:41Z; the Codex
// session spawned, found nothing, and drained no_work at 00:11:56Z. 125 such
// phantom sessions are recorded, all on templates carrying a custom
// scale_check.
//
// What this suite does NOT cover: whether the reconciler acts on the requests
// it receives, the per-tick create budget, and the failed-create backoff that
// sits downstream of all of it (pool_create_backoff_scope_test.go). Those can
// each suppress an individual spawn; none of them makes the demand right, and
// a suite that asserted on spawns rather than requests would go green whenever
// the budget happened to be spent.
//
// Run: go test ./cmd/gc/ -run SharedRouteAnonymousDemand -count=1
package main

import (
	"testing"

	sessionpkg "github.com/gastownhall/gascity/internal/session"

	"github.com/gastownhall/gascity/internal/config"
)

// sharedClaimRouteCity builds the two-pool shape the city runs: a Claude pool
// owning the route and a Codex pool claiming onto it, both cold, both rig
// scoped. Claim routes are given ALREADY EXPANDED because computePoolDesiredStates
// is downstream of expandAgentClaimRoutes; the unexpanded form is pinned by
// pool_claim_routes_test.go and is a different seam.
func sharedClaimRouteCity() *config.City {
	return &config.City{
		Rigs: []config.Rig{{Name: "astoria-sel4", Path: "/tmp/astoria-sel4"}},
		Agents: []config.Agent{
			{
				Name:              "lab.engineer",
				Dir:               "astoria-sel4",
				MinActiveSessions: intPtr(0),
				MaxActiveSessions: intPtr(2),
			},
			{
				Name:              "lab.engineer-codex",
				Dir:               "astoria-sel4",
				MinActiveSessions: intPtr(0),
				MaxActiveSessions: intPtr(2),
				// The two fields that together make this pool's demand
				// anonymous: it counts the OTHER template's route, and it
				// counts it with a script gc cannot read bead ids out of.
				ClaimRoutes: []string{"astoria-sel4/lab.engineer"},
				ScaleCheck:  "lab-engineer-queue.sh count astoria-sel4/lab.engineer",
				WorkQuery:   "lab-engineer-queue.sh claim astoria-sel4/lab.engineer",
			},
		},
	}
}

func countNewTierRequests(states []PoolDesiredState) (total int, byTemplate map[string]int) {
	byTemplate = map[string]int{}
	for _, state := range states {
		for _, req := range state.Requests {
			if req.Tier != "new" {
				continue
			}
			total++
			byTemplate[state.Template]++
		}
	}
	return total, byTemplate
}

// TestSharedRouteAnonymousDemandEarnsOneSessionPerReadyBead pins that ONE ready
// bead on a shared claim route earns ONE new-demand session, whichever pool
// takes it -- not one per template that can see it.
//
// The count is deliberately 1 on BOTH templates, because that is what the
// store actually reports: the two scale checks read the same route and so
// return the same number. Asserting on the SUM rather than on either
// template's own figure is the whole point; both per-template figures are
// individually correct.
//
// The bead-identified request is the one that must survive. It is the only one
// gc can prove claimable -- assigned_work_scope.go's readyAssigned gate applies
// to a bead id and to nothing else -- so withholding the anonymous twin is the
// only choice that keeps a provable request.
func TestSharedRouteAnonymousDemandEarnsOneSessionPerReadyBead(t *testing.T) {
	cfg := sharedClaimRouteCity()

	states := ComputePoolDesiredStatesWithDemandTraced(
		cfg,
		nil, // no assigned work: both pools are cold
		nil, // no live sessions
		map[string]int{
			"astoria-sel4/lab.engineer":       1,
			"astoria-sel4/lab.engineer-codex": 1,
		},
		map[string]scaleCheckDemand{
			// Only the default enumerating demand path fills this in. The
			// Codex template is absent from the map, which is exactly what a
			// scale_check ending in `jq length` leaves behind.
			"astoria-sel4/lab.engineer": {Count: 1, WorkBeadIDs: []string{"as-74qq"}},
		},
		nil,
	)

	total, byTemplate := countNewTierRequests(states)
	if total != 1 {
		t.Fatalf("one ready bead on a shared route earned %d new-demand sessions %v, want 1; "+
			"the extra session spawns, finds the bead already claimed, and drains no_work", total, byTemplate)
	}
	if byTemplate["astoria-sel4/lab.engineer"] != 1 {
		t.Errorf("the surviving request is on %v, want it on the bead-identified template "+
			"astoria-sel4/lab.engineer -- an anonymous request cannot be proved claimable", byTemplate)
	}
}

// TestSharedRouteAnonymousDemandSurvivesWhenNoTemplateNamesTheBead pins the
// other half, and it is what stops the fix from being "always drop the
// anonymous request": when NO template on the route carries a bead id, the
// anonymous demand is the only demand there is and must still spawn one
// session. Dropping it would make a Codex-only rig permanently cold.
func TestSharedRouteAnonymousDemandSurvivesWhenNoTemplateNamesTheBead(t *testing.T) {
	cfg := sharedClaimRouteCity()

	states := ComputePoolDesiredStatesWithDemandTraced(
		cfg,
		nil,
		nil,
		map[string]int{"astoria-sel4/lab.engineer-codex": 1},
		nil, // nothing enumerated anywhere
		nil,
	)

	total, byTemplate := countNewTierRequests(states)
	if total != 1 || byTemplate["astoria-sel4/lab.engineer-codex"] != 1 {
		t.Fatalf("anonymous-only demand earned %d new-demand sessions %v, want exactly 1 on the codex pool", total, byTemplate)
	}
}

// TestSharedRouteAnonymousDemandIsAbsorbedByASiblingTemplatesInFlightSession
// pins the second shape the same defect takes: not a same-tick twin, but an
// already-created session on the OTHER template that has not claimed yet.
//
// gc already spends new demand against such a session -- poolInFlightNewRequests
// converts it into a reused request instead of an additional create -- but only
// WITHIN its own template. The Claude pool's in-flight session therefore
// absorbs the Claude count and leaves the Codex count untouched, so the same
// bead still buys two sessions. The first assertion checks the absorption
// actually happened, because a fixture that failed isEphemeralSessionInfoForAgent
// would make the second assertion fail for the wrong reason.
func TestSharedRouteAnonymousDemandIsAbsorbedByASiblingTemplatesInFlightSession(t *testing.T) {
	cfg := sharedClaimRouteCity()

	// start-pending is the window poolSessionConsumesNewDemandInfo recognizes,
	// and it is the state the running city's trace recorded for both templates
	// at 00:11:31.005Z -- the tick that produced the phantom.
	inFlight := sessionpkg.Info{
		ID:            "ci-claude-inflight",
		Template:      "astoria-sel4/lab.engineer",
		PoolManaged:   true,
		PoolSlot:      "1",
		MetadataState: string(sessionpkg.StateStartPending),
		TriggerBeadID: "as-74qq",
	}

	states := ComputePoolDesiredStatesWithDemandTraced(
		cfg,
		nil,
		[]sessionpkg.Info{inFlight},
		map[string]int{
			"astoria-sel4/lab.engineer":       1,
			"astoria-sel4/lab.engineer-codex": 1,
		},
		map[string]scaleCheckDemand{
			"astoria-sel4/lab.engineer": {Count: 1, WorkBeadIDs: []string{"as-74qq"}},
		},
		nil,
	)

	total, byTemplate := countNewTierRequests(states)
	reused := false
	for _, state := range states {
		for _, req := range state.Requests {
			if req.SessionBeadID == "ci-claude-inflight" {
				reused = true
			}
		}
	}
	if !reused {
		t.Fatalf("the in-flight session was not reused, so this fixture does not model an in-flight create; requests %v", byTemplate)
	}
	if total != 1 {
		t.Fatalf("one ready bead with a sibling template's create already in flight earned %d new-demand sessions %v, want 1", total, byTemplate)
	}
}

// TestSharedRouteAnonymousDemandGroupsTemplatesBridgedByAThirdTemplate pins
// that route groups merge transitively, and it is declared in the order that
// makes the merge load-bearing: the bridging template comes LAST.
//
// With A on route r1, C on route r2 and B claiming both, processing A then C
// then B opens two groups before B arrives. A grouping that only joins the
// first route it recognizes leaves C holding a separate budget for r2, so B
// and C count the same bead twice -- the original defect, one level out.
// Processing B second would hide this entirely, which is why the fixture does
// not.
//
// No city configuration has this shape today. The merge is tested rather than
// deleted because the alternative to a tested branch is an untested one: the
// grouping cannot be order-independent without it, and order here is agent
// declaration order, which any pack edit can change.
func TestSharedRouteAnonymousDemandGroupsTemplatesBridgedByAThirdTemplate(t *testing.T) {
	cfg := &config.City{
		Rigs: []config.Rig{{Name: "r", Path: "/tmp/r"}},
		Agents: []config.Agent{
			{Name: "a", Dir: "r", MaxActiveSessions: intPtr(2), ClaimRoutes: []string{"r/shared-one"}},
			{Name: "c", Dir: "r", MaxActiveSessions: intPtr(2), ClaimRoutes: []string{"r/shared-two"}},
			{Name: "b", Dir: "r", MaxActiveSessions: intPtr(2), ClaimRoutes: []string{"r/shared-one", "r/shared-two"}},
		},
	}

	states := ComputePoolDesiredStatesWithDemandTraced(
		cfg, nil, nil,
		map[string]int{"r/a": 1, "r/c": 1, "r/b": 1},
		nil, nil,
	)

	total, byTemplate := countNewTierRequests(states)
	if total != 1 {
		t.Fatalf("one bead visible to three transitively bridged templates earned %d new-demand sessions %v, want 1", total, byTemplate)
	}
}
