package main

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Scope: whether a pool session's self-drain on no_work is a demand/claim
// DISAGREEMENT or a demand that was satisfied, which is the one thing
// session.demand_claim_mismatch claims to report and could not tell apart.
//
// The suite exists because the event was measured against the live city and
// found to be majority-false. Over 2026-09-07..09-12, 489 of the 552 recorded
// mismatches named a trigger bead; 275 of those 489 were sessions that HAD
// claimed work, and 255 had claimed the very bead the demand named. Two
// independent readings agree and do not overlap: claim evidence (gc.session_id
// stamped on a work bead) and session lifetime -- every did-claim session lived
// at least 107s, every never-claimed one at most 71s. A count of these rows was
// therefore never a count of phantom spawns, and two prior investigations
// (ci-fp7pzt, ci-um0db7) were scoped from one.
//
// What these tests do NOT cover, because the reconciler cannot see it: a
// session whose trigger was REBOUND mid-life to a second bead
// (computePoolTriggerBindingPatch) while it claimed the first. Measured at 20
// of the 489. Those still report a mismatch. Closing that hole needs a durable
// per-session claim record, and writing one from the claim path is a rejected
// alternative -- f00a44e30 removed exactly that because bd's fuzzy ID resolver
// can redirect a post-claim session update onto a prefix-colliding session.
//
// Run: go test ./cmd/gc/ -run DemandClaimSettled

// demandClaimSettledFixture drives a pool session created for a seeded trigger
// bead through a self-acknowledged no_work drain to its close. stamp runs once
// the session bead exists, so a test can record on the trigger who claimed it.
// Returns the closed session bead and the events recorded during the finalize.
func demandClaimSettledFixture(
	t *testing.T,
	triggerStoreRef string,
	seed func(store beads.Store) string,
	stamp func(store beads.Store, triggerID, sessionID string),
) (beads.Bead, *events.Fake) {
	t.Helper()
	env := newReconcilerTestEnv()
	triggerID := ""
	if seed != nil {
		triggerID = seed(env.store)
	}
	session := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&session, map[string]string{
		"pool_managed":                          "true",
		"session_origin":                        "ephemeral",
		beadmeta.TriggerBeadIDMetadataKey:       triggerID,
		beadmeta.TriggerBeadStoreRefMetadataKey: triggerStoreRef,
	})
	env.markSessionActive(&session)
	if stamp != nil {
		stamp(env.store, triggerID, session.ID)
	}
	if err := env.sp.Start(context.Background(), "worker", runtime.Config{Command: "test-cmd"}); err != nil {
		t.Fatalf("Start(worker): %v", err)
	}

	dops := newFakeDrainOps()
	if err := dops.setDrainAckWithReason("worker", hookClaimReasonNoWork); err != nil {
		t.Fatalf("setDrainAckWithReason: %v", err)
	}
	if woken := env.reconcileWithPoolDesiredAndDrainOps([]beads.Bead{session}, nil, dops); woken != 0 {
		t.Fatalf("woken = %d, want 0", woken)
	}

	rec := events.NewFake()
	env.rec = rec
	closed := env.reconcileStopPendingToTerminal(t, env.sp, session, dops, nil)
	if closed.Status != "closed" {
		t.Fatalf("status = %q, want closed; metadata=%v", closed.Status, closed.Metadata)
	}
	return closed, rec
}

func recordedDemandClaimMismatch(rec *events.Fake) bool {
	for i := range rec.Events {
		if rec.Events[i].Type == events.SessionDemandClaimMismatch {
			return true
		}
	}
	return false
}

// seedOpenTriggerBead puts the bead the demand named into the store and returns
// its id.
func seedOpenTriggerBead(t *testing.T) func(beads.Store) string {
	t.Helper()
	return func(store beads.Store) string {
		created, err := store.Create(beads.Bead{Title: "the work the demand named", Type: "task"})
		if err != nil {
			t.Fatalf("seeding trigger bead: %v", err)
		}
		return created.ID
	}
}

