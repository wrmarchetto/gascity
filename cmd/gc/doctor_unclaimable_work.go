// cmd/gc/doctor_unclaimable_work.go
//
// Reports claimable ready work that names no addressee at all, so no pool door
// it could arrive at exists.
//
// The shape this exists for: a bead that is ready, is real work, carries neither
// an assignee nor a route any active agent answers to, and that nothing will
// therefore ever spawn a session to claim. It is not blocked and not held, so
// every queue view counts it as pending work and no instrument contradicts them.
// ci-mqqe measured 7h23m of one such P1 with zero sessions running; ci-i19e
// measured why that was even noticed -- the city's stall page fires on
// `running_agents == 0`, a city-wide count, so one busy agent anywhere hides the
// whole class for as long as it runs.
//
// THE BOUNDARY WITH unclaimable-assignee (internal/doctor, ci-n785), which is
// what keeps two checks from reporting one bead. A bead is addressed either by
// assignee or by route, and each mechanism gets exactly one detector:
//
//	assignee present -> unclaimable-assignee's, whether or not it resolves.
//	                    It reconciles the name against every identity the city
//	                    can produce and honors [doctor] external_assignees, the
//	                    operator's declaration that a name is deliberately not
//	                    an agent. This check stays silent on those beads --
//	                    duplicating them here would ALSO report the declared
//	                    external names, which is the false positive that gets a
//	                    detector turned off.
//	assignee absent  -> this check's. An unaddressed bead has no assignee to
//	                    reconcile, so it is invisible to a check that reads only
//	                    assigned rows, and it is the shape a filer who forgets
//	                    to route produces.
//
// WHY A DOCTOR CHECK AND NOT A SHELL PROBE, considered and rejected. The city
// order that pages on a stalled queue could ask the same question in Python, and
// would have to re-derive which routes resolve -- gc's own answer moves (the
// ready projection started carrying `assignee` between 2026-08-09 and
// 2026-08-10, and the slot bound moves with config). A second copy of routing
// resolution drifts from the reconciler silently and in the safe-looking
// direction: it reports nothing. This check calls the reconciler's own resolver
// instead, and a city order can consume `gc doctor --json` with no copy at all.
//
// It reports and does not route. Which pool an unrouted bead belongs to is a
// judgment call that must stay out of Go (AGENTS.md, "keep judgment out of
// Go"), and ci-mqqe already rejected an order that auto-slung the top ready
// unassigned bead for hardcoding exactly that. CanFix is false for the same
// reason.
//
// City store only, mirroring backlogDepthCheck and unclaimable-assignee. Work
// stranded in a rig store is NOT examined: the resolution here is
// city-config-scoped, and a rig store's ready set needs the reconciler's
// per-store target fan-out, which needs a live controller context this check
// does not have.
//
// Verified by cmd/gc/doctor_unclaimable_work_test.go; its registration is
// pinned by TestBuildDoctorChecks_NameSetUnchanged.
package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// unclaimableWorkCheck reports claimable ready work that carries no assignee and
// no route an active agent answers to. Such a bead is not blocked and not held --
// it is simply invisible to the reconciler, so no session is ever spawned to
// claim it.
type unclaimableWorkCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
}

func newUnclaimableWorkCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *unclaimableWorkCheck {
	return &unclaimableWorkCheck{cfg: cfg, cityPath: cityPath, newStore: newStore}
}

func (c *unclaimableWorkCheck) Name() string { return "unclaimable-work" }

func (c *unclaimableWorkCheck) CanFix() bool { return false }

func (c *unclaimableWorkCheck) Fix(_ *doctor.CheckContext) error { return nil }

// WarmupEligible returns false, and the absence is deliberate rather than
// copied from the sibling checks. The `gc start` warm-up scan is the one path
// that mails a doctor result to the mayor unprompted, which is exactly the
// addressee a routing gap wants -- but no store-reading check opts into warm-up
// today, and the scan mails every result at StatusWarning or worse. This check
// answers "unknown" when the city store is unreadable, so opting in before
// store readiness during warm-up is established would mail the mayor a warning
// at every start of a city whose store comes up after the scan.
func (c *unclaimableWorkCheck) WarmupEligible() bool { return false }

const unclaimableWorkFixHint = "route it or address it: " +
	"'bd update <id> --assignee <pool>' hands it to a pool any slot can claim, " +
	"'gc sling <id> --to <agent>' stamps gc.routed_to."

// unclaimableWorkSummaryIDs bounds how many bead IDs the one-line summary names.
// The summary has to stand alone: Details is shown only under --verbose, and the
// warm-up mailer carries Message and FixHint but drops Details entirely, so a
// count with no IDs reaches the reader as something they cannot act on. The
// overflow is always stated rather than silently truncated.
const unclaimableWorkSummaryIDs = 5

