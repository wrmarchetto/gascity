#!/usr/bin/env python3
"""Replay scripts/push-gate-select over landed branches, and summarize it.

`replay` asks the selector what each sweep landing's branch tip would run
from its fork point off main, in a detached worktree under /var/tmp, and
writes one JSON line per landing. `summarize` prints the decision rates, why
each full decision widened, and each scoped selection's share of summed
package time from `go test -json` logs of `make test`. Figures and re-run
commands: TESTING.md, "What the push gate's selection would save".
"""

from __future__ import annotations

import argparse
import collections
import importlib.machinery
import importlib.util
import json
import os
import re
import statistics
import subprocess
import sys
import tempfile
import time

SCRIPTS = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, SCRIPTS)

import goaffected  # noqa: E402

MODULE = "github.com/gastownhall/gascity"
MERGE_SUBJECT = re.compile(r"^merge: (\S+) \(bead (\S+)\)")


def load_selector():
    loader = importlib.machinery.SourceFileLoader(
        "push_gate_select", os.path.join(SCRIPTS, "push-gate-select")
    )
    spec = importlib.util.spec_from_loader("push_gate_select", loader)
    module = importlib.util.module_from_spec(spec)
    loader.exec_module(module)
    return module


def git(repo: str, *args: str) -> str:
    return subprocess.run(
        ["git", *args], cwd=repo, check=True, stdout=subprocess.PIPE, text=True
    ).stdout.strip()


def landings(repo: str, since: str, until: str) -> list[dict]:
    rows = []
    log = git(
        repo, "log", "--first-parent", "--format=%H%x09%P%x09%cI%x09%s",
        "main", f"--since={since}", f"--until={until}",
    )
    for line in log.splitlines():
        sha, parents, when, subject = line.split("\t", 3)
        parents = parents.split()
        match = MERGE_SUBJECT.match(subject)
        if match and len(parents) == 2:
            rows.append({
                "merge": sha, "tip": parents[1], "when": when,
                "branch": match.group(1), "bead": match.group(2),
                "base": git(repo, "merge-base", parents[0], parents[1]),
            })
    rows.reverse()
    return rows


def select(selector, row: dict) -> None:
    """Fill row with the selector's decision for row's tip from row's base."""
    try:
        records = selector.changed_records([(row["tip"], row["base"])])
        row["changed"] = len(records)
        row["go_changed"] = sum(
            1 for _status, path in records if goaffected.is_go_build_input(path)
        )
        if not records:
            row["decision"] = "none"
            return
        inventory = goaffected.package_inventory("go")
        row["graph_packages"] = len(inventory["arg_by_import"])
        if not selector.classify_paths(records, inventory):
            row["decision"] = "none"
            return
        closure = goaffected.affected_package_args(records, "go", inventory)
        always = selector.always_run_packages(inventory)
        row["decision"] = "scoped" if closure else "none"
        row["closure_pkgs"] = closure
        row["selected"] = len(set(closure) | set(always))
    except goaffected.SelectionError as error:
        row["decision"] = "full"
        row["reason"] = str(error)


def replay(args: argparse.Namespace) -> int:
    repo = os.path.abspath(args.repo)
    selector = load_selector()
    rows = landings(repo, args.since, args.until)
    if not rows:
        print("no sweep merges in that window", file=sys.stderr)
        return 1
    worktree = tempfile.mkdtemp(prefix="push-gate-replay.", dir="/var/tmp")
    os.rmdir(worktree)
    git(repo, "worktree", "add", "-q", "--detach", worktree, rows[0]["tip"])
    try:
        with open(args.out, "w", encoding="utf-8") as out:
            for row in rows:
                git(worktree, "checkout", "-q", "--detach", "--force",
                    row["tip"])
                started = time.monotonic()
                os.chdir(worktree)
                try:
                    select(selector, row)
                finally:
                    os.chdir(repo)
                row["select_s"] = round(time.monotonic() - started, 2)
                out.write(json.dumps(row, sort_keys=True) + "\n")
    finally:
        git(repo, "worktree", "remove", "--force", worktree)
    return 0


def quantile(values: list[float], p: float) -> float:
    """Linear-interpolation percentile, numpy's default method."""
    ordered = sorted(values)
    k = (len(ordered) - 1) * p
    low = int(k)
    high = min(low + 1, len(ordered) - 1)
    return ordered[low] + (ordered[high] - ordered[low]) * (k - low)


