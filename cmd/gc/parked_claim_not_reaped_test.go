package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// Regression suite for ci-xm2c86: a pool session that parks its own claim must
// not be reaped as surplus.
//
// Scope is the join between the two halves that produced the incident -- the
// demand pass that decides a session is wanted, and the reconciler pass that
// drains the ones that are not. Each half is covered on its own elsewhere
// (pool_desired_state_test.go for the resume tier, session_reconciler_test.go
// for drain classification), and neither alone could have caught this: the
// demand pass has no notion of draining and the reconciler was handed a
// desiredState built by hand, so the status that fell out between them was
// invisible to both.
//
// WHAT THIS SUITE CANNOT REPRESENT: the live store. It drives MemStore through
// ComputePoolDesiredStates, so a backend that filters status differently --
// the native Dolt store applies ListQuery.Status by value -- is out of reach
// here and is pinned separately in build_desired_state_blocked_demand_test.go.
//
//	go test ./cmd/gc/ -run ParkedClaim -count=1
func TestParkedClaimKeepsItsPoolSessionOutOfTheOrphanArm(t *testing.T) {
	// INVARIANT: a live pool session whose only claim is parked stays in the
	// desired set, so the reconciler's surplus arm never sees it.
	//
	// This drives the measured sequence end to end -- claim, park, then the
	// demand pass whose result the reconciler consumes -- rather than asserting
	// on the resume tier alone. The incident was NOT that the resume tier
	// returned the wrong shape; it was that its silence became a drain two
	// ticks later, and only a test spanning both can fail for that reason.
	//
	// desiredState is DERIVED from the demand pass rather than injected. An
	// injected one is what let the existing parked-claim fixtures pass over
	// this defect: a session placed in the desired set by hand can never reach
	// the orphan arm, whatever the demand pass would have said about it.
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", intPtr(2), 0)},
	}
	session := beads.Bead{
		ID:     "sess-parked",
		Status: "open",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name":         "tech-sess-parked",
			"alias":                "tech",
			"template":             "rig/claude",
			poolManagedMetadataKey: boolMetadata(true),
		},
	}
	// The claim is parked on an operator gate, which is the shape the bench
	// technician reached: still assigned, still the session's, not runnable.
	claim := workBead("w-parked", "rig/claude", "tech", "blocked", 5)

	states := ComputePoolDesiredStates(
		cfg, []beads.Bead{claim}, sessionInfosFromBeads([]beads.Bead{session}), nil)

	// The resume request is what carries the session into desiredState. Absent
	// it the reconciler's own input is empty, and an empty desiredState is
	// precisely the condition session_reconciler.go:2213 spells "orphaned".
	var resumedSessionBeadID string
	for _, st := range states {
		for _, req := range st.Requests {
			if req.Tier == "resume" {
				resumedSessionBeadID = req.SessionBeadID
			}
		}
	}
	if resumedSessionBeadID != session.ID {
		t.Fatalf("resumed session bead = %q, want %q -- a parked claim left its live session out of the desired set, which is the orphan arm's entry condition",
			resumedSessionBeadID, session.ID)
	}
}
