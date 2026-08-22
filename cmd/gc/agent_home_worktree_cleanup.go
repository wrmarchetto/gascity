package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/git"
)

const worktreeStaleFileName = ".worktree-stale"

// agentWorktreeGitProbe is the subset of git.Git used by
// cleanupClosedBeadAgentHomeWorktrees. Defined as an interface so tests can
// inject a fake without standing up real git worktrees.
//
// HasUncommittedWork is absent in favor of HasUncommittedWorkExcluding. This
// pass only ever looks at worktrees that HAVE a .worktree-stale marker, and the
// marker is untracked dirt in the worktree it describes -- so the plain probe
// answered "dirty" on the pass's own entry condition, and Case B could never
// fire for any marker it was written to clear (bead ci-ciu63).
type agentWorktreeGitProbe interface {
	IsRepo() bool
	CurrentBranch() (string, error)
	HasUncommittedWorkExcluding(paths ...string) bool
	CheckoutDetach(ref string) error
	DefaultBranch() (string, error)
}

// newAgentWorktreeGitProbe is the factory for the git probe. Tests may
// replace this var to inject a fake implementation.
var newAgentWorktreeGitProbe = func(workDir string) agentWorktreeGitProbe {
	return git.New(workDir)
}

// cleanupClosedBeadAgentHomeWorktrees scans the named-session (agent home)
// worktrees for each rig and cleans up stale .worktree-stale markers:
//
//   - Case A: worktree is already detached (CurrentBranch == "HEAD") →
//     remove the marker unconditionally (no rebase can be needed).
//   - Case B: worktree is on a branch whose bead ID is confirmed closed →
//     reset to detached origin/main and remove the marker, provided the
//     working tree has no uncommitted changes.
//
// Scope is every agent home, INCLUDING the numbered and namepool slot homes a
// pool materializes -- see isConfiguredAgentHomeDir for why an exact match
// against cfg.Agents silently excluded exactly the directories this pass exists
// to recover. Per-bead worktrees are skipped; the bead_worktree_reaper handles
// those.
// Returns the number of worktrees cleaned.
func cleanupClosedBeadAgentHomeWorktrees(
	cityPath string,
	cfg *config.City,
	cityStore beads.Store,
	rigBeadStores map[string]beads.Store,
	stderr io.Writer,
) int {
	if stderr == nil {
		stderr = io.Discard
	}
	if cfg == nil || len(rigBeadStores) == 0 {
		return 0
	}

	wtRoot := filepath.Join(cityPath, ".gc", "worktrees")
	cleaned := 0

	for rigName := range rigBeadStores {
		rigWorktreeDir := filepath.Join(wtRoot, rigName)
		entries, err := os.ReadDir(rigWorktreeDir)
		if err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(stderr, "cleanupClosedBeadAgentHomeWorktrees: reading %s: %v\n", rigWorktreeDir, err) //nolint:errcheck
			}
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isConfiguredAgentHomeDir(cfg, name) {
				continue
			}

			worktreePath := filepath.Join(rigWorktreeDir, name)
			stalePath := filepath.Join(worktreePath, worktreeStaleFileName)
			if _, err := os.Stat(stalePath); err != nil {
				continue
			}

			wg := newAgentWorktreeGitProbe(worktreePath)
			if !wg.IsRepo() {
				continue
			}

			branch, err := wg.CurrentBranch()
			if err != nil {
				continue
			}

			// Case A: already detached — the stale marker is false, remove it.
			if branch == "HEAD" {
				if removeErr := os.Remove(stalePath); removeErr == nil {
					fmt.Fprintf(stderr, "cleanupClosedBeadAgentHomeWorktrees: removed false .worktree-stale from %s (already detached)\n", worktreePath) //nolint:errcheck
					cleaned++
				}
				continue
			}

			// Case B: on a named branch — check whether its bead is closed.
			beadID := beadIDFromBranch(cfg, branch)
			if beadID == "" {
				continue
			}
			// Named agent-home worktrees are allocated for city work, so their
			// branch bead belongs to the city's store even though the worktree
			// itself lives below a rig directory.
			if cityStore == nil {
				continue
			}
			bead, err := cityStore.Get(beadID)
			if err != nil || bead.Status != "closed" {
				continue
			}

			// Safety: never reset a worktree that has uncommitted work. The
			// marker that put this worktree in scope is excluded -- see the
			// agentWorktreeGitProbe doc comment.
			if wg.HasUncommittedWorkExcluding(worktreeStaleFileName) {
				fmt.Fprintf(stderr, "cleanupClosedBeadAgentHomeWorktrees: skipping %s: bead %s closed but has uncommitted work\n", worktreePath, beadID) //nolint:errcheck
				continue
			}

			defaultBranch, err := wg.DefaultBranch()
			if err != nil || strings.TrimSpace(defaultBranch) == "" {
				defaultBranch = "main"
			}
			resetRef := "origin/" + defaultBranch
			if err := wg.CheckoutDetach(resetRef); err != nil {
				fmt.Fprintf(stderr, "cleanupClosedBeadAgentHomeWorktrees: resetting %s to %s: %v\n", worktreePath, resetRef, err) //nolint:errcheck
				continue
			}
			if removeErr := os.Remove(stalePath); removeErr != nil && !os.IsNotExist(removeErr) {
				fmt.Fprintf(stderr, "cleanupClosedBeadAgentHomeWorktrees: removing stale marker from %s: %v\n", worktreePath, removeErr) //nolint:errcheck
			}
			fmt.Fprintf(stderr, "cleanupClosedBeadAgentHomeWorktrees: reset %s to %s (bead %s closed)\n", worktreePath, resetRef, beadID) //nolint:errcheck
			cleaned++
		}
	}
	return cleaned
}

