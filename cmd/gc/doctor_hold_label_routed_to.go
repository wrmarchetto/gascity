package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// holdLabelRoutedToCheck detects beads carrying a hold:<value> label whose
// gc.routed_to metadata is ABSENT. gc.routed_to is the sole persisted routing
// key (ga-eld2x) and it means WHO WORKS THIS, so a held bead without one
// raises demand on nobody while the hold stands and goes nowhere when the
// hold lifts.
//
// IT DOES NOT COMPARE THE ROUTE TO THE HOLD VALUE, and that comparison is
// what it used to be. The two name different actors by design -- the route
// names who resumes the bead, the hold names who must move it first -- and
// they differ for every externally gated bead, which is the normal case
// rather than an edge one (docs/hold-park.md; `gc city hold-park` preserves
// the route and says so).
//
// The old premise was settled false empirically, not by argument: a mayor
// followed this check's own --fix hint and set gc.routed_to to the hold value
// on six beads, and city:route-pool-absent fired within ninety seconds,
// because "external" is not a pool and a route must name an agent the city
// can mint (ci-gu5rld). The worse half was silent -- for hold:mayor the
// backfill wrote a REAL pool name over the worker's route, so no other check
// could object and the only record of who resumes the bead was gone.
//
// hold:external IS INCLUDED, and its exclusion was the sharpest defect here.
// It was skipped on the grounds that external names no agent, which is true
// of the hold and says nothing about the route -- so the one shape guaranteed
// to have no worker waiting for it was the one shape never reported. There is
// consequently no holdLabelExternalValue constant any more; reintroducing one
// restores that blind spot.
type holdLabelRoutedToCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
}

func newHoldLabelRoutedToCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *holdLabelRoutedToCheck {
	return &holdLabelRoutedToCheck{cfg: cfg, cityPath: cityPath, newStore: newStore}
}

func (c *holdLabelRoutedToCheck) Name() string { return "hold-label-routed-to" }

// CanFix returns false. The correct route is the pool that will work the bead
// once its hold lifts, which only the caller knows; the one value a check can
// derive is the hold's, and deriving it is the defect described above. A
// repair that guesses is worse than a report, because `gc doctor --fix`
// applies the guess to every bead at once.
func (c *holdLabelRoutedToCheck) CanFix() bool { return false }

func (c *holdLabelRoutedToCheck) WarmupEligible() bool { return false }

// holdLabelValue returns the hold value carried by labels, if any
// hold:<value> label is present. Every value counts, "external" included --
// see the type comment for why excluding it hid the worst case.
func holdLabelValue(labels []string) (string, bool) {
	for _, l := range labels {
		val, ok := strings.CutPrefix(l, "hold:")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		return val, true
	}
	return "", false
}

// holdRouteTarget is a single held bead carrying no gc.routed_to. `hold` is
// the label's value and is reported only so the reader knows who to ask; it
// is deliberately NOT a proposed route.
type holdRouteTarget struct {
	label  string
	store  beads.Store
	beadID string
	hold   string
}

func (c *holdLabelRoutedToCheck) collect() (targets []holdRouteTarget, skipped []string) {
	scopes := []struct{ label, path string }{{"city", c.cityPath}}
	if c.cfg != nil {
		for _, rig := range c.cfg.Rigs {
			if rig.Suspended || strings.TrimSpace(rig.Path) == "" {
				continue
			}
			scopes = append(scopes, struct{ label, path string }{"rig " + rig.Name, rig.Path})
		}
	}
	for _, sc := range scopes {
		if c.newStore == nil || strings.TrimSpace(sc.path) == "" {
			continue
		}
		store, err := c.newStore(sc.path)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s skipped: opening bead store: %v", sc.label, err))
			continue
		}
		// hold:<value> carries a dynamic value suffix, so no targeted
		// label/metadata query is possible; AllowScan is required for a
		// broad filter (internal/beads/query.go). Status is left unset so the
		// scan matches every non-closed bead (open, in_progress, blocked,
		// deferred, ...), not just "open" — an exact Status match would
		// silently hide hold:<value> drift on any other status (ga-fm2vgd.2).
		items, err := store.List(beads.ListQuery{AllowScan: true})
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s skipped: listing beads: %v", sc.label, err))
			continue
		}
		for _, b := range items {
			hold, ok := holdLabelValue(b.Labels)
			if !ok {
				continue
			}
			// Whitespace counts as absent: a route of spaces names no pool
			// and would satisfy a bare != "" test while raising no demand.
			if strings.TrimSpace(b.Metadata[beadmeta.RoutedToMetadataKey]) != "" {
				continue
			}
			targets = append(targets, holdRouteTarget{label: sc.label, store: store, beadID: b.ID, hold: hold})
		}
	}
	return targets, skipped
}

func (c *holdLabelRoutedToCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	targets, skipped := c.collect()
	if len(targets) == 0 && len(skipped) == 0 {
		return okCheck(c.Name(), "every held bead names the agent that will work it")
	}
	details := make([]string, 0, len(targets)+len(skipped))
	for _, tgt := range targets {
		details = append(details, fmt.Sprintf("%s bead %s is held (%s) and has no gc.routed_to, so it raises demand on nobody", tgt.label, tgt.beadID, tgt.hold))
	}
	details = append(details, skipped...)
	sort.Strings(details)
	if len(targets) == 0 {
		return warnCheck(c.Name(),
			fmt.Sprintf("hold-label-routed-to check skipped %d scope(s)", len(skipped)),
			"fix bead store access, then rerun gc doctor",
			details)
	}
	return warnCheck(c.Name(),
		fmt.Sprintf("%d held bead(s) name no agent to work them", len(targets)),
		// The remedy names the WORKER and offers no derivation, because the
		// previous hint offered one and a mayor followed it into a store-wide
		// wrong write. The hold value is excluded from this string on
		// purpose; a test asserts its absence.
		"set gc.routed_to to the agent or pool that will work each bead once "+
			"its hold lifts: gc bd update <bead> --set-metadata "+
			"gc.routed_to=<worker>. The actor named by the hold is who must "+
			"move it first, NOT the route",
		details)
}

// Fix is a no-op. See CanFix. It stays present so the check satisfies the same
// interface as its siblings -- checks_dolt_backup.go and
// checks_bd_backup_state.go do the same for the same reason -- and it must
// keep writing nothing even if a caller invokes it without consulting
// CanFix, which is what the test asserts.
func (c *holdLabelRoutedToCheck) Fix(_ *doctor.CheckContext) error { return nil }
