package beads

import (
	"fmt"
	"testing"
)

// Scope: the read-through refresh on CachingStore.List's Live/ParentID branch,
// and what it is allowed to install into c.beads.
//
// c.beads is the ACTIVE bead universe. cacheFullScanQuery pins
// IncludeClosed:false and the reconcile diff treats any cached id absent from
// that scan as evictable, so a closed row installed here can never be
// refreshed -- it only waits to be evicted. The suite exists because a closed
// history read reached that branch in production and installed 21,505 closed
// order-tracking rows into the city cache every 15 minutes (ci-an8f), which no
// existing test could see: the caller's result slice is correct either way, and
// CacheStats.TotalBeads is not recomputed on this path.
//
// The eviction accounting and the merge decision table are pinned elsewhere
// (caching_store_reconcile_*_test.go); this file asserts only what enters the
// map on a read.
//
//	go test ./internal/beads/ -run 'Absorb|HandlesForWrapped'

// seedClosedHistory fills store with n closed beads carrying label, plus one
// open bead, and returns the open bead's id. The open bead is what a primed
// cache legitimately holds, so a test can tell "the cache kept what it should"
// apart from "the cache kept nothing at all".
func seedClosedHistory(t *testing.T, store *MemStore, n int, label string) string {
	t.Helper()
	open, err := store.Create(Bead{Title: "active work", Status: "open"})
	if err != nil {
		t.Fatalf("Create open bead: %v", err)
	}
	for i := range n {
		// Create then Close: MemStore.Create stamps Status "open" over whatever
		// the caller passed, so a seed that only sets the field produces an
		// all-open corpus and every assertion below passes vacuously.
		b, err := store.Create(Bead{
			Title:  fmt.Sprintf("closed run %d", i),
			Labels: []string{label},
		})
		if err != nil {
			t.Fatalf("Create closed bead %d: %v", i, err)
		}
		if err := store.Close(b.ID); err != nil {
			t.Fatalf("Close bead %d: %v", i, err)
		}
	}
	return open.ID
}

// cacheSize reads len(c.beads) under the lock. Stats().TotalBeads is NOT a
// substitute: updateStatsLocked does not run on the read-through refresh path,
// so the field reads stale exactly where this suite needs the truth.
func cacheSize(c *CachingStore) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.beads)
}

// storeWrapper embeds the beads.Store INTERFACE and adds an unrelated method,
// the shape every wrapper in cmd/gc uses (orderTrackingSweepScopedStore,
// beadPolicyStore). Handles() is not part of beads.Store, so a wrapper that
// does not forward it explicitly makes HandlesFor fall back to the logical
// readers. It is defined here rather than reusing an existing test double
// because the fallback is triggered by the METHOD SET, and a double that
// happens to forward Handles() would make the test vacuous.
type storeWrapper struct {
	Store
	label string
}

func (w storeWrapper) sweepLabel() string { return w.label }

