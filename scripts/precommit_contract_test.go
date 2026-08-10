package scripts_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestPreCommitFormatterPreservesFileMode(t *testing.T) {
	// The staged bytes deliberately lack the trailing newline the linter
	// adds, so the formatter has to take its rewrite path. Handing it
	// already-formatted content would short-circuit at the `cmp -s` and the
	// mode assertion would pass without the rewrite it exists to check ever
	// having run.
	out, source, err := runStagedGoFormatter(t, formatterFixture{
		withGo:        true,
		pinned:        newlineAppendingLinter(),
		sourceContent: "package main",
	})
	if err != nil {
		t.Fatalf("precommit formatter failed: %v\n%s", err, out)
	}

	info, err := os.Stat(source)
	if err != nil {
		t.Fatalf("stat formatted source: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Fatalf("formatted source mode = %o, want 644", got)
	}
	if content := readOptionalFile(t, source); content != "package main\n" {
		t.Fatalf("formatted content = %q, want package main with newline", content)
	}
}

// TestPreCommitFormatterNamesRemedyWhenLinterUnreachable pins the message a
// contributor sees when golangci-lint resolves nowhere. Blocking the commit
// is correct -- what was not is `golangci-lint: command not found` plus a
// generic summary, which names neither the cause nor a remedy and reads as a
// broken hook. The next move it invites is --no-verify, and on the push side
// --no-verify also skips scripts/push-ownership-guard.sh (bead ci-f6u4).
func TestPreCommitFormatterNamesRemedyWhenLinterUnreachable(t *testing.T) {
	tests := []struct {
		name   string
		withGo bool
	}{
		// Both arms of resolution come up empty: nothing on PATH, and
		// nothing at the pinned location either.
		{name: "installed nowhere", withGo: true},
		// `go` itself missing means the pinned location cannot even be
		// computed. That must still reach the remedy, not die inside the
		// `go env GOPATH` call under set -e with an unrelated error.
		{name: "go unavailable to resolve the pinned location", withGo: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _, err := runStagedGoFormatter(t, formatterFixture{withGo: tt.withGo})
			if err == nil {
				t.Fatalf("formatter must fail when golangci-lint resolves nowhere, got exit 0:\n%s", out)
			}
			report := string(out)
			if strings.Contains(report, "command not found") {
				t.Fatalf("formatter must catch golangci-lint's absence itself rather than letting bash "+
					"report `command not found`, which names neither cause nor remedy:\n%s", report)
			}
			for _, want := range []string{"golangci-lint", "make install-tools", "go env GOPATH"} {
				if !strings.Contains(report, want) {
					t.Fatalf("formatter's missing-linter failure must name %q -- it has to state the cause "+
						"(the binary resolves nowhere) and BOTH remedies, installing it and putting the "+
						"install directory on PATH, because a city agent session hits this with the binary "+
						"already installed:\n%s", want, report)
				}
			}
			if !strings.Contains(report, "--no-verify") {
				t.Fatalf("formatter's missing-linter failure must warn against --no-verify: it is the "+
					"obvious next move here, and on the push side it also disarms "+
					"scripts/push-ownership-guard.sh:\n%s", report)
			}
		})
	}
}

// TestPreCommitFormatterPrefersMakefilePinnedLinter pins the resolution order
// against the Makefile's. Every other consumer of golangci-lint in this repo
// -- lint-changed, lint-full, fmt, fmt-check -- runs $(GOLANGCI_LINT), the
// pinned install, never PATH. This script resolving PATH first would let it
// format with one version while `make lint-changed`, three lines later in the
// same hook run, checks with another.
//
// The assertion is that the pinned fake's output lands in the file, not merely
// that the run exits 0: a formatter that quietly reached neither binary also
// exits 0 for every already-formatted file.
func TestPreCommitFormatterPrefersMakefilePinnedLinter(t *testing.T) {
	out, source, err := runStagedGoFormatter(t, formatterFixture{
		withGo: true,
		pinned: markerLinter("pinned"),
		onPath: markerLinter("path"),
	})
	if err != nil {
		t.Fatalf("formatter failed: %v\n%s", err, out)
	}

	content := readOptionalFile(t, source)
	if !strings.Contains(content, "// formatted by pinned") {
		t.Fatalf("formatter must run the golangci-lint the Makefile pins at $(go env GOPATH)/bin, so it "+
			"formats with the same version `make lint-changed` checks with later in the same hook run. "+
			"Formatted file:\n%s\nformatter output:\n%s", content, out)
	}
	if strings.Contains(content, "// formatted by path") {
		t.Fatalf("formatter ran the PATH golangci-lint in preference to the pinned one:\n%s", content)
	}
}

// TestPreCommitFormatterFallsBackToPATHLinter is the other half of the order:
// a contributor whose golangci-lint is installed anywhere but GOPATH/bin must
// still get formatted, not blocked.
func TestPreCommitFormatterFallsBackToPATHLinter(t *testing.T) {
	out, source, err := runStagedGoFormatter(t, formatterFixture{
		withGo: true,
		onPath: markerLinter("path"),
	})
	if err != nil {
		t.Fatalf("formatter failed: %v\n%s", err, out)
	}
	if content := readOptionalFile(t, source); !strings.Contains(content, "// formatted by path") {
		t.Fatalf("formatter must fall back to a golangci-lint on PATH when the pinned install is absent:\n%s", content)
	}
}

// TestPreCommitFormatterSeparatesLinterFailureFromAbsence is the distinction
// the bead asks for. A linter that ran and rejected a file needs a different
// message than one that could not be found: telling a contributor to run
// `make install-tools` when the tool is installed and working sends them to
// fix the wrong thing.
func TestPreCommitFormatterSeparatesLinterFailureFromAbsence(t *testing.T) {
	rejecting := `#!/usr/bin/env bash
set -euo pipefail
echo "syntax error: unexpected }" >&2
exit 1
`
	out, source, err := runStagedGoFormatter(t, formatterFixture{withGo: true, pinned: rejecting})
	if err == nil {
		t.Fatalf("formatter must fail when golangci-lint rejects a staged file, got exit 0:\n%s", out)
	}
	report := string(out)
	if !strings.Contains(report, filepath.Base(source)) {
		t.Fatalf("formatter must name the file golangci-lint rejected (%s) -- with several files staged the "+
			"linter's own output does not say which one is being reported:\n%s", filepath.Base(source), report)
	}
	if strings.Contains(report, "make install-tools") {
		t.Fatalf("formatter must not offer the install remedy when the linter ran and rejected the file -- "+
			"the tool is present and working, so that remedy sends the reader to fix the wrong thing:\n%s", report)
	}
}

