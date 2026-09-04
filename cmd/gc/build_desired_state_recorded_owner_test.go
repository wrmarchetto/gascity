// Scope: the paired invariant that lets a bead record its intended owner
// WITHOUT that record being an instruction to start work -- a dispatch hold
// label suppresses every spawn path while the assignee or route stays on the
// bead, and an otherwise identical bead with no hold label still sizes its
// pool.
//
// Why this suite exists as a peer to build_desired_state_pool_alias_demand_test.go
// rather than as more rows inside it: that suite asks "does the in-process
// reader agree with the shell predicate", one exclusion at a time, and its hold
// rows assert the demand COUNT alone. Demand is only half of a spawn. A pool
// session is realized from ScaleCheckCounts AND from the desired-state map the
// wake/resume tiers write into, and a hold row that reads only the counter
// cannot observe the second. Its deferred and blocked siblings assert both; the
// hold rows assert the count alone. The city-facing claim is about the SESSION,
// not the counter, so this suite asserts both numbers and asserts them against a
// matched unheld control on the same fixture.
//
// The control is the load-bearing half. ci-t98zgv was filed because a mayor
// concluded there was no way to record ownership without starting work, and the
// remedy that thought suggests -- narrowing what an assignee is allowed to mean
// -- would idle the whole city while passing any test that only checked that a
// held bead stays cold. Every case here therefore runs its own negative control
// through the same code path, so a change that kills all demand fails here
// rather than in production.
//
// Run it with:
//
//	go test ./cmd/gc/ -run RecordedOwner
//
// Delegated elsewhere, deliberately not re-tested here: which shapes each
// generated shell predicate excludes, and the Go/shell agreement that motivates
// them (build_desired_state_pool_alias_demand_test.go and
// internal/config/workquery_hold_label_test.go); the label vocabulary itself
// (internal/beadmeta/hold_labels_test.go); and the OTHER lever for the same
// need, bd's status-based indefinite deferral -- the "park it at DEFERRED"
// convention -- which internal/beads owns and which never reaches this reader
// at all, because Ready() drops a StatusDeferred issue carrying no defer_until
// (native_dolt_store.go).
package main

