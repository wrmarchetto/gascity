package tmux

import (
	"errors"
	"testing"
	"time"
)

// noSleep is a sleep stub so the confirm loop runs instantly under test.
func noSleep(time.Duration) {}

// TestSubmitEnterAndConfirmReEntersWhileIdle proves the ga-bwm fix: when the
// first Enter is lost (the pane stays idle with the message still drafted), the
// loop re-sends Enter, and the send that lands drives the agent busy.
func TestSubmitEnterAndConfirmReEntersWhileIdle(t *testing.T) {
	var enters int
	// Busy only becomes true once a second Enter has been sent, i.e. the first
	// Enter raced the paste and was dropped.
	busy := func() (bool, error) { return enters >= 2, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true (re-sent Enter should submit)")
	}
	if enters != 2 {
		t.Fatalf("enters = %d, want 2 (initial + one re-send)", enters)
	}
}

// TestSubmitEnterAndConfirmStopsWhenBusy proves the common case: a single Enter
// that submits is confirmed on the first poll with no wasted re-send.
func TestSubmitEnterAndConfirmStopsWhenBusy(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return enters >= 1, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 (no re-send once submitted)", enters)
	}
}

// TestSubmitConfirmBudgetExceedsProviderTurnStartLatency proves that the
// confirmation window outlasts the observed three-second delay between Claude
// accepting a prompt and rendering its first busy indicator (ci-ddapcs).
func TestSubmitConfirmBudgetExceedsProviderTurnStartLatency(t *testing.T) {
	const observedProviderTurnStartLatency = 3 * time.Second

	var elapsed time.Duration
	var enters int
	busy := func() (bool, error) {
		return elapsed >= observedProviderTurnStartLatency, nil
	}
	sleep := func(delay time.Duration) { elapsed += delay }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, sleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatalf("confirmed = false after %s provider turn-start latency, want true", observedProviderTurnStartLatency)
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 (a delayed busy indicator must not re-submit)", enters)
	}
}

// TestSubmitEnterAndConfirmNoDoubleSubmitOnFastTurn proves the safety property:
// if a turn goes busy after the first send's polls but before a re-send, the
// pre-re-send busy check catches it and no second Enter is issued.
func TestSubmitEnterAndConfirmNoDoubleSubmitOnFastTurn(t *testing.T) {
	var enters int
	var busyCalls int
	busy := func() (bool, error) {
		busyCalls++
		// Idle for the first send's polls; busy at the pre-re-send check.
		return busyCalls > submitConfirmPollsPerSend, nil
	}
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false, want true")
	}
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 (pre-re-send busy check must prevent double-submit)", enters)
	}
}

// TestSubmitEnterAndConfirmBestEffortWhenNeverBusy proves that a pane which
// never reports busy is delivered best-effort (bounded re-sends, no error) so
// the caller's contract (nil == delivered to tmux) is preserved.
func TestSubmitEnterAndConfirmBestEffortWhenNeverBusy(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return false, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d (bounded best-effort sends)", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmClearsStaleSendError proves a transient first-send
// failure followed by a successful send (busy never observed) is reported as
// best-effort delivery (false, nil), not a stale error — matching the
// historical "nil == handed to tmux" contract.
func TestSubmitEnterAndConfirmClearsStaleSendError(t *testing.T) {
	var enters int
	sendEnter := func() error {
		enters++
		if enters == 1 {
			return errors.New("transient: no server yet")
		}
		return nil
	}
	busy := func() (bool, error) { return false, nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil (later send succeeded)", err)
	}
	if confirmed {
		t.Fatal("confirmed = true, want false (never busy)")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmReturnsSendError proves a genuine tmux-layer send
// failure (session gone) is surfaced, matching the pre-fix contract.
func TestSubmitEnterAndConfirmReturnsSendError(t *testing.T) {
	sendErr := errors.New("no server")
	var enters int
	sendEnter := func() error { enters++; return sendErr }
	busy := func() (bool, error) { return false, nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, noSleep)
	if confirmed {
		t.Fatal("confirmed = true, want false")
	}
	if !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want sendErr chain", err)
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmCannotSeeATurnShorterThanTheIndicator pins a KNOWN
// DEFECT, deliberately, and asserts the WRONG answer so that fixing the defect
// breaks this test and forces it to be rewritten. Do not "fix" it by relaxing
// the assertion.
//
// THE HOLE. Confirmation asks "is this pane visibly working", and the busy
// indicator does not render for about 3.3s after a prompt is accepted -- the
// latency submitConfirmPollsPerSend was sized to outlast (ci-ddapcs, and the
// comment above those constants). Nothing asked what happens when the turn is
// SHORTER than that latency. Then the indicator never renders at all, every
// poll correctly answers "not busy", and a submit that landed and was answered
// is reported identically to one that never landed.
//
// THIS IS NOT HYPOTHETICAL. On 2026-09-06 the governor's account was at its
// usage cap, so every nudge was answered in 1.2-1.6s with an API error turn
// ("You've hit your session limit"). All four nudges to governor-ci-xbhzyp
// were delivered, submitted and answered; all four reported unconfirmed. The
// pool then held that slot for 1h35m and governor oversight went dark for
// 1h44m (ci-mdfcgs).
//
// WHY THE SIBLING TESTS ABOVE CANNOT REPRESENT IT. Every one of them models
// busy as a latch that, once true, stays true -- a turn that never ends. A
// turn that ENDS before the indicator renders needs busy to be an interval,
// which is what this test injects. Note in particular that
// TestSubmitEnterAndConfirmNoDoubleSubmitOnFastTurn is misnamed: its turn goes
// busy after the first send's polls and stays busy, which is a SLOW turn.
//
// THE SECOND ASSERTION IS THE COSTLIER HALF. Three Enters are delivered into a
// session that already answered, roughly 3.8s apart. The pre-re-send guard at
// the top of the loop is blind for the same reason the confirmation is, so the
// double-submit safety property the sibling test claims does not hold here.
func TestSubmitEnterAndConfirmCannotSeeATurnShorterThanTheIndicator(t *testing.T) {
	// Both figures are measured, not chosen: 3.3s is the render latency the
	// constants above cite, and 1.4s is the middle of the 1.2-1.6s range the
	// four capped governor turns took (transcript 85fbe4da-..., 2026-09-06).
	const indicatorRenderLatency = 3300 * time.Millisecond
	const answeredTurnDuration = 1400 * time.Millisecond

	var elapsed time.Duration
	var enters int
	// Busy is TRUE only while the indicator is on screen: from the render
	// latency until the turn ends. For a turn this short that interval is
	// empty, so this closure never returns true -- which is the whole point.
	busy := func() (bool, error) {
		return elapsed >= indicatorRenderLatency && elapsed < answeredTurnDuration, nil
	}
	sleep := func(delay time.Duration) { elapsed += delay }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, sleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true -- the defect this test pins is FIXED; rewrite " +
			"this test to assert the correct behavior and drop the known-defect framing")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d -- a session that already answered still "+
			"receives every re-send, because the pre-re-send guard reads the same "+
			"blind predicate", enters, submitEnterMaxSends)
	}
	// The budget is spent in full, which is what makes the caller's start take
	// ~12.9s instead of ~6s and is how the condition is recognizable in
	// ~/.gc/supervisor.log without any of the session's own state.
	if elapsed < submitConfirmPollInterval*time.Duration(submitConfirmPollsPerSend*submitEnterMaxSends) {
		t.Fatalf("elapsed = %s, want the full poll budget spent", elapsed)
	}
}