// formatterFixture describes one run of scripts/precommit-format-staged-go.
// Both resolution arms are pinned explicitly, and PATH is built from scratch
// rather than prepended to the host's: a developer machine with golangci-lint
// installed would otherwise turn every absence case vacuous.
//
// One exec.Command serves every case here on purpose. A second copy is not
// only duplication: os/exec construction in test source is ratcheted by
// internal/testpolicy/resourcecensus against test/test-resources.toml, so
// adding a call site fails TestRepositoryLedgerMatchesCensusAndDocumentation
// until someone raises a checked baseline through council review.
type formatterFixture struct {
	withGo        bool   // put the real `go` on PATH, so $(go env GOPATH) resolves
	pinned        string // fake installed at $GOPATH/bin/golangci-lint, empty for none
	onPath        string // fake installed on PATH, empty for none
	sourceContent string // staged file's initial bytes, empty for an already-formatted default
}

// runStagedGoFormatter runs the formatter over a single staged Go file and
// returns its combined output and that file's path.
func runStagedGoFormatter(t *testing.T, fixture formatterFixture) ([]byte, string, error) {
	t.Helper()
	// Only what the script itself reaches for. `env` is absent on purpose:
	// the kernel runs the shebang's /usr/bin/env by absolute path, so PATH
	// only has to carry bash.
	tools := []string{"bash", "mktemp", "cmp", "rm", "cat"}
	if fixture.withGo {
		tools = append(tools, "go")
	}
	binDir := t.TempDir()
	linkRealTools(t, binDir, tools)
	if fixture.onPath != "" {
		writeExecutable(t, filepath.Join(binDir, "golangci-lint"), fixture.onPath)
	}

	gopath := t.TempDir()
	if fixture.pinned != "" {
		pinned := filepath.Join(gopath, makefilePinnedLinterDir(t), "golangci-lint")
		if err := os.MkdirAll(filepath.Dir(pinned), 0o755); err != nil {
			t.Fatalf("create pinned linter directory: %v", err)
		}
		writeExecutable(t, pinned, fixture.pinned)
	}

	source := filepath.Join(t.TempDir(), "staged.go")
	staged := fixture.sourceContent
	if staged == "" {
		staged = "package main\n"
	}
	writeTestFile(t, source, staged)

	cmd := exec.Command(filepath.Join(repoRoot(t), "scripts", "precommit-format-staged-go"))
	cmd.Dir = repoRoot(t)
	cmd.Env = []string{
		"PATH=" + binDir,
		"HOME=" + hermeticHome(t),
		"TMPDIR=" + t.TempDir(),
		"GOPATH=" + gopath,
	}
	cmd.Stdin = strings.NewReader(source + "\n")
	out, err := cmd.CombinedOutput()
	return out, source, err
}

