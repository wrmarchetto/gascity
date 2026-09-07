// Contract tests for .githooks/pre-rebase, the gate that refuses a rebase
// which would silently flatten a merge commit.
//
// This suite exists because the fault it guards is INVISIBLE from the outside:
// `git pull --rebase` over a merge drops that merge and exits 0, so no exit
// code, conflict, or warning distinguishes the damage from success. A test
// that only grepped the hook for a keyword would pass against a hook git never
// invokes, so every case here drives a real repository through real git and
// asserts on the resulting commit graph.
//
// Scope is the hook's refusal and its converse. The session-completion
// procedure the hook backs up is prose in AGENTS.md and is not tested here.
//
// Run: go test ./scripts -run TestPreRebase
package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every process here goes through testCommand, the package's existing
// exec wrapper, and this file deliberately does not import os/exec.
// test/test-resources.toml ratchets untagged subprocess call sites and files
// and forbids growth, so a literal spawn in a new file is new resource debt --
// measured, when this suite first used one: calls 409 -> 412 and files
// 118 -> 119, which the push gate rejected.
//
// gitIn runs one command in dir and fails the test on a nonzero exit, except
// where the caller is asking for the status.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitTry(t, dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return out
}

func gitTry(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := testCommand("git", args...)
	cmd.Dir = dir
	// An operator's own git identity, editor, and hook path must not decide
	// the result. GC_ALLOW_MERGE_REBASE is cleared for the same reason: an
	// exported bypass in the developer's shell would turn the refusal cases
	// into silent passes, which is precisely the failure this suite exists
	// to make impossible.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GC_ALLOW_MERGE_REBASE=",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// mergeFixture builds a repository whose HEAD is a merge commit and whose
// upstream ref is an ancestor of it -- the exact shape a finished session has
// after merging origin/main. Returns the worktree path and the upstream ref
// name.
//
// A real commit graph rather than a synthesized one: the whole question is
// what git itself does to these commits, so a fixture that faked the graph
// would be testing the fixture.
func mergeFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")

	gitIn(t, root, "init", "-q", "-b", "main", work)
	writeTestFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitIn(t, work, "add", "base.txt")
	gitIn(t, work, "commit", "-qm", "base")

	// An "upstream" that advances independently, then a local commit, then the
	// merge -- so upstream..HEAD contains a merge and upstream is an ancestor.
	gitIn(t, work, "branch", "upstream")
	gitIn(t, work, "checkout", "-q", "upstream")
	writeTestFile(t, filepath.Join(work, "upstream.txt"), "upstream\n")
	gitIn(t, work, "add", "upstream.txt")
	gitIn(t, work, "commit", "-qm", "upstream work")

	gitIn(t, work, "checkout", "-q", "main")
	writeTestFile(t, filepath.Join(work, "local.txt"), "local\n")
	gitIn(t, work, "add", "local.txt")
	gitIn(t, work, "commit", "-qm", "local work")
	gitIn(t, work, "merge", "-q", "--no-ff", "upstream", "-m", "merge: bring in upstream")

	return work, "upstream"
}

// installPreRebase points the fixture at the repository's real hook, so the
// test cannot pass against a copy that has drifted from what ships.
func installPreRebase(t *testing.T, work string) {
	t.Helper()
	source := filepath.Join(repoRoot(t), ".githooks", "pre-rebase")
	body, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read shipped pre-rebase hook: %v", err)
	}
	writeExecutable(t, filepath.Join(work, ".git", "hooks", "pre-rebase"), string(body))
}

func parentCount(t *testing.T, work string) int {
	t.Helper()
	out := gitIn(t, work, "rev-list", "--parents", "-n", "1", "HEAD")
	return len(strings.Fields(strings.TrimSpace(out))) - 1
}

// TestPreRebaseHookRefusesToFlattenMerges pins the refusal AND the survival of
// the merge. Asserting only the nonzero exit would be satisfied by a hook that
// refused after git had already rewritten the branch, and asserting only the
// parent count would be satisfied by a rebase that failed for an unrelated
// reason -- so both, plus the absence of a half-finished rebase state, are
// required together.
func TestPreRebaseHookRefusesToFlattenMerges(t *testing.T) {
	work, upstream := mergeFixture(t)
	installPreRebase(t, work)

	if got := parentCount(t, work); got != 2 {
		t.Fatalf("fixture must start at a merge commit, got %d parent(s)", got)
	}

	out, err := gitTry(t, work, "rebase", upstream)
	if err == nil {
		t.Fatalf("rebase over a merge must be refused, but it succeeded:\n%s", out)
	}
	if !strings.Contains(out, "pre-rebase: refusing") {
		t.Errorf("refusal must come from the hook and name itself:\n%s", out)
	}
	if !strings.Contains(out, "--ff-only") {
		t.Errorf("refusal must name the remedy, not merely the fault:\n%s", out)
	}
	if got := parentCount(t, work); got != 2 {
		t.Errorf("the merge must survive the refusal, got %d parent(s)", got)
	}
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(work, ".git", dir)); err == nil {
			t.Errorf("refusal must leave no half-finished rebase state (%s exists)", dir)
		}
	}
}

