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
# that from files this repository owns.
#
# Sources: docs/roadmap.md (epic:mayor-slack-bridge, criteria 5 and 8),
# docs/pm-log.md #57 (the alert seam's one-way rejection) and #58 (Socket Mode,
# assistant-turns-only), bead gs-8ra.
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
    {
        git ls-files -- '*.go' | xargs grep -l '^package main' 2>/dev/null |
            xargs -n1 dirname
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
        # shellcheck disable=SC2086 -- word splitting is the point: each
        # component is a separate pathspec, and component paths cannot
        # contain whitespace (they are package.json directories).
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

if [ "$list_only" -eq 1 ]; then
    printf '%s\n' "$scan_set"
    exit 0
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

    # Criterion 8: the alerts seam. Its destination keys and its two scripts,
    # matched against comment-stripped code -- documenting the prohibition is
    # the expected thing for an engineer to do, and refusing that comment
    # would get this check deleted rather than obeyed.
    for seam in SLACK_CHANNEL_ID SLACK_WEBHOOK_URL notify.sh slack-deliver; do
        if hit=$(printf '%s\n' "$code" | grep -nF -- "$seam"); then
            echo "$hit" >&2
            fail "$path reaches the alerts seam through '$seam' (see above).
  The alerts channel is one-way by decision, not by oversight (pm-log #57), and
  the two channels are deliberately not unified. SLACK_CHANNEL_ID and
  SLACK_WEBHOOK_URL name the ALERT destination in
  city assets/scripts/slack-deliver.py -- the bridge needs its own channel key,
  and must not call notify.sh or slack-deliver.py."
        fi
    done

    # Credential and channel literals, over the WHOLE file rather than the
    # comment-stripped code: a token in a comment is still a token in the
    # repository. Channel ids only (C-prefixed). A user or DM id is
    # deliberately not matched -- a mirror pointed at a DM is not the
    # alerts-channel threat this criterion names, and the token patterns cover
    # the credential half.
    if hit=$(grep -nE "\\bC[0-9][A-Z0-9]{7,}\\b|xox[bpa]-|xapp-" -- "$path"); then
        echo "$hit" >&2
        fail "$path carries a Slack channel id or token literal (see above).
  Credentials and channel ids live in \${GC_HOME}/secrets.env, never in
  city.toml and never in the repository (criterion 6). A hardcoded channel id
  is also how the mirror silently becomes a second writer to the alerts
  channel.
  Remedy: read the id from the environment the supervisor provides."
    fi
done <<<"$scan_set"

printf 'check-extmsg-bridge-isolation: OK (%s bridge-path files, %s role names, alerts seam unreachable)\n' \
    "$(printf '%s\n' "$scan_set" | grep -c .)" \
    "$(printf '%s\n' "$roles" | grep -c .)"
