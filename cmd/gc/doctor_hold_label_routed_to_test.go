package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// TestHoldLabelRoutedToCheckFlagsOnlyAnAbsentRoute pins what this check
// asserts after ci-gu5rld: a held bead with NO gc.routed_to raises demand on
// nobody and goes nowhere when the hold clears, which is the finding. A held
// bead that HAS a route is clean whatever its hold says, because the two name
// different actors by design -- gc.routed_to is who WORKS it and the hold is
// who must MOVE it.
//
// It used to assert route == hold value and backfill the difference. That
// premise was settled false empirically: setting gc.routed_to to a hold value
// tripped city:route-pool-absent within ninety seconds, because "external" is
// not a pool and a route must name an agent the city can mint (ci-gu5rld).
//
// hold:external is now INCLUDED and its exclusion was the worst of the old
// behavior. It was skipped on the grounds that it names no agent, which is
// true of the hold and says nothing about the route -- so the one shape that
// is guaranteed to have no worker waiting for it was the one shape never
// reported.
func TestHoldLabelRoutedToCheckFlagsOnlyAnAbsentRoute(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	cfg := &config.City{Rigs: []config.Rig{{Name: "repo", Path: rigDir}}}

	cityStore := beads.NewMemStoreFrom(0, []beads.Bead{
		// The finding: held, and addressed to nobody.
		{ID: "H-1", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"}},
		// THE REGRESSION THIS CASE EXISTS TO PREVENT. Held by the mayor,
		// worked by a reviewer. The old check called this drift and --fix
		// overwrote "reviewer" with "mayor", destroying the only record of
		// who resumes the bead -- silently, because "mayor" is a real pool
		// and no other check could object.
		{
			ID: "H-2", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"},
			Metadata: map[string]string{"gc.routed_to": "reviewer"},
		},
		// Held and routed at the same actor. Clean, and indistinguishable
		// from a bead the mayor genuinely works -- which is why equality
		// cannot be the predicate in either direction.
		{
			ID: "H-3", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"},
			Metadata: map[string]string{"gc.routed_to": "mayor"},
		},
		// hold:external with no route: the most invisible shape there is, and
		// the one the old exclusion let through.
		{ID: "H-4", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:external"}},
		// hold:external WITH a route is fine. External names the gate, the
		// route names who resumes once it lifts.
		{
			ID: "H-5", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:external"},
			Metadata: map[string]string{"gc.routed_to": "bench-engineer"},
		},
		// No hold label: not this check's subject even with an empty route.
		// city:ready-assignee and the demand probe own an unrouted bead that
		// is not held.
		{ID: "T-1", Title: "work", Type: "task", Status: "open"},
		// Generic over the hold value, so no special-casing of the canonical
		// two can creep back in.
		{ID: "H-6", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:qa-lead"}},
		// A non-open status must still be scanned, or in_progress, blocked
		// and deferred hold-labeled beads escape silently (ga-fm2vgd.2). An
		// empty route on a whitespace-only value counts as absent.
		{
			ID: "H-7", Title: "held", Type: "task", Status: "in_progress", Labels: []string{"hold:mayor"},
			Metadata: map[string]string{"gc.routed_to": "   "},
		},
	}, nil)
	rigStore := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "RH-1", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"}},
	}, nil)
	stores := map[string]beads.Store{cityDir: cityStore, rigDir: rigStore}
	factory := func(path string) (beads.Store, error) {
		store, ok := stores[path]
		if !ok {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	}

	check := newHoldLabelRoutedToCheck(cfg, cityDir, factory)
	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Run status = %v, want warning: %#v", res.Status, res)
	}
	details := strings.Join(res.Details, "\n")
	for _, want := range []string{"H-1", "H-4", "H-6", "H-7", "RH-1"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}
	for _, notWant := range []string{"H-2", "H-3", "H-5", "T-1"} {
		if strings.Contains(details, notWant) {
			t.Errorf("details should not mention %q:\n%s", notWant, details)
		}
	}

	// THE REMEDY IS THE MOST DANGEROUS PART OF A CHECK, because it is the only
	// part that tells the reader what to type. The old one said to backfill
	// from the hold label and a mayor followed it into a store-wide wrong
	// write. It must now name the worker and must not offer the hold value.
	if strings.Contains(res.FixHint, "hold:") {
		t.Errorf("fix hint still points at the hold label: %q", res.FixHint)
	}
	if !strings.Contains(res.FixHint, "gc.routed_to") {
		t.Errorf("fix hint does not name the key to set: %q", res.FixHint)
	}
}

// TestHoldLabelRoutedToCheckAdvertisesNoFix pins the absence of remediation.
// The correct route is the pool that will work the bead once the hold lifts,
// which only the caller knows; a --fix can only guess, and the guess it used
// to make was the hold value. A check that repairs by guessing is worse than
// one that reports, because the guess lands under `gc doctor --fix` on every
// bead at once.
func TestHoldLabelRoutedToCheckAdvertisesNoFix(t *testing.T) {
	cityDir := t.TempDir()
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "H-1", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"}},
	}, nil)
	check := newHoldLabelRoutedToCheck(nil, cityDir, func(path string) (beads.Store, error) {
		if path != cityDir {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	})

	if check.CanFix() {
		t.Error("CanFix() = true, want false: the worker's route cannot be derived from a hold")
	}
	if err := check.Fix(&doctor.CheckContext{}); err != nil {
		t.Errorf("Fix() = %v, want nil no-op", err)
	}
	// The write, not just the advertisement. CanFix() returning false while
	// Fix still wrote would leave the damage reachable through any caller that
	// invokes Fix without consulting CanFix.
	held, err := store.Get("H-1")
	if err != nil {
		t.Fatalf("get H-1: %v", err)
	}
	if got := held.Metadata["gc.routed_to"]; got != "" {
		t.Errorf("Fix wrote gc.routed_to = %q; it must write nothing", got)
	}
}

