#!/usr/bin/env bash
# scripts/check-extmsg-bridge-isolation.sh -- the two negatives
# epic:mayor-slack-bridge must not violate, expressed as refusals.
#
# Acceptance criterion 5 (ZERO hardcoded roles) and criterion 8 (the existing
# alerts seam is untouched) are both cross-cutting negatives: every bead in the
# epic satisfies them today and any later edit can break them, and neither is
# visible in the round-trip demo. A demo that passes proves nothing about
# whether a role name is hardcoded or whether the alerts channel is reachable
# from the bridge, so the guarantee has to be a gate that exits nonzero rather
# than a paragraph in the roadmap (bead gs-8ra).
#
# Run standalone, or through TestExtmsgBridgeIsolation in
# scripts/extmsg_bridge_isolation_test.go, which puts it in the `make test`
# sweep and drives the mutation cases that prove each refusal below can fail.
#
# The optional first argument is the repository root to scan. It exists so the
# test can point the gate at a mutated copy of the tree -- the tree IS the
# dependency here, so injecting it is the sanctioned way to reach a failure
# branch. It is NOT a mode switch: there is one code path and the test drives
# the same one production does.
#
# WHAT THIS GATE DOES NOT JUDGE, both halves deliberate.
#
# 1. Byte-identity of the city's own alert seam. assets/scripts/notify.sh and
#    slack-deliver.py live in the city repository, not here, so no gate in
#    this repo can assert they are unmodified. Refusal 5 asserts the half
#    that IS enforceable from here -- the seam must never be copied INTO the
#    bridge's repo -- and criterion 8's "notify.sh stays exactly as it is"
#    needs a city-side check to be mechanical. Recorded on gs-8ra.
#
# 2. The knob-name collision in ${GC_HOME}/secrets.env, which is a live
#    defect and not a hypothetical. Measured 2026-09-07: slack-deliver.py
#    reads SLACK_BOT_TOKEN and SLACK_CHANNEL_ID from that file, and this
#    bridge reads knobs of the SAME two names from its environment while its
#    own README (the "Keep ... only in ${GC_HOME}/secrets.env" line) tells
#    the operator to put them there. So configuration alone -- with no code
#    edit for any refusal below to catch -- points the mirror at the alerts
#    channel, or repoints the alerts seam at the mirror's channel. Fixing it
#    means renaming the bridge's knobs, which is bridge code this gate does
#    not own; filed as its own bead from gs-8ra. Do NOT "fix" it by relaxing
#    refusal 4: the collision is upstream of every scan here.
set -euo pipefail

ROOT=${1:-$(cd "$(dirname "$0")/.." && pwd)}
STATUS=0

# Every scan below runs on repo-relative paths, which is what git ls-files
# emits, so the gate works from the root rather than prefixing $ROOT onto each
# one.
cd "$ROOT"

fail() {
    echo "check-extmsg-bridge-isolation: FAIL: $1" >&2
    STATUS=1
}

# --- the bridge path, as the build defines it ---
#
# The scan list is read out of the Makefile recipe that builds and tests the
# bridge rather than written here. A hand-kept list of files rots at the next
# file added, and the whole point of this gate is to catch the edit nobody
# listed. Deriving the package directory from the target means a bridge that
# moves takes the gate with it, and a bridge whose target is deleted fails
# here instead of silently going unscanned.
PKG=$(awk '/^test-openclaw-bridge:/ {f=1; next} f && /^\t/ {print; exit} f {exit}' \
    Makefile | sed -n 's/^[[:space:]]*cd \([^ ]*\) .*/\1/p')
if [ -z "$PKG" ] || [ ! -d "$PKG" ]; then
    echo "check-extmsg-bridge-isolation: FAIL: cannot derive the bridge package" >&2
    echo "  from the test-openclaw-bridge recipe in Makefile." >&2
    echo "  Remedy: restore the target, or repoint this gate at the recipe that" >&2
    echo "  replaced it. Do NOT hand-write the package path here." >&2
    exit 1
fi

