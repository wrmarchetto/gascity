package main

// Scope: the gc.work_branch stamp a work release writes, driven over a REAL
// git worktree for the retirement path and with literal branch values for the
// release facade.
//
// Why this suite exists: gc.work_branch is resolved ONCE, at claim time
// (hookClaimIdentityPatch, cmd/gc/cmd_hook_claim.go), and an agent runs
// `gc hook --claim` exactly once per session, BEFORE it cuts its feature
// branch. The stamp therefore said "main" while the work sat on
// feat/<bead>-<slug>. When the session died, the bead was released carrying
// that stale handle, so the next claimant had no way to find ~40 minutes of
// uncommitted work and redid it, and the salvage had to be done by hand
// (ci-q3qbo9; measured on gs-eh2 and as-2mhs, 2026-09-07).
//
// The real-git half is not ceremony. hookResolveWorkBranch shells out to
// `git rev-parse --abbrev-ref HEAD`, so a fake returning a canned string
// would agree with itself whatever the worktree holds, and could not tell a
// resolved branch from a detached HEAD or a missing worktree -- the two cases
// that must stamp NOTHING.
//
// It delegates the release CAS contract to work_assignment_release_cas_test.go.
//
// Run: go test ./cmd/gc/ -run 'WorkReleaseBranch'

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/workrelease"
)

// newWorkBranchRepo makes a real repo on branch `main` with one commit, then
// leaves it on branch, returning its path.
func newWorkBranchRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	runGitInTest(t, dir, "init", "-q", "-b", "main")
	runGitInTest(t, dir, "config", "user.email", "test@example.com")
	runGitInTest(t, dir, "config", "user.name", "test")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runGitInTest(t, dir, "add", "f.txt")
	runGitInTest(t, dir, "commit", "-q", "-m", "seed")
	if branch != "main" {
		runGitInTest(t, dir, "switch", "-q", "-c", branch)
	}
	return dir
}

// TestWorkReleaseBranchRetirementStampsTheWorktreesActualBranch is the
// end-to-end case: a stranded session whose worktree is on a feature branch
// must release its work carrying THAT branch, not the one recorded at claim
// time.
//
// Run over BOTH worker-dir spellings. Info.WorkerDir is the canonical mirror
// and Info.WorkDir only the legacy one, so reading the legacy field directly
// resolves nothing for a session carrying the canonical key and leaves every
// stamp silently stale. A single-field case would pass over exactly that
// mistake -- it was made and caught here.
func TestWorkReleaseBranchRetirementStampsTheWorktreesActualBranch(t *testing.T) {
	const featureBranch = "feat/gs-eh2-mirror-daemon"

	for _, tc := range []struct {
		name string
		info func(repo string) sessionpkg.Info
	}{
		{"canonical worker_dir", func(repo string) sessionpkg.Info {
			return sessionpkg.Info{ID: "sess-1", SessionName: "sess-1", WorkerDir: repo}
		}},
		{"legacy work_dir", func(repo string) sessionpkg.Info {
			return sessionpkg.Info{ID: "sess-1", SessionName: "sess-1", WorkDir: repo}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newWorkBranchRepo(t, featureBranch)

			store := beads.NewMemStore()
			created, err := store.Create(beads.Bead{Title: "work", Type: "task"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			inProgress, holder := "in_progress", "sess-1"
			// The stale handle the claim wrote before the agent cut its branch.
			if err := store.Update(created.ID, beads.UpdateOpts{
				Status:   &inProgress,
				Assignee: &holder,
				Metadata: map[string]string{beadmeta.WorkBranchMetadataKey: "main"},
			}); err != nil {
				t.Fatalf("claim: %v", err)
			}

			res := unclaimWorkAssignedToRetiredSessionInfo(store, nil, tc.info(repo), "", workrelease.SeatRetired, io.Discard)
			if res.Released == 0 {
				t.Fatalf("nothing released: %#v", res)
			}

			got, err := store.Get(created.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Status != "open" || got.Assignee != "" {
				t.Fatalf("status=%q assignee=%q, want the bead released", got.Status, got.Assignee)
			}
			if branch := got.Metadata[beadmeta.WorkBranchMetadataKey]; branch != featureBranch {
				t.Fatalf("gc.work_branch = %q, want %q -- a released bead must name the branch its work is actually on", branch, featureBranch)
			}
		})
	}
}

// TestWorkReleaseBranchRetirementLeavesStampAloneWhenWorktreeIsGone pins the
// degradation path. A pruned or never-created worktree resolves no branch, and
// the release must then leave the existing handle untouched rather than
// clearing it -- a stale branch name is a worse pointer than none, but an
// ERASED one is worse than either, since it destroys the only record of where
// the predecessor was working.
func TestWorkReleaseBranchRetirementLeavesStampAloneWhenWorktreeIsGone(t *testing.T) {
	store := beads.NewMemStore()
	created, err := store.Create(beads.Bead{Title: "work", Type: "task"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	inProgress, holder := "in_progress", "sess-1"
	if err := store.Update(created.ID, beads.UpdateOpts{
		Status:   &inProgress,
		Assignee: &holder,
		Metadata: map[string]string{beadmeta.WorkBranchMetadataKey: "main"},
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A directory that is not a repo at all: hookResolveWorkBranch returns "".
	retired := sessionpkg.Info{ID: "sess-1", SessionName: "sess-1", WorkDir: t.TempDir()}
	if res := unclaimWorkAssignedToRetiredSessionInfo(store, nil, retired, "", workrelease.SeatRetired, io.Discard); res.Released == 0 {
		t.Fatalf("nothing released: %#v", res)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if branch := got.Metadata[beadmeta.WorkBranchMetadataKey]; branch != "main" {
		t.Fatalf("gc.work_branch = %q, want the prior handle %q left intact when no branch resolves", branch, "main")
	}
}

// TestWorkReleaseBranchFacadeStampsOnlyWhenItDiffers pins the release
// facade's own contract without git: a branch equal to the recorded one adds
// no key (so the emitted write stays byte-identical to the pre-change op),
// and an empty branch adds no key either.
func TestWorkReleaseBranchFacadeStampsOnlyWhenItDiffers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		recorded string
		resolved string
		want     string // "" means the key must be absent from the emitted patch
	}{
		{name: "differs", recorded: "main", resolved: "feat/x", want: "feat/x"},
		{name: "same", recorded: "feat/x", resolved: "feat/x", want: ""},
		{name: "unresolved", recorded: "main", resolved: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := newRecordingWriteWorkStore()
			wa := workAssignmentForStore(beads.WorkStore{Store: rec})

			item := heldBeadInRecordingStore(t, rec, "agent-1",
				map[string]string{beadmeta.WorkBranchMetadataKey: tc.recorded})

			if err := wa.ReleaseWorkBead(item, "", tc.resolved); err != nil {
				t.Fatalf("ReleaseWorkBead: %v", err)
			}
			if len(rec.updates) != 1 {
				t.Fatalf("expected 1 metadata Update, got %d: %#v", len(rec.updates), rec.updates)
			}
			got, present := rec.updates[0].opts.Metadata[beadmeta.WorkBranchMetadataKey]
			if tc.want == "" {
				if present {
					t.Fatalf("gc.work_branch = %q, want the key ABSENT so the emitted write is unchanged", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("gc.work_branch = %q (present=%v), want %q", got, present, tc.want)
			}
		})
	}
}