func (c *unclaimableWorkCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name(), Severity: doctor.SeverityAdvisory}
	if c.newStore == nil || strings.TrimSpace(c.cityPath) == "" {
		res.Status = doctor.StatusWarning
		res.Message = "unclaimable work unknown: no city bead store configured"
		return res
	}
	store, err := c.newStore(c.cityPath)
	if err != nil {
		res.Status = doctor.StatusWarning
		res.Message = fmt.Sprintf("unclaimable work unknown: opening city bead store: %v", err)
		return res
	}
	open, err := store.ListOpen("open")
	if err != nil {
		res.Status = doctor.StatusWarning
		res.Message = fmt.Sprintf("unclaimable work unknown: listing open beads: %v", err)
		return res
	}
	ready, err := store.Ready()
	if err != nil {
		res.Status = doctor.StatusWarning
		res.Message = fmt.Sprintf("unclaimable work unknown: listing ready beads: %v", err)
		return res
	}
	readyIDs := make(map[string]bool, len(ready))
	for _, r := range ready {
		readyIDs[r.ID] = true
	}
	claimable := classifyBacklog(open, readyIDs).real
	scope := newUnclaimableWorkScope(c.cfg)

	var details, ids []string
	for _, b := range claimable {
		if reason := scope.strandedReason(b); reason != "" {
			details = append(details, fmt.Sprintf("%s %s (%s)", b.ID, strings.TrimSpace(b.Title), reason))
			ids = append(ids, b.ID)
		}
	}
	sort.Strings(details)
	sort.Strings(ids)

	if len(details) == 0 {
		res.Status = doctor.StatusOK
		res.Message = fmt.Sprintf("every one of %d claimable bead(s) in the city store is addressed", len(claimable))
		return res
	}
	res.Status = doctor.StatusError
	res.Message = fmt.Sprintf("%d of %d claimable bead(s) reach no pool door: %s",
		len(details), len(claimable), summarizeUnclaimableIDs(ids))
	res.Details = details
	res.FixHint = unclaimableWorkFixHint
	return res
}

// summarizeUnclaimableIDs renders ids for the one-line summary, naming the
// overflow count rather than trailing off.
func summarizeUnclaimableIDs(ids []string) string {
	if len(ids) <= unclaimableWorkSummaryIDs {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s (+%d more)",
		strings.Join(ids[:unclaimableWorkSummaryIDs], ", "), len(ids)-unclaimableWorkSummaryIDs)
}

// unclaimableWorkScope is the set of route targets a pool door exists for.
// Built once per Run so the per-bead predicate stays a set lookup.
type unclaimableWorkScope struct {
	cfg     *config.City
	targets map[string]struct{}
}

// newUnclaimableWorkScope collects the route target of every agent that is not
// config-suspended.
//
// Both target spellings are admitted for a pool with an explicit pool_name --
// the pool name (agentutil.RoutedToIdentity's answer) and the agent's qualified
// name. Admitting both can only suppress a report, never create one, which is
// the right direction to be wrong in for a detector an operator has to trust.
//
// Suspension is read from the agent's own `suspended` only, the same field
// isConfiguredNamedSessionIdentity reads. CITY AND RIG suspension are NOT
// consulted: a city-wide suspend stops every pool, so honoring it here would
// report the entire ready queue at once, which is noise rather than news.
func newUnclaimableWorkScope(cfg *config.City) unclaimableWorkScope {
	scope := unclaimableWorkScope{cfg: cfg, targets: make(map[string]struct{})}
	if cfg == nil {
		return scope
	}
	for i := range cfg.Agents {
		if cfg.Agents[i].Suspended {
			continue
		}
		for _, identity := range []string{
			agentutil.RoutedToIdentity(&cfg.Agents[i]),
			cfg.Agents[i].QualifiedName(),
		} {
			if identity = strings.TrimSpace(identity); identity != "" {
				scope.targets[identity] = struct{}{}
			}
		}
	}
	return scope
}

// strandedReason returns why no pool door exists for b, or "" when one does or
// when the bead is not this check's to report.
func (s unclaimableWorkScope) strandedReason(b beads.Bead) string {
	// An assignee makes the bead unclaimable-assignee's, resolvable or not --
	// see the boundary in this file's header. Deferring on the whole class
	// rather than on unresolvable names is what keeps the two checks from both
	// reporting one bead, and is why nothing here reads [doctor]
	// external_assignees: this check never looks at an assignee at all.
	if strings.TrimSpace(b.Assignee) != "" {
		return ""
	}
	// A hold label is the sanctioned way to park an unaddressed bead on an
	// actor (engdocs/contributors/hold-label-conventions.md), and the routed
	// demand tiers already skip these. Without the exclusion every deliberately
	// parked bead would report, which is how a detector gets turned off.
	for _, label := range beadmeta.DispatchHoldLabels {
		if beadLabelsContain(b.Labels, label) {
			return ""
		}
	}

	route := strings.TrimSpace(routedToOrLegacyWorkflowTarget(b))
	if route == "" {
		return "no assignee and no gc.routed_to"
	}
	if controllerDemandRouteTarget(s.cfg, b, s.targets) != "" {
		return ""
	}
	return fmt.Sprintf("routed to %q, which names no active agent", route)
}
