package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Scope: the drain-origin wiring across the reconciler's drain-ack close path
// (ci-wxag) -- which actor each path stamps, and that the stamp survives to the
// close on a LATER tick than the one that read it off the runtime. The rendering
// half (origin -> close_reason text) is pinned in
// internal/session/drain_origin_test.go.
//
// These drive the whole reconcile loop rather than calling
// finalizeDrainAckStoppedSession directly, because the bug was a LOST fact, not
// a wrong mapping: the runtime metadata carrying the origin is destroyed between
// the tick that observes the ack and the tick that closes the bead. A test that
// called the finalizer in one breath would read the origin straight from the
// live fake and pass against code that never stamped anything.
//
// Run: go test ./cmd/gc/ -run DrainOrigin

// drainAckOriginFixture drives a session from live-and-acked through the
// stop-pending finalize, returning the closed bead. seedAck decides which actor
// acknowledged.
func drainAckOriginFixture(t *testing.T, seedAck func(dops *fakeDrainOps, name string)) beads.Bead {
	t.Helper()
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	if err := env.sp.Start(context.Background(), "worker", runtime.Config{Command: "test-cmd"}); err != nil {
		t.Fatalf("Start(worker): %v", err)
	}

	dops := newFakeDrainOps()
	seedAck(dops, "worker")

	if woken := env.reconcileWithPoolDesiredAndDrainOps([]beads.Bead{session}, nil, dops); woken != 0 {
		t.Fatalf("woken = %d, want 0", woken)
	}
	got := env.reconcileStopPendingToTerminal(t, env.sp, session, dops, nil)
	if got.Status != "closed" {
		t.Fatalf("status = %q, want closed; metadata=%v", got.Status, got.Metadata)
	}
	return got
}

// TestDrainOriginSeparatesSelfDrainFromReconcilerRetirement is the regression
// test ci-wxag names: the two paths must produce DIFFERENT observable records.
//
// Both expectations are written as literals rather than derived from
// DrainedCloseReason, and the final check compares the two records to each
// other. Asserting only that each path closes the session -- or that each
// renders CanonicalCloseReason("drained") -- passes with the bug present, which
// is how the ambiguity survived long enough to misdirect ci-fh4o.
func TestDrainOriginSeparatesSelfDrainFromReconcilerRetirement(t *testing.T) {
	selfClosed := drainAckOriginFixture(t, func(dops *fakeDrainOps, name string) {
		if err := dops.setDrainAckWithReason(name, hookClaimReasonNoWork); err != nil {
			t.Fatalf("setDrainAckWithReason: %v", err)
		}
	})
	reconcilerClosed := drainAckOriginFixture(t, func(dops *fakeDrainOps, name string) {
		dops.setReconcilerDrainAck(name, "no-wake-reason")
	})

	selfReason := selfClosed.Metadata["close_reason"]
	reconcilerReason := reconcilerClosed.Metadata["close_reason"]

	if want := "session drained: agent retired itself (self: no_work)"; selfReason != want {
		t.Errorf("self-drain close_reason = %q, want %q", selfReason, want)
	}
	if want := "session retired: reconciler reclaimed the slot (reconciler: no-wake-reason)"; reconcilerReason != want {
		t.Errorf("reconciler close_reason = %q, want %q", reconcilerReason, want)
	}
	if selfReason == reconcilerReason {
		t.Errorf("both paths closed with %q; a self-retired session and a reconciler-retired slot must be distinguishable from the closed bead alone", selfReason)
	}

	// The machine-readable pair must agree with the prose, and must be present:
	// an operator greps the reason, a tool reads the keys.
	if got := selfClosed.Metadata[sessionpkg.DrainOriginMetadataKey]; got != string(sessionpkg.DrainOriginSelf) {
		t.Errorf("self-drain drain_origin = %q, want %q", got, sessionpkg.DrainOriginSelf)
	}
	if got := selfClosed.Metadata[sessionpkg.DrainAckReasonMetadataKey]; got != hookClaimReasonNoWork {
		t.Errorf("self-drain drain_ack_reason = %q, want %q", got, hookClaimReasonNoWork)
	}
	if got := reconcilerClosed.Metadata[sessionpkg.DrainOriginMetadataKey]; got != string(sessionpkg.DrainOriginReconciler) {
		t.Errorf("reconciler drain_origin = %q, want %q", got, sessionpkg.DrainOriginReconciler)
	}
	if got := reconcilerClosed.Metadata[sessionpkg.DrainAckReasonMetadataKey]; got != "no-wake-reason" {
		t.Errorf("reconciler drain_ack_reason = %q, want %q", got, "no-wake-reason")
	}

	// state stays the short canonical code on both. Reconciler logic and
	// closedNamedSessionReopenEligible switch on it, so the origin had to travel
	// beside it, never inside it.
	for _, tc := range []struct {
		name string
		bead beads.Bead
	}{{"self", selfClosed}, {"reconciler", reconcilerClosed}} {
		if got := tc.bead.Metadata["state"]; got != "drained" {
			t.Errorf("%s: state = %q, want %q", tc.name, got, "drained")
		}
	}
}

