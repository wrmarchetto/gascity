package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// upstreamProbeMetadataKey records the command that proves a fork patch is
// still needed when its production changes are removed and its tests remain.
// The city-level upstream audit consumes the same key after the branch lands.
const upstreamProbeMetadataKey = beadmeta.UpstreamProbeMetadataKey

const upstreamProbeUpstreamRef = "upstream/main"

// upstreamProbeBaseRef is the fork tip the worker branched from. Ownership is
// compared with upstream, but only the work branch's own delta may be reversed;
// reversing every historic fork patch would test a tree nobody authored.
const upstreamProbeBaseRef = "main"

// runUpstreamProbeCloseGate moves the fork-retirement experiment to the close
// boundary, while the worker that ran the verification can still correct its
// command. It only applies when the current checkout has upstream/main and
// the work branch changes a path owned by that ref. A branch made solely of
// fork-owned files is intentionally outside the requirement.
func runUpstreamProbeCloseGate(bdArgs []string, store beads.Store, preFetched map[string]beads.Bead, stderr io.Writer) bool {
	repoDir, err := os.Getwd()
	if err != nil {
		return false
	}
	return evaluateUpstreamProbeCloseGate(bdArgs, store, preFetched, repoDir, stderr)
}

// evaluateUpstreamProbeCloseGate is the store-driven close-time gate. Keeping
// the current checkout explicit makes the experiment testable with a real git
// fixture and prevents claim-time branch metadata from selecting a branch a
// worker switched away from before closing its bead.
func evaluateUpstreamProbeCloseGate(bdArgs []string, store beads.Store, preFetched map[string]beads.Bead, repoDir string, stderr io.Writer) (block bool) {
	ids, ok := workRecordCloseTargets(bdArgs)
	if !ok || store == nil {
		return false
	}
	for _, id := range ids {
		bead, cached := preFetched[id]
		if !cached {
			var err error
			bead, err = store.Get(id)
			if err != nil {
				continue
			}
		}
		if !isUpstreamProbeGatedBead(bead) {
			continue
		}
		projected, err := applyWorkRecordUpdateMetadata(bead, bdArgs)
		if err != nil {
			continue
		}
		if violation := upstreamProbeCloseViolation(repoDir, projected.Metadata[upstreamProbeMetadataKey]); violation != "" {
			fmt.Fprintf(stderr, "gc bd: upstream-probe close gate: close of %s: %s\n", id, violation) //nolint:errcheck // best-effort stderr
			block = true
		}
	}
	return block
}

// isUpstreamProbeGatedBead identifies ordinary work records whose source
// changes may need a fork-retirement probe. Unlike the typed work-record gate,
// this gate applies to every user work type: a bug or feature can change
// upstream-owned code just as a task can. Control records are closed by the
// dispatcher rather than by the worker that performed the code change.
func isUpstreamProbeGatedBead(bead beads.Bead) bool {
	if strings.TrimSpace(bead.Metadata[beadmeta.KindMetadataKey]) != "" {
		return false
	}
	switch strings.TrimSpace(bead.Type) {
	case "convoy", "message", "molecule":
		return false
	default:
		return true
	}
}

// upstreamProbeCloseViolation returns an empty string when no upstream-probe
// contract applies, when probe distinguishes the branch from its production
// changes, or when a branch with no behavioral production change (only tests,
// comments or unembedded docs) has a probe that runs a test green. A
// non-empty result is a close refusal explaining the failed experiment. The
// experiment runs in a worktree this function creates and removes, never in
// the worker's checkout.
func upstreamProbeCloseViolation(repoDir, probe string) string {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" || !upstreamProbeGitOK(repoDir, "rev-parse", "--is-inside-work-tree") {
		return ""
	}
	if !upstreamProbeGitOK(repoDir, "rev-parse", "--verify", "--quiet", upstreamProbeUpstreamRef+"^{commit}") {
		return ""
	}
	if !upstreamProbeGitOK(repoDir, "rev-parse", "--verify", "--quiet", upstreamProbeBaseRef+"^{commit}") {
		return ""
	}
	if !upstreamProbeGitOK(repoDir, "symbolic-ref", "--quiet", "HEAD") {
		return ""
	}

	// A criss-cross has several bases and A...B silently picks one. The paths
	// are read against each: a branch touching no upstream-owned path stays
	// exempt, and any other is refused below rather than judged on git's pick.
	bases, err := upstreamProbeGitLines(repoDir, "merge-base", "--all", upstreamProbeBaseRef, "HEAD")
	if err != nil {
		return fmt.Sprintf("could not find the merge base with %s: %v", upstreamProbeBaseRef, err)
	}
	var changed []string
	for _, base := range bases {
		paths, err := upstreamProbeGitLines(repoDir, "diff", "--name-only", base, "HEAD")
		if err != nil {
			return fmt.Sprintf("could not inspect this branch against %s: %v", upstreamProbeBaseRef, err)
		}
		changed = append(changed, paths...)
	}
	if len(changed) == 0 {
		return ""
	}
	upstreamTree, err := upstreamProbeGitLines(repoDir, "ls-tree", "-r", "--name-only", upstreamProbeUpstreamRef)
	if err != nil {
		return fmt.Sprintf("could not read %s: %v", upstreamProbeUpstreamRef, err)
	}
	owned := make(map[string]struct{}, len(upstreamTree))
	for _, path := range upstreamTree {
		owned[path] = struct{}{}
	}
	touchesUpstream := false
	for _, path := range changed {
		if _, ok := owned[path]; ok {
			touchesUpstream = true
			break
		}
	}
	if !touchesUpstream {
		return ""
	}
	if len(bases) != 1 {
		short := make([]string, len(bases))
		for i, base := range bases {
			short[i] = base[:min(10, len(base))]
		}
		return fmt.Sprintf("%[1]s and HEAD have %[2]d merge bases (%[3]s), so no single diff is this branch's own change; merge %[1]s into this branch (git merge %[1]s) so it is an ancestor, then close again", upstreamProbeBaseRef, len(bases), strings.Join(short, ", "))
	}
	mergeBase := bases[0]
	kept, reverted, behavioral, embedErr := upstreamProbeClassifyPaths(repoDir, mergeBase, "HEAD", changed)
	violation := upstreamProbeSplitViolation(repoDir, mergeBase, strings.TrimSpace(probe), kept, reverted, behavioral)
	if violation != "" && embedErr != nil {
		// The paths the refusal names are innocent, so the cause travels too.
		violation += fmt.Sprintf("; the //go:embed scan of HEAD failed, so no comment, doc or sweep spec could be read as unembedded: %v; correct that directive and close again", embedErr)
	}
	return violation
}