// TestPreRebaseHookAllowsAnOrdinaryRebase is the converse, and it is the case
// that makes the refusal above meaningful. A hook that refused unconditionally
// would satisfy the test above and break every legitimate rebase, so this one
// replays REAL work: a fixture whose range is empty would report a pass the
// hook never earned, which is why the local commit and the divergence are both
// asserted before the rebase runs.
func TestPreRebaseHookAllowsAnOrdinaryRebase(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	gitIn(t, root, "init", "-q", "-b", "main", work)
	writeTestFile(t, filepath.Join(work, "base.txt"), "base\n")
	gitIn(t, work, "add", "base.txt")
	gitIn(t, work, "commit", "-qm", "base")

	gitIn(t, work, "branch", "upstream")
	gitIn(t, work, "checkout", "-q", "upstream")
	writeTestFile(t, filepath.Join(work, "upstream.txt"), "upstream\n")
	gitIn(t, work, "add", "upstream.txt")
	gitIn(t, work, "commit", "-qm", "upstream work")

	gitIn(t, work, "checkout", "-q", "main")
	writeTestFile(t, filepath.Join(work, "local.txt"), "local\n")
	gitIn(t, work, "add", "local.txt")
	gitIn(t, work, "commit", "-qm", "ordinary local commit")

	installPreRebase(t, work)

	if got := strings.TrimSpace(gitIn(t, work, "rev-list", "--count", "upstream..HEAD")); got != "1" {
		t.Fatalf("fixture must have exactly one commit to replay, got %q", got)
	}
	if got := strings.TrimSpace(gitIn(t, work, "rev-list", "--merges", "--count", "upstream..HEAD")); got != "0" {
		t.Fatalf("fixture must contain no merges, got %q", got)
	}
	if got := strings.TrimSpace(gitIn(t, work, "rev-list", "--count", "HEAD..upstream")); got == "0" {
		t.Fatal("fixture must be behind upstream, or the rebase replays nothing and proves nothing")
	}

	if out, err := gitTry(t, work, "rebase", "upstream"); err != nil {
		t.Fatalf("an ordinary rebase carrying no merge must be allowed:\n%s", out)
	}
	if got := strings.TrimSpace(gitIn(t, work, "log", "-1", "--format=%s")); got != "ordinary local commit" {
		t.Errorf("the replayed commit must survive, got %q", got)
	}
}

// TestPreRebaseHookRefusesAnUnresolvableRange pins the fail-closed arm. git
// itself always hands the hook a range it has already resolved, so this branch
// is unreachable through `git rebase` -- the hook is therefore invoked
// DIRECTLY, at its documented interface of one upstream argument, rather than
// by weakening any caller to manufacture the case.
//
// It earns a test because the alternative reading is silent: a rev-list that
// errors prints nothing on stdout, so a hook treating failure as "no merges
// found" would accept every rebase it could not analyze and look identical to
// one that had checked. Mutation-checked -- flipping this arm to exit 0 is a
// mutant no other case in this file kills.
func TestPreRebaseHookRefusesAnUnresolvableRange(t *testing.T) {
	work, _ := mergeFixture(t)
	hook := filepath.Join(repoRoot(t), ".githooks", "pre-rebase")

	cmd := testCommand(hook, "no-such-ref-cf3a91")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GC_ALLOW_MERGE_REBASE=")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("an unresolvable range must be refused, not read as zero merges:\n%s", out)
	}
	if !strings.Contains(string(out), "cannot resolve") {
		t.Errorf("refusal must say the range could not be resolved, not report a merge count:\n%s", out)
	}
}

// TestPreRebaseHookHonorsTheNamedBypass pins the escape hatch, because a gate
// with no sanctioned override gets deleted rather than overridden -- and a
// deleted hook protects nothing. `git rebase` has no --no-verify, so this
// variable is the only route that is not removal.
func TestPreRebaseHookHonorsTheNamedBypass(t *testing.T) {
	work, upstream := mergeFixture(t)
	installPreRebase(t, work)

	cmd := testCommand("git", "rebase", "--rebase-merges", upstream)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GC_ALLOW_MERGE_REBASE=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the named bypass must let a deliberate merge-preserving rebase run:\n%s", out)
	}
	if strings.Contains(string(out), "pre-rebase: refusing") {
		t.Errorf("the bypass must suppress the refusal:\n%s", out)
	}
}
