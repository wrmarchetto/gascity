package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
	"github.com/gastownhall/gascity/internal/pathutil"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// gitProbe is the slice of internal/git.Git used by the worker-dir
// auto-prune path. Defined as an interface so tests can inject a fake
// without standing up real git worktrees.
//
// HasStashesResult is deliberately ABSENT and must not be added back, in any
// scoping. It runs `git stash list`, and refs/stash is a single repo-wide ref:
// every worktree of a repo gets the same answer, so one stash anywhere made this
// gate true for every slot, each teardown wrote a stashed-work marker, and the
// reconciler then refused every spawn into those slots (bead ci-auomj).
//
// Narrowing the question — filtering to the stash's recorded branch or parent
// commit — was available and was rejected, so do not add it back "scoped
// properly" either. Two reasons, and the second is the load-bearing one:
//
//   - There is nothing to protect. A stash lives on refs/stash in the common
//     dir and survives `git worktree remove` intact, so no version of this gate
//     prevents a loss. TestRealGitStashSurvivesWorktreeRemoval pins that.
//   - A wrong answer here is permanent, not transient. The marker this path
//     writes is untracked and not gitignored, so it makes its own worktree
//     dirty, and the next teardown re-marks the slot as uncommitted-work
//     whatever the original reason. A gate that wins nothing cannot justify a
//     latch that costs a slot.
//
// HasUnpushedCommitsResult is ABSENT for the same reason, one step weaker. Push
// state is not what this path destroys: `git worktree remove` deletes the
// checkout, never refs/heads, so commits a local branch still reaches survive
// it. Asking `git log HEAD --not --remotes` therefore latched a marker onto
// every slot that had committed before its branch landed -- the normal teardown
// of this pool, not an edge case. Measured 2026-08-12: toolsmith-1 marked
// reason=unpushed-commits with unpushed=1 unreachable=0, nothing at risk, slot
// out of service (bead ci-hh8aa). HasUnreachableCommitsResult asks what the
// removal actually orphans; internal/git states the same distinction from the
// probe's side, and cmd/gc/bead_worktree_reaper.go was migrated onto it first
// (1baf214eb).
//
// internal/doctor's NestedWorktreePruneCheck still gates on push state and is
// NOT wrong to: it removes only nested worktrees it can reproduce with
// `git worktree add <path> origin/<branch>`, so a remote ref is its recovery
// path rather than a proxy for safety.
//
// HasUncommittedWork is absent in favor of HasUncommittedWorkExcluding for a
// third instance of the same shape: the marker this path writes is untracked
// and lands in the worktree it describes, so the plain probe answered "dirty"
// on gc's own bookkeeping file. That made every false marker permanent and
// overwrote reason= with uncommitted-work on each pass, which is why the reason
// on a bricked slot could not be trusted when diagnosing (bead ci-ciu63). Do
// not narrow the exclusion to "only when the marker is untracked" -- the tracked
// variant is what bead ci-2uh5p was.
type gitProbe interface {
	IsRepo() bool
	CurrentBranch() (string, error)
	HasUncommittedWorkExcluding(paths ...string) bool
	HasUnreachableCommitsResult() (bool, error)
	WorktreeRemove(path string, force bool) error
}

// newGitProbe returns a gitProbe scoped to the given directory. Indirected
// through a package-level var so tests can stub the git invocations.
var newGitProbe = func(workDir string) gitProbe { return git.New(workDir) }

// writeWorktreeStaleMarker records why workerDir was left in place instead of
// pruned, so cleanupClosedBeadAgentHomeWorktrees (agent_home_worktree_cleanup.go)
// can later detect when it's safe to reclaim. Best-effort: write failures are
// logged but never alter the caller's control flow.
func writeWorktreeStaleMarker(gp gitProbe, workerDir, reason string, stderr io.Writer) {
	branch, err := gp.CurrentBranch()
	if err != nil {
		branch = ""
	}
	content := fmt.Sprintf("branch=%s\nreason=%s\n", branch, reason)
	if err := os.WriteFile(filepath.Join(workerDir, worktreeStaleFileName), []byte(content), 0o644); err != nil {
		fmt.Fprintf(stderr, "session reconciler: writing %s marker for %s: %v\n", worktreeStaleFileName, workerDir, err) //nolint:errcheck
	}
}

