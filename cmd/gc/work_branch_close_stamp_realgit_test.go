package main

// Scope: the close-time gc.work_branch refresh, driven over REAL git worktrees
// so the branch a bead records is the one its worktree is actually on when the
// bead closes.
//
// Why this suite exists: the field had exactly one machine writer, the
// claim-time stamp, and a claim happens BEFORE the agent cuts its feature
// branch. A seat reuses one worktree across beads, so the sample taken at
// claim is whatever the PREVIOUS bead left checked out -- a real branch name,
// belonging to different work, which survives every sanity check a reader
// would apply. Measured on seven consecutive bench-engineer beads
// (ci-cocmug, 2026-09-20): 7 of 7 machine stamps named the prior bead's
// branch, and the three rows that read correctly were repaired by hand.
//
// Why the shape is two beads and not one. A single-bead fixture stamps
// correctly today by accident -- there is no prior branch for it to inherit --
// so the defect is invisible to it. These drive the sequence the seat actually
// delivers: close A on fix/A, leave the worktree there, claim B, cut fix/B,
// close B.
//
// The real-git half is not ceremony. hookResolveWorkBranch shells out to
// `git rev-parse --abbrev-ref HEAD`; a fake returning a canned string agrees
// with itself whatever the worktree holds and cannot tell a resolved branch
// from a detached HEAD or a pruned worktree, which are the two cases that must
// write NOTHING.
//
// Delegated elsewhere: the claim-time stamp's directory resolution lives in
// cmd_hook_claim_workerdir_realgit_test.go, and the release path's stamp in
// work_release_branch_realgit_test.go. This suite asserts only what the close
// seam writes.
//
// Run: go test ./cmd/gc/ -run 'WorkBranchCloseStamp'

