// Contract suite for scripts/check-shell-lint.sh, the repo's shell static-lint
// gate, and for the three callers that make it run: the Makefile target, the
// preflight-static CI step, and this file (which is what puts the gate in the
// `make test` sweep).
//
// Why the suite is shaped this way. The gate exists because
// scripts/check-extmsg-bridge-isolation.sh carried
// `# shellcheck disable=SC2086 -- word splitting is the point`; a directive
// with a bare `--` tail does not parse, ShellCheck then analyzes nothing else
// in the file, and that gate script was statically linted by nothing from the
// day it was written (bead gs-fn6). A plain sweep catches that much on its
// own. What it does NOT catch is a file-level `disable=SC1072,SC1073` above
// such a directive: the run then exits 0 with zero findings while the file is
// entirely unanalyzed. TestShellcheckAloneCannotSeeASuppressedParseAbort
// measures that hole against the real binary rather than asserting it from
// this comment, and TestShellLintGateRefusesSuppressedParseAborts requires
// the gate to close it.
//
// The gate's directive validator is hand-rolled, so it can drift from
// ShellCheck's own parser into accepting a form ShellCheck rejects -- which
// would be silent, because the accepted file then lints as nothing.
// TestShellLintGateAcceptsOnlyFormsShellcheckParses is the binding: every
// form the gate accepts is also run through ShellCheck and must produce no
// directive diagnostic.
//
// Fixtures are real git repositories because the gate enumerates files with
// `git ls-files`. That keeps one production code path -- there is no
// test-only enumeration branch that a passing suite could be pinning instead
// of the one CI runs.
//
// Delegated elsewhere: whether each individual script is correct (their own
// suites, e.g. scripts/test-push-gate-lock.sh), and the sweep's coverage
// beyond scripts/ and .githooks/ (stated as a bound in the gate script).
//
// Run: go test ./scripts -run ShellLint
//      go test ./scripts -run Shellcheck

package scripts_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Directive diagnostics: SC1072/SC1073 are raised when a directive does not
// parse, SC1107 when its key is unrecognized. Their presence means the file
// was not fully analyzed.
var directiveDiagnostics = []string{"SC1072", "SC1073", "SC1107"}

const shellcheckRemedy = "make install-tools"

// shellcheckBin resolves the linter the same way the gate does, and FAILS
// rather than skipping when it is absent. A skip here would let the whole
// suite go green on a host with no linter, which is the failure mode the gate
// itself refuses; the remedy is one command and it is named in the message.
func shellcheckBin(t *testing.T) string {
	t.Helper()
	// Running --version, not stat-ing the path: a PATH shim can be present and
	// refuse to execute, and the gate makes the same distinction.
	runnable := func(path string) bool {
		return path != "" && exec.Command(path, "--version").Run() == nil
	}
	if out, err := exec.Command("go", "env", "GOPATH").Output(); err == nil {
		if candidate := filepath.Join(strings.TrimSpace(string(out)), "bin", "shellcheck"); runnable(candidate) {
			return candidate
		}
	}
	if candidate, err := exec.LookPath("shellcheck"); err == nil && runnable(candidate) {
		return candidate
	}
	t.Fatalf("shellcheck is not installed or not runnable; install the pinned build with: %s", shellcheckRemedy)
	return ""
}

// runGate runs the gate over root and returns its combined output and exit code.
func runGate(t *testing.T, root string, extraEnv ...string) (string, int) {
	t.Helper()
	gate := filepath.Join(repoRoot(t), "scripts", "check-shell-lint.sh")
	cmd := exec.Command("bash", gate, root)
	cmd.Env = append(os.Environ(), extraEnv...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run gate: %v\n%s", err, out)
	}
	return string(out), exitErr.ExitCode()
}

// fixtureRepo materializes files into a fresh git repository and returns its
// path. Global and system git config are detached so a developer's own
// hooksPath or template settings cannot change what `git ls-files` reports.
func fixtureRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	for _, args := range [][]string{
		{"init", "-q", "--initial-branch=main"},
		{"add", "-A"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in fixture: %v\n%s", args, err, out)
		}
	}
	return root
}

