#!/usr/bin/env bash
# scripts/check-extmsg-bridge-isolation.sh -- the two negatives of
# epic:mayor-slack-bridge, as a command that exits nonzero.
#
# Usage: check-extmsg-bridge-isolation.sh [--list] [repo-root]
#
# Criterion 5: the bridge binds a NAMED SESSION from config, so no role name
# may appear in source on the bridge path.
# Criterion 8: the existing alerts seam is untouched, so the alerts channel
# must not be reachable from the bridge's config or code path.
#
# Why a gate and not a paragraph: both criteria are satisfied by every bead in
# the epic today and broken by any later edit, and neither is visible in the
# epic's round-trip demo. A demo that passes proves nothing about whether a
# role name is hardcoded or whether the alerts channel is reachable, so the
# rule has to be mechanical or it rots. scripts/extmsg_bridge_isolation_test.go
# is what establishes this script can actually fail.
#
# THE SCAN SET IS DERIVED, NOT LISTED. A hand-kept list of bridge files rots at
# the next file added -- and this gate was written while two other beads were
# adding files to the bridge path, so it would have rotted the same week. Two
# unioned derivations, both read out of `git ls-files`:
#
#   1. Every tracked file under a BRIDGE COMPONENT: a directory the build
#      already treats as a component -- an npm workspace (package.json, how
#      the test-openclaw-bridge target finds one) or a Go main package -- with
#      at least one tracked file naming an extmsg wire route. This is the
#      earns its keep: an adapter entrypoint typically names no route itself,
#      because it delegates the wire to a lib file, and it is exactly where a
#      convenience default lands. Measured on 2026-09-07 against the in-flight
#      Slack adapter: contrib/openclaw-bridge/slack-bridge.mjs names no route,
#      lib/slack.mjs does.
#   2. Every tracked file anywhere naming an extmsg wire route, which covers a
#      mirror daemon that lands outside a package.json component, and covers
#      the gc-side route implementation -- "a convenience default of mayor
#      somewhere in the bridge path" is refused in the handler too, not only in
#      the adapter.
#
# NOT taken: the Go import closure of internal/extmsg. Measured 42 in-module
# packages, including internal/config, internal/session and internal/agent,
# every one of which carries pre-existing role names in generic code that this
# epic does not own. A closure scan fails on day one over code that is not the
# bridge, which gets the gate suppressed rather than obeyed.
#
# THE ROLE LIST IS DERIVED TOO, from the two taxonomies the tree already keeps:
# roleEmoji in internal/runtime/tmux/tmux.go and the forbidden list in
# examples/gastown/tmux_theme_script_test.go. Rejected: requiring the two to
# match. They legitimately differ -- the runtime map carries display-only roles
# (coordinator, health-check) the gastown pack has no agent for, and the pack
# guard carries boot and dog, which have no emoji -- so equality would fail on
# a correct tree. Each source must be non-empty instead, which is what catches
# a rename.
#
# A derived list cannot know a role no source names, so the name check is only
# the first arm. The second refuses the SHAPE -- a binding key with a literal
# fallback -- whatever the fallback spells.
#
# Deliberately NOT checking that the city's assets/scripts/notify.sh is byte
# unmodified, which is criterion 8's other half. That file lives in the city
# repository, not this one, so this gate cannot see it: a CI checkout has no
# city tree, and reading one when present would be a skip-on-absence that goes
# green in every CI run. What IS enforced here is the threat the criterion
# names -- the mirror pointed at the alerts channel -- because the bridge does
# that from files this repository owns. It is reached four ways and all four
# are refused: by naming a seam key or script in CODE, by instructing an
# operator to do so in the component's PROSE, by carrying a conversation id or
# credential as a LITERAL, and by copying the seam's own scripts INTO this
# repository, which arrives as a new file rather than as an edit.
#
# Sources: docs/roadmap.md (epic:mayor-slack-bridge, criteria 5 and 8),
# docs/pm-log.md #57 (the alert seam's one-way rejection) and #58 (Socket Mode,
# assistant-turns-only), beads gs-8ra and gs-fn6.
set -euo pipefail