// hermeticHome returns a throwaway HOME for a child `go` invocation. It is
// deliberately NOT t.TempDir: the go command writes telemetry counters under
// $HOME/.config/go/telemetry and keeps writing them after `go env` has
// exited, so t.TempDir's cleanup intermittently fails the whole test with
// "unlinkat .../telemetry: directory not empty" -- RemoveAll walking a
// directory that gains a file underneath it. Removal here is best effort for
// the same reason. Setting GOTELEMETRY=off in the child's environment does
// not help: the mode is read from a file in the config directory, not the
// environment (verified against go1.26.5).
func hermeticHome(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gc-precommit-home-")
	if err != nil {
		t.Fatalf("create home for child go: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newlineAppendingLinter builds the smallest golangci-lint stand-in that
// still rewrites its input: it terminates the final line. Used where the test
// asserts on the formatted bytes themselves, so a marker comment would have
// to be written into the expectation.
func newlineAppendingLinter() string {
	return refuseUnlessFmtStdin + "cat\nprintf '\\n'\n"
}

// markerLinter builds a golangci-lint stand-in that appends an identifiable
// line, so a test can tell WHICH binary ran from the formatted file. It
// refuses any invocation but `fmt --stdin` rather than passing input through
// unexamined -- a stand-in that answers everything with success hands a pass
// to whatever the suite forgot to script.
func markerLinter(marker string) string {
	return refuseUnlessFmtStdin + "cat\nprintf '// formatted by %s\\n' " + marker + "\n"
}

// refuseUnlessFmtStdin is the prologue every golangci-lint stand-in here
// shares. It refuses any invocation but the one the formatter is supposed to
// make, rather than passing input through unexamined -- a stand-in that
// answers everything with success hands a pass to whatever the suite forgot
// to script, and the formatter losing its `fmt --stdin` arguments would then
// look exactly like a clean run.
const refuseUnlessFmtStdin = `#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ] || [ "$1" != "fmt" ] || [ "$2" != "--stdin" ]; then
  echo "unexpected golangci-lint args: $*" >&2
  exit 2
fi
`

// makefilePinnedLinterDir asserts the Makefile still installs golangci-lint
// under $(go env GOPATH)/bin and returns that GOPATH-relative directory. The
// script carries its own copy of the location -- a pre-commit hook cannot
// afford a `make` subprocess just to ask for a variable -- and this is what
// binds the two copies: moving the Makefile's install directory fails here
// instead of silently costing the hook its fallback.
func makefilePinnedLinterDir(t *testing.T) string {
	t.Helper()
	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	for _, decl := range []string{
		"BIN_DIR := $(shell go env GOPATH)/bin",
		"GOLANGCI_LINT := $(BIN_DIR)/golangci-lint",
	} {
		if !strings.Contains(string(makefile), decl) {
			t.Fatalf("Makefile no longer declares %q. scripts/precommit-format-staged-go resolves the same "+
				"pinned binary by that declaration, so teach the script the new location before moving it "+
				"here (bead ci-f6u4)", decl)
		}
	}
	return "bin"
}

func TestTestFastParallelUsesSanitizedEnvironmentAndMachineAwareConcurrency(t *testing.T) {
	repoRoot := repoRoot(t)
	baseEnv := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "LOCAL_TEST_JOBS=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_CPUS=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_MEMORY_KIB=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_MEMINFO=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_PROC_CGROUP=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_CGROUP_ROOT=") ||
			strings.HasPrefix(entry, "GC_PUSH_GATE_NO_CAP=") ||
			strings.HasPrefix(entry, "PUSH_GATE_MAX_CONCURRENT=") ||
			strings.HasPrefix(entry, "PUSH_GATE_MAX_WAIT_SECONDS=") ||
			strings.HasPrefix(entry, "PUSH_GATE_POLL_SECONDS=") ||
			strings.HasPrefix(entry, "PUSH_GATE_UNRELATED_SENTINEL=") ||
			strings.HasPrefix(entry, "GC_TEST_LOCAL_LOADAVG=") {
			continue
		}
		baseEnv = append(baseEnv, entry)
	}
	tests := []struct {
		name      string
		cpus      string
		memoryKiB string
		makeArgs  []string
		wantJobs  string
		cgroup    string
		limit     string
		current   string
	}{
		{name: "large host uses automatic ceiling", cpus: "192", memoryKiB: "536870912", wantJobs: "16"},
		{name: "memory constrains fanout", cpus: "16", memoryKiB: "12582912", wantJobs: "3"},
		{name: "cpu constrains fanout", cpus: "2", memoryKiB: "67108864", wantJobs: "2"},
		{name: "small machine still runs one job", cpus: "8", memoryKiB: "2097152", wantJobs: "1"},
		{name: "unknown memory preserves safe fallback", cpus: "64", memoryKiB: "0", wantJobs: "3"},
		{name: "nested cgroup v2 ancestor constrains fanout", cpus: "16", wantJobs: "3", cgroup: "v2", limit: "12884901888", current: "0"},
		{name: "nested cgroup v1 ancestor constrains fanout", cpus: "16", wantJobs: "2", cgroup: "v1", limit: "8589934592", current: "0"},
		{name: "hybrid cgroup falls through to v1 memory controller", cpus: "16", wantJobs: "3", cgroup: "hybrid", limit: "12884901888", current: "0"},
		{name: "exhausted cgroup forces one job", cpus: "16", wantJobs: "1", cgroup: "v2", limit: "4294967296", current: "4294967296"},
		{name: "explicit override wins", cpus: "192", memoryKiB: "536870912", makeArgs: []string{"LOCAL_TEST_JOBS=7"}, wantJobs: "7"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"-n"}, tt.makeArgs...)
			args = append(args, "test-fast-parallel")
			cmd := exec.Command("make", args...)
			cmd.Dir = repoRoot
			// This table exercises the cpu/memory/cgroup axes only; pin loadavg=0
			// so a live host's real /proc/loadavg can't shrink the expected job
			// count out from under an unrelated case (ga-04m84s).
			cmd.Env = append(append([]string(nil), baseEnv...),
				"GC_TEST_LOCAL_CPUS="+tt.cpus,
				"GC_TEST_LOCAL_LOADAVG=0",
				"GC_PUSH_GATE_NO_CAP=1",
				"PUSH_GATE_MAX_CONCURRENT=7",
				"PUSH_GATE_MAX_WAIT_SECONDS=13",
				"PUSH_GATE_POLL_SECONDS=2",
				"PUSH_GATE_UNRELATED_SENTINEL=must-not-leak",
			)
			if tt.memoryKiB != "" {
				cmd.Env = append(cmd.Env, "GC_TEST_LOCAL_MEMORY_KIB="+tt.memoryKiB)
			}
			if tt.cgroup != "" {
				cmd.Env = append(cmd.Env, localTestCgroupEnv(t, tt.cgroup, tt.limit, tt.current)...)
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make -n test-fast-parallel failed: %v\n%s", err, out)
			}
			command := string(out)
			if !strings.Contains(command, "env -i") {
				t.Fatalf("test-fast-parallel recipe should use TEST_ENV env -i wrapper:\n%s", command)
			}
			if !strings.Contains(command, "./scripts/test-local-parallel fast") {
				t.Fatalf("test-fast-parallel recipe should still dispatch the sharded fast runner:\n%s", command)
			}
			wantJobAssignment := " LOCAL_TEST_JOBS=" + tt.wantJobs + " CMD_GC_PROCESS_TOTAL="
			if !strings.Contains(command, wantJobAssignment) {
				t.Fatalf("test-fast-parallel job count should be %s:\n%s", tt.wantJobs, command)
			}
			for _, key := range []string{
				"GC_PUSH_GATE_NO_CAP",
				"PUSH_GATE_MAX_CONCURRENT",
				"PUSH_GATE_MAX_WAIT_SECONDS",
				"PUSH_GATE_POLL_SECONDS",
			} {
				wantForwarding := key + `="${` + key + `-}"`
				if !strings.Contains(command, wantForwarding) {
					t.Fatalf("test-fast-parallel should forward %s through TEST_ENV:\n%s", key, command)
				}
			}
			if strings.Contains(command, "PUSH_GATE_UNRELATED_SENTINEL") {
				t.Fatalf("test-fast-parallel must keep unrelated ambient variables out of TEST_ENV:\n%s", command)
			}
		})
	}
}

