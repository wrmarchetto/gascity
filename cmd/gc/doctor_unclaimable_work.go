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
// The city store and every active rig store are scanned. A rig's work queue is
// just as durable as city work, so a city health result that omitted it would
// report a clean system while work had no pool door. Suspended rigs are skipped
// to match the other per-rig doctor checks: opening their stores can start
// orphaned data services.
//
// Verified by cmd/gc/doctor_unclaimable_work_test.go; its registration is
// pinned by TestBuildDoctorChecks_NameSetUnchanged.
package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/suspensionstate"
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

type unclaimableWorkStore struct {
	path   string
	label  string
	isCity bool
}

type scopedUnclaimableWorkBead struct {
	bead  beads.Bead
	store unclaimableWorkStore
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

// unclaimableWorkDeliberatelyDoorlessLabel marks the short authoring window
// where a bead must exist before its procedure artifact can be named and the
// bead can safely receive a route. It suppresses only this reachability check;
// it neither creates dispatch demand nor changes the bead's ready state.
const unclaimableWorkDeliberatelyDoorlessLabel = "gc:deliberately-doorless"

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
	stores := c.activeStores()
	var claimable []scopedUnclaimableWorkBead
	for _, source := range stores {
		store, err := c.newStore(source.path)
		if err != nil {
			res.Status = doctor.StatusWarning
			res.Message = fmt.Sprintf("unclaimable work unknown: opening %s bead store: %v", source.label, err)
			return res
		}
		open, err := store.ListOpen("open")
		if err != nil {
			res.Status = doctor.StatusWarning
			if source.isCity {
				res.Message = fmt.Sprintf("unclaimable work unknown: listing open beads: %v", err)
			} else {
				res.Message = fmt.Sprintf("unclaimable work unknown: listing open beads from %s: %v", source.label, err)
			}
			return res
		}
		ready, err := store.Ready()
		if err != nil {
			res.Status = doctor.StatusWarning
			if source.isCity {
				res.Message = fmt.Sprintf("unclaimable work unknown: listing ready beads: %v", err)
			} else {
				res.Message = fmt.Sprintf("unclaimable work unknown: listing ready beads from %s: %v", source.label, err)
			}
			return res
		}
		readyIDs := make(map[string]bool, len(ready))
		for _, b := range ready {
			readyIDs[b.ID] = true
		}
		for _, b := range classifyBacklog(open, readyIDs).real {
			claimable = append(claimable, scopedUnclaimableWorkBead{bead: b, store: source})
		}
	}
	scope := newUnclaimableWorkScope(c.cfg)

	var details, ids []string
	for _, candidate := range claimable {
		if reason := scope.strandedReason(candidate.bead); reason != "" {
			detail := fmt.Sprintf("%s %s (%s)", candidate.bead.ID, strings.TrimSpace(candidate.bead.Title), reason)
			id := candidate.bead.ID
			if !candidate.store.isCity {
				detail = fmt.Sprintf("%s: %s", candidate.store.label, detail)
				id = candidate.store.label + "/" + id
			}
			details = append(details, detail)
			ids = append(ids, id)
		}
	}
	sort.Strings(details)
	sort.Strings(ids)

	if len(details) == 0 {
		res.Status = doctor.StatusOK
		res.Message = fmt.Sprintf("every one of %d claimable bead(s) %s is addressed", len(claimable), unclaimableWorkStoreScope(stores))
		return res
	}
	res.Status = doctor.StatusError
	res.Message = fmt.Sprintf("%d of %d claimable bead(s) %s reach no pool door: %s",
		len(details), len(claimable), unclaimableWorkStoreScope(stores), summarizeUnclaimableIDs(ids))
	res.Details = details
	res.FixHint = unclaimableWorkFixHint
	return res
}

// activeStores returns the city store plus registered, non-suspended rig
// stores. It owns the store-read scope so every Run has one complete answer.
func (c *unclaimableWorkCheck) activeStores() []unclaimableWorkStore {
	stores := []unclaimableWorkStore{{path: c.cityPath, label: "city", isCity: true}}
	if c.cfg == nil {
		return stores
	}
	suspension, _ := loadSuspensionState(fsys.OSFS{}, c.cityPath)
	seenPaths := map[string]struct{}{filepath.Clean(c.cityPath): {}}
	for _, rig := range c.cfg.Rigs {
		path := strings.TrimSpace(rig.Path)
		if path == "" || suspensionstate.EffectiveRigSuspended(suspension, rig.Name, rig.EffectiveSuspendedOnStart()) {
			continue
		}
		cleanPath := filepath.Clean(path)
		if _, seen := seenPaths[cleanPath]; seen {
			continue
		}
		seenPaths[cleanPath] = struct{}{}
		stores = append(stores, unclaimableWorkStore{path: path, label: fmt.Sprintf(`rig %q`, rig.Name)})
	}
	return stores
}

func unclaimableWorkStoreScope(stores []unclaimableWorkStore) string {
	rigCount := len(stores) - 1
	if rigCount <= 0 {
		return "in the city store"
	}
	if rigCount == 1 {
		return "across the city store and 1 rig store"
	}
	return fmt.Sprintf("across the city store and %d rig stores", rigCount)
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
	// A producer can explicitly declare the temporary pre-routing window it
	// creates while authoring a procedure artifact. This is deliberately not
	// inferred from a description or another bead's state: the marker makes the
	// exception auditable and removing it returns the bead to this check.
	if beadLabelsContain(b.Labels, unclaimableWorkDeliberatelyDoorlessLabel) {
		return ""
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
