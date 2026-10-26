// cmd/gc/order_dispatch_cursor_scan_test.go
//
// Pins the per-tick cost SHAPE of the order dispatcher's event-cursor read:
// the ring walk must not pay one full-history scan per event-triggered order,
// every tick, forever.
//
// This suite exists because the cost it pins is invisible to every other test
// in cmd/gc. A correctness suite over the cursor -- TestOrderDispatchEventExec
// AdvancesCursor and its siblings in order_dispatch_test.go -- goes green
// whether the dispatcher issues one cursor read or forty: the VERDICT is
// identical, only the row count and the call count differ. The defect it was
// written for (ci-jg1k70) was measured on the live controller, where four event
// orders each paid 1.4-2.5s of a ~7.4s orders.dispatch phase, and nothing in the
// repository would have gone red.
//
// The counted quantity is the read SHAPE, not wall time: a timing assertion
// here would pin the host's load rather than the dispatcher's query plan.
//
// Delegated elsewhere: the retirement rule and the index read semantics it
// depends on -- internal/orders/cursor_marker_test.go.
//
// Run: go test ./cmd/gc/ -run 'TestOrderDispatch(EventCursor|LiveCursor|WispPathRetires)'
package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

// cursorScanSpyStore counts the per-order event-cursor scans a dispatch tick
// issues. The distinguishing mark is the "order:<name>" label -- the per-order
// cursor label the dispatcher stamps on each run -- which no other dispatch
// read selects on. The shared live-cursor marker is a DIFFERENT label
// (labelOrderCursor, "order-cursor"), so it is deliberately not matched here:
// the point of the count is that the per-order scans disappear, not that all
// reads do.
//
// List is called from abandoned gate goroutines (gateOpenWorkBounded), so the
// counter is mutex-guarded rather than a bare int.
type cursorScanSpyStore struct {
	beads.Store
	mu    sync.Mutex
	scans map[string]int
}

func newCursorScanSpyStore(inner beads.Store) *cursorScanSpyStore {
	return &cursorScanSpyStore{Store: inner, scans: make(map[string]int)}
}

func (s *cursorScanSpyStore) List(q beads.ListQuery) ([]beads.Bead, error) {
	if name, ok := strings.CutPrefix(q.Label, "order:"); ok {
		s.mu.Lock()
		s.scans[name]++
		s.mu.Unlock()
	}
	return s.Store.List(q)
}

func (s *cursorScanSpyStore) totalScans() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for _, n := range s.scans {
		total += n
	}
	return total
}

func (s *cursorScanSpyStore) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scans = make(map[string]int)
}

// journalReadSpy counts event-journal reads. Every List against the
// file-backed provider rescans the whole active events.jsonl, so the count is
// the per-tick scan cost (ci-e4jp3a).
type journalReadSpy struct {
	events.Provider
	mu    sync.Mutex
	lists int
}

func (p *journalReadSpy) List(f events.Filter) ([]events.Event, error) {
	p.mu.Lock()
	p.lists++
	p.mu.Unlock()
	return p.Provider.List(f)
}

func (p *journalReadSpy) takeLists() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := p.lists
	p.lists = 0
	return n
}

