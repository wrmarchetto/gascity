package beads

import (
	"encoding/json"
	"testing"
)

// applierWrapper is embeddingWrapper (liveness_test.go) plus the cache-apply
// handle declaration, mirroring what cmd/gc's policy store does.
type applierWrapper struct{ Store }

func (w applierWrapper) CacheApplierHandle() (CacheApplier, bool) {
	return CacheApplierFor(w.Store)
}

// applyEventThroughWrapperFixture returns a primed cache over an empty backing
// and the JSON payload of a bead that backing does not hold. A bead readable
// after ApplyEvent can therefore only have come from the event, which is what
// distinguishes an applier that reached the cache from one that discarded the
// payload -- ApplyEvent returns nothing, so there is no return value to assert.
func applyEventThroughWrapperFixture(t *testing.T) (*CachingStore, json.RawMessage) {
	t.Helper()
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	if err := cache.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	payload, err := json.Marshal(Bead{ID: "mc-1", Title: "from the bus", Status: "open"})
	if err != nil {
		t.Fatalf("marshal bead: %v", err)
	}
	return cache, payload
}

func TestCacheApplierForResolvesThroughDelegatingWrapper(t *testing.T) {
	// The applier must stay bound to the cache rather than capture its state at
	// resolve time: the controller resolves once per event for the life of the
	// process, so a handle that snapshotted anything would decay silently.
	cache, payload := applyEventThroughWrapperFixture(t)
	store := applierWrapper{Store: cache}

	applier, ok := CacheApplierFor(store)
	if !ok {
		t.Fatalf("CacheApplierFor(%T) reported no capability, want the wrapped cache", store)
	}
	applier.ApplyEvent("bead.created", payload)

	got, err := cache.Get("mc-1")
	if err != nil {
		t.Fatalf("Get after ApplyEvent through the wrapper: %v", err)
	}
	if got.Title != "from the bus" {
		t.Fatalf("Get().Title = %q, want the event payload's title", got.Title)
	}
}

func TestCacheApplierForResolvesThroughNestedDelegatingWrappers(t *testing.T) {
	// Depth comes from the delegation chain, not from a loop in CacheApplierFor,
	// so a stack of wrappers is the case that proves the chain composes.
	cache, payload := applyEventThroughWrapperFixture(t)
	store := applierWrapper{Store: applierWrapper{Store: cache}}
	applier, ok := CacheApplierFor(store)
	if !ok {
		t.Fatalf("CacheApplierFor(%T) reported no capability through two wrappers", store)
	}
	applier.ApplyEvent("bead.created", payload)
	if _, err := cache.Get("mc-1"); err != nil {
		t.Fatalf("Get after ApplyEvent through two wrappers: %v", err)
	}
}

func TestCacheApplierForRejectsUndeclaredWrapper(t *testing.T) {
	// This absence is the defect ci-1p6a recorded, kept as a pin rather than
	// repaired: a wrapper that declares nothing CANNOT be seen through, since Go
	// promotes no optional method of an embedded interface. Any new wrapper on
	// the controller's store path must declare CacheApplierHandle, and this test
	// is what says so out loud. Repairing it would mean reflection or a
	// registry -- both hide the requirement instead of stating it.
	cache, _ := applyEventThroughWrapperFixture(t)
	if applier, ok := CacheApplierFor(embeddingWrapper{Store: cache}); ok {
		t.Fatalf("CacheApplierFor(undeclared wrapper) = (%T, true), want no capability", applier)
	}
}

func TestCacheApplierForReportsNoCapabilityForUncachedStores(t *testing.T) {
	// "No cache" must stay distinguishable from "cache updated". A delegating
	// wrapper over an uncached store is the case that would break it if the
	// wrapper implemented ApplyEvent directly: its only available behavior there
	// is to discard the event, which is exactly the failure the capability
	// exists to make visible.
	for _, tc := range []struct {
		name  string
		store Store
	}{
		{"nil store", nil},
		{"uncached store", NewMemStore()},
		{"delegating wrapper over uncached store", applierWrapper{Store: NewMemStore()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if applier, ok := CacheApplierFor(tc.store); ok {
				t.Fatalf("CacheApplierFor(%s) = (%T, true), want no capability", tc.name, applier)
			}
		})
	}
}

func TestCacheApplierForResolvesBareCachingStore(t *testing.T) {
	// Unwrapped stores still reach the fan-out (tests, and any future call site
	// holding a cache directly), so the direct assertion has to keep working
	// alongside the handle path.
	cache := NewCachingStoreForTest(NewMemStore(), nil)
	applier, ok := CacheApplierFor(cache)
	if !ok {
		t.Fatal("CacheApplierFor(*CachingStore) reported no capability")
	}
	if applier != CacheApplier(cache) {
		t.Fatalf("CacheApplierFor returned %p, want the cache itself %p", applier, cache)
	}
}
