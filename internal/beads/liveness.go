package beads

// LivenessReporter is the optional store capability that reports cache
// freshness. Only *CachingStore implements it natively. Stores with no cache
// (BdStore, MemStore, file stores) have no live/not-live concept at all, and
// their absence from this interface is the signal callers gate on — there is
// deliberately no zero-valued "always live" stand-in, because a fabricated
// live=true is indistinguishable at the call site from a measured one.
type LivenessReporter interface {
	IsLive() bool
	Stats() CacheStats
}

// LivenessHandleProvider exposes a liveness handle for wrappers whose
// capability depends on the store they wrap. It mirrors
// GraphApplyHandleProvider: a wrapper delegates instead of claiming
// LivenessReporter outright, so wrapping a non-caching store still reports
// "no cache" rather than inventing a healthy one.
//
// Delegation is what makes this expressible. A Go method set is fixed at
// compile time, so a wrapper that implemented IsLive/Stats directly would
// have to answer for uncached backings too — and cmd/gc's policy wrapper
// wraps plain stores on several paths, not only the cached controller store.
// Its only available answers there are a live=true that lies and a
// live=false that 503s an uncached deployment forever.
type LivenessHandleProvider interface {
	LivenessHandle() (LivenessReporter, bool)
}

// LivenessFor returns the cache-liveness capability for store when one is
// available, following a wrapper-supplied handle down to the store that owns
// the cache.
//
// Callers must route through this rather than asserting LivenessReporter on
// the store directly. A wrapper that embeds the Store INTERFACE satisfies
// Store while dropping every optional method of the concrete value beneath
// it, so a bare assertion answers "no cache" for a wrapped CachingStore and
// any staleness gate built on it degrades to an unconditional pass. That is
// not hypothetical: the API's X-GC-Cache-Age-S header read 0 on every
// response while its store had not reconciled in 29 hours, because cmd/gc's
// policy wrapper hid the CachingStore underneath (ci-0lwn, found via ci-enyk).
//
// Following is single-hop by design, matching GraphApplyFor: a wrapper's
// LivenessHandle recurses through this function for its own inner store, so
// depth comes from the delegation chain itself. No cycle bound is imposed
// because, unlike ConditionalWritesResolveTargeter, no wrapper can name
// itself as the target — it can only return what LivenessFor already
// resolved beneath it.
func LivenessFor(store Store) (LivenessReporter, bool) {
	if store == nil {
		return nil, false
	}
	if reporter, ok := store.(LivenessReporter); ok {
		return reporter, true
	}
	if provider, ok := store.(LivenessHandleProvider); ok {
		return provider.LivenessHandle()
	}
	return nil, false
}
