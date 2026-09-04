// cmd/gc/build_desired_state_dead_slot_route_demand_test.go
//
// Scope: the composite wake path for PLAIN WORK left addressed to a POOL SLOT
// identity ("worker-2") whose slot is cold, and routed to that slot's BASE
// pool ("worker"). One tick of buildDesiredState reports zero demand for that
// form; the orphan sweep clears the dead address and the NEXT tick counts the
// bead as ordinary routed-unassigned demand.
//
// The work TYPE is a discriminator, not a detail, and getting this wrong was
// the first draft's error -- it generalized the phase-1 zero to "this shape".
// Measured 2026-09-04, phase-1 desired sessions for an otherwise identical
// bead:
//
//	task                       0
//	wisp      + ephemeral      1
//	molecule  + ephemeral      1
//	molecule  + gc.kind=workflow   1
//
// isOpenAssignedMoleculeWork (build_desired_state.go) stamps readiness for the
// molecule forms, so the wake-known-identity tier reaches them in-tick with no
// release involved. Only the plain form depends on the release. The molecule
// case is pinned below rather than described, because a reader who takes the
// zero as a property of the addressing shape draws the opposite conclusion
// about whether a fix is needed.
//
// Why this suite exists: ci-vjesdq argued the shape was invisible to every
// demand path, having traced only the in-tick tiers -- controllerDemandPoolAliasTarget
// refuses it (assignee != route, so #2527 reads it as a concrete handoff) and
// filterAssignedWorkBeadsForPoolDemand skips it (appendOpenRoutedWorkUnique
// never markReadyAssigned, so no readiness verdict exists). Both observations
// are correct and neither is the whole path. Nothing pinned the composite, so
// the conclusion could be drawn from either half in isolation and be wrong
// either way: a suite that asserts demand within ONE call goes red over correct
// behavior, and a suite that asserts only the release goes green over a demand
// tier that has stopped counting the released bead.
//
// Delegated elsewhere: the release decision gate-by-gate is
// TestOrphanSweepStillReleasesRoutedSeatAddressWhenNoSessionBearsIt
// (cmd_session_close_addressed_work_test.go), which owns the boundary against
// the session-close path. That the ASSIGNED form must NOT count as pool demand
// is TestDefaultScaleCheckCountsDoesNotTreatTemplateAssigneeAsDemand and
// TestDefaultScaleCheckCountsExcludesBeadsAssignedToSession
// (build_desired_state_test.go, #2527). This suite owns only the composition
// of those two, in the order the reconciler runs them.
//
// Why the build/release/build sequence is the production order and not a
// harness convenience: of the four non-test call sites of
// filterAssignedWorkBeadsForPoolDemand, the one that SETS PoolDesiredCounts is
// loadDemandSnapshot (city_runtime.go:3449, reached at :1291) and it runs
// BEFORE beadReconcileTick performs the release (:2363). So a release is never
// visible on the tick that performs it; it lands on the next demand-snapshot
// refresh, which the fingerprint bead forces because it hashes Assignee and
// UpdatedAt -- both of which the release changes -- and which happens every
// patrol tick anyway while demandSnapshotPatrolMaxAge() is 0. "Eventually" is
// one patrol tick, not indefinitely.
//
// What this suite cannot represent, so the manual check is not mistaken for
// redundant:
//
//   - Every store here is a MemStore. liveWorkAssignmentStillReleasable reads
//     the backing store with Live + TierBoth, and BdStore's behavior for that
//     on a real Dolt-backed rig store is unexercised by any host fixture.
//   - The release is fail-closed in three places -- snapshotQueryPartial
//     (pool_session_name.go:108), the liveOpenSessionAssignmentExists List-error
//     branch (:566), and liveWorkAssignmentStillReleasable's error return
//     (:659). Each DELAYS release by a tick rather than preventing it, but a
//     store with intermittent List failures therefore holds a dead address
//     longer than anything below shows.
//   - Tick ordering itself. These tests call buildDesiredState directly and
//     sequence the release by hand; only the cityRuntime tick proves the order
//     above still holds.
//   - A CYCLE. These tests drive one release. Production has recorded the same
//     bead being released repeatedly -- bead.dead_assignee_reopened fired ten
//     times for as-1xp inside 6.5 minutes on 2026-09-04, in the window
//     city.toml records as "session creates climbed 2 -> 10 in six minutes".
//     Something re-addresses a bead after the release and the release fires
//     again. So "recovers on the next tick" is one iteration; it is NOT a
//     claim that the bead reaches a fixed point, and two hand-fed ticks cannot
//     represent a loop. Attribution is owned elsewhere (ci-vcornx, ci-77oav9).
//   - Whether the shape occurs at all. A population sweep on 2026-09-04 found
//     zero live instances in every store, but 731c531cb landed that same day
//     and is what makes this address PERSIST -- before it, session close
//     stripped it. A post-change sweep cannot tell "healed" from
//     "not yet created", so that zero bounds nothing about frequency. Nor can
//     doctor/ready-assignee see the shape either way: it strips a trailing
//     "-<N>" before checking membership, so a dead slot's address resolves to
//     its live base pool and the check reports success.
//
// Run: go test ./cmd/gc/ -run 'DeadSlotAssignment|FullyColdCity|OrphanedOpenWork'
package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// deadSlotPoolAgent builds a cold multi-slot pool. min=0 is what makes the
// assertion meaningful: any desired session for this template had to come from
// bead-backed demand rather than from a configured floor.
func deadSlotPoolAgent(name string) config.Agent {
	return config.Agent{
		Name:              name,
		StartCommand:      "true",
		MinActiveSessions: intPtr(0),
		MaxActiveSessions: intPtr(3),
	}
}