func localTestCgroupEnv(t *testing.T, version, limit, current string) []string {
	t.Helper()
	root := t.TempDir()
	cgroupRoot := filepath.Join(root, "cgroup")
	procCgroup := filepath.Join(root, "proc-self-cgroup")
	meminfo := filepath.Join(root, "meminfo")
	writeTestFile(t, meminfo, "MemAvailable: 67108864 kB\n")

	var controllerRoot, procLine, limitFile, currentFile string
	switch version {
	case "v2":
		controllerRoot = cgroupRoot
		procLine = "0::/parent/child\n"
		limitFile = "memory.max"
		currentFile = "memory.current"
	case "v1":
		controllerRoot = filepath.Join(cgroupRoot, "memory")
		procLine = "5:memory:/parent/child\n"
		limitFile = "memory.limit_in_bytes"
		currentFile = "memory.usage_in_bytes"
	case "hybrid":
		controllerRoot = filepath.Join(cgroupRoot, "memory")
		procLine = "0::/unified/child\n5:memory:/parent/child\n"
		limitFile = "memory.limit_in_bytes"
		currentFile = "memory.usage_in_bytes"
	default:
		t.Fatalf("unsupported cgroup fixture version %q", version)
	}

	writeTestFile(t, procCgroup, procLine)
	if err := os.MkdirAll(filepath.Join(controllerRoot, "parent", "child"), 0o755); err != nil {
		t.Fatalf("create nested cgroup fixture: %v", err)
	}
	writeTestFile(t, filepath.Join(controllerRoot, "parent", limitFile), limit+"\n")
	writeTestFile(t, filepath.Join(controllerRoot, "parent", currentFile), current+"\n")

	return []string{
		"GC_TEST_LOCAL_MEMINFO=" + meminfo,
		"GC_TEST_LOCAL_PROC_CGROUP=" + procCgroup,
		"GC_TEST_LOCAL_CGROUP_ROOT=" + cgroupRoot,
	}
}

func TestPrePushUsesCanonicalMachineAwareConcurrency(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, ".githooks", "pre-push"))
	if err != nil {
		t.Fatalf("read pre-push hook: %v", err)
	}
	content := string(script)
	if strings.Contains(content, `LOCAL_TEST_JOBS="${LOCAL_TEST_JOBS:-3}"`) {
		t.Fatal("pre-push hook must not replace the canonical machine-aware default with a fixed three-job cap")
	}
	if !strings.Contains(content, "exec make test-fast-parallel") {
		t.Fatal("pre-push hook must continue delegating the unchanged fast-suite inventory to make test-fast-parallel")
	}
	for _, path := range []string{"Makefile", filepath.Join("scripts", "test-local-parallel")} {
		content, err := os.ReadFile(filepath.Join(repoRoot, path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(content), "scripts/test-local-job-count") {
			t.Fatalf("%s must use the canonical machine-aware job detector", path)
		}
	}
}

func TestPreCommitRegeneratesDashboardClientOnSpecChange(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, ".githooks", "pre-commit"))
	if err != nil {
		t.Fatalf("read pre-commit hook: %v", err)
	}
	content := string(script)

	npmBlockStart := strings.Index(content, "if command -v npm")
	if npmBlockStart < 0 {
		t.Fatal("pre-commit hook must guard dashboard regeneration on npm availability")
	}
	npmBlock := content[npmBlockStart:]

	genClientIdx := strings.Index(npmBlock, "npm run generate:client")
	if genClientIdx < 0 {
		t.Fatal("pre-commit hook must run 'npm run generate:client' when internal/api/openapi.json changes — " +
			"make dashboard-check only builds and typechecks against whatever client is already on disk, it never " +
			"regenerates it (that's make dashboard-ci's job, which the hook never calls). A spec-only commit " +
			"currently ships a stale generated TS client (see PR #4627, #4607)")
	}

	dashboardCheckIdx := strings.Index(npmBlock, "make dashboard-check")
	if dashboardCheckIdx < 0 {
		t.Fatal("pre-commit hook must still run make dashboard-check dashboard-smoke")
	}
	if genClientIdx > dashboardCheckIdx {
		t.Fatal("pre-commit hook must regenerate the dashboard client BEFORE typecheck/build, so a client that " +
			"doesn't match the new spec fails typecheck immediately instead of silently building against stale types")
	}

	clientAddNeedle := "git add internal/api/dashboardspa/web/shared/src/generated/gc-supervisor-client"
	genClientAddIdx := strings.Index(npmBlock, clientAddNeedle)
	if genClientAddIdx < 0 {
		t.Fatal("pre-commit hook must stage the regenerated dashboard client so a spec-only commit includes it")
	}
	if genClientAddIdx < genClientIdx {
		t.Fatal("pre-commit hook must stage the generated client after regenerating it, not before")
	}

	if strings.Contains(content, "regenerate the TS types, typecheck, and rebuild") {
		t.Fatal("pre-commit hook's dashboard block comment must not claim it regenerates the TS types unless it " +
			"actually calls npm run generate:client")
	}

	if !strings.Contains(content, `echo "warning: npm not on PATH`) {
		t.Fatal("pre-commit hook must still warn and no-op cleanly when npm is not on PATH")
	}
}

func TestPreCommitReachesDashboardBlockWhenOnlySpecFileStaged(t *testing.T) {
	// The stubbed make stands in for the real dashboard-check/dashboard-smoke
	// targets, which need the full repo: this test verifies the control flow
	// reaches the dashboard block at all (the reviewer's criterion-2 gap).
	tmpRepo := newPreCommitFixtureRepo(t, map[string]string{
		"internal/api/openapi.json": "{}\n",
		"internal/api/dashboardspa/web/shared/src/generated/gc-supervisor-client": "placeholder\n",
		"internal/api/dashboardspa/dist/placeholder":                              "placeholder\n",
	})

	// Stage ONLY a change to openapi.json -- no .go, web-src, or doc files
	// are staged, matching the reviewer's criterion-2 repro scenario.
	writeTestFile(t, filepath.Join(tmpRepo, "internal", "api", "openapi.json"), `{"changed":true}`+"\n")
	runFixtureGit(t, tmpRepo, "add", "internal/api/openapi.json")

	stubs := newPreCommitToolStubs(t)
	out, err := runPreCommitHook(t, tmpRepo, stubs.path)
	if err != nil {
		t.Fatalf("pre-commit hook failed: %v\n%s", err, out)
	}

	invocations := stubs.npmInvocations(t)
	if !strings.Contains(invocations, "generate:client") {
		t.Fatalf("pre-commit hook must run 'npm run generate:client' when only internal/api/openapi.json is "+
			"staged -- the go/web/docs early guard must not skip a spec-only commit. npm invocations:\n%s\n"+
			"hook output:\n%s", invocations, out)
	}
}

