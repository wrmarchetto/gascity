#!/usr/bin/env python3
"""scripts/canary-eval.py -- decide keep or revert for epic:agent-efficiency.

Compares one measurement window against the recorded baseline, per agent, and
exits nonzero when an agent's revert line is crossed.

The shape follows from one hazard the commissioning bead (gs-jbyc) names
outright: "a threshold chosen after the numbers are in is not a criterion, it
is a rationalization". So the margins live in a committed TOML file written
before the window opened, and the baseline side of every comparison is
RECOMPUTED from the stated baseline window rather than pinned as a literal --
a pinned figure is a second copy of a derived value and drifts from the log it
came from.

Three answers, and they are deliberately three rather than two:

  0  keep       every subject inside its line, every control held
  2  revert     at least one subject crossed its line; the report names them
  3  no verdict the run could not separate the regimes

Exit 3 is the whole reason this is a script and not a judgment call. A window
that is too thin, a control that moved, or an agent whose level plainly never
reached a running session all produce a comparison that cannot answer the
question, and the expensive mistake is reporting one of those as a pass --
that retires the epic's open question by silence.

Editing constraints:

  - No agent name appears in this file. Which agents are judged, on what
    metric and against what margin, is entirely the criteria file's business
    (AGENTS.md: zero hardcoded roles).
  - Every refusal path must be reachable from the criteria and the data alone.
    Do not add a flag that forces a verdict; the suite would then pin the flag
    rather than the rule.

scripts/canary_eval_test.go pins the arithmetic, the sample floors, the
control handling and all three exit codes.
"""

import argparse
import json
import sys
import tomllib
from collections import defaultdict
from datetime import datetime, timedelta, timezone

# --- exit codes ---

EXIT_KEEP = 0
EXIT_USAGE = 1
EXIT_REVERT = 2
EXIT_NO_VERDICT = 3

# The baseline document states its window in -05:00 and the day floors below
# count local days, so bead close timestamps are converted to this zone before
# they are bucketed. Bucketing on UTC instead straddles local days and blurs
# the very day-to-day spread the margins were derived from.
LOCAL = timezone(timedelta(hours=-5))

# Beads that are records of the machinery rather than units of delivered work.
# `gc.kind` marks molecule steps, wisps and workflow beads; the two labels mark
# order execution records, of which this city closes about 9,000 a day. Left in
# the denominator they would divide the fleet's token spend by a count that has
# nothing to do with how much work got done.
INFRASTRUCTURE_LABELS = ("order-tracking", "exec")
WORK_BEAD_TYPES = ("task", "bug", "chore", "spec")


def is_work_bead(bead):
    """Whether a bead record is a unit of delivered work."""
    if (bead.get("issue_type") or "") not in WORK_BEAD_TYPES:
        return False
    if (bead.get("metadata") or {}).get("gc.kind") is not None:
        return False
    labels = bead.get("labels") or []
    return not any(lab in labels for lab in INFRASTRUCTURE_LABELS)


def local_day(timestamp):
    """The local calendar day a bead closed on, or None if it never closed."""
    if not timestamp:
        return None
    return (
        datetime.fromisoformat(timestamp.replace("Z", "+00:00"))
        .astimezone(LOCAL)
        .date()
        .isoformat()
    )


def load_json(path):
    with open(path, "rb") as handle:
        return json.load(handle)


def load_usage(path):
    """Index one gc usage --by session --json report by session id.

    Returns {} for an absent path so a metric that needs no usage side -- an
    authored-bead rate, for instance -- does not force the caller to supply a
    report it will not read.
    """
    if not path:
        return {}
    doc = load_json(path)
    return {group["key"]: group for group in (doc.get("groups") or [])}


def load_beads(paths):
    """Concatenate several bd list --json dumps, keeping only work beads.

    Several paths because a bead store is per rig and `bd` is scoped by working
    directory: a fleet-wide reading is the union of every rig's store, and one
    store's dump silently answers for one rig alone.
    """
    out = []
    for path in paths or []:
        for bead in (load_json(path) or []):
            if is_work_bead(bead):
                out.append(bead)
    return out


class Sample:
    """One side of one agent's comparison, and what it was computed from."""

    def __init__(self):
        self.beads = 0
        self.days = set()
        self.invocations = 0.0
        self.output_tokens = 0.0
        self.cache_read_tokens = 0.0
        self.verdicts = 0
        self.failures = 0

    @property
    def invocations_per_bead(self):
        return self.invocations / self.beads if self.beads else None

    @property
    def output_per_invocation(self):
        return self.output_tokens / self.invocations if self.invocations else None

    @property
    def fail_rate(self):
        return self.failures / self.verdicts if self.verdicts else None