// deadSlotCity puts the pool agents beside an on_demand named session, which is
// load-bearing rather than scenery. readyAssignedWorkAssignees seeds the Ready
// probe list from live session beads plus configured on_demand named sessions,
// and collectAssignedWorkBeadsWithStores falls back to an UNFILTERED Ready()
// only when that list comes back empty. Without the named session, an empty
// probe list makes the unfiltered fallback capture the dead-slot bead and stamp
// it ready, so the pool wakes in phase 1 and the suite reports behavior no
// running city produces -- a city always has at least one open session bead.
// Measured: with the named session removed, phase 1 reads demand 1 instead of 0.
// The same trap is recorded on poolAliasDemandCity.
func deadSlotCity(poolNames ...string) *config.City {
	cfg := &config.City{
		Workspace:     config.Workspace{Name: "gc"},
		NamedSessions: []config.NamedSession{{Template: "overseer", Mode: "on_demand"}},
		Providers:     map[string]config.ProviderSpec{"mock": {Command: "true"}},
	}
	for _, name := range poolNames {
		cfg.Agents = append(cfg.Agents, deadSlotPoolAgent(name))
	}
	cfg.Agents = append(cfg.Agents, config.Agent{Name: "overseer", Provider: "mock"})
	return cfg
}

func deadSlotDesiredCount(res DesiredStateResult, template string) int {
	n := 0
	for _, tp := range res.State {
		if tp.TemplateName == template {
			n++
		}
	}
	return n
}

