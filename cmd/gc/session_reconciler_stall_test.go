package main

import (
	"context"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Scope: the session reconciler's composition of the two liveness arms -- that
// a transcript stall reaches TimerFacts at all, that it is labeled as the
// stall arm in the trace, and that the probe it hands the tracker resolves
// this session's own transcript.
//
// Why this suite exists: everything below the reconciler can be correct and
// the feature still dead, because the reconciler is where the second arm is
// either consulted or not. ci-jvbkio is exactly a signal that was never
// consulted -- no reconciler.idle_timeout records at all for a session hung
// 12h -- so the composition is the part worth pinning hardest.
//
// It cannot represent: whether a real hung Claude session's transcript
// actually goes quiet while its pane does not. That is a live-fleet
// measurement, recorded on the bead, not something a fake provider can show.
//
//	go test ./cmd/gc/ -run TestReconcileSessionBeads_Stall

// TestReconcileSessionBeads_StallTimeoutStopsAFreshPaneSession pins the whole
// point of the arm: the pane arm reports NOT idle and the session is stopped
// anyway, on the transcript alone.
func TestReconcileSessionBeads_StallTimeoutStopsAFreshPaneSession(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)

	it := newFakeIdleTracker()
	// Deliberately NOT it.idle["worker"]: the pane arm says this session is
	// live, which is what a spinner reports for a hung turn.
	it.stalled["worker"] = true

	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeads(
		context.Background(), []beads.Bead{session}, env.desiredState, cfgNames,
		env.cfg, env.sp, env.store, nil, nil, nil, env.dt, map[string]int{}, false, nil, "",
		it, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr,
	)

	if env.sp.IsRunning("worker") {
		t.Error("session with a stalled transcript must be stopped even though its pane is fresh")
	}
	b, err := env.store.Get(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Metadata["sleep_reason"] != string(sessionpkg.SleepReasonIdleTimeout) {
		t.Errorf("sleep_reason = %q, want %q -- a stall kill is an idle kill downstream", b.Metadata["sleep_reason"], sessionpkg.SleepReasonIdleTimeout)
	}
}

// TestReconcileSessionBeads_StallTimeoutIsNamedInTheTrace pins the recorded
// reason code. ci-jvbkio was diagnosed by reading these records and the whole
// diagnosis turned on which arm fired; a stall stop recorded as "idle_timeout"
// is indistinguishable in exactly the artifact an operator reaches for.
func TestReconcileSessionBeads_StallTimeoutIsNamedInTheTrace(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)

	it := newFakeIdleTracker()
	it.stalled["worker"] = true
	rec := events.NewFake()
	env.rec = rec
	trace := newStallTraceCycle("worker")

	poolDesired := make(map[string]int)
	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeadsTraced(
		context.Background(), "", []beads.Bead{session}, env.desiredState, cfgNames, env.cfg, env.sp,
		env.store, nil, nil, nil, nil, env.dt, poolDesired, false, nil, "",
		it, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr, trace,
	)

	var sawStall, sawPlainIdle bool
	for _, r := range trace.records {
		if r.SiteCode != TraceSiteReconcilerIdleTimeout || r.OutcomeCode != TraceOutcomeStop {
			continue
		}
		switch r.ReasonCode {
		case TraceReasonStallTimeout:
			sawStall = true
		case TraceReasonIdleTimeout:
			sawPlainIdle = true
		}
	}
	if !sawStall {
		t.Errorf("no %q stop recorded at %q; idle-timeout site records=%v", TraceReasonStallTimeout, TraceSiteReconcilerIdleTimeout, idleTimeoutTraceCodes(trace))
	}
	if sawPlainIdle {
		t.Error("a transcript stall must not be recorded as a plain idle_timeout stop")
	}
}

