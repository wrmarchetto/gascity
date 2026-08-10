// Scope: the live ralph control loop stops in a single iteration when the
// iteration itself declared a HARD failure, and keeps iterating for every other
// failure class.
//
// This suite exists because the protection was written once, on a path nothing
// ran. The branch lived in processRalphCheck, reachable only from a bead
// carrying gc.kind=check, which no formula compiler emits -- so it read as
// covered while the live path (processRalphControl -> evaluateRalphIteration)
// mapped GatePass to attemptPass and EVERYTHING ELSE to attemptContinue and
// never emitted attemptHardFail at all (ci-81u8, exposed by ci-zg0l's deletion
// of that dead kind). The failure it exists to prevent is the treadmill that
// abort_scope-killed molecules: an iteration closing gc.outcome=fail with
// gc.failure_class=hard -- a live HEAD that moved, a revoked credential,
// anything no amount of re-running repairs -- cloned iterations all the way to
// gc.max_attempts.
//
// The second invariant here is the DIVERGENCE from the retry loop, and it is
// the one a re-implementation gets wrong by reaching for the nearest classifier:
// classifyRetryAttempt maps an EMPTY gc.failure_class to hard (retry.go
// `case beadmeta.FailureClassHard, "":`), whereas this loop must keep an empty,
// transient or unrecognized class repairable. Copying that classifier wholesale
// makes every unclassified iteration failure terminal.
//
// Every test enters through processRalphControl or evaluateRalphIteration with
// gc.kind=ralph and a real exec script. A test asserting against a hand-built
// gc.kind=check bead pins a hard dispatcher error, not a behavior, and must not
// be added here.
//
// Gate-exec infra outcomes (GateError/GateTimeout) are a different question on
// the same seam and are not covered here. Gate observation persistence
// (gc.stdout, gc.exit_code) is delegated to ralph_check_observation_test.go;
// this suite asserts the observation only to prove the gate never ran.
//
// Run: go test ./internal/dispatch/ -run 'RalphIterationHard|RalphIterationOnlyExplicitHard'

package dispatch

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// hardIterationFailureReason is stamped on the iteration and asserted on the
// control. It is a real class of unrepairable failure (the live branch moved
// under a merge attempt) rather than a placeholder, so a reader of a failing
// assertion sees the case the branch exists for.
const hardIterationFailureReason = "external_live_head_changed"

// stampIterationClose applies a worker's terminal metadata to the fixture's
// already-closed iteration.
//
// Writing after the close, rather than before it, is deliberate: that is the
// order a real worker produces (gc bd update --set-metadata, then gc bd close),
// and the loop reads the iteration fresh from the store either way.
func stampIterationClose(t *testing.T, store beads.Store, iterationID string, metadata map[string]string) {
	t.Helper()
	if err := store.SetMetadataBatch(iterationID, metadata); err != nil {
		t.Fatalf("stamp iteration close metadata: %v", err)
	}
}