list_only=0
if [ "${1:-}" = "--list" ]; then
    list_only=1
    shift
fi

# Default to this script's own checkout. The positional root exists so the
# contract test can inject a fixture tree -- the checks are identical for
# both, and the test runs the real tree too, so neither path can be the only
# one exercised.
root="${1:-$(git -C "$(dirname "$0")/.." rev-parse --show-toplevel)}"
cd "$root"

fail() {
    echo "check-extmsg-bridge-isolation: FAIL: $1" >&2
    exit 1
}

# The whole derivation is `git ls-files`, so a root that is not a checkout
# yields nothing. It still exits nonzero via the empty-scan-set refusal below,
# but only after three lines of `fatal: not a git repository` that read as a
# broken gate rather than a misinvocation. Say which it is instead.
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    fail "$root is not a git work tree, so no bridge path can be derived from
  it -- this gate reads the tree through \`git ls-files\`.
  Usage: check-extmsg-bridge-isolation.sh [--list] [repo-root]
  Run it with no argument to check its own checkout."
fi

# --- the bridge path ---

# The wire surface an extmsg client cannot avoid naming. Kept to the three
# adapter-facing routes rather than every extmsg path: /extmsg/transcripts and
# the binding routes are read by the dashboard too, and pulling the SPA in
# would widen the scan to code that is not a bridge.
route_re='extmsg/(inbound|outbound|adapters)'

# Prose and build output. Prose is excluded because criterion 5 is about
# source and docs/pm-log.md legitimately discusses the mayor at length.
# Generated artifacts are excluded because they are rewritten from their
# source on the next build, so a finding in one is unactionable -- the
# generated OpenAPI client names every extmsg route.
skip_re='\.(md|txt)$|(^|/)package-lock\.json$|/generated/|\.gen\.(ts|go)$|genclient/client_gen\.go$|(^|/)openapi\.(json|txt)$'

# Test files. A test fixture naming a role is how role-as-configuration gets
# tested at all, so refusing them would forbid the epic's own tests.
test_re='(^|/)tests?/|_test\.go$|\.test\.(mjs|js|ts)$|_test\.py$'

# This gate's own two files. Not a general exclusion and not rot-prone: the
# only thing it can hide is the gate itself, never a bridge file. It is here
# because this script necessarily spells out what it refuses -- SLACK_CHANNEL_ID
# appears in its own `for seam` loop as code, not prose -- so the moment a
# comment in it names a route literally, derivation 2 pulls the script into its
# own scan set and it refuses ITSELF, citing the alerts seam. Measured
# 2026-09-07: adding one comment line reading "/extmsg/inbound" turned a clean
# tree red with a message that reads exactly like a genuine criterion-8
# violation, which is how a gate gets suppressed instead of obeyed. Pinned by
# the self-exclusion case in scripts/extmsg_bridge_isolation_test.go.
self_re='(^|/)check-extmsg-bridge-isolation\.sh$|(^|/)extmsg_bridge_isolation_test\.go$'

# Route-naming tracked files under the given pathspecs, minus everything the
# scan set would discard anyway. The self_re term is load-bearing here for a
# different reason than in the scan set: without it this gate's own directory
# is classified as a bridge component, because the script necessarily contains
# the route pattern. Measured 2026-09-07 with it removed: the scan set went
# from 16 files to 75 and the gate false-failed on
# scripts/push-gate-lock-lib.sh:94, which has nothing to do with the bridge.
#
# `|| true` wraps the whole pipeline, not just its tail. Under `set -o
# pipefail` the status is the RIGHTMOST nonzero one, and `xargs` answers 123
# whenever any batch's grep found nothing -- so a tail that succeeds does not
# absorb it, and the script would die here with no output at all.
tracked_matching_route() {
    {
        git ls-files -z -- "$@" | xargs -0 grep -lE "$route_re" 2>/dev/null |
            grep -vE "$skip_re" | grep -vE "$self_re"
    } || true
}

