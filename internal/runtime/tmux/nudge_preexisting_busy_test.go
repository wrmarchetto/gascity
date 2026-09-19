package tmux

import (
	"errors"
	"testing"
	"time"
)

// The busy indicator is evidence of a submit only when it was ABSENT before
// the submit key was sent.
//
// submitEnterAndConfirm's own docstring says it confirms "by observing the
// agent transition to its busy/processing state". It never observed a
// transition: it read the busy STATE, so a pane that was ALREADY busy when the
// nudge arrived satisfied the witness on the first poll after the first send.
// The message had not been submitted at all -- a provider whose composer
// accepts input during a run QUEUES it behind the turn, which is what the
// footer hint "Press up to edit queued messages" is reporting. Whether that
// queue then drains is a separate question this loop cannot see, and reading
// the busy state as a submit answered it without asking.
//
// MEASURED 2026-09-18T05:13-05:16Z (ci-fo6au4): all three toolsmith sessions
// parked in exactly that state with three ready P1s in the queue, each pane
// holding gc's own backstop text verbatim. `gc session nudge <agent>
// --delivery immediate` printed "Nudged <agent>" and the panes were unchanged
// 20s later. What moved them was an operator pressing the interface's own
// "send now" binding in each pane -- not a mechanism this city can rely on.
//
// CORRECTED BY ci-tihynr, 2026-09-18, and the correction does not weaken
// these cases. This file used to answer that separate question with "the turn
// ending does not submit the queue", inferred from those three panes, and it
// is false: a queued message drains at turn end unaided. Those panes were
// blocked on a modal dialog, which has no turn end.
// Abstaining on pre-existing busy is still right for both, because neither is
// a transition this loop observed -- what changed is that the caller now
// settles which one it is from the provider's queue ledger rather than
// assuming the worse.
//
// THE FILE ALREADY CONTAINED THE CORRECT REASONING FOR THE OTHER SOURCE. The
// draft signal is guarded by draftSeen: "Its later ABSENCE only means
// 'submitted' if it was there to begin with." The mirror of that sentence was
// never applied to the busy signal -- its later PRESENCE only means
// "submitted" if it was absent to begin with -- and these cases are that
// mirror.
//
// Both directions are pinned here. Making the busy source abstain is only
// correct if the ordinary transition still confirms, and a suite that pinned
// the abstention alone would be satisfied by a witness that never confirms
// anything.
//
//	go test ./internal/runtime/tmux/ -run PreexistingBusy

// alternatingSource returns a source answering from vals in order, repeating
// its last value once exhausted. Used to model a pane whose state CHANGES
// during the loop, which is the only way to distinguish a transition witness
// from a state witness.
func alternatingSource(vals ...bool) func() (bool, error) {
	i := 0
	return func() (bool, error) {
		v := vals[i]
		if i < len(vals)-1 {
			i++
		}
		return v, nil
	}
}

func TestPreexistingBusyIsNotReadAsASubmit(t *testing.T) {
	sends := 0
	confirmed, err := submitEnterAndConfirm(
		func() error { sends++; return nil },
		func() {},
		constantSource(true), // busy before the send and still busy after
		constantSource(true), // the draft is sitting in the composer, queued
		alwaysFalse,          // no overlay
		func(time.Duration) {},
	)
	if confirmed {
		t.Fatal("confirmed = true: the pane was busy BEFORE the submit key was sent, so its busyness cannot witness this message submitting -- this is the false success that parked three toolsmith sessions on ci-fo6au4")
	}
	if err != nil && !errors.Is(err, errSubmitQueuedBehindRun) {
		t.Fatalf("err = %v, want nil or errSubmitQueuedBehindRun", err)
	}
	if sends != 1 {
		t.Fatalf("sends = %d, want 1: a second submit key into a turn that is already running queues another copy of the message rather than delivering this one", sends)
	}
}

func TestPreexistingBusyStillConfirmsWhenTheDraftLeavesTheComposer(t *testing.T) {
	// The turn ended during the poll window and the provider submitted the
	// queued draft. The draft source is the one that can see that, and it must
	// keep deciding -- abstaining on busy must not blind the loop entirely.
	confirmed, err := submitEnterAndConfirm(
		func() error { return nil },
		func() {},
		constantSource(true),
		alternatingSource(true, false), // drafted at entry, gone once polled
		alwaysFalse,
		func(time.Duration) {},
	)
	if !confirmed {
		t.Fatalf("confirmed = false (err %v): the draft was observed leaving the composer, which is a submit however busy the pane was", err)
	}
}

func TestAnIdlePaneGoingBusyStillConfirms(t *testing.T) {
	// The ordinary case, and the regression this change most endangers. The
	// pane is idle when the key is sent and busy afterwards: a real
	// transition, and the signal the loop was built on.
	sends := 0
	confirmed, err := submitEnterAndConfirm(
		func() error { sends++; return nil },
		func() {},
		alternatingSource(false, true), // idle at entry, busy once polled
		alwaysFalse,                    // draft never seen, so that source abstains
		alwaysFalse,
		func(time.Duration) {},
	)
	if !confirmed || err != nil {
		t.Fatalf("confirmed = %v, err = %v: an idle pane that goes busy after the submit key is the transition this loop exists to observe", confirmed, err)
	}
	if sends != 1 {
		t.Fatalf("sends = %d, want 1: the transition was observed on the first send, so nothing may be re-sent", sends)
	}
}

func TestAPaneIdleThroughoutStillExhaustsItsResends(t *testing.T) {
	// The other regression direction. A pane that is idle at entry and never
	// goes busy is the lost-Enter case the re-send budget exists for, and
	// abstaining on PRE-EXISTING busy must not touch it.
	sends := 0
	confirmed, _ := submitEnterAndConfirm(
		func() error { sends++; return nil },
		func() {},
		alwaysFalse,
		alwaysFalse,
		alwaysFalse,
		func(time.Duration) {},
	)
	if sends != submitEnterMaxSends {
		t.Fatalf("sends = %d, want %d: an idle pane that never goes busy must still exhaust the re-send budget", sends, submitEnterMaxSends)
	}
	if confirmed {
		t.Fatal("confirmed = true: nothing ever observed a submit")
	}
}
