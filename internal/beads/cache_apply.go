package beads

import "encoding/json"

// CacheApplier is the optional store capability that absorbs a bead event from
// the event bus into an in-memory cache. Only *CachingStore implements it
// natively. Stores with no cache (BdStore, MemStore, file stores) have nothing
// to absorb into, and their absence from this interface is the signal callers
// gate on -- there is deliberately no no-op stand-in, because a silently
// discarding applier is indistinguishable at the call site from one that
// updated a cache.
type CacheApplier interface {
	ApplyEvent(eventType string, payload json.RawMessage)
}

// CacheApplierHandleProvider exposes a cache-apply handle for wrappers whose
// capability depends on the store they wrap. It mirrors
// LivenessHandleProvider: a wrapper delegates instead of claiming CacheApplier
// outright, so wrapping an uncached store still reports "no cache" rather than
// silently swallowing every event the bus delivers.
type CacheApplierHandleProvider interface {
	CacheApplierHandle() (CacheApplier, bool)
}

// CacheApplierFor returns the cache-apply capability for store when one is
// available, following a wrapper-supplied handle down to the store that owns
// the cache.
//
// Callers must route through this rather than asserting *CachingStore on the
// store directly. A concrete-type assertion is the exact shape that made the
// controller's event fan-out structurally dead: every store the controller
// holds is policy-wrapped (wrapWithCachingStore unwraps the policy layer,
// builds the cache, and re-wraps on the way out), so
// `store.(*beads.CachingStore)` matched nothing in production while passing in
// every test that assigned a bare cache. Caches then converged only at
// reconcile cadence -- 60s, 120s once a store classifies LARGE -- with the
// onChange/Record loop still emitting events nothing consumed (ci-1p6a, same
// class as ci-0lwn).
//
// Following is single-hop by design, matching LivenessFor and GraphApplyFor: a
// wrapper's CacheApplierHandle recurses through this function for its own inner
// store, so depth comes from the delegation chain itself.
func CacheApplierFor(store Store) (CacheApplier, bool) {
	if store == nil {
		return nil, false
	}
	if applier, ok := store.(CacheApplier); ok {
		return applier, true
	}
	if provider, ok := store.(CacheApplierHandleProvider); ok {
		return provider.CacheApplierHandle()
	}
	return nil, false
}