// TestDeadSlotAssignmentRoutedToItsBasePoolWakesThatPoolOnlyAfterOrphanRelease
// pins that the cold pool DOES eventually wake for a bead addressed to one of
// its own dead slots, and pins the release as the step that makes it happen.
//
// The three-phase shape is the invariant, not an artifact of the harness.
// Phase 1 asserting zero is what stops a future editor collapsing the address
// onto its template in the in-tick tiers; phase 3 asserting non-zero is what
// fails if the release stops firing or the routed tier stops counting the
// freed bead -- the two ways this work strands with no live instance to
// notice.
//
// Mutation ledger, all executed on 2026-09-04, because a green assertion here
// is worth nothing until it has been seen to go red:
//
//	appendOpenRoutedWorkUnique stamps a readiness verdict     phase 1 -> red
//	alias tier normalizes the assignee AND drops the
//	  concrete-handoff guard                                  phase 1 -> red
//	release skips a slot alias of its own template            phase 2 -> red
//	release skips any slot-alias assignee                     phase 2 -> red
//	release also clears gc.routed_to                    phase 2/3(beta) -> red
//	scale-check count accumulates per tick                    phase 4 -> red
//	appendOpenRoutedWorkUnique drops its route requirement   unrouted -> red
//	the unfiltered Ready() fallback is removed              cold city -> red
//	appendOpenAssignedMoleculeWorkUnique stops stamping
//	  readiness                                              molecule -> red
//
// Two assertions were NOT independently observed. Phase 3 of the same-pool test:
// Every mutation reaching it trips an earlier assertion in the same function
// first. Its cross-pool twin (the beta assertion below) died under the
// clear-gc.routed_to mutation and exercises the same counting code, so the
// coverage is real but inherited -- do not read it as directly witnessed. And
// the cold-city test's UPPER bound (more than one desired session) has been
// reasoned to from the measured duplicate capture, never seen red: no cheap
// mutation makes the duplicate inflate demand without rewriting the demand
// reader itself. It is a tripwire for a future consumer, not a proven guard.
//
// Recorded because it was measured and is the opposite of what was expected:
// normalizing the assignee in controllerDemandPoolAliasTarget WITHOUT also
// dropping the concrete-handoff guard leaves both tests here green. It turns
// TestPoolAliasDemandExclusionsMatchTheShellPredicate/slot_identity red
// instead ("demand = 1, want 0 -- this shape raises spawn demand no session
// can consume"), which already owns that invariant. Nothing in this file
// pins it, and it must not be re-derived here.
func TestDeadSlotAssignmentRoutedToItsBasePoolWakesThatPoolOnlyAfterOrphanRelease(t *testing.T) {
	const (
		pool = "worker"
		slot = "worker-2"
	)
	cityPath := t.TempDir()
	store := beads.NewMemStore()

	// Type "task" is the load-bearing choice, NOT scenery -- see the type
	// dependence recorded in this file's header. A plain non-molecule work type
	// is the only form that reads zero in phase 1.
	//
	// Deliberately NOT sourced from a live bead. An earlier draft cited as-7uha
	// as an instance of this shape; ci-vjesdq retracts that reading as
	// confounded (the slot-suffixed assignee there is the residue of a
	// successful pool-alias claim, and its production dead-assignee records
	// show a BARE template assignee against a DIFFERENT pool's route, which is
	// the ci-vcornx cross-store shape). This fixture is constructed from the
	// code paths under test, so it cannot inherit that error.
	work, err := store.Create(beads.Bead{
		Title:    "left addressed to a dead slot, routed to its own pool",
		Type:     "task",
		Status:   "open",
		Assignee: slot,
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

	// Phase 1: no session bead bears the slot identity, so the slot is cold.
	before := build()
	if got := deadSlotDesiredCount(before, pool); got != 0 {
		t.Fatalf("desired %q sessions before orphan release = %d, want 0; an addressed bead must not raise pool-door demand while it still names a concrete slot (#2527)", pool, got)
	}
	if got := before.PoolDesiredCounts[pool]; got != 0 {
		t.Fatalf("PoolDesiredCounts[%q] before orphan release = %d, want 0", pool, got)
	}

	// Phase 2: the sweep is fed exactly the snapshot the reconciler feeds it.
	// Building the input by hand would let this test keep passing after the bead
	// stopped reaching AssignedWorkBeads at all, which is one of the two failure
	// modes it exists to catch.
	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, cityPath, nil,
		before.AssignedWorkBeads, before.AssignedWorkStores, before.AssignedWorkStoreRefs, nil,
	)
	if len(released) != 1 || released[0].ID != work.ID {
		t.Fatalf("orphan release returned %v, want exactly [%s]; the slot-orphan path is the only door this shape has, so a skip here strands the bead permanently", released, work.ID)
	}
	after, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get released work: %v", err)
	}
	if after.Assignee != "" {
		t.Fatalf("released work assignee = %q, want cleared", after.Assignee)
	}
	if after.Metadata["gc.routed_to"] != pool {
		t.Fatalf("released work gc.routed_to = %q, want %q retained; the release must degrade one-slot to any-slot, not discard the route", after.Metadata["gc.routed_to"], pool)
	}

	// Phase 3: the next tick sees unassigned routed work and wakes the pool.
	next := build()
	woke := deadSlotDesiredCount(next, pool)
	if woke < 1 {
		t.Fatalf("desired %q sessions after orphan release = %d, want at least 1; PoolDesiredCounts=%v ScaleCheckCounts=%v", pool, woke, next.PoolDesiredCounts, next.ScaleCheckCounts)
	}

	// Phase 4 is the discriminating direction, and it is why phase 3 asserts a
	// COUNT rather than "at least one session appeared". One freed bead is a
	// bounded overshoot: the pool should want one slot for it and go on wanting
	// exactly one while the bead sits open and unclaimed. A demand reader that
	// re-counted the same bead every tick looks identical in phase 3 and is the
	// PR #1516 wake/drain storm, so the tick number is the only variable moved
	// here -- the store is untouched between the two builds.
	again := build()
	if got := deadSlotDesiredCount(again, pool); got != woke {
		t.Fatalf("desired %q sessions grew from %d to %d across two ticks over ONE still-open bead; demand must be a level the reconciler spends, not an increment per tick. PoolDesiredCounts=%v", pool, woke, got, again.PoolDesiredCounts)
	}
	if woke > 1 {
		t.Fatalf("one freed bead wants %d %q slots; a single unit of work must not overshoot into a spawn storm", woke, pool)
	}
}

