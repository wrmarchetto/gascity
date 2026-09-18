// Package main test: whether a HELD bead ROUTED at a pool, with no assignee,
// refuses that pool session's drain acknowledgement.
//
// Scope: reconcileSessionBeads over a MemStore, asserting the state_reason the
// drain-ack arm writes. Two cases, differing only in how the work bead
// addresses the pool -- by route, and by assignee -- because the whole question
// is whether the ADDRESS is what pins the session.
//
// WHY THIS SUITE EXISTS. ci-d1huhf reported a bench-engineer session wedged for
// two hours and named the mechanism as "the route counts as assigned work",
// with a held, unassigned, routed bead as the cause. That mechanism is not in
// the code: the close gate resolves a session's ASSIGNEE identities
// (sessionAssignmentIdentifiersForConfigInfo -> session.AssigneeIdentities) and
// queries beads.ListQuery{Assignee:...}, which every store answers with a
// literal assignee compare (internal/beads/query.go). Nothing expands that
// query to gc.routed_to. This file is the experiment that says so out loud, so
// the next reader of that bead does not re-derive it from the report.
//
// WHAT IT DELIBERATELY DOES NOT PIN. It does not assert that hold labels are
// irrelevant to the drain gate in general. The gate is assignee-scoped and
// therefore hold-transparent BY DESIGN (internal/beadmeta/hold_labels.go: the
// hold list "must exclude a bead from route-scoped, unassigned automatic
// dispatch" while "assignee-scoped queries ... are hold-transparent by design
// and must never filter on this list"). The second case here is what keeps that
// honest: a HELD bead genuinely assigned to the session's alias still refuses
// the ack, and a future change that filtered the gate on DispatchHoldLabels
// would turn it red.
//
// WHAT A HOST SUITE CANNOT REPRESENT HERE, named so the manual check is not
// mistaken for redundant. MemStore stores the status a fixture writes;
// NativeDoltStore and BdStore map bd's status vocabulary through mapBdStatus
// (internal/beads/bdstore.go), which collapses `blocked`, `deferred`, `review`
// and `testing` to "open". A bead the tracker calls blocked is therefore open
// to this gate in production and not in any fixture below. That divergence is
// a store-conformance question and belongs in internal/beads, not here.
//
// Run it with:
//
//	go test ./cmd/gc/ -run TestReconcileSessionBeads_AgentDrainAckWithHeld -count=1
package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// heldRouteDrainAckOutcome drives one drain-ack tick for a pool-managed
// bench-shaped session over a single work bead and returns the state_reason the
// reconciler wrote.
//
// The session shape is the measured one: a canonical singleton pool, whose slot
// alias is the unsuffixed template name and therefore IS the pool's queue
// address. That coincidence is what makes the address question decidable at all
// -- with a `-N` suffix the route and the alias are different strings and no
// fixture could tell which one the gate read.
func heldRouteDrainAckOutcome(t *testing.T, work beads.Bead) string {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		"alias":          "worker",
		"pool_managed":   "true",
		"session_origin": "ephemeral",
	})
	if _, err := env.store.Create(work); err != nil {
		t.Fatalf("Create(work): %v", err)
	}

	dops := newFakeDrainOps()
	if err := dops.setDrainAck("worker"); err != nil {
		t.Fatalf("setDrainAck: %v", err)
	}
	if woken := reconcileSessionBeads(
		context.Background(),
		[]beads.Bead{session},
		env.desiredState,
		map[string]bool{"worker": true},
		env.cfg,
		env.sp,
		env.store,
		dops,
		nil,
		nil,
		env.dt,
		nil,
		false,
		nil,
		"",
		nil,
		env.clk,
		env.rec,
		0,
		0,
		&env.stdout,
		&env.stderr,
	); woken != 0 {
		t.Fatalf("woken = %d, want 0", woken)
	}
	got, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("Get session after drain-ack: %v", err)
	}
	return got.Metadata["state_reason"]
}

// TestReconcileSessionBeads_AgentDrainAckWithHeldRoutedWorkReleasesSlot is the
// experiment ci-d1huhf asked for, written in the shape that bead reported and
// run against the code it accused: an open bead with NO assignee, addressing
// the pool only through gc.routed_to, carrying hold:external.
//
// The bead predicted this would be RED. It is green, and the green is the
// finding: the gate never saw that bead, because the query it runs is keyed on
// assignee and the fixture has none. Any fix built on the reported mechanism
// would have been aimed at a code path that does not exist.
func TestReconcileSessionBeads_AgentDrainAckWithHeldRoutedWorkReleasesSlot(t *testing.T) {
	reason := heldRouteDrainAckOutcome(t, beads.Bead{
		Title:  "parked escalation routed at the pool",
		Type:   "task",
		Status: "open",
		Labels: []string{beadmeta.HoldExternalLabel},
		Metadata: map[string]string{
			beadmeta.RoutedToMetadataKey: "worker",
		},
	})
	if reason == sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q: a held bead that addresses the pool only by gc.routed_to refused the ack, which would mean the close gate reads routes as assignments", reason)
	}
	if reason != sessionpkg.DrainAckStopPendingReason {
		t.Fatalf("state_reason = %q, want %q: an honored ack on a live runtime queues the async stop", reason, sessionpkg.DrainAckStopPendingReason)
	}
}

// TestReconcileSessionBeads_AgentDrainAckWithHeldAliasPinnedWorkStaysActive is
// the control, and without it the case above is satisfiable by a gate that
// stopped refusing anything at all.
//
// It also pins the hold-transparency contract from the other side. The bead is
// pinned to this session (gc.session_affinity), so isUnpinnedQueuedWorkBead
// does not excuse it, and it carries hold:external -- so a change that taught
// this assignee-scoped gate to filter on beadmeta.DispatchHoldLabels turns this
// red. That is the intended alarm, not collateral: work assigned to a session
// is the session's to release, and a hold on it is a reason to release it
// deliberately rather than to walk away from it.
func TestReconcileSessionBeads_AgentDrainAckWithHeldAliasPinnedWorkStaysActive(t *testing.T) {
	reason := heldRouteDrainAckOutcome(t, beads.Bead{
		Title:    "held work assigned to the slot alias and pinned to this session",
		Type:     "task",
		Status:   "open",
		Assignee: "worker",
		Labels:   []string{beadmeta.HoldExternalLabel},
		Metadata: map[string]string{
			beadmeta.SessionAffinityMetadataKey: "require",
		},
	})
	if reason != sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q, want %q: held work pinned to this session must still refuse the acknowledgement", reason, sessionpkg.DrainAckAssignedWorkReason)
	}
}