// stampTriggerClaimedBy writes the claim-time back-reference `gc hook --claim`
// stamps on work it takes (hookClaimIdentityPatch), then closes the bead: that
// is the shape the majority population has, a session that finished the work
// before its next claim came back empty. owner "" means the claiming session's
// own id.
func stampTriggerClaimedBy(t *testing.T, owner string) func(beads.Store, string, string) {
	t.Helper()
	return func(store beads.Store, triggerID, sessionID string) {
		claimant := owner
		if claimant == "" {
			claimant = sessionID
		}
		if err := store.Update(triggerID, beads.UpdateOpts{
			Metadata: map[string]string{beadmeta.SessionIDMetadataKey: claimant},
		}); err != nil {
			t.Fatalf("stamping trigger bead claimant: %v", err)
		}
		if err := store.Close(triggerID); err != nil {
			t.Fatalf("closing claimed trigger bead: %v", err)
		}
	}
}

// TestDemandClaimSettledWhenTheSessionTookItsOwnTriggerBead is the invariant the
// live measurement names: a session that claimed the bead its demand named
// satisfied that demand, whatever its LAST claim returned. Reporting that as a
// disagreement is what made the event majority-false.
//
// The assertion covers both consequences rather than the event alone. The same
// predicate stamps the claim_no_work create backoff, which brakes the pool's
// lowest free slot for that trigger, so a session that did the work must leave
// neither record behind.
func TestDemandClaimSettledWhenTheSessionTookItsOwnTriggerBead(t *testing.T) {
	closed, rec := demandClaimSettledFixture(t, "", seedOpenTriggerBead(t), stampTriggerClaimedBy(t, ""))
	if recordedDemandClaimMismatch(rec) {
		t.Fatal("recorded a demand/claim mismatch for a session that claimed the bead the demand named")
	}
	if got := closed.Metadata[poolCreateFailureClassMetadataKey]; got == poolCreateFailureClassClaimNoWork {
		t.Fatal("stamped the claim_no_work create backoff on a session that claimed the bead the demand named")
	}
}

// TestDemandClaimSettledStillReportsAStrangersTriggerBead is the over-correction
// guard. The suppression must key on THIS session having taken the bead, not on
// the bead having been taken at all: a bead claimed by another session is
// exactly the lost-race phantom the event exists to report, measured at 22 of
// the 214 real phantoms in the same window.
func TestDemandClaimSettledStillReportsAStrangersTriggerBead(t *testing.T) {
	_, rec := demandClaimSettledFixture(t, "", seedOpenTriggerBead(t), stampTriggerClaimedBy(t, "some-other-session"))
	if !recordedDemandClaimMismatch(rec) {
		t.Fatal("suppressed the mismatch for a trigger bead another session claimed")
	}
}

// TestDemandClaimSettledStillReportsAnUnclaimedTriggerBead covers the dominant
// real phantom: the demand named a bead, a session was spawned for it, and
// nobody ever took it. 192 of the 214 measured phantoms have this shape.
func TestDemandClaimSettledStillReportsAnUnclaimedTriggerBead(t *testing.T) {
	_, rec := demandClaimSettledFixture(t, "", seedOpenTriggerBead(t), nil)
	if !recordedDemandClaimMismatch(rec) {
		t.Fatal("suppressed the mismatch for a trigger bead no session ever claimed")
	}
}

// TestDemandClaimSettledFailsOpenOnAnUnreadableTrigger pins the direction of the
// unknown case. A trigger bead the reconciler cannot read -- absent, or in a rig
// store this process did not open -- yields no evidence either way, and the
// event must keep firing rather than go quiet: an inflated count is a bad
// measurement, a suppressed one is a defect nobody sees.
func TestDemandClaimSettledFailsOpenOnAnUnreadableTrigger(t *testing.T) {
	_, rec := demandClaimSettledFixture(t, "rig:unopened", func(beads.Store) string { return "work-absent" }, nil)
	if !recordedDemandClaimMismatch(rec) {
		t.Fatal("suppressed the mismatch for a trigger bead that could not be read")
	}
}