// TestHoldLabelRoutedToCheckCleanStore confirms a store where every held bead
// carries a route reports OK -- including one whose route differs from its
// hold, which is the normal shape for an externally gated bead rather than an
// edge case.
func TestHoldLabelRoutedToCheckCleanStore(t *testing.T) {
	cityDir := t.TempDir()
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{
			ID: "H-9", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"},
			Metadata: map[string]string{"gc.routed_to": "toolsmith"},
		},
		{
			ID: "H-10", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:external"},
			Metadata: map[string]string{"gc.routed_to": "bench-engineer"},
		},
	}, nil)
	check := newHoldLabelRoutedToCheck(nil, cityDir, func(path string) (beads.Store, error) {
		if path != cityDir {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return store, nil
	})
	if res := check.Run(&doctor.CheckContext{}); res.Status != doctor.StatusOK {
		t.Fatalf("Run status = %v, want OK: %#v", res.Status, res)
	}
}

// TestHoldLabelRoutedToRunReportsOpenFailures keeps the scope-skip reporting
// that used to be asserted through Fix. An unreadable rig store must be named
// rather than read as a rig with nothing held -- a store outage and a clean
// store are the two readings this check must never confuse.
func TestHoldLabelRoutedToRunReportsOpenFailures(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	cfg := &config.City{Rigs: []config.Rig{{Name: "repo", Path: rigDir}}}
	cityStore := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "H-1", Title: "held", Type: "task", Status: "open", Labels: []string{"hold:mayor"}},
	}, nil)
	check := newHoldLabelRoutedToCheck(cfg, cityDir, func(path string) (beads.Store, error) {
		if path == rigDir {
			return nil, errors.New("permission denied")
		}
		return cityStore, nil
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Run status = %v, want warning: %#v", res.Status, res)
	}
	details := strings.Join(res.Details, "\n")
	if !strings.Contains(details, "rig repo skipped") || !strings.Contains(details, "permission denied") {
		t.Fatalf("details missing the rig open failure:\n%s", details)
	}
	// The reachable finding is still reported alongside the skip. A skipped
	// scope that suppressed the scopes that DID read would turn one
	// unreadable rig into a city-wide blind spot.
	if !strings.Contains(details, "H-1") {
		t.Fatalf("details dropped the readable store's finding:\n%s", details)
	}
}

// TestHoldLabelRoutedToRunReportsListFailures covers the same confusion one
// layer in: the store opens and then refuses the scan.
func TestHoldLabelRoutedToRunReportsListFailures(t *testing.T) {
	cityDir := t.TempDir()
	check := newHoldLabelRoutedToCheck(nil, cityDir, func(path string) (beads.Store, error) {
		if path != cityDir {
			return nil, fmt.Errorf("unexpected store path %q", path)
		}
		return holdLabelListErrorStore{Store: beads.NewMemStore()}, nil
	})

	res := check.Run(&doctor.CheckContext{})
	if res.Status != doctor.StatusWarning {
		t.Fatalf("Run status = %v, want warning: %#v", res.Status, res)
	}
	details := strings.Join(res.Details, "\n")
	if !strings.Contains(details, "city skipped") || !strings.Contains(details, "listing failed") {
		t.Fatalf("details missing the list failure:\n%s", details)
	}
}

type holdLabelListErrorStore struct {
	beads.Store
}

func (s holdLabelListErrorStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, errors.New("listing failed")
}
