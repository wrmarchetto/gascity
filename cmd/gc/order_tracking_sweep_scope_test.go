package main

// Scope guards for the order-tracking sweep's store resolution: a sweep target
// carrying no scope root must be declined, never opened.
//
// This suite exists because the two order-tracking watchdogs resolve their
// stores from a path that can be EMPTY, and the real opener does not fail on
// an empty path -- openStoreResultAtForCityWithConfig substitutes
// cityForStoreDir("") (main.go:1403), which walks up from the PROCESS CWD for
// a city marker. Under `go test` that cwd is the package source directory, so
// an unguarded open either spawns a managed dolt server into the source tree
// or, when a real city encloses the checkout, opens and MUTATES that city's
// bead store. Measured 2026-09-08 in a worktree under a live city: the
// retention watchdog opened the live city store and listed 76364 closed
// order-tracking runs (bead gs-mns).
//
// Both watchdogs are covered on purpose. They share
// CityRuntime.orderTrackingSweepStores, and the arm that CLOSES beads
// (runOrderTrackingSweepWatchdog) is as destructive against a wrong store as
// the arm that DELETES them (runOrderTrackingRetentionWatchdog).
//
// What this suite deliberately does NOT assert: that
// orderTrackingSweepTargetsForConfig("", nil) returns no targets. Dropping the
// city target whenever cityPath is empty would also drop it for the sibling
// tests that legitimately pair an empty cityPath with standaloneCityStore
// (TestOrderTrackingRetentionWatchdog_PrunesEligibleBeads and the
// TestOrderTrackingSweepWatchdog* cases), which would then sweep nothing and
// pass for the wrong reason. The discriminator is not "is cityPath empty" but
// "this target has no store AND no location to open one from", so
// TestOrderTrackingSweepStoresUsesStandaloneStoreWithoutOpening pins the
// affordance that the refusal must not break.
//
//	go test ./cmd/gc/ -run 'OrderTrackingSweepStores|WatchdogWithNoCityPath'

import (
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// failIfSweepStoreOpened swaps newCityRuntimeOpenSweepStore for a stub that
// fails the test when called, and returns a pointer to the call count so a
// caller can assert on it directly. Recording the count as well as failing is
// not redundant: the retention watchdog swallows the opener's error into
// stderr, so a test that only checked for an error line would go green if the
// opener were called and then failed.
func failIfSweepStoreOpened(t *testing.T) *int {
	t.Helper()
	calls := 0
	prev := newCityRuntimeOpenSweepStore
	newCityRuntimeOpenSweepStore = func(scopeRoot, cityPath string) (beads.Store, error) {
		calls++
		t.Errorf("opened a sweep store with scopeRoot=%q cityPath=%q; an empty scope root must be declined, not resolved from the process cwd", scopeRoot, cityPath)
		return nil, fmt.Errorf("opener must not be called")
	}
	t.Cleanup(func() { newCityRuntimeOpenSweepStore = prev })
	return &calls
}

func TestRetentionWatchdogWithNoCityPathOpensNoStore(t *testing.T) {
	calls := failIfSweepStoreOpened(t)
	cr := &CityRuntime{
		cityName:  "test-city",
		cfg:       nil,
		stdout:    io.Discard,
		stderr:    io.Discard,
		logPrefix: "gc test",
	}

	cr.runOrderTrackingRetentionWatchdog(time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))

	if *calls != 0 {
		t.Fatalf("sweep store opens = %d, want 0", *calls)
	}
}

func TestSweepWatchdogWithNoCityPathOpensNoStore(t *testing.T) {
	calls := failIfSweepStoreOpened(t)
	cr := &CityRuntime{
		cityName:  "test-city",
		cfg:       nil,
		stdout:    io.Discard,
		stderr:    io.Discard,
		logPrefix: "gc test",
	}

	cr.runOrderTrackingSweepWatchdog(time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC))

	if *calls != 0 {
		t.Fatalf("sweep store opens = %d, want 0", *calls)
	}
}

func TestOrderTrackingSweepStoresDeclinesTargetWithNoScopeRoot(t *testing.T) {
	calls := failIfSweepStoreOpened(t)
	cr := &CityRuntime{cityName: "test-city", cfg: nil}

	stores, targets, closeOpened, err := cr.orderTrackingSweepStores()
	defer closeOpened()

	if err != nil {
		t.Fatalf("orderTrackingSweepStores() err = %v, want nil: a declined target is not a failure", err)
	}
	if *calls != 0 {
		t.Fatalf("sweep store opens = %d, want 0", *calls)
	}
	// The target is still enumerated -- only its OPEN is refused. Asserting on
	// the target list here is what separates this fix from one that drops the
	// city target outright, which the sibling standalone-store tests rely on.
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1 (the city target stays enumerated)", len(targets))
	}
	if len(stores) != 0 {
		t.Fatalf("stores = %d, want 0", len(stores))
	}
}

