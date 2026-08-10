#!/usr/bin/env bash
#
# test-check-generated-docs-drift.sh -- behavioral tests for
# scripts/check-generated-docs-drift.sh, the gate CI's preflight-generated job
# runs and whose patch output .github/workflows/docs-autofix.yml applies to the
# PR branch (bead ci-d4lw).
#
# The suite exists because that script has two outputs a caller depends on and
# they can disagree: the exit status CI reads, and the patch the privileged
# autofix job applies. A gate that exits 1 with no patch is a red PR nobody can
# autofix; one that exits 0 having written a patch is drift that ships. Reading
# the script's text cannot catch either, so every case here runs the real script
# and asserts on both.
#
# It also pins the classification the script now delegates to
# check-artifact-drift.sh. That matters most for the case CI never sees: a
# developer with an unstaged edit to a Go file the generator reads used to get
# the same "STALE" message as a genuinely stale commit.
#
# The generator is injected by putting a stand-in `go` first on PATH -- not by a
# flag on the script. A GENERATOR=... switch would be read before the code under
# test runs, so these tests would pin the switch while the real
# `CGO_ENABLED=0 go run ./cmd/genschema` line could be deleted with the suite
# still green. The stand-in REFUSES any argv it was not scripted for, so a
# future change to how the script invokes the generator fails loudly here
# instead of being silently accepted.
#
# Scope: exit status, patch emission and verdict wording. Nothing is compiled
# and no docs are generated. The classifier's own branches are pinned
# separately by scripts/test-check-artifact-drift.sh.
#
# Run: go test ./scripts/ -run TestGeneratedDocsDriftGate
#      (or ./scripts/test-check-generated-docs-drift.sh directly)

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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

export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="author@example.com"
export GIT_COMMITTER_NAME="Test Author" GIT_COMMITTER_EMAIL="author@example.com"
export GIT_CONFIG_NOSYSTEM=1
unset GIT_DIR GIT_WORK_TREE 2>/dev/null || true

GEN_PATHS=(
    docs/reference/cli.md
    docs/reference/config.md
    docs/reference/schema/city-schema.json
    docs/reference/schema/city-schema.txt
    docs/reference/schema/pack-schema.json
    docs/reference/schema/pack-schema.txt
)

FIXTURE_ROOT="$(mktemp -d "${TMPDIR:-/var/tmp}/gc-docsdrift-test.XXXXXX")"
trap 'rm -rf "$FIXTURE_ROOT"' EXIT INT TERM

# --- fixture ---

# new_fixture: a repo holding the two scripts under test, a Go source tree
# standing in for the generator's inputs, and the six committed generated docs.
# The regenerated content each doc gets is written by the stand-in `go` from
# <repo>/.regen/<path>, so a test controls drift by editing that staging copy
# and never by reaching into the script.
new_fixture() {
    local d
    d="$(mktemp -d -p "$FIXTURE_ROOT" repo.XXXXXX)"
    git -C "$d" init -q -b main
    git -C "$d" config commit.gpgsign false

    mkdir -p "$d/scripts" "$d/internal/config" "$d/cmd/gc" "$d/docs/reference/schema"
    cp -f "$TEST_DIR/check-generated-docs-drift.sh" "$TEST_DIR/check-artifact-drift.sh" \
        "$d/scripts/"
    printf 'package config\n' >"$d/internal/config/config.go"
    printf 'package main\n' >"$d/cmd/gc/main.go"
    printf 'module example.test\n' >"$d/go.mod"
    printf '\n' >"$d/go.sum"

    mkdir -p "$d/.regen/docs/reference/schema"
    local path
    for path in "${GEN_PATHS[@]}"; do
        printf 'generated %s\n' "$path" >"$d/$path"
        printf 'generated %s\n' "$path" >"$d/.regen/$path"
    done

    git -C "$d" add -A -- scripts internal cmd go.mod go.sum docs
    git -C "$d" commit -qm base
    install_fake_go "$d"
    printf '%s' "$d"
}

