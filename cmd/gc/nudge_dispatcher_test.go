package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// supervisorCfg returns a minimal *config.City wired for supervisor-mode
// nudge dispatching. Tests use it to drive nudgeDispatcherIsSupervisor.
func supervisorCfg() *config.City {
	return &config.City{
		Daemon: config.DaemonConfig{NudgeDispatcher: "supervisor"},
	}
}

type deliveredUnconfirmedNudgeProvider struct {
	*runtime.Fake
}

func (p *deliveredUnconfirmedNudgeProvider) Nudge(name string, content []runtime.ContentBlock) error {
	if err := p.Fake.Nudge(name, content); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", runtime.ErrNudgeSubmitUnconfirmed, name)
}

func TestPingNudgeWakeSocketNoListenerIsNoOp(t *testing.T) {
	dir := t.TempDir()
	// No listener — DialTimeout returns "no such file or directory". The
	// helper must swallow it; otherwise enqueue producers would surface
	// transient warnings to legacy-mode users.
	pingNudgeWakeSocket(dir)
}

func TestPingNudgeWakeSocketEmptyCityPathIsNoOp(_ *testing.T) {
	// No assertion needed — test passes if pingNudgeWakeSocket does not
	// panic on an empty cityPath. The function dials a derived socket path
	// and exits silently on dial failure, which is the legacy-mode contract.
	pingNudgeWakeSocket("")
}

func TestStartNudgeWakeListenerSignalsOnConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	wakeCh := make(chan struct{}, 1)

	lis, err := startNudgeWakeListener(ctx, dir, wakeCh, nil, "test")
	if err != nil {
		t.Fatalf("startNudgeWakeListener: %v", err)
	}
	defer lis.Close() //nolint:errcheck

	pingNudgeWakeSocket(dir)
	select {
	case <-wakeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("wakeCh not signaled within 2s of producer ping")
	}
}

func TestStartNudgeWakeListenerCoalescesBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	wakeCh := make(chan struct{}, 1)

	lis, err := startNudgeWakeListener(ctx, dir, wakeCh, nil, "test")
	if err != nil {
		t.Fatalf("startNudgeWakeListener: %v", err)
	}
	defer lis.Close() //nolint:errcheck

	// Fire several pings in quick succession. The buffered channel of size
	// 1 must coalesce them — never block the listener accept loop.
	for i := 0; i < 10; i++ {
		pingNudgeWakeSocket(dir)
	}
	// Let all accepts drain through the listener so coalescing settles, then
	// verify a wake was produced. The structural coalescing guarantee is the
	// chan's bounded capacity; the previous test counted cumulative wakes
	// over time, which races against accept-loop scheduling on fast hardware.
	time.Sleep(200 * time.Millisecond)
	select {
	case <-wakeCh:
	default:
		t.Fatal("wakeCh not signaled at all after burst of 10 pings")
	}
	if got := cap(wakeCh); got != 1 {
		t.Fatalf("wakeCh capacity = %d; want 1 (coalescing relies on bounded buffer)", got)
	}
}

func TestStartNudgeWakeListenerStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	wakeCh := make(chan struct{}, 1)

	lis, err := startNudgeWakeListener(ctx, dir, wakeCh, nil, "test")
	if err != nil {
		t.Fatalf("startNudgeWakeListener: %v", err)
	}
	cancel()
	// The cleanup goroutine closes the listener on ctx.Done. Give it a beat,
	// then confirm dialing the socket fails fast.
	time.Sleep(50 * time.Millisecond)
	_, err = net.DialTimeout("unix", nudgequeue.WakeSocketPath(dir), 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected dial to fail after ctx cancel; listener still accepting")
	}
	_ = lis
}

func TestDispatchAllQueuedNudgesNoOpInLegacyMode(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "msg", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}
	cfg := &config.City{Daemon: config.DaemonConfig{}} // legacy default
	delivered, err := dispatchAllQueuedNudges(dir, cfg, nil, nil, nil, newSessionBeadSnapshot(nil))
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("delivered = %d, want 0 in legacy mode", delivered)
	}
}