// TestOpenMoleculeRootOnADeadSlotRaisesWakeDemandWithoutTheRelease pins the
// discriminator, so the zero next door can never be read as a property of the
// addressing shape.
//
// An open molecule/wisp root that is a direct assignment is genuinely
// actionable -- isOpenAssignedMoleculeWork admits it and stamps readiness --
// so the wake-known-identity tier reaches it on the SAME tick, with no orphan
// release involved. This is why the two candidate fixes on ci-vjesdq are not
// symmetric: the tier that would serve the plain form already exists and
// already fires here, one readiness verdict away.
//
// Absence documented where a reader looks for it: there is deliberately no
// blocked-molecule case here. appendOpenAssignedMoleculeWorkUnique applies no
// dependency gate, so a BLOCKED open molecule root would also be stamped ready
// -- which is the hole the readiness gate in assigned_work_scope.go exists to
// close, and it is ci-vjesdq's open question, not this file's. Adding a case
// that asserts today's answer would freeze a question that is still open.
func TestOpenMoleculeRootOnADeadSlotRaisesWakeDemandWithoutTheRelease(t *testing.T) {
	const (
		pool = "worker"
		slot = "worker-2"
	)
	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		Title:     "orphaned wisp root on a dead slot",
		Type:      "wisp",
		Status:    "open",
		Assignee:  slot,
		Ephemeral: true,
		Metadata:  map[string]string{"gc.routed_to": pool},
	}); err != nil {
		t.Fatalf("create work: %v", err)
	}
	result := buildDesiredStateWithSessionBeads(
		"gc", t.TempDir(), time.Now().UTC(), deadSlotCity(pool), runtime.NewFake(),
		store, nil, &sessionBeadSnapshot{}, nil, io.Discard,
	)
	if got := deadSlotDesiredCount(result, pool); got != 1 {
		t.Fatalf("desired %q sessions for an open wisp root on a dead slot = %d, want exactly 1 on the FIRST tick; zero means isOpenAssignedMoleculeWork stopped reaching it and the molecule form now depends on the orphan release like plain work does, more than one means the wake tier and another tier are both counting it. ScaleCheckCounts=%v", pool, got, result.ScaleCheckCounts)
	}
}