# install_fake_go: a stand-in generator, first on PATH, that copies
# .regen/<path> over each generated doc. It accepts EXACTLY the argv
# check-generated-docs-drift.sh passes and exits 97 on anything else, so a
# change to how the generator is invoked surfaces as a failure here rather than
# as a test that quietly stopped covering the real command.
install_fake_go() {
    local d="$1"
    mkdir -p "$d/.fakebin"
    cat >"$d/.fakebin/go" <<'FAKE'
#!/usr/bin/env bash
set -uo pipefail
if [ "$#" -ne 2 ] || [ "$1" != "run" ] || [ "$2" != "./cmd/genschema" ]; then
    printf 'fake go: refusing unscripted argv: %s\n' "$*" >&2
    exit 97
fi
if [ "${CGO_ENABLED:-unset}" != "0" ]; then
    printf 'fake go: expected CGO_ENABLED=0, got %s\n' "${CGO_ENABLED:-unset}" >&2
    exit 97
fi
# A generator that is present but FAILS -- what CGO_ENABLED=0 exists to avoid on
# a host without ICU headers. Read here rather than by the script under test: a
# switch the script itself read would be evaluated before the code under test
# runs, so the suite would pin the switch instead of the propagation.
if [ "${FAKE_GO_EXIT:-0}" != "0" ]; then
    printf 'fake go: simulated build failure\n' >&2
    exit "$FAKE_GO_EXIT"
fi
shopt -s nullglob dotglob
while IFS= read -r -d '' src; do
    dest="${src#./.regen/}"
    mkdir -p "$(dirname "$dest")"
    cp -f "$src" "$dest"
done < <(find ./.regen -type f -print0)
echo "fake go: regenerated"
FAKE
    chmod 0755 "$d/.fakebin/go"
}

# run_script <repo> [env=value]...: run the gate the way CI does -- cwd at the
# repo root, patch written to the default name. Extra arguments are passed as
# environment to the run, which is how a case drives the stand-in generator into
# failing. Sets OUT, STATUS, PATCH_BODY and PATCH_EXISTS.
run_script() {
    local repo="$1"
    shift
    # `cd ""` is a successful no-op in bash, so an empty fixture path would run
    # the gate against the REAL repository this suite lives in. 98 is a status
    # no case expects.
    if [ -z "$repo" ]; then
        OUT="fixture path is empty"
        STATUS=98
        PATCH_BODY=""
        PATCH_EXISTS=0
        return
    fi
    OUT="$(
        cd "$repo" || exit 99
        env "$@" PATH="$repo/.fakebin:$PATH" \
            ./scripts/check-generated-docs-drift.sh 2>&1
    )"
    STATUS=$?
    PATCH_FILE="$repo/generated-docs-freshness.patch"
    if [ -f "$PATCH_FILE" ]; then
        PATCH_BODY="$(cat "$PATCH_FILE")"
        PATCH_EXISTS=1
    else
        PATCH_BODY=""
        PATCH_EXISTS=0
    fi
}

# --- assertions ---

assert_status() {
    local name="$1" want="$2"
    if [ "$STATUS" -ne "$want" ]; then
        record_fail "$name" "exit $STATUS, want $want; output: $OUT"
        return 1
    fi
    return 0
}

assert_contains() {
    local name="$1" needle="$2"
    case "$OUT" in
    *"$needle"*) return 0 ;;
    esac
    record_fail "$name" "output missing '$needle'; got: $OUT"
    return 1
}

assert_absent() {
    local name="$1" needle="$2"
    case "$OUT" in
    *"$needle"*)
        record_fail "$name" "output should not contain '$needle'; got: $OUT"
        return 1
        ;;
    esac
    return 0
}

assert_patch_absent() {
    local name="$1"
    if [ "$PATCH_EXISTS" -eq 1 ]; then
        record_fail "$name" "patch file should not exist; body: $PATCH_BODY"
        return 1
    fi
    return 0
}

# --- tests ---

# The CI steady state. A stale patch left over from a previous failing run must
# be removed, or the autofix job applies a patch for drift already fixed.
test_fresh_docs_pass_and_remove_a_stale_patch() {
    local name="fresh_docs_pass_and_remove_a_stale_patch"
    local repo
    repo="$(new_fixture)"
    printf 'leftover from an earlier run\n' >"$repo/generated-docs-freshness.patch"
    run_script "$repo"
    assert_status "$name" 0 || return
    assert_contains "$name" "Generated reference docs are fresh." || return
    assert_patch_absent "$name" || return
    record_pass "$name"
}

