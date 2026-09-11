package runtime

import (
	"errors"
	"testing"
)

// countingLister records ListRunning calls and REFUSES everything else: it
// embeds a nil Provider, so any other method the code under test reaches
// panics rather than quietly answering. A stand-in that satisfied every call
// would hand a pass to whatever these tests forgot to script.
type countingLister struct {
	Provider
	calls int
	names []string
	err   error
}

func (c *countingLister) ListRunning(prefix string) ([]string, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	if prefix == "" {
		return c.names, nil
	}
	var out []string
	for _, n := range c.names {
		if len(n) >= len(prefix) && n[:len(prefix)] == prefix {
			out = append(out, n)
		}
	}
	return out, nil
}

// TestSessionListCacheTakesOneListingPerPass pins the whole point: N calls in
// one pass cost one listing. Asserted on the underlying call COUNT rather than
// on the returned names, because a wrapper that returned correct names while
// re-fetching each time is the defect, and the names alone cannot see it.
func TestSessionListCacheTakesOneListingPerPass(t *testing.T) {
	inner := &countingLister{names: []string{"a-1", "a-2", "b-1"}}
	c := NewSessionListCache(inner)
	for i := 0; i < 50; i++ {
		if _, err := c.ListRunning(""); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("50 ListRunning calls cost %d listings, want 1", inner.calls)
	}
}

// TestSessionListCacheFiltersByPrefixFromOneListing pins that the memoized
// full listing serves prefixed callers correctly.
//
// This is the property that lets one listing serve a pass whose callers use
// different prefixes, and it rests on ListRunning being specified to return
// the sessions carrying the given prefix. If that ever stops holding for some
// provider, this fails here rather than as a wrong pool expansion somewhere
// downstream.
func TestSessionListCacheFiltersByPrefixFromOneListing(t *testing.T) {
	inner := &countingLister{names: []string{"city-a-1", "city-a-2", "city-b-1"}}
	c := NewSessionListCache(inner)
	got, err := c.ListRunning("city-a-")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "city-a-1" || got[1] != "city-a-2" {
		t.Fatalf("prefix filter returned %v, want the two city-a- sessions", got)
	}
	all, err := c.ListRunning("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("unprefixed call returned %v, want all three", all)
	}
	if inner.calls != 1 {
		t.Fatalf("two differently-prefixed calls cost %d listings, want 1",
			inner.calls)
	}
}

// TestSessionListCacheRepeatsTheOutageRatherThanRetrying pins the
// PartialListError contract across the wrapper.
//
// ListRunning turns a dead tmux server into a PartialListError so the
// reconciler's destructive guards defer instead of concluding zero sessions
// exist. A wrapper that cached only the names would retry on every call and
// could report an outage to the first caller in a pass and a healthy empty
// list to the next, which is the shape those guards cannot survive. The error
// is memoized with the names, so a pass sees one answer.
func TestSessionListCacheRepeatsTheOutageRatherThanRetrying(t *testing.T) {
	sentinel := errors.New("no server running")
	inner := &countingLister{err: sentinel}
	c := NewSessionListCache(inner)
	for i := 0; i < 5; i++ {
		if _, err := c.ListRunning(""); !errors.Is(err, sentinel) {
			t.Fatalf("call %d returned %v, want the underlying outage", i, err)
		}
	}
	if inner.calls != 1 {
		t.Fatalf("5 calls during an outage cost %d listings, want 1", inner.calls)
	}
}

// TestSessionListCacheOfNilProviderIsNil pins the branch-free call site: a
// caller that only sometimes builds a provider must be able to wrap
// unconditionally. Returning a non-nil wrapper around a nil Provider would
// turn every `if sp == nil` guard in the tree into a nil-pointer panic on
// first use, which is a worse failure than the one being fixed.
func TestSessionListCacheOfNilProviderIsNil(t *testing.T) {
	if got := NewSessionListCache(nil); got != nil {
		t.Fatalf("NewSessionListCache(nil) = %#v, want nil", got)
	}
}
