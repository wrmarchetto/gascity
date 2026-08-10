// Scope: the live ralph control loop separates a check gate that RAN and
// returned a verdict from one that could not run at all, and spends a
// gc.attempt only on the former.
//
// This suite exists because the protection was written once, on the wrong
// path. maxCheckInfraRetries and its regression tests lived on processRalphCheck
// -- reachable only from a bead carrying gc.kind=check, which no compiler
// emits -- so the guard read as covered while the live path
// (processRalphControl -> evaluateRalphIteration) had no GateError/GateTimeout
// branch at all (ci-6wwn, exposed by ci-zg0l's deletion of that dead kind).
// The failure it exists to prevent is the maintainer-city "zero-merge day":
// adopt-pr gates could not execute during an outage, each gate-exec error
// consumed an attempt, and abort_scope fired on genuinely-green PRs.
//
// Every test here therefore enters through processRalphControl with
// gc.kind=ralph and a real exec script. A test asserting against a hand-built
// gc.kind=check bead pins a hard dispatcher error, not a behavior, and must
// not be added here.
//
// Each fixture sets gc.max_attempts=1 on purpose. At that budget the burn path
// and the infra-retry path have unmistakably different observables -- a closed
// control with gc.outcome=fail versus an open control and ErrControlPending --
// whereas at a higher budget both spawn-next and infra-retry surface as
// ErrControlPending from a MemStore fixture with no molecule scaffolding, and
// the assertion could not tell them apart.
//
// Gate observation persistence (gc.stdout, gc.exit_code, gc.duration_ms) is
// delegated to ralph_check_observation_test.go; this suite asserts the
// observation only where it proves which gate outcome actually occurred.
//
// Run: go test ./internal/dispatch/ -run TestProcessRalphControlGate

package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// infraGateTimeout is the per-check deadline unrunnableGateControl installs,
// and the lower bound the duration assertion is derived from. Stated once and
// used for both so the assertion cannot drift away from the configured value
// and silently start passing against a gate that never reached its deadline.
const infraGateTimeout = 100 * time.Millisecond

