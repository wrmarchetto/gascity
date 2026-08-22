package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Scope: the worker-dir auto-prune path exercised against REAL git worktrees,
// not the fakeGitProbe used by session_worktree_prune_test.go.
//
// This file exists because the fake cannot express the bug it pins. A stub with
// a single hasStashes bool answers "does this worktree have stashed work" as
// though that question were per-worktree; refs/stash is one repo-wide ref, so
// the real probe answers the same for every worktree of a repo. Any test built
// on the fake passes both before and after the fix, which is why a repo-wide
// probe read as correct for as long as it did.
//
// Every git mutation here goes through runGitInTest (cmd_rig_test.go) and every
// git READ through internal/git, rather than a local exec.Command helper. Two
// reasons, in order: the reads then exercise the very probes the prune path
// calls, so this suite cannot pass against a probe that the production gate
// would read differently; and the untagged-subprocess census
// (internal/testpolicy/resourcecensus) ratchets on call sites in test source, so
// a new spawn helper here would have had to grow another owner's debt baseline
// to land.
//
// Run: go test ./cmd/gc/ -run RealGit

// realGitPruneFixture wires a city whose rig is a real clone of a real bare
// origin, with two real detached worktrees under .gc/worktrees — the shape the
// pool actually provisions.
//
// The origin was originally here because the commit gate read push state, so
// without a remote-tracking ref that gate fired first and masked the stash
// gate. Since bead ci-hh8aa the gate reads reachability instead and refs/heads
// alone would satisfy it, so the origin is now fixture fidelity rather than a
// precondition — keep it, and do not read a failure here as a missing remote.
type realGitPruneFixture struct {
	cityPath  string
	rigRoot   string
	worktreeA string
	worktreeB string
	cfg       *config.City
}

func newRealGitPruneFixture(t *testing.T) *realGitPruneFixture {
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
	// Push rather than clone: the bare origin starts empty, so a clone has no
	// branch to check out. The push is what creates refs/remotes/origin/main.
	runGitInTest(t, rigRoot, "remote", "add", "origin", origin)
	runGitInTest(t, rigRoot, "push", "-u", "origin", "main")

	// Detached, matching how the pool provisions a fresh slot worktree, and how
	// toolsmith-2 was sitting when it was falsely marked. Do not read the detach
	// as load-bearing beyond that: slots spend most of their life on a named
	// branch an agent cut, so a test relying on every slot being detached would
	// be pinning the fixture rather than the city.
	wtRoot := filepath.Join(cityPath, ".gc", "worktrees", "demo", "toolsmith")
	worktreeA := filepath.Join(wtRoot, "toolsmith-A")
	worktreeB := filepath.Join(wtRoot, "toolsmith-B")
	runGitInTest(t, rigRoot, "worktree", "add", "--detach", worktreeA, "HEAD")
	runGitInTest(t, rigRoot, "worktree", "add", "--detach", worktreeB, "HEAD")

	return &realGitPruneFixture{
		cityPath:  cityPath,
		rigRoot:   rigRoot,
		worktreeA: worktreeA,
		worktreeB: worktreeB,
		cfg: &config.City{
			Rigs: []config.Rig{{Name: "demo", Path: rigRoot}},
		},
	}
}

// stashIn creates an untracked file in the given worktree and stashes it, the
// exact shape of the two stashes that bricked the toolsmith pool: agent work
// pushed onto refs/stash with -u.
func (fx *realGitPruneFixture) stashIn(t *testing.T, worktree, message string) {
	t.Helper()
	realGitWriteFile(t, filepath.Join(worktree, "agent-wip.txt"), "work in progress\n")
	runGitInTest(t, worktree, "stash", "push", "-u", "-m", message)
}

func (fx *realGitPruneFixture) sessionBeadFor(worktree string) beads.Bead {
	return beads.Bead{
		ID: "session-1",
		Metadata: map[string]string{
			"worker_dir":   worktree,
			"template":     "demo/toolsmith",
			"session_name": "demo/toolsmith-B",
		},
	}
}