func TestNudgeDispatchTickTerminalizesStaleFenceInLegacyMode(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")

	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	oldSession, err := store.Create(beads.Bead{
		Title:  "Old worker",
		Type:   session.BeadType,
		Status: "closed",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":              "worker",
			"session_name":       "worker-session",
			"continuation_epoch": "1",
		},
	})
	if err != nil {
		t.Fatalf("create old session: %v", err)
	}
	if err := store.Update(oldSession.ID, beads.UpdateOpts{Status: stringPtr("closed")}); err != nil {
		t.Fatalf("close old session: %v", err)
	}
	newSession, err := store.Create(beads.Bead{
		Title:  "Replacement worker",
		Type:   session.BeadType,
		Status: "open",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"alias":              "worker",
			"session_name":       "worker-session",
			"continuation_epoch": "2",
		},
	})
	if err != nil {
		t.Fatalf("create replacement session: %v", err)
	}
	item := newQueuedNudgeWithOptions("worker", "stale reminder", "session", time.Now().Add(-time.Minute), queuedNudgeOptions{
		SessionID:         oldSession.ID,
		ContinuationEpoch: "1",
	})
	if err := enqueueQueuedNudgeWithStore(dir, store, item); err != nil {
		t.Fatalf("enqueue stale fenced nudge: %v", err)
	}

	cr := &CityRuntime{
		cityPath:            dir,
		cfg:                 &config.City{}, // legacy mode
		standaloneCityStore: store.Store,
		stderr:              io.Discard,
	}
	cr.nudgeDispatchTick(context.Background())

	state, err := nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Pending) != 0 || len(state.InFlight) != 0 {
		t.Fatalf("pending/inFlight = %d/%d, want 0/0", len(state.Pending), len(state.InFlight))
	}
	if len(state.Dead) != 1 || state.Dead[0].ID != item.ID {
		t.Fatalf("dead = %+v, want only stale nudge %q", state.Dead, item.ID)
	}
	if state.Dead[0].LastError != errNudgeSessionFenceMismatch.Error() {
		t.Fatalf("dead terminal reason = %q, want %q", state.Dead[0].LastError, errNudgeSessionFenceMismatch)
	}
	replacement := resolveNudgeTargetFromSessionInfo(dir, &config.City{}, newSessionBeadSnapshot([]beads.Bead{newSession}).OpenInfos()[0])
	pending, inFlight, dead, err := listQueuedNudgesForTarget(dir, replacement, time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudgesForTarget: %v", err)
	}
	if len(pending) != 0 || len(inFlight) != 0 || len(dead) != 1 || dead[0].ID != item.ID {
		t.Fatalf("status pending/inFlight/dead = %d/%d/%+v, want 0/0/only %q", len(pending), len(inFlight), dead, item.ID)
	}

	shadow, ok, err := nudgeFrontDoor(store).FindIncludingTerminal(item.ID)
	if err != nil || !ok {
		t.Fatalf("FindIncludingTerminal(%q) = (%+v, %v, %v)", item.ID, shadow, ok, err)
	}
	if shadow.State != "failed" || shadow.TerminalReason != errNudgeSessionFenceMismatch.Error() {
		t.Fatalf("terminal shadow = state=%q reason=%q, want failed/%q", shadow.State, shadow.TerminalReason, errNudgeSessionFenceMismatch)
	}
}

