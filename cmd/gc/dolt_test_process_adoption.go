package main

// Adoption of provider-script-spawned managed dolt servers into the cmd/gc
// test reaper (bead ci-9r6x).
//
// TestMain's reapManagedDoltTestProcesses exists to kill managed dolt servers
// a test forgot to stop, but no server started through the provider lifecycle
// was ever registered with it. Registration happened in one place --
// startManagedDoltSQLServerWithTestWatchdog, which a test reaches only by
// calling it directly, never by starting a store the way production does.
// resolveProviderLifecycleGCBinary (beads_provider_lifecycle.go)
// returns "" for every *.test binary, so GC_BIN never reaches the provider
// script; without it the script's load_start_managed_from_gc cannot delegate
// to `gc dolt-state start-managed` and falls through to spawning
// `dolt sql-server` under nohup itself
// (examples/bd/assets/scripts/gc-beads-bd.sh). That PID reached no Go memory
// at all, so the safety net was down for precisely the case it was built for.
//
// Two alternatives were rejected. Making isTestBinary() resolve a real gc
// binary would restore the delegating route, but that guard is what keeps
// tests off host infrastructure -- and the delegate is a separate gc process,
// so its in-process registration would never reach the test binary's registry
// anyway. Widening TestMain's leak scan is a different mechanism with a
// different purpose: the scan asserts that the test stopped its server, and
// turning an assertion into a cleanup path removes the assertion.
//
// What both spawn routes do share is the PID file. The Go starter writes it
// (dolt_start_managed.go) and so does the script, on the spawn and the
// adopt-an-existing-server paths alike, so reading GC_DOLT_PID_FILE after a
// provider op covers them both from one seam.
//
// That seam is runProviderOpWithEnvContext, which every lifecycle op --
// start, init, health, recover, stop -- passes through. The package's other
// exec site, runProviderProbe, is deliberately NOT hooked: the script's
// op_probe only inspects and can never leave a server behind.
//
// Known limit: only a REAL dolt binary is adoptable. A test that starts its
// store with a shell fake named `dolt` (writeFakeDoltSQLServer) is invisible
// here, because a shebang exec puts the interpreter in argv[0] while
// looksLikeDoltSQLServer matches on argv[0] being `dolt`. That blindness
// already applies to discoverDoltProcesses and so to TestMain's leak guard
// too, so nothing previously covered is lost -- and the leak this backstop
// exists for is a real server holding a real data dir.
//
// Load-bearing constraint: never register a PID that has not been confirmed to
// be a live `dolt sql-server`. Everything the registry holds is SIGTERMed at
// package teardown, and a PID file outlives a server that died without
// cleaning up -- it then names a number the kernel is free to reuse. The
// confirmation is fail-closed: a host where neither /proc nor ps can answer
// registers nothing and stays exactly as blind as it was before this file.

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// adoptScriptSpawnedManagedDoltForTests registers the managed dolt server
// recorded in the provider environment's GC_DOLT_PID_FILE with the cmd/gc test
// reaper. environ is the environment the provider op was run with.
//
// Outside managed-dolt test mode this returns before touching the filesystem,
// which is what keeps production provider ops -- every `gc start`, health
// probe and stop -- free of the per-op PID read and process inspection below.
//
// Two deliberate omissions. This never falls back to this process's own
// environment when environ is empty: the ambient GC_DOLT_PID_FILE of whatever
// city the developer is standing in would then decide which PID gets adopted,
// and everything adopted is eventually signaled. And it does not reproduce the
// script's fallback chain for an unset GC_DOLT_PID_FILE
// (GC_PACK_STATE_DIR, then GC_CITY_RUNTIME_DIR, then <city>/.gc/runtime) --
// every lifecycle op reaches the script through
// providerLifecycleProcessEnvWithError, which always projects the variable, so
// a second copy of that precedence would only be free to drift from the
// script's with nothing to catch it.
func adoptScriptSpawnedManagedDoltForTests(environ []string) {
	if !managedDoltTestModeEnabled() {
		return
	}
	env := runtimeEnvEntriesToMap(environ)
	pid := managedDoltPIDFromProviderPIDFile(env["GC_DOLT_PID_FILE"])
	// Refusing our own PID is redundant behind the argv check below -- a test
	// binary is not a dolt sql-server -- and kept anyway, because the failure
	// it prevents is the reaper SIGTERMing the test binary at teardown, and
	// nothing else in the process would survive to report it.
	if pid <= 0 || pid == os.Getpid() {
		return
	}
	if !adoptionPIDIsDoltSQLServer(pid) {
		return
	}
	registerManagedDoltTestProcess(managedDoltStartedProcess{
		CityPath: env["GC_CITY_PATH"],
		PID:      pid,
	})
}

// managedDoltPIDFromProviderPIDFile reads the PID a provider op recorded at
// path, returning 0 when there is nothing readable there.
//
// managedPIDFromPIDFile (dolt_process_inspection.go) parses the same file but
// deletes it when the PID is dead or unparseable. That belongs to the code
// that owns the managed server's lifecycle, not here: adoption runs after
// every provider op including "stop", and a passive observer that removes the
// provider's own state file changes what the next op sees.
func managedDoltPIDFromProviderPIDFile(path string) int {
	if strings.TrimSpace(path) == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

// adoptionPIDIsDoltSQLServer is the confirmation seam. Substituting it lets a
// test drive the adoption path without a process wearing dolt's argv, so the
// suite spawns nothing to prove the wiring. The real function stays covered
// where it matters: TestProviderOpAdoptsRealScriptSpawnedDolt runs it against
// a genuine managed server, and a direct test pins its refusal of a live
// non-dolt process.
var adoptionPIDIsDoltSQLServer = pidIsDoltSQLServer

// pidIsDoltSQLServer reports whether pid is a live process whose argv is a
// `dolt sql-server` invocation. A dead PID, an unrelated process, and a host
// that cannot answer all return false.
func pidIsDoltSQLServer(pid int) bool {
	if pid <= 0 {
		return false
	}
	if procfsAvailable() {
		_, ok := readDoltSQLServerArgv(pid)
		return ok
	}
	return psArgvIsDoltSQLServer(pid)
}

// procfsAvailable reports whether this host exposes a Linux-style /proc.
//
// It probes /proc/self/cmdline rather than listing /proc: the check runs per
// provider op, and a directory listing on a busy host walks thousands of
// entries to answer a question a single stat settles. Naming a file inside
// /proc/self also means a plain directory that happens to be called /proc
// cannot satisfy it.
func procfsAvailable() bool {
	_, err := os.Stat("/proc/self/cmdline")
	return err == nil
}

// psArgvIsDoltSQLServer answers pidIsDoltSQLServer on hosts without /proc
// (Darwin), mirroring the per-PID ps call readProcStartIdentity already makes
// there. Splitting ps output on whitespace misreads an executable path
// containing spaces; that costs a missed adoption, never a wrong one, because
// the mangled argv[0] cannot match "dolt".
func psArgvIsDoltSQLServer(pid int) bool {
	ctx, cancel := context.WithTimeout(context.Background(), procEnumerationTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return false
	}
	return looksLikeDoltSQLServer(strings.Fields(strings.TrimSpace(string(out))))
}