// sessionInfoFor mirrors sessionBeadFor in the session.Info shape, so both
// prune forms are driven from one fixture.
func (fx *realGitPruneFixture) sessionInfoFor(worktree string) sessionpkg.Info {
	return sessionpkg.Info{
		ID:                  "session-1",
		WorkerDir:           worktree,
		Template:            "demo/toolsmith",
		SessionNameMetadata: "demo/toolsmith-B",
	}
}

func realGitMkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func realGitWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRealGitPruneIgnoresASiblingWorktreesStash pins the invariant the whole
// fix exists for: a stash belonging to a DIFFERENT worktree must not stop this
// worktree being pruned, and must not leave a stale marker behind.
//
// The marker is the damage, not the skipped prune. cleanupClosedBeadAgentHome-
// Worktrees and the reconciler both read it, so a marker written here makes the
// reconciler refuse every future spawn into the slot — one stash anywhere in the
// repo brings the pool to zero, one teardown at a time (bead ci-auomj).
//
// Constructed deliberately so the stash is in worktree A and the prune targets
// worktree B: a test that stashes in the worktree under test passes both before
// and after the fix, and so proves nothing.
func TestRealGitPruneIgnoresASiblingWorktreesStash(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.stashIn(t, fx.worktreeA, "toolsmith-A private work")

	// Guard the premise rather than assuming it, and guard it with the very probe
	// the prune path consults: if worktree B were dirty, the uncommitted gate
	// would fire first and a passing prune would prove nothing about stashes.
	if git.New(fx.worktreeB).HasUncommittedWork() {
		t.Fatal("worktree B should be clean before the prune")
	}

	var stderr bytes.Buffer
	pruned := pruneAgentHomeWorktreeIfSafe(fx.sessionBeadFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)

	assertNoWorktreeStaleMarker(t, fx.worktreeB)
	if !pruned {
		t.Errorf("prune returned false for a clean worktree; stderr = %q", stderr.String())
	}
	if _, err := os.Stat(fx.worktreeB); !os.IsNotExist(err) {
		t.Errorf("worktree B still on disk after prune (stat err = %v)", err)
	}
}

// TestRealGitPruneInfoIgnoresASiblingWorktreesStash is the session.Info form of
// the invariant above. Both forms are pinned because they carry independent
// copies of the same gate sequence, so a fix applied to one only is invisible
// from the other's tests.
func TestRealGitPruneInfoIgnoresASiblingWorktreesStash(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.stashIn(t, fx.worktreeA, "toolsmith-A private work")

	var stderr bytes.Buffer
	pruneAgentHomeWorktreeIfSafeInfo(fx.sessionInfoFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)

	assertNoWorktreeStaleMarker(t, fx.worktreeB)
	if _, err := os.Stat(fx.worktreeB); !os.IsNotExist(err) {
		t.Errorf("worktree B still on disk after prune (stat err = %v); stderr = %q", err, stderr.String())
	}
}

// TestRealGitPruneStillProtectsRealUncommittedWork is the other half of the
// pair: dropping the stash gate must not have dropped the gates that guard work
// a removal really would destroy. Uncommitted changes live only in the
// worktree's own checkout, so `git worktree remove` is what loses them — unlike
// a stash, which survives on refs/stash.
func TestRealGitPruneStillProtectsRealUncommittedWork(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	realGitWriteFile(t, filepath.Join(fx.worktreeB, "tracked.txt"), "edited, never committed\n")

	var stderr bytes.Buffer
	if pruneAgentHomeWorktreeIfSafe(fx.sessionBeadFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr) {
		t.Fatal("prune returned true for a worktree with uncommitted changes")
	}
	if _, err := os.Stat(fx.worktreeB); err != nil {
		t.Errorf("worktree B removed despite uncommitted changes: %v", err)
	}
	assertWorktreeStaleMarker(t, fx.worktreeB, "HEAD", "uncommitted-work")
}

