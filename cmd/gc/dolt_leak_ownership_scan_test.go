package main

// Tests for the ownership scope of the cmd/gc test dolt leak guard
// (dolt_leak_ownership_test.go, bead ci-u3i2).
//
// Scope: the ownership rule itself, and its union with the older config-path
// scope in snapshotGuardedDoltProcesses. The guard's other behavior -- the
// initial/final diff, reap ordering, the per-test requireNoLeakedDoltAfter
// form -- is pinned in dolt_leak_helper_test.go and is not re-tested here.
//
// One test spawns a real child. That is deliberate and not interchangeable
// with the scripted ones: a scripted reader proves the matching rule, but only
// a real child proves /proc/<pid>/environ is readable on this host, and a
// check that silently reads nothing looks exactly like a clean run.
//
//	go test ./cmd/gc/ -run 'ManagedDoltProcessOwned|EnvironNamesManagedDolt|SnapshotGuardedDolt|DoltLeakGuard'

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	marked.Env = append(os.Environ(), managedDoltTestParentPIDEnv+"="+strconv.Itoa(os.Getpid()))
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

func TestManagedDoltProcessOwnedByRejectsUnreadableAndForeignMarkers(t *testing.T) {
	environ := func(pid int) ([]byte, error) {
		switch pid {
		case 11:
			return []byte("PATH=/usr/bin\x00" + managedDoltTestParentPIDEnv + "=4242\x00"), nil
		case 12:
			// A sibling test binary's child. Adopting it would let one
			// package's teardown SIGTERM another package's fixture.
			return []byte(managedDoltTestParentPIDEnv + "=4243\x00"), nil
		case 13:
			return []byte("PATH=/usr/bin\x00"), nil
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

func TestEnvironNamesManagedDoltTestParentRejectsPrefixMatch(t *testing.T) {
	// A variable whose name merely starts with the marker's name must not
	// match: the guard SIGTERMs what it reports, so a loose name comparison
	// aims that signal at an unrelated process.
	blob := []byte(managedDoltTestParentPIDEnv + "_SHADOW=99\x00OTHER=1\x00")
	if environNamesManagedDoltTestParent(blob, 99) {
		t.Fatalf("environ %q matched on a name prefix", blob)
	}
	if !environNamesManagedDoltTestParent([]byte(managedDoltTestParentPIDEnv+"=99\x00"), 99) {
		t.Fatal("exact marker did not match")
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
