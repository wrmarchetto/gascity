package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/shellquote"
)

// bdReadyPoolDemandShell returns the canonical bd ready predicate for
// unassigned, non-epic pool demand routed to target. gc.routed_to is the
// canonical persisted routing key: the graph.v2 stamper and the legacy stamper
// both stamp it on every routable bead, including the workflow root (ga-eld2x
// retired the short-lived gc.run_target wire field). This predicate is the main
// source of truth for "is there work on this routed queue?" that both the
// worker (via EffectiveWorkQuery Tier 3) and the reconciler (via
// EffectivePoolDemandQuery, count-form) ask; diverging the two re-introduces
// the protocol-mismatch class (see the "scale_check ↔ work_query
// correspondence" note in engdocs/architecture/dispatch.md).
//
// target is passed as a positional argument to the outer sh -c command, not
// interpolated into the nested shell body. That keeps routes containing shell
// metacharacters as data instead of executable syntax.
func bdReadyIncludeEphemeralArg(includeEphemeralReady bool) string {
	if includeEphemeralReady {
		return " --include-ephemeral"
	}
	return ""
}

// excludeHoldLabelsShellArgs renders a repeated --exclude-label flag for
// every beadmeta.DispatchHoldLabels value, so route-scoped, unassigned
// pool-demand queries never surface a bead intentionally parked on a
// dispatch hold (ga-x9kptu / ga-5736js). Assignee-scoped tiers (Tier 1/2)
// must stay hold-transparent by design and must never call this.
func excludeHoldLabelsShellArgs() string {
	var args string
	for _, label := range beadmeta.DispatchHoldLabels {
		args += ` --exclude-label "` + label + `"`
	}
	return args
}

// excludeHoldLabelsJQClause returns a jq select(...) clause dropping beads
// that carry any beadmeta.DispatchHoldLabels value, for jq-based pool-demand
// filters that have no bd-side --exclude-label flag to lean on. Mirrors the
// bracketed-count style of the dependency-blocking select above it so both
// clauses read the same way (ga-x9kptu / ga-5736js).
func excludeHoldLabelsJQClause() string {
	conds := make([]string, len(beadmeta.DispatchHoldLabels))
	for i, label := range beadmeta.DispatchHoldLabels {
		conds[i] = `. == "` + label + `"`
	}
	return ` | select(([ (.labels // [])[] | select(` + strings.Join(conds, " or ") + `) ] | length) == 0)`
}

// ExcludeMessageTypeArg keeps mail out of the ASSIGNED work tiers.
//
// A message bead carries the recipient in `assignee`, which is the identical
// shape a real assigned bead has, so `bd ready --assignee=<id>` returns it as
// work. The claim side already refuses it (hookClaimCandidateIsMessage in
// cmd/gc/cmd_hook_claim.go) and that asymmetry is the whole bug: demand said
// work exists, the claim handed out nothing, the session drain-acked, and
// nothing about the message changed -- so the next tick spawned another
// session, forever, at boot cadence. Worse, the assigned tier exits the
// ladder on its first hit, so a message also HID the routed work waiting
// below it in the same batch (#4419 named this: "ahead of any real routed
// work waiting in the same batch"; it taught the claim, not the demand).
//
// This is applied where DEMAND is computed, so both sides now share one
// predicate. The invariants it pins are in
// workquery_message_displacement_test.go.
//
// It is EXPORTED for one consumer, and the export is the point: the control
// dispatcher's readiness scan (cmd/gc/dispatch_runtime.go) is a second,
// hand-written copy of this same assignee-scoped predicate, and the copy is
// what let 0de389a72 fix one path and leave the other spawning work-blind
// sessions (ci-bhvf). A future edit here now reaches both. Do not re-inline
// the literal at either site.
//
// Documented absence: the route-scoped tiers (bdReadyPoolDemandShell,
// routed_ready in the control scan) deliberately do NOT carry it. They pass
// --unassigned and mail always carries its recipient in assignee, so the flag
// would filter nothing while implying a vector that does not exist. The one
// route-scoped tier that DOES carry it, bdReadyPoolAliasDemandShell, matches
// on assignee rather than on emptiness -- see its own comment.
//
// Documented absence: mail deliberately does NOT create demand and will NOT
// spawn or sustain a session. That is not a regression to restore -- a
// session cannot consume its own mail, because `gc hook --claim` is its FIRST
// command, so it drained before ever reading the message that spawned it.
// Mail is read by a live session; it is not a reason to start one.
const ExcludeMessageTypeArg = ` --exclude-type=message`

// excludeMessageJQClause is the jq form of ExcludeMessageTypeArg, for the
// ephemeral tiers that filter in jq because `bd query` has no --exclude-type.
// Both spellings of the field are read: `bd` emits issue_type, some legacy
// rows carry type.
func excludeMessageJQClause() string {
	return ` | select((((.issue_type // .type // "") | ascii_downcase)) != "message")`
}

// jqMeta renders the jq expression that reads a bead-metadata key with an
// empty-string default, e.g. (.metadata["gc.routed_to"] // ""). Shell/jq
// builders use it so embedded key spellings stay anchored to the beadmeta
// vocabulary constants.
func jqMeta(key string) string {
	return `(.metadata["` + key + `"] // "")`
}

func bdReadyPoolDemandShell(limitFlag string, includeEphemeralReady bool) string {
	return `bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --metadata-field "` + beadmeta.RoutedToMetadataKey + `=$target" --unassigned --exclude-type=epic` + excludeHoldLabelsShellArgs() + ` --json ` + limitFlag
}