// cleanScript is a shellcheck-clean script with nothing for the sweep to
// report, so a red verdict on a directive fixture can only have come from the
// directive.
const cleanScript = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "ok"
`

// scriptWithDirective puts the directive under test on line 2, ahead of the
// first command. A directive must attach to something: as the last line of a
// file it is SC1072 ("Expected a command") whatever its own syntax, which
// would make every fixture red for a reason that is not the one under test.
// Line 2 also makes it file-level, which is the only position a `shell=` key
// is accepted in.
func scriptWithDirective(directive string) string {
	return "#!/usr/bin/env bash\n" + directive + "\nset -euo pipefail\nprintf '%s\\n' \"ok\"\n"
}

func TestShellLintGatePassesOnThisRepo(t *testing.T) {
	shellcheckBin(t)
	out, code := runGate(t, repoRoot(t))
	if code != 0 {
		t.Fatalf("gate failed on this repository (exit %d):\n%s", code, out)
	}
}

// TestShellcheckAloneCannotSeeASuppressedParseAbort measures the hole the
// gate's directive check exists to close, against the real binary. If a
// future ShellCheck stops honoring a `disable=` of its own parse diagnostics,
// this test fails and the directive check becomes belt-and-braces rather than
// load-bearing -- which is worth knowing, so it must not be asserted from
// prose.
func TestShellcheckAloneCannotSeeASuppressedParseAbort(t *testing.T) {
	bin := shellcheckBin(t)
	dir := t.TempDir()
	// Order matters: the file-level directive has to precede the first
	// command, and the malformed one has to follow it, or ShellCheck reports
	// the parse failure anyway. Measured on 0.11.0.
	script := filepath.Join(dir, "unparsed.sh")
	body := "#!/usr/bin/env bash\n" +
		"# shellcheck disable=SC1072,SC1073\n" +
		"set -euo pipefail\n" +
		"# shellcheck disable=SC2086 -- word splitting is the point\n" +
		"run() { echo $1; }\n" +
		"run \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	out, err := exec.Command(bin, script).CombinedOutput()
	if err != nil {
		t.Fatalf("shellcheck reported the suppressed parse abort (exit %v), so the hole is closed upstream and the gate's directive check is no longer the only thing catching it:\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("shellcheck exited 0 but printed findings, which this test did not anticipate:\n%s", out)
	}
	// The unquoted $1 on the line after the malformed directive is a real
	// SC2086. Its absence is the proof that nothing in the file was analyzed.
	if strings.Contains(string(out), "SC2086") {
		t.Fatal("shellcheck still analyzed the file body")
	}
}

func TestShellLintGateRefusesSuppressedParseAborts(t *testing.T) {
	shellcheckBin(t)
	root := fixtureRepo(t, map[string]string{
		"scripts/unparsed.sh": "#!/usr/bin/env bash\n" +
			"# shellcheck disable=SC1072,SC1073\n" +
			"set -euo pipefail\n" +
			"# shellcheck disable=SC2086 -- word splitting is the point\n" +
			"run() { echo $1; }\n" +
			"run \"$@\"\n",
	})
	out, code := runGate(t, root)
	if code == 0 {
		t.Fatalf("gate accepted a file whose directives suppress the parse diagnostics:\n%s", out)
	}
	for _, want := range []string{"SC1072", "unparseable shellcheck directive token"} {
		if !strings.Contains(out, want) {
			t.Errorf("gate output should name %q so the reader learns the file is unanalyzed, not merely suppressed:\n%s", want, out)
		}
	}
}

// TestShellLintGateRefusesSuppressingParseDiagnostics isolates the refusal
// that closes the hole TestShellcheckAloneCannotSeeASuppressedParseAbort
// measures. The fixture carries the suppression and NOTHING else wrong,
// because the guard's job is to refuse the license, not to wait for someone to
// use it: the next malformed directive added to such a file is silent.
//
// Isolating it was not optional. With the combined fixture as the only
// coverage, emptying the unsuppressable-code list left the whole suite green
// -- the token-grammar check was refusing that fixture first, so the guard
// under test never ran. Found by mutation.
func TestShellLintGateRefusesSuppressingParseDiagnostics(t *testing.T) {
	shellcheckBin(t)
	for _, directive := range []string{
		"# shellcheck disable=SC1072",
		"# shellcheck disable=SC1073",
		"# shellcheck disable=SC1072,SC1073",
		"# shellcheck disable=SC2086,SC1073 # reason",
		"# shellcheck disable=SC1107",
	} {
		t.Run(directive, func(t *testing.T) {
			root := fixtureRepo(t, map[string]string{
				"scripts/subject.sh": scriptWithDirective(directive),
			})
			out, code := runGate(t, root)
			if code == 0 {
				t.Fatalf("gate accepted %q, which licenses a silent unanalyzed file:\n%s", directive, out)
			}
			if !strings.Contains(out, "disables a diagnostic that reports an unparseable directive") {
				t.Errorf("refusal came from some other check, so this guard is still unpinned:\n%s", out)
			}
		})
	}
}

// SC1090 and SC1091 report a source ShellCheck could not follow. They say
// nothing about whether the file parsed and are legitimately disabled in this
// repo, so the guard above must NOT refuse them -- otherwise closing the hole
// would break .githooks/pre-push and scripts/rebase-resolve-lib.sh.
func TestShellLintGateStillAllowsDisablingSourceFollowing(t *testing.T) {
	shellcheckBin(t)
	for _, directive := range []string{
		"# shellcheck disable=SC1091",
		"# shellcheck source=./lib.sh disable=SC1090,SC1091",
	} {
		t.Run(directive, func(t *testing.T) {
			root := fixtureRepo(t, map[string]string{
				"scripts/subject.sh": scriptWithDirective(directive),
			})
			out, code := runGate(t, root)
			if code != 0 {
				t.Fatalf("gate refused %q, which every sourcing script in this repo needs (exit %d):\n%s", directive, code, out)
			}
		})
	}
}

func TestShellLintGateRefusesDisableAll(t *testing.T) {
	shellcheckBin(t)
	root := fixtureRepo(t, map[string]string{
		"scripts/wide.sh": "#!/usr/bin/env bash\n" +
			"# shellcheck disable=all\n" +
			"set -euo pipefail\n" +
			"printf '%s\\n' \"ok\"\n",
	})
	out, code := runGate(t, root)
	if code == 0 {
		t.Fatalf("gate accepted disable=all, which suppresses SC1072/SC1073 too:\n%s", out)
	}
	if !strings.Contains(out, "disable=all") {
		t.Errorf("gate output should name the offending directive:\n%s", out)
	}
}

// malformedDirectives are the forms the gate must refuse. Each is the whole
// comment line, appended to a clean script.
var malformedDirectives = map[string]string{
	// The incident: a reason written after `--` instead of after `#`.
	"prose_after_double_dash": "# shellcheck disable=SC2086 -- word splitting is the point",
	"spaces_around_equals":    "# shellcheck disable = SC2086",
	"no_key_value_pair":       "# shellcheck",
	"reason_without_marker":   "# shellcheck disable=SC2086 word splitting is the point",
	"trailing_comma_in_list":  "# shellcheck disable=SC2086,",
	"key_with_colon":          "# shellcheck: disable=SC2086",
	// Prose that happens to start with the word: ShellCheck reads any comment
	// whose first word is that literal as a directive. This one bit the gate's
	// own installer script while it was being written.
	"prose_starting_with_word": "# shellcheck is provided by make install-tools",
}

