package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// closedPoolSessionBead creates a closed pool-managed session bead for
// "rig/claude", the template every caller configures via
// poolAgent("claude", "rig", ...). Used to construct "session bead closed but
// template still configured" scenarios.
//
// The template is fixed rather than a parameter, and re-adding the parameter
// is the mistake to avoid. computePoolDesiredStates skips sb.Closed before it
// populates sessionBeadTemplate, and canonicalSingletonAliasHeldTemplates
// skips closed beads too, so nothing reads this key on a closed bead --
// setting it to "rig/bogus-never-configured" left all four callers green. A
// call site passing a different template would therefore pin no behavior,
// which is what unparam reported. Making the value load-bearing needs an OPEN
// bead, which is the resume tier and not this one. The key is still written
// because a real pool-managed session bead carries one.
//
// Callers: go test ./cmd/gc/ -run 'TestComputePoolDesiredStates_(WakeKnownIdentityForClosedSession|WakeKnownIdentityDedupsMultipleBeadsForSameSession|SlotAssignedWorkWakesAfterSessionClose|SlotDistinctSlotsWakeIndependently)' -count=1
func closedPoolSessionBead(id string) beads.Bead {
	return beads.Bead{
		ID:     id,
		Status: "closed",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"template":             "rig/claude",
			poolManagedMetadataKey: boolMetadata(true),
		},
	}
}

// TestComputePoolDesiredStates_WakeKnownIdentityForClosedSession verifies that
// an in-progress work bead assigned to a configured, non-suspended pool
// template produces a "wake-known-identity" request when no live session owns
// it.
//
// This is the canonical "orphan recovery" case: a pool agent claimed work,
// the city restarted (or the session was killed), and the session bead is now
// closed — but the template is still live. The reconciler must revive the
// template rather than leaving the work stranded.
func TestComputePoolDesiredStates_WakeKnownIdentityForClosedSession(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", nil, 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "rig/claude", "in_progress", 5),
	}
	closed := closedPoolSessionBead("sess-1")

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads([]beads.Bead{closed}), nil)

	wakeCount := 0
	for _, ds := range result {
		for _, req := range ds.Requests {
			if req.Tier == "wake-known-identity" {
				wakeCount++
			}
		}
	}
	if wakeCount != 1 {
		t.Errorf("wake-known-identity count = %d, want 1 — closed session with known template must produce a wake request", wakeCount)
	}
}

// TestComputePoolDesiredStates_WakeKnownIdentityUnknownAssigneeProducesNoRequest
// verifies that a work bead whose assignee does not match any session bead
// (open or closed) produces no request. An unknown assignee cannot be mapped
// to a known identity, so it remains orphaned.
func TestComputePoolDesiredStates_WakeKnownIdentityUnknownAssigneeProducesNoRequest(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", nil, 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "unknown-session-id", "in_progress", 5),
	}
	// No session beads at all — assignee doesn't resolve.
	result := ComputePoolDesiredStates(cfg, work, nil, nil)

	total := 0
	for _, ds := range result {
		total += len(ds.Requests)
	}
	if total != 0 {
		t.Errorf("total requests = %d, want 0 — unknown assignee must produce no request", total)
	}
}

// TestComputePoolDesiredStates_WakeKnownIdentityDedupsMultipleBeadsForSameSession
// verifies that two work beads both assigned to the same configured template
// deduplicate to exactly one wake-known-identity request, not two.
func TestComputePoolDesiredStates_WakeKnownIdentityDedupsMultipleBeadsForSameSession(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", nil, 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "rig/claude", "in_progress", 5),
		workBead("w2", "rig/claude", "rig/claude", "open", 3),
	}
	closed := closedPoolSessionBead("sess-1")

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads([]beads.Bead{closed}), nil)

	wakeCount := 0
	for _, ds := range result {
		for _, req := range ds.Requests {
			if req.Tier == "wake-known-identity" {
				wakeCount++
			}
		}
	}
	if wakeCount != 1 {
		t.Errorf("wake-known-identity count = %d, want 1 — two beads for the same closed session must deduplicate to one wake request", wakeCount)
	}
}

// TestComputePoolDesiredStates_LiveSessionContinuesAsResumeTier verifies that
// open sessions still produce Tier="resume" and are not reclassified as
// wake-known-identity. Closed-session recovery must not touch live sessions.
func TestComputePoolDesiredStates_LiveSessionContinuesAsResumeTier(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", nil, 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "sess-live", "in_progress", 5),
	}
	sessions := []beads.Bead{sessionBead("sess-live", "open")}

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads(sessions), nil)

	if len(result) != 1 || len(result[0].Requests) != 1 {
		t.Fatalf("expected 1 request, got %#v", result)
	}
	req := result[0].Requests[0]
	if req.Tier != "resume" {
		t.Errorf("tier = %q, want resume — live session must stay in resume tier", req.Tier)
	}
	if req.SessionBeadID != "sess-live" {
		t.Errorf("SessionBeadID = %q, want sess-live", req.SessionBeadID)
	}
}

// TestApplyNestedCaps_WakeKnownIdentityOutranksMoreUrgentNew verifies that
// when a cap admits only one request, a wake-known-identity request keeps the
// slot even though the competing new request is MORE urgent.
//
// This test used to give both requests the SAME rank and pin only the
// tie-break. That state no producer generates: since ci-7vyl6k a nil priority
// ranks as P2 and every resume-like rank is 1-5, while a new request's rank is
// the 0 reserved for "no driving bead", so resume-vs-new never ties. Re-derived
// for ci-qbhi4g to assert the rule that actually has to hold -- tier outranks
// urgency -- which is the one a later edit populating the new tier's rank would
// otherwise break silently.
//
// Wake-known-identity is the tier where shedding is destructive rather than
// merely slow: for a slot-named assignee, releaseOrphanedPoolAssignments clears
// the assignee and reverts in_progress to open later in the SAME tick, because
// no re-homed session bead exists to make ownership.ownsWork true.
func TestApplyNestedCaps_WakeKnownIdentityOutranksMoreUrgentNew(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "", intPtr(1), 0)},
	}
	// The new request is listed first AND carries the most urgent rank, so
	// both slice order and urgency point the wrong way.
	requests := []SessionRequest{
		{Template: "claude", Tier: "new", BeadPriorityRank: rankP0},
		{Template: "claude", Tier: "wake-known-identity", SessionBeadID: "sess-closed", BeadPriorityRank: rankP4},
	}

	result := applyNestedCaps(cfg, requests, nil, nil)

	if len(result) != 1 {
		t.Fatalf("len(result) = %d, want 1", len(result))
	}
	if len(result[0].Requests) != 1 {
		t.Fatalf("accepted = %d, want 1 (cap=1)", len(result[0].Requests))
	}
	if result[0].Requests[0].Tier != "wake-known-identity" {
		t.Errorf("accepted tier = %q, want wake-known-identity -- must outrank new work however urgent", result[0].Requests[0].Tier)
	}
}
