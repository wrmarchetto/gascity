#!/usr/bin/env bash
# scripts/check-shell-lint.sh -- static-lint gate for this repo's shell code.
#
# Two independent refusals, because neither one covers the other:
#
#   1. DIRECTIVE SANITY, over every tracked shell file in the repo. ShellCheck
#      reads any comment BEGINNING with the literal "shellcheck" as a
#      directive -- prefix, not whole word, so `# shellcheckrc lives here` is
#      one too; a directive it cannot parse aborts analysis of the rest of
#      the file, reporting SC1072/SC1073 and nothing else. That much the sweep
#      below would catch on its own. What it would NOT catch is a file-level
#      `disable=SC1072,SC1073` (or `disable=all`) sitting above such a
#      directive: measured on 0.11.0, that combination exits 0 with ZERO
#      findings while the file is entirely unanalyzed. So this check refuses
#      the malformed directive AND refuses any directive that disables the
#      diagnostics which report one.
#
#   2. THE SWEEP, over scripts/ and .githooks/ only. Everything the linter
#      finds, at its default severity, with `-x` so sourced libraries are
#      analyzed in context rather than reported as SC1091.
#
# WHY THE TWO SCOPES DIFFER. Measured 2026-09-07 on the tree this file lands
# in, shellcheck 0.11.0 linux.x86_64, with the same `-x -P SCRIPTDIR` the sweep
# uses: 140 tracked shell files, 104 clean, 36 dirty -- and every one of the 36
# is outside scripts/ and .githooks/ (20 examples/, 6 internal/, 4 test/, 4
# contrib/, 1 schemas/, 1 .github/). Cleaning those is separate work, so the
# sweep is scoped to the repo's own gate and tooling scripts, which is where
# the incident that produced this file happened, while check 1 costs nothing
# and runs everywhere.
#
# THE BOUND ON THAT, so it does not go unnoticed: an ordinary shellcheck
# finding in a file outside scripts/ and .githooks/ is refused by NOTHING.
# That includes the pack scripts the SDK ships -- one of them carries an
# SC2115 on `rm -rf "$ARCHIVE_REPO/$db"`. Widen SWEEP_PATHS as those
# directories are cleaned. The count above describes the tree at THIS commit
# and nothing later; re-measure before quoting it.
#
# The rejected alternative was a skip when shellcheck is absent. Every CI run
# would then be green whether or not the linter existed, which is the failure
# mode the rest of this repo's guards are written to avoid. This gate fails
# closed instead, and `make check-shell-lint` provisions the pinned binary via
# scripts/install-shellcheck.sh so absent is not a state a caller lands in by
# accident.
#
# The file list comes from `git ls-files`, so a script that is written but not
# yet staged is invisible to a local run. That matches what CI sees and is the
# same rule the repo's other gates follow; `git add` first if a local run must
# cover a new file. This gate's own script went unlinted for exactly that
# reason until it was committed.
#
# Run:      make check-shell-lint
# Pinned by: scripts/shell_lint_gate_test.go (make test)
#
# Usage: scripts/check-shell-lint.sh [REPO_ROOT]
#
# REPO_ROOT defaults to the repository this script lives in. It is a positional
# argument, not a test-only switch: the fixture trees in shell_lint_gate_test.go
# are real git repositories the gate walks through its ONE production code path,
# so there is no branch that only tests take. Same shape as
# scripts/check-gomod-replace.sh, which takes the go.mod to read.

set -euo pipefail

root="${1:-}"
if [ -z "$root" ]; then
  root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fi
cd "$root"

# --- scope ---

# Directories whose shell files must be shellcheck-clean. Both are this repo's
# own tooling: the gate scripts, the test runners, and the git hooks.
SWEEP_PATHS=(scripts .githooks)

# testdata holds deliberately-broken fixtures (internal/beads/exec/testdata,
# and this gate's own cases), so linting it would refuse the fixtures for
# being what they are.
is_excluded() {
  case "$1" in
    */testdata/* | testdata/*) return 0 ;;
    *) return 1 ;;
  esac
}

# A tracked file is shell if its name ends .sh or its shebang names a shell.
# Both arms are load-bearing: extension alone would miss the 11 extensionless
# runners under scripts/ and both .githooks/ hooks (counted 2026-09-07), and
# shebang alone would miss a library meant only to be sourced, which need not
# carry one. The interpreter is matched on its BASENAME, so neither
# `#!/bin/sh -e` nor an `env` form escapes an anchored pattern.
is_shell_file() {
  local path="$1" first interp
  case "$path" in *.sh) return 0 ;; esac
  [ -f "$path" ] || return 1
  IFS= read -r first < "$path" || return 1
  case "$first" in '#!'*) ;; *) return 1 ;; esac
  interp="${first#\#!}"
  interp="${interp# }"
  # Splitting the shebang line into words IS the operation here.
  # shellcheck disable=SC2086
  set -- $interp
  # `#!/usr/bin/env bash` names the interpreter in the second word.
  if [ "${1##*/}" = "env" ]; then
    shift || true
  fi
  case "${1##*/}" in
    sh | bash | dash | ksh | zsh) return 0 ;;
    *) return 1 ;;
  esac
}

shell_files() {
  local path
  while IFS= read -r path; do
    is_excluded "$path" && continue
    is_shell_file "$path" && printf '%s\n' "$path"
  done < <(git ls-files -- "$@")
}

findings=0
report() {
  printf 'check-shell-lint: %s\n' "$1" >&2
  findings=$((findings + 1))
}

# --- check 1: directive sanity ---

