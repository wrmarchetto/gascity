// Scope: the live ralph control path persists what its check script observed
// -- exit code, stdout, stderr, duration, truncation -- onto the control bead.
//
// This suite exists because that record was absent for months with a green
// suite. The writer (persistCheckResult) sits on processRalphCheck, reached
// only by a bead with gc.kind=check, and the formula compiler emits no such
// bead: internal/formula/ralph.go returns control(ralph)+spec+iteration. Every
// pre-existing check-result fixture hand-set gc.kind=check, so the tests
// exercised a subsystem no production path reaches while the live path
// (processRalphControl -> evaluateRalphIteration) dropped four of the six
// GateResult fields on the floor.
//
// The tests therefore enter through processRalphControl with gc.kind=ralph and
// a real exec script. A test that calls processRalphCheck, or that asserts
// against a hand-built gc.kind=check bead, re-pins the dead path and must not
// be added here.
//
// Iteration-attempt history is delegated: gc.attempt_log is covered by
// TestAttemptLogMultipleEntries in control_test.go. These tests assert only
// the most-recent-check observation.
//
// Run: go test ./internal/dispatch/ -run TestProcessRalphControlPersistsCheck

package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// writeSaltedCheckScript writes an exec check that echoes salt to both streams
// and exits with exitCode, returning the salt.
//
// The salt varies per run and the assertion is derived from it rather than
// chosen alongside it: a bead still holding a previous run's gc.stdout, or an
// assertion accidentally comparing a constant against a constant, cannot pass.
func writeSaltedCheckScript(t *testing.T, cityPath string, exitCode int) string {
	t.Helper()
	salt := fmt.Sprintf("salt-%d-%s", time.Now().UnixNano(), t.Name())
	script := filepath.Join(cityPath, "check.sh")
	body := fmt.Sprintf("#!/bin/sh\necho 'out %s'\necho 'err %s' >&2\nexit %d\n", salt, salt, exitCode)
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return salt
}

// ralphCheckObservationFixture builds a root + ralph control + closed iteration
// wired so processAttemptControl reaches evaluateRalphIteration and runs the
// check script.
//
// The iteration carries no gc.outcome deliberately. runRalphCheck
// short-circuits on gc.outcome=fail (ralph.go:212) and synthesizes a
// GateResult without executing anything, which would make an assertion about
// the script's own exit code vacuous.
func ralphCheckObservationFixture(t *testing.T, store beads.Store, cityPath string, maxAttempts int) beads.Bead {
	t.Helper()
	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "bench execute",
		Metadata: map[string]string{
			"gc.kind":          "ralph",
			"gc.root_bead_id":  root.ID,
			"gc.step_ref":      "mol-test.bench-execute",
			"gc.step_id":       "bench-execute",
			"gc.check_mode":    "exec",
			"gc.check_path":    "check.sh",
			"gc.max_attempts":  strconv.Itoa(maxAttempts),
			"gc.control_epoch": "1",
			"gc.source_step_spec": `{"id":"bench-execute","title":"Bench execute","type":"task",` +
				`"ralph":{"max_attempts":` + strconv.Itoa(maxAttempts) +
				`,"check":{"mode":"exec","path":"check.sh"}}}`,
		},
	})
	iteration := mustCreate(t, store, beads.Bead{
		Title: "bench execute iteration 1",
		Metadata: map[string]string{
			"gc.kind":         "scope",
			"gc.root_bead_id": root.ID,
			"gc.step_ref":     "mol-test.bench-execute.iteration.1",
			"gc.scope_role":   "body",
			"gc.attempt":      "1",
		},
	})
	mustClose(t, store, iteration.ID)
	mustDep(t, store, control.ID, iteration.ID, "blocks")
	return control
}

// TestProcessRalphControlPersistsCheckObservationOnPass pins that a PASSING
// check leaves its exit code and both output streams on the control bead.
//
// The pass case is the one that hides a check which passed for the wrong
// reason: with the output discarded there is no way, after the fact, to tell a
// real verification from a script that exited 0 without testing anything.
func TestProcessRalphControlPersistsCheckObservationOnPass(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	salt := writeSaltedCheckScript(t, cityPath, 0)
	store := beads.NewMemStore()
	control := ralphCheckObservationFixture(t, store, cityPath, 2)

	result, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "pass" {
		t.Fatalf("result = %+v, want processed pass", result)
	}

	after := mustGet(t, store, control.ID)
	if got := after.Metadata["gc.exit_code"]; got != "0" {
		t.Errorf("gc.exit_code = %q, want %q", got, "0")
	}
	if got := after.Metadata["gc.stdout"]; !strings.Contains(got, "out "+salt) {
		t.Errorf("gc.stdout = %q, want it to contain %q", got, "out "+salt)
	}
	if got := after.Metadata["gc.stderr"]; !strings.Contains(got, "err "+salt) {
		t.Errorf("gc.stderr = %q, want it to contain %q", got, "err "+salt)
	}
	if got := after.Metadata["gc.truncated"]; got != "false" {
		t.Errorf("gc.truncated = %q, want %q", got, "false")
	}
	// Asserted as a lower bound only. The check is a shell script whose real
	// wall time is milliseconds and machine-dependent, so any nominal upper
	// bound would be a flake; presence and non-negativity are what pin that
	// the field was measured rather than defaulted.
	raw := after.Metadata["gc.duration_ms"]
	if raw == "" {
		t.Error("gc.duration_ms missing, want the measured check duration")
	} else if ms, convErr := strconv.ParseInt(raw, 10, 64); convErr != nil || ms < 0 {
		t.Errorf("gc.duration_ms = %q, want a non-negative integer", raw)
	}

	// The disposition still owns gc.outcome. A mid-flight gate outcome must
	// not leak into it -- see the continue-path test below.
	if got := after.Metadata["gc.outcome"]; got != "pass" {
		t.Errorf("gc.outcome = %q, want %q", got, "pass")
	}
}

