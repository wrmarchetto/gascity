package main

// cmd/gc/pool_slot_recycle_test.go
//
// The pool-slot recycle contract for a singleton pool whose only session was
// force-stopped by the assigned-work-exhausted backstop.
//
// Scope: one reconciler tick, driven through the real
// buildDesiredState -> ComputePoolDesiredStates -> ComputeAwakeSet ->
// reconcileSessionBeads pipeline against memory stores and the fake runtime
// provider. It delegates the per-reason allowlist table to
// TestIsPoolSessionSlotFreeable_Matrix and the queue-vs-owned item predicate to
// the drain-ack close-gate tests; what only this file can represent is the
// INTERACTION -- a bead that three separate subsystems each read differently.
//
// Why the suite exists at all: a singleton pool's alias and its queue address
// are the SAME string, so "work assigned to this session" and "work waiting at
// this pool's door" are indistinguishable by assignee. Three readers disagreed
// about that bead. The idle ladder counted it as owned and deferred the kill;
// ComputeAwakeSet did not count it and refused the wake; the pool-slot close
// gate counted it and refused the close. The result is a stable fixed point --
// no wake, no spawn, no close -- that a test of any single reader goes green
// over. Measured on the live city 2026-09-13: session ci-5co05c held the
// bench-engineer alias for ~4h with a ready P1 behind it, then ci-v2ocb2 did it
// again for ~4h more (city bead ci-l38chb).
//
// Run it:
//
//	go test ./cmd/gc/ -run 'TestPoolSlotRecycle'
//
// WHAT THIS SUITE CANNOT REPRESENT, so the absence is not mistaken for
// coverage: it never runs a provider, so it cannot show that the replacement
// session claims the queued bead, and it does not drive repeated ticks, so it
// says nothing about a spawn/backstop treadmill. Both are city-side
// observations -- doctor/session-spawn-rate owns the second.
//
// DELIBERATELY ABSENT: a safety case holding the rig claim with NO queued work
// beside it. It was written, it passed, and it was VACUOUS -- with the close
// gate forced to hasAssignedWork=false it still passed, because with no queue
// work there is no pool demand, so the holder is never made a reconcile target
// and the gate does not execute (probed at the poolFreeable site: no decision
// is recorded for that session at all). The claim-beside-queued-work case below
// is the rig-store guard that can actually go red; do not re-add the quiet one.

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// poolSlotRecycleResult is one tick's observable outcome for the holder bead.
type poolSlotRecycleResult struct {
	holderClosed bool
	starts       []string
	sessionBeads int
}

