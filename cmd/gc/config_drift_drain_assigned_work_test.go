package main

// cmd/gc/config_drift_drain_assigned_work_test.go
//
// Pins that a config-drift drain yields to work the session claimed AFTER the
// drain began, the same way the drain-start guard already refuses to begin one
// over work it can see (session_reconciler.go, "Skipping config-drift drain
// ... live assigned work found").
//
// The suite exists because the two checks disagreed and the gap was measured,
// not hypothesized (ci-ke4nck). astoria-sel4/lab.engineer sessions ci-xqkene
// and ci-sv0fq7 each began a config-drift drain while between claims, claimed
// a bead one to twenty-five seconds later, had the reconciler's own deferred
// drain-ack refused for holding that bead, and were force-stopped at the 2m
// drain deadline anyway -- asleep, still the assignee of in_progress work. That
// one stranded claim then drove the controller to mint a fresh session for its
// wake demand on every tick, five in two minutes, four of them into slot 3 of a
// max-2 pool.
//
// Built at the reconciler entry point rather than at
// assignedWorkDrainReasonCancelable because the defect was the ORDER: the
// refusal at the drain-ack site ran, declined to cancel, and the drain-advance
// pass later in the SAME tick force-stopped on the expired deadline. A test
// against the predicate alone passes over a fix that cancels at one site and
// stops at the other.
//
// Not represented: the tmux pane. The fake provider's IsRunning is the
// runtime the reconciler reads, so "still running" here is the reconciler's
// view and nothing more.
//
//	go test ./cmd/gc/ -run 'ConfigDriftDrain'

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

func TestReconcileSessionBeads_ConfigDriftDrainYieldsToWorkClaimedMidDrain(t *testing.T) {
	// The measured shape: the drain is past its deadline (the 2m
	// drift_drain_timeout ran out), the reconciler's deferred GC_DRAIN_ACK
	// carries reason config-drift, and an in_progress bead is assigned to the
	// session. Before the fix the refusal stamped drain-ack-assigned-work and
	// the deadline path then stopped the session in the same tick.
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	work := env.createWorkBead(beads.Bead{
		Title:    "claimed after the drain began",
		Type:     "task",
		Status:   "in_progress",
		Assignee: session.ID,
	})
	if err := setReconcilerDrainAckMetadata(env.sp, "worker", &drainState{
		reason:     "config-drift",
		generation: 1,
		ackSet:     true,
	}); err != nil {
		t.Fatalf("setReconcilerDrainAckMetadata: %v", err)
	}
	env.dt.set(session.ID, &drainState{
		startedAt:  env.clk.Now().Add(-defaultDrainTimeout),
		deadline:   env.clk.Now().Add(-time.Second),
		reason:     "config-drift",
		generation: 1,
		ackSet:     true,
	})

	reconcileSessionBeadsAtPath(
		context.Background(), "", []beads.Bead{session}, env.desiredState, nil,
		env.cfg, env.sp, env.store, newDrainOps(env.sp), []beads.Bead{work},
		nil, nil, env.dt, map[string]int{"worker": 1}, false, nil, "", nil,
		env.clk, env.rec, 0, 0, &env.stdout, &env.stderr,
	)

	if !env.sp.IsRunning("worker") {
		t.Fatalf("session stopped by a config-drift drain while holding in_progress work %s -- the holder is put to sleep with its claim, which is the stranded claim that drove ci-ke4nck's minting loop; stdout:\n%s", work.ID, env.stdout.String())
	}
	if ds := env.dt.get(session.ID); ds != nil {
		t.Fatalf("drain = %+v, want canceled -- left in the tracker it force-stops on the next tick instead", ds)
	}
	if ack, _ := env.sp.GetMeta("worker", "GC_DRAIN_ACK"); ack != "" {
		t.Fatalf("GC_DRAIN_ACK = %q, want cleared", ack)
	}
	if got := env.sessionInfo(session.ID); string(got.State) == "asleep" {
		t.Fatalf("session state = asleep, want it left awake to finish %s", work.ID)
	}
}

func TestAdvanceSessionDrains_ConfigDriftDrainCanceledForAssignedWork(t *testing.T) {
	// The drain-advance half of the same rule. A tick whose drain-ack site
	// never fires -- the deferred ack not yet set, or already cleared by an
	// earlier refusal -- reaches the deadline here, so this site needs the
	// assigned-work cancel independently of the other.
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	clk := &clock.Fake{Time: now}
	sp := runtime.NewFake()
	store := beads.NewMemStore()
	dt := newDrainTracker()

	if err := sp.Start(context.Background(), "test-session", runtime.Config{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b, err := store.Create(beads.Bead{
		Title:  "test",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"session_name": "test-session",
			"template":     "worker",
			"provider":     "claude",
			"work_dir":     t.TempDir(),
			"generation":   "3",
			"state":        "active",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	dt.set(b.ID, &drainState{
		startedAt:  now.Add(-defaultDrainTimeout),
		deadline:   now.Add(-time.Second),
		reason:     "config-drift",
		generation: 3,
		ackSet:     true,
	})

	advanceSessionDrainsWithSessionsTraced(
		dt, sp, store,
		infoLookupFromBeadLookup(func(id string) *beads.Bead {
			got, _ := store.Get(id)
			return &got
		}),
		map[string]wakeEvaluation{
			b.ID: {Reasons: []WakeReason{WakeWork}, Reason: "assigned-work"},
		},
		&config.City{Agents: []config.Agent{{Name: "worker"}}},
		clk,
		nil,
	)

	if !sp.IsRunning("test-session") {
		t.Fatal("expired config-drift drain stopped a session the awake set holds for assigned work")
	}
	if ds := dt.get(b.ID); ds != nil {
		t.Fatalf("drain = %+v, want canceled for assigned work", ds)
	}
}
