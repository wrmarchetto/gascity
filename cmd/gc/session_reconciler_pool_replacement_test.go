package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// TestReconcileSessionBeads_DrainAckNoWorkFreesSlotAndReallocates is the
// end-to-end regression guard for gastownhall/gascity#2520 ("pool over-counts
// supply when session drain-acks with no work and bead stays active").
//
// Scenario (min_active=0, max>=2 pool; two routed-ready beads; two sessions
// race to claim one): the winner takes bead-1 (in_progress), the loser gets
// "already claimed" and calls `gc runtime drain-ack` with NO work attached.
// The report claimed the loser's session bead lingers in state=active, the
// pool counts it as an occupied supply slot, and the next still-ready bead is
// never served until an operator runs `gc session close`.
//
// The maintainer classified #2520 as test-hardening: current main already
// behaves correctly (the drain-ack lands the loser in a terminal drained state,
// and a drained pool bead is excluded from the running-session supply count so
// the still-ready work is still served), but the full "no-work pool drain-ack
// PLUS replacement-allocation" path had no end-to-end coverage. Existing tests
// stop at the state transition or the pool-bead close; none then re-drives the
// supply probe to prove the drained loser is excluded AND a replacement slot is
// desired for the still-ready queue bead. This test locks in both halves.
//
// The second sub-test is the load-bearing regression assertion: it fails RED on
// the pre-#3419 revision (where poolSessionIsLive did not exclude drained pool
// beads, so a phantom drained bead counted toward runningSessions, forced
// isCold=false, suppressed the cold-wake probe, and stranded the ready bead —
// exactly #2520's over-count symptom) and passes on current main.
func TestReconcileSessionBeads_DrainAckNoWorkFreesSlotAndReallocates(t *testing.T) {
	// Part 1 — the real reconciler drains a no-work drain-acking loser to a
	// terminal state (it does NOT linger in state=active), which is the
	// precondition the #2520 report says was violated.
	t.Run("reconciler_drains_no_work_loser_to_terminal", func(t *testing.T) {
		now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
		cityDir := t.TempDir()
		writeCityTOML(t, cityDir, "trace-town", "worker")

		cfg := &config.City{
			Workspace: config.Workspace{Name: "trace-town"},
			Session:   config.SessionConfig{Provider: "fake"},
			Agents: []config.Agent{{
				Name:              "worker",
				Dir:               "repo",
				StartCommand:      "true",
				MinActiveSessions: intPtr(0),
				MaxActiveSessions: intPtr(2),
			}},
		}
		store := beads.NewMemStore()
		sp := runtime.NewFake()

		// Two routed-ready beads. bead-1 goes in_progress under the winner;
		// bead-2 stays ready in the queue.
		beadOne := createRoutedReadyBeadForReplacement(t, store, "repo/worker", "queued work 1")
		createRoutedReadyBeadForReplacement(t, store, "repo/worker", "queued work 2")

		// Winner: slot 1, active, holds bead-1 in_progress.
		winner := createCanonicalPoolSession(t, store, &cfg.Agents[0], now, 1)
		setPoolSessionActive(t, store, winner.ID)
		if err := sp.Start(context.Background(), winner.Metadata["session_name"], runtime.Config{}); err != nil {
			t.Fatalf("start winner runtime: %v", err)
		}
		statusInProgress := "in_progress"
		winnerAssignee := winner.ID
		if err := store.Update(beadOne.ID, beads.UpdateOpts{Status: &statusInProgress, Assignee: &winnerAssignee}); err != nil {
			t.Fatalf("assign bead-1 to winner: %v", err)
		}

		// Loser: slot 2, active, NO assigned work, agent-set drain-ack (the
		// #1425 stranded event never fires because hasAssignedWork=false).
		loser := createCanonicalPoolSession(t, store, &cfg.Agents[0], now, 2)
		setPoolSessionActive(t, store, loser.ID)
		loser, err := store.Get(loser.ID)
		if err != nil {
			t.Fatalf("reload loser: %v", err)
		}
		loserName := loser.Metadata["session_name"]
		if err := sp.Start(context.Background(), loserName, runtime.Config{}); err != nil {
			t.Fatalf("start loser runtime: %v", err)
		}
		dops := newFakeDrainOps()
		if err := dops.setDrainAck(loserName); err != nil {
			t.Fatalf("setDrainAck(loser): %v", err)
		}

		ds := buildDesiredState("trace-town", cityDir, now, cfg, sp, store, io.Discard)
		dt := newDrainTracker()
		clk := &clock.Fake{Time: now}

		// Tick 1: alive + agent-sourced drain-ack -> mark stop-pending and queue
		// the async provider stop.
		reconcileSessionBeads(
			context.Background(), []beads.Bead{loser}, ds.State, map[string]bool{"repo/worker": true},
			cfg, sp, store, dops, nil, nil, dt, ds.PoolDesiredCounts, false, nil, "trace-town",
			nil, clk, events.Discard, 0, 0, io.Discard, io.Discard,
		)
		waitForProviderStopped(t, sp, loserName)

		reloaded, err := store.Get(loser.ID)
		if err != nil {
			t.Fatalf("reload loser after tick 1: %v", err)
		}

		// Tick 2: runtime is gone -> finalize the stop-pending session to a
		// terminal drained state (pool-managed + no work -> close the bead).
		reconcileSessionBeads(
			context.Background(), []beads.Bead{reloaded}, ds.State, map[string]bool{"repo/worker": true},
			cfg, sp, store, dops, nil, nil, dt, ds.PoolDesiredCounts, false, nil, "trace-town",
			nil, clk, events.Discard, 0, 0, io.Discard, io.Discard,
		)

		got, err := store.Get(loser.ID)
		if err != nil {
			t.Fatalf("reload loser after tick 2: %v", err)
		}
		// #2520's precondition for the over-count is the loser lingering as a
		// live session. Assert it did NOT: it reached a terminal drained state.
		if got.Metadata["state"] == "active" && got.Status != "closed" {
			t.Fatalf("no-work drain-acked loser lingered as a live supply slot: state=%q status=%q metadata=%v",
				got.Metadata["state"], got.Status, got.Metadata)
		}
		if got.Metadata["state"] != "drained" {
			t.Fatalf("loser state = %q, want drained", got.Metadata["state"])
		}
		if poolSessionIsLiveInfo(sessiontest.SeedBead(t, got)) {
			t.Fatalf("drained loser still reports poolSessionIsLiveInfo=true; it would over-count supply: metadata=%v", got.Metadata)
		}
	})

	// Part 2 — replacement-allocation. A drained phantom pool session (the exact
	// terminal state Part 1 produces, but left open in the store as the report
	// describes it "lingering") must be excluded from the running-session supply
	// count, so a min=0 pool with still-ready cross-store work is NOT treated as
	// warm: its cold-wake probe fires and desires a replacement slot to serve the
	// stranded bead.
	//
	// This is the RED assertion: on the pre-#3419 revision the drained phantom
	// counts toward runningSessions -> isCold=false -> cold-wake probe suppressed
	// -> demand 0 and no desired slot (the ready bead is stranded). On current
	// main the phantom is excluded -> isCold=true -> demand 1 and one desired
	// slot. The parallel "active" sub-case is the control: a genuinely-live
	// session MUST still suppress the probe.
	t.Run("drained_phantom_excluded_from_supply_reallocates", func(t *testing.T) {
		cases := []struct {
			name          string
			meta          map[string]string
			wantDemand    int
			wantSlots     int
			wantStillLive bool
		}{
			{
				name:       "drained_phantom_frees_slot",
				meta:       map[string]string{"state": "drained"},
				wantDemand: 1, wantSlots: 1, wantStillLive: false,
			},
			{
				name:       "asleep_idle_phantom_frees_slot",
				meta:       map[string]string{"state": "asleep", "sleep_reason": "idle"},
				wantDemand: 1, wantSlots: 1, wantStillLive: false,
			},
			{
				name:       "active_session_still_suppresses_probe",
				meta:       map[string]string{"state": "active"},
				wantDemand: 0, wantSlots: 0, wantStillLive: true,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				tmpDir := t.TempDir()
				rigPath := tmpDir + "/rigs/rig-A"
				if err := os.MkdirAll(rigPath, 0o755); err != nil {
					t.Fatalf("mkdir rig path: %v", err)
				}
				maxSess := 5
				minSess := 0
				cfg := &config.City{
					Agents: []config.Agent{{
						Name:              "worker",
						MaxActiveSessions: &maxSess,
						MinActiveSessions: &minSess,
						ScaleCheck:        "printf 0", // custom check reports 0; only a cold-wake probe can raise demand
						Dir:               "rig-A",
						Provider:          "mock",
					}},
					Rigs:      []config.Rig{{Name: "rig-A", Path: rigPath}},
					Providers: map[string]config.ProviderSpec{"mock": {Command: "true"}},
				}
				cityStore := beads.NewMemStore()
				rigStore := beads.NewMemStore()
				rigStores := map[string]beads.Store{"rig-A": rigStore}
				qualifiedName := "rig-A/worker"

				meta := map[string]string{
					"template":     qualifiedName,
					"session_name": "worker-1",
					"pool_slot":    "1",
				}
				for k, v := range tc.meta {
					meta[k] = v
				}
				phantom, err := rigStore.Create(beads.Bead{
					ID: "session-loser", Status: "open", Type: sessionBeadType, Metadata: meta,
				})
				if err != nil {
					t.Fatalf("create phantom pool session: %v", err)
				}
				if live := poolSessionIsLiveInfo(sessiontest.SeedBead(t, phantom)); live != tc.wantStillLive {
					t.Fatalf("poolSessionIsLiveInfo(%s phantom) = %v, want %v", tc.name, live, tc.wantStillLive)
				}

				// Still-ready routed bead delivered cross-store to the city store
				// (the sleeping rig pool's own-store probe cannot see it, so only a
				// cold-wake probe over all stores serves it).
				if _, err := cityStore.Create(beads.Bead{
					ID: "bead-ready", Status: "open", Type: "task",
					Metadata: map[string]string{"gc.routed_to": qualifiedName},
				}); err != nil {
					t.Fatalf("create still-ready routed bead: %v", err)
				}

				result := buildDesiredStateWithSessionBeads(
					"test-city", tmpDir, time.Now(), cfg, &localMockProvider{},
					cityStore, rigStores, &sessionBeadSnapshot{}, nil, os.Stderr,
				)
				if demand := result.ScaleCheckCounts[qualifiedName]; demand != tc.wantDemand {
					t.Fatalf("ScaleCheckCounts[%s] = %d, want %d (drained/asleep phantom must not over-count supply; #2520)",
						qualifiedName, demand, tc.wantDemand)
				}
				workerSlots := 0
				for _, tp := range result.State {
					if tp.TemplateName == qualifiedName {
						workerSlots++
					}
				}
				if workerSlots != tc.wantSlots {
					t.Fatalf("desired %s slots = %d, want %d (replacement slot for the still-ready bead)",
						qualifiedName, workerSlots, tc.wantSlots)
				}
			})
		}
	})
}

