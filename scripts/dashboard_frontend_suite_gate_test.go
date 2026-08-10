// Scope: the local dashboard gate's coverage of the SPA frontend vitest
// suite, internal/api/dashboardspa/web/frontend/src/**/*.test.{ts,tsx},
// which make dashboard-check must run (bead ci-w09j).
//
// This suite exists because the gate and its own documentation had drifted
// apart in the direction that reads as green. .githooks/pre-commit calls
// `make dashboard-check dashboard-smoke` as its blocking gate on staged SPA
// source and explains the choice with "The Makefile target also runs the
// Vitest suite" -- and no revision of dashboard-check has ever run it. Every
// Makefile revision touching the target was scanned and none carries a
// vitest or npm-test line; the pre-commit sentence dates to e1be2b246
// (2026-04-21), two months before the current SPA was vendored at 677ce243f
// (2026-06-28), so it was never true of this suite either. CI's Dashboard
// SPA job does run it, so the only symptom was a frontend regression
// committing cleanly and failing later in CI.
//
// The reason that survived a survey for it is worth recording, because the
// next person will run the same search: `grep -rn vitest` over the
// Makefile, .githooks/ and .github/workflows/ reports NOTHING even though
// CI runs the suite. The CI step is named "Vitest" and its command is
// `npm run --workspace gas-city-dashboard-frontend test`, so the lowercase
// token appears in neither. The suite reads as ungated from the outside and
// as gated from the inside, which is why this file pins the wiring instead
// of trusting a grep.
//
// Delegated elsewhere: whether the suite passes is the suite's own job, and
// dashboard-ci's generated-artifact staleness verdicts belong to
// check_artifact_drift_test.go. Nothing here runs npm or node.
//
// Run: go test ./scripts -run 'TestDashboard.*Frontend|TestDashboardCheck'

package scripts_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// frontendWorkspace owns the vitest suite. Matched as a substring of one
// expanded recipe line rather than pinning the whole `npm run --workspace
// <name> test` invocation, so reordering npm's flags cannot fail a gate that
// still runs the suite.
const frontendWorkspace = "gas-city-dashboard-frontend"

// frontendSuiteInclude is vitest's collection glob, verbatim from
// frontend/vitest.config.ts. Pinned here because the non-empty check below
// counts files under frontend/src: if the glob moves to another directory
// and this constant does not follow, that count stops proving anything.
const frontendSuiteInclude = "'src/**/*.test.{ts,tsx}'"

// frontendSuiteInvocation returns the expanded recipe line that runs the
// frontend workspace's `test` script, or "" if no line does. `test` is
// matched as a whole field so the typecheck:test step, which compiles the
// same files without executing an assertion, cannot satisfy this.
func frontendSuiteInvocation(recipe string) string {
	for _, line := range strings.Split(recipe, "\n") {
		if !strings.Contains(line, frontendWorkspace) {
			continue
		}
		for _, field := range strings.Fields(line) {
			if field == "test" {
				return line
			}
		}
	}
	return ""
}

// TestDashboardCheckRunsFrontendVitestSuite pins that the local gate
// executes the frontend suite, not merely type-checks it.
//
// It reads the EXPANDED recipe from `make -n` rather than grepping the
// Makefile, for the reason check_artifact_drift_test.go records: a step
// reached through a prerequisite or a variable still shows up. `make -n` is
// safe on this chain because nothing in it is a $(MAKE) recursion or a
// `+`-prefixed line, so no recipe executes -- the only reason a test can
// afford to touch a target whose real run is an npm build.
func TestDashboardCheckRunsFrontendVitestSuite(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command("make", "-n", "dashboard-check")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n dashboard-check failed: %v\n%s", err, out)
	}

	if frontendSuiteInvocation(string(out)) == "" {
		t.Fatalf("make dashboard-check does not execute the %s vitest suite.\n"+
			".githooks/pre-commit calls this target as its blocking gate on staged SPA source and says it "+
			"runs the suite, AGENTS.md names make dashboard-ci -- which depends on this target -- as the "+
			"gate a dashboard change must pass, and CI's Dashboard SPA job runs the suite. Without this "+
			"step the local gate passes a frontend regression that CI then rejects, and typecheck:test "+
			"keeps it looking covered by compiling the tests without running them.\n"+
			"Remedy: add this line to the dashboard-check recipe:\n"+
			"\tcd internal/api/dashboardspa/web && npm run --workspace %s test\n"+
			"Expanded recipe:\n%s", frontendWorkspace, frontendWorkspace, out)
	}
}

// TestDashboardFrontendSuiteCannotPassWithoutRunningTests pins the two ways
// the step above could stay wired and still cover nothing.
//
// `vitest run` exits 1 when its glob collects no file -- measured on vitest
// 4.1.9, not assumed -- which is why this gate needs no wrapper script of
// its own, the way the shared node:test suite needed
// scripts/dashboard-shared-tests.sh to turn a zero-file run into a failure.
// passWithNoTests is the one setting that would take that self-guard away,
// so its absence is asserted rather than relied on.
func TestDashboardFrontendSuiteCannotPassWithoutRunningTests(t *testing.T) {
	frontend := filepath.Join(repoRoot(t), "internal", "api", "dashboardspa", "web", "frontend")

	config, err := os.ReadFile(filepath.Join(frontend, "vitest.config.ts"))
	if err != nil {
		t.Fatalf("read frontend/vitest.config.ts: %v", err)
	}
	if strings.Contains(string(config), "passWithNoTests") {
		t.Fatal("frontend/vitest.config.ts must not set passWithNoTests: it converts a suite whose " +
			"glob has stopped matching into a green run, which is the failure this gate exists to catch")
	}
	if !strings.Contains(string(config), frontendSuiteInclude) {
		t.Fatalf("frontend/vitest.config.ts no longer collects %s, so the file count below no longer "+
			"describes what the gate runs. Point frontendSuiteInclude and the counted directory at the "+
			"new layout -- do not delete the check", frontendSuiteInclude)
	}

	src := filepath.Join(frontend, "src")
	suiteFiles := 0
	err = filepath.WalkDir(src, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".test.ts") || strings.HasSuffix(name, ".test.tsx") {
			suiteFiles++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", src, err)
	}
	if suiteFiles == 0 {
		t.Fatalf("no *.test.ts or *.test.tsx found under %s. The frontend suite is never legitimately "+
			"empty: this means the layout moved and the dashboard-check step has stopped covering "+
			"anything -- fix the path, do not delete the gate", src)
	}
}