// TestOrderDispatchEventCursorScanDoesNotScalePerOrder pins that a STEADY-STATE
// tick pays no per-order cursor scan and one journal read, however many event
// orders are in the ring, while each order still sees only the events after
// its own cursor.
//
// Two ticks, not one, and the assertion is on the second. The first tick is
// cold by design: no run carries the live-cursor marker yet, so every event
// order falls back to the exact per-order scan, which is what makes the fix
// self-healing on an upgrade instead of needing a backfill sweep. The steady
// state -- every tick after the first dispatch of each order -- is the state
// the controller spends its life in and the only one worth pinning.
//
// Four orders rather than one: a single-order case cannot tell "one read per
// order" from "one read per tick", which is the entire distinction.
//
// The spy counts the "order:<name>" label, NOT "order-cursor": the grouped read
// this fix introduces is itself a List, so a test that counted all cursor-ish
// reads would assert 1 and pass on either implementation.
//
// The third tick is the one a lazily-widened journal read fails: the ring
// reaches the bead.created orders, whose cursors are now the higher ones,
// before the bead.closed orders, so a read sized to the first cursor asked for
// must be repeated for the second. It also fails a shared read that forgets
// the per-order cursor, because bead.created seq 3 is then still in view and
// the created orders fire on it twice.
func TestOrderDispatchEventCursorScanDoesNotScalePerOrder(t *testing.T) {
	spy := newCursorScanSpyStore(beads.NewMemStore())
	eventLog := events.NewFake()
	journal := &journalReadSpy{Provider: eventLog}
	eventLog.Record(events.Event{Type: events.BeadCreated, Actor: "test"})
	eventLog.Record(events.Event{Type: events.BeadClosed, Actor: "test"})

	watch := map[string]string{
		"created-a": events.BeadCreated,
		"created-b": events.BeadCreated,
		"closed-a":  events.BeadClosed,
		"closed-b":  events.BeadClosed,
	}
	names := []string{"created-a", "created-b", "closed-a", "closed-b"}
	aa := make([]orders.Order, 0, len(names))
	for _, name := range names {
		aa = append(aa, orders.Order{
			Name:    name,
			Trigger: "event",
			On:      watch[name],
			Exec:    "true",
		})
	}

	execRun := func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("ok"), nil
	}
	ad := buildOrderDispatcherFromListExec(aa, spy, journal, execRun, events.Discard)
	if ad == nil {
		t.Fatal("buildOrderDispatcherFromListExec returned nil")
	}
	// A budget rotates the ring start to just past the last dispatch, which
	// is always a lower cursor. Unbudgeted, the walk follows the slice order.
	ad.(*memoryOrderDispatcher).maxDispatchesPerTick = 0
	cityPath := t.TempDir()
	runs := func() map[string]int {
		got := make(map[string]int, len(names))
		for _, name := range names {
			got[name] = len(trackingBeads(t, spy, "order:"+name))
		}
		return got
	}

	// Cold tick.
	ad.dispatch(context.Background(), cityPath, time.Now())
	ad.drain(context.Background())
	before := runs()
	for _, name := range names {
		if before[name] == 0 {
			t.Fatalf("cold tick left no cursor-bearing run for %s; the tick did not dispatch", name)
		}
	}

	steady := []struct {
		record string
		fires  string
	}{
		{record: events.BeadCreated, fires: events.BeadCreated},
		{record: events.BeadClosed, fires: events.BeadClosed},
	}
	for i, tick := range steady {
		eventLog.Record(events.Event{Type: tick.record, Actor: "test"})
		spy.reset()
		journal.takeLists()

		at := time.Now().Add(time.Duration(i+1) * 10 * time.Second)
		ad.dispatch(context.Background(), cityPath, at)
		ad.drain(context.Background())

		if got := spy.totalScans(); got != 0 {
			t.Fatalf("steady tick %d issued %d per-order cursor scans (%v), want 0: "+
				"the ring walk is paying one full-history scan per event order per tick",
				i+1, got, spy.scans)
		}
		if got := journal.takeLists(); got != 1 {
			t.Fatalf("steady tick %d read the event journal %d times, want 1: "+
				"each event order is rescanning events.jsonl on its own",
				i+1, got)
		}
		after := runs()
		for _, name := range names {
			want := 0
			if watch[name] == tick.fires {
				want = 1
			}
			if got := after[name] - before[name]; got != want {
				t.Errorf("steady tick %d: %s (on %s) dispatched %d times, want %d",
					i+1, name, watch[name], got, want)
			}
		}
		before = after
	}
}

// TestOrderTickEventsWidensForACursorBelowItsRead drives the branch a ring walk
// with a carried hint never reaches: no hint, the higher cursor asked first.
func TestOrderTickEventsWidensForACursorBelowItsRead(t *testing.T) {
	eventLog := events.NewFake()
	for _, typ := range []string{events.BeadClosed, events.BeadCreated, events.BeadClosed, events.BeadCreated, events.SessionWoke} {
		eventLog.Record(events.Event{Type: typ, Actor: "test"})
	}
	journal := &journalReadSpy{Provider: eventLog}
	m := &memoryOrderDispatcher{ep: journal, aa: []orders.Order{
		{Name: "hi", Trigger: "event", On: events.BeadCreated},
		{Name: "lo", Trigger: "event", On: events.BeadClosed},
	}}
	shared := m.newOrderTickEvents()

	seqs := func(f events.Filter) []uint64 {
		t.Helper()
		got, err := shared.List(f)
		if err != nil {
			t.Fatalf("List(%+v): %v", f, err)
		}
		var out []uint64
		for _, e := range got {
			out = append(out, e.Seq)
		}
		return out
	}
	if got := seqs(events.Filter{Type: events.BeadCreated, AfterSeq: 3}); !slices.Equal(got, []uint64{4}) {
		t.Fatalf("bead.created after 3 = %v, want [4]", got)
	}
	if got := seqs(events.Filter{Type: events.BeadClosed, AfterSeq: 0}); !slices.Equal(got, []uint64{1, 3}) {
		t.Fatalf("bead.closed after 0 = %v, want [1 3]: the read was not widened to the lower cursor", got)
	}
	if got := journal.takeLists(); got != 2 {
		t.Fatalf("journal reads = %d, want 2: one read, then one widening", got)
	}
	if got := seqs(events.Filter{Type: events.SessionWoke}); !slices.Equal(got, []uint64{5}) {
		t.Fatalf("unwatched session.woke = %v, want [5]: it must go to the provider, not the shared read", got)
	}
	m.keepEventFloorHint(shared)
	if !m.hasEventFloorHint || m.eventFloorHint != 0 {
		t.Fatalf("carried hint = (%d, %v), want (0, true): the lowest cursor asked for",
			m.eventFloorHint, m.hasEventFloorHint)
	}
}

