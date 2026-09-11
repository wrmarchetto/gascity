package runtime

import "strings"

// SessionListCache wraps a Provider so that one read-only pass over the agent
// config costs a single session listing instead of one per agent.
//
// WHY A WRAPPER RATHER THAN A CACHE INSIDE THE PROVIDER. The tmux provider
// already has a StateCache (2s TTL, singleflight) and ListRunning deliberately
// does not use it, for two reasons that a shared cache would silently undo.
// StateCache is built from `list-panes -a` and skips dead panes, so it cannot
// see a remain-on-exit corpse that ListRunning reports and
// CleanupOrphanedSessions exists to reap; and it ABSORBS a tmux outage by
// serving last-known-good, where ListRunning converts ErrNoServer into a
// PartialListError precisely so the reconciler's on_death, provider-swap,
// shutdown and orphan-cleanup guards defer destructive action during a blip.
// Routing ListRunning through it would hand those guards a stale-but-plausible
// list instead of the partial-list signal they are built on.
//
// A wrapper keeps that asymmetry explicit. Only a caller that opts in gets
// memoization, and the trust window is the wrapper's own lifetime -- one CLI
// invocation or one HTTP request -- rather than a wall-clock TTL nobody at the
// call site chose. Every destructive caller holds the bare provider and is
// unaffected, which is checkable by reading who constructs one of these.
//
// NOT SAFE TO SHARE ACROSS PASSES, and deliberately not made so: there is no
// invalidation and no TTL. Construct one, use it for a pass, drop it. A
// long-lived instance would report a session that has since died as running,
// which is the same class of defect this file's own reasoning rejects for
// StateCache.
//
// NOT CONCURRENCY-SAFE. The passes this exists for are sequential loops over
// cfg.Agents. Adding a mutex would invite exactly the long-lived shared
// instance the paragraph above forbids.
//
// Pinned by internal/runtime/session_list_cache_test.go and, at the call
// sites, by TestDoRigListJSONSessionListingIsIndependentOfAgentCount in
// cmd/gc.
type SessionListCache struct {
	Provider
	fetched bool
	names   []string
	err     error
}

// NewSessionListCache returns a Provider that answers every ListRunning call
// in one pass from a single underlying listing. It returns p unchanged when p
// is nil, so a caller that only sometimes builds a provider needs no branch.
func NewSessionListCache(p Provider) Provider {
	if p == nil {
		return nil
	}
	return &SessionListCache{Provider: p}
}

// ListRunning answers from the one listing this pass has taken, fetching it on
// first use.
//
// The underlying fetch always asks for every session and the prefix is applied
// here, because ListRunning is specified to return the running sessions whose
// names carry the given prefix -- so filtering the full list is the same
// answer, and one memoized listing then serves callers using different
// prefixes. The alternative, memoizing per prefix, would pay a subprocess for
// each distinct prefix and buy nothing.
//
// The ERROR is memoized alongside the names, and that is what preserves the
// PartialListError contract: a pass that hits a tmux outage sees the same
// partial-list signal on every call rather than one failure followed by
// retries that may each land differently. A pass must not act on a list that
// changed shape halfway through it.
func (c *SessionListCache) ListRunning(prefix string) ([]string, error) {
	if !c.fetched {
		c.names, c.err = c.Provider.ListRunning("")
		c.fetched = true
	}
	if c.err != nil {
		return nil, c.err
	}
	if prefix == "" {
		return c.names, nil
	}
	out := make([]string, 0, len(c.names))
	for _, n := range c.names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out, nil
}