// legalDirectives are the forms the gate must accept. Every one is also
// checked against ShellCheck itself by
// TestShellLintGateAcceptsOnlyFormsShellcheckParses.
var legalDirectives = map[string]string{
	"bare_disable":          "# shellcheck disable=SC2086",
	"disable_list":          "# shellcheck disable=SC2086,SC2181",
	"reason_after_hash":     "# shellcheck disable=SC2086 # word splitting is the point",
	"reason_after_two_hash": "# shellcheck disable=SC2086  # word splitting is the point",
	"source_and_disable":    "# shellcheck source=./lib.sh disable=SC1091",
	"shell_key":             "# shellcheck shell=bash",
	"indented":              "  # shellcheck disable=SC2086",
	"no_space_after_marker": "#shellcheck disable=SC2086",
	// Not a directive at all: the literal is not the comment's first word, so
	// ShellCheck ignores it and so must the gate.
	"literal_mid_sentence": "# see shellcheck disable=SC2086 for why",
}

func TestShellLintGateRefusesMalformedDirectives(t *testing.T) {
	shellcheckBin(t)
	for name, directive := range malformedDirectives {
		t.Run(name, func(t *testing.T) {
			root := fixtureRepo(t, map[string]string{
				"scripts/subject.sh": scriptWithDirective(directive),
			})
			out, code := runGate(t, root)
			if code == 0 {
				t.Fatalf("gate accepted %q:\n%s", directive, out)
			}
		})
	}
}

func TestShellLintGateAcceptsLegalDirectives(t *testing.T) {
	shellcheckBin(t)
	for name, directive := range legalDirectives {
		t.Run(name, func(t *testing.T) {
			root := fixtureRepo(t, map[string]string{
				"scripts/subject.sh": scriptWithDirective(directive),
			})
			out, code := runGate(t, root)
			if code != 0 {
				t.Fatalf("gate refused the legal directive %q (exit %d):\n%s", directive, code, out)
			}
		})
	}
}