// TestReconcileSessionBeads_PaneIdleKeepsTheIdleTraceReason pins the other
// direction. When the pane arm is the one that fired, the recorded reason
// must stay "idle_timeout" -- adding an arm must not reclassify the kills
// that were already happening.
func TestReconcileSessionBeads_PaneIdleKeepsTheIdleTraceReason(t *testing.T) {
	env := newReconcilerTestEnv()
	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)

	it := newFakeIdleTracker()
	it.idle["worker"] = true
	it.stalled["worker"] = true // both arms fire; the pane arm must win the label
	rec := events.NewFake()
	env.rec = rec
	trace := newStallTraceCycle("worker")

	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeadsTraced(
		context.Background(), "", []beads.Bead{session}, env.desiredState, cfgNames, env.cfg, env.sp,
		env.store, nil, nil, nil, nil, env.dt, map[string]int{}, false, nil, "",
		it, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr, trace,
	)

	var sawIdle bool
	for _, r := range trace.records {
		if r.SiteCode == TraceSiteReconcilerIdleTimeout && r.OutcomeCode == TraceOutcomeStop && r.ReasonCode == TraceReasonIdleTimeout {
			sawIdle = true
		}
	}
	if !sawIdle {
		t.Errorf("a pane-idle stop must still record %q; idle-timeout site records=%v", TraceReasonIdleTimeout, idleTimeoutTraceCodes(trace))
	}
}

// TestReconcileSessionBeads_StallProbeResolvesThisSessionsTranscript pins the
// probe the reconciler actually hands the tracker. Every other test here
// scripts the tracker's answer, so none of them would notice the reconciler
// passing a probe that resolves nothing -- which is the shape in which this
// feature would ship dead.
func TestReconcileSessionBeads_StallProbeResolvesThisSessionsTranscript(t *testing.T) {
	env := newReconcilerTestEnv()
	root := t.TempDir()
	workDir := t.TempDir()
	key := "af97db1a-ee48-41e7-ab72-01702c8580b9"
	want := time.Now().Add(-11 * time.Hour).Truncate(time.Second)
	writeKeyedTranscript(t, root, workDir, key, want)

	env.cfg = &config.City{Agents: []config.Agent{{Name: "worker"}}}
	env.cfg.Daemon.ObservePaths = []string{root}
	env.addDesired("worker", "worker", true)
	session := env.createSessionBead("worker", "worker")
	env.markSessionActive(&session)
	env.setSessionMetadata(&session, map[string]string{
		"session_key": key,
		"work_dir":    workDir,
		"provider":    "claude",
	})

	it := newFakeIdleTracker()
	// Register the arm without scripting a verdict, so the reconciler's probe
	// is the only thing under test.
	it.stallTemplates["worker"] = false

	cfgNames := configuredSessionNames(env.cfg, "", env.store)
	reconcileSessionBeads(
		context.Background(), []beads.Bead{session}, env.desiredState, cfgNames,
		env.cfg, env.sp, env.store, nil, nil, nil, env.dt, map[string]int{}, false, nil, "",
		it, env.clk, env.rec, 0, 0, &env.stdout, &env.stderr,
	)

	got, ok := it.lastTranscripts["worker"]
	if !ok {
		t.Fatalf("reconciler never handed checkStalled a transcript probe for %q", "worker")
	}
	if !got.Equal(want) {
		t.Fatalf("probe resolved %v, want this session's transcript mtime %v", got, want)
	}
}

// idleTimeoutTraceCodes reduces a cycle's records to the reason/outcome pairs
// recorded at the idle-timeout site, so a failure prints the vocabulary under
// test instead of every field of every record in the tick.
func idleTimeoutTraceCodes(trace *sessionReconcilerTraceCycle) []string {
	var out []string
	for _, r := range trace.records {
		if r.SiteCode == TraceSiteReconcilerIdleTimeout {
			out = append(out, string(r.ReasonCode)+"/"+string(r.OutcomeCode))
		}
	}
	return out
}

// newStallTraceCycle builds a trace cycle that keeps per-session detail for
// one session name, so the assertions above can read individual records
// rather than aggregate counts.
func newStallTraceCycle(sessionName string) *sessionReconcilerTraceCycle {
	return &sessionReconcilerTraceCycle{
		tracer: &SessionReconcilerTracer{
			detail: map[string]TraceSource{sessionName: TraceSourceManual},
		},
		dropReasons:       map[string]int{},
		pendingDetail:     map[string][]SessionReconcilerTraceRecord{},
		pendingDropped:    map[string]int{},
		templatesTouched:  map[string]struct{}{},
		detailedTemplates: map[string]struct{}{},
		decisionCounts:    map[string]int{},
		operationCounts:   map[string]int{},
		mutationCounts:    map[string]int{},
		reasonCounts:      map[string]int{},
		outcomeCounts:     map[string]int{},
	}
}