// pruneAgentHomeWorktreeIfSafe removes the worktree at the closed session's
// worker_dir, after applying the safety gates listed below. Returns true when
// the removal actually happened.
//
// The gate set is deliberately NOT doctor's NestedWorktreePruneCheck set, which
// this comment claimed until bead ci-hh8aa: the commit gate here is
// reachability, not push state. See the gitProbe doc comment for why the two
// callers legitimately differ.
//
// The decision is mechanical, never role-coupled: any pool-managed agent
// worktree that lives under the city's .gc/worktrees/ tree, is a git
// worktree, and probes clean is safe to reclaim. Pool sessions are
// transient by design — their worktrees were never meant to outlive the
// session bead.
//
// No-op when:
//   - cfg.Daemon.AutoPruneWorkerDir is false
//   - the session bead has no worker_dir metadata
//   - the worker_dir does not live under cityPath/.gc/worktrees/
//   - the worker_dir is missing on disk or has no .git pointer
//   - the worktree has uncommitted changes, or commits no branch, tag, or
//     remote-tracking ref reaches
//   - the rig that owns the session cannot be resolved to a filesystem path
//
// Removal failures are logged but never surfaced — an orphaned worktree
// still shows up via `gc doctor` later, which is the operator's existing
// reclaim path.
func pruneAgentHomeWorktreeIfSafe(session beads.Bead, cityPath string, cfg *config.City, stderr io.Writer) bool {
	if cfg == nil || !cfg.Daemon.AutoPruneWorkerDirEnabled() {
		return false
	}
	workerDir := strings.TrimSpace(contract.WorkerDirFromMetadata(session.Metadata))
	if workerDir == "" {
		return false
	}
	if !filepath.IsAbs(workerDir) {
		return false
	}

	wtRoot := filepath.Join(cityPath, ".gc", "worktrees")
	if !pathutil.PathWithin(wtRoot, workerDir) || pathutil.SamePath(wtRoot, workerDir) {
		return false
	}

	if _, err := os.Stat(filepath.Join(workerDir, ".git")); err != nil {
		// Already gone, or never a worktree — nothing to do.
		return false
	}

	gp := newGitProbe(workerDir)
	if !gp.IsRepo() {
		return false
	}
	// The marker this path may be about to write is excluded from the probe:
	// see the gitProbe doc comment.
	if gp.HasUncommittedWorkExcluding(worktreeStaleFileName) {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: has uncommitted changes\n", workerDir) //nolint:errcheck
		writeWorktreeStaleMarker(gp, workerDir, "uncommitted-work", stderr)
		return false
	}
	hasUnreachable, err := gp.HasUnreachableCommitsResult()
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: unreachable-commit probe failed: %v\n", workerDir, err) //nolint:errcheck
		return false
	}
	if hasUnreachable {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: has commits no branch, tag, or remote ref reaches; removing it would orphan them\n", workerDir) //nolint:errcheck
		writeWorktreeStaleMarker(gp, workerDir, "unreachable-commits", stderr)
		return false
	}

	// No stash gate here, on purpose — see the gitProbe doc comment.

	// Run `git worktree remove` from the rig root rather than from the
	// worktree being removed: git refuses to remove a worktree whose path
	// equals cwd in some configurations, and operating from cwd of a
	// directory we are about to delete is fragile in general.
	rigRoot := lookupRigRootForSession(session, cfg)
	if rigRoot == "" {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: rig path unresolved\n", workerDir) //nolint:errcheck
		return false
	}
	if err := newGitProbe(rigRoot).WorktreeRemove(workerDir, true); err != nil {
		fmt.Fprintf(stderr, "session reconciler: pruning worker_dir %s: %v\n", workerDir, err) //nolint:errcheck
		return false
	}
	fmt.Fprintf(stderr, "session reconciler: pruned worker_dir %s (session %s)\n", workerDir, session.Metadata["session_name"]) //nolint:errcheck
	return true
}

