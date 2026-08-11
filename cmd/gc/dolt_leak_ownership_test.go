package main

// Ownership scoping for the cmd/gc test dolt leak guard (bead ci-u3i2).
//
// The guard in path_helpers_test.go answered "is this server ours?" with the
// --config path alone: keep the process only when its config lives under the
// per-run temp root. A managed dolt whose config lives anywhere else was
// invisible to it, and that was observed rather than argued -- under ci-9r6x,
// pid 1190160, dolt sql-server --config
// <repo>/cmd/gc/.gc/runtime/packs/dolt/dolt-config.yaml, alive 7h50m across
// every run in that window with the guard green throughout. A test that
// reaches a provider op without redirecting the runtime dir writes its config
// into the source tree, never into the temp root the scan scoped to.
//
// The second identifier is the env marker the provider lifecycle already
// projects into every managed-dolt child in test mode:
// GC_MANAGED_DOLT_TEST_PARENT_PID (providerLifecycleProcessEnvFromBase in
// beads_provider_lifecycle.go). A process carrying THIS test binary's PID in
// that variable was spawned by this test binary's managed-dolt plumbing
// wherever its config landed.
//
// OWNERSHIP ALONE IS NOT THE RULE, and that is the load-bearing decision
// here. Ownership is unioned with the config-path scope but intersected with
// the argv check the guard already applied through discoverDoltProcesses,
// because the marker is inherited by every descendant of a provider op, not
// just the server. Measured on a full cmd/gc run 2026-08-10, an argv-blind
// ownership scan reported three leaks, none of them a dolt server:
//
//	pid=2428399 argv="sleep 600"
//	pid=2428466 argv="sleep 600"
//	pid=2443095 argv="sleep 30"
//
// Those are run_with_timeout watchdogs from gc-beads-bd.sh, which outlive the
// command they time. The same run broke the three TestManagedDoltScopeWatchdog
// tests, whose helper binary deliberately leaves its watchdog running for the
// parent to inspect -- an argv-blind guard inside that helper calls its own
// fixture a leak. Neither is a dolt server holding a data dir, which is the
// leak this guard exists for.
//
// Rejected: widening looksLikeDoltSQLServer so a shebang argv counts. That
// predicate is production code -- `gc dolt-state` reaps against it -- so
// teaching it that `/bin/sh .../dolt sql-server` names a dolt server would put
// an operator's unrelated shell script in range of a SIGKILL to fix a
// test-only blindness. The fake is made recognizable at its source instead;
// see writeFakeDoltSQLServer (dolt_start_managed_test.go).
//
// Rejected: turning the guard into a cleanup path. It is an ASSERTION -- it
// fails the package when a test did not stop its own server -- and ci-9r6x
// deliberately kept it one. This widens what the assertion can see; it does
// not soften the assertion.
//
// ABSENT ON DARWIN. /proc/<pid>/environ is Linux-only. macOS exposes another
// process's environment only through `ps -E`, which needs root for anything
// but your own processes and truncates the block at the argument-list limit,
// so a scan built on it would be silently partial rather than reliably empty.
// Where /proc cannot answer, ownership reports false and the guard keeps
// exactly the config-path scope it had before -- which is why
// snapshotGuardedDoltProcesses unions the two scopes rather than replacing
// one with the other. A Darwin equivalent needs a different signal (process
// group, or a marker file the provider script writes); nothing here is a
// starting point for one.
//
// These invariants are pinned by dolt_leak_ownership_scan_test.go.

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// managedDoltProcessOwnedByThisTestBinary reports whether pid's environment
// names this test binary in GC_MANAGED_DOLT_TEST_PARENT_PID.
//
// An unreadable /proc entry is not ownership. EACCES on another user's process
// is the normal case on a shared host, and a dead PID is the normal case in a
// scan that walks a snapshot -- treating either as "ours" would put a stranger
// in range of the reap, so the failure direction is fixed here and not left to
// callers.
func managedDoltProcessOwnedByThisTestBinary(pid int) bool {
	return managedDoltProcessOwnedBy(pid, readProcEnviron, os.Getpid())
}

// managedDoltProcessOwnedBy is the injectable core. Unit tests supply a
// scripted reader so the matching rule can be pinned without spawning
// anything; the tests that DO spawn a real child exist to prove
// /proc/<pid>/environ is actually readable here, which a scripted reader
// cannot tell you.
func managedDoltProcessOwnedBy(pid int, readEnviron func(int) ([]byte, error), wantParentPID int) bool {
	if pid <= 0 || wantParentPID <= 0 {
		return false
	}
	data, err := readEnviron(pid)
	if err != nil {
		return false
	}
	return environNamesManagedDoltTestParent(data, wantParentPID)
}

// environNamesManagedDoltTestParent reports whether a NUL-separated
// /proc/<pid>/environ blob sets GC_MANAGED_DOLT_TEST_PARENT_PID to want.
//
// The value is parsed as a number rather than string-compared so it agrees
// with managedDoltTestParentPID(), which is what wrote it. The name is matched
// whole: a prefix match would let an unrelated GC_MANAGED_DOLT_TEST_PARENT_PID_*
// variable aim the reap's SIGTERM at a process this binary never started.
func environNamesManagedDoltTestParent(data []byte, want int) bool {
	for _, entry := range strings.Split(string(data), "\x00") {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name != managedDoltTestParentPIDEnv {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			continue
		}
		if pid == want {
			return true
		}
	}
	return false
}

// readProcEnviron reads /proc/<pid>/environ through the same deadline the
// discovery walk uses, so a process wedged in uninterruptible sleep cannot
// hang package teardown.
func readProcEnviron(pid int) ([]byte, error) {
	return readWithTimeout(filepath.Join("/proc", strconv.Itoa(pid), "environ"))
}

// ownershipScanSupportedGOOS reports whether this GOOS is one where the
// ownership check is meant to work at all.
//
// Deliberately NOT procfsAvailable() (dolt_test_process_adoption.go), which
// asks whether /proc answers right now. Tests gate on the GOOS so that a Linux
// host with an unreadable /proc FAILS the ownership tests instead of skipping
// them -- an unreadable /proc there is the fault, and a skip would report it
// as a clean run.
func ownershipScanSupportedGOOS() bool {
	return runtime.GOOS == "linux"
}