// TestFullyColdCityRecoversOrphanedOpenWorkThroughTheUnfilteredReadyProbe is
// the other direction of the same mechanism, and it exists because the two
// regimes reach demand through DIFFERENT tiers -- a suite that pinned only the
// running-city path would go green over the cold path being deleted.
//
// With no open session bead and no on_demand named session anywhere,
// readyAssignedWorkAssignees returns an empty probe list and
// collectAssignedWorkBeadsWithStores falls back to an UNFILTERED Ready(). That
// stamps the dead-slot bead ready, so it arrives as RESUME demand on tick 1 --
// the scale-check tier never sees it, because the bead is still assigned.
//
// The consequence a future editor needs to know, and the reason this is an
// assertion rather than a comment: the build then mints a session bead whose
// alias IS the dead slot's, after which openSessionOwnsWork skips the orphan
// release for that address permanently. Cold-city recovery and running-city
// recovery are therefore not two roads to one place; taking the cold one
// forecloses the other. Removing the unfiltered fallback would strand this
// bead with nothing else reaching it, and this test is what says so.
func TestFullyColdCityRecoversOrphanedOpenWorkThroughTheUnfilteredReadyProbe(t *testing.T) {
	const (
		pool = "worker"
		slot = "worker-2"
	)
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		Title:    "orphaned open work in a stopped city",
		Type:     "task",
		Status:   "open",
		Assignee: slot,
		Metadata: map[string]string{"gc.routed_to": pool},
	}); err != nil {
		t.Fatalf("create work: %v", err)
	}

	// Deliberately WITHOUT the on_demand named session deadSlotCity adds: an
	// empty probe list is the whole precondition under test, so borrowing that
	// fixture would silently test the running-city path instead.
	cfg := &config.City{
		Workspace: config.Workspace{Name: "gc"},
		Agents:    []config.Agent{deadSlotPoolAgent(pool)},
	}
	result := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
		store, nil, &sessionBeadSnapshot{}, nil, io.Discard,
	)
	// Exactly one, not "at least one", and the upper bound is load-bearing.
	// In THIS regime the bead is captured twice -- once by
	// appendOpenRoutedWorkUnique and once by the unfiltered Ready() handoff
	// pass -- because appendWorkUnique's `seen` map is per-fan-out and the
	// second fan-out allocates a fresh one. Measured 2026-09-04:
	// AssignedWorkBeads holds gc-1 twice here and once in the running-city
	// regime. Nothing downstream inflates on that today, which is exactly why
	// it needs an assertion rather than a comment: the duplicate is latent, and
	// the first consumer to count poolWorkBeads by length turns this red instead
	// of quietly spawning two slots for one bead at boot.
	if got := deadSlotDesiredCount(result, pool); got != 1 {
		t.Fatalf("desired %q sessions in a fully-cold city = %d, want exactly 1; zero means the unfiltered Ready() fallback no longer reaches orphaned OPEN assigned work and a stopped city cannot restart its own stranded work, while more than one means the duplicate capture in AssignedWorkBeads has started inflating demand. PoolDesiredCounts=%v ScaleCheckCounts=%v AssignedWorkBeads=%d", pool, got, result.PoolDesiredCounts, result.ScaleCheckCounts, len(result.AssignedWorkBeads))
	}
}

