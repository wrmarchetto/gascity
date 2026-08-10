package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
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

// specGateArtifacts are the six paths one `make spec-ci` run writes: the spec
// itself, its four committed docs mirrors, and the typed Go client generated
// from it. They are one artifact SET, judged by one invocation, because five of
// the six would otherwise read as dirty sources of the sixth -- both
// internal/api/openapi.json and internal/api/genclient/client_gen.go sit inside
// the internal/ source pathspec (bead ci-d4lw).
var specGateArtifacts = []string{
	"internal/api/openapi.json",
	"docs/reference/schema/openapi.json",
	"docs/reference/schema/openapi.txt",
	"docs/reference/schema/events.json",
	"docs/reference/schema/events.txt",
	"internal/api/genclient/client_gen.go",
}

// genschemaArtifacts are exactly what cmd/genschema writes -- the same list as
// GEN_PATHS in scripts/check-generated-docs-drift.sh and the path allowlist in
// scripts/docs-autofix-push.sh.
//
// Both gates over this set (make check-schema and the CI script) must name
// these six paths and nothing else. check-schema used to diff all of
// docs/reference/, where only these 6 of 28 tracked files come from
// cmd/genschema: 4 more are cmd/genspec output (the openapi.* and events.*
// mirrors, gated by spec-ci, which is why narrowing to 6 loses no coverage) and
// the remaining 18 are hand-written, so editing one of those failed a gate that
// never regenerates it.
var genschemaArtifacts = []string{
	"docs/reference/cli.md",
	"docs/reference/config.md",
	"docs/reference/schema/city-schema.json",
	"docs/reference/schema/city-schema.txt",
	"docs/reference/schema/pack-schema.json",
	"docs/reference/schema/pack-schema.txt",
}

// generatedDocsPathOwners are the four files that each carry their own copy of
// the genschema output list, each paired with the regexp that isolates the list
// itself.
//
// Two filters, and BOTH are load-bearing against the same mutation -- a path
// commented out rather than deleted, which is how a list actually falls behind.
// The section anchor is needed because every one of these lists sits next to
// prose naming the same paths, so a whole-file match reads a path demoted into
// neighboring documentation as though it were still in the list. Comment
// stripping is needed because the anchor alone does not help when the comment
// is INSIDE the list.
//
// Verified by mutation both ways: replacing one arm of path_allowed with
// `# docs/reference/cli.md is intentionally not allowlisted right now` leaves
// the whole scripts package green under a whole-file match AND under a section
// match that keeps comments, while the autofix job silently declines to apply
// the fix CI just computed.
//
// Requiring a file extension is what drops the one non-path match, the
// docs/reference/schema mkdir target in the generator.
var generatedDocsPathOwners = []struct {
	path    string
	section *regexp.Regexp
}{
	{"cmd/genschema/main.go", regexp.MustCompile(`(?s)files := \[\]string\{.*?\n\t\}`)},
	{"scripts/check-generated-docs-drift.sh", regexp.MustCompile(`(?sm)^GEN_PATHS=\(.*?^\)`)},
	{"scripts/docs-autofix-push.sh", regexp.MustCompile(`(?s)path_allowed\(\) \{.*?\n\}`)},
	{"scripts/test-check-generated-docs-drift.sh", regexp.MustCompile(`(?sm)^GEN_PATHS=\(.*?^\)`)},
}

var (
	generatedDocsPathRE = regexp.MustCompile(`docs/reference/[A-Za-z0-9_./-]+\.[A-Za-z0-9]+`)
	// Covers both syntaxes in generatedDocsPathOwners; no list here has a
	// trailing comment on a path line, so whole-line stripping is enough.
	commentLineRE = regexp.MustCompile(`(?m)^[ \t]*(#|//).*$`)
)

// listedPaths returns the generated-docs paths a list literal names, with
// commented-out lines removed first. See generatedDocsPathOwners for why both
// steps are required.
func listedPaths(section string) []string {
	paths := generatedDocsPathRE.FindAllString(commentLineRE.ReplaceAllString(section, ""), -1)
	slices.Sort(paths)
	return slices.Compact(paths)
}

