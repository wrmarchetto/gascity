package main

import (
	"context"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: whether the pool-create path may take a session alias from the bead
// that already holds it. It pins one exception -- a pool slot reclaiming the
// alias from its OWN outgoing incarnation -- and, in equal measure, the cases
// that must still be refused. It says nothing about what a refusal records:
// that is TestPoolSessionAliasRefusal* in the sibling file.
//
// The suite exists because the reconciler creates a slot's replacement session
// bead BEFORE the outgoing incarnation is closed, so for a few seconds one open
// bead holds the alias the replacement needs. The replacement was then created
// alias-less, and because the launch env is frozen from the bead at create time
// the session claimed under its session-name spelling for the rest of its life
// -- unresolvable to any consumer joining on the agent name. Measured in the
// city 2026-09-06: ci-k1bkmt was created 17:10:21Z while ci-aqhncx (same
// agent_name toolsmith-1, same pool_slot 1, state=city-stop) was still open,
// and ci-aqhncx closed at 17:10:22Z -- one second late. The pane launched at
// 17:10:23Z with no GC_ALIAS in its environ and GC_AGENT=BEADS_ACTOR=
// toolsmith-ci-k1bkmt, and still had none after the alias was back-filled onto
// the bead.
//
// The exception is dangerous in exactly one direction -- hand an alias to a
// second LIVE session and two processes claim as one agent -- so every test
// here that asserts a handover has a partner asserting the refusal it must not
// generalize into.
//
// Run: go test ./cmd/gc/ -run TestPoolSessionAliasHandover

// outgoingPoolIncarnation seeds this slot's own retired-but-not-yet-closed
// incarnation: same agent_name, same pool_slot, pool_managed, and a state the
// reconciler only writes on the way out. state is "city-stop" rather than
// "asleep" because that is the value measured on ci-aqhncx, and it is NOT one
// of the session.State constants -- the handover must therefore be keyed on
// which states are LIVE, never on an allowlist of retiring ones.
func outgoingPoolIncarnation(t *testing.T, store beads.Store, state, slot string) beads.Bead {
	t.Helper()
	const (
		agentName   = "worker-1"
		sessionName = "worker-ci-predecessor"
	)
	created, err := store.Create(beads.Bead{
		Title:  agentName,
		Type:   session.BeadType,
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": sessionName,
			"alias":        agentName,
			"agent_name":   agentName,
			"template":     "worker",
			"state":        state,
			"pool_slot":    slot,
			"pool_managed": "true",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(outgoing incarnation): %v", err)
	}
	return created
}

// TestPoolSessionAliasHandoverReclaimsFromOwnOutgoingIncarnation is the
// regression. The replacement must launch holding the alias, because the alias
// it holds at create time is the one its process environment is frozen with.
func TestPoolSessionAliasHandoverReclaimsFromOwnOutgoingIncarnation(t *testing.T) {
	bp, cfg, store, stderr := poolSessionAliasRefusalFixture(t)
	predecessor := outgoingPoolIncarnation(t, store, "city-stop", "1")

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "worker-1" {
		t.Fatalf("alias = %q, want %q: the replacement did not reclaim the alias from %s", info.Alias, "worker-1", predecessor.ID)
	}
	stored, err := store.Get(info.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", info.ID, err)
	}
	if got := stored.Metadata[aliasReservationRefusedMetadataKey]; got != "" {
		t.Errorf("%s = %q on a session that reclaimed its alias, want absent", aliasReservationRefusedMetadataKey, got)
	}
	if !strings.Contains(stderr.String(), predecessor.ID) {
		t.Errorf("stderr does not name the superseded holder %s; the handover is the one branch here with no durable record, so the log line is its only trace:\n%s", predecessor.ID, stderr.String())
	}
}

// TestPoolSessionAliasHandoverRefusesWhileTheHolderStillRuns is the partner
// assertion: the runtime probe, not the metadata state, is what proves nobody
// is still claiming under the alias. A bead can say it is on the way out while
// its pane is very much alive.
func TestPoolSessionAliasHandoverRefusesWhileTheHolderStillRuns(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	outgoingPoolIncarnation(t, store, "city-stop", "1")
	if err := bp.sp.Start(context.Background(), "worker-ci-predecessor", runtime.Config{}); err != nil {
		t.Fatalf("seeding a running runtime session: %v", err)
	}

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "" {
		t.Fatalf("alias = %q, want empty: the alias was taken from a holder whose runtime is still up", info.Alias)
	}
}

// TestPoolSessionAliasHandoverRefusesALiveStateHolder pins the second half of
// the two-condition rule. A holder the reconciler still calls awake keeps its
// alias even when its runtime has vanished: that shape is a crash for the sweep
// to heal, and declining is exactly the pre-fix behavior, so nothing regresses.
func TestPoolSessionAliasHandoverRefusesALiveStateHolder(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	outgoingPoolIncarnation(t, store, "awake", "1")

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "" {
		t.Fatalf("alias = %q, want empty: state=awake must still block the reservation", info.Alias)
	}
}

// TestPoolSessionAliasHandoverRefusesAnotherSlotsHolder pins that the exception
// is scoped to ONE pool identity. Without the slot comparison the predicate
// degrades into "any dormant pool bead may be stripped of its alias", which
// would quietly rename a slot that is merely asleep between assignments.
func TestPoolSessionAliasHandoverRefusesAnotherSlotsHolder(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	outgoingPoolIncarnation(t, store, "asleep", "2")

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "" {
		t.Fatalf("alias = %q, want empty: slot 2's bead is not slot 1's outgoing incarnation", info.Alias)
	}
}

// TestPoolSessionAliasHandoverRefusesANonPoolHolder pins the pool_managed
// condition. A manual or named session holding the alias is somebody else's
// identity, and the reconciler has no standing to move it.
func TestPoolSessionAliasHandoverRefusesANonPoolHolder(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	holder := outgoingPoolIncarnation(t, store, "asleep", "1")
	if err := store.Update(holder.ID, beads.UpdateOpts{Metadata: map[string]string{"pool_managed": ""}}); err != nil {
		t.Fatalf("store.Update(clear pool_managed): %v", err)
	}

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "" {
		t.Fatalf("alias = %q, want empty: a non-pool holder must keep its alias", info.Alias)
	}
}

// TestPoolSessionAliasHandoverRefusesAHolderOfAnotherIdentity pins the
// agent_name condition, which is the one the other four fixtures cannot reach:
// they all agree on the identity and differ elsewhere, so the predicate passes
// its mutation with the comparison deleted. The shape here is a dormant
// pool bead of a DIFFERENT identity that merely persisted this alias -- exactly
// what the alias-only test would wave through -- and taking its alias renames
// a slot that is only asleep between assignments.
func TestPoolSessionAliasHandoverRefusesAHolderOfAnotherIdentity(t *testing.T) {
	bp, cfg, store, _ := poolSessionAliasRefusalFixture(t)
	holder := outgoingPoolIncarnation(t, store, "asleep", "1")
	if err := store.Update(holder.ID, beads.UpdateOpts{Metadata: map[string]string{"agent_name": "other-1"}}); err != nil {
		t.Fatalf("store.Update(agent_name): %v", err)
	}

	info, err := createPoolSessionBeadWithGuardedAlias(bp, &cfg.Agents[0], "worker", "worker-1", 1, nil)
	if err != nil {
		t.Fatalf("createPoolSessionBeadWithGuardedAlias: %v", err)
	}
	if info.Alias != "" {
		t.Fatalf("alias = %q, want empty: %s holds this alias but is not this identity", info.Alias, holder.ID)
	}
}
