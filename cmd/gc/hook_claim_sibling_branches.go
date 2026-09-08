package main

// cmd/gc/hook_claim_sibling_branches.go
//
// The claim-time sibling-branch signal: when a session claims a bead, report
// the unlanded local branches that already name that bead or one related to it,
// so the claimant knows work exists before it starts over.
//
// Why this exists as a SIGNAL and not a gate. Seven sessions each claimed a
// bead filed under one marker label, each cut a branch from the same frozen
// integration tip, and none of them consulted the six sibling branches sitting
// in `git branch` -- roughly 120 minutes of duplicated work, one merge
// conflict, and one commit landed crediting the wrong bead (ci-fykz7j, from
// the measurements in ci-nn7r6m and ci-tsh4u1). The rejected alternative is
// the obvious one: refuse the claim when a sibling branch exists. It was
// rejected on measurement, not taste -- in the incident it would have refused
// all seven branches, including the single one that actually fixed the
// integration branch, leaving it red. A refusal cannot admit its own fix.
// Nothing here may therefore refuse, delay past its budget, or fail a claim;
// every failure path leaves the field absent and the claim reported.
//
// Why the report names a base and a state rather than just a branch. A bare
// "a sibling exists" trades not-knowing for knowing-the-wrong-thing: a
// claimant told only that a branch exists cannot tell live work from an
// abandoned attempt, and building on dead work is worse than starting fresh.
// Each entry therefore carries where the branch diverged (base), how far it
// went (ahead, tip, last_commit) and what happened to the bead it belongs to
// (bead_status).
//
// The relation between beads is a shared LABEL, plus the claimed bead's own
// id. Deliberately absent: any similarity heuristic over titles, paths or
// descriptions. A bead with no labels gets no sibling lookup at all -- only
// the scan for its own prior attempts -- because widening the relation to
// something inferred would put a judgment call in Go and would point
// claimants at unrelated branches, which is the one failure mode a
// non-refusing signal cannot recover from.
//
// WHICH REPOSITORY IS SCANNED, and the absence that follows from it. `dir` is
// the BEAD STORE's directory, threaded through from the federated claim loop
// in cmd_hook.go -- not the claiming agent's checkout. That is right for the
// measured incident (its seven branches were cut in the city repo, the same
// repo that holds the city bead store) and it is the same seam
// hookResolveWorkBranch already uses. It also means a bead in store X whose
// fix lands in a DIFFERENT repository Y gets no sibling reported: absence of
// the field says "no unlanded branch names this bead in the store's repo", not
// "no sibling exists anywhere". Widening it would mean guessing which
// repository an agent will edit, which is a judgment call and belongs in the
// prompt, not here.
//
// Cost, measured 2026-09-08 against a gascity worktree holding 255 local
// branches: 7ms to list the refs, and 60ms for the twenty git calls a full
// cap of ten reported branches pays. The store side is one bd subprocess per
// label on the claimed bead and none at all when it carries no labels; a
// city-scoped claim already spends 3-4s in bd, so the label query is a
// fraction of that rather than a new order of magnitude. NOT measured: the
// composed production path against a live bd store.
//
// Invariants, verified by cmd_hook_claim_sibling_signal_test.go (wire shape,
// omission, and the no-refusal guarantee including a panicking scan),
// cmd_hook_claim_sibling_branch_realgit_test.go (base and state over real
// repositories), cmd_hook_claim_sibling_relation_test.go (which beads count
// as related, and why each is reported) and
// hook_claim_sibling_prompt_contract_test.go (the shipped prompts name the
// field gc actually emits):
//
//   - a scan failure never changes the claim's exit code and never withholds
//     the claim record
//   - an already-landed branch is never reported
//   - the field is absent, not empty, when there is nothing to point at

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

const (
	// Length branch shas are abbreviated to in the report. Fixed rather than
	// taken from `git rev-parse --short`, which would cost a third subprocess
	// per branch and whose length varies with repository size -- an unstable
	// width makes two reports of the same branch differ textually.
	hookClaimSiblingShaLen = 12
	// Maximum branches reported. Bounds both the git round trips paid on the
	// claim path (two per matching branch) and the reading an agent has to do
	// before it starts work. A marker that has been firing for weeks can
	// accumulate far more branches than a claimant can usefully weigh.
	hookClaimSiblingBranchLimit = 10
	// Maximum sibling beads read per label. The label query is only a
	// CANDIDATE generator -- branch existence is the real filter -- so a broad
	// label costs one bounded store read and still reports nothing unless a
	// branch names one of the beads it returned.
	hookClaimSiblingBeadsPerLabel = 50
	// Reported as `via` for a branch naming the claimed bead itself -- a prior
	// session's own unlanded attempt, not a sibling's.
	hookSiblingViaSelf = "self"
)

// hookClaimSiblingScanTimeout bounds the whole scan. It is deliberately well
// under hookClaimMutationTimeout: the claim has already committed when the
// scan runs, so time spent here is time the owning session is not yet told
// what it holds.
var hookClaimSiblingScanTimeout = 5 * time.Second