// TestShellLintGateAcceptsOnlyFormsShellcheckParses binds the gate's
// hand-rolled validator to the real parser. A form the gate accepts and
// ShellCheck cannot parse is the worst case: the file lints as nothing and
// both halves of the gate report success.
func TestShellLintGateAcceptsOnlyFormsShellcheckParses(t *testing.T) {
	bin := shellcheckBin(t)
	dir := t.TempDir()
	for name, directive := range legalDirectives {
		t.Run(name, func(t *testing.T) {
			script := filepath.Join(dir, name+".sh")
			if err := os.WriteFile(script, []byte(scriptWithDirective(directive)), 0o755); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			out, _ := exec.Command(bin, "-f", "gcc", script).CombinedOutput()
			for _, code := range directiveDiagnostics {
				if strings.Contains(string(out), code) {
					t.Fatalf("the gate accepts %q but shellcheck reports %s on it, so a file carrying it is analyzed by nothing:\n%s", directive, code, out)
				}
			}
		})
	}
}

// TestShellLintGateRefusesOrdinaryFindings proves the sweep half runs. Without
// it every red verdict in this suite could be coming from the directive check
// alone and the sweep could be stubbed out with the suite still green.
func TestShellLintGateRefusesOrdinaryFindings(t *testing.T) {
	shellcheckBin(t)
	root := fixtureRepo(t, map[string]string{
		"scripts/unquoted.sh": "#!/usr/bin/env bash\nset -euo pipefail\nrun() { echo $1; }\nrun \"$@\"\n",
	})
	out, code := runGate(t, root)
	if code == 0 {
		t.Fatalf("gate accepted an unquoted expansion, so the shellcheck sweep is not running:\n%s", out)
	}
	if !strings.Contains(out, "SC2086") {
		t.Errorf("gate output should carry the shellcheck finding:\n%s", out)
	}
}

// TestShellLintGateSweepsExtensionlessScripts pins the file selector's shebang
// arm. Twelve of the runners under scripts/ and both git hooks carry no .sh
// suffix, so an extension-only selector would sweep none of them and this
// suite would still be green.
func TestShellLintGateSweepsExtensionlessScripts(t *testing.T) {
	shellcheckBin(t)
	for name, body := range map[string]string{
		"scripts/runner":     "#!/usr/bin/env bash\nrun() { echo $1; }\nrun \"$@\"\n",
		".githooks/pre-push": "#!/bin/sh -e\nrun() { echo $1; }\nrun \"$@\"\n",
	} {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			root := fixtureRepo(t, map[string]string{name: body})
			out, code := runGate(t, root)
			if code == 0 {
				t.Fatalf("gate did not sweep %s, which carries a shebang but no .sh suffix:\n%s", name, out)
			}
			// A non-zero exit alone proves nothing here: with the selector's
			// shebang arm removed the sweep finds no targets at all and
			// reports THAT, which is also non-zero. Asserting on the finding
			// is what separates "swept and found the defect" from "swept
			// nothing". Found by mutation -- the exit-code-only form was a
			// survivor.
			if !strings.Contains(out, "SC2086") {
				t.Errorf("gate exited non-zero without reporting the file's finding, so %s was not linted:\n%s", name, out)
			}
			if strings.Contains(out, "found no shell files") {
				t.Errorf("the file selector skipped %s entirely:\n%s", name, out)
			}
		})
	}
}

// TestShellLintGateChecksDirectivesOutsideTheSweepScope pins the gate's two
// different scopes. The sweep covers scripts/ and .githooks/ only -- 51 of the
// 138 tracked shell files in this repo are dirty and cleaning them is separate
// work -- but the directive check costs nothing and needs no linter, so it runs
// over every tracked shell file. Nothing else in this suite would notice if it
// silently narrowed to the sweep's scope, because inside that scope the sweep
// catches a malformed directive too.
//
// The colon form is chosen deliberately: it is the one a whole-word detector
// missed, and inside the sweep's scope the sweep hid the miss. Mutation
// survivor until this test existed.
func TestShellLintGateChecksDirectivesOutsideTheSweepScope(t *testing.T) {
	shellcheckBin(t)
	root := fixtureRepo(t, map[string]string{
		"scripts/ok.sh":     cleanScript,
		"examples/thing.sh": scriptWithDirective("# shellcheck: disable=SC2086"),
	})
	out, code := runGate(t, root)
	if code == 0 {
		t.Fatalf("gate accepted a malformed directive outside scripts/ and .githooks/:\n%s", out)
	}
	if !strings.Contains(out, "examples/thing.sh") {
		t.Errorf("refusal does not name the offending file, so the directive check is not running repo-wide:\n%s", out)
	}
	if !strings.Contains(out, "unparseable shellcheck directive token") {
		t.Errorf("refusal came from the sweep rather than the directive check:\n%s", out)
	}
}

