package main

// Unit suite for the managed Dolt maintenance hold (ci-lg7q7t).
//
// Scope: the hold file's lapse and fail-closed rules, and every door that can
// start or recover a managed server -- the health/recover door, the start
// door, the bd transport-retry door, the env-build target resolver, the
// controller-tick preflight, and the two spawners under the provider script.
// Each door is driven through a stand-in provider script that LOGS every op it
// receives, so "refused" is asserted as "no op reached the script" rather than
// as an error string alone. Each door test is paired with a control that runs
// the same fixture with no hold and requires the op to arrive: without the
// control, a fixture that never reached the door would pass the refusal test
// vacuously.
//
// Delegated: that a refusal actually keeps a REAL sql-server down, and that
// `gc maintenance hold` stops one, is dolt_maintenance_hold_integration_test.go
// (build tag integration). This suite cannot represent a server process.
//
// Run: go test ./cmd/gc -run 'MaintenanceHold' -count=1

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// writeHoldForTest records a hold that expires an hour from the real clock,
// long enough that no test outlives it. Tests that need the lapse move
// managedDoltMaintenanceHoldNow instead. The reason is salted from the test
// name and time so a refusal message carrying it cannot have come from some
// other fixture.
func writeHoldForTest(t *testing.T, cityPath string) managedDoltMaintenanceHold {
	t.Helper()
	now := time.Now().UTC()
	hold := managedDoltMaintenanceHold{
		Holder:    "test-holder",
		Reason:    fmt.Sprintf("%s@%d", t.Name(), now.UnixNano()),
		SetAt:     now,
		ExpiresAt: now.Add(time.Hour),
	}
	if err := writeManagedDoltMaintenanceHold(cityPath, hold); err != nil {
		t.Fatalf("writeManagedDoltMaintenanceHold: %v", err)
	}
	return hold
}

// requireHoldRefusal asserts err is the hold refusal and that it names the
// holder, the reason, and the release command -- the three things an agent
// whose bd call just failed needs in order to act.
func requireHoldRefusal(t *testing.T, err error, hold managedDoltMaintenanceHold) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want maintenance hold refusal")
	}
	if !isManagedDoltMaintenanceHold(err) {
		t.Fatalf("error = %v, want *errManagedDoltMaintenanceHold in chain", err)
	}
	for _, want := range []string{hold.Holder, hold.Reason, "gc maintenance release"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err.Error(), want)
		}
	}
}

func readProviderOps(t *testing.T, opsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(opsFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read provider ops: %v", err)
	}
	return strings.Fields(strings.TrimSpace(string(data)))
}

// holdDoorFixture builds a managed bd city whose provider script logs each op
// and answers health with failure, so the health door proceeds to recover
// when nothing stops it. Returns (cityPath, opsFile).
func holdDoorFixture(t *testing.T) (string, string) {
	t.Helper()
	cityPath := t.TempDir()
	writeMinimalCityToml(t, cityPath)
	opsFile := writeBreakerAwarePreflightFakes(t, cityPath, "unhealthy")
	cityKey := normalizePathForCompare(cityPath)
	lastBeadsProviderRecover.Delete(cityKey)
	t.Cleanup(func() { lastBeadsProviderRecover.Delete(cityKey) })
	return cityPath, opsFile
}

// --- the hold file ---

// TestMaintenanceHoldLapsesAtExpiresAt pins that the hold refuses strictly
// before expires_at and is inert from that instant on. The clock is injected
// so the boundary is exact: a test that slept past a short TTL could not tell
// "lapses at expiry" from "lapses some time after".
func TestMaintenanceHoldLapsesAtExpiresAt(t *testing.T) {
	cityPath := t.TempDir()
	hold := writeHoldForTest(t, cityPath)
	prev := managedDoltMaintenanceHoldNow
	t.Cleanup(func() { managedDoltMaintenanceHoldNow = prev })

	managedDoltMaintenanceHoldNow = func() time.Time { return hold.ExpiresAt.Add(-time.Nanosecond) }
	requireHoldRefusal(t, managedDoltMaintenanceHoldRefusal(cityPath), hold)

	managedDoltMaintenanceHoldNow = func() time.Time { return hold.ExpiresAt }
	if err := managedDoltMaintenanceHoldRefusal(cityPath); err != nil {
		t.Fatalf("refusal at expires_at = %v, want nil (hold lapsed)", err)
	}
}

