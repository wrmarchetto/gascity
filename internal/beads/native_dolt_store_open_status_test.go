package beads

// What ListQuery.Status "open" selects on the native backend.
//
// The suite exists because "open" is the one status value whose meaning used
// to depend on which backend answered. BdStore passes it through as bd's raw
// --status=open and DoltliteReadStore as `i.status = 'open'`, so both select
// the STORED status alone; NativeDoltStore translated it to
// ExcludeStatus=[closed, in_progress], which is the inverse of mapBdStatus's
// two named cases rather than a status selection, and so also returned every
// bead stored blocked, deferred, pinned, hooked or in a custom status. A
// caller could not tell the two apart after the fact: mapBdStatus collapses
// all of them to "open" on decode, so the disagreement is invisible in the
// result rows and shows up only as a tracker and its own store answering
// differently about what is workable (ci-iillrh).
//
// Scope is the query seam -- the IssueFilter this store emits, and the rows
// List and Count return for it. The DECODE collapse is deliberately NOT under
// test here and is unchanged: Gas City's Bead.Status has three values and
// TestNativeDoltStoreMapsUpstreamStatusesToGasCityContract pins that mapping.
// Cross-backend agreement against real SQL is delegated to
// TestDoltliteAndNativeStoresAgreeOnStatusOpenSelection, which needs the
// tagged doltlite fixture; what is pinned here is the native side alone.
//
// Run: go test ./internal/beads/ -run 'NativeDoltStore.*StatusOpen|NativeDoltStoreOpenStatus'

import (
	"context"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// nativeStoredStatusFixture is one issue per built-in upstream status plus
// one custom status, so a filter that selects by exclusion rather than by
// value is caught by whichever row it forgot. The built-in set is
// types.AllStatuses (beads internal/types/types.go, `AllStatuses`): open,
// in_progress, blocked, deferred, closed, pinned, hooked. Pinned and hooked
// are spelled as literals because the root package re-exports neither alias,
// which is also why this list cannot be derived and has to be re-checked when
// the pinned beads version moves. "review" is NOT a built-in status -- it is
// the custom status the existing native suites use -- and it is here
// precisely because an exclude-list can never account for a status the list's
// author never saw.
func nativeStoredStatusFixture() []*beadslib.Issue {
	return []*beadslib.Issue{
		{ID: "gc-open", Title: "open", Status: beadslib.StatusOpen, IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-blocked", Title: "blocked", Status: beadslib.StatusBlocked, IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-deferred", Title: "deferred", Status: beadslib.StatusDeferred, IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-pinned", Title: "pinned", Status: beadslib.Status("pinned"), IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-hooked", Title: "hooked", Status: beadslib.Status("hooked"), IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-review", Title: "review", Status: beadslib.Status("review"), IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-active", Title: "active", Status: beadslib.StatusInProgress, IssueType: beadslib.TypeTask, Priority: 2},
		{ID: "gc-closed", Title: "closed", Status: beadslib.StatusClosed, IssueType: beadslib.TypeTask, Priority: 2},
	}
}

// TestNativeDoltStoreOpenStatusFilterSelectsStoredOpenOnly pins the emitted
// IssueFilter rather than the rows, because the filter is what a real Dolt
// server sees and the spy below is this package's own re-implementation of
// upstream's predicate. A filter asserted here cannot be satisfied by a
// sympathetic stand-in.
func TestNativeDoltStoreOpenStatusFilterSelectsStoredOpenOnly(t *testing.T) {
	filter := nativeIssueFilterFromListQuery(ListQuery{Status: "open", AllowScan: true})

	if filter.Status == nil {
		t.Fatalf("Status filter = nil, want a positive selection of %q", beadslib.StatusOpen)
	}
	if *filter.Status != beadslib.StatusOpen {
		t.Fatalf("Status filter = %q, want %q", *filter.Status, beadslib.StatusOpen)
	}
	// An exclude-list alongside the positive selection would be dead weight at
	// best and, if it ever disagreed with it, the same ambiguity this commit
	// removed.
	if len(filter.ExcludeStatus) != 0 {
		t.Fatalf("ExcludeStatus = %v, want empty once the status is selected by value", filter.ExcludeStatus)
	}
}

// TestNativeDoltStoreListStatusOpenReturnsOnlyStoredOpenBeads is the
// end-to-end half: the rows a caller gets back. Every non-open row in the
// fixture decodes to Bead.Status "open" through mapBdStatus, so the assertion
// is on IDs -- a status assertion on the result would pass whether or not the
// filter worked.
func TestNativeDoltStoreListStatusOpenReturnsOnlyStoredOpenBeads(t *testing.T) {
	issues := nativeStoredStatusFixture()
	storage := &nativeDoltStorageSpy{
		searchIssues: func(_ context.Context, _ string, filter beadslib.IssueFilter) ([]*beadslib.Issue, error) {
			return filterNativeIssuesForTest(issues, filter), nil
		},
	}
	store := newNativeDoltStoreForTest(storage)

	got, err := store.List(ListQuery{AllowScan: true, Status: "open", TierMode: TierBoth})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != 1 || got[0].ID != "gc-open" {
		t.Fatalf("List(Status: open) = %v, want exactly the stored-open bead gc-open", nativeBeadIDsForTest(got))
	}
}

// TestNativeDoltStoreCountStatusOpenMatchesListCardinality pins the Counter
// contract for the shape nativeDoltCountSupported used to refuse. The refusal
// existed only because the exclude-list translation over-counted against
// Matches' exact status compare; with the status selected by value the two
// agree, and a Count that silently drifted from List would be a wrong
// store-health denominator rather than a visible error.
func TestNativeDoltStoreCountStatusOpenMatchesListCardinality(t *testing.T) {
	issues := nativeStoredStatusFixture()
	storage := &nativeDoltStorageSpy{
		searchIssues: func(_ context.Context, _ string, filter beadslib.IssueFilter) ([]*beadslib.Issue, error) {
			return filterNativeIssuesForTest(issues, filter), nil
		},
		countIssues: func(_ context.Context, _ string, filter beadslib.IssueFilter) (int64, error) {
			return int64(len(filterNativeIssuesForTest(issues, filter))), nil
		},
	}
	store := newNativeDoltStoreForTest(storage)
	query := ListQuery{AllowScan: true, Status: "open", TierMode: TierBoth}

	listed, err := store.List(query)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	counted, err := store.Count(context.Background(), query)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if counted != len(listed) {
		t.Fatalf("Count(Status: open) = %d, List cardinality = %d (%v)", counted, len(listed), nativeBeadIDsForTest(listed))
	}
}

func nativeBeadIDsForTest(items []Bead) []string {
	ids := make([]string, 0, len(items))
	for _, b := range items {
		ids = append(ids, b.ID)
	}
	return ids
}