# Codes whose suppression can only ever hide an unanalyzed file: the two the
# parser raises on a malformed directive (SC1072, SC1073), the one raised on an
# unrecognized directive key (SC1107), the one raised on invalid directive
# syntax (SC1125), and the one raised for a key that only .shellcheckrc may set
# (SC1144). Deliberately NOT here: SC1090 and SC1091, which report a source
# this run could not follow. Those are routinely and legitimately disabled --
# .githooks/pre-push and scripts/rebase-resolve-lib.sh both do -- and they say
# nothing about whether the file itself parsed.
UNSUPPRESSABLE='SC1072|SC1073|SC1107|SC1125|SC1144'

check_directives() {
  local path line lineno body rest token value
  while IFS= read -r path; do
    lineno=0
    while IFS= read -r line || [ -n "$line" ]; do
      lineno=$((lineno + 1))
      # Strip leading whitespace and the comment marker, then test for the
      # literal as a PREFIX, not as a whole word. That is ShellCheck's own
      # rule, measured against 0.11.0: `# shellcheck: disable=SC2086`,
      # `# shellcheckrc lives here` and `# shellcheck-clean is a goal` are all
      # directives that fail to parse, while `# see shellcheck disable=...` is
      # not a directive at all, and `# ShellCheck disable=...` is silently
      # ignored because the match is case-sensitive. A whole-word test here
      # let the colon form through -- found by mutation, not by reading.
      body="${line#"${line%%[![:space:]]*}"}"
      case "$body" in '#'*) ;; *) continue ;; esac
      body="${body#\#}"
      body="${body#"${body%%[![:space:]]*}"}"
      case "$body" in
        shellcheck*) ;;
        *) continue ;;
      esac
      rest="${body#shellcheck}"
      # Drop a trailing `# prose` comment, which ShellCheck accepts. This is
      # the legal way to write the reason for a suppression; a bare `--` tail
      # is what does not parse.
      rest="${rest%%#*}"

      if [ -z "${rest//[[:space:]]/}" ]; then
        report "$path:$lineno: shellcheck directive with no key=value pair; ShellCheck cannot parse it and stops analyzing this file. Write prose that must not be read as a directive so it does not begin with the word 'shellcheck'."
        continue
      fi

      for token in $rest; do
        case "$token" in
          [a-z]*=*) ;;
          *)
            report "$path:$lineno: unparseable shellcheck directive token '$token'; ShellCheck stops analyzing this file and reports only SC1072/SC1073. A reason goes after a '#', never after a '--'."
            continue 2
            ;;
        esac
        value="${token#*=}"
        case "$value" in
          '' | ,* | *, | *,,*)
            report "$path:$lineno: malformed value in shellcheck directive token '$token'."
            continue 2
            ;;
        esac
      done

      for token in $rest; do
        case "$token" in
          disable=*)
            value="${token#disable=}"
            if [ "$value" = "all" ]; then
              report "$path:$lineno: 'disable=all' hides SC1072/SC1073 too, so a malformed directive in this file would be silent and the file unanalyzed. Disable the specific codes."
              continue 2
            fi
            if printf '%s' ",$value," | grep -qE ",($UNSUPPRESSABLE),"; then
              report "$path:$lineno: '$token' disables a diagnostic that reports an unparseable directive. Measured on 0.11.0: with those suppressed, a file whose directive does not parse exits 0 with zero findings while nothing in it is analyzed. Fix the directive instead."
              continue 2
            fi
            ;;
        esac
      done
    done < "$path"
  done < <(shell_files)
}

# --- check 2: the sweep ---

resolve_shellcheck() {
  local candidate
  # GOPATH/bin first: that is where scripts/install-shellcheck.sh puts the
  # pinned build, and preferring it over PATH keeps the gate off a version
  # the host happens to carry. It also sidesteps a PATH shim that is present
  # but unrunnable, which is what made scripts/test-slice-enroll-test probe
  # `shellcheck --version` before trusting a non-zero exit.
  candidate="$(go env GOPATH 2>/dev/null || true)/bin/shellcheck"
  if [ -x "$candidate" ] && "$candidate" --version >/dev/null 2>&1; then
    printf '%s\n' "$candidate"
    return 0
  fi
  if candidate="$(command -v shellcheck 2>/dev/null)" && "$candidate" --version >/dev/null 2>&1; then
    printf '%s\n' "$candidate"
    return 0
  fi
  return 1
}

check_sweep() {
  local shellcheck_bin path
  if ! shellcheck_bin="$(resolve_shellcheck)"; then
    report "shellcheck is not installed or not runnable. Install the pinned build with: make install-tools"
    return
  fi

  # -x follows `source`d files so a library is analyzed in the context that
  # sources it; without it every one of the six sourcing scripts here reports
  # SC1091 and the libraries themselves go unchecked through that path.
  # -P SCRIPTDIR resolves a source path relative to the sourcing script, which
  # is how all of them are written.
  local -a targets=()
  while IFS= read -r path; do
    targets+=("$path")
  done < <(shell_files "${SWEEP_PATHS[@]}")

  if [ "${#targets[@]}" -eq 0 ]; then
    report "found no shell files under ${SWEEP_PATHS[*]}; the file selector in this script is broken, not the tree"
    return
  fi

  for path in "${targets[@]}"; do
    if ! "$shellcheck_bin" -x -P SCRIPTDIR -- "$path" >&2; then
      report "$path: shellcheck reported findings (see above)"
    fi
  done
}

check_directives
check_sweep

if [ "$findings" -gt 0 ]; then
  printf 'check-shell-lint: FAIL (%d)\n' "$findings" >&2
  exit 1
fi
echo "check-shell-lint: OK (directives parse, shellcheck clean under ${SWEEP_PATHS[*]})"
