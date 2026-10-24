#!/usr/bin/env bash
#
# test-push-gate-select.sh -- hook wiring and always-run manifest tests for
# the scoped pre-push gate (bead ci-4w2t).
#
# scripts/push_gate_select_test.go pins what the SELECTOR decides. This file
# pins the two things around it: what the HOOK does with that decision, and
# whether the always-run manifest still names every repo-wide scanner. Both
# are separate failure surfaces from the selector, and both are the kind that
# gate a push: a correct selector wired to a hook that ignores it, or that
# consumes stdin before the ownership guard sees it, produces exactly the
# silent no-op this whole mechanism exists to prevent.
#
# WHY THESE ARE SHELL AND NOT `go test`. A Go trampoline would need
# exec.Command to drive this script, `git ls-files`, and `go list` -- and
# every one of those is a tracked subprocess occurrence in
# internal/testpolicy/resourcecensus, including the scope=all audit row whose
# invariant is "totals cannot grow; reductions must lower this baseline",
# with no per-file exemption. A first draft of ci-4w2t did exactly that and
# the push gate rejected it: +3 calls, +2 files over baseline. Driving the
# checks as a plain shell job keeps them out of the Go AST scan entirely, so
# they need no baseline change and no owner exemption. This mirrors
# push-gate-lock-selftest and local-concurrency-selftest, which are wired the
# same way for the same reason (see TESTING.md's push-gate slots section).
#
# Real .githooks/pre-push, real temp repos, real bare remotes. `make` and the
# selector are stubbed on PATH so a test costs milliseconds instead of a
# 16-minute suite -- the stub records its argv and environment, which is the
# observation these tests are actually about.
#
# Run: bash scripts/test-push-gate-select.sh -- run as the
# push-gate-select-selftest job inside scripts/test-local-parallel.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$TEST_DIR/.." && pwd)"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 -- $2"; fail=$((fail + 1)); }

export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="author@example.com"
export GIT_COMMITTER_NAME="Test Pusher" GIT_COMMITTER_EMAIL="pusher@example.com"
export GIT_CONFIG_NOSYSTEM=1
unset GIT_DIR GIT_WORK_TREE 2>/dev/null || true

# --- harness ---------------------------------------------------------------

# Every fixture lands in this one dir, removed on exit. Fixtures are built
# inside $(...) subshells, where a trap of their own would fire at once.
PGS_SCRATCH="$(mktemp -d "${TMPDIR:-/tmp}/gc-pgs-run.XXXXXX")"
trap 'rm -rf "$PGS_SCRATCH"' EXIT

# new_push_fixture <scope-decision>: a repo wired to the real pre-push hook,
# pushing to a real bare remote, with `make` and the selector stubbed. Prints
# "<repo> <bindir>"; the make stub writes its invocation to <bindir>/make.log.
new_push_fixture() {
    local decision="$1"
    local root repo remote bindir
    root="$(mktemp -d "$PGS_SCRATCH/test.XXXXXX")"
    repo="$root/repo"
    remote="$root/remote.git"
    bindir="$root/bin"
    mkdir -p "$repo" "$bindir"

    git init -q --bare "$remote"
    git init -q -b main "$repo"
    git -C "$repo" config commit.gpgsign false
    git -C "$repo" config user.email pgs@example.invalid
    git -C "$repo" config user.name pgs-test
    git -C "$repo" remote add origin "$remote"

    mkdir -p "$repo/scripts" "$repo/.githooks"
    cp -f "$REPO_ROOT/.githooks/pre-push" "$repo/.githooks/pre-push"
    chmod +x "$repo/.githooks/pre-push"
    git -C "$repo" config core.hooksPath .githooks

    # The ownership guard is a separate concern with its own suite
    # (test-push-ownership-guard.sh). Stub it to allow, so a failure here is
    # unambiguously about scope wiring.
    cat >"$repo/scripts/push-ownership-guard.sh" <<'GUARD'
assert_bead_still_claimed() { echo "ownership-guard: ran" >>"$PGS_GUARD_LOG"; return 0; }
GUARD

    cat >"$repo/scripts/push-gate-select" <<SELECT
#!/usr/bin/env bash
cat >"\$PGS_SELECTOR_STDIN"
printf '%s\n' '${decision}'
SELECT
    chmod +x "$repo/scripts/push-gate-select"

    cat >"$bindir/make" <<'MAKE'
#!/usr/bin/env bash
{
  echo "ARGS: $*"
  echo "SCOPE: ${GC_SCOPED_TEST_PACKAGES-<unset>}"
} >>"$PGS_MAKE_LOG"
MAKE
    chmod +x "$bindir/make"

    echo "baseline" >"$repo/README.md"
    git -C "$repo" add -A
    git -C "$repo" commit -qm baseline
    git -C "$repo" push -q --no-verify origin main 2>/dev/null

    printf '%s %s' "$repo" "$bindir"
}

