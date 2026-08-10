// Scope: the pre-push test-scope selector, scripts/push-gate-select, which
// decides whether a push runs the full fast suite or only the packages its
// changed paths can affect (bead ci-4w2t).
//
// This suite exists because the selector's failure mode is silent. A gate
// that stops running a package it should have run is indistinguishable from
// a green build, so almost every case below pins a decision to *widen* --
// unknown path shapes, unreadable graphs, missing bases -- rather than a
// decision to narrow. The narrowing cases are pinned too, but a bug there
// costs wall clock; a bug in the widening cases costs a broken main.
//
// Delegated elsewhere: the graph engine's package-closure semantics are
// pinned by pr_static_scope_contract_test.go against the lint gate, which
// shares scripts/goaffected.py. The always-run manifest's completeness is
// pinned by push_gate_always_run_test.go. End-to-end hook wiring is pinned
// by scripts/test-push-gate-select.sh.
//
// Run: go test ./scripts -run TestPushGateSelect

package scripts_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pushGateFixture is a throwaway Go module in a real git repo, with a real
// remote-side commit to diff against. The selector reads a pre-push stdin
// record, so the fixture has to carry genuine object names -- a synthetic
// SHA would exercise the parser but not the diff.
type pushGateFixture struct {
	repoRoot string
	baseSHA  string
	headSHA  string
	// Fixture packages are not the real repo's, so the real always-run
	// manifest could never resolve against them. Every case gets an empty
	// one by default; the cases that care override it through extraEnv.
	manifest string
}

const pushGateZeroSHA = "0000000000000000000000000000000000000000"

func newPushGateFixture(t *testing.T, base map[string]string) *pushGateFixture {
	t.Helper()

	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatalf("create temporary repository: %v", err)
	}
	writeTestFile(t, filepath.Join(repo, "go.mod"), "module example.com/push-gate\n\ngo 1.23\n")
	for name, content := range base {
		writeTestFile(t, filepath.Join(repo, name), content)
	}

	manifest := filepath.Join(t.TempDir(), "empty-always-run.manifest")
	writeTestFile(t, manifest, "# no always-run packages in the fixture module\n")

	fixture := &pushGateFixture{repoRoot: repo, manifest: manifest}
	fixture.git(t, "init", "-q", "-b", "main")
	fixture.git(t, "config", "user.email", "push-gate@example.invalid")
	fixture.git(t, "config", "user.name", "push-gate-test")
	fixture.git(t, "config", "commit.gpgsign", "false")
	fixture.git(t, "add", "-A")
	fixture.git(t, "commit", "-qm", "baseline")
	fixture.baseSHA = fixture.revParse(t, "HEAD")
	fixture.headSHA = fixture.baseSHA
	return fixture
}

func (f *pushGateFixture) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := testCommand("git", args...)
	cmd.Dir = f.repoRoot
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=push-gate-test", "GIT_AUTHOR_EMAIL=push-gate@example.invalid",
		"GIT_COMMITTER_NAME=push-gate-test", "GIT_COMMITTER_EMAIL=push-gate@example.invalid",
		"HOME="+f.repoRoot,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *pushGateFixture) revParse(t *testing.T, ref string) string {
	t.Helper()
	return f.git(t, "rev-parse", ref)
}

// commit writes files and commits them, advancing headSHA. A nil content
// value deletes the path, so deletion cases need no separate helper.
func (f *pushGateFixture) commit(t *testing.T, message string, files map[string]*string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(f.repoRoot, name)
		if content == nil {
			if err := os.Remove(full); err != nil {
				t.Fatalf("delete %s: %v", name, err)
			}
			continue
		}
		writeTestFile(t, full, *content)
	}
	f.git(t, "add", "-A")
	f.git(t, "commit", "-qm", message)
	f.headSHA = f.revParse(t, "HEAD")
}

