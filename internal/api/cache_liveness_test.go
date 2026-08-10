package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
)

// fakeLivenessStore satisfies beads.Store by embedding a MemStore. Tests
// swap in *CachingStore separately to exercise the Live/NotLive gate.
type fakeLivenessStore struct{ beads.Store }

func TestCacheLiveOr503_NonCachingStorePasses(t *testing.T) {
	// When the handler store is not a *CachingStore (e.g., a plain
	// MemStore in tests, or a BdStore without caching wrapping), there's
	// no liveness concept to gate on — the gate is a no-op.
	mem := beads.NewMemStore()
	if err := cacheLiveOr503(fakeLivenessStore{Store: mem}); err != nil {
		t.Fatalf("cacheLiveOr503(non-caching) = %v, want nil", err)
	}
}

func TestCacheLiveOr503_NilStorePasses(t *testing.T) {
	// A nil store is treated as "no cache to gate" — the handler's own
	// nil-store guard (if any) is responsible for 503-on-no-store.
	if err := cacheLiveOr503(nil); err != nil {
		t.Fatalf("cacheLiveOr503(nil) = %v, want nil", err)
	}
}

func TestCacheLiveOr503_LiveCachePasses(t *testing.T) {
	mem := beads.NewMemStore()
	cache := beads.NewCachingStoreForTest(mem, nil)
	if err := cache.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if !cache.IsLive() {
		t.Fatalf("expected IsLive true after Prime")
	}
	if err := cacheLiveOr503(cache); err != nil {
		t.Errorf("cacheLiveOr503(live) = %v, want nil", err)
	}
}

func TestCacheLiveOr503_NotLiveReturns503(t *testing.T) {
	mem := beads.NewMemStore()
	cache := beads.NewCachingStoreForTest(mem, nil)
	// Don't call Prime; cache stays uninitialized → not live.
	if cache.IsLive() {
		t.Fatalf("expected IsLive false before Prime")
	}
	err := cacheLiveOr503(cache)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	var he huma.StatusError
	if !errors.As(err, &he) {
		t.Fatalf("expected huma.StatusError, got %T: %v", err, err)
	}
	if he.GetStatus() != 503 {
		t.Errorf("status = %d, want 503", he.GetStatus())
	}
	if !strings.Contains(err.Error(), "cache_not_live") {
		t.Errorf("err = %q, want substring 'cache_not_live'", err.Error())
	}
}

func TestCacheAgeSeconds(t *testing.T) {
	// Deterministic against a real CachingStore: freeze the clock a fixed
	// interval past the primed LastFreshAt and assert the exact age. (Before
	// clock injection this test could only assert monotonicity.)
	mem := beads.NewMemStore()
	cache := beads.NewCachingStoreForTest(mem, nil)
	if err := cache.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	lastFresh := cache.Stats().LastFreshAt
	if lastFresh.IsZero() {
		t.Fatal("expected non-zero LastFreshAt after Prime")
	}
	restore := SetLivenessClockForTest(&clock.Fake{Time: lastFresh.Add(12 * time.Second)})
	defer restore()
	if got := cacheAgeSeconds(cache); got != 12 {
		t.Errorf("cacheAgeSeconds = %v, want exactly 12", got)
	}
}

// stubLivenessReporter is a fully controllable beads.LivenessReporter for the
// cache-age conformance lane. It embeds a nil beads.Store so it satisfies the
// Store type cacheAgeSeconds/cacheLiveOr503 accept; only the two liveness
// methods those helpers actually call are implemented.
type stubLivenessReporter struct {
	beads.Store
	live      bool
	lastFresh time.Time
}

func (s stubLivenessReporter) IsLive() bool { return s.live }
func (s stubLivenessReporter) Stats() beads.CacheStats {
	return beads.CacheStats{LastFreshAt: s.lastFresh}
}

