// Scope: the guessed-default-branch warning emitted by Provision's step 12,
// and the predicate that decides when it fires.
//
// The suite exists because the branch this warning describes is the only path
// on which gc rig add persists a value it knows might be wrong: a rig
// registered from a checkout several agent sessions share records whichever
// session's feature branch was on disk, and default_branch is what polecats
// and the refinery target (ci-6m97). Silence there cost gascity its mainline
// once already, so the warning is pinned as behavior, not left to the CLI's
// output tests.
//
// Whether the probe itself resolves a branch correctly belongs to
// internal/git (TestProbeDefaultBranch_*); here ProbeBranch is injected, so
// these tests pin only what Provision does with a guessed answer.
//
// Run: go test ./internal/rig/ -run TestProvisionGuessedDefaultBranch

package rig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// probeStub returns a ProbeBranch that answers only for wantPath and fails the
// test for anything else. A stub that answered every path would hand a pass to
// a Provision that probed the city directory, or the wrong rig on a multi-rig
// config, and the returned branch would look identical from the outside.
func probeStub(t *testing.T, wantPath, branch string, guessed bool, calls *int) func(string) (string, bool) {
	t.Helper()
	return func(got string) (string, bool) {
		if got != wantPath {
			t.Fatalf("ProbeBranch(%q), want probe of %q", got, wantPath)
		}
		*calls++
		return branch, guessed
	}
}

// guessedWarningCity builds a temp city whose city.toml exists on disk (the
// write phase needs it) plus a rig directory carrying a .git entry, since
// resolveGitDefaultBranch stats rigPath/.git before it consults the probe. The
// .git is an empty directory, not a repo: ProbeBranch is injected, so nothing
// here shells out to git.
func guessedWarningCity(t *testing.T, existing []config.Rig) (Deps, string) {
	t.Helper()
	cityPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"branchcity\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rigPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rigPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	deps := stubDeps(cityPath)
	deps.NormalizeScopes = func(string, *config.City) error { return nil }
	if len(existing) > 0 {
		for i := range existing {
			existing[i].Path = rigPath
		}
		deps.Cfg = &config.City{Rigs: existing}
	}
	return deps, rigPath
}

// warningMentioning returns the single warning containing needle, or "" — the
// test asserts on content rather than on Warnings[0] so an unrelated warning
// arriving later cannot silently shift the index and pass.
func warningMentioning(warnings []string, needle string) string {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return w
		}
	}
	return ""
}

func TestProvisionGuessedDefaultBranchWarns(t *testing.T) {
	deps, rigPath := guessedWarningCity(t, nil)
	calls := 0
	deps.ProbeBranch = probeStub(t, rigPath, "fix/ci-yxpd-stop-gate", true, &calls)

	_, res, err := Provision(deps, ProvisionRequest{Name: "branchrig", Path: rigPath})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if calls != 1 {
		t.Fatalf("ProbeBranch called %d times, want 1", calls)
	}

	warn := warningMentioning(res.Warnings, "fix/ci-yxpd-stop-gate")
	if warn == "" {
		t.Fatalf("no warning named the guessed branch; warnings: %v", res.Warnings)
	}
	// The operator has to be able to act on this without reading the source, so
	// the text is pinned for the three things it must carry: what went wrong,
	// which rig it landed on, and the remedy.
	for _, want := range []string{"refs/remotes/origin/HEAD", "branchrig", "default_branch", "--default-branch"} {
		if !strings.Contains(warn, want) {
			t.Errorf("warning is missing %q:\n%s", want, warn)
		}
	}
	if !slices.ContainsFunc(res.Steps, func(s ProvisionStep) bool {
		return s.Name == "default-branch-guessed" && s.Warn
	}) {
		t.Errorf("no Warn step named default-branch-guessed; steps: %v", res.Steps)
	}
}

func TestProvisionGuessedDefaultBranchQuietWhenAuthoritative(t *testing.T) {
	deps, rigPath := guessedWarningCity(t, nil)
	calls := 0
	deps.ProbeBranch = probeStub(t, rigPath, "trunk", false, &calls)

	_, res, err := Provision(deps, ProvisionRequest{Name: "branchrig", Path: rigPath})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := warningMentioning(res.Warnings, "default branch"); got != "" {
		t.Errorf("an authoritative probe must not warn, got:\n%s", got)
	}
	// The informational line is separate from the warning and must survive: a
	// fix that suppressed both would pass a warning-only assertion.
	if !slices.ContainsFunc(res.Steps, func(s ProvisionStep) bool {
		return s.Name == "default-branch" && s.Detail == "  Default branch: trunk"
	}) {
		t.Errorf("the Default branch line went missing; steps: %v", res.Steps)
	}
}

// TestProvisionGuessedDefaultBranchSkipsProbeForOverride pins that
// --default-branch short-circuits the probe entirely. Warning about a name the
// operator typed would train them to ignore the warning that matters, and the
// injected stub would fail the test if it were consulted at all.
func TestProvisionGuessedDefaultBranchSkipsProbeForOverride(t *testing.T) {
	deps, rigPath := guessedWarningCity(t, nil)
	deps.ProbeBranch = func(got string) (string, bool) {
		t.Fatalf("ProbeBranch(%q) ran despite an explicit --default-branch", got)
		return "", false
	}

	_, res, err := Provision(deps, ProvisionRequest{Name: "branchrig", Path: rigPath, DefaultBranch: "develop"})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := warningMentioning(res.Warnings, "default branch"); got != "" {
		t.Errorf("an explicit override must not warn, got:\n%s", got)
	}
}

// TestProvisionGuessedDefaultBranchWarnsOnBackfillingReAdd covers the re-add
// leg. A rig entry that predates default-branch detection has no
// default_branch, so buildNextRigConfig backfills the probe's answer -- guess
// included -- and the re-add path emits no "Default branch:" line at all, which
// makes this warning the only signal that a feature branch just became the
// rig's mainline.
func TestProvisionGuessedDefaultBranchWarnsOnBackfillingReAdd(t *testing.T) {
	deps, rigPath := guessedWarningCity(t, []config.Rig{{Name: "branchrig"}})
	calls := 0
	deps.ProbeBranch = probeStub(t, rigPath, "fix/ci-yxpd-stop-gate", true, &calls)

	_, res, err := Provision(deps, ProvisionRequest{Name: "branchrig", Path: rigPath})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if warningMentioning(res.Warnings, "fix/ci-yxpd-stop-gate") == "" {
		t.Fatalf("a backfilling re-add must warn; warnings: %v", res.Warnings)
	}
}

// TestProvisionGuessedDefaultBranchQuietWhenAlreadyRecorded is the other half
// of the re-add leg: an operator who has already corrected default_branch by
// hand must not be warned about a value this run will not touch.
func TestProvisionGuessedDefaultBranchQuietWhenAlreadyRecorded(t *testing.T) {
	deps, rigPath := guessedWarningCity(t, []config.Rig{{Name: "branchrig", DefaultBranch: "main"}})
	calls := 0
	deps.ProbeBranch = probeStub(t, rigPath, "fix/ci-yxpd-stop-gate", true, &calls)

	_, res, err := Provision(deps, ProvisionRequest{Name: "branchrig", Path: rigPath})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := warningMentioning(res.Warnings, "fix/ci-yxpd-stop-gate"); got != "" {
		t.Errorf("a rig with default_branch already recorded must not warn, got:\n%s", got)
	}
}