// bdReadyPoolAliasDemandShell returns the canonical bd ready predicate for work
// PARKED ON the pool alias itself -- hand-assigned with
// `bd update --assignee <pool>` rather than routed with gc.routed_to.
//
// It exists because bd matches --assignee exactly and the assigned tiers only
// ever probe $GC_SESSION_ID, $GC_SESSION_NAME and $GC_ALIAS. At
// max_active_sessions=1 GC_ALIAS IS the bare pool name (see
// Agent.UsesCanonicalSingletonPoolIdentity), so hand-assigned work matched there;
// above 1 every slot's GC_ALIAS is its own suffixed name and nothing carries the
// bare name any more, making the bead unclaimable by any slot with no error and
// no log line (ci-c000 measured an entire historical queue stranded this way).
//
// Adding the bare name as a fourth entry in the assigned loop was the rejected
// alternative. That name is ALSO a [[named_session]] holder's own identity, so
// treating it as own-identity work lets a suffixed slot adopt the holder's live
// in_progress bead -- the ga-80pen8 regression. This tier is therefore
// route-scoped like the unassigned pool-demand tier beside it: it runs behind the
// GC_SESSION_ORIGIN gate, and the claim side takes the bead by atomic transfer
// (BdStore.PoolClaim) rather than by adoption.
//
// The exclusions are load-bearing, not tidiness:
//
//   - --exclude-type=message: mail carries its recipient in `assignee`, so mail
//     addressed to the pool by name is indistinguishable from pool-assigned work
//     at the bd level. Left in, it raises demand no session can consume, and the
//     spawn repeats at boot cadence forever (#4419; see ExcludeMessageTypeArg).
//     Measured bound on that claim, bd 1.1.1: mail materializes as an EPHEMERAL
//     message wisp, and `bd ready --include-ephemeral --assignee=<pool>` does not
//     return one, so today this exclusion is latent -- removing it would not
//     regress anything observable, because the tier has no ephemeral probe for
//     mail to arrive through (see the absence noted below). It stays because the
//     protection has to already be in place if either of those two facts
//     changes, and because the tests can only assert the flag's presence, never
//     its effect, while mail cannot reach here.
//   - --exclude-type=epic: a parent epic has no executable spec, so a worker
//     claiming one does undefined work (gc-udx).
//   - hold labels: this tier creates spawn demand, so a bead deliberately parked
//     on a dispatch hold must not raise it (ga-x9kptu / ga-5736js).
//
// Documented absence: there is no ephemeral (wisp) counterpart to this tier.
// Ephemeral beads are materialized by formulas, which route with gc.routed_to and
// leave assignee empty; nothing hand-assigns a wisp to a pool name. Adding one
// would mean duplicating legacyEphemeralPoolDemandShell's jq filter for a
// retirement-window path that has no producer.
func bdReadyPoolAliasDemandShell(limitFlag string, includeEphemeralReady bool) string {
	return `bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --assignee="$target" --exclude-type=epic` + ExcludeMessageTypeArg + excludeHoldLabelsShellArgs() + ` --json ` + limitFlag
}

// bdReadyPoolDemandMigrationShell is a temporary raw compatibility probe for
// graph.v2 workflow roots created before gc.routed_to root stamping shipped.
// It is scoped to workflow roots so gc.run_target remains an authoring hint
// everywhere else. Callers must pass its output through
// poolDemandMigrationFilterJQ so a stale divergent gc.run_target cannot remain
// visible once a root carries gc.routed_to. This retirement-window fallback
// requires jq in the default worker/reconciler environment; remove it with the
// Go-side legacy candidates after the backfill completion tracked by ga-dhf44.
//
// The window is written here rather than taken from the caller, so both the
// count and first-row paths are unbounded by construction. This tier is the
// one that cannot survive a ceiling at all: its routed_to filter runs in jq
// AFTER bd applies the window, so a window filled entirely with
// already-backfilled roots reports an EMPTY queue while a pre-backfill root
// sits just past it -- a sharper form of the ci-rzq2 window defect than the
// canonical tiers have, since those at least return something claimable. The
// limitFlag parameter this replaced made re-bounding it a one-word edit at a
// call site; unparam flagged it dead once both callers agreed, and taking the
// hint makes the ceiling a compile error instead of a discouraged argument.
func bdReadyPoolDemandMigrationShell(includeEphemeralReady bool) string {
	return `bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --metadata-field "` + beadmeta.RunTargetMetadataKey + `=$target" --metadata-field "` + beadmeta.KindMetadataKey + `=` + beadmeta.KindWorkflow + `" --unassigned --exclude-type=epic` + excludeHoldLabelsShellArgs() + ` --json --sort oldest --limit 0`
}

