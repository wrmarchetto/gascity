package main

// Tests for the ownership scope of the cmd/gc test dolt leak guard
// (dolt_leak_ownership_test.go, bead ci-u3i2).
//
// Scope: the ownership rule itself, its union with the older config-path scope
// in snapshotGuardedDoltProcesses, the startup sweep's tag arm, the handoff
// exemption, and the fake dolt's visibility to process discovery. The guard's
// other behavior -- the initial/final diff, reap ordering, the per-test
// requireNoLeakedDoltAfter form -- is pinned in dolt_leak_helper_test.go and
// is not re-tested here.
//
// One test spawns a real child. That is deliberate and not interchangeable
// with the scripted ones: a scripted reader proves the matching rule, but only
// a real child proves /proc/<pid>/environ is readable on this host, and a
// check that silently reads nothing looks exactly like a clean run.
//
//	go test ./cmd/gc/ -run 'ManagedDolt|SnapshotGuardedDolt|DoltLeakGuard|StaleCmdGCTestDolt|FakeDoltSQLServer|HandOffManagedDolt'

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func doltProc(pid int, configPath string) DoltProcInfo {
	return DoltProcInfo{PID: pid, Argv: []string{"dolt", "sql-server", "--config", configPath}}
}

func TestManagedDoltProcessOwnedByThisTestBinaryReadsRealProcEnviron(t *testing.T) {
	if !ownershipScanSupportedGOOS() {
		t.Skip("ownership check reads /proc/<pid>/environ; absent on this GOOS by design")
	}
	// `sleep` rather than a dolt fake on purpose: this test is about the
	// /proc read, and using a process that could satisfy the argv check
	// would let an argv-based answer pass for an environ-based one.
	marked := exec.Command("sleep", "120")
	// Both tag variables explicitly, though TestMain already exports the mode
	// one into this binary's environment. Inheriting it would make the test
	// pass without the child carrying a complete tag, which is the one thing
	// the reader is supposed to insist on.
	marked.Env = append(os.Environ(),
		managedDoltTestModeEnv+"=1",
		managedDoltTestParentPIDEnv+"="+strconv.Itoa(os.Getpid()),
	)
	if err := marked.Start(); err != nil {
		t.Fatalf("start marked sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = marked.Process.Kill()
		_, _ = marked.Process.Wait()
	})

	// The child exists before Start returns but the kernel populates
	// /proc/<pid>/environ at exec, so poll instead of sampling once.
	deadline := time.Now().Add(5 * time.Second)
	owned := false
	for time.Now().Before(deadline) && !owned {
		owned = managedDoltProcessOwnedByThisTestBinary(marked.Process.Pid)
		if !owned {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if !owned {
		t.Fatalf("pid %d not recognized as owned; /proc/<pid>/environ is unreadable here and the ownership scope is dead weight", marked.Process.Pid)
	}

	unmarked := exec.Command("sleep", "120")
	unmarked.Env = []string{"PATH=" + os.Getenv("PATH")}
	if err := unmarked.Start(); err != nil {
		t.Fatalf("start unmarked sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = unmarked.Process.Kill()
		_, _ = unmarked.Process.Wait()
	})
	time.Sleep(200 * time.Millisecond)
	if managedDoltProcessOwnedByThisTestBinary(unmarked.Process.Pid) {
		t.Fatalf("pid %d claimed as owned with no marker in its environment", unmarked.Process.Pid)
	}
}

// environBlob builds a NUL-separated /proc/<pid>/environ blob from entries.
func environBlob(entries ...string) []byte {
	return []byte(strings.Join(entries, "\x00") + "\x00")
}

// taggedEnviron builds a blob carrying the full managed-dolt test tag. It
// writes BOTH variables because the reader requires both, and a helper that
// emitted only the PID one would make every caller below agree with a reader
// that had silently lost the mode check.
func taggedEnviron(parentPID string, extra ...string) []byte {
	return environBlob(append([]string{
		managedDoltTestModeEnv + "=1",
		managedDoltTestParentPIDEnv + "=" + parentPID,
	}, extra...)...)
}

func TestManagedDoltProcessOwnedByRejectsUnreadableAndForeignMarkers(t *testing.T) {
	environ := func(pid int) ([]byte, error) {
		switch pid {
		case 11:
			return taggedEnviron("4242", "PATH=/usr/bin"), nil
		case 12:
			// A sibling test binary's child. Adopting it would let one
			// package's teardown SIGTERM another package's fixture.
			return taggedEnviron("4243"), nil
		case 13:
			return environBlob("PATH=/usr/bin"), nil
		}
		// Unreadable: EACCES on another user's process, or a PID that
		// exited mid-scan. Neither is ownership.
		return nil, os.ErrPermission
	}

	for pid, want := range map[int]bool{11: true, 12: false, 13: false, 14: false} {
		if got := managedDoltProcessOwnedBy(pid, environ, 4242); got != want {
			t.Errorf("managedDoltProcessOwnedBy(%d) = %v, want %v", pid, got, want)
		}
	}
}

// TestManagedDoltTestParentPIDFromEnvironRequiresBothTagVariables pins the
// tag rule the ownership scope and the startup sweep both rest on.
//
// The mode-only and pid-only rows are the point of the table. They separate
// "the provider lifecycle tagged this server" from "this process inherited one
// stray variable", and a reader answering on either alone would hand a guard
// that SIGTERMs what it reports a real server started from an operator's
// shell that happened to export GC_MANAGED_DOLT_TEST_PARENT_PID.
func TestManagedDoltTestParentPIDFromEnvironRequiresBothTagVariables(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    int
	}{
		{
			name:    "both variables",
			entries: []string{"PATH=/usr/bin", managedDoltTestModeEnv + "=1", managedDoltTestParentPIDEnv + "=1934922"},
			want:    1934922,
		},
		{
			name:    "mode without parent pid",
			entries: []string{managedDoltTestModeEnv + "=1"},
			want:    0,
		},
		{
			name:    "parent pid without mode",
			entries: []string{managedDoltTestParentPIDEnv + "=1934922"},
			want:    0,
		},
		{
			name:    "mode disabled",
			entries: []string{managedDoltTestModeEnv + "=0", managedDoltTestParentPIDEnv + "=1934922"},
			want:    0,
		},
		{
			name:    "unparseable parent pid",
			entries: []string{managedDoltTestModeEnv + "=1", managedDoltTestParentPIDEnv + "=not-a-pid"},
			want:    0,
		},
		{
			// A name that merely STARTS with the marker's name must not
			// match: the guard signals what it reports, so a loose
			// comparison aims that signal at an unrelated process.
			name:    "parent pid name prefix only",
			entries: []string{managedDoltTestModeEnv + "=1", managedDoltTestParentPIDEnv + "_SHADOW=99"},
			want:    0,
		},
		{
			name:    "no gc variables at all",
			entries: []string{"PATH=/usr/bin", "HOME=/home/dev"},
			want:    0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blob := environBlob(tc.entries...)
			if got := managedDoltTestParentPIDFromEnviron(blob); got != tc.want {
				t.Fatalf("managedDoltTestParentPIDFromEnviron(%q) = %d, want %d", blob, got, tc.want)
			}
		})
	}
}

