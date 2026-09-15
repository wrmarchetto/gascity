package tmux

import (
	"strconv"
	"testing"
	"time"
)

// Scope: the premise the durable poke record rests on -- that a poke recorded
// by one gc process is invisible to every other one. ci-49vlf3.
//
// This is the hole TestDiscountPokeActivity and TestPokeActivityRealTmux could
// not see. Both drive ONE Tmux value, so both go green while the discount is
// structurally unreachable in production: the process that sends the keystrokes
// (the `gc session nudge` CLI, or the per-session nudge poller) exits or never
// shares memory with the controller that later reads activity and decides a
// session is idle. Two independently constructed Tmux values over one session
// stand in for that boundary; they are the same thing the defect is, one
// address space closer.
//
// The controller half of the fix -- applying the discount to a poke read off
// the session bead -- is pinned in cmd/gc/idle_tracker_poke_test.go.
//
//	go test ./internal/runtime/tmux/ -run PokeDoesNotCross

// TestPokeDoesNotCrossProviderValues pins that Tmux.pokes is per-value state
// and that a reader without the record sees the RAW, echo-advanced activity.
// It must keep failing to discount: the day it starts discounting, the durable
// record has grown a second source of truth and the two can disagree.
func TestPokeDoesNotCrossProviderValues(t *testing.T) {
	t.Parallel()

	const sess = "wedged"
	// The echo is older than pokeGrace, because a poke still inside the grace
	// window is not discounted at all -- the sender's discount is this test's
	// control rig and a rig that cannot discount attributes nothing. Derived
	// from pokeGrace rather than a fixed literal so widening the window cannot
	// silently turn the control green-for-the-wrong-reason.
	now := time.Now().Truncate(time.Second)
	echo := now.Add(-4 * pokeGrace)
	genuine := now.Add(-3 * time.Hour)

	// tmux reports the keystroke echo, not agent output -- the whole premise.
	newAt := func(activity time.Time) *Tmux {
		return &Tmux{
			cfg:  DefaultConfig(),
			exec: &fakeExecutor{out: strconv.FormatInt(activity.Unix(), 10)},
		}
	}

	sender := newAt(echo)
	sender.recordPokeAt(sess, genuine, echo)

	if got, err := sender.GetSessionActivity(sess); err != nil {
		t.Fatalf("sender GetSessionActivity: %v", err)
	} else if !got.Equal(genuine) {
		t.Fatalf("sender activity = %v, want genuine %v; the sender's own discount is the control for this test and must work", got, genuine)
	}

	// A second value stands for the controller: same session, same tmux, no
	// poke on record.
	reader := newAt(echo)
	got, err := reader.GetSessionActivity(sess)
	if err != nil {
		t.Fatalf("reader GetSessionActivity: %v", err)
	}
	if !got.Equal(echo) {
		t.Fatalf("reader activity = %v, want the raw echo %v", got, echo)
	}
	if got.Equal(genuine) {
		t.Fatal("reader discounted a poke it never recorded; Tmux.pokes has become shared state and the durable record now has a rival source of truth")
	}
}

// TestLastPokeReportsOnlyRecordedPokes pins the sender's read-back contract.
// The absence of a poke MUST be reportable: a delivery that sent no keystrokes
// (ACP, hook transport) has nothing to discount, and a LastPoke that invented
// one would make the controller treat genuine agent output as gc's own echo.
func TestLastPokeReportsOnlyRecordedPokes(t *testing.T) {
	t.Parallel()

	tm := &Tmux{cfg: DefaultConfig(), exec: &fakeExecutor{out: "0"}}

	if _, ok := tm.LastPoke("never-poked"); ok {
		t.Fatal("LastPoke reported a poke for a session that was never poked")
	}

	at := time.Now().Truncate(time.Second)
	prior := at.Add(-time.Hour)
	tm.recordPokeAt("poked", prior, at)

	pk, ok := tm.LastPoke("poked")
	if !ok {
		t.Fatal("LastPoke reported nothing for a recorded poke")
	}
	if !pk.At.Equal(at) || !pk.Prior.Equal(prior) {
		t.Fatalf("LastPoke = %+v, want {At:%v Prior:%v}", pk, at, prior)
	}
	if !pk.Complete() {
		t.Fatal("a recorded poke reports incomplete; nothing downstream would ever stamp it")
	}
}
