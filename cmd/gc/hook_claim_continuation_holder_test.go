// Package main test: whether a claim records THIS session on the continuation
// siblings it preassigns to itself.
//
// Scope: preassignHookContinuationGroup driven through doHookClaim with faked
// hookClaimOps, asserting the metadata patch that reaches StampWorkMeta. It
// asserts nothing about what reads that stamp; the gates that do are pinned in
// drain_ack_affinity_pin_test.go (the drain-ack close gate) and
// cmd_hook_stop_test.go (the Stop gate).
//
// WHY THIS SUITE EXISTS. A preassigned sibling used to carry an assignee and an
// affinity pin and nothing else, which on a canonical singleton pool is
// byte-for-byte what graphroute writes on a pool step NOBODY has claimed -- the
// assignee is the slot alias in both cases. Both gates have to tell those
// apart, and could not: ci-7tn14g refused 16 drain acknowledgements from six
// occupants of the bench-engineer slot over six days (ci-d1huhf). The stamp is
// what makes the distinction exist in the data rather than being guessed from
// it.
//
// WHAT IT DELIBERATELY DOES NOT PIN. That the stamp is compare-and-skipped
// against the sibling's current metadata is asserted only through the
// already-stamped case below; the claimed bead's own identical patch is
// stampHookClaimIdentity's, tested in cmd_hook_test.go, and this file does not
// re-test it.
//
// Run it with:
//
//	go test ./cmd/gc/ -run TestHookClaimStampsTheHolder -count=1
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// continuationHolderStampPatch runs one claim whose bead carries a continuation
// group, with a single sibling holding siblingMeta, and returns the patch the
// preassign stamped on that sibling. A nil result means no write was issued.
func continuationHolderStampPatch(t *testing.T, env []string, siblingMeta map[string]string) map[string]string {
	t.Helper()
	claimed := map[string]string{
		beadmeta.KindMetadataKey:              "workflow",
		beadmeta.RunTargetMetadataKey:         "route-1",
		beadmeta.RootBeadIDMetadataKey:        "root-1",
		beadmeta.ContinuationGroupMetadataKey: "group-a",
	}
	candidates := []beads.Bead{{ID: "bead-1", Status: "open", Metadata: claimed}}
	output, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}

	var stamped map[string]string
	var stampedBead string
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(output), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress", Metadata: claimed}, true, nil
		},
		ListContinuation: func(_ context.Context, _ string, _ []string, _, _ string) ([]beads.Bead, error) {
			// gc.routed_to is not decoration: preassignHookContinuationGroup
			// skips any sibling that fails hookClaimMatchesRoute, and a fixture
			// without it walks straight past the code under test and reports
			// "nothing was stamped" for the wrong reason. Seen: the first
			// version of this fixture carried gc.run_target alone, which only
			// matches for gc.kind=workflow, and every case here went green or
			// red for that reason instead of the stamp.
			sibling := map[string]string{beadmeta.RoutedToMetadataKey: "route-1"}
			for k, v := range siblingMeta {
				sibling[k] = v
			}
			return []beads.Bead{{ID: "sib-1", Status: "open", Metadata: sibling}}, nil
		},
		AssignContinuation: func(context.Context, string, []string, string, string) error { return nil },
		StampWorkMeta: func(_ context.Context, _ string, _ []string, beadID, _ string, patch map[string]string) error {
			// The claimed bead gets its own patch from stampHookClaimIdentity
			// on this same path, so filtering by id is what keeps this
			// assertion about the SIBLING rather than about whichever write
			// happened to land last.
			if beadID != "sib-1" {
				return nil
			}
			if stampedBead != "" {
				t.Fatalf("the sibling was stamped twice; the second patch was %#v", patch)
			}
			stampedBead = beadID
			stamped = patch
			return nil
		},
		DrainAck: func(string, io.Writer) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	if code := doHookClaim("query", "rig-store", hookClaimOptions{
		Assignee:           "worker",
		IdentityCandidates: []string{"worker"},
		RouteTargets:       []string{"route-1"},
		Env:                env,
		JSON:               true,
	}, ops, &stdout, &stderr); code != 0 {
		t.Fatalf("doHookClaim() = %d, want 0; stderr=%s", code, stderr.String())
	}
	return stamped
}

// TestHookClaimStampsTheHolderOnPreassignedSiblings pins that a sibling handed
// to a session carries that session's own identity, not just the slot's.
//
// The assignee is deliberately "worker" -- a bare pool alias with no `-N`
// suffix, the canonical singleton shape where the alias IS the queue address --
// because that is the only case where the stamp carries information the
// assignee does not.
func TestHookClaimStampsTheHolderOnPreassignedSiblings(t *testing.T) {
	patch := continuationHolderStampPatch(t, []string{"GC_SESSION_ID=ci-me", "GC_SESSION_NAME=city-worker"}, nil)
	if patch == nil {
		t.Fatal("no metadata was stamped on the preassigned sibling: it is indistinguishable from a pool step nobody claimed, and both drain gates will release it")
	}
	if got := patch[beadmeta.SessionIDMetadataKey]; got != "ci-me" {
		t.Fatalf("%s = %q, want ci-me", beadmeta.SessionIDMetadataKey, got)
	}
	if got := patch[beadmeta.SessionNameMetadataKey]; got != "city-worker" {
		t.Fatalf("%s = %q, want city-worker", beadmeta.SessionNameMetadataKey, got)
	}
}

// TestHookClaimStampsTheHolderOnlyWhenItChanges pins the compare-and-skip.
// preassignHookContinuationGroup runs again on every adoption tick, and an
// unconditional write would emit a bead.updated per sibling per tick -- the
// cache-reconcile flood stampHookClaimIdentity's patch is compare-and-skipped
// to avoid.
func TestHookClaimStampsTheHolderOnlyWhenItChanges(t *testing.T) {
	patch := continuationHolderStampPatch(t,
		[]string{"GC_SESSION_ID=ci-me", "GC_SESSION_NAME=city-worker"},
		map[string]string{
			beadmeta.SessionIDMetadataKey:   "ci-me",
			beadmeta.SessionNameMetadataKey: "city-worker",
		})
	if patch != nil {
		t.Fatalf("stamped %#v on a sibling that already carried it; every adoption tick would write again", patch)
	}
}

// TestHookClaimStampsNoHolderWithoutASession pins the documented absence. A
// hand-run `gc hook --claim` has no instance to name, and stamping the empty
// string would name the slot -- reinstating for every later occupant exactly
// the inheritance the stamp exists to break.
func TestHookClaimStampsNoHolderWithoutASession(t *testing.T) {
	patch := continuationHolderStampPatch(t, nil, nil)
	if patch != nil {
		t.Fatalf("stamped %#v with no GC_SESSION_ID set", patch)
	}
}
