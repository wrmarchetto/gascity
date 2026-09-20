package main

// cmd/gc/work_branch_close_stamp.go -- the close-time gc.work_branch refresh.
//
// The field records where a bead's work lives while it is still unlanded, and
// it had exactly one machine writer: the claim-time stamp
// (hookClaimIdentityPatch). A claim happens BEFORE the agent cuts its feature
// branch, and a seat reuses one worktree across beads, so that single sample
// names whatever the PREVIOUS bead left checked out. ci-hdnj73 corrected WHICH
// tree is read; WHEN it is read stayed wrong, and the two in-tree comments that
// dismissed ordering as a non-defect were written while the directory bug was
// still live.
//
// Measured on seven consecutive bench-engineer beads (ci-cocmug, 2026-09-20):
// every machine stamp named the prior bead's branch, and the rows that read
// correctly had been repaired by hand, two of them after the close.
//
// Editing constraint: this writes ONLY when the closing process can honestly
// speak for the bead's worktree -- it must be the session that holds the bead.
// The refusals are not defensive padding; each is a case where a write would
// reinstate the defect from the other side, and each is pinned in
// work_branch_close_stamp_realgit_test.go.

import (
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// workBranchCloseStampOps carries the close-time refresh's two environment
// dependencies: the git read, injected so the rule can be driven over a real
// worktree without a bd binary, and the closing process's own session id.
type workBranchCloseStampOps struct {
	ResolveBranch func(dir string) string // nil disables the refresh
	SessionID     string                  // GC_SESSION_ID; "" disables the refresh
}

// stampWorkBranchOnClose refreshes gc.work_branch on each bead a close
// invocation closes, from that bead's own gc.work_dir HEAD, so the recorded
// branch is the one the work was done on rather than the one the seat happened
// to be on when the bead was claimed.
//
// It runs before the close is forwarded to bd and updates preFetched in place
// as well as the store, because the gates that run after it are handed those
// same beads and would otherwise judge a close against the value this call
// just corrected.
//
// Best-effort throughout: a read or write failure warns and moves on. A close
// must never fail because gc could not improve a record.
//
// Four refusals, and why each has to be a refusal rather than a fallback:
//
//   - A closer that is not the bead's holder. The worktree named by
//     gc.work_dir belongs to the HOLDER, and by the time someone else closes
//     the bead -- the mayor sweeping a stranded one, an operator at a prompt
//     -- that tree has moved on to other work. Reading it then produces
//     exactly the confident-wrong-branch this fixes. The empty-sessionID test
//     at the top of the loop is an early-out for a non-session caller, not a
//     fifth refusal: "" never equals a holder, so the per-bead check already
//     covers it.
//   - A bead outside the work-record scope (isWorkRecordGatedBead). Control
//     steps and message beads are closed by the dispatch engine and have no
//     worktree of their own; the claim path excludes them from session
//     identity for the same reason.
//   - No gc.work_dir. There is deliberately NO fallback to the scope root:
//     that is the shared checkout, and resolving a branch there is the
//     ci-hdnj73 defect verbatim.
//   - An unresolvable branch -- pruned worktree, detached HEAD, non-repo path.
//     The existing handle is LEFT, never cleared: a stale name is a bad
//     pointer, but erasing it destroys the only record of where a predecessor
//     was working (withReleasedWorkBranch records the same call).
//
// The claim-time stamp is not replaced by this. It is what the release path
// hands a successor when a session dies mid-work, long before any close.
func stampWorkBranchOnClose(bdArgs []string, store beads.Store, preFetched map[string]beads.Bead, ops workBranchCloseStampOps, stderr io.Writer) {
	sessionID := strings.TrimSpace(ops.SessionID)
	if sessionID == "" || ops.ResolveBranch == nil || store == nil {
		return
	}
	ids, ok := workRecordCloseTargets(bdArgs)
	if !ok {
		return
	}
	for _, id := range ids {
		bead, cached := preFetched[id]
		if !cached {
			var getErr error
			if bead, getErr = store.Get(id); getErr != nil {
				continue
			}
		}
		branch, refresh := workBranchCloseStampFor(bead, sessionID, ops.ResolveBranch)
		if !refresh {
			continue
		}
		if err := store.Update(id, beads.UpdateOpts{
			Metadata: map[string]string{beadmeta.WorkBranchMetadataKey: branch},
		}); err != nil {
			fmt.Fprintf(stderr, "gc bd: refreshing %s on %s from %s: %v\n", //nolint:errcheck // best-effort stderr
				beadmeta.WorkBranchMetadataKey, id, bead.Metadata[beadmeta.WorkDirMetadataKey], err)
			continue
		}
		if cached {
			preFetched[id] = withWorkBranch(bead, branch)
		}
	}
}

// workBranchCloseStampFor returns the branch to record on bead and whether a
// write is warranted at all. Split out from the loop so the whole rule -- every
// refusal and the compare-and-skip -- reads in one place, and so the caller
// holds nothing but IO.
func workBranchCloseStampFor(bead beads.Bead, sessionID string, resolve func(string) string) (string, bool) {
	if !isWorkRecordGatedBead(bead) {
		return "", false
	}
	if strings.TrimSpace(bead.Metadata[beadmeta.SessionIDMetadataKey]) != sessionID {
		return "", false
	}
	dir := strings.TrimSpace(bead.Metadata[beadmeta.WorkDirMetadataKey])
	if dir == "" {
		return "", false
	}
	branch := strings.TrimSpace(resolve(dir))
	if branch == "" || branch == strings.TrimSpace(bead.Metadata[beadmeta.WorkBranchMetadataKey]) {
		return "", false
	}
	return branch, true
}

// withWorkBranch returns a copy of bead carrying branch, leaving the caller's
// metadata map untouched. The prefetched beads are read by the gates that run
// after the refresh and may alias the store's own map, so mutating in place
// would edit rows nobody asked to change.
func withWorkBranch(bead beads.Bead, branch string) beads.Bead {
	metadata := make(beads.StringMap, len(bead.Metadata)+1)
	for key, value := range bead.Metadata {
		metadata[key] = value
	}
	metadata[beadmeta.WorkBranchMetadataKey] = branch
	bead.Metadata = metadata
	return bead
}
