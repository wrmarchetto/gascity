// internal/executionevent/reconciler.go
//
// The completion-fact reconcile pass, and the memo that stops it re-proving
// the same thing every patrol tick.

package executionevent

import (
	"sort"
	"strings"
	"sync"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// CompletedReconciler runs the completion-fact repair pass and remembers which
// roots are settled, so a long-lived controller pays the full walk once rather
// than once per patrol tick. It is safe for concurrent use.
//
// Hold one per controller. A fresh reconciler has an empty memo and behaves
// exactly like the stateless [ReconcileCompletedStores].
//
// WHY THE MEMO EXISTS. The pass repairs a completion fact stranded between a
// durable step close and the best-effort event append. Its cost is one
// unbounded metadata scan for the workflow roots plus ANOTHER unbounded scan
// per root for that root's steps -- an N+1 over the whole issue table.
// Measured on the live city 2026-09-08, in this package against the store the
// controller uses (`go test -tags measure`): 58 graph.v2 roots, all closed,
// 261 steps, all closed, 387 completion facts already in the journal. The
// per-root scans cost 33.4s of a 35.0s pass at 576ms each, the root scan
// 0.56s, the per-step Gets 1.05s, and the journal read 3.67s. The pass emitted
// nothing. That 35s phase is 94% of a 42.3s self-clocked patrol tick, and the
// tick period -- not the dispatch cap -- is what held city:order-capacity red
// at 6.2/min supplied against 12.23/min demanded (ci-dyndhr, ci-sbdsjh).
//
// A closed graph.v2 root whose steps are all closed and whose facts are all
// present can never produce another fact: the step set of a closed root is
// fixed, and a closed step's close has already happened. Walking it again is
// the definition of work that cannot change an outcome, so this remembers that
// it was walked.
//
// WHAT THE MEMO DELIBERATELY DOES NOT DO, and the exposure that leaves:
//
//   - It is NOT persisted. A controller restart re-walks everything, paying
//     one expensive pass and then settling again. That is the safety net
//     rather than an oversight: it bounds every risk below to "repaired at
//     the next restart" instead of "never". A bead-metadata marker would
//     survive restarts and was rejected for that reason -- it trades the net
//     for ledger writes on closed rows.
//   - A root is NOT settled by the pass that emitted for it.
//     events.Provider.Record is best-effort on the optional tier and can lose
//     an append with no error; today a lost append is retried next tick.
//     Settling in the emitting pass would convert that retry into a hole, so
//     the NEXT pass -- which sees the fact present -- is the one that settles.
//   - A step that LifecycleEvent currently REJECTS (a control kind, or a
//     missing session id) does not block settling. Requiring every step to
//     project would settle almost nothing: the live city's roots each carry a
//     spec sidecar with no session id. The exposure is a closed step of a
//     closed root gaining a session id later, which is repaired at the next
//     restart and is not a shape anything in this city writes.
//   - It is NOT time-based. There is no "re-walk every N passes" cadence,
//     because a threshold picked here would be a judgment in Go with no
//     measurement behind it.
//
// Verified by internal/executionevent/reconciler_test.go. The live-store
// measurements and the list-row/Get-row equivalence check behind dropping the
// per-step Get are in reconcile_cost_measure_test.go, build tag `measure`.
type CompletedReconciler struct {
	mu      sync.Mutex
	settled map[string]struct{}
}

// NewCompletedReconciler returns a reconciler with an empty memo.
func NewCompletedReconciler() *CompletedReconciler {
	return &CompletedReconciler{settled: make(map[string]struct{})}
}

// settledKey identifies a root across the stores one pass scans.
//
// Composed from the row's own gc.root_store_ref plus its id rather than from
// the store handle's identity: handles are rebuilt every pass and reordered
// when the rig set changes, so a pointer- or index-keyed memo would silently
// miss after a reload. A row with no store ref keys on its id alone, which is
// still deterministic.
func settledKey(root beads.Bead) string {
	return root.Metadata[beadmeta.RootStoreRefMetadataKey] + "\x00" + root.ID
}

func (r *CompletedReconciler) isSettled(root beads.Bead) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.settled[settledKey(root)]
	return ok
}

func (r *CompletedReconciler) markSettled(root beads.Bead) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settled == nil {
		r.settled = make(map[string]struct{})
	}
	r.settled[settledKey(root)] = struct{}{}
}

func isClosedStatus(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "closed")
}

// Reconcile repairs completion facts across graphStores with one journal read,
// skipping roots this reconciler has already settled. It returns the number of
// facts emitted.
//
// The completed-fact index is updated after each append so the pass stays
// idempotent even when more than one source is scanned.
func (r *CompletedReconciler) Reconcile(recorder events.Provider, graphStores []beads.GraphStore, actor string) int {
	if recorder == nil {
		return 0
	}
	hasStore := false
	for _, graphStore := range graphStores {
		if graphStore.Store != nil {
			hasStore = true
			break
		}
	}
	if !hasStore {
		return 0
	}

	existing, err := completedFacts(recorder)
	if err != nil {
		// If the journal cannot be read, avoid generating duplicate recovery
		// facts. A later reconciliation pass can safely retry.
		return 0
	}
	completed := make(map[completedFactKey]struct{}, len(existing))
	for _, event := range existing {
		if event.Type == events.ExecutionStepCompleted {
			completed[completedFactKeyFor(event)] = struct{}{}
		}
	}

	emitted := 0
	for _, graphStore := range graphStores {
		if graphStore.Store == nil {
			continue
		}
		roots, err := graphStore.ListByMetadata(
			map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
			0,
			beads.IncludeClosed,
			beads.WithBothTiers,
		)
		if err != nil {
			continue
		}
		sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
		for _, root := range roots {
			if root.Metadata[beadmeta.FormulaContractMetadataKey] != beadmeta.FormulaContractGraphV2 {
				continue
			}
			rootClosed := isClosedStatus(root.Status)
			if rootClosed && r.isSettled(root) {
				continue
			}
			steps, err := currentStepRows(graphStore, root.ID)
			if err != nil {
				continue
			}
			emittedHere, allStepsClosed := 0, true
			for _, step := range steps {
				// The row ListByMetadata already returned, not a second Get of
				// the same row. Equivalence checked against the live bd-contract
				// store: 261 rows, 0 status mismatches, 0 projected-event
				// mismatches (reconcile_cost_measure_test.go, tag `measure`).
				if !isClosedStatus(step.row.Status) {
					allStepsClosed = false
					continue
				}
				event, ok := LifecycleEvent(events.ExecutionStepCompleted, root, step.row, actor)
				if !ok {
					continue
				}
				key := completedFactKeyFor(event)
				if _, exists := completed[key]; exists {
					continue
				}
				recorder.Record(event)
				completed[key] = struct{}{}
				emitted++
				emittedHere++
			}
			if rootClosed && allStepsClosed && emittedHere == 0 {
				r.markSettled(root)
			}
		}
	}
	return emitted
}
