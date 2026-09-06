// Scope: an exhausted ralph control closes with a close_reason that names its
// iteration beads and their refusal conditions, and that reason reaches the
// store method which forwards it to `bd close --reason`.
//
// This suite exists because the exhaust path used to close through
// `bd update --status closed`, which has no --reason: the bead landed with
// gc.outcome=fail and a NULL close_reason, a shape indistinguishable from the
// genuine defect it mimics -- an agent that closed a bead without recording
// why. On 2026-09-05 a mayor handoff read ci-9oeb5x that way and made it the
// session's first instruction; it was not a defect, the reasons were on the
// iteration beads and simply unreachable from the bead a reader lands on
// (ci-ae1ob2).
//
// Two invariants, and the first alone is not enough. Asserting only that
// close_reason appears in the control's metadata passes on the broken code
// too, because the exhaust path already wrote whatever it was handed into the
// metadata batch -- the value just never reached `bd close`. The routing test
// is what pins the half that was missing, by proving the close went through
// beads.Store.Close (which reads metadata["close_reason"] and forwards it,
// BdStore.Close) rather than through the reasonless Update(status=closed).
//
// What this suite cannot represent: bd itself. MemStore records no close
// reason of its own, so "the reason was readable by the forwarding code at
// close time" is the strongest observable here. BdStore's forwarding is pinned
// separately by TestBdStoreCloseForwardsMetadataReason in
// internal/beads/bdstore_test.go.
//
// Run: go test ./internal/dispatch/ -run RalphExhausted

package dispatch

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/convergence"
)

// ralphExhaustionFixture builds a root + ralph control + `iterations` closed
// iteration beads, all failed, wired so processRalphControl reaches the
// exhaust branch on the next tick. It returns the control and the iteration
// bead ids in attempt order.
//
// Every iteration carries gc.control_for and gc.attempt because that is what
// production mints (buildAttemptRecipe, S38) and what the id enumeration
// matches on. An iteration missing the stamp is a real pre-S38 shape and is
// covered by its own test below, not by weakening this fixture.
//
// gc.failure_class is deliberately absent: with it set to hard the loop takes
// the terminal hard-fail branch instead and never exhausts, so the test would
// pin the wrong closure. Its absence is also the measured shape of ci-9oeb5x.
func ralphExhaustionFixture(t *testing.T, store beads.Store, iterations int, stampControlFor bool) (beads.Bead, []string) {
	t.Helper()
	root := mustCreate(t, store, beads.Bead{
		Title:    "workflow",
		Metadata: map[string]string{"gc.kind": "workflow"},
	})
	control := mustCreate(t, store, beads.Bead{
		Title: "bench execute",
		Metadata: map[string]string{
			"gc.kind":          "ralph",
			"gc.root_bead_id":  root.ID,
			"gc.step_ref":      "mol-test.bench-execute",
			"gc.step_id":       "bench-execute",
			"gc.check_mode":    "exec",
			"gc.check_path":    "check.sh",
			"gc.max_attempts":  strconv.Itoa(iterations),
			"gc.control_epoch": "1",
		},
	})
	ids := make([]string, 0, iterations)
	for n := 1; n <= iterations; n++ {
		meta := map[string]string{
			"gc.kind":         "scope",
			"gc.root_bead_id": root.ID,
			"gc.step_ref":     "mol-test.bench-execute.iteration." + strconv.Itoa(n),
			"gc.scope_role":   "body",
			"gc.attempt":      strconv.Itoa(n),
			// runRalphCheck short-circuits a failed subject into a synthesized
			// GateFail whose stderr becomes the attempt log's reason, so the
			// check script is never executed and no city path is needed.
			"gc.outcome": beadmeta.OutcomeFail,
		}
		if stampControlFor {
			meta["gc.control_for"] = control.ID
		}
		iteration := mustCreate(t, store, beads.Bead{
			Title:    "bench execute iteration " + strconv.Itoa(n),
			Metadata: meta,
		})
		mustClose(t, store, iteration.ID)
		mustDep(t, store, control.ID, iteration.ID, "blocks")
		ids = append(ids, iteration.ID)
	}
	// Seed the log the earlier ticks would have written. Without it only the
	// final iteration carries a condition, and an assertion that every
	// iteration is paired with its refusal would be pinning the fixture's
	// emptiness rather than the join.
	if len(ids) > 1 {
		seeded := make([]map[string]string, 0, len(ids)-1)
		for n, id := range ids[:len(ids)-1] {
			seeded = append(seeded, map[string]string{
				"attempt": strconv.Itoa(n + 1),
				"outcome": convergence.GateFail,
				"action":  convergence.GateFail,
				"reason":  ralphAlreadyFailedCondition(id),
			})
		}
		encoded, err := json.Marshal(seeded)
		if err != nil {
			t.Fatalf("marshal seeded attempt log: %v", err)
		}
		if err := store.SetMetadata(control.ID, beadmeta.AttemptLogMetadataKey, string(encoded)); err != nil {
			t.Fatalf("seed %s: %v", beadmeta.AttemptLogMetadataKey, err)
		}
	}
	return mustGet(t, store, control.ID), ids
}

// ralphAlreadyFailedCondition re-derives the gate stderr runRalphCheck
// synthesizes for a subject that already failed. Derived from the same shape
// the production path emits rather than copied as a literal, so a change to
// that message shows up here as a failure instead of a stale expectation.
func ralphAlreadyFailedCondition(iterationID string) string {
	return "attempt subject " + iterationID + " already failed"
}

