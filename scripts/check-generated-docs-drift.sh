#!/usr/bin/env bash
# check-generated-docs-drift.sh - regenerate the genschema reference docs and
# fail on drift, leaving the exact regeneration patch for the docs-autofix
# workflow to apply (see .github/workflows/docs-autofix.yml).
#
# The generated set is exactly what cmd/genschema writes. GEN_PATHS, the list in
# cmd/genschema/main.go, and the path allowlist in scripts/docs-autofix-push.sh
# must all name the same six paths -- pinned by TestGeneratedDocsPathListsAgree
# rather than by the three "keep in sync" comments that used to be the only
# thing holding them together.
#
# The verdict comes from scripts/check-artifact-drift.sh, not from this script's
# own `git diff`. The diff it replaced answered "the docs moved", which is a
# different question from "did the committed docs stop matching the committed
# sources": an unstaged edit to any Go file the generator reads produced the
# same "STALE" message as a genuinely stale commit, and an added output file was
# invisible to it entirely. Bead ci-c425 for the classifier, ci-d4lw for this
# wiring and for why the source set is as coarse as it is.
#
# Outputs generated-docs-freshness.patch (override with PATCH_OUT), removed
# whenever there is no patch to hand over. Three exits, because the autofix
# workflow must never be handed a patch from a run that judged nothing:
#
#   0  fresh
#   1  drift, patch written -- CI fails the step, autofix applies the patch
#   2  the gate could not judge (bad arguments, git unreadable), NO patch
#
# Drift can also occur with no patch: an untracked generated file is a real
# stale artifact that `git diff` cannot express, so exit 1 arrives with the
# patch removed and the reason stated on stderr. docs-autofix-push.sh treats an
# absent or empty patch as nothing to do.
#
# Invariants verified by scripts/test-check-generated-docs-drift.sh.

set -euo pipefail

PATCH_OUT="${PATCH_OUT:-generated-docs-freshness.patch}"

# The gate is resolved from THIS script's directory, not from the caller's cwd.
# Both callers (CI's preflight-generated job and `make check-schema`) run at the
# repository root, so `./scripts/...` would work for them -- but the drift gate
# reads the tree with git, and a self-relative path is what lets the test suite
# run a copy of both scripts inside a temp repository.
GATE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/check-artifact-drift.sh"

GEN_PATHS=(
    docs/reference/cli.md
    docs/reference/config.md
    docs/reference/schema/city-schema.json
    docs/reference/schema/city-schema.txt
    docs/reference/schema/pack-schema.json
    docs/reference/schema/pack-schema.txt
)

# Sources of the generated docs, deliberately coarser than the generator's
# import closure. Erring wide can only over-hedge (an `unattributable` verdict
# where `stale-index` was true); erring narrow reports `stale-index` -- "the
# committed artifact is not a build of the committed sources" -- for a diff the
# reader's own uncommitted edit explains, which is the ci-gpxg agent-hour
# restated. Rejected narrower sets and the measurements that ruled them out are
# recorded at goGeneratedSources in scripts/check_artifact_drift_test.go; the
# short version is that internal/config embeds a type from internal/pricing, and
# docgen extracts doc comments by walking every tracked top-level directory, so
# neither a per-package list nor `go list -deps` covers the real inputs.
#
# Absent on purpose: test/, examples/, scripts/ and docs/. They are inside that
# doc-comment walk, but a comment only reaches the output for a type the
# reflector reflected, and no config type is defined in them. Adding them would
# put an unrelated dirty _test.go into every local verdict. That exclusion is an
# assumption about where config types live, not a property of the generator: if
# one is ever defined under test/ or examples/, this set under-covers silently
# and the directory belongs here.
GEN_SOURCES=(cmd internal go.mod go.sum)

# CGO off: genschema is pure Go, and the transitive dolt ICU dependency
# fails to compile on hosts without ICU headers (mirrors the pure-Go build
# the beads pipeline uses for the same reason).
CGO_ENABLED=0 go run ./cmd/genschema

gate_args=(--label 'generated reference docs')
for path in "${GEN_PATHS[@]}"; do
    gate_args+=(--artifact "$path")
done
for path in "${GEN_SOURCES[@]}"; do
    gate_args+=(--source "$path")
done
gate_args+=(--regen 'make generate')

# The gate's exit status is the verdict, and `set -e` must not swallow the
# distinction: 1 is drift and 2 is the gate itself failing (bad arguments, git
# unreadable). A 2 propagates as-is rather than being reported as drift, because
# a patch written from a run that could not judge anything is worse than no
# patch.
gate_status=0
"$GATE" "${gate_args[@]}" || gate_status=$?

if [ "$gate_status" -eq 0 ]; then
    echo "Generated reference docs are fresh."
    rm -f "$PATCH_OUT"
    exit 0
fi
if [ "$gate_status" -ne 1 ]; then
    echo "check-generated-docs-drift.sh: drift gate failed (exit $gate_status); no patch written." >&2
    exit "$gate_status"
fi

git diff -- "${GEN_PATHS[@]}" >"$PATCH_OUT"
if [ -s "$PATCH_OUT" ]; then
    echo "Generated reference docs are STALE; regeneration patch written to $PATCH_OUT:"
    git diff --stat -- "${GEN_PATHS[@]}"
else
    # An untracked generated file is drift `git diff` cannot express, so
    # there is no patch to hand the autofix workflow -- it treats an empty
    # patch as nothing to do. Say so here rather than leave a reader to
    # conclude the verdict was spurious.
    rm -f "$PATCH_OUT"
    echo "Generated reference docs are STALE, but the drift is an UNTRACKED file:" >&2
    echo "  git diff cannot express it, so no autofix patch was written." >&2
    echo "  Commit the new generated path by hand." >&2
fi
echo "Fix locally with: make generate && git commit -- ${GEN_PATHS[*]}"
exit 1
