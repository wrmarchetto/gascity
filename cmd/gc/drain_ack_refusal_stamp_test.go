// Scope: the instant the controller records when it REFUSES a drain
// acknowledgement. Whether the refusal happens at all, and which work shapes
// provoke it, belong to session_reconciler_test.go's
// TestReconcileSessionBeads_AgentDrainAck* family and to the drain_ack_*
// suites; this file asserts only that the refusal is dated.
//
// Why this suite exists: state_reason=drain-ack-assigned-work is a state with
// no exit of its own (drain_ack_wedge_exit_test.go), so a session that enters
// it stays in it until something stops the session. The reason therefore says
// nothing about HOW LONG, and nothing else in the tree did either. Measured
// 2026-09-21 (ci-amflbh): a session sat in this state for twelve hours holding
// a P1 claim, and reconstructing when it started meant reading the session's
// own Claude Code transcript by hand, because the session bead's updated_at
// moves on every reconciler write and the event that names the refusal lives
// in a rotating log. The stamp is what lets a doctor check, a dashboard or an
// operator subtract two numbers instead.
//
// Run: go test ./cmd/gc/ -run TestReconcileSessionBeads_RefusedDrainAck

package main

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// refusedDrainAckTick drives one reconciler tick over a live session that has
// acknowledged drain while still holding an in-progress claim, and returns the
// session bead the tick left behind.
func refusedDrainAckTick(t *testing.T) (beads.Bead, time.Time) {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	if _, err := env.store.Create(beads.Bead{
		Title:    "the work the acknowledgement was refused over",
		Type:     "task",
		Status:   "in_progress",
		Assignee: session.ID,
	}); err != nil {
		t.Fatalf("Create(assigned work): %v", err)
	}
	dops := newFakeDrainOps()
	if err := dops.setDrainAck("worker"); err != nil {
		t.Fatalf("setDrainAck: %v", err)
	}

	reconcileSessionBeads(
		context.Background(), []beads.Bead{session}, env.desiredState,
		map[string]bool{"worker": true}, env.cfg, env.sp, env.store, dops,
		nil, nil, env.dt, nil, false, nil, "", nil, env.clk, env.rec, 0, 0,
		&env.stdout, &env.stderr,
	)

	got, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("Get session after the refusal: %v", err)
	}
	if got.Metadata["state_reason"] != sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q, want %q -- the fixture must actually provoke a refusal, or the stamp assertion below proves nothing",
			got.Metadata["state_reason"], sessionpkg.DrainAckAssignedWorkReason)
	}
	return got, env.clk.Now().UTC()
}

func TestReconcileSessionBeads_RefusedDrainAckIsDated(t *testing.T) {
	got, tickNow := refusedDrainAckTick(t)

	raw := got.Metadata[sessionpkg.DrainAckRefusedAtMetadataKey]
	if raw == "" {
		t.Fatalf("%s is unset; the refusal is recorded with no date, so nothing downstream can say how long the session has been wedged",
			sessionpkg.DrainAckRefusedAtMetadataKey)
	}
	stamped, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("%s = %q, which does not parse as RFC3339: %v", sessionpkg.DrainAckRefusedAtMetadataKey, raw, err)
	}
	// Equality against the tick's OWN clock, not a window around the wall
	// clock. The reconciler runs on an injected clock that every other dated
	// decision in the tick reads, and this suite's fixture pins it years away
	// from now -- so a stamp taken from time.Now() would sit inside any
	// wall-clock window and outside this assertion. That is the mistake worth
	// pinning: a refusal dated on a different clock from the tick that made it
	// cannot be compared against anything else the tick wrote.
	if !stamped.Equal(tickNow) {
		t.Fatalf("%s = %s, want the tick's own clock %s -- a stamp from time.Now() disagrees with every other instant in the same tick",
			sessionpkg.DrainAckRefusedAtMetadataKey, stamped, tickNow)
	}
}