# do_push <repo> <bindir>: commit a change and push through the real hook.
# Prints the push's combined output; sets PUSH_STATUS.
do_push() {
    local repo="$1" bindir="$2"
    echo "change $RANDOM" >>"$repo/README.md"
    git -C "$repo" add -A
    git -C "$repo" commit -qm change
    local out
    out="$(cd "$repo" && PATH="$bindir:$PATH" git push origin main 2>&1)"
    PUSH_STATUS=$?
    printf '%s' "$out"
}

# --- tests -----------------------------------------------------------------

test_scoped_push_runs_the_fast_suite_over_the_selection() {
    # The report line is what the city's assets/scripts/bead-cycle-time.py
    # PUSH_SELECT classifies a push by, so its prefix is pinned too.
    local name="a scoped push decision runs the fast suite over the selection"
    read -r repo bindir <<<"$(new_push_fixture 'scoped ./alpha ./beta')"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_MAKE_LOG"

    local out
    out="$(do_push "$repo" "$bindir")"
    if ! grep -q 'ARGS: test-fast-parallel' "$PGS_MAKE_LOG"; then
        record_fail "$name" "make was not invoked with test-fast-parallel: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    if ! grep -qx 'SCOPE: ./alpha ./beta' "$PGS_MAKE_LOG"; then
        record_fail "$name" "the push did not hand make the selection: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    if ! grep -q 'pre-push: scoped fast suite to 2 package(s)' <<<"$out"; then
        record_fail "$name" "the push did not report the selection: $out"
        return
    fi
    record_pass "$name"
}

test_full_decision_leaves_scope_unset() {
    # An unset scope is what makes test-local-parallel run the whole tree. A
    # `full` decision that leaked an empty-string scope would still take the
    # narrowing branch and run nothing. The Makefile forwards the pusher's own
    # scope, so one exported in the pushing shell must not survive either.
    local name="a full decision leaves GC_SCOPED_TEST_PACKAGES unset"
    read -r repo bindir <<<"$(new_push_fixture 'full')"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_MAKE_LOG"

    GC_SCOPED_TEST_PACKAGES=./stale do_push "$repo" "$bindir" >/dev/null
    if ! grep -qx 'SCOPE: <unset>' "$PGS_MAKE_LOG"; then
        record_fail "$name" "expected an unset scope, got: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    record_pass "$name"
}

test_none_decision_runs_no_suite() {
    # The observation this case is really about is an ABSENCE -- make never
    # ran -- and a hook that died early, or never ran at all, produces that
    # same absence. So the push status and the guard log stand in as positive
    # proof the hook executed and reached the decision, rather than falling
    # over before it. Not hypothetical: with the hook replaced by a bare
    # `exit 0` this case still reported ok while the other six failed.
    local name="a none decision runs no suite at all"
    read -r repo bindir <<<"$(new_push_fixture 'none')"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_MAKE_LOG"
    : >"$PGS_GUARD_LOG"

    do_push "$repo" "$bindir" >/dev/null
    if [ "$PUSH_STATUS" -ne 0 ]; then
        record_fail "$name" "push failed with status $PUSH_STATUS; an empty make log proves nothing about the none path"
        return
    fi
    if ! grep -q 'ownership-guard: ran' "$PGS_GUARD_LOG"; then
        record_fail "$name" "the hook never reached the scope decision"
        return
    fi
    if [ -s "$PGS_MAKE_LOG" ]; then
        record_fail "$name" "make ran anyway: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    record_pass "$name"
}

