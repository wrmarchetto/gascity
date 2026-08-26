// Scope: the wake-known-identity tier's assignee gate in
// computePoolDesiredStates, for pool agents scaled past one slot.
//
// Why this file exists separately from pool_desired_state_wake_test.go: that
// suite only ever assigns work to the agent's BARE template name, which is the
// shape a singleton pool produces (max_active_sessions=1 keeps the canonical
// identity, so the one session's alias IS the template name). Once the cap
// goes above 1, gc stops using the canonical singleton identity and every
// session gets a slot identity instead -- "toolsmith-1", "toolsmith-2" -- and
// `gc hook --claim` stamps THAT on the work bead. No test covered the assignee
// shape the multi-slot configuration actually writes.
//
// Delegated elsewhere: cap arithmetic (pool_desired_state_test.go), the
// scale_check channel for unassigned routed work (build_desired_state_test.go),
// and the bare-template assignee shape (pool_desired_state_wake_test.go).
//
// Run: go test ./cmd/gc/ -run TestComputePoolDesiredStates_Slot -count=1
package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// TestComputePoolDesiredStates_SharedRouteClaimResumesConcreteOwner pins the
// distinction between a queue route and an ownership identity. A pool may
// claim work from a configured shared route, but after that claim the concrete
// slot is the only session that must be retained for the in-progress work.
//
// The queue route intentionally names a different template. Treating it as
// the work owner's template drops the owning slot from the resume tier: the
// shared pool does not own the Codex session, while the Codex pool appears to
// own no work and gets orphan-drained. That produces an empty replacement slot
// on the next demand sample.
func TestComputePoolDesiredStates_SharedRouteClaimResumesConcreteOwner(t *testing.T) {
	const (
		codexTemplate = "astoria-sel4/lab.engineer-codex"
		sharedRoute   = "astoria-sel4/lab.engineer"
		slotIdentity  = codexTemplate + "-1"
	)

	cfg := &config.City{
		Agents: []config.Agent{{
			Name:              "lab.engineer-codex",
			Dir:               "astoria-sel4",
			MaxActiveSessions: intPtr(2),
			ClaimRoutes:       []string{sharedRoute},
		}},
	}
	work := []beads.Bead{
		workBead("shared-work", sharedRoute, slotIdentity, "in_progress", 5),
	}
	sessions := []beads.Bead{{
		ID:     "codex-slot-1",
		Status: "open",
		Type:   sessionBeadType,
		Metadata: map[string]string{
			"template":     codexTemplate,
			"session_name": "lab-engineer-codex-1",
			"alias":        slotIdentity,
			"state":        "active",
			"pool_slot":    "1",
		},
	}}

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads(sessions), map[string]int{codexTemplate: 0})
	if len(result) != 1 || len(result[0].Requests) != 1 {
		t.Fatalf("requests = %#v, want one resume request for the concrete owning slot", result)
	}
	request := result[0].Requests[0]
	if request.Template != codexTemplate {
		t.Errorf("template = %q, want %q", request.Template, codexTemplate)
	}
	if request.Tier != "resume" {
		t.Errorf("tier = %q, want resume", request.Tier)
	}
	if request.SessionBeadID != "codex-slot-1" {
		t.Errorf("SessionBeadID = %q, want codex-slot-1", request.SessionBeadID)
	}
	if request.WorkBeadID != "shared-work" {
		t.Errorf("WorkBeadID = %q, want shared-work", request.WorkBeadID)
	}
}

// TestComputePoolDesiredStates_SlotAssignedWorkWakesAfterSessionClose pins the
// orphan-recovery invariant across the identity form a multi-slot pool writes:
// in-progress work assigned to a slot identity of a configured pool template
// must still produce a wake request once its session bead closes.
//
// Constructed with the slot identity ("rig/claude-2") rather than the bare
// template name because the bare name is already covered by
// TestComputePoolDesiredStates_WakeKnownIdentityForClosedSession, and that test
// passing tells you nothing about this one: the assignee gate compares the raw
// assignee string, while the sibling routedTo value on the same bead is
// normalized through NormalizePoolRouteTarget first. Only an assignee carrying
// a live instance suffix separates the two paths.
//
// The work bead is deliberately in_progress: that is the state a session dies
// in, and it is the state no other channel can recover. The routed pool-demand
// probe counts only ready UNASSIGNED beads, so an in-progress bead still
// stamped with its dead owner's name is invisible to scale_check -- the wake
// tier is the ONLY path back. If this drops the bead, the work is stranded for
// good, with neither a session nor any demand to create one.
func TestComputePoolDesiredStates_SlotAssignedWorkWakesAfterSessionClose(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", intPtr(2), 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "rig/claude-2", "in_progress", 5),
	}
	closed := closedPoolSessionBead("sess-2")

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads([]beads.Bead{closed}), nil)

	total := 0
	for _, ds := range result {
		total += len(ds.Requests)
	}
	if total != 1 {
		t.Fatalf("requests = %d, want 1 -- in-progress work assigned to slot identity %q must wake its template; got %#v",
			total, "rig/claude-2", result)
	}
	if got := result[0].Requests[0].Tier; got != "wake-known-identity" {
		t.Errorf("tier = %q, want wake-known-identity", got)
	}
	if got := result[0].Requests[0].WorkBeadID; got != "w1" {
		t.Errorf("WorkBeadID = %q, want w1 -- the wake request must name the bead that justified it", got)
	}
}

// TestComputePoolDesiredStates_SlotDistinctSlotsWakeIndependently pins that two
// dead slots recover two sessions, not one.
//
// The wake tier keys its dedup on the TEMPLATE (wakeRequestedTemplates), so
// every bead after the first collapses regardless of which slot owned it. That
// is right for two beads owned by one identity -- one session, one request,
// which
// TestComputePoolDesiredStates_WakeKnownIdentityDedupsMultipleBeadsForSameSession
// pins -- and wrong here, where the two beads name two different dead sessions
// and only one of them would ever come back.
//
// max_active_sessions is 2 so the cap cannot be what rejects the second
// request: any count below 2 is the dedup, not a cap. Asserting on the set of
// WorkBeadIDs rather than on the count alone keeps the test honest if one bead
// ever produces two requests.
func TestComputePoolDesiredStates_SlotDistinctSlotsWakeIndependently(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", intPtr(2), 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "rig/claude-1", "in_progress", 5),
		workBead("w2", "rig/claude", "rig/claude-2", "in_progress", 5),
	}
	sessions := []beads.Bead{
		closedPoolSessionBead("sess-1"),
		closedPoolSessionBead("sess-2"),
	}

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads(sessions), nil)

	woken := map[string]bool{}
	for _, ds := range result {
		for _, req := range ds.Requests {
			if req.Tier == "wake-known-identity" {
				woken[req.WorkBeadID] = true
			}
		}
	}
	if !woken["w1"] || !woken["w2"] {
		t.Errorf("woken work beads = %v, want both w1 and w2 -- two dead slots must recover two sessions", woken)
	}
}