import (
	"os"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// recordedOwnerShapes are the two ways a bead records who should eventually own
// it. Both must behave identically under a hold, because an author choosing
// between them is choosing how the work will be CLAIMED (atomic transfer off
// the pool alias, versus an unassigned routed queue), not whether the record
// starts anything.
var recordedOwnerShapes = map[string]func(b *beads.Bead){
	"assignee": func(b *beads.Bead) { b.Assignee = "toolsmith" },
	"routed_to": func(b *beads.Bead) {
		b.Metadata = map[string]string{beadmeta.RoutedToMetadataKey: "toolsmith"}
	},
}

// TestRecordedOwnerWithHoldRaisesNoSpawn is the statement ci-t98zgv asked for,
// in both directions on one fixture: a bead carrying a dispatch hold label
// records its owner and produces neither demand nor a desired session, while
// the same bead without the label produces both.
//
// Mutation-verified rather than read, both refusals flipped to their accepting
// counterpart and each run recorded: `hasDispatchHoldLabel` forced to false
// killed all four held subtests on BOTH assertions and left the controls green;
// forced to true -- the shape of a change that suppresses demand wholesale --
// killed all four controls and left the held assertions green. Neither mutation
// is caught by a row that reads only one side, which is why the controls are
// here and not decoration.
func TestRecordedOwnerWithHoldRaisesNoSpawn(t *testing.T) {
	for shape, record := range recordedOwnerShapes {
		for _, hold := range beadmeta.DispatchHoldLabels {
			t.Run(shape+"/"+hold, func(t *testing.T) {
				held := beads.Bead{ID: "b", Status: "open", Type: "task", Labels: []string{hold}}
				record(&held)
				result := poolAliasDemandResult(t, poolAliasDemandCity(), held)
				if got := result.ScaleCheckCounts["toolsmith"]; got != 0 {
					t.Errorf("held demand = %d, want 0 — recording an owner must not be an instruction to start", got)
				}
				if got := len(result.State); got != 0 {
					t.Errorf("held desired sessions = %d, want 0 — a session spawned here finds nothing it may claim, drains, and is spawned again next tick", got)
				}

				// The control. Same shape, same fixture, label removed.
				open := beads.Bead{ID: "b", Status: "open", Type: "task"}
				record(&open)
				control := poolAliasDemandResult(t, poolAliasDemandCity(), open)
				if got := control.ScaleCheckCounts["toolsmith"]; got != 1 {
					t.Errorf("unheld demand = %d, want 1 — ordinary %s work must still size its pool", got, shape)
				}
				if got := len(control.State); got != 1 {
					t.Errorf("unheld desired sessions = %d, want 1 — suppressing this shape idles the city", got)
				}
			})
		}
	}
}

// TestRecordedOwnerSurvivesTheHoldTick pins the ATTRIBUTABLE half of the same
// contract. Suppressing demand by rewriting the bead -- clearing the assignee,
// dropping the route -- would satisfy the test above and destroy the only
// record of who the work was meant for, which is the whole reason the bead is
// allowed to exist before its verdict lands.
//
// It reads the store back after a full reconcile tick rather than asserting on
// the reader's return value, because the reader is not the only writer in that
// path.
func TestRecordedOwnerSurvivesTheHoldTick(t *testing.T) {
	for shape, record := range recordedOwnerShapes {
		t.Run(shape, func(t *testing.T) {
			held := beads.Bead{
				ID: "b", Status: "open", Type: "task",
				Labels: []string{beadmeta.DispatchHoldLabels[0]},
			}
			record(&held)

			store := beads.NewMemStore()
			// MemStore mints its own ID, so the readback uses what Create
			// returned; a hard-coded "b" turns this into a bead-not-found
			// failure that reads like the assertion under test.
			created, err := store.Create(held)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			buildDesiredStateWithSessionBeads(
				"test-city", t.TempDir(), time.Now(), poolAliasDemandCity(), &localMockProvider{},
				store, nil, &sessionBeadSnapshot{}, nil, os.Stderr,
			)

			got, err := store.Get(created.ID)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Assignee != held.Assignee {
				t.Errorf("assignee = %q, want %q — the recorded owner must outlive the hold", got.Assignee, held.Assignee)
			}
			if want := held.Metadata[beadmeta.RoutedToMetadataKey]; got.Metadata[beadmeta.RoutedToMetadataKey] != want {
				t.Errorf("gc.routed_to = %q, want %q — the recorded owner must outlive the hold",
					got.Metadata[beadmeta.RoutedToMetadataKey], want)
			}
			if !beadLabelsContain(got.Labels, beadmeta.DispatchHoldLabels[0]) {
				t.Errorf("labels = %v, want the hold retained — clearing it here would release the bead on the next tick", got.Labels)
			}
		})
	}
}

// TestRecordedOwnerHoldIsTransparentToASingletonPoolsOwnIdentity pins the LIMIT
// the section "What a hold label does to dispatch" in
// engdocs/contributors/hold-label-conventions.md tells readers to plan around,
// so the paragraph has a gate behind it instead of a derivation a reader has to
// redo. The claim it pins is a composite of two separately-pinned facts, and a
// composite is exactly what neither of them can catch moving:
//
//   - a singleton pool launches under its BARE qualified name, with no slot
//     suffix, and that name reaches the session as GC_ALIAS
//     (poolDesiredRequestIdentity -> setTemplateEnvIdentity).
//   - the assignee-scoped ready tier, which probes GC_ALIAS, is deliberately
//     hold-transparent (internal/config,
//     TestEphemeralAssignedReadyProbeScriptDoesNotExcludeDispatchHoldLabels).
//
// Together: a live singleton-pool session can CLAIM a held bead recorded
// against its pool name, even though no session is ever SPAWNED for one. That
// is the mechanism working -- a hold names the actor who must move next -- but
// it is the half a reader plans around, and raising max_active_sessions above 1
// silently changes it. The multi-slot row is the control: there the alias is
// suffixed, so the bare pool name is reachable only through the route-scoped
// tier, which does exclude holds.
//
// This asserts the IDENTITY, not the claim outcome. Driving a real claim would
// need a live session and a bd binary; the identity is the whole reason the
// transparent tier reaches the bead, and it is the half that moves with config.
func TestRecordedOwnerHoldIsTransparentToASingletonPoolsOwnIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		maxSessions      int
		wantBarePoolName bool
	}{
		"singleton pool":  {maxSessions: 1, wantBarePoolName: true},
		"multi-slot pool": {maxSessions: 2, wantBarePoolName: false},
	} {
		t.Run(name, func(t *testing.T) {
			agent := &config.Agent{
				Name: "toolsmith", Provider: "mock",
				MaxActiveSessions: intPtr(tc.maxSessions), MinActiveSessions: intPtr(0),
			}
			_, identity, _ := poolDesiredRequestIdentity(agent, 1)
			if got := identity == agent.QualifiedName(); got != tc.wantBarePoolName {
				t.Fatalf("identity = %q, bare pool name = %v, want %v — GC_ALIAS decides whether the hold-transparent assigned tier can reach work recorded against the pool name",
					identity, got, tc.wantBarePoolName)
			}
		})
	}
}
