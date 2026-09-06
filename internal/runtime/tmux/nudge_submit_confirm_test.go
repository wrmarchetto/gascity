package tmux

import (
	"errors"
	"testing"
	"time"
)

// noSleep is a sleep stub so the confirm loop runs instantly under test.
func noSleep(time.Duration) {}

// neverDrafted abstains from the draft-disappearance evidence: it reports the
// draft as never having been observed in the input box, which makes the busy
// indicator the sole source. Every case written before that source existed is
// stated in terms of busy alone, so abstaining is what keeps each one
// measuring what its name claims.
func neverDrafted() (bool, error) { return false, nil }

// TestSubmitEnterAndConfirmReEntersWhileIdle proves the ga-bwm fix: when the
// first Enter is lost (the pane stays idle with the message still drafted), the
// loop re-sends Enter, and the send that lands drives the agent busy.
func TestSubmitEnterAndConfirmReEntersWhileIdle(t *testing.T) {
	var enters int
	// Busy only becomes true once a second Enter has been sent, i.e. the first
	// Enter raced the paste and was dropped.
	busy := func() (bool, error) { return enters >= 2, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, noSleep)
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

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, noSleep)
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

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, sleep)
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

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, noSleep)
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

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, noSleep)
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

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, noSleep)
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

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, neverDrafted, noSleep)
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

// TestSubmitEnterAndConfirmConfirmsATurnShorterThanTheIndicator is the rewrite
// the previous revision of this test demanded. It used to assert the WRONG
// answer on purpose -- that a turn ending before the busy indicator renders is
// reported unconfirmed -- and to fail the moment that was fixed. This is that
// failure, resolved (ci-mdfcgs -> ci-uihrrv).
//
// THE DEFECT IT REPLACES. The busy indicator takes a measured ~3.3s to render
// (see the constants block). A turn that ENDS inside that window never renders
// it, so every poll correctly answers not-busy and a submit that landed and was
// answered is indistinguishable from one that never landed. An account at its
// usage cap answers in 1.2-1.6s, which is how a governor session held a pool
// slot for 1h35m while reporting three failed nudges it had in fact delivered.
//
// WHAT MAKES IT CONFIRMABLE is the draft leaving the input box. busy is
// injected here as a predicate that is NEVER true -- the indicator genuinely
// never rendered, which is the physical situation rather than a stub
// convenience -- so this case passes on the second source alone. Remove or
// invert the draft evidence and busy cannot rescue it.
func TestSubmitEnterAndConfirmConfirmsATurnShorterThanTheIndicator(t *testing.T) {
	const indicatorRenderLatency = 3300 * time.Millisecond
	const answeredTurnDuration = 1400 * time.Millisecond

	var elapsed time.Duration
	var enters int
	busy := func() (bool, error) {
		return elapsed >= indicatorRenderLatency && elapsed < answeredTurnDuration, nil
	}
	// In the box until the Enter lands, gone once it has.
	drafted := func() (bool, error) { return enters == 0, nil }
	sleep := func(delay time.Duration) { elapsed += delay }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, drafted, sleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if !confirmed {
		t.Fatal("confirmed = false for a turn that answered before the indicator " +
			"rendered -- the draft left the input box, which IS the submit landing")
	}
	// The costlier half of the old defect: three Enters went into a session
	// that had already answered, ~3.8s apart. One is now enough.
	if enters != 1 {
		t.Fatalf("enters = %d, want 1 -- a session that already answered must not "+
			"be re-submitted", enters)
	}
	// And it must not burn the full budget doing it, which is what made the
	// condition legible as a ~12.9s start in the supervisor log.
	if elapsed >= submitConfirmPollInterval*time.Duration(submitConfirmPollsPerSend) {
		t.Fatalf("elapsed = %s, want well inside one send's poll window", elapsed)
	}
}

