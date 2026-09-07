#!/usr/bin/env bash
# gate-sweep — evaluate and close pending gates.
#
# Runs as an exec order (no LLM, no agent, no wisp). bd dispatches per
# type. The `|| true` on the gh-gate line is load-bearing: bd shells
# out to `gh` for gh:run / gh:pr gates, and fresh cities without
# `gh auth` would otherwise fail this order on every 30s cooldown.
# bd's combined output reaches the controller log only on non-zero
# exit (see the `if err != nil` branch of `dispatchOne` in
# cmd/gc/order_dispatch.go), so suppressing gh-gate errors also
# hides real bd errors on that line — diagnose by hand.
#
# Timer-gate evaluation is local-only (no `gh` shell-out, no auth
# requirement) so its failures should propagate to the controller log.
# `|| true` would silently mask real bd regressions in timer-gate
# evaluation — see #1734 for the rationale.
#
# Bead-type gates are skipped: in beads v1.0.2, checkBeadGate is
# hard-coded to fail because cross-rig routing was removed upstream.
# Restore `gc bd gate check --type=bead --escalate` when beads adds it back.
#
# EVERY STORE IS SWEPT EXPLICITLY, and that is the whole shape of this
# script. A bare `gc bd gate check` is HQ-scoped from the city cwd, so
# for as long as this order ran two bare calls, a timer gate in any RIG
# store had no closer at all. Measured on 2026-09-07 (ci-q4fyku) with a
# two-arm experiment, one variable: two timer gates created 60s apart
# with identical 1m timeouts, one per store, HQ closed 134s past expiry
# and the rig arm was still open 15 minutes later. The bead that
# surfaced it, as-oavp, sat unresolved 2h37m past its 6h timeout across
# roughly 310 passes of this order.
#
# WHY `-C <path>` AND NOT `--rig <name>`. The sibling
# renudge-stale-human-gates.sh walks scopes with `--rig`, and matching it
# would be the more obvious choice. It is rejected because `--rig` is a
# gc GLOBAL flag and has to precede the subcommand, which makes `$1`
# `--rig` rather than `bd` -- and the `gc` wrapper in _bd_trace.sh keys
# its trace on exactly that word. Under `--rig`, every rig-scoped call
# would drop out of $GC_BD_TRACE_JSON, silently, for precisely the
# scopes this change adds. Given bd's output reaches the controller log
# only on non-zero exit (above), the trace is the other half of the
# diagnostics and is not worth trading for symmetry with a sibling.
# `-C` scopes identically -- verified against both forms of
# `gc bd gate list` on the live city.
#
# TWO PROPERTIES THE LOOP HOLDS AT ONCE, and they pull against each
# other. A timer failure must still exit non-zero (#1734, above), but one
# unreachable rig must NOT stop the stores after it -- that turns a
# single broken rig into a fleet-wide gate outage, which is the same
# failure being fixed here. So a timer failure is recorded and re-raised
# after the walk instead of aborting it. Pinned by
# TestGateSweepContinuesPastAFailingStoreAndStillExitsNonzero.
set -euo pipefail

# Trace bd invocations to $GC_BD_TRACE when set (no-op otherwise).
__SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
. "$__SCRIPT_DIR/_bd_trace.sh" "gate-sweep"

# Stores to sweep: HQ first, as the bare call, then every non-hq rig by
# path. The hq pseudo-rig is EXCLUDED because the bare call already
# covers it -- addressing it again would double-escalate every HQ gate on
# every pass.
#
# A rig with no resolved path -- declared in city.toml but never cloned,
# which reports "" -- is excluded for the SAME reason rather than for the
# obvious one. It does not become a `gc bd gate check -C ""`: the loop's
# own emptiness test below intercepts it first and treats the rig as HQ,
# so the cost is a second HQ sweep and the double-escalation above, not a
# check against the current directory. The guard is kept anyway, because
# relying on the loop to absorb it makes the two tests load-bearing
# together and neither says so. Both omissions are pinned by
# TestGateSweepSkipsStoresItCannotAddress, which asserts the hq sweep
# COUNT for exactly this reason.
#
# A failure to enumerate rigs leaves the list at HQ alone rather than
# aborting: degrading to the old behavior still closes HQ gates, while
# exiting here would close none.
STORES=("")
RIGS_JSON="$(gc rig list --json 2>/dev/null || true)"
if [ -n "$RIGS_JSON" ]; then
    while IFS= read -r store_path; do
        [ -n "$store_path" ] || continue
        STORES+=("$store_path")
    done < <(printf '%s' "$RIGS_JSON" \
        | jq -r '(.rigs // [])[] | select(.hq != true) | .path // ""' \
            2>/dev/null || true)
fi

TIMER_FAILED=0
for store in "${STORES[@]}"; do
    # An empty element is HQ and takes no scope flag at all. Quoting a
    # bare "" into the argv instead would pass gc an empty positional and
    # be rejected.
    if [ -n "$store" ]; then
        gc bd gate check -C "$store" --type=timer --escalate || TIMER_FAILED=1
        gc bd gate check -C "$store" --type=gh --escalate || true
    else
        gc bd gate check --type=timer --escalate || TIMER_FAILED=1
        gc bd gate check --type=gh --escalate || true
    fi
done

exit "$TIMER_FAILED"
