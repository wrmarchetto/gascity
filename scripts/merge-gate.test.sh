#!/usr/bin/env bash
# merge-gate.test.sh -- make merge-gate runs build, vet, dashboard-ci and the
# fast suite once each and fails when any of them fails, rebuilds dist/ before
# any Go step, runs the dashboard checks beside the Go side, and runs the
# suite unscoped whatever GC_SCOPED_TEST_PACKAGES its caller exports or passes.
#
# Run: bash scripts/merge-gate.test.sh (scripts/test-local-concurrency.sh
# runs it inside the local-concurrency-selftest job).
# hands off to: the sweep's own run logs, for the wall time it buys. The stubs
# touch no tree, so a Go test reading what dashboard-ci rewrites mid-run is
# not represented (internal/api/dashboard_label_literal_gate_test.go).

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 -- $2"; fail=$((fail + 1)); }

# stub_head <dir> <name>: a stub's lines that log the call as "<name> <args>"
# and exit 7 when it matches the glob in <dir>/fail.
stub_head() {
    cat <<HEAD
call="$2\${1+ \$*}"
echo "start \$call" >>"$1/calls.log"
case "\$call" in \$(cat "$1/fail" 2>/dev/null))
  echo "fail \$call" >>"$1/calls.log"; exit 7 ;;
esac
HEAD
}

# rendezvous <dir> <name> <my mark> <their mark>: a stub that waits up to
# 30 s for the other side to have started.
rendezvous() {
    cat <<STUB
#!/bin/sh
$(stub_head "$1" "$2")
echo "scope $2 [\${GC_SCOPED_TEST_PACKAGES-<unset>}]" >>"$1/calls.log"
: >"$1/marks/$3"
i=0
while [ ! -e "$1/marks/$4" ]; do
  i=\$((i + 1))
  [ "\$i" -gt 300 ] && {
    echo "$2 never ran beside the other side" >>"$1/calls.log"; exit 4; }
  sleep 0.1
done
STUB
}

# A scratch tree holding the real Makefile, with stub go, npm and runner
# scripts that log each call. Paths are baked in because TEST_ENV's env -i
# strips the environment. The tree starts with no dist/, so a Go step ahead
# of the rebuild fails, and npm ci sleeps so an unordered Go step goes first.
new_fixture() {
    local dir
    dir="$(mktemp -d -p "${TMPDIR:-/var/tmp}")"
    mkdir -p "$dir/marks" "$dir/bin" "$dir/scripts" \
        "$dir/internal/api/dashboardspa/web"
    cp -f "$TEST_DIR/../Makefile" "$dir/Makefile"
    cat >"$dir/bin/go" <<STUB
#!/bin/sh
[ "\$1" = env ] && { echo stub; exit 0; }
$(stub_head "$dir" go)
[ -f "$dir/internal/api/dashboardspa/dist/bundle" ] ||
  { echo "go \$* ran before dist was rebuilt" >>"$dir/calls.log"; exit 3; }
STUB
    cat >"$dir/bin/npm" <<STUB
#!/bin/sh
$(stub_head "$dir" npm)
case "\$*" in
  "ci --silent") sleep 0.5 ;;
  "run build") mkdir -p frontend/dist && echo bundle >frontend/dist/bundle ;;
  "run lint") : >"$dir/marks/dashboard-started" ;;