// goGeneratedSources is the source set both Go-generator gates declare. It is
// deliberately COARSER than each generator's import closure, and the direction
// of the error is the whole point: a source set that is a superset of the real
// inputs can only ever over-hedge (report unattributable where stale-index was
// true), while a set missing one real input reports stale-index with confidence
// when the reader's own uncommitted edit explains the diff -- the ci-gpxg
// agent-hour, restated.
//
// Three measurements ruled out the narrower sets (ci-d4lw):
//
//   - Per-package naming is wrong. internal/config embeds pricing.ModelPricing
//     from internal/pricing, so city-schema.json reflects types outside
//     internal/config; and docs/reference/cli.md renders the whole cobra tree,
//     whose flag defaults reach constants across internal/.
//   - `go list -deps` is wrong too, and worse because it looks precise. It
//     reports 141 of the module's 191 packages for cmd/genschema + cmd/gc, and
//     it reports DIRECTORIES -- so it cannot exclude the _test.go files that
//     are the most-edited thing not affecting generated output. Its real defect
//     is that docgen calls jsonschema AddGoComments over every git-tracked
//     non-hidden top-level directory (internal/docgen/schema.go), so doc
//     comments outside the import closure are inputs and the closure would
//     under-cover.
//   - test/, examples/, scripts/ and docs/ are excluded on purpose. They are
//     inside that AddGoComments walk, but a comment only lands in the output
//     for a type the reflector actually reflected, and no config or wire type
//     is defined there today. Including them would put a dirty _test.go in
//     every local verdict, which is the "unattributable on every run" failure.
//     If a config or wire type is ever defined under test/ or examples/, this
//     set silently under-covers and belongs here.
var goGeneratedSources = []string{"cmd", "internal", "go.mod", "go.sum"}