// TestOrderTrackingSweepStoresUsesStandaloneStoreWithoutOpening pins the
// affordance the refusal must not break: an empty cityPath paired with an
// in-memory standalone store still yields that store, and still opens nothing.
func TestOrderTrackingSweepStoresUsesStandaloneStoreWithoutOpening(t *testing.T) {
	calls := failIfSweepStoreOpened(t)
	store := beads.NewMemStore()
	cr := &CityRuntime{
		cityName:            "test-city",
		cfg:                 &config.City{Workspace: config.Workspace{Name: "test-city"}},
		standaloneCityStore: store,
	}

	stores, _, closeOpened, err := cr.orderTrackingSweepStores()
	defer closeOpened()

	if err != nil {
		t.Fatalf("orderTrackingSweepStores() err = %v, want nil", err)
	}
	if *calls != 0 {
		t.Fatalf("sweep store opens = %d, want 0; the standalone store already satisfies the city target", *calls)
	}
	if len(stores) != 1 {
		t.Fatalf("stores = %d, want 1", len(stores))
	}
}

// TestRetentionWatchdogWithNoCityPathStillPrunesStandaloneStore is the
// mutation witness for the refusal: it must stay green. A fix that skipped the
// city target whenever cityPath was empty -- rather than only refusing to OPEN
// it -- would leave this store unswept while every refusal test above still
// passed.
func TestRetentionWatchdogWithNoCityPathStillPrunesStandaloneStore(t *testing.T) {
	failIfSweepStoreOpened(t)
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	// 8 days old, past the 7d default TTL; the single oldest exceeds retain-10.
	seed := make([]beads.Bead, 0, minClosedOrderTrackingRetained+1)
	for i := range minClosedOrderTrackingRetained + 1 {
		seed = append(seed, beads.Bead{
			ID:        fmt.Sprintf("prune-%02d", i),
			Title:     "order:prune",
			Status:    "closed",
			Type:      "task",
			CreatedAt: now.Add(-8*24*time.Hour + time.Duration(i)*time.Minute),
			Labels:    []string{"order-run:prune", labelOrderTracking},
			Ephemeral: true,
		})
	}
	cr := &CityRuntime{
		cityName:            "test-city",
		cfg:                 &config.City{Workspace: config.Workspace{Name: "test-city"}},
		standaloneCityStore: beads.NewMemStoreFrom(100, seed, nil),
		stdout:              io.Discard,
		stderr:              io.Discard,
		logPrefix:           "gc test",
	}

	cr.runOrderTrackingRetentionWatchdog(now)

	store := cr.standaloneCityStore
	if _, err := store.Get("prune-00"); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("Get(prune-00) err = %v, want ErrNotFound: the standalone store must still be pruned", err)
	}
	if _, err := store.Get("prune-01"); err != nil {
		t.Fatalf("prune-01 should be preserved at the retain floor: %v", err)
	}
}

// TestOrderTrackingSweepStoresFromTargetsSkipsDeclinedTarget pins the opener
// contract the refusal depends on. A (nil, nil) return must produce no store
// rather than a wrapped one: orderTrackingSweepScopedStore embeds beads.Store
// (order_store.go:83), so a nil inner store is still a non-nil interface value
// and passes every `store == nil` guard in the sweep functions before
// nil-panicking inside one of them. This is a latent trap on the `gc order
// sweep-tracking` CLI path too, which shares this helper.
func TestOrderTrackingSweepStoresFromTargetsSkipsDeclinedTarget(t *testing.T) {
	targets := []orderTrackingSweepTarget{
		{target: execStoreTarget{ScopeRoot: "", ScopeKind: "city"}, label: "city"},
		{target: execStoreTarget{ScopeRoot: "/real/rig", ScopeKind: "rig", RigName: "frontend"}, label: `rig "frontend"`},
	}
	rigStore := beads.NewMemStore()

	stores, err := orderTrackingSweepStoresFromTargets(targets, func(sweepTarget orderTrackingSweepTarget) (beads.Store, error) {
		if sweepTarget.target.ScopeRoot == "" {
			return nil, nil
		}
		return rigStore, nil
	})
	if err != nil {
		t.Fatalf("orderTrackingSweepStoresFromTargets() err = %v, want nil", err)
	}
	if len(stores) != 1 {
		t.Fatalf("stores = %d, want 1 (the declined target must be skipped, not wrapped)", len(stores))
	}
	// Reaching through the wrapper proves the surviving entry is the real
	// store and not a nil-inner wrapper that merely counted as one.
	if _, err := stores[0].Get("nope"); !errors.Is(err, beads.ErrNotFound) {
		t.Fatalf("Get on the surviving store err = %v, want ErrNotFound; a nil-inner wrapper would not answer at all", err)
	}
}
