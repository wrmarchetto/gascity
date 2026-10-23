#!/usr/bin/env bash
#
# test-local-concurrency.sh — unit tests for load-aware outer job counting
# (scripts/test-local-job-count) and GOFLAGS=-p= inner-test-binary
# parallelism (scripts/lib/inner-parallelism.sh), plus static assertions
# that the inner-parallelism piece is wired into scripts/test-local-parallel
# correctly (ga-04m84s).
#
# Part A exercises scripts/test-local-job-count as a real subprocess, since
# its behavior spans multiple detection functions and env-var seams already
# tested that way. Part B sources scripts/lib/inner-parallelism.sh directly
# and calls gc_inner_parallelism in-process — the pure-arithmetic half is
# extracted into a sourceable lib specifically so this self-test (itself one
# of fast's own jobs) never has to shell out to the real, heavyweight
# scripts/test-local-parallel end-to-end. Static wiring assertions cover
# that the lib is actually plumbed into test-local-parallel: sourced,
# invoked, exported into GOFLAGS, documented in usage(), reported in the
# per-run echo, and self-tested from both the fast) and full) job lists.
#
# Coverage: outer-job load subtraction (zero/mid/saturating load), the
# min_auto_jobs=2 floor, a small machine skipping load adjustment
# entirely, fractional-load truncation (not rounding), a malformed
# GC_TEST_LOCAL_LOADAVG failing by name, a live-host regression guard that
# the default path actually reads /proc/loadavg (skipped when strace is
# unavailable), inner-parallelism arithmetic (clean division, the real
# ga-04m84s repro numbers, job-count-exceeds-outer-jobs, the trivial 1x1
# case, the GC_TEST_INNER_P override, a malformed GC_TEST_INNER_P failing by
# name), and the test-local-parallel wiring described above.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOB_COUNT="$TEST_DIR/test-local-job-count"
LOCAL_PARALLEL="$TEST_DIR/test-local-parallel"
INNER_LIB="$TEST_DIR/lib/inner-parallelism.sh"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 — $2"; fail=$((fail + 1)); }

assert_eq() {
    local name="$1" got="$2" want="$3"
    if [[ "$got" == "$want" ]]; then record_pass "$name"
    else record_fail "$name" "got '$got', want '$want'"; fi
}
assert_true()  { if "${@:2}"; then record_pass "$1"; else record_fail "$1" "expected true"; fi; }
assert_contains() {
    local name="$1" haystack="$2" needle="$3"
    if [[ "$haystack" == *"$needle"* ]]; then record_pass "$name"
    else record_fail "$name" "missing '$needle' in: $haystack"; fi
}

# A huge, non-binding memory pin so every Part A case below exercises
# load-awareness alone — never accidentally gated by the real host's live
# /proc/meminfo or cgroup budget.
HUGE_MEM_KIB=$((64 * 1024 * 1024))

# ============================================================
# Part A — scripts/test-local-job-count (real subprocess, pinned cpus/memory)
# ============================================================

GOT="$(GC_TEST_LOCAL_CPUS=16 GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=0 "$JOB_COUNT")"
assert_eq "loadavg.zero_load_unchanged" "$GOT" "16"

GOT="$(GC_TEST_LOCAL_CPUS=16 GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=10 "$JOB_COUNT")"
assert_eq "loadavg.subtracts_from_cpus" "$GOT" "6"

GOT="$(GC_TEST_LOCAL_CPUS=16 GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=28 "$JOB_COUNT")"
assert_eq "loadavg.floors_at_min_auto_jobs" "$GOT" "2"

GOT="$(GC_TEST_LOCAL_CPUS=4 GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=28 "$JOB_COUNT")"
assert_eq "loadavg.small_machine_skips_load_adjustment" "$GOT" "4"

GOT="$(GC_TEST_LOCAL_CPUS=16 GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=3.9 "$JOB_COUNT")"
assert_eq "loadavg.truncates_fractional_load" "$GOT" "13"