// TestCheckArtifactDriftGate runs the shell self-test for
// scripts/check-artifact-drift.sh, the generated-artifact staleness gate
// dashboard-ci uses (bead ci-c425). It drives every classification branch --
// clean, stale-index, unattributable, unproven -- against real temp git repos
// holding plain text files, and asserts the two failure verdicts cannot be
// confused with each other. Hermetic: temp git repos only, no npm, no node,
// no network.
func TestCheckArtifactDriftGate(t *testing.T) {
	runShellSuite(t, "test-check-artifact-drift.sh")
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
	recipe := expandedRecipe(t, "dashboard-ci")

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

// TestSpecCIAttributesArtifactDrift pins the spec-ci wiring the same way, and
// pins one thing the dashboard gates never needed: all six generated paths must
// reach a SINGLE gate invocation. Split across six invocations the gate is
// unable to fire at all, because each run would see the other five rebuilt
// paths as dirty sources under internal/ and answer unattributable -- or, when
// the drifted path is not the one that run is scanning, exit 0.
//
// Read from `make -n` rather than the Makefile text so a check smuggled in
// through a variable or a prerequisite target still shows up. Safe to run:
// nothing in the spec-ci chain is a $(MAKE) recursion or a `+`-prefixed line,
// so no recipe executes -- which is the only reason a test can afford to touch
// a target whose real run compiles the API and shells out to oapi-codegen.
func TestSpecCIAttributesArtifactDrift(t *testing.T) {
	recipe := expandedRecipe(t, "spec-ci")

	if !strings.Contains(recipe, "scripts/check-artifact-drift.sh") {
		t.Fatalf("spec-ci must route staleness checks through scripts/check-artifact-drift.sh:\n%s", recipe)
	}
	if got := strings.Count(recipe, "scripts/check-artifact-drift.sh"); got != 1 {
		t.Errorf("spec-ci invokes the gate %d times, want 1: the six generated paths are one "+
			"artifact set, and a per-path invocation reads the other five as dirty sources:\n%s", got, recipe)
	}
	for _, artifact := range specGateArtifacts {
		if !strings.Contains(recipe, "--artifact "+artifact) {
			t.Errorf("spec-ci does not check %s through the attributing gate:\n%s", artifact, recipe)
		}
	}
	assertNoBareDiff(t, "spec-ci", recipe)
	assertDeclaresGoSources(t, "spec-ci", recipe)
}

// TestCheckSchemaDelegatesToGeneratedDocsGate pins that make check-schema does
// not carry a second implementation of the genschema drift gate. It used to,
// and the copy had drifted from the CI one in both directions: it diffed the
// whole of docs/reference/ (see genschemaArtifacts for the file counts) and it
// ran the generator without CGO_ENABLED=0, which the script sets because the
// transitive dolt ICU dependency will not compile on hosts without ICU headers.
//
// Delegation is also what keeps the list of generated paths from acquiring a
// fourth copy -- cmd/genschema writes it, the script's GEN_PATHS names it, and
// scripts/docs-autofix-push.sh allowlists it.
func TestCheckSchemaDelegatesToGeneratedDocsGate(t *testing.T) {
	recipe := expandedRecipe(t, "check-schema")

	if !strings.Contains(recipe, "scripts/check-generated-docs-drift.sh") {
		t.Fatalf("check-schema must delegate to scripts/check-generated-docs-drift.sh rather than "+
			"reimplement the genschema drift gate:\n%s", recipe)
	}
	if regexp.MustCompile(`docs/reference/?[\s)"']`).MatchString(recipe) {
		t.Errorf("check-schema still judges the whole docs/reference/ tree; only the six paths "+
			"cmd/genschema writes are generated, and the rest are hand-written:\n%s", recipe)
	}
	// The generator must run only inside the script, which sets CGO_ENABLED=0.
	// The `generate` prerequisite this target used to carry ran the same command
	// without it, and on a host with no ICU headers that is a build failure in a
	// dependency genschema never uses. Nothing else in the suite pins this.
	if strings.Contains(recipe, "cmd/genschema") {
		t.Errorf("check-schema runs the generator itself; it must reach cmd/genschema only "+
			"through check-generated-docs-drift.sh, which sets CGO_ENABLED=0:\n%s", recipe)
	}
	assertNoBareDiff(t, "check-schema", recipe)
}

// TestGeneratedDocsDriftGate runs the behavioral suite for
// scripts/check-generated-docs-drift.sh -- the gate CI's preflight-generated
// job runs and whose patch output the docs-autofix workflow applies.
//
// The suite drives the real script with a stand-in `go` first on PATH, in a
// temp git repo, so the classification, the exit status and the patch emission
// are all exercised without compiling cmd/genschema. Injecting at the process
// boundary rather than adding a flag to the script is deliberate: a
// GENERATOR=... switch would be read before the code under test runs, so the
// suite would pin the switch and the real invocation could be deleted with the
// tests still green. The stand-in refuses any argv it was not scripted for.
func TestGeneratedDocsDriftGate(t *testing.T) {
	runShellSuite(t, "test-check-generated-docs-drift.sh")
}

// TestGeneratedDocsPathListsAgree makes an existing hand-sync rule mechanical.
// The generated-docs list is written out four times -- the generator that
// produces the files, the GEN_PATHS the drift gate judges, the allowlist
// confining which paths an untrusted autofix patch may name, and the fixture
// that drives the gate's own suite -- and until this test the only thing
// holding them together was a "keep in sync" comment in three of them.
//
// Each copy fails differently when it falls behind, and none of the failures is
// loud: a path missing from GEN_PATHS is a generated file no gate ever checks,
// a path missing from the allowlist is an autofix that silently declines to
// apply the fix CI just computed, a path missing from the fixture is a
// generated file the suite stops driving, and a path in any of them that the
// generator no longer writes is a gate asserting freshness of a file nothing
// regenerates.
func TestGeneratedDocsPathListsAgree(t *testing.T) {
	root := repoRoot(t)

	want := slices.Clone(genschemaArtifacts)
	slices.Sort(want)

	for _, owner := range generatedDocsPathOwners {
		body, err := os.ReadFile(filepath.Join(root, owner.path))
		if err != nil {
			t.Fatalf("reading %s: %v", owner.path, err)
		}
		section := owner.section.FindString(string(body))
		if section == "" {
			t.Errorf("%s: no list matching %s -- the list was renamed or reshaped, "+
				"so this test can no longer see it", owner.path, owner.section)
			continue
		}
		if got := listedPaths(section); !slices.Equal(got, want) {
			t.Errorf("%s names generated docs\n  %v\nwant\n  %v", owner.path, got, want)
		}
	}
}

// runShellSuite runs one of the shell test suites in scripts/ and fails with
// its full output. Both suites go through this rather than each spawning their
// own process: test/test-resources.toml ratchets subprocess call sites and
// forbids growth, so a second literal exec.Command here would be new debt for
// no gain.
//
// The environment is deliberately minimal and HOME/TMPDIR are per-test temp
// dirs -- the suites build git repositories, and a leaked real HOME would let
// the developer's ~/.gitconfig change what the fixtures do.
func runShellSuite(t *testing.T, script string) {
	t.Helper()
	root := repoRoot(t)

	cmd := exec.Command(filepath.Join(root, "scripts", script))
	cmd.Dir = root
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"TMPDIR=" + t.TempDir(),
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v\n%s", script, err, out)
	}
}

