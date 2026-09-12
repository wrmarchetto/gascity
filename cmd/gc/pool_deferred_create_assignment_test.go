// cmd/gc/pool_deferred_create_assignment_test.go
//
// Scope: what happens to a work bead's SLOT assignment when the pool accepts
// its wake-known-identity request and then does not create the replacement
// session on that same tick.
//
// Why the suite exists: cap acceptance is not what protects the assignment --
// a PERSISTED session bead bearing the slot is. releaseOrphanedPoolAssignments
// runs later in the same beadReconcileTick (city_runtime.go:2402) and asks the
// open-session snapshot whether anything bears the assignee. A create that was
// planned and then deferred leaves that snapshot unchanged, so the sweep
// reaches the same verdict it would for a slot nobody ever intended to revive,
// on a path whose log line says the wake was scheduled. What is lost with the
// assignee is gc.session_affinity, gc.continuation_group, and work_dir
// continuity -- work_dir templates expand {{.AgentBase}}, which carries the
// slot, so the replacement runs in a DIFFERENT checkout and anything the dead
// slot left uncommitted is stranded there.
//
// All four defer causes are driven here rather than argued from a code read,
// and three of them are worse than a reading of the budget case predicts.
// Measured 2026-09-12 against two dead slots holding one in_progress bead
// each, before the guard existed:
//
//	cause                         sessions created   assignments released
//	create budget exhausted       1                  1 (the deferred one)
//	provider health red           0                  2 (both)
//	scale_check partial           0                  2 (both)
//	failed-create backoff         0                  2 (both)
//
// Only the budget is per-request. The other three are template-wide, so they
// shed every accepted wake the pool has, which is the shape a city coming back
// from a restart presents.
//
// Delegated elsewhere, and deliberately not re-derived here:
//
//   - Whether a wake SHOULD outrank new work at a cap is ci-qbhi4g's comparator
//     (cmd/gc/pool_request_tier_precedence_test.go). That bounds one way INTO
//     this loss; this suite bounds the loss.
//   - That a dead-slot address with NO wake request must still be released is
//     TestDeadSlotAssignmentRoutedToItsBasePoolWakesThatPoolOnlyAfterOrphanRelease
//     (build_desired_state_dead_slot_route_demand_test.go). The discriminator
//     below re-asserts only that this guard did not disable it.
//
// Mutation ledger, all executed 2026-09-12, because a green assertion here is
// worth nothing until it has been seen to go red:
//
//	guard removed from the sweep                     6 protected cases -> red
//	guard made unconditional (blanket amnesty)       discriminator     -> red
//	deferred identity keyed on the template, not
//	  the raw assignee                               6 protected cases -> red
//	DeferredWakeIdentities not plumbed into the
//	  sweep from the result                          6 protected cases -> red
//	DeferredWakeIdentities never populated on the
//	  result                                         6 protected cases -> red
//
// The first and second are the pair that matters: the guard must be reachable
// AND narrow, and a suite that only asserted the protected cases would accept
// a sweep that had simply been turned off.
//
// What this suite cannot represent, so a manual check is not mistaken for
// redundant: every store is a MemStore, the tick is assembled by hand rather
// than driven through cityRuntime, and one tick is driven per case -- a slot
// whose create is deferred on EVERY tick (a provider red for an hour) holds
// its assignment for that whole span, which is the intended trade and is not
// exercised as a span here.
//
// Run: go test ./cmd/gc/ -run 'DeferredCreate|DeferredWake'
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/runtime"
)

// deferredWakePool is the one pool every case here uses. It is a file
// constant rather than a parameter because the suite's subject is the defer,
// not the naming: a second pool name would add a dimension nothing asserts.
const deferredWakePool = "worker"

