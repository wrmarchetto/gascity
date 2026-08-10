package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/clock"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: what a multi-slot pool's session beads are allowed to keep in
// alias_history. The invariant is one line -- the pool's OWN name is never one
// slot's private history -- and it is enforced in syncSessionBeads, so these
// tests drive the real sync rather than the pruning helper.
//
// Why the suite exists. alias_history is delivery continuity across a ROTATED
// alias: a namepool handle like "nux" -> "slit" is one session's successive
// names, and session.AssigneeIdentities reads the history back as a full
// assignee identity so work addressed to the old handle still resolves to its
// owner. The canonical singleton pool alias is a different kind of name. At
// max_active_sessions=1 it is the agent template's name AND the session's
// alias (config.Agent.UsesCanonicalSingletonPoolIdentity); raising the cap
// renames that session to a slot ("pack/worker" -> "pack/worker-1") and the
// generic rotation rule filed the pool's name in the slot's history. From then
// on every bead hand-assigned to the pool resolved to that ONE session bead, so
// computePoolDesiredStates turned them all into resume requests for a single
// session and isDuplicateSessionRequest dropped the rest -- pool demand pinned
// at 1 at any cap, for as long as that session bead lived (ci-ako1, measured
// against ci-nfoo during the ci-rdbw window).
//
// The prerequisite is ci-c000, not an implementation detail of these tests:
// dropping the bare name from a session's identities is only safe because work
// addressed to a pool by name is now claimable by any of its slots. Without
// that, the beads would fall to the wake-known-identity tier, wake a session
// that could not claim them, and loop.
//
// Delegated elsewhere: that claim/demand tier is
// cmd/gc/build_desired_state_pool_alias_demand_test.go and
// internal/config/workquery_pool_alias_test.go. The generic rotation rule these
// tests carve an exception into is internal/session/names_test.go.
//
//	go test ./cmd/gc/ -run PoolNameAliasHistory

// poolNameAliasHistoryCity is a two-slot pool. The cap matters: at 1 the agent
// uses the canonical singleton identity and its alias legitimately IS the bare
// name, which is the case the prune must NOT touch.
func poolNameAliasHistoryCity(maxActive int) *config.City {
	return &config.City{
		Agents: []config.Agent{{
			Name:              "worker",
			Dir:               "pack",
			MaxActiveSessions: intPtr(maxActive),
		}},
	}
}