# Directories the build treats as an out-of-process component in their own
# right: an npm workspace, or the directory of a Go main package. Both markers
# are the build's, not this gate's, so a component added later is picked up
# without touching this file.
#
# Go main packages are covered because criterion 4 puts the mirror daemon in
# the same supervised shape as the adapter, and nothing says it must be
# JavaScript. Verified a no-op against this tree on 2026-09-07: no main package
# names an extmsg route, so the arm costs nothing today and closes the same
# entrypoint-names-no-route hole the npm arm closes.
component_markers() {
    { git ls-files | grep -E '(^|/)package\.json$' | sed 's|/package\.json$||'; } || true
    #
    # `while read` rather than `xargs -n1 dirname`: GNU xargs runs its command
    # once with NO arguments when its input is empty, so a tree whose Go files
    # declare no main package printed two lines of `dirname: missing operand`
    # on stderr before every OK. Harmless to the verdict -- the `|| true`
    # absorbs the status -- but a gate that prints an error while passing is a
    # gate that gets read as broken. `xargs -r` is the GNU spelling of the fix
    # and this script runs in the Mac unit sweep too.
    {
        git ls-files -- '*.go' | xargs grep -l '^package main' 2>/dev/null |
            while IFS= read -r gofile; do
                [ -n "$gofile" ] && dirname "$gofile"
            done
    } || true
}

# Every pipeline below ends `|| true`. Without it a tree with no package.json,
# or none naming a route, exits grep 1 under `set -o pipefail` and the script
# dies at the assignment having printed NOTHING -- the fail-closed refusal
# below would never be reached, which is the one outcome worse than a false
# pass. Pinned by the two empty-scan-set cases in the contract test.
components=$(
    component_markers | sort -u |
        while IFS= read -r dir; do
            [ -n "$dir" ] || continue
            if tracked_matching_route "$dir" | grep -q .; then
                printf '%s\n' "$dir"
            fi
        done || true
)

scan_set=$(
    {
        # Word splitting is the point: each component is a separate
        # pathspec, and component paths cannot contain whitespace (they are
        # package.json directories).
        #
        # The directive carries the code ALONE. Trailing prose after it is
        # SC1072/SC1073 and aborts the parse of the whole FILE, so the form
        # this line used to have left the script linted by nothing --
        # measured 2026-09-07 against v0.10.0. A comment merely OPENING with
        # the linter's name is read as a directive too, so this paragraph
        # does not.
        # shellcheck disable=SC2086
        [ -z "$components" ] || git ls-files -- $components
        tracked_matching_route
    } | { grep -vE "$skip_re" || true; } | { grep -vE "$test_re" || true; } |
        { grep -vE "$self_re" || true; } | sort -u
)

if [ -z "$scan_set" ]; then
    # Fail closed. An empty scan set is indistinguishable from a clean tree
    # in the gate's own output, so it must be the loud case: it means the
    # derivation stopped finding the bridge, and every check below then
    # inspects nothing and reports OK.
    fail "the bridge-path scan set is empty -- nothing was checked.
  No directory carrying a package.json also names an extmsg wire route, and no
  tracked file names one either. Either the bridge moved out of both
  derivations or the routes were renamed.
  Inspect the derivation:  scripts/check-extmsg-bridge-isolation.sh --list
  Re-derive by hand:       git ls-files -z | xargs -0 grep -lE '$route_re'"
fi

# The bridge components' own prose, scanned by the alerts-seam arm ALONE.
#
# Derived from the COMPONENT arm only, never from derivation 2's repo-wide
# route match. That is the whole reason skip_re keeps .md out of the code scan:
# docs/pm-log.md discusses both the mayor and the alert seam's one-way decision
# at length, and refusing the project's own log for recording the decision is
# an unactionable finding. A component's README is the opposite -- it is the
# instruction an operator acts on, so a line telling them to set
# SLACK_WEBHOOK_URL unifies the two channels exactly as effectively as code
# that reads it.
#
# NOT filtered by test_re, unlike the code scan. A README under a test
# directory still ships with the component and still reads as instruction; the
# test exclusion exists for role names in FIXTURES, which prose has none of.
#
# Deliberately absent from --list, which stays a dump of the code scan set:
# TestExtmsgBridgeIsolationDerivationAnchors asserts no .md is in it, and that
# assertion is what catches the prose exclusion collapsing. The prose count is
# printed on the OK line instead.
prose_set=$(
    if [ -n "$components" ]; then
        # Same word splitting as the scan set.
        # shellcheck disable=SC2086
        git ls-files -- $components | { grep -E '\.md$' || true; }
    fi
)

