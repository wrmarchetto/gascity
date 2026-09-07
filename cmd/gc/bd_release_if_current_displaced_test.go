package main

// Scope: the diagnostic content of `gc bd release-if-current`'s non-release
// outcome, and nothing about whether the write happens --
// TestDoBdReleaseIfCurrentUpdatesOnlyMatchingAssignment owns the CAS itself
// and this suite deliberately does not re-assert it.
//
// Why this suite exists: `skipped` was one word for three unrelated
// conditions. beads.ReleaseIfCurrent returns (false, nil) when the bead is
// missing, when it is not in_progress, and when a DIFFERENT holder has it
// (internal/beads/memstore.go reassignIfCurrent), and doBdReleaseIfCurrent
// printed the same line and exited 0 for all three.
//
// The third is a displaced agent's only contact with the truth. An agent
// handing work back after an operator reassigned the bead out from under it
// mid-turn saw `skipped`, exit 0, and had no way to tell that from "already
// released, nothing to do" -- so the displacement stayed silent on both
// sides (ci-32fp1p, deferred half of ci-q5spdz item 1). The decision recorded
// there is that the displaced holder learns at the boundary it already
// crosses rather than by being interrupted, which makes this line the
// notification.
//
// The exit status stays 0 in every skip case and that is asserted, not
// incidental: the agent's intent -- do not release a bead someone else holds
// -- was honored, and a nonzero exit would turn every correct refusal into a
// failed step for callers that check it.
//
// Run: go test ./cmd/gc/ -run ReleaseIfCurrentSkip

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// releaseSkipFixture builds a city with a file-backed store and returns it
// with the store. Separate from the sibling suite's inline setup because
// three cases need it and each needs a different bead state.
func releaseSkipFixture(t *testing.T) (string, beads.Store, execStoreTarget) {
	t.Helper()
	cityDir := t.TempDir()
	toml := "[workspace]\nname = \"demo\"\n\n[beads]\nprovider = \"file\"\n"
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	return cityDir, store, execStoreTarget{ScopeRoot: cityDir, ScopeKind: "city", Prefix: "gc"}
}

// TestReleaseIfCurrentSkipNamesTheHolderThatDisplacedTheCaller is the case the
// suite exists for. The bead is in_progress under someone else, which is what
// an operator assign leaves behind, and the caller is the agent that used to
// hold it.
//
// Asserting on the HOLDER's name rather than on any phrasing: the agent has
// to be able to say who took the bead, and a message that only reported "not
// yours" would leave it unable to. The expected assignee is asserted too, so
// a message that named one side and not the other cannot pass.
func TestReleaseIfCurrentSkipNamesTheHolderThatDisplacedTheCaller(t *testing.T) {
	cityDir, store, target := releaseSkipFixture(t)
	created, err := store.Create(beads.Bead{Title: "work", Assignee: "worker-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "worker-1", &stdout, &stderr); got != 0 {
		t.Fatalf("exit = %d, want 0: a refusal to release another agent's bead is the correct outcome, not a failed step; stderr=%q", got, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "worker-2") {
		t.Errorf("output = %q, want it to name worker-2: the displaced agent cannot report who took its bead from a message that omits the holder", out)
	}
	if !strings.Contains(out, "worker-1") {
		t.Errorf("output = %q, want it to name worker-1: without the expected assignee the line does not say whose claim was refused", out)
	}
}

// TestReleaseIfCurrentSkipDistinguishesAMissingBeadFromADisplacement pins the
// discrimination, not just the presence of detail. A single reworded `skipped`
// line would satisfy the test above and still conflate the three conditions,
// which is the defect. So a missing bead must NOT read as held by anyone.
func TestReleaseIfCurrentSkipDistinguishesAMissingBeadFromADisplacement(t *testing.T) {
	cityDir, _, target := releaseSkipFixture(t)

	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, "gc-nosuch", "worker-1", &stdout, &stderr); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "gc-nosuch") {
		t.Errorf("output = %q, want it to name the id it could not find", out)
	}
	if strings.Contains(out, "held by") {
		t.Errorf("output = %q, want no holder claim for a bead that does not exist -- reporting one sends an agent hunting a session that never had it", out)
	}
}

// TestReleaseIfCurrentSkipDistinguishesAnUnheldBeadFromADisplacement is the
// third condition: the bead exists and is open, so nobody holds it and there
// is nothing to release. An agent whose own release already landed, or whose
// bead was reopened by the reconciler, reaches this -- and must not be told
// someone displaced it.
func TestReleaseIfCurrentSkipDistinguishesAnUnheldBeadFromADisplacement(t *testing.T) {
	cityDir, store, target := releaseSkipFixture(t)
	created, err := store.Create(beads.Bead{Title: "work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "worker-1", &stdout, &stderr); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	out := stdout.String()
	if strings.Contains(out, "held by") {
		t.Errorf("output = %q, want no holder claim for an unheld bead", out)
	}
	if !strings.Contains(out, "open") {
		t.Errorf("output = %q, want it to name the status that made the release a no-op", out)
	}
}

// TestReleaseIfCurrentSuccessLineIsUnchanged is the control, and it is what
// keeps the three assertions above from being satisfied by a change that
// rewrote every outcome. `released` is the token callers key on for the
// success path; the diagnostics are only for the skip path.
func TestReleaseIfCurrentSuccessLineIsUnchanged(t *testing.T) {
	cityDir, store, target := releaseSkipFixture(t)
	created, err := store.Create(beads.Bead{Title: "work", Assignee: "worker-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "worker-1", &stdout, &stderr); got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "released" {
		t.Fatalf("success output = %q, want exactly \"released\"", stdout.String())
	}
}

// TestReleaseIfCurrentSkipKeepsItsFirstToken pins the compatibility bound of
// the change. Every skip line still begins with `skipped`, so a caller that
// matched the old one-word output by prefix or by substring keeps working;
// only an exact-equality match sees a difference, and the sibling suite's own
// assertion was the one such match in the tree.
func TestReleaseIfCurrentSkipKeepsItsFirstToken(t *testing.T) {
	cityDir, store, target := releaseSkipFixture(t)
	created, err := store.Create(beads.Bead{Title: "work", Assignee: "worker-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	var stdout, stderr bytes.Buffer
	doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "worker-1", &stdout, &stderr)
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "skipped") {
		t.Fatalf("skip output = %q, want it to still begin with \"skipped\"", stdout.String())
	}
}