test_unrecognized_decision_runs_full_suite() {
    # The catch-all is the whole fail-closed story. A selector that grows a
    # debug print, or a new verb this hook has not learned, must widen.
    local name="an unrecognized decision runs the full suite"
    read -r repo bindir <<<"$(new_push_fixture 'perhaps-later')"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_MAKE_LOG"

    do_push "$repo" "$bindir" >/dev/null
    if ! grep -q 'ARGS: test-fast-parallel' "$PGS_MAKE_LOG"; then
        record_fail "$name" "make did not run the full suite: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    if ! grep -qx 'SCOPE: <unset>' "$PGS_MAKE_LOG"; then
        record_fail "$name" "expected an unset scope, got: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    record_pass "$name"
}

test_missing_selector_runs_full_suite() {
    local name="a missing selector runs the full suite"
    read -r repo bindir <<<"$(new_push_fixture 'full')"
    rm -f "$repo/scripts/push-gate-select"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_MAKE_LOG"

    do_push "$repo" "$bindir" >/dev/null
    if ! grep -q 'ARGS: test-fast-parallel' "$PGS_MAKE_LOG"; then
        record_fail "$name" "make did not run the full suite: $(cat "$PGS_MAKE_LOG")"
        return
    fi
    record_pass "$name"
}

test_ownership_guard_still_runs_on_a_scoped_push() {
    # The bead is explicit that this guard stays unconditional. It is also
    # the one thing the stdin restructuring could plausibly have broken, so
    # it gets its own assertion rather than being assumed.
    local name="the bead ownership guard still runs on a scoped push"
    read -r repo bindir <<<"$(new_push_fixture 'scoped ./alpha')"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_GUARD_LOG"

    do_push "$repo" "$bindir" >/dev/null
    if ! grep -q 'ownership-guard: ran' "$PGS_GUARD_LOG"; then
        record_fail "$name" "guard did not run on a scoped push"
        return
    fi
    record_pass "$name"
}

test_selector_receives_the_pre_push_records() {
    # The hook reads stdin once and replays it. If the replay were dropped,
    # the selector would see an empty stream, decide `full` forever, and the
    # whole change would silently do nothing -- a green, slow no-op.
    local name="the selector receives the pre-push records on stdin"
    read -r repo bindir <<<"$(new_push_fixture 'full')"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"
    : >"$PGS_SELECTOR_STDIN"

    do_push "$repo" "$bindir" >/dev/null
    if ! grep -qE 'refs/heads/main [0-9a-f]{40} refs/heads/main [0-9a-f]{40}' "$PGS_SELECTOR_STDIN"; then
        record_fail "$name" "selector stdin was not a pre-push record: $(cat "$PGS_SELECTOR_STDIN")"
        return
    fi
    record_pass "$name"
}

# --- through the real Makefile ---------------------------------------------

# A make stub records what the caller exported, not what the recipe's env -i
# hands the runner, and the scope was dropped at exactly that join. These
# cases run the real Makefile over a stub runner instead.

# install_runner_stub <dir>: the real Makefile, plus a runner stub logging to
# <dir>/runner.log by absolute path, since env -i strips every PGS_ variable.
install_runner_stub() {
    local dir="$1"
    mkdir -p "$dir/scripts"
    cp -f "$REPO_ROOT/Makefile" "$dir/Makefile"
    printf '#!/usr/bin/env bash\necho 1\n' >"$dir/scripts/test-local-job-count"
    cat >"$dir/scripts/test-local-parallel" <<RUNNER
#!/usr/bin/env bash
echo "ARGS: \$* SCOPE: [\${GC_SCOPED_TEST_PACKAGES-<unset>}]" >>"$dir/runner.log"
echo "YIELD: [\${LOCAL_TEST_YIELD-<unset>}]" >>"$dir/runner.log"
RUNNER
    chmod +x "$dir/scripts/test-local-job-count" "$dir/scripts/test-local-parallel"
    : >"$dir/runner.log"
}

