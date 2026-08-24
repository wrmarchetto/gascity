package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
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
// contract applies or when probe distinguishes the branch from its production
// changes. A non-empty result is a close refusal explaining the failed
// experiment. The experiment runs in a worktree this function creates and
// removes, never in the worker's checkout.
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

	changed, err := upstreamProbeGitLines(repoDir, "diff", "--name-only", upstreamProbeBaseRef+"...HEAD")
	if err != nil {
		return fmt.Sprintf("could not inspect this branch against %s: %v", upstreamProbeBaseRef, err)
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
	var touchesUpstream bool
	var kept, reverted []string
	for _, path := range changed {
		if _, ok := owned[path]; ok {
			touchesUpstream = true
		}
		if upstreamProbeTestPath(path) {
			kept = append(kept, path)
		} else {
			reverted = append(reverted, path)
		}
	}
	if !touchesUpstream {
		return ""
	}
	probe = strings.TrimSpace(probe)
	if probe == "" {
		return fmt.Sprintf("edits upstream-owned code; set %s to the test command that fails without this patch", upstreamProbeMetadataKey)
	}
	if len(kept) == 0 {
		return "edits upstream-owned code but adds no test path to keep for its retirement experiment"
	}
	if len(reverted) == 0 {
		return "edits only test paths, so there is no production change to test for retirement"
	}

	return runUpstreamProbeExperiment(repoDir, probe, reverted)
}

func runUpstreamProbeExperiment(repoDir, probe string, reverted []string) string {
	mergeBase, err := upstreamProbeGitOutput(repoDir, "merge-base", upstreamProbeBaseRef, "HEAD")
	if err != nil {
		return fmt.Sprintf("could not find the merge base with %s: %v", upstreamProbeBaseRef, err)
	}
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
		return fmt.Sprintf("%s fails before production changes are reverted (exit %d); correct the command before closing", upstreamProbeMetadataKey, exitCode(err))
	} else if upstreamProbeVacuous(output) {
		return fmt.Sprintf("%s ran no test before production changes were reverted; point it at the regression test", upstreamProbeMetadataKey)
	}

	patch, err := upstreamProbeGitOutput(repoDir, append([]string{"diff", "--binary", "HEAD", strings.TrimSpace(mergeBase), "--"}, reverted...)...)
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
		return fmt.Sprintf("%s still passes after this branch's production changes are reverted; use the regression command that fails without the patch", upstreamProbeMetadataKey)
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

func upstreamProbeGitOutput(repoDir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
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

func upstreamProbeTestPath(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.HasSuffix(path, ".test.ts") || strings.HasSuffix(path, ".test.tsx") ||
		strings.HasSuffix(path, ".test.js") || strings.HasSuffix(path, ".test.jsx") ||
		strings.HasSuffix(path, ".test.py") || strings.HasSuffix(path, "_test.py") ||
		strings.HasSuffix(path, ".test.sh") ||
		strings.HasPrefix(path, "test/") || strings.HasPrefix(path, "testdata/") ||
		strings.Contains(path, "/testdata/")
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
