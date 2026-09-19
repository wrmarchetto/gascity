//go:build gascity_native_beads

package beads

// Cross-backend agreement on what ListQuery.Status "open" selects.
//
// The suite exists because the two live read backends answered the SAME query
// differently and no test could see it. DoltliteReadStore emits
// `i.status = 'open'` and so selects the stored status; NativeDoltStore
// translated "open" into ExcludeStatus=[closed, in_progress] and so also
// returned blocked, deferred, pinned, hooked and custom-status rows. Both
// results then pass through mapBdStatus, which collapses every one of those
// to Bead.Status "open", so the two answers are indistinguishable downstream
// and the disagreement surfaced only as the controller counting work bd
// itself refuses to serve (ci-iillrh).
//
// Scope is the selection alone, driven from ONE row list so a fixture that
// drifts cannot make the two sides agree by describing different corpora. The
// third backend, BdStore, is delegated: it shells out to `bd list
// --status=open` and its selection is bd's, not this repository's. Decode and
// per-backend query translation are pinned by each store's own suite.
//
// Real SQL on one side is the point -- the native side is driven through this
// package's filterNativeIssuesForTest re-implementation of upstream's
// predicate, so an agreement asserted against another stand-in would prove
// nothing. What this CANNOT represent is upstream's own SQL: a Dolt server
// whose status index drifts (gcy-1on) is covered by
// TestNativeDoltStoreListStatusOpenExcludesClosedBeadsFromUpstreamDrift.
//
// Run: make test-native-doltlite-beads

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	beadslib "github.com/steveyegge/beads"
	_ "modernc.org/sqlite"
)

// statusAgreementRows is the one corpus both backends are driven from: every
// built-in upstream status plus a custom one. The custom row is what an
// exclude-list can never account for, so it is the row that separates
// "selects open" from "excludes the statuses someone thought of".
var statusAgreementRows = []struct {
	id     string
	status string
}{
	{"gc-agree-open", "open"},
	{"gc-agree-blocked", "blocked"},
	{"gc-agree-deferred", "deferred"},
	{"gc-agree-pinned", "pinned"},
	{"gc-agree-hooked", "hooked"},
	{"gc-agree-review", "review"},
	{"gc-agree-active", "in_progress"},
	{"gc-agree-closed", "closed"},
}

func TestDoltliteAndNativeStoresAgreeOnStatusOpenSelection(t *testing.T) {
	query := ListQuery{Status: "open", AllowScan: true}

	doltlite, closeDoltlite := newStatusAgreementDoltliteStore(t)
	defer closeDoltlite()
	doltliteRows, err := doltlite.List(query)
	if err != nil {
		t.Fatalf("DoltliteReadStore.List: %v", err)
	}

	native := newNativeDoltStoreForTest(&nativeDoltStorageSpy{
		searchIssues: func(_ context.Context, _ string, filter beadslib.IssueFilter) ([]*beadslib.Issue, error) {
			return filterNativeIssuesForTest(statusAgreementNativeIssues(), filter), nil
		},
	})
	nativeRows, err := native.List(query)
	if err != nil {
		t.Fatalf("NativeDoltStore.List: %v", err)
	}

	gotDoltlite := sortedBeadIDs(doltliteRows)
	gotNative := sortedBeadIDs(nativeRows)

	// Asserted before the comparison: two empty results agree, and an
	// agreement on nothing is what a broken fixture produces.
	if len(gotDoltlite) == 0 {
		t.Fatalf("DoltliteReadStore.List(Status: open) returned nothing; the fixture carries a stored-open row")
	}
	if len(gotDoltlite) != len(gotNative) {
		t.Fatalf("backends disagree: doltlite = %v, native = %v", gotDoltlite, gotNative)
	}
	for i := range gotDoltlite {
		if gotDoltlite[i] != gotNative[i] {
			t.Fatalf("backends disagree: doltlite = %v, native = %v", gotDoltlite, gotNative)
		}
	}
	// Both agreeing on the WRONG set would still pass the comparison above, so
	// the expected set is stated independently rather than derived from either
	// answer.
	if len(gotDoltlite) != 1 || gotDoltlite[0] != "gc-agree-open" {
		t.Fatalf("Status open selected %v, want only the stored-open row gc-agree-open", gotDoltlite)
	}
}

func statusAgreementNativeIssues() []*beadslib.Issue {
	issues := make([]*beadslib.Issue, 0, len(statusAgreementRows))
	for _, row := range statusAgreementRows {
		issues = append(issues, &beadslib.Issue{
			ID:        row.id,
			Title:     row.status,
			Status:    beadslib.Status(row.status),
			IssueType: beadslib.TypeTask,
			Priority:  2,
		})
	}
	return issues
}

// newStatusAgreementDoltliteStore builds its own fixture database rather than
// extending newTestDoltliteReadStore's shared one, whose row set several
// sibling tests assert cardinalities against.
func newStatusAgreementDoltliteStore(t *testing.T) (*DoltliteReadStore, func()) {
	t.Helper()
	dir := t.TempDir()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(filepath.Join(beadsDir, "doltlite"), 0o755); err != nil {
		t.Fatalf("mkdir doltlite dir: %v", err)
	}
	meta := []byte(`{"backend":"doltlite","database":"doltlite","dolt_database":"hq"}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), meta, 0o600); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(beadsDir, "doltlite", "hq.db")+"?_busy_timeout=10000")
	if err != nil {
		t.Fatalf("open doltlite fixture db: %v", err)
	}
	defer db.Close() //nolint:errcheck // test cleanup
	createTestDoltliteSchema(t, db)

	now := time.Now().UTC()
	for i, row := range statusAgreementRows {
		insertTestDoltliteIssue(t, db, "issues", "labels", "dependencies", testDoltliteIssue{
			ID:        row.id,
			Title:     row.status,
			Status:    row.status,
			IssueType: "task",
			Priority:  2,
			CreatedAt: now.Add(time.Duration(i) * time.Second),
		})
	}

	backing := NewBdStore(dir, func(string, string, ...string) ([]byte, error) {
		t.Fatal("backing bd runner should not be called by the status-agreement test")
		return nil, nil
	})
	store, err := NewDoltliteReadStore(dir, backing)
	if err != nil {
		t.Fatalf("NewDoltliteReadStore: %v", err)
	}
	return store, func() { _ = store.CloseStore() }
}

func sortedBeadIDs(items []Bead) []string {
	ids := make([]string, 0, len(items))
	for _, b := range items {
		ids = append(ids, b.ID)
	}
	sort.Strings(ids)
	return ids
}
