package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: the cross-process half of the send-keys activity discount, as the
// controller's idle check sees it. ci-49vlf3.
//
// These tests exist because the discount's existing coverage cannot reach the
// defect. internal/runtime/tmux's poke tests drive one provider value holding
// one in-process map, and the whole failure is that the map belongs to the
// process that SENT the keystrokes -- the nudge CLI -- while the process that
// reads activity and decides a session is idle is the controller, whose map is
// permanently empty. A suite that never crosses that boundary goes green while
// every nudge grants the session another full idle_timeout of immunity.
//
// So the poke here arrives the way it arrives in production: as DATA off the
// session bead, not from the provider. TestTmuxPokeDoesNotCrossProviderValues
// (internal/runtime/tmux) pins the premise these rest on.
//
//	go test ./cmd/gc/ -run 'IdleTrackerDurablePoke|IdleTimeoutSurvives'

// TestIdleTrackerDurablePokeRevealsUnansweredNudge pins the fix: a session
// whose only recent "activity" is gc's own nudge echo must still be seen as
// idle by a process that did not send that nudge.
//
// Constructed from the measured production shape: the agent last produced
// output three hours ago, the nudge landed seconds ago and advanced the
// terminal activity clock, and the timeout is two hours. Expected values are
// derived from the timeout and the ages, never from the discount's own
// constants, so a change to PokeEcho/PokeGrace cannot quietly make this agree
// with a broken implementation.
func TestIdleTrackerDurablePokeRevealsUnansweredNudge(t *testing.T) {
	t.Parallel()

	const timeout = 2 * time.Hour
	now := time.Now()
	genuine := now.Add(-3 * time.Hour) // the agent's last real turn
	nudgedAt := now.Add(-time.Minute)  // gc send-keys, well past PokeGrace

	it := newIdleTracker()
	it.setTimeout("wedged", timeout)

	sp := runtime.NewFake()
	startFakeSession(t, sp, "wedged")
	// What tmux reports: the keystroke echo, not agent output.
	sp.SetActivity("wedged", nudgedAt)

	poke := runtime.Poke{At: nudgedAt, Prior: genuine}
	if !it.checkIdle("wedged", "", sp, now, poke) {
		t.Fatalf("checkIdle = false with a durable poke on record; want true "+
			"(genuine activity %v is %v old against a %v timeout -- the nudge echo at %v is not a turn)",
			genuine, now.Sub(genuine), timeout, nudgedAt)
	}
}

// TestIdleTrackerDurablePokeSpareResponsiveAgent is the other side of the
// same gate: an agent that DID answer the nudge must not be idle-killed. Its
// output lands after the echo window, so the raw activity stands and the
// discount must not reach back to the pre-nudge baseline.
func TestIdleTrackerDurablePokeSparesResponsiveAgent(t *testing.T) {
	t.Parallel()

	now := time.Now()
	genuine := now.Add(-3 * time.Hour)
	nudgedAt := now.Add(-10 * time.Minute)
	// A real turn, comfortably outside the echo window around the nudge.
	answeredAt := nudgedAt.Add(2 * time.Minute)

	it := newIdleTracker()
	it.setTimeout("responsive", 2*time.Hour)

	sp := runtime.NewFake()
	startFakeSession(t, sp, "responsive")
	sp.SetActivity("responsive", answeredAt)

	if it.checkIdle("responsive", "", sp, now, runtime.Poke{At: nudgedAt, Prior: genuine}) {
		t.Fatalf("checkIdle = true for an agent that answered at %v (%v ago); want false",
			answeredAt, now.Sub(answeredAt))
	}
}

// TestIdleTrackerDurablePokeRespectsGraceWindow pins the other half of the
// spare: a nudge delivered seconds ago has not been answered yet, and an agent
// that is about to reply must not be idle-killed while it types. The window is
// derived from runtime.PokeGrace rather than a literal, so widening the grace
// cannot leave this test agreeing with an implementation that ignores it.
func TestIdleTrackerDurablePokeRespectsGraceWindow(t *testing.T) {
	t.Parallel()

	now := time.Now()
	genuine := now.Add(-3 * time.Hour)
	nudgedAt := now.Add(-runtime.PokeGrace / 2)

	it := newIdleTracker()
	it.setTimeout("just-nudged", 2*time.Hour)

	sp := runtime.NewFake()
	startFakeSession(t, sp, "just-nudged")
	sp.SetActivity("just-nudged", nudgedAt)

	if it.checkIdle("just-nudged", "", sp, now, runtime.Poke{At: nudgedAt, Prior: genuine}) {
		t.Fatalf("checkIdle = true %v after the nudge; want false until the %v grace window elapses",
			now.Sub(nudgedAt), runtime.PokeGrace)
	}
}

// TestIdleTrackerIncompletePokeFailsOpen pins the degradation contract: a
// half-written or unparsable durable record must behave exactly as no record
// does. A poke missing its prior cannot name the genuine activity, and
// guessing one would idle-kill live sessions -- the opposite and worse defect.
func TestIdleTrackerIncompletePokeFailsOpen(t *testing.T) {
	t.Parallel()

	now := time.Now()
	nudgedAt := now.Add(-time.Minute)

	it := newIdleTracker()
	it.setTimeout("halfwritten", 2*time.Hour)

	sp := runtime.NewFake()
	startFakeSession(t, sp, "halfwritten")
	sp.SetActivity("halfwritten", nudgedAt)

	for _, tc := range []struct {
		name string
		poke runtime.Poke
	}{
		{"no record at all", runtime.Poke{}},
		{"at without prior", runtime.Poke{At: nudgedAt}},
		{"prior without at", runtime.Poke{Prior: now.Add(-3 * time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if it.checkIdle("halfwritten", "", sp, now, tc.poke) {
				t.Fatalf("checkIdle = true for %s; want false (an incomplete record must not discount)", tc.name)
			}
		})
	}
}

// TestInfoDurablePokeRoundTripsThroughMetadata pins the wire between the two
// processes: what StampPokePatch writes is what DurablePoke reads back.
// Written and read by different binaries, so a suite testing each end against
// its own literal would agree with any pair of keys, matching or not.
func TestInfoDurablePokeRoundTripsThroughMetadata(t *testing.T) {
	t.Parallel()

	// Whole seconds: RFC3339 as stamped carries no sub-second part, and the
	// terminal activity clock this is compared against has 1s granularity.
	want := runtime.Poke{
		At:    time.Date(2026, 9, 15, 5, 18, 3, 0, time.UTC),
		Prior: time.Date(2026, 9, 15, 2, 4, 9, 0, time.UTC),
	}

	var info session.Info
	got := info.ApplyPatch(session.StampPokePatch(want)).DurablePoke()

	if !got.At.Equal(want.At) || !got.Prior.Equal(want.Prior) {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	if !got.Complete() {
		t.Fatal("round-tripped poke reports incomplete; the discount would never fire")
	}
}