// upstreamProbeSplitViolation judges probe against the branch's classified
// paths, returning the close refusal or "" as upstreamProbeCloseViolation does.
func upstreamProbeSplitViolation(repoDir, mergeBase, probe string, kept, reverted, behavioral []string) string {
	if probe == "" && len(behavioral) == 0 {
		if tests := upstreamProbeKeptTests(kept); len(tests) == 0 {
			return upstreamProbeUntestedViolation(repoDir, reverted)
		}
		return fmt.Sprintf("edits only test paths or behavior-free paths (comments, unembedded docs), so no command can fail without this patch; set %s to the command that runs the branch's tests, which the merge sweep still requires", upstreamProbeMetadataKey)
	}
	if probe == "" {
		return fmt.Sprintf("edits upstream-owned code; set %s to the test command that fails without this patch", upstreamProbeMetadataKey)
	}
	if len(behavioral) == 0 {
		return runUpstreamProbeExperiment(repoDir, mergeBase, probe, nil, nil)
	}
	if len(upstreamProbeKeptTests(kept)) == 0 {
		return "edits upstream-owned code but adds no test path to keep for its retirement experiment"
	}

	return runUpstreamProbeExperiment(repoDir, mergeBase, probe, reverted, behavioral)
}

// upstreamProbeUntestedViolation refuses a behavior-free branch that keeps no
// test, so its remedy cannot be the branch's own tests. It names what this
// gate checks and, where one exists, a touched package that holds tests.
func upstreamProbeUntestedViolation(repoDir string, reverted []string) string {
	msg := fmt.Sprintf("edits only behavior-free paths (comments, unembedded docs) and keeps no test, so no command can fail without this patch; the upstream audit reports it inconclusive without running %s, but the merge sweep requires one declared, and this gate accepts any command that exits 0 on the branch tip and runs at least one test", upstreamProbeMetadataKey)
	pkg, err := upstreamProbeTestedPackage(repoDir, reverted)
	switch {
	case err != nil:
		msg += fmt.Sprintf("; could not list HEAD to name a tested package: %v", err)
	case pkg != "":
		msg += ", such as go test -run <an existing test> " + pkg
	}
	return msg
}

// upstreamProbeTestedPackage returns, as a go test argument, the first
// directory in HEAD's tree order that holds a _test.go and a reverted path, or
// "" when no touched directory holds a test.
func upstreamProbeTestedPackage(repoDir string, reverted []string) (string, error) {
	touched := map[string]bool{}
	for _, p := range reverted {
		touched[path.Dir(p)] = true
	}
	tree, err := upstreamProbeGitLines(repoDir, "ls-tree", "-r", "--name-only", "HEAD")
	if err != nil {
		return "", err
	}
	for _, p := range tree {
		if dir := path.Dir(p); strings.HasSuffix(p, "_test.go") && touched[dir] {
			return "./" + strings.TrimPrefix(dir+"/", "./"), nil
		}
	}
	return "", nil
}

