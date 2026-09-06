package tmuxtest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/pidutil"
)

// testNonLivePID is a PID value that will not correspond to a live process
// on any reasonable system (max PID on Linux is well below this).
const testNonLivePID = 2147483647

func nonLivePID(t *testing.T) int {
	t.Helper()
	if pidutil.Alive(testNonLivePID) {
		t.Skipf("test PID %d is unexpectedly alive", testNonLivePID)
	}
	return testNonLivePID
}

func backdatePastSweepAge(t *testing.T, path string) {
	t.Helper()
	old := time.Now().Add(-2 * socketParentSweepMinAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
}

func pidPrefixedTestDir(t *testing.T, root, prefix string, pid int) string {
	t.Helper()
	dir := filepath.Join(root, prefix+strconv.Itoa(pid)+"-fixture")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", dir, err)
	}
	return dir
}

func TestSweepOrphanPIDPrefixedDirsRemovesStaleDeadPIDWithNilDiagnostics(t *testing.T) {
	root := t.TempDir()
	dir := pidPrefixedTestDir(t, root, "pfx-", nonLivePID(t))
	backdatePastSweepAge(t, dir)

	SweepOrphanPIDPrefixedDirs(root, "pfx-", nil)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("stale dead-PID dir survived sweep: %s", dir)
	}
}

func TestSweepOrphanPIDPrefixedDirsPreservesHeldSentinel(t *testing.T) {
	root := t.TempDir()
	dir := pidPrefixedTestDir(t, root, "pfx-", nonLivePID(t))
	backdatePastSweepAge(t, dir)

	sentinel, err := HoldAliveSentinel(dir)
	if err != nil {
		t.Fatalf("HoldAliveSentinel: %v", err)
	}
	defer func() { _ = sentinel.Close() }()

	SweepOrphanPIDPrefixedDirs(root, "pfx-", io.Discard)

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dir with held sentinel was removed by sweep: %v", err)
	}
}

func TestSweepOrphanPIDPrefixedDirsRemovesFreeSentinel(t *testing.T) {
	root := t.TempDir()
	dir := pidPrefixedTestDir(t, root, "pfx-", nonLivePID(t))

	sentinel, err := HoldAliveSentinel(dir)
	if err != nil {
		t.Fatalf("HoldAliveSentinel: %v", err)
	}
	_ = sentinel.Close() // release the flock, simulating a crashed creator

	backdatePastSweepAge(t, dir)

	var diagnostics bytes.Buffer
	SweepOrphanPIDPrefixedDirs(root, "pfx-", &diagnostics)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir with free sentinel survived sweep: %s", dir)
	}
	wantDiagnostics := fmt.Sprintf("tmuxtest: removing orphaned socket parent %s (free sentinel)\n", dir)
	if got := diagnostics.String(); got != wantDiagnostics {
		t.Errorf("diagnostics = %q, want %q", got, wantDiagnostics)
	}
}

func TestSweepOrphanPIDPrefixedDirsSkipsYoungDir(t *testing.T) {
	root := t.TempDir()
	dir := pidPrefixedTestDir(t, root, "pfx-", nonLivePID(t))
	// No backdate: dir is fresh, inside the min-age window.

	SweepOrphanPIDPrefixedDirs(root, "pfx-", io.Discard)

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("young dir was removed by sweep despite age guard: %v", err)
	}
}

func TestSweepOrphanPIDPrefixedDirsSkipsSelfPID(t *testing.T) {
	root := t.TempDir()
	dir := pidPrefixedTestDir(t, root, "pfx-", os.Getpid())
	backdatePastSweepAge(t, dir)

	SweepOrphanPIDPrefixedDirs(root, "pfx-", io.Discard)

	if _, err := os.Stat(dir); err != nil {
		t.Errorf("sweep removed a dir carrying its own PID: %v", err)
	}
}

func TestSweepOrphanPIDPrefixedDirsSkipsNonDirectories(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pfx-123")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	SweepOrphanPIDPrefixedDirs(root, "pfx-", io.Discard)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Error("SweepOrphanPIDPrefixedDirs removed a non-directory file")
	}
}

