package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

// Scope: cleanupClosedBeadAgentHomeWorktrees exercised against a REAL git
// worktree, not the fakeAgentWorktreeGit of agent_home_worktree_cleanup_test.go.
//
// This file exists for one assertion the fake cannot make. Case B's safety gate
// asks "does this worktree have uncommitted work", and the .worktree-stale
// marker is untracked and sits inside the worktree it describes — so the marker
// whose presence is Case B's own entry condition was itself the answer, and Case
// B could never run for a marker it had not already been unable to clear.
// fakeAgentWorktreeGit answers that gate from a bool no file on disk can move,
// so a test built on it passes identically before and after the fix.
//
// Git mutations go through runGitInTest (cmd_rig_test.go) and git reads through
// internal/git, for the reasons recorded at the top of
// session_worktree_prune_realgit_test.go.
//
// Run: go test ./cmd/gc/ -run RealGitCleanup

// realGitCleanupFixture wires a city whose rig "demo" is a real clone-shaped
// repo with a real agent-home worktree under .gc/worktrees/demo/, sitting on a
// named branch. origin exists so origin/main is a resolvable reset target — the
// ref Case B detaches onto.
type realGitCleanupFixture struct {
	cityPath  string
	rigRoot   string
	home      string
	stalePath string
	cfg       *config.City
	store     beads.Store
}

// newRealGitCleanupFixture builds the fixture with the agent home named
// homeDirName, so callers can choose between a configured-agent name and a pool
// slot name without duplicating the setup.
func newRealGitCleanupFixture(t *testing.T, homeDirName, branch, beadID string) *realGitCleanupFixture {
	t.Helper()
	cityPath := t.TempDir()

	origin := filepath.Join(cityPath, "origin.git")
	realGitMkdirAll(t, origin)
	runGitInTest(t, origin, "init", "--bare", "--initial-branch=main")

	rigRoot := filepath.Join(cityPath, "repos", "demo")
	realGitMkdirAll(t, rigRoot)
	runGitInTest(t, rigRoot, "init", "--initial-branch=main")
	runGitInTest(t, rigRoot, "config", "user.email", "test@test.com")
	runGitInTest(t, rigRoot, "config", "user.name", "Test")
	realGitWriteFile(t, filepath.Join(rigRoot, "tracked.txt"), "base\n")
	runGitInTest(t, rigRoot, "add", "tracked.txt")
	runGitInTest(t, rigRoot, "commit", "-m", "base")
	runGitInTest(t, rigRoot, "remote", "add", "origin", origin)
	runGitInTest(t, rigRoot, "push", "-u", "origin", "main")

	home := filepath.Join(cityPath, ".gc", "worktrees", "demo", homeDirName)
	runGitInTest(t, rigRoot, "worktree", "add", "-b", branch, home, "HEAD")

	stalePath := filepath.Join(home, worktreeStaleFileName)
	realGitWriteFile(t, stalePath, "branch="+branch+"\nreason=unreachable-commits\n")

	return &realGitCleanupFixture{
		cityPath:  cityPath,
		rigRoot:   rigRoot,
		home:      home,
		stalePath: stalePath,
		cfg: &config.City{
			Workspace: config.Workspace{Name: "test", Prefix: "ga"},
			Rigs:      []config.Rig{{Name: "demo", Path: rigRoot}},
			Agents:    []config.Agent{{Name: "builder"}},
		},
		store: beads.NewMemStoreFrom(1, []beads.Bead{{ID: beadID, Status: "closed"}}, nil),
	}
}

func (fx *realGitCleanupFixture) run(t *testing.T) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	cleaned := cleanupClosedBeadAgentHomeWorktrees(fx.cityPath, fx.cfg, fx.store, map[string]beads.Store{"demo": fx.store}, &stderr)
	return cleaned, stderr.String()
}

// TestRealGitCleanupCaseBIgnoresItsOwnStaleMarker pins the third and last place
// the marker latched: Case B's uncommitted-work gate.
//
// Case B only runs on a worktree that HAS a marker, and the marker is untracked
// dirt in that same worktree, so the gate always saw work and always skipped —
// the recovery path for a closed-bead branch could not fire on any marker it
// was written to clear (bead ci-ciu63). The reset itself is asserted, not just
// the marker removal, because removing the marker while leaving the worktree on
// a dead branch would re-mark it on the next teardown.
func TestRealGitCleanupCaseBIgnoresItsOwnStaleMarker(t *testing.T) {
	fx := newRealGitCleanupFixture(t, "builder", "builder/ga-abc123", "ga-abc123")

	// The premise, read with the unfiltered probe the gate used to call.
	if !git.New(fx.home).HasUncommittedWork() {
		t.Fatal("the marker alone no longer makes the worktree dirty; the fixture no longer reproduces the latch")
	}

	cleaned, stderr := fx.run(t)
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1; stderr = %q", cleaned, stderr)
	}
	if _, err := os.Stat(fx.stalePath); !os.IsNotExist(err) {
		t.Errorf("stale marker still present after Case B; stderr = %q", stderr)
	}
	branch, err := git.New(fx.home).CurrentBranch()
	if err != nil {
		t.Fatalf("reading branch after cleanup: %v", err)
	}
	if branch != "HEAD" {
		t.Errorf("worktree left on branch %q, want detached; Case B did not reset it", branch)
	}
}

// TestRealGitCleanupCaseBStillProtectsRealUncommittedWork is the other half of
// the pair: excluding gc's own marker must not have excluded the agent's work.
// A modified tracked file is exactly what CheckoutDetach would discard, so the
// gate must still hold.
func TestRealGitCleanupCaseBStillProtectsRealUncommittedWork(t *testing.T) {
	fx := newRealGitCleanupFixture(t, "builder", "builder/ga-abc123", "ga-abc123")
	realGitWriteFile(t, filepath.Join(fx.home, "tracked.txt"), "edited, never committed\n")

	cleaned, stderr := fx.run(t)
	if cleaned != 0 {
		t.Errorf("cleaned = %d, want 0 for a worktree with real uncommitted work", cleaned)
	}
	if _, err := os.Stat(fx.stalePath); err != nil {
		t.Error("stale marker removed despite real uncommitted work")
	}
	data, err := os.ReadFile(filepath.Join(fx.home, "tracked.txt"))
	if err != nil || string(data) != "edited, never committed\n" {
		t.Errorf("uncommitted edit lost: content = %q, err = %v; stderr = %q", string(data), err, stderr)
	}
}

// TestRealGitCleanupCaseBReachesANumberedPoolSlot is the two defects meeting.
// The slot home is the shape the pool actually provisions ("builder-1"), and
// its recovery needs both halves of the fix: the scope test must admit the
// directory at all, and the uncommitted gate must then not read the marker as
// work. Either half alone leaves the slot exactly as bricked as before.
func TestRealGitCleanupCaseBReachesANumberedPoolSlot(t *testing.T) {
	fx := newRealGitCleanupFixture(t, "builder-1", "builder/ga-abc123", "ga-abc123")

	cleaned, stderr := fx.run(t)
	if cleaned != 1 {
		t.Errorf("cleaned = %d, want 1; stderr = %q", cleaned, stderr)
	}
	if _, err := os.Stat(fx.stalePath); !os.IsNotExist(err) {
		t.Errorf("stale marker still present in pool slot home; stderr = %q", stderr)
	}
}
