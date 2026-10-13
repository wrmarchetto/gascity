//go:build linux

package proctable

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// buildFakeProc builds a minimal /proc-shaped fixture tree under root for pid
// with parent PID 1 (init). environ is written as NUL-delimited key=value pairs.
func buildFakeProc(t *testing.T, root string, pid int, env map[string]string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var buf []byte
	for k, v := range env {
		buf = append(buf, []byte(k+"="+v+"\x00")...)
	}
	if err := os.WriteFile(filepath.Join(dir, "environ"), buf, 0o644); err != nil {
		t.Fatalf("write environ: %v", err)
	}
	// stat: ppid=1 (init) so this process is classified as a root.
	stat := strconv.Itoa(pid) + " (cmd) S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
}

func TestScanWithRootStatVanished(t *testing.T) {
	root := t.TempDir()
	pid := 500
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Write environ but no stat file (process died between environ read and stat check).
	env := []byte("GC_SESSION_ID=ga-test\x00")
	if err := os.WriteFile(filepath.Join(dir, "environ"), env, 0o644); err != nil {
		t.Fatalf("write environ: %v", err)
	}
	// stat file absent — simulates TOCTOU race.

	got, err := scanWithRoot(root, "ga-test")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("scanWithRoot returned %d runtimes for a vanished process, want 0", len(got))
	}
}

func TestScanWithRootEmptyReturnsNonNilSlice(t *testing.T) {
	root := t.TempDir()
	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if got == nil {
		t.Fatal("scanWithRoot returned nil slice, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("scanWithRoot returned %d runtimes, want 0", len(got))
	}
}

func TestScanWithRootFiltersBySessionID(t *testing.T) {
	root := t.TempDir()
	// pid 100: parent 1 (init), session ga-abc
	buildFakeProc(t, root, 100, map[string]string{"GC_SESSION_ID": "ga-abc"})
	// pid 200: parent 1 (init), session ga-xyz
	buildFakeProc(t, root, 200, map[string]string{"GC_SESSION_ID": "ga-xyz"})

	got, err := scanWithRoot(root, "ga-abc")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != "ga-abc" || got[0].PID != 100 {
		t.Fatalf("scanWithRoot = %v, want [{ga-abc pid=100}]", got)
	}
}

func TestScanWithRootEmptyIDReturnsAll(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 100, map[string]string{"GC_SESSION_ID": "ga-abc"})
	buildFakeProc(t, root, 200, map[string]string{"GC_SESSION_ID": "ga-xyz"})

	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("scanWithRoot = %d entries, want 2", len(got))
	}
}

func TestScanWithRootParsesEpoch(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 300, map[string]string{
		"GC_SESSION_ID":    "ga-epoch",
		"GC_RUNTIME_EPOCH": "42",
	})

	got, err := scanWithRoot(root, "ga-epoch")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Epoch != 42 {
		t.Fatalf("Epoch = %d, want 42", got[0].Epoch)
	}
}

func TestScanWithRootPopulatesCityFromGCPath(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 310, map[string]string{
		"GC_SESSION_ID": "ga-city",
		"GC_CITY_PATH":  "/tmp/primary-city",
		"GC_CITY":       "/tmp/fallback-city",
	})

	got, err := scanWithRoot(root, "ga-city")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].City != "/tmp/primary-city" {
		t.Fatalf("City = %q, want GC_CITY_PATH value", got[0].City)
	}
}

func TestScanWithRootPopulatesCityFromGCCityFallback(t *testing.T) {
	root := t.TempDir()
	buildFakeProc(t, root, 320, map[string]string{
		"GC_SESSION_ID": "ga-city",
		"GC_CITY":       "/tmp/fallback-city",
	})

	got, err := scanWithRoot(root, "ga-city")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].City != "/tmp/fallback-city" {
		t.Fatalf("City = %q, want GC_CITY fallback value", got[0].City)
	}
}

func TestScanWithRootMissingEnvironSkipped(t *testing.T) {
	root := t.TempDir()
	// Directory exists but no environ (ENOENT) — should be skipped without error.
	dir := filepath.Join(root, "400")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries, want 0", len(got))
	}
}

// buildFakeProcUnder builds a /proc-shaped fixture entry for pid whose parent
// is ppid and whose comm is comm, so a test can lay out a real process tree
// rather than a forest of init children.
func buildFakeProcUnder(t *testing.T, root string, pid, ppid int, comm string, env map[string]string) {
	t.Helper()
	buildFakeProc(t, root, pid, env)
	dir := filepath.Join(root, strconv.Itoa(pid))
	stat := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(stat), 0o644); err != nil {
		t.Fatalf("write stat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte(comm+"\n"), 0o644); err != nil {
		t.Fatalf("write comm: %v", err)
	}
}