// TestOrphanedOpenWorkWithNoRouteReachesNeitherDemandNorTheOrphanSweep records
// the residue of ci-vjesdq rather than a fix, and it is the boundary of the two
// tests above: everything they prove depends on gc.routed_to being present.
//
// appendOpenRoutedWorkUnique admits a bead only when it carries a route, and
// releaseOrphanedPoolAssignments skips on the same `template == ""` guard. An
// OPEN bead addressed to a cold slot with NO route is therefore invisible to
// every demand path AND to the sweep that would free it, and its address is
// deliberately preserved -- TestOrphanSweepStillReleasesRoutedSeatAddressWhenNoSessionBearsIt
// (ci-8vx85v) exists to keep session close from stripping exactly this field.
// The routed shape self-heals in one extra tick; this one does not heal at all,
// and only a slot spawning for some other reason ever finds it.
//
// This test asserts the CURRENT behavior, which is the defect, so it is written
// to fail loudly if that changes rather than to bless it: whoever closes the
// gap must come here, read why the address is protected, and delete this test
// deliberately. Recording it as an assertion rather than a comment is the point
// -- prose about a known hole expires silently, because every later change
// declares the file untouched.
func TestOrphanedOpenWorkWithNoRouteReachesNeitherDemandNorTheOrphanSweep(t *testing.T) {
	const (
		pool = "worker"
		slot = "worker-2"
	)
	cityPath := t.TempDir()
	store := beads.NewMemStore()

	work, err := store.Create(beads.Bead{
		Title:    "addressed to a dead slot, no route",
		Type:     "task",
		Status:   "open",
		Assignee: slot,
	})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}

	cfg := deadSlotCity(pool)
	result := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
		store, nil, &sessionBeadSnapshot{}, nil, io.Discard,
	)
	if got := deadSlotDesiredCount(result, pool); got != 0 {
		t.Fatalf("desired %q sessions = %d; if an unrouted slot address now raises demand the gap is closed and this test should be deleted, not adjusted", pool, got)
	}
	// Not merely absent from demand: absent from the snapshot the sweep reads,
	// which is what makes the state terminal rather than delayed.
	if len(result.AssignedWorkBeads) != 0 {
		t.Fatalf("AssignedWorkBeads = %d, want 0; unrouted work reaching the snapshot means appendOpenRoutedWorkUnique changed and the sweep may now free it", len(result.AssignedWorkBeads))
	}
	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, cityPath, nil,
		result.AssignedWorkBeads, result.AssignedWorkStores, result.AssignedWorkStoreRefs, nil,
	)
	if len(released) != 0 {
		t.Fatalf("orphan release returned %v, want none; the sweep cannot see unrouted work", released)
	}
	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatalf("get work: %v", err)
	}
	if got.Assignee != slot {
		t.Fatalf("work assignee = %q, want %q retained; ci-8vx85v protects this address on purpose", got.Assignee, slot)
	}
}

// TestDeadSlotAssignmentRoutedToAnotherPoolNeverWakesTheSlotsOwnPool is the
// other direction, and it is the one a normalize-the-assignee fix breaks. Work
// routed to pool "beta" but still addressed to a dead slot of pool "alpha" is
// beta's work: alpha must stay cold through the release and after it, because
// alpha's only claim on the bead is a leftover address.
//
// Beta waking is correct and is asserted here so the test cannot be satisfied
// by a change that simply stops releasing this shape -- that would leave the
// bead stranded and alpha cold, passing an alpha-only assertion.
func TestDeadSlotAssignmentRoutedToAnotherPoolNeverWakesTheSlotsOwnPool(t *testing.T) {
	const (
		alpha     = "alpha"
		beta      = "beta"
		alphaSlot = "alpha-2"
	)
	cityPath := t.TempDir()
	store := beads.NewMemStore()

	work, err := store.Create(beads.Bead{
		Title:    "handed to beta, still addressed to a dead alpha slot",
		Type:     "task",
		Status:   "open",
		Assignee: alphaSlot,
		Metadata: map[string]string{"gc.routed_to": beta},
	})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}

	cfg := deadSlotCity(alpha, beta)
	build := func() DesiredStateResult {
		return buildDesiredStateWithSessionBeads(
			"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
			store, nil, &sessionBeadSnapshot{}, nil, io.Discard,
		)
	}

	before := build()
	if got := deadSlotDesiredCount(before, alpha); got != 0 {
		t.Fatalf("desired %q sessions before orphan release = %d, want 0", alpha, got)
	}

	released := releaseOrphanedPoolAssignmentsFromBeads(
		store, cfg, cityPath, nil,
		before.AssignedWorkBeads, before.AssignedWorkStores, before.AssignedWorkStoreRefs, nil,
	)
	if len(released) != 1 || released[0].ID != work.ID {
		t.Fatalf("orphan release returned %v, want exactly [%s]", released, work.ID)
	}

	after := build()
	if got := deadSlotDesiredCount(after, alpha); got != 0 {
		t.Fatalf("desired %q sessions after orphan release = %d, want 0; a dead slot's leftover address must never resurrect its own pool for work routed elsewhere (#2527)", alpha, got)
	}
	if got := deadSlotDesiredCount(after, beta); got < 1 {
		t.Fatalf("desired %q sessions after orphan release = %d, want at least 1; the route is the addressee once the dead address is cleared. PoolDesiredCounts=%v", beta, got, after.PoolDesiredCounts)
	}
}
