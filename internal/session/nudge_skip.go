package session

import (
	"errors"

	"github.com/gastownhall/gascity/internal/runtime"
)

// NudgeSkip names why a best-effort live nudge was not delivered.
//
// The wait-idle nudge paths are allowed to decline: a session that is not
// running, a provider with no pane to write to, a runtime that cannot observe
// an idle boundary, and a session that is mid-turn are all ordinary outcomes a
// caller answers by falling back to the nudge queue. What was NOT ordinary is
// that every one of them returned (false, nil) and was therefore
// indistinguishable at the caller. `gc mail send --notify` reported success
// for a message that reached nobody, because "not delivered" and "delivered"
// were the only two facts it could observe (ci-7b1ueb).
//
// REJECTED: returning an error instead. None of these is an operational
// failure, callers legitimately continue past all of them, and an error would
// force every caller to re-classify the ones it is happy to ignore. The
// alternative a reader reaches for second -- matching on the provider's error
// text -- is what the runtime.ErrIdleTimeout sentinel exists to avoid.
//
// The empty value means delivery happened. Nothing in this package returns a
// true delivered flag alongside a non-empty skip, and
// TestEveryUndeliveredWaitIdleNudgeNamesASkipReason in nudge_skip_test.go is
// what holds that.
type NudgeSkip string

const (
	// NudgeSkipNone is the zero value and means the nudge was delivered.
	NudgeSkipNone NudgeSkip = ""
	// NudgeSkipNotRunning means the runtime had no live session to write to.
	NudgeSkipNotRunning NudgeSkip = "session_not_running"
	// NudgeSkipProviderUnsupported means the session's provider has no
	// wait-idle nudge path. Only claude-kind sessions do.
	NudgeSkipProviderUnsupported NudgeSkip = "provider_not_nudgeable"
	// NudgeSkipNoIdleWait means the runtime cannot observe an idle boundary,
	// so a safe moment to write can never be established.
	NudgeSkipNoIdleWait NudgeSkip = "runtime_cannot_wait_for_idle"
	// NudgeSkipBusy means the session was live and reachable but still
	// mid-turn when the wait expired. This is the recoverable one: the
	// message is queued and the session sees it at its next surfacing point.
	NudgeSkipBusy NudgeSkip = "session_busy_mid_turn"
	// NudgeSkipNudgeWriteFailed means an idle boundary was reached and the
	// write to the session itself failed.
	NudgeSkipNudgeWriteFailed NudgeSkip = "nudge_write_failed"
	// NudgeSkipIdleWaitFailed means the wait for an idle boundary failed for
	// a reason that is not a timeout -- the session went away underneath it,
	// or the runtime could not read the pane. Distinct from NudgeSkipBusy
	// because a queued message reaches a busy session later and does not
	// reach one that is gone.
	NudgeSkipIdleWaitFailed NudgeSkip = "idle_wait_failed"
	// NudgeSkipUnclassified means the nudge did not happen and no path named
	// a reason. Today that is only the session lookup failing before any
	// nudge path runs, which also returns an error.
	//
	// It exists so an undelivered nudge can never carry an EMPTY reason: the
	// empty value means delivered, so a caller printing the reason would
	// print nothing at the one moment it matters.
	NudgeSkipUnclassified NudgeSkip = "unclassified"
)

// ClassifyIdleWaitFailure names the skip for a failed wait-idle.
//
// Only a genuine timeout is reported as a mid-turn session. Everything else
// the wait can return -- an unsupported runtime, a session that vanished, a
// pane that could not be read -- is a different fact for the sender, and
// calling all of them "busy" would restate the collapse this type exists to
// undo one level down.
func ClassifyIdleWaitFailure(err error) NudgeSkip {
	switch {
	case err == nil:
		return NudgeSkipNone
	case errors.Is(err, runtime.ErrIdleTimeout):
		return NudgeSkipBusy
	case errors.Is(err, runtime.ErrInteractionUnsupported):
		return NudgeSkipNoIdleWait
	default:
		return NudgeSkipIdleWaitFailed
	}
}

// resolveNudgeSkip keeps the delivered flag and the skip from contradicting.
//
// A delivered nudge always reports NudgeSkipNone, and an undelivered one
// always reports something. Callers read the two together, so a pair that
// disagrees is worse than either alone.
func resolveNudgeSkip(delivered bool, skip NudgeSkip, err error) NudgeSkip {
	if delivered && err == nil {
		return NudgeSkipNone
	}
	if skip == NudgeSkipNone {
		return NudgeSkipUnclassified
	}
	return skip
}

// Delivered reports whether the skip represents a delivered nudge.
func (s NudgeSkip) Delivered() bool { return s == NudgeSkipNone }

// Explain renders the skip for an operator, in the second person about the
// recipient. It returns "" for NudgeSkipNone so a caller cannot print a
// non-explanation for a delivery that happened.
//
// The strings say what the sender can DO about it, because the case that
// motivated this type is one where the sender had already moved on believing
// the message landed.
func (s NudgeSkip) Explain() string {
	switch s {
	case NudgeSkipNone:
		return ""
	case NudgeSkipNotRunning:
		return "the session is not running"
	case NudgeSkipProviderUnsupported:
		return "the session's provider has no live-nudge path"
	case NudgeSkipNoIdleWait:
		return "the runtime cannot detect an idle prompt"
	case NudgeSkipBusy:
		return "the session is mid-turn and cannot be interrupted safely"
	case NudgeSkipNudgeWriteFailed:
		return "writing to the session failed"
	case NudgeSkipIdleWaitFailed:
		return "the session could not be observed for an idle prompt"
	case NudgeSkipUnclassified:
		return "the session could not be looked up"
	default:
		return string(s)
	}
}