func TestCacheAgeSeconds_ClockInjectedStates(t *testing.T) {
	base := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	restore := SetLivenessClockForTest(&clock.Fake{Time: base})
	defer restore()

	for _, tc := range []struct {
		name  string
		store beads.Store
		want  float64
	}{
		{"live-2s", stubLivenessReporter{live: true, lastFresh: base.Add(-2 * time.Second)}, 2},
		{"lagging-35s-past-banner", stubLivenessReporter{live: true, lastFresh: base.Add(-35 * time.Second)}, 35},
		{"priming-never-fresh", stubLivenessReporter{live: false, lastFresh: time.Time{}}, 0},
		{"clock-skew-negative-clamped", stubLivenessReporter{live: true, lastFresh: base.Add(5 * time.Second)}, 0},
		{"non-caching", beads.NewMemStore(), 0},
		{"nil-store", nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cacheAgeSeconds(tc.store); got != tc.want {
				t.Errorf("cacheAgeSeconds(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestCacheLiveOr503_StubStates(t *testing.T) {
	if err := cacheLiveOr503(stubLivenessReporter{live: true}); err != nil {
		t.Errorf("live stub = %v, want nil", err)
	}
	err := cacheLiveOr503(stubLivenessReporter{live: false})
	if err == nil || !strings.Contains(err.Error(), "cache_not_live") {
		t.Errorf("not-live stub = %v, want cache_not_live 503", err)
	}
}

// policyWrapperStub reproduces the shape the controller actually hands these
// handlers: an embedded beads.Store interface, which drops the inner store's
// optional methods, plus the LivenessHandle delegation that puts them back.
// internal/api cannot import cmd/gc, so the shape is restated here rather than
// exercised directly; that the real wrapper carries the delegation is pinned by
// TestControllerStoreCompositionPreservesCacheLiveness in cmd/gc. That split is
// the structural reason this defect survived — neither package's suite could
// see the other half.
type policyWrapperStub struct{ beads.Store }

func (w policyWrapperStub) LivenessHandle() (beads.LivenessReporter, bool) {
	return beads.LivenessFor(w.Store)
}

func TestCacheLiveOr503_ResolvesThroughWrapperHandle(t *testing.T) {
	// Every controller store reaches these handlers wrapped. Asserting the
	// liveness capability on the wrapper itself fails, and it fails silently:
	// the gate reads that as "no cache to gate on" and passes a priming store's
	// partial reads through as though they were complete.
	if err := cacheLiveOr503(policyWrapperStub{Store: stubLivenessReporter{live: true}}); err != nil {
		t.Errorf("wrapped live store = %v, want nil", err)
	}
	err := cacheLiveOr503(policyWrapperStub{Store: stubLivenessReporter{live: false}})
	if err == nil || !strings.Contains(err.Error(), "cache_not_live") {
		t.Errorf("wrapped not-live store = %v, want cache_not_live 503", err)
	}
	// A wrapper over an uncached store must still pass: resolving through the
	// handle must not manufacture a gate where no cache exists.
	if err := cacheLiveOr503(policyWrapperStub{Store: beads.NewMemStore()}); err != nil {
		t.Errorf("wrapped uncached store = %v, want nil", err)
	}
}

func TestCacheAgeSeconds_ResolvesThroughWrapperHandle(t *testing.T) {
	// 35s is past the CLI's 30s stale-read banner on purpose: the reported
	// symptom was a header reading 0 while the store had not reconciled in 29
	// hours, so the assertion has to distinguish a real age from the unresolved
	// path's constant zero, not merely be non-zero.
	base := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	restore := SetLivenessClockForTest(&clock.Fake{Time: base})
	defer restore()

	wrapped := policyWrapperStub{Store: stubLivenessReporter{
		live:      true,
		lastFresh: base.Add(-35 * time.Second),
	}}
	if got := cacheAgeSeconds(wrapped); got != 35 {
		t.Errorf("cacheAgeSeconds(wrapped) = %v, want exactly 35", got)
	}
	if got := cacheAgeSeconds(policyWrapperStub{Store: beads.NewMemStore()}); got != 0 {
		t.Errorf("cacheAgeSeconds(wrapped uncached) = %v, want 0", got)
	}
}

func TestSetLivenessClockForTest_Restores(t *testing.T) {
	before := livenessClock
	restore := SetLivenessClockForTest(&clock.Fake{Time: time.Unix(0, 0)})
	if livenessClock == before {
		t.Fatal("SetLivenessClockForTest did not swap the clock")
	}
	restore()
	if livenessClock != before {
		t.Fatal("restore did not put the original clock back")
	}
}
