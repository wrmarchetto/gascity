package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/orders"
)

// orderHistoryStatusOrder is the order the status fixtures below run under.
const orderHistoryStatusOrder = "digest"

// orderHistoryStatusFixture seeds one tracking bead per run state the history
// table has to tell apart -- a failed wisp dispatch carrying its reason, a
// clean completed run, and an open in-flight one -- newest first, one hour
// apart. It goes through the bd-backed store rather than MemStore because
// MemStore.Create stamps its own CreatedAt and these rows are ordered by it.
//
// The dispatch reason is deliberately only on the failed row: a renderer that
// prints the reason unconditionally, or reads it off the wrong row, shows up
// as an extra line rather than as a matching table.
func orderHistoryStatusFixture(t *testing.T, now time.Time, scoped string) beads.Store {
	t.Helper()
	rows := []string{
		fmt.Sprintf(
			`{"id":"WP-failed","title":"order:%s","status":"closed","issue_type":"task","created_at":%q,`+
				`"labels":["order-run:%s","order-tracking","wisp","wisp-failed"],"metadata":{%q:%q}}`,
			scoped, now.Format(time.RFC3339Nano), scoped,
			beadmeta.OrderDispatchFailureMetadataKey, "formula \"mol-digest\" not found",
		),
		fmt.Sprintf(
			`{"id":"WP-done","title":"order:%s","status":"closed","issue_type":"task","created_at":%q,`+
				`"labels":["order-run:%s","order-tracking","wisp"]}`,
			scoped, now.Add(-time.Hour).Format(time.RFC3339Nano), scoped,
		),
		fmt.Sprintf(
			`{"id":"WP-open","title":"order:%s","status":"open","issue_type":"task","created_at":%q,`+
				`"labels":["order-run:%s","order-tracking","wisp"]}`,
			scoped, now.Add(-2*time.Hour).Format(time.RFC3339Nano), scoped,
		),
	}
	payload := []byte("[" + strings.Join(rows, ",") + "]")
	return beads.NewBdStore(t.TempDir(), func(_, _ string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "--label=order-run:"+scoped) {
			return payload, nil
		}
		return []byte(`[]`), nil
	})
}

