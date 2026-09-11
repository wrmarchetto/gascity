// Scope: whether a session LEFT in state_reason=drain-ack-assigned-work ever
// leaves it. Entering that state is already pinned by
// session_reconciler_test.go and cmd_runtime_drain_test.go; this file asserts
// the other direction, which ci-07ebae found nothing anywhere asserted. The
// defer-streak backstop's own mechanics (per-anchor reset, limit resolution)
// belong to TestReconcileSessionBeads_AssignedWorkDeferBackstop* and are not
// re-tested here.
//
// Why this suite exists: the reconciler refuses a drain-ack issued while the
// session still holds assigned work, clears the ack and writes the state
// reason (session_reconciler.go). The session stays active and is meant to
// carry on working -- but it was never told, so it ends its turn believing it
// retired. The Stop gate cannot hold it, the pool claim backstop skips it
// because its bead IS claimed, and the idle reaper is the only thing left.
// Measured 2026-09-11: three sessions held slots for 86, 154 and 202 minutes.
//
// Why the unarmed arm is here and is not redundant: it is the negative
// control, and it is the whole finding. Without it the armed arm proves only
// that a reaper reaps, which its own suite already showed. Together they
// establish the causal claim this bead acts on -- that the wedge has no exit
// UNTIL a timeout is configured, and that configuring one is what supplies
// the exit.
//
// Run: go test ./cmd/gc/ -run TestReconcileSessionBeads_DrainAckAssignedWorkWedge

package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// drainAckWedgeEnv builds the state the refusal LEAVES BEHIND, not the
// refusal itself: an alive, active session carrying the refused-ack state
// reason, holding an in-progress bead assigned to it, with no drain marker
// pending (the refusal path calls clearDrain before writing the reason). The
// anchor bead is set because the defer streak is keyed on it -- a session
// with no anchor would reset its own streak every tick and could never
// exhaust, which would make the armed arm below pass for the wrong reason.
func drainAckWedgeEnv(t *testing.T) (*reconcilerTestEnv, beads.Bead) {
	t.Helper()
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "witness"}}}
	env.addDesired("witness", "witness", true)
	session := env.createSessionBead("witness", "witness")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		"state_reason":                 sessionpkg.DrainAckAssignedWorkReason,
		"currently_processing_bead_id": "ga-wedged",
	})
	if err := env.sp.SetMeta("witness", "GC_SESSION_ID", session.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}
	if _, err := env.store.Create(beads.Bead{
		Title:    "the work the refusal was about",
		Type:     "task",
		Status:   "in_progress",
		Assignee: session.ID,
	}); err != nil {
		t.Fatalf("Create(assigned work): %v", err)
	}
	return env, session
}

func drainAckWedgeTick(env *reconcilerTestEnv, session beads.Bead, it idleTracker, tr assignedWorkDeferTracker) (*sessionReconcilerTraceCycle, *events.Fake) {
	rec := events.NewFake()
	trace := idleTimeoutBackstopTrace("witness")
	poolDesired := make(map[string]int)
	for _, tp := range env.desiredState {
		if tp.TemplateName != "" {
			poolDesired[tp.TemplateName]++
		}
	}
	opts := []startExecutionOption{}
	if tr != nil {
		opts = append(opts, withAssignedWorkDeferTracker(tr))
	}
	reconcileSessionBeadsTraced(
		context.Background(), "", []beads.Bead{session}, env.desiredState,
		configuredSessionNames(env.cfg, "", env.store), env.cfg, env.sp,
		env.store, nil, nil, nil, nil, env.dt, poolDesired, false, nil, "",
		it, env.clk, rec, 0, 0, &env.stdout, &env.stderr, trace,
		opts...,
	)
	return trace, rec
}

// The live defect, stated as an assertion: with no idle_timeout configured
// anywhere, buildIdleTracker returns nil, the reconciler's whole idle block
// is gated on `it != nil`, and the wedged session is never reaped however
// long it sits. Ten ticks is not a proof of "forever" -- nothing in a unit
// test can be -- but the reaper is the ONLY caller that could end this state,
// and it is unreachable here, so the count only has to exceed the defer limit
// it would have exhausted at (3) to show the bound is absent rather than
// merely slow.
func TestReconcileSessionBeads_DrainAckAssignedWorkWedgeHasNoExitUnarmed(t *testing.T) {
	env, session := drainAckWedgeEnv(t)

	for i := range 10 {
		_, rec := drainAckWedgeTick(env, session, nil, newAssignedWorkDeferTracker())
		if idleTimeoutBackstopKilled(rec) {
			t.Fatalf("tick %d: session was reaped with no idle_timeout configured; "+
				"the unarmed wedge is supposed to have no exit at all", i+1)
		}
	}

	b, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Metadata["state_reason"]; got != sessionpkg.DrainAckAssignedWorkReason {
		t.Errorf("state_reason = %q, want it still %q -- nothing unarmed transitions out of it",
			got, sessionpkg.DrainAckAssignedWorkReason)
	}
	if got := b.Metadata["sleep_reason"]; got != "" {
		t.Errorf("sleep_reason = %q, want unset -- the session was never stopped", got)
	}
}

// The exit, once a timeout is armed. The idle tracker standing in for a
// configured idle_timeout reports the session idle; DecideIdleTimeout defers
// while it holds assigned work, and the consecutive-defer streak on the
// unchanged anchor converts that defer into a forced stop. This is the
// transition out of drain-ack-assigned-work that ci-07ebae found missing --
// note it arrives as a STOP, not as a repair: the session dies and the bead
// stays assigned for the next occupant, which is the outcome being chosen
// here, not an accident of the mechanism.
func TestReconcileSessionBeads_DrainAckAssignedWorkWedgeExitsWhenArmed(t *testing.T) {
	env, session := drainAckWedgeEnv(t)

	it := newFakeIdleTracker()
	it.idle["witness"] = true
	tr := newAssignedWorkDeferTracker()
	tr.setLimit("witness", 2)

	for i, wantKill := range []bool{false, false, true} {
		trace, rec := drainAckWedgeTick(env, session, it, tr)
		if got := idleTimeoutBackstopKilled(rec); got != wantKill {
			t.Fatalf("tick %d: killed = %v, want %v", i+1, got, wantKill)
		}
		if !wantKill {
			continue
		}
		if !idleTimeoutBackstopTraceHasDecision(trace, TraceReasonAssignedWorkExhausted, TraceOutcomeStopDeferExhausted) {
			t.Fatalf("tick %d: stop recorded no AssignedWorkExhausted decision, so the wedge "+
				"ended under some other rule and this test would not notice the backstop being removed", i+1)
		}
		b, err := env.store.Get(session.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got := b.Metadata["sleep_reason"]; got != string(sessionpkg.SleepReasonAssignedWorkExhausted) {
			t.Errorf("sleep_reason = %q, want %q", got, sessionpkg.SleepReasonAssignedWorkExhausted)
		}
	}
}
