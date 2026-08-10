package session

import (
	"fmt"
	"strings"
)

// Drain origin: who decided a drained session should stop.
//
// A session bead reaching state "drained" says the drain completed; it does not
// say who started it. Two very different events produce that state: an agent
// that booted, found nothing claimable, and retired itself, and the reconciler
// deciding the slot was surplus. Before this record existed both rendered
// CanonicalCloseReason("drained"), whose text named the reconciler outright --
// so the self-drain case read as a reconciler decision and sent the first
// investigation of ci-fh4o into the wrong subsystem (see ci-wxag).
//
// The origin is stamped on the session bead rather than left in runtime
// provider metadata (GC_DRAIN_ACK_SOURCE) because that metadata lives in the
// tmux session environment and dies with the runtime. The reconciler stops the
// runtime one tick and finalizes the close on a later one, by which point the
// only surviving record is the bead.
//
// The two keys and DrainedCloseReason are verified together by
// TestDrainedCloseReasonSeparatesSelfFromReconciler in drain_origin_test.go and
// by TestDrainAckOriginPatchRoundTripsThroughInfo in info_codec_test.go.

// DrainOrigin identifies who acknowledged a drain.
type DrainOrigin string

const (
	// DrainOriginUnrecorded is a drain whose origin nothing captured: a bead
	// closed before this record existed, or a finalize whose runtime metadata
	// was already gone. It is NOT a synonym for either real origin -- rendering
	// it as one is the bug this type exists to prevent.
	DrainOriginUnrecorded DrainOrigin = ""
	// DrainOriginSelf is an agent that acknowledged its own drain, via
	// `gc runtime drain-ack` or `gc hook --claim --drain-ack`.
	DrainOriginSelf DrainOrigin = "self"
	// DrainOriginReconciler is a drain the reconciler started and acknowledged
	// on the session's behalf.
	DrainOriginReconciler DrainOrigin = "reconciler"
)

const (
	// DrainOriginMetadataKey holds a DrainOrigin on the session bead.
	DrainOriginMetadataKey = "drain_origin"
	// DrainAckReasonMetadataKey holds the origin's own reason code verbatim --
	// "no_work"/"claims_errored" from the agent, "idle"/"config-drift"/
	// "orphaned"/"suspended"/"no-wake-reason" from the reconciler. The two
	// vocabularies are deliberately NOT merged into one enum: the pair
	// (origin, reason) is the decision record, and a shared enum would let a
	// future reason be read as coming from the wrong actor.
	DrainAckReasonMetadataKey = "drain_ack_reason"
)

// NormalizeDrainOrigin maps a raw metadata value onto a known origin. An
// unrecognized value normalizes to DrainOriginUnrecorded rather than being
// passed through, so a typo or a value from a newer gc cannot render as an
// authoritative actor.
func NormalizeDrainOrigin(raw string) DrainOrigin {
	switch DrainOrigin(strings.TrimSpace(raw)) {
	case DrainOriginSelf:
		return DrainOriginSelf
	case DrainOriginReconciler:
		return DrainOriginReconciler
	}
	return DrainOriginUnrecorded
}

// DrainAckOriginPatch records who acknowledged a drain and why. An empty origin
// or reason writes an empty value, which clears any stale marker from a
// previous incarnation on the same bead -- MetadataPatch treats "" as a delete,
// so omitting the key instead would leave the stale value in place.
func DrainAckOriginPatch(origin DrainOrigin, reason string) MetadataPatch {
	return MetadataPatch{
		DrainOriginMetadataKey:    string(NormalizeDrainOrigin(string(origin))),
		DrainAckReasonMetadataKey: strings.TrimSpace(reason),
	}
}

// DrainAckCloseOverlay is the terminal metadata a drain-ack close adds on top of
// ClosePatch: the origin-specific close_reason plus the machine-readable
// (origin, reason) pair behind it. It is an overlay rather than a whole close
// patch so `state` stays the short canonical code "drained" -- reconciler logic
// and closedNamedSessionReopenEligible switch on that field, and encoding the
// origin there instead would have changed what those reads mean.
//
// The prose and the pair are written together, from one call, so a closed bead
// can never carry a close_reason naming one actor and a drain_origin naming
// another.
func DrainAckCloseOverlay(origin DrainOrigin, reason string) MetadataPatch {
	overlay := DrainAckOriginPatch(origin, reason)
	overlay["close_reason"] = DrainedCloseReason(origin, reason)
	return overlay
}

// DrainedCloseReason renders the close_reason for a session closing out of a
// drain, naming the actor that decided it. The reason code is passed through
// verbatim rather than mapped to prose: a lookup table would have to be
// extended in lockstep with every new drain reason on both sides, and the one
// that was missed would silently render as the generic text.
//
// Every branch clears bd's 20-character validation.on-close floor with the
// reason omitted, so a reasonless origin still closes under
// validation.on-close=error (pinned by
// TestDrainedCloseReasonMeetsValidatorThreshold).
func DrainedCloseReason(origin DrainOrigin, reason string) string {
	reason = strings.TrimSpace(reason)
	switch NormalizeDrainOrigin(string(origin)) {
	case DrainOriginSelf:
		if reason == "" {
			return "session drained: agent retired itself (self)"
		}
		return fmt.Sprintf("session drained: agent retired itself (self: %s)", reason)
	case DrainOriginReconciler:
		if reason == "" {
			return "session retired: reconciler reclaimed the slot (reconciler)"
		}
		return fmt.Sprintf("session retired: reconciler reclaimed the slot (reconciler: %s)", reason)
	}
	return "session drained: drain origin not recorded"
}
