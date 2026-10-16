package dolt_test

// Sync classification of a divergence the compactor already owns (ci-tv6nxl).
//
// Scope: commands/sync/run.sh reading the compactor's deferred-push markers
// (compact-pending-gc/<db>, compact-pending-push/<db>) when the fetched remote
// head and the local branch have diverged. Why the suite exists: a flatten
// rewrites local history, and when the compactor defers its own
// compare-and-swap force-push (writer race, failed GC, failed fetch) the
// ff-only patrol read the rewrite as "diverged -- manual reconcile" and failed
// every 15m until the next compactor run. That paged a P1 suspecting a second
// writer (ci-9bsc7i) for a state that resolved itself at 16:22Z.
//
// The suite pins both directions. A marker whose remote, branch, verified
// expected head and freshness all match what the compactor will act on turns
// the run into a non-failing skip that never pushes. Every mismatch -- remote
// head moved, unverified head, stale marker, another remote or branch -- must
// still read as diverged and exit nonzero, because each is a case the
// compactor's push_remote_after_compaction refuses or defers, so a quiet patrol
// there would hide an un-pushed database indefinitely.
//
// Delegated: the compactor's own retry and CAS push are pinned by the compact
// suites. This suite drives sync against a fake dolt and cannot represent real
// Dolt's dolt_remote_branches shape; that query was checked by hand against the
// city's as database on 2026-10-16 (Dolt 2.2.3).
//
// Run: go test ./examples/bd/dolt/ -run 'TestSync.*Compaction|TestSyncPendingPushMaxAgeMatchesCompactor'

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// runFFSyncWithState is runFFSync plus a hook to stage pack state under the
// city before the run, and the exit status. runFFSync discards the status,
// which is the whole assertion for "diverged must still fail".
func runFFSyncWithState(t *testing.T, binDir string, stage func(cityPath string), args ...string) (string, int) {
	t.Helper()
	root := repoRoot(t)
	script := filepath.Join(root, syncScript)
	port, cleanup := startReachableTCPListener(t)
	defer cleanup()

	cityPath := t.TempDir()
	dataDir := filepath.Join(cityPath, "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "app", ".dolt"), 0o755); err != nil {
		t.Fatalf("mkdir db: %v", err)
	}
	writeSyncFakeBeadsBD(t, cityPath)
	stage(cityPath)

	cmd := exec.Command("sh", append([]string{script}, args...)...)
	cmd.Env = append(syncFilteredEnv(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"GC_CITY_PATH="+cityPath,
		"GC_PACK_DIR="+root,
		"GC_DOLT_DATA_DIR="+dataDir,
		fmt.Sprintf("GC_DOLT_PORT=%d", port),
		"GC_DOLT_USER=root",
		"GC_DOLT_PASSWORD=",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run sync: %v\n%s", err, out)
	}
	return string(out), code
}

// writeSyncFakeDoltDivergedWithRemoteHead models a flattened database: fetch
// succeeds, the range queries report 4 ahead / 7 behind, and the remote
// tracking ref resolves to remoteHead.
func writeSyncFakeDoltDivergedWithRemoteHead(t *testing.T, dir, remoteHead string) string {
	t.Helper()
	branch := "main"
	logPath := filepath.Join(dir, "dolt.log")
	aheadPat := "dolt_log('remotes/origin/" + branch + ".." + branch + "')"
	behindPat := "dolt_log('" + branch + "..remotes/origin/" + branch + "')"
	body := fakeDoltHeader(logPath, branch) +
		"  *\"CALL DOLT_FETCH(\"*) exit 0 ;;\n" +
		"  *\"" + aheadPat + "\"*) printf 'n\\n4\\n' ; exit 0 ;;\n" +
		"  *\"" + behindPat + "\"*) printf 'n\\n7\\n' ; exit 0 ;;\n" +
		"  *\"FROM dolt_remote_branches WHERE name = 'remotes/origin/" + branch + "'\"*)\n" +
		"    printf 'hash\\n" + remoteHead + "\\n' ; exit 0 ;;\n" +
		"esac\nexit 0\n"
	return installFFFakeDolt(t, dir, body)
}

type compactMarker struct {
	dir       string // compact-pending-gc | compact-pending-push
	remote    string
	branch    string // remote_branch=
	expected  string // expected_remote_head=
	verified  string // expected_remote_head_verified=
	createdAt time.Time
	rawCreate *string // overrides createdAt verbatim, for malformed values
}

func (m compactMarker) stage(t *testing.T) func(string) {
	return func(cityPath string) {
		d := filepath.Join(cityPath, ".gc", "runtime", "packs", "dolt", m.dir)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir marker dir: %v", err)
		}
		created := m.createdAt.UTC().Format("2006-01-02T15:04:05Z")
		if m.rawCreate != nil {
			created = *m.rawCreate
		}
		body := strings.Join([]string{
			"db=app",
			"reason=writer race during flatten deferred full GC",
			"created_at=" + created,
			"remote=" + m.remote,
			"expected_remote_head=" + m.expected,
			"expected_remote_head_verified=" + m.verified,
			"compacted_from_head=" + m.expected,
			"local_branch=main",
			"remote_branch=" + m.branch,
		}, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(d, "app"), []byte(body), 0o600); err != nil {
			t.Fatalf("write marker: %v", err)
		}
	}
}

