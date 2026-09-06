// cmd/gc/order_dispatch_failure_reason_test.go
//
// Pins the invariant that an order whose wisp dispatch fails BEFORE cooking
// leaves the reason in durable store state, not only on the event stream.
//
// The suite exists because of ci-pserre: governor-wake fired at 20:28:38.929Z
// against a formulas/governor-wake.toml that held git conflict markers in the
// working tree, cooked no wake bead, and left the city without scheduled
// oversight for 90 minutes. The toml parse error reached exactly one place --
// .gc/events.jsonl -- so a reader who noticed the missing bead could not find
// out why. The tracking bead carried the wisp-failed label and a generic
// close_reason and nothing else.
//
// The tests here therefore assert on the STORE, never on the recorder alone.
// Every pre-existing wisp-failure test in order_dispatch_test.go asserts only
// the wisp-failed label, which is why they went green over the defect for the
// whole life of the dispatcher.
//
// Run: go test ./cmd/gc/ -run OrderWispFailure
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

// conflictedFormulaBody is the shape ci-pserre actually hit: an add/add merge
// left conflict markers in a live formula file. The markers make the file
// unparseable at line 11, matching the recorded toml error. Written as a
// literal rather than produced by a git merge in the test so the fixture does
// not depend on git being present or on merge-driver configuration.
const conflictedFormulaBody = `formula = "governor-wake"
version = 1

[[steps]]
id = "wake"
title = "Wake the governor"
description = "Governor reviews the city."

[[steps]]
<<<<<<< HEAD
id = "review"
=======
id = "audit"
>>>>>>> origin/main
title = "Review"
description = "Review."
`

// TestOrderWispFailureReasonReachesTrackingBead pins that a pre-cook dispatch
// failure is recoverable from the tracking bead alone. It fires one real tick
// so the assertion covers the production path (prepareOrderWispRecipe ->
// markTrackingFailure -> the deferred tracking close), not a hand-called
// dispatchWisp: the close is what erased the reason's only other candidate
// home, so a test that skips it cannot see the defect.
func TestOrderWispFailureReasonReachesTrackingBead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "governor-wake.toml"), []byte(conflictedFormulaBody), 0o644); err != nil {
		t.Fatal(err)
	}
	store := beads.NewMemStore()
	var rec memRecorder

	ad := buildOrderDispatcherFromListExec([]orders.Order{{
		Name:         "governor-wake",
		Trigger:      "cooldown",
		Interval:     "15m",
		Formula:      "governor-wake",
		FormulaLayer: dir,
	}}, store, nil, successfulExec, &rec)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}

	ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())

	all := trackingBeads(t, store, "order-run:governor-wake")
	if len(all) != 1 {
		t.Fatalf("tracking beads = %d, want 1 (no wisp is cooked on a parse failure)", len(all))
	}
	tracking := all[0]
	if !slicesContain(tracking.Labels, "wisp-failed") {
		t.Fatalf("tracking bead labels = %v, want wisp-failed", tracking.Labels)
	}

	reason := tracking.Metadata[beadmeta.OrderDispatchFailureMetadataKey]
	if reason == "" {
		t.Fatalf("tracking bead %s carries no %s; the failure reason survives only on the event stream, which is the ci-pserre defect (metadata = %v)",
			tracking.ID, beadmeta.OrderDispatchFailureMetadataKey, tracking.Metadata)
	}

	// The durable reason must be the SAME string the event carried. Asserting
	// only that it is non-empty would pass over a placeholder, and asserting a
	// hand-written substring of the toml parser's message would pin the
	// parser's wording rather than the delivery this test exists to check.
	eventMsg := rec.messageOfType(events.OrderFailed)
	if eventMsg == "" {
		t.Fatal("no order.failed event recorded; the fixture did not reach the failure path")
	}
	if reason != eventMsg {
		t.Errorf("durable reason and event message diverged:\n durable = %q\n event   = %q", reason, eventMsg)
	}
	// Independent of the implementation's error text: whatever the parser
	// says, a reader must be able to tell WHICH file refused to parse.
	if !strings.Contains(reason, "governor-wake") {
		t.Errorf("reason %q does not name the formula that failed", reason)
	}
}

// TestOrderWispFailureReasonDecodesOntoOrderRun pins the reason onto the typed
// read path, so `gc order history` and the orders API can surface it without
// each growing its own metadata crack. Separate from the write test on
// purpose: a value written by the dispatcher and read by no decoder is
// invisible to a suite that tests each end alone.
func TestOrderWispFailureReasonDecodesOntoOrderRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "governor-wake.toml"), []byte(conflictedFormulaBody), 0o644); err != nil {
		t.Fatal(err)
	}
	store := beads.NewMemStore()

	ad := buildOrderDispatcherFromListExec([]orders.Order{{
		Name:         "governor-wake",
		Trigger:      "cooldown",
		Interval:     "15m",
		Formula:      "governor-wake",
		FormulaLayer: dir,
	}}, store, nil, successfulExec, nil)
	if ad == nil {
		t.Fatal("expected non-nil dispatcher")
	}
	ad.dispatch(context.Background(), t.TempDir(), time.Now())
	ad.drain(context.Background())

	runs, err := orders.NewStore(beads.OrdersStore{Store: store}).RecentRuns("governor-wake", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("recent runs = %d, want 1", len(runs))
	}
	if runs[0].Outcome.Display() != "failed" {
		t.Fatalf("run outcome = %q, want failed", runs[0].Outcome.Display())
	}
	if runs[0].DispatchFailure == "" {
		t.Fatalf("OrderRun.DispatchFailure is empty for a failed wisp run %s; the reason is unreachable through the typed read", runs[0].ID)
	}
}