func poolDemandMigrationFilterJQ(limit int) string {
	filter := `[.[] | select(` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == "")]`
	if limit > 0 {
		filter += ` | .[:` + strconv.Itoa(limit) + `]`
	}
	return shellquote.Join([]string{"jq", filter})
}

func bdQueryEphemeralStatusShell(status string) string {
	return `bd query --json ` + shellquote.Quote("ephemeral=true AND status="+status) + ` --limit=0`
}

func bdQueryEphemeralStatusQuietShell(status string) string {
	return bdQueryEphemeralStatusShell(status) + ` 2>/dev/null`
}

func legacyEphemeralReadyFilterJQ(selector string, limit int, excludeHoldLabels bool) string {
	body := selector +
		excludeMessageJQClause() +
		` | select(((.issue_type // .type // "") != "epic"))` +
		` | select(([ (.dependencies // [])[]` +
		` | select((.type // .dep_type // "") as $t | ($t == "blocks" or $t == "waits-for" or $t == "conditional-blocks"))` +
		` | select((.status // .depends_on_status // "") != "closed") ] | length) == 0)`
	if excludeHoldLabels {
		body += excludeHoldLabelsJQClause()
	}
	filter := `[.[] | ` + body + `]` + ` | sort_by(.created_at // "")`
	if limit > 0 {
		filter += ` | .[:` + strconv.Itoa(limit) + `]`
	}
	return filter
}

func legacyEphemeralPoolDemandShell(limit int, includeEphemeralReady, quiet bool) string {
	if includeEphemeralReady {
		return `printf "[]"`
	}
	filter := legacyEphemeralReadyFilterJQ(
		`select((.assignee // "") == "")`+
			` | select((`+jqMeta(beadmeta.RoutedToMetadataKey)+` == $target) or ((`+jqMeta(beadmeta.RoutedToMetadataKey)+` == "") and (`+jqMeta(beadmeta.RunTargetMetadataKey)+` == $target) and (`+jqMeta(beadmeta.KindMetadataKey)+` == "`+beadmeta.KindWorkflow+`")))`,
		limit,
		true,
	)
	query := bdQueryEphemeralStatusShell("open")
	if quiet {
		query = bdQueryEphemeralStatusQuietShell("open")
	}
	jqStderr := ""
	if quiet {
		jqStderr = ` 2>/dev/null`
	}
	return `{ ` + query + ` | jq --arg target "$target" ` + shellquote.Quote(filter) + jqStderr + `; } || printf "[]"`
}

// poolDemandFirstRowFunctionScript emits the work_query Tier 3 function: it
// reads the first ready, unassigned, routed bead for the supplied target,
// prints it, and exits 0. The caller appends a terminal fallthrough
// (printf "[]") for the empty case.
//
// No tier here caps its row count: a cap decides which beads are candidates at
// all, which is the ci-rzq2 defect (see routedReadyTierCommand). The lone
// surviving 20 is legacyEphemeralPoolDemandShell's, and it is deliberately left
// -- jq applies it AFTER the readiness filter and the caller then slices
// `.[0:1]`, so raising it would change no output, only the golden fixtures.
func poolDemandFirstRowFunctionScript(includeEphemeralReady bool) string {
	return `probe_pool_demand() { ` +
		`target="$1"; ` +
		`[ -z "$target" ] && return 1; ` +
		`r=$(` + routedReadyTierCommand(includeEphemeralReady) + `); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`r=$(` + poolAliasReadyTierCommand(includeEphemeralReady) + `); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`legacy_candidates=$(` + bdReadyPoolDemandMigrationShell(includeEphemeralReady) + ` 2>/dev/null); ` +
		`r=$(printf "%s" "$legacy_candidates" | ` + poolDemandMigrationFilterJQ(1) + ` 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`legacy_ephemeral_candidates=$(` + legacyEphemeralPoolDemandShell(20, includeEphemeralReady, true) + `); ` +
		`r=$(printf "%s" "$legacy_ephemeral_candidates" | jq '.[0:1]' 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		`return 1; ` +
		`}; `
}

// poolAliasReadyTierCommand is the worker first-row form of the pool-alias tier.
// It carries no row ceiling for the same reason as routedReadyTierCommand,
// stated there: a ceiling here decides candidacy, not just how much is read.
//
// `--sort oldest` is the documented routed-queue policy (FIFO before priority,
// engdocs/architecture/dispatch.md), not bd's default -- unflagged, bd orders
// ready work (priority, created_at, id). Do NOT "fix" this to priority in
// isolation: it is the ordering the whole pool queue is scheduled on, a strict
// priority sort starves the long P3 tail, and the policy statement has to move
// with it (TestRoutedQueueOrderingPolicyMatchesEmittedWorkQuery).
//
// This tier reached the flag by copying the routed tier's shape (ci-c000) at a
// time when only the routed tier's ordering was written down, so ci-q2vx read
// the resulting FIFO here as a dispatch bug. It is deliberate, and now stated.
func poolAliasReadyTierCommand(includeEphemeralReady bool) string {
	return bdReadyPoolAliasDemandShell("--sort oldest --limit 0", includeEphemeralReady) + ` 2>/dev/null`
}

func routedReadyTierCommand(includeEphemeralReady bool) string {
	// The shared predicate stays order-free so the count-form does no wasted
	// sorting; the worker first-row path asks bd for the oldest candidates.
	//
	// `--sort oldest` is policy, not a default: it overrides bd's own
	// (priority, created_at, id) ready order so newer high-priority work does
	// not jump the queue (engdocs/architecture/dispatch.md, PR #2800). See
	// poolAliasReadyTierCommand for why it must not be changed in isolation.
	//
	// The window is unbounded so the sort decides ORDER only, never candidacy.
	// At --limit=20 (the shape through ci-rzq2) ready work past the oldest 20
	// was never returned by bd, so no Go-side reordering could reach it and it
	// became claimable only as the head drained: 41 ready rows on one pool
	// alias with a P1 at row 21, measured on the city store 2026-08-09.
	//
	// Do NOT re-bound this to a larger constant -- that is the same defect
	// further out. The limit was only ever load-bearing for a second property,
	// that a self-blocked head (is_blocked / status==blocked) has ready routed
	// work behind it to fall through to instead of idle-exiting, since the hook
	// layer (filterUnreadyHookCandidates) strips the blocked head (#3881). Any
	// window wider than one row satisfies that, unbounded included. The read is
	// already paid for: poolDemandCountShell runs this same predicate at
	// --limit 0 on every reconciler tick, more often than a session boots, and
	// the claim loop over the candidates is bounded by hookClaimMutationTimeout
	// rather than by the row count.
	return bdReadyPoolDemandShell("--sort oldest --limit 0", includeEphemeralReady) + ` 2>/dev/null`
}

// poolDemandCountShell emits the reconciler count-form for target: it counts
// ready routed demand -- unassigned, plus beads hand-assigned to the pool name
// itself -- and prints the array length. It shares the canonical, pool-alias and
// migration predicates with poolDemandFirstRowFunctionScript so the reconciler's
// spawn decision and the worker's claim decision read the same demand shape.
// Counting a shape the worker can claim but the reconciler cannot see leaves the
// bead unworked with no session ever spawned (ci-rdbw); counting one the worker
// cannot claim is the wake/drain spawn storm of PR #1516.
//
// Unlike the work_query probe, this form must NOT redirect bd stderr or default
// to zero: a failed `bd ready` has to surface as an error rather than
// masquerade as "no demand", which would silently stop the pool from spawning.
// The && chain ensures any non-zero bd exit short-circuits the whole expression
// (TestEffectiveScaleCheckUsesReadyOnly).
func poolDemandCountShell(target string, includeEphemeralReady bool) string {
	script := `target="$1"; ` +
		`ready_json=$(` + bdReadyPoolDemandShell("--limit 0", includeEphemeralReady) + `) || exit $?; ` +
		`alias_json=$(` + bdReadyPoolAliasDemandShell("--limit 0", includeEphemeralReady) + `) || exit $?; ` +
		`legacy_candidates=$(` + bdReadyPoolDemandMigrationShell(includeEphemeralReady) + `) || exit $?; ` +
		`legacy_json=$(printf "%s" "$legacy_candidates" | ` + poolDemandMigrationFilterJQ(0) + `) || exit $?; ` +
		`legacy_ephemeral_json=$(` + legacyEphemeralPoolDemandShell(0, includeEphemeralReady, false) + `); ` +
		`printf "%s\n%s\n%s\n%s\n" "$ready_json" "$alias_json" "$legacy_json" "$legacy_ephemeral_json" | jq -s "(add // []) | unique_by(.id) | length"`
	return shellquote.Join([]string{"sh", "-c", script, "--", target})
}

func (a *Agent) poolDemandTarget() string {
	target := a.QualifiedName()
	if a.PoolName != "" {
		target = a.PoolName
	}
	return target
}

func standardAssignedWorkQueryScript(includeEphemeralReady bool) string {
	return standardAssignedInProgressWorkQueryScript(includeEphemeralReady) +
		standardAssignedReadyWorkQueryScript(includeEphemeralReady)
}

func standardAssignedInProgressWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`r=$(bd list --status in_progress --assignee="$id"` + ExcludeMessageTypeArg + ` --json --limit=1 2>/dev/null); ` +
		`if [ -n "$r" ] && [ "$r" != "[]" ]; then ` +
		inProgressBlockedByEnrichmentScript("r") +
		`fi; ` +
		ephemeralAssignedInProgressProbeScript("id", includeEphemeralReady) +
		`done; `
}

// inProgressBlockedByEnrichmentScript hardens the in_progress "crash recovery"
// work-query tier against re-serving a bead that cannot progress.
//
// `bd list --status in_progress` does no readiness computation: unlike
// `bd ready` it emits neither blocked_by nor is_blocked. That makes the
// defensive hook-side filter (filterUnreadyHookCandidates ->
// isDepBlockedHookCandidate) a structural no-op for this tier, because an
// absent blocked_by is correctly read as "not blocked". A step that is
// in_progress + assigned but held by an open gate or an unclosed blocking
// dependency is therefore re-served on every hook tick, forever.
//
// `bd ready` cannot be substituted here: it excludes in_progress by design,
// so it would return nothing and defeat crash recovery entirely. Instead we
// read the candidate's own dependency rows and attach the blocked_by array
// the rest of the pipeline already knows how to interpret. When the candidate
// is blocked we skip it and fall through to the ready-gated tier, so a session
// holding one blocked step can still be served its other ready assigned work.
//
// Only ready-blocking dependency types are considered, matching
// beads.IsReadyBlockingDependencyType; parent-child and tracks edges never
// block readiness. Status interpretation is left to the shared Go filter:
// any non-closed blocker counts.
//
// Enrichment is fail-open: a failed or unparseable `bd show` / `bd list`
// degrades to the stock behavior of serving the candidate unchanged, never to
// dropping it, so a malformed or log-prefixed bd stdout can never disable
// crash recovery.
func inProgressBlockedByEnrichmentScript(shellVar string) string {
	const blockingDepsJQ = `[.[0].dependencies[]? | ` +
		`select(.dependency_type == "blocks" or .dependency_type == "waits-for" or ` +
		`.dependency_type == "conditional-blocks") | {id, status}]`
	const openBlockerCountJQ = `[.[] | select(((.status // "") | ascii_downcase) != "closed")] | length`

	const enrichJQ = `map(. + {blocked_by: $bb})`

	v := `$` + shellVar
	// The enriched payload lands in a scratch var derived from shellVar so the
	// candidate itself is never clobbered: if jq fails (non-JSON or
	// log-prefixed `bd list` stdout) the original is served unchanged.
	enrichedVar := shellVar + `_enriched`
	e := `$` + enrichedVar
	return `bid=$(printf "%s" "` + v + `" | jq -r ".[0].id // empty" 2>/dev/null); ` +
		`bb="[]"; ` +
		`[ -n "$bid" ] && bb=$(bd show "$bid" --json 2>/dev/null | ` +
		`jq -c ` + shellquote.Quote(blockingDepsJQ) + ` 2>/dev/null); ` +
		`[ -z "$bb" ] && bb="[]"; ` +
		`nblocked=$(printf "%s" "$bb" | jq -r ` + shellquote.Quote(openBlockerCountJQ) + ` 2>/dev/null); ` +
		`[ -z "$nblocked" ] && nblocked=0; ` +
		`if [ "$nblocked" = "0" ]; then ` +
		enrichedVar + `=$(printf "%s" "` + v + `" | jq -c --argjson bb "$bb" ` +
		shellquote.Quote(enrichJQ) + ` 2>/dev/null); ` +
		`[ -n "` + e + `" ] && [ "` + e + `" != "[]" ] && ` + shellVar + `="` + e + `"; ` +
		`printf "%s" "` + v + `" && exit 0; ` +
		`fi; `
}

func standardAssignedReadyWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`r=$(bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --assignee="$id"` + ExcludeMessageTypeArg + ` --json --limit=1 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		ephemeralAssignedReadyProbeScript("id", includeEphemeralReady) +
		`done; `
}

func legacyControlAssignedWorkQueryScript(includeEphemeralReady bool) string {
	return legacyControlAssignedInProgressWorkQueryScript(includeEphemeralReady) +
		legacyControlAssignedReadyWorkQueryScript(includeEphemeralReady)
}

func legacyControlAssignedInProgressWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`legacy=""; case "$id" in *control-dispatcher) legacy="${id%control-dispatcher}workflow-control";; esac; ` +
		`for cand in "$id" "$legacy"; do ` +
		`[ -z "$cand" ] && continue; ` +
		`r=$(bd list --status in_progress --assignee="$cand"` + ExcludeMessageTypeArg + ` --json --limit=1 2>/dev/null); ` +
		`if [ -n "$r" ] && [ "$r" != "[]" ]; then ` +
		inProgressBlockedByEnrichmentScript("r") +
		`fi; ` +
		ephemeralAssignedInProgressProbeScript("cand", includeEphemeralReady) +
		`done; ` +
		`done; `
}

func legacyControlAssignedReadyWorkQueryScript(includeEphemeralReady bool) string {
	return `for id in "$GC_SESSION_ID" "$GC_SESSION_NAME" "$GC_ALIAS"; do ` +
		`[ -z "$id" ] && continue; ` +
		`legacy=""; case "$id" in *control-dispatcher) legacy="${id%control-dispatcher}workflow-control";; esac; ` +
		`for cand in "$id" "$legacy"; do ` +
		`[ -z "$cand" ] && continue; ` +
		`r=$(bd ready` + bdReadyIncludeEphemeralArg(includeEphemeralReady) + ` --assignee="$cand"` + ExcludeMessageTypeArg + ` --json --limit=1 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; ` +
		ephemeralAssignedReadyProbeScript("cand", includeEphemeralReady) +
		`done; ` +
		`done; `
}

func ephemeralAssignedInProgressProbeScript(shellVar string, includeEphemeralReady bool) string {
	_ = includeEphemeralReady
	return `r=$(` + bdQueryEphemeralStatusQuietShell("in_progress") + ` | ` +
		`jq --arg id "$` + shellVar + `" '[.[] | select((.assignee // "") == $id)` + excludeMessageJQClause() + `] | .[:1]' 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; `
}

func ephemeralAssignedReadyProbeScript(shellVar string, includeEphemeralReady bool) string {
	if includeEphemeralReady {
		return ""
	}
	filter := legacyEphemeralReadyFilterJQ(`select((.assignee // "") == $id)`, 1, false)
	return `r=$(` + bdQueryEphemeralStatusQuietShell("open") + ` | ` +
		`jq --arg id "$` + shellVar + `" ` + shellquote.Quote(filter) + ` 2>/dev/null); ` +
		`[ -n "$r" ] && [ "$r" != "[]" ] && printf "%s" "$r" && exit 0; `
}

func poolDemandOriginGateScript() string {
	return `case "$GC_SESSION_ORIGIN" in ` +
		`ephemeral|"") ;; ` +
		`*) exit 0 ;; ` +
		`esac; `
}

func routedPoolWorkQueryProbeScript(includeEphemeralReady bool, targetCount int) string {
	script := poolDemandOriginGateScript() + poolDemandFirstRowFunctionScript(includeEphemeralReady)
	for i := 1; i <= targetCount; i++ {
		script += fmt.Sprintf(`probe_pool_demand "$%d"; `, i)
	}
	return script + `printf "[]"`
}

func routedPoolWorkQueryCommand(includeEphemeralReady bool, targets ...string) string {
	args := []string{"sh", "-c", routedPoolWorkQueryProbeScript(includeEphemeralReady, len(targets)), "--"}
	args = append(args, targets...)
	return shellquote.Join(args)
}

// queryKind names one of the built-in agent query shapes.
type queryKind int

const (
	queryWork queryKind = iota
	queryAssignedInProgress
	queryAssignedReady
	queryRoutedPool
	queryPoolDemand
	queryOnDeath
	queryOnBoot
)

// querySpec describes how one query kind resolves: which user override
// field short-circuits the default, and how the default script is built.
type querySpec struct {
	// override returns the user-supplied command that replaces the
	// default entirely, or "" when the default applies.
	override func(*Agent) string
	// build returns the default command. includeEphemeralReady carries
	// beads.UsesBD105ReadySemantics(); the onDeath/onBoot builders ignore
	// it today and MUST keep ignoring it (S04b invariant I6).
	build func(a *Agent, includeEphemeralReady bool) string
}

// queryTable maps every query kind to its override field and default
// builder. It is populated once at init and only read afterward.
var queryTable = map[queryKind]querySpec{
	queryWork:               {override: func(a *Agent) string { return a.WorkQuery }, build: buildWorkQuery},
	queryAssignedInProgress: {override: func(a *Agent) string { return a.WorkQuery }, build: buildAssignedInProgressQuery},
	queryAssignedReady:      {override: func(a *Agent) string { return a.WorkQuery }, build: buildAssignedReadyQuery},
	queryRoutedPool:         {override: func(a *Agent) string { return a.WorkQuery }, build: buildRoutedPoolQuery},
	queryPoolDemand:         {override: func(a *Agent) string { return a.ScaleCheck }, build: buildPoolDemandQuery},
	queryOnDeath:            {override: func(a *Agent) string { return a.OnDeath }, build: buildOnDeath},
	queryOnBoot:             {override: func(a *Agent) string { return a.OnBoot }, build: buildOnBoot},
}

// effectiveQuery is the single resolver behind every Effective*Query
// accessor: the kind's user override verbatim if set, else the kind's
// default builder.
func (a *Agent) effectiveQuery(kind queryKind, includeEphemeralReady bool) string {
	spec := queryTable[kind]
	if o := spec.override(a); o != "" {
		return o
	}
	return spec.build(a, includeEphemeralReady)
}

// effectiveQueryForBeads resolves a kind using the bd compatibility
// semantics configured for the city.
func (a *Agent) effectiveQueryForBeads(kind queryKind, beads BeadsConfig) string {
	return a.effectiveQuery(kind, beads.UsesBD105ReadySemantics())
}

// EffectiveWorkQuery returns the work query command for this agent.
// If WorkQuery is set, returns it as-is. Otherwise returns the default
// three-tier query with multi-identifier assignee resolution.
//
// Assignee resolution order: $GC_SESSION_ID (bead ID) > $GC_SESSION_NAME
// (tmux session name) > $GC_ALIAS (named identity / qualified name).
// All three are checked so work is found regardless of which identifier
// was used when assigning.
//
// State priority: in_progress+assigned (crash recovery) >
// ready+assigned (pre-assigned) > ready+unassigned+routed_to (pool) >
// ready+assigned-to-the-pool-name (hand-assigned pool work; see
// bdReadyPoolAliasDemandShell for why that last tier is route-scoped rather
// than a fourth identity in the assigned loop).
// Executable formula roots can be epic-typed; the bead storage policy decides
// whether those roots are history-backed, no-history, or ephemeral for the
// configured bd compatibility mode. Molecule containers are not routable
// demand.
//
// Parent epics are excluded from the routed (pool) tier only
// (--exclude-type=epic). An unassigned parent epic has no executable spec —
// its semantic is "all children done" — so a pool worker claiming one does
// undefined work (gc-udx; the repro is a routed parent epic, see
// TestEffectiveWorkQuerySkipsEpicLeafScenario). The assigned tiers do NOT
// exclude epics: work already assigned to this agent is owned, and the
// patrol-loop pattern (gastown witness/refinery/deacon) can self-assign an
// epic wisp that the agent must resume after a session restart. Excluding
// epics there silently stranded those wisps (gc hook exited 1 with empty
// output). Roles that need different behavior still opt in via an explicit
// work_query in their agent config; that custom query is returned unchanged
// above.
//
// When the reconciler runs the query for demand detection (no session
// context), all identity vars are empty → assignee tiers skip → only
// the routed_to tier fires to detect new demand.
//
// Tier 3's canonical and migration predicates are shared with
// EffectivePoolDemandQuery so reconciler spawn decisions and worker claim
// decisions stay symmetric.
func (a *Agent) EffectiveWorkQuery() string {
	return a.effectiveQuery(queryWork, false)
}

// EffectiveWorkQueryForBeads returns the default work query using the bd
// compatibility semantics configured for the city.
func (a *Agent) EffectiveWorkQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryWork, beads)
}

func buildWorkQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	legacyTarget := legacyWorkflowControlQualifiedName(target)
	if legacyTarget == "" {
		script := standardAssignedWorkQueryScript(includeEphemeralReady) +
			poolDemandOriginGateScript() +
			poolDemandFirstRowFunctionScript(includeEphemeralReady) +
			`probe_pool_demand "$1"; ` +
			`printf "[]"`
		return shellquote.Join([]string{"sh", "-c", script, "--", target})
	}
	script := legacyControlAssignedWorkQueryScript(includeEphemeralReady) +
		poolDemandOriginGateScript() +
		poolDemandFirstRowFunctionScript(includeEphemeralReady) +
		`probe_pool_demand "$1"; ` +
		`probe_pool_demand "$2"; ` +
		`printf "[]"`
	return shellquote.Join([]string{"sh", "-c", script, "--", target, legacyTarget})
}