// TestSyncDivergenceOwnedByPendingCompactionPushIsNotAFailure pins the green
// side. The remote head the fake reports is derived per subtest and the marker
// copies it, so a sync that compared against a constant (or skipped the
// remote-head probe) cannot match by accident: the moved-head case in the
// sibling test uses a different derived hash.
func TestSyncDivergenceOwnedByPendingCompactionPushIsNotAFailure(t *testing.T) {
	for _, dir := range []string{"compact-pending-gc", "compact-pending-push"} {
		t.Run(dir, func(t *testing.T) {
			head := fmt.Sprintf("h%dabc", time.Now().UnixNano())
			binDir := t.TempDir()
			logPath := writeSyncFakeDoltDivergedWithRemoteHead(t, binDir, head)
			m := compactMarker{dir: dir, remote: "origin", branch: "main", expected: head, verified: "1", createdAt: time.Now().Add(-2 * time.Hour)}
			out, code := runFFSyncWithState(t, binDir, m.stage(t), "--db", "app")
			if pushed(readLog(t, logPath)) {
				t.Fatalf("sync must leave the compactor's push to the compactor.\nout:\n%s", out)
			}
			if code != 0 {
				t.Fatalf("a compactor-owned divergence must not fail the patrol: exit %d\nout:\n%s", code, out)
			}
			if !strings.Contains(out, "compaction push pending") {
				t.Fatalf("expected a 'compaction push pending' status.\nout:\n%s", out)
			}
			if strings.Contains(out, "manual reconcile") {
				t.Fatalf("must not page manual reconcile for a compactor-owned push.\nout:\n%s", out)
			}
		})
	}
}

// TestSyncDivergenceStaysAFailureWhenCompactorWillNotPush pins the red side:
// each case is a marker the compactor would refuse or defer, so the patrol must
// keep failing. Every row differs from the green fixture in exactly one field.
func TestSyncDivergenceStaysAFailureWhenCompactorWillNotPush(t *testing.T) {
	empty := ""
	cases := []struct {
		name   string
		mutate func(m *compactMarker, remoteHead *string)
		noFile bool
		// shadow stages a matching pending-push marker beside the mutated
		// pending-gc one. The compactor reads pending-gc first and returns,
		// so the pending-push marker is not acted on this run.
		shadow bool
	}{
		{name: "no marker", noFile: true},
		{name: "remote head moved off expected", mutate: func(_ *compactMarker, h *string) { *h += "moved" }},
		{name: "expected head unverified", mutate: func(m *compactMarker, _ *string) { m.verified = "0" }},
		// Both sides empty: without the shape check, "" == "" would match.
		{name: "expected head empty", mutate: func(m *compactMarker, h *string) { m.expected = ""; *h = "" }},
		// GNU date reads "" as today's midnight, which is inside the window.
		{name: "created_at empty", mutate: func(m *compactMarker, _ *string) { m.rawCreate = &empty }},
		{name: "pending-gc shadows a matching pending-push", shadow: true, mutate: func(m *compactMarker, _ *string) { m.remote = "backup" }},
		{name: "marker for another remote", mutate: func(m *compactMarker, _ *string) { m.remote = "backup" }},
		{name: "marker for another branch", mutate: func(m *compactMarker, _ *string) { m.branch = "other" }},
		// 172800s is the compactor's default retry window. Past it the
		// compactor demands manual review, so the patrol must say so too.
		{name: "marker past compactor retry window", mutate: func(m *compactMarker, _ *string) { m.createdAt = time.Now().Add(-49 * time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head := fmt.Sprintf("h%dabc", time.Now().UnixNano())
			m := compactMarker{dir: "compact-pending-gc", remote: "origin", branch: "main", expected: head, verified: "1", createdAt: time.Now().Add(-2 * time.Hour)}
			remoteHead := head
			if tc.mutate != nil {
				tc.mutate(&m, &remoteHead)
			}
			binDir := t.TempDir()
			logPath := writeSyncFakeDoltDivergedWithRemoteHead(t, binDir, remoteHead)
			stage := m.stage(t)
			if tc.noFile {
				stage = func(string) {}
			}
			if tc.shadow {
				good := compactMarker{dir: "compact-pending-push", remote: "origin", branch: "main", expected: head, verified: "1", createdAt: m.createdAt}
				gcStage, pushStage := stage, good.stage(t)
				stage = func(c string) { gcStage(c); pushStage(c) }
			}
			out, code := runFFSyncWithState(t, binDir, stage, "--db", "app")
			if pushed(readLog(t, logPath)) {
				t.Fatalf("diverged DB must NOT be pushed.\nout:\n%s", out)
			}
			if code == 0 {
				t.Fatalf("divergence the compactor will not push must fail the patrol.\nout:\n%s", out)
			}
			if !strings.Contains(out, "diverged (4 ahead / 7 behind)") {
				t.Fatalf("expected the diverged status.\nout:\n%s", out)
			}
		})
	}
}

// TestSyncPendingPushMaxAgeMatchesCompactor gates the one constant the two
// scripts share by value. If the compactor's retry window changes and sync's
// does not, the patrol goes quiet for markers the compactor already refuses.
func TestSyncPendingPushMaxAgeMatchesCompactor(t *testing.T) {
	root := repoRoot(t)
	re := regexp.MustCompile(`GC_DOLT_COMPACT_PENDING_PUSH_MAX_AGE_SECS:-([0-9]+)`)
	get := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		m := re.FindAllStringSubmatch(string(data), -1)
		if len(m) == 0 {
			t.Fatalf("%s carries no GC_DOLT_COMPACT_PENDING_PUSH_MAX_AGE_SECS default", rel)
		}
		for _, x := range m[1:] {
			if x[1] != m[0][1] {
				t.Fatalf("%s carries two different defaults: %s and %s", rel, m[0][1], x[1])
			}
		}
		return m[0][1]
	}
	if c, s := get("commands/compact/run.sh"), get(syncScript); c != s {
		t.Fatalf("pending-push max age differs: compact=%s sync=%s", c, s)
	}
}
