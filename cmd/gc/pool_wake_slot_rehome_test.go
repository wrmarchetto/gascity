package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Scope: the identity a wake-known-identity request recovers, from the tier
// that raises it (ComputePoolDesiredStates) through the allocator that turns
// it into a concrete slot (selectOrPlanPoolSessionBead). It delegates the
// claim side to cmd_hook_test.go -- what this suite pins is that the
// replacement session comes up WEARING the dead slot's name, which is the
// precondition every claim tier depends on.
//
// Why the suite exists: the wake tier is the only path back for in-progress
// work whose owning session died, and until ci-me7as9 it dropped the one
// thing that made the wake recoverable. The request named the template and
// the work bead but not the dead slot, so the allocator planned the lowest
// free slot: a bead stamped "worker-3" woke a session called "worker-1",
// whose work query (bd list --status in_progress --assignee=<own identity>,
// internal/config/workquery.go) never returns it and whose claim tiers
// (hookClaimExistingAssignment, hookCandidatePoolAlias in cmd_hook_claim.go)
// each refuse a stranger's in-progress bead. The session booted, got no_work
// and drained. Measured on the live city 2026-09-07..09-12: 51 such spawns,
// every one on a bead assigned to a SLOT name, lifetimes 15.4s to 381.8s
// against a 107.0s floor for every session that did claim.
//
// Run: go test ./cmd/gc/ -count=1 -run 'TestWake(ForDeadSlot|RequestCarries)'
func wakeRehomeBuildParams(t *testing.T, cfg *config.City) *agentBuildParams {
	t.Helper()
	now := time.Date(2026, 9, 1, 1, 30, 0, 0, time.UTC)
	return &agentBuildParams{
		city:                   cfg,
		cityName:               cfg.EffectiveCityName(),
		cityPath:               t.TempDir(),
		agents:                 cfg.Agents,
		beadStore:              beads.NewMemStore(),
		sessionBeads:           newSessionBeadSnapshotFromInfos(nil),
		beaconTime:             now,
		now:                    func() time.Time { return now },
		providerHealthSnapshot: &providerHealthSnapshot{},
	}
}

// TestWakeRequestCarriesTheDeadSlotIdentity pins that the wake tier hands the
// allocator the assignee it fired for.
//
// Asserted at the tier rather than only end to end because the two halves
// fail independently: an allocator that honors WakeInstance is inert if the
// tier never sets it, and that combination is green in every existing wake
// test -- they assert Tier and WorkBeadID and nothing about identity.
//
// The resume tier is asserted in the same breath as an absence: a resume
// request already names its concrete session through SessionBeadID, and
// setting WakeInstance there too would make the allocator prefer a slot
// NUMBER over the live session bead it was told to reuse.
func TestWakeRequestCarriesTheDeadSlotIdentity(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{poolAgent("claude", "rig", intPtr(3), 0)},
	}
	work := []beads.Bead{
		workBead("w1", "rig/claude", "rig/claude-3", "in_progress", 5),
	}
	closed := closedPoolSessionBead("sess-3")

	result := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads([]beads.Bead{closed}), nil)

	var wake *SessionRequest
	for i := range result {
		for j := range result[i].Requests {
			if result[i].Requests[j].Tier == "wake-known-identity" {
				wake = &result[i].Requests[j]
			}
		}
	}
	if wake == nil {
		t.Fatalf("no wake-known-identity request for dead slot work; got %#v", result)
	}
	if wake.WakeInstance != "rig/claude-3" {
		t.Errorf("WakeInstance = %q, want %q -- the wake must name the slot whose work it recovers, or the allocator cannot re-home it", wake.WakeInstance, "rig/claude-3")
	}
}

