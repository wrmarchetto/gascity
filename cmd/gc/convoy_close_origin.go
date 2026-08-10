package main

// Convoy autoclose attribution: which collector closed a convoy, and who ran
// it.
//
// A convoy bead reaching closed says a collector ran. It did not used to say
// WHICH one: three independent collectors all stamped the single string
// "convoy autoclose: all children closed", so a closed convoy was
// unattributable. That is not a cosmetic gap -- ci-zz26 read the one observed
// autoclose as evidence the event-driven handler worked, when the close had
// come from an operator running `gc convoy check` by hand and the handler had
// never fired at all. Two premises of that investigation had to be corrected
// mid-flight because the close record could not tell the two apart (ci-eh7h).
//
// This is the convoy-side counterpart of internal/session/drain_origin.go
// (ci-wxag), and it mirrors that file's shape deliberately: an unrecorded zero
// value that is nobody's synonym, normalization of unknown values back to it,
// and one render branch per actor. The convoy seam is smaller because
// closeConvoyWithReason already took the reason as a parameter -- only the
// callers were collapsed.
//
// Why the actor rides the bead and not just the ConvoyClosed event: the event
// already carries eventActor(), but the event log rotates and the convoy bead
// does not. The requirement is that a convoy closed weeks ago still names its
// collector, which rules out the log as the record of ORIGIN -- the same
// reasoning that put drain origin on the session bead instead of in runtime
// provider metadata.
//
// Editing constraint: every branch of convoyAutocloseReasonFor must clear bd's
// validation.on-close=error 20-character floor, or that branch cannot close a
// convoy in a city running the validator. Both that floor and the
// one-record-per-call-site rule are verified by convoy_close_origin_test.go.

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// convoyCloseOrigin identifies which collector auto-closed a convoy.
type convoyCloseOrigin string

const (
	// convoyCloseOriginUnrecorded is a close whose collector nothing captured:
	// a convoy closed before this record existed, or a future call site that
	// declared no origin. It is NOT a synonym for any real collector --
	// rendering it as one is the bug this type exists to prevent.
	convoyCloseOriginUnrecorded convoyCloseOrigin = ""

	// convoyCloseOriginSweep is the `gc convoy check` bulk sweep. It covers
	// both an operator running the sweep by hand and a deployment's
	// convoy-autoclose-leak doctor fix running it: the fix execs `gc convoy
	// check` verbatim, so nothing INSIDE the sweep can distinguish them. A
	// fourth origin for the doctor case was rejected for that reason -- it
	// would have to be plumbed in as a flag or an environment variable, and
	// the actor key already separates them from data gc resolves anyway.
	convoyCloseOriginSweep convoyCloseOrigin = "sweep"

	// convoyCloseOriginController is the controller's in-process bead.closed
	// handler (runBeadCloseAutoclose), which replaced the shell on_close hook
	// chain in gastownhall/gascity#3248. This is the only origin that proves
	// the automatic mechanism fired on its own, which is the claim ci-zz26
	// could not check.
	convoyCloseOriginController convoyCloseOrigin = "controller"

	// convoyCloseOriginCloseHook is the standalone `gc convoy autoclose <id>`
	// CLI, the pre-#3248 bd on_close hook entry point. Kept distinct from the
	// controller origin rather than folded into it even though the two share
	// doConvoyAutocloseWith: a close attributed here is evidence the legacy
	// hook is still wired somewhere, and the controller origin would hide it.
	convoyCloseOriginCloseHook convoyCloseOrigin = "close-hook"
)

const (
	// convoyCloseOriginMetadataKey holds a convoyCloseOrigin on the convoy bead.
	convoyCloseOriginMetadataKey = "convoy_close_origin"

	// convoyCloseActorMetadataKey holds the eventActor() identity that ran the
	// collector -- an agent alias, a session id, or "human". It answers a
	// different question from the origin: the origin is WHICH mechanism, the
	// actor is WHO drove it. Keeping them separate is what lets a hand-run
	// sweep be told from the doctor fix's sweep without inventing an origin
	// for each caller of `gc convoy check`.
	convoyCloseActorMetadataKey = "convoy_close_actor"
)

// normalizeConvoyCloseOrigin maps a raw value onto a known origin. An
// unrecognized value normalizes to convoyCloseOriginUnrecorded rather than
// passing through, so a typo or a value written by a newer gc cannot render as
// an authoritative collector.
func normalizeConvoyCloseOrigin(raw string) convoyCloseOrigin {
	switch convoyCloseOrigin(strings.TrimSpace(raw)) {
	case convoyCloseOriginSweep:
		return convoyCloseOriginSweep
	case convoyCloseOriginController:
		return convoyCloseOriginController
	case convoyCloseOriginCloseHook:
		return convoyCloseOriginCloseHook
	}
	return convoyCloseOriginUnrecorded
}

// convoyAutocloseReasonFor renders the close_reason prose for a convoy
// auto-closed by the given collector. Each branch names the collector in terms
// an operator reading `bd show` can act on -- the command to re-run, or the
// handler to go look at -- rather than echoing the enum value, which is already
// available verbatim under convoyCloseOriginMetadataKey.
func convoyAutocloseReasonFor(origin convoyCloseOrigin) string {
	switch normalizeConvoyCloseOrigin(string(origin)) {
	case convoyCloseOriginSweep:
		return "convoy autoclose: collected by the gc convoy check sweep"
	case convoyCloseOriginController:
		return "convoy autoclose: collected by the controller bead.closed handler"
	case convoyCloseOriginCloseHook:
		return "convoy autoclose: collected by the gc convoy autoclose hook"
	}
	return "convoy autoclose: collector not recorded"
}

// closeConvoyWithOrigin closes an auto-collected convoy and records who
// collected it: the origin-specific close_reason plus the machine-readable
// (origin, actor) pair behind it.
//
// All three keys go in one SetMetadataBatch, before the close, so a closed
// convoy can never carry a close_reason naming one collector and an origin
// naming another. Stamping them after the close is the obvious alternative and
// it reopens exactly the window this record exists to eliminate -- a closed,
// unattributable convoy, indistinguishable from one closed by an older gc.
//
// It does not reuse closeConvoyWithReason: that path takes prose only, and the
// manual and land closes that still use it are already unambiguous by
// construction, having exactly one call site each.
func closeConvoyWithOrigin(store beads.Store, id string, origin convoyCloseOrigin) error {
	origin = normalizeConvoyCloseOrigin(string(origin))
	reason := convoyAutocloseReasonFor(origin)

	// An unrecorded origin writes an EMPTY value rather than omitting the key.
	// Omitting it is the tidier-looking option and it is wrong: a convoy that
	// was closed, reopened, and re-collected would keep the previous
	// incarnation's origin, which is a stale attribution presented as a current
	// one -- the exact failure this record exists to prevent. Same rule as
	// session.DrainAckOriginPatch. Pinned by
	// TestCloseConvoyWithOriginClearsAStaleOriginOnRecollect.
	attribution := map[string]string{
		"close_reason":               reason,
		convoyCloseOriginMetadataKey: string(origin),
		convoyCloseActorMetadataKey:  eventActor(),
	}
	if err := store.SetMetadataBatch(id, attribution); err != nil {
		return fmt.Errorf("stamping convoy %s close attribution: %w", id, err)
	}

	if closer, ok := store.(explicitReasonCloser); ok {
		return closer.CloseWithReason(id, reason)
	}
	return store.Close(id)
}