// selectScope runs the real selector against the fixture with the given
// pre-push stdin record, and returns its decision line.
func (f *pushGateFixture) selectScope(t *testing.T, stdin string, extraEnv ...string) string {
	t.Helper()
	selector := filepath.Join(repoRoot(t), "scripts", "push-gate-select")
	cmd := testCommand("python3", selector)
	cmd.Dir = f.repoRoot
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Env = append(os.Environ(),
		"PYTHONDONTWRITEBYTECODE=1",
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME="+f.repoRoot,
		"PUSH_GATE_ALWAYS_RUN_MANIFEST="+f.manifest,
	)
	// os/exec keeps the last occurrence of a duplicated variable, so an
	// extraEnv override of the manifest wins over the default above.
	cmd.Env = append(cmd.Env, extraEnv...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("push-gate-select: %v\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(string(out))
}

func (f *pushGateFixture) pushRecord() string {
	return "refs/heads/main " + f.headSHA + " refs/heads/main " + f.baseSHA + "\n"
}

func ptr(s string) *string { return &s }

// The module the narrowing cases run against: alpha is a leaf, consumer
// imports it, and island is reachable from neither. A correct selection of
// an alpha change contains alpha and consumer and NOT island -- pinning both
// the closure and its boundary, since a selector that returned everything
// would pass a containment-only assertion.
func pushGateBaseFiles() map[string]string {
	return map[string]string{
		"alpha/alpha.go": "package alpha\n\nfunc Value() int { return 1 }\n",
		"consumer/consumer.go": `package consumer

import "example.com/push-gate/alpha"

func Value() int { return alpha.Value() }
`,
		"island/island.go": "package island\n\nfunc Value() int { return 1 }\n",
		"docs/guide.md":    "baseline\n",
	}
}

func decisionPackages(t *testing.T, decision string) []string {
	t.Helper()
	fields := strings.Fields(decision)
	if len(fields) == 0 || fields[0] != "scoped" {
		t.Fatalf("decision %q is not a scoped selection", decision)
	}
	return fields[1:]
}

func requireContains(t *testing.T, got []string, want string) {
	t.Helper()
	for _, item := range got {
		if item == want {
			return
		}
	}
	t.Errorf("selection %v is missing %q", got, want)
}

func requireExcludes(t *testing.T, got []string, unwanted string) {
	t.Helper()
	for _, item := range got {
		if item == unwanted {
			t.Errorf("selection %v unexpectedly contains %q", got, unwanted)
		}
	}
}

func TestPushGateSelectNarrowsToTheChangedPackageAndItsDependents(t *testing.T) {
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	packages := decisionPackages(t, f.selectScope(t, f.pushRecord()))
	requireContains(t, packages, "./alpha")
	requireContains(t, packages, "./consumer")
	requireExcludes(t, packages, "./island")
}

func TestPushGateSelectWidensToFullOnPathsOutsideTheGoGraph(t *testing.T) {
	// The engine's graph covers Go build inputs and embedded files. A test
	// may also read testdata, a shell script, or the Makefile at run time,
	// and no mechanical inventory of that exists -- so any path the graph
	// cannot account for has to widen the run rather than be assumed inert.
	cases := []struct {
		name string
		path string
	}{
		{"markdown", "docs/guide.md"},
		{"shell script", "scripts/helper.sh"},
		{"testdata fixture", "alpha/testdata/golden.txt"},
		{"makefile", "Makefile"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newPushGateFixture(t, pushGateBaseFiles())
			f.commit(t, "touch "+testCase.path, map[string]*string{
				"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
				testCase.path:    ptr("changed\n"),
			})

			if decision := f.selectScope(t, f.pushRecord()); decision != "full" {
				t.Errorf("changing %s decided %q, want full", testCase.path, decision)
			}
		})
	}
}

func pushGateEmbedFiles() map[string]string {
	base := pushGateBaseFiles()
	base["assets/assets.go"] = `package assets

import _ "embed"

//go:embed data.txt
var Data string
`
	base["assets/data.txt"] = "baseline\n"
	return base
}

func TestPushGateSelectLeavesADocsOnlyPushUntestedAsBefore(t *testing.T) {
	// Regression guard on the trigger condition, in the direction that is
	// easy to break while every other test stays green. The old hook keyed
	// on "*.go changed" and skipped a docs-only push for free; an
	// unknown-path rule applied BEFORE that question turns the same push
	// into a 16-minute full suite. Roughly a quarter of recent commits here
	// touch no Go file at all, so getting this backwards makes the gate
	// slower on average while every scoped-run test still passes.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "docs only", map[string]*string{
		"docs/guide.md": ptr("revised\n"),
	})

	if decision := f.selectScope(t, f.pushRecord()); decision != "none" {
		t.Errorf("docs-only push decided %q, want none (the pre-existing skip)", decision)
	}
}