# The fault the gate exists for, in the shape CI sees it: a committed doc that
# is not what the generator produces, on an otherwise clean checkout. The patch
# is what the privileged autofix job applies, so its absence is as much a
# failure as a wrong exit status.
test_stale_docs_fail_with_a_patch() {
    local name="stale_docs_fail_with_a_patch"
    local repo
    repo="$(new_fixture)"
    printf 'regenerated cli reference\n' >"$repo/.regen/docs/reference/cli.md"
    run_script "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: stale-index" || return
    # The INDENTED form, from the gate's changed-path listing. The bare path
    # also appears in the "Fix locally with: ... git commit --" line, which
    # names all six paths on every failing run, so matching it proves nothing
    # about which path the gate actually found.
    assert_contains "$name" "    docs/reference/cli.md" || return
    assert_contains "$name" "1 artifact path(s) changed" || return
    if [ "$PATCH_EXISTS" -ne 1 ] || [ -z "$PATCH_BODY" ]; then
        record_fail "$name" "no regeneration patch written; output: $OUT"
        return
    fi
    case "$PATCH_BODY" in
    *"regenerated cli reference"*) ;;
    *)
        record_fail "$name" "patch does not carry the regenerated content: $PATCH_BODY"
        return
        ;;
    esac
    record_pass "$name"
}

# The local-run fault the bare `git diff` conflated with the one above. An
# unstaged edit to a Go file the generator reads means the docs this run
# regenerated are a build of sources that are NOT in the index, so the diff
# proves nothing about the committed docs. Both verdicts exit 1 and both print a
# diffstat, so only the wording separates them.
test_unstaged_go_edit_is_unattributable() {
    local name="unstaged_go_edit_is_unattributable"
    local repo
    repo="$(new_fixture)"
    printf 'package config\n\n// Field documents a thing.\n' >"$repo/internal/config/config.go"
    printf 'regenerated with the new comment\n' >"$repo/.regen/docs/reference/config.md"
    run_script "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: unattributable" || return
    assert_absent "$name" "class: stale-index" || return
    assert_contains "$name" "internal/config/config.go" || return
    record_pass "$name"
}

# A staged Go edit is a different case that looks identical from outside: the
# worktree sources equal the index sources, so the assertion the gate makes does
# hold and the committed docs really are stale. Pinned here and not only in the
# classifier's suite because the source set THIS script declares is what decides
# it -- a set that omitted internal/ would answer stale-index for both cases.
test_staged_go_edit_is_stale_index() {
    local name="staged_go_edit_is_stale_index"
    local repo
    repo="$(new_fixture)"
    printf 'package config\n\n// Field documents a thing.\n' >"$repo/internal/config/config.go"
    git -C "$repo" add internal/config/config.go
    printf 'regenerated with the new comment\n' >"$repo/.regen/docs/reference/config.md"
    run_script "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "class: stale-index" || return
    assert_absent "$name" "class: unattributable" || return
    record_pass "$name"
}

# Every generated path must be judged, not just the first. The list is written
# out three times across the repo (TestGeneratedDocsPathListsAgree), and a path
# dropped from the gate's set is a generated file nothing ever checks.
test_every_generated_path_is_judged() {
    local name="every_generated_path_is_judged"
    local repo path
    for path in "${GEN_PATHS[@]}"; do
        repo="$(new_fixture)"
        printf 'regenerated %s differently\n' "$path" >"$repo/.regen/$path"
        run_script "$repo"
        if [ "$STATUS" -ne 1 ]; then
            record_fail "$name" "drift in $path exited $STATUS, want 1; output: $OUT"
            return
        fi
        # Indented, for the same reason as above.
        case "$OUT" in
        *"    $path"*) ;;
        *)
            record_fail "$name" "verdict for drift in $path never names it: $OUT"
            return
            ;;
        esac
    done
    record_pass "$name"
}

# Drift that only ADDS a generated file is real drift the predecessor could not
# see at all, and `git diff` still cannot express it as a patch. The gate must
# fail, and must say why the autofix workflow will get nothing to apply rather
# than leave an empty patch that reads as "nothing to do".
test_untracked_generated_file_fails_without_an_empty_patch() {
    local name="untracked_generated_file_fails_without_an_empty_patch"
    local repo
    repo="$(new_fixture)"
    git -C "$repo" rm -q --cached docs/reference/config.md
    git -C "$repo" commit -qm 'drop config.md from the index'
    rm -f "$repo/docs/reference/config.md"
    run_script "$repo"
    assert_status "$name" 1 || return
    assert_contains "$name" "UNTRACKED" || return
    assert_contains "$name" "    docs/reference/config.md" || return
    assert_patch_absent "$name" || return
    record_pass "$name"
}