# The .mjs half of the scan set, which is the scope of the two configuration
# refusals below. Their remedy is a helper -- lib/gc-client.mjs's env() -- that
# only a module importing it can use, and the component also ships extensionless
# stand-ins (fake-imsg/imsg, fake-telegram/bot-api) that a demo launcher spawns
# as separate programs. Those load node builtins alone and read only their own
# FAKE_* knobs, so they can bind neither a session nor a channel; refusing their
# `process.env.FAKE_TG_PORT || 8932` would refuse something that is not a
# violation, and the fix would be to import a bridge module into a stand-in --
# worse than the thing refused. Measured 2026-09-07: those two files are the
# only inline-fallback hits in the whole scan set.
#
# The EXTENSION is the derivation, NOT a list of exempt filenames. A stand-in
# rewritten as a .mjs and imported is in scope the moment it is.
module_set=$(printf '%s\n' "$scan_set" | { grep -E '\.mjs$' || true; })

if [ "$list_only" -eq 1 ]; then
    printf '%s\n' "$scan_set"
    exit 0
fi

# --- criterion 8, the half that reads no scan set ---
#
# The city's notify.sh cannot be checked for modification from here, so this
# refuses the move that would make it modifiable from here: copying the seam
# into this repository. That lands as a NEW FILE rather than as an edit, which
# every arm below misses -- a copy dropped outside every bridge component is in
# no scan set at all.
#
# Matched on a whole path component rather than as a substring, which is the
# opposite of how the seam's names are matched in code. There the reference is
# reached through a path and has no boundary before it (assets/scripts/
# notify.sh); here the FILENAME is the whole question, and refusing a bridge's
# own bridge-notify.sh as the city's seam is how this check gets deleted.
seam_copies=$(git ls-files | { grep -E '(^|/)(notify\.sh|slack-deliver\.py)$' || true; })
if [ -n "$seam_copies" ]; then
    echo "$seam_copies" >&2
    fail "the city's alert seam is tracked in this repository (see above).
  notify.sh and slack-deliver.py live in the city repo and the alerts channel
  is one-way by decision (pm-log #57). A copy here is that unification arriving
  as a new file instead of as an edit, and it puts the seam somewhere this
  repository can change it.
  Remedy: delete the copy and call the city's seam, or -- if the new file is
  not the alert seam -- rename it so it does not read as one."
fi

# --- the role taxonomy ---

# Both extractions read a gofmt-stable single-line form. gofmt is enforced in
# CI, which is the same assumption check-eventexport-isolation.sh documents for
# its one-source-of-truth matching, and both fail closed (empty) rather than
# quietly partial if a definition is renamed or reflowed.
runtime_roles=$(
    sed -nE '/^var roleEmoji = map\[string\]string\{/,/^\}/ s/^[[:space:]]*"([^"]+)":.*/\1/p' \
        internal/runtime/tmux/tmux.go 2>/dev/null || true
)
if [ -z "$runtime_roles" ]; then
    fail "no role names extracted from the roleEmoji map in
  internal/runtime/tmux/tmux.go. Renaming or reflowing that map leaves this
  gate checking against an empty role list, which passes over every violation.
  Remedy: restore the 'var roleEmoji = map[string]string{' form, or repoint
  this extraction at whatever replaced it and extend the anchors in
  scripts/extmsg_bridge_isolation_test.go."
fi

pack_roles=$(
    sed -nE '/forbidden := \[\]string\{/,/^[[:space:]]*\}/ s/[^"]*"([^"]+)"[^"]*/\1 /gp' \
        examples/gastown/tmux_theme_script_test.go 2>/dev/null | tr ' ' '\n' | grep -v '^$' || true
)
if [ -z "$pack_roles" ]; then
    fail "no role names extracted from the forbidden list in
  examples/gastown/tmux_theme_script_test.go. That list is this gate's second
  taxonomy source and an empty one silently narrows what is refused.
  Remedy: restore the 'forbidden := []string{' form in
  TestTmuxThemeScriptHasNoHardcodedRoleNames, or repoint this extraction."
fi

roles=$(printf '%s\n%s\n' "$runtime_roles" "$pack_roles" | sort -u)

# --- code, with full-line comments removed ---

# Full-line comments only. Truncating a line at its first // would delete the
# tail of any URL literal, which is the one direction that turns a violation
# into a pass -- a role name inside a hardcoded URL would vanish. The cost is
# that a role name in a TRAILING comment is refused; the remedy is to move the
# comment onto its own line, and refusing too much is the survivable direction.
code_of() {
    sed -E 's/^[[:space:]]+//' "$1" | grep -vE '^(//|#|\*|/\*)' || true
}

# --- criterion 8, the alerts seam by name ---

# The seam's destination keys and its two scripts. SLACK_BOT_TOKEN is here
# because it was the one hole the gs-8ra merge left open: the literal arm below
# catches xoxb- and xapp- STRINGS, so a bridge reading the alert seam's own
# bot-token key by name passed the whole gate clean (gs-fn6).
#
# hooks.slack.com is the same threat with no env key at all -- a hardcoded
# incoming-webhook URL names none of the three keys and carries no xoxb- token,
# so every other arm passes it.
#
# The three destination keys match on identifier boundaries, the scripts and
# the host as substrings, and the asymmetry is load-bearing. Criterion 8
# REQUIRES the bridge to carry keys of its own, and the shape they take in
# contrib/openclaw-bridge/slack-bridge.mjs is BRIDGE_SLACK_CHANNEL_ID and
# BRIDGE_SLACK_BOT_TOKEN -- each contains a seam key, so a substring match
# refuses the correct isolation and reads as a real criterion-8 violation. The
# scripts stay substrings because they are reached through a path, and there is
# no boundary before notify.sh in assets/scripts/notify.sh.
#
# Rejected: dropping the keys from this list once the bridge grew its own. A
# later edit could then read the alert destination directly, which is the whole
# of the criterion. What separates the bridge's key from the alerts key is the
# boundary, not the presence of the name.
# Pinned by TestExtmsgBridgeIsolationAcceptsANamespacedBridgeKey.
#
# Rejected as redundant, and recorded so it is not re-proposed: origin's
# refusal 7, a comm -12 of these three names against the knobs the module graph
# reads through required(). Boundary-matching the names over the whole scan set
# is strictly broader -- it also refuses process.env.SLACK_BOT_TOKEN, a shell
# ${SLACK_BOT_TOKEN}, and any file the module graph does not reach -- so the
# intersection would only restate a subset of what this already refuses.
alert_seam=(
    SLACK_CHANNEL_ID SLACK_WEBHOOK_URL SLACK_BOT_TOKEN
    notify.sh slack-deliver hooks.slack.com
)

# Run over two different texts, because criterion 8 is reachable two ways.
#
# For CODE the text is comment-stripped: documenting the prohibition is the
# expected thing for an engineer to do, and refusing that comment would get
# this check deleted rather than obeyed.
#
# For PROSE the text is the raw file, and the same allowance is NOT made. A
# README has no code for a comment to sit beside -- it is instruction, and
# nothing mechanical separates "never point this at SLACK_WEBHOOK_URL" from
# "point this at SLACK_WEBHOOK_URL". The over-refusal is in the same direction
# as the trailing-comment refusal above, the remedy is to name the city's alert
# seam rather than its keys, and refusing too much is the survivable direction.
refuse_alerts_seam() {
    local path=$1 text=$2 kind=$3 seam hit remedy

    case "$kind" in
        prose) remedy="Remedy: describe the boundary by naming the city's alert seam, not the
  keys or scripts that reach it -- an operator acts on this file." ;;
        *)     remedy="Remedy: the bridge needs its own BRIDGE_SLACK_* keys, and must not call
  notify.sh or slack-deliver.py." ;;
    esac

    for seam in "${alert_seam[@]}"; do
        case "$seam" in
            SLACK_*) hit=$(printf '%s\n' "$text" | grep -nE -- "\\b${seam}\\b") || continue ;;
            *)       hit=$(printf '%s\n' "$text" | grep -nF -- "$seam") || continue ;;
        esac
        echo "$hit" >&2
        fail "$path reaches the alerts seam through '$seam' (see above).
  The alerts channel is one-way by decision, not by oversight (pm-log #57), and
  the two channels are deliberately not unified. SLACK_CHANNEL_ID,
  SLACK_WEBHOOK_URL and SLACK_BOT_TOKEN name the ALERT destination in
  city assets/scripts/slack-deliver.py.
  $remedy"
    done
}

