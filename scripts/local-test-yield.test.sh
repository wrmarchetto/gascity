#!/usr/bin/env bash
#
# local-test-yield.test.sh -- job priority in scripts/test-local-parallel, read
# from the running jobs: a development run yields, a LOCAL_TEST_YIELD=0 run
# keeps the caller's priority, and any other value is refused before a job.
#
# Run: bash scripts/local-test-yield.test.sh (scripts/test-local-concurrency.sh
# runs it inside the local-concurrency-selftest job).
# hands off to: scripts/test-push-gate-select.sh, for the hook's opt-out.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOCAL_PARALLEL="$TEST_DIR/test-local-parallel"
INNER_LIB="$TEST_DIR/lib/inner-parallelism.sh"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 -- $2"; fail=$((fail + 1)); }
assert_eq() {
    if [[ "$2" == "$3" ]]; then record_pass "$1"
    else record_fail "$1" "got '$2', want '$3'"; fi
}
assert_true() { if "${@:2}"; then record_pass "$1"; else record_fail "$1" "expected true"; fi; }
assert_contains() {
    local name="$1" haystack="$2" needle="$3"
    if [[ "$haystack" == *"$needle"* ]]; then record_pass "$name"
    else record_fail "$name" "missing '$needle' in: $haystack"; fi
}

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

# --- job count ---

# Shrinking for host load is a yield too, so a merge-path run keeps the cpu
# and memory budget alone. Load 28 on 16 cpus is the 2-job floor otherwise.
JOB_COUNT="$TEST_DIR/test-local-job-count"
HUGE_MEM_KIB=$((64 * 1024 * 1024))
FIVE_JOBS_KIB=$((5 * 4 * 1024 * 1024))
# job_count <memory KiB> <env args...>
job_count() {
    local mem="$1"
    shift
    env "$@" GC_TEST_LOCAL_CPUS=16 GC_TEST_LOCAL_LOADAVG=28 \
        GC_TEST_LOCAL_MEMORY_KIB="$mem" "$JOB_COUNT"
}
assert_eq "jobs.development_run_shrinks_for_load" \
    "$(job_count "$HUGE_MEM_KIB" -u LOCAL_TEST_YIELD)" 2
assert_eq "jobs.merge_path_run_ignores_load" \
    "$(job_count "$HUGE_MEM_KIB" LOCAL_TEST_YIELD=0)" 16
assert_eq "jobs.merge_path_run_keeps_the_memory_budget" \
    "$(job_count "$FIVE_JOBS_KIB" LOCAL_TEST_YIELD=0)" 5

# The gates reach the count through the Makefile's $(shell), which sees only
# the environment make was started with.
make_jobs() {
    (cd "$TEST_DIR/.." && env "$@" GC_TEST_LOCAL_CPUS=16 \
        GC_TEST_LOCAL_MEMORY_KIB="$HUGE_MEM_KIB" GC_TEST_LOCAL_LOADAVG=28 \
        make -n test-fast-parallel 2>&1 |
        grep -o 'LOCAL_TEST_JOBS=[0-9]*' | head -1)
}
assert_eq "jobs.make_carries_the_opt_out_to_the_count" \
    "$(make_jobs -u LOCAL_TEST_JOBS LOCAL_TEST_YIELD=0)" "LOCAL_TEST_JOBS=16"
assert_eq "jobs.make_development_run_shrinks_for_load" \
    "$(make_jobs -u LOCAL_TEST_JOBS -u LOCAL_TEST_YIELD)" "LOCAL_TEST_JOBS=2"

echo
echo "local-test-yield tests: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