// recycleTickForExhaustedPoolHolder drives one reconciler tick over a singleton
// pool whose sole session bead is asleep under sleepReason, holding the pool's
// canonical alias with its runtime gone.
//
// queuedWork and rigWork are created before the tick: queuedWork lands in the
// city store, rigWork in a separate store registered as a rig. Both are
// addressed to the bare template name, which is the canonical singleton's alias
// AND the pool's queue address -- that collision is the whole subject.
//
// The agent is scope=city so reachableStoresForSessionInfo federates across
// both stores. A rig-scoped agent would read the city store alone and pass the
// rig-store case vacuously, which is the defect this construction exists to
// make unrepresentable.
func recycleTickForExhaustedPoolHolder(
	t *testing.T,
	sleepReason sessionpkg.SleepReason,
	queuedWork []beads.Bead,
	rigWork []beads.Bead,
) poolSlotRecycleResult {
	t.Helper()

	const template = "bench-engineer"
	const holderSessionName = template + "-ci-holder"

	cityPath := t.TempDir()
	store := beads.NewMemStore()
	rigStore := beads.NewMemStore()
	rigStores := map[string]beads.Store{"lab": rigStore}
	clk := &clock.Fake{Time: time.Date(2026, 9, 13, 17, 30, 0, 0, time.UTC)}
	sp := runtime.NewFake()

	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:              template,
			Scope:             "city",
			StartCommand:      "true",
			MaxActiveSessions: intPtr(1),
			MinActiveSessions: intPtr(0),
			// Empty scale_check output on purpose. Demand must arrive through
			// the pool-alias assignee tier, which is how the live city files
			// this work; a work_query that manufactured demand would let the
			// tick pass without ever exercising that tier.
			WorkQuery: "printf ''",
		}},
		Rigs: []config.Rig{{Name: "lab", Path: cityPath}},
	}

	for _, b := range queuedWork {
		createWorkBeadWithStatus(t, store, b)
	}
	for _, b := range rigWork {
		createWorkBeadWithStatus(t, rigStore, b)
	}

	holder, err := store.Create(beads.Bead{
		Title:  holderSessionName,
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": holderSessionName,
			// alias == template is the canonical singleton identity collapse.
			// pool_slot is deliberately ABSENT, matching the live bead:
			// poolSlotMetadataInt reads an absent key as slot 0, which is the
			// slot poolDesiredRequestIdentity hands a singleton.
			"alias":          template,
			"agent_name":     template,
			"template":       template,
			"pool_managed":   "true",
			"state":          "asleep",
			"sleep_reason":   string(sleepReason),
			"generation":     "1",
			"instance_token": "holder-token",
		},
	})
	if err != nil {
		t.Fatalf("Create(session): %v", err)
	}

	var stdout, stderr bytes.Buffer
	dsResult := buildDesiredState(cfg.EffectiveCityName(), cityPath, clk.Now().UTC(), cfg, sp, store, &stderr)
	cfgNames := configuredSessionNames(cfg, cfg.EffectiveCityName(), store)
	syncSessionBeads(cityPath, store, dsResult.State, sp, cfgNames, cfg, clk, &stderr, true)
	sessions, err := loadSessionBeads(store)
	if err != nil {
		t.Fatalf("loadSessionBeads: %v", err)
	}
	poolDesired := PoolDesiredCounts(ComputePoolDesiredStates(cfg, dsResult.AssignedWorkBeads, sessionInfosFromBeads(sessions), dsResult.ScaleCheckCounts))
	if poolDesired == nil {
		poolDesired = make(map[string]int)
	}

	snap := newSessionBeadSnapshot(sessions)
	reconcileSessionBeadsAtPathWithNamedDemand(
		context.Background(), cityPath, snap.OpenForReconcile(), snap, dsResult.State, cfgNames, cfg, sp,
		store, nil, dsResult.AssignedWorkBeads, rigStores, nil, newDrainTracker(), nil, poolDesired,
		dsResult.NamedSessionDemand, dsResult.NamedSessionRoutedDemand, dsResult.StoreQueryPartial, nil, cfg.EffectiveCityName(),
		nil, clk, events.Discard, 0, 0, &stdout, &stderr,
	)

	result := poolSlotRecycleResult{}
	for _, call := range sp.SnapshotCalls() {
		if call.Method == "Start" {
			result.starts = append(result.starts, call.Name)
		}
	}
	post, err := loadSessionBeads(store)
	if err != nil {
		t.Fatalf("loadSessionBeads (post-reconcile): %v", err)
	}
	result.sessionBeads = len(post)
	result.holderClosed = true
	for _, b := range post {
		if b.ID == holder.ID {
			result.holderClosed = b.Status == "closed"
		}
	}
	return result
}

