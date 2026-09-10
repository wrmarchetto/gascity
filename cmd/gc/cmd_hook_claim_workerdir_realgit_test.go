package main

// Scope: WHICH directory the claim-time gc.work_branch / gc.work_dir stamp
// resolves, driven over a REAL git worktree layout -- a main checkout plus a
// linked worktree on a feature branch -- with the PRODUCTION branch resolver.
//
// Why this suite exists: the stamp used to resolve `dir`, the bead store's
// shared checkout (agentCommandDir, cmd/gc/cmd_start.go), so the field
// recorded the operator's working state re-sampled at every hook tick and
// never saw the agent's worktree at all. Measured: 29 of 127 decidable
// non-main stamps were the city root transiently on a feature branch,
// carried across DIFFERENT assignees in DIFFERENT worktrees
// (docs/work-branch-semantics.md on the city repo).
//
// The real-git half is the whole point and cannot be delegated to a fake.
// cmd_hook_claim_stamp_test.go injects ResolveWorkBranch as a
// constant-returning fake over a dir of "/tmp/work" that is not a repository,
// so it asserts THAT a branch is stamped and never WHICH tree was read: no
// assertion against that fake can distinguish the shared root from the
// worker's worktree. Only the production resolver over two real trees on two
// different branches can.
//
// It delegates the patch-minimality and session-identity contracts to
// cmd_hook_claim_stamp_test.go, and the release/retirement stamp to
// work_release_branch_realgit_test.go.
//
// Run: go test ./cmd/gc/ -run HookClaimWorkerDir

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// claimWorkerBranch is the branch the agent's own worktree sits on in every
// case here. Fixed rather than a parameter: only the SHARED root's branch
// varies between cases, and the whole suite asserts that varying it changes
// nothing.
const claimWorkerBranch = "feat/x"

// newClaimWorktreeLayout builds the two-tree shape a city agent actually runs
// in: a shared bead-store checkout parked on rootBranch, plus a linked
// worktree on claimWorkerBranch. Returns (rootDir, workerDir).
//
// A linked worktree rather than a second clone, because that is what
// worktree-advance.sh provisions for a pool slot, and `git -C <linked>
// rev-parse --abbrev-ref HEAD` is the read that has to keep working through
// the .git-file indirection.
func newClaimWorktreeLayout(t *testing.T, rootBranch string) (string, string) {
	t.Helper()
	root := t.TempDir()
	runGitInTest(t, root, "init", "-q", "-b", "main")
	runGitInTest(t, root, "config", "user.email", "test@example.com")
	runGitInTest(t, root, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitInTest(t, root, "add", "f.txt")
	runGitInTest(t, root, "commit", "-q", "-m", "seed")

	worker := filepath.Join(t.TempDir(), "worker")
	runGitInTest(t, root, "worktree", "add", "-q", "-b", claimWorkerBranch, worker)
	if rootBranch != "main" {
		runGitInTest(t, root, "switch", "-q", "-c", rootBranch)
	}
	return root, worker
}

// realGitClaimOps is poolClaimOps with the branch resolver left UNSET, so
// applyDefaults installs the production hookResolveWorkBranch, and with the
// session-bead read answering from sessionMeta.
func realGitClaimOps(sessionMeta map[string]string, spy *stampMetaSpy) hookClaimOps {
	ops := poolClaimOps(
		`[{"id":"hw-tree","status":"open","metadata":{"gc.routed_to":"worker"}}]`,
		map[string]string{"gc.routed_to": "worker"},
		"", // ignored: ResolveWorkBranch is cleared below
		spy,
	)
	ops.ResolveWorkBranch = nil
	ops.ReadSessionBead = func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
		return beads.Bead{ID: id, Type: "session", Metadata: sessionMeta}, nil
	}
	return ops
}

// TestHookClaimWorkerDirStampsTheAgentsWorktreeNotTheStoreRoot is the primary
// case. Both worker-dir spellings are driven: worker_dir is the canonical key
// and work_dir the legacy one that live session beads in this city still
// carry, and reading either one alone resolves nothing for half the fleet --
// the same single-field mistake work_release_branch_realgit_test.go caught on
// the retirement path.
func TestHookClaimWorkerDirStampsTheAgentsWorktreeNotTheStoreRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{"canonical worker_dir", beadmeta.WorkerDirMetadataKey},
		{"legacy work_dir", beadmeta.LegacyWorkDirMetadataKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, worker := newClaimWorktreeLayout(t, "main")
			spy := &stampMetaSpy{}
			ops := realGitClaimOps(map[string]string{tc.key: worker}, spy)

			var stdout, stderr bytes.Buffer
			if code := doHookClaim("bd ready --json", root, poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
				t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
			}
			if spy.calls != 1 {
				t.Fatalf("StampWorkMeta calls = %d, want 1; stderr=%s", spy.calls, stderr.String())
			}
			if got := spy.patch[beadmeta.WorkBranchMetadataKey]; got != claimWorkerBranch {
				t.Fatalf("stamped %s=%q, want %q -- the claim resolved the store root, not the agent's worktree",
					beadmeta.WorkBranchMetadataKey, got, claimWorkerBranch)
			}
			if got := spy.patch[beadmeta.WorkDirMetadataKey]; got != worker {
				t.Fatalf("stamped %s=%q, want %q -- ADR-0009 says the branch is stamped alongside the work dir, and the close gate falls back to the scope root without it",
					beadmeta.WorkDirMetadataKey, got, worker)
			}
		})
	}
}

