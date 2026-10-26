#!/usr/bin/env bash
#
# merge-gate.test.sh -- make merge-gate runs build, vet, dashboard-ci and the
# fast suite once each, rebuilds dist/ before any Go step, and runs the
# dashboard checks beside the Go side rather than after it.
#
# Run: bash scripts/merge-gate.test.sh (scripts/test-local-concurrency.sh
# runs it inside the local-concurrency-selftest job).
# hands off to: the sweep's own run logs, for the wall time it buys.

set -uo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

pass=0; fail=0
record_pass() { echo "  ok   $1"; pass=$((pass + 1)); }
record_fail() { echo "  FAIL $1 -- $2"; fail=$((fail + 1)); }

# rendezvous <dir> <name> <my mark> <their mark>: a stub that waits up to
# 30 s for the other side to have started.
rendezvous() {
    cat <<STUB
#!/bin/sh
echo "start $2 \$*" >>"$1/calls.log"
: >"$1/marks/$3"
i=0
while [ ! -e "$1/marks/$4" ]; do
  i=\$((i + 1))
  [ "\$i" -gt 300 ] && { echo "$2 never ran beside the other side" >>"$1/calls.log"; exit 4; }
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
echo "start go \$*" >>"$dir/calls.log"
[ -f "$dir/internal/api/dashboardspa/dist/bundle" ] ||
  { echo "go \$* ran before dist was rebuilt" >>"$dir/calls.log"; exit 3; }
STUB
    cat >"$dir/bin/npm" <<STUB
#!/bin/sh
echo "start npm \$*" >>"$dir/calls.log"
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
    printf '#!/bin/sh\necho "start shared-tests" >>"%s/calls.log"\n' "$dir" \
        >"$dir/scripts/dashboard-shared-tests.sh"
    printf '#!/bin/sh\necho 2\n' >"$dir/scripts/test-local-job-count"
    chmod +x "$dir"/bin/* "$dir"/scripts/*
    printf '%s' "$dir"
}

# run_fixture <dir> <target>: make in the fixture, a caller's flags cleared.
run_fixture() {
    (cd "$1" && env MAKEFLAGS= MFLAGS= PATH="$1/bin:$PATH" \
        make --no-print-directory "$2") >"$1/run.log" 2>&1
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
    GATE="$(new_fixture)"
    if run_fixture "$GATE" merge-gate; then
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
    rm -rf "$GATE"
else
    record_fail "merge_gate.control_dashboard_ci_runs" \
        "$(tail -20 "$CONTROL/run.log")"
fi
rm -rf "$CONTROL"

echo
echo "merge-gate tests: $pass passed, $fail failed"
[[ "$fail" -eq 0 ]]