// TestMaintenanceHoldAbsentFileRefusesNothing pins the steady state: no file,
// no refusal. Every door calls this on every invocation.
func TestMaintenanceHoldAbsentFileRefusesNothing(t *testing.T) {
	if err := managedDoltMaintenanceHoldRefusal(t.TempDir()); err != nil {
		t.Fatalf("refusal with no hold file = %v, want nil", err)
	}
}

// TestMaintenanceHoldUnreadableFileRefuses pins fail-closed. A torn or
// hand-mangled hold must not read as "no hold", and a hold missing its expiry
// must not read as lapsed: either would let a door restart the server under a
// running offline GC.
func TestMaintenanceHoldUnreadableFileRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"garbage":        "{not json",
		"missing-expiry": `{"holder":"x","reason":"y"}`,
	} {
		t.Run(name, func(t *testing.T) {
			cityPath := t.TempDir()
			path := managedDoltMaintenanceHoldPath(cityPath)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			err := managedDoltMaintenanceHoldRefusal(cityPath)
			if !isManagedDoltMaintenanceHold(err) {
				t.Fatalf("refusal = %v, want fail-closed hold refusal", err)
			}
			if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "gc maintenance release") {
				t.Fatalf("refusal %q must name the file and the release command", err)
			}
		})
	}
}

// --- recovery doors ---

// TestMaintenanceHoldHealthDoorRunsNoProviderOp pins the door every
// healthBeadsProvider caller passes through: `gc start`, `gc beads`, the
// supervisor, the controller tick, and the bd env build. Under a hold it must
// not run health OR recover -- recover is the restart, and health is a
// subprocess per agent call buying nothing while the server is down by
// declaration.
func TestMaintenanceHoldHealthDoorRunsNoProviderOp(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)

	requireHoldRefusal(t, healthBeadsProvider(cityPath), hold)
	if ops := readProviderOps(t, opsFile); len(ops) != 0 {
		t.Fatalf("provider ops under hold = %v, want none", ops)
	}
}

// TestMaintenanceHoldHealthDoorControlReachesRecover is the control for the
// test above: the same fixture with no hold must reach recover.
func TestMaintenanceHoldHealthDoorControlReachesRecover(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	_ = healthBeadsProvider(cityPath)
	if _, r := countOps(readProviderOps(t, opsFile), "health", "recover"); r != 1 {
		t.Fatalf("provider ops = %v, want one recover with no hold", readProviderOps(t, opsFile))
	}
}

// TestMaintenanceHoldLapsedHoldReachesRecover pins that expiry actually
// releases the health door, not merely the predicate in isolation.
func TestMaintenanceHoldLapsedHoldReachesRecover(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)
	prev := managedDoltMaintenanceHoldNow
	t.Cleanup(func() { managedDoltMaintenanceHoldNow = prev })
	managedDoltMaintenanceHoldNow = func() time.Time { return hold.ExpiresAt.Add(time.Second) }

	_ = healthBeadsProvider(cityPath)
	if _, r := countOps(readProviderOps(t, opsFile), "health", "recover"); r != 1 {
		t.Fatalf("provider ops = %v, want one recover after the hold lapsed", readProviderOps(t, opsFile))
	}
}

// TestMaintenanceHoldControllerTickRunsNoProviderOp drives the controller's
// patrol preflight with the production health function, so the tick is
// proven to inherit the health door rather than assumed to.
func TestMaintenanceHoldControllerTickRunsNoProviderOp(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)

	var stderr strings.Builder
	ensureManagedDoltPublishedForRuntime(cityPath, &stderr, "test", healthBeadsProvider,
		func(string) (bool, error) { return true, nil },
		func(string) string { return "" })
	if ops := readProviderOps(t, opsFile); len(ops) != 0 {
		t.Fatalf("provider ops from controller tick under hold = %v, want none", ops)
	}
	if !strings.Contains(stderr.String(), hold.Reason) {
		t.Fatalf("controller tick stderr %q does not report the hold", stderr.String())
	}
}