// hookClaimSiblingBranch describes one unlanded local branch naming a bead
// related to the one just claimed, as seen from the claimant's own HEAD.
type hookClaimSiblingBranch struct {
	Branch     string `json:"branch"`
	BeadID     string `json:"bead_id"`
	BeadStatus string `json:"bead_status,omitempty"`
	Via        string `json:"via,omitempty"`         // "self", or the label that related this bead to the claim
	Base       string `json:"base,omitempty"`        // merge base with the claimant's HEAD, abbreviated
	Tip        string `json:"tip,omitempty"`         // branch head, abbreviated
	Ahead      int    `json:"ahead,omitempty"`       // commits the branch has that HEAD does not
	LastCommit string `json:"last_commit,omitempty"` // committer date of the tip, RFC3339
}

// hookSiblingBead pairs a bead related to the claimed one with WHY it is
// related. The reason is reported so a claimant can dismiss a coincidence in a
// glance: real cities label work beads with state and role labels
// (`hold:external`, `epic:timer-validation`) alongside condition markers, and
// two beads sharing a state label are not the same work.
//
// The rejected alternative is a denylist of label prefixes that do not imply
// relation. It would rot at the next label anyone invents, and it puts a
// judgment call in Go -- naming the relation moves the judgment to the reader,
// who has the context to make it.
type hookSiblingBead struct {
	Bead beads.Bead
	Via  string
}

// hookResolveSiblingBranchesFunc is the claim's sibling-scan seam.
type hookResolveSiblingBranchesFunc func(ctx context.Context, dir string, env []string, bead beads.Bead) ([]hookClaimSiblingBranch, error)

// hookResolveSiblingBranches is the production scan: find the beads related to
// the claimed one, then intersect them with the unlanded branches on disk.
func hookResolveSiblingBranches(ctx context.Context, dir string, env []string, bead beads.Bead) ([]hookClaimSiblingBranch, error) {
	related, err := hookSiblingBeads(ctx, dir, env, bead)
	if err != nil {
		return nil, err
	}
	if len(related) == 0 {
		return nil, nil
	}
	return hookSiblingBranchesInRepo(ctx, dir, related)
}

// hookSiblingLister returns the beads carrying one label. It is the seam at
// the store boundary: everything above it is relation logic worth testing,
// and the implementation below it is one call, so there is nothing in the
// unexercised half to get wrong.
type hookSiblingLister func(label string) ([]beads.Bead, error)

// hookSiblingBeads binds the relation walk to this claim's bead store.
//
// Closed beads are included on purpose: a closed bead holding an unlanded
// branch is exactly the abandoned-sibling case a claimant has to be able to
// recognize, and excluding it would leave "no sibling" and "a dead one"
// looking identical.
func hookSiblingBeads(ctx context.Context, dir string, env []string, bead beads.Bead) ([]hookSiblingBead, error) {
	store := hookClaimBdStoreContext(ctx, dir, env, "")
	return hookSiblingBeadsFrom(bead, func(label string) ([]beads.Bead, error) {
		return store.ListByLabel(label, hookClaimSiblingBeadsPerLabel, beads.IncludeClosed)
	})
}

// hookSiblingBeadsFrom returns the claimed bead plus every bead sharing one of
// its labels, deduplicated by id and each carrying the relation that found it.
//
// The claimed bead is included because a branch naming it is the strongest
// signal of all -- a prior session's unlanded attempt at the very same work.
// The adoption triage in an agent prompt only sees such a branch when the
// worktree happens to still be sitting on it, so a branch cut for this bead in
// a different pool slot is otherwise invisible.
//
// A bead with NO labels queries the store zero times. That absence is
// load-bearing: the scan runs on the claim path, after the mutation has
// committed and before the owning session is told what it holds, and most
// beads carry no labels at all.
//
// A lister failure aborts rather than returning what it has. A partial
// candidate set produces a signal that says "no sibling branch" when one
// exists, which is the state this whole mechanism was built to end.
func hookSiblingBeadsFrom(bead beads.Bead, list hookSiblingLister) ([]hookSiblingBead, error) {
	related := make([]hookSiblingBead, 0, 1+len(bead.Labels))
	seen := make(map[string]bool, 1+len(bead.Labels))
	if id := strings.TrimSpace(bead.ID); id != "" {
		related = append(related, hookSiblingBead{Bead: bead, Via: hookSiblingViaSelf})
		seen[id] = true
	}
	for _, label := range bead.Labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		found, err := list(label)
		if err != nil {
			return nil, fmt.Errorf("listing beads labeled %q: %w", label, err)
		}
		for _, candidate := range found {
			id := strings.TrimSpace(candidate.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			related = append(related, hookSiblingBead{Bead: candidate, Via: label})
		}
	}
	return related, nil
}