// closePathSpyStore records which of the two close routes a bead took, and
// what metadata["close_reason"] held at the moment Close was called.
//
// The captured-at-close-time reason is the point: BdStore.Close re-reads the
// bead and forwards that value, so a reason written after the close, or
// written into a batch that closes the bead in the same call, is invisible to
// bd however it looks in the final metadata.
type closePathSpyStore struct {
	beads.Store
	targetID          string
	closedViaUpdate   bool
	closeCalls        int
	reasonSeenByClose string
}

func (s *closePathSpyStore) Update(id string, opts beads.UpdateOpts) error {
	if id == s.targetID && opts.Status != nil && *opts.Status == "closed" {
		s.closedViaUpdate = true
	}
	return s.Store.Update(id, opts)
}

func (s *closePathSpyStore) Close(id string) error {
	if id == s.targetID {
		s.closeCalls++
		if b, err := s.Get(id); err == nil {
			s.reasonSeenByClose = strings.TrimSpace(b.Metadata["close_reason"])
		}
	}
	return s.Store.Close(id)
}

// TestRalphExhaustedCloseReasonNamesIterationBeads pins the content contract:
// the bead a reader lands on says it exhausted and says where the per-iteration
// refusal reasons are, by id.
func TestRalphExhaustedCloseReasonNamesIterationBeads(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()
	control, iterationIDs := ralphExhaustionFixture(t, store, 2, true)

	result, err := processRalphControl(store, control, ProcessOptions{})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "fail" {
		t.Fatalf("result = %+v, want processed fail", result)
	}

	after := mustGet(t, store, control.ID)
	if after.Status != "closed" {
		t.Fatalf("control status = %q, want closed", after.Status)
	}
	reason := after.Metadata["close_reason"]
	if reason == "" {
		t.Fatal("close_reason is empty -- an exhausted control that records no reason is the ci-ae1ob2 defect")
	}
	// bd's validation.on-close=error rejects a reason under 20 characters, so
	// a synthesized reason that cannot satisfy it is worse than none: the
	// close itself fails on a city running that validator.
	if len(reason) < 20 {
		t.Errorf("close_reason = %q (%d chars), want at least 20 for bd validation.on-close=error", reason, len(reason))
	}
	for _, id := range iterationIDs {
		if !strings.Contains(reason, id) {
			t.Errorf("close_reason = %q, want it to name iteration bead %s", reason, id)
		}
	}
	// The condition PAIRED with its id, not merely present somewhere in the
	// string. A reason that lists every id and then every condition separately
	// is unusable for the triage this exists to serve.
	for n, id := range iterationIDs {
		want := "iteration " + strconv.Itoa(n+1) + " " + id + ": " + ralphAlreadyFailedCondition(id)
		if !strings.Contains(reason, want) {
			t.Errorf("close_reason = %q, want it to contain %q", reason, want)
		}
	}
	if !strings.Contains(reason, "exhausted") {
		t.Errorf("close_reason = %q, want it to say the budget was exhausted", reason)
	}
}

// TestRalphExhaustedCloseReasonReachesTheForwardingClose pins the routing half:
// the reason is readable from the bead's metadata at the moment Close is
// called, and the close does NOT take the Update(status=closed) route, which
// has no --reason argument and is what left close_reason NULL.
func TestRalphExhaustedCloseReasonReachesTheForwardingClose(t *testing.T) {
	t.Parallel()
	base := beads.NewMemStore()
	control, iterationIDs := ralphExhaustionFixture(t, base, 2, true)
	store := &closePathSpyStore{Store: base, targetID: control.ID}

	result, err := processRalphControl(store, mustGet(t, store, control.ID), ProcessOptions{})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "fail" {
		t.Fatalf("result = %+v, want processed fail", result)
	}
	if store.closedViaUpdate {
		t.Error("control closed via Update(status=closed), which carries no --reason -- close_reason lands NULL")
	}
	if store.closeCalls != 1 {
		t.Fatalf("Close calls = %d, want exactly 1", store.closeCalls)
	}
	for _, id := range iterationIDs {
		if !strings.Contains(store.reasonSeenByClose, id) {
			t.Errorf("close_reason readable at Close time = %q, want it to name iteration bead %s", store.reasonSeenByClose, id)
		}
	}
}

// TestRalphExhaustedCloseReasonWithoutLineageStampStillRecordsAReason pins the
// documented degradation. Iteration roots minted before the gc.control_for
// stamp existed (S38) cannot be enumerated by id, and the synthesized reason
// then names the budget and points at gc.attempt_log instead of inventing an
// id. A null close_reason is the one outcome this path must never produce.
func TestRalphExhaustedCloseReasonWithoutLineageStampStillRecordsAReason(t *testing.T) {
	t.Parallel()
	store := beads.NewMemStore()
	control, _ := ralphExhaustionFixture(t, store, 2, false)

	result, err := processRalphControl(store, control, ProcessOptions{})
	if err != nil {
		t.Fatalf("processRalphControl: %v", err)
	}
	if !result.Processed || result.Action != "fail" {
		t.Fatalf("result = %+v, want processed fail", result)
	}

	reason := mustGet(t, store, control.ID).Metadata["close_reason"]
	if reason == "" {
		t.Fatal("close_reason is empty -- an unstamped lineage must still record why the control closed")
	}
	if !strings.Contains(reason, beadmeta.AttemptLogMetadataKey) {
		t.Errorf("close_reason = %q, want it to point at %s when no iteration id could be resolved",
			reason, beadmeta.AttemptLogMetadataKey)
	}
}