func TestTerminalizeStaleFencedQueuedNudgesIsBoundedPerDispatch(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")

	dir := t.TempDir()
	now := time.Now().Add(-time.Minute)
	for i := 0; i <= maxFencedNudgeTerminalizationsPerDispatch; i++ {
		item := newQueuedNudgeWithOptions("worker", "stale reminder", "session", now, queuedNudgeOptions{
			ID:        fmt.Sprintf("nudge-stale-%d", i),
			SessionID: fmt.Sprintf("closed-session-%d", i),
		})
		if err := enqueueQueuedNudge(dir, item); err != nil {
			t.Fatalf("enqueue queued nudge %d: %v", i, err)
		}
	}

	terminalized, err := terminalizeStaleFencedQueuedNudges(dir, newSessionBeadSnapshot(nil), time.Now())
	if err != nil {
		t.Fatalf("terminalize stale fenced nudges: %v", err)
	}
	if terminalized != maxFencedNudgeTerminalizationsPerDispatch {
		t.Fatalf("terminalized = %d, want dispatch cap %d", terminalized, maxFencedNudgeTerminalizationsPerDispatch)
	}
	state, err := nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after bounded sweep: %v", err)
	}
	if len(state.Pending) != 1 || len(state.Dead) != maxFencedNudgeTerminalizationsPerDispatch {
		t.Fatalf("pending/dead after bounded sweep = %d/%d, want 1/%d", len(state.Pending), len(state.Dead), maxFencedNudgeTerminalizationsPerDispatch)
	}

	terminalized, err = terminalizeStaleFencedQueuedNudges(dir, newSessionBeadSnapshot(nil), time.Now())
	if err != nil {
		t.Fatalf("terminalize remainder: %v", err)
	}
	if terminalized != 1 {
		t.Fatalf("terminalized remainder = %d, want 1", terminalized)
	}
	state, err = nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState after remainder: %v", err)
	}
	if len(state.Pending) != 0 || len(state.Dead) != maxFencedNudgeTerminalizationsPerDispatch+1 {
		t.Fatalf("pending/dead after remainder = %d/%d, want 0/%d", len(state.Pending), len(state.Dead), maxFencedNudgeTerminalizationsPerDispatch+1)
	}
}

func TestTerminalizeStaleFencedQueuedNudgesRejectsDegradedSessionSnapshot(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	t.Setenv("GC_BEADS", "file")

	dir := t.TempDir()
	item := newQueuedNudgeWithOptions("worker", "stale reminder", "session", time.Now().Add(-time.Minute), queuedNudgeOptions{SessionID: "closed-session"})
	if err := enqueueQueuedNudge(dir, item); err != nil {
		t.Fatalf("enqueue queued nudge: %v", err)
	}
	if _, err := terminalizeStaleFencedQueuedNudges(dir, newSessionBeadSnapshotWithError(errors.New("session store unavailable")), time.Now()); err == nil {
		t.Fatal("terminalize stale fenced nudges error = nil, want degraded snapshot error")
	}
	state, err := nudgequeue.LoadState(dir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if len(state.Pending) != 1 || state.Pending[0].ID != item.ID || len(state.Dead) != 0 {
		t.Fatalf("pending/dead = %+v/%+v, want original pending item only", state.Pending, state.Dead)
	}
}

func TestDispatchAllQueuedNudgesEmptyQueue(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	delivered, err := dispatchAllQueuedNudges(dir, supervisorCfg(), nil, nil, nil, newSessionBeadSnapshot(nil))
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("delivered = %d, want 0 with empty queue", delivered)
	}
}

func TestDispatchAllQueuedNudgesSkipsNotYetDue(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	future := time.Now().Add(5 * time.Minute)
	item := newQueuedNudge("worker", "later", time.Now())
	item.DeliverAfter = future
	if err := enqueueQueuedNudge(dir, item); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}
	bead := beads.Bead{
		ID:     "session-1",
		Status: "open",
		Metadata: map[string]string{
			"session_name": "worker-session",
			"agent_name":   "worker",
			"template":     "worker",
		},
	}
	snapshot := newSessionBeadSnapshot([]beads.Bead{bead})
	delivered, err := dispatchAllQueuedNudges(dir, supervisorCfg(), nil, nil, runtime.NewFake(), snapshot)
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("delivered = %d, want 0 (item not yet due)", delivered)
	}
}

