package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/git"
)

// Scope: the commit-safety gate of the worker-dir auto-prune path, exercised
// against REAL git worktrees. Pins that the gate asks what the removal
// destroys -- commits no ref reaches -- rather than what has been pushed.
//
// This is a separate file from session_worktree_prune_realgit_test.go, whose
// subject is the stash gate, but it reuses that file's newRealGitPruneFixture
// wholesale: a real bare origin, a real rig clone and two real worktrees under
// .gc/worktrees is the shape both gates need, and a second fixture would
// diverge from it.
//
// It cannot be written against fakeGitProbe (session_worktree_prune_test.go).
// That stub carries one bool modeling the ANSWER, so flipping the production
// call from HasUnpushedCommitsResult to HasUnreachableCommitsResult leaves
// every fake-probe test green -- the fake would answer the new question with
// the old field. Only real git can distinguish the two questions, because the
// distinction lives in which refs `git log --not` is given.
//
// Run: go test ./cmd/gc/ -run RealGitPruneReachability

// commitOnLocalBranchIn cuts a named branch in the given worktree and commits
// to it without pushing, leaving HEAD reachable from refs/heads and from no
// remote-tracking ref.
//
// That is toolsmith-1's measured state on 2026-08-12 (marker
// branch=fix/ci-k5ey0-renew-runtime-provider-waivers reason=unpushed-commits,
// unpushed=1 unreachable=0) and it is the normal shape of every pool slot that
// has committed but whose branch has not yet landed -- not an edge case.
func (fx *realGitPruneFixture) commitOnLocalBranchIn(t *testing.T, worktree, branch string) {
	t.Helper()
	runGitInTest(t, worktree, "switch", "-c", branch)
	realGitWriteFile(t, filepath.Join(worktree, "tracked.txt"), "agent work, committed locally\n")
	runGitInTest(t, worktree, "add", "tracked.txt")
	runGitInTest(t, worktree, "commit", "-m", "agent work")
}

// commitOnDetachedHEADIn commits in a detached worktree, so the new commit is
// reachable from that worktree's HEAD and from no branch, tag, or remote ref.
// Removing the worktree is what would orphan it, which makes this the one
// commit state the gate exists to protect.
func (fx *realGitPruneFixture) commitOnDetachedHEADIn(t *testing.T, worktree string) {
	t.Helper()
	realGitWriteFile(t, filepath.Join(worktree, "tracked.txt"), "work no ref reaches\n")
	runGitInTest(t, worktree, "add", "tracked.txt")
	runGitInTest(t, worktree, "commit", "-m", "orphan-to-be")
}

// assertPruneGatePremise fails the test unless the worktree is in the state
// that makes the assertions below meaningful: clean, unpushed, and reachable or
// not as wantUnreachable says.
//
// Unpushed is asserted unconditionally rather than passed in, because every
// test in this file needs the OLD gate to have fired -- a case where it did not
// would prove nothing about which question the gate asks. That half
// deliberately probes something production no longer calls: without it a green
// run is indistinguishable from a fixture that simply never tripped the old
// gate, which is exactly how the fake-probe suite stayed green against this
// defect. Both probes are read from internal/git so the premise is measured,
// not inferred from how the fixture was built.
func assertPruneGatePremise(t *testing.T, worktree string, wantUnreachable bool) {
	t.Helper()
	g := git.New(worktree)
	if g.HasUncommittedWork() {
		t.Fatal("worktree is dirty; the uncommitted gate would fire first and mask the commit gate")
	}
	unpushed, err := g.HasUnpushedCommitsResult()
	if err != nil {
		t.Fatalf("unpushed probe: %v", err)
	}
	if !unpushed {
		t.Fatal("fixture wrong: HasUnpushedCommitsResult() = false, so the old gate would not have fired and a pass proves nothing")
	}
	unreachable, err := g.HasUnreachableCommitsResult()
	if err != nil {
		t.Fatalf("unreachable probe: %v", err)
	}
	if unreachable != wantUnreachable {
		t.Fatalf("fixture wrong: HasUnreachableCommitsResult() = %v, want %v", unreachable, wantUnreachable)
	}
}

