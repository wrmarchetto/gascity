#!/usr/bin/env bash
# scripts/canary-fetch.sh -- collect one side of the agent-efficiency canary.
#
# Writes the four inputs scripts/canary-eval.py reads into one directory:
# a usage report for each of the two windows, and a bead dump per rig store
# for each of the two windows.
#
# It is a separate script from the evaluator on purpose. Collection touches
# two live systems (gc and bd) and cannot run inside a unit test; the
# evaluator is pure and is driven end to end by scripts/canary_eval_test.go
# over files. Folding them together would put the whole decision layer behind
# a live dependency and leave it untested.
#
# WINDOW BOUNDS ARE PASSED AS RFC3339 WITH AN EXPLICIT OFFSET, and the two
# tools must be given the same zone. `gc usage` reads a bare YYYY-MM-DD as
# local; `bd list --closed-after` reads one as UTC. Mixing them shifts the
# bead side of the comparison by the UTC offset -- five hours here -- which
# silently swaps beads in and out at both ends of every window. Verified
# rather than assumed: on the gascity store, --closed-after 2026-09-04T00:00Z
# returns 171 beads where both 2026-09-04T00:00:00-05:00 and its equivalent
# 2026-09-04T05:00:00Z return 170.
#
# Usage:
#   scripts/canary-fetch.sh --out DIR \
#     --baseline SINCE UNTIL --window SINCE UNTIL \
#     --store NAME=PATH [--store NAME=PATH ...]
#
# NAME labels the output file; PATH is the rig root holding .beads. Stores are
# named rather than discovered because `bd` is scoped by working directory and
# a fleet-wide reading is the union of every rig's store -- one store's dump
# answers for one rig alone, on exit 0, which is the shape of a confidently
# wrong number.

set -euo pipefail

die() {
  printf 'canary-fetch: %s\n' "$1" >&2
  exit 1
}

out=""
baseline_since=""
baseline_until=""
window_since=""
window_until=""
stores=()

while [ $# -gt 0 ]; do
  case "$1" in
    --out)
      [ $# -ge 2 ] || die "--out needs a directory"
      out="$2"
      shift 2
      ;;
    --baseline)
      [ $# -ge 3 ] || die "--baseline needs SINCE and UNTIL"
      baseline_since="$2"
      baseline_until="$3"
      shift 3
      ;;
    --window)
      [ $# -ge 3 ] || die "--window needs SINCE and UNTIL"
      window_since="$2"
      window_until="$3"
      shift 3
      ;;
    --store)
      [ $# -ge 2 ] || die "--store needs NAME=PATH"
      stores+=("$2")
      shift 2
      ;;
    *)
      die "unknown argument '$1'; see the header for the invocation"
      ;;
  esac
done

[ -n "$out" ] || die "--out is required"
[ -n "$baseline_since" ] || die "--baseline is required"
[ -n "$window_since" ] || die "--window is required"
[ "${#stores[@]}" -gt 0 ] || die "at least one --store NAME=PATH is required"

# An offset-less bound is the defect described in the header, so refuse it
# here rather than emitting a quietly shifted comparison. A trailing Z counts:
# it is explicit, just not local.
for bound in "$baseline_since" "$baseline_until" "$window_since" "$window_until"; do
  case "$bound" in
    *T*Z | *T*+[0-9][0-9]:[0-9][0-9] | *T*-[0-9][0-9]:[0-9][0-9]) ;;
    *) die "window bound '$bound' carries no timezone; gc reads a bare date as local and bd reads it as UTC" ;;
  esac
done

command -v gc >/dev/null 2>&1 || die "gc is not on PATH"
command -v bd >/dev/null 2>&1 || die "bd is not on PATH"

mkdir -p "$out"

fetch_usage() {
  local label="$1" since="$2" until="$3"
  gc usage --by session --since "$since" --until "$until" --top 0 --json \
    >"$out/$label-usage.json" ||
    die "gc usage failed for the $label window; a partial file is not a window"
}

fetch_beads() {
  local label="$1" since="$2" until="$3" name="$4" root="$5"
  [ -d "$root/.beads" ] || die "no .beads under '$root' for store '$name'"
  # GC_RIG and GC_RIG_ROOT are cleared because a city session exports both and
  # they outrank the explicit BEADS_DIR below, which would silently answer for
  # the caller's own rig for every store in the list.
  env -u GC_RIG -u GC_RIG_ROOT \
    BEADS_DIR="$root/.beads" GC_BEADS_SCOPE_ROOT="$root" \
    bd list --all --include-gates --include-infra \
    --closed-after "$since" --closed-before "$until" \
    --limit 0 --json >"$out/$label-beads-$name.json" ||
    die "bd list failed for store '$name' in the $label window"
}

fetch_usage baseline "$baseline_since" "$baseline_until"
fetch_usage window "$window_since" "$window_until"

for store in "${stores[@]}"; do
  name="${store%%=*}"
  root="${store#*=}"
  [ "$name" != "$store" ] || die "--store '$store' is not NAME=PATH"
  fetch_beads baseline "$baseline_since" "$baseline_until" "$name" "$root"
  fetch_beads window "$window_since" "$window_until" "$name" "$root"
done

printf 'collected into %s:\n' "$out"
for file in "$out"/*.json; do
  printf '  %s\n' "$file"
done