// orderHistoryStatusRuns reads the fixture back through the same front door
// doOrderHistoryBounded uses, so every expectation below is derived from the
// OrderRun the store actually returns. Spelling the states as literals here
// would pin the fixture's labels rather than State()'s truth table, and would
// keep passing if the renderer stopped consulting State() at all.
func orderHistoryStatusRuns(t *testing.T, store beads.Store, scoped string) []orders.OrderRun {
	t.Helper()
	runs, err := orders.NewStore(beads.OrdersStore{Store: store}).RecentRuns(scoped, 0)
	if err != nil {
		t.Fatalf("RecentRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("fixture returned %d runs, want 3", len(runs))
	}
	return runs
}

// TestOrderHistoryJSONCarriesRunState pins the machine-readable half: every
// history entry reports the run's lifecycle state, so a failed run is
// distinguishable from a successful one without counting rows by hand
// (ci-7gg9ra). The failed row also carries the dispatch reason ci-pserre
// stored, since a verdict nobody can explain is what that bead was about.
func TestOrderHistoryJSONCarriesRunState(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	store := orderHistoryStatusFixture(t, now, orderHistoryStatusOrder)
	runs := orderHistoryStatusRuns(t, store, orderHistoryStatusOrder)
	aa := []orders.Order{{Name: orderHistoryStatusOrder, Formula: "mol-digest"}}
	resolver := func(orders.Order) ([]beads.OrdersStore, error) {
		return []beads.OrdersStore{{Store: store}}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := doOrderHistoryBounded(orderHistoryStatusOrder, "", aa, resolver, orderHistoryBounds{}, true, &stdout, &stderr); code != 0 {
		t.Fatalf("doOrderHistoryBounded = %d, want 0; stderr: %s", code, stderr.String())
	}

	var payload orderHistoryJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if len(payload.Entries) != len(runs) {
		t.Fatalf("entries = %d, want %d", len(payload.Entries), len(runs))
	}
	for i, run := range runs {
		got := payload.Entries[i]
		if got.BeadID != run.ID {
			t.Fatalf("entry %d bead_id = %q, want %q", i, got.BeadID, run.ID)
		}
		if got.Status != run.State() {
			t.Fatalf("entry %d (%s) status = %q, want %q", i, run.ID, got.Status, run.State())
		}
		if got.DispatchFailure != run.DispatchFailure {
			t.Fatalf("entry %d (%s) dispatch_failure = %q, want %q", i, run.ID, got.DispatchFailure, run.DispatchFailure)
		}
	}
	// The three fixture rows must not collapse to one state; a renderer that
	// hardcoded any single value would otherwise satisfy the loop above.
	states := map[string]bool{}
	for _, e := range payload.Entries {
		states[e.Status] = true
	}
	if len(states) != 3 {
		t.Fatalf("entries reported %d distinct states %v, want 3", len(states), states)
	}
}

// TestOrderHistoryTableShowsStateAndDispatchReason pins the human half -- the
// surface the operator who found governor-wake cooking no wake bead actually
// read. A STATUS column with no reason underneath still leaves them guessing
// why the run failed, so both are asserted.
func TestOrderHistoryTableShowsStateAndDispatchReason(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	store := orderHistoryStatusFixture(t, now, orderHistoryStatusOrder)
	runs := orderHistoryStatusRuns(t, store, orderHistoryStatusOrder)
	aa := []orders.Order{{Name: orderHistoryStatusOrder, Formula: "mol-digest"}}
	resolver := func(orders.Order) ([]beads.OrdersStore, error) {
		return []beads.OrdersStore{{Store: store}}, nil
	}

	var stdout, stderr bytes.Buffer
	if code := doOrderHistoryBounded(orderHistoryStatusOrder, "", aa, resolver, orderHistoryBounds{}, false, &stdout, &stderr); code != 0 {
		t.Fatalf("doOrderHistoryBounded = %d, want 0; stderr: %s", code, stderr.String())
	}
	out := stdout.String()

	if !strings.Contains(out, "STATUS") {
		t.Fatalf("table has no STATUS header:\n%s", out)
	}
	for _, run := range runs {
		line := orderHistoryLineFor(t, out, run.ID)
		if !strings.Contains(line, run.State()) {
			t.Fatalf("row for %s = %q, want it to report state %q", run.ID, line, run.State())
		}
	}
	failed := runs[0]
	if failed.DispatchFailure == "" {
		t.Fatalf("fixture run %s carries no dispatch failure; the reason assertion below would be vacuous", failed.ID)
	}
	if !strings.Contains(out, failed.DispatchFailure) {
		t.Fatalf("table omits the dispatch reason %q:\n%s", failed.DispatchFailure, out)
	}
	// The reason belongs to one run, so it must appear once. Printing it on
	// every row would attribute a failure to runs that succeeded.
	if n := strings.Count(out, failed.DispatchFailure); n != 1 {
		t.Fatalf("dispatch reason appears %d times, want 1:\n%s", n, out)
	}
}

// orderHistoryLineFor returns the single output line naming beadID.
func orderHistoryLineFor(t *testing.T, out, beadID string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, beadID) {
			return line
		}
	}
	t.Fatalf("no output line names %q:\n%s", beadID, out)
	return ""
}