func TestNewSocketParentDirCreatesSentinelHeldDir(t *testing.T) {
	root := t.TempDir()

	dir, sentinel, err := NewSocketParentDir(root, io.Discard)
	if err != nil {
		t.Fatalf("NewSocketParentDir: %v", err)
	}
	defer func() { _ = sentinel.Close() }()
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("created dir does not exist: %v", err)
	}
	exists, held := aliveSentinelHeld(dir)
	if !exists || !held {
		t.Errorf("aliveSentinelHeld(%s) = (%v, %v), want (true, true)", dir, exists, held)
	}
	pid, ok := pidFromPrefixedDirName(filepath.Base(dir), SocketParentDirPrefix)
	if !ok || pid != os.Getpid() {
		t.Errorf("created dir %q does not embed this process's PID", dir)
	}
}

func TestNewSocketParentDirReapsOrphanedSibling(t *testing.T) {
	root := t.TempDir()
	orphan := pidPrefixedTestDir(t, root, SocketParentDirPrefix, nonLivePID(t))
	backdatePastSweepAge(t, orphan)

	var diagnostics bytes.Buffer
	dir, sentinel, err := NewSocketParentDir(root, &diagnostics)
	if err != nil {
		t.Fatalf("NewSocketParentDir: %v", err)
	}
	defer func() { _ = sentinel.Close() }()
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("orphaned sibling survived NewSocketParentDir: %s", orphan)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("freshly created dir missing: %v", err)
	}
	wantDiagnostics := fmt.Sprintf("tmuxtest: removing orphaned socket parent %s (pid dead, no sentinel)\n", orphan)
	if got := diagnostics.String(); got != wantDiagnostics {
		t.Errorf("diagnostics = %q, want %q", got, wantDiagnostics)
	}
}

func TestSweepOrphanPIDPrefixedDirsPreservesLegacyNoDashDir(t *testing.T) {
	root := t.TempDir()
	// The pre-sweep harness created its socket parent with
	// os.MkdirTemp(root, "pfx-"), yielding an all-digit "pfx-<random>" name
	// with no "-" separator and no alive sentinel. Those trailing digits are a
	// MkdirTemp random suffix, not an owner PID -- parsing them as a (dead) PID
	// would let the sweep reap a still-live legacy sibling. Even backdated past
	// the age guard and with digits that look like a dead PID, the missing
	// separator must keep the dir out of the sweep.
	legacy := filepath.Join(root, "pfx-"+strconv.Itoa(nonLivePID(t)))
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatalf("Mkdir(%s): %v", legacy, err)
	}
	backdatePastSweepAge(t, legacy)

	SweepOrphanPIDPrefixedDirs(root, "pfx-", io.Discard)

	if _, err := os.Stat(legacy); err != nil {
		t.Errorf("legacy no-separator dir was removed by sweep: %v", err)
	}
}

func TestPIDFromPrefixedDirName(t *testing.T) {
	const prefix = "gct-"
	cases := []struct {
		name    string
		wantPID int
		wantOK  bool
	}{
		{"gct-1234-0007", 1234, true}, // canonical <prefix><PID>-<random>
		{"gct-1234-", 1234, true},     // separator present, empty random suffix
		{"gct-1234", 0, false},        // legacy no-separator name: rejected
		{"gct-", 0, false},            // no digits
		{"gct-abc", 0, false},         // non-digit suffix
		{"gct-12ab-3", 0, false},      // digits not terminated by "-"
		{"other-1234-5", 0, false},    // wrong prefix
	}
	for _, tc := range cases {
		gotPID, gotOK := pidFromPrefixedDirName(tc.name, prefix)
		if gotPID != tc.wantPID || gotOK != tc.wantOK {
			t.Errorf("pidFromPrefixedDirName(%q, %q) = (%d, %v), want (%d, %v)",
				tc.name, prefix, gotPID, gotOK, tc.wantPID, tc.wantOK)
		}
	}
}

