package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// distGateArtifacts are the two generated artifacts dashboard-ci is
// responsible for: the embedded SPA bundle and the typed API client generated
// from internal/api/openapi.json. Both are rebuilt by the recipe and both must
// be judged by the attributing gate rather than a bare worktree-vs-index diff.
var distGateArtifacts = []string{
	"internal/api/dashboardspa/dist",
	"internal/api/dashboardspa/web/shared/src/generated/gc-supervisor-client",
}

// TestCheckArtifactDriftGate runs the shell self-test for
// scripts/check-artifact-drift.sh, the generated-artifact staleness gate
// dashboard-ci uses (bead ci-c425). It drives every classification branch --
// clean, stale-index, unattributable, unproven -- against real temp git repos
// holding plain text files, and asserts the two failure verdicts cannot be
// confused with each other. Hermetic: temp git repos only, no npm, no node,
// no network.
func TestCheckArtifactDriftGate(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command(filepath.Join(root, "scripts", "test-check-artifact-drift.sh"))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test-check-artifact-drift.sh failed: %v\n%s", err, out)
	}
}

// TestDashboardCIAttributesArtifactDrift pins the wiring: dashboard-ci must
// judge both generated artifacts through check-artifact-drift.sh and must not
// carry a bare `git diff --quiet -- <artifact>` staleness check of its own.
//
// It reads the EXPANDED recipe from `make -n` rather than grepping the
// Makefile text, so a check smuggled in through a variable or a sibling target
// still shows up. `make -n` is safe to run here because nothing in the
// dashboard-ci chain is a $(MAKE) recursion or a `+`-prefixed line, so no
// recipe executes -- which is the only reason a test can afford to touch a
// target whose real run is an npm build.
//
// A bare diff is not merely redundant here, it is the ci-gpxg regression: it
// answers "the artifact moved" when the question is "did the committed
// artifact stop matching the committed sources", and it cannot see an
// untracked new asset at all.
func TestDashboardCIAttributesArtifactDrift(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command("make", "-n", "dashboard-ci")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n dashboard-ci failed: %v\n%s", err, out)
	}
	recipe := string(out)

	if !strings.Contains(recipe, "scripts/check-artifact-drift.sh") {
		t.Fatalf("dashboard-ci must route staleness checks through scripts/check-artifact-drift.sh:\n%s", recipe)
	}
	for _, artifact := range distGateArtifacts {
		if !strings.Contains(recipe, "--artifact "+artifact) {
			t.Errorf("dashboard-ci does not check %s through the attributing gate:\n%s", artifact, recipe)
		}
		if strings.Contains(recipe, "git diff --quiet -- "+artifact) {
			t.Errorf("dashboard-ci still carries a bare `git diff --quiet -- %s` staleness check; "+
				"that conflates a stale committed artifact with unstaged source edits (ci-c425)", artifact)
		}
	}
}