// createWorkBeadWithStatus creates want and leaves it in want.Status, reading
// the status back before returning.
//
// MemStore.Create hardcodes Status to "open" (internal/beads/memstore.go), so a
// fixture that passes Status:"in_progress" gets an OPEN bead and no error. That
// is not a cosmetic difference here -- open-versus-in_progress IS the
// discriminator the close gate turns on, so the silent downgrade turns a
// "holder still owns a claim" case into a "queued work only" case and the test
// asserts the opposite of what it reads. It happened while writing this file:
// the rig-store safety case passed against a bead that was open the whole time.
// The readback is what makes the fixture unable to lie again.
func createWorkBeadWithStatus(t *testing.T, store beads.Store, want beads.Bead) {
	t.Helper()
	created, err := store.Create(want)
	if err != nil {
		t.Fatalf("Create(%q): %v", want.Title, err)
	}
	if want.Status != "" && created.Status != want.Status {
		if err := store.Update(created.ID, beads.UpdateOpts{Status: &want.Status}); err != nil {
			t.Fatalf("Update(%q -> status %q): %v", want.Title, want.Status, err)
		}
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get(%q): %v", created.ID, err)
	}
	if want.Status != "" && got.Status != want.Status {
		t.Fatalf("fixture %q landed in status %q, want %q", want.Title, got.Status, want.Status)
	}
}

// queuedPoolDoorBead is open work addressed to the bare pool name and pinned to
// no session: the shape city automation files when it assigns work to a pool by
// name. It carries no session-affinity metadata, which is what makes it the
// queue's and not the departing occupant's.
func queuedPoolDoorBead(title string) beads.Bead {
	return beads.Bead{
		Title:    title,
		Type:     "task",
		Status:   "open",
		Assignee: "bench-engineer",
	}
}

// claimedPoolDoorBead is work the holder actually CLAIMED: in_progress under
// the same alias string. A claim is instance ownership no matter which identity
// it was claimed under, so this bead must pin the slot shut.
func claimedPoolDoorBead(title string) beads.Bead {
	return beads.Bead{
		Title:    title,
		Type:     "task",
		Status:   "in_progress",
		Assignee: "bench-engineer",
	}
}

// TestPoolSlotRecycleFreesExhaustedHolderWithOnlyQueuedWork pins the recovery
// half: a singleton pool holder that was force-stopped by the
// assigned-work-exhausted backstop, whose runtime is gone and which holds no
// claim, must stop occupying the pool.
//
// The assertion is the DISJUNCTION -- closed, or a session started -- rather
// than "closed", because either discharges the starvation and pinning the
// mechanism would refuse a future fix that chose the wake instead. What must
// never happen is neither, which is the measured live state: the alias held,
// the queue full, and nothing moving.
func TestPoolSlotRecycleFreesExhaustedHolderWithOnlyQueuedWork(t *testing.T) {
	got := recycleTickForExhaustedPoolHolder(
		t,
		sessionpkg.SleepReasonAssignedWorkExhausted,
		[]beads.Bead{queuedPoolDoorBead("queued P1 behind the held alias")},
		nil,
	)
	if !got.holderClosed && len(got.starts) == 0 {
		t.Fatalf("exhausted holder neither closed nor replaced: holderClosed=%v starts=%v sessionBeads=%d; "+
			"the pool alias stays held and queued work waits for a human to run gc session close",
			got.holderClosed, got.starts, got.sessionBeads)
	}
}

// TestPoolSlotRecycleKeepsHolderWithClaimedWorkBesideQueuedWork is the
// discriminator both other cases only half-establish: queued work and a live
// claim are present at once, which is the realistic shape when a session wedges
// mid-task and the queue keeps filling behind it.
//
// An implementation that answers the queued case by ignoring alias-addressed
// work wholesale passes both tests above and fails this one, because it would
// ignore the claim too.
func TestPoolSlotRecycleKeepsHolderWithClaimedWorkBesideQueuedWork(t *testing.T) {
	got := recycleTickForExhaustedPoolHolder(
		t,
		sessionpkg.SleepReasonAssignedWorkExhausted,
		[]beads.Bead{queuedPoolDoorBead("queued work behind the wedged holder")},
		[]beads.Bead{claimedPoolDoorBead("rig work this holder claimed and never finished")},
	)
	if got.holderClosed {
		t.Fatalf("holder closed while holding an in_progress claim: starts=%v; "+
			"queued work at the pool door must not license closing a session that still owns a claim",
			got.starts)
	}
}