// hookSiblingBranchesInRepo reports the local branches of dir whose name
// embeds one of the given bead ids and whose commits are NOT yet reachable
// from dir's HEAD.
//
// Reachability from HEAD is the whole filter, and it does double duty: it
// drops branches whose fix already landed, and it drops the claimant's own
// checked-out branch without needing to ask which branch that is.
func hookSiblingBranchesInRepo(ctx context.Context, dir string, siblings []hookSiblingBead) ([]hookClaimSiblingBranch, error) {
	if strings.TrimSpace(dir) == "" || len(siblings) == 0 {
		return nil, nil
	}
	// Sorted so that a branch name embedding two sibling ids is always
	// attributed to the same one. Iterating the caller's order would make the
	// report depend on store return order.
	ordered := make([]hookSiblingBead, len(siblings))
	copy(ordered, siblings)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Bead.ID < ordered[j].Bead.ID })

	listing, err := hookSiblingGitOutput(ctx, dir, "for-each-ref", "--sort=refname",
		"--format=%(refname:short)%09%(objectname)%09%(committerdate:iso-strict)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("listing local branches in %s: %w", dir, err)
	}
	var reports []hookClaimSiblingBranch
	for _, line := range strings.Split(listing, "\n") {
		if len(reports) >= hookClaimSiblingBranchLimit {
			break
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		branch, tip, committed := fields[0], fields[1], fields[2]
		sibling, matched := hookSiblingBeadNamedBy(branch, ordered)
		if !matched {
			continue
		}
		ahead, err := hookSiblingGitOutput(ctx, dir, "rev-list", "--count", "HEAD.."+tip)
		if err != nil {
			return nil, fmt.Errorf("counting commits on %s in %s: %w", branch, dir, err)
		}
		count, err := strconv.Atoi(strings.TrimSpace(ahead))
		if err != nil {
			return nil, fmt.Errorf("parsing commit count %q for %s: %w", ahead, branch, err)
		}
		if count == 0 {
			continue
		}
		report := hookClaimSiblingBranch{
			Branch:     branch,
			BeadID:     sibling.Bead.ID,
			BeadStatus: strings.TrimSpace(sibling.Bead.Status),
			Via:        sibling.Via,
			Tip:        hookSiblingAbbrev(tip),
			Ahead:      count,
			LastCommit: committed,
		}
		// A missing merge base is a fact about the branch, not a scan
		// failure: histories that share no commit have no base to name, and
		// the empty field says exactly that. Reporting the branch anyway keeps
		// the claimant informed that it exists.
		if base, err := hookSiblingGitOutput(ctx, dir, "merge-base", tip, "HEAD"); err == nil {
			report.Base = hookSiblingAbbrev(strings.TrimSpace(base))
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// hookSiblingBeadNamedBy returns the first bead in ordered whose id appears in
// the branch name. Substring rather than a `<prefix>/<id>-<slug>` pattern: the
// branch naming convention belongs to whatever pack configures the agents, and
// a pattern hardcoded here would silently stop signaling the day a pack spells
// its branches differently.
func hookSiblingBeadNamedBy(branch string, ordered []hookSiblingBead) (hookSiblingBead, bool) {
	for _, sibling := range ordered {
		id := strings.TrimSpace(sibling.Bead.ID)
		if id != "" && strings.Contains(branch, id) {
			return sibling, true
		}
	}
	return hookSiblingBead{}, false
}

// hookSiblingAbbrev shortens a full object name for display.
func hookSiblingAbbrev(sha string) string {
	if len(sha) <= hookClaimSiblingShaLen {
		return sha
	}
	return sha[:hookClaimSiblingShaLen]
}

func hookSiblingGitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// reportHookClaimSiblingBranches runs the scan for one claimed bead and
// returns what to publish in the claim record. It is the whole no-refusal
// boundary: every failure -- an error, a panic, or a scan that outran its
// budget -- yields no entries and a stderr line, never a nonzero code and
// never a withheld claim record.
//
// The recover is not defensive habit. This runs after the claim mutation has
// committed and before the record reaches stdout, so a panic here would exit
// the process with the bead assigned and in_progress and the one session that
// owns it never told which bead it holds -- the ci-gyj39 shape, reintroduced
// by a signal whose defining requirement is that it cannot refuse a claim.
func reportHookClaimSiblingBranches(bead beads.Bead, opts hookClaimOptions, ops hookClaimOps, dir string, stderr io.Writer) (reports []hookClaimSiblingBranch) {
	defer func() {
		if recovered := recover(); recovered != nil {
			reports = nil
			fmt.Fprintf(stderr, "gc hook --claim: scanning sibling branches for %s panicked: %v\n", bead.ID, recovered) //nolint:errcheck
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), hookClaimSiblingScanTimeout)
	defer cancel()
	found, err := ops.ResolveSiblingBranches(ctx, dir, opts.Env, bead)
	if err != nil {
		fmt.Fprintf(stderr, "gc hook --claim: scanning sibling branches for %s: %v\n", bead.ID, err) //nolint:errcheck
		return nil
	}
	return found
}