import (
	"io"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// heldWorkBead creates an in-progress task bead carrying the metadata a claim
// would have stamped on it, and returns its id.
func heldWorkBead(t *testing.T, store beads.Store, meta map[string]string) string {
	t.Helper()
	created, err := store.Create(beads.Bead{Title: "work", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inProgress, holder := "in_progress", "seat-1"
	if err := store.Update(created.ID, beads.UpdateOpts{
		Status:   &inProgress,
		Assignee: &holder,
		Metadata: meta,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	return created.ID
}

func workBranchOf(t *testing.T, store beads.Store, id string) string {
	t.Helper()
	got, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get %s: %v", id, err)
	}
	return got.Metadata[beadmeta.WorkBranchMetadataKey]
}

// TestWorkBranchCloseStampNamesTheSecondBeadsOwnBranch is the reproduction.
// Two beads worked in sequence in ONE reused worktree: the second must close
// recording the branch it was worked on, not the one the first left behind.
//
// The assertion on bead A is half the test, and it is checked AFTER B so the
// reproduction fails on B. A refresh that walked every bead the seat ever held
// would "fix" B by rewriting A to B's branch -- the same defect pointed the
// other way.
func TestWorkBranchCloseStampNamesTheSecondBeadsOwnBranch(t *testing.T) {
	const (
		branchA = "fix/ci-8qs2ov-undeclared-mram-write"
		branchB = "fix/ci-mtfsyf-stale-board-state"
	)
	repo := newWorkBranchRepo(t, branchA)
	store := beads.NewMemStore()

	// Bead A: claimed on main, worked on branchA, closed from there. Its
	// claim-time stamp is the stale "main" the seat was on at claim.
	beadA := heldWorkBead(t, store, map[string]string{
		beadmeta.WorkBranchMetadataKey: "main",
		beadmeta.WorkDirMetadataKey:    repo,
		beadmeta.SessionIDMetadataKey:  "sess-a",
	})
	stampWorkBranchOnClose([]string{"close", beadA}, store, nil,
		workBranchCloseStampOps{ResolveBranch: hookResolveWorkBranch, SessionID: "sess-a"}, io.Discard)

	// The seat's worktree is NOT reset between beads, so bead B is claimed
	// while HEAD still names branchA -- which is what the claim stamps.
	beadB := heldWorkBead(t, store, map[string]string{
		beadmeta.WorkBranchMetadataKey: branchA,
		beadmeta.WorkDirMetadataKey:    repo,
		beadmeta.SessionIDMetadataKey:  "sess-b",
	})
	runGitInTest(t, repo, "switch", "-q", "-c", branchB)

	stampWorkBranchOnClose([]string{"close", beadB}, store, nil,
		workBranchCloseStampOps{ResolveBranch: hookResolveWorkBranch, SessionID: "sess-b"}, io.Discard)

	if got := workBranchOf(t, store, beadB); got != branchB {
		t.Fatalf("bead B gc.work_branch = %q, want %q -- the second bead of a reused worktree recorded the first bead's branch", got, branchB)
	}
	if got := workBranchOf(t, store, beadA); got != branchA {
		t.Fatalf("bead A gc.work_branch = %q, want its own %q, untouched by bead B's close", got, branchA)
	}
}

// TestWorkBranchCloseStampAcceptsBothCloseSpellings pins that the refresh
// covers `bd update --status=closed` as well as `bd close`. The worker
// formulas stamp metadata and close in one update, so a refresh keyed on the
// close subcommand alone would miss every formula-driven close while looking
// correct against an agent typing `gc bd close`.
func TestWorkBranchCloseStampAcceptsBothCloseSpellings(t *testing.T) {
	const branch = "fix/ci-to8uh9-mram-mosaic-residency"
	for _, tc := range []struct {
		name string
		args func(id string) []string
	}{
		{"close subcommand", func(id string) []string { return []string{"close", id} }},
		{"update --status=closed", func(id string) []string {
			return []string{"update", id, "--set-metadata", "gc.work_outcome=shipped", "--status=closed"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newWorkBranchRepo(t, branch)
			store := beads.NewMemStore()
			id := heldWorkBead(t, store, map[string]string{
				beadmeta.WorkBranchMetadataKey: "main",
				beadmeta.WorkDirMetadataKey:    repo,
				beadmeta.SessionIDMetadataKey:  "sess-1",
			})
			stampWorkBranchOnClose(tc.args(id), store, nil,
				workBranchCloseStampOps{ResolveBranch: hookResolveWorkBranch, SessionID: "sess-1"}, io.Discard)
			if got := workBranchOf(t, store, id); got != branch {
				t.Fatalf("gc.work_branch = %q, want %q", got, branch)
			}
		})
	}
}

// TestWorkBranchCloseStampWritesNothingOutsideItsWarrant is the refusal
// sweep. Each row is a case where the closing process cannot honestly speak
// for the worktree, and every one must leave the recorded handle exactly as
// the claim left it -- erasing it is worse than a stale value, because it
// destroys the only record of where a predecessor was working
// (withReleasedWorkBranch records the same reasoning for the release path).
func TestWorkBranchCloseStampWritesNothingOutsideItsWarrant(t *testing.T) {
	const (
		recorded = "fix/ci-r1mzh3-sweep-launch-poll"
		onDisk   = "fix/ci-4aubin-cross-check-findings"
	)
	// closer is the GC_SESSION_ID of the process running the close; "sess-1"
	// is the session that holds the bead in every row.
	for _, tc := range []struct {
		name string
		// mutate adjusts the claim-time metadata and may move the repo.
		mutate func(t *testing.T, repo string, meta map[string]string)
		args   func(id string) []string
		closer string
	}{
		{
			name: "another session closes the bead",
			// The closer is not the holder, so the holder's worktree may
			// already be on unrelated work. The mayor closing a stranded
			// bead is the live instance.
			closer: "sess-mayor",
		},
		{
			name:   "the closing process is not a session at all",
			closer: "",
		},
		{
			name:   "the bead names no worktree",
			closer: "sess-1",
			mutate: func(_ *testing.T, _ string, meta map[string]string) {
				delete(meta, beadmeta.WorkDirMetadataKey)
			},
		},
		{
			name:   "the worktree is gone",
			closer: "sess-1",
			mutate: func(t *testing.T, _ string, meta map[string]string) {
				meta[beadmeta.WorkDirMetadataKey] = t.TempDir()
			},
		},
		{
			name:   "the worktree is on a detached HEAD",
			closer: "sess-1",
			mutate: func(t *testing.T, repo string, _ map[string]string) {
				runGitInTest(t, repo, "switch", "-q", "--detach", "HEAD")
			},
		},
		{
			name:   "the bead is a control step",
			closer: "sess-1",
			mutate: func(_ *testing.T, _ string, meta map[string]string) {
				meta[beadmeta.KindMetadataKey] = "check"
			},
		},
		{
			name:   "the invocation is not a close",
			closer: "sess-1",
			args: func(id string) []string {
				return []string{"update", id, "--set-metadata", "gc.work_outcome=shipped"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newWorkBranchRepo(t, onDisk)
			store := beads.NewMemStore()
			meta := map[string]string{
				beadmeta.WorkBranchMetadataKey: recorded,
				beadmeta.WorkDirMetadataKey:    repo,
				beadmeta.SessionIDMetadataKey:  "sess-1",
			}
			if tc.mutate != nil {
				tc.mutate(t, repo, meta)
			}
			id := heldWorkBead(t, store, meta)
			args := []string{"close", id}
			if tc.args != nil {
				args = tc.args(id)
			}
			stampWorkBranchOnClose(args, store, nil,
				workBranchCloseStampOps{ResolveBranch: hookResolveWorkBranch, SessionID: tc.closer}, io.Discard)
			if got := workBranchOf(t, store, id); got != recorded {
				t.Fatalf("gc.work_branch = %q, want the claim-time %q left intact", got, recorded)
			}
		})
	}
}

// TestWorkBranchCloseStampRefreshesTheGatesPrefetchedCopy pins the coupling to
// the gate that runs after it. The work-record gate is handed the beads an
// earlier guard already read; if the refresh writes only to the store, that
// gate evaluates `shipped ⇒ commit reachable on gc.work_branch` against the
// stale branch it was holding and reports a false violation on the very close
// this refresh just made correct.
func TestWorkBranchCloseStampRefreshesTheGatesPrefetchedCopy(t *testing.T) {
	const branch = "fix/ci-td1za5-declare-run-wrapper-captures"
	repo := newWorkBranchRepo(t, branch)
	store := beads.NewMemStore()
	id := heldWorkBead(t, store, map[string]string{
		beadmeta.WorkBranchMetadataKey: "main",
		beadmeta.WorkDirMetadataKey:    repo,
		beadmeta.SessionIDMetadataKey:  "sess-1",
	})
	held, err := store.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	prefetched := map[string]beads.Bead{id: held}

	stampWorkBranchOnClose([]string{"close", id}, store, prefetched,
		workBranchCloseStampOps{ResolveBranch: hookResolveWorkBranch, SessionID: "sess-1"}, io.Discard)

	if got := prefetched[id].Metadata[beadmeta.WorkBranchMetadataKey]; got != branch {
		t.Fatalf("prefetched gc.work_branch = %q, want %q so the gates downstream read the refreshed value", got, branch)
	}
}

// countingWorkBranchStore counts Update calls so a test can assert that NO
// write was issued. A store that merely holds the right value cannot tell a
// suppressed write from one that wrote the same bytes.
type countingWorkBranchStore struct {
	beads.Store
	updates int
}

func (s *countingWorkBranchStore) Update(id string, opts beads.UpdateOpts) error {
	s.updates++
	return s.Store.Update(id, opts)
}

// TestWorkBranchCloseStampIssuesNoWriteWhenTheBranchIsUnchanged pins the
// compare-and-skip. Every close of every in-progress bead reaches this code,
// and an unconditional write would emit a bead.updated per close carrying no
// new information -- the cache-reconcile flood class the claim-time stamp
// documents at stampHookClaimIdentity. Asserting the stored VALUE cannot see
// this: a redundant write leaves the same value behind.
func TestWorkBranchCloseStampIssuesNoWriteWhenTheBranchIsUnchanged(t *testing.T) {
	const branch = "fix/ci-l8kfw5-declare-mram-left"
	repo := newWorkBranchRepo(t, branch)
	store := &countingWorkBranchStore{Store: beads.NewMemStore()}
	id := heldWorkBead(t, store, map[string]string{
		beadmeta.WorkBranchMetadataKey: branch,
		beadmeta.WorkDirMetadataKey:    repo,
		beadmeta.SessionIDMetadataKey:  "sess-1",
	})
	store.updates = 0

	stampWorkBranchOnClose([]string{"close", id}, store, nil,
		workBranchCloseStampOps{ResolveBranch: hookResolveWorkBranch, SessionID: "sess-1"}, io.Discard)

	if store.updates != 0 {
		t.Fatalf("%d store writes, want 0 when the recorded branch already matches HEAD", store.updates)
	}
}

// TestWorkBranchCloseStampNeverResolvesAnUnknownWorktree pins the documented
// absence: a bead naming no worktree gets NO branch read at all. The refusal
// has to be observed at the resolver call, not at the stored value -- git
// against an empty path fails and returns "", so removing the guard leaves
// the value correct and the absence unproven, while the code would have
// acquired a path on which a later fallback to the scope root looks harmless.
func TestWorkBranchCloseStampNeverResolvesAnUnknownWorktree(t *testing.T) {
	const recorded = "fix/ci-4aubin-cross-check-findings"
	store := beads.NewMemStore()
	id := heldWorkBead(t, store, map[string]string{
		beadmeta.WorkBranchMetadataKey: recorded,
		beadmeta.SessionIDMetadataKey:  "sess-1",
	})
	resolve := func(dir string) string {
		t.Fatalf("resolver consulted with dir=%q for a bead carrying no %s", dir, beadmeta.WorkDirMetadataKey)
		return ""
	}

	stampWorkBranchOnClose([]string{"close", id}, store, nil,
		workBranchCloseStampOps{ResolveBranch: resolve, SessionID: "sess-1"}, io.Discard)

	if got := workBranchOf(t, store, id); got != recorded {
		t.Fatalf("gc.work_branch = %q, want the claim-time %q left intact", got, recorded)
	}
}