// TestMaintenanceHoldStartDoorRunsNoProviderOp pins ensureBeadsProvider, the
// `start` door reached by `gc start` and the beads lifecycle.
func TestMaintenanceHoldStartDoorRunsNoProviderOp(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)

	requireHoldRefusal(t, ensureBeadsProvider(cityPath), hold)
	if ops := readProviderOps(t, opsFile); len(ops) != 0 {
		t.Fatalf("provider ops under hold = %v, want none", ops)
	}
}

// TestMaintenanceHoldStartDoorControlReachesStart is its control.
func TestMaintenanceHoldStartDoorControlReachesStart(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	_ = ensureBeadsProvider(cityPath)
	if s, _ := countOps(readProviderOps(t, opsFile), "start", "recover"); s != 1 {
		t.Fatalf("provider ops = %v, want one start with no hold", readProviderOps(t, opsFile))
	}
}

// TestMaintenanceHoldBdRecoverDoorRunsNoProviderOp pins recoverManagedBDCommand,
// the door the bd transport retry calls on "connection refused". It is a new
// process per agent call, so no cooldown ever applied to it.
func TestMaintenanceHoldBdRecoverDoorRunsNoProviderOp(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)

	requireHoldRefusal(t, recoverManagedBDCommand(cityPath), hold)
	if ops := readProviderOps(t, opsFile); len(ops) != 0 {
		t.Fatalf("provider ops under hold = %v, want none", ops)
	}
}

// TestMaintenanceHoldBdRecoverDoorControlReachesRecover is its control.
func TestMaintenanceHoldBdRecoverDoorControlReachesRecover(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	_ = recoverManagedBDCommand(cityPath)
	if _, r := countOps(readProviderOps(t, opsFile), "health", "recover"); r != 1 {
		t.Fatalf("provider ops = %v, want one recover with no hold", readProviderOps(t, opsFile))
	}
}

// stubConnectionRefusedBd replaces the bd exec runner with one that fails
// every call with a transport error and counts the calls. Returns the counter.
func stubConnectionRefusedBd(t *testing.T) *int {
	t.Helper()
	calls := 0
	prevRunner, prevSleep := beadsExecCommandRunnerWithEnv, bdCommandRetrySleep
	beadsExecCommandRunnerWithEnv = func(map[string]string) beads.CommandRunner {
		return func(string, string, ...string) ([]byte, error) {
			calls++
			return nil, errors.New("dial tcp 127.0.0.1:3307: connect: connection refused")
		}
	}
	bdCommandRetrySleep = func(time.Duration) {}
	t.Cleanup(func() {
		beadsExecCommandRunnerWithEnv, bdCommandRetrySleep = prevRunner, prevSleep
	})
	return &calls
}

func managedLocalBdEnv(string) (map[string]string, error) {
	return map[string]string{"GC_DOLT_HOST": "127.0.0.1"}, nil
}

// TestMaintenanceHoldBdRetryFailsFastNamingHold pins the agent-visible half of
// the bd transport door: under a hold the runner surfaces the hold refusal and
// does NOT retry. Before, a failed recover returned the bare "connection
// refused", so an agent saw a dead server with no reason and retried it.
func TestMaintenanceHoldBdRetryFailsFastNamingHold(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)
	calls := stubConnectionRefusedBd(t)

	_, err := bdCommandRunnerWithManagedRetryErr(cityPath, managedLocalBdEnv)(cityPath, "bd", "list")
	requireHoldRefusal(t, err, hold)
	if *calls != 1 {
		t.Fatalf("bd invocations under hold = %d, want 1 (no retry)", *calls)
	}
	if ops := readProviderOps(t, opsFile); len(ops) != 0 {
		t.Fatalf("provider ops under hold = %v, want none", ops)
	}
}

