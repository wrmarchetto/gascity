#!/usr/bin/env bash
#
# test-check-artifact-drift.sh -- unit tests for
# scripts/check-artifact-drift.sh, the generated-artifact staleness gate that
# `make dashboard-ci` runs after rebuilding the bundle (bead ci-c425).
#
# The suite exists for one invariant: a rebuilt artifact that differs from the
# index means two entirely different things depending on whether the SOURCES
# also differ from the index, and the gate must say which. Conflating them is
# what cost roughly an agent-hour on ci-gpxg -- a staged Accounts.tsx edit
# with no rebuild presented as 22 of 35 assets changing at once, and the one
# message the old gate had ("dist is stale") sent the investigation looking
# for a Node/dependency difference instead. Every failure case below therefore
# asserts the class token AND asserts the other class's wording is absent; a
# gate that printed one message for both would pass the exit-code checks.
#
# Scope: classification and messages only, driven against real temp git repos
# with plain text files standing in for the bundle. Nothing is built, because
# the gate never builds either -- it reads the tree the caller's build left
# behind. That is what keeps this suite npm-free and under a second. The
# Makefile wiring is a separate concern, pinned by
# scripts/check_artifact_drift_test.go.
#
# Run: go test ./scripts/ -run TestCheckArtifactDrift
#      (or ./scripts/test-check-artifact-drift.sh directly)

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GATE="$TEST_DIR/check-artifact-drift.sh"

pass=0
fail=0
record_pass() {
    echo "  ok   $1"
    pass=$((pass + 1))
}
record_fail() {
    echo "  FAIL $1 -- $2"
    fail=$((fail + 1))
}

# Deterministic, hermetic git identity for the temp repos.
export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="author@example.com"
export GIT_COMMITTER_NAME="Test Author" GIT_COMMITTER_EMAIL="author@example.com"
export GIT_CONFIG_NOSYSTEM=1
unset GIT_DIR GIT_WORK_TREE 2>/dev/null || true

# The note is the caller's slot for artifact-specific explanation. The dist
# gate uses it for the rollup hash-cascade warning; here it only has to be a
# string the tests can look for.
NOTE="rollup folds child hashes into parent names"

# One parent for every fixture, so the trap can reap them all. Each fixture is
# built inside a command substitution, so a subshell cannot register its own
# cleanup -- the parent dir is the only handle the main shell keeps.
FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/var/tmp}/gc-drift-test.XXXXXX")"
trap 'rm -rf "$FIXTURE_ROOT"' EXIT INT TERM

# --- fixture ---

# new_fixture: a repo whose committed state is src/ (two source files) plus
# out/ (two artifact files), index and worktree identical. Prints the path.
#
# out/ is a SIBLING of src/, not a child: the gate reads sources and artifact
# as disjoint pathspecs, and a fixture that nested them would let a bug that
# scans the artifact as a source pass unnoticed.
new_fixture() {
    local d
    d="$(mktemp -d -p "$FIXTURE_ROOT" repo.XXXXXX)"
    git -C "$d" init -q -b main
    git -C "$d" config commit.gpgsign false
    mkdir -p "$d/src" "$d/out"
    printf 'source a\n' >"$d/src/a.tsx"
    printf 'source b\n' >"$d/src/b.tsx"
    printf 'built from a\n' >"$d/out/asset-aaaa.js"
    printf 'built from b\n' >"$d/out/asset-bbbb.js"
    git -C "$d" add -A
    git -C "$d" commit -qm base
    printf '%s' "$d"
}

# run_gate <repo>: run the gate at the repo root the way dashboard-ci does.
# Sets GATE_OUT (stdout+stderr merged) and GATE_STATUS.
run_gate() {
    local repo="$1"
    GATE_OUT="$(
        cd "$repo" || exit 99
        "$GATE" \
            --label 'dashboard bundle' \
            --artifact out \
            --source src \
            --regen 'make dashboard-build' \
            --note "$NOTE" 2>&1
    )"
    GATE_STATUS=$?
}

# --- assertions ---

assert_status() {
    local name="$1" want="$2"
    if [ "$GATE_STATUS" -ne "$want" ]; then
        record_fail "$name" "exit $GATE_STATUS, want $want; output: $GATE_OUT"
        return 1
    fi
    return 0
}

assert_contains() {
    local name="$1" needle="$2"
    case "$GATE_OUT" in
    *"$needle"*) return 0 ;;
    esac
    record_fail "$name" "output missing '$needle'; got: $GATE_OUT"
    return 1
}

