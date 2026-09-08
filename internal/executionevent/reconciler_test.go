package executionevent

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// countingGraphStore counts the calls the reconcile pass makes so a test can
// assert what was NOT done. The whole point of CompletedReconciler is the work
// it skips, and a pass that skipped nothing is indistinguishable from one that
// did by looking at emitted counts alone.
//
// It embeds beads.Store rather than reimplementing it: a hand-written stand-in
// that answered every method would hand a pass to whatever the reconcile path
// starts calling next.
type countingGraphStore struct {
	beads.Store
	metadataScans int
	gets          int
	scanFilters   []map[string]string
}

func (s *countingGraphStore) ListByMetadata(filters map[string]string, limit int, opts ...beads.QueryOpt) ([]beads.Bead, error) {
	s.metadataScans++
	copied := make(map[string]string, len(filters))
	for key, value := range filters {
		copied[key] = value
	}
	s.scanFilters = append(s.scanFilters, copied)
	return s.Store.ListByMetadata(filters, limit, opts...)
}

func (s *countingGraphStore) Get(id string) (beads.Bead, error) {
	s.gets++
	return s.Store.Get(id)
}

// settledFixture builds one closed graph.v2 root whose single step is closed
// and already carries its completion fact, which is the shape ci-sbdsjh
// measured on every one of the live city's 58 roots.
func settledFixture(t *testing.T) (*countingGraphStore, *events.Fake, beads.Bead) {
	t.Helper()
	graph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, graph, "")
	step := mustCreateProjectionStep(t, graph, "gcg-attempt", root.ID, "build", `["prepare"]`)
	closed := "closed"
	if err := graph.Update(step.ID, beads.UpdateOpts{
		Status:   &closed,
		Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := graph.Update(root.ID, beads.UpdateOpts{Status: &closed}); err != nil {
		t.Fatal(err)
	}
	counting := &countingGraphStore{Store: graph}
	return counting, events.NewFake(), root
}

func TestReconcilerSkipsASettledRootOnTheSecondPass(t *testing.T) {
	// The invariant: a closed graph.v2 root whose steps are all closed and
	// whose facts are all present is walked ONCE, not once per patrol tick.
	//
	// Asserted on the scan COUNT rather than on the emitted count, because
	// emitted is already 0 on the second pass today -- that is exactly the
	// defect: 33 of the phase's 35 seconds are spent proving there is nothing
	// to do. Measured on the live city 2026-09-08: 58 roots, 58 per-root
	// scans at 576ms mean, 0 facts emitted.
	graph, recorder, _ := settledFixture(t)
	reconciler := NewCompletedReconciler()
	stores := []beads.GraphStore{{Store: graph}}

	if got := reconciler.Reconcile(recorder, stores, "execution-reconcile"); got != 1 {
		t.Fatalf("first pass emitted %d, want the one stranded fact repaired", got)
	}
	firstScans := graph.metadataScans

	// The pass that emitted must NOT mark the root settled -- see
	// TestReconcilerDoesNotSettleARootItJustEmittedFor. So the second pass
	// still walks, finds the fact present, and settles it.
	if got := reconciler.Reconcile(recorder, stores, "execution-reconcile"); got != 0 {
		t.Fatalf("second pass emitted %d, want 0", got)
	}
	secondScans := graph.metadataScans
	if secondScans <= firstScans {
		t.Fatalf("second pass made %d scans total, want more than the first pass's %d: "+
			"a pass that emitted must re-walk before settling", secondScans, firstScans)
	}

	if got := reconciler.Reconcile(recorder, stores, "execution-reconcile"); got != 0 {
		t.Fatalf("third pass emitted %d, want 0", got)
	}
	// One scan for the root list, and none for the settled root's steps.
	if walked := graph.metadataScans - secondScans; walked != 1 {
		t.Fatalf("third pass made %d metadata scans, want exactly 1 (the root "+
			"list): the settled root must not be re-walked", walked)
	}
	if got, want := graph.scanFilters[len(graph.scanFilters)-1][beadmeta.KindMetadataKey], beadmeta.KindWorkflow; got != want {
		t.Fatalf("third pass's only scan filtered on %q=%q, want the root list", beadmeta.KindMetadataKey, got)
	}
}

func TestReconcilerDoesNotSettleARootItJustEmittedFor(t *testing.T) {
	// events.Provider.Record is best-effort on the optional tier, so an append
	// can be lost with no error. Today a lost append is retried on the next
	// tick. Settling a root in the same pass that emitted for it would convert
	// that retry into a permanent hole until the controller restarts, so the
	// pass that emits deliberately leaves the root unsettled and the NEXT pass
	// -- which sees the fact present -- is the one that settles it.
	graph, recorder, _ := settledFixture(t)
	reconciler := NewCompletedReconciler()
	stores := []beads.GraphStore{{Store: graph}}

	if got := reconciler.Reconcile(recorder, stores, "execution-reconcile"); got != 1 {
		t.Fatalf("first pass emitted %d, want 1", got)
	}
	before := graph.metadataScans
	reconciler.Reconcile(recorder, stores, "execution-reconcile")
	if walked := graph.metadataScans - before; walked < 2 {
		t.Fatalf("pass after an emission made %d scans, want the root list plus "+
			"a re-walk of the root it emitted for", walked)
	}
}

func TestReconcilerReWalksARootWhoseStepIsStillOpen(t *testing.T) {
	// A root cannot be settled while any of its steps could still close: the
	// close is what produces the fact this pass exists to repair. The trap
	// this pins is settling on "root is closed" alone, which is true here.
	graph := beads.NewMemStore()
	root := mustCreateProjectionRoot(t, graph, "")
	openStep := mustCreateProjectionStep(t, graph, "gcg-open-attempt", root.ID, "build", `["prepare"]`)
	closed := "closed"
	if err := graph.Update(root.ID, beads.UpdateOpts{Status: &closed}); err != nil {
		t.Fatal(err)
	}
	counting := &countingGraphStore{Store: graph}
	recorder := events.NewFake()
	reconciler := NewCompletedReconciler()
	stores := []beads.GraphStore{{Store: counting}}

	reconciler.Reconcile(recorder, stores, "execution-reconcile")
	before := counting.metadataScans
	reconciler.Reconcile(recorder, stores, "execution-reconcile")
	if walked := counting.metadataScans - before; walked < 2 {
		t.Fatalf("pass over a root with an open step made %d scans, want a re-walk", walked)
	}

	// And when that step finally closes, the fact is still repaired -- the
	// skip must not have swallowed it.
	if err := graph.Update(openStep.ID, beads.UpdateOpts{
		Status:   &closed,
		Metadata: map[string]string{beadmeta.SessionIDMetadataKey: "gcs-session"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := reconciler.Reconcile(recorder, stores, "execution-reconcile"); got != 1 {
		t.Fatalf("pass after the step closed emitted %d, want the repaired fact", got)
	}
}

func TestReconcilerReWalksASettledRootThatReopens(t *testing.T) {
	// Settled is a claim about a CLOSED root. A reopened root can close a
	// step again, so the skip is gated on the root's status at skip time and
	// not on the memo alone.
	graph, recorder, root := settledFixture(t)
	reconciler := NewCompletedReconciler()
	stores := []beads.GraphStore{{Store: graph}}
	reconciler.Reconcile(recorder, stores, "execution-reconcile")
	reconciler.Reconcile(recorder, stores, "execution-reconcile")
	before := graph.metadataScans

	open := "open"
	inner, ok := graph.Store.(*beads.MemStore)
	if !ok {
		t.Fatalf("fixture store is %T, want *beads.MemStore", graph.Store)
	}
	if err := inner.Update(root.ID, beads.UpdateOpts{Status: &open}); err != nil {
		t.Fatal(err)
	}
	reconciler.Reconcile(recorder, stores, "execution-reconcile")
	if walked := graph.metadataScans - before; walked < 2 {
		t.Fatalf("pass over a reopened root made %d scans, want a re-walk", walked)
	}
}

func TestReconcilerDropsTheRedundantPerStepGet(t *testing.T) {
	// currentSteps already fetched every step row and threw the bodies away,
	// and the reconcile loop then re-read each one with Get purely to read
	// Status. Verified equivalent on the live store 2026-09-08: 261 step rows,
	// 0 status mismatches, 0 projected-event mismatches between the list row
	// and the Get row (TestListRowEqualsGetRowForEveryGraphV2Step, build tag
	// `measure`).
	//
	// Asserted as ZERO gets rather than "fewer": one remaining Get per step is
	// the entire cost this removes, so a bound that still allows one is not a
	// witness for anything.
	graph, recorder, _ := settledFixture(t)
	reconciler := NewCompletedReconciler()
	stores := []beads.GraphStore{{Store: graph}}
	if got := reconciler.Reconcile(recorder, stores, "execution-reconcile"); got != 1 {
		t.Fatalf("first pass emitted %d, want 1", got)
	}
	if graph.gets != 0 {
		t.Fatalf("pass made %d per-step Gets, want 0: the row from "+
			"ListByMetadata is what the projection consumes", graph.gets)
	}
}

func TestReconcileCompletedStoresStaysStatelessAcrossCalls(t *testing.T) {
	// The package-level entry point keeps its old contract: each call is an
	// independent pass. A memo hidden in package state would make the second
	// call's 0 mean "skipped" instead of "nothing to repair", and every
	// existing caller and test reads that 0 as the latter.
	graph, recorder, _ := settledFixture(t)
	stores := []beads.GraphStore{{Store: graph}}
	if got := ReconcileCompletedStores(recorder, stores, "execution-reconcile"); got != 1 {
		t.Fatalf("first ReconcileCompletedStores = %d, want 1", got)
	}
	before := graph.metadataScans
	if got := ReconcileCompletedStores(recorder, stores, "execution-reconcile"); got != 0 {
		t.Fatalf("second ReconcileCompletedStores = %d, want 0", got)
	}
	if walked := graph.metadataScans - before; walked < 2 {
		t.Fatalf("second stateless call made %d scans, want a full walk: "+
			"it must not inherit another call's memo", walked)
	}
}