// TestHookClaimWorkerDirIgnoresAFeatureBranchOnTheSharedRoot is the mutation
// arm, and it is the one that kills the measured W1 writer. Park the shared
// checkout on a branch nobody's work is on -- exactly what the city root does
// while the operator reviews a feature -- and the stamp must not move.
//
// The arm asserts the NEGATIVE explicitly rather than only re-asserting the
// positive: a resolver that returned the empty string for every tree would
// satisfy "not feat/unrelated" while stamping nothing at all.
func TestHookClaimWorkerDirIgnoresAFeatureBranchOnTheSharedRoot(t *testing.T) {
	const rootBranch = "feat/unrelated"
	root, worker := newClaimWorktreeLayout(t, rootBranch)
	spy := &stampMetaSpy{}
	ops := realGitClaimOps(map[string]string{beadmeta.WorkerDirMetadataKey: worker}, spy)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", root, poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	got := spy.patch[beadmeta.WorkBranchMetadataKey]
	if got == rootBranch {
		t.Fatalf("stamped %s=%q -- the shared root's transient branch leaked onto the bead", beadmeta.WorkBranchMetadataKey, got)
	}
	if got != claimWorkerBranch {
		t.Fatalf("stamped %s=%q, want %q", beadmeta.WorkBranchMetadataKey, got, claimWorkerBranch)
	}
}

// TestHookClaimWorkerDirStampsNothingWhenTheWorktreeIsUnknown pins the
// documented absence: with no resolvable worker dir there is NO fallback to
// the store root. A fallback would reinstate the whole defect for every
// session bead that carries no dir, and a wrong branch is worse than none --
// it is read as a pointer to work that is not there.
func TestHookClaimWorkerDirStampsNothingWhenTheWorktreeIsUnknown(t *testing.T) {
	root, _ := newClaimWorktreeLayout(t, "feat/unrelated")
	spy := &stampMetaSpy{}
	ops := realGitClaimOps(map[string]string{}, spy)

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", root, poolClaimOpts(), ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if got, ok := spy.patch[beadmeta.WorkBranchMetadataKey]; ok {
		t.Fatalf("stamped %s=%q from a session with no worker dir; the store root must never be resolved", beadmeta.WorkBranchMetadataKey, got)
	}
	if got, ok := spy.patch[beadmeta.WorkDirMetadataKey]; ok {
		t.Fatalf("stamped %s=%q from a session with no worker dir", beadmeta.WorkDirMetadataKey, got)
	}
	// The session back-reference is unaffected by an unknown worktree: it is
	// the key that must survive when the branch cannot be resolved.
	if got := spy.patch[beadmeta.SessionIDMetadataKey]; got != "mc-sess1" {
		t.Fatalf("stamped %s=%q, want mc-sess1", beadmeta.SessionIDMetadataKey, got)
	}
}

// TestHookClaimWorkerDirSkipsTheSessionReadWithoutASession bounds the cost of
// the extra store read this fix introduces. It runs once per claim tick
// against a live bd subprocess, so a path with no session bead to name must
// not pay for it -- and must not stamp a branch either, since there is no
// worker whose tree could be resolved.
func TestHookClaimWorkerDirSkipsTheSessionReadWithoutASession(t *testing.T) {
	root, worker := newClaimWorktreeLayout(t, "feat/unrelated")
	spy := &stampMetaSpy{}
	reads := 0
	ops := realGitClaimOps(map[string]string{beadmeta.WorkerDirMetadataKey: worker}, spy)
	inner := ops.ReadSessionBead
	ops.ReadSessionBead = func(ctx context.Context, dir string, env []string, id, actor string) (beads.Bead, error) {
		reads++
		return inner(ctx, dir, env, id, actor)
	}
	opts := poolClaimOpts()
	opts.Env = []string{"GC_SESSION_NAME=gc__role-mc-sess1"} // GC_SESSION_ID absent

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", root, opts, ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}
	if reads != 0 {
		t.Fatalf("session-bead reads = %d, want 0 with no GC_SESSION_ID", reads)
	}
	if got, ok := spy.patch[beadmeta.WorkBranchMetadataKey]; ok {
		t.Fatalf("stamped %s=%q with no session to resolve a worktree from", beadmeta.WorkBranchMetadataKey, got)
	}
}