// writeGateScript writes the fixture's check script at the path
// ralphCheckObservationFixture hardcodes into gc.check_path.
func writeGateScript(t *testing.T, cityPath, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(cityPath, "check.sh"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// unrunnableGateControl builds a ralph control whose check script cannot
// produce a verdict, either because it never finishes or because it cannot be
// executed at all.
func unrunnableGateControl(t *testing.T, cityPath, scriptBody string, store beads.Store) beads.Bead {
	t.Helper()
	writeGateScript(t, cityPath, scriptBody)
	control := ralphCheckObservationFixture(t, store, 1)
	if err := store.SetMetadata(control.ID, beadmeta.CheckTimeoutMetadataKey, infraGateTimeout.String()); err != nil {
		t.Fatalf("set gc.check_timeout: %v", err)
	}
	return control
}

// TestProcessRalphControlGateTimeoutDoesNotBurnIteration pins that a gate
// killed by its own deadline re-runs instead of consuming the loop's last
// attempt.
//
// A GateTimeout means the gate produced no verdict, so treating it as a fail is
// a fabricated verdict, not a conservative one. The assertions below name both
// halves: gc.check_infra_retry must move (the re-run was recorded) and the
// control must stay open with no gc.outcome (nothing was concluded). Asserting
// only ErrControlPending would be vacuous -- the spawn-next path returns the
// same sentinel from this fixture.
func TestProcessRalphControlGateTimeoutDoesNotBurnIteration(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	control := unrunnableGateControl(t, cityPath, "#!/bin/sh\nsleep 5\n", store)

	_, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if !errors.Is(err, ErrControlPending) {
		t.Fatalf("processRalphControl on GateTimeout = %v, want ErrControlPending (re-run without burning an attempt)", err)
	}

	after := mustGet(t, store, control.ID)
	if got := after.Metadata[beadmeta.CheckInfraRetryMetadataKey]; got != "1" {
		t.Errorf("gc.check_infra_retry = %q, want %q", got, "1")
	}
	if after.Status == "closed" {
		t.Error("control closed on a gate that never produced a verdict, want it open for the re-run")
	}
	if got := after.Metadata[beadmeta.OutcomeMetadataKey]; got != "" {
		t.Errorf("gc.outcome = %q, want absent -- an unrunnable gate concluded nothing", got)
	}
	if got := after.Metadata[beadmeta.AttemptLogMetadataKey]; got != "" {
		t.Errorf("gc.attempt_log = %q, want absent -- an infra re-run is not an attempt", got)
	}
	// Lower bound only, and deliberately loose. It proves the gate ran until
	// its deadline rather than failing earlier for some other reason (a path
	// that resolves but cannot exec returns GateError in microseconds, and
	// would otherwise satisfy every assertion above).
	raw := after.Metadata[beadmeta.DurationMsMetadataKey]
	ms, convErr := strconv.ParseInt(raw, 10, 64)
	if convErr != nil || ms < infraGateTimeout.Milliseconds() {
		t.Errorf("gc.duration_ms = %q, want at least %dms (the configured gc.check_timeout)", raw, infraGateTimeout.Milliseconds())
	}
}

// TestProcessRalphControlGateErrorDoesNotBurnIteration pins the same protection
// for the other infra outcome: a gate that could not be executed at all.
//
// GateError and GateTimeout are produced by different branches of
// convergence.runOnceNoPreExecRetry (exec failure versus deadline), so a fix
// that handles one and not the other passes the timeout test above while
// leaving the incident's literal shape -- a gate that cannot start -- unguarded.
// The script is mode 0755 and a regular file so it clears ResolveConditionPath,
// which rejects a missing or non-executable path with a hard Go error long
// before the gate runs; only the interpreter is absent.
func TestProcessRalphControlGateErrorDoesNotBurnIteration(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	control := unrunnableGateControl(t, cityPath, "#!/nonexistent/interpreter-ci6wwn\ntrue\n", store)

	_, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if !errors.Is(err, ErrControlPending) {
		t.Fatalf("processRalphControl on GateError = %v, want ErrControlPending (re-run without burning an attempt)", err)
	}

	after := mustGet(t, store, control.ID)
	if got := after.Metadata[beadmeta.CheckInfraRetryMetadataKey]; got != "1" {
		t.Errorf("gc.check_infra_retry = %q, want %q", got, "1")
	}
	if after.Status == "closed" {
		t.Error("control closed on a gate that could not execute, want it open for the re-run")
	}
	if got := after.Metadata[beadmeta.OutcomeMetadataKey]; got != "" {
		t.Errorf("gc.outcome = %q, want absent -- an unrunnable gate concluded nothing", got)
	}
}

// TestProcessRalphControlGateInfraRetryBudgetExhaustionBurns pins the bound
// that makes the protection safe: a gate that can NEVER run must still
// terminate the workflow.
//
// Without this the infra-retry path is an infinite pend -- a missing
// interpreter or a permanently overloaded host would hold a molecule open
// forever, which is a worse failure than the burned attempt the retry exists to
// prevent. The budget is primed rather than driven to exhaustion by 20 real
// dispatches because each one costs a 100ms gate deadline; the boundary is what
// is under test, not the arithmetic reaching it.
func TestProcessRalphControlGateInfraRetryBudgetExhaustionBurns(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	control := unrunnableGateControl(t, cityPath, "#!/bin/sh\nsleep 5\n", store)
	if err := store.SetMetadata(control.ID, beadmeta.CheckInfraRetryMetadataKey, strconv.Itoa(maxCheckInfraRetries)); err != nil {
		t.Fatalf("prime infra-retry budget: %v", err)
	}

	result, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "fail" {
		t.Fatalf("result = %+v, want processed fail (an unrunnable gate must terminate once its budget is spent)", result)
	}
	after := mustGet(t, store, control.ID)
	if got := after.Metadata[beadmeta.OutcomeMetadataKey]; got != beadmeta.OutcomeFail {
		t.Errorf("gc.outcome = %q, want %q", got, beadmeta.OutcomeFail)
	}
}

// TestProcessRalphControlGateFailStillBurnsIteration pins the other side of the
// split: a gate that RAN and returned a verdict must still spend its attempt.
//
// This is the assertion that keeps the fix from being a blanket "never burn on
// a non-pass". A branch keyed on "outcome != pass" rather than on the two infra
// outcomes passes every test above and turns gc.max_attempts into a no-op, so
// the exhaustion assertion here is load-bearing, not a duplicate of the
// observation suite's failing-check case.
func TestProcessRalphControlGateFailStillBurnsIteration(t *testing.T) {
	t.Parallel()
	cityPath := t.TempDir()
	store := beads.NewMemStore()
	writeGateScript(t, cityPath, "#!/bin/sh\nexit 3\n")
	control := ralphCheckObservationFixture(t, store, 1)

	result, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{CityPath: cityPath})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "fail" {
		t.Fatalf("result = %+v, want processed fail (a gate that ran and failed spends its attempt)", result)
	}
	after := mustGet(t, store, control.ID)
	if got := after.Metadata[beadmeta.CheckInfraRetryMetadataKey]; got != "" {
		t.Errorf("gc.check_infra_retry = %q, want absent -- exit 3 is a verdict, not an infra outcome", got)
	}
	if got := after.Metadata[beadmeta.ExitCodeMetadataKey]; got != "3" {
		t.Errorf("gc.exit_code = %q, want %q", got, "3")
	}
}