func TestPreCommitFailsClosedWhenSpecStagedButNpmAbsent(t *testing.T) {
	tmpRepo := newPreCommitFixtureRepo(t, map[string]string{
		"internal/api/openapi.json": "{}\n",
	})

	// Stage ONLY a change to openapi.json -- same repro shape as
	// TestPreCommitReachesDashboardBlockWhenOnlySpecFileStaged, but this
	// time npm itself is unreachable on PATH.
	writeTestFile(t, filepath.Join(tmpRepo, "internal", "api", "openapi.json"), `{"changed":true}`+"\n")
	runFixtureGit(t, tmpRepo, "add", "internal/api/openapi.json")

	out, err := runPreCommitHook(t, tmpRepo, restrictedPathWithoutNpm(t, nil))
	if err == nil {
		t.Fatalf("pre-commit hook must fail when internal/api/openapi.json is staged and npm is not on PATH "+
			"-- the generated TS client can't be regenerated, so the commit would silently ship a stale "+
			"client with no enforcement until CI runs. Hook exited 0, output:\n%s", out)
	}
	if !strings.Contains(string(out), "npm ci") || !strings.Contains(string(out), "generate:client") {
		t.Fatalf("pre-commit hook's npm-absent+spec-staged failure must name the exact recovery command "+
			"(cd internal/api/dashboardspa/web && npm ci && npm run generate:client), got:\n%s", out)
	}
}

func TestPreCommitFailsClosedWhenGoBlockStagesSpecAsSideEffectAndNpmAbsent(t *testing.T) {
	// Every path the Go block unconditionally `git add`s after each
	// generation step must already exist on disk, or that `git add` fails
	// closed under `set -euo pipefail` before the hook ever reaches the
	// npm-absent branch this test targets.
	const formatStagedGo = "scripts/precommit-format-staged-go"
	tmpRepo := newPreCommitFixtureRepo(t, map[string]string{
		"main.go":                                "package main\n\nfunc main() {}\n",
		formatStagedGo:                           "#!/usr/bin/env bash\nexit 0\n",
		"internal/api/openapi.json":              "{}\n",
		"docs/reference/schema/openapi.json":     "{}\n",
		"docs/reference/schema/openapi.txt":      "{}\n",
		"internal/api/genclient/client_gen.go":   "{}\n",
		"docs/reference/schema/city-schema.json": "{}\n",
		"docs/reference/schema/city-schema.txt":  "{}\n",
		"docs/reference/config.md":               "{}\n",
		"docs/reference/cli.md":                  "{}\n",
	}, formatStagedGo)

	// Stage ONLY a .go file -- internal/api/openapi.json is untouched by the
	// user's own `git add`. The hook's own Go block (staged_go_files branch)
	// regenerates and stages openapi.json as a SIDE EFFECT via
	// `go run ./cmd/genspec`, which is exactly the #4627/#4607 staleness
	// trap the npm-present branch re-reads for (fresh client_src_changed) but
	// which the npm-absent fail-closed branch used to miss (ga-jg89a5): it
	// checked a snapshot taken before the hook ran at all, so it never saw
	// the spec this commit was actually about to ship.
	writeTestFile(t, filepath.Join(tmpRepo, "main.go"), "package main\n\nfunc main() { println(1) }\n")
	runFixtureGit(t, tmpRepo, "add", "main.go")

	goStub := `#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = "run" ] && [ "$2" = "./cmd/genspec" ]; then
  printf '{"changed":true}\n' > internal/api/openapi.json
fi
exit 0
`

	out, err := runPreCommitHook(t, tmpRepo, restrictedPathWithoutNpm(t, map[string]string{
		"make": "#!/usr/bin/env bash\nexit 0\n",
		// Stands in for format/lint/genspec/genclient/genschema/vet.
		// Only `run ./cmd/genspec` has an observable side effect
		// (rewriting internal/api/openapi.json, which the hook's own
		// `git add` then stages), matching what the real cmd/genspec
		// does against a live Huma API -- the rest of the Go block is
		// exercised for control-flow only.
		"go": goStub,
	}))
	if err == nil {
		t.Fatalf("pre-commit hook must fail when its own Go block stages internal/api/openapi.json as a side "+
			"effect (go run ./cmd/genspec, triggered by staging a .go file) and npm is not on PATH -- the "+
			"generated TS client can't be regenerated, so the commit would silently ship a stale client with "+
			"no enforcement until CI runs. Hook exited 0, output:\n%s", out)
	}
	if !strings.Contains(string(out), "npm ci") || !strings.Contains(string(out), "generate:client") {
		t.Fatalf("pre-commit hook's npm-absent+spec-staged-as-side-effect failure must name the exact "+
			"recovery command (cd internal/api/dashboardspa/web && npm ci && npm run generate:client), got:\n%s", out)
	}
}