// TestDrainOriginSurvivesTheRuntimeItWasReadFrom pins the reason the origin is
// stamped on the bead instead of re-read at close time. The finalize that
// renders the close reason runs on a later tick, against a runtime the
// stop-pending transition already killed -- so a drainOps that has forgotten the
// session entirely must still yield the recorded actor.
func TestDrainOriginSurvivesTheRuntimeItWasReadFrom(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	if err := env.sp.Start(context.Background(), "worker", runtime.Config{Command: "test-cmd"}); err != nil {
		t.Fatalf("Start(worker): %v", err)
	}

	dops := newFakeDrainOps()
	dops.setReconcilerDrainAck("worker", "config-drift")
	if woken := env.reconcileWithPoolDesiredAndDrainOps([]beads.Bead{session}, nil, dops); woken != 0 {
		t.Fatalf("woken = %d, want 0", woken)
	}

	stopPending, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatalf("Get after stop-pending: %v", err)
	}
	if got := stopPending.Metadata[sessionpkg.DrainOriginMetadataKey]; got != string(sessionpkg.DrainOriginReconciler) {
		t.Fatalf("drain_origin at stop-pending = %q, want %q -- the origin must be durable BEFORE the runtime is stopped", got, sessionpkg.DrainOriginReconciler)
	}

	// Amnesiac drainOps: still reports the ack (the reconciler needs that to
	// reach the finalize at all) but no longer knows who set it, exactly as a
	// real provider behaves once the tmux session is gone.
	amnesiac := newFakeDrainOps()
	amnesiac.acked["worker"] = true
	if origin, reason := amnesiac.drainAckOrigin("worker"); origin != sessionpkg.DrainOriginUnrecorded || reason != "" {
		t.Fatalf("amnesiac drainAckOrigin = (%q, %q), want unrecorded -- the fixture is not modeling a dead runtime", origin, reason)
	}

	closed := env.reconcileStopPendingToTerminal(t, env.sp, session, amnesiac, nil)
	if closed.Status != "closed" {
		t.Fatalf("status = %q, want closed; metadata=%v", closed.Status, closed.Metadata)
	}
	if want := "session retired: reconciler reclaimed the slot (reconciler: config-drift)"; closed.Metadata["close_reason"] != want {
		t.Errorf("close_reason = %q, want %q", closed.Metadata["close_reason"], want)
	}
}

// TestDrainOriginUnrecordedNamesNoActor pins the honest-ignorance case: a
// drain-ack whose origin nothing captured must close saying so, not defaulting
// to either actor. Naming a default here is the original bug in miniature.
func TestDrainOriginUnrecordedNamesNoActor(t *testing.T) {
	closed := drainAckOriginFixture(t, func(dops *fakeDrainOps, name string) {
		// Acked with no source recorded: a legacy runtime, or one whose metadata
		// write was lost.
		dops.acked[name] = true
	})
	got := closed.Metadata["close_reason"]
	if want := "session drained: drain origin not recorded"; got != want {
		t.Errorf("close_reason = %q, want %q", got, want)
	}
	for _, actor := range []string{"reconciler", "agent"} {
		if strings.Contains(got, actor) {
			t.Errorf("close_reason = %q names %q with no evidence for it", got, actor)
		}
	}
	if got := closed.Metadata[sessionpkg.DrainOriginMetadataKey]; got != "" {
		t.Errorf("drain_origin = %q, want empty for an unrecorded origin", got)
	}
}

