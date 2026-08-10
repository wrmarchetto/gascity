#!/usr/bin/env bash
#
# scripts/check-artifact-drift.sh -- decide whether a committed generated
# artifact is stale, and name WHICH of two faults produced the diff.
#
# The caller rebuilds the artifact into the worktree first and then calls
# this; the gate never builds anything itself. Two reasons: the build is slow
# and tool-specific, and a build-free classifier is what lets
# scripts/test-check-artifact-drift.sh drive every branch against plain text
# files in temp git repos in under a second.
#
# What the gate asserts is build(sources at the INDEX) == artifact at the
# INDEX. The rebuild the caller ran was a build of the WORKTREE sources, so
# that assertion only follows when worktree and index sources agree -- which
# is why there are four outcomes and not two:
#
#   sources clean, artifact matches   pass, assertion proven, silent
#   sources clean, artifact differs   fail, class: stale-index
#   sources dirty, artifact differs   fail, class: unattributable
#   sources dirty, artifact matches   pass, class: unproven (note only)
#
# The predecessor collapsed all four into one `git diff --quiet -- <artifact>`
# behind one message ("dist is stale -- run make dashboard-build and commit").
# On ci-gpxg that message was attached to a real stale-index fault, but since
# it was also what an unstaged edit produced, and since one source edit
# renames every chunk above it (22 of 35 assets moved at once), it read as a
# toolchain difference and cost about an agent-hour. Bead ci-c425.
#
# Untracked files count on both sides. `git diff` cannot see them, so the
# predecessor was blind to a rebuild that only ADDS an asset -- a new
# content-hashed chunk that renames nothing -- and to a brand-new source file
# that is as much a build input as a modified one.
#
# --artifact is repeatable, and one regeneration command writing several paths
# must pass all of them to ONE invocation. Every named artifact is excluded from
# the source scan, so splitting a set across invocations makes each run see the
# others' rebuilt output as dirty sources -- which is not a cosmetic loss: the
# verdict degrades to unattributable, or to exit 0 when the drifted path is not
# the one that run was scanning. make spec-ci writes six paths, two of them
# inside its own internal/ source pathspec (bead ci-d4lw).
#
# Invariants verified by scripts/test-check-artifact-drift.sh. The Makefile
# wiring of every caller is pinned by scripts/check_artifact_drift_test.go, and
# scripts/check-generated-docs-drift.sh -- the one caller that is not a Makefile
# recipe -- by scripts/test-check-generated-docs-drift.sh.

set -uo pipefail

usage() {
    cat <<'EOF'
usage: check-artifact-drift.sh --label <text> --artifact <path> [--artifact <path>]...
                               --source <path> [--source <path>]...
                               --regen <command> [--note <text>]

Run from the repository root, after the artifact has been rebuilt into the
worktree.

  --label     human name for the artifact, used in the verdict
  --artifact  path (file or directory) holding a generated artifact.
              Repeatable -- pass every path one regeneration command writes
              to a single invocation
  --source    path the artifact is generated from; repeatable
  --regen     command that regenerates the artifact, quoted in the remedy
  --note      one line of artifact-specific explanation, printed with a
              stale-index verdict; repeatable, and each line is printed as
              given (keep them under 74 columns)

Exit: 0 no drift, 1 drift (verdict on stderr), 2 usage or git error.
EOF
}

label=""
regen=""
artifacts=()
sources=()
notes=()

while [ $# -gt 0 ]; do
    case "$1" in
    --label | --artifact | --source | --regen | --note)
        if [ $# -lt 2 ]; then
            printf 'check-artifact-drift.sh: %s needs a value\n' "$1" >&2
            exit 2
        fi
        case "$1" in
        --label) label="$2" ;;
        --artifact) artifacts+=("$2") ;;
        --source) sources+=("$2") ;;
        --regen) regen="$2" ;;
        --note) notes+=("$2") ;;
        esac
        shift 2
        ;;
    -h | --help)
        usage
        exit 0
        ;;
    *)
        printf 'check-artifact-drift.sh: unknown argument %s\n' "$1" >&2
        usage >&2
        exit 2
        ;;
    esac
done

for required in label regen; do
    if [ -z "${!required}" ]; then
        printf 'check-artifact-drift.sh: --%s is required\n' "$required" >&2
        usage >&2
        exit 2
    fi
done
if [ "${#artifacts[@]}" -eq 0 ]; then
    printf 'check-artifact-drift.sh: at least one --artifact is required\n' >&2
    usage >&2
    exit 2
fi
if [ "${#sources[@]}" -eq 0 ]; then
    printf 'check-artifact-drift.sh: at least one --source is required\n' >&2
    usage >&2
    exit 2