// TestOrderDispatchLiveCursorMarkerStaysAtOneRowPerOrder pins the property that
// makes the grouped read cheap in the first place: the marked set does not grow
// with run count.
//
// Without retirement the grouped read still returns the right cursor -- every
// reader folds with max -- so no correctness test can see the difference. What
// it loses is the whole point: the marked set would grow one row per run exactly
// like order:<scoped> does, reaching the 9781 rows measured on nudge-on-route,
// and the read would be back to scanning the full history with one extra label
// in the way. This is therefore asserted on the STORE, by counting marked beads,
// not on any verdict.
//
// Three dispatches, not two, because only the third retires a bead that had
// ITSELF retired one. Retirement reading its own earlier writes is the property
// that keeps the set at one forever rather than for one round, and two
// dispatches never exercise it.
func TestOrderDispatchLiveCursorMarkerStaysAtOneRowPerOrder(t *testing.T) {
	store := beads.NewMemStore()
	eventLog := events.NewFake()
	execRun := func(context.Context, string, string, []string) ([]byte, error) {
		return []byte("ok"), nil
	}
	ad := buildOrderDispatcherFromListExec([]orders.Order{{
		Name:    "cursor-churn",
		Trigger: "event",
		On:      events.BeadClosed,
		Exec:    "true",
	}}, store, eventLog, execRun, events.Discard)
	if ad == nil {
		t.Fatal("buildOrderDispatcherFromListExec returned nil")
	}
	cityPath := t.TempDir()

	for i := range 3 {
		eventLog.Record(events.Event{Type: events.BeadClosed, Actor: "test"})
		ad.dispatch(context.Background(), cityPath, time.Now().Add(time.Duration(i)*10*time.Second))
		ad.drain(context.Background())
	}

	runs := trackingBeads(t, store, "order:cursor-churn")
	if len(runs) < 3 {
		t.Fatalf("cursor-bearing runs = %d, want at least 3; the ticks did not dispatch, so the marker count below proves nothing", len(runs))
	}
	marked := trackingBeads(t, store, "order-cursor")
	if len(marked) != 1 {
		t.Fatalf("beads carrying the live-cursor marker = %d across %d runs, want 1: "+
			"stale markers are not being retired, so the grouped read grows one row per run",
			len(marked), len(runs))
	}
	if got := orders.MaxSeqFromLabels([][]string{marked[0].Labels}); got != 3 {
		t.Fatalf("surviving marker carries seq %d, want 3 (the newest run)", got)
	}
}

