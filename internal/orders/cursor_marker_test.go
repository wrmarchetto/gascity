// internal/orders/cursor_marker_test.go
//
// Pins both halves of the live-cursor marker contract: the retirement rule --
// which beads lose the marker and, the part that carries the risk, which must
// not -- and the read semantics of CursorIndex, above all its absent-versus-zero
// distinction and its refusal to answer from a partial federation.
//
// The marker is what lets the dispatcher read every event order's cursor in one
// query (ci-jg1k70). Retirement keeps the marked set at one row per order, but a
// retirement that unmarks the wrong row silently lowers a cursor, and a lowered
// cursor replays consumed events: duplicate nudges, duplicate notifications.
// Nothing downstream reports that as an error, so it is pinned here rather than
// left to a dispatcher-level verdict test, which would see the same "not due".
//
// Delegated elsewhere: that the dispatch loop actually USES the index, and that
// each stamping path retires -- cmd/gc/order_dispatch_cursor_scan_test.go.
//
// Run: go test ./internal/orders/ -run 'TestRetireCursorMarkers|TestCursorIndex'
package orders

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func seedMarkedRun(t *testing.T, store beads.Store, scoped string, seq uint64) beads.Bead {
	t.Helper()
	b, err := store.Create(beads.Bead{
		Title:  trackingTitle(scoped),
		Labels: append([]string{labelOrderTracking, labelOrderRunPrefix + scoped}, CursorLabels(scoped, EventCursor(seq))...),
	})
	if err != nil {
		t.Fatalf("seeding run seq=%d: %v", seq, err)
	}
	return b
}

func markedIDs(t *testing.T, store beads.Store) map[string]bool {
	t.Helper()
	list, err := store.List(beads.ListQuery{Label: labelOrderCursor, IncludeClosed: true, TierMode: beads.TierBoth})
	if err != nil {
		t.Fatalf("listing marked beads: %v", err)
	}
	out := make(map[string]bool, len(list))
	for _, b := range list {
		out[b.ID] = true
	}
	return out
}

// TestRetireCursorMarkersDropsOnlyLowerSeqs proves retirement is selected by
// RECORDED SEQ, not by identity.
//
// The two beads differ only in seq, and the keeper is the LOWER one -- the shape
// an out-of-order write produces when a manual gc order run races a tick, or an
// event provider hands back a seq behind one already stamped. Retiring
// "everything that is not keepID" would unmark the higher row here and lose the
// true maximum; comparing seqs cannot. A same-seq row is also kept: it carries
// the max, so dropping it would be a coin flip on which copy survives.
func TestRetireCursorMarkersDropsOnlyLowerSeqs(t *testing.T) {
	store := beads.NewMemStore()
	front := NewStore(beads.OrdersStore{Store: store})

	older := seedMarkedRun(t, store, "rig/agent", 3)
	equal := seedMarkedRun(t, store, "rig/agent", 5)
	higher := seedMarkedRun(t, store, "rig/agent", 9)
	keeper := seedMarkedRun(t, store, "rig/agent", 5)

	if err := front.RetireCursorMarkers("rig/agent", keeper.ID, EventCursor(5)); err != nil {
		t.Fatalf("RetireCursorMarkers(): %v", err)
	}

	marked := markedIDs(t, store)
	if marked[older.ID] {
		t.Errorf("run at seq 3 kept its marker; a strictly lower seq must be retired")
	}
	for _, keep := range []struct {
		id   string
		what string
	}{
		{keeper.ID, "the run just stamped"},
		{equal.ID, "a run at the same seq"},
		{higher.ID, "a run at a HIGHER seq -- retiring it would lower the cursor and replay events"},
	} {
		if !marked[keep.id] {
			t.Errorf("marker retired from %s", keep.what)
		}
	}

	if got, err := front.CursorIndex(); err != nil {
		t.Fatalf("CursorIndex(): %v", err)
	} else if got["rig/agent"] != EventCursor(9) {
		t.Errorf("cursor after retirement = %d, want 9 (the surviving maximum)", got["rig/agent"])
	}
}

