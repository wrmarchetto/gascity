package main

// Scope: hookSiblingBranchesInRepo -- the git half of the claim-time sibling
// signal, driven over REAL repositories.
//
// Why real git: the acceptance condition on this signal is that a claimant can
// tell a LIVE sibling from an abandoned one from the signal alone, and every
// fact that carries that distinction (the merge base, how far the branch is
// ahead of the claimant's HEAD, when it last moved, whether it already landed)
// is computed by shelling out. A fake returning canned strings agrees with
// itself whatever the repository holds; it cannot tell an unlanded branch from
// a merged one, which is the single filter this function exists to apply. The
// landed-branch case is the one a canned seam would silently pass -- it was
// the failure that made the signal necessary in the first place, since a
// pointer at already-merged work is a pointer at nothing.
//
// Expected merge bases are captured from the repository AT THE MOMENT the
// branch is cut, not recomputed with the same `git merge-base` call the
// implementation makes. A test that re-derived them would agree with a
// dropped or inverted base argument.
//
// Delegated elsewhere: the wire shape of the field and the no-refusal
// guarantee (cmd_hook_claim_sibling_signal_test.go), and how sibling beads are
// found from the claimed bead's labels (the store half, injected there).
//
// Run: go test ./cmd/gc/ -run HookClaimSiblingBranchesInRepo
import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// gitOutputInTest returns the trimmed stdout of a git command, failing the
// test on error. Separate from runGitInTest, which discards output: the
// branch-cut shas are the test's own independent record of where each branch
// started, so they have to be read back here rather than recomputed later.
func gitOutputInTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return strings.TrimSpace(string(out))
}

// commitInTest adds one commit touching a uniquely named file, so no two
// commits in a repo share a tree and every branch really diverges.
func commitInTest(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGitInTest(t, dir, "add", name)
	runGitInTest(t, dir, "commit", "-q", "-m", name)
}