def measure_session_cost(beads, usage, agent_type):
    """Cost of the beads one agent type closed, joined through gc.session_id.

    A session that closed several beads bought all of them with the same
    invocations, so its cost is divided between them rather than charged to
    each. Charging in full would report a session that finished three beads as
    three times more expensive than one that finished one, which inverts the
    thing being measured.
    """
    sample = Sample()
    holders = defaultdict(list)
    for bead in beads:
        session_id = (bead.get("metadata") or {}).get("gc.session_id")
        if not session_id or session_id not in usage:
            continue
        group = usage[session_id]
        if agent_type not in (group.get("agent_types") or []):
            continue
        holders[session_id].append(bead)
    for session_id, held in holders.items():
        group = usage[session_id]
        share = 1.0 / len(held)
        for bead in held:
            sample.beads += 1
            day = local_day(bead.get("closed_at"))
            if day:
                sample.days.add(day)
            sample.invocations += (group.get("invocations") or 0) * share
            sample.output_tokens += (group.get("output_tokens") or 0) * share
            sample.cache_read_tokens += (group.get("cache_read_tokens") or 0) * share
    return sample


def measure_type_usage(usage, agent_type):
    """Every recorded session of one agent type, with no bead join.

    This is the witness side of a row rather than the metric side. It is
    separate from measure_session_cost because the agent whose level change
    carries the most risk here closes no beads at all -- it authors them -- so
    a witness read off the bead join would be silently absent for exactly the
    row that most needs one.
    """
    sample = Sample()
    for group in usage.values():
        if agent_type not in (group.get("agent_types") or []):
            continue
        sample.invocations += group.get("invocations") or 0
        sample.output_tokens += group.get("output_tokens") or 0
        sample.cache_read_tokens += group.get("cache_read_tokens") or 0
    return sample


def measure_authored(beads, author):
    """Verdicts and failures among the beads one actor authored.

    The metric for an agent whose output is other agents' work: its own token
    spend says how hard it thought, never whether it thought correctly. A bead
    with no gc.outcome is not counted either way -- an unrecorded verdict is
    not a pass, and treating it as one would let the rate fall whenever
    recording slipped.
    """
    sample = Sample()
    for bead in beads:
        if bead.get("created_by") != author:
            continue
        outcome = (bead.get("metadata") or {}).get("gc.outcome")
        if outcome is None:
            continue
        sample.verdicts += 1
        if outcome == "fail":
            sample.failures += 1
        day = local_day(bead.get("closed_at"))
        if day:
            sample.days.add(day)
    return sample


def format_value(metric, value):
    if value is None:
        return "-"
    if metric == "author_fail_rate":
        return f"{100 * value:.2f}%"
    return f"{value:,.2f}"


def evaluate_row(spec, baseline, window, baseline_witness, window_witness):
    """Judge one criteria row, returning (state, line, detail).

    state is one of: inside, crossed, insufficient, no-witness, no-baseline.
    Only a subject's "crossed" reverts; only a control's "crossed" voids. The
    caller owns that distinction so this function stays free of role semantics.
    """
    metric = spec["metric"]
    margin = float(spec["margin"])

    if metric == "invocations_per_bead":
        base_value = baseline.invocations_per_bead
        win_value = window.invocations_per_bead
        floor_key, floor = "min_beads", int(spec.get("min_beads", 0))
        have_base, have_win = baseline.beads, window.beads
    elif metric == "author_fail_rate":
        base_value = baseline.fail_rate
        win_value = window.fail_rate
        floor_key, floor = "min_verdicts", int(spec.get("min_verdicts", 0))
        have_base, have_win = baseline.verdicts, window.verdicts
    else:
        raise SystemExit(f"canary-eval: unknown metric {metric!r} in criteria")

    # The two sides are not the same size and never will be: the baseline is a
    # closed window that cannot grow while the canary window is twice its
    # length. A floor sized for the window is therefore unsatisfiable on the
    # baseline side, and the row reports no-baseline forever rather than
    # failing conservatively. The default is still the window's floor, so
    # omitting the key can only tighten, never loosen.
    baseline_floor = int(spec.get("min_baseline_" + floor_key[4:], floor))

    if base_value is None or have_base < baseline_floor:
        return ("no-baseline", None,
                f"baseline {floor_key}={have_base} under floor {baseline_floor}")
    if win_value is None or have_win < floor:
        return ("insufficient", None,
                f"window {floor_key}={have_win} under floor {floor}")

    min_days = int(spec.get("min_days", 0))
    if len(window.days) < min_days:
        return ("insufficient", None,
                f"window covered {len(window.days)} local days, floor {min_days}")

    line = base_value * (1.0 + margin)

    # The witness answers a different question from the line: did the change
    # this window is measuring actually reach the running sessions? Lowering an
    # agent's reasoning effort should not RAISE its output tokens per
    # invocation, and when it does -- past the row's own margin, so ordinary
    # jitter cannot trip it -- the likeliest reading is that the row never left
    # the old regime. A comparison against the wrong regime is worse than none,
    # so the row declines instead of answering.
    #
    # A witness that stays FLAT is deliberately not caught here. It is genuinely
    # ambiguous between "the level changed and cost nothing" and "the level
    # never changed", and only the live process command line separates those --
    # see the manual step in the canary document.
    if spec.get("witness") == "output_per_invocation_not_up":
        base_opi = baseline_witness.output_per_invocation
        win_opi = window_witness.output_per_invocation
        if base_opi and win_opi and win_opi > base_opi * (1.0 + margin):
            return ("no-witness", line,
                    f"output/invocation rose {base_opi:,.0f} -> {win_opi:,.0f}; "
                    "the row is probably not running the level under test")

    state = "crossed" if win_value > line else "inside"
    shown = format_value(metric, base_value), format_value(metric, win_value), \
        format_value(metric, line)
    return (state, line, f"{shown[0]} -> {shown[1]}, line {shown[2]}")


