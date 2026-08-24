package main

import (
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// Scope: the in-process controller demand reader's pool-alias tier -- work
// hand-assigned to a pool's bare name with `bd update <id> --assignee <pool>`
// rather than routed with gc.routed_to.
//
// Why this suite exists as a peer to internal/config/workquery_pool_alias_test.go
// rather than inside it: ci-c000 taught the two GENERATED SHELL predicates about
// that shape (bdReadyPoolAliasDemandShell, unioned into poolDemandCountShell and
// poolDemandFirstRowFunctionScript), but the reconciler does not shell out for a
// pool with no custom scale_check -- it counts in Go, here. That Go reader still
// skipped every bead carrying an assignee, so the two disagreed: the shell form
// counted a bead the in-process form could not see. dispatch.md invariant 11
// forbids exactly that divergence, and ci-mqqe measured what it costs -- 7h23m
// of a P1 sitting ready while zero sessions existed, ending only when a human
// ran `gc sling`.
//
// These tests therefore assert AGREEMENT with the shell predicate, not merely
// that a session appears. A test that only checked "pool wakes" would pass
// against a reader that admitted mail, epics, or held beads, each of which the
// shell form excludes for a recorded reason.
//
//	go test ./cmd/gc/ -run PoolAliasDemand

// poolAliasDemandCity is a pool with no custom scale_check -- the default-probe
// shape, which is what forces the in-process Go reader instead of the shell
// form -- beside an on_demand named session.
//
// The named session is load-bearing, not scenery. readyAssignedWorkAssignees
// seeds its probe list from live session beads plus configured on_demand named
// sessions, and the assigned-work collection falls back to an UNFILTERED Ready()
// only when that list comes back empty. A config without one lets assigned work
// enter the reconciler by accident through that fallback, so the suite would
// report behavior no real city produces.
func poolAliasDemandCity() *config.City {
	return &config.City{
		Agents: []config.Agent{
			{Name: "toolsmith", MaxActiveSessions: intPtr(2), MinActiveSessions: intPtr(0), Provider: "mock"},
			{Name: "mayor", Provider: "mock"},
		},
		NamedSessions: []config.NamedSession{{Template: "mayor", Mode: "on_demand"}},
		Providers:     map[string]config.ProviderSpec{"mock": {Command: "true"}},
	}
}

func poolAliasDemandResult(t *testing.T, cfg *config.City, seed ...beads.Bead) DesiredStateResult {
	t.Helper()
	store := beads.NewMemStore()
	for _, b := range seed {
		if _, err := store.Create(b); err != nil {
			t.Fatalf("Create(%s): %v", b.ID, err)
		}
	}
	return buildDesiredStateWithSessionBeads(
		"test-city", t.TempDir(), time.Now(), cfg, &localMockProvider{},
		store, nil, &sessionBeadSnapshot{}, nil, os.Stderr,
	)
}

// TestPoolAliasDemandWakesColdPool is the end-to-end statement of ci-mqqe: work
// handed to a pool the way work is handed to every other agent must wake it on
// the reconcile tick, with no `gc sling` in between.
//
// Asserts the demand COUNT, not merely that a request exists, because the count
// is what realizePoolDesiredSessions actually spends -- a reader that surfaced
// the bead without counting it would still leave the pool cold.
func TestPoolAliasDemandWakesColdPool(t *testing.T) {
	result := poolAliasDemandResult(t, poolAliasDemandCity(),
		beads.Bead{ID: "ci-work-1", Status: "open", Type: "task", Assignee: "toolsmith"})

	if got := result.ScaleCheckCounts["toolsmith"]; got != 1 {
		t.Errorf("demand = %d, want 1 — a bead assigned to the bare pool name must wake the cold pool", got)
	}
	if len(result.State) != 1 {
		t.Errorf("desired sessions = %d, want 1", len(result.State))
	}
}

// TestPoolAliasDemandSkipsDeferredWork ensures that the pool-alias tier uses
// the same ready-work boundary as gc hook --claim. A deferred bead may retain
// a bare pool assignee while waiting for its defer_until time, but that
// assignee must not create session demand until the bead becomes ready.
func TestPoolAliasDemandSkipsDeferredWork(t *testing.T) {
	deferUntil := time.Now().Add(time.Hour)
	result := poolAliasDemandResult(t, poolAliasDemandCity(), beads.Bead{
		ID:         "ci-deferred-work",
		Status:     "open",
		Type:       "task",
		Assignee:   "toolsmith",
		DeferUntil: &deferUntil,
	})

	if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
		t.Errorf("demand = %d, want 0 — deferred pool-alias work must not spawn a session before gc hook can claim it", got)
	}
	if len(result.State) != 0 {
		t.Errorf("desired sessions = %d, want 0 — deferred pool-alias work must not start an empty session", len(result.State))
	}
}