// TestHookClaimSiblingBranchesInRepoNamesBaseAndStateOfUnlandedBranches is the
// end-to-end case. The repository holds four branches covering all four
// populations at once -- live sibling, abandoned sibling, already-landed
// sibling, unrelated branch -- because the filter is only meaningful as a
// partition: a suite that checked each population in its own repository would
// pass against an implementation that reported every branch it found.
func TestHookClaimSiblingBranchesInRepoNamesBaseAndStateOfUnlandedBranches(t *testing.T) {
	dir := newWorkBranchRepo(t, "main")
	// The frozen integration tip every sibling was cut from. Captured here, so
	// the expectation does not come from the same merge-base call under test.
	base := gitOutputInTest(t, dir, "rev-parse", "HEAD")

	// A live sibling: two commits, never merged.
	runGitInTest(t, dir, "switch", "-q", "-c", "fix/ci-live00-open-route-demand")
	commitInTest(t, dir, "live-a.txt")
	commitInTest(t, dir, "live-b.txt")
	liveTip := gitOutputInTest(t, dir, "rev-parse", "HEAD")

	// An abandoned sibling: its bead is closed, its branch never landed. It
	// must still be REPORTED -- suppressing it would leave the claimant unable
	// to tell "no sibling" from "a dead one" -- with its state carrying the
	// difference.
	runGitInTest(t, dir, "switch", "-q", "-c", "fix/ci-dead00-open-route-demand", base)
	commitInTest(t, dir, "dead-a.txt")

	// A sibling whose fix already landed. It shares the label and the branch
	// naming convention and must NOT be reported: pointing a claimant at
	// merged work is worse than silence.
	runGitInTest(t, dir, "switch", "-q", "-c", "fix/ci-land00-open-route-demand", base)
	commitInTest(t, dir, "land-a.txt")

	// A branch naming a bead outside the sibling set.
	runGitInTest(t, dir, "switch", "-q", "-c", "fix/zz-other0-unrelated", base)
	commitInTest(t, dir, "other-a.txt")

	runGitInTest(t, dir, "switch", "-q", "main")
	runGitInTest(t, dir, "merge", "-q", "--no-ff", "-m", "land the fix", "fix/ci-land00-open-route-demand")

	// The live branch is related by a shared label, the abandoned one by being
	// the claimed bead itself -- both relations in one repository, so `via`
	// cannot be satisfied by a constant.
	siblings := []hookSiblingBead{
		{Bead: beads.Bead{ID: "ci-live00", Status: "in_progress"}, Via: "marker:queue-stalled"},
		{Bead: beads.Bead{ID: "ci-dead00", Status: "closed"}, Via: hookSiblingViaSelf},
		{Bead: beads.Bead{ID: "ci-land00", Status: "closed"}, Via: "marker:queue-stalled"},
	}
	got, err := hookSiblingBranchesInRepo(context.Background(), dir, siblings)
	if err != nil {
		t.Fatalf("hookSiblingBranchesInRepo: %v", err)
	}
	byBranch := map[string]hookClaimSiblingBranch{}
	for _, b := range got {
		byBranch[b.Branch] = b
	}
	if len(got) != 2 {
		t.Fatalf("reported %d branches (%v), want exactly the live and abandoned siblings", len(got), byBranch)
	}
	if _, present := byBranch["fix/ci-land00-open-route-demand"]; present {
		t.Error("the landed sibling was reported: an already-merged branch is a pointer at nothing")
	}
	if _, present := byBranch["fix/zz-other0-unrelated"]; present {
		t.Error("a branch naming a bead outside the sibling set was reported")
	}
	live, present := byBranch["fix/ci-live00-open-route-demand"]
	if !present {
		t.Fatalf("the live sibling is missing from %v", byBranch)
	}
	if live.BeadID != "ci-live00" || live.BeadStatus != "in_progress" {
		t.Errorf("live sibling identity = %+v, want bead ci-live00 in_progress", live)
	}
	// The relation is reported, so a claimant can dismiss a branch related only
	// by a state label without reading its commits.
	if live.Via != "marker:queue-stalled" {
		t.Errorf("live sibling via = %q, want the shared label", live.Via)
	}
	// Base and state, the two facts the acceptance condition names. `ahead` is
	// asserted at its exact value rather than merely nonzero: a count taken in
	// the wrong direction (HEAD ahead of the branch) is also nonzero, and on
	// this repository it is 1 -- the merge commit -- so a `> 0` assertion would
	// pass against an inverted range.
	if live.Base != base[:hookClaimSiblingShaLen] {
		t.Errorf("live sibling base = %q, want %q (the tip every sibling was cut from)", live.Base, base[:hookClaimSiblingShaLen])
	}
	if live.Tip != liveTip[:hookClaimSiblingShaLen] {
		t.Errorf("live sibling tip = %q, want %q", live.Tip, liveTip[:hookClaimSiblingShaLen])
	}
	if live.Ahead != 2 {
		t.Errorf("live sibling ahead = %d, want 2 (its two unlanded commits)", live.Ahead)
	}
	if live.LastCommit == "" {
		t.Error("live sibling last_commit is empty: staleness is how an abandoned branch is recognized")
	}
	dead, present := byBranch["fix/ci-dead00-open-route-demand"]
	if !present {
		t.Fatalf("the abandoned sibling is missing from %v", byBranch)
	}
	if dead.BeadStatus != "closed" || dead.Ahead != 1 {
		t.Errorf("abandoned sibling = %+v, want status closed and 1 commit ahead", dead)
	}
	if dead.Via != hookSiblingViaSelf {
		t.Errorf("abandoned sibling via = %q, want %q", dead.Via, hookSiblingViaSelf)
	}

	// Determinism. This cannot fail against the current implementation, which
	// walks a ref listing git already sorted; it is here for the refactor that
	// would break it -- iterating the sibling beads as a map and matching
	// branches inside, whose Go map order varies per run. Recorded as such
	// rather than dressed up as a live check.
	again, err := hookSiblingBranchesInRepo(context.Background(), dir, siblings)
	if err != nil {
		t.Fatalf("second hookSiblingBranchesInRepo: %v", err)
	}
	if len(again) != len(got) {
		t.Fatalf("second call reported %d branches, first reported %d", len(again), len(got))
	}
	for i := range got {
		if again[i] != got[i] {
			t.Errorf("entry %d differs between calls: %+v vs %+v", i, again[i], got[i])
		}
	}
}