// EffectiveAssignedInProgressQuery returns the assigned-in-progress-only command
// for prompt templates that spell out crash recovery as a separate startup tier.
// A custom WorkQuery is treated as the caller-owned full discovery contract, so
// split-tier prompts may run that same custom command in each query slot.
func (a *Agent) EffectiveAssignedInProgressQuery() string {
	return a.effectiveQuery(queryAssignedInProgress, false)
}

// EffectiveAssignedInProgressQueryForBeads returns the assigned-in-progress
// query using the bd compatibility semantics configured for the city.
func (a *Agent) EffectiveAssignedInProgressQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryAssignedInProgress, beads)
}

func buildAssignedInProgressQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	if legacyWorkflowControlQualifiedName(target) != "" {
		return shellquote.Join([]string{"sh", "-c", legacyControlAssignedInProgressWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
	}
	return shellquote.Join([]string{"sh", "-c", standardAssignedInProgressWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
}

// EffectiveAssignedReadyQuery returns the assigned-ready-only command for
// prompt templates that spell out claim-first startup in separate tiers. A
// custom WorkQuery is treated as the caller-owned full discovery contract, so
// split-tier prompts may run that same custom command in each query slot.
func (a *Agent) EffectiveAssignedReadyQuery() string {
	return a.effectiveQuery(queryAssignedReady, false)
}

// EffectiveAssignedReadyQueryForBeads returns the assigned-ready-only query
// using the bd compatibility semantics configured for the city.
func (a *Agent) EffectiveAssignedReadyQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryAssignedReady, beads)
}

func buildAssignedReadyQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	if legacyWorkflowControlQualifiedName(target) != "" {
		return shellquote.Join([]string{"sh", "-c", legacyControlAssignedReadyWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
	}
	return shellquote.Join([]string{"sh", "-c", standardAssignedReadyWorkQueryScript(includeEphemeralReady) + `printf "[]"`})
}

// EffectiveRoutedPoolQuery returns the routed-pool-only command for prompt
// templates that spell out claim-first startup in separate tiers. It is the
// prompt-side counterpart to EffectiveWorkQuery's routed pool tier.
func (a *Agent) EffectiveRoutedPoolQuery() string {
	return a.effectiveQuery(queryRoutedPool, false)
}

// EffectiveRoutedPoolQueryForBeads returns the routed-pool-only command using
// the bd compatibility semantics configured for the city.
func (a *Agent) EffectiveRoutedPoolQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryRoutedPool, beads)
}

func buildRoutedPoolQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	legacyTarget := legacyWorkflowControlQualifiedName(target)
	if legacyTarget == "" {
		return routedPoolWorkQueryCommand(includeEphemeralReady, target)
	}
	return routedPoolWorkQueryCommand(includeEphemeralReady, target, legacyTarget)
}

func legacyWorkflowControlQualifiedName(target string) string {
	target = strings.TrimSpace(target)
	if target == ControlDispatcherAgentName {
		return "workflow-control"
	}
	const suffix = "/" + ControlDispatcherAgentName
	if strings.HasSuffix(target, suffix) {
		return strings.TrimSuffix(target, suffix) + "/workflow-control"
	}
	return ""
}

// EffectiveSlingQuery returns the sling query command template for this agent.
// The template uses {} as a placeholder for the bead ID.
// If SlingQuery is set, returns it as-is. Otherwise returns the default:
// "bd update {} --set-metadata gc.routed_to=<template>"
//
// All agents use metadata-based routing. The reconciler and scale_check
// handle session creation; sling just stamps the target template.
func (a *Agent) EffectiveSlingQuery() string {
	if a.SlingQuery != "" {
		return a.SlingQuery
	}
	return a.DefaultSlingQuery()
}

// DefaultSlingQuery returns the built-in metadata-routing sling query for
// this agent. Callers outside config should prefer this helper over rebuilding
// the command string to preserve the bd boundary invariant.
func (a *Agent) DefaultSlingQuery() string {
	route := a.QualifiedName()
	if a.PoolName != "" {
		route = a.PoolName
	}
	return "bd update {} --set-metadata " + beadmeta.RoutedToMetadataKey + "=" + route
}