func TestPreCommitWarnsOnlyWhenNpmAbsentAndSpecNotStaged(t *testing.T) {
	tmpRepo := newPreCommitFixtureRepo(t, map[string]string{
		"README.md": "hello\n",
	})

	// Stage a docs-only change -- internal/api/openapi.json is untouched,
	// so npm's absence must stay a warning, not a hard failure. staged_docs
	// being non-empty also exercises `make check-docs`, so stub `make` as a
	// no-op; the fixture repo has none of the real doc-lint machinery.
	writeTestFile(t, filepath.Join(tmpRepo, "README.md"), "hello again\n")
	runFixtureGit(t, tmpRepo, "add", "README.md")

	out, err := runPreCommitHook(t, tmpRepo, restrictedPathWithoutNpm(t, map[string]string{
		"make": "#!/usr/bin/env bash\nexit 0\n",
	}))
	if err != nil {
		t.Fatalf("pre-commit hook must still succeed (warn-only) when npm is absent and "+
			"internal/api/openapi.json is NOT staged -- contributors without Node tooling must not be "+
			"blocked on unrelated commits, got exit error: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "npm not on PATH") {
		t.Fatalf("pre-commit hook should still warn when npm is absent, got:\n%s", out)
	}
}

// spaSourceRoot is the SPA workspace tree. Everything tracked under it is
// SPA source or SPA config -- no prose, no fixture data -- which is what
// makes "every tracked file" the correct trigger set for the dashboard gate
// rather than a list somebody has to remember to extend.
const spaSourceRoot = "internal/api/dashboardspa/web"

// TestPreCommitSPAGateCoversEverySPASourceFile pins the dashboard trigger as
// a coverage rule instead of a file list. .githooks/pre-commit decides
// whether to run `make dashboard-check dashboard-smoke` from the pathspecs it
// hands `git diff --cached`, and while those pathspecs enumerated individual
// files the enumeration drifted behind the tree it described: at the time this
// test was written it missed eslint.config.mjs, prettier.config.mjs,
// .prettierignore and openapi-ts.config.ts, so the files that CONFIGURE lint
// and format were exactly the ones whose changes got no local lint or format
// check -- plus package-lock.json, the tailwind/postcss/vitest/playwright
// configs, and the whole frontend/e2e tree (bead ci-padp, follow-up to
// ci-dxot).
//
// The assertion is set coverage computed from `git ls-files`, deliberately
// NOT a list of the twelve names above: a test that enumerates the misses can
// only catch the misses somebody already found, which is the same failure as
// the list it is guarding. A new SPA config file is covered the moment it is
// tracked.
func TestPreCommitSPAGateCoversEverySPASourceFile(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, ".githooks", "pre-commit"))
	if err != nil {
		t.Fatalf("read pre-commit hook: %v", err)
	}

	tracked := gitLsFiles(t, repoRoot, spaSourceRoot)
	if len(tracked) == 0 {
		t.Fatalf("no tracked files under %s -- the coverage comparison below would pass "+
			"vacuously, so the fixture is wrong, not the hook", spaSourceRoot)
	}

	covered := gitLsFiles(t, repoRoot, spaTriggerPathspecs(t, string(script))...)
	var missing []string
	for path := range tracked {
		if !covered[path] {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if len(missing) > 12 {
			missing = append(missing[:12], fmt.Sprintf("... and %d more", len(missing)-12))
		}
		t.Fatalf("pre-commit hook's staged_web_src pathspecs miss %d of %d tracked files under %s, so a "+
			"commit touching only those skips `make dashboard-check dashboard-smoke` entirely and gets no "+
			"local lint, format, typecheck or build check (bead ci-padp). Match the whole tree instead of "+
			"enumerating it. Missing:\n  %s",
			len(missing), len(tracked), spaSourceRoot, strings.Join(missing, "\n  "))
	}
}

// TestPreCommitRunsDashboardGateWhenOnlySPAConfigStaged is the behavioral half
// of the coverage test above: it proves a commit staging nothing but an SPA
// lint config actually reaches `make dashboard-check dashboard-smoke`, rather
// than exiting at the hook's go/web/docs/spec early guard.
//
// It also asserts the widened SPA trigger did NOT widen client regeneration:
// eslint.config.mjs is not an input to the generated TS client, so npm must
// stay untouched here.
func TestPreCommitRunsDashboardGateWhenOnlySPAConfigStaged(t *testing.T) {
	lintConfig := spaSourceRoot + "/eslint.config.mjs"
	repo := newPreCommitFixtureRepo(t, map[string]string{
		lintConfig: "export default [];\n",
		// The dashboard branch `git add`s dist unconditionally after make,
		// and that add fails closed under `set -euo pipefail` on a missing
		// path -- which would mask the control-flow question being asked.
		"internal/api/dashboardspa/dist/placeholder": "placeholder\n",
	})
	writeTestFile(t, filepath.Join(repo, filepath.FromSlash(lintConfig)), "export default [{}];\n")
	runFixtureGit(t, repo, "add", lintConfig)

	stubs := newPreCommitToolStubs(t)
	out, err := runPreCommitHook(t, repo, stubs.path)
	if err != nil {
		t.Fatalf("pre-commit hook failed: %v\n%s", err, out)
	}

	if !strings.Contains(stubs.makeInvocations(t), "dashboard-check dashboard-smoke") {
		t.Fatalf("pre-commit hook must run `make dashboard-check dashboard-smoke` when %s is staged -- the "+
			"file that configures eslint is exactly the one whose change gets no local lint check otherwise "+
			"(bead ci-padp). make invocations:\n%s\nhook output:\n%s",
			lintConfig, stubs.makeInvocations(t), out)
	}
	if strings.Contains(stubs.npmInvocations(t), "generate:client") {
		t.Fatalf("pre-commit hook must not regenerate the TS client for an SPA config change that is not one "+
			"of its inputs, got npm invocations:\n%s", stubs.npmInvocations(t))
	}
}

// TestPreCommitRegeneratesClientForEveryDeclaredClientSource pins the hook's
// regeneration trigger to the SAME input set dashboard-ci declares to
// check-artifact-drift.sh for the generated TS client. Keying regeneration on
// internal/api/openapi.json alone left a commit that retuned
// openapi-ts.config.ts, or bumped the generator in package-lock.json, shipping
// a client built by the OLD config -- caught nowhere until CI's drift gate
// (bead ci-padp).
//
// The expected set is read out of the Makefile rather than restated here, so
// declaring a fourth --source without teaching the hook about it fails this
// test instead of quietly reopening the hole.
func TestPreCommitRegeneratesClientForEveryDeclaredClientSource(t *testing.T) {
	for _, source := range dashboardClientSources(t) {
		t.Run(source, func(t *testing.T) {
			repo := newClientSourceFixtureRepo(t, source)
			stubs := newPreCommitToolStubs(t)
			out, err := runPreCommitHook(t, repo, stubs.path)
			if err != nil {
				t.Fatalf("pre-commit hook failed: %v\n%s", err, out)
			}
			if !strings.Contains(stubs.npmInvocations(t), "generate:client") {
				t.Fatalf("pre-commit hook must run `npm run generate:client` when %s is staged -- "+
					"dashboard-ci declares it a --source of the generated client, so a commit that "+
					"changes it and not the spec ships a client built by the old inputs. npm "+
					"invocations:\n%s\nhook output:\n%s", source, stubs.npmInvocations(t), out)
			}
		})
	}
}