// pruneAgentHomeWorktreeIfSafeInfo is the session.Info form of
// pruneAgentHomeWorktreeIfSafe: the worker_dir read routes through
// session.WorkerDirFromInfo (the canonical→legacy Info fallback equivalent to
// contract.WorkerDirFromMetadata), the rig-root lookup reads Info.Template via
// lookupRigRootForSessionInfo, and the log line reads Info.SessionNameMetadata —
// every safety gate and the removal itself are unchanged. Byte-identical to the
// raw form, which survives for its test callers.
func pruneAgentHomeWorktreeIfSafeInfo(info sessionpkg.Info, cityPath string, cfg *config.City, stderr io.Writer) {
	if cfg == nil || !cfg.Daemon.AutoPruneWorkerDirEnabled() {
		return
	}
	workerDir := strings.TrimSpace(sessionpkg.WorkerDirFromInfo(info))
	if workerDir == "" {
		return
	}
	if !filepath.IsAbs(workerDir) {
		return
	}

	wtRoot := filepath.Join(cityPath, ".gc", "worktrees")
	if !pathutil.PathWithin(wtRoot, workerDir) || pathutil.SamePath(wtRoot, workerDir) {
		return
	}

	if _, err := os.Stat(filepath.Join(workerDir, ".git")); err != nil {
		return
	}

	gp := newGitProbe(workerDir)
	if !gp.IsRepo() {
		return
	}
	// The marker this path may be about to write is excluded from the probe:
	// see the gitProbe doc comment.
	if gp.HasUncommittedWorkExcluding(worktreeStaleFileName) {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: has uncommitted changes\n", workerDir) //nolint:errcheck
		writeWorktreeStaleMarker(gp, workerDir, "uncommitted-work", stderr)
		return
	}
	hasUnreachable, err := gp.HasUnreachableCommitsResult()
	if err != nil {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: unreachable-commit probe failed: %v\n", workerDir, err) //nolint:errcheck
		return
	}
	if hasUnreachable {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: has commits no branch, tag, or remote ref reaches; removing it would orphan them\n", workerDir) //nolint:errcheck
		writeWorktreeStaleMarker(gp, workerDir, "unreachable-commits", stderr)
		return
	}

	// No stash gate here, on purpose — see the gitProbe doc comment.

	rigRoot := lookupRigRootForSessionInfo(info, cfg)
	if rigRoot == "" {
		fmt.Fprintf(stderr, "session reconciler: not pruning worker_dir %s: rig path unresolved\n", workerDir) //nolint:errcheck
		return
	}
	if err := newGitProbe(rigRoot).WorktreeRemove(workerDir, true); err != nil {
		fmt.Fprintf(stderr, "session reconciler: pruning worker_dir %s: %v\n", workerDir, err) //nolint:errcheck
		return
	}
	fmt.Fprintf(stderr, "session reconciler: pruned worker_dir %s (session %s)\n", workerDir, info.SessionNameMetadata) //nolint:errcheck
}

// lookupRigRootForSession returns the filesystem path of the rig that owns
// the given session bead, derived from the qualified template metadata
// ("<rig>/<template>"). Returns "" when the rig cannot be identified or
// has no configured path.
func lookupRigRootForSession(session beads.Bead, cfg *config.City) string {
	qt := strings.TrimSpace(session.Metadata["template"])
	slash := strings.IndexByte(qt, '/')
	if slash <= 0 {
		return ""
	}
	rigName := qt[:slash]
	for i := range cfg.Rigs {
		if cfg.Rigs[i].Name == rigName {
			return strings.TrimSpace(cfg.Rigs[i].Path)
		}
	}
	return ""
}

// lookupRigRootForSessionInfo is the session.Info form of
// lookupRigRootForSession: it reads the qualified template off Info.Template (the
// verbatim raw mirror of b.Metadata["template"]), so the rig resolution is
// byte-identical to the raw form.
func lookupRigRootForSessionInfo(info sessionpkg.Info, cfg *config.City) string {
	qt := strings.TrimSpace(info.Template)
	slash := strings.IndexByte(qt, '/')
	if slash <= 0 {
		return ""
	}
	rigName := qt[:slash]
	for i := range cfg.Rigs {
		if cfg.Rigs[i].Name == rigName {
			return strings.TrimSpace(cfg.Rigs[i].Path)
		}
	}
	return ""
}