MALFORMED_OUT="$(GC_TEST_LOCAL_CPUS=16 GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=abc "$JOB_COUNT" 2>&1)"
MALFORMED_RC=$?
assert_true "loadavg.malformed_nonzero_exit" test "$MALFORMED_RC" -ne 0
assert_contains "loadavg.malformed_names_var" "$MALFORMED_OUT" "GC_TEST_LOCAL_LOADAVG"

assert_true "loadavg.script_defines_min_auto_jobs_2" grep -qE 'min_auto_jobs=2' "$JOB_COUNT"
assert_true "loadavg.script_references_seam" grep -q 'GC_TEST_LOCAL_LOADAVG' "$JOB_COUNT"

# Regression guard: the default (no-override) path must actually read
# /proc/loadavg, mirroring how detect_memory_kib is already proven to read
# /proc/meminfo. Skipped gracefully where strace is unavailable (containers
# without CAP_SYS_PTRACE, macOS) rather than failing the whole suite on an
# environment gap unrelated to the feature itself. The cpu seam is pinned
# above the small-machine threshold because test-local-job-count skips
# load-awareness entirely at cpus <= min_auto_jobs*2 — an unpinned probe
# inherits the real host's core count and so false-fails on a small host.
# GC_TEST_LOCAL_LOADAVG stays unset, which is what gives the guard its
# teeth: it still proves the default path reads /proc/loadavg.
if command -v strace >/dev/null 2>&1; then
    # Captured into a variable rather than piped live into grep: a piped
    # `grep -q` closes its end of the pipe as soon as it finds a match, and
    # under pipefail that early close can race strace's own exit — SIGPIPEing
    # strace mid-write turns into a spurious pipeline failure even though the
    # match was genuinely found. Capturing first removes the race entirely.
    STRACE_OUT="$(GC_TEST_LOCAL_CPUS=16 strace -f -e trace=%file -- "$JOB_COUNT" 2>&1 >/dev/null || true)"
    if [[ "$STRACE_OUT" == *"/proc/loadavg"* ]]; then
        record_pass "loadavg.default_path_opens_proc_loadavg"
    else
        record_fail "loadavg.default_path_opens_proc_loadavg" "/proc/loadavg not opened by the default (no-override) path"
    fi
else
    echo "  skip loadavg.default_path_opens_proc_loadavg — strace not installed"
fi

# ============================================================
# Part B — scripts/lib/inner-parallelism.sh (sourced in-process)
# ============================================================

if [[ -r "$INNER_LIB" ]]; then
    # shellcheck source=lib/inner-parallelism.sh disable=SC1091
    . "$INNER_LIB"
fi
assert_true "inner_p.lib_file_exists" test -r "$INNER_LIB"

GOT="$(gc_inner_parallelism 16 4 2>/dev/null)"
assert_eq "inner_p.clean_division" "$GOT" "4"

GOT="$(gc_inner_parallelism 16 9 2>/dev/null)"
assert_eq "inner_p.matches_ga_04m84s_repro_numbers" "$GOT" "1"

GOT="$(gc_inner_parallelism 4 9 2>/dev/null)"
assert_eq "inner_p.job_count_exceeds_outer_jobs" "$GOT" "1"

GOT="$(gc_inner_parallelism 1 1 2>/dev/null)"
assert_eq "inner_p.trivial_single_job" "$GOT" "1"

GOT="$(GC_TEST_INNER_P=7 gc_inner_parallelism 16 9 2>/dev/null)"
assert_eq "inner_p.explicit_override_wins" "$GOT" "7"

MALFORMED_INNER_OUT="$(GC_TEST_INNER_P=abc gc_inner_parallelism 16 9 2>&1)"
MALFORMED_INNER_RC=$?
assert_true "inner_p.malformed_override_nonzero_exit" test "$MALFORMED_INNER_RC" -ne 0
assert_contains "inner_p.malformed_override_names_var" "$MALFORMED_INNER_OUT" "GC_TEST_INNER_P"