assert_absent() {
    local name="$1" needle="$2"
    case "$GATE_OUT" in
    *"$needle"*)
        record_fail "$name" "output should not contain '$needle'; got: $GATE_OUT"
        return 1
        ;;
    esac
    return 0
}

# --- tests ---

# A tree whose worktree, index and artifact all agree is the CI steady state.
# It must be silent: a gate that chatters on success trains readers to skip
# its output, which is how the ci-gpxg message went unread.
test_clean_tree_passes_silently() {
    local name="clean_tree_passes_silently"
    local repo
    repo="$(new_fixture)"
    run_gate "$repo"
    assert_status "$name" 0 || return
    if [ -n "$GATE_OUT" ]; then
        record_fail "$name" "expected no output, got: $GATE_OUT"
        return
    fi
    record_pass "$name"
}

# The input the gate sees on EVERY successful run, and the one most likely to
# break it: dashboard-build does `rm -rf dist && cp -rf`, so each file arrives
# with a new mtime and inode and identical content. A gate that answered from
# stat info alone -- which `git diff --quiet` is permitted to do -- would fail
# CI on every green build.
test_rewritten_but_identical_artifact_passes() {
    local name="rewritten_but_identical_artifact_passes"
    local repo content
    repo="$(new_fixture)"
    for f in "$repo"/out/*.js; do
        content="$(cat "$f")"
        rm -f "$f"
        printf '%s\n' "$content" >"$f"
    done
    run_gate "$repo"
    assert_status "$name" 0 || return
    if [ -n "$GATE_OUT" ]; then
        record_fail "$name" "expected no output, got: $GATE_OUT"
        return
    fi
    record_pass "$name"
}

# The fault the gate exists to name: the rebuild differs from the index and
# nothing in the worktree can explain it, so the committed artifact is not the
# build of the committed sources.
test_rebuilt_artifact_with_clean_sources_is_stale_index() {
    local name="rebuilt_artifact_with_clean_sources_is_stale_index"
    local repo
    repo="$(new_fixture)"
    printf 'rebuilt from a\n' >"$repo/out/asset-aaaa.js"
    run_gate "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: stale-index" || return
    assert_contains "$name" "make dashboard-build" || return
    assert_contains "$name" "$NOTE" || return
    assert_contains "$name" "out/asset-aaaa.js" || return
    record_pass "$name"
}

# The hole in the predecessor gate. `git diff` cannot see an untracked file,
# so a rebuild that only ADDS an asset -- a new content-hashed chunk that
# renames nothing -- read as clean. The artifact is still not the committed
# one.
test_rebuild_that_only_adds_an_untracked_asset_is_stale_index() {
    local name="rebuild_that_only_adds_an_untracked_asset_is_stale_index"
    local repo
    repo="$(new_fixture)"
    printf 'new lazy chunk\n' >"$repo/out/asset-cccc.js"
    run_gate "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: stale-index" || return
    assert_contains "$name" "out/asset-cccc.js" || return
    record_pass "$name"
}

# A caller that points --source at an ancestor of the artifact (here, the
# whole repo) must still get a usable verdict. Without the artifact excluded
# from the source scan, the rebuilt asset counts as a dirty source and the
# gate can only ever answer "unattributable" -- it never fires on the fault it
# exists for.
test_artifact_under_a_source_path_is_not_its_own_dirty_source() {
    local name="artifact_under_a_source_path_is_not_its_own_dirty_source"
    local repo
    repo="$(new_fixture)"
    printf 'rebuilt from a\n' >"$repo/out/asset-aaaa.js"
    GATE_OUT="$(
        cd "$repo" || exit 99
        "$GATE" --label 'dashboard bundle' --artifact out --source . \
            --regen 'make dashboard-build' 2>&1
    )"
    GATE_STATUS=$?
    assert_status "$name" 1 || return
    assert_contains "$name" "class: stale-index" || return
    record_pass "$name"
}

# The ci-gpxg shape, replayed: the source edit is STAGED, the committed
# artifact still holds the previous build, and the caller's rebuild produced
# the new one in the worktree. Worktree sources equal index sources, so the
# assertion holds and the verdict must be the real fault -- not the
# uncommitted-edits excuse.
test_staged_source_edit_without_rebuild_is_stale_index() {
    local name="staged_source_edit_without_rebuild_is_stale_index"
    local repo
    repo="$(new_fixture)"
    printf 'source a, edited\n' >"$repo/src/a.tsx"
    git -C "$repo" add src/a.tsx
    printf 'rebuilt from edited a\n' >"$repo/out/asset-aaaa.js"
    run_gate "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: stale-index" || return
    assert_absent "$name" "class: unattributable" || return
    record_pass "$name"
}

# The other side: an unstaged source edit means the rebuild was a build of
# sources that are not in the index, so the artifact diff proves nothing about
# the committed artifact. Naming the dirty file is the whole remedy.
test_unstaged_source_edit_is_unattributable() {
    local name="unstaged_source_edit_is_unattributable"
    local repo
    repo="$(new_fixture)"
    printf 'source a, edited\n' >"$repo/src/a.tsx"
    printf 'rebuilt from edited a\n' >"$repo/out/asset-aaaa.js"
    run_gate "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: unattributable" || return
    assert_contains "$name" "src/a.tsx" || return
    record_pass "$name"
}

# An untracked source file is as much a build input as a modified one, and
# `git diff` misses it exactly as it misses an untracked asset.
test_untracked_source_file_is_unattributable() {
    local name="untracked_source_file_is_unattributable"
    local repo
    repo="$(new_fixture)"
    printf 'brand new component\n' >"$repo/src/c.tsx"
    printf 'rebuilt with c\n' >"$repo/out/asset-aaaa.js"
    run_gate "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: unattributable" || return
    assert_contains "$name" "src/c.tsx" || return
    record_pass "$name"
}

# Dirty sources that leave the bundle byte-identical (a test-only or
# comment-only edit) must not fail the gate -- but the run did not prove the
# committed artifact is a build of the committed sources either, and saying so
# is cheaper than a reader assuming it did.
test_dirty_sources_with_matching_artifact_pass_unproven() {
    local name="dirty_sources_with_matching_artifact_pass_unproven"
    local repo
    repo="$(new_fixture)"
    printf 'source a, comment only\n' >"$repo/src/a.tsx"
    run_gate "$repo"
    assert_status "$name" 0 || return
    assert_contains "$name" "class: unproven" || return
    assert_contains "$name" "src/a.tsx" || return
    record_pass "$name"
}

# The bead's actual complaint. Both failures exit 1 and both print a diff, so
# exit codes and diffstats cannot tell a reader which fault they have; only
# the wording can, and only if the two wordings share no verdict sentence.
test_the_two_failure_verdicts_are_distinguishable() {
    local name="the_two_failure_verdicts_are_distinguishable"
    local repo stale_out dirty_out

    repo="$(new_fixture)"
    printf 'rebuilt from a\n' >"$repo/out/asset-aaaa.js"
    run_gate "$repo"
    stale_out="$GATE_OUT"

    repo="$(new_fixture)"
    printf 'source a, edited\n' >"$repo/src/a.tsx"
    printf 'rebuilt from edited a\n' >"$repo/out/asset-aaaa.js"
    run_gate "$repo"
    dirty_out="$GATE_OUT"

    if [ "$stale_out" = "$dirty_out" ]; then
        record_fail "$name" "both faults printed the same message: $stale_out"
        return
    fi
    case "$dirty_out" in
    *"class: stale-index"*)
        record_fail "$name" "unattributable output claims the index is stale: $dirty_out"
        return
        ;;
    esac
    case "$stale_out" in
    *"class: unattributable"*)
        record_fail "$name" "stale-index output hedges as unattributable: $stale_out"
        return
        ;;
    esac
    record_pass "$name"
}

# A gate invoked wrong must not answer "no drift". Exit 2 keeps the usage
# error distinguishable from a real finding for a caller that only reads the
# status.
test_missing_required_flag_is_a_usage_error() {
    local name="missing_required_flag_is_a_usage_error"
    local repo
    repo="$(new_fixture)"
    GATE_OUT="$(cd "$repo" && "$GATE" --label 'dashboard bundle' --source src 2>&1)"
    GATE_STATUS=$?
    assert_status "$name" 2 || return
    assert_contains "$name" "--artifact" || return
    record_pass "$name"
}

# --- run ---

echo "check-artifact-drift.sh"
test_clean_tree_passes_silently
test_rewritten_but_identical_artifact_passes
test_rebuilt_artifact_with_clean_sources_is_stale_index
test_rebuild_that_only_adds_an_untracked_asset_is_stale_index
test_artifact_under_a_source_path_is_not_its_own_dirty_source
test_staged_source_edit_without_rebuild_is_stale_index
test_unstaged_source_edit_is_unattributable
test_untracked_source_file_is_unattributable
test_dirty_sources_with_matching_artifact_pass_unproven
test_the_two_failure_verdicts_are_distinguishable
test_missing_required_flag_is_a_usage_error

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