// runUpstreamProbeExperiment requires probe to run a test and pass on the
// branch tip, then to fail once reverted is undone. An empty reverted (a
// branch with no behavioral production change) stops after the first half:
// the city's upstream audit reports such a patch inconclusive and leaves it
// to reading. behavioral only names the reason in a "still passes" refusal.
func runUpstreamProbeExperiment(repoDir, mergeBase, probe string, reverted, behavioral []string) string {
	tmpDir, err := os.MkdirTemp("/var/tmp", "gc-upstream-probe-")
	if err != nil {
		return fmt.Sprintf("could not create isolated probe worktree: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	worktree := filepath.Join(tmpDir, "tree")
	defer func() {
		_, _ = upstreamProbeGitOutput(repoDir, "worktree", "remove", "--force", worktree)
		_, _ = upstreamProbeGitOutput(repoDir, "worktree", "prune")
	}()
	if _, err := upstreamProbeGitOutput(repoDir, "worktree", "add", "--detach", worktree, "HEAD"); err != nil {
		return fmt.Sprintf("could not create isolated probe worktree: %v", err)
	}
	if output, err := upstreamProbeRun(worktree, probe); err != nil {
		return fmt.Sprintf("%s fails on the branch tip, before anything is reverted (exit %d); correct the command before closing", upstreamProbeMetadataKey, exitCode(err))
	} else if upstreamProbeVacuous(output) {
		return fmt.Sprintf("%s ran no test on the branch tip, before anything is reverted; point it at the regression test", upstreamProbeMetadataKey)
	}
	if len(reverted) == 0 {
		return ""
	}

	patch, err := upstreamProbeGitOutput(repoDir, append([]string{"diff", "--binary", "HEAD", mergeBase, "--"}, reverted...)...)
	if err != nil || strings.TrimSpace(patch) == "" {
		return "could not build the reverse patch for this branch's production changes"
	}
	patchPath := filepath.Join(tmpDir, "reverse.patch")
	if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
		return fmt.Sprintf("could not write the reverse patch: %v", err)
	}
	if _, err := upstreamProbeGitOutput(worktree, "apply", patchPath); err != nil {
		return fmt.Sprintf("could not remove this branch's production changes in isolation: %v", err)
	}
	if output, err := upstreamProbeRun(worktree, probe); err == nil {
		if upstreamProbeVacuous(output) {
			return fmt.Sprintf("%s ran no test after production changes were reverted; point it at the regression test", upstreamProbeMetadataKey)
		}
		return fmt.Sprintf("%s still passes after this branch's production changes are reverted (behavioral: %s); use the regression command that fails without the patch", upstreamProbeMetadataKey, strings.Join(behavioral, ", "))
	}
	return ""
}

func upstreamProbeGitOK(repoDir string, args ...string) bool {
	_, err := upstreamProbeGitOutput(repoDir, args...)
	return err == nil
}

func upstreamProbeGitLines(repoDir string, args ...string) ([]string, error) {
	out, err := upstreamProbeGitOutput(repoDir, args...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// upstreamProbeGitOutput returns git's stdout alone. Its stderr can carry a
// warning on success (a second merge base under A...B), which a path list
// would read as paths, so stderr travels only in the error.
func upstreamProbeGitOutput(repoDir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repoDir}, args...)...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(bytes.TrimSpace(exitErr.Stderr)) > 0 {
			return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(exitErr.Stderr))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func upstreamProbeRun(worktree, probe string) (string, error) {
	cmd := exec.Command("bash", "-c", probe)
	cmd.Dir = worktree
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// upstreamProbeTestPath decides which branch paths the retirement experiment
// keeps. A test suffix missing here is reverted as production code, and a
// runner that skips a missing file (node --test does) then reruns the
// pre-patch suite green, so the gate refuses a correct probe. contrib/
// openclaw-bridge tests are Node modules under a nested test/, which the
// top-level test/ prefix does not reach, hence .test.mjs and .test.cjs.
// The city's upstream-patch audit reads this split through
// `gc internal upstream-classify` and keeps no copy of it.
func upstreamProbeTestPath(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.tsx") ||
		strings.HasSuffix(path, ".test.js") || strings.HasSuffix(path, ".test.jsx") ||
		strings.HasSuffix(path, ".test.mjs") || strings.HasSuffix(path, ".test.cjs") ||
		strings.HasSuffix(path, ".test.py") || strings.HasSuffix(path, "_test.py") ||
		strings.HasSuffix(path, ".test.sh") ||
		strings.HasPrefix(path, "test/") || upstreamProbeTestdataPath(path)
}

// upstreamProbeTestdataPath reports whether path lies under a directory named
// testdata. The //go:embed scan skips these because the experiment keeps them.
func upstreamProbeTestdataPath(path string) bool {
	return strings.HasPrefix(path, "testdata/") || strings.Contains(path, "/testdata/")
}

// upstreamProbeKeptTests returns the kept paths that are tests. A sweep spec
// is kept so the revert leaves it alone, but no probe runs it, so it cannot
// stand in for the test a behavioral patch owes its retirement experiment.
func upstreamProbeKeptTests(kept []string) []string {
	var tests []string
	for _, path := range kept {
		if upstreamProbeTestPath(path) {
			tests = append(tests, path)
		}
	}
	return tests
}

func upstreamProbeVacuous(output string) bool {
	output = strings.ToLower(output)
	for _, marker := range []string{"no tests to run", "no test files", "no tests ran", "no test files found", "collected 0 items"} {
		if strings.Contains(output, marker) {
			return true
		}
	}
	return false
}