func TestSnapshotGuardedDoltProcessesKeepsOwnedServerOutsideConfigRoot(t *testing.T) {
	root := filepath.Join("/tmp", "gct4242-current")
	// The specimen ci-9r6x observed alive for 7h50m: a real managed dolt
	// whose config landed in the source tree, so the config-root scope
	// dropped it and the guard stayed green.
	outsideRoot := doltProc(1001, "/home/dev/gascity/cmd/gc/.gc/runtime/packs/dolt/dolt-config.yaml")

	got, err := snapshotGuardedDoltProcesses(
		func() ([]DoltProcInfo, error) { return []DoltProcInfo{outsideRoot}, nil },
		root,
		func(pid int) bool { return pid == 1001 },
	)
	if err != nil {
		t.Fatalf("snapshotGuardedDoltProcesses: %v", err)
	}
	if len(got) != 1 || got[1001].PID != 1001 {
		t.Fatalf("snapshot = %#v, want the owned server outside the config root", got)
	}
}

func TestSnapshotGuardedDoltProcessesExcludesUnownedServerOutsideConfigRoot(t *testing.T) {
	root := filepath.Join("/tmp", "gct4242-current")
	// The operator's own city dolt: outside the temp root, no marker.
	// Widening the scope must not put it in range of the reap.
	unowned := doltProc(2001, "/home/dev/city/.gc/runtime/packs/dolt/dolt-config.yaml")

	got, err := snapshotGuardedDoltProcesses(
		func() ([]DoltProcInfo, error) { return []DoltProcInfo{unowned}, nil },
		root,
		func(int) bool { return false },
	)
	if err != nil {
		t.Fatalf("snapshotGuardedDoltProcesses: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("snapshot = %#v, want empty; an unmarked server outside the temp root is not ours", got)
	}
}

func TestSnapshotGuardedDoltProcessesKeepsUnownedServerUnderConfigRoot(t *testing.T) {
	// The pre-ci-u3i2 rule still stands on its own. A server under the temp
	// root whose environment carries no marker -- one started outside a
	// provider op, or whose marker was stripped -- must stay in scope.
	root := filepath.Join("/tmp", "gct4242-current")
	underRoot := doltProc(3001, filepath.Join(root, "TestCase", ".gc", "runtime", "packs", "dolt", "dolt-config.yaml"))

	got, err := snapshotGuardedDoltProcesses(
		func() ([]DoltProcInfo, error) { return []DoltProcInfo{underRoot}, nil },
		root,
		func(int) bool { return false },
	)
	if err != nil {
		t.Fatalf("snapshotGuardedDoltProcesses: %v", err)
	}
	if len(got) != 1 || got[3001].PID != 3001 {
		t.Fatalf("snapshot = %#v, want the unmarked server under the config root", got)
	}
}

func TestSnapshotGuardedDoltProcessesNeverReportsTheTestBinaryItself(t *testing.T) {
	// The guard SIGTERMs everything it reports. A test binary launched by an
	// outer harness inherits the marker, so without this it would scan itself
	// into the leak list and kill the process about to print the report.
	self := DoltProcInfo{PID: os.Getpid(), Argv: []string{"dolt", "sql-server", "--config", "/tmp/gct4242-current/x.yaml"}}

	got, err := snapshotGuardedDoltProcesses(
		func() ([]DoltProcInfo, error) { return []DoltProcInfo{self}, nil },
		filepath.Join("/tmp", "gct4242-current"),
		func(int) bool { return true },
	)
	if err != nil {
		t.Fatalf("snapshotGuardedDoltProcesses: %v", err)
	}
	if _, ok := got[os.Getpid()]; ok {
		t.Fatalf("snapshot contains this test binary (pid %d)", os.Getpid())
	}
}

func TestDoltLeakGuardOwnedServerOutsideConfigRootFailsThePackage(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "gct12345-current")
	// Not under the temp root: invisible to the pre-ci-u3i2 guard, which is
	// what this test exists to keep from regressing.
	leaked := doltProc(1001, "/home/dev/gascity/cmd/gc/.gc/runtime/packs/dolt/dolt-config.yaml")

	var scan int
	var reapedLeaks []DoltProcInfo
	g := newDoltLeakGuardedTestingM(nil, tempRoot)

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) {
			scan++
			if scan == 1 {
				return nil, nil
			}
			return []DoltProcInfo{leaked}, nil
		},
		func(pid int) bool { return pid == 1001 },
		func(int) bool { return false },
		func(string) bool { return false },
		func() {},
		func() {},
		func(leaked []DoltProcInfo) { reapedLeaks = append(reapedLeaks, leaked...) },
	)

	if code != 1 {
		t.Fatalf("guard returned code %d, want 1 for a server owned by this test binary", code)
	}
	if len(reapedLeaks) != 1 || reapedLeaks[0].PID != leaked.PID {
		t.Fatalf("reaped leaks = %#v, want only PID %d", reapedLeaks, leaked.PID)
	}
}