// TestSweepOrphanKillsTmuxServerBeforeRemovingItsSocketDir pins the half of
// the orphan sweep that removing a directory cannot do: killing the tmux
// server whose socket lives inside it.
//
// THE DEFECT (ci-87655r). The sweep removed an orphaned socket parent with
// os.RemoveAll and nothing ever killed the server. Deleting a socket does not
// stop tmux -- it makes the server UNREACHABLE AND ALIVE, with no socket and
// no working directory left to address it by, which is exactly the shape of
// the 24-day server found on this host.
//
// THE ASSERTION IS ON THE PROCESS, NOT THE SOCKET, and that is the whole
// design of this case. After the sweep the socket file is gone either way, so
// `tmux -S <socket> list-sessions` fails whether the server died or was
// orphaned -- a test written against the socket passes over the exact defect
// it is meant to catch. The server's own PID is captured while it is still
// addressable and the assertion is that the PROCESS is gone.
//
// The session command BLOCKS (sleep) on purpose. With a command that exits
// when its pipe dies, a stranded server self-terminates in ~10s and the leak
// is invisible; a real provider CLI waiting on input is what turned that
// window into 24 days (ci-ah94vk).
func TestSweepOrphanKillsTmuxServerBeforeRemovingItsSocketDir(t *testing.T) {
	RequireTmux(t)
	// NOT t.TempDir(): a unix socket path is capped near 108 bytes and the
	// per-test temp name alone blows it, which surfaces as "File name too
	// long" from tmux -- a setup fault that a skip would have turned into a
	// green run.
	root, err := os.MkdirTemp("/tmp", "gctsw")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	dir := pidPrefixedTestDir(t, root, SocketParentDirPrefix, nonLivePID(t))

	// The real layout: tmux puts its socket at <TMUX_TMPDIR>/tmux-<uid>/<name>.
	socketDir := filepath.Join(dir, "tmux-"+strconv.Itoa(os.Getuid()))
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", socketDir, err)
	}
	socket := filepath.Join(socketDir, "gctest-sweepkill")

	start := exec.Command("tmux", "-S", socket, "new-session", "-d",
		"-s", "gctest-sweepkill", "sleep 600")
	// tmux is present (RequireTmux above), so a start failure here is a fault
	// in this fixture, not a missing precondition. Failing is the point.
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start tmux server on %s: %v: %s", socket, err, out)
	}

	pidOut, err := exec.Command("tmux", "-S", socket, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		t.Fatalf("read tmux server pid: %v", err)
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(string(pidOut)))
	if err != nil {
		t.Fatalf("parse tmux server pid %q: %v", pidOut, err)
	}
	// Never leave a real tmux server behind, even on a failing path. Targeted
	// at this socket and this PID only -- never a bare kill-server.
	t.Cleanup(func() {
		_ = exec.Command("tmux", "-S", socket, "kill-server").Run()
		if pidutil.Alive(serverPID) {
			_ = syscall.Kill(serverPID, syscall.SIGKILL)
		}
	})

	// The witness that this case is not vacuous: a server really was running
	// before the sweep. Without it, a sweep that kills nothing on a host where
	// tmux never started passes.
	if !pidutil.Alive(serverPID) {
		t.Fatalf("tmux server %d is not alive before the sweep; the case would prove nothing", serverPID)
	}

	backdatePastSweepAge(t, dir)
	SweepOrphanPIDPrefixedDirs(root, SocketParentDirPrefix, io.Discard)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("orphaned socket parent %s survived the sweep (stat err=%v)", dir, err)
	}
	// tmux exits asynchronously after kill-server, so wait rather than sample
	// once -- but bounded, so a server that never dies fails instead of hanging.
	deadline := time.Now().Add(10 * time.Second)
	for pidutil.Alive(serverPID) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if pidutil.Alive(serverPID) {
		t.Fatalf("tmux server %d is STILL ALIVE after its socket directory was removed: unreachable and alive is the ci-87655r leak, and removing the directory is what makes it permanent", serverPID)
	}
}
