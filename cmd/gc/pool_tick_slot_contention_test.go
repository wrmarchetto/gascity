package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// Scope: one reconciler tick's worth of slot contention -- which session each
// request in a tick ends up bound to, when a fresh create and a live session's
// resume want the same slot number. It covers computePoolDesiredStates' request
// ORDER and the Phase A selection loop that consumes it. It delegates the
// ownership predicate to pool_session_work_ownership_test.go and the wake
// re-home to pool_wake_slot_rehome_test.go; what it adds is the interaction,
// which neither can represent because each drives a single request.
//
// Why the suite exists: a suite of single-request cases goes green over this
// entire class. Every request below is individually correct -- the wake is
// entitled to a session, each resume names its own -- and the defect is only in
// what the first one leaves for the others. The fixture is a REPLAY of a
// measured tick rather than a constructed scenario, because the ordering that
// produces it was not one a reading of applyNestedCaps predicted: the
// in-progress beads arrive from the cache tier with Priority UNSET while the
// open pool-door bead carries its live value, and the BeadPriority-DESC sort
// then puts a P1 wake ahead of two P0 resumes.
//
// NOT represented here, and named so the gap is not mistaken for coverage: the
// claim side. These tests assert which session each request binds to, never
// that the bound session's `gc hook --claim` then succeeds -- that needs the
// work query and a bd store, and lives in cmd_hook_test.go.
//
// Run: go test ./cmd/gc/ -count=1 -run TestFreshCreate
func contentionSession(t *testing.T, id, alias, slot string, created time.Time) sessionpkg.Info {
	t.Helper()
	return sessiontest.SeedBead(t, beads.Bead{
		ID:        id,
		Type:      sessionpkg.BeadType,
		Status:    "open",
		Labels:    []string{sessionpkg.LabelSession},
		CreatedAt: created,
		Metadata: map[string]string{
			"template": "rig/worker", "session_name": "gc-" + id,
			"pool_managed": "true", "pool_slot": slot, "alias": alias, "state": "awake",
		},
	})
}

// contentionWork builds a work bead. A negative priority leaves Priority NIL,
// which is how the cache tier returns an in-progress bead -- and the
// difference is load-bearing here rather than cosmetic, because beadPriority
// reads it raw and the request sort is keyed on it.
func contentionWork(id, assignee, status string, priority int) beads.Bead {
	bead := beads.Bead{
		ID: id, Status: status, Assignee: assignee,
		Metadata: map[string]string{"gc.routed_to": "rig/worker"},
	}
	if priority >= 0 {
		p := priority
		bead.Priority = &p
	}
	return bead
}

// planOneTick runs computePoolDesiredStates and then the Phase A selection
// loop of realizePoolDesiredSessions over its requests, in order, sharing one
// used/usedSlots pair the way the production loop does. It returns work bead id
// -> outcome, where an outcome is either "reuse <session id>", "create <slot
// instance>", or "error: <message>".
//
// Phase A is reproduced here rather than calling realizePoolDesiredSessions
// because that function also materializes store writes, resolves tmux aliases
// and installs side effects -- none of which decide the binding, and all of
// which would need fixtures that obscure what this test is about.
func planOneTick(t *testing.T, cfg *config.City, sessions []sessionpkg.Info, work []beads.Bead) map[string]string {
	t.Helper()
	now := time.Date(2026, 9, 11, 4, 47, 49, 0, time.UTC)
	bp := &agentBuildParams{
		city:                   cfg,
		cityName:               cfg.EffectiveCityName(),
		cityPath:               t.TempDir(),
		agents:                 cfg.Agents,
		beadStore:              beads.NewMemStore(),
		sessionBeads:           newSessionBeadSnapshotFromInfos(sessions),
		assignedWorkBeads:      work,
		beaconTime:             now,
		now:                    func() time.Time { return now },
		providerHealthSnapshot: &providerHealthSnapshot{},
	}
	used := map[string]bool{}
	usedSlots := map[int]bool{}
	outcome := map[string]string{}
	for _, state := range ComputePoolDesiredStates(cfg, work, sessions, nil) {
		for _, request := range state.Requests {
			var prefer *sessionpkg.Info
			if request.SessionBeadID != "" {
				if candidate, ok := bp.sessionBeads.FindInfoByID(request.SessionBeadID); ok {
					prefer = &candidate
				}
			}
			info, _, plan, err := selectOrPlanPoolSessionBead(
				bp, &cfg.Agents[0], "rig/worker", prefer, request, used, usedSlots)
			switch {
			case err != nil:
				outcome[request.WorkBeadID] = "error: " + err.Error()
			case plan != nil:
				outcome[request.WorkBeadID] = "create " + plan.qualifiedInstance
			default:
				used[info.ID] = true
				outcome[request.WorkBeadID] = "reuse " + info.ID
			}
		}
	}
	return outcome
}