// TestDemandTriggeredNoWorkDrainBacksOffAndEmitsMismatch proves the completed
// start path has the same durable retry guard as an aborted create. A pool
// session that was created for a trigger but drains itself with no_work is the
// observable demand/claim disagreement: the next matching desired-state pass
// must not recreate it immediately, and observers must receive the typed fact.
func TestDemandTriggeredNoWorkDrainBacksOffAndEmitsMismatch(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&session, map[string]string{
		"pool_managed":                          "true",
		"session_origin":                        "ephemeral",
		beadmeta.TriggerBeadIDMetadataKey:       "work-1",
		beadmeta.TriggerBeadStoreRefMetadataKey: "rig:work",
	})
	env.markSessionActive(&session)
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
	if got := closed.Metadata[poolCreateFailureClassMetadataKey]; got != poolCreateFailureClassClaimNoWork {
		t.Fatalf("retry class = %q, want %q", got, poolCreateFailureClassClaimNoWork)
	}
	if active, err := poolCreateFailureBackoffActive(sessionFrontDoor(env.store), "worker", "worker", "work-1", env.clk.Now()); err != nil {
		t.Fatalf("poolCreateFailureBackoffActive: %v", err)
	} else if !active {
		t.Fatal("matching no-work drain did not block an immediate replacement create")
	}

	var mismatch *events.Event
	for i := range rec.Events {
		if rec.Events[i].Type == events.SessionDemandClaimMismatch {
			mismatch = &rec.Events[i]
			break
		}
	}
	if mismatch == nil {
		t.Fatal("no demand/claim mismatch event recorded")
	}
	var payload api.SessionDemandClaimMismatchPayload
	if err := json.Unmarshal(mismatch.Payload, &payload); err != nil {
		t.Fatalf("unmarshal mismatch payload: %v", err)
	}
	if payload.SessionID != session.ID || payload.Template != "worker" || payload.DemandScope != "work_bead" || payload.TriggerBeadID != "work-1" || payload.TriggerBeadStoreRef != "rig:work" || payload.Reason != hookClaimReasonNoWork {
		t.Fatalf("mismatch payload = %+v, want session/template/trigger/store/reason facts", payload)
	}
}

// TestCountOnlyDemandNoWorkDrainBacksOffAndEmitsMismatch covers custom
// scale_check pools, whose integer result creates a session without a specific
// work-bead trigger. The retry boundary is then per pool identity rather than
// per bead, but it must still stop a count/claim race from recreating a session
// every patrol tick.
func TestCountOnlyDemandNoWorkDrainBacksOffAndEmitsMismatch(t *testing.T) {
	env := newReconcilerTestEnv()
	session := env.createSessionBead("worker", "worker")
	env.setSessionMetadata(&session, map[string]string{
		"pool_managed":   "true",
		"session_origin": "ephemeral",
	})
	env.markSessionActive(&session)
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
	if got := closed.Metadata[poolCreateFailureClassMetadataKey]; got != poolCreateFailureClassClaimNoWork {
		t.Fatalf("retry class = %q, want %q", got, poolCreateFailureClassClaimNoWork)
	}
	if active, err := poolCreateFailureBackoffActive(sessionFrontDoor(env.store), "worker", "worker", "", env.clk.Now()); err != nil {
		t.Fatalf("poolCreateFailureBackoffActive: %v", err)
	} else if !active {
		t.Fatal("count-only no-work drain did not block an immediate replacement create")
	}
	if active, err := poolCreateFailureBackoffActive(sessionFrontDoor(env.store), "worker", "worker", "new-work", env.clk.Now()); err != nil {
		t.Fatalf("poolCreateFailureBackoffActive for new work: %v", err)
	} else if active {
		t.Fatal("count-only retry ledger blocked an independent concrete work trigger")
	}

	var mismatch *events.Event
	for i := range rec.Events {
		if rec.Events[i].Type == events.SessionDemandClaimMismatch {
			mismatch = &rec.Events[i]
			break
		}
	}
	if mismatch == nil {
		t.Fatal("no count-only demand/claim mismatch event recorded")
	}
	var payload api.SessionDemandClaimMismatchPayload
	if err := json.Unmarshal(mismatch.Payload, &payload); err != nil {
		t.Fatalf("unmarshal mismatch payload: %v", err)
	}
	if payload.SessionID != session.ID || payload.Template != "worker" || payload.DemandScope != "pool" || payload.TriggerBeadID != "" || payload.TriggerBeadStoreRef != "" || payload.Reason != hookClaimReasonNoWork {
		t.Fatalf("mismatch payload = %+v, want session/template/reason with no bead trigger", payload)
	}
}