// expandedRecipe returns `make -n <target>` output, which is the recipe as make
// would run it with every variable and prerequisite expanded.
func expandedRecipe(t *testing.T, target string) string {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("make", "-n", target)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s failed: %v\n%s", target, err, out)
	}
	return string(out)
}

// bareStalenessCheckRE matches a recipe deciding staleness from its own
// worktree-vs-index comparison. It is a pattern rather than the two literal
// strings the predecessor listed, because the two forms this change removed are
// not the only phrasings: `test -z "$(git diff --name-only -- docs/reference)"`
// restores the identical defect and walked straight past a
// strings.Contains denylist. `git status --porcelain` is the other spelling.
//
// The scripts the recipes CALL run `git diff --name-only` internally; only the
// expanded recipe text is searched, so that is not a false positive.
var bareStalenessCheckRE = regexp.MustCompile(
	`git (--no-pager )?diff[^|\n]*(--quiet|--exit-code|--name-only|--stat)|git status --porcelain`)

// assertNoBareDiff fails when a recipe still decides staleness with its own
// worktree-vs-index comparison. A bare diff is not merely redundant beside the
// gate: it answers "the artifact moved" when the question is "did the committed
// artifact stop matching the committed sources", and it cannot see an untracked
// new output file at all.
func assertNoBareDiff(t *testing.T, target, recipe string) {
	t.Helper()
	if bare := bareStalenessCheckRE.FindString(recipe); bare != "" {
		t.Errorf("%s still decides staleness with its own `%s`; that conflates a stale "+
			"committed artifact with unstaged source edits and is blind to untracked "+
			"additions (ci-c425, ci-d4lw):\n%s", target, bare, recipe)
	}
}

// assertDeclaresGoSources fails when a Go-generator gate does not declare the
// full coarse source set. A gate missing one of these reports stale-index --
// "the committed artifact is not a build of the committed sources" -- for a
// diff the reader's own uncommitted edit explains, which is the
// confidently-wrong verdict ci-c425 set out to eliminate.
func assertDeclaresGoSources(t *testing.T, target, recipe string) {
	t.Helper()
	for _, source := range goGeneratedSources {
		if !strings.Contains(recipe, "--source "+source+" ") &&
			!strings.HasSuffix(strings.TrimRight(recipe, "\n"), "--source "+source) {
			t.Errorf("%s does not declare %q as a source of its generated artifacts; "+
				"see goGeneratedSources for why the set is this coarse:\n%s", target, source, recipe)
		}
	}
}
