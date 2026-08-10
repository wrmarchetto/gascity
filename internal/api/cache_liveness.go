package api

import (
	"github.com/gastownhall/gascity/internal/api/apierr"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
)

// The liveness capability is deliberately NOT declared here as a local
// interface. A package-local copy invites the bare `store.(livenessReporter)`
// assertion that ci-0lwn was: it answers "no cache" for every wrapped store,
// which reads as a passing gate. beads.LivenessFor is the only supported way
// to obtain it, and it follows wrapper-declared handles.

// livenessClock is the clock cacheAgeSeconds reads to compute cache age. It is
// clock.Real in production; SetLivenessClockForTest swaps it so the
// CLI-unification characterization harness can freeze the Tier-B cache-age lane
// (the _cache_age_s field and the >30s stale-read banner) deterministically.
// Process-global: bracket clock-sensitive lanes serially, never concurrently.
var livenessClock clock.Clock = clock.Real{}

// SetLivenessClockForTest overrides the clock cacheAgeSeconds uses and returns a
// restore func. Test/harness only — production never mutates it.
func SetLivenessClockForTest(c clock.Clock) (restore func()) {
	prev := livenessClock
	livenessClock = c
	return func() { livenessClock = prev }
}

// cacheLiveOr503 returns a 503 typed error when the given store is backed by a
// CachingStore that has not yet reached the live state. Read handlers call
// this at entry so the CLI receives a fallbackable signal instead of empty
// or partial data while the cache is priming or reconciling. Non-caching
// stores pass through (there's no live/not-live concept to gate).
//
// The error's detail string is prefixed with "cache_not_live:" so
// internal/api.Client can classify the 503 into *cacheNotLiveError, which
// api.ShouldFallback reports as fallbackable.
//
// The capability comes from beads.LivenessFor, never a direct type assertion:
// controller stores arrive here behind cmd/gc's policy wrapper, which embeds
// the beads.Store INTERFACE and so hides the cache underneath. An assertion
// against the wrapper turns this gate into a no-op that reports success.
func cacheLiveOr503(store beads.Store) error {
	lr, ok := beads.LivenessFor(store)
	if !ok {
		return nil
	}
	if lr.IsLive() {
		return nil
	}
	return apierr.StoreUnavailable.Msg("cache_not_live: supervisor cache is priming or reconciling; retry via fallback")
}

// cacheAgeSeconds returns the age in seconds of the store's latest fresh
// observation, or 0 when the store is nil, non-caching, or has never been
// primed. Handlers surface this value through the X-GC-Cache-Age-S
// response header so CLI consumers can flag stale reads.
//
// Same wrapper hazard as cacheLiveOr503, so the same resolution: a header that
// reports 0 for a wrapped store is worse than an absent one, because 0 asserts
// a freshness nothing measured.
func cacheAgeSeconds(store beads.Store) float64 {
	lr, ok := beads.LivenessFor(store)
	if !ok {
		return 0
	}
	s := lr.Stats()
	if s.LastFreshAt.IsZero() {
		return 0
	}
	age := livenessClock.Now().Sub(s.LastFreshAt).Seconds()
	if age < 0 {
		return 0
	}
	return age
}