def main(argv=None):
    parser = argparse.ArgumentParser(
        description="Decide keep or revert for the agent-efficiency canary.")
    parser.add_argument("--criteria", required=True,
                        help="committed TOML naming each agent, its metric and its margin")
    parser.add_argument("--baseline-usage",
                        help="gc usage --by session --json over the baseline window")
    parser.add_argument("--window-usage",
                        help="gc usage --by session --json over the canary window")
    parser.add_argument("--baseline-beads", action="append", default=[],
                        help="bd list --json of beads closed in the baseline window (repeatable, one per rig)")
    parser.add_argument("--window-beads", action="append", default=[],
                        help="bd list --json of beads closed in the canary window (repeatable, one per rig)")
    parser.add_argument("--json", action="store_true", dest="as_json",
                        help="emit the report as JSON instead of a table")
    args = parser.parse_args(argv)

    with open(args.criteria, "rb") as handle:
        criteria = tomllib.load(handle)
    specs = criteria.get("agent", [])
    if not specs:
        print("canary-eval: criteria file names no agents", file=sys.stderr)
        return EXIT_USAGE

    baseline_usage = load_usage(args.baseline_usage)
    window_usage = load_usage(args.window_usage)
    baseline_beads = load_beads(args.baseline_beads)
    window_beads = load_beads(args.window_beads)

    rows = []
    reverts, voids = [], []
    for spec in specs:
        agent_type = spec["type"]
        role = spec.get("role", "report-only")
        metric = spec["metric"]
        if metric == "author_fail_rate":
            author = spec.get("author", agent_type)
            baseline = measure_authored(baseline_beads, author)
            window = measure_authored(window_beads, author)
        else:
            baseline = measure_session_cost(baseline_beads, baseline_usage, agent_type)
            window = measure_session_cost(window_beads, window_usage, agent_type)

        baseline_witness = measure_type_usage(baseline_usage, agent_type)
        window_witness = measure_type_usage(window_usage, agent_type)
        state, line, detail = evaluate_row(
            spec, baseline, window, baseline_witness, window_witness)
        if role == "subject":
            if state == "crossed":
                reverts.append(agent_type)
            elif state != "inside":
                voids.append(f"{agent_type}: {state} ({detail})")
        elif role == "control":
            # A control that moved says the fleet moved, so nothing this run
            # measured can be attributed to the level change. It voids rather
            # than reverts: reverting an agent on evidence that says nothing
            # about it is the error the control exists to prevent.
            if state != "inside":
                voids.append(f"{agent_type}: control {state} ({detail})")

        base_value = (baseline.fail_rate if metric == "author_fail_rate"
                      else baseline.invocations_per_bead)
        win_value = (window.fail_rate if metric == "author_fail_rate"
                     else window.invocations_per_bead)
        rows.append({
            "type": agent_type,
            "role": role,
            "metric": metric,
            "margin": float(spec["margin"]),
            "baseline": base_value,
            "window": win_value,
            "line": line,
            "state": state,
            "detail": detail,
            "baseline_beads": baseline.beads,
            "window_beads": window.beads,
            "baseline_verdicts": baseline.verdicts,
            "window_verdicts": window.verdicts,
            "window_days": len(window.days),
            "baseline_output_per_invocation": baseline_witness.output_per_invocation,
            "window_output_per_invocation": window_witness.output_per_invocation,
        })

    # A void outranks a revert. Both can be true at once -- a fleet-wide rise
    # crosses a subject's line and the control's together -- and in that case
    # the subject's crossing is exactly the reading the control says not to
    # trust.
    if voids:
        verdict, code = "no-verdict", EXIT_NO_VERDICT
    elif reverts:
        verdict, code = "revert", EXIT_REVERT
    else:
        verdict, code = "keep", EXIT_KEEP

    if args.as_json:
        print(json.dumps({
            "schema_version": "1",
            "verdict": verdict,
            "revert": reverts,
            "void": voids,
            "rows": rows,
        }, indent=1))
        return code

    print(f"{'agent':<26}{'role':<12}{'metric':<22}"
          f"{'baseline':>12}{'window':>12}{'line':>12}  state")
    for row in rows:
        print(f"{row['type']:<26}{row['role']:<12}{row['metric']:<22}"
              f"{format_value(row['metric'], row['baseline']):>12}"
              f"{format_value(row['metric'], row['window']):>12}"
              f"{format_value(row['metric'], row['line']):>12}  {row['state']}")
        print(f"{'':<26}{row['detail']}")
    print()
    print(f"VERDICT: {verdict}")
    for agent in reverts:
        print(f"  REVERT {agent} to its pre-epic level")
    for note in voids:
        print(f"  NO VERDICT {note}")
    return code


if __name__ == "__main__":
    sys.exit(main())