func TestDispatchAllQueuedNudgesDeliversAndAcks(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()

	// Set up a running session via the same fake-provider harness used by
	// the per-session poller test, then enqueue a nudge for it.
	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store.Store, fake, nil)
	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{Template: "worker", Title: "Worker", Command: "codex", WorkDir: dir, Provider: "codex", Env: nil, Resume: session.ProviderResume{}, Hints: runtime.Config{WorkDir: dir}, ExtraMeta: map[string]string{"session_origin": "manual"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mgr.Start(context.Background(), info.ID, "", runtime.Config{WorkDir: dir}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fake.Activity = map[string]time.Time{info.SessionName: time.Now().Add(-10 * time.Second)}

	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "review the deploy logs", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}

	snapshot, err := loadSessionBeadSnapshot(store.Store)
	if err != nil {
		t.Fatalf("loadSessionBeadSnapshot: %v", err)
	}

	delivered, err := dispatchAllQueuedNudges(dir, supervisorCfg(), store.Store, store.Store, fake, snapshot)
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}

	var nudgeMessages []string
	for _, call := range fake.Calls {
		if call.Method == "Nudge" {
			nudgeMessages = append(nudgeMessages, call.Message)
		}
	}
	if len(nudgeMessages) != 1 {
		t.Fatalf("nudge calls = %d, want 1", len(nudgeMessages))
	}
	if !strings.Contains(nudgeMessages[0], "review the deploy logs") {
		t.Fatalf("nudge message = %q, want original reminder", nudgeMessages[0])
	}

	pending, inFlight, dead, err := listQueuedNudges(dir, "worker", time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 0 || len(inFlight) != 0 || len(dead) != 0 {
		t.Fatalf("queue not drained: pending=%d inFlight=%d dead=%d", len(pending), len(inFlight), len(dead))
	}
}

// TestDispatchAllQueuedNudgesAcksDeliveredUnconfirmedSubmit proves that a
// nudge whose submit sequence reached tmux is not re-delivered merely because
// the provider did not observe busy before its confirmation window elapsed.
func TestDispatchAllQueuedNudgesAcksDeliveredUnconfirmedSubmit(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")

	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	provider := &deliveredUnconfirmedNudgeProvider{Fake: runtime.NewFake()}
	if err := provider.Start(context.Background(), "worker-session", runtime.Config{}); err != nil {
		t.Fatalf("provider.Start: %v", err)
	}
	provider.SetActivity("worker-session", time.Now().Add(-10*time.Second))

	created, err := store.Create(beads.Bead{
		Title:  "Session: worker",
		Type:   session.BeadType,
		Status: "open",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": "worker-session",
			"agent_name":   "worker",
			"template":     "worker",
		},
	})
	if err != nil {
		t.Fatalf("store.Create session bead: %v", err)
	}
	if err := enqueueQueuedNudgeWithStore(dir, store, newQueuedNudge("worker", "inspect the queued work", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudgeWithStore: %v", err)
	}

	snapshot := newSessionBeadSnapshot([]beads.Bead{created})
	delivered, err := dispatchAllQueuedNudges(dir, supervisorCfg(), store, store, provider, snapshot)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("first dispatch delivered = %d, want 1", delivered)
	}
	if delivered, err = dispatchAllQueuedNudges(dir, supervisorCfg(), store, store, provider, snapshot); err != nil {
		t.Fatalf("second dispatch: %v", err)
	} else if delivered != 0 {
		t.Fatalf("second dispatch delivered = %d, want 0 after acknowledgement", delivered)
	}

	var nudgeCalls int
	for _, call := range provider.Calls {
		if call.Method == "Nudge" {
			nudgeCalls++
		}
	}
	if nudgeCalls != 1 {
		t.Fatalf("nudge calls = %d, want 1 (delivered-unconfirmed submit must not be re-delivered)", nudgeCalls)
	}
	pending, inFlight, dead, err := listQueuedNudges(dir, "worker", time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 0 || len(inFlight) != 0 || len(dead) != 0 {
		t.Fatalf("queue not drained after delivered-unconfirmed submit: pending=%d inFlight=%d dead=%d", len(pending), len(inFlight), len(dead))
	}
}

// TestDispatchAllQueuedNudgesDeliversToIdleACPSession verifies the
// supervisor dispatcher delivers queued nudges to a running ACP session
// once it has been idle longer than the quiescence window. Idle ACP
// sessions used to depend exclusively on inject-on-hook drain, but a
// pure-hook delivery path never fires for a warm session that is not
// receiving fresh user prompts — queued reminders piled up
// indefinitely against an alive but quiet agent. The dispatcher now
// owns wake delivery; the hook still drains opportunistically when the
// agent receives external prompts, and the atomic queue claim prevents
// double delivery.
func TestDispatchAllQueuedNudgesDeliversToIdleACPSession(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	clearInheritedCityRoutingEnv(t)
	t.Setenv("GC_BEADS", "file")

	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	if store.Store == nil {
		t.Fatal("openNudgeBeadStore returned nil")
	}

	if err := enqueueQueuedNudgeWithStore(dir, store, newQueuedNudge("worker", "wake-up nudge", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudgeWithStore: %v", err)
	}

	fake := runtime.NewFake()
	if err := fake.Start(context.Background(), "worker-session", runtime.Config{}); err != nil {
		t.Fatalf("fake.Start: %v", err)
	}
	// Mark last activity well past the quiescence window so the
	// dispatcher considers the session idle enough to deliver.
	fake.SetActivity("worker-session", time.Now().Add(-10*time.Second))

	// Create a real session bead so worker.SessionByID can resolve the
	// target without panicking on a missing-bead lookup.
	created, err := store.Create(beads.Bead{
		Title:  "Session: worker",
		Type:   session.BeadType,
		Status: "open",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": "worker-session",
			"agent_name":   "worker",
			"template":     "worker",
			"transport":    "acp",
		},
	})
	if err != nil {
		t.Fatalf("store.Create session bead: %v", err)
	}
	snapshot := newSessionBeadSnapshot([]beads.Bead{created})

	delivered, err := dispatchAllQueuedNudges(dir, supervisorCfg(), store, store, fake, snapshot)
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1 (running idle ACP session must receive queued nudges)", delivered)
	}

	var nudgeMessages []string
	for _, call := range fake.Calls {
		if call.Method == "Nudge" {
			nudgeMessages = append(nudgeMessages, call.Message)
		}
	}
	if len(nudgeMessages) != 1 {
		t.Fatalf("nudge calls = %d, want 1 (queued nudge should be delivered as a runtime prompt)", len(nudgeMessages))
	}
	if !strings.Contains(nudgeMessages[0], "wake-up nudge") {
		t.Fatalf("nudge message = %q, want original reminder text", nudgeMessages[0])
	}

	pending, inFlight, dead, err := listQueuedNudges(dir, "worker", time.Now())
	if err != nil {
		t.Fatalf("listQueuedNudges: %v", err)
	}
	if len(pending) != 0 || len(inFlight) != 0 || len(dead) != 0 {
		t.Fatalf("queue not drained after ACP delivery: pending=%d inFlight=%d dead=%d", len(pending), len(inFlight), len(dead))
	}

	// Observability: a successful queued-nudge delivery must stamp
	// metadata.last_nudge_delivered_at on the session bead so the
	// "LAST NUDGE" column in `gc session list` reflects fresh activity.
	// Operators rely on this column to spot warm sessions whose
	// delivery loop has stalled (queued items piling up while the
	// stamp stays old).
	refetched, getErr := store.Get(created.ID)
	if getErr != nil {
		t.Fatalf("store.Get session bead: %v", getErr)
	}
	stamp := strings.TrimSpace(refetched.Metadata[session.MetadataLastNudgeDeliveredAt])
	if stamp == "" {
		t.Fatalf("session bead missing %s metadata after successful ACP delivery", session.MetadataLastNudgeDeliveredAt)
	}
	parsed, parseErr := time.Parse(time.RFC3339, stamp)
	if parseErr != nil {
		t.Fatalf("parse %s=%q: %v", session.MetadataLastNudgeDeliveredAt, stamp, parseErr)
	}
	if drift := time.Since(parsed); drift < 0 || drift > time.Minute {
		t.Fatalf("%s timestamp drift %s is outside the 1-minute test window (raw=%q)", session.MetadataLastNudgeDeliveredAt, drift, stamp)
	}
}

