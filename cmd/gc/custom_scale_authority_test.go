package main

import (
	"io"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// TestBuildDesiredState_CustomScaleCheckRemainsAuthoritativeWhenCold prevents a
// generic routed bead from repeatedly waking a cold pool whose configured
// selector has returned zero. The worker's custom work_query can exclude that
// bead, in which case a generic fallback creates an unclaimable start/drain
// loop instead of respecting the configured scale policy.
func TestBuildDesiredState_CustomScaleCheckRemainsAuthoritativeWhenCold(t *testing.T) {
	minSessions, maxSessions := 0, 1
	store := beads.NewMemStore()
	if _, err := store.Create(beads.Bead{
		ID:     "routed-but-custom-excluded",
		Status: "open",
		Type:   "task",
		Metadata: map[string]string{
			"gc.routed_to": "worker",
		},
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{Agents: []config.Agent{{
		Name:              "worker",
		MinActiveSessions: &minSessions,
		MaxActiveSessions: &maxSessions,
		ScaleCheck:        "printf 0",
		WorkQuery:         "printf '[]'",
		StartCommand:      "true",
	}}}

	got := buildDesiredState(
		"test-city", t.TempDir(), time.Now(), cfg, runtime.NewFake(), store, io.Discard,
	)
	if demand := got.ScaleCheckCounts["worker"]; demand != 0 {
		t.Fatalf("ScaleCheckCounts[worker] = %d, want 0: custom scale_check must not be overridden by generic cold demand", demand)
	}
	if len(got.State) != 0 {
		t.Fatalf("desired sessions = %d, want 0", len(got.State))
	}
}

func TestBuildDesiredState_CustomScaleCheckCanStillRequestColdCapacity(t *testing.T) {
	minSessions, maxSessions := 0, 1
	cfg := &config.City{Agents: []config.Agent{{
		Name:              "worker",
		MinActiveSessions: &minSessions,
		MaxActiveSessions: &maxSessions,
		ScaleCheck:        "printf 1",
		WorkQuery:         "printf '[]'",
		StartCommand:      "true",
	}}}

	got := buildDesiredState(
		"test-city", t.TempDir(), time.Now(), cfg, runtime.NewFake(), beads.NewMemStore(), io.Discard,
	)
	if demand := got.ScaleCheckCounts["worker"]; demand != 1 {
		t.Fatalf("ScaleCheckCounts[worker] = %d, want 1", demand)
	}
	if len(got.State) != 1 {
		t.Fatalf("desired sessions = %d, want 1", len(got.State))
	}
}