// TestMaintenanceHoldBdRetryControlRecoversAndRetries is its control: with no
// hold the same transport error reaches recover and retries once.
func TestMaintenanceHoldBdRetryControlRecoversAndRetries(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	calls := stubConnectionRefusedBd(t)

	_, _ = bdCommandRunnerWithManagedRetryErr(cityPath, managedLocalBdEnv)(cityPath, "bd", "list")
	if *calls != 2 {
		t.Fatalf("bd invocations with no hold = %d, want 2 (one retry)", *calls)
	}
	if _, r := countOps(readProviderOps(t, opsFile), "health", "recover"); r != 1 {
		t.Fatalf("provider ops = %v, want one recover with no hold", readProviderOps(t, opsFile))
	}
}

// TestMaintenanceHoldEnvBuildSurfacesHold pins the `gc bd` / `gc hook` env
// build. It reaches the health door under allowRecovery and previously
// discarded that door's error for a generic "managed runtime unavailable",
// which would hide the reason from every agent.
func TestMaintenanceHoldEnvBuildSurfacesHold(t *testing.T) {
	cityPath, opsFile := holdDoorFixture(t)
	hold := writeHoldForTest(t, cityPath)

	_, _, err := resolvedRuntimeCityDoltTargetContext(context.Background(), cityPath, true)
	requireHoldRefusal(t, err, hold)
	if ops := readProviderOps(t, opsFile); len(ops) != 0 {
		t.Fatalf("provider ops under hold = %v, want none", ops)
	}
}

// --- spawners under the provider script ---

// TestMaintenanceHoldStartManagedSpawnsNothing pins the innermost spawner,
// reached by `gc-beads-bd start` and by recover-managed. The sql-server seam
// fails the test if called, so the refusal must come before any spawn.
func TestMaintenanceHoldStartManagedSpawnsNothing(t *testing.T) {
	cityPath := t.TempDir()
	hold := writeHoldForTest(t, cityPath)
	prev := managedDoltStartSQLServerFn
	managedDoltStartSQLServerFn = func(string, string, string, *os.File) (managedDoltStartedProcess, error) {
		t.Fatal("sql-server spawned under a maintenance hold")
		return managedDoltStartedProcess{}, nil
	}
	t.Cleanup(func() { managedDoltStartSQLServerFn = prev })

	_, err := startManagedDoltProcessWithOptions(cityPath, "127.0.0.1", "3307", "root", "warning", -1, time.Second, true)
	requireHoldRefusal(t, err, hold)
}

// TestMaintenanceHoldRecoverManagedRunsNoOp pins recover-managed. Every op is
// a stand-in that fails the test when called: under a hold, recover must not
// even stop or clean, since those touch the data dir an offline GC owns.
func TestMaintenanceHoldRecoverManagedRunsNoOp(t *testing.T) {
	cityPath := t.TempDir()
	hold := writeHoldForTest(t, cityPath)
	refuse := func(op string) { t.Fatalf("recover-managed ran %s under a maintenance hold", op) }
	ops := managedDoltRecoveryOps{
		queryProbe: func(string, string, string) error { refuse("queryProbe"); return nil },
		healthCheck: func(string, string, string) (managedDoltSQLHealthReport, error) {
			refuse("healthCheck")
			return managedDoltSQLHealthReport{}, nil
		},
		stop: func(string, string) (managedDoltStopReport, error) {
			refuse("stop")
			return managedDoltStopReport{}, nil
		},
		preflightCleanup: func(string) error { refuse("preflightCleanup"); return nil },
		start: func(string, string, string, string, string, time.Duration) (managedDoltStartReport, error) {
			refuse("start")
			return managedDoltStartReport{}, nil
		},
		publish:       func(string) error { refuse("publish"); return nil },
		failedCleanup: func(string, int, int, error) error { refuse("failedCleanup"); return nil },
	}

	_, err := recoverManagedDoltProcessWithOps(cityPath, "127.0.0.1", "3307", "root", "warning", time.Second, ops)
	requireHoldRefusal(t, err, hold)
}

// --- the provider script, run by hand ---