// TestFreshCreatePlannedFirstLeavesWorkingSlotsAlone replays the tick measured
// on the live city at 2026-09-11 04:47:49 and pins that every session doing
// work keeps its own slot.
//
// The three assertions are one invariant seen from three sides, and dropping
// any one of them leaves a failure mode green:
//
//   - ci-ekvagt and ci-1lyxjz must each reuse THEIR OWN session. Before the
//     ownership fix, ci-qser4e's wake found both live sessions reusable --
//     sessionBeadHasAssignedWorkInfo did not read the alias their work is
//     assigned under -- took the oldest, and displaced the chain: ci-ekvagt
//     landed on ci-a0zqsl and ci-1lyxjz fell through to a fresh create stamped
//     with a bead that create could never claim. That is the phantom.
//   - ci-qser4e must be a CREATE, not a reuse. It is pool-door work with no
//     session of its own, so the wake is correct; asserting only that the
//     resumes are right would pass a fix that simply dropped the wake.
//   - No outcome may be an error. With the ownership fix alone the create is
//     planned first onto slot 1 -- the lowest FREE slot, free only because
//     usedSlots records this tick's earlier requests and a live session that
//     has not been planned yet has reserved nothing. ci-mmlieb's own resume
//     then fails claimPreferredPoolSlotWithConfigInfo and is dropped with
//     "concrete slot already claimed": a session doing work removed from the
//     desired state to seat one that will find none. Measured, not predicted;
//     it is why poolSlotsHeldByWorkingSessionsInfo exists.
func TestFreshCreatePlannedFirstLeavesWorkingSlotsAlone(t *testing.T) {
	base := time.Date(2026, 9, 11, 4, 44, 0, 0, time.UTC)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{poolAgent("worker", "rig", intPtr(3), 0)},
	}
	sessions := []sessionpkg.Info{
		contentionSession(t, "ci-mmlieb", "rig/worker-1", "1", base.Add(24*time.Second)),
		contentionSession(t, "ci-a0zqsl", "rig/worker-2", "2", base.Add(37*time.Second)),
	}
	work := []beads.Bead{
		contentionWork("ci-ekvagt", "rig/worker-1", "in_progress", -1),
		contentionWork("ci-1lyxjz", "rig/worker-2", "in_progress", -1),
		contentionWork("ci-qser4e", "rig/worker", "open", 1),
	}

	got := planOneTick(t, cfg, sessions, work)

	want := map[string]string{
		"ci-ekvagt": "reuse ci-mmlieb",
		"ci-1lyxjz": "reuse ci-a0zqsl",
		"ci-qser4e": "create rig/worker-3",
	}
	for id, expected := range want {
		if got[id] != expected {
			t.Errorf("%s bound to %q, want %q", id, got[id], expected)
		}
	}
}