// TestReusablePoolSessionInfo_DrainAckStopPendingNotReusable is the regression
// guard for the drain-ack + min_sessions=1 replacement bug.
//
// A pool session in state=draining (set by drain-ack-stop-pending) must NOT be
// returned as reusable. Without this exclusion, the min-floor fires (draining
// doesn't consume demand via poolSessionConsumesNewDemandInfo), but
// selectOrPlanPoolSessionBead maps the draining bead as the desired slot via
// reusablePoolSessionInfos, so no replacement session is started until the
// draining bead is eventually closed — requiring another reload or several more
// patrol ticks.
func TestReusablePoolSessionInfo_DrainAckStopPendingNotReusable(t *testing.T) {
	t.Parallel()

	drainingInfo := session.Info{
		ID:                  "session-draining",
		Template:            "repo/worker",
		SessionNameMetadata: "worker-1",
		PoolManaged:         true,
		PoolSlot:            "1",
		MetadataState:       string(session.StateDraining),
	}

	bp := &agentBuildParams{}
	cfgAgent := &config.Agent{Name: "worker", Dir: "repo"}

	if got := reusablePoolSessionInfo(bp, cfgAgent, "repo/worker", drainingInfo, nil); got {
		t.Fatalf("reusablePoolSessionInfo(state=%q) = true; "+
			"a drain-ack-stop-pending session must not be reusable as the desired slot "+
			"(fixes min_sessions=1 replacement stall, #drain-ack-min1-bug)",
			drainingInfo.MetadataState)
	}
}

