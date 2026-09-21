package main

// Scope: memoryIdleTracker's DECLINE reporting -- the paths on which checkIdle
// reaches no idle verdict at all, and the throttle that governs how often one
// of them is put in front of an operator.
//
// The suite exists because those paths were silent (ci-kjh8vc): a failed
// activity read and a genuinely busy agent produced byte-identical output --
// none -- so an idle reaper that could never fire was indistinguishable from
// one with nothing to reap. Every assertion below is on the decline vocabulary
// rather than on a rendered string, so the wording of the operator line can
// change without the contract moving.
//
// Delegated elsewhere: the idle/not-idle threshold arithmetic and the template
// fallback are pinned by idle_tracker_test.go, the poke discount by
// idle_tracker_poke_test.go, and the reconciler's emission of the line by
// TestReconcileSessionBeads_IdleCheckDeclineReachesStderr in
// session_reconciler_test.go. None is re-tested here.
//
// Run: go test ./cmd/gc/ -run TestIdleTrackerDecline

import (
	"errors"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// declineTracker builds a tracker whose one registered session reads its last
// activity from stub. The stub stands in for the whole observe path, which is
// the only way to exercise a read that is PRESENT AND WRONG -- withholding the
// provider instead short-circuits before checkIdle's own error mapping runs.
func declineTracker(t *testing.T, stub activityReader) *memoryIdleTracker {
	t.Helper()
	it := newIdleTracker()
	it.setTimeout("worker", 5*time.Minute)
	it.readActivity = func(sp runtime.Provider, sessionName string) (time.Time, error) {
		if sessionName != "worker" {
			t.Fatalf("readActivity called for %q, want the one registered session", sessionName)
		}
		return stub(sp, sessionName)
	}
	return it
}

// TestIdleTrackerDecline_FailedReadIsReportedWithItsError pins that a read
// error becomes a reportable decline carrying the error, not a bare false.
func TestIdleTrackerDecline_FailedReadIsReportedWithItsError(t *testing.T) {
	t.Parallel()

	readErr := errors.New("observe: session unavailable")
	it := declineTracker(t, func(runtime.Provider, string) (time.Time, error) {
		return time.Time{}, readErr
	})

	got := it.checkIdle("worker", "", nil, time.Now(), runtime.Poke{})
	if got.Idle {
		t.Errorf("Idle = true, want false: a failed read reaches no verdict")
	}
	if got.Decline != idleDeclineReadFailed {
		t.Errorf("Decline = %q, want %q", got.Decline, idleDeclineReadFailed)
	}
	if !got.Report {
		t.Errorf("Report = false, want true on the first decline for a session")
	}
	if !errors.Is(got.Err, readErr) {
		t.Errorf("Err = %v, want the read error %v", got.Err, readErr)
	}
}

// TestIdleTrackerDecline_MissingTimestampIsReported pins the decline an
// operator actually sees in production: LiveObservation swallows the
// provider's own read error, so a broken activity read arrives here as a
// successful observe carrying no timestamp.
func TestIdleTrackerDecline_MissingTimestampIsReported(t *testing.T) {
	t.Parallel()

	it := declineTracker(t, func(runtime.Provider, string) (time.Time, error) {
		return time.Time{}, nil
	})

	got := it.checkIdle("worker", "", nil, time.Now(), runtime.Poke{})
	if got.Decline != idleDeclineNoActivity {
		t.Errorf("Decline = %q, want %q", got.Decline, idleDeclineNoActivity)
	}
	if !got.Report {
		t.Errorf("Report = false, want true on the first decline for a session")
	}
	if got.Err != nil {
		t.Errorf("Err = %v, want nil: the observe succeeded", got.Err)
	}
}

// TestIdleTrackerDecline_VerdictReportsNothing pins the other half: a read
// that produced a usable timestamp must stay silent whichever way the
// threshold falls, or the log fills with one line per session per tick.
func TestIdleTrackerDecline_VerdictReportsNothing(t *testing.T) {
	t.Parallel()

	now := time.Now()
	for _, tc := range []struct {
		name     string
		activity time.Time
		wantIdle bool
	}{
		{"past the threshold", now.Add(-10 * time.Minute), true},
		{"inside the threshold", now.Add(-1 * time.Minute), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := declineTracker(t, func(runtime.Provider, string) (time.Time, error) {
				return tc.activity, nil
			})
			got := it.checkIdle("worker", "", nil, now, runtime.Poke{})
			if got.Idle != tc.wantIdle {
				t.Errorf("Idle = %v, want %v", got.Idle, tc.wantIdle)
			}
			if got.Decline != "" || got.Report {
				t.Errorf("Decline = %q Report = %v, want no decline for a reached verdict", got.Decline, got.Report)
			}
		})
	}
}

