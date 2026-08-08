package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// convoyAutocloseLeakCheck fails when a convoy is collectable but was never
// collected -- open, unowned, and every child terminal.
//
// Why this check exists rather than a promise that the collector runs: convoy
// autoclose has exactly one automatic trigger, the controller's bead.closed
// handler (runBeadCloseAutoclose in api_state.go), and `gc convoy check` is
// that same collector driven by hand. Nothing calls either on a schedule, so
// the event path is a single point of failure with NO backstop -- one missed
// event leaks its convoy permanently, and repairing the event source would
// still leave every close missed before the repair leaked forever.
//
// That is not hypothetical. For a close written by another process, the only
// producer of bead.closed is the caching store's reconcile eviction
// (internal/beads/caching_store_reconcile.go). In the city measured on
// 2026-08-08 the city store's reconciler had not completed a cycle since
// 2026-08-07T17:53 local, and no city work bead had emitted bead.closed since
// 22:02Z that day, so the handler could not run for the one population that
// needs it: five convoys had accumulated. The same build kept collecting
// rig-store convoys correctly the whole time, which is exactly why the
// mechanism looked healthy from the outside.
//
// That failure is invisible from every surface anyone reads. A leaked convoy
// is Ready-visible and looks exactly like a live dispatch; telling them apart
// means opening the child. So the leak degrades the ready queue monotonically
// and silently, and no amount of care in the collector would have surfaced it.
// A gate that exits nonzero is the only form of this fix that stays true after
// the next refactor.
//
// SeverityBlocking is the load-bearing choice: `gc doctor` derives its exit
// code from BlockingFailed alone, so an advisory result would print the same
// line and still exit 0. The one case that must NOT block is an unreachable
// store -- then the leak set is unknown, and failing closed would wedge the
// gate and everything downstream of it whenever Dolt is briefly away.
//
// Invariant: the leak predicate is convoyIsComplete, shared with both
// collectors. Do not restate it here -- TestConvoyIsCompleteMatchesTheCollector
// pins the gate's verdict against what the real sweep closes.
type convoyAutocloseLeakCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
}

func newConvoyAutocloseLeakCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *convoyAutocloseLeakCheck {
	return &convoyAutocloseLeakCheck{cfg: cfg, cityPath: cityPath, newStore: newStore}
}

func (c *convoyAutocloseLeakCheck) Name() string { return "convoy-autoclose-leak" }

func (c *convoyAutocloseLeakCheck) CanFix() bool { return true }

// WarmupEligible keeps this out of `gc start`'s warm-up scan. The leak is
// chronic, not a startup hazard, and a blocking failure in warm-up would
// refuse to start a city over convoys that are merely uncollected.
func (c *convoyAutocloseLeakCheck) WarmupEligible() bool { return false }

// leakedConvoy is one collectable-but-open convoy, carrying the store that
// owns it so Fix can close it without re-deriving the scope.
type leakedConvoy struct {
	scope  string
	store  beads.Store
	bead   beads.Bead
	closed int
}

// collect returns the leaked convoys across the city store and every active
// rig store. skipped carries scopes whose store could not be read: those are
// reported, never silently treated as clean, because an unreadable store and a
// store with no leaks are indistinguishable from the result alone.
func (c *convoyAutocloseLeakCheck) collect() (leaks []leakedConvoy, skipped []string) {
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
		convoys, err := store.List(beads.ListQuery{Type: "convoy"})
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("%s skipped: listing convoys: %v", sc.label, err))
			continue
		}
		for _, b := range convoys {
			complete, err := convoyIsComplete(store, b)
			if err != nil {
				skipped = append(skipped, fmt.Sprintf("%s %s skipped: reading children: %v", sc.label, b.ID, err))
				continue
			}
			if !complete {
				continue
			}
			children, _ := listConvoyChildren(store, b.ID, true)
			leaks = append(leaks, leakedConvoy{scope: sc.label, store: store, bead: b, closed: len(children)})
		}
	}
	sort.SliceStable(leaks, func(i, j int) bool { return leaks[i].bead.ID < leaks[j].bead.ID })
	return leaks, skipped
}

func (c *convoyAutocloseLeakCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name()}
	leaks, skipped := c.collect()

	if len(skipped) > 0 && len(leaks) == 0 {
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = fmt.Sprintf("leaked convoys unknown: %d scope(s) unreadable", len(skipped))
		res.Details = skipped
		return res
	}

	if len(leaks) == 0 {
		res.Status = doctor.StatusOK
		res.Message = "no leaked convoys"
		return res
	}

	res.Status = doctor.StatusError
	res.Severity = doctor.SeverityBlocking
	res.Message = fmt.Sprintf("%d convoy(s) open with every child closed — autoclose did not collect them", len(leaks))
	res.FixHint = "gc doctor --fix (or gc convoy check) closes them"
	details := make([]string, 0, len(leaks)+len(skipped))
	for _, l := range leaks {
		details = append(details, fmt.Sprintf("%s: %s %q — %d/%d children closed, convoy still open",
			l.scope, l.bead.ID, strings.TrimSpace(l.bead.Title), l.closed, l.closed))
	}
	details = append(details, skipped...)
	res.Details = details
	return res
}

// Fix closes the leaked convoys with the collector's own reason string, so a
// convoy collected here is indistinguishable in the audit trail from one the
// controller collected. It reports the first failure rather than continuing
// silently: a convoy that will not close is a different defect from one that
// was never visited, and collapsing them hides the second.
func (c *convoyAutocloseLeakCheck) Fix(_ *doctor.CheckContext) error {
	leaks, _ := c.collect()
	for _, l := range leaks {
		if err := closeConvoyWithReason(l.store, l.bead.ID, convoyAutocloseReason); err != nil {
			return fmt.Errorf("closing leaked convoy %s: %w", l.bead.ID, err)
		}
	}
	return nil
}
