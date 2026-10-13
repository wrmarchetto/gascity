//go:build integration

package main

// Integration suite for the managed Dolt maintenance hold (ci-lg7q7t), against
// a REAL dolt sql-server started by the real gc-beads-bd script with a gc
// binary built from this tree as its helper.
//
// Why it exists: the unit suite proves each door refuses before reaching a
// stand-in script. It cannot represent a server process, so it cannot show
// that the server stays DOWN -- that no path it forgot to model brings one
// back. This suite drives every recovery path ci-wgre2l named, then counts
// live sql-server processes for the city, which is the only witness that
// answers that question.
//
// The control is expiry: the same controller-tick call that is refused under
// the hold must restart the server once the hold lapses. Without it, a rig
// that could not restart the server at all would pass every refusal. The
// lapse is a hold file rewritten with expires_at in the past rather than a
// wait: the script's gc helper is a subprocess reading the real clock, and a
// present-but-lapsed file is exactly the state a crashed holder leaves.
//
// Every process count is taken immediately, with no settle window. Each path
// driven here is synchronous to the server's lifecycle: hold's stop waits for
// the process to exit and the data-dir lock to clear, and the script's start
// returns only after the server answers queries. A restart a path caused is
// therefore already visible when the call returns.
//
// Cannot represent: the long-running supervisor's own cooldown state, and
// concurrent agents. Both reach the same doors driven here.
//
// Run: go test -tags integration ./cmd/gc -run 'MaintenanceHoldKeepsRealServerDown' -count=1 -v
// (requires dolt and flock on PATH; builds bd and gc from source on first use)

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// citySQLServers returns the PIDs of live dolt sql-server processes whose
// argv names this city, wherever they came from.
func citySQLServers(t *testing.T, cityPath string) []int {
	t.Helper()
	procs, err := discoverDoltProcesses()
	if err != nil {
		t.Fatalf("discoverDoltProcesses: %v", err)
	}
	var pids []int
	for _, p := range procs {
		argv := strings.Join(p.Argv, " ")
		if strings.Contains(argv, "sql-server") && strings.Contains(argv, cityPath) && pidAlive(p.PID) {
			pids = append(pids, p.PID)
		}
	}
	return pids
}

func TestMaintenanceHoldKeepsRealServerDownAcrossRecoveryPaths(t *testing.T) {
	cityPath, _ := setupManagedBdWaitTestCity(t)
	gcBin := currentGCBinaryForTests(t)

	before, err := readDoltRuntimeStateFile(managedDoltStatePath(cityPath))
	if err != nil {
		t.Fatalf("readDoltRuntimeStateFile(before): %v", err)
	}
	if before.PID <= 0 || !pidAlive(before.PID) {
		t.Fatalf("fixture server not running: %+v", before)
	}
	if pids := citySQLServers(t, cityPath); len(pids) == 0 {
		t.Fatal("witness sees no sql-server for the city before the hold; it cannot see a restart either")
	}
	// An env built while the server was up: the shape an agent holds when the
	// server goes away under it, and what drives the bd transport retry.
	staleEnv, err := bdRuntimeEnvWithErrorRecovery(cityPath, true)
	if err != nil {
		t.Fatalf("bdRuntimeEnvWithErrorRecovery(before hold): %v", err)
	}

	const holdFor = 10 * time.Minute
	var stdout, stderr strings.Builder
	if code := cmdMaintenanceHold(cityPath, holdFor, "integration: offline gc", &stdout, &stderr); code != 0 {
		t.Fatalf("gc maintenance hold exit = %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	hold, _, err := readManagedDoltMaintenanceHold(cityPath)
	if err != nil {
		t.Fatalf("read hold: %v", err)
	}
	if pids := citySQLServers(t, cityPath); len(pids) != 0 {
		t.Fatalf("gc maintenance hold left sql-server running: %v", pids)
	}

	// 1. Controller patrol tick, with the production health, ownership and
	//    port functions.
	var tickErr strings.Builder
	ensureManagedDoltPublishedForRuntime(cityPath, &tickErr, "test", healthBeadsProvider,
		managedDoltLifecycleOwned, currentResolvableManagedDoltPort)
	if !strings.Contains(tickErr.String(), hold.Reason) {
		t.Errorf("controller tick did not report the hold: %q", tickErr.String())
	}

	// 2. The env build every gc bd / hook / mail / sling runs.
	if _, err := bdRuntimeEnvWithErrorRecovery(cityPath, true); !isManagedDoltMaintenanceHold(err) {
		t.Errorf("env build under hold = %v, want hold refusal", err)
	}

	// 3. The bd transport retry: a real bd against the stale endpoint gets a
	//    real connection refusal and goes to recover.
	start := time.Now()
	_, err = bdCommandRunnerWithManagedRetryErr(cityPath, func(string) (map[string]string, error) {
		return staleEnv, nil
	})(cityPath, "bd", "list", "--json")
	if !isManagedDoltMaintenanceHold(err) {
		t.Errorf("bd transport retry under hold = %v, want hold refusal", err)
	}
	// The pre-hold failure mode was a ~2 min hang on the GC's noms/LOCK. Bound
	// the refusal well under that, with headroom for bd's own connect timeout.
	if elapsed := time.Since(start); elapsed > 60*time.Second {
		t.Errorf("bd transport refusal took %s, want well under the 2 min lock hang", elapsed)
	}

	// 4. The provider script run by hand, with gc as its helper.
	if err := runProviderOpWithEnv(gcBeadsBdScriptPath(cityPath),
		sanitizedBaseEnv("GC_CITY="+cityPath, "GC_CITY_PATH="+cityPath, "GC_BIN="+gcBin), "start"); err == nil {
		t.Error("hand-run gc-beads-bd start under hold exited 0")
	}

	if pids := citySQLServers(t, cityPath); len(pids) != 0 {
		t.Fatalf("sql-server restarted under the hold: %v", pids)
	}

	// Control: once the hold lapses, the same tick restarts the server.
	lapsed := hold
	lapsed.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if err := writeManagedDoltMaintenanceHold(cityPath, lapsed); err != nil {
		t.Fatalf("rewrite hold as lapsed: %v", err)
	}
	tickErr.Reset()
	ensureManagedDoltPublishedForRuntime(cityPath, &tickErr, "test", healthBeadsProvider,
		managedDoltLifecycleOwned, currentResolvableManagedDoltPort)
	if pids := citySQLServers(t, cityPath); len(pids) == 0 {
		t.Fatalf("lapsed hold: controller tick did not restart sql-server (stderr %q)", tickErr.String())
	}
	after, err := readDoltRuntimeStateFile(managedDoltStatePath(cityPath))
	if err != nil {
		t.Fatalf("readDoltRuntimeStateFile(after): %v", err)
	}
	if after.PID == before.PID || !doltPortReachable(strconv.Itoa(after.Port)) {
		t.Fatalf("server after lapse is not a fresh reachable one: before=%+v after=%+v", before, after)
	}

	stdout.Reset()
	stderr.Reset()
	if code := cmdMaintenanceRelease(cityPath, &stdout, &stderr); code != 0 {
		t.Fatalf("gc maintenance release after lapse exit = %d: %s", code, stderr.String())
	}
}