# A binding key is one whose name ends in what it binds. That convention is
# what makes the shape arm possible: GC_TARGET_SESSION and GC_SESSION_NAME
# match, GC_SESSION_LOG_DIR does not, so an ordinary default on an ordinary key
# is left alone. A key named against the convention is invisible to this arm --
# the name arm is what covers it, and the contract test pins both boundaries.
bind_key='[A-Z][A-Z0-9_]*(SESSION|AGENT|TARGET|HANDLE|ROLE)(_(NAME|ID))?'
default_forms=(
    "env\\(['\"]${bind_key}['\"][[:space:]]*,[[:space:]]*['\"][^'\"]+['\"]"
    "process\\.env\\.${bind_key}[[:space:]]*(\\|\\||\\?\\?)[[:space:]]*['\"][^'\"]+['\"]"
    "process\\.env\\[['\"]${bind_key}['\"]\\][[:space:]]*(\\|\\||\\?\\?)[[:space:]]*['\"][^'\"]+['\"]"
    "\\\$\\{${bind_key}:-[^}]+\\}"
)

# --- configuration knobs, accumulated across the module files ---
#
# Requiredness is a property of the KNOB, not of one call site, so the two sets
# are built as the loop below walks the module files and compared once it ends.
# The refusal cannot be a per-file arm: the failure it names is a convenience
# default added in ANOTHER file next to the required() read that already
# exists, which is exactly why it survives review.
#
# The loop reaches these through the same `*.mjs` test module_set is built
# from, rather than iterating module_set separately, so the readability guard
# and the comment stripping apply to them once each.
#
# Both sets come from the source itself, so no list is kept here and a knob
# added tomorrow is covered the moment it is read. Both quote styles are
# accepted because nothing in this repository lints quote style, and a set
# built from one style silently omits every knob written in the other.
knob_ident='[A-Z_][A-Z0-9_]*'
required_knobs=
defaulted_knobs=