// TestRetireCursorMarkersLeavesOtherOrdersAlone proves the retirement is scoped
// by the order:<scoped> label rather than by the shared marker alone.
//
// The marker is deliberately NOT per-order -- that is what makes the read a
// single query -- so the one selector that separates orders inside the matched
// set is the order label. A retirement that skipped that check would unmark
// every other order on each dispatch, and the symptom would not be an error: the
// next tick would find those orders absent from the index and quietly fall back
// to the per-order full-history scan, which is the exact cost this all exists to
// remove.
func TestRetireCursorMarkersLeavesOtherOrdersAlone(t *testing.T) {
	store := beads.NewMemStore()
	front := NewStore(beads.OrdersStore{Store: store})

	neighbor := seedMarkedRun(t, store, "rig/other", 2)
	keeper := seedMarkedRun(t, store, "rig/agent", 7)

	if err := front.RetireCursorMarkers("rig/agent", keeper.ID, EventCursor(7)); err != nil {
		t.Fatalf("RetireCursorMarkers(): %v", err)
	}

	if !markedIDs(t, store)[neighbor.ID] {
		t.Fatal("retiring rig/agent's markers also unmarked rig/other")
	}
	index, err := front.CursorIndex()
	if err != nil {
		t.Fatalf("CursorIndex(): %v", err)
	}
	want := map[string]EventCursor{"rig/agent": 7, "rig/other": 2}
	for name, seq := range want {
		if index[name] != seq {
			t.Errorf("CursorIndex()[%q] = %d, want %d", name, index[name], seq)
		}
	}
	if len(index) != len(want) {
		t.Errorf("CursorIndex() = %v, want exactly %v", index, want)
	}
}

// TestCursorIndexReportsAbsenceRatherThanZero proves an order with no marker is
// MISSING from the index rather than present at zero.
//
// The dispatcher branches on that distinction to decide whether to pay the exact
// per-order scan, and Go's zero value makes the two indistinguishable to a
// caller that only reads the value. Pinning it here keeps a future "return 0 for
// convenience" from turning every pre-upgrade city's first tick into a full
// event-backlog replay.
func TestCursorIndexReportsAbsenceRatherThanZero(t *testing.T) {
	store := beads.NewMemStore()
	front := NewStore(beads.OrdersStore{Store: store})

	// A pre-marker run: the cursor pair, no marker.
	if _, err := store.Create(beads.Bead{
		Title: trackingTitle("rig/legacy"),
		Labels: []string{
			labelOrderTracking,
			labelOrderRunPrefix + "rig/legacy",
			labelOrderTitlePrefix + "rig/legacy",
			fmt.Sprintf("%s%d", labelSeqPrefix, 4),
		},
	}); err != nil {
		t.Fatalf("seeding pre-marker run: %v", err)
	}

	index, err := front.CursorIndex()
	if err != nil {
		t.Fatalf("CursorIndex(): %v", err)
	}
	if _, ok := index["rig/legacy"]; ok {
		t.Fatalf("CursorIndex() reported rig/legacy = %d; an unmarked order must be ABSENT so the caller falls back to the exact scan", index["rig/legacy"])
	}
}

// TestCursorIndexReportsZeroSeqAsPresent proves an order marked at seq 0 is
// PRESENT at zero rather than absent.
//
// A first run against an empty event log stamps seq:0, and Go's zero value makes
// "marked at 0" and "not marked" identical to any reader that compares against
// it. Collapsing them is not a correctness fault -- the caller's fallback
// returns the same 0 -- which is exactly why it needs a test: the only symptom
// would be that order paying the full per-order scan on every tick for as long
// as the event log stays empty, with nothing anywhere reporting it.
func TestCursorIndexReportsZeroSeqAsPresent(t *testing.T) {
	store := beads.NewMemStore()
	front := NewStore(beads.OrdersStore{Store: store})
	seedMarkedRun(t, store, "rig/agent", 0)

	index, err := front.CursorIndex()
	if err != nil {
		t.Fatalf("CursorIndex(): %v", err)
	}
	got, ok := index["rig/agent"]
	if !ok {
		t.Fatal("an order marked at seq 0 is absent from the index; it will pay the per-order scan every tick")
	}
	if got != 0 {
		t.Fatalf("CursorIndex()[rig/agent] = %d, want 0", got)
	}
}

// listFailStore fails every List, standing in for a scope whose store is
// unreachable. It refuses rather than answering, so a read that should have
// surfaced the failure cannot get a plausible-looking empty result instead.
type listFailStore struct {
	beads.Store
}

func (listFailStore) List(beads.ListQuery) ([]beads.Bead, error) {
	return nil, fmt.Errorf("scope unreachable")
}

// TestCursorIndexSurfacesLegFailure proves a failed leg is reported, not folded
// into a smaller answer.
//
// The sibling LastRun/Cursor reads deliberately tolerate a partial scope failure
// -- surviving rows win. Copying that habit here would be a replay bug: this
// read folds to max(seq), so a dropped scope lowers the cursor, and a lowered
// cursor re-consumes events the order already acted on. The error is what routes
// the dispatcher to the exact per-order scan, so it has to reach it.
func TestCursorIndexSurfacesLegFailure(t *testing.T) {
	store := beads.NewMemStore()
	front := NewStoreWithGraph(beads.OrdersStore{Store: store}, beads.GraphStore{Store: listFailStore{Store: beads.NewMemStore()}})
	seedMarkedRun(t, store, "rig/agent", 11)

	index, err := front.CursorIndex()
	if err == nil {
		t.Fatalf("CursorIndex() = %v with nil error; a failed leg was folded into a partial answer that can only under-report the cursor", index)
	}
}
