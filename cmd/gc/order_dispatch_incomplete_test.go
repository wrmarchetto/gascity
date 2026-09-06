package main

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/orders"
)

// exitErrorWithCode returns a genuine *exec.ExitError carrying code.
//
// It runs a real process rather than fabricating an os.ProcessState because
// the classification under test reads the exit status THROUGH the error the
// runner returns, and only a real one has the wrapping and the ExitCode()
// behavior the production path meets. A hand-built stand-in that answered
// ExitCode() directly would pass whatever the classifier did with the error
// chain, which is exactly the part that can be wrong.
func exitErrorWithCode(t *testing.T, code int) *exec.ExitError {
	t.Helper()
	err := exec.Command("sh", "-c", "exit "+itoaForTest(code)).Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("sh -c 'exit %d' returned %v, want *exec.ExitError", code, err)
	}
	if exitErr.ExitCode() != code {
		t.Fatalf("ExitCode() = %d, want %d", exitErr.ExitCode(), code)
	}
	return exitErr
}

func itoaForTest(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

// seedExecTracking creates the tracking bead dispatchExec labels its outcome
// onto, for the one order name every test in this file uses.
// incompleteTestOrder is the order name every test here dispatches, so the
// tracking-bead label the assertions query is fixed.
const incompleteTestOrder = "sweep"

func seedExecTracking(t *testing.T, store beads.Store) string {
	t.Helper()
	tracking, err := store.Create(beads.Bead{
		Title:  "order:" + incompleteTestOrder,
		Labels: []string{"order-run:" + incompleteTestOrder, labelOrderTracking},
	})
	if err != nil {
		t.Fatalf("seed tracking bead: %v", err)
	}
	return tracking.ID
}

// TestOrderDispatchExecDeclaredIncompleteExitIsNotAFailure pins the fix for
// ci-iv9asy: an exec order whose command exits with a status the order declared
// in incomplete_exit_codes ran to completion with work outstanding, and must
// record a completed run rather than a failed one.
//
// The false alarm this prevents is not cosmetic. The order-firing doctor check
// escalates three consecutive failed runs, so a recurring sweep that reports
// its unfinished queue through a nonzero exit was permanently red while it was
// merging branches -- which is precisely the shape that trains an operator to
// ignore a red check.
func TestOrderDispatchExecDeclaredIncompleteExitIsNotAFailure(t *testing.T) {
	store := beads.NewMemStore()
	var rec memRecorder
	trackingID := seedExecTracking(t, store)

	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("blocked=1 merged=2\n"), exitErrorWithCode(t, 3)
	}
	aa := []orders.Order{{
		Name:                "sweep",
		Trigger:             "cooldown",
		Interval:            "30m",
		Exec:                "scripts/sweep.sh",
		IncompleteExitCodes: []int{3},
	}}
	mad := buildOrderDispatcherFromListExec(aa, store, nil, fakeExec, &rec).(*memoryOrderDispatcher)
	mad.dispatchExec(context.Background(), orders.NewStore(beads.OrdersStore{Store: store}), execStoreTarget{ScopeRoot: t.TempDir()}, aa[0], t.TempDir(), trackingID, nil)

	if rec.hasType(events.OrderFailed) || !rec.hasType(events.OrderCompleted) {
		t.Fatalf("events = %+v, want completed without failed", rec.events)
	}
	all := trackingBeads(t, store, "order-run:sweep")
	if len(all) != 1 {
		t.Fatalf("tracking beads = %d, want 1", len(all))
	}
	if !slicesContain(all[0].Labels, "exec-incomplete") {
		t.Fatalf("tracking labels = %v, want exec-incomplete", all[0].Labels)
	}
	if slicesContain(all[0].Labels, "exec-failed") {
		t.Fatalf("tracking labels = %v, want no exec-failed", all[0].Labels)
	}
	// The command's own report is the only durable evidence of WHAT was left
	// outstanding. Dropping it because the run is not a failure would put the
	// reason back where this bead found it -- in the rotating event log alone.
	if got := all[0].Metadata[beadmeta.OrderExecFailureOutputMetadataKey]; !strings.Contains(got, "blocked=1") {
		t.Fatalf("tracking bead output = %q, want the command's report retained", got)
	}
	var completed events.Event
	for _, e := range rec.events {
		if e.Type == events.OrderCompleted {
			completed = e
		}
	}
	// Both halves asserted: an operator reading the log sees WHICH order stopped
	// short and on what status. The status alone is what err.Error() already
	// says, so an assertion on it alone would pass with the label stripped and
	// the event indistinguishable from a clean completion.
	if !strings.Contains(completed.Message, "incomplete") || !strings.Contains(completed.Message, "exit status 3") {
		t.Fatalf("completed event message = %q, want it labeled incomplete and naming the exit status", completed.Message)
	}
}