// isConfiguredAgentHomeDir reports whether dirName, a direct child of
// .gc/worktrees/<rig>/, is the home worktree of a configured agent -- either
// the agent's own home or one of the pool-slot instances the controller
// materializes from it.
//
// Exact-matching cfg.Agents names, which is what this pass did until bead
// ci-ciu63, misses every pool slot. Pool instances are runtime-only identities
// built by deepCopyAgent (cmd/gc/pool.go) during reconcile and never appended
// to cfg.Agents, so a home like "toolsmith-2" matched nothing and nothing in
// the city ever cleared its .worktree-stale marker. That is what made a false
// marker cost a slot permanently rather than until the next teardown, and it
// also makes doctor_worktree_stale.go's FixHint -- "the controller clears
// markers only after its fail-closed recovery checks prove the worktree is
// resolved" -- true again for the case an operator actually reads it in.
//
// The numeric form is matched by pattern rather than enumerated because
// max_active_sessions may be unlimited, so there is no finite slot list to
// build. Themed namepool instances REPLACE the base name rather than suffixing
// it (poolInstanceName, cmd/gc/build_desired_state.go), so "furiosa" shares no
// prefix with "polecat" and has to be matched literally.
//
// Two bounds keep the widening from becoming "clear markers everywhere":
// slot-shaped names count only for agents that actually get slot identities
// synthesized (SupportsExpandedSessionIdentities), and a name that parses as a
// bead ID is never an agent home however else it matches. "<prefix>-<digits>"
// is a legal bead ID and would otherwise collide with the numeric slot pattern
// of an agent named after the bead prefix; per-bead worktrees belong to the
// reaper, which applies liveness and orphan-commit gates this pass does not
// have.
func isConfiguredAgentHomeDir(cfg *config.City, dirName string) bool {
	if cfg == nil || dirName == "" {
		return false
	}
	if extractBeadIDFromWorktreeName(cfg, dirName) != "" {
		return false
	}
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		base := a.BindingQualifiedName()
		if base == "" {
			continue
		}
		if dirName == base {
			return true
		}
		if !a.SupportsExpandedSessionIdentities() {
			continue
		}
		if slot, ok := strings.CutPrefix(dirName, base+"-"); ok && isDecimalDigits(slot) {
			return true
		}
		for _, themed := range a.NamepoolNames {
			if themed != "" && dirName == a.BindingPrefix()+themed {
				return true
			}
		}
	}
	return false
}

// isDecimalDigits reports whether s is one or more ASCII digits. Deliberately
// not strconv.Atoi, which accepts a leading sign and would make "builder--1"
// read as slot -1.
func isDecimalDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// beadIDFromBranch extracts a bead ID from a branch name of the form
// "<agent>/<beadID-slug>" or bare "<beadID>". Returns "" when the branch
// contains no valid configured bead ID.
func beadIDFromBranch(cfg *config.City, branch string) string {
	if branch == "" || branch == "HEAD" {
		return ""
	}
	// Strip optional leading agent-name segment (e.g. "builder/ga-abc123" → "ga-abc123").
	suffix := branch
	for i := 0; i < len(branch); i++ {
		if branch[i] == '/' {
			suffix = branch[i+1:]
			break
		}
	}
	return extractBeadIDFromWorktreeName(cfg, suffix)
}