// TestProcessRalphControlPersistsCheckExitCodeOnExhaustion pins the exit code
// of a FAILING check, which is the distinction the whole record exists for.
//
// assets/scripts/bench-check.sh passes the engineer's exit status straight
// through, and that convention splits exit 1 (a finding about the run) from
// exit 2 (a finding about the rig). Both reach the analyst as an identical
// failed check when the code is dropped, so a retry budget gets spent
// re-running a bench on a rig fault. Exit 2 is used here rather than 1
// precisely because 1 is the value runRalphCheck synthesizes for an
// already-failed subject: asserting 2 proves the code came from the script.
func TestProcessRalphControlPersistsCheckExitCodeOnExhaustion(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	salt := writeSaltedCheckScript(t, cityPath, 2)
	store := beads.NewMemStore()
	// max_attempts=1 so the failing check exhausts immediately and takes the
	// strategy.exhaust seam, which is a different write from the pass close.
	control := ralphCheckObservationFixture(t, store, cityPath, 1)

	result, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "fail" {
		t.Fatalf("result = %+v, want processed fail", result)
	}

	after := mustGet(t, store, control.ID)
	if got := after.Metadata["gc.exit_code"]; got != "2" {
		t.Errorf("gc.exit_code = %q, want %q -- the rig-fault/run-finding split is unrecoverable without it", got, "2")
	}
	if got := after.Metadata["gc.stdout"]; !strings.Contains(got, "out "+salt) {
		t.Errorf("gc.stdout = %q, want it to contain %q", got, "out "+salt)
	}
	if got := after.Metadata["gc.stderr"]; !strings.Contains(got, "err "+salt) {
		t.Errorf("gc.stderr = %q, want it to contain %q", got, "err "+salt)
	}
	if got := after.Metadata["gc.outcome"]; got != "fail" {
		t.Errorf("gc.outcome = %q, want %q", got, "fail")
	}
}

// TestEvaluateRalphIterationObservationDoesNotPreemptOutcome pins that a
// failing check with budget left records its observation while leaving
// gc.outcome ABSENT on the still-open control bead.
//
// This is why evaluateRalphIteration must call persistGateObservation and not
// persistCheckResult, whose batch includes gc.outcome from the gate result.
// Stamping gc.outcome=fail on a control that is about to spawn another
// iteration publishes a failed verdict for a loop still running, and beadmeta
// documents gc.outcome as the control-plane step outcome -- the final
// disposition, not one attempt's.
//
// This one enters at evaluateRalphIteration rather than processRalphControl
// because the continue disposition goes on to spawn the next iteration, which
// needs molecule scaffolding a MemStore fixture does not have (it returns
// ErrControlPending). The spawn is irrelevant to this invariant, and the two
// tests above already prove the full processRalphControl path.
func TestEvaluateRalphIterationObservationDoesNotPreemptOutcome(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	salt := writeSaltedCheckScript(t, cityPath, 1)
	store := beads.NewMemStore()
	control := ralphCheckObservationFixture(t, store, cityPath, 3)
	iteration := mustGet(t, store, latestIterationID(t, store, control.ID))

	eval, err := evaluateRalphIteration(store, mustGet(t, store, control.ID), iteration, 1, ProcessOptions{CityPath: cityPath})
	if err != nil {
		t.Fatalf("evaluateRalphIteration: %v", err)
	}
	if eval.disposition != attemptContinue {
		t.Fatalf("disposition = %v, want attemptContinue", eval.disposition)
	}

	after := mustGet(t, store, control.ID)
	if got, ok := after.Metadata["gc.outcome"]; ok && got != "" {
		t.Errorf("gc.outcome = %q on an open control with budget left, want absent", got)
	}
	// The observation must be durable at this point, before any close. The
	// record of what the check saw matters most on the path that keeps going.
	if got := after.Metadata["gc.exit_code"]; got != "1" {
		t.Errorf("gc.exit_code = %q, want %q", got, "1")
	}
	if got := after.Metadata["gc.stderr"]; !strings.Contains(got, "err "+salt) {
		t.Errorf("gc.stderr = %q, want it to contain %q", got, "err "+salt)
	}
}

// latestIterationID returns the single iteration the fixture wired under the
// control bead.
func latestIterationID(t *testing.T, store beads.Store, controlID string) string {
	t.Helper()
	deps, err := store.DepList(controlID, "down")
	if err != nil {
		t.Fatalf("DepList(%s): %v", controlID, err)
	}
	for _, dep := range deps {
		if dep.Type == "blocks" {
			return dep.DependsOnID
		}
	}
	t.Fatalf("no blocking iteration found under control %s", controlID)
	return ""
}
