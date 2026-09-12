package beads

// Cross-backend agreement on how a stored priority reaches a caller.
//
// The suite exists because Priority is the one Bead field two live backends
// used to disagree about for the most common input there is: NativeDoltStore
// collapsed the upstream default to a nil pointer while BdStore passed the
// same row through as an explicit 2, so any ordering keyed on Priority
// depended on which store constructor fired. Nothing else pinned that seam --
// the per-store suites each assert their own encoding, and a reader picking a
// nil fallback (readySortPriority here, beadPriority in cmd/gc) agrees with
// its sibling only by convention.
//
// Scope is the decode seam and the caller-visible store round trip. Ranking
// policy for a Bead that genuinely carries no priority is delegated to the
// readers' own suites; what is pinned here is that neither live backend hands
// them that case for a default-priority bead.
//
// Run: go test ./internal/beads/ -run BackendsDecodeDefaultPriority\|RoundTripsDefaultPriority

import (
	"encoding/json"
	"testing"

	beadslib "github.com/steveyegge/beads"
)

// upstreamDefaultPriority is the value the upstream schema writes when a row
// declares no priority: `priority INT NOT NULL DEFAULT 2` (beads
// internal/storage/schema/migrations/0001_create_issues.up.sql line 10). The
// column is NOT NULL, so a stored row has no unset state for a backend to
// represent -- which is why both decoders below must produce the same pointer
// and neither may produce nil.
const upstreamDefaultPriority = 2

// TestBackendsDecodeDefaultPriorityIdentically drives the two live decode
// paths with the bytes each backend actually receives for one default-priority
// bead -- a beadslib.Issue off native storage, and a bd JSON row captured from
// the live hq store -- and requires the Beads they produce to rank the same.
//
// The inputs are deliberately not built from a shared fixture: each is the
// wire form of its own backend, so a decoder that invents an encoding of its
// own shows up as a disagreement rather than being laundered through a common
// constructor.
func TestBackendsDecodeDefaultPriorityIdentically(t *testing.T) {
	native, err := beadFromNativeIssue(&beadslib.Issue{
		ID:        "gc-default",
		Title:     "default priority",
		Status:    beadslib.StatusOpen,
		IssueType: beadslib.TypeTask,
		Priority:  upstreamDefaultPriority,
	})
	if err != nil {
		t.Fatalf("beadFromNativeIssue: %v", err)
	}

	// Shape copied from `gc bd list --status open --json` against the live hq
	// store, where all 657 open rows carried an explicit priority key.
	const bdRow = `{
		"id": "gc-default",
		"title": "default priority",
		"status": "open",
		"priority": 2,
		"issue_type": "task"
	}`
	var raw bdIssue
	if err := json.Unmarshal([]byte(bdRow), &raw); err != nil {
		t.Fatalf("decoding bd row: %v", err)
	}
	bd := raw.toBead()

	if native.Priority == nil {
		t.Errorf("NativeDoltStore Priority = nil, want explicit P%d", upstreamDefaultPriority)
	}
	if bd.Priority == nil {
		t.Errorf("BdStore Priority = nil, want explicit P%d", upstreamDefaultPriority)
	}
	if native.Priority != nil && bd.Priority != nil && *native.Priority != *bd.Priority {
		t.Errorf("Priority disagrees across backends: native P%d, bd P%d", *native.Priority, *bd.Priority)
	}
	if native.Priority != nil && *native.Priority != upstreamDefaultPriority {
		t.Errorf("NativeDoltStore Priority = P%d, want P%d", *native.Priority, upstreamDefaultPriority)
	}
	if got, want := readySortPriority(native), readySortPriority(bd); got != want {
		t.Errorf("readySortPriority disagrees across backends: native %d, bd %d", got, want)
	}
}

// TestNativeDoltStoreRoundTripsDefaultPriorityAsExplicitP2 creates a bead
// through the real NativeDoltStore surface with no priority set and requires
// both the create result and a subsequent Get to report the default the store
// actually wrote, not a nil the caller then has to re-interpret.
//
// The store-level round trip is what the decode test above cannot cover: a
// caller that never names a priority still ends up holding a stored row, and
// the write path has already substituted 2 for it by then.
func TestNativeDoltStoreRoundTripsDefaultPriorityAsExplicitP2(t *testing.T) {
	store := newNativeDoltStoreForTest(newNativeDoltMemStorage())

	created, err := store.Create(Bead{Title: "no priority named"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Priority == nil || *created.Priority != upstreamDefaultPriority {
		t.Fatalf("Create Priority = %v, want P%d", created.Priority, upstreamDefaultPriority)
	}

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Priority == nil || *got.Priority != upstreamDefaultPriority {
		t.Fatalf("Get Priority = %v, want P%d", got.Priority, upstreamDefaultPriority)
	}
}
