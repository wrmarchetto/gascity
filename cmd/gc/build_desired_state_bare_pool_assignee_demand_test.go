// cmd/gc/build_desired_state_bare_pool_assignee_demand_test.go
//
// Scope: the composite wake path for PLAIN WORK whose assignee is a pool's OWN
// NAME ("worker") and whose route names that same pool. This is the sibling of
// the dead-slot form in build_desired_state_dead_slot_route_demand_test.go, and
// it differs in the one way that matters: the dead-slot form has a door, and
// this one has none. The orphan sweep clears "worker-2" and the next tick
// counts the bead; it deliberately SKIPS a bare pool name (db2fee2a0, bead
// ci-vcornx), so no later tick ever reaches this shape.
//
// Why this suite exists: ci-vjesdq measured the release as the recovery path
// for plain work and concluded the shape recovers on the next tick. That
// conclusion is correct for a slot-suffixed assignee and FALSE for a bare pool
// name, which it never tested. Production ci-ulltnb: a P1 sat unserved for an
// hour with no log line of any kind, because every guard that declines this
// bead declines it with a bare continue.
//
// The two guards are individually correct and are NOT what this suite argues
// against: controllerDemandPoolAliasTarget refuses a non-wisp assigned+routed
// bead (#2527 reads it as a concrete handoff), and
// filterAssignedWorkBeadsForPoolDemand requires a readiness verdict that
// appendOpenRoutedWorkUnique never stamps.
// What this suite pins is that the composite must still reach ONE session,
// because the worker's own claim tier (hookCandidatePoolAlias) will claim this
// bead the moment a session exists -- demand a pool can claim is demand the
// controller must count, and dispatch.md invariant 11 forbids the two
// disagreeing.
//
// Run: go test ./cmd/gc/ -run BarePoolAssignee
package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestBarePoolAssigneeRoutedToItsOwnPoolWakesThatPool pins that plain open work
// addressed to a pool's own name wakes that pool, and pins the bound at exactly
// one session so a fix cannot buy the wake with a spawn storm.
//
// The named session inside deadSlotCity is load-bearing, not scenery:
// readyAssignedWorkAssignees seeds the Ready probe from live session beads
// plus on_demand named sessions, and collectAssignedWorkBeadsWithStores falls
// back to an UNFILTERED Ready() only when that list is empty. Without it the
// fallback stamps the bead ready and this test reports a wake no running city
// produces -- verified against this fixture, which reads 1 with the named
// session removed and 0 with it present.
func TestBarePoolAssigneeRoutedToItsOwnPoolWakesThatPool(t *testing.T) {
	const pool = "worker"
	cityPath := t.TempDir()
	store := beads.NewMemStore()

	// Plain type is the discriminator the sibling file measured: the molecule
	// and wisp forms are stamped ready by isOpenAssignedMoleculeWork and reach
	// the wake tier in-tick, so only this form depends on the missing verdict.
	work, err := store.Create(beads.Bead{
		Title:    "addressed to the pool's own name, routed to that same pool",
		Type:     "task",
		Status:   "open",
		Assignee: pool,
		Metadata: map[string]string{"gc.routed_to": pool},
	})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}

	cfg := deadSlotCity(pool)
	build := func() DesiredStateResult {
		return buildDesiredStateWithSessionBeads(
			"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
			store, nil, &sessionBeadSnapshot{}, nil, io.Discard,
		)
	}

	first := build()

	// The orphan sweep is asserted to do NOTHING here. That is the whole
	// difference from the dead-slot sibling, and asserting it keeps this test
	// from being satisfied by re-opening the ci-vcornx hole: a fix that makes
	// the sweep clear a live pool address would pass the demand assertion below
	// while reintroducing the cross-store failure db2fee2a0 closed.
	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, cityPath, nil,
		first.AssignedWorkBeads, first.AssignedWorkStores, first.AssignedWorkStoreRefs, nil,
	)
	if len(released) != 0 {
		t.Fatalf("orphan release returned %v, want none; a pool's own name is a live address and must stay on the bead (db2fee2a0, ci-vcornx)", released)
	}
	stillAddressed, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work: %v", err)
	}
	if stillAddressed.Assignee != pool {
		t.Fatalf("assignee = %q, want %q retained", stillAddressed.Assignee, pool)
	}

	// The wake itself. Asserted after the sweep so the failure message cannot be
	// misread as "the release had not run yet" -- there is no later tick that
	// changes this bead.
	woke := deadSlotDesiredCount(first, pool)
	if woke < 1 {
		t.Fatalf("desired %q sessions = %d, want at least 1; the worker's pool-alias claim tier would claim %s the moment a session existed, so the controller must raise the demand that starts one. PoolDesiredCounts=%v ScaleCheckCounts=%v",
			pool, woke, work.ID, first.PoolDesiredCounts, first.ScaleCheckCounts)
	}
	if woke > 1 {
		t.Fatalf("one bead wants %d %q slots; a single unit of work must not overshoot into a spawn storm (PR #1516)", woke, pool)
	}

	// Exactly one row for one bead. The readiness probe re-finds work the
	// earlier passes already captured, and merging both copies is invisible
	// from a session count -- the wake tier collapses them by (template,
	// assignee) -- so this is asserted where the duplication happens rather
	// than left to a downstream dedup in another file to absorb.
	rows := 0
	for _, b := range first.AssignedWorkBeads {
		if b.ID == work.ID {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("AssignedWorkBeads carries %s %d times, want 1; a duplicated row doubles the per-tick operator log and the awake work-bead trace counts", work.ID, rows)
	}

	// Demand must be a level the reconciler spends, not an increment per tick.
	// The store is untouched between the two builds, so the tick number is the
	// only variable moved.
	again := build()
	if got := deadSlotDesiredCount(again, pool); got != woke {
		t.Fatalf("desired %q sessions grew from %d to %d across two ticks over ONE still-open bead; PoolDesiredCounts=%v", pool, woke, got, again.PoolDesiredCounts)
	}
}