// TestOrderHistoryRenderersAgree is the cross-renderer guard this bead was
// split out for. `gc order history` has two independent renderers --
// doOrderHistoryBounded over the local store and renderOrderHistoryFromAPI
// over the supervisor -- and an assertion that one of them prints "failed"
// passes over the other printing nothing. Driving both from the same OrderRun
// set and requiring byte-identical tables is what closes that hole: the
// operator sees the same history whether or not the controller is up.
//
// Both column layouts run, because the rig and no-rig tables are separate
// branches in each renderer and the STATUS column had to be added to all four.
func TestOrderHistoryRenderersAgree(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	for _, rig := range []string{"", "dart"} {
		t.Run("rig="+rig, func(t *testing.T) {
			scoped := orderHistoryStatusOrder
			if rig != "" {
				scoped = orderHistoryStatusOrder + ":rig:" + rig
			}
			store := orderHistoryStatusFixture(t, now, scoped)
			runs := orderHistoryStatusRuns(t, store, scoped)
			aa := []orders.Order{{Name: orderHistoryStatusOrder, Formula: "mol-digest", Rig: rig}}
			resolver := func(orders.Order) ([]beads.OrdersStore, error) {
				return []beads.OrdersStore{{Store: store}}, nil
			}

			var localOut, localErr bytes.Buffer
			if code := doOrderHistoryBounded(orderHistoryStatusOrder, "", aa, resolver, orderHistoryBounds{}, false, &localOut, &localErr); code != 0 {
				t.Fatalf("doOrderHistoryBounded = %d, want 0; stderr: %s", code, localErr.String())
			}
			if !strings.Contains(localOut.String(), "STATUS") {
				t.Fatalf("local table rendered no rows for rig %q:\n%s", rig, localOut.String())
			}

			views := make([]api.OrderHistoryView, 0, len(runs))
			for _, run := range runs {
				views = append(views, api.OrderHistoryView{
					BeadID:          run.ID,
					Name:            orderHistoryStatusOrder,
					ScopedName:      run.Scoped,
					Rig:             rig,
					CreatedAt:       run.CreatedAt.Format(time.RFC3339),
					Status:          run.State(),
					DispatchFailure: run.DispatchFailure,
				})
			}

			var apiOut, apiErr bytes.Buffer
			cr := api.CachedRead[[]api.OrderHistoryView]{Body: views}
			if code := renderOrderHistoryFromAPI(cr, orderHistoryStatusOrder, rig, orderHistoryBounds{}, false, &apiOut, &apiErr); code != 0 {
				t.Fatalf("renderOrderHistoryFromAPI = %d, want 0; stderr: %s", code, apiErr.String())
			}

			if localOut.String() != apiOut.String() {
				t.Fatalf("renderers disagree.\nlocal:\n%s\napi:\n%s", localOut.String(), apiOut.String())
			}
		})
	}
}

// TestOrderHistoryAPIJSONCarriesRunState pins the API renderer's
// machine-readable output independently of the human table, since --json and
// the table are separate code paths in the same function.
func TestOrderHistoryAPIJSONCarriesRunState(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	store := orderHistoryStatusFixture(t, now, orderHistoryStatusOrder)
	runs := orderHistoryStatusRuns(t, store, orderHistoryStatusOrder)

	views := make([]api.OrderHistoryView, 0, len(runs))
	for _, run := range runs {
		views = append(views, api.OrderHistoryView{
			BeadID:          run.ID,
			Name:            orderHistoryStatusOrder,
			ScopedName:      run.Scoped,
			CreatedAt:       run.CreatedAt.Format(time.RFC3339),
			Status:          run.State(),
			DispatchFailure: run.DispatchFailure,
		})
	}

	var stdout, stderr bytes.Buffer
	cr := api.CachedRead[[]api.OrderHistoryView]{Body: views}
	if code := renderOrderHistoryFromAPI(cr, orderHistoryStatusOrder, "", orderHistoryBounds{}, true, &stdout, &stderr); code != 0 {
		t.Fatalf("renderOrderHistoryFromAPI = %d, want 0; stderr: %s", code, stderr.String())
	}
	var payload orderHistoryJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if len(payload.Entries) != len(runs) {
		t.Fatalf("entries = %d, want %d", len(payload.Entries), len(runs))
	}
	for i, run := range runs {
		if got := payload.Entries[i].Status; got != run.State() {
			t.Fatalf("entry %d status = %q, want %q", i, got, run.State())
		}
		if got := payload.Entries[i].DispatchFailure; got != run.DispatchFailure {
			t.Fatalf("entry %d dispatch_failure = %q, want %q", i, got, run.DispatchFailure)
		}
	}
}