// TestRealGitPruneDoesNotCountItsOwnStaleMarkerAsWork pins the prune half of the
// .worktree-stale latch: a slot that was falsely marked once must still be
// reclaimable on the next teardown.
//
// The marker is untracked and lives inside the very worktree it describes, so an
// unfiltered `git status --porcelain` reports it. The uncommitted-work gate runs
// FIRST, so before the fix the marker WAS the uncommitted work: one false marker
// of any cause became permanent, and every re-mark overwrote reason= with
// uncommitted-work, so a bricked slot could not even tell you what had marked it
// (bead ci-ciu63).
//
// Real git rather than fakeGitProbe is load-bearing here, not a preference. The
// fake answers hasUncommitted from a bool that no file on disk can move, so the
// same test written against it passes identically before and after the fix.
func TestRealGitPruneDoesNotCountItsOwnStaleMarkerAsWork(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	realGitWriteFile(t, filepath.Join(fx.worktreeB, worktreeStaleFileName), "branch=HEAD\nreason=unreachable-commits\n")

	// State the premise with the unfiltered probe rather than assuming it: if the
	// marker did not make the worktree read as dirty, a passing prune below would
	// prove nothing about the latch.
	if !git.New(fx.worktreeB).HasUncommittedWork() {
		t.Fatal("the marker alone no longer makes the worktree dirty; the fixture no longer reproduces the latch")
	}

	var stderr bytes.Buffer
	pruned := pruneAgentHomeWorktreeIfSafe(fx.sessionBeadFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)
	if !pruned {
		t.Errorf("prune returned false for a worktree whose only dirt is gc's own marker; stderr = %q", stderr.String())
	}
	if _, err := os.Stat(fx.worktreeB); !os.IsNotExist(err) {
		t.Errorf("worktree B still on disk after prune (stat err = %v)", err)
	}
}

// TestRealGitPruneInfoDoesNotCountItsOwnStaleMarkerAsWork is the session.Info
// form. Both forms carry independent copies of the same gate sequence, so a fix
// applied to one only is invisible from the other's tests.
func TestRealGitPruneInfoDoesNotCountItsOwnStaleMarkerAsWork(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	realGitWriteFile(t, filepath.Join(fx.worktreeB, worktreeStaleFileName), "branch=HEAD\nreason=unreachable-commits\n")

	var stderr bytes.Buffer
	pruneAgentHomeWorktreeIfSafeInfo(fx.sessionInfoFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)

	if _, err := os.Stat(fx.worktreeB); !os.IsNotExist(err) {
		t.Errorf("worktree B still on disk after prune (stat err = %v); stderr = %q", err, stderr.String())
	}
}

// TestRealGitPruneKeepsTheRealReasonWhenReMarking pins the provenance half of
// the same defect, which the test above cannot see because a successful prune
// leaves no marker to read.
//
// A slot that is legitimately held back -- here by a commit no ref reaches -- and
// that already carries a marker must be re-marked with the reason that actually
// held it back. Before the fix the pre-existing marker tripped the uncommitted
// gate first, so every re-mark read uncommitted-work whatever the truth was, and
// the marker overwrote its own provenance on each pass.
func TestRealGitPruneKeepsTheRealReasonWhenReMarking(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	// A commit on detached HEAD: no branch, tag, or remote ref reaches it, which
	// is exactly what the removal would orphan.
	realGitWriteFile(t, filepath.Join(fx.worktreeB, "tracked.txt"), "committed but unreachable\n")
	runGitInTest(t, fx.worktreeB, "add", "tracked.txt")
	runGitInTest(t, fx.worktreeB, "commit", "-m", "orphan-to-be")
	realGitWriteFile(t, filepath.Join(fx.worktreeB, worktreeStaleFileName), "branch=HEAD\nreason=unreachable-commits\n")

	var stderr bytes.Buffer
	if pruneAgentHomeWorktreeIfSafe(fx.sessionBeadFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr) {
		t.Fatal("prune returned true for a worktree holding commits no ref reaches")
	}
	assertWorktreeStaleMarker(t, fx.worktreeB, "HEAD", "unreachable-commits")
}