// deferredWakeCity builds a cold pool beside an on_demand named session. The
// named session is load-bearing for the same reason deadSlotCity records: with
// an empty Ready-probe list, collectAssignedWorkBeadsWithStores falls back to
// an unfiltered Ready() and the fixture stops resembling a running city.
//
// max_active_sessions is 3 rather than 2 so the agent cap never binds: a cap
// REJECTION is a different door (ci-qbhi4g) and would make every assertion
// below ambiguous. It must also stay above the highest slot number in use --
// NormalizePoolRouteTarget is bounded by the agent's own cap, so a slot the
// config could not produce reads as a stranger and raises no wake request at
// all, which looks identical to the defect from the outside.
func deferredWakeCity(maxWakesPerTick int, provider, scaleCheck string) *config.City {
	const pool = deferredWakePool
	cfg := &config.City{
		Workspace:     config.Workspace{Name: "gc"},
		NamedSessions: []config.NamedSession{{Template: "overseer", Mode: "on_demand"}},
		Providers:     map[string]config.ProviderSpec{"mock": {Command: "true"}},
	}
	if maxWakesPerTick > 0 {
		limit := maxWakesPerTick
		cfg.Daemon = config.DaemonConfig{MaxWakesPerTick: &limit}
	}
	cfg.Agents = append(cfg.Agents,
		config.Agent{
			Name:              pool,
			StartCommand:      "true",
			Provider:          provider,
			ScaleCheck:        scaleCheck,
			MinActiveSessions: intPtr(0),
			MaxActiveSessions: intPtr(3),
		},
		config.Agent{Name: "overseer", Provider: "mock"},
	)
	return cfg
}

// seedDeadSlotWork creates one in_progress work bead per slot, addressed to
// that slot and routed to its base pool.
//
// The two-step create-then-update is required, not stylistic: MemStore.Create
// forces status "open" whatever the literal says, and an open plain task is
// filtered out of pool demand by filterAssignedWorkBeadsForPoolDemand (only
// in_progress is exempt from needing a Ready() verdict). A one-step fixture
// therefore produces no wake request, no release, and a green test measuring
// nothing.
func seedDeadSlotWork(t *testing.T, store beads.Store, pool string, slots ...string) []string {
	t.Helper()
	ids := make([]string, 0, len(slots))
	for _, slot := range slots {
		created, err := store.Create(beads.Bead{
			Title:    "claimed by " + slot + " before it died",
			Type:     "task",
			Status:   "open",
			Assignee: slot,
			Metadata: map[string]string{"gc.routed_to": pool},
		})
		if err != nil {
			t.Fatalf("create work for %s: %v", slot, err)
		}
		inProgress := "in_progress"
		if err := store.Update(created.ID, beads.UpdateOpts{Status: &inProgress}); err != nil {
			t.Fatalf("mark %s in_progress: %v", created.ID, err)
		}
		ids = append(ids, created.ID)
	}
	return ids
}

// writeProviderRed stamps the provider-health cache the create gate reads.
func writeProviderRed(t *testing.T, cityPath, provider string) {
	t.Helper()
	cachePath := filepath.Join(cityPath, ".gc", "cache", "provider-health.json")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatalf("mkdir provider health cache: %v", err)
	}
	payload, err := json.Marshal(map[string]any{"providers": []map[string]any{{
		"provider": provider,
		"status":   "red",
		// Fresh: loadProviderHealthSnapshot drops records older than
		// providerHealthTTL, and a dropped record fails OPEN.
		"probed_at": float64(time.Now().UnixNano()) / 1e9,
	}}})
	if err != nil {
		t.Fatalf("marshal provider health: %v", err)
	}
	if err := os.WriteFile(cachePath, payload, 0o644); err != nil {
		t.Fatalf("write provider health cache: %v", err)
	}
}

// seedCreateFailureBackoff writes the closed session rows that
// poolCreateFailureBackoffActive scans. The metadata keys are the ones session.Info decodes, NOT their
// beadmeta spellings: the template arrives under "template", not
// "gc.template". Getting that wrong yields a fixture that looks correct in a
// store dump and never activates the backoff -- measured, and it is why the
// created rows are asserted against below rather than trusted.
func seedCreateFailureBackoff(t *testing.T, store beads.Store, pool string, slots, triggers []string) {
	t.Helper()
	retryAfter := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	for i, slot := range slots {
		row, err := store.Create(beads.Bead{
			Title:  "failed create for " + slot,
			Type:   "session",
			Status: "open",
			Metadata: map[string]string{
				"template":                pool,
				"agent_name":              slot,
				"session_origin":          "ephemeral",
				"gc.create_failure_class": "claim_no_work",
				"gc.create_retry_after":   retryAfter,
				"gc.trigger_bead_id":      triggers[i],
			},
		})
		if err != nil {
			t.Fatalf("create backoff row for %s: %v", slot, err)
		}
		closed := "closed"
		if err := store.Update(row.ID, beads.UpdateOpts{Status: &closed}); err != nil {
			t.Fatalf("close backoff row %s: %v", row.ID, err)
		}
	}
}