func TestPushGateSelectScopesAnEmbeddedFileToItsOwningPackage(t *testing.T) {
	// Embedded files are the one non-Go path shape the graph accounts for:
	// go list names the owner exactly. So once a Go change has engaged the
	// gate, a changed embed seeds its owner instead of widening the run --
	// pack and template edits travel with Go edits constantly here.
	f := newPushGateFixture(t, pushGateEmbedFiles())
	f.commit(t, "touch embedded data alongside a Go change", map[string]*string{
		"assets/data.txt": ptr("changed\n"),
		"alpha/alpha.go":  ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	packages := decisionPackages(t, f.selectScope(t, f.pushRecord()))
	requireContains(t, packages, "./assets")
	requireContains(t, packages, "./alpha")
	requireExcludes(t, packages, "./island")
}

func TestPushGateSelectLeavesAnEmbedOnlyChangeUntestedAsBefore(t *testing.T) {
	// Pins a DELIBERATE ABSENCE, not an oversight. The pre-existing hook
	// keyed on "*.go changed" and skipped an embed-only push entirely; this
	// selector keeps that trigger byte-for-byte so it can never run less
	// than before. Engaging on embeds would be a coverage improvement, but
	// the bootstrap packs are embedded by a package ~180 others import, so
	// it converts a free push into a full-suite push. If that trade is ever
	// taken, it needs its own measurement -- and this test is what will
	// fail to announce the change.
	f := newPushGateFixture(t, pushGateEmbedFiles())
	f.commit(t, "touch only embedded data", map[string]*string{
		"assets/data.txt": ptr("changed\n"),
	})

	if decision := f.selectScope(t, f.pushRecord()); decision != "none" {
		t.Errorf("embed-only push decided %q, want none (the pre-existing skip)", decision)
	}
}

func TestPushGateSelectWidensToFullWhenNoBaseExistsToDiffAgainst(t *testing.T) {
	// A brand-new remote branch has no cheap base. The pre-existing hook
	// ran the full suite here and this must not quietly become a narrow
	// run: there is no diff at all, so there is no evidence to narrow on.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	record := "refs/heads/main " + f.headSHA + " refs/heads/main " + pushGateZeroSHA + "\n"
	if decision := f.selectScope(t, record); decision != "full" {
		t.Errorf("new remote branch decided %q, want full", decision)
	}
}

func TestPushGateSelectReportsNothingToTestForABranchDeletion(t *testing.T) {
	f := newPushGateFixture(t, pushGateBaseFiles())
	record := "(delete) " + pushGateZeroSHA + " refs/heads/gone " + f.baseSHA + "\n"
	if decision := f.selectScope(t, record); decision != "none" {
		t.Errorf("branch deletion decided %q, want none", decision)
	}
}

func TestPushGateSelectWidensToFullWhenTheGraphCannotBeRead(t *testing.T) {
	// Fail-closed on an unusable toolchain. Without this the selector would
	// answer "nothing changed" for a repo it simply could not parse, which
	// is the exact shape of a green build that ran nothing.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	brokenGo := filepath.Join(t.TempDir(), "go")
	writeExecutable(t, brokenGo, "#!/bin/sh\necho 'go list exploded' >&2\nexit 1\n")
	decision := f.selectScope(t, f.pushRecord(), "PUSH_GATE_GO="+brokenGo)
	if decision != "full" {
		t.Errorf("unreadable graph decided %q, want full", decision)
	}
}

func TestPushGateSelectWidensToFullWhenAPackageLosesItsLastGoFile(t *testing.T) {
	// Deleting a package's only source leaves a changed path whose owning
	// package no longer exists in the graph. The engine cannot prove what
	// that deletion affected, so the run must widen.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "delete island", map[string]*string{
		"island/island.go": nil,
	})

	if decision := f.selectScope(t, f.pushRecord()); decision != "full" {
		t.Errorf("package deletion decided %q, want full", decision)
	}
}

func TestPushGateSelectAlwaysIncludesTheRepoWideScannerPackages(t *testing.T) {
	// The always-run manifest names packages whose tests read the whole
	// repository rather than their own imports (the resource census, the
	// git-tracked-source guards). Nothing imports them from a changed leaf,
	// so the closure alone would drop them from every narrow run.
	base := pushGateBaseFiles()
	base["census/census.go"] = "package census\n\nfunc Value() int { return 1 }\n"
	f := newPushGateFixture(t, base)
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	manifest := filepath.Join(t.TempDir(), "always-run.manifest")
	writeTestFile(t, manifest, "# fixture manifest\n./census\n")
	packages := decisionPackages(t, f.selectScope(t, f.pushRecord(),
		"PUSH_GATE_ALWAYS_RUN_MANIFEST="+manifest))

	requireContains(t, packages, "./census")
	requireContains(t, packages, "./alpha")
	requireExcludes(t, packages, "./island")
}