// TestPoolAliasDemandSkipsBlockedAssignedRoutedWork pins the controller path
// that used to admit an assigned+routed bead only so the orphan reaper could
// inspect it. That bead must not become a wake-known-identity request unless
// it is ready: its pool assignee cannot be claimed while an open dependency
// blocks it.
func TestPoolAliasDemandSkipsBlockedAssignedRoutedWork(t *testing.T) {
	store := beads.NewMemStore()
	blocker, err := store.Create(beads.Bead{ID: "blocker", Status: "open", Type: "task"})
	if err != nil {
		t.Fatalf("Create blocker: %v", err)
	}
	blocked, err := store.Create(beads.Bead{
		ID:       "blocked",
		Status:   "open",
		Type:     "task",
		Assignee: "toolsmith",
		Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "toolsmith"},
	})
	if err != nil {
		t.Fatalf("Create blocked work: %v", err)
	}
	if err := store.DepAdd(blocked.ID, blocker.ID, "blocks"); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	result := buildDesiredStateWithSessionBeads(
		"test-city", t.TempDir(), time.Now(), poolAliasDemandCity(), &localMockProvider{},
		store, nil, &sessionBeadSnapshot{}, nil, os.Stderr,
	)

	if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
		t.Errorf("demand = %d, want 0 — blocked work must not create fresh pool demand", got)
	}
	if len(result.State) != 0 {
		t.Errorf("desired sessions = %d, want 0 — blocked work must not wake an unclaimable pool", len(result.State))
	}
}

// TestPoolAliasDemandLeavesTheAssigneeInPlace pins the half most likely to be
// "fixed" the wrong way. An earlier attempt at ci-mqqe cleared the assignee and
// stamped gc.routed_to instead, which does wake the pool -- and silently
// bypasses the claim path ci-c000 built for exactly this bead: hookCandidate-
// PoolAlias plus BdStore.ReassignIfAssignee, which takes it by atomic
// compare-and-swap so two slots cannot both win it. Rewriting the bead also
// discards the operator's recorded addressee. Demand must be READ from the
// assignee, never by rewriting it.
func TestPoolAliasDemandLeavesTheAssigneeInPlace(t *testing.T) {
	cfg := poolAliasDemandCity()
	store := beads.NewMemStore()
	// Reads the ID back from Create rather than asserting the one passed in:
	// MemStore mints its own, and a hard-coded "ci-work-1" turns this into a
	// bead-not-found failure that looks like the assertion under test.
	created, err := store.Create(beads.Bead{Status: "open", Type: "task", Assignee: "toolsmith"})
	if err != nil {
		t.Fatal(err)
	}

	buildDesiredStateWithSessionBeads(
		"test-city", t.TempDir(), time.Now(), cfg, &localMockProvider{},
		store, nil, &sessionBeadSnapshot{}, nil, os.Stderr,
	)

	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Assignee != "toolsmith" {
		t.Errorf("assignee = %q, want toolsmith — the reconciler must not rewrite work to raise demand", got.Assignee)
	}
	if routed := got.Metadata[beadmeta.RoutedToMetadataKey]; routed != "" {
		t.Errorf("gc.routed_to = %q, want empty — stamping a route here bypasses the pool-alias claim transfer", routed)
	}
}