// TestOrderDispatchEventCursorFallsBackToExactScanBeforeFirstMarker pins the
// upgrade story: a city whose event orders all ran BEFORE the marker label
// existed must read its real cursor, not a zero.
//
// This is the failure the fix could plausibly have shipped with. An order absent
// from the grouped index looks exactly like an order with cursor 0, and treating
// it as one would make every event order re-consume its entire event backlog on
// the first tick after the upgrade -- re-nudging every route, re-notifying every
// human gate. The seeded run here carries the OLD label shape (order:<name> +
// seq:<N>, no marker), which is precisely what a pre-upgrade store holds.
func TestOrderDispatchEventCursorFallsBackToExactScanBeforeFirstMarker(t *testing.T) {
	store := beads.NewMemStore()
	eventLog := events.NewFake()
	eventLog.Record(events.Event{Type: events.BeadClosed, Actor: "test"})
	headSeq, err := eventLog.LatestSeq()
	if err != nil {
		t.Fatalf("LatestSeq(): %v", err)
	}

	// A pre-upgrade run: the cursor pair with no live-cursor marker.
	//
	// It is CLOSED through the store rather than created with Status "closed",
	// because MemStore.Create does not honor the field. An open order-tracking
	// bead trips the single-flight gate ahead of the cursor read, which
	// suppresses the dispatch for a reason that has nothing to do with cursors --
	// and the assertion below (exec never ran) cannot tell the two apart. That is
	// how the first draft of this test passed under the mutation it was written
	// to catch.
	seeded, err := store.Create(beads.Bead{
		Title: "order:legacy-cursor",
		Labels: []string{
			"order-tracking",
			"order-run:legacy-cursor",
			"order:legacy-cursor",
			fmt.Sprintf("seq:%d", headSeq),
			"exec",
		},
	})
	if err != nil {
		t.Fatalf("seeding pre-upgrade run: %v", err)
	}
	if err := store.Close(seeded.ID); err != nil {
		t.Fatalf("closing seeded pre-upgrade run: %v", err)
	}
	if got, err := store.Get(seeded.ID); err != nil || got.Status != "closed" {
		t.Fatalf("seeded run status = %q (err %v), want closed; an open run would suppress the dispatch on its own", got.Status, err)
	}

	var calls int
	execRun := func(context.Context, string, string, []string) ([]byte, error) {
		calls++
		return []byte("ok"), nil
	}
	ad := buildOrderDispatcherFromListExec([]orders.Order{{
		Name:    "legacy-cursor",
		Trigger: "event",
		On:      events.BeadClosed,
		Exec:    "true",
	}}, store, eventLog, execRun, events.Discard)
	if ad == nil {
		t.Fatal("buildOrderDispatcherFromListExec returned nil")
	}

	ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())

	if calls != 0 {
		t.Fatalf("exec calls = %d, want 0: the unmarked pre-upgrade run was read as cursor 0, "+
			"so the order replayed an event it had already consumed", calls)
	}
}

// TestOrderDispatchWispPathRetiresLiveCursorMarkers covers the OTHER stamping
// shape.
//
// The exec writers all funnel through Store.SetCursor, which retires
// predecessors itself, so forgetting retirement there is impossible. The wisp
// path does not: it stamps the cursor inside the molecule root's combined
// label + routing-metadata Update, so it carries its OWN retirement call and is
// the one site a future edit can drop. Nothing else would notice -- the cursor
// stays correct, the marked set just grows one row per run until the grouped
// read costs what the per-order scan did.
//
// dispatchWisp is driven DIRECTLY rather than through dispatch(): a molecule
// root does not auto-close, so the wisp-aware open-work gate correctly
// suppresses every tick after the first and a loop over dispatch() yields one
// run, not three. Bypassing the gate is the point here -- what is under test is
// the stamp-and-retire pair, and the gate is pinned by its own suite.
func TestOrderDispatchWispPathRetiresLiveCursorMarkers(t *testing.T) {
	store := beads.NewMemStore()
	eventLog := events.NewFake()
	a := orders.Order{
		Name:         "wisp-cursor",
		Trigger:      "event",
		On:           events.BeadClosed,
		Formula:      "test-formula",
		FormulaLayer: sharedTestFormulaDir,
	}
	var rec memRecorder
	m := &memoryOrderDispatcher{cfg: &config.City{}, rec: &rec, ep: eventLog, stderr: io.Discard}
	cityPath := t.TempDir()

	for range 3 {
		eventLog.Record(events.Event{Type: events.BeadClosed, Actor: "test"})
		tracking, err := store.Create(beads.Bead{
			Title:  "order:wisp-cursor",
			Labels: []string{"order-run:wisp-cursor", labelOrderTracking},
		})
		if err != nil {
			t.Fatalf("seeding tracking bead: %v", err)
		}
		m.dispatchWisp(context.Background(), store, execStoreTarget{}, a, cityPath, tracking.ID, nil)
	}

	runs := trackingBeads(t, store, "order:wisp-cursor")
	if len(runs) < 3 {
		t.Fatalf("cursor-bearing beads = %d, want at least 3; the wisp dispatches did not stamp, so the marker count below proves nothing", len(runs))
	}
	marked := trackingBeads(t, store, "order-cursor")
	if len(marked) != 1 {
		t.Fatalf("beads carrying the live-cursor marker = %d across %d cursor-bearing beads, want 1: "+
			"the wisp stamp is not retiring its predecessors", len(marked), len(runs))
	}
	if got := orders.MaxSeqFromLabels([][]string{marked[0].Labels}); got != 3 {
		t.Fatalf("surviving marker carries seq %d, want 3 (the newest run)", got)
	}
}
