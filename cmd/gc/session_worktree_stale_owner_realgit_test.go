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

// Scope: the .worktree-stale writer and the session-assignment gate driven
// end to end over a REAL git worktree holding real uncommitted work.
//
// This file exists for the one thing fakeGitProbe cannot represent. Its
// uncommitted-work answer is a bool no file on disk can move, so a paired test
// built on it agrees with itself whatever the marker does to the tree it sits
// in -- and the marker being its own dirt is the whole mechanism of the
// deadlock this pair asserts against (bead ci-4btflb, and ci-ciu63 for the
// earlier instance of the same shape). Here the dirt is an untracked file the
// prune path can actually see, and the marker lands beside it.
//
// Git mutations go through runGitInTest (cmd_rig_test.go) and git reads through
// internal/git, for the reasons recorded at the top of
// session_worktree_prune_realgit_test.go.
//
// Run: go test ./cmd/gc/ -run RealGitStaleMarker

// realGitStaleOwnerFixture is a city whose rig "demo" is a real repo with a
// real agent-home worktree under .gc/worktrees/demo/, sitting on a named
// branch and holding one untracked file -- the shape a session leaves behind
// when it dies mid-work.
type realGitStaleOwnerFixture struct {
	cityPath  string
	rigRoot   string
	home      string
	stalePath string
	cfg       *config.City
	session   beads.Bead
}

func newRealGitStaleOwnerFixture(t *testing.T, branch string) *realGitStaleOwnerFixture {
	t.Helper()
	cityPath := t.TempDir()

	rigRoot := filepath.Join(cityPath, "repos", "demo")
	realGitMkdirAll(t, rigRoot)
	runGitInTest(t, rigRoot, "init", "--initial-branch=main")
	runGitInTest(t, rigRoot, "config", "user.email", "test@test.com")
	runGitInTest(t, rigRoot, "config", "user.name", "Test")
	realGitWriteFile(t, filepath.Join(rigRoot, "tracked.txt"), "base\n")
	runGitInTest(t, rigRoot, "add", "tracked.txt")
	runGitInTest(t, rigRoot, "commit", "-m", "base")

	home := filepath.Join(cityPath, ".gc", "worktrees", "demo", "builder")
	runGitInTest(t, rigRoot, "worktree", "add", "-b", branch, home, "HEAD")

	// The work a dying session leaves behind: untracked, so `git worktree
	// remove` would delete it and no ref anywhere reaches it. This is the
	// shape ci-dek4sj was in -- a finished 494-line deliverable, never added.
	realGitWriteFile(t, filepath.Join(home, "adjudication.md"), "the deliverable, never committed\n")

	return &realGitStaleOwnerFixture{
		cityPath:  cityPath,
		rigRoot:   rigRoot,
		home:      home,
		stalePath: filepath.Join(home, worktreeStaleFileName),
		cfg: &config.City{
			Workspace: config.Workspace{Name: "test", Prefix: "ga"},
			Rigs:      []config.Rig{{Name: "demo", Path: rigRoot}},
			Agents:    []config.Agent{{Name: "builder", Dir: "demo"}},
		},
		session: beads.Bead{
			ID: "session-1",
			Metadata: map[string]string{
				"worker_dir":   home,
				"template":     "demo/builder",
				"session_name": "builder-ga-abc123",
			},
		},
	}
}

// TestRealGitStaleMarkerWriterAdmitsItsOwnSlot is the bead's reproduction, run
// forwards: real uncommitted work makes the prune path refuse and write the
// marker, and the gate must then admit the slot whose home that is.
//
// Both halves run against the same directory in one test on purpose. Split
// across two tests, each half passes on the deadlocked code -- the prune
// refusal is correct and the assignment refusal is correct, and only their
// conjunction is the defect.
func TestRealGitStaleMarkerWriterAdmitsItsOwnSlot(t *testing.T) {
	fx := newRealGitStaleOwnerFixture(t, "fix/ga-abc123-deliverable")

	// The premise: the work is genuinely at risk, so the prune refusal that
	// writes the marker is the correct behavior and must not be "fixed".
	if !git.New(fx.home).HasUncommittedWorkExcluding(worktreeStaleFileName) {
		t.Fatal("the untracked file no longer reads as uncommitted work; the fixture no longer reproduces the writer half")
	}

	var stderr bytes.Buffer
	if pruneAgentHomeWorktreeIfSafe(fx.session, fx.cityPath, fx.cfg, &stderr) {
		t.Fatalf("prune removed a worktree holding untracked work; stderr = %q", stderr.String())
	}
	if _, err := os.Stat(filepath.Join(fx.home, "adjudication.md")); err != nil {
		t.Fatalf("the untracked deliverable is gone: %v", err)
	}
	assertWorktreeStaleMarker(t, fx.home, "fix/ga-abc123-deliverable", "uncommitted-work")

	if err := validateWorkDirForSessionAssignment(fx.home, fx.home); err != nil {
		t.Fatalf("gate refused the slot that owns the marked worktree: %v", err)
	}
}

// TestRealGitStaleMarkerStillRefusesAForeignSlot is the third arm the bead
// required: without it the change removes the protection instead of fixing the
// deadlock. A candidate configured for a different home, pointed at this marked
// worktree by an override, is still refused.
func TestRealGitStaleMarkerStillRefusesAForeignSlot(t *testing.T) {
	fx := newRealGitStaleOwnerFixture(t, "fix/ga-abc123-deliverable")

	var stderr bytes.Buffer
	if pruneAgentHomeWorktreeIfSafe(fx.session, fx.cityPath, fx.cfg, &stderr) {
		t.Fatalf("prune removed a worktree holding untracked work; stderr = %q", stderr.String())
	}

	foreignHome := filepath.Join(fx.cityPath, ".gc", "worktrees", "demo", "reviewer")
	if err := validateWorkDirForSessionAssignment(fx.home, foreignHome); err == nil {
		t.Fatal("gate admitted a slot configured for a different home; the marker now protects nothing")
	}
}
