// Package tmux test: the overlay guard inside the submit confirm loop.
//
// Scope: submitEnterAndConfirm's re-send arm only. The predicate it consults is
// covered by nudge_overlay_test.go against real pane captures; this file drives
// the loop with scripted sources and counts KEYSTROKES, because the defect is a
// keystroke that should never have been sent.
//
// What it delegates: whether a given pane really is an overlay (fixtures), and
// whether Enter really advances a picker (driving the TUIs, ci-vkadhb).
//
// Run:
//
//	go test ./internal/runtime/tmux/ -run OverlayResend
package tmux

import (
	"errors"
	"testing"
	"time"
)

// scriptedSource answers from a fixed list and REFUSES to run off the end.
// A source that kept answering its last value would hand a pass to whatever
// the test forgot to script -- and the thing under test is how many times the
// loop looks.
func scriptedSource(t *testing.T, name string, answers ...bool) func() (bool, error) {
	t.Helper()
	i := 0
	return func() (bool, error) {
		if i >= len(answers) {
			t.Fatalf("%s source consulted %d times, only %d answers scripted", name, i+1, len(answers))
		}
		v := answers[i]
		i++
		return v, nil
	}
}

func alwaysFalse() (bool, error) { return false, nil }

// TestOverlayResendStopsBeforeTheSecondEnter is the invariant: once the pane is
// showing an overlay, the loop must stop rather than press Enter again.
//
// It asserts on the SEND COUNT rather than on the returned error, because the
// error is a report and the keystroke is the damage. A fix that named the
// overlay correctly and still sent the key would pass an error-only assertion.
func TestOverlayResendStopsBeforeTheSecondEnter(t *testing.T) {
	sends := 0
	sendEnter := func() error { sends++; return nil }
	confirmed, err := submitEnterAndConfirm(
		sendEnter,
		func() {},
		alwaysFalse, // never busy: the palette shows no busy indicator
		alwaysFalse, // draft never seen, so that source abstains
		constantSource(true),
		func(time.Duration) {},
	)
	if sends != 1 {
		t.Fatalf("sends = %d, want 1: the overlay was showing, so the submit key must not be re-sent -- on codex three of these walked /model to a committed change", sends)
	}
	if confirmed {
		t.Fatal("confirmed = true: nothing observed a submit, so the nudge must be reported unconfirmed")
	}
	if !errors.Is(err, errSubmitOverlayPresent) {
		t.Fatalf("err = %v, want errSubmitOverlayPresent so the operator is not sent hunting a wedged agent", err)
	}
}

// TestOverlayResendStillResendsWithoutAnOverlay is the control, and it is the
// case the guard must not break: an idle pane with no overlay keeps the ga-bwm
// re-send behavior the confirm loop exists for.
func TestOverlayResendStillResendsWithoutAnOverlay(t *testing.T) {
	sends := 0
	sendEnter := func() error { sends++; return nil }
	confirmed, err := submitEnterAndConfirm(
		sendEnter,
		func() {},
		alwaysFalse,
		alwaysFalse,
		alwaysFalse,
		func(time.Duration) {},
	)
	if sends != submitEnterMaxSends {
		t.Fatalf("sends = %d, want %d: with no overlay the loop must still exhaust its re-sends", sends, submitEnterMaxSends)
	}
	if confirmed || err != nil {
		t.Fatalf("confirmed = %v, err = %v: nothing ever confirmed, so this is the plain unconfirmed outcome", confirmed, err)
	}
}

// TestOverlayResendAbstainsOnAnUnreadablePane pins the direction an
// observation failure takes. Every other source in this loop treats "could not
// look" as no evidence; if the overlay source blocked instead, a flaky
// capture-pane would silently disable the re-send for everybody -- the same
// never-fires-and-looks-fine shape the loop itself was written to fix.
func TestOverlayResendAbstainsOnAnUnreadablePane(t *testing.T) {
	sends := 0
	blind := func() (bool, error) { return false, errors.New("capture-pane failed") }
	confirmed, _ := submitEnterAndConfirm(
		func() error { sends++; return nil },
		func() {},
		alwaysFalse,
		alwaysFalse,
		blind,
		func(time.Duration) {},
	)
	if sends != submitEnterMaxSends {
		t.Fatalf("sends = %d, want %d: an unreadable pane is no evidence of an overlay", sends, submitEnterMaxSends)
	}
	if confirmed {
		t.Fatal("confirmed = true without any positive observation")
	}
}

// TestOverlayResendChecksTheOverlayOnlyAfterTheFirstSend pins the scope the
// bead set: the first send is today's behavior for every family, and changing
// it is a separate argument. An overlay already on screen when the nudge
// arrives still gets its one Enter.
func TestOverlayResendChecksTheOverlayOnlyAfterTheFirstSend(t *testing.T) {
	sends := 0
	// One answer only. If the loop consulted the source before the first send
	// the second consultation runs off the script and fails the test by name.
	overlay := scriptedSource(t, "overlay", true)
	// The outcome is asserted by the send count and by scriptedSource refusing
	// a second consultation; both returns are deliberately unused here.
	_, _ = submitEnterAndConfirm(
		func() error { sends++; return nil },
		func() {},
		alwaysFalse,
		alwaysFalse,
		overlay,
		func(time.Duration) {},
	)
	if sends != 1 {
		t.Fatalf("sends = %d, want 1", sends)
	}
}

// constantSource returns a source that always answers v. Spelled at the call
// site as constantSource(true) so a reader sees which answer is being fixed
// without counting positional arguments.
func constantSource(v bool) func() (bool, error) {
	return func() (bool, error) { return v, nil }
}
