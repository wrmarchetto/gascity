package main

import (
	"testing"
	"time"
)

// Scope: the transcript-quiescence arm of the idle tracker (checkStalled and
// its registration), pinned separately from the pane-activity arm in
// idle_tracker_test.go because the two signals resolve independent timeouts
// off the same session-name/template registry.
//
// Why this suite exists: pane output tracks a Claude Code TUI's spinner, so a
// session hung mid-turn is never idle by the pane measure for as long as it
// hangs -- measured 2026-09-21, two mid-turn sessions held #{window_activity}
// at 0s age across 105s while three idle sessions aged the full 105s
// (ci-jvbkio). The stall arm exists to reap exactly that shape, so every test
// here drives a FRESH pane alongside a stale transcript.
//
// Delegated elsewhere: the reconciler's composition of the two arms into
// TimerFacts.Triggered lives in session_reconciler_stall_test.go; the decision
// ladder is internal/session/lifecycle_timers_test.go.
//
//	go test ./cmd/gc/ -run TestIdleTrackerStall

// at returns a probe that answers with a fixed time.
func at(t time.Time) func() time.Time { return func() time.Time { return t } }

// probeMustNotRun is a probe that FAILS the test if it is called. It pins the
// laziness the reconciler depends on: attributing a transcript costs a path
// resolution and a stat per session per tick, so a session with no stall
// timeout registered must never pay for one. A probe that quietly returned a
// zero time instead would let that cost creep back with every test still
// green.
func probeMustNotRun(t *testing.T) func() time.Time {
	t.Helper()
	return func() time.Time {
		t.Errorf("transcript probe ran for a session with no stall timeout registered")
		return time.Time{}
	}
}

// TestIdleTrackerStall_TriggersOnQuiescentTranscript pins the arm's whole
// reason for existing: a transcript older than the stall timeout is a stall,
// and the pane is not consulted. The pane is deliberately absent from this
// call -- checkStalled takes no provider -- which is what makes the signal
// independent rather than a second threshold on the first.
func TestIdleTrackerStall_TriggersOnQuiescentTranscript(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.setStallTimeout("mayor", 6*time.Hour)

	now := time.Now()
	if !it.checkStalled("mayor", "", at(now.Add(-12*time.Hour)), now) {
		t.Fatalf("checkStalled = false for a transcript 12h stale against a 6h stall timeout, want true")
	}
	if it.checkStalled("mayor", "", at(now.Add(-1*time.Hour)), now) {
		t.Fatalf("checkStalled = true for a transcript 1h stale against a 6h stall timeout, want false")
	}
}

// TestIdleTrackerStall_UnregisteredSessionNeverStalls pins the disabled
// default. An empty stall_timeout must not fall back to the idle timeout:
// the two measure different channels and the idle value is chosen against a
// distribution the transcript does not share.
func TestIdleTrackerStall_UnregisteredSessionNeverStalls(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.setTimeout("mayor", 5*time.Minute)

	now := time.Now()
	if it.checkStalled("mayor", "", probeMustNotRun(t), now) {
		t.Fatalf("checkStalled = true with no stall timeout registered, want false (idle timeout must not stand in)")
	}
}

// TestIdleTrackerStall_TemplateFallbackResolvesPoolSession mirrors the idle
// arm's pool behavior. A pool session's runtime name is minted from a bead ID
// at sling time, so a per-name registration can never match it; without the
// template fallback the stall arm would silently cover only named sessions,
// which is the same hole ci-3bkfvj found in the idle arm.
func TestIdleTrackerStall_TemplateFallbackResolvesPoolSession(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.setStallTimeoutForTemplate("local-core/builder", 6*time.Hour)

	now := time.Now()
	sessionName := sessionNameFromBeadID("fm-miv1io")
	if !it.checkStalled(sessionName, "local-core/builder", at(now.Add(-7*time.Hour)), now) {
		t.Fatalf("checkStalled = false for bead-derived pool session via template fallback, want true")
	}
}

// TestIdleTrackerStall_PerNameBeatsTemplate pins precedence: an explicitly
// registered session name wins over the template it belongs to, matching
// checkIdle. A pool member given a longer stall bound of its own must not
// have the template's shorter one applied to it.
func TestIdleTrackerStall_PerNameBeatsTemplate(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.setStallTimeoutForTemplate("local-core/builder", 1*time.Hour)
	it.setStallTimeout("builder-ci-abc123", 12*time.Hour)

	now := time.Now()
	if it.checkStalled("builder-ci-abc123", "local-core/builder", at(now.Add(-6*time.Hour)), now) {
		t.Fatalf("checkStalled = true at 6h; the per-name 12h registration must win over the template's 1h")
	}
}

// TestIdleTrackerStall_ExemptSessionIgnoresTemplateFallback pins that the
// mode="always" exemption covers both arms off one registration. A named
// always-on session sharing a template with pool siblings must not be reaped
// by either signal, and an exemption that covered only the pane arm would
// leave the stall arm killing the session the exemption exists to protect.
func TestIdleTrackerStall_ExemptSessionIgnoresTemplateFallback(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.setStallTimeoutForTemplate("local-core/builder", 1*time.Hour)
	it.exemptTemplateFallbackForSession("builder")

	now := time.Now()
	if it.checkStalled("builder", "local-core/builder", probeMustNotRun(t), now) {
		t.Fatalf("checkStalled = true for a template-fallback-exempt session, want false")
	}
}

// TestIdleTrackerStall_UnknownTranscriptNeverStalls pins the fail-open on a
// missing signal. A zero lastTranscript means the reconciler could not
// attribute a transcript to this session at all -- no provider support, no
// session key, a file not yet written -- and that is an ABSENT reading, not
// an infinitely old one. Treating it as stale would reap every session on a
// provider gc cannot read, which is most of them.
func TestIdleTrackerStall_UnknownTranscriptNeverStalls(t *testing.T) {
	t.Parallel()

	it := newIdleTracker()
	it.setStallTimeout("mayor", 6*time.Hour)

	if it.checkStalled("mayor", "", at(time.Time{}), time.Now()) {
		t.Fatalf("checkStalled = true for a zero (unattributable) transcript time, want false")
	}
}