fi

# Refresh the cached stat info before diffing. A generator that replaces its
# whole output tree -- dashboard-build does `rm -rf dist && cp -rf` -- leaves
# every file with a fresh mtime and inode, so the index is stat-dirty on every
# single run. `git diff --quiet` is allowed to answer from stat info alone and
# would call that a difference; the --name-only form below compares content,
# and this refresh keeps it from re-reading the entire tree to do so. A
# nonzero status here only means some file really did change, which is the
# question the rest of the script answers.
git update-index -q --refresh >/dev/null 2>&1 || true

# changed_paths <pathspec>...: every path under the pathspecs that differs
# between the worktree and the index, tracked or not. Fails nonzero if git
# itself fails, so the caller can fail closed rather than read a git error as
# "nothing changed".
changed_paths() {
    git diff --name-only -- "$@" &&
        git ls-files --others --exclude-standard -- "$@"
}

if ! artifact_changed="$(changed_paths "${artifacts[@]}")"; then
    printf 'check-artifact-drift.sh: git failed reading %s -- cannot judge drift\n' \
        "${artifacts[*]}" >&2
    exit 2
fi
# No artifact is ever one of its own sources, and that has to hold for EVERY
# path in the set, not just one. Excluding them by pathspec rather than
# documenting "pass disjoint paths" is what keeps a caller that points --source
# at an ancestor directory from getting a gate that can only ever answer
# "unattributable" -- a gate that cannot fire is worse than none. Missing even
# one exclusion is worse than that: spec-ci writes six paths, so five of them
# would read as dirty sources and turn a real stale-index into either an
# unattributable failure or -- when the drifted path is not the one being
# scanned -- an exit-0 pass. Pinned by test-check-artifact-drift.sh's
# a_sibling_artifact_is_not_a_dirty_source.
excludes=()
for a in "${artifacts[@]}"; do
    excludes+=(":(exclude)$a")
done
if ! source_changed="$(changed_paths "${sources[@]}" "${excludes[@]}")"; then
    printf 'check-artifact-drift.sh: git failed reading %s -- cannot judge drift\n' \
        "${sources[*]}" >&2
    exit 2
fi

count_paths() {
    if [ -z "$1" ]; then
        printf '0'
        return
    fi
    printf '%s\n' "$1" | wc -l | tr -d ' '
}

# print_paths <list>: indented, capped. The cap is announced rather than
# silent -- a truncated list that looked complete is how a reader concludes
# only 20 assets moved.
print_paths() {
    local list="$1" total shown
    total="$(count_paths "$list")"
    shown=20
    printf '%s\n' "$list" | head -n "$shown" | sed 's/^/    /' >&2
    if [ "$total" -gt "$shown" ]; then
        printf '    ... and %d more\n' "$((total - shown))" >&2
    fi
}

artifact_count="$(count_paths "$artifact_changed")"
source_count="$(count_paths "$source_changed")"

if [ "$artifact_count" -eq 0 ]; then
    if [ "$source_count" -gt 0 ]; then
        printf 'NOTE: %s matches the index (class: unproven).\n' "$label" >&2
        printf '  %d source path(s) differ from the index, so this run proved only that\n' \
            "$source_count" >&2
        printf '  your WORKTREE sources build it, not the committed ones:\n' >&2
        print_paths "$source_changed"
    fi
    exit 0
fi

if [ "$source_count" -gt 0 ]; then
    printf 'ERROR: %s drift (class: unattributable)\n' "$label" >&2
    printf '  %d source path(s) differ between your worktree and the index, so the\n' \
        "$source_count" >&2
    printf '  rebuild this gate judged was NOT a build of the sources in the index.\n' >&2
    printf '  The %d changed artifact path(s) may be entirely your own uncommitted\n' \
        "$artifact_count" >&2
    printf '  edits; they are NOT evidence that the committed artifact is stale.\n' >&2
    printf '  Fix: stage or stash these, then re-run this check:\n' >&2
    print_paths "$source_changed"
    exit 1
fi

printf 'ERROR: %s drift (class: stale-index)\n' "$label" >&2
printf '  The rebuild differs from the copy in the index, and every source path\n' >&2
printf '  is identical to the index -- so the committed artifact is not a build\n' >&2
printf '  of the committed sources.\n' >&2
printf '  Fix: %s && git add -- %s\n' "$regen" "${artifacts[*]}" >&2
if [ "${#notes[@]}" -gt 0 ]; then
    printf '  %s\n' "${notes[@]}" >&2
fi
printf '  %d artifact path(s) changed:\n' "$artifact_count" >&2
print_paths "$artifact_changed"
exit 1