// TestRalphIterationHardFailureTerminatesInOneAttempt is the ci-81u8 regression
// guard: an iteration that closed gc.outcome=fail with gc.failure_class=hard
// closes the control terminally on iteration 1 instead of spending
// gc.max_attempts on a failure no re-run repairs.
//
// gc.max_attempts is 5, not 1, precisely so the budget is one the loop must
// DECLINE to spend. At 1 the ordinary exhaust path terminates the loop anyway
// and neither the action nor the closed control could tell a hard failure from
// an exhausted one.
//
// The check script exits 0. runRalphCheck short-circuits a gc.outcome=fail
// subject into a synthesized GateFail without executing anything, so a script
// that WOULD have passed is what proves the termination came from the
// iteration's failure class rather than from a gate verdict -- pinned by the
// salt's absence from the persisted observation.
//
// Deliberately NOT asserted: that no second iteration bead exists. A MemStore
// fixture has no molecule scaffolding, so spawnNextAttempt cannot succeed here
// even on the unfixed code -- a scan for gc.attempt=2 would pass either way and
// read as coverage it does not carry. The single hard-fail attempt-log entry on
// a closed control is the observable that separates the two.
func TestRalphIterationHardFailureTerminatesInOneAttempt(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	salt := writeSaltedCheckScript(t, cityPath, 0)
	store := beads.NewMemStore()
	control := ralphCheckObservationFixture(t, store, 5)
	stampIterationClose(t, store, latestIterationID(t, store, control.ID), map[string]string{
		beadmeta.OutcomeMetadataKey:       beadmeta.OutcomeFail,
		beadmeta.FailureClassMetadataKey:  beadmeta.FailureClassHard,
		beadmeta.FailureReasonMetadataKey: hardIterationFailureReason,
	})

	result, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "hard-fail" {
		t.Fatalf("result = %+v, want processed hard-fail", result)
	}
	if result.Created != 0 {
		t.Errorf("result.Created = %d, want 0 -- a hard failure must not clone an iteration", result.Created)
	}

	after := mustGet(t, store, control.ID)
	if after.Status != "closed" {
		t.Errorf("control status = %q, want closed", after.Status)
	}
	for _, want := range []struct{ key, value string }{
		{beadmeta.OutcomeMetadataKey, beadmeta.OutcomeFail},
		{beadmeta.FailureClassMetadataKey, beadmeta.FailureClassHard},
		{beadmeta.FailureReasonMetadataKey, hardIterationFailureReason},
		{beadmeta.FinalDispositionMetadataKey, beadmeta.DispositionHardFail},
		{beadmeta.FailedAttemptMetadataKey, "1"},
	} {
		if got := after.Metadata[want.key]; got != want.value {
			t.Errorf("control %s = %q, want %q", want.key, got, want.value)
		}
	}
	// gc.failure_class on the control can only have come from the hard-fail
	// disposition: propagateRetrySubjectMetadata skips every gc.-prefixed key,
	// so the iteration's own class is never copied across.

	// One entry, and its action names what the loop did. "hard" is the only
	// attempt-log outcome appendAttemptLogValue renders as action=hard-fail,
	// and no convergence gate outcome (pass/fail/error/timeout) collides with
	// it -- recording the gate's synthesized "fail" here would leave a
	// terminal iteration indistinguishable from a repairable one in the audit
	// history the loop's whole diagnosis rests on.
	var log []map[string]string
	if err := json.Unmarshal([]byte(after.Metadata[beadmeta.AttemptLogMetadataKey]), &log); err != nil {
		t.Fatalf("unmarshal gc.attempt_log %q: %v", after.Metadata[beadmeta.AttemptLogMetadataKey], err)
	}
	if len(log) != 1 {
		t.Fatalf("gc.attempt_log = %v, want exactly one entry", log)
	}
	if log[0]["attempt"] != "1" || log[0]["outcome"] != "hard" || log[0]["action"] != "hard-fail" {
		t.Errorf("gc.attempt_log entry = %v, want attempt 1 outcome hard action hard-fail", log[0])
	}
	if got := log[0]["reason"]; got != hardIterationFailureReason {
		t.Errorf("gc.attempt_log reason = %q, want %q", got, hardIterationFailureReason)
	}

	if got := after.Metadata["gc.stdout"]; strings.Contains(got, salt) {
		t.Errorf("gc.stdout = %q, want it free of %q -- the gate must not have run for an already-failed iteration", got, salt)
	}
}