# REACHABILITY is the principle the scanned set is built on: a file is in the
# bridge path if the bridge can execute it. Roots are enumerated below; from
# each root the walk follows relative imports, which is what puts a lib file
# in scope the moment it is wired in and leaves one that is written but never
# imported -- dead code the bridge cannot run -- out of scope.
#
# Tracked files only, so an untracked scratch file cannot fail the gate and a
# vendored node_modules is never walked. Bare specifiers (@slack/socket-mode
# and friends) stop the walk: they resolve into node_modules, which this repo
# neither writes nor ships.
resolve_import() {
    local base=$1 spec=$2 path
    path="$(dirname "$base")/$spec"
    # Collapse a/./b and a/b/../c without realpath --relative-to, which is
    # GNU-only and this gate runs in the Mac unit sweep too. Both forms must go:
    # one file reached as ./lib/x.mjs and as lib/x.mjs is two keys in SEEN, and
    # the closure then reports and greps the same file several times.
    path=${path#./}
    while [[ $path == *"/./"* ]]; do
        path=${path//\/.\//\/}
    done
    while [[ $path == *"/../"* ]]; do
        path=$(printf '%s' "$path" | sed 's|[^/]*/\.\./||')
    done
    printf '%s\n' "$path"
}

# Three root rules, UNIONED. Each is a different way this package says "this
# file is a program", and dropping any one of them opens a hole shaped like
# the files it stopped covering:
#
#   - a .mjs at the package root, the package's own entrypoint convention
#     (`node slack-bridge.mjs`). Kept even though every such file also
#     carries a shebang today, because an entrypoint written without one
#     would otherwise leave the closure silently, taking its imports with it.
#   - a shebang, which makes a file its own entrypoint whatever its name or
#     extension. This is what reaches demo.sh and the extensionless channel
#     stand-ins, which nothing imports. The launchers are not decoration:
#     demo.sh writes an agent directory and binds `gc session new <agent>`,
#     naming a session identity WITHOUT going through required(), so a
#     convenience default of a role name lands there as readily as in a lib.
#     Rooting on the shebang is what keeps the NEXT launcher covered too.
#   - node --test's discovery rule, for the test roots.
#
# The shebang is read out of the file rather than taken from the execute bit,
# which a checkout on a filesystem carrying no permissions would lose.
#
# The package-root test is written against a path with the prefix stripped
# and an explicit */* rejection, NOT as a "$PKG"/*.mjs glob: `*` in a case
# pattern matches `/` too, so that glob makes every lib module a root and
# quietly destroys the property below -- that a module nothing imports is
# dead code and stays out of the scan.
entrypoints() {
    local f rel
    while read -r f; do
        rel=${f#"$PKG"/}
        case $rel in
        */*) ;;
        *.mjs)
            printf '%s\n' "$f"
            continue
            ;;
        esac
        case $rel in
        *.test.mjs | test/*.mjs)
            printf '%s\n' "$f"
            continue
            ;;
        esac
        if [ -f "$f" ] && [ "$(head -c 2 "$f")" = '#!' ]; then
            printf '%s\n' "$f"
        fi
    done < <(git ls-files -- "$PKG")
}

# Relative specifiers in every form ESM offers: `from`, a dynamic `import()`
# and the bare side-effect `import './x.mjs'`, in either quote style. The
# narrower single-quoted `from`-only pattern matched today's bridge exactly,
# which is the problem -- nothing in this package lints quote style, so one
# file written with double quotes would drop its whole subtree out of the
# closure and take any violation in it along. A file set that silently
# excludes the file the violation lands in is the failure this gate is
# supposed to be immune to.
imports_of() {
    grep -oE "(from|import)[[:space:]]*\(?[[:space:]]*['\"]\.[^'\"]*['\"]" "$1" |
        sed -E "s/.*['\"](\.[^'\"]*)['\"].*/\1/" || true
}

QUEUE=$(entrypoints)
if [ -z "$QUEUE" ]; then
    echo "check-extmsg-bridge-isolation: FAIL: no bridge entrypoint under $PKG." >&2
    echo "  Expected shebang-carrying programs and node --test files." >&2
    echo "  Remedy: restore them, or repoint this gate at where they moved." >&2
    exit 1
fi

# SEEN is a newline-delimited string and the queue is a string rather than
# mapfile plus an associative array, because both of those are bash 4 and the
# bash macOS ships is 3.2. This gate is executed by a Go test in the unit
# sweep, which runs whatever `bash` PATH resolves to; a gate that cannot start
# there is a gate that does not run.
SEEN=
PKG_FILES=()
while [ -n "$QUEUE" ]; do
    current=${QUEUE%%$'\n'*}
    if [ "$current" = "$QUEUE" ]; then QUEUE=; else QUEUE=${QUEUE#*$'\n'}; fi
    [ -n "$current" ] || continue
    case $'\n'"$SEEN" in
    *$'\n'"$current"$'\n'*) continue ;;
    esac
    [ -f "$current" ] || continue
    SEEN="$SEEN$current"$'\n'
    PKG_FILES+=("$current")
    while read -r spec; do
        [ -n "$spec" ] || continue
        QUEUE="$QUEUE"$'\n'"$(resolve_import "$current" "$spec")"
    done < <(imports_of "$current")
done

# --- three scopes, because the refusals ask three different questions ---
#
# PKG_FILES, built above, is everything the package can RUN. Refusals 1, 4
# and 6 use it whole: a role name, an alerts-seam reference or a hardcoded
# destination is forbidden anywhere the bridge ships, launchers and stand-ins
# included, because that text binds an identity wherever it sits.
#
# MODULE_FILES is the narrower question -- the bridge's own ESM module graph,
# the .mjs half of the same closure. Refusals 2 and 3 are about HOW the bridge
# reads its configuration, and their remedy is a helper (lib/gc-client.mjs's
# env()) that only a module importing it can use. The extensionless stand-ins
# fake-imsg/imsg and fake-telegram/bot-api are separate programs spawned by
# demo.sh: they load node builtins alone, read only their own FAKE_* knobs,
# and so can bind neither a session nor a channel. Refusing their
# `process.env.FAKE_TG_PORT || 8932` would refuse something that is not a
# violation, and the fix would be to import a bridge module into a stand-in
# -- worse than the thing refused. The EXTENSION is the derivation, NOT a
# list of exempt filenames: a stand-in rewritten as a .mjs and imported is in
# the narrow scope the moment it is.
#
# DOC_FILES is the package's prose, which nothing imports and which therefore
# appears in no closure. It is added ONLY to refusal 4, and the asymmetry is
# the point. A README naming a role in a SLACK_TARGET_AGENT example, or
# showing SLACK_CHANNEL_ID=C012345, is criterion 5 and 8 being SATISFIED --
# it shows the identity arriving as configuration -- so refusals 1 and 6
# spare prose and would otherwise punish good documentation. Refusal 4 is the
# opposite case: prose pointing an operator at the alerts webhook unifies the
# two channels just as effectively as code that reads it.
MODULE_FILES=()
for f in "${PKG_FILES[@]}"; do
    case $f in
    *.mjs) MODULE_FILES+=("$f") ;;
    esac
done

DOC_FILES=()
while read -r doc; do
    [ -n "$doc" ] && DOC_FILES+=("$doc")
done < <(git ls-files -- "$PKG" | grep '\.md$' || true)

# --- refusal 1: no role name in the bridge source ---
#
# The role vocabulary is derived from the agent directories the in-tree packs
# declare, so a pack that adds a role extends this gate with it. AGENTS.md
# names mayor, deacon and polecat as the roles the SDK must NOT know about;
# requiring all three to survive the derivation is what stops a deleted
# example pack from quietly shrinking the denylist to nothing.
#
# This layer is the readable one. It catches a role name spelled the way the
# packs spell it and nothing else -- a convenience default of "lab/lead" or
# "the boss" walks straight through it. Refusals 2 and 3 are the structural
# backstop that does not depend on knowing the name.
#
# The vocabulary contains ordinary English words, because some packs name an
# agent `worker`, `crew`, `dog` or `coder`. So this refuses those words in a
# comment as readily as in a lookup key -- it scans text and cannot tell the
# two apart. That over-refusal is the INTENDED direction: the remedy is to
# reword the comment or rename the fixture, never to narrow the pattern,
# because every narrowing is a hole shaped like the thing it excused.
ROLES=()
while read -r role; do
    [ -n "$role" ] && ROLES+=("$role")
done < <(git ls-files |
    sed -n 's|.*/agents/\([^/]*\)/agent\.toml$|\1|p' | sort -u)
if [ "${#ROLES[@]}" -eq 0 ]; then
    echo "check-extmsg-bridge-isolation: FAIL: no role names derived." >&2
    echo "  No */agents/<name>/agent.toml is tracked, so refusal 1 would" >&2
    echo "  refuse nothing. Remedy: restore the packs, or re-derive the" >&2
    echo "  vocabulary from whatever now declares agents." >&2
    exit 1
fi
for required_role in mayor deacon polecat; do
    if ! printf '%s\n' "${ROLES[@]}" | grep -qx "$required_role"; then
        fail "the role vocabulary derived from the in-tree packs lost \
'$required_role'.
  AGENTS.md names it as a role the SDK must not know, so its absence means the
  derivation broke or the pack declaring it was removed. Remedy: restore the
  pack, or re-derive the vocabulary from whatever replaced it. Do NOT drop the
  name from this list."
    fi
done
ROLE_PATTERN=$(printf '%s|' "${ROLES[@]}")
ROLE_PATTERN="\\b(${ROLE_PATTERN%|})\\b"
if [ "${#PKG_FILES[@]}" -gt 0 ]; then
    hits=$(grep -rniE "$ROLE_PATTERN" -- "${PKG_FILES[@]}" || true)
    if [ -n "$hits" ]; then
        echo "$hits" >&2
        fail "role name in the bridge source (above). The bridge binds a
  configured named session; criterion 5 is ZERO hardcoded roles. Remedy: take
  the identity from configuration -- required('SLACK_TARGET_AGENT') or
  required('GC_MIRROR_SESSION') -- and name test fixtures after what they
  test, not after a role."
    fi
fi

# --- refusal 2: a required knob never acquires a default ---
#
# Requiredness is a property of the KNOB, not of one call site. The failure
# this refuses is an agent adding a convenience default -- env('SLACK_TARGET_
# AGENT', 'mayor') in some new file -- next to the required() read that already
# exists, which turns a missing configuration into a silent bind to whatever
# the default names. Both sets come from the source itself, so no list is kept
# here and a knob added tomorrow is covered the moment it is read.
readonly IDENT='[A-Z_][A-Z0-9_]*'
required_knobs=$(grep -rhoE "required\('$IDENT'" -- "${MODULE_FILES[@]}" |
    sed "s/.*'\(.*\)'/\1/" | sort -u || true)
defaulted_knobs=$(grep -rhoE "env\('$IDENT'[[:space:]]*," -- "${MODULE_FILES[@]}" |
    sed "s/.*'\($IDENT\)'.*/\1/" | sort -u || true)
both=$(comm -12 <(printf '%s\n' "$required_knobs") <(printf '%s\n' "$defaulted_knobs") |
    grep -v '^$' || true)
if [ -n "$both" ]; then
    echo "$both" >&2
    fail "the knob(s) above are read with required() in one place and with a
  default in another. A required knob that acquires a default stops failing
  fast and starts binding to the default instead. Remedy: keep the knob
  required everywhere, or make it optional everywhere and say in the README
  what the default binds to."
fi

# --- refusal 3: defaults route through the one env() helper ---
#
# The evasion refusal 2 cannot see is an inline fallback on a raw read:
# process.env.SLACK_TARGET_AGENT ?? 'mayor'. Forbidding the shape rather than
# the name means the refusal holds whatever the default is spelled.
#
# lib/gc-client.mjs's `export const env =` line is the ONE sanctioned
# exception and it is exempted by its definition text rather than by filename,
# so a second default hidden elsewhere in that file is still refused. It
# cannot itself hide a default: its fallback is its own second argument,
# supplied by the caller the gate is reading.
inline=$(grep -rnE "process\.env[^=]*(\|\||\?\?|\?)" -- "${MODULE_FILES[@]}" |
    grep -v 'export const env =' || true)
if [ -n "$inline" ]; then
    echo "$inline" >&2
    fail "inline fallback on a raw process.env read (above). Remedy: route the
  default through env(name, default) from lib/gc-client.mjs, which is the one
  place a bridge default is written, or use required(name) when there must not
  be one."
fi

# --- refusal 4: the alerts seam is unreachable from the bridge ---
#
# The city's alerts channel has exactly one name in this fleet -- the webhook
# URL the alert seam reads from ${GC_HOME}/secrets.env -- so refusing that
# name, and the seam's two scripts, is the mechanical form of "the alerts
# channel id never appears in the bridge's config or code path". The literal
# channel id is a secret and is deliberately NOT written here: a gate that
# must know a secret to run cannot run.
#
# Prose is scanned too. A README that tells an operator to point the bridge at
# the alerts webhook unifies the two channels just as effectively as code that
# reads it, and pm-log #57 records that unification as REJECTED, not pending.
SEAM='SLACK_WEBHOOK_URL|slack-deliver|notify\.sh|hooks\.slack\.com'
seam_hits=$(grep -rnE "$SEAM" -- "${PKG_FILES[@]}" "${DOC_FILES[@]}" || true)
if [ -n "$seam_hits" ]; then
    echo "$seam_hits" >&2
    fail "alerts-seam reference in the bridge (above). The alerts channel is
  one-way by decision (pm-log #57, and the header of the city's notify.sh);
  criterion 8 keeps it that way. Remedy: publish through the bridge's own
  configured conversation via POST /extmsg/outbound. Do NOT unify the two
  channels."
fi

# --- refusal 5: the alerts seam is not reimplemented in this repo ---
#
# The city's notify.sh cannot be checked for modification from here, so this
# refuses the move that would make it modifiable from here: copying the seam
# into the bridge's own repository. Landing either script in this tree is the
# unification criterion 8 forbids, arriving as a new file rather than as an
# edit.
seam_files=$(git ls-files |
    grep -E '(^|/)(notify\.sh|slack-deliver\.py)$' || true)
if [ -n "$seam_files" ]; then
    echo "$seam_files" >&2
    fail "the city's alert seam appears in this repository (above). It lives in
  the city repo and is one-way by decision. Remedy: delete the copy and call
  the city's seam, or state the new file's purpose and rename it so it is not
  the alert seam under another roof."
fi

# --- refusal 6: the conversation arrives as configuration ---
#
# This is criterion 8's letter -- "the alerts channel id never appears in the
# bridge's config or code path" -- expressed without the gate needing to know
# the id. A Slack conversation id is C, G or D followed by eight or more
# uppercase alphanumerics, so refusing the SHAPE refuses the alerts channel's
# id along with every other hardcoded destination, and a gate that must be
# told a secret to run is a gate that cannot run in CI.
#
# Code and launchers only, deliberately NOT the prose, for the same reason
# refusal 1 spares prose: `SLACK_CHANNEL_ID=C012345` in the README is the
# criterion being SATISFIED -- it shows the destination arriving as
# configuration. Refusing an example there would only teach the next author
# to stop writing examples.
#
# The digit requirement is what separates an id from a word: the shape alone
# matches CONVERSATION, CREDENTIALS and DESTINATION, and it did -- a banner
# string reading "CHILD CONVERSATION" in demo-telegram.sh is what forced this
# second pass. An id carries digits; a word in caps does not.
#
# ABSENCE, so the next reader does not think it was missed: an all-letter
# conversation id walks past this refusal. Nothing distinguishes such a token
# from prose, and buying it would cost every capitalized word in the package.
# The backstops for that case are refusal 4 (the alerts seam by name) and the
# required('SLACK_CHANNEL_ID') read that makes a literal redundant in the
# first place.
CHANNEL_LITERAL='\b[CGD][A-Z0-9]{8,}\b'
channel_hits=$(grep -rnoE "$CHANNEL_LITERAL" -- "${PKG_FILES[@]}" |
    awk -F: '$NF ~ /[0-9]/' || true)
if [ -n "$channel_hits" ]; then
    echo "$channel_hits" >&2
    fail "Slack conversation id literal in the bridge path (above). The
  conversation is configuration -- required('SLACK_CHANNEL_ID') -- so a literal
  here binds one channel forever and could bind the alerts channel. Remedy:
  read the id from configuration. If this is not a channel id, rename the
  constant so it does not read as one."
fi

if [ "$STATUS" -eq 0 ]; then
    # Both counts are printed because a scope that silently collapsed to a
    # handful of files is the one way every refusal above passes while
    # checking almost nothing, and the number is the only place it shows.
    echo "check-extmsg-bridge-isolation: OK ($PKG:" \
        "${#PKG_FILES[@]} executable files," \
        "${#MODULE_FILES[@]} in the module graph," \
        "${#DOC_FILES[@]} docs, ${#ROLES[@]} role names)"
fi
exit "$STATUS"