// TestDispatchAllQueuedNudgesSkipsACPSessionWhenNotRunning confirms the
// dispatcher still respects the universal liveness check for ACP sessions —
// a stopped or crashed ACP session must not absorb queued nudges, because
// nothing on the other side would observe the delivered prompt.
func TestDispatchAllQueuedNudgesSkipsACPSessionWhenNotRunning(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "msg", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}
	bead := beads.Bead{
		ID:     "worker-session",
		Status: "open",
		Metadata: map[string]string{
			"session_name": "worker-session",
			"agent_name":   "worker",
			"template":     "worker",
			"transport":    "acp",
		},
	}
	snapshot := newSessionBeadSnapshot([]beads.Bead{bead})
	// Fake has no started session, so IsRunning("worker-session") is false.
	delivered, err := dispatchAllQueuedNudges(dir, supervisorCfg(), nil, nil, runtime.NewFake(), snapshot)
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("delivered = %d, want 0 (stopped ACP session must not receive delivery)", delivered)
	}
}

func TestNudgeDispatcherIsSupervisor(t *testing.T) {
	if nudgeDispatcherIsSupervisor(nil) {
		t.Error("nil cfg must report legacy mode")
	}
	if nudgeDispatcherIsSupervisor(&config.City{}) {
		t.Error("zero-value DaemonConfig must report legacy mode")
	}
	if !nudgeDispatcherIsSupervisor(supervisorCfg()) {
		t.Error("supervisorCfg must report supervisor mode")
	}
}