// TestPreCommitFailsClosedWhenAnyClientSourceStagedAndNpmAbsent extends the
// npm-absent fail-closed branch to the same declared input set. The two
// branches must not diverge on which inputs count as "the client is about to
// go stale" -- diverging is what ga-jg89a5 was, one branch trusting a
// different snapshot than the other.
func TestPreCommitFailsClosedWhenAnyClientSourceStagedAndNpmAbsent(t *testing.T) {
	for _, source := range dashboardClientSources(t) {
		t.Run(source, func(t *testing.T) {
			repo := newClientSourceFixtureRepo(t, source)
			out, err := runPreCommitHook(t, repo, restrictedPathWithoutNpm(t, nil))
			if err == nil {
				t.Fatalf("pre-commit hook must fail when %s is staged and npm is not on PATH -- the "+
					"generated TS client can't be regenerated, so the commit would silently ship a "+
					"stale client with no enforcement until CI runs. Hook exited 0, output:\n%s",
					source, out)
			}
			if !strings.Contains(string(out), "npm ci") || !strings.Contains(string(out), "generate:client") {
				t.Fatalf("pre-commit hook's npm-absent failure must name the exact recovery command "+
					"(cd internal/api/dashboardspa/web && npm ci && npm run generate:client), got:\n%s", out)
			}
			if !strings.Contains(string(out), source) {
				t.Fatalf("pre-commit hook's npm-absent failure must name the staged input that triggered "+
					"it (%s), not a fixed path -- otherwise the message sends the reader to a file they "+
					"never touched. Got:\n%s", source, out)
			}
		})
	}
}

