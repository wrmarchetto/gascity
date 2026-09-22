package main

import (
	"testing"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestTimerTraceCodesTotal drives every reachable TimerDecision from
// DecideMaxSessionAge, DecideIdleTimeout (all TimerFacts combinations,
// including both blocker kinds), and the parameterless
// DecideAssignedWorkExhausted, and asserts that timerTraceCodes (a) maps each
// traced reason/outcome onto a NAMED constant — never falling through to the
// identity default arm — and (b) round-trips to the exact producer strings.
// When the timer ladders grow a new traced value, this test goes red instead
// of silently un-typing the vocabulary.
func TestTimerTraceCodesTotal(t *testing.T) {
	namedReasons := map[TraceReasonCode]bool{
		TraceReasonMaxSessionAge:         true,
		TraceReasonIdleTimeout:           true,
		TraceReasonStallTimeout:          true,
		TraceReasonUserHold:              true,
		TraceReasonQuarantine:            true,
		TraceReasonPending:               true,
		TraceReasonAssignedWork:          true,
		TraceReasonAssignedWorkExhausted: true,
	}
	namedOutcomes := map[TraceOutcomeCode]bool{
		TraceOutcomeStop:               true,
		TraceOutcomeDeferredUserHold:   true,
		TraceOutcomeDeferredQuarantine: true,
		TraceOutcomeDeferredPending:    true,
		TraceOutcomeDeferredBusy:       true,
		TraceOutcomeStopDeferExhausted: true,
	}

	blockers := []string{"", "user_hold", "quarantine"}
	pendings := []sessionpkg.PendingFact{
		sessionpkg.PendingUnknown, sessionpkg.PendingNo, sessionpkg.PendingYes,
	}
	assigned := []sessionpkg.AssignedWorkFact{
		sessionpkg.AssignedWorkUnknown, sessionpkg.AssignedWorkNone, sessionpkg.AssignedWorkHas,
	}
	// The trigger source is part of the cross product because it changes the
	// terminal reason on the idle ladder. Leaving it out is how this test
	// passed a vocabulary it did not cover: every case set the zero value,
	// so the new arm's reason was never converted.
	triggers := []sessionpkg.TimerTrigger{
		sessionpkg.TimerTriggerIdle, sessionpkg.TimerTriggerStall,
	}

	var decisions []sessionpkg.TimerDecision
	for _, b := range blockers {
		for _, p := range pendings {
			for _, a := range assigned {
				for _, tr := range triggers {
					facts := sessionpkg.TimerFacts{Triggered: true, Trigger: tr, Blocker: b, Pending: p, AssignedWork: a}
					decisions = append(decisions, sessionpkg.DecideMaxSessionAge(facts))
					decisions = append(decisions, sessionpkg.DecideIdleTimeout(facts))
				}
			}
		}
	}
	decisions = append(decisions, sessionpkg.DecideAssignedWorkExhausted())

	sawTraced := false
	for _, dec := range decisions {
		// Only Defer/Stop decisions carry trace codes and reach a
		// RecordDecision call site; gather/none actions leave them empty.
		if dec.Action != sessionpkg.TimerActionDefer && dec.Action != sessionpkg.TimerActionStop {
			continue
		}
		sawTraced = true
		reason, outcome := timerTraceCodes(dec)
		if string(reason) != dec.TraceReason {
			t.Errorf("reason round-trip: got %q, want %q", string(reason), dec.TraceReason)
		}
		if string(outcome) != dec.TraceOutcome {
			t.Errorf("outcome round-trip: got %q, want %q", string(outcome), dec.TraceOutcome)
		}
		if !namedReasons[reason] {
			t.Errorf("reason %q fell through to the identity default arm (unnamed vocabulary)", string(reason))
		}
		if !namedOutcomes[outcome] {
			t.Errorf("outcome %q fell through to the identity default arm (unnamed vocabulary)", string(outcome))
		}
	}
	if !sawTraced {
		t.Fatal("no traced TimerDecision exercised — enumeration is broken")
	}
}

// TestTimerStopAutoArmsAndDeferDoesNot pins the auto-arm policy for the two
// lifecycle-timer ladders: a decision that STOPS a session arms the template
// at detail, a decision that DEFERS one does not.
//
// The decisions are driven through the deciders rather than listed as
// reason/outcome literals on purpose. A literal list is a second copy of the
// ladders' vocabulary and rots the moment a rung is added -- the new rung
// would be absent from both the list and shouldAutoArmForTrace, and the suite
// would stay green over a decision nobody can see. Enumerating the fact space
// instead makes a new rung fail here until its side of the policy is chosen.
//
// The asymmetry is the whole point and it is a volume argument, not an
// oversight. An auto-arm arms the WHOLE template at detail for ten minutes,
// measured at roughly 1-1.5 records per second per template, against a cap of
// sessionReconcilerTraceMaxAutoArms concurrent arms shared with the failure
// triggers. A stop happens at most once per session lifetime. An
// assigned_work defer happens once per tick for the entire time a wedged
// session sits, so arming on it would hold the cap indefinitely and starve
// the anomaly arms it shares with.
func TestTimerStopAutoArmsAndDeferDoesNot(t *testing.T) {
	blockers := []string{"", "user_hold", "quarantine"}
	pendings := []sessionpkg.PendingFact{
		sessionpkg.PendingUnknown, sessionpkg.PendingNo, sessionpkg.PendingYes,
	}
	assigned := []sessionpkg.AssignedWorkFact{
		sessionpkg.AssignedWorkUnknown, sessionpkg.AssignedWorkNone, sessionpkg.AssignedWorkHas,
	}

	var decisions []sessionpkg.TimerDecision
	for _, b := range blockers {
		for _, p := range pendings {
			for _, a := range assigned {
				facts := sessionpkg.TimerFacts{Triggered: true, Blocker: b, Pending: p, AssignedWork: a}
				decisions = append(decisions, sessionpkg.DecideMaxSessionAge(facts))
				decisions = append(decisions, sessionpkg.DecideIdleTimeout(facts))
			}
		}
	}
	decisions = append(decisions, sessionpkg.DecideAssignedWorkExhausted())

	stops, defers := 0, 0
	for _, dec := range decisions {
		reason, outcome := timerTraceCodes(dec)
		switch dec.Action {
		case sessionpkg.TimerActionStop:
			stops++
			if !shouldAutoArmForTrace(reason, outcome) {
				t.Errorf("stop (%s/%s) does not auto-arm: the kill would be recorded only on a hand-armed template", reason, outcome)
			}
		case sessionpkg.TimerActionDefer:
			defers++
			if shouldAutoArmForTrace(reason, outcome) {
				t.Errorf("defer (%s/%s) auto-arms: a per-tick decision would hold the auto-arm cap indefinitely", reason, outcome)
			}
		}
	}
	// Both counts guard against a vacuous pass: a broken enumeration that
	// produced only gather actions would satisfy every assertion above.
	if stops == 0 || defers == 0 {
		t.Fatalf("enumeration produced %d stops and %d defers, want both non-zero", stops, defers)
	}
}