// TestWakeForDeadSlotPlansThatSlotNotTheLowestFree pins the allocator half:
// a fresh create raised by a wake takes the dead slot's own number.
//
// Every case shares one agent whose bound is 3 slots, so a fallback to the
// lowest free slot always reads as "worker-1" and a successful re-home
// always reads as the slot named in the request. The three fallback cases
// are over-correction guards, and each names a different reason the dead
// slot is unavailable: taken this tick, not a slot at all, and outside the
// configured bound. Without them a fix that simply always trusted the
// request's spelling would pass on the first case alone.
func TestWakeForDeadSlotPlansThatSlotNotTheLowestFree(t *testing.T) {
	agent := config.Agent{Name: "worker", MaxActiveSessions: intPtr(3)}
	cfg := &config.City{Workspace: config.Workspace{Name: "test-city"}, Agents: []config.Agent{agent}}

	tests := []struct {
		name         string
		wakeInstance string
		usedSlots    map[int]bool
		wantInstance string
	}{
		{
			// The defect. Slot 3's session died holding in-progress work; the
			// replacement must wear slot 3's name or no claim tier will offer
			// it the bead.
			name:         "dead slot is recovered by name",
			wakeInstance: "worker-3",
			usedSlots:    map[int]bool{},
			wantInstance: "worker-3",
		},
		{
			// A live session already reserved slot 3 this tick. Stealing it
			// would rename a running session's identity out from under its own
			// claimed work, which is strictly worse than the phantom.
			name:         "a slot taken this tick is never stolen",
			wakeInstance: "worker-3",
			usedSlots:    map[int]bool{3: true},
			wantInstance: "worker-1",
		},
		{
			// The bare pool door carries no slot number. Work parked there is
			// addressed to the pool, so any slot may serve it and the lowest
			// free one is correct.
			name:         "pool-door work keeps the lowest free slot",
			wakeInstance: "worker",
			usedSlots:    map[int]bool{},
			wantInstance: "worker-1",
		},
		{
			// A slot number the config can no longer produce -- max_active_sessions
			// was lowered under a bead still stamped with the old slot. Honoring
			// it would create a session outside the pool's own bound.
			name:         "a slot outside the configured bound falls back",
			wakeInstance: "worker-9",
			usedSlots:    map[int]bool{},
			wantInstance: "worker-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bp := wakeRehomeBuildParams(t, cfg)
			request := SessionRequest{
				Template:     "worker",
				Tier:         "wake-known-identity",
				WorkBeadID:   "w1",
				WakeInstance: tt.wakeInstance,
			}
			_, _, plan, err := selectOrPlanPoolSessionBead(
				bp, &agent, "worker", nil, request, map[string]bool{}, tt.usedSlots)
			if err != nil {
				t.Fatalf("selectOrPlanPoolSessionBead err = %v, want a create plan", err)
			}
			if plan == nil {
				t.Fatal("no create plan for a wake request")
			}
			if plan.qualifiedInstance != tt.wantInstance {
				t.Fatalf("planned instance = %q, want %q -- the replacement session's identity is what every claim tier joins on", plan.qualifiedInstance, tt.wantInstance)
			}
		})
	}
}

// TestWakeDoesNotRehomeOntoALiveWorkingSlot pins the second half of the
// re-home's refusal: a slot is unavailable because a live session is WEARING
// it, not only because an earlier request this tick reserved it.
//
// Separate from the table above because it needs a session snapshot, and the
// table's whole point is that it has none -- every case there is decided by the
// request and the config alone. The two refusals are also reachable
// independently: usedSlots is populated by this tick's ordering, the live set
// by the store, and a tick can present either without the other.
//
// The wake names slot 3 and the fixture puts a live, working session on slot 3.
// After the ownership fix such a bead resolves to its live session and becomes
// a resume rather than a wake, so this is defense in depth -- but it is the
// cheap half of a pair whose expensive half already cost a live session its
// request once.
func TestWakeDoesNotRehomeOntoALiveWorkingSlot(t *testing.T) {
	base := time.Date(2026, 9, 11, 4, 44, 0, 0, time.UTC)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents:    []config.Agent{poolAgent("worker", "rig", intPtr(3), 0)},
	}
	live := contentionSession(t, "ci-live3", "rig/worker-3", "3", base)
	work := []beads.Bead{contentionWork("wb-live", "rig/worker-3", "in_progress", -1)}

	bp := wakeRehomeBuildParams(t, cfg)
	bp.sessionBeads = newSessionBeadSnapshotFromInfos([]sessionpkg.Info{live})
	bp.assignedWorkBeads = work

	request := SessionRequest{
		Template:     "rig/worker",
		Tier:         "wake-known-identity",
		WorkBeadID:   "wb-other",
		WakeInstance: "rig/worker-3",
	}
	_, _, plan, err := selectOrPlanPoolSessionBead(
		bp, &cfg.Agents[0], "rig/worker", nil, request, map[string]bool{}, map[int]bool{})
	if err != nil {
		t.Fatalf("selectOrPlanPoolSessionBead err = %v, want a create plan", err)
	}
	if plan == nil {
		t.Fatal("no create plan for a wake request")
	}
	if plan.qualifiedInstance == "rig/worker-3" {
		t.Fatal("the wake re-homed onto a slot a live working session is wearing; that session's own resume then loses its slot and is dropped from the tick")
	}
}