// deferredWakeTick runs one build and then the orphan sweep, in the order and
// through the wrapper beadReconcileTick uses. The open-session snapshot is
// RELOADED from the store between the two rather than carried over, because
// that reload is what makes a session bead created during the build visible to
// the sweep -- and a test that skipped it would report the defect everywhere,
// including after the fix.
func deferredWakeTick(t *testing.T, cfg *config.City, cityPath string, store beads.Store) (desired int, released []string, stderr string) {
	t.Helper()
	var errbuf strings.Builder
	result := buildDesiredStateWithSessionBeads(
		"gc", cityPath, time.Now().UTC(), cfg, runtime.NewFake(),
		store, nil, &sessionBeadSnapshot{}, nil, &errbuf,
	)
	snapshot, err := loadSessionBeadSnapshot(store)
	if err != nil {
		t.Fatalf("reload session snapshot: %v", err)
	}
	for _, entry := range releaseOrphanedPoolAssignmentsWhenSnapshotsComplete(
		store, cfg, cityPath, snapshot.OpenInfos(), result, nil,
	) {
		released = append(released, entry.ID)
	}
	return len(result.State), released, errbuf.String()
}

// TestDeferredCreateKeepsTheWakeAssignmentItAccepted is the discriminating
// test. Every case accepts two wake-known-identity requests and then fails to
// persist at least one replacement session; none of them may cost a work bead
// its assignee.
//
// wantSessions is asserted, not merely tolerated, because it is the whole
// precondition: a case that quietly started creating every session would
// satisfy the release assertion while measuring nothing at all.
func TestDeferredCreateKeepsTheWakeAssignmentItAccepted(t *testing.T) {
	const pool = deferredWakePool
	slots := []string{pool + "-1", pool + "-2"}

	cases := []struct {
		name         string
		wantSessions int
		wantStderr   string
		setup        func(t *testing.T, cityPath string, store beads.Store, ids []string) *config.City
	}{
		{
			name:         "create budget exhausted",
			wantSessions: 1,
			wantStderr:   "budget exhausted",
			setup: func(_ *testing.T, _ string, _ beads.Store, _ []string) *config.City {
				return deferredWakeCity(1, "", "")
			},
		},
		{
			name:         "provider health red",
			wantSessions: 0,
			wantStderr:   "provider red",
			setup: func(t *testing.T, cityPath string, _ beads.Store, _ []string) *config.City {
				writeProviderRed(t, cityPath, "mock")
				return deferredWakeCity(0, "mock", "")
			},
		},
		{
			name:         "scale_check partial",
			wantSessions: 0,
			wantStderr:   "demand read partial",
			setup: func(_ *testing.T, _ string, _ beads.Store, _ []string) *config.City {
				return deferredWakeCity(0, "", "exit 7")
			},
		},
		{
			name:         "failed-create backoff",
			wantSessions: 0,
			wantStderr:   "failed-create backoff active",
			setup: func(t *testing.T, _ string, store beads.Store, ids []string) *config.City {
				seedCreateFailureBackoff(t, store, pool, slots, ids)
				return deferredWakeCity(0, "", "")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityPath := t.TempDir()
			store := beads.NewMemStore()
			ids := seedDeadSlotWork(t, store, pool, slots...)
			cfg := tc.setup(t, cityPath, store, ids)

			desired, released, stderr := deferredWakeTick(t, cfg, cityPath, store)

			if !strings.Contains(stderr, tc.wantStderr) {
				t.Fatalf("no %q in build output, so this case did not exercise the defer it names:\n%s", tc.wantStderr, stderr)
			}
			if desired != tc.wantSessions {
				t.Fatalf("desired sessions = %d, want %d; both wakes were accepted, so a different number means this case no longer defers the create it was written for", desired, tc.wantSessions)
			}
			if len(released) != 0 {
				t.Fatalf("released %v; a wake the pool ACCEPTED and then deferred must keep its slot assignment, or the deferral costs the same affinity, continuation group and work dir a cap rejection would", released)
			}
			for i, id := range ids {
				got, err := store.Get(id)
				if err != nil {
					t.Fatalf("get %s: %v", id, err)
				}
				if got.Assignee != slots[i] {
					t.Fatalf("work %s assignee = %q, want %q retained", id, got.Assignee, slots[i])
				}
				if got.Status != "in_progress" {
					t.Fatalf("work %s status = %q, want in_progress retained", id, got.Status)
				}
			}
		})
	}
}