// TestPoolAliasDemandExclusionsMatchTheShellPredicate covers every shape
// bdReadyPoolAliasDemandShell excludes. Each exclusion creates SPAWN demand if
// dropped, so a miss here is a pool that wakes, finds nothing it may claim,
// drains, and is woken again on the next tick:
//
//   - message: mail carries its recipient in `assignee`, so mail addressed to a
//     pool by name is indistinguishable from pool-assigned work (#4419).
//   - epic: a parent epic has no executable spec, so a claimer does undefined
//     work (gc-udx).
//   - hold-labeled: a bead deliberately parked on a dispatch hold must not
//     raise spawn demand (ga-x9kptu / ga-5736js).
//   - slot identity ("toolsmith-1"): a specific dead session's assignment,
//     recovered by the slot-orphan wake path (ci-rdbw), not pool-door demand.
//   - unknown assignee: names no configured agent at all.
//
// Verified by mutation, and the results are not uniform -- recorded so a reader
// knows which rows pin THIS tier. Dropping the type exclusions fails `epic`,
// dropping the hold check fails `hold`, and normalizing the assignee before the
// template lookup fails `slot identity`. `unknown` is not enforced here at all:
// it falls out of the template-set membership test above this tier, so that row
// pins the outcome rather than any line in pool_alias_demand.go.
func TestPoolAliasDemandExclusionsMatchTheShellPredicate(t *testing.T) {
	for name, bead := range map[string]beads.Bead{
		"message":       {ID: "b", Status: "open", Type: "message", Assignee: "toolsmith"},
		"epic":          {ID: "b", Status: "open", Type: "epic", Assignee: "toolsmith"},
		"slot identity": {ID: "b", Status: "open", Type: "task", Assignee: "toolsmith-1"},
		"unknown":       {ID: "b", Status: "open", Type: "task", Assignee: "nobody"},
		"hold": {
			ID: "b", Status: "open", Type: "task", Assignee: "toolsmith",
			Labels: []string{beadmeta.DispatchHoldLabels[0]},
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := poolAliasDemandResult(t, poolAliasDemandCity(), bead)
			if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
				t.Errorf("demand = %d, want 0 — this shape raises spawn demand no session can consume", got)
			}
		})
	}
}

// TestRoutedPoolDemandSkipsDispatchHoldLabels pins the ci-wv1o6 failure
// mode: the default in-process scale check counted a held, unassigned routed
// bead even though the worker's route-scoped query excludes it. That mismatch
// woke a session which could never claim the bead, drained it, then repeated on
// the next reconcile tick.
//
// Keep this beside the pool-alias exclusions because the two demand routes are
// alternatives in defaultScaleCheckCountsAndDemand and must apply the same
// dispatch-hold predicate.
func TestRoutedPoolDemandSkipsDispatchHoldLabels(t *testing.T) {
	for _, hold := range beadmeta.DispatchHoldLabels {
		t.Run(hold, func(t *testing.T) {
			result := poolAliasDemandResult(t, poolAliasDemandCity(), beads.Bead{
				ID:     "held-routed-work",
				Status: "open",
				Type:   "task",
				Labels: []string{hold},
				Metadata: map[string]string{
					beadmeta.RoutedToMetadataKey: "toolsmith",
				},
			})
			if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
				t.Errorf("demand = %d, want 0 — held routed work must not spawn a session that cannot claim it", got)
			}
		})
	}
}

// TestPoolAliasDemandSkipsSuspendedAgent pins the outcome that a suspended agent
// is never woken by this tier -- which is what the suspension means.
//
// It is an outcome test, not a branch test: no line in pool_alias_demand.go
// enforces it. A suspended agent never enters the default scale-check template
// set, so its beads fail the membership lookup before this tier is consulted.
// The row is here because that upstream guarantee is the whole reason the tier
// can compare assignees against the template set directly, and a change that
// broadened the set would surface here rather than in production.
func TestPoolAliasDemandSkipsSuspendedAgent(t *testing.T) {
	cfg := poolAliasDemandCity()
	cfg.Agents = append(cfg.Agents, config.Agent{Name: "asleep", Provider: "mock", Suspended: true})

	result := poolAliasDemandResult(t, cfg,
		beads.Bead{ID: "b", Status: "open", Type: "task", Assignee: "asleep"})

	if got := result.ScaleCheckCounts["asleep"]; got != 0 {
		t.Errorf("demand = %d, want 0 — a suspended agent must not be woken", got)
	}
}