// dashboardClientSources reads the --source paths dashboard-ci hands
// check-artifact-drift.sh for the generated TS client. That recipe is the
// declaration of what the client is built from; the pre-commit hook is a
// consumer of the same fact and must not carry its own copy.
func dashboardClientSources(t *testing.T) []string {
	t.Helper()
	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	content := string(makefile)

	const clientArtifact = "--artifact " + spaSourceRoot + "/shared/src/generated/gc-supervisor-client"
	artifactIdx := strings.Index(content, clientArtifact)
	if artifactIdx < 0 {
		t.Fatalf("Makefile must still judge the generated dashboard client through "+
			"check-artifact-drift.sh (%q not found)", clientArtifact)
	}
	// Bounded at --regen so the NEXT check-artifact-drift.sh call in the same
	// recipe -- the embedded bundle, whose --source is the whole web tree --
	// cannot leak its paths into this list.
	invocation := content[artifactIdx:]
	if end := strings.Index(invocation, "--regen"); end >= 0 {
		invocation = invocation[:end]
	}

	// Recipe continuations survive strings.Fields as bare `\` tokens; they
	// always precede a flag, never follow one, so the value after --source is
	// the path. The guard below catches it if that ever stops being true.
	fields := strings.Fields(invocation)
	var sources []string
	for i, field := range fields {
		if field != "--source" {
			continue
		}
		if i+1 >= len(fields) || strings.HasPrefix(fields[i+1], "-") || fields[i+1] == `\` {
			t.Fatalf("dangling --source in dashboard-ci's client drift check:\n%s", invocation)
		}
		sources = append(sources, fields[i+1])
	}
	if len(sources) == 0 {
		t.Fatalf("could not parse --source paths out of dashboard-ci's client drift check:\n%s", invocation)
	}
	return sources
}

// newClientSourceFixtureRepo builds a repo whose only staged change is the
// given generated-client input. The generated client and the embedded dist
// both exist because the hook `git add`s them after regenerating and after
// make -- an add of a missing path fails closed under `set -euo pipefail`,
// which would answer a different question than the one under test.
func newClientSourceFixtureRepo(t *testing.T, source string) string {
	t.Helper()
	repo := newPreCommitFixtureRepo(t, map[string]string{
		source: "{}\n",
		spaSourceRoot + "/shared/src/generated/gc-supervisor-client/index.ts": "export {};\n",
		"internal/api/dashboardspa/dist/placeholder":                          "placeholder\n",
	})
	writeTestFile(t, filepath.Join(repo, filepath.FromSlash(source)), `{"changed":true}`+"\n")
	runFixtureGit(t, repo, "add", source)
	return repo
}

// spaTriggerPathspecs extracts the pathspecs the hook hands `git diff
// --cached` when deciding whether staged SPA sources exist.
func spaTriggerPathspecs(t *testing.T, hook string) []string {
	t.Helper()
	const assignment = "staged_web_src=$("
	start := strings.Index(hook, assignment)
	if start < 0 {
		t.Fatal("pre-commit hook must still compute staged_web_src from a git diff --cached pathspec list")
	}
	segment := hook[start+len(assignment):]
	end := strings.Index(segment, ")")
	if end < 0 {
		t.Fatal("pre-commit hook's staged_web_src command substitution is unterminated")
	}
	segment = segment[:end]
	sep := strings.Index(segment, " -- ")
	if sep < 0 {
		t.Fatalf("pre-commit hook's staged_web_src must pass its paths after a `--` separator, got: %s", segment)
	}
	segment = segment[sep+len(" -- "):]

	var specs []string
	for {
		open := strings.Index(segment, "'")
		if open < 0 {
			break
		}
		segment = segment[open+1:]
		closed := strings.Index(segment, "'")
		if closed < 0 {
			t.Fatal("pre-commit hook's staged_web_src has an unterminated quoted pathspec")
		}
		specs = append(specs, segment[:closed])
		segment = segment[closed+1:]
	}
	if len(specs) == 0 {
		t.Fatal("pre-commit hook's staged_web_src passes no pathspecs, so no SPA change can ever trigger the gate")
	}
	return specs
}

// gitLsFiles returns the tracked paths matching pathspecs, as a set. It asks
// git rather than matching pathspecs in Go so the test agrees with the matcher
// the hook itself runs under, including directory-prefix and wildcard forms.
func gitLsFiles(t *testing.T, dir string, pathspecs ...string) map[string]bool {
	t.Helper()
	cmd := exec.Command("git", append([]string{"ls-files", "-z", "--"}, pathspecs...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files %v: %v", pathspecs, err)
	}
	files := make(map[string]bool)
	for _, name := range strings.Split(string(out), "\x00") {
		if name != "" {
			files[name] = true
		}
	}
	return files
}

// newPreCommitFixtureRepo creates a throwaway git repository holding files
// (repo-relative slash paths) and commits them, so callers can then rewrite
// and stage whichever paths their case is about. Any path also named in
// executables gets the exec bit -- the hook pipes into
// scripts/precommit-format-staged-go by path, so a 0644 stand-in there fails
// the hook on permissions instead of exercising the branch under test.
func newPreCommitFixtureRepo(t *testing.T, files map[string]string, executables ...string) string {
	t.Helper()
	repo := t.TempDir()
	runFixtureGit(t, repo, "init")
	for rel, content := range files {
		writeTestFile(t, filepath.Join(repo, filepath.FromSlash(rel)), content)
	}
	for _, rel := range executables {
		if _, ok := files[rel]; !ok {
			t.Fatalf("executable %q is not one of the fixture files", rel)
		}
		if err := os.Chmod(filepath.Join(repo, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatalf("chmod fixture executable %s: %v", rel, err)
		}
	}
	runFixtureGit(t, repo, "add", "-A")
	runFixtureGit(t, repo, "commit", "-m", "init")
	return repo
}

func runFixtureGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test.invalid",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// runPreCommitHook runs the hook against a fixture repo under exactly the
// given PATH. It takes the whole PATH rather than a directory to prepend:
// prepending would leave the ambient PATH reachable behind it, which silently
// defeats restrictedPathWithoutNpm and turns a fail-closed assertion into a
// test of whatever npm the host happens to have.
func runPreCommitHook(t *testing.T, repo, path string) ([]byte, error) {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(repoRoot(t), ".githooks", "pre-commit"))
	cmd.Dir = repo
	cmd.Env = []string{
		"PATH=" + path,
		"HOME=" + t.TempDir(),
	}
	return cmd.CombinedOutput()
}

type preCommitToolStubs struct {
	path    string
	makeLog string
	npmLog  string
}

// newPreCommitToolStubs installs make and npm stand-ins that record every
// invocation and REFUSE anything the hook is not expected to reach. A stub
// that answered every call with success would hand a pass to whatever a case
// forgot to script, and the gap would be invisible from the outside.
func newPreCommitToolStubs(t *testing.T) preCommitToolStubs {
	t.Helper()
	binDir := t.TempDir()
	stubs := preCommitToolStubs{
		path:    binDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		makeLog: filepath.Join(binDir, "make.log"),
		npmLog:  filepath.Join(binDir, "npm.log"),
	}
	writeExecutable(t, filepath.Join(binDir, "make"), `#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "`+stubs.makeLog+`"
case "$*" in
"dashboard-check dashboard-smoke") exit 0 ;;
esac
echo "unscripted make target: $*" >&2
exit 2
`)
	writeExecutable(t, filepath.Join(binDir, "npm"), `#!/usr/bin/env bash
set -euo pipefail
echo "$*" >> "`+stubs.npmLog+`"
case "$*" in
"ci --silent" | "run generate:client") exit 0 ;;
esac
echo "unscripted npm invocation: $*" >&2
exit 2
`)
	return stubs
}

func (s preCommitToolStubs) makeInvocations(t *testing.T) string {
	return readOptionalFile(t, s.makeLog)
}

func (s preCommitToolStubs) npmInvocations(t *testing.T) string {
	return readOptionalFile(t, s.npmLog)
}

func readOptionalFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "(never invoked)"
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// restrictedPathWithoutNpm builds a PATH containing only symlinks to the
// real bash and git (plus any provided stub scripts), guaranteeing npm is
// unreachable regardless of what's installed on the test host -- falling
// back to the ambient PATH would make these tests flaky on any machine
// that actually has npm installed.
func restrictedPathWithoutNpm(t *testing.T, stubs map[string]string) string {
	t.Helper()
	binDir := t.TempDir()
	linkRealTools(t, binDir, []string{"bash", "git", "xargs"})
	for name, script := range stubs {
		writeExecutable(t, filepath.Join(binDir, name), script)
	}
	return binDir
}

// linkRealTools symlinks the host's own copy of each named tool into dir,
// which is how a restricted PATH stays usable without letting the rest of the
// host's PATH back in behind it.
func linkRealTools(t *testing.T, dir string, names []string) {
	t.Helper()
	for _, name := range names {
		realPath, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("resolve real %s on test host PATH: %v", name, err)
		}
		if err := os.Symlink(realPath, filepath.Join(dir, name)); err != nil {
			t.Fatalf("symlink %s: %v", name, err)
		}
	}
}

func TestNativeDoltliteBeadsTargetRunsTaggedSuite(t *testing.T) {
	repoRoot := repoRoot(t)
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := validateNativeDoltliteMakefile(string(makefile)); err != nil {
		t.Fatalf("test-native-doltlite-beads recipe: %v", err)
	}

	cmd := exec.Command("make", "-n", "test-native-doltlite-beads")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n test-native-doltlite-beads failed: %v\n%s", err, out)
	}
	command := string(out)
	if err := validateNativeDoltliteDryRun(command); err != nil {
		t.Fatalf("make -n test-native-doltlite-beads output: %v", err)
	}
	for _, want := range []string{
		"CGO_ENABLED=0",
		"-tags gascity_native_beads",
		"-run '^TestDoltlite'",
		"./internal/beads",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("test-native-doltlite-beads recipe missing %q:\n%s", want, command)
		}
	}
	for _, banned := range []string{
		"CGO_ENABLED=1",
		"cgo,gascity_native_beads",
	} {
		if strings.Contains(command, banned) {
			t.Fatalf("test-native-doltlite-beads recipe must not contain %q (doltlite store now uses pure-Go modernc):\n%s", banned, command)
		}
	}
	assertNativeDoltliteBeadsSelectionMatchesTaggedOwners(t, repoRoot)
}

func TestLocalParallelAllowlistIncludesObservableEnv(t *testing.T) {
	repoRoot := repoRoot(t)
	script, err := os.ReadFile(filepath.Join(repoRoot, "scripts", "test-local-parallel"))
	if err != nil {
		t.Fatalf("read test-local-parallel: %v", err)
	}
	content := string(script)
	for _, key := range []string{"OBSERVABLE_TEST_LOG", "OBSERVABLE_FAILURE_LINES"} {
		if !strings.Contains(content, key+"=") {
			t.Fatalf("test-local-parallel job env should pass through %s", key)
		}
	}
	for _, key := range []string{"GC_CITY", "GC_HOME", "GC_SESSION_ID"} {
		if strings.Contains(content, key+"=") {
			t.Fatalf("test-local-parallel job env must not pass through live session env %s", key)
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