// EffectivePoolDemandQuery returns the count-form pool-demand query the
// reconciler runs to detect new unassigned routed work. It is the
// reconciler-side counterpart to EffectiveWorkQuery's Tier 3 (the worker
// claim path): both derive their predicates from the same helpers so
// any future change to the pool-demand shape flows to both paths
// simultaneously.
//
// If ScaleCheck is set (user override), it takes precedence and is
// returned as-is. Otherwise the default count-form is returned.
//
// Assigned in-progress work is resumed from session beads, so it must
// not create additional generic pool demand here.
//
// See engdocs/architecture/dispatch.md "scale_check ↔ work_query
// correspondence" and the protocol-mismatch class regression addressed
// by PR #1516.
func (a *Agent) EffectivePoolDemandQuery() string {
	return a.effectiveQuery(queryPoolDemand, false)
}

// EffectivePoolDemandQueryForBeads returns the count-form demand query using
// the bd compatibility semantics configured for the city.
func (a *Agent) EffectivePoolDemandQueryForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryPoolDemand, beads)
}

func buildPoolDemandQuery(a *Agent, includeEphemeralReady bool) string {
	target := a.poolDemandTarget()
	return poolDemandCountShell(target, includeEphemeralReady)
}

// EffectiveScaleCheck returns the scale check command for this agent.
// Pass-through to EffectivePoolDemandQuery for back-compat with code and
// configs that name the predicate "scale_check"; new call sites should
// prefer EffectivePoolDemandQuery to make the dependency on the
// work_query predicate explicit.
func (a *Agent) EffectiveScaleCheck() string {
	return a.EffectivePoolDemandQuery()
}