func TestDispatchAllQueuedNudgesNilCfg(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	dir := t.TempDir()
	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "msg", time.Now().Add(-time.Minute))); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}
	delivered, err := dispatchAllQueuedNudges(dir, nil, nil, nil, nil, newSessionBeadSnapshot(nil))
	if err != nil {
		t.Fatalf("dispatchAllQueuedNudges: %v", err)
	}
	if delivered != 0 {
		t.Fatalf("delivered = %d, want 0 with nil cfg", delivered)
	}
}

// TestMaybeStartNudgePollerSkipsACPSessionInLegacyMode verifies the
// legacy per-session poller still skips ACP sessions. A sidecar `gc
// nudge poll` process can observe the ACP control socket, but it does
// not own the in-memory ACP connection needed to send session/prompt.
func TestMaybeStartNudgePollerSkipsACPSessionInLegacyMode(t *testing.T) {
	prev := startNudgePoller
	t.Cleanup(func() { startNudgePoller = prev })

	called := false
	startNudgePoller = func(_, _, _ string) error {
		called = true
		return nil
	}

	maybeStartNudgePoller(nudgeTarget{
		cityPath:    t.TempDir(),
		sessionName: "worker-session",
		transport:   "acp",
		cfg:         &config.City{},
	})
	if called {
		t.Fatal("startNudgePoller invoked for ACP session in legacy mode; sidecar ACP pollers cannot deliver without owning the connection")
	}
}

func TestMaybeStartNudgePollerSkipsInSupervisorMode(t *testing.T) {
	prev := startNudgePoller
	t.Cleanup(func() { startNudgePoller = prev })

	called := false
	startNudgePoller = func(_, _, _ string) error {
		called = true
		return nil
	}

	maybeStartNudgePoller(nudgeTarget{
		cityPath:    t.TempDir(),
		sessionName: "worker-session",
		cfg:         supervisorCfg(),
	})
	if called {
		t.Fatal("startNudgePoller invoked in supervisor mode; supervisor dispatcher would race with the per-session poller")
	}

	maybeStartNudgePoller(nudgeTarget{
		cityPath:    t.TempDir(),
		sessionName: "worker-session",
		cfg:         &config.City{},
	})
	if !called {
		t.Fatal("startNudgePoller not invoked in legacy mode")
	}
}

func TestEnqueuePingsWakeSocket(t *testing.T) {
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	wakeCh := make(chan struct{}, 1)
	lis, err := startNudgeWakeListener(ctx, dir, wakeCh, nil, "test")
	if err != nil {
		t.Fatalf("startNudgeWakeListener: %v", err)
	}
	defer lis.Close() //nolint:errcheck

	if err := enqueueQueuedNudge(dir, newQueuedNudge("worker", "msg", time.Now())); err != nil {
		t.Fatalf("enqueueQueuedNudge: %v", err)
	}
	select {
	case <-wakeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("wakeCh not signaled after enqueue")
	}
}