// A test child an agent session spawns with an overridden GC_SESSION_ID is a
// scan root -- its id differs from its parent's -- and it inherits the
// session's GC_RUNTIME_EPOCH and GC_CITY_PATH, so nothing else in the record
// tells it from a gc-started runtime (ci-ddfq1d). EnclosingSessionID is that
// distinction: the parent's session id, recorded only when the parent is a
// session process rather than tmux infrastructure or a process carrying no
// id. Both negative arms are the shapes every gc-started root has, so a scan
// that recorded the parent's id unconditionally fails here too.
func TestScanWithRootRecordsEnclosingSessionOfNestedRoot(t *testing.T) {
	root := t.TempDir()
	const city = "/city"
	agent := map[string]string{"GC_SESSION_ID": "ci-agent", "GC_RUNTIME_EPOCH": "3", "GC_CITY_PATH": city}
	// An agent session's process, and a test child under it that overrides
	// only GC_SESSION_ID and inherits the rest.
	buildFakeProcUnder(t, root, 300, 1, "bash", agent)
	buildFakeProcUnder(t, root, 301, 300, "python3",
		map[string]string{"GC_SESSION_ID": "ci-sess", "GC_RUNTIME_EPOCH": "3", "GC_CITY_PATH": city})
	// A tmux-started runtime: the server carries some other session's id.
	buildFakeProcUnder(t, root, 400, 1, "tmux: server", map[string]string{"GC_SESSION_ID": "ci-first"})
	buildFakeProcUnder(t, root, 401, 400, "claude",
		map[string]string{"GC_SESSION_ID": "ci-tmuxed", "GC_RUNTIME_EPOCH": "1", "GC_CITY_PATH": city})
	// A subprocess-provider runtime: its parent, the controller, has no id.
	buildFakeProcUnder(t, root, 500, 1, "gc", map[string]string{"GC_CITY_PATH": city})
	buildFakeProcUnder(t, root, 501, 500, "sh",
		map[string]string{"GC_SESSION_ID": "ci-sub", "GC_RUNTIME_EPOCH": "1", "GC_CITY_PATH": city})

	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	enclosing := map[string]string{}
	for _, live := range got {
		enclosing[live.SessionID] = live.EnclosingSessionID
	}
	want := map[string]string{
		"ci-agent":  "",
		"ci-sess":   "ci-agent",
		"ci-first":  "",
		"ci-tmuxed": "",
		"ci-sub":    "",
	}
	if len(enclosing) != len(want) {
		t.Fatalf("scan roots = %v, want exactly %v", enclosing, want)
	}
	for id, w := range want {
		g, ok := enclosing[id]
		if !ok {
			t.Errorf("%s is not a scan root, want one", id)
			continue
		}
		if g != w {
			t.Errorf("%s EnclosingSessionID = %q, want %q", id, g, w)
		}
	}
}

// A process carrying its parent's own session id is inside that session's
// envelope and is not a root -- otherwise every shell and tool an agent runs
// would be swept as a runtime of its own. The tmux exception is what lets a
// pane's first process be a root when the server happens to carry the same
// id. Pinned here because isRootWithSessionID was restructured to return the
// enclosing session (ci-ddfq1d) and no other case drove the same-id branch.
func TestScanWithRootSameSessionChildIsRootOnlyUnderTmux(t *testing.T) {
	root := t.TempDir()
	same := map[string]string{"GC_SESSION_ID": "ci-same", "GC_RUNTIME_EPOCH": "1"}
	buildFakeProcUnder(t, root, 600, 1, "claude", same)
	buildFakeProcUnder(t, root, 601, 600, "bash", same)
	tmuxed := map[string]string{"GC_SESSION_ID": "ci-pane", "GC_RUNTIME_EPOCH": "1"}
	buildFakeProcUnder(t, root, 700, 1, "tmux: server", tmuxed)
	buildFakeProcUnder(t, root, 701, 700, "claude", tmuxed)

	got, err := scanWithRoot(root, "")
	if err != nil {
		t.Fatalf("scanWithRoot error: %v", err)
	}
	var pids []int
	for _, live := range got {
		pids = append(pids, live.PID)
	}
	if want := []int{600, 700, 701}; !slices.Equal(pids, want) {
		t.Fatalf("scan root pids = %v, want %v (601 shares a non-tmux parent's id)", pids, want)
	}
}