// TestPoolAliasDemandSkipsAssignedAndRoutedHandoff pins the boundary between
// this tier and the invariant #2527 restored: a bead that is BOTH assigned and
// routed is a concrete handoff (the refinery done-sequence), which must wake its
// named holder without also raising generic pool demand. A route present means
// routing was expressed explicitly and the assignee sits on top of it as
// ownership; a route absent means the assignee IS the routing expression, which
// is the only shape this tier serves.
//
// Both directions are covered because only one of them is caught by the existing
// #2527 regressions: routed to the SAME name is what those tests use, while
// routed ELSEWHERE would slip past a naive same-target comparison and raise
// demand on a pool the work has already left.
func TestPoolAliasDemandSkipsAssignedAndRoutedHandoff(t *testing.T) {
	for name, route := range map[string]string{
		"routed to the same name": "toolsmith",
		"routed elsewhere":        "mayor",
	} {
		t.Run(name, func(t *testing.T) {
			result := poolAliasDemandResult(t, poolAliasDemandCity(), beads.Bead{
				ID: "b", Status: "open", Type: "task", Assignee: "toolsmith",
				Metadata: map[string]string{beadmeta.RoutedToMetadataKey: route},
			})
			if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
				t.Errorf("demand = %d, want 0 — an assigned+routed bead is a concrete handoff, not pool-door work", got)
			}
		})
	}
}

// TestPoolAliasDemandSkipsNamedSessionIdentity guards the mechanism this tier is
// most likely to cannibalize. A named session is woken by namedWorkReady
// matching its bare assignee; counting the same bead as pool demand would wake
// the backing pool for work the holder is already being spawned to do. The shell
// form cannot reach this case -- it only ever runs for a target the reconciler
// already picked as a pool -- so nothing but this guard stops it.
func TestPoolAliasDemandSkipsNamedSessionIdentity(t *testing.T) {
	result := poolAliasDemandResult(t, poolAliasDemandCity(),
		beads.Bead{ID: "b", Status: "open", Type: "task", Assignee: "mayor"})

	if got := result.ScaleCheckCounts["mayor"]; got != 0 {
		t.Errorf("pool demand = %d, want 0 — namedWorkReady already wakes this holder", got)
	}
}

// TestPoolAliasDemandCountsRigScopedQualifiedName guards the target-resolution
// join. The reader must match the assignee against the SAME identity the shell
// form's `--assignee="$target"` uses (Agent.poolDemandTarget: PoolName when set,
// else QualifiedName), which for a rig-scoped agent is Dir-prefixed. Matching a
// bare Name instead would count nothing here while still passing every
// city-scoped row above.
//
// Not hypothetical: this city registered gascity as a rig in place, so its
// worker qualifies as "gascity/lab.engineer".
func TestPoolAliasDemandCountsRigScopedQualifiedName(t *testing.T) {
	cfg := poolAliasDemandCity()
	cfg.Agents = append(cfg.Agents, config.Agent{
		Name: "lab.engineer", Dir: "gascity",
		MaxActiveSessions: intPtr(2), MinActiveSessions: intPtr(0), Provider: "mock",
	})

	result := poolAliasDemandResult(t, cfg,
		beads.Bead{ID: "b", Status: "open", Type: "task", Assignee: "gascity/lab.engineer"})

	if got := result.ScaleCheckCounts["gascity/lab.engineer"]; got != 1 {
		t.Errorf("demand = %d, want 1 — a rig-scoped pool must wake from its qualified name", got)
	}
}