# `while read` rather than `for path in $scan_set`: word splitting would turn
# one path containing a space into two paths that do not exist, and grep
# answers nonzero for a missing file, which the `if` below reads as "clean".
# The readability guard is the same hole from the other side -- a tracked file
# deleted from the worktree without staging the deletion is still listed by
# `git ls-files`, and skipping it would drop a bridge file from the scan while
# the gate still reported OK over the count that included it.
while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ ! -r "$path" ]; then
        fail "$path is in the derived scan set but is not readable, so it was
  never inspected. A tracked file deleted from the worktree without staging
  the deletion produces exactly this.
  Remedy: restore it, or stage the deletion so it leaves the scan set."
    fi
    code=$(code_of "$path")

    # Criterion 5, first arm: a role name the tree already knows, used in code.
    while IFS= read -r role; do
        [ -n "$role" ] || continue
        if hit=$(printf '%s\n' "$code" | grep -niwF -- "$role"); then
            echo "$hit" >&2
            fail "$path uses the role name '$role' in code (see above).
  The bridge binds a named session from config, so a role name in source makes
  the binding a property of the code instead. Read the session name from
  configuration and let the deployment supply it.
  Role list derived from internal/runtime/tmux/tmux.go and
  examples/gastown/tmux_theme_script_test.go."
        fi
    done <<<"$roles"

    # Criterion 5, second arm: the shape, for the role nobody listed. The
    # binding must be required, not defaulted -- GC_CITY in
    # contrib/openclaw-bridge/bridge.mjs is the pattern to copy: read with no
    # default, and the process exits when it is absent.
    for form in "${default_forms[@]}"; do
        if hit=$(printf '%s\n' "$code" | grep -nE -- "$form"); then
            echo "$hit" >&2
            fail "$path gives a session binding a literal default (see above).
  A default makes the binding work without configuration, which is how a role
  name gets hardcoded under a name this gate's role list does not carry.
  Require the value and exit when it is absent, the way bridge.mjs handles
  GC_CITY."
        fi
    done

    # Criterion 5, third arm, and the evasion the second one cannot see: a
    # fallback written straight onto a raw process.env read, so the knob never
    # reaches env() at all and its name never has to end in what it binds.
    # Forbidding the SHAPE means the refusal holds whatever the default spells.
    #
    # lib/gc-client.mjs's `export const env =` line is the ONE sanctioned
    # exception, exempted by its definition TEXT rather than by filename, so a
    # second default hidden elsewhere in that same file is still refused. It
    # cannot itself hide a default: its fallback is its own second argument,
    # supplied by the caller this gate is reading.
    #
    # That exemption is inert against the helper as written today --
    # contrib/openclaw-bridge/lib/gc-client.mjs:12 spells the fallback with
    # !== tests and matches nothing, measured 2026-09-07. It is carried because
    # the obvious simplification of that line, `process.env[k] ?? d`, does
    # match, and a gate that refuses the one place defaults are allowed to live
    # gets deleted rather than obeyed.
    case "$path" in
        *.mjs)
            if hit=$(printf '%s\n' "$code" | grep -nE -- 'process\.env[^=]*(\|\||\?\?|\?)' |
                grep -v 'export const env ='); then
                echo "$hit" >&2
                fail "$path puts a fallback on a raw process.env read (see above).
  A default written inline is invisible to the knob-drift refusal, which reads
  env() and required() call sites, so the knob can be required elsewhere and
  silently defaulted here.
  Remedy: route the default through env(name, default) from lib/gc-client.mjs,
  which is the one place a bridge default is written, or use required(name)
  when there must not be one."
            fi

            required_knobs="$required_knobs$(printf '%s\n' "$code" |
                { grep -oE "required\\(['\"]${knob_ident}['\"]" || true; } |
                sed -E "s/.*['\"](${knob_ident})['\"].*/\\1/")"$'\n'
            defaulted_knobs="$defaulted_knobs$(printf '%s\n' "$code" |
                { grep -oE "env\\(['\"]${knob_ident}['\"][[:space:]]*," || true; } |
                sed -E "s/.*['\"](${knob_ident})['\"].*/\\1/")"$'\n'
            ;;
    esac

    refuse_alerts_seam "$path" "$code" code

    # Credential and conversation-id literals, over the WHOLE file rather than
    # the comment-stripped code: a token in a comment is still a token in the
    # repository.
    #
    # A Slack conversation id is C, G or D followed by eight or more uppercase
    # alphanumerics. All three prefixes are refused: a mirror pointed at a
    # private group or a DM is not the alerts channel, but it is still a
    # destination bound in code instead of in configuration, and the narrower
    # C-only form also required the DIGIT in position two, which Slack does not
    # promise.
    #
    # The digit is required somewhere in the token instead, and that filter is
    # what makes the widened shape usable: the shape alone matches shouted
    # English -- CONVERSATION, CREDENTIALS, DESTINATION -- and it does here.
    # Measured 2026-09-07 across the whole scan set, the only shape-only hit is
    # a banner string reading CONVERSATION in
    # contrib/openclaw-bridge/demo-telegram.sh:203. An id carries digits; a
    # word in caps does not.
    #
    # ABSENCE: an all-letter conversation id walks past this. Nothing
    # distinguishes such a token from prose, and buying it would cost every
    # capitalized word on the bridge path. The backstops are the seam arm above
    # and the required() read that makes a literal redundant in the first place.
    #
    # `grep -o` prints line:match and awk keeps the matches carrying a digit,
    # so the two greps cannot be one: awk always exits 0, and folding the token
    # patterns into the same pipeline would make their verdict unreadable.
    id_hit=$(grep -noE "\\b[CGD][A-Z0-9]{8,}\\b" -- "$path" | awk -F: '$NF ~ /[0-9]/' || true)
    tok_hit=$(grep -nE "xox[bpa]-|xapp-" -- "$path" || true)
    if [ -n "$id_hit$tok_hit" ]; then
        printf '%s\n' "$id_hit" "$tok_hit" | grep -v '^$' >&2 || true
        fail "$path carries a Slack conversation id or token literal (see above).
  Credentials and conversation ids live in \${GC_HOME}/secrets.env, never in
  city.toml and never in the repository (criterion 6). A hardcoded id is also
  how the mirror silently becomes a second writer to the alerts channel.
  Remedy: read the id from the environment the supervisor provides. If this is
  not a conversation id, rename the constant so it does not read as one."
    fi
