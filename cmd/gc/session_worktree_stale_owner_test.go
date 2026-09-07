package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
)

// Scope: the ownership rule in validateWorkDirForSessionAssignment
// (session_reconciler.go) -- which session a .worktree-stale marker may refuse,
// and which one it must admit.
//
// The suite exists because the two protections around that marker deadlocked.
// The prune path refuses to remove a worktree holding uncommitted work and
// writes the marker saying so; the assignment gate then refused to start any
// session in a worktree carrying the marker. No session could reach the tree to
// commit the work, so the condition the marker records could never clear and
// the slot respawned into the refusal on a loop -- three slots, each holding a
// P1, until the operator committed by hand (bead ci-4btflb).
//
// Two arms live elsewhere and are not duplicated here:
// TestPrepareStartCandidateForCity_RejectsStaleAssignedTaskWorkDir
// (session_lifecycle_parallel_test.go) is the foreign-worktree refusal through
// the real prepare path, and
// TestRealGitStaleMarkerWriterAdmitsItsOwnSlot
// (session_worktree_stale_owner_realgit_test.go) drives writer and gate against
// real git, which is the only place the marker's untracked-dirt property is
// representable at all.
//
// Run: go test ./cmd/gc/ -run StaleWorktreeOwner

// TestStaleWorktreeOwnerGateAdmitsItsOwnSlot pins the arm that broke the loop: a
// candidate whose CONFIGURED work dir is the marked directory is admitted.
//
// Ownership is decided from the configured home rather than from the marker's
// branch= line. Branch matching reads as the narrower, safer rule and does not
// break the loop at all: every session in the incident was a fresh one carrying
// a different bead, which is the normal shape after a mid-turn death, so branch
// matching refuses exactly the sessions that have to be admitted.
func TestStaleWorktreeOwnerGateAdmitsItsOwnSlot(t *testing.T) {
	home := t.TempDir()
	writeStaleMarkerForTest(t, home, "fix/ci-4btflb-slug")

	if err := validateWorkDirForSessionAssignment(home, home); err != nil {
		t.Fatalf("validateWorkDirForSessionAssignment(own home) = %v, want nil: no other actor can ever commit the work this marker protects", err)
	}
}

// TestStaleWorktreeOwnerGateRefusesForeignWorktree is the arm that keeps the
// protection: a candidate redirected into someone else's marked worktree --
// through a task bead's work_dir, a stored session work_dir, or a config
// mistake -- is still refused. Without this arm the change removes the marker
// rather than fixing the deadlock.
func TestStaleWorktreeOwnerGateRefusesForeignWorktree(t *testing.T) {
	foreign := t.TempDir()
	own := t.TempDir()
	writeStaleMarkerForTest(t, foreign, "fix/ci-other-slug")

	err := validateWorkDirForSessionAssignment(foreign, own)
	if !errors.Is(err, errStaleWorktreeMarker) {
		t.Fatalf("validateWorkDirForSessionAssignment(foreign) = %v, want errStaleWorktreeMarker", err)
	}
	if got := staleWorktreeMarkerRefusedWorkDir(err); got != foreign {
		t.Fatalf("refused work dir = %q, want %q: the quarantine and its operator mail read the marker at this path, not at the candidate's configured home", got, foreign)
	}
}

// TestStaleWorktreeOwnerGateRefusesWithoutKnownOwner pins the fail-closed
// residue: an empty configured home establishes no ownership, so it admits
// nothing. Deliberately NOT admit-on-unknown -- an unresolvable home means the
// marked directory was reached through an override, which is the foreign case.
func TestStaleWorktreeOwnerGateRefusesWithoutKnownOwner(t *testing.T) {
	marked := t.TempDir()
	writeStaleMarkerForTest(t, marked, "fix/ci-4btflb-slug")

	if err := validateWorkDirForSessionAssignment(marked, ""); !errors.Is(err, errStaleWorktreeMarker) {
		t.Fatalf("validateWorkDirForSessionAssignment(unknown owner) = %v, want errStaleWorktreeMarker", err)
	}
}