# ============================================================
# Static wiring assertions against scripts/test-local-parallel
# ============================================================

assert_true "wiring.sources_inner_parallelism_lib" grep -q 'lib/inner-parallelism.sh' "$LOCAL_PARALLEL"
assert_true "wiring.calls_gc_inner_parallelism"     grep -q 'gc_inner_parallelism'     "$LOCAL_PARALLEL"
assert_true "wiring.exports_goflags_dash_p"         grep -qE 'GOFLAGS=.*-p='           "$LOCAL_PARALLEL"
assert_true "wiring.usage_mentions_inner_p_seam"    grep -q 'GC_TEST_INNER_P'          "$LOCAL_PARALLEL"

echo_line="$(grep -n '^echo "Running' "$LOCAL_PARALLEL" | head -1 | cut -d: -f1)"
if [[ -n "$echo_line" ]]; then
    ECHO_TEXT="$(sed -n "${echo_line}p" "$LOCAL_PARALLEL")"
    assert_contains "wiring.echo_reports_inner_p" "$ECHO_TEXT" "inner_p="
else
    record_fail "wiring.echo_reports_inner_p" "no 'Running ... jobspecs' echo line found in $LOCAL_PARALLEL"
fi

# add_local_concurrency_selftest_job must be called from inside BOTH the
# fast) and full) case blocks — line-ranged the same way the push-gate
# precedent isolates a case block, so a call sitting in some other block (or
# only one of the two) can't false-positive a bare whole-file grep.
fast_start="$(grep -n '^  fast)' "$LOCAL_PARALLEL" | head -1 | cut -d: -f1)"
fast_end="$(grep -n '^  cmd-gc-process)' "$LOCAL_PARALLEL" | head -1 | cut -d: -f1)"
if [[ -n "$fast_start" && -n "$fast_end" ]]; then
    FAST_BLOCK="$(sed -n "${fast_start},${fast_end}p" "$LOCAL_PARALLEL")"
    assert_contains "wiring.fast_case_calls_selftest" "$FAST_BLOCK" "add_local_concurrency_selftest_job"
else
    record_fail "wiring.fast_case_calls_selftest" "could not locate the fast) case block in $LOCAL_PARALLEL"
fi

full_start="$(grep -n '^  full)' "$LOCAL_PARALLEL" | head -1 | cut -d: -f1)"
full_end="$(grep -n '^  \*)' "$LOCAL_PARALLEL" | head -1 | cut -d: -f1)"
if [[ -n "$full_start" && -n "$full_end" ]]; then
    FULL_BLOCK="$(sed -n "${full_start},${full_end}p" "$LOCAL_PARALLEL")"
    assert_contains "wiring.full_case_calls_selftest" "$FULL_BLOCK" "add_local_concurrency_selftest_job"
else
    record_fail "wiring.full_case_calls_selftest" "could not locate the full) case block in $LOCAL_PARALLEL"
fi

# --- part c: job priority, read from the running jobs ---

# The real runner and its libs in a scratch repo. Every job it starts in a
# cmd/gc-only scoped fast run is a stub that appends its own niceness and I/O
# class, read from the process table, to <dir>/priority.log. The log path is
# baked in because the runner's env -i strips the environment.
PRIO_JOBS="test-go-test-shard test-local-concurrency.sh test-push-gate-lock.sh test-push-gate-select.sh"
new_priority_fixture() {
    local dir job
    dir="$(mktemp -d -p /var/tmp)"
    mkdir -p "$dir/scripts/lib" "$dir/bin"
    cp -f "$LOCAL_PARALLEL" "$dir/scripts/test-local-parallel"
    cp -f "$TEST_DIR/lib/test-slice.sh" "$INNER_LIB" "$dir/scripts/lib/"
    cp -f "$TEST_DIR/push-gate-lock-lib.sh" "$dir/scripts/"
    printf '#!/usr/bin/env bash\necho 2\n' >"$dir/scripts/test-local-job-count"
    for job in $PRIO_JOBS; do
        cat >"$dir/scripts/$job" <<STUB
#!/usr/bin/env bash
ni="\$(ps -o ni= -p \$\$ | tr -d ' ')"
io="\$(ionice -p \$\$ 2>/dev/null | cut -d: -f1)"
echo "$job ni=\$ni io=\$io" >>"$dir/priority.log"
STUB
    done
    printf '#!/usr/bin/env bash\nexit 0\n' >"$dir/bin/go"
    chmod +x "$dir"/scripts/test-* "$dir/bin/go"
    printf '%s' "$dir"
}