func TestPoolNameAliasHistoryDroppedOnCapRaise(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 8, 9, 12, 55, 48, 0, time.UTC)}
	sp := runtime.NewFake()

	// Created under max_active_sessions=1, so the alias IS the pool's name.
	live, err := store.Create(beads.Bead{
		Title:  "pack/worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/worker"},
		Metadata: map[string]string{
			"template":                "pack/worker",
			"session_name":            "pack-worker-live",
			"agent_name":              "pack/worker",
			"alias":                   "pack/worker",
			"canonical_instance_name": "pack/worker",
			"state":                   "awake",
			"session_origin":          "ephemeral",
			poolManagedMetadataKey:    boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// The cap raise landing mid-window: the reconciler now wants slot 1.
	desired := map[string]TemplateParams{
		"pack-worker-live": {
			TemplateName: "pack/worker",
			InstanceName: "pack/worker-1",
			Alias:        "pack/worker-1",
			Command:      "claude",
			PoolSlot:     1,
		},
	}

	var stderr bytes.Buffer
	syncSessionBeads("", store, desired, sp, allConfiguredDS(desired), poolNameAliasHistoryCity(2), clk, &stderr, false)

	got, err := store.Get(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Asserted before the alias_history value because this is the consequence
	// and that is only the mechanism: what breaks is the pool's name becoming
	// one slot's private assignee identity, whichever metadata key carries it.
	for _, identity := range session.AssigneeIdentities(seedSessionInfo(got)) {
		if identity == "pack/worker" {
			t.Fatalf("session %s answers to the pool's own name %q; every bead assigned to the pool would resolve to this one slot", got.ID, identity)
		}
	}
	if got.Metadata["alias"] != "pack/worker-1" {
		t.Fatalf("alias = %q, want pack/worker-1", got.Metadata["alias"])
	}
	if history := got.Metadata["alias_history"]; history != "" {
		t.Fatalf("alias_history = %q, want empty", history)
	}
}

func TestPoolNameAliasHistoryStopsPinningPoolDemandToOneSlot(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 8, 9, 12, 55, 48, 0, time.UTC)}
	sp := runtime.NewFake()
	cfg := poolNameAliasHistoryCity(2)

	live, err := store.Create(beads.Bead{
		Title:  "pack/worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/worker"},
		Metadata: map[string]string{
			"template":                "pack/worker",
			"session_name":            "pack-worker-live",
			"agent_name":              "pack/worker",
			"alias":                   "pack/worker",
			"canonical_instance_name": "pack/worker",
			"state":                   "awake",
			"session_origin":          "ephemeral",
			poolManagedMetadataKey:    boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	desired := map[string]TemplateParams{
		"pack-worker-live": {
			TemplateName: "pack/worker",
			InstanceName: "pack/worker-1",
			Alias:        "pack/worker-1",
			Command:      "claude",
			PoolSlot:     1,
		},
	}
	var stderr bytes.Buffer
	syncSessionBeads("", store, desired, sp, allConfiguredDS(desired), cfg, clk, &stderr, false)

	renamed, err := store.Get(live.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Two beads hand-assigned to the pool by name, which is the shape ci-c000
	// made claimable by any slot. They are the pool's demand, not this slot's
	// work: routing them at the session is what collapsed them into duplicate
	// resume requests for one session bead, of which isDuplicateSessionRequest
	// keeps exactly one.
	work := []beads.Bead{
		workBead("w1", "", "pack/worker", "open", 1),
		workBead("w2", "", "pack/worker", "open", 1),
	}

	// No scale_check counts are passed, so the correct result is NO request at
	// all: pool-addressed work raises demand through the pool-alias tier ci-c000
	// landed (controllerDemandPoolAliasTarget, counted per bead and covered by
	// build_desired_state_pool_alias_demand_test.go), not through this one. What
	// this asserts is that the resume tier stops claiming the work on the slot's
	// behalf -- before the fix it resolved both beads to this session bead and
	// isDuplicateSessionRequest kept one, so the reconciler believed a live slot
	// was already carrying every bead the pool had and asked for nothing more.
	for _, state := range ComputePoolDesiredStates(cfg, work, sessionInfosFromBeads([]beads.Bead{renamed}), nil) {
		for _, req := range state.Requests {
			if req.SessionBeadID == live.ID {
				t.Fatalf("request %+v routes pool-addressed work to slot %s; the pool's demand is pinned to that one session", req, live.ID)
			}
		}
	}
}

func TestPoolNameAliasHistoryPrunedFromAlreadyRenamedSlot(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 8, 9, 13, 0, 0, 0, time.UTC)}
	sp := runtime.NewFake()

	// The alias is already correct, so this tick takes the revalidation path and
	// never recomputes alias_history. A write-time-only fix leaves this bead
	// pinning the pool's name for the rest of its life.
	live, err := store.Create(beads.Bead{
		Title:  "pack/worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/worker"},
		Metadata: map[string]string{
			"template":             "pack/worker",
			"session_name":         "pack-worker-live",
			"agent_name":           "pack/worker-1",
			"alias":                "pack/worker-1",
			"alias_history":        "pack/worker,nux",
			"pool_slot":            "1",
			"state":                "awake",
			"session_origin":       "ephemeral",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	desired := map[string]TemplateParams{
		"pack-worker-live": {
			TemplateName: "pack/worker",
			InstanceName: "pack/worker-1",
			Alias:        "pack/worker-1",
			Command:      "claude",
			PoolSlot:     1,
		},
	}

	var stderr bytes.Buffer
	syncSessionBeads("", store, desired, sp, allConfiguredDS(desired), poolNameAliasHistoryCity(2), clk, &stderr, false)

	got, err := store.Get(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if history := got.Metadata["alias_history"]; history != "nux" {
		t.Fatalf("alias_history = %q, want %q: the pool name is pruned and the rotated handle kept", history, "nux")
	}
}

func TestPoolNameAliasHistoryKeepsRotatedHandleAcrossSlotRename(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 8, 9, 13, 5, 0, 0, time.UTC)}
	sp := runtime.NewFake()

	// A genuine rotation: "nux" is this session's own former name, not the
	// pool's, so nothing may drop it. Without this the prune could be written as
	// "clear alias_history for pool slots" and every test above would still pass.
	live, err := store.Create(beads.Bead{
		Title:  "pack/worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/worker"},
		Metadata: map[string]string{
			"template":             "pack/worker",
			"session_name":         "pack-worker-live",
			"agent_name":           "pack/worker-1",
			"alias":                "nux",
			"pool_slot":            "1",
			"state":                "awake",
			"session_origin":       "ephemeral",
			poolManagedMetadataKey: boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	desired := map[string]TemplateParams{
		"pack-worker-live": {
			TemplateName: "pack/worker",
			InstanceName: "pack/worker-1",
			Alias:        "pack/worker-1",
			Command:      "claude",
			PoolSlot:     1,
		},
	}

	var stderr bytes.Buffer
	syncSessionBeads("", store, desired, sp, allConfiguredDS(desired), poolNameAliasHistoryCity(2), clk, &stderr, false)

	got, err := store.Get(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if history := got.Metadata["alias_history"]; history != "nux" {
		t.Fatalf("alias_history = %q, want %q: a rotated handle is delivery continuity and must survive", history, "nux")
	}
}

func TestPoolNameAliasHistoryKeptForDeferredSingletonPool(t *testing.T) {
	store := beads.NewMemStore()
	clk := &clock.Fake{Time: time.Date(2026, 8, 9, 13, 10, 0, 0, time.UTC)}
	sp := runtime.NewFake()

	// At max_active_sessions=1 the pool's name is not a pool-wide address at
	// all: it is this one session's own identity, so the prune must be off. The
	// state that makes the difference observable is the deferred alias conflict
	// -- recordDeferredNonExpandingPoolAliasConflictInfo clears the alias and files
	// it in alias_history, so while the singleton waits for its contested name
	// back, alias_history is the ONLY place its identity survives. Any other
	// singleton fixture is undecidable here, because the rotation rule already
	// drops nextAlias from history and produces the same bytes either way.
	if _, err := store.Create(beads.Bead{
		Title:  "manual alias owner",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/manual"},
		Metadata: map[string]string{
			"template":       "pack/manual",
			"session_name":   "manual-pack-worker",
			"agent_name":     "pack/manual",
			"alias":          "pack/worker",
			"state":          "awake",
			"session_origin": "manual",
			"manual_session": "true",
		},
	}); err != nil {
		t.Fatal(err)
	}
	live, err := store.Create(beads.Bead{
		Title:  "pack/worker",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:pack/worker"},
		Metadata: map[string]string{
			"template":                   "pack/worker",
			"session_name":               "pack-worker-live",
			"agent_name":                 "pack/worker",
			"alias":                      "",
			"alias_history":              "pack/worker",
			"state":                      "awake",
			"session_origin":             "ephemeral",
			poolAliasConflictMetadataKey: "pack/worker",
			poolManagedMetadataKey:       boolMetadata(true),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	desired := map[string]TemplateParams{
		"pack-worker-live": {
			TemplateName: "pack/worker",
			InstanceName: "pack/worker",
			Alias:        "pack/worker",
			Command:      "claude",
		},
	}

	var stderr bytes.Buffer
	syncSessionBeads("", store, desired, sp, allConfiguredDS(desired), poolNameAliasHistoryCity(1), clk, &stderr, false)

	got, err := store.Get(live.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata["alias"] != "" {
		t.Fatalf("test setup: alias = %q, want the conflict to still be unresolved", got.Metadata["alias"])
	}
	if history := got.Metadata["alias_history"]; history != "pack/worker" {
		t.Fatalf("alias_history = %q, want %q: a singleton pool's name is its own session's identity, not the pool's", history, "pack/worker")
	}
}
