package runtime

import "time"

// A "poke" is gc's own synthetic input into an agent's terminal -- the
// send-keys a wake or a nudge delivers. tmux advances #{window_activity} on
// that keystroke echo exactly as it does on agent output, so a woken-but-dead
// agent reads as perpetually active unless the echo is discounted. The two
// pure functions below own that discount; they live here, not in the tmux
// provider, because the process that SENDS the poke is never the process that
// later reads activity and decides a session is idle.
//
// Rejected: keeping the discount private to internal/runtime/tmux and having
// the reader ask the provider. The provider's poke record is an in-process
// map, so the controller's copy is always empty -- which is the defect
// (ci-49vlf3): every nudge granted the session another full idle_timeout of
// immunity because the controller read the raw, echo-advanced value. The poke
// has to travel as DATA, and both halves of it travel: `At` alone cannot say
// what the genuine activity was.

const (
	// PokeEcho is the window within which raw terminal activity is treated as
	// the poke's own keystroke echo rather than agent output.
	PokeEcho = 3 * time.Second
	// PokeGrace is how long a just-poked agent still counts as active, so a
	// responsive agent about to reply is not flipped to idle. After it elapses
	// with no agent output, the poke is discounted.
	PokeGrace = 15 * time.Second
)

// Poke records one gc-initiated send-keys to a session: when it happened, and
// the genuine session activity observed just before it.
//
// Prior is not redundant with At. Discounting only answers "the raw timestamp
// is our own echo"; it still has to name the activity that WAS genuine, and
// only the sender saw it. A Poke carrying a zero At or a zero Prior is
// incomplete and declines to discount anything -- which is the pre-fix
// behavior, so a half-written durable record fails open rather than idle-
// killing a live session.
type Poke struct {
	At    time.Time // when gc sent the keystrokes
	Prior time.Time // genuine activity immediately before the poke
}

// Complete reports whether pk carries both halves and can therefore discount.
func (pk Poke) Complete() bool {
	return !pk.At.IsZero() && !pk.Prior.IsZero()
}

// PokeReporter is an optional extension for runtimes that deliver input as
// synthetic keystrokes and therefore record pokes. A runtime that delivers
// out of band (ACP, subprocess) implements nothing here, and callers MUST
// treat that absence as "no poke happened" rather than synthesizing one:
// stamping a poke for a delivery that never touched the terminal would
// suppress genuine idle detection.
type PokeReporter interface {
	// LastPoke returns the most recent poke this process recorded for the
	// session, and whether one exists.
	LastPoke(session string) (Poke, bool)
}

// DiscountPokeActivity resolves the genuine activity time from the raw
// terminal activity (activity), the last recorded poke (pk) and the current
// time.
//
// If activity is only the poke's own keystroke echo (within PokeEcho of the
// poke) AND the grace window has elapsed with no later agent output, it
// returns the activity seen before the poke -- revealing that the agent never
// actually responded. Otherwise activity stands (a real post-poke turn, or a
// still-in-grace recent poke). Pure function for testability.
func DiscountPokeActivity(activity time.Time, pk Poke, now time.Time) time.Time {
	if !pk.Complete() {
		return activity
	}
	echoOnly := activity.Sub(pk.At).Abs() <= PokeEcho
	graceElapsed := now.Sub(pk.At) >= PokeGrace
	if echoOnly && graceElapsed {
		return pk.Prior
	}
	return activity
}

// PokePriorBaseline selects the genuine activity to record as a new poke's
// prior. When an earlier poke is on record and the current raw activity is
// only that poke's own echo (raw within PokeEcho of the earlier poke, i.e. no
// genuine agent output since), the last genuine activity is the earlier poke's
// prior, so it is carried forward. This stops chained unanswered nudges inside
// PokeGrace from recording gc's own earlier nudge echo as the new baseline --
// which DiscountPokeActivity would otherwise later surface as last activity,
// masking a stalled agent. Otherwise the freshly observed raw activity is
// genuine and becomes the new prior. Pure function for testability.
func PokePriorBaseline(raw time.Time, pk Poke, hasPoke bool) time.Time {
	if hasPoke && pk.Complete() && raw.Sub(pk.At).Abs() <= PokeEcho {
		return pk.Prior
	}
	return raw
}