// TestSubmitEnterAndConfirmStillReSendsWhenTheDraftStaysPut is the other
// direction, and the regression that matters most: ga-bwm, a submit Enter
// dropped against an unfinished bracketed paste, leaving the text drafted and
// never submitted. The draft is STILL IN THE BOX there, so the new evidence
// must withhold confirmation and let the loop re-send.
//
// Without this case the draft source could be inverted -- confirming on
// PRESENCE rather than absence -- and the case above would still pass while
// every lost Enter went unretried.
func TestSubmitEnterAndConfirmStillReSendsWhenTheDraftStaysPut(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return false, nil }
	drafted := func() (bool, error) { return true, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, drafted, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true while the draft never left the input box")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d bounded re-sends", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmIgnoresAnEmptyBoxItNeverSawFilled pins the
// abstention, and it is what stops this evidence being dangerous. If the PASTE
// failed, the box is empty before the Enter and empty after it -- and "empty"
// reads as "the draft left" if absence alone is trusted. That would report a
// nudge delivered which the agent never saw, which is worse than the defect
// being fixed: the old bug re-sent too often, this one would silently drop the
// message.
//
// So the disappearance counts only when the draft was OBSERVED in the box
// first. Here it never was, so the loop falls back to busy alone and spends its
// full bounded budget exactly as it did before this source existed.
func TestSubmitEnterAndConfirmIgnoresAnEmptyBoxItNeverSawFilled(t *testing.T) {
	var enters int
	busy := func() (bool, error) { return false, nil }
	drafted := func() (bool, error) { return false, nil }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, busy, drafted, noSleep)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if confirmed {
		t.Fatal("confirmed = true from an input box never seen holding the draft")
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d bounded re-sends", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmSaysSoWhenThePaneCouldNeverBeObserved pins the
// difference between "the pane says idle" and "the pane could not be read".
// Both sources read through capture-pane, and its error used to be discarded
// by `err == nil && isBusy`, so a broken observer and a wedged agent produced
// byte-identical evidence with nothing logged either way. Every symptom of the
// 2026-09-06 pool-slot outage looks the same under a failing capture, and
// nothing in the supervisor log could have told them apart.
//
// The control flow is deliberately unchanged -- an unobservable pane already
// ended unconfirmed and already returned an error -- so what this pins is the
// error a reader is handed, which is the only thing that decides whether they
// go looking at the agent or at the observer.
func TestSubmitEnterAndConfirmSaysSoWhenThePaneCouldNeverBeObserved(t *testing.T) {
	captureErr := errors.New("capture-pane: no such pane")
	var enters int
	blind := func() (bool, error) { return false, captureErr }
	sendEnter := func() error { enters++; return nil }

	confirmed, err := submitEnterAndConfirm(sendEnter, func() {}, blind, blind, noSleep)
	if confirmed {
		t.Fatal("confirmed = true from a pane that was never read")
	}
	if !errors.Is(err, captureErr) {
		t.Fatalf("err = %v, want the capture error in the chain", err)
	}
	if enters != submitEnterMaxSends {
		t.Fatalf("enters = %d, want %d -- delivery still runs its full budget", enters, submitEnterMaxSends)
	}
}

// TestSubmitEnterAndConfirmPrefersATmuxSendErrorOverAnObservationError pins the
// precedence. A send that never reached tmux is a harder fact than a pane that
// could not be read, and reporting the observer's failure would send the reader
// to the wrong layer.
func TestSubmitEnterAndConfirmPrefersATmuxSendErrorOverAnObservationError(t *testing.T) {
	sendErr := errors.New("no server")
	captureErr := errors.New("capture-pane: no such pane")
	blind := func() (bool, error) { return false, captureErr }

	_, err := submitEnterAndConfirm(func() error { return sendErr }, func() {}, blind, blind, noSleep)
	if !errors.Is(err, sendErr) {
		t.Fatalf("err = %v, want the send error to win", err)
	}
}

// TestSubmitEnterAndConfirmKeepsTheOldWordingWhenThePaneWasReadable pins the
// other side: a pane that was successfully read and simply never went busy is
// the ORIGINAL best-effort outcome, and must keep returning (false, nil) so the
// caller renders the historical unconfirmed error rather than an observation
// failure that did not happen.
func TestSubmitEnterAndConfirmKeepsTheOldWordingWhenThePaneWasReadable(t *testing.T) {
	idle := func() (bool, error) { return false, nil }
	confirmed, err := submitEnterAndConfirm(func() error { return nil }, func() {}, idle, idle, noSleep)
	if confirmed {
		t.Fatal("confirmed = true from an idle pane")
	}
	if err != nil {
		t.Fatalf("err = %v, want nil -- the pane WAS observed, it was just idle", err)
	}
}