// TestBarePoolAssigneeSurvivesTheSelfRetirementThatCreatedIt pins the
// production transition rather than the resting state: the pool is warm, its
// session is the sole carrier of the alias the Ready probe was seeded from,
// and that session retires ITSELF while the work is still open. The pool must
// come back.
//
// This is a separate case from the cold-start test above and not a duplicate of
// it. A fix that seeded the probe from live sessions only would pass the cold
// test through some other door and still fail here, because the alias carrier
// disappears at exactly the moment the verdict is needed. Retirement CAUSE is
// deliberately not modeled -- self-drain and reaped-drain both end as a closed
// session bead, and the demand path reads the bead, not the reason -- so the
// fixture closes the bead instead of driving gc runtime drain-ack.
func TestBarePoolAssigneeSurvivesTheSelfRetirementThatCreatedIt(t *testing.T) {
	const pool = "worker"
	cityPath := t.TempDir()
	store := beads.NewMemStore()

	work, err := store.Create(beads.Bead{
		Title:    "still open when the only session retired itself",
		Type:     "task",
		Status:   "open",
		Assignee: pool,
		Metadata: map[string]string{"gc.routed_to": pool},
	})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}

	cfg := deadSlotCity(pool)
	live := beads.Bead{
		ID:     "session-worker",
		Type:   sessionBeadType,
		Status: "open",
		Metadata: map[string]string{
			"template":     pool,
			"session_name": "worker-session",
			"alias":        pool,
			"pool_managed": "true",
		},
	}
	build := func(sessions ...beads.Bead) DesiredStateResult {
		return buildDesiredStateWithSessionBeads(
			"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
			store, nil, newSessionBeadSnapshot(sessions), nil, io.Discard,
		)
	}

	// While the session lives it carries the alias, so the bead is reachable and
	// the pool is already at its max of one. Asserting the verdict rather than a
	// session count keeps this step from passing merely because the pool is full.
	warm := build(live)
	if !warm.ReadyAssigned[storeScopedBeadKey{ID: work.ID}] {
		t.Fatalf("warm tick: ReadyAssigned missing %s (%v); the fixture no longer models a reachable bead, so the cold assertion below would prove nothing", work.ID, warm.ReadyAssigned)
	}

	// The retirement. Nothing about the work changes.
	closed := live
	closed.Status = "closed"
	cold := build(closed)

	if !cold.ReadyAssigned[storeScopedBeadKey{ID: work.ID}] {
		t.Errorf("after self-retirement: ReadyAssigned lost %s (%v); the readiness verdict must not depend on a session bearing the alias, or the pool that just retired can never be re-minted for its own open work", work.ID, cold.ReadyAssigned)
	}
	if got := deadSlotDesiredCount(cold, pool); got != 1 {
		t.Errorf("desired %q sessions after self-retirement = %d, want 1; PoolDesiredCounts=%v ScaleCheckCounts=%v", pool, got, cold.PoolDesiredCounts, cold.ScaleCheckCounts)
	}
}