// TestRealGitStashSurvivesWorktreeRemoval records the experiment that justifies
// removing the gate rather than narrowing it, so the reasoning is reproducible
// and not merely asserted in a commit message.
//
// refs/stash lives in the repository's common directory. `git worktree remove`
// deletes the checkout and that worktree's admin directory under
// .git/worktrees/<name>, and touches no refs — so stashed work is recoverable
// after the removal, and gating removal on it protects nothing. This mirrors the
// reasoning already recorded for HasUnreachableCommitsResult
// (internal/git/git.go), which is narrower than the unpushed probe for the same
// reason.
//
// If this test ever fails, the removal of the stash gates is no longer safe and
// the gates must come back, scoped some other way.
func TestRealGitStashSurvivesWorktreeRemoval(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.stashIn(t, fx.worktreeA, "work that must survive")

	if !git.New(fx.rigRoot).HasStashes() {
		t.Fatal("stash not visible from the rig root before removal; fixture is wrong")
	}
	runGitInTest(t, fx.rigRoot, "worktree", "remove", "--force", fx.worktreeA)

	if !git.New(fx.rigRoot).HasStashes() {
		t.Fatal("stash vanished when its worktree was removed; the removed gates guarded real work after all")
	}
	// Reachability of the entry is what matters, not just the ref: apply it
	// somewhere else and confirm the content came back intact.
	runGitInTest(t, fx.worktreeB, "stash", "apply", "stash@{0}")
	data, err := os.ReadFile(filepath.Join(fx.worktreeB, "agent-wip.txt"))
	if err != nil {
		t.Fatalf("stashed file not recoverable after its worktree was removed: %v", err)
	}
	if got := string(data); got != "work in progress\n" {
		t.Errorf("recovered stash content = %q, want %q", got, "work in progress\n")
	}
}

// TestRealGitStashIsRepoWide pins the one property of git that the removal
// depends on: refs/stash is a single repo-wide ref, so a stash made in one
// worktree is reported by `git stash list` in every other worktree of the same
// repo. That is the whole mechanism by which one agent's stash wrote a
// stashed-work marker into a sibling slot that had nothing stashed.
//
// It deliberately asserts NOTHING about per-stash attribution. An earlier
// version of this test claimed git records nothing usable — that every slot is
// detached, so the reflog subject reads "On (no branch)" and every sibling
// shares the stash's parent commit. That was false of the city and false of the
// incident, and it is recorded here because believing it would send the next
// reader down a wrong path. Measured against the incident's own preserved
// stashes:
//
//	preserved/stash-ci-qbkr-superseded  "On fix/ci-w09j-..."  parent e7280eb41
//	preserved/stash-ci-ako1-superseded  "On main"             parent a68a9f0ab
//	toolsmith-2 (the bricked victim)    detached at f4538502f
//
// Both stashes named a branch, and all three commits differ, so a branch- or
// parent-scoped filter WOULD have discriminated here. Live, 8 of 10 gascity
// slots sit on named branches. Attribution was available; it was not the reason.
//
// The reason is that the gate protects nothing and its false positives are
// permanent. A stash survives worktree removal intact
// (TestRealGitStashSurvivesWorktreeRemoval), so a correctly-scoped stash gate
// would guard against no loss at all — while every false positive it did emit
// latches a slot out of service, because the marker it writes is untracked and
// so makes its own worktree dirty on the next teardown. A gate with nothing to
// win and a permanent loss on error is worth removing, not narrowing.
func TestRealGitStashIsRepoWide(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.stashIn(t, fx.worktreeA, "toolsmith-A private work")

	// Probed exactly as the production gates did, from each worktree in turn. An
	// identical answer from the worktree that owns the stash and one that does
	// not IS the defect: the question has no per-worktree answer to give.
	fromA, err := git.New(fx.worktreeA).HasStashesResult()
	if err != nil {
		t.Fatalf("stash probe in worktree A: %v", err)
	}
	fromB, err := git.New(fx.worktreeB).HasStashesResult()
	if err != nil {
		t.Fatalf("stash probe in worktree B: %v", err)
	}
	if !fromA {
		t.Fatal("stash not reported in the worktree that made it; fixture is wrong")
	}
	if fromB != fromA {
		t.Errorf("stash probe = %v in worktree B, %v in worktree A; refs/stash is no longer repo-wide, so per-worktree scoping may now be possible", fromB, fromA)
	}
}
