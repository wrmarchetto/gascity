package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/nudgequeue"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: the mid-turn (PostToolUse) drain -- the only path by which anything
// reaches a session that is inside one long autonomous turn. Two invariants,
// and they pull in opposite directions, which is why they are pinned together:
// the drain must write NOTHING when there is no queued nudge (it runs on every
// tool call of every managed session), and when there IS one it must both
// deliver it in the refusal shape and CONSUME it (or it re-fires on every
// subsequent tool call for the rest of the turn).
//
// The consumed half is asserted against the queue, never against the output. A
// queue entry that is present but never read is exactly the failure this whole
// line of work started from, and it passes any presence check.
//
// Delegated elsewhere: whether the refusal shape actually reaches a model is a
// property of Claude Code, not of gc -- see the measured table in
// hook_output_claude.go. This suite pins gc's side of that contract only.
//
// Run: go test ./cmd/gc/ -run MidTurnDrain

// midTurnDrainCity builds a city with one active session bead and returns its
// directory and the session bead id.
func midTurnDrainCity(t *testing.T) (string, string, beads.Store) {
	t.Helper()
	clearGCEnv(t)
	disableManagedDoltRecoveryForTest(t)
	t.Setenv("GC_BEADS", "file")
	cityDir := t.TempDir()
	writeNamedSessionCityTOML(t, cityDir)
	t.Setenv("GC_CITY", cityDir)

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	created, err := store.Create(beads.Bead{
		Title: "Session: worker", Type: session.BeadType, Status: "open",
		Labels: []string{session.LabelSession},
		Metadata: map[string]string{
			"session_name": "worker-session", "agent_name": "worker",
			"template": "worker", "state": string(session.StateActive),
		},
	})
	if err != nil {
		t.Fatalf("store.Create session: %v", err)
	}
	return cityDir, created.ID, store
}

func midTurnDrainEnqueue(t *testing.T, cityDir, sessionID, text string, store beads.Store) {
	t.Helper()
	item := newQueuedNudgeWithOptions("worker", text, "mail", time.Now().Add(-time.Minute),
		queuedNudgeOptions{SessionID: sessionID})
	if err := enqueueQueuedNudgeWithStore(cityDir, beads.NudgesStore{Store: store}, item); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
}