// holdScriptFixture materializes the real gc-beads-bd script into a temp city
// and returns (cityPath, script, binDir). binDir is put first on a PATH that
// holds no gc binary, so each case chooses whether gc is reachable at all.
func holdScriptFixture(t *testing.T) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not installed")
	}
	cityPath := t.TempDir()
	writeMinimalCityToml(t, cityPath)
	materializeBuiltinPacksForTest(t, cityPath)
	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A dolt that logs every call and refuses all but identity lookups, so
	// an unscripted spawn can neither succeed nor go unnoticed.
	doltLog := filepath.Join(binDir, "dolt.log")
	fakeDolt := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\n[ \"$1\" = config ] && exit 0\nexit 1\n", doltLog)
	if err := os.WriteFile(filepath.Join(binDir, "dolt"), []byte(fakeDolt), 0o755); err != nil {
		t.Fatal(err)
	}
	return cityPath, gcBeadsBdScriptPath(cityPath), binDir
}

// writeStubGC writes a gc that logs every call, answers hold-check with
// holdExit, and refuses everything else.
func writeStubGC(t *testing.T, binDir string, holdExit int) (string, string) {
	t.Helper()
	gcLog := filepath.Join(binDir, "gc.log")
	body := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
if [ "$1 $2" = "dolt-state hold-check" ]; then
  [ %d -eq 0 ] || echo "stub: held by test-holder" >&2
  exit %d
fi
exit 1
`, gcLog, holdExit, holdExit)
	gcPath := filepath.Join(binDir, "gc-stub")
	if err := os.WriteFile(gcPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return gcPath, gcLog
}

// runHoldScript runs one op of the materialized script through
// runProviderOpWithEnv, the runner gc itself uses, so the exit-status mapping
// is production's: exit 2 is "not needed" and returns nil, and any other
// failure returns an error carrying the script's stderr.
func runHoldScript(script, binDir, cityPath, gcBin, op string) error {
	return runProviderOpWithEnv(script, sanitizedBaseEnv(
		"GC_CITY_PATH="+cityPath,
		"GC_BIN="+gcBin,
		"PATH="+binDir+string(os.PathListSeparator)+"/usr/bin:/bin",
	), op)
}

// TestMaintenanceHoldScriptStartAndRecoverConsultGC pins that a hand-run
// start or recover asks gc before doing any start work: with the hold
// in force neither op reaches start-managed, and the refusal is gc's own.
// The control half lets hold-check pass and requires start-managed to be
// reached, which proves the guard sits ahead of it rather than nowhere.
func TestMaintenanceHoldScriptStartAndRecoverConsultGC(t *testing.T) {
	for _, op := range []string{"start", "recover"} {
		t.Run(op, func(t *testing.T) {
			cityPath, script, binDir := holdScriptFixture(t)
			writeHoldForTest(t, cityPath)
			gcBin, gcLog := writeStubGC(t, binDir, 1)

			err := runHoldScript(script, binDir, cityPath, gcBin, op)
			if err == nil {
				t.Fatalf("%s under hold succeeded", op)
			}
			if !strings.Contains(err.Error(), "stub: held by test-holder") {
				t.Fatalf("%s failure does not carry gc's refusal: %v", op, err)
			}
			calls, _ := os.ReadFile(gcLog)
			if !strings.Contains(string(calls), "dolt-state hold-check") ||
				strings.Contains(string(calls), "start-managed") || strings.Contains(string(calls), "recover-managed") {
				t.Fatalf("gc calls under hold = %q, want hold-check and no start/recover helper", calls)
			}
		})
		t.Run(op+"-control", func(t *testing.T) {
			cityPath, script, binDir := holdScriptFixture(t)
			writeHoldForTest(t, cityPath)
			gcBin, gcLog := writeStubGC(t, binDir, 0)

			_ = runHoldScript(script, binDir, cityPath, gcBin, op)
			calls, _ := os.ReadFile(gcLog)
			want := map[string]string{"start": "start-managed", "recover": "recover-managed"}[op]
			if !strings.Contains(string(calls), want) {
				t.Fatalf("gc calls with hold-check passing = %q, want %s reached", calls, want)
			}
		})
	}
}

// TestMaintenanceHoldScriptWithoutGCRefusesOnFile pins the fail-closed branch:
// without GC_BIN the script cannot read the expiry, and the shell fallback it
// would take spawns dolt sql-server directly, so a present hold file must
// refuse. The PATH deliberately keeps /usr/bin, where this fleet has Graphviz's
// gc: a guard that fell back to a PATH lookup ran it, got exit 0, and failed
// open. The die text is matched so the refusal is known to be the guard's and
// not some unrelated early exit.
func TestMaintenanceHoldScriptWithoutGCRefusesOnFile(t *testing.T) {
	cityPath, script, binDir := holdScriptFixture(t)
	writeHoldForTest(t, cityPath)

	err := runHoldScript(script, binDir, cityPath, "", "start")
	if err == nil {
		t.Fatal("start with hold file and no gc succeeded")
	}
	if !strings.Contains(err.Error(), "GC_BIN names no gc binary to read its expiry") {
		t.Fatalf("start failure is not the hold guard's refusal: %v", err)
	}
	if calls, _ := os.ReadFile(filepath.Join(binDir, "dolt.log")); strings.Contains(string(calls), "sql-server") {
		t.Fatalf("dolt sql-server invoked under hold: %q", calls)
	}
}

// --- visibility ---

// TestMaintenanceHoldStatusBannerPrintsBeforeStoreOpen pins `gc status`: the
// banner appears in text mode whatever the store open does next -- under a
// hold it normally fails -- and stays out of --json. The bare city here has
// no running store, so the exit code is not asserted.
func TestMaintenanceHoldStatusBannerPrintsBeforeStoreOpen(t *testing.T) {
	cityPath := t.TempDir()
	writeMinimalCityToml(t, cityPath)
	hold := writeHoldForTest(t, cityPath)

	var stdout, stderr strings.Builder
	_ = cmdCityStatus([]string{cityPath}, false, &stdout, &stderr)
	if !strings.Contains(stdout.String(), hold.Reason) || !strings.Contains(stdout.String(), "gc maintenance release") {
		t.Fatalf("gc status text output %q does not show the hold", stdout.String())
	}

	stdout.Reset()
	_ = cmdCityStatus([]string{cityPath}, true, &stdout, &stderr)
	if strings.Contains(stdout.String(), hold.Reason) {
		t.Fatalf("gc status --json carries the banner: %q", stdout.String())
	}
}

// TestMaintenanceHoldStatusLineEmptyWithoutHold pins that no hold and a lapsed
// hold print nothing, so the banner never cries wolf.
func TestMaintenanceHoldStatusLineEmptyWithoutHold(t *testing.T) {
	cityPath := t.TempDir()
	if line := managedDoltMaintenanceHoldStatusLine(cityPath); line != "" {
		t.Fatalf("status line with no hold = %q, want empty", line)
	}
	hold := writeHoldForTest(t, cityPath)
	prev := managedDoltMaintenanceHoldNow
	t.Cleanup(func() { managedDoltMaintenanceHoldNow = prev })
	managedDoltMaintenanceHoldNow = func() time.Time { return hold.ExpiresAt }
	if line := managedDoltMaintenanceHoldStatusLine(cityPath); line != "" {
		t.Fatalf("status line for a lapsed hold = %q, want empty", line)
	}
}

// TestMaintenanceHoldDoltStateHoldCheckExitsNonzeroUnderHold pins the hidden
// command the provider script consults. The script tests above use a stub gc,
// so without this the real command could answer 0 under a hold and only the
// integration suite would notice.
func TestMaintenanceHoldDoltStateHoldCheckExitsNonzeroUnderHold(t *testing.T) {
	run := func(cityPath string) (string, error) {
		var stdout, stderr strings.Builder
		cmd := newDoltStateCmd(&stdout, &stderr)
		cmd.SetArgs([]string{"hold-check", "--city", cityPath})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		err := cmd.Execute()
		return stderr.String(), err
	}
	cityPath := t.TempDir()
	if _, err := run(cityPath); err != nil {
		t.Fatalf("hold-check with no hold = %v, want nil", err)
	}
	hold := writeHoldForTest(t, cityPath)
	stderr, err := run(cityPath)
	if err == nil {
		t.Fatal("hold-check under a hold exited 0")
	}
	if !strings.Contains(stderr, hold.Reason) {
		t.Fatalf("hold-check stderr %q does not name the hold", stderr)
	}
}