// RecoveryHookMarker prefixes every diagnostic the DEFAULT on_death/on_boot
// recovery hooks print to stdout when a bd release fails. It is the contract
// between the generated templates (which emit it) and the controller callers
// (which surface only marked output): a user-supplied on_death/on_boot override
// is passed through verbatim and carries no marker, so its stdout is not
// mislabeled or spammed into the recovery log.
const RecoveryHookMarker = "gc-recovery:"

// EffectiveOnDeath returns the on_death command for this agent.
// If OnDeath is set, returns it. Otherwise returns the default recovery hook
// that unclaims in-progress work assigned to this concrete agent identity.
func (a *Agent) EffectiveOnDeath() string {
	return a.effectiveQuery(queryOnDeath, false)
}

// EffectiveOnDeathForBeads returns the default on_death command using the bd
// compatibility semantics configured for the city.
func (a *Agent) EffectiveOnDeathForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryOnDeath, beads)
}

func buildOnDeath(a *Agent, includeEphemeralInProgress bool) string {
	route := a.QualifiedName()
	if a.PoolName != "" {
		route = a.PoolName
	}
	_ = includeEphemeralInProgress
	ephemeralRead := bdQueryEphemeralStatusQuietShell("in_progress") + ` | ` +
		`jq -r --arg assignee ` + shellquote.Quote(a.QualifiedName()) + ` '.[] | select((.assignee // "") == $assignee) | [.id, ` + jqMeta(beadmeta.RunTargetMetadataKey) + `, ` + jqMeta(beadmeta.RoutedToMetadataKey) + `] | @tsv' 2>/dev/null; `
	// Reset both assignee and status: clearing assignee alone leaves the bead
	// invisible to every work_query tier (Tier 1 needs assignee match, Tiers
	// 2/3 only match "ready" status). The next worker re-claims via Tier 3.
	// If routed metadata is missing entirely, backfill the canonical
	// gc.run_target route so reopened direct-assigned work does not stay
	// invisible.
	return `{ ` +
		`bd list --assignee=` + a.QualifiedName() +
		` --status=in_progress --json 2>/dev/null | ` +
		`jq -r '.[] | [.id, ` + jqMeta(beadmeta.RunTargetMetadataKey) + `, ` + jqMeta(beadmeta.RoutedToMetadataKey) + `] | @tsv' 2>/dev/null; ` +
		ephemeralRead +
		`} | ` +
		`while IFS="$(printf '\t')" read -r id run_target routed_to; do ` +
		`[ -z "$id" ] && continue; ` +
		`if [ -n "$run_target" ] || [ -n "$routed_to" ]; then ` +
		`if ! err=$(bd update "$id" --assignee "" --status open 2>&1 >/dev/null); then printf 'gc-recovery: on_death release failed for %s: %s\n' "$id" "$err"; fi; ` +
		`else if ! err=$(bd update "$id" --assignee "" --status open --set-metadata ` + shellquote.Quote(beadmeta.RunTargetMetadataKey+"="+route) + ` 2>&1 >/dev/null); then printf 'gc-recovery: on_death release failed for %s: %s\n' "$id" "$err"; fi; ` +
		`fi; ` +
		`done`
}