// TestIdleTrackerDecline_UnregisteredSessionIsNotADecline pins the deliberate
// absence: a session with no configured timeout has a switched-off reaper, not
// a broken one, and reporting it would print for every session in a city that
// configures no idle timeouts at all.
func TestIdleTrackerDecline_UnregisteredSessionIsNotADecline(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.readActivity = func(runtime.Provider, string) (time.Time, error) {
		t.Fatalf("readActivity called for a session with no registered timeout")
		return time.Time{}, nil
	}

	got := it.checkIdle("worker", "", nil, time.Now(), runtime.Poke{})
	if got.Idle || got.Decline != "" || got.Report {
		t.Errorf("checkIdle = %+v, want the zero idleCheck", got)
	}
}

// TestIdleTrackerDecline_UnchangedDeclineRepeatsOnlyAfterTheInterval pins the
// throttle in both directions. The window is computed from
// idleDeclineRepeatInterval rather than from a literal, so shortening the
// interval cannot leave this test asserting a duration nothing uses -- and the
// re-report half is what keeps a long-lived condition alive across a log
// rotation.
func TestIdleTrackerDecline_UnchangedDeclineRepeatsOnlyAfterTheInterval(t *testing.T) {
	t.Parallel()

	it := declineTracker(t, func(runtime.Provider, string) (time.Time, error) {
		return time.Time{}, nil
	})
	start := time.Now()

	if got := it.checkIdle("worker", "", nil, start, runtime.Poke{}); !got.Report {
		t.Fatalf("first decline Report = false, want true")
	}
	justInside := start.Add(idleDeclineRepeatInterval - time.Second)
	got := it.checkIdle("worker", "", nil, justInside, runtime.Poke{})
	if got.Decline != idleDeclineNoActivity {
		t.Errorf("Decline = %q, want the condition still named while throttled", got.Decline)
	}
	if got.Report {
		t.Errorf("Report = true one second inside the repeat interval, want false")
	}
	if got := it.checkIdle("worker", "", nil, start.Add(idleDeclineRepeatInterval), runtime.Poke{}); !got.Report {
		t.Errorf("Report = false at the repeat interval, want true")
	}
}

// TestIdleTrackerDecline_ChangedReasonReportsImmediately pins that the
// throttle is keyed on the reason, not on the session alone: a session whose
// condition changes is a new fact and must not be suppressed by the previous
// one's window.
func TestIdleTrackerDecline_ChangedReasonReportsImmediately(t *testing.T) {
	t.Parallel()

	var readErr error
	it := declineTracker(t, func(runtime.Provider, string) (time.Time, error) {
		return time.Time{}, readErr
	})
	start := time.Now()

	if got := it.checkIdle("worker", "", nil, start, runtime.Poke{}); got.Decline != idleDeclineNoActivity {
		t.Fatalf("Decline = %q, want %q", got.Decline, idleDeclineNoActivity)
	}
	readErr = errors.New("observe: session unavailable")
	got := it.checkIdle("worker", "", nil, start.Add(time.Second), runtime.Poke{})
	if got.Decline != idleDeclineReadFailed {
		t.Errorf("Decline = %q, want %q", got.Decline, idleDeclineReadFailed)
	}
	if !got.Report {
		t.Errorf("Report = false for a changed reason inside the repeat interval, want true")
	}
}

// TestIdleTrackerDecline_RecoveryRearmsTheReport pins that a session which
// recovers and then fails again reports at once. Without the clear, the second
// episode would be suppressed by the first episode's window -- the failure
// mode being that the condition an operator most wants to see, a flapping
// provider, is the one the throttle hides.
func TestIdleTrackerDecline_RecoveryRearmsTheReport(t *testing.T) {
	t.Parallel()

	recovered := false
	now := time.Now()
	it := declineTracker(t, func(runtime.Provider, string) (time.Time, error) {
		if recovered {
			return now.Add(-time.Minute), nil
		}
		return time.Time{}, nil
	})

	if got := it.checkIdle("worker", "", nil, now, runtime.Poke{}); !got.Report {
		t.Fatalf("first decline Report = false, want true")
	}
	recovered = true
	if got := it.checkIdle("worker", "", nil, now.Add(time.Second), runtime.Poke{}); got.Decline != "" {
		t.Fatalf("Decline = %q after recovery, want none", got.Decline)
	}
	recovered = false
	if got := it.checkIdle("worker", "", nil, now.Add(2*time.Second), runtime.Poke{}); !got.Report {
		t.Errorf("Report = false on the second episode, want true: recovery must re-arm the report")
	}
}