# run_priority_fixture <dir> <env args...>: one fast run, cap and slice off.
run_priority_fixture() {
    local dir="$1"
    shift
    (cd "$dir" && env "$@" PATH="$dir/bin:$PATH" GC_TEST_NO_SLICE=1 \
        GC_PUSH_GATE_NO_CAP=1 GC_SCOPED_TEST_PACKAGES=./cmd/gc \
        CMD_GC_PROCESS_TOTAL=1 LOCAL_TEST_JOBS=2 \
        ./scripts/test-local-parallel fast) >"$dir/run.log" 2>&1
}

# check_priority_arm <name> <dir> <want "ni=N io=CLASS">: every job, once each.
check_priority_arm() {
    local name="$1" dir="$2" want="$3" job got expected=""
    for job in $PRIO_JOBS; do
        expected+="$job $want"$'\n'
    done
    got="$(sort "$dir/priority.log" 2>/dev/null)"
    if [[ "$got"$'\n' != "$expected" ]]; then
        record_fail "$name" "want: ${expected} got: ${got:-no job ran: $(tail -3 "$dir/run.log")}"
        return
    fi
    record_pass "$name"
}

# A merge-path run keeps the caller's priority, and a development run lowers
# it, so both are measured against this shell's own.
base_ni="$(ps -o ni= -p $$ | tr -d ' ')"
base_io="$(ionice -p $$ 2>/dev/null | cut -d: -f1)"
dev_ni=$((base_ni + 10 > 19 ? 19 : base_ni + 10))
dev_io="$(command -v ionice >/dev/null 2>&1 && echo idle)"
if [[ "$dev_ni" -eq "$base_ni" ]]; then
    record_fail "priority.observable" "this shell runs at nice ${base_ni}, so +10 is not observable"
else
    PRIO_DIR="$(new_priority_fixture)"
    run_priority_fixture "$PRIO_DIR" -u LOCAL_TEST_YIELD
    check_priority_arm "priority.development_run_yields" "$PRIO_DIR" "ni=${dev_ni} io=${dev_io}"
    rm -rf "$PRIO_DIR"

    PRIO_DIR="$(new_priority_fixture)"
    run_priority_fixture "$PRIO_DIR" LOCAL_TEST_YIELD=0
    check_priority_arm "priority.merge_path_run_does_not_yield" "$PRIO_DIR" "ni=${base_ni} io=${base_io}"
    rm -rf "$PRIO_DIR"
fi

# not a word: set -u arithmetic refuses one with or without the guard
PRIO_DIR="$(new_priority_fixture)"
run_priority_fixture "$PRIO_DIR" LOCAL_TEST_YIELD=2
PRIO_RC=$?
assert_true "priority.malformed_nonzero_exit" test "$PRIO_RC" -ne 0
assert_contains "priority.malformed_names_var" "$(cat "$PRIO_DIR/run.log")" "LOCAL_TEST_YIELD"
assert_true "priority.malformed_runs_no_job" test ! -e "$PRIO_DIR/priority.log"
rm -rf "$PRIO_DIR"

echo
echo "local-concurrency tests: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