// EffectiveOnBoot returns the on_boot command for this agent.
// If OnBoot is set, returns it. Otherwise returns the default recovery hook
// that unclaims in-progress work routed to this backing config.
func (a *Agent) EffectiveOnBoot() string {
	return a.effectiveQuery(queryOnBoot, false)
}

// EffectiveOnBootForBeads returns the default on_boot command using the bd
// compatibility semantics configured for the city.
func (a *Agent) EffectiveOnBootForBeads(beads BeadsConfig) string {
	return a.effectiveQueryForBeads(queryOnBoot, beads)
}

func buildOnBoot(a *Agent, includeEphemeralInProgress bool) string {
	template := a.QualifiedName()
	if a.PoolName != "" {
		template = a.PoolName
	}
	_ = includeEphemeralInProgress
	ephemeralRead := bdQueryEphemeralStatusQuietShell("in_progress") + ` | ` +
		`jq -r --arg template "$template" '.[] | select((.assignee // "") == "") | select((` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == $template) or ((` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == "") and (` + jqMeta(beadmeta.RunTargetMetadataKey) + ` == $template) and (` + jqMeta(beadmeta.KindMetadataKey) + ` == "` + beadmeta.KindWorkflow + `"))) | .id' 2>/dev/null; `
	return `template=` + shellquote.Quote(template) + `; ` +
		`{ ` +
		`bd list --metadata-field "` + beadmeta.RoutedToMetadataKey + `=$template" --status=in_progress --no-assignee --json 2>/dev/null | ` +
		`jq -r '.[].id' 2>/dev/null; ` +
		`bd list --metadata-field "` + beadmeta.RunTargetMetadataKey + `=$template" --metadata-field "` + beadmeta.KindMetadataKey + `=` + beadmeta.KindWorkflow + `" --status=in_progress --no-assignee --json 2>/dev/null | ` +
		`jq -r '.[] | select(` + jqMeta(beadmeta.RoutedToMetadataKey) + ` == "") | .id' 2>/dev/null; ` +
		ephemeralRead +
		`} | awk 'NF && !seen[$0]++' | ` +
		`xargs -rI{} sh -c 'if ! err=$(bd update "$1" --status open 2>&1 >/dev/null); then printf "gc-recovery: on_boot reopen failed for %s: %s\n" "$1" "$err"; fi' _ {}`
}