// TestHookClaimSiblingBranchesInRepoFabricatesNothingWithoutMatches pins the
// empty half against a repository that has branches, just none belonging to a
// sibling. This is the case a signaling-only fix leaves untested, and getting
// it wrong is the more damaging direction: a claimant that trusts a fabricated
// pointer builds on work that does not exist.
func TestHookClaimSiblingBranchesInRepoFabricatesNothingWithoutMatches(t *testing.T) {
	dir := newWorkBranchRepo(t, "main")
	head := gitOutputInTest(t, dir, "rev-parse", "HEAD")
	runGitInTest(t, dir, "switch", "-q", "-c", "fix/zz-other0-unrelated", head)
	commitInTest(t, dir, "other-a.txt")
	runGitInTest(t, dir, "switch", "-q", "main")

	got, err := hookSiblingBranchesInRepo(context.Background(), dir,
		[]hookSiblingBead{{Bead: beads.Bead{ID: "ci-live00", Status: "in_progress"}, Via: "marker:queue-stalled"}})
	if err != nil {
		t.Fatalf("hookSiblingBranchesInRepo: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("reported %+v with no sibling branch on disk, want nothing", got)
	}
}

// TestHookClaimSiblingBranchesInRepoReportsAnUnreadableRepo pins that a
// directory git cannot list refs in surfaces as an error rather than as a
// confident empty result. The caller turns it into a stderr line and leaves
// the field absent; swallowing it here would make a broken scan
// indistinguishable from a clean city, which is how a signal that never fires
// goes unnoticed.
func TestHookClaimSiblingBranchesInRepoReportsAnUnreadableRepo(t *testing.T) {
	got, err := hookSiblingBranchesInRepo(context.Background(), t.TempDir(),
		[]hookSiblingBead{{Bead: beads.Bead{ID: "ci-live00", Status: "in_progress"}, Via: "marker:queue-stalled"}})
	if err == nil {
		t.Fatalf("hookSiblingBranchesInRepo over a non-repository returned %+v and no error", got)
	}
	if len(got) != 0 {
		t.Errorf("returned %+v alongside the error, want no entries", got)
	}
}

// TestHookClaimSiblingBranchesInRepoCapsTheReport pins the bound on the
// signal. The measured incident had six sibling branches; a marker that fires
// for weeks can accumulate far more, and an unbounded list both costs a git
// round trip per branch on the claim path and buries the newest sibling in a
// wall of dead ones.
func TestHookClaimSiblingBranchesInRepoCapsTheReport(t *testing.T) {
	dir := newWorkBranchRepo(t, "main")
	base := gitOutputInTest(t, dir, "rev-parse", "HEAD")
	siblings := make([]hookSiblingBead, 0, hookClaimSiblingBranchLimit+2)
	for i := 0; i < hookClaimSiblingBranchLimit+2; i++ {
		id := "ci-cap" + string(rune('a'+i))
		runGitInTest(t, dir, "switch", "-q", "-c", "fix/"+id+"-open-route-demand", base)
		commitInTest(t, dir, id+".txt")
		siblings = append(siblings, hookSiblingBead{Bead: beads.Bead{ID: id, Status: "open"}, Via: "marker:queue-stalled"})
	}
	runGitInTest(t, dir, "switch", "-q", "main")

	got, err := hookSiblingBranchesInRepo(context.Background(), dir, siblings)
	if err != nil {
		t.Fatalf("hookSiblingBranchesInRepo: %v", err)
	}
	if len(got) != hookClaimSiblingBranchLimit {
		t.Fatalf("reported %d branches, want the cap %d", len(got), hookClaimSiblingBranchLimit)
	}
}
