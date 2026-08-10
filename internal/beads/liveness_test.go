package beads

import (
	"testing"
)

// embeddingWrapper is the shape every store wrapper in this repo takes: it
// embeds the Store INTERFACE, so it satisfies Store while dropping the
// optional methods of whatever concrete value it holds. It declares no
// liveness handle, which is the point — it stands in for a wrapper whose
// author did not know one was needed.
type embeddingWrapper struct{ Store }

// delegatingWrapper is embeddingWrapper plus the handle declaration, mirroring
// what cmd/gc's policy store does.
type delegatingWrapper struct{ Store }

func (w delegatingWrapper) LivenessHandle() (LivenessReporter, bool) {
	return LivenessFor(w.Store)
}

func TestLivenessForResolvesThroughDelegatingWrapper(t *testing.T) {
	// The reporter must stay bound to the cache rather than capture its state
	// at resolve time: the API resolves per request, but a handle that
	// snapshotted liveness would report "priming" for the life of the process
	// once anything resolved it before the first Prime.
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	store := delegatingWrapper{Store: cache}

	reporter, ok := LivenessFor(store)
	if !ok {
		t.Fatalf("LivenessFor(%T) reported no capability, want the wrapped cache", store)
	}
	if reporter.IsLive() {
		t.Fatal("IsLive = true before Prime, want false")
	}
	if err := cache.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if !reporter.IsLive() {
		t.Fatal("IsLive = false after Prime, want true through the same handle")
	}
	// Stats is asserted separately from IsLive because the reported symptom
	// rode on Stats alone: the API's cache-age header reads LastFreshAt, and a
	// zero there renders X-GC-Cache-Age-S: 0 whatever IsLive says.
	got, want := reporter.Stats().LastFreshAt, cache.Stats().LastFreshAt
	if got.IsZero() {
		t.Fatal("Stats().LastFreshAt is zero after Prime; a cache-age header built on this would read 0")
	}
	if !got.Equal(want) {
		t.Fatalf("Stats().LastFreshAt = %v, want the wrapped cache's %v", got, want)
	}
}

func TestLivenessForResolvesThroughNestedDelegatingWrappers(t *testing.T) {
	// Depth comes from the delegation chain, not from a loop in LivenessFor,
	// so a stack of wrappers is the case that proves the chain composes.
	// cmd/gc reaches depth two whenever a class wrapper sits over the policy
	// store.
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	store := delegatingWrapper{Store: delegatingWrapper{Store: cache}}
	if _, ok := LivenessFor(store); !ok {
		t.Fatalf("LivenessFor(%T) reported no capability through two wrappers", store)
	}
}

func TestLivenessForRejectsUndeclaredWrapper(t *testing.T) {
	// This absence is the defect ci-0lwn recorded, kept as a pin rather than
	// repaired: a wrapper that declares nothing CANNOT be seen through, since
	// Go promotes no optional method of an embedded interface. Any new wrapper
	// on a read path must declare LivenessHandle, and this test is what says
	// so out loud. Repairing it would mean reflection or a registry — both
	// hide the requirement instead of stating it.
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	if err := cache.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	if reporter, ok := LivenessFor(embeddingWrapper{Store: cache}); ok {
		t.Fatalf("LivenessFor(undeclared wrapper) = (%T, true), want no capability", reporter)
	}
}

func TestLivenessForReportsNoCapabilityForUncachedStores(t *testing.T) {
	// "No cache" must stay distinguishable from "cache checked and live".
	// A delegating wrapper over an uncached store is the case that would
	// break it if the wrapper implemented IsLive directly: its only honest
	// answers are a live=true that lies and a live=false that 503s an
	// uncached deployment forever.
	for _, tc := range []struct {
		name  string
		store Store
	}{
		{"nil store", nil},
		{"uncached store", NewMemStore()},
		{"delegating wrapper over uncached store", delegatingWrapper{Store: NewMemStore()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if reporter, ok := LivenessFor(tc.store); ok {
				t.Fatalf("LivenessFor(%s) = (%T, true), want no capability", tc.name, reporter)
			}
		})
	}
}

func TestLivenessForResolvesBareCachingStore(t *testing.T) {
	// Unwrapped stores still reach handlers (suspended rigs, tests), so the
	// direct assertion has to keep working alongside the handle path.
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	reporter, ok := LivenessFor(cache)
	if !ok {
		t.Fatal("LivenessFor(*CachingStore) reported no capability")
	}
	if reporter != LivenessReporter(cache) {
		t.Fatalf("LivenessFor returned %p, want the cache itself %p", reporter, cache)
	}
}