// TestRealGitPruneReachabilityReclaimsALocallyCommittedSlot pins the invariant
// the fix exists for: a slot whose HEAD a local branch still reaches is
// reclaimable, because `git worktree remove` deletes the checkout and not
// refs/heads.
//
// The skipped prune is the lesser damage. The marker is durable and no consumer
// re-reads the git state that produced it -- validateWorkDirForSessionAssignment
// (session_reconciler.go) refuses every future assignment into a marked
// directory -- so one teardown on a locally-committed branch takes the slot out
// of service permanently. Every agent in this pool commits before its branch
// lands, so that is the pool's normal teardown path.
func TestRealGitPruneReachabilityReclaimsALocallyCommittedSlot(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.commitOnLocalBranchIn(t, fx.worktreeB, "fix/ci-hh8aa-local-only")
	assertPruneGatePremise(t, fx.worktreeB, false)

	var stderr bytes.Buffer
	pruned := pruneAgentHomeWorktreeIfSafe(fx.sessionBeadFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)

	assertNoWorktreeStaleMarker(t, fx.worktreeB)
	if !pruned {
		t.Errorf("prune returned false for a worktree whose branch still reaches HEAD; stderr = %q", stderr.String())
	}
	if _, err := os.Stat(fx.worktreeB); !os.IsNotExist(err) {
		t.Errorf("worktree B still on disk after prune (stat err = %v)", err)
	}
	// The premise of calling the removal non-destructive, asserted rather than
	// argued: the branch, and so the commit, is still there afterwards.
	// runGitInTest rather than internal/git because there is no ref-existence
	// probe in internal/git and the production gate reads none -- routing this
	// read through a probe would exercise nothing the gate consults.
	runGitInTest(t, fx.rigRoot, "rev-parse", "--verify", "refs/heads/fix/ci-hh8aa-local-only")
}

// TestRealGitPruneReachabilityInfoReclaimsALocallyCommittedSlot is the
// session.Info form. Both forms are pinned because they carry independent
// copies of the same gate sequence, so a fix applied to one is invisible from
// the other's tests.
func TestRealGitPruneReachabilityInfoReclaimsALocallyCommittedSlot(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.commitOnLocalBranchIn(t, fx.worktreeB, "fix/ci-hh8aa-local-only-info")
	assertPruneGatePremise(t, fx.worktreeB, false)

	var stderr bytes.Buffer
	pruneAgentHomeWorktreeIfSafeInfo(fx.sessionInfoFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)

	assertNoWorktreeStaleMarker(t, fx.worktreeB)
	if _, err := os.Stat(fx.worktreeB); !os.IsNotExist(err) {
		t.Errorf("worktree B still on disk after prune (stat err = %v); stderr = %q", err, stderr.String())
	}
	runGitInTest(t, fx.rigRoot, "rev-parse", "--verify", "refs/heads/fix/ci-hh8aa-local-only-info")
}

// TestRealGitPruneReachabilityStillProtectsOrphanCommits is the other half of
// the pair, and without it the fix trades one failure for its opposite: a
// commit on a detached HEAD that no branch, tag, or remote ref reaches is
// destroyed by the removal, so it must still stop the prune and still leave a
// marker for the operator.
func TestRealGitPruneReachabilityStillProtectsOrphanCommits(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.commitOnDetachedHEADIn(t, fx.worktreeB)
	assertPruneGatePremise(t, fx.worktreeB, true)

	var stderr bytes.Buffer
	if pruneAgentHomeWorktreeIfSafe(fx.sessionBeadFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr) {
		t.Fatal("prune returned true for a detached worktree holding a commit no ref reaches")
	}
	if _, err := os.Stat(fx.worktreeB); err != nil {
		t.Errorf("worktree B removed despite holding orphan commits: %v", err)
	}
	assertWorktreeStaleMarker(t, fx.worktreeB, "HEAD", "unreachable-commits")
}

// TestRealGitPruneReachabilityInfoStillProtectsOrphanCommits is the
// session.Info form of the protection half.
func TestRealGitPruneReachabilityInfoStillProtectsOrphanCommits(t *testing.T) {
	fx := newRealGitPruneFixture(t)
	fx.commitOnDetachedHEADIn(t, fx.worktreeB)
	assertPruneGatePremise(t, fx.worktreeB, true)

	var stderr bytes.Buffer
	pruneAgentHomeWorktreeIfSafeInfo(fx.sessionInfoFor(fx.worktreeB), fx.cityPath, fx.cfg, &stderr)

	if _, err := os.Stat(fx.worktreeB); err != nil {
		t.Errorf("worktree B removed despite holding orphan commits: %v", err)
	}
	assertWorktreeStaleMarker(t, fx.worktreeB, "HEAD", "unreachable-commits")
}