// TestOrderDispatchExecUndeclaredExitStaysFailed pins the other side, and it is
// the assertion that keeps the fix from being a suppression: an exit status the
// order did NOT declare is a failure however close it sits to a declared one.
// Without this, "declare 3" would drift into "tolerate nonzero" and hide the
// genuine crashes the check exists to surface.
func TestOrderDispatchExecUndeclaredExitStaysFailed(t *testing.T) {
	for _, code := range []int{1, 2, 4} {
		t.Run("exit-"+itoaForTest(code), func(t *testing.T) {
			store := beads.NewMemStore()
			var rec memRecorder
			trackingID := seedExecTracking(t, store)

			fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
				return []byte("boom\n"), exitErrorWithCode(t, code)
			}
			aa := []orders.Order{{
				Name:                "sweep",
				Trigger:             "cooldown",
				Interval:            "30m",
				Exec:                "scripts/sweep.sh",
				IncompleteExitCodes: []int{3},
			}}
			mad := buildOrderDispatcherFromListExec(aa, store, nil, fakeExec, &rec).(*memoryOrderDispatcher)
			mad.dispatchExec(context.Background(), orders.NewStore(beads.OrdersStore{Store: store}), execStoreTarget{ScopeRoot: t.TempDir()}, aa[0], t.TempDir(), trackingID, nil)

			if !rec.hasType(events.OrderFailed) || rec.hasType(events.OrderCompleted) {
				t.Fatalf("events = %+v, want failed without completed", rec.events)
			}
			all := trackingBeads(t, store, "order-run:sweep")
			if len(all) != 1 || !slicesContain(all[0].Labels, "exec-failed") {
				t.Fatalf("tracking labels = %v, want exec-failed", all)
			}
		})
	}
}

// TestOrderDispatchExecUndeclaringOrderKeepsEveryNonzeroExitFailed pins that
// the classification is opt-in. Every order in the fleet predates this field,
// so an order declaring nothing must behave exactly as it did before.
func TestOrderDispatchExecUndeclaringOrderKeepsEveryNonzeroExitFailed(t *testing.T) {
	store := beads.NewMemStore()
	var rec memRecorder
	trackingID := seedExecTracking(t, store)

	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		return []byte("blocked=1\n"), exitErrorWithCode(t, 3)
	}
	aa := []orders.Order{{Name: "sweep", Trigger: "cooldown", Interval: "30m", Exec: "scripts/sweep.sh"}}
	mad := buildOrderDispatcherFromListExec(aa, store, nil, fakeExec, &rec).(*memoryOrderDispatcher)
	mad.dispatchExec(context.Background(), orders.NewStore(beads.OrdersStore{Store: store}), execStoreTarget{ScopeRoot: t.TempDir()}, aa[0], t.TempDir(), trackingID, nil)

	if !rec.hasType(events.OrderFailed) {
		t.Fatalf("events = %+v, want failed", rec.events)
	}
}

// TestOrderDispatchExecKilledRunIsNeverIncomplete pins the case that would
// otherwise hide the exact defect this order already suffered: a run killed at
// its timeout is a fault, and it must stay one even when the shell reports an
// exit status the order declared.
//
// A killed process surfaces as a signal, not a status, so the code cannot
// collide in production -- but the classifier must not depend on that. It is
// driven here with a canceled context AND a declared code so the guard is
// exercised rather than assumed: without the context check the order would go
// green on every timeout, which is the failure mode that took 22 consecutive
// kills to notice the last time (see orders/merge-closed-features.toml).
func TestOrderDispatchExecKilledRunIsNeverIncomplete(t *testing.T) {
	store := beads.NewMemStore()
	var rec memRecorder
	trackingID := seedExecTracking(t, store)

	ctx, cancel := context.WithCancel(context.Background())
	fakeExec := func(_ context.Context, _, _ string, _ []string) ([]byte, error) {
		cancel()
		return []byte("partial\n"), exitErrorWithCode(t, 3)
	}
	aa := []orders.Order{{
		Name:                "sweep",
		Trigger:             "cooldown",
		Interval:            "30m",
		Exec:                "scripts/sweep.sh",
		IncompleteExitCodes: []int{3},
	}}
	mad := buildOrderDispatcherFromListExec(aa, store, nil, fakeExec, &rec).(*memoryOrderDispatcher)
	mad.dispatchExec(ctx, orders.NewStore(beads.OrdersStore{Store: store}), execStoreTarget{ScopeRoot: t.TempDir()}, aa[0], t.TempDir(), trackingID, nil)

	if !rec.hasType(events.OrderFailed) || rec.hasType(events.OrderCompleted) {
		t.Fatalf("events = %+v, want failed without completed", rec.events)
	}
}