# new_affected_fixture <scope-decision>: scripts/test-affected over a stub
# selector that logs its argv, the real Makefile and a stub runner. Prints
# the fixture directory.
new_affected_fixture() {
    local decision="$1" dir
    dir="$(mktemp -d "$PGS_SCRATCH/affected.XXXXXX")"
    install_runner_stub "$dir"
    cp -f "$REPO_ROOT/scripts/test-affected" "$dir/scripts/test-affected"
    cat >"$dir/scripts/push-gate-select" <<SELECT
#!/usr/bin/env bash
echo "\$*" >"$dir/selector.argv"
printf '%s\n' '${decision}'
SELECT
    chmod +x "$dir/scripts/test-affected" "$dir/scripts/push-gate-select"
    printf '%s' "$dir"
}

test_push_scope_reaches_the_runner_through_make() {
    local name="a scoped push reaches the runner as GC_SCOPED_TEST_PACKAGES through the real Makefile"
    read -r repo bindir <<<"$(new_push_fixture 'scoped ./alpha ./beta')"
    rm -f "$bindir/make"
    install_runner_stub "$repo"
    export PGS_MAKE_LOG="$bindir/make.log" PGS_GUARD_LOG="$bindir/guard.log"
    export PGS_SELECTOR_STDIN="$bindir/selector.stdin"

    do_push "$repo" "$bindir" >/dev/null
    if ! grep -qx 'ARGS: fast SCOPE: \[./alpha ./beta\]' "$repo/runner.log"; then
        record_fail "$name" "runner saw: $(cat "$repo/runner.log")"
        return
    fi
    # The push gate is on the merge path and must not yield (pm-log #162).
    if ! grep -qx 'YIELD: \[0\]' "$repo/runner.log"; then
        record_fail "$name" "the push gate reached the runner yielding: $(cat "$repo/runner.log")"
        return
    fi
    record_pass "$name"
}

test_affected_scope_reaches_the_runner_through_make() {
    local name="a scoped test-affected run reaches the runner as GC_SCOPED_TEST_PACKAGES"
    local dir
    dir="$(new_affected_fixture 'scoped ./alpha ./beta')"

    (cd "$dir" && env -u LOCAL_TEST_YIELD ./scripts/test-affected) >/dev/null 2>&1
    if ! grep -qx 'ARGS: fast SCOPE: \[./alpha ./beta\]' "$dir/runner.log"; then
        record_fail "$name" "runner saw: $(cat "$dir/runner.log")"
        return
    fi
    # A development run says nothing, so the runner keeps its yield.
    if ! grep -qE '^YIELD: \[(<unset>)?\]$' "$dir/runner.log"; then
        record_fail "$name" "a development run reached the runner not yielding: $(cat "$dir/runner.log")"
        return
    fi
    if ! grep -qx -- '--worktree' "$dir/selector.argv"; then
        record_fail "$name" "selector was not asked in --worktree mode: $(cat "$dir/selector.argv")"
        return
    fi
    record_pass "$name"
}

test_affected_full_decision_runs_unscoped() {
    # Empty and unset both mean the whole tree to test-local-parallel. The
    # caller's own scope is forwarded by the Makefile, so it must not survive.
    local name="a full test-affected decision reaches the runner unscoped"
    local dir
    dir="$(new_affected_fixture 'full')"

    (cd "$dir" && GC_SCOPED_TEST_PACKAGES=./stale ./scripts/test-affected) >/dev/null 2>&1
    if ! grep -qE '^ARGS: fast SCOPE: \[(<unset>)?\]$' "$dir/runner.log"; then
        record_fail "$name" "runner saw: $(cat "$dir/runner.log")"
        return
    fi
    record_pass "$name"
}

test_affected_unrecognized_decision_runs_unscoped() {
    local name="an unrecognized test-affected decision runs the full suite"
    local dir
    dir="$(new_affected_fixture 'perhaps-later')"

    (cd "$dir" && ./scripts/test-affected) >/dev/null 2>&1
    if ! grep -qE '^ARGS: fast SCOPE: \[(<unset>)?\]$' "$dir/runner.log"; then
        record_fail "$name" "runner saw: $(cat "$dir/runner.log")"
        return
    fi
    record_pass "$name"
}

