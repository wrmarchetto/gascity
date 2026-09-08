package api

import "github.com/gastownhall/gascity/internal/api/apierr"

// withIdempotency runs create() at most once per (scoped key, request body),
// giving create endpoints safe retries via the Idempotency-Key header.
//
// The scoped key is "POST:<path>:<key>" — namespaced by path so the same
// Idempotency-Key value on two different endpoints within one city can't
// collide. Every city-scoped caller passes a static endpoint path (e.g.
// "/v0/agents"), so this scoping is intra-city only; cross-city isolation does
// NOT come from the key. It comes from each city owning a separate *Server,
// and therefore a separate idem cache, built per city by getCityServer via
// New(state). A future refactor that hoisted a per-city cache to a
// process-wide scope would silently reintroduce cross-city key collisions
// despite this scoping. On a repeat with a completed reservation it replays
// the cached typed body value; an in-flight repeat returns
// apierr.IdempotencyInFlight (409); a same-key/different-body repeat returns
// apierr.IdempotencyMismatch (422).
//
// It ALWAYS releases the pending reservation when create() returns an error OR
// panics (via defer), so no caller can leak a reservation — the defect the
// hand-rolled per-handler unreserve boilerplate was prone to. When key == "" it
// is a passthrough: create() runs exactly once and nothing is cached.
//
// create() should perform all fallible work (validation, the store write) and
// return the domain body value to cache. Callers wrap that value in their
// response envelope after withIdempotency returns, so envelope fields derived
// from live state (e.g. the X-GC-Index event sequence) stay fresh on replay.
//
// idem is the owning cache: per-city handlers pass s.idem (the per-city cache
// described above); supervisor-scope handlers (POST /v0/city, where no per-city
// Server exists yet) pass sm.idem, the SupervisorMux's own process-wide cache.
func withIdempotency[T any](idem *idempotencyCache, path, key string, body any, create func() (T, error)) (T, error) {
	return withIdempotencyReplaying(idem, path, key, body, create, nil)
}

// withIdempotencyReplaying is withIdempotency plus a reconcile step that runs
// on the replay path, where create() does not.
//
// It exists for one endpoint and should stay rare. The replay-skips-create
// rule is right wherever create() produces a durable resource with its own
// identity — a bead, a convoy, a rig, a mail message, an appended event —
// because the key names one logical operation that already happened, and
// re-running it is precisely what the caller asked to be spared. POST
// /v0/extmsg/adapters is the exception: its effect is an upsert into an
// in-memory registry that the caller treats as a renewable LEASE, so a replay
// that skips it answers "registered" over an empty registry.
//
// reconcile must be the create's effect WITHOUT its announcement. Suppressing
// the duplicate event is the whole reason such an endpoint is idempotent, so a
// reconcile that re-emitted would leave the endpoint with no reason to be here
// at all.
//
// Deliberately NOT a change to withIdempotency's own replay branch. Ten other
// callers rely on that branch skipping their create, and giving the helper a
// blanket re-run would make an unrelated endpoint's side effect fire twice for
// no reader of this file to notice.
//
// It runs AFTER the mismatch and in-flight guards, which is why the effect
// cannot simply be hoisted out of create() and run unconditionally before the
// helper. Hoisting it would register the adapter and then reject the request
// 422 on a same-key/different-body repeat.
func withIdempotencyReplaying[T any](idem *idempotencyCache, path, key string, body any, create func() (T, error), reconcile func() error) (T, error) {
	var zero T
	if key == "" {
		return create()
	}
	scopedKey := "POST:" + path + ":" + key
	bodyHash := hashBody(body)

	existing, found := idem.reserve(scopedKey, bodyHash)
	if found {
		if existing.bodyHash != bodyHash {
			return zero, apierr.IdempotencyMismatch.Msg("idempotency_mismatch: Idempotency-Key reused with different request body")
		}
		if existing.pending {
			return zero, apierr.IdempotencyInFlight.Msg("in_flight: request with this Idempotency-Key is already in progress")
		}
		if v, ok := replayAs[T](existing); ok {
			if reconcile != nil {
				if err := reconcile(); err != nil {
					return zero, err
				}
			}
			return v, nil
		}
		// Completed entry of an unexpected type (should be impossible for a
		// given endpoint's fixed T). Fall through and recreate rather than
		// serve a wrong-typed replay.
	}

	// Release the reservation on any non-success exit — an error return OR a
	// panic in create(). unreserve only drops a *pending* entry, so on the
	// wrong-typed fall-through above (where this caller holds no reservation) it
	// is a harmless no-op against the completed entry.
	settled := false
	defer func() {
		if !settled {
			idem.unreserve(scopedKey)
		}
	}()

	v, err := create()
	if err != nil {
		return zero, err
	}
	settled = true
	idem.storeResponse(scopedKey, bodyHash, v)
	return v, nil
}
