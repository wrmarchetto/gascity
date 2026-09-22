package session

import "testing"

// Scope: the idle-timeout ladder's reporting of WHICH liveness signal fired.
// The ladder's precedence itself is pinned in lifecycle_timers_test.go; this
// file pins only that a stall-triggered stop is distinguishable in the trace
// from a pane-idle one, and that the distinction does not leak into any other
// rung.
//
// Why the suite exists: ci-jvbkio was diagnosed by reading
// reconciler.session.idle_timeout decision records, and the whole diagnosis
// turned on which arm had -- and had not -- fired. A stall stop that records
// "idle_timeout" is indistinguishable from the pane arm in exactly the
// artifact an operator reaches for.
//
//	go test ./internal/session/ -run TestIdleTimeoutTrigger

// TestIdleTimeoutTriggerStallNamesItselfInTheTrace pins the distinct trace
// reason for a stop reached via the transcript arm.
func TestIdleTimeoutTriggerStallNamesItselfInTheTrace(t *testing.T) {
	t.Parallel()

	dec := DecideIdleTimeout(TimerFacts{
		Triggered:    true,
		Trigger:      TimerTriggerStall,
		Pending:      PendingNo,
		AssignedWork: AssignedWorkNone,
	})
	if dec.Action != TimerActionStop {
		t.Fatalf("Action = %v, want TimerActionStop", dec.Action)
	}
	if dec.TraceReason != "stall_timeout" {
		t.Fatalf("TraceReason = %q, want %q", dec.TraceReason, "stall_timeout")
	}
}

// TestIdleTimeoutTriggerDefaultStaysIdleTimeout pins that the zero Trigger
// keeps the pre-existing vocabulary. Every caller that does not set the field
// -- and every stored trace record written before it existed -- must keep
// reading "idle_timeout", or the change silently reclassifies history.
func TestIdleTimeoutTriggerDefaultStaysIdleTimeout(t *testing.T) {
	t.Parallel()

	dec := DecideIdleTimeout(TimerFacts{
		Triggered:    true,
		Pending:      PendingNo,
		AssignedWork: AssignedWorkNone,
	})
	if dec.TraceReason != "idle_timeout" {
		t.Fatalf("TraceReason = %q, want %q for an unset Trigger", dec.TraceReason, "idle_timeout")
	}
}

// TestIdleTimeoutTriggerStallKeepsTheIdleSleepReason pins a deliberate
// ABSENCE: there is no SleepReasonStallTimeout. A stall stop is an idle kill
// for every downstream consumer -- churn accounting, continuation reset,
// IsDeliberateSleepReason -- and minting a second reason would silently opt
// stall kills out of each of those sets, which is a behavior change nobody
// asked for. The trace reason carries the distinction instead.
func TestIdleTimeoutTriggerStallKeepsTheIdleSleepReason(t *testing.T) {
	t.Parallel()

	stall := DecideIdleTimeout(TimerFacts{
		Triggered: true, Trigger: TimerTriggerStall,
		Pending: PendingNo, AssignedWork: AssignedWorkNone,
	})
	idle := DecideIdleTimeout(TimerFacts{
		Triggered: true, Trigger: TimerTriggerIdle,
		Pending: PendingNo, AssignedWork: AssignedWorkNone,
	})
	if stall.SleepReason != idle.SleepReason {
		t.Fatalf("stall SleepReason = %q, idle SleepReason = %q; want identical", stall.SleepReason, idle.SleepReason)
	}
	if stall.SleepReason != string(SleepReasonIdleTimeout) {
		t.Fatalf("SleepReason = %q, want %q", stall.SleepReason, SleepReasonIdleTimeout)
	}
}

// TestIdleTimeoutTriggerStallStillDefersOnEveryRung pins that the new trigger
// source changes only the terminal vocabulary. A stall must not bypass the
// blocker, pending-interaction or assigned-work rungs -- those exist because
// a session can be legitimately quiet, and the stall arm is the arm most
// likely to fire on a long-running tool call.
func TestIdleTimeoutTriggerStallStillDefersOnEveryRung(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		facts TimerFacts
		want  string
	}{
		{"blocker", TimerFacts{Triggered: true, Trigger: TimerTriggerStall, Blocker: "user_hold"}, "user_hold"},
		{"pending", TimerFacts{Triggered: true, Trigger: TimerTriggerStall, Pending: PendingYes}, "pending"},
		{"assigned_work", TimerFacts{Triggered: true, Trigger: TimerTriggerStall, Pending: PendingNo, AssignedWork: AssignedWorkHas}, "assigned_work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := DecideIdleTimeout(tc.facts)
			if dec.Action != TimerActionDefer {
				t.Fatalf("Action = %v, want TimerActionDefer", dec.Action)
			}
			if dec.TraceReason != tc.want {
				t.Fatalf("TraceReason = %q, want %q", dec.TraceReason, tc.want)
			}
		})
	}
}

// TestIdleTimeoutTriggerStallDoesNotReachMaxSessionAge pins that the field is
// inert on the other ladder. Max-session-age measures wall-clock lifetime and
// has no liveness signal to name; a shared TimerFacts must not make its trace
// reason depend on a field it does not consult.
func TestIdleTimeoutTriggerStallDoesNotReachMaxSessionAge(t *testing.T) {
	t.Parallel()

	dec := DecideMaxSessionAge(TimerFacts{
		Triggered: true, Trigger: TimerTriggerStall,
		Pending: PendingNo, AssignedWork: AssignedWorkNone,
	})
	if dec.TraceReason != "max_session_age" {
		t.Fatalf("TraceReason = %q, want %q", dec.TraceReason, "max_session_age")
	}
}