func createRoutedReadyBeadForReplacement(t *testing.T, store beads.Store, template, title string) beads.Bead {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:    title,
		Type:     "task",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": template},
	})
	if err != nil {
		t.Fatalf("create routed ready bead %q: %v", title, err)
	}
	return b
}

func setPoolSessionActive(t *testing.T, store beads.Store, id string) {
	t.Helper()
	for k, v := range map[string]string{
		"state":                     "active",
		"pending_create_claim":      "",
		"pending_create_started_at": "",
	} {
		if err := store.SetMetadata(id, k, v); err != nil {
			t.Fatalf("SetMetadata(%s=%s): %v", k, v, err)
		}
	}
}

// TestReconcile_StaleWorktreeQuarantineHoldsPoolSlotAcrossTicks pins the
// invariant that a stale-worktree quarantine holds its pool slot for as long as
// the marker is on disk, across an arbitrary number of ticks.
//
// The test spans the whole quarantine hold, with the real state heal between
// ticks, because the defect is invisible on one. Tick 1 already reuses the
// quarantined bead; it is the heal in that same tick that rewrites the state,
// and only tick 2 sees the rewritten value and mints a replacement. A
// single-tick test goes green over the bug (ci-v1yc5x: 17 beads minted for one
// slot in 25 minutes, one mint every second tick).
//
// The tick COUNT is derived rather than chosen -- see quarantineHold below.
// Two ticks caught the original period-2 defect and nothing longer, which is
// the hole ci-s83v7p was filed for.
//
// The heal is driven through healStateWithRollbackInfo rather than a hand-built
// patch so the test cannot agree with a wrong projection: the assertion is on
// the planner's observable output (no new session bead for the held slot), not
// on the metadata the projection happens to write.
func TestReconcile_StaleWorktreeQuarantineHoldsPoolSlotAcrossTicks(t *testing.T) {
	now := time.Date(2026, 9, 5, 19, 57, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	cityDir := t.TempDir()
	writeCityTOML(t, cityDir, "quarantine-town", "toolsmith")

	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, worktreeStaleFileName), []byte("branch=fix/ci-sptsk3\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write stale worktree marker: %v", err)
	}

	cfg := &config.City{
		Workspace: config.Workspace{Name: "quarantine-town"},
		Session:   config.SessionConfig{Provider: "fake"},
		Agents: []config.Agent{{
			Name:              "toolsmith",
			Dir:               "repo",
			StartCommand:      "true",
			MinActiveSessions: intPtr(3),
			MaxActiveSessions: intPtr(3),
		}},
	}
	store := beads.NewMemStore()
	sp := runtime.NewFake()

	// Ready work routed at the pool. Without it the planner has no demand to
	// satisfy and would not mint even against a demoted bead, so the test would
	// pass for the wrong reason.
	createRoutedReadyBeadForReplacement(t, store, "repo/toolsmith", "queued toolsmith work")

	// THE WINDOW IS COMPUTED AGAINST THE CADENCE, NOT PICKED. Two ticks is the
	// MINIMUM that can observe a period-2 defect, so a two-tick test cannot
	// tell "the quarantine holds while the marker is on disk" from "it holds
	// one cycle and then demotes" -- a regression to any longer period
	// restores the ci-v1yc5x treadmill at a lower rate with this test still
	// green. The span is the quarantine hold over the interval the patrol
	// actually runs at, read through the same accessor production reads, so
	// changing either default moves this window with it instead of leaving a
	// literal behind.
	const quarantineHold = time.Hour
	ticks := int(quarantineHold / cfg.Daemon.PatrolIntervalDuration())
	if ticks < 2 {
		t.Fatalf("observation window is %d ticks, want at least 2: a window shorter than the defect's period cannot observe it, and this assertion would be vacuous", ticks)
	}

	held := createCanonicalPoolSession(t, store, &cfg.Agents[0], now, 1)
	quarantinePendingCreateForStaleWorktree(
		sessiontest.SeedBead(t, held), sessionFrontDoor(store), workDir, now, quarantineHold, io.Discard,
	)
	heldAgent := held.Metadata["agent_name"]
	if heldAgent == "" {
		t.Fatal("held pool session has no agent_name, want the canonical slot identity")
	}

	for _, slot := range []int{2, 3} {
		busy := createCanonicalPoolSession(t, store, &cfg.Agents[0], now, slot)
		setPoolSessionActive(t, store, busy.ID)
		reloaded, err := store.Get(busy.ID)
		if err != nil {
			t.Fatalf("reload busy slot %d: %v", slot, err)
		}
		if err := sp.Start(context.Background(), reloaded.Metadata["session_name"], runtime.Config{}); err != nil {
			t.Fatalf("start busy slot %d runtime: %v", slot, err)
		}
	}

	countSessionBeads := func(t *testing.T) (open int, forHeldSlot int) {
		t.Helper()
		all, err := store.List(beads.ListQuery{Type: sessionBeadType})
		if err != nil {
			t.Fatalf("list session beads: %v", err)
		}
		for _, b := range all {
			if b.Status == "closed" {
				continue
			}
			open++
			if b.Metadata["agent_name"] == heldAgent {
				forHeldSlot++
			}
		}
		return open, forHeldSlot
	}

	if open, _ := countSessionBeads(t); open != 3 {
		t.Fatalf("seeded open session beads = %d, want 3", open)
	}

	for tick := 1; tick <= ticks; tick++ {
		buildDesiredState("quarantine-town", cityDir, now, cfg, sp, store, io.Discard)
		open, forHeldSlot := countSessionBeads(t)
		if forHeldSlot != 1 {
			t.Fatalf("tick %d: session beads for %s = %d, want 1 (a second bead lands in the same marked worktree and is refused again)", tick, heldAgent, forHeldSlot)
		}
		if open != 3 {
			t.Fatalf("tick %d: open session beads = %d, want 3 (pool_desired is 3 and the held slot is still the canonical occupant)", tick, open)
		}

		// The reconciler heals advisory state in the same tick it plans, with
		// the runtime observed dead for the quarantined slot.
		current, err := store.Get(held.ID)
		if err != nil {
			t.Fatalf("tick %d: reload held slot: %v", tick, err)
		}
		if _, err := healStateWithRollbackInfo(seedSessionInfo(current), false, sessionFrontDoor(store), clk, 0, true); err != nil {
			t.Fatalf("tick %d: heal held slot: %v", tick, err)
		}
	}

	final, err := store.Get(held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := final.Metadata["quarantined_until"]; got == "" {
		t.Fatal("quarantined_until was cleared while the marker is still on disk; the slot would be woken straight back into the refusal")
	}
}