// midTurnPendingCount counts queue entries still awaiting delivery.
func midTurnPendingCount(t *testing.T, cityDir string) int {
	t.Helper()
	state, err := nudgequeue.LoadState(cityDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	return len(state.Pending)
}

func TestMidTurnDrainWritesNothingWithNoQueuedNudge(t *testing.T) {
	// The cost guarantee. This runs after every tool call of every managed
	// session, so an empty queue must produce an empty stdout -- not a clock
	// line, not a formula step, not "{}". Any of those becomes a refusal on
	// this event (hook_output_claude.go), which would interrupt an agent on
	// every single tool call to tell it the time.
	_, sessionID, _ := midTurnDrainCity(t)

	var stdout, stderr bytes.Buffer
	if code := cmdNudgeDrainWithFormat([]string{sessionID}, true, hookOutputFormatClaude,
		claudeHookEventPostToolUse, &stdout, &stderr); code != 0 {
		t.Fatalf("drain = %d, want 0; stderr=%s", code, stderr.String())
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("mid-turn drain with an empty queue wrote %q, want nothing at all", got)
	}
}

func TestMidTurnDrainDeliversAQueuedNudgeAsARefusal(t *testing.T) {
	cityDir, sessionID, store := midTurnDrainCity(t)
	midTurnDrainEnqueue(t, cityDir, sessionID, "REDIRECT: the design is superseded", store)

	var stdout, stderr bytes.Buffer
	if code := cmdNudgeDrainWithFormat([]string{sessionID}, true, hookOutputFormatClaude,
		claudeHookEventPostToolUse, &stdout, &stderr); code != 0 {
		t.Fatalf("drain = %d, want 0; stderr=%s", code, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("stdout is not one JSON document (%v): %q", err, stdout.String())
	}
	if payload["decision"] != "block" {
		t.Fatalf("decision = %v, want block -- the only shape measured to reach a model mid-turn", payload["decision"])
	}
	reason, _ := payload["reason"].(string)
	if !strings.Contains(reason, "REDIRECT: the design is superseded") {
		t.Fatalf("reason = %q, want it to carry the queued message", reason)
	}
}

func TestMidTurnDrainConsumesTheNudgeSoItDoesNotRefireEveryToolCall(t *testing.T) {
	// The invariant ci-auiqx3 named: assert on CONSUMED, not on queued. A
	// delivered-but-not-consumed nudge blocks every remaining tool call of the
	// turn with the same message, which is worse than never delivering it.
	//
	// The claim-on-delivery this relies on is claimDueQueuedNudgesForTarget
	// and predates this event, so this is a characterization test, not a
	// guarantee introduced here. It is worth its place anyway: consumption was
	// merely tidy at a prompt boundary, where a re-fire costs one duplicate
	// line, and is load-bearing mid-turn, where it costs every remaining tool
	// call of the turn.
	cityDir, sessionID, store := midTurnDrainCity(t)
	midTurnDrainEnqueue(t, cityDir, sessionID, "read me once", store)
	if got := midTurnPendingCount(t, cityDir); got != 1 {
		t.Fatalf("pending before drain = %d, want 1 (fixture is wrong, not the code)", got)
	}

	var stdout, stderr bytes.Buffer
	if code := cmdNudgeDrainWithFormat([]string{sessionID}, true, hookOutputFormatClaude,
		claudeHookEventPostToolUse, &stdout, &stderr); code != 0 {
		t.Fatalf("first drain = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "read me once") {
		t.Fatalf("first drain did not deliver: %q", stdout.String())
	}
	if got := midTurnPendingCount(t, cityDir); got != 0 {
		t.Fatalf("pending after drain = %d, want 0 -- the nudge was delivered but not consumed", got)
	}

	// The next tool call in the same turn.
	var stdout2, stderr2 bytes.Buffer
	if code := cmdNudgeDrainWithFormat([]string{sessionID}, true, hookOutputFormatClaude,
		claudeHookEventPostToolUse, &stdout2, &stderr2); code != 0 {
		t.Fatalf("second drain = %d, want 0; stderr=%s", code, stderr2.String())
	}
	if got := stdout2.String(); got != "" {
		t.Fatalf("second drain re-fired with %q, want nothing", got)
	}
}

func TestPromptBoundaryDrainStillEmitsItsOrientationContext(t *testing.T) {
	// The suppression must be keyed on the mid-turn event, not applied to
	// every drain: a UserPromptSubmit drain with an empty queue still owes the
	// session its clock line, and losing that silently is the regression the
	// mid-turn branch could most easily cause.
	_, sessionID, _ := midTurnDrainCity(t)

	var stdout, stderr bytes.Buffer
	if code := cmdNudgeDrainWithFormat([]string{sessionID}, true, "", "", &stdout, &stderr); code != 0 {
		t.Fatalf("drain = %d, want 0; stderr=%s", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("prompt-boundary drain wrote nothing; the mid-turn suppression leaked onto the default event")
	}
}

func TestMidTurnDrainDefaultEventIsUnchanged(t *testing.T) {
	// An empty --hook-event must resolve to the prompt boundary, so every
	// caller and every registered city hook that names no event keeps its
	// behavior.
	if got := normalizeNudgeHookEvent(""); got != nudgeHookEventUserPromptSubmit {
		t.Fatalf("normalizeNudgeHookEvent(\"\") = %q, want %q", got, nudgeHookEventUserPromptSubmit)
	}
	if isMidTurnNudgeHookEvent("") || isMidTurnNudgeHookEvent(nudgeHookEventUserPromptSubmit) {
		t.Fatal("a boundary event was classified as mid-turn")
	}
	if !isMidTurnNudgeHookEvent("PostToolUse") {
		t.Fatal("PostToolUse was not classified as mid-turn")
	}
}