// TestDeferredWakeGuardHoldsUnderAnAgentCapAndAWorkspaceCap runs the budget
// case twice with a non-binding cap expressed at each level.
//
// No difference is expected -- acceptance shares one nestedCapUsage -- and
// that is exactly why it is run: the expectation was recorded on ci-ms1qhp as
// unmeasured, and an unmeasured expectation about which cap applies is how a
// guard ends up scoped to one of them.
func TestDeferredWakeGuardHoldsUnderAnAgentCapAndAWorkspaceCap(t *testing.T) {
	const pool = deferredWakePool
	slots := []string{pool + "-1", pool + "-2"}

	for _, level := range []string{"agent", "workspace"} {
		t.Run(level, func(t *testing.T) {
			cityPath := t.TempDir()
			store := beads.NewMemStore()
			seedDeadSlotWork(t, store, pool, slots...)
			cfg := deferredWakeCity(1, "", "")
			if level == "workspace" {
				cfg.Workspace.MaxActiveSessions = intPtr(8)
			}

			desired, released, stderr := deferredWakeTick(t, cfg, cityPath, store)
			if !strings.Contains(stderr, "budget exhausted") {
				t.Fatalf("no deferred create under a %s cap:\n%s", level, stderr)
			}
			if desired != 1 {
				t.Fatalf("desired sessions under a %s cap = %d, want 1", level, desired)
			}
			if len(released) != 0 {
				t.Fatalf("released %v under a %s cap; the guard must not depend on which level the non-binding cap is written at", released, level)
			}
		})
	}
}

// TestDeferredWakeGuardStillReleasesAnAddressNoWakeAsked pins the guard's
// narrowness in the only direction that matters: it protects the identities
// whose creates were planned and deferred THIS tick, and nothing else.
//
// An open plain task addressed to a dead slot raises no wake request at all
// (filterAssignedWorkBeadsForPoolDemand admits it only with a Ready() verdict,
// which appendOpenRoutedWorkUnique does not stamp), and releasing it is the
// only door that shape has -- owned by
// build_desired_state_dead_slot_route_demand_test.go. If the guard ever
// widened into "skip anything a pool might want", that suite would go red one
// file away from here; this case makes the boundary fail in the file that
// introduced it.
func TestDeferredWakeGuardStillReleasesAnAddressNoWakeAsked(t *testing.T) {
	const pool = deferredWakePool
	cityPath := t.TempDir()
	store := beads.NewMemStore()

	work, err := store.Create(beads.Bead{
		Title:    "addressed to a dead slot, never claimed",
		Type:     "task",
		Status:   "open",
		Assignee: pool + "-2",
		Metadata: map[string]string{"gc.routed_to": pool},
	})
	if err != nil {
		t.Fatalf("create work: %v", err)
	}

	// max_wakes_per_tick=1 is carried over from the protected cases on
	// purpose: the budget is set up to be exhaustible, and the bead is still
	// released, so the release cannot be attributed to the absence of a
	// deferral.
	_, released, _ := deferredWakeTick(t, deferredWakeCity(1, "", ""), cityPath, store)
	if len(released) != 1 || released[0] != work.ID {
		t.Fatalf("released %v, want exactly [%s]; an address that asked for no wake has only the orphan sweep to recover it", released, work.ID)
	}
}