// TestReconcile_ClearingTheStaleMarkerReactivatesTheSameBead pins the unwedge
// remedy: when an operator removes a .worktree-stale marker, the quarantined
// slot is reactivated IN PLACE rather than replaced.
//
// WHY IDENTITY IS THE ASSERTION, and not the state fields. A replacement bead
// would satisfy every state check here -- it too would read asleep with an
// empty quarantined_until -- while restoring exactly the treadmill ci-v1yc5x
// fixed, on a slot an operator had just correctly repaired. instance_token and
// generation are what distinguish reactivation from replacement, so they are
// what this asserts, alongside the count of session beads the planner mints
// afterwards.
//
// The expected transition was captured by hand on 2026-09-06 while unwedging
// city/bench-engineer (bead ci-tcvde5, instance_token 46b26d86..., generation 2
// unchanged across it). A live observation is not a test: it does not run
// again, which is why ci-s83v7p asked for this.
//
// doctor/slot-wedged-by-marker prints this remedy verbatim, so a regression
// here silently turns a documented repair into a no-op.
func TestReconcile_ClearingTheStaleMarkerReactivatesTheSameBead(t *testing.T) {
	now := time.Date(2026, 9, 6, 5, 0, 4, 0, time.UTC)
	cityDir := t.TempDir()
	writeCityTOML(t, cityDir, "quarantine-town", "toolsmith")

	workDir := t.TempDir()
	marker := filepath.Join(workDir, worktreeStaleFileName)
	if err := os.WriteFile(marker, []byte("branch=fix/ci-tcvde5\nreason=uncommitted-work\n"), 0o644); err != nil {
		t.Fatalf("write stale worktree marker: %v", err)
	}

	cfg := &config.City{
		Workspace: config.Workspace{Name: "quarantine-town"},
		Session:   config.SessionConfig{Provider: "fake"},
		Agents: []config.Agent{{
			Name:              "toolsmith",
			Dir:               "repo",
			StartCommand:      "true",
			MinActiveSessions: intPtr(3),
			MaxActiveSessions: intPtr(3),
		}},
	}
	store := beads.NewMemStore()
	sp := runtime.NewFake()

	// Ready work routed at the pool, for the reason the sibling case above
	// carries it: with no demand the planner would not mint even against a
	// released bead, so the final assertion would hold for the wrong reason.
	createRoutedReadyBeadForReplacement(t, store, "repo/toolsmith", "queued toolsmith work")

	// Same derivation as the sibling case: the observation window comes from
	// the cadence the patrol actually runs at, never a picked sample count.
	const quarantineHold = time.Hour
	ticks := int(quarantineHold / cfg.Daemon.PatrolIntervalDuration())
	if ticks < 2 {
		t.Fatalf("observation window is %d ticks, want at least 2", ticks)
	}

	held := createCanonicalPoolSession(t, store, &cfg.Agents[0], now, 1)
	if err := store.SetMetadata(held.ID, "work_dir", workDir); err != nil {
		t.Fatalf("set work_dir: %v", err)
	}
	reloaded, err := store.Get(held.ID)
	if err != nil {
		t.Fatalf("reload held slot: %v", err)
	}
	quarantinePendingCreateForStaleWorktree(
		sessiontest.SeedBead(t, reloaded), sessionFrontDoor(store), workDir, now, quarantineHold, io.Discard,
	)
	heldAgent := held.Metadata["agent_name"]

	for _, slot := range []int{2, 3} {
		busy := createCanonicalPoolSession(t, store, &cfg.Agents[0], now, slot)
		setPoolSessionActive(t, store, busy.ID)
		live, err := store.Get(busy.ID)
		if err != nil {
			t.Fatalf("reload busy slot %d: %v", slot, err)
		}
		if err := sp.Start(context.Background(), live.Metadata["session_name"], runtime.Config{}); err != nil {
			t.Fatalf("start busy slot %d runtime: %v", slot, err)
		}
	}

	before, err := store.Get(held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Metadata["state"] != "quarantined" {
		t.Fatalf("precondition: state = %q, want quarantined", before.Metadata["state"])
	}
	if before.Metadata["quarantined_until"] == "" {
		t.Fatal("precondition: quarantined_until is empty, want the hold that the marker created")
	}
	if before.Metadata[worktreeStaleMarkerFingerprintKey] == "" {
		t.Fatalf("precondition: %s is empty, want the marker fingerprint", worktreeStaleMarkerFingerprintKey)
	}
	tokenBefore, genBefore := before.Metadata["instance_token"], before.Metadata["generation"]
	if tokenBefore == "" || genBefore == "" {
		t.Fatalf("precondition: instance_token=%q generation=%q, want both set -- they are the identity this case asserts on", tokenBefore, genBefore)
	}

	// THE OPERATOR'S ACTION, and the only input that changes.
	if err := os.Remove(marker); err != nil {
		t.Fatalf("remove stale worktree marker: %v", err)
	}

	next := clearResolvedStaleWorktreeQuarantineInfo(seedSessionInfo(before), sessionFrontDoor(store))

	after, err := store.Get(held.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct{ key, value string }{
		{"state", "asleep"},
		{"state_reason", "reactivated"},
		{"quarantined_until", ""},
		{"sleep_reason", ""},
		{worktreeStaleMarkerFingerprintKey, ""},
	} {
		if got := after.Metadata[want.key]; got != want.value {
			t.Errorf("after clearing the marker, %s = %q, want %q", want.key, got, want.value)
		}
	}
	if next.ID != held.ID {
		t.Errorf("returned Info.ID = %q, want the same bead %q", next.ID, held.ID)
	}

	// REACTIVATION IN PLACE, NOT REPLACEMENT. A new bead would pass every
	// state assertion above and still be the defect.
	if got := after.Metadata["instance_token"]; got != tokenBefore {
		t.Errorf("instance_token = %q, want %q unchanged: a new token is a replacement bead, which is the treadmill returning on a repaired slot", got, tokenBefore)
	}
	if got := after.Metadata["generation"]; got != genBefore {
		t.Errorf("generation = %q, want %q unchanged: a bumped generation is a replacement, not a reactivation", got, genBefore)
	}

	// AND THE PLANNER SETTLES. This is the observable consequence, and the
	// assertion is STABILITY rather than a count of one.
	//
	// THE COUNT IS DELIBERATELY NOT PINNED AT 1, and the reason is a design
	// choice one line of reusablePoolSessionInfo makes explicitly: an asleep
	// bead does NOT occupy its pool slot. A reactivated slot is asleep, so the
	// planner mints one live occupant beside it and the steady state is two
	// open beads for the slot -- measured here across the same window, 2 on
	// every tick. Asserting 1 would pin a behavior the code intentionally does
	// not have and would fail against a correct implementation.
	//
	// What ci-v1yc5x was about is GROWTH, and that is what this refuses: a
	// count that climbs tick after tick is the mint-refuse treadmill, and it
	// is invisible to any single-tick sample.
	openForSlot := func() int {
		t.Helper()
		all, err := store.List(beads.ListQuery{Type: sessionBeadType})
		if err != nil {
			t.Fatalf("list session beads: %v", err)
		}
		n := 0
		for _, b := range all {
			if b.Status != "closed" && b.Metadata["agent_name"] == heldAgent {
				n++
			}
		}
		return n
	}
	buildDesiredState("quarantine-town", cityDir, now, cfg, sp, store, io.Discard)
	settled := openForSlot()
	for tick := 2; tick <= ticks; tick++ {
		buildDesiredState("quarantine-town", cityDir, now, cfg, sp, store, io.Discard)
		if got := openForSlot(); got != settled {
			t.Fatalf("tick %d: open session beads for %s = %d, want %d unchanged: a count that grows after the marker is cleared is the ci-v1yc5x treadmill returning on a slot an operator had just repaired", tick, heldAgent, got, settled)
		}
	}

	// The reactivated bead itself must survive all of it, still the same one.
	final, err := store.Get(held.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status == "closed" {
		t.Error("the reactivated bead was closed by a later tick; the slot was replaced, not repaired")
	}
	if got := final.Metadata["instance_token"]; got != tokenBefore {
		t.Errorf("after %d ticks instance_token = %q, want %q unchanged", ticks, got, tokenBefore)
	}
}
