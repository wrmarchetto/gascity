// Scope: the one recorded-owner shape
// build_desired_state_recorded_owner_test.go cannot express -- a bead
// carrying BOTH a bare pool assignee and a gc.routed_to naming that same
// pool -- and the hold-label contract on it.
//
// Why this is a separate suite rather than a third row in recordedOwnerShapes:
// that suite's control asserts demand == 1 alongside desired sessions == 1, and
// this shape's demand is 0 by design. controllerDemandPoolAliasTarget declines
// assigned+routed work as a concrete handoff (#2527, pool_alias_demand.go), so
// the desired session here comes from the wake-known-identity tier alone. A row
// sharing that suite's assertions would have to loosen the demand check for
// every shape, which is the half of it that catches a wholesale suppression.
//
// This shape is not hypothetical and is not rare:
// assets/scripts/escalate-bead.py files every merge-closed-features summons
// with exactly it, and `gc hook --claim` stamps gc.routed_to onto pool-alias
// work it claims. Both halves of the pair therefore appear on ordinary city
// beads.
//
// Run it with:
//
//	go test ./cmd/gc/ -run PoolAssigneeAndRouteHold
package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// TestPoolAssigneeAndRouteHoldRaisesNoDesiredSession pins that a dispatch hold
// label suppresses the desired session for work addressed to a pool by
// assignee AND route together, which is the guarantee
// engdocs/contributors/hold-label-conventions.md states without qualification:
// "A held bead raises no pool demand and produces no desired session, whether
// it records its owner by assignee or by route."
//
// Measured in production 2026-09-12 (ci-q24fm8) before the gate existed: the
// city pool `toolsmith` repeatedly spawned sessions for ci-fhp0sl (open,
// assignee toolsmith, gc.routed_to toolsmith, hold:external) and for ci-7le3t1
// while it was held. Each one booted, got no_work from `gc hook --claim` --
// that side's candidate query DOES exclude the labels -- and drained. The cost
// is the spawn itself: a seat, a worktree and a provider session for work no
// query will ever offer, plus a claim_no_work create-backoff row per cycle.
//
// What this test does NOT claim, because the measurement refuted it: that the
// churn stranded claimable work. Over the window it was observed, `bd ready
// --assignee toolsmith` with the same hold exclusions returned zero rows, so
// the cold pool was correct. Suppressing the phantom spawn is the whole of it.
//
// The demand assertion is deliberately 0 on BOTH rows. It is not a weaker
// version of the control -- it records that the pool-alias demand reader
// declines this shape outright, so the wake tier is the only producer of the
// desired session and the only place the hold can be enforced. If that ever
// changes, this line fails and the reader of the failure learns which tier
// moved.
func TestPoolAssigneeAndRouteHoldRaisesNoDesiredSession(t *testing.T) {
	poolRouted := func(labels ...string) beads.Bead {
		return beads.Bead{
			ID:       "b",
			Status:   "open",
			Type:     "task",
			Assignee: "toolsmith",
			Labels:   labels,
			Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "toolsmith"},
		}
	}

	for _, hold := range beadmeta.DispatchHoldLabels {
		t.Run(hold, func(t *testing.T) {
			result := poolAliasDemandResult(t, poolAliasDemandCity(), poolRouted(hold))
			if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
				t.Errorf("held demand = %d, want 0 -- assigned+routed work is a concrete handoff and raises no pool-door demand", got)
			}
			if got := len(result.State); got != 0 {
				t.Errorf("held desired sessions = %d, want 0 -- no claim query offers a held bead at the pool door, so the session spawned here boots, gets no_work, and drains", got)
			}

			// The control. Same fixture, label removed. Without it a change
			// that refuses this shape wholesale passes above and strands every
			// escalation summons the city files.
			control := poolAliasDemandResult(t, poolAliasDemandCity(), poolRouted())
			if got := control.ScaleCheckCounts["toolsmith"]; got != 0 {
				t.Errorf("unheld demand = %d, want 0 -- the demand reader declines this shape whether or not it is held", got)
			}
			if got := len(control.State); got != 1 {
				t.Errorf("unheld desired sessions = %d, want 1 -- the wake tier is what serves pool work addressed by assignee and route together", got)
			}
		})
	}
}

// TestPoolSlotIdentityHoldStillWakesForCrashRecovery pins the BOUNDARY of the
// gate above, which is the half a later editor is most likely to erase. A hold
// names the actor who must move next, so that actor reaching its own claimed
// work is the mechanism working -- the assignee-scoped tiers are hold-
// transparent by design (ga-5736js). A dead slot's name normalizes to the same
// template as the pool door, so a gate written against the NORMALIZED assignee
// passes the suite above and silently strands every crashed slot's in-progress
// work behind a hold that was never about the pool.
//
// This row drives ComputePoolDesiredStates directly rather than the full
// buildDesiredState fixture the suite above uses, and the reason is a trap
// worth naming: beads.MemStore.Create overwrites Status with "open"
// (internal/beads/memstore.go), and an OPEN bead assigned to a slot identity is
// dropped upstream by filterAssignedWorkBeadsForPoolDemand's readyAssigned gate
// -- no Ready(assignee=) probe ever runs for a slot name. Seeded through the
// store this test would go green without the tier ever seeing the bead.
func TestPoolSlotIdentityHoldStillWakesForCrashRecovery(t *testing.T) {
	for _, hold := range beadmeta.DispatchHoldLabels {
		t.Run(hold, func(t *testing.T) {
			crashed := workBead("b", "toolsmith", "toolsmith-1", "in_progress", 2)
			crashed.Labels = []string{hold}

			// No session infos: the slot that claimed this bead is gone, which
			// is the whole shape of crash recovery.
			states := ComputePoolDesiredStates(poolAliasDemandCity(), []beads.Bead{crashed}, nil, nil)

			wakes := 0
			for _, ds := range states {
				for _, req := range ds.Requests {
					if req.Tier == "wake-known-identity" {
						wakes++
					}
				}
			}
			if wakes != 1 {
				t.Errorf("wake-known-identity count = %d, want 1 -- a hold must not strand a dead slot's own in-progress work", wakes)
			}
		})
	}
}
