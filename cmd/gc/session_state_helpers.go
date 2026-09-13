package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

func isDrainedSessionMetadata(meta map[string]string) bool {
	state := strings.TrimSpace(meta["state"])
	if state == "drained" {
		return true
	}
	return state == "asleep" && strings.TrimSpace(meta["sleep_reason"]) == string(sessionpkg.SleepReasonDrained)
}

func isDrainedSessionBead(session beads.Bead) bool {
	return isDrainedSessionMetadata(session.Metadata)
}

// isDrainedSessionInfo is the session.Info mirror of isDrainedSessionBead. It
// reads the RAW metadata state (Info.MetadataState) and sleep reason
// (Info.SleepReason), matching the bead form's untrimmed-key reads.
func isDrainedSessionInfo(i sessionpkg.Info) bool {
	state := strings.TrimSpace(i.MetadataState)
	if state == "drained" {
		return true
	}
	return state == "asleep" && strings.TrimSpace(i.SleepReason) == string(sessionpkg.SleepReasonDrained)
}

// poolSessionIsLiveInfo reports whether a pool session represents an actively
// running session for the runningSessions counter in build_desired_state. An
// asleep or drained session is not live — it holds no active process and must
// not suppress the isCold cross-store wake probe. It reads the RAW state
// metadata (Info.MetadataState) and delegates the drained/asleep-drained check
// to isDrainedSessionInfo, matching the untrimmed-key reads the bead carried.
func poolSessionIsLiveInfo(i sessionpkg.Info) bool {
	if strings.TrimSpace(i.MetadataState) == "asleep" {
		return false
	}
	if isDrainedSessionInfo(i) {
		return false
	}
	return true
}

// isPoolSessionSlotFreeable reports whether a session's bead is in a terminal
// state where the pool slot it occupies can be freed — either explicitly
// drained, or asleep from a normal idle transition. Sessions parked via
// `gc session wait` (sleep_reason=wait-hold), held by context-churn
// quarantine, or otherwise signaling "don't touch me" keep their slot.
//
// Distinct from `isDrainedSessionBead` because drain-ack can land pool
// workers in state=asleep+sleep_reason=idle when the pre-close ownership
// snapshot falsely reports assigned work. Freeing the slot for idle-asleep
// pool beads lets the supervisor spawn a fresh worker for ready queue work
// instead of stranding it on a ghost slot.
//
// A session parked with sleep_reason=provider-terminal-error is also freeable:
// markProviderTerminalError has classified it as a dead, non-retryable provider
// failure, so its slot must be reaped — otherwise the dead bead and its worktree
// leak indefinitely while still excluded from pool capacity.
//
// A session stopped by the assigned-work-exhausted backstop is freeable for
// the same reason as idle: DecideAssignedWorkExhausted fires only after the
// session has re-deferred the idle kill on one anchor bead past the
// consecutive-defer limit, which is the reconciler concluding that this
// incarnation will not make progress. Leaving its slot occupied made the
// conclusion unactionable -- on a canonical singleton pool the alias IS the
// queue address, so the dead holder absorbed every later assignment while
// nothing could wake it (ComputeAwakeSet matches no alias) and nothing could
// replace it (its resume request spends the only cap slot). Measured on the
// live city 2026-09-13: bench-engineer sessions ci-5co05c and ci-v2ocb2 each
// held the alias ~4h against a ready P1, freed only by a hand-run
// `gc session close` (ci-l38chb). The close itself stays gated on the
// session owning no claim -- see the pool-slot-release probe at the
// reconciler's poolFreeable site.
//
// ABSENT, and each absence is a decision:
//
//   - max-session-age. The same starvation is REACHABLE through it, but
//     DecideMaxSessionAge has no override for its assigned-work defer rung, so
//     it can only stop a session that holds nothing, and no occurrence has
//     been measured. Predicted, not observed; adding it needs its own evidence.
//   - context-churn, quarantine, rate_limit. Each parks a bead the recovery
//     path still needs: churn feeds the circuit breaker, a quarantined bead
//     must keep consuming pool demand for its whole window
//     (reusablePoolSessionInfo says why), and a rate-limited session resumes
//     when its account window resets. Freeing any of them spawns a
//     replacement into the condition the park exists to wait out.
//   - user-hold, wait-hold. Deliberate parks; freeing them discards an
//     operator's or a human gate's intent.
//
// An explicit sleep_reason is required: deny-by-default for unknown or
// missing reasons so writes that land in state=asleep without a known
// reason (legacy beads, regressions, write races) cannot silently free
// their slot.
func isPoolSessionSlotFreeable(session beads.Bead) bool {
	if isDrainedSessionBead(session) {
		return true
	}
	if strings.TrimSpace(session.Metadata["state"]) != "asleep" {
		return false
	}
	reason := strings.TrimSpace(session.Metadata["sleep_reason"])
	switch reason {
	case string(sessionpkg.SleepReasonIdle), string(sessionpkg.SleepReasonIdleTimeout),
		string(sessionpkg.SleepReasonCityStop), string(sessionpkg.SleepReasonFailedCreate),
		string(sessionpkg.SleepReasonRuntimeMissing), string(sessionpkg.SleepReasonProviderTerminalError),
		string(sessionpkg.SleepReasonAssignedWorkExhausted):
		return true
	}
	return false
}

// isPoolSessionSlotFreeableInfo is the session.Info mirror of isPoolSessionSlotFreeable.
func isPoolSessionSlotFreeableInfo(i sessionpkg.Info) bool {
	if isDrainedSessionInfo(i) {
		return true
	}
	if strings.TrimSpace(i.MetadataState) != "asleep" {
		return false
	}
	reason := strings.TrimSpace(i.SleepReason)
	switch reason {
	case string(sessionpkg.SleepReasonIdle), string(sessionpkg.SleepReasonIdleTimeout),
		string(sessionpkg.SleepReasonCityStop), string(sessionpkg.SleepReasonFailedCreate),
		string(sessionpkg.SleepReasonRuntimeMissing), string(sessionpkg.SleepReasonProviderTerminalError),
		string(sessionpkg.SleepReasonAssignedWorkExhausted):
		return true
	}
	return false
}
