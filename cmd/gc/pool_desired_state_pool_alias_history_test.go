package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

func poolNameAliasHistoryCity(maxActive int) *config.City {
	return &config.City{Agents: []config.Agent{{
		Name:              "worker",
		Dir:               "pack",
		MaxActiveSessions: intPtr(maxActive),
	}}}
}

func poolAliasHistoryRequests(states []PoolDesiredState) map[string]SessionRequest {
	requests := make(map[string]SessionRequest)
	for _, state := range states {
		for _, request := range state.Requests {
			requests[request.WorkBeadID] = request
		}
	}
	return requests
}

func poolAliasHistoryRequestCount(states []PoolDesiredState) int {
	count := 0
	for _, state := range states {
		count += len(state.Requests)
	}
	return count
}

func TestPoolOwnedHistoricalAlias(t *testing.T) {
	info := seedSessionInfo(beads.Bead{
		ID: "sess-1",
		Metadata: map[string]string{
			"template":      "pack/worker",
			"alias":         "pack/worker-1",
			"alias_history": "pack/worker",
		},
	})
	if got := poolOwnedHistoricalAlias(poolNameAliasHistoryCity(1), info); got != "" {
		t.Errorf("singleton historical alias = %q, want empty", got)
	}
	if got := poolOwnedHistoricalAlias(poolNameAliasHistoryCity(2), info); got != "pack/worker" {
		t.Errorf("multi-slot historical alias = %q, want pack/worker", got)
	}
	info.Alias = "pack/worker"
	if got := poolOwnedHistoricalAlias(poolNameAliasHistoryCity(2), info); got != "" {
		t.Errorf("current alias = %q, want empty", got)
	}
	info.Alias = "pack/worker-1"
	info.AliasHistory = []string{"slit"}
	if got := poolOwnedHistoricalAlias(poolNameAliasHistoryCity(2), info); got != "" {
		t.Errorf("rotated handle = %q, want empty", got)
	}
}

// TestComputePoolDesiredStates_PoolNameHistoryKeepsOnlyHeldWork proves that
// the singleton pool name is pool-owned after a cap raise. The session keeps
// it in alias_history solely to preserve work it already claimed; open work
// addressed to the pool must remain unbound so it can produce its own demand.
func TestComputePoolDesiredStates_PoolNameHistoryKeepsOnlyHeldWork(t *testing.T) {
	cfg := poolNameAliasHistoryCity(2)
	live := beads.Bead{
		ID:     "sess-1",
		Status: "open",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel},
		Metadata: map[string]string{
			"template":             "pack/worker",
			"agent_name":           "pack/worker-1",
			"alias":                "pack/worker-1",
			"alias_history":        "pack/worker",
			"session_name":         PoolSessionName("pack/worker", "sess-1"),
			"pool_slot":            "1",
			poolManagedMetadataKey: boolMetadata(true),
		},
	}
	work := []beads.Bead{
		workBead("held", "pack/worker", "pack/worker", "in_progress", 1),
		workBead("queued", "pack/worker", "pack/worker", "open", 1),
	}

	states := ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads([]beads.Bead{live}), nil)
	if got := poolAliasHistoryRequestCount(states); got != 2 {
		t.Fatalf("requests = %d, want 2; got %#v", got, states)
	}
	requests := poolAliasHistoryRequests(states)
	if got := requests["held"]; got.Tier != "resume" || got.SessionBeadID != live.ID {
		t.Errorf("held request = %+v, want resume for %s", got, live.ID)
	}
	if got := requests["queued"]; got.Tier != "wake-known-identity" || got.SessionBeadID != "" {
		t.Errorf("queued request = %+v, want an unbound wake-known-identity request", got)
	}
}

// TestReleaseOrphanedPoolAssignments_PreservesPoolNameHistoryClaim proves the
// related safety property: the historical pool name remains sufficient owner
// evidence for work claimed before the cap raise. Removing it during session
// synchronization reopens live in-progress work on the next reconcile tick.
func TestReleaseOrphanedPoolAssignments_PreservesPoolNameHistoryClaim(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)}
	live, err := store.Create(beads.Bead{
		Title:  "pack/worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/worker"},
		Metadata: map[string]string{
			"template":             "pack/worker",
			"session_name":         "pack-worker-live",
			"agent_name":           "pack/worker",
			"alias":                "pack/worker",
			"state":                "awake",
			"session_origin":       "ephemeral",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.Create(beads.Bead{
		Title:    "work claimed before cap raise",
		Assignee: "pack/worker",
		Metadata: map[string]string{"gc.routed_to": "pack/worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(work.ID, beads.UpdateOpts{Status: stringPtr("in_progress")}); err != nil {
		t.Fatal(err)
	}
	work, err = store.Get(work.ID)
	if err != nil {
		t.Fatal(err)
	}

	desired := map[string]TemplateParams{
		"pack-worker-live": {TemplateName: "pack/worker", InstanceName: "pack/worker-1", Alias: "pack/worker-1", Command: "claude", PoolSlot: 1},
	}
	var stderr bytes.Buffer
	syncSessionBeads("", store, desired, runtime.NewFake(), allConfiguredDS(desired), poolNameAliasHistoryCity(2), clk, &stderr, false)
	live, err = store.Get(live.ID)
	if err != nil {
		t.Fatal(err)
	}

	released := releaseOrphanedPoolAssignmentsFromBeads(store, poolNameAliasHistoryCity(2), "", []beads.Bead{live}, []beads.Bead{work}, nil, nil, nil)
	if len(released) != 0 {
		t.Fatalf("released = %#v, want live claim preserved", released)
	}
	got, err := store.Get(work.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "in_progress" || got.Assignee != "pack/worker" {
		t.Errorf("work after orphan check = status %q assignee %q, want in_progress/pack/worker", got.Status, got.Assignee)
	}
}
