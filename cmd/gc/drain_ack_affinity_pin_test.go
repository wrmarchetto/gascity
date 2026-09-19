// Package main test: whether a session-affinity pin on OPEN work parked on a
// pool slot alias is read as proof that a live session HOLDS that work.
//
// Scope: reconcileSessionBeads over a MemStore, asserting the state_reason the
// drain-ack arm writes. Every case here is alias-assigned, open and pinned, and
// they differ only in WHO the pin names -- nobody, a predecessor, or this
// session. The corroborating half of the same fix, that a claiming session
// stamps its back-reference onto the continuation siblings it is handed, is
// pinned in cmd_hook_claim_test.go; the Stop gate's mirror of this predicate is
// pinned in cmd_hook_stop_test.go.
//
// WHY THIS SUITE EXISTS. A bench-engineer pool slot was wedged from 2026-09-12
// to 2026-09-18 by ci-7tn14g: a graph step routed at the pool, assigned to the
// bare pool alias, carrying gc.session_affinity=require and
// gc.continuation_group, and never claimed by anything. isUnpinnedQueuedWorkBead
// read those keys as proof a live session held it, so every occupant of that
// slot had its drain acknowledgement refused over work no session had ever
// taken, and 16 session.drain_acked_with_assigned_work events were recorded
// against that one bead. graphroute writes those keys at ROUTE time
// (graphroute.go, "stamp continuation group so preassignHookContinuationGroup
// keeps all steps together"), which makes them a requirement to run on one
// session -- not a record that one does. A requirement is not a holder
// (ci-d1huhf).
//
// WHAT IT DELIBERATELY DOES NOT PIN. It says nothing about hold labels: this
// gate is assignee-scoped and therefore hold-transparent by design
// (internal/beadmeta/hold_labels.go). It says nothing about non-pool sessions
// either -- poolQueueAliasIdentities returns an empty set for those, so the
// whole exclusion is inert there, and
// TestReconcileSessionBeads_AgentDrainAckWithNamedHolderAliasOpenWorkStaysActive
// is what covers that boundary.
//
// Run it with:
//
//	go test ./cmd/gc/ -run TestReconcileSessionBeads_AgentDrainAckWithAffinityPin -count=1
package main

import (
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// strandedPoolStepMetadata is ci-7tn14g's metadata, read off the live bead
// rather than composed: the routing keys graphroute stamps on a pool-routed
// step, and nothing else. The absence of gc.session_id is the whole fixture --
// it is what distinguishes a step that must run on one session from a step that
// a session has taken.
func strandedPoolStepMetadata() map[string]string {
	return map[string]string{
		beadmeta.SessionAffinityMetadataKey:   "require",
		beadmeta.ContinuationGroupMetadataKey: "pool-workflow",
		beadmeta.RootBeadIDMetadataKey:        "root-1",
		beadmeta.RoutedToMetadataKey:          "worker",
	}
}

// TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingNoSessionReleasesSlot
// is the ci-d1huhf regression itself: the exact shape that wedged the
// bench-engineer slot for six days.
//
// It must fail before the fix and pass after it. Against the pre-fix predicate
// the pin alone excused the bead from the queue-work exclusion, the ack was
// refused with drain-ack-assigned-work, and the occupant could not satisfy the
// refusal by any action -- claiming the step was impossible (its dependency
// made it unready, so the work query never served it) and releasing the alias
// required the drain the refusal was blocking.
func TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingNoSessionReleasesSlot(t *testing.T) {
	reason := heldRouteDrainAckOutcome(t, func(string) beads.Bead {
		return beads.Bead{
			Title:    "pool-routed step nobody ever claimed",
			Type:     "task",
			Status:   "open",
			Assignee: "worker",
			Metadata: strandedPoolStepMetadata(),
		}
	})
	if reason == sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q: an affinity pin naming no session refused the ack, so a routing requirement is still being read as a live holder", reason)
	}
	if reason != sessionpkg.DrainAckStopPendingReason {
		t.Fatalf("state_reason = %q, want %q: an honored ack on a live runtime queues the async stop", reason, sessionpkg.DrainAckStopPendingReason)
	}
}

// TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingAPredecessorReleasesSlot
// is the same defect one step further along, and it is the case a fix keyed on
// "is gc.session_id present" rather than "does it name ME" would miss.
//
// On a max_active_sessions=1 pool the slot alias outlives its occupant, so a
// sibling stamped by the session that held the slot yesterday is still assigned
// to today's occupant by the alias alone. Inheriting a predecessor's pin is how
// the wedge would become permanent instead of merely long.
func TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingAPredecessorReleasesSlot(t *testing.T) {
	reason := heldRouteDrainAckOutcome(t, func(string) beads.Bead {
		meta := strandedPoolStepMetadata()
		meta[beadmeta.SessionIDMetadataKey] = "ci-predecessor"
		return beads.Bead{
			Title:    "continuation sibling stamped by the previous occupant of this slot",
			Type:     "task",
			Status:   "open",
			Assignee: "worker",
			Metadata: meta,
		}
	})
	if reason == sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q: a pin naming a DIFFERENT session refused this session's ack, so a slot occupant still inherits its predecessor's hold", reason)
	}
	if reason != sessionpkg.DrainAckStopPendingReason {
		t.Fatalf("state_reason = %q, want %q: an honored ack on a live runtime queues the async stop", reason, sessionpkg.DrainAckStopPendingReason)
	}
}

// TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingTheSlotReleasesSlot
// pins that a back-reference naming the SLOT is not a back-reference at all.
//
// sessionInstanceIdentities subtracts poolQueueAliasIdentities from the query
// identifiers for this one reason: the alias is the next occupant's address
// too, so a bead whose gc.session_name is the alias identifies no instance and
// can pin nobody. Without this case, dropping that subtraction -- the obvious
// simplification, since the identifiers are already right there -- leaves every
// other case in this file green while handing the wedge back under a different
// key.
//
// The fixture's session_name deliberately equals the alias, which is the
// canonical singleton pool's own shape and the only one where the two strings
// collide at all.
func TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingTheSlotReleasesSlot(t *testing.T) {
	reason := heldRouteDrainAckOutcome(t, func(string) beads.Bead {
		meta := strandedPoolStepMetadata()
		meta[beadmeta.SessionNameMetadataKey] = "worker"
		return beads.Bead{
			Title:    "step whose back-reference names the pool slot, not an occupant of it",
			Type:     "task",
			Status:   "open",
			Assignee: "worker",
			Metadata: meta,
		}
	})
	if reason == sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q: a pin naming the slot alias refused the ack, so an inherited address is still counting as an instance", reason)
	}
	if reason != sessionpkg.DrainAckStopPendingReason {
		t.Fatalf("state_reason = %q, want %q: an honored ack on a live runtime queues the async stop", reason, sessionpkg.DrainAckStopPendingReason)
	}
}

// TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingThisSessionStaysActive
// is what 18691002b bought, restated as evidence instead of as inference:
// preassignHookContinuationGroup hands a session its continuation siblings at
// status OPEN, and those must keep refusing the ack so an agent cannot end its
// turn with its group unfinished.
//
// The fixture differs from the two above in ONE key. If a future change stops
// reading gc.session_id, or reads gc.session_name only, this goes red while
// both cases above stay green -- which is the alarm that the exclusion has
// widened from "the pin names nobody" to "any pin at all", the mutation
// ci-fx4duc's classification matrix was written to catch.
func TestReconcileSessionBeads_AgentDrainAckWithAffinityPinNamingThisSessionStaysActive(t *testing.T) {
	reason := heldRouteDrainAckOutcome(t, func(sessionID string) beads.Bead {
		meta := strandedPoolStepMetadata()
		meta[beadmeta.SessionIDMetadataKey] = sessionID
		return beads.Bead{
			Title:    "continuation sibling this session was handed at claim time",
			Type:     "task",
			Status:   "open",
			Assignee: "worker",
			Metadata: meta,
		}
	})
	if reason != sessionpkg.DrainAckAssignedWorkReason {
		t.Fatalf("state_reason = %q, want %q: a sibling this session was handed at claim time must keep refusing the acknowledgement", reason, sessionpkg.DrainAckAssignedWorkReason)
	}
}