// TestStaleWorktreeOwnerWriterAndGateCannotDeadlock is the invariant the bead
// asked to assert, and it is a PAIRED test on purpose: it drives the real
// writer (the prune path's uncommitted-work refusal) and then the real reader
// (the assignment gate) over the same directory. Asserting each end separately
// is what let the deadlock exist -- both refusals are correct alone, and only
// the pair is wrong.
func TestStaleWorktreeOwnerWriterAndGateCannotDeadlock(t *testing.T) {
	fx := newPruneFixture(t)
	fx.setProbe(fx.workerDir, &fakeGitProbe{
		isRepo:         true,
		hasUncommitted: true,
		currentBranch:  "fix/ci-4btflb-slug",
	})

	var stderr bytes.Buffer
	if pruneAgentHomeWorktreeIfSafe(fx.sessionBead(), fx.cityPath, fx.cfg, &stderr) {
		t.Fatal("prune removed a worktree with uncommitted work; the fixture no longer reproduces the writer half")
	}
	assertWorktreeStaleMarker(t, fx.workerDir, "fix/ci-4btflb-slug", "uncommitted-work")

	if err := validateWorkDirForSessionAssignment(fx.workerDir, fx.workerDir); err != nil {
		t.Fatalf("gate refused the slot the writer just marked: %v -- the reconciler must never refuse both to clean and to use one worktree", err)
	}
}

// TestStaleWorktreeOwnerPrepareAdmitsOwnHome runs the admitted arm through
// prepareStartCandidateForCity rather than the gate alone, because the gate is
// called with a work dir that three separate resolution steps can move
// (configured template, task bead work_dir, stored session work_dir). A gate
// test cannot see a caller that passes the wrong pair of paths.
func TestStaleWorktreeOwnerPrepareAdmitsOwnHome(t *testing.T) {
	store := beads.NewMemStore()
	home := t.TempDir()
	writeStaleMarkerForTest(t, home, "fix/ci-4btflb-slug")

	session, err := store.Create(beads.Bead{
		Title:  "worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:frontend/worker-1"},
		Metadata: map[string]string{
			"template":     "worker",
			"session_name": "custom-worker-1",
			"pool_slot":    "1",
			"work_dir":     home,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	prepared, err := prepareStartCandidateForCity(startCandidate{
		info: sessiontest.SeedBead(t, session),
		tp: TemplateParams{
			TemplateName: "frontend/worker",
			SessionName:  "custom-worker-1",
			WorkDir:      home,
		},
	}, "", "", &config.City{
		Agents: []config.Agent{{Name: "worker", Dir: "frontend", MinActiveSessions: intPtr(1), MaxActiveSessions: intPtr(2)}},
	}, nil, store, &clock.Fake{Time: time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)}, nil, nil)
	if err != nil {
		t.Fatalf("prepareStartCandidateForCity() error = %v, want nil for the slot the marker names", err)
	}
	if prepared.cfg.WorkDir != home {
		t.Fatalf("prepared.cfg.WorkDir = %q, want %q", prepared.cfg.WorkDir, home)
	}
}

// writeStaleMarkerForTest writes a marker in the same shape
// writeWorktreeStaleMarker produces, with the reason the prune path's
// uncommitted-work refusal records. Hand-written rather than routed through the
// writer so the gate arms stay readable at the point of failure; the
// writer/reader pairing is asserted by
// TestStaleWorktreeOwnerWriterAndGateCannotDeadlock instead.
//
// The reason is fixed rather than a parameter because the gate does not read it
// -- only branch= and reason= consumers do (staleWorktreeAlertFromMarker), and
// those are pinned in session_lifecycle_parallel_test.go. A reason parameter
// here would suggest the gate varies with it.
func writeStaleMarkerForTest(t *testing.T, dir, branch string) {
	t.Helper()
	content := "branch=" + branch + "\nreason=uncommitted-work\n"
	if err := os.WriteFile(filepath.Join(dir, worktreeStaleFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s marker: %v", worktreeStaleFileName, err)
	}
}
