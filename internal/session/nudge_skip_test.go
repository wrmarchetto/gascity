// Scope: the reason a wait-idle nudge did not reach a live session, and the
// invariant that an undelivered nudge always carries one.
//
// The suite exists because every decline used to return (false, nil). A caller
// could see that delivery had not happened and nothing else, so
// `gc mail send --notify` reported success for a message a mid-turn recipient
// never saw (ci-7b1ueb). These cases pin the DISTINCTIONS rather than the
// delivery: whether a nudge lands at all is already covered by the wait-idle
// cases in submit_test.go and internal/worker/handle_test.go, and the
// end-to-end mail reporting is cmd/gc/cmd_nudge_test.go's.
//
// Run: go test ./internal/session/ -run 'WaitIdleNudge|NudgeSkip'
package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// startedClaudeSession builds a live claude-kind session on a fake runtime and
// returns the manager, the fake and the session info.
func startedClaudeSession(t *testing.T) (*Manager, *runtime.Fake, Info) {
	t.Helper()
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	sp.WaitForIdleErrors = map[string]error{}
	mgr := NewManagerWithOptions(store, sp)

	info, err := mgr.CreateSession(context.Background(), CreateOptions{
		Template: "helper", Title: "", Command: "claude", WorkDir: t.TempDir(),
		Provider: "claude", Env: nil, Resume: ProviderResume{}, Hints: runtime.Config{},
		ExtraMeta: map[string]string{"session_origin": "manual"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if err := mgr.Start(context.Background(), info.ID, "", runtime.Config{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return mgr, sp, info
}

// nudgeReachedPane reports whether any pane write happened for sessName.
func nudgeReachedPane(sp *runtime.Fake, sessName string) bool {
	for _, call := range sp.Calls {
		if call.Name != sessName {
			continue
		}
		if call.Method == "Nudge" || call.Method == "NudgeNow" {
			return true
		}
	}
	return false
}

// TestWaitIdleNudgeLiveOnlyReportsBusyWhenSessionIsMidTurn pins the case the
// whole type was added for: the session is live and reachable, the wait for an
// idle prompt expires, and nothing is written to the pane.
//
// The assertion is on the SKIP and on the absence of a pane write together. A
// skip asserted alone would still pass if the fix reported "busy" and then
// wrote mid-turn anyway, which is the unsafe write the wait exists to prevent.
func TestWaitIdleNudgeLiveOnlyReportsBusyWhenSessionIsMidTurn(t *testing.T) {
	mgr, sp, info := startedClaudeSession(t)
	sp.WaitForIdleErrors[info.SessionName] = runtime.ErrIdleTimeout

	delivered, skip, err := mgr.TryWaitIdleNudgeLiveOnly(context.Background(), info.ID, "mail", "You have mail from mayor")
	if err != nil {
		t.Fatalf("TryWaitIdleNudgeLiveOnly: %v", err)
	}
	if delivered {
		t.Fatal("delivered = true, want false for a mid-turn session")
	}
	if skip != NudgeSkipBusy {
		t.Fatalf("skip = %q, want %q", skip, NudgeSkipBusy)
	}
	if nudgeReachedPane(sp, info.SessionName) {
		t.Fatalf("calls = %#v, want no pane write while the session is mid-turn", sp.Calls)
	}
}

// TestWaitIdleNudgeLiveOnlyDistinguishesAnUnsupportedRuntimeFromABusySession
// pins the distinction that makes the operator message honest. Both outcomes
// decline, but only one of them is answered by waiting.
func TestWaitIdleNudgeLiveOnlyDistinguishesAnUnsupportedRuntimeFromABusySession(t *testing.T) {
	mgr, sp, info := startedClaudeSession(t)
	sp.WaitForIdleErrors[info.SessionName] = runtime.ErrInteractionUnsupported

	delivered, skip, err := mgr.TryWaitIdleNudgeLiveOnly(context.Background(), info.ID, "mail", "You have mail from mayor")
	if err != nil {
		t.Fatalf("TryWaitIdleNudgeLiveOnly: %v", err)
	}
	if delivered {
		t.Fatal("delivered = true, want false when the runtime cannot wait for idle")
	}
	if skip != NudgeSkipNoIdleWait {
		t.Fatalf("skip = %q, want %q", skip, NudgeSkipNoIdleWait)
	}
}

// TestWaitIdleNudgeLiveOnlyReportsAFailedWaitApartFromABusyOne pins that an
// unrecognized wait failure is NOT reported as mid-turn. A session that has
// gone away is not one a queued message reaches later, so reporting it as busy
// would tell the sender to expect a delivery that cannot happen.
func TestWaitIdleNudgeLiveOnlyReportsAFailedWaitApartFromABusyOne(t *testing.T) {
	mgr, sp, info := startedClaudeSession(t)
	sp.WaitForIdleErrors[info.SessionName] = fmt.Errorf("capture pane: no such session")

	delivered, skip, err := mgr.TryWaitIdleNudgeLiveOnly(context.Background(), info.ID, "mail", "You have mail from mayor")
	if err != nil {
		t.Fatalf("TryWaitIdleNudgeLiveOnly: %v", err)
	}
	if delivered {
		t.Fatal("delivered = true, want false when the idle wait fails")
	}
	if skip != NudgeSkipIdleWaitFailed {
		t.Fatalf("skip = %q, want %q", skip, NudgeSkipIdleWaitFailed)
	}
}

// TestWaitIdleNudgeLiveOnlyReportsNotRunningWithoutTouchingTheRuntime pins the
// live-only contract: a stopped session is declined by name, and the wait is
// never attempted.
func TestWaitIdleNudgeLiveOnlyReportsNotRunningWithoutTouchingTheRuntime(t *testing.T) {
	mgr, sp, info := startedClaudeSession(t)
	if err := mgr.Suspend(info.ID); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	start := len(sp.Calls)

	delivered, skip, err := mgr.TryWaitIdleNudgeLiveOnly(context.Background(), info.ID, "mail", "You have mail from mayor")
	if err != nil {
		t.Fatalf("TryWaitIdleNudgeLiveOnly: %v", err)
	}
	if delivered {
		t.Fatal("delivered = true, want false for a stopped session")
	}
	if skip != NudgeSkipNotRunning {
		t.Fatalf("skip = %q, want %q", skip, NudgeSkipNotRunning)
	}
	for _, call := range sp.Calls[start:] {
		if call.Method == "WaitForIdle" {
			t.Fatalf("calls = %#v, want no idle wait on a stopped session", sp.Calls[start:])
		}
	}
}

// TestWaitIdleNudgeDeliversAndReportsNoSkip is the positive control. Without
// it the three cases above are satisfied by a wait-idle path that never
// delivers anything at all.
func TestWaitIdleNudgeDeliversAndReportsNoSkip(t *testing.T) {
	mgr, sp, info := startedClaudeSession(t)
	sp.WaitForIdleErrors[info.SessionName] = nil

	delivered, skip, err := mgr.TryWaitIdleNudgeLiveOnly(context.Background(), info.ID, "mail", "You have mail from mayor")
	if err != nil {
		t.Fatalf("TryWaitIdleNudgeLiveOnly: %v", err)
	}
	if !delivered {
		t.Fatalf("delivered = false, want true; calls = %#v", sp.Calls)
	}
	if skip != NudgeSkipNone {
		t.Fatalf("skip = %q, want empty on a delivered nudge", skip)
	}
	if !nudgeReachedPane(sp, info.SessionName) {
		t.Fatalf("calls = %#v, want a pane write", sp.Calls)
	}
}

// TestEveryUndeliveredWaitIdleNudgeNamesASkipReason holds the invariant the
// two return values are read against: delivered iff the skip is empty.
//
// It drives the same outcomes through both wait-idle entry points, because the
// live-only and resuming variants are separate functions that have already
// drifted from each other once -- they are near-identical bodies, and a fix
// applied to one is easy to miss on the other.
func TestEveryUndeliveredWaitIdleNudgeNamesASkipReason(t *testing.T) {
	waits := map[string]error{
		"delivered":   nil,
		"busy":        runtime.ErrIdleTimeout,
		"unsupported": runtime.ErrInteractionUnsupported,
		"failed":      errors.New("capture pane failed"),
	}
	for name, waitErr := range waits {
		for _, liveOnly := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/liveOnly=%v", name, liveOnly), func(t *testing.T) {
				mgr, sp, info := startedClaudeSession(t)
				sp.WaitForIdleErrors[info.SessionName] = waitErr

				var delivered bool
				var skip NudgeSkip
				var err error
				if liveOnly {
					delivered, skip, err = mgr.TryWaitIdleNudgeLiveOnly(context.Background(), info.ID, "mail", "m")
				} else {
					delivered, skip, err = mgr.TryWaitIdleNudge(context.Background(), info.ID, "mail", "m", "", runtime.Config{})
				}
				if err != nil {
					t.Fatalf("nudge: %v", err)
				}
				if delivered != (skip == NudgeSkipNone) {
					t.Fatalf("delivered = %v with skip = %q; the two must agree", delivered, skip)
				}
				if !delivered && skip.Explain() == "" {
					t.Fatalf("skip %q explains nothing, so a sender is told delivery failed and not why", skip)
				}
			})
		}
	}
}