done <<<"$scan_set"

# --- criterion 8 in the components' prose ---
#
# Only the seam arm runs here. The role arm must NOT follow it in: a README
# naming the session an operator binds is criterion 5 being SATISFIED -- it
# shows the identity arriving as configuration -- and neither must the literal
# arm, because a placeholder is how a README tells an operator the shape of
# the value to supply, and refusing the example teaches the next author to
# stop writing examples. The measured case is the credential half:
# contrib/openclaw-bridge/README.md:298-299 carries
# BRIDGE_SLACK_APP_TOKEN=xapp-... and BRIDGE_SLACK_BOT_TOKEN=xoxb-..., which
# the token pattern matches. Its channel example is BRIDGE_SLACK_CHANNEL_ID=
# C012345, one character too short for the conversation-id shape to reach, so
# that half of the exclusion is not exercised by this tree -- it is here for
# the full-length example the next author writes, on the same reasoning.
#
# The readability guard is the same one the code loop carries, for the same
# reason: a tracked file deleted from the worktree without staging the deletion
# would leave the prose scan silently while the count below still included it.
while IFS= read -r path; do
    [ -n "$path" ] || continue
    if [ ! -r "$path" ]; then
        fail "$path is in the derived prose set but is not readable, so it was
  never inspected. A tracked file deleted from the worktree without staging
  the deletion produces exactly this.
  Remedy: restore it, or stage the deletion so it leaves the scan."
    fi
    refuse_alerts_seam "$path" "$(cat "$path")" prose