esac
STUB
    # The suite and the dashboard's last check each wait for the other, so
    # a gate that runs its two sides in turn times out.
    rendezvous "$dir" suite suite-started dashboard-started \
        >"$dir/scripts/test-local-parallel"
    rendezvous "$dir" drift drift-started suite-started \
        >"$dir/scripts/check-artifact-drift.sh"
    printf '#!/bin/sh\n%s\n' "$(stub_head "$dir" shared-tests)" \
        >"$dir/scripts/dashboard-shared-tests.sh"
    printf '#!/bin/sh\necho 2\n' >"$dir/scripts/test-local-job-count"
    chmod +x "$dir"/bin/* "$dir"/scripts/*
    printf '%s' "$dir"
}

# run_fixture <dir> <target> [make args]: make in the fixture, a caller's
# flags cleared.
run_fixture() {
    (cd "$1" && env MAKEFLAGS= MFLAGS= PATH="$1/bin:$PATH" \
        make --no-print-directory "${@:2}") >"$1/run.log" 2>&1
}

# The sorted calls a run made, repetitions kept.
calls() { sed -n 's/^start //p' "$1/calls.log" 2>/dev/null | sort; }

# dashboard-ci alone, its drift stub told the suite already started, is the
# call set the gate must reproduce on its dashboard side.
CONTROL="$(new_fixture)"
: >"$CONTROL/marks/suite-started"
if run_fixture "$CONTROL" dashboard-ci; then
    want="$( { calls "$CONTROL"
        printf '%s\n' "go build ./..." "go vet ./..." "suite fast"; } | sort)"
    scope="./scope-leak-${RANDOM}$$"
    GATE="$(new_fixture)"
    # Both routes at once: a fix missing either one leaks a visible scope.
    if GC_SCOPED_TEST_PACKAGES="$scope" run_fixture "$GATE" merge-gate \
        "GC_SCOPED_TEST_PACKAGES=$scope"; then
        record_pass "merge_gate.passes_over_green_stubs"
    else
        record_fail "merge_gate.passes_over_green_stubs" \
            "$(cat "$GATE/calls.log" "$GATE/run.log" 2>/dev/null | tail -20)"
    fi
    got="$(calls "$GATE")"
    if [[ "$got" == "$want" ]]; then
        record_pass "merge_gate.runs_build_vet_suite_and_dashboard_ci_once_each"
    else
        record_fail "merge_gate.runs_build_vet_suite_and_dashboard_ci_once_each" \
            "made a different call set: got: ${got} want: ${want}"
    fi
    got_scope="$(sed -n 's/^scope suite //p' "$GATE/calls.log")"
    if [[ "$got_scope" == "[]" ]]; then
        record_pass "merge_gate.suite_runs_unscoped_under_a_callers_scope"
    else
        record_fail "merge_gate.suite_runs_unscoped_under_a_callers_scope" \
            "the suite saw '${got_scope}', the caller passed ${scope} both ways"
    fi
    rm -rf "$GATE"
else
    record_fail "merge_gate.control_dashboard_ci_runs" \
        "$(tail -20 "$CONTROL/run.log")"
fi
rm -rf "$CONTROL"

# Each child failing alone fails the gate, through both makes. Every mark is
# set ahead so no rendezvous waits on a side the failure stopped.
while read -r name glob; do
    FAILING="$(new_fixture)"
    : >"$FAILING/marks/suite-started"
    : >"$FAILING/marks/dashboard-started"
    : >"$FAILING/marks/drift-started"
    printf '%s\n' "$glob" >"$FAILING/fail"
    run_fixture "$FAILING" merge-gate </dev/null
    status=$?
    if ! grep -q '^fail ' "$FAILING/calls.log" 2>/dev/null; then
        record_fail "merge_gate.fails_when_${name}_fails" \
            "no call matched '${glob}': $(calls "$FAILING" | tr '\n' ',')"
    elif [[ "$status" -eq 0 ]]; then
        record_fail "merge_gate.fails_when_${name}_fails" \
            "the gate exited 0 over a failing '${glob}'"
    else
        record_pass "merge_gate.fails_when_${name}_fails"
    fi
    rm -rf "$FAILING"
done <<'CASES'
go_build                  go build *
go_vet                    go vet *
the_suite                 suite *
a_dashboard_check         shared-tests
the_dashboard_drift_check drift --label dashboard bundle *
CASES

echo
echo "merge-gate tests: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
