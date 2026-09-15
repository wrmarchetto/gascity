package orders

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestLastRunIsBoundedToNewestRun is the regression guard for the read the
// order-firing doctor check depends on (ga-klv). The check answers "did this
// order fire recently", which needs exactly one row; if this query ever loses
// its bound it pulls the order's whole retained run history on a city with tens
// of thousands of order-run beads and blows the check's 15s budget.
func TestLastRunIsBoundedToNewestRun(t *testing.T) {
	spy := &listSpyStore{Store: beads.NewMemStore()}
	store := NewStore(beads.OrdersStore{Store: spy})

	if _, err := store.LastRun("digest"); err != nil {
		t.Fatalf("LastRun(): %v", err)
	}
	if len(spy.queries) == 0 {
		t.Fatal("LastRun issued no list query")
	}
	for i, q := range spy.queries {
		if q.Limit != 1 {
			t.Fatalf("query %d: Limit = %d, want 1; an unbounded last-run read is the ga-klv doctor timeout", i, q.Limit)
		}
		if !q.AllowBackingCreatedLimit {
			t.Fatalf("query %d: AllowBackingCreatedLimit = false, want true so the bound reaches the backing instead of being cut client-side", i)
		}
		if q.Sort != beads.SortCreatedDesc {
			t.Fatalf("query %d: Sort = %v, want SortCreatedDesc so row one is the newest run", i, q.Sort)
		}
	}
}

// TestRecentRunsPushesLimitToBacking guards the `gc order history` read. A
// positive limit must reach the backing: cutting client-side still fetches and
// serializes the full retained corpus, which is what made the command take 22s
// interactively on a city with 11k+ order-run rows (ga-klv).
func TestRecentRunsPushesLimitToBacking(t *testing.T) {
	spy := &listSpyStore{Store: beads.NewMemStore()}
	store := NewStore(beads.OrdersStore{Store: spy})

	if _, err := store.RecentRuns("digest", 20); err != nil {
		t.Fatalf("RecentRuns(): %v", err)
	}
	if len(spy.queries) == 0 {
		t.Fatal("RecentRuns issued no list query")
	}
	for i, q := range spy.queries {
		if q.Limit != 20 {
			t.Fatalf("query %d: Limit = %d, want 20 (the caller's bound must be threaded through)", i, q.Limit)
		}
		if !q.AllowBackingCreatedLimit {
			t.Fatalf("query %d: AllowBackingCreatedLimit = false, want true; the bound must reach the backing", i)
		}
	}
}

// TestRecentRunsUnlimitedStaysUnlimited keeps the explicit opt-out working: a
// non-positive limit still means "every run", so `--limit 0` remains an honest
// escape hatch for operators who really do want the full history.
func TestRecentRunsUnlimitedStaysUnlimited(t *testing.T) {
	spy := &listSpyStore{Store: beads.NewMemStore()}
	store := NewStore(beads.OrdersStore{Store: spy})

	if _, err := store.RecentRuns("digest", 0); err != nil {
		t.Fatalf("RecentRuns(): %v", err)
	}
	if len(spy.queries) == 0 {
		t.Fatal("RecentRuns issued no list query")
	}
	for i, q := range spy.queries {
		if q.Limit != 0 {
			t.Fatalf("query %d: Limit = %d, want 0 (unlimited opt-out)", i, q.Limit)
		}
	}
}

// TestRecentRunsWindowPushesCutoffToBacking is the guard for `gc order history
// --since`. The window is what the caller is asking about, so it has to reach
// the backing: with the cutoff applied Go-side instead, the read still fetches
// every retained run and the cost tracks RETENTION rather than the window --
// measured 18.6s unbounded against a 7s floor on a 122k-row city (ci-a2lyow),
// growing without bound as retention does.
//
// The limit is deliberately 0 here. A bounded read was already fast; the
// unbounded one is the shape the order-capacity doctor check is forced into,
// because gc applies --limit before --since and a bound it cannot see would
// understate supply.
func TestRecentRunsWindowPushesCutoffToBacking(t *testing.T) {
	spy := &listSpyStore{Store: beads.NewMemStore()}
	store := NewStore(beads.OrdersStore{Store: spy})
	cutoff := time.Now().Add(-time.Hour)

	if _, err := store.RecentRunsWindow("digest", 0, cutoff); err != nil {
		t.Fatalf("RecentRunsWindow(): %v", err)
	}
	if len(spy.queries) == 0 {
		t.Fatal("RecentRunsWindow issued no list query")
	}
	for i, q := range spy.queries {
		if !q.CreatedAfter.Equal(cutoff) {
			t.Fatalf("query %d: CreatedAfter = %v, want %v; an unpushed cutoff makes the read cost track retention", i, q.CreatedAfter, cutoff)
		}
	}
}

// TestRecentRunsLeavesTheWindowUnbounded pins the absence: the plain RecentRuns
// spelling asks for every retained run and must not acquire a cutoff of its own.
// A defaulted window would silently hide runs from every existing caller.
func TestRecentRunsLeavesTheWindowUnbounded(t *testing.T) {
	spy := &listSpyStore{Store: beads.NewMemStore()}
	store := NewStore(beads.OrdersStore{Store: spy})

	if _, err := store.RecentRuns("digest", 20); err != nil {
		t.Fatalf("RecentRuns(): %v", err)
	}
	if len(spy.queries) == 0 {
		t.Fatal("RecentRuns issued no list query")
	}
	for i, q := range spy.queries {
		if !q.CreatedAfter.IsZero() {
			t.Fatalf("query %d: CreatedAfter = %v, want zero", i, q.CreatedAfter)
		}
	}
}