# A gate that cannot judge must not be reported as drift. Exit 2 from the
# classifier (bad arguments, git unreadable) has to propagate: a patch written
# from a run that judged nothing is worse than no patch, and a 1 would send the
# autofix job looking for one.
test_gate_usage_error_propagates_as_two() {
    local name="gate_usage_error_propagates_as_two"
    local repo
    repo="$(new_fixture)"
    printf '#!/usr/bin/env bash\nexit 2\n' >"$repo/scripts/check-artifact-drift.sh"
    chmod 0755 "$repo/scripts/check-artifact-drift.sh"
    run_script "$repo"
    assert_status "$name" 2 || return
    assert_contains "$name" "no patch written" || return
    assert_patch_absent "$name" || return
    record_pass "$name"
}

# A generator that fails must never be reported as fresh docs. Nothing else in
# the suite drives this: the stand-in always succeeds on the scripted argv, so
# `CGO_ENABLED=0 go run ./cmd/genschema || true` -- or any change that stops
# `set -e` from aborting there -- leaves the script comparing the docs already
# on disk against themselves and printing "fresh" while real drift ships.
test_generator_failure_is_not_reported_as_fresh() {
    local name="generator_failure_is_not_reported_as_fresh"
    local repo
    repo="$(new_fixture)"
    printf 'regenerated cli reference\n' >"$repo/.regen/docs/reference/cli.md"
    run_script "$repo" FAKE_GO_EXIT=3
    if [ "$STATUS" -eq 0 ]; then
        record_fail "$name" "generator failed but the gate passed: $OUT"
        return
    fi
    assert_absent "$name" "are fresh" || return
    assert_patch_absent "$name" || return
    record_pass "$name"
}

# The stand-in generator is the suite's own load-bearing part: if it answered
# every argv with success, a script that stopped running the generator (or ran
# it with CGO on) would keep the suite green. Prove it refuses.
test_the_fake_generator_refuses_unscripted_argv() {
    local name="the_fake_generator_refuses_unscripted_argv"
    local repo out status
    repo="$(new_fixture)"
    out="$(cd "$repo" && PATH="$repo/.fakebin:$PATH" CGO_ENABLED=0 go build ./... 2>&1)"
    status=$?
    if [ "$status" -ne 97 ]; then
        record_fail "$name" "fake go accepted 'build ./...' with exit $status: $out"
        return
    fi
    out="$(cd "$repo" && PATH="$repo/.fakebin:$PATH" go run ./cmd/genschema 2>&1)"
    status=$?
    if [ "$status" -ne 97 ]; then
        record_fail "$name" "fake go accepted CGO_ENABLED unset with exit $status: $out"
        return
    fi
    out="$(cd "$repo" && PATH="$repo/.fakebin:$PATH" CGO_ENABLED=0 FAKE_GO_EXIT=3 \
        go run ./cmd/genschema 2>&1)"
    status=$?
    if [ "$status" -ne 3 ]; then
        record_fail "$name" "fake go ignored FAKE_GO_EXIT, exit $status: $out"
        return
    fi
    record_pass "$name"
}

# --- run ---
#
# Driven from an array and reconciled against it below: called directly, a
# misspelled or deleted name is a "command not found" on stderr, no verdict, and
# a green exit status.

TESTS=(
    test_the_fake_generator_refuses_unscripted_argv
    test_fresh_docs_pass_and_remove_a_stale_patch
    test_stale_docs_fail_with_a_patch
    test_unstaged_go_edit_is_unattributable
    test_staged_go_edit_is_stale_index
    test_every_generated_path_is_judged
    test_untracked_generated_file_fails_without_an_empty_patch
    test_gate_usage_error_propagates_as_two
    test_generator_failure_is_not_reported_as_fresh
)

echo "check-generated-docs-drift.sh"
for t in "${TESTS[@]}"; do
    if ! declare -F "$t" >/dev/null; then
        record_fail "$t" "no such test function"
        continue
    fi
    "$t"
done

echo
echo "passed: $pass  failed: $fail"
if [ "$((pass + fail))" -ne "${#TESTS[@]}" ]; then
    printf 'ERROR: %d cases collected, %d reported a verdict -- a case returned\n' \
        "${#TESTS[@]}" "$((pass + fail))" >&2
    printf '  without recording pass or fail, which reads as green.\n' >&2
    exit 1
fi
[ "$fail" -eq 0 ]