// TestShellLintGateRefusesAnEmptySweep pins the guard that turns a broken file
// selector into a loud failure. Without it a selector that matched nothing
// would report success over an unswept tree -- and that is not hypothetical:
// removing the selector's shebang arm produced exactly that state, and only
// this guard made the run non-zero.
func TestShellLintGateRefusesAnEmptySweep(t *testing.T) {
	shellcheckBin(t)
	root := fixtureRepo(t, map[string]string{
		"README.md":         "no shell here\n",
		"scripts/notes.txt": "also not a script\n",
	})
	out, code := runGate(t, root)
	if code == 0 {
		t.Fatalf("gate reported success with nothing to sweep:\n%s", out)
	}
	if !strings.Contains(out, "found no shell files") {
		t.Errorf("failure should say the selector found nothing, not something else:\n%s", out)
	}
}

// TestShellLintGateFailsClosedWithoutShellcheck is the decision the bead asked
// to be made deliberately. The rejected alternative was skipping the sweep
// when the linter is absent, which makes every CI run green whether or not it
// exists.
func TestShellLintGateFailsClosedWithoutShellcheck(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git is required to run the gate: %v", err)
	}
	// A PATH holding git and nothing else. Filtering shellcheck out of the
	// real PATH by directory would not work: on the CI images it lives in
	// /usr/bin alongside git.
	bin := t.TempDir()
	if err := os.Symlink(git, filepath.Join(bin, "git")); err != nil {
		t.Fatalf("link git into the stripped PATH: %v", err)
	}
	root := fixtureRepo(t, map[string]string{"scripts/subject.sh": cleanScript})

	out, code := runGate(t, root, "PATH="+bin, "GOPATH="+t.TempDir())
	if code == 0 {
		t.Fatalf("gate exited 0 with no shellcheck available; it must fail closed:\n%s", out)
	}
	if !strings.Contains(out, shellcheckRemedy) {
		t.Errorf("failure should name the remedy %q:\n%s", shellcheckRemedy, out)
	}
	// Distinguishes the intended branch from the gate simply finding no files
	// because git was unreachable on the stripped PATH.
	if strings.Contains(out, "found no shell files") {
		t.Errorf("the fixture's file was not enumerated, so this run did not reach the missing-linter branch:\n%s", out)
	}
}

func TestShellLintGateIsWiredIntoTheGateRun(t *testing.T) {
	root := repoRoot(t)

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	for _, want := range []string{
		"check-shell-lint:",
		"scripts/check-shell-lint.sh",
		// The target must provision the linter rather than hope for it,
		// because the gate fails closed without one.
		"check-shell-lint: $(SHELLCHECK)",
	} {
		if !strings.Contains(string(makefile), want) {
			t.Errorf("Makefile missing %q", want)
		}
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatalf("read ci.yml: %v", err)
	}
	if !strings.Contains(string(workflow), "make check-shell-lint") {
		t.Error("ci.yml preflight-static job is missing the check-shell-lint step")
	}
}

// TestShellLintGatePinsOneShellcheckVersion keeps the Makefile pin and the
// installer's digest table in step. Bumping one alone leaves the installer
// refusing the version the Makefile asks for, which surfaces as a CI failure
// with no obvious cause.
func TestShellLintGatePinsOneShellcheckVersion(t *testing.T) {
	root := repoRoot(t)

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	var pinned string
	for _, line := range strings.Split(string(makefile), "\n") {
		if rest, ok := strings.CutPrefix(line, "SHELLCHECK_VERSION := "); ok {
			pinned = strings.TrimSpace(rest)
			break
		}
	}
	if pinned == "" {
		t.Fatal("Makefile does not pin SHELLCHECK_VERSION")
	}

	installer, err := os.ReadFile(filepath.Join(root, "scripts", "install-shellcheck.sh"))
	if err != nil {
		t.Fatalf("read installer: %v", err)
	}
	if !strings.Contains(string(installer), pinned+":linux.x86_64)") {
		t.Errorf("scripts/install-shellcheck.sh has no pinned digest for shellcheck %s on linux.x86_64", pinned)
	}
}