test_affected_none_decision_runs_nothing() {
    # An empty runner log is also what a script that died early leaves, so
    # the exit status and the selector's argv log prove it got that far.
    local name="a none test-affected decision runs no suite"
    local dir status
    dir="$(new_affected_fixture 'none')"

    (cd "$dir" && ./scripts/test-affected) >/dev/null 2>&1
    status=$?
    if [ "$status" -ne 0 ]; then
        record_fail "$name" "test-affected exited $status"
        return
    fi
    if ! [ -s "$dir/selector.argv" ]; then
        record_fail "$name" "the selector never ran"
        return
    fi
    if [ -s "$dir/runner.log" ]; then
        record_fail "$name" "the runner ran anyway: $(cat "$dir/runner.log")"
        return
    fi
    record_pass "$name"
}

# --- always-run manifest completeness -------------------------------------

MANIFEST="$REPO_ROOT/scripts/push-gate-always-run.manifest"

# manifest_entries: the manifest's package args, comments and blanks stripped.
manifest_entries() {
    sed -e 's/#.*//' -e 's/[[:space:]]*$//' "$MANIFEST" | grep -v '^$'
}

# source_tree_scanner_packages: recomputes the manifest's conservative answer
# from the tree.
# Reads git-tracked files rather than walking the filesystem so build output,
# vendored trees and a dirty worktree cannot change the answer -- the same
# reasoning internal/api/apierr_guard_test.go records.
#
# The scan covers every tracked *.go, NOT only *_test.go, and that is
# load-bearing rather than lazy: internal/testpolicy/resourcecensus walks
# every tracked source file from census.go, a non-test file its _test.go
# merely calls, so a test-files-only scan drops the single most repo-wide
# package in the tree.
source_tree_scanner_packages() {
    git -C "$REPO_ROOT" ls-files -z -- '*.go' \
        | xargs -0 grep -lEi 'repo_?root|ls-files|show-toplevel|ls-tree|go\.mod|runtime\.Caller|filepath\.Walk(Dir)?' \
        | xargs -n1 dirname \
        | sort -u \
        | sed 's|^|./|'
}

# The package the manifest header's load-bearing claim rests on:
# resourcecensus does its walking from census.go, a NON-test file, so it is
# precisely what a test-files-only scan would drop. Pinning it by name means
# a rename has to come back through this file and that prose together.
MANIFEST_SCAN_ANCHOR="./internal/testpolicy/resourcecensus"

# These anchors exercise the root-discovery forms that are easy to miss when
# the scan is reduced to just git-oriented names: a generator derived from its
# source file, a package-local tree walk, a module-root ascent, and git's tree
# listing. They guard the scanner rule itself; the manifest comparison below
# guards the resulting package set.
KNOWN_ROOT_FORM_SCANNERS=(
    "./cmd/gen-command-census"
    "./examples"
    "./internal/docgen"
    "./internal/logutil"
    "./internal/storebinding"
)

test_manifest_scanner_covers_known_root_forms() {
    local name="the always-run scanner recognizes every known repository-root form"
    local scanned scanner
    scanned="$(source_tree_scanner_packages)"
    for scanner in "${KNOWN_ROOT_FORM_SCANNERS[@]}"; do
        if ! printf '%s\n' "$scanned" | grep -qxF "$scanner"; then
            record_fail "$name" "the scan missed $scanner"
            return
        fi
    done
    record_pass "$name"
}

