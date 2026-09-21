package main

import (
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Scope: buildIdleTracker's registration of the transcript-quiescence arm,
// pinned separately from the pane arm's registration in cmd_start_test.go.
// The two arms share one registry and one exemption set but resolve
// independent durations, so the question this suite answers is whether the
// registration walk reaches the same session names for both.
//
// Delegated elsewhere: resolution precedence is idle_tracker_stall_test.go;
// the reconciler's use of the arm is session_reconciler_stall_test.go.
//
//	go test ./cmd/gc/ -run TestBuildIdleTrackerStall

// TestBuildIdleTrackerStall_ArmsWithoutAnIdleTimeout pins the independence of
// the two arms at registration. buildIdleTracker used to return nil the
// moment no agent had an idle_timeout, and its per-agent loop skipped any
// agent whose idle timeout was zero -- so a city that armed only
// stall_timeout would have got no tracker at all, and the feature would have
// been dead with every other test still green.
func TestBuildIdleTrackerStall_ArmsWithoutAnIdleTimeout(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{Name: "mayor", Dir: "local-core", StallTimeout: "6h"}},
	}
	template := cfg.Agents[0].QualifiedName()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	it, ok := buildIdleTracker(cfg, "city", "", runtime.NewFake()).(*memoryIdleTracker)
	if !ok {
		t.Fatalf("buildIdleTracker returned %T, want *memoryIdleTracker for a stall-only city", it)
	}
	if _, ok := it.stall.byTemplate[template]; !ok {
		t.Fatalf("stall timeout not registered for template %q in %v", template, it.stall.byTemplate)
	}
	if len(it.idle.byTemplate) != 0 || len(it.idle.bySession) != 0 {
		t.Fatalf("idle arm registered %v / %v with no idle_timeout configured", it.idle.bySession, it.idle.byTemplate)
	}
	if !it.checkStalled("mayor-ci-abc123", template, at(now.Add(-7*time.Hour)), now) {
		t.Fatalf("stall-only city did not stall out a session of template %q", template)
	}
}

// TestBuildIdleTrackerStall_NoTimeoutsAtAllStaysNil pins the disabled default
// end to end: a city that configures neither timeout must still get a nil
// tracker, so the reconciler does no per-session work at all.
func TestBuildIdleTrackerStall_NoTimeoutsAtAllStaysNil(t *testing.T) {
	cfg := &config.City{Agents: []config.Agent{{Name: "mayor", Dir: "local-core"}}}
	if it := buildIdleTracker(cfg, "city", "", runtime.NewFake()); it != nil {
		t.Fatalf("buildIdleTracker = %v, want nil when neither timeout is configured", it)
	}
}

// TestBuildIdleTrackerStall_PoolAgentGetsTemplateFallback pins that the pool
// path arms both registries. A pool agent's runtime session name is minted
// from a bead ID, so only the template fallback can ever match it -- the
// hole ci-3bkfvj found in the pane arm, which the stall arm would otherwise
// reproduce exactly.
func TestBuildIdleTrackerStall_PoolAgentGetsTemplateFallback(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{
			Name:              "builder",
			Dir:               "local-core",
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(5),
			StallTimeout:      "6h",
		}},
	}
	template := cfg.Agents[0].QualifiedName()
	sessionName := sessionNameFromBeadID("fm-r56l0x")
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	it, ok := buildIdleTracker(cfg, "city", "", runtime.NewFake()).(*memoryIdleTracker)
	if !ok {
		t.Fatalf("buildIdleTracker returned %T, want *memoryIdleTracker", it)
	}
	if _, ok := it.stall.byTemplate[template]; !ok {
		t.Fatalf("stall tracker missing template %q in %v", template, it.stall.byTemplate)
	}
	if !it.checkStalled(sessionName, template, at(now.Add(-7*time.Hour)), now) {
		t.Fatalf("pool session %q did not stall out via template %q", sessionName, template)
	}
}

// TestBuildIdleTrackerStall_AlwaysNamedSessionIsExemptFromBothArms pins the
// shared exemption at the registration layer. A mode="always" named session
// sharing a template with pool siblings is registered for neither arm's
// per-name lookup and must not inherit either template fallback: the newer
// arm must not reap the sessions the older exemption protects.
func TestBuildIdleTrackerStall_AlwaysNamedSessionIsExemptFromBothArms(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{
			Name:              "builder",
			Dir:               "local-core",
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(5),
			IdleTimeout:       "1h",
			StallTimeout:      "6h",
		}},
		NamedSessions: []config.NamedSession{{
			Template: "builder", Dir: "local-core", Mode: "always",
		}},
	}
	template := cfg.Agents[0].QualifiedName()
	named := config.NamedSessionRuntimeName("city", cfg.Workspace, template)
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

	it, ok := buildIdleTracker(cfg, "city", "", runtime.NewFake()).(*memoryIdleTracker)
	if !ok {
		t.Fatalf("buildIdleTracker returned %T, want *memoryIdleTracker", it)
	}
	if it.checkStalled(named, template, at(now.Add(-99*time.Hour)), now) {
		t.Fatalf("always-mode named session %q stalled out via template %q; the exemption must cover both arms", named, template)
	}
}
