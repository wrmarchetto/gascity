package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: the last-line refusal in createPoolSessionBeadWithGuardedAlias -- a
// pool create must not be materialized at an (agent_name, slot) that another
// open session bead is still running under. It delegates which slot the
// allocator picks to pool_tick_slot_contention_test.go and what a refused
// ALIAS records to build_desired_state_alias_refusal_test.go.
//
// Why the suite exists: the slot number IS the worktree. work_dir is
// <worktrees>/<rig>/<AgentBase>, so two sessions at one slot share one git
// working tree, and the first `git switch` either runs moves the other's HEAD.
// Measured 2026-10-16 (ci-1tv1cr): ci-masvo2 was created at
// astoria-sel4/lab.engineer-3 while ci-ijiif9 was running there. The alias
// check DID fire -- it named the asleep ci-sv0fq7 -- and the create went ahead
// alias-less, because an alias refusal is a naming problem the create path is
// designed to survive. ci-ijiif9 itself held no alias, so no alias scan could
// ever see it. Only a scan on agent_name plus slot can.
//
// NOT represented: the runtime is runtime.NewFake, so "running" here is the
// fake's session table, not a tmux pane. The probe itself is
// poolSessionBeadRuntimeRunning, already trusted by the sweep to CLOSE beads.
//
// Run: go test ./cmd/gc/ -count=1 -run TestPoolSessionCreate

// seedSlotOccupant seeds an alias-less pool session at worker-1 / slot 1, the
// shape ci-ijiif9 had: its own alias reservation was refused at create, so it
// carries the identity in agent_name and pool_slot alone.
func seedSlotOccupant(t *testing.T, store beads.Store, sp runtime.Provider, agentName, slot string, running bool) beads.Bead {
	t.Helper()
	sessionName := "worker-ci-occupant-" + agentName + "-" + slot
	created, err := store.Create(beads.Bead{
		Title:  agentName,
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": sessionName,
			"agent_name":   agentName,
			"template":     "worker",
			"state":        "awake",
			"pool_slot":    slot,
			"pool_managed": "true",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(occupant): %v", err)
	}
	if running {
		if err := sp.Start(context.Background(), sessionName, runtime.Config{}); err != nil {
			t.Fatalf("starting occupant runtime: %v", err)
		}
	}
	return created
}

// TestPoolSessionCreateRefusesASlotARunningSessionOccupies replays the
// ci-masvo2 create: the alias is held by the slot's asleep predecessor, and an
// alias-less session is running at the same agent_name and slot. The create
// must fail and leave no bead behind -- a refused create that still wrote its
// bead would be launched by the next reconcile pass anyway.
func TestPoolSessionCreateRefusesASlotARunningSessionOccupies(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	outgoingPoolIncarnation(t, store, "asleep", "1")
	occupant := seedSlotOccupant(t, store, bp.sp, "worker-1", "1", true)
	before := countOpenSessionBeads(t, store)

	_, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if !errors.Is(err, errPoolSlotOccupied) {
		t.Fatalf("err = %v, want errPoolSlotOccupied: a second session was materialized in the worktree %s is running in", err, occupant.ID)
	}
	if !strings.Contains(err.Error(), occupant.ID) {
		t.Errorf("refusal %q does not name the occupant %s; the controller log line is the only trace of a refused create", err, occupant.ID)
	}
	if after := countOpenSessionBeads(t, store); after != before {
		t.Errorf("open session beads %d -> %d: the refused create still wrote a bead", before, after)
	}
}

// TestPoolSessionCreateProceedsPastANonOccupant is the over-correction guard.
// Each case differs from the refusal above in exactly one condition, so
// deleting that condition from the guard turns its case red. Without these the
// guard passes its mutation sweep as "refuse whenever any session bead exists".
func TestPoolSessionCreateProceedsPastANonOccupant(t *testing.T) {
	cases := []struct {
		name      string
		agentName string
		slot      string
		running   bool
		mutate    map[string]string
	}{
		// The awake-but-dead bead is a crash for the sweep to heal. Refusing
		// here would wedge the slot on a bead nothing is running under.
		{name: "not running", agentName: "worker-1", slot: "1", running: false},
		{name: "another slot", agentName: "worker-1", slot: "2", running: true},
		{name: "another identity", agentName: "other-1", slot: "1", running: true},
		{name: "not pool managed", agentName: "worker-1", slot: "1", running: true, mutate: map[string]string{"pool_managed": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
			occupant := seedSlotOccupant(t, store, bp.sp, tc.agentName, tc.slot, tc.running)
			if tc.mutate != nil {
				if err := store.Update(occupant.ID, beads.UpdateOpts{Metadata: tc.mutate}); err != nil {
					t.Fatalf("store.Update: %v", err)
				}
			}
			if _, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil); err != nil {
				t.Fatalf("create refused past %s (%s): %v", occupant.ID, tc.name, err)
			}
		})
	}
}

// TestPoolSessionCreateRefusesAClosedOccupantNever pins that a CLOSED bead is
// not an occupant even when its runtime is still up: the sweep closes beads
// whose sessions are gone, and a closed bead with a live pane is an orphan for
// the orphan reaper, not a reason to stop the slot from being refilled.
func TestPoolSessionCreateRefusesAClosedOccupantNever(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	occupant := seedSlotOccupant(t, store, bp.sp, "worker-1", "1", true)
	if err := store.Close(occupant.ID); err != nil {
		t.Fatalf("store.Close: %v", err)
	}
	if _, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil); err != nil {
		t.Fatalf("create refused past closed %s: %v", occupant.ID, err)
	}
}

func countOpenSessionBeads(t *testing.T, store beads.Store) int {
	t.Helper()
	all, err := loadSessionBeads(store)
	if err != nil {
		t.Fatalf("loadSessionBeads: %v", err)
	}
	return len(all)
}