// TestFreshCreateStillTakesADrainedSlotsNumber is the over-correction guard on
// the slot reservation, and it is the reason that reservation is scoped to
// sessions holding WORK rather than to every open session.
//
// A drained session is precisely what a fresh create replaces. Reserving its
// number in a pool already at max_active_sessions would send the allocator's
// unbounded slot loop past the configured cap and mint "rig/worker-4" in a
// three-slot pool -- a create outside the pool's own bound, which no other
// test here would notice.
//
// The drained session is given work assigned to its alias deliberately: that is
// the shape the reservation keys on, so a drained session with no work would
// pass whether the check existed or not. The pool is deliberately FULL -- both
// its slots accounted for, one drained and one live -- because that is the only
// configuration where reserving the drained number has anywhere to go, and
// where it goes is slot 3 in a two-slot pool.
//
// max_active_sessions is 2 rather than 1: at 1 the agent uses the canonical
// singleton identity, every slot resolves to 0, and the test would pin nothing.
func TestFreshCreateStillTakesADrainedSlotsNumber(t *testing.T) {
	base := time.Date(2026, 9, 11, 4, 44, 0, 0, time.UTC)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{poolAgent("worker", "rig", intPtr(2), 0)},
	}
	drained := contentionSession(t, "ci-drained", "rig/worker-1", "1", base)
	drained.MetadataState = "drained"
	live := contentionSession(t, "ci-live", "rig/worker-2", "2", base)

	work := []beads.Bead{
		contentionWork("wb-orphan", "rig/worker-1", "in_progress", -1),
		contentionWork("wb-live", "rig/worker-2", "in_progress", -1),
	}

	bp := &agentBuildParams{
		city:                   cfg,
		cityName:               cfg.EffectiveCityName(),
		cityPath:               t.TempDir(),
		agents:                 cfg.Agents,
		beadStore:              beads.NewMemStore(),
		sessionBeads:           newSessionBeadSnapshotFromInfos([]sessionpkg.Info{drained, live}),
		assignedWorkBeads:      work,
		beaconTime:             base,
		now:                    func() time.Time { return base },
		providerHealthSnapshot: &providerHealthSnapshot{},
	}
	held := poolSlotsHeldByWorkingSessionsInfo(bp, &cfg.Agents[0])
	if held[1] {
		t.Fatal("a drained session reserved its slot; with the pool full the create is pushed to rig/worker-3, past max_active_sessions")
	}
	if !held[2] {
		t.Fatal("the live working session did not reserve its slot; this fixture no longer distinguishes the drained arm from an empty reservation")
	}
	slot := claimFreshCreatePoolSlotInfo(bp, &cfg.Agents[0], map[int]bool{})
	if slot != 1 {
		t.Errorf("fresh create took slot %d, want 1 -- the drained session's number is exactly what a replacement is meant to reuse", slot)
	}
}

// TestFreshCreateStillTakesAnIdleAsleepSlotsNumber pins the OTHER edge of the
// reservation's scope: it must not widen to every live session.
//
// An asleep ephemeral is not restarted -- reusablePoolSessionInfo excludes it
// so a fresh session is created in its place -- and it holds no work, so the
// ownership condition is the only thing keeping its number available. Drop that
// condition and this full two-slot pool has both numbers reserved, sending the
// allocator's unbounded loop to slot 3.
//
// This case exists because a mutation sweep found the drained case could not
// see the difference: isDrainedSessionInfo already excludes the drained
// session whether the ownership condition is there or not, so
// reserve-every-live-slot SURVIVED against that test alone. A plain asleep
// session (SleepReason not "drained") is the shape only the ownership
// condition excludes.
func TestFreshCreateStillTakesAnIdleAsleepSlotsNumber(t *testing.T) {
	base := time.Date(2026, 9, 11, 4, 44, 0, 0, time.UTC)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{poolAgent("worker", "rig", intPtr(2), 0)},
	}
	asleep := contentionSession(t, "ci-asleep", "rig/worker-1", "1", base)
	asleep.MetadataState = "asleep"
	live := contentionSession(t, "ci-live", "rig/worker-2", "2", base)

	bp := &agentBuildParams{
		city:                   cfg,
		cityName:               cfg.EffectiveCityName(),
		cityPath:               t.TempDir(),
		agents:                 cfg.Agents,
		beadStore:              beads.NewMemStore(),
		sessionBeads:           newSessionBeadSnapshotFromInfos([]sessionpkg.Info{asleep, live}),
		assignedWorkBeads:      []beads.Bead{contentionWork("wb-live", "rig/worker-2", "in_progress", -1)},
		beaconTime:             base,
		now:                    func() time.Time { return base },
		providerHealthSnapshot: &providerHealthSnapshot{},
	}
	held := poolSlotsHeldByWorkingSessionsInfo(bp, &cfg.Agents[0])
	if held[1] {
		t.Fatal("an idle asleep session reserved its slot; with the pool full the replacement is pushed to rig/worker-3, past max_active_sessions")
	}
	if !held[2] {
		t.Fatal("the live working session did not reserve its slot; this fixture no longer distinguishes the idle arm from an empty reservation")
	}
	if slot := claimFreshCreatePoolSlotInfo(bp, &cfg.Agents[0], map[int]bool{}); slot != 1 {
		t.Errorf("fresh create took slot %d, want 1 -- an asleep ephemeral is replaced in place, not worked around", slot)
	}
}