// TestEvaluateRalphIterationOnlyExplicitHardClassIsTerminal pins the whole
// classification surface: only an explicit hard class terminates, and a passing
// gate outranks the class entirely.
//
// It enters at evaluateRalphIteration rather than processRalphControl because
// the continue disposition goes on to spawn the next iteration, which needs
// molecule scaffolding a MemStore fixture does not have; the test above already
// proves the full processRalphControl path for the terminal case.
//
// The repairable rows are the guard against re-implementing this branch by
// calling classifyRetryAttempt, which maps an empty class to hard. They pass
// against the pre-fix code by construction -- pre-fix, everything continued --
// so their value is entirely as a mutation trap, and two were run to establish
// it: widening the branch to the retry classifier's `case FailureClassHard, "":`
// form fails the absent row, and widening it to `!= FailureClassTransient`
// fails the absent and unrecognized rows both.
func TestEvaluateRalphIterationOnlyExplicitHardClassIsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// close is the metadata a worker left on the closed iteration.
		close map[string]string
		want  attemptDisposition
	}{
		{
			name: "explicit hard class terminates",
			close: map[string]string{
				beadmeta.OutcomeMetadataKey:      beadmeta.OutcomeFail,
				beadmeta.FailureClassMetadataKey: beadmeta.FailureClassHard,
			},
			want: attemptHardFail,
		},
		{
			// Metadata written by a shell heredoc arrives with the trailing
			// newline attached. Dropping the TrimSpace turns every such
			// hard failure back into a treadmill.
			name: "hard class survives surrounding whitespace",
			close: map[string]string{
				beadmeta.OutcomeMetadataKey:      beadmeta.OutcomeFail,
				beadmeta.FailureClassMetadataKey: " hard\n",
			},
			want: attemptHardFail,
		},
		{
			name:  "absent class stays repairable",
			close: map[string]string{beadmeta.OutcomeMetadataKey: beadmeta.OutcomeFail},
			want:  attemptContinue,
		},
		{
			name: "transient class stays repairable",
			close: map[string]string{
				beadmeta.OutcomeMetadataKey:      beadmeta.OutcomeFail,
				beadmeta.FailureClassMetadataKey: beadmeta.FailureClassTransient,
			},
			want: attemptContinue,
		},
		{
			// The retry classifier funnels an unrecognized class to transient;
			// this loop must reach the same place by the same reasoning --
			// terminal is reserved for the one class that says so.
			name: "unrecognized class stays repairable",
			close: map[string]string{
				beadmeta.OutcomeMetadataKey:      beadmeta.OutcomeFail,
				beadmeta.FailureClassMetadataKey: "flaky",
			},
			want: attemptContinue,
		},
		{
			// A stale hard class on an iteration that did NOT fail must not
			// override a gate that ran and passed. This is the one row where
			// the exit-0 script actually executes: without gc.outcome=fail
			// there is no short-circuit.
			//
			// What this row can catch was measured, because it is narrower than
			// it looks. Neither single edit breaks it -- the branch ordering
			// absorbs a dropped gc.outcome conjunct, and the conjunct absorbs a
			// reordering -- so it survives each alone and fails only when both
			// land together. That pair is not a contrived mutation: it is what
			// a "collapse the switch" refactor of hardIterationFailure and its
			// call site does in one edit.
			name: "passing gate outranks a stale hard class",
			close: map[string]string{
				beadmeta.OutcomeMetadataKey:      beadmeta.OutcomePass,
				beadmeta.FailureClassMetadataKey: beadmeta.FailureClassHard,
			},
			want: attemptPass,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cityPath := t.TempDir()
			writeSaltedCheckScript(t, cityPath, 0)
			store := beads.NewMemStore()
			control := ralphCheckObservationFixture(t, store, 5)
			iterationID := latestIterationID(t, store, control.ID)
			stampIterationClose(t, store, iterationID, tt.close)

			eval, err := evaluateRalphIteration(store, mustGet(t, store, control.ID),
				mustGet(t, store, iterationID), 1, ProcessOptions{CityPath: cityPath})
			if err != nil {
				t.Fatalf("evaluateRalphIteration: %v", err)
			}
			if eval.disposition != tt.want {
				t.Fatalf("disposition = %v, want %v", eval.disposition, tt.want)
			}
		})
	}
}
