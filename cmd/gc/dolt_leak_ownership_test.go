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
// The second identifier is the env tag the provider lifecycle already
// projects into every managed-dolt child in test mode:
// GC_MANAGED_DOLT_TEST_MODE=1 together with GC_MANAGED_DOLT_TEST_PARENT_PID
// (providerLifecycleProcessEnvFromBase in beads_provider_lifecycle.go). A
// process carrying THIS test binary's PID in that pair was spawned by this
// test binary's managed-dolt plumbing wherever its config landed.
//
// The tag answers TWO questions, and they are not the same question. Ownership
// -- the tag names US -- scopes the assertion. Orphanhood -- the tag names a
// binary that is GONE -- scopes the startup sweep, which reclaims a server no
// later run will ever be able to blame on anyone. The ci-9r6x specimen needed
// both: the run that leaked it should have failed, and every run after it
// should have collected it.
//
// One case is neither, and it is declared rather than inferred: a helper
// binary that starts a server and exits so its CALLER can watch production
// supervision with the spawner gone. See handOffManagedDoltToParentProcess
// (path_helpers_test.go) for why that exemption is per-PID.
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
// tags this test binary as its managed-dolt test parent.
//
// An unreadable /proc entry is not ownership. EACCES on another user's process
// is the normal case on a shared host, and a dead PID is the normal case in a
// scan that walks a snapshot -- treating either as "ours" would put a stranger
// in range of the reap, so the failure direction is fixed here and not left to
// callers.
func managedDoltProcessOwnedByThisTestBinary(pid int) bool {
	return managedDoltProcessOwnedBy(pid, readProcEnviron, os.Getpid())
}

// orphanedManagedDoltFromDeadTestBinary is the startup sweep's binding of the
// rule below to this host: read the real /proc, compare against this PID, and
// ask the kernel whether the tagging binary is still alive.
func orphanedManagedDoltFromDeadTestBinary(pid int) bool {
	return managedDoltProcessOrphanedByDeadTestBinary(pid, readProcEnviron, os.Getpid(), pidAlive)
}

// managedDoltProcessHasLiveTestBinaryParent reports whether a managed-dolt
// process is still owned by a live test binary, including a sibling binary
// running concurrently in another cmd/gc test shard.
func managedDoltProcessHasLiveTestBinaryParent(pid int) bool {
	parent := managedDoltTestParentPIDOf(pid, readProcEnviron)
	return parent > 0 && pidAlive(parent)
}

// managedDoltProcessOwnedBy is the injectable core. Unit tests supply a
// scripted reader so the matching rule can be pinned without spawning
// anything; the tests that DO spawn a real child exist to prove
// /proc/<pid>/environ is actually readable here, which a scripted reader
// cannot tell you.
func managedDoltProcessOwnedBy(pid int, readEnviron func(int) ([]byte, error), wantParentPID int) bool {
	if wantParentPID <= 0 {
		return false
	}
	return managedDoltTestParentPIDOf(pid, readEnviron) == wantParentPID
}

// managedDoltProcessOrphanedByDeadTestBinary reports whether pid is a tagged
// managed dolt whose test binary is GONE. That is the startup sweep's
// question and it is NOT the assertion's: a server whose run has ended can no
// longer fail anything, so nothing else will ever reclaim it, wherever its
// config landed. The ci-9r6x specimen is exactly this shape -- orphaned at a
// config path in the source tree, which no temp-root rule can reach and which
// therefore survived every later run.
//
// Fail-closed on PID reuse: a recycled parent number reads as alive and the
// process is left alone, costing a missed reap rather than a wrong kill. Our
// own PID is excluded explicitly. The sweep runs at startup, before this
// binary has started anything, so the case cannot arise today -- the
// exclusion is what stops a later reordering from making the sweep reap the
// run that invoked it.
func managedDoltProcessOrphanedByDeadTestBinary(pid int, readEnviron func(int) ([]byte, error), selfPID int, alive func(int) bool) bool {
	parent := managedDoltTestParentPIDOf(pid, readEnviron)
	if parent <= 0 || parent == selfPID {
		return false
	}
	return !alive(parent)
}

// managedDoltTestParentPIDOf returns the PID that pid's environment names as
// its managed-dolt test parent, or 0 when pid carries no such tag. Every
// failure -- an unreadable environ, a missing variable, an unparseable value
// -- returns 0, which denies ownership AND staleness, so the two callers can
// only under-claim.
func managedDoltTestParentPIDOf(pid int, readEnviron func(int) ([]byte, error)) int {
	if pid <= 0 {
		return 0
	}
	data, err := readEnviron(pid)
	if err != nil {
		return 0
	}
	return managedDoltTestParentPIDFromEnviron(data)
}

// managedDoltTestParentPIDFromEnviron reads the managed-dolt test tag out of a
// NUL-separated /proc/<pid>/environ blob.
//
// BOTH variables are required, not the PID one alone. Every producer writes
// them as a pair or not at all -- providerLifecycleProcessEnvFromBase strips
// both, then appends both only for a real test binary
// (beads_provider_lifecycle.go) -- so demanding the pair costs nothing here
// and keeps a process that merely inherited a stray
// GC_MANAGED_DOLT_TEST_PARENT_PID from an operator's shell out of reach of a
// guard that SIGTERMs what it reports.
//
// The PID is parsed as a number rather than string-compared, so it agrees
// with managedDoltTestParentPID(), which is what wrote it. Both names are
// matched whole: a prefix match would let an unrelated
// GC_MANAGED_DOLT_TEST_PARENT_PID_* variable aim that signal at a process this
// binary never started. Last occurrence wins, as it does for the kernel's own
// getenv.
func managedDoltTestParentPIDFromEnviron(data []byte) int {
	parent := 0
	tagged := false
	for _, entry := range strings.Split(string(data), "\x00") {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		switch name {
		case managedDoltTestModeEnv:
			tagged = strings.TrimSpace(value) == "1"
		case managedDoltTestParentPIDEnv:
			pid, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || pid <= 0 {
				parent = 0
				continue
			}
			parent = pid
		}
	}
	if !tagged {
		return 0
	}
	return parent
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