func TestDoltLeakGuardIgnoresOwnedServerAlreadyRunningAtStartup(t *testing.T) {
	// PID reuse: a previous test binary's marked child can still be alive on
	// a PID this run now claims as its own. The initial/final diff is what
	// keeps that off this run's bill, so it must cover the ownership scope
	// and not only the config-path scope.
	tempRoot := filepath.Join(t.TempDir(), "gct12345-current")
	preexisting := doltProc(1001, "/home/dev/gascity/cmd/gc/.gc/runtime/packs/dolt/dolt-config.yaml")

	var reapedLeaks []DoltProcInfo
	g := newDoltLeakGuardedTestingM(nil, tempRoot)

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) { return []DoltProcInfo{preexisting}, nil },
		func(pid int) bool { return pid == 1001 },
		func(int) bool { return false },
		func(string) bool { return false },
		func() {},
		func() {},
		func(leaked []DoltProcInfo) { reapedLeaks = append(reapedLeaks, leaked...) },
	)

	if code != 0 {
		t.Fatalf("guard returned code %d, want 0: the process predates this run", code)
	}
	if len(reapedLeaks) != 0 {
		t.Fatalf("reaped %#v; a process this run did not start must not be signaled", reapedLeaks)
	}
}

// TestFakeDoltSQLServerIsVisibleToDoltProcessDiscovery pins gap 2 of ci-u3i2
// at its source. The guard, the per-test requireNoLeakedDoltAfter form, the
// production reaper and ci-9r6x's adoption all decide "is this a dolt server"
// through looksLikeDoltSQLServer, so a fake whose argv that predicate rejects
// is outside every one of them by construction -- and a test that forgets to
// stop its fake looks exactly like a test that stopped it.
//
// The assertion is against discoverDoltProcesses rather than against
// looksLikeDoltSQLServer directly: the predicate reads argv, and only the
// enumerator proves the argv reaching /proc is the one the predicate accepts.
func TestFakeDoltSQLServerIsVisibleToDoltProcessDiscovery(t *testing.T) {
	if !ownershipScanSupportedGOOS() {
		t.Skip("discovery walks /proc; absent on this GOOS by design")
	}
	configPath := filepath.Join(t.TempDir(), "dolt-config.yaml")
	cmd := exec.Command(filepath.Join(writeFakeDoltSQLServer(t), "dolt"), "sql-server", "--config", configPath)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake dolt: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		procs, err := discoverDoltProcesses()
		if err != nil {
			t.Fatalf("discoverDoltProcesses: %v", err)
		}
		for _, proc := range procs {
			if proc.PID != cmd.Process.Pid {
				continue
			}
			if got := extractConfigPath(proc.Argv); got != configPath {
				t.Fatalf("discovered config path %q, want %q -- the guard scopes on this", got, configPath)
			}
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("fake dolt pid %d never appeared in discoverDoltProcesses; a leaked fake stays invisible to the leak guard", cmd.Process.Pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFakeDoltSQLServerRefusesArgsThatAreNotSQLServer(t *testing.T) {
	// The fake stands in for a server, so it must REFUSE anything else
	// rather than succeed quietly: a test that mistypes the subcommand
	// would otherwise get a clean exit and prove nothing.
	out, err := exec.Command(filepath.Join(writeFakeDoltSQLServer(t), "dolt"), "gc").CombinedOutput()
	if err == nil {
		t.Fatalf("fake dolt accepted a non-sql-server invocation; output: %s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
		t.Fatalf("fake dolt exit = %v, want exit status 2; output: %s", err, out)
	}
}

// TestStaleCmdGCTestDoltSweepClaimsTaggedOrphanFromDeadTestBinary pins the
// startup sweep's tag arm: a tagged server whose test binary is gone is an
// orphan nothing else will ever reclaim, wherever its config lives. This is
// the half of the guard that would have collected ci-9r6x's 7h50m specimen
// instead of only failing the run that produced it.
func TestStaleCmdGCTestDoltSweepClaimsTaggedOrphanFromDeadTestBinary(t *testing.T) {
	const deadParent = 1934922
	environ := func(int) ([]byte, error) { return taggedEnviron(strconv.Itoa(deadParent)), nil }
	alive := func(pid int) bool { return pid != deadParent }

	if !managedDoltProcessOrphanedByDeadTestBinary(4103, environ, os.Getpid(), alive) {
		t.Fatal("tagged orphan whose test binary is gone was not classified stale")
	}
}

// TestStaleCmdGCTestDoltSweepSkipsTaggedServerWithLiveTestBinary pins the
// fail-closed direction. A live parent PID also covers PID reuse -- the number
// was recycled by something unrelated -- and the sweep must give up the reap
// rather than risk signaling a running sibling's server.
func TestStaleCmdGCTestDoltSweepSkipsTaggedServerWithLiveTestBinary(t *testing.T) {
	environ := func(int) ([]byte, error) { return taggedEnviron("1934922"), nil }

	if managedDoltProcessOrphanedByDeadTestBinary(4104, environ, os.Getpid(), func(int) bool { return true }) {
		t.Fatal("tagged server with a live test binary was classified stale")
	}
}

// TestStaleCmdGCTestDoltSweepSkipsServerTaggedByThisTestBinary pins the
// self-reap exclusion, which no other test can reach: a dead-parent check
// cannot fire for our own PID while we are running, so only this assertion
// records that the sweep must never treat its own run's server as an orphan.
func TestStaleCmdGCTestDoltSweepSkipsServerTaggedByThisTestBinary(t *testing.T) {
	environ := func(int) ([]byte, error) { return taggedEnviron(strconv.Itoa(os.Getpid())), nil }

	if managedDoltProcessOrphanedByDeadTestBinary(4105, environ, os.Getpid(), func(int) bool { return false }) {
		t.Fatal("this run's own server was classified stale by the startup sweep")
	}
}

// TestStaleCmdGCTestDoltSweepIgnoresUntaggedServerOutsideTestRoots pins that
// the tag arm claims nothing on its own: an operator's real city server,
// untagged and outside every test root, must stay invisible to the sweep even
// with every PID on the host reported dead.
func TestStaleCmdGCTestDoltSweepIgnoresUntaggedServerOutsideTestRoots(t *testing.T) {
	environ := func(int) ([]byte, error) { return environBlob("PATH=/usr/bin", "HOME=/home/dev"), nil }

	if managedDoltProcessOrphanedByDeadTestBinary(4106, environ, os.Getpid(), func(int) bool { return false }) {
		t.Fatal("untagged server outside every test root was classified stale")
	}
}

// TestStaleCmdGCTestDoltSweepHandsTaggedOrphanToTheReaper is the wiring pin.
// The sweep the guard actually calls has to consult the tag arm, not only the
// config-path one; without this the rule above can be correct and never
// referenced, with every other test in this file still green.
func TestStaleCmdGCTestDoltSweepHandsTaggedOrphanToTheReaper(t *testing.T) {
	// Outside the guard's temp parent on purpose, so the pre-existing path
	// arm cannot be what answers. Asserted rather than assumed below: that
	// is the way this test could go vacuous.
	orphan := doltProc(4107, "/home/dev/gascity/cmd/gc/.gc/runtime/packs/dolt/dolt-config.yaml")
	tempRoot := filepath.Join(t.TempDir(), "gct12345-current")
	g := newDoltLeakGuardedTestingM(nil, tempRoot)
	if isStaleCmdGCTestConfigPath(extractConfigPath(orphan.Argv), cmdGCTestActiveRoots(tempRoot), filepath.Dir(filepath.Clean(tempRoot))) {
		t.Fatal("config-path arm claimed the orphan; the tag arm is no longer what this test exercises")
	}

	var reaped []DoltProcInfo
	swept := g.sweepStaleCmdGCTestDoltProcessesWith(
		"test",
		func() ([]DoltProcInfo, error) { return []DoltProcInfo{orphan}, nil },
		func(pid int) bool { return pid == 4107 },
		func(procs []DoltProcInfo) { reaped = append(reaped, procs...) },
	)

	if !swept {
		t.Fatal("startup sweep reported nothing swept for a tagged orphan")
	}
	if len(reaped) != 1 || reaped[0].PID != 4107 {
		t.Fatalf("reaped = %#v, want the tagged orphan PID 4107", reaped)
	}
}

// TestDoltLeakGuardExemptsOnlyTheServerHandedToTheParentProcess pins both
// halves of the handoff at once, and the second half is why they share a test:
// an exemption that widened to the whole run would still satisfy an assertion
// that only checked the declared PID went unreported.
func TestDoltLeakGuardExemptsOnlyTheServerHandedToTheParentProcess(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "gct12345-current")
	handedOff := doltProc(1001, filepath.Join(tempRoot, "handed-off", "dolt-config.yaml"))
	forgotten := doltProc(1002, filepath.Join(tempRoot, "forgotten", "dolt-config.yaml"))

	var scan int
	var reapedLeaks []DoltProcInfo
	g := newDoltLeakGuardedTestingM(nil, tempRoot)

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) {
			scan++
			if scan == 1 {
				return nil, nil
			}
			return []DoltProcInfo{handedOff, forgotten}, nil
		},
		func(int) bool { return false },
		func(pid int) bool { return pid == 1001 },
		func(string) bool { return false },
		func() {},
		func() {},
		func(leaked []DoltProcInfo) { reapedLeaks = append(reapedLeaks, leaked...) },
	)

	if code != 1 {
		t.Fatalf("guard returned code %d, want 1: an undeclared server outlived the package", code)
	}
	if len(reapedLeaks) != 1 || reapedLeaks[0].PID != forgotten.PID {
		t.Fatalf("reaped leaks = %#v, want only the undeclared PID %d", reapedLeaks, forgotten.PID)
	}
}

// TestDoltLeakGuardPassesWhenEveryLeftoverServerWasHandedOff is the other
// direction: the run a helper binary actually performs, where the only server
// still up is the one its caller is about to stop.
func TestDoltLeakGuardPassesWhenEveryLeftoverServerWasHandedOff(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "gct12345-current")
	handedOff := doltProc(1001, filepath.Join(tempRoot, "handed-off", "dolt-config.yaml"))

	var scan int
	var reapedLeaks []DoltProcInfo
	g := newDoltLeakGuardedTestingM(nil, tempRoot)

	code := g.runWith(
		func() int { return 0 },
		func() ([]DoltProcInfo, error) {
			scan++
			if scan == 1 {
				return nil, nil
			}
			return []DoltProcInfo{handedOff}, nil
		},
		func(int) bool { return false },
		func(pid int) bool { return pid == 1001 },
		func(string) bool { return false },
		func() {},
		func() {},
		func(leaked []DoltProcInfo) { reapedLeaks = append(reapedLeaks, leaked...) },
	)

	if code != 0 {
		t.Fatalf("guard returned code %d, want 0: the only server left was declared a handoff", code)
	}
	if len(reapedLeaks) != 0 {
		t.Fatalf("reaped %#v; a declared handoff must not be signaled -- its caller is still going to stop it", reapedLeaks)
	}
}

// TestHandOffManagedDoltToParentProcessRecordsOnlyRealPIDs pins the registry
// the helper writes into. A non-positive PID is what a failed start hands
// back, and recording it would exempt every future scan entry the map is asked
// about with that key.
func TestHandOffManagedDoltToParentProcessRecordsOnlyRealPIDs(t *testing.T) {
	handOffManagedDoltToParentProcess(0)
	handOffManagedDoltToParentProcess(-1)
	if handedOffManagedDoltProcess(0) || handedOffManagedDoltProcess(-1) {
		t.Fatal("a non-positive PID was recorded as a handoff")
	}

	// A PID this binary will never see in a scan: the registry has no
	// per-test reset, so a real one could collide with a sibling's fixture.
	const pid = 1 << 30
	if handedOffManagedDoltProcess(pid) {
		t.Fatalf("PID %d was already declared a handoff before this test declared it", pid)
	}
	handOffManagedDoltToParentProcess(pid)
	if !handedOffManagedDoltProcess(pid) {
		t.Fatalf("PID %d was not recorded as a handoff", pid)
	}
}
