#!/usr/bin/env bash
# dashboard-shared-tests.sh
#
# Run the dashboard shared-package test suite
# (internal/api/dashboardspa/web/shared/src/**/*.test.ts).
#
# Why this file exists rather than a line in the Makefile: the suite ran in NO
# gate at all before it. The shared tests are node:test suites, so the frontend
# vitest job does not collect them -- vitest's include glob is rooted at
# frontend/src -- and nothing else invoked the shared workspace's own test
# script, not CI's dashboard job and not .githooks/pre-commit. Thirteen green,
# unenforced test files, including the account-quota staleness rules that have
# no server-side equivalent. make dashboard-check now calls this.
#
# Why the file list is built here instead of handed to the runner as a glob:
# `tsx --test "src/**/*.test.ts"` makes the RUNNER expand the pattern, which
# only Node 22 does; on Node 18 it exits 1 with "Could not find". Both
# near-miss repairs are worse than the breakage. Letting the shell expand it
# matches ONE directory level (POSIX sh has no recursive `**`), quietly
# dropping src/fixtures/test-city/. Piping find into xargs reports
# "# tests 0" and exits 0 when it matches nothing -- measured, not assumed.
# A suite that passes without running is worse than no suite, so the count is
# checked below and zero is fatal.
#
# Verified by: make dashboard-check (and dashboard-ci, which depends on it).

set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
shared_dir="$repo_root/internal/api/dashboardspa/web/shared"
# Workspace installs hoist to web/node_modules, so the runner lives one level
# above the package under test. Named explicitly rather than found on PATH:
# this script is called from make, where node_modules/.bin is absent, and
# `npx tsx` would silently reach for the network on a cold tree.
tsx="$repo_root/internal/api/dashboardspa/web/node_modules/.bin/tsx"

if [ ! -x "$tsx" ]; then
  echo "error: $tsx not found or not executable" >&2
  echo "remedy: cd $repo_root/internal/api/dashboardspa/web && npm ci" >&2
  exit 1
fi

cd "$shared_dir"

# Sorted so failures report in the same order on every host -- find returns
# directory order, which differs by filesystem.
test_files=()
while IFS= read -r file; do
  test_files+=("$file")
done < <(find src -name '*.test.ts' | sort)

if [ "${#test_files[@]}" -eq 0 ]; then
  echo "error: no *.test.ts found under $shared_dir/src" >&2
  echo "The shared suite is never legitimately empty. This means the layout moved and" >&2
  echo "this gate has stopped covering anything -- fix the path, do not delete the gate." >&2
  exit 1
fi

echo "shared suite: ${#test_files[@]} test files, node $(node --version)"
exec "$tsx" --test "${test_files[@]}"