def package_times(paths: list[str]) -> dict[str, float]:
    """Mean package-level Elapsed across go test -json logs."""
    samples: dict[str, list[float]] = collections.defaultdict(list)
    for path in paths:
        final: dict[str, float] = {}
        with open(path, encoding="utf-8", errors="replace") as handle:
            for line in handle:
                try:
                    event = json.loads(line)
                except ValueError:
                    continue
                if (
                    event.get("Test") is None
                    and event.get("Action") in ("pass", "fail", "skip")
                    and "Elapsed" in event
                ):
                    final[event["Package"]] = event["Elapsed"]
        for package, elapsed in final.items():
            samples[package].append(elapsed)
    return {package: statistics.mean(v) for package, v in samples.items()}


def import_path(arg: str) -> str:
    return MODULE if arg == "." else MODULE + arg[1:]


def widening_kind(reason: str) -> str:
    if "outside the Go package graph" not in reason:
        return reason
    path = reason.split("'")[1]
    if "/testdata/" in path or path.startswith("testdata/"):
        return "outside the graph: a testdata/ file"
    if "/" not in path:
        return f"outside the graph: {path} at the root"
    return f"outside the graph: under {path.split('/')[0]}/"


def summarize(args: argparse.Namespace) -> int:
    with open(args.rows, encoding="utf-8") as handle:
        rows = [json.loads(line) for line in handle if line.strip()]
    rows = [r for r in rows if r["when"] >= args.since]
    with open(args.manifest, encoding="utf-8") as handle:
        manifest = [
            line.strip() for line in handle
            if line.strip() and not line.strip().startswith("#")
        ]
    times = package_times(args.test_json)
    total = sum(times.values())
    floor = sum(times.get(import_path(arg), 0.0) for arg in manifest)

    print(f"landings: {len(rows)}, {rows[0]['when']} to {rows[-1]['when']}")
    print("decisions:", dict(collections.Counter(r["decision"] for r in rows)))
    go_rows = [r for r in rows if r.get("go_changed")]
    go_decisions = collections.Counter(r["decision"] for r in go_rows)
    print(f"changing a Go build input: {len(go_rows)}, {dict(go_decisions)}, "
          f"full {100 * go_decisions['full'] / max(len(go_rows), 1):.1f}%")
    print("full decisions by the first path or fault that widened:")
    reasons = collections.Counter(
        widening_kind(r["reason"]) for r in rows if r["decision"] == "full"
    )
    for reason, count in reasons.most_common():
        print(f"  {count:4d}  {reason}")

    print(f"\npackage time: {len(args.test_json)} log(s), "
          f"{len(times)} packages, {total:.0f}s summed")
    print(f"always-run manifest, {len(manifest)} entries: {floor:.0f}s, "
          f"{100 * floor / total:.1f}%")
    scoped = [r for r in rows if r["decision"] == "scoped"]
    if not scoped:
        return 0
    def share(args_: set[str]) -> float:
        return 100 * sum(times.get(import_path(a), 0.0) for a in args_) / total

    shares = [share(set(r["closure_pkgs"]) | set(manifest)) for r in scoped]
    closure_shares = [share(set(r["closure_pkgs"])) for r in scoped]
    sizes = [r["selected"] for r in scoped]
    print(f"scoped: {len(scoped)}, packages selected median "
          f"{quantile(sizes, 0.5):.0f}, p90 {quantile(sizes, 0.9):.0f}, "
          f"closure reaches ./cmd/gc in "
          f"{sum('./cmd/gc' in r['closure_pkgs'] for r in scoped)}")
    for label, values in (("selected", shares),
                          ("closure alone", closure_shares)):
        print(f"  share of summed package time, {label}: median "
              f"{quantile(values, 0.5):.1f}%, "
              f"p10 {quantile(values, 0.1):.1f}%, "
              f"p90 {quantile(values, 0.9):.1f}%")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    commands = parser.add_subparsers(dest="command", required=True)
    run = commands.add_parser("replay")
    run.add_argument("--repo", default=".")
    run.add_argument("--since", required=True)
    run.add_argument("--until", required=True)
    run.add_argument("--out", required=True)
    report = commands.add_parser("summarize")
    report.add_argument("--rows", required=True)
    report.add_argument("--manifest", required=True)
    report.add_argument("--since", default="",
                        help="ISO time, rows before it are left out")
    report.add_argument("test_json", nargs="+")
    args = parser.parse_args()
    return replay(args) if args.command == "replay" else summarize(args)


if __name__ == "__main__":
    sys.exit(main())