done <<<"$prose_set"

# --- a required knob never acquires a default ---
#
# Reached only when every per-file arm above passed, because this script is
# fail-fast. That ordering is deliberate: a per-file violation names one line,
# and this one names a knob whose two halves sit in different files, so the
# specific finding should be the one an author sees first.
#
# `grep -v '^$'` on the comm output is load-bearing. Both accumulators end in a
# newline and are empty on a tree with no module files, so without it comm
# reports the empty line as common to both sets and every clean tree is refused
# for a knob with no name.
knob_drift=$(
    comm -12 \
        <(printf '%s\n' "$required_knobs" | sort -u) \
        <(printf '%s\n' "$defaulted_knobs" | sort -u) |
        grep -v '^$' || true
)
if [ -n "$knob_drift" ]; then
    echo "$knob_drift" >&2
    fail "the knob(s) above are read with required() in one place and given a
  default in another. A required knob that acquires a default stops failing
  fast on a missing configuration and starts binding to the default instead,
  which is how a role name lands under a key this gate's role list does not
  carry.
  Remedy: keep the knob required everywhere, or make it optional everywhere
  and say in the README what the default binds to."
fi

# Four counts, because a scope that silently collapsed to a handful of files is
# the one way every refusal above passes while checking almost nothing, and
# these numbers are the only place that shows.
printf 'check-extmsg-bridge-isolation: OK (%s bridge-path files, %s in the module graph, %s component docs, %s role names, alerts seam unreachable)\n' \
    "$(printf '%s\n' "$scan_set" | grep -c .)" \
    "$(printf '%s\n' "$module_set" | grep -c .)" \
    "$(printf '%s\n' "$prose_set" | grep -c .)" \
    "$(printf '%s\n' "$roles" | grep -c .)"