test_manifest_names_every_selected_source_tree_scanner() {
    # comm reports only what the scan found and the manifest lacks, so a scan
    # that finds NOTHING reports nothing missing and this case passes while
    # guarding nothing -- which would quietly retire the "membership is
    # mechanical" guarantee the manifest header stakes its Do-NOT-prune rule
    # on. Proven reachable, not theorized: breaking the marker pattern left
    # the pipeline printing `dirname: missing operand` to stderr and the
    # suite still green at 10 passed / 0 failed. The two guards below are
    # what refuse that, and they fail toward "this file is broken" rather
    # than toward a clean manifest.
    local name="the always-run manifest names every selected source-tree scanner"
    local scanned missing
    scanned="$(source_tree_scanner_packages)"
    if [ -z "$scanned" ]; then
        record_fail "$name" "the scan found no source-tree scanner candidates at all; the pipeline in this file is broken, not the manifest"
        return
    fi
    if ! printf '%s\n' "$scanned" | grep -qxF "$MANIFEST_SCAN_ANCHOR"; then
        record_fail "$name" "the scan missed $MANIFEST_SCAN_ANCHOR, which it must always find; the pipeline in this file is broken, not the manifest"
        return
    fi
    missing="$(comm -23 <(printf '%s\n' "$scanned") <(manifest_entries | sort))"
    if [ -n "$missing" ]; then
        record_fail "$name" "packages match the source-tree scanner rule but are absent from the manifest: ${missing//$'\n'/ }"
        return
    fi
    record_pass "$name"
}

test_manifest_regenerates_byte_for_byte() {
    local name="the always-run manifest regenerates byte-for-byte from the scanner"
    local scanned
    scanned="$(source_tree_scanner_packages)"
    if ! diff -u <(printf '%s\n' "$scanned") <(manifest_entries) >/dev/null; then
        record_fail "$name" "the scanner output differs from the manifest; regenerate it with the header command"
        return
    fi
    record_pass "$name"
}

test_manifest_entries_are_real_packages() {
    # A stale entry is not harmless: push-gate-select refuses to narrow at
    # all when the manifest names something `go list` does not know, so a
    # deleted package here silently returns every push to the full suite.
    local name="every always-run manifest entry is a real package"
    local entries out
    entries="$(manifest_entries)"
    if [ -z "$entries" ]; then
        record_fail "$name" "manifest is empty; a scoped gate would run no repo-wide scanner"
        return
    fi
    # $entries is a newline-separated package list and go list must receive
    # one argument per package. Quoting it passes the whole list as a single
    # import path, which go list rejects, so this case would then report every
    # manifest entry as stale rather than checking any of them.
    # shellcheck disable=SC2086
    if ! out="$(cd "$REPO_ROOT" && go list $entries 2>&1)"; then
        record_fail "$name" "go list rejected an entry: $(echo "$out" | head -3)"
        return
    fi
    record_pass "$name"
}

test_manifest_is_sorted_and_unique() {
    # The manifest is regenerated by a sorted pipeline recorded in its own
    # header. Pinning the order keeps a regeneration from producing a
    # reordering diff that hides a real membership change inside it.
    local name="the always-run manifest is sorted and free of duplicates"
    if ! diff -q <(manifest_entries) <(manifest_entries | sort) >/dev/null; then
        record_fail "$name" "manifest is not sorted; regenerate it with the header's pipeline"
        return
    fi
    if [ "$(manifest_entries | sort -u | wc -l)" -ne "$(manifest_entries | wc -l)" ]; then
        record_fail "$name" "manifest lists a package twice"
        return
    fi
    record_pass "$name"
}

echo "push-gate scope wiring"
test_scoped_push_runs_the_fast_suite_over_the_selection
test_full_decision_leaves_scope_unset
test_none_decision_runs_no_suite
test_unrecognized_decision_runs_full_suite
test_missing_selector_runs_full_suite
test_ownership_guard_still_runs_on_a_scoped_push
test_selector_receives_the_pre_push_records

echo
echo "through the real Makefile"
test_push_scope_reaches_the_runner_through_make
test_affected_scope_reaches_the_runner_through_make
test_affected_full_decision_runs_unscoped
test_affected_unrecognized_decision_runs_unscoped
test_affected_none_decision_runs_nothing

echo
echo "always-run manifest"
test_manifest_scanner_covers_known_root_forms
test_manifest_names_every_selected_source_tree_scanner
test_manifest_regenerates_byte_for_byte
test_manifest_entries_are_real_packages
test_manifest_is_sorted_and_unique

echo
echo "passed: $pass  failed: $fail"
[ "$fail" -eq 0 ]