// TestLiveListDoesNotAbsorbUnseenClosedRows pins the invariant that a Live list
// of closed history leaves the active map alone. The assertion is on the cache
// size rather than on the returned rows because the caller's slice was always
// correct -- refreshCachedBeads builds it from the backing items whether or not
// it absorbs -- which is why this shipped unnoticed.
func TestLiveListDoesNotAbsorbUnseenClosedRows(t *testing.T) {
	t.Parallel()

	backing := NewMemStore()
	openID := seedClosedHistory(t, backing, 500, "order-tracking")

	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	before := cacheSize(cache)

	rows, err := cache.List(ListQuery{
		Status:   "closed",
		Label:    "order-tracking",
		Live:     true,
		TierMode: TierBoth,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 500 {
		t.Fatalf("Live list returned %d closed rows, want 500", len(rows))
	}

	if got := cacheSize(cache); got != before {
		t.Fatalf("active cache grew from %d to %d rows on a closed-history Live list; c.beads is the active universe and a closed row installed there can only wait to be evicted", before, got)
	}
	if _, err := cache.Handles().Cached.Get(openID); err != nil {
		t.Fatalf("Cached.Get(open bead) after Live list: %v; the guard must skip closed rows, not empty the cache", err)
	}
}

// TestHandlesForWrappedCacheLiveListDoesNotAbsorb is the production shape:
// HandlesFor applied to a value that WRAPS a CachingStore by embedding the
// Store interface. The native handle bypasses the cache
// (liveStoreReader.List calls backing.List), the logical fallback does not
// (logicalLiveStoreReader.List re-enters CachingStore.List with Live set), and
// nothing at the call site makes the degradation visible. Both are asserted in
// one test so a fix that repairs only the wrapper cannot pass it while the
// mechanism stays live.
func TestHandlesForWrappedCacheLiveListDoesNotAbsorb(t *testing.T) {
	t.Parallel()

	backing := NewMemStore()
	seedClosedHistory(t, backing, 500, "order-tracking")

	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	query := ListQuery{Status: "closed", Label: "order-tracking", TierMode: TierBoth}

	before := cacheSize(cache)
	if _, err := cache.Handles().Live.List(query); err != nil {
		t.Fatalf("native Live.List: %v", err)
	}
	if got := cacheSize(cache); got != before {
		t.Fatalf("native Live handle grew the active cache from %d to %d rows", before, got)
	}

	wrapped := storeWrapper{Store: cache, label: "city"}
	// The wrapper's own method is what makes it a faithful stand-in: the
	// production wrappers exist to carry scope labels, and it is that added
	// method set -- Store plus something, minus Handles -- that HandlesFor
	// cannot see through.
	if got := wrapped.sweepLabel(); got != "city" {
		t.Fatalf("sweepLabel() = %q, want %q", got, "city")
	}
	if _, err := HandlesFor(wrapped).Live.List(query); err != nil {
		t.Fatalf("wrapped Live.List: %v", err)
	}
	if got := cacheSize(cache); got != before {
		t.Fatalf("Live handle on a Store-embedding wrapper grew the active cache from %d to %d rows; HandlesFor fell back to logicalLiveStoreReader because the wrapper hides Handles()", before, got)
	}
}

// TestLiveListAbsorbsClosedRowForResidentBead pins the first carve-out in
// absorbableOnRefreshLocked. Without it the guard reads as "closed rows never
// enter the cache", and a row the cache still holds as OPEN would keep serving
// that stale status until the next reconcile, which is a regression the
// eviction accounting cannot see. The two absorb tests above pass whether or
// not this carve-out survives, so it needs its own assertion.
func TestLiveListAbsorbsClosedRowForResidentBead(t *testing.T) {
	t.Parallel()

	backing := NewMemStore()
	resident, err := backing.Create(Bead{Title: "resident work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	// Close through the BACKING store: an external close, so the cache learns
	// of it only through the read below. Closing through the cache would absorb
	// the row on the write path and prove nothing about the read path.
	if err := backing.Close(resident.ID); err != nil {
		t.Fatalf("backing Close: %v", err)
	}

	if _, err := cache.List(ListQuery{IncludeClosed: true, AllowScan: true, Live: true}); err != nil {
		t.Fatalf("List: %v", err)
	}

	cache.mu.RLock()
	cached, ok := cache.beads[resident.ID]
	cache.mu.RUnlock()
	if !ok {
		t.Fatalf("resident bead %s was dropped from the cache; the guard must skip UNSEEN closed rows, not evict resident ones", resident.ID)
	}
	if cached.Status != "closed" {
		t.Fatalf("resident bead status = %q after a Live read that saw it closed, want %q; the cache is serving a stale open row", cached.Status, "closed")
	}
}

// TestLiveListAbsorbsClosedRowForDirtyBead pins the second carve-out. A dirty
// id is one whose local write outcome the cache does not know; the absorb is
// what clears the fence, and cachedListOnly / CachedList decline to serve while
// any fence is set. Skipping the absorb for a dirty closed row would therefore
// hold the whole cache in backing-store fallback until the reconcile fence GC
// ran — a far worse failure than the pollution the guard exists to stop.
//
// The fence is seeded directly rather than produced by a failed write: the
// branch under test keys on c.dirty alone, and driving a real write failure
// would pin the failure injection instead of the branch.
func TestLiveListAbsorbsClosedRowForDirtyBead(t *testing.T) {
	t.Parallel()

	backing := NewMemStore()
	orphan, err := backing.Create(Bead{Title: "dirty work"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := backing.Close(orphan.ID); err != nil {
		t.Fatalf("backing Close: %v", err)
	}

	cache := NewCachingStoreForTest(backing, nil)
	if err := cache.PrimeActive(); err != nil {
		t.Fatalf("PrimeActive: %v", err)
	}
	cache.mu.Lock()
	cache.dirty[orphan.ID] = struct{}{}
	cache.mu.Unlock()

	if _, err := cache.List(ListQuery{IncludeClosed: true, AllowScan: true, Live: true}); err != nil {
		t.Fatalf("List: %v", err)
	}

	cache.mu.RLock()
	_, stillDirty := cache.dirty[orphan.ID]
	cache.mu.RUnlock()
	if stillDirty {
		t.Fatalf("dirty fence on %s survived a Live read that resolved it; every cache-only read declines while a fence is set", orphan.ID)
	}
}