func TestPushGateSelectWidensToFullWhenTheAlwaysRunManifestIsUnreadable(t *testing.T) {
	// A missing manifest must not silently drop the repo-wide scanners from
	// the run. Widening is the only safe reading of "I could not tell which
	// packages must always run".
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	missing := filepath.Join(t.TempDir(), "absent.manifest")
	if decision := f.selectScope(t, f.pushRecord(),
		"PUSH_GATE_ALWAYS_RUN_MANIFEST="+missing); decision != "full" {
		t.Errorf("missing always-run manifest decided %q, want full", decision)
	}
}

func TestPushGateSelectWidensToFullWhenAManifestPackageIsNotInTheGraph(t *testing.T) {
	// A manifest entry that no longer names a real package means the
	// always-run set has rotted. Running the listed-but-absent package is
	// impossible, so the run widens instead of quietly skipping it.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	manifest := filepath.Join(t.TempDir(), "always-run.manifest")
	writeTestFile(t, manifest, "./no/such/package\n")
	if decision := f.selectScope(t, f.pushRecord(),
		"PUSH_GATE_ALWAYS_RUN_MANIFEST="+manifest); decision != "full" {
		t.Errorf("stale manifest entry decided %q, want full", decision)
	}
}

func TestPushGateSelectUnionsEveryPushedRef(t *testing.T) {
	// git feeds one stdin line per pushed ref. Selecting from only the first
	// would leave a second branch's packages untested on a multi-ref push.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})
	alphaHead := f.headSHA
	alphaBase := f.baseSHA

	f.commit(t, "touch island", map[string]*string{
		"island/island.go": ptr("package island\n\nfunc Value() int { return 3 }\n"),
	})

	record := "refs/heads/main " + alphaHead + " refs/heads/main " + alphaBase + "\n" +
		"refs/heads/next " + f.headSHA + " refs/heads/next " + alphaHead + "\n"
	packages := decisionPackages(t, f.selectScope(t, record))
	requireContains(t, packages, "./alpha")
	requireContains(t, packages, "./island")
}

func TestPushGateSelectRefusesAnUnparseableStdinRecord(t *testing.T) {
	// Malformed input is not "no refs pushed". Treating it as nothing to do
	// would let a garbled hand-off skip the suite entirely.
	f := newPushGateFixture(t, pushGateBaseFiles())
	if decision := f.selectScope(t, "not-a-valid-record\n"); decision != "full" {
		t.Errorf("malformed stdin decided %q, want full", decision)
	}
}

func TestPushGateSelectEmitsExactlyOneRecognizedDecisionLine(t *testing.T) {
	// The hook parses this on stdout and treats anything unrecognized as
	// full. Pin the shape so a debug print added later cannot be read as a
	// package name.
	f := newPushGateFixture(t, pushGateBaseFiles())
	f.commit(t, "touch alpha", map[string]*string{
		"alpha/alpha.go": ptr("package alpha\n\nfunc Value() int { return 2 }\n"),
	})

	raw := f.selectScope(t, f.pushRecord())
	if strings.Count(raw, "\n") != 0 {
		t.Fatalf("selector emitted %d stdout lines, want exactly 1:\n%s",
			strings.Count(raw, "\n")+1, raw)
	}
	verb := strings.Fields(raw)[0]
	if verb != "full" && verb != "none" && verb != "scoped" {
		t.Fatalf("selector emitted unrecognized decision verb %q", verb)
	}
}

func TestPushGateSelectIsNotReachedByAFakeGoOnPath(t *testing.T) {
	// Guard on the test harness itself: PUSH_GATE_GO is the only injection
	// seam, so a fixture that forgot it would silently exercise the real
	// toolchain against a temp module and pass for the wrong reason.
	selector := filepath.Join(repoRoot(t), "scripts", "push-gate-select")
	content, err := os.ReadFile(selector)
	if err != nil {
		t.Fatalf("read selector: %v", err)
	}
	if !strings.Contains(string(content), "PUSH_GATE_GO") {
		t.Fatal("push-gate-select must keep PUSH_GATE_GO as its go-tool injection seam")
	}
}
