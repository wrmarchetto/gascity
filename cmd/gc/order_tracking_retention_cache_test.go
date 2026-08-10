package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// TestOrderTrackingRetentionWatchdog_DoesNotAbsorbClosedRunsIntoCache pins the
// invariant that the 15-minute retention read leaves the city cache's active
// map alone.
//
// The watchdog reads through beads.HandlesFor(store).Live, which for a bare
// *CachingStore is liveStoreReader and goes straight to the backing store. In
// production the store is not bare: orderTrackingSweepStoresFromTargets wraps
// every scope in orderTrackingSweepScopedStore, which embeds the beads.Store
// INTERFACE, so Handles() (not a Store method) does not promote, HandlesFor
// falls back to logicalLiveStoreReader, and the read re-enters
// CachingStore.List -> refreshCachedBeads, which absorbs every returned row
// including the closed ones. On the ci city that installed 21,505 closed
// order-tracking rows into the cache every 15 minutes, evicted again on the
// next reconcile (ci-an8f).
//
// The corpus here is deliberately INSIDE the 7d TTL, so the sweep deletes
// nothing: that is the production condition, and it is what makes the bug
// silent -- runOrderTrackingRetentionWatchdog logs only when deleted > 0.
//
// Cache size is read back through a second PrimeActive because
// CacheStats.TotalBeads is memoized and refreshCachedBeads does not refresh it;
// PrimeActive merges the active set into the existing map (it does not rebuild
// it, unlike Prime) and then recomputes the stats, so the absorbed rows are
// still counted.
func TestOrderTrackingRetentionWatchdog_DoesNotAbsorbClosedRunsIntoCache(t *testing.T) {
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	const closedRuns = 500

	seed := []beads.Bead{{
		ID:        "active-work",
		Title:     "active work",
		Status:    "open",
		Type:      "task",
		CreatedAt: now.Add(-time.Hour),
	}}
	for i := range closedRuns {
		seed = append(seed, beads.Bead{
			ID:        fmt.Sprintf("run-%03d", i),
			Title:     "order:guard",
			Status:    "closed",
			Type:      "task",
			CreatedAt: now.Add(-time.Hour + time.Duration(i)*time.Second),
			Labels:    []string{"order-run:guard", labelOrderTracking},
			Ephemeral: true,
		})
	}
	backing := beads.NewMemStoreFrom(1000, seed, nil)

	cache := beads.NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	before := cache.Stats().TotalBeads

	cr := &CityRuntime{
		cityName: "test-city",
		// cityPath empty skips the doctor.BulkDeleteSafe guard, matching every
		// other TestOrderTrackingRetentionWatchdog_* case; the guard is not
		// what this test is about.
		cfg:                 &config.City{Workspace: config.Workspace{Name: "test-city"}},
		standaloneCityStore: cache,
	}
	cr.runOrderTrackingRetentionWatchdog(now)

	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive after watchdog: %v", err)
	}
	if got := cache.Stats().TotalBeads; got != before {
		t.Fatalf("city cache grew from %d to %d rows across one retention watchdog pass; the closed order-tracking corpus was absorbed into the active map", before, got)
	}
}
