// cmd/gc/pool_alias_demand.go
//
// The in-process controller demand reader's pool-alias tier: work hand-assigned
// to a pool's bare name (`bd update <id> --assignee toolsmith`) rather than
// routed with gc.routed_to.
//
// Why this exists as a second admission rule beside the routed one rather than
// as a normalization pass over the beads: ci-c000 already taught the generated
// shell predicates this shape (bdReadyPoolAliasDemandShell, unioned into
// poolDemandCountShell for the reconciler and poolDemandFirstRowFunctionScript
// for the worker), and built the claim side to match -- hookCandidatePoolAlias
// plus BdStore.ReassignIfAssignee, which takes the bead by compare-and-swap so
// two slots cannot both win it. The reconciler only shells out for a pool with a
// custom scale_check; with the default probe it counts in Go, and that Go reader
// skipped every bead carrying an assignee. So the two halves disagreed, which
// dispatch.md invariant 11 forbids: the shell form counted a bead the in-process
// form could not see, and a pool whose work arrived that way stayed cold (ci-mqqe
// measured 7h23m of a ready P1 with zero sessions).
//
// The rejected alternative was to rewrite the bead -- clear the assignee, stamp
// gc.routed_to -- so the existing routed tier picks it up. It wakes the pool, but
// it bypasses that compare-and-swap claim transfer, discards the operator's
// recorded addressee, and leaves the Go/shell disagreement in place rather than
// closing it: any bead the rewrite skipped would still be counted by one half and
// not the other. Reading demand from the assignee closes the gap at its source.
//
// Editing constraint: the exclusions below mirror bdReadyPoolAliasDemandShell
// and each one creates SPAWN demand if dropped, so a miss is a pool that wakes,
// finds nothing it may claim, drains, and is woken again next tick. They are
// pinned by cmd/gc/build_desired_state_pool_alias_demand_test.go.
package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// controllerDemandPoolAliasTarget returns the pool-demand template a bead is
// parked on by assignee alone, or "" when the bead is not pool-alias demand.
//
// The assignee is compared to the template set EXACTLY, with no pool-route
// normalization, matching the shell form's `--assignee="$target"`. Normalizing
// here would fold a slot identity ("toolsmith-1") onto its template and raise
// pool-door demand for what is one dead session's assignment -- that belongs to
// the slot-orphan wake path (ci-rdbw), which can tell the difference.
func controllerDemandPoolAliasTarget(cfg *config.City, b beads.Bead, templates map[string]struct{}) string {
	assignee := strings.TrimSpace(b.Assignee)
	if assignee == "" {
		return ""
	}
	// A route to a DIFFERENT target means routing was expressed as a concrete
	// handoff (#2527), so its assignee must not also create demand for the old
	// pool. A root-only order wisp intentionally preserves both the pool
	// assignee and the canonical route to that SAME pool; the worker's pool-alias
	// claim tier serves that shape, so the controller must count it as well.
	// Other assigned+routed work remains a concrete handoff.
	routed := routedToOrLegacyWorkflowTarget(b)
	if routed != "" && (routed != assignee || b.Metadata[beadmeta.KindMetadataKey] != beadmeta.KindWisp) {
		return ""
	}
	if _, ok := templates[assignee]; !ok {
		return ""
	}
	if !poolAliasDemandEligible(cfg, b, assignee) {
		return ""
	}
	return assignee
}

// poolAliasDemandEligible reports whether a bead already matched to a template
// may raise spawn demand for it.
//
// beads.IsReadyExcludedType already hides mail and the bookkeeping types from
// controllerDemandReady, so the message check is redundant TODAY. It is written
// anyway because this tier is the one place where a type reaching it is
// indistinguishable from pool-assigned work -- mail carries its recipient in
// `assignee` -- and the cost of the redundancy is one comparison against
// re-introducing #4419 at boot cadence if that upstream filter ever narrows.
func poolAliasDemandEligible(cfg *config.City, b beads.Bead, assignee string) bool {
	// controlReadyExcludeType IS "epic", so this one line covers both the
	// bookkeeping types and the shell form's separate --exclude-type=epic (a
	// parent epic has no executable spec, so a session claiming one does
	// undefined work -- gc-udx). Written as one condition rather than a
	// second explicit epic branch because the duplicate was measurably dead:
	// removing it left every exclusion test green.
	if beads.IsReadyExcludedType(b.Type) || beads.IsMoleculeType(b.Type) || b.Type == controlReadyExcludeType {
		return false
	}
	if hasDispatchHoldLabel(b.Labels) {
		return false
	}
	// A configured named session's identity is its OWN wake signal: namedWorkReady
	// matches Assignee=<identity> and spawns the holder directly. Counting the same
	// bead as pool demand would wake the backing pool for work the named session is
	// already being woken to do. The shell form cannot reach this case -- it only
	// ever runs for a target the reconciler picked as a pool -- so the guard has to
	// live on this side.
	return !isConfiguredNamedSessionIdentity(cfg, assignee)
}

// hasDispatchHoldLabel reports whether labels park a bead outside of pool
// demand. It is shared by both default demand routes so the controller never
// counts work its corresponding worker query deliberately excludes.
func hasDispatchHoldLabel(labels []string) bool {
	for _, label := range beadmeta.DispatchHoldLabels {
		if beadLabelsContain(labels, label) {
			return true
		}
	}
	return false
}
