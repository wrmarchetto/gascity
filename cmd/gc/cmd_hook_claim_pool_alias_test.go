// cmd_hook_claim_pool_alias_test.go pins the claim half of the pool-alias fix
// (ci-c000): a bead hand-assigned to a pool's BARE name with
// `bd update --assignee <pool>` is claimable by any of that pool's slots.
//
// The query half (internal/config/workquery.go) only makes the bead VISIBLE.
// Surfacing it without this half reproduces the original silence in a new place:
// the candidate arrives, every claim branch rejects it (it is neither this
// session's own identity nor unassigned), and the hook drain-acks as if the queue
// were empty. So these tests assert on which mutation ran, not merely on the exit
// code -- a green claim through the wrong branch is the failure being guarded.
//
// Run: go test ./cmd/gc/ -run PoolAlias
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestHookPoolClaimSwapsBeforeItLeasesAndReloadsCanonically(t *testing.T) {
	// The order is the whole guarantee: leasing first would take a bead the
	// compare-and-swap had not yet won. The canonical show at the end is what
	// carries gc.root_bead_id / gc.continuation_group into the continuation and
	// identity-stamp code, which a partial `bd update --claim` projection omits.
	originalRunner := hookClaimCommandRunnerWithEnvContext
	t.Cleanup(func() { hookClaimCommandRunnerWithEnvContext = originalRunner })

	var calls [][]string
	hookClaimCommandRunnerWithEnvContext = func(_ context.Context, _ map[string]string) beads.CommandRunner {
		return func(_ string, name string, args ...string) ([]byte, error) {
			if name != "bd" {
				t.Fatalf("command name = %q, want bd", name)
			}
			calls = append(calls, append([]string(nil), args...))
			switch {
			case reflect.DeepEqual(args, []string{"update", "work-1", "--if-assignee", "crew", "--assignee", "crew-2", "--set-metadata", "gc.routed_to=crew", "--json"}):
				return []byte(`[{"id":"work-1","status":"open","assignee":"crew-2","metadata":{"gc.routed_to":"crew"}}]`), nil
			case reflect.DeepEqual(args, []string{"update", "work-1", "--claim", "--json"}):
				return []byte(`[{"id":"work-1","status":"in_progress","assignee":"crew-2"}]`), nil
			case reflect.DeepEqual(args, []string{"show", "--json", "work-1"}):
				return []byte(`[{"id":"work-1","status":"in_progress","assignee":"crew-2","metadata":{"gc.routed_to":"crew","gc.root_bead_id":"root-1","gc.continuation_group":"review"}}]`), nil
			default:
				t.Fatalf("unexpected bd args: %#v", args)
				return nil, nil
			}
		}
	}

	claimed, ok, err := hookPoolClaimWithBdStore(context.Background(), "/rig", nil, "work-1", "crew", "crew-2")
	if err != nil {
		t.Fatalf("hookPoolClaimWithBdStore: %v", err)
	}
	if !ok {
		t.Fatal("hookPoolClaimWithBdStore ok = false, want true")
	}
	if claimed.Metadata["gc.root_bead_id"] != "root-1" || claimed.Metadata["gc.continuation_group"] != "review" {
		t.Fatalf("claimed metadata = %#v, want canonical root and continuation group", claimed.Metadata)
	}
	if got := claimed.Metadata["gc.routed_to"]; got != "crew" {
		t.Fatalf("claimed gc.routed_to = %q, want the original pool alias crew", got)
	}
	if len(calls) < 2 || !strings.Contains(strings.Join(calls[0], " "), "--if-assignee") {
		t.Fatalf("bd calls = %#v, want the guarded swap first", calls)
	}
	if !strings.Contains(strings.Join(calls[1], " "), "--claim") {
		t.Fatalf("bd calls = %#v, want the lease taken after the swap", calls)
	}
}

func TestHookPoolClaimDoesNotLeaseAfterALostSwap(t *testing.T) {
	originalRunner := hookClaimCommandRunnerWithEnvContext
	t.Cleanup(func() { hookClaimCommandRunnerWithEnvContext = originalRunner })

	hookClaimCommandRunnerWithEnvContext = func(_ context.Context, _ map[string]string) beads.CommandRunner {
		return func(_ string, name string, args ...string) ([]byte, error) {
			switch {
			case reflect.DeepEqual(args, []string{"update", "work-1", "--if-assignee", "crew", "--assignee", "crew-2", "--set-metadata", "gc.routed_to=crew", "--json"}):
				return []byte(`assignee mismatch: work-1 is held by "crew-3", expected "crew"`), errors.New("exit status 13")
			case reflect.DeepEqual(args, []string{"show", "--json", "work-1"}):
				return []byte(`[{"id":"work-1","status":"in_progress","assignee":"crew-3"}]`), nil
			}
			t.Fatalf("unexpected bd args after a lost swap: %s %#v", name, args)
			return nil, nil
		}
	}

	current, ok, err := hookPoolClaimWithBdStore(context.Background(), "/rig", nil, "work-1", "crew", "crew-2")
	if err != nil {
		t.Fatalf("a lost swap is not an operational failure, got err=%v", err)
	}
	if ok {
		t.Fatal("hookPoolClaimWithBdStore ok = true, want false after a lost swap")
	}
	if current.Assignee != "crew-3" {
		t.Fatalf("current.Assignee = %q, want the winner crew-3 for the rejection event", current.Assignee)
	}
}

// poolAliasClaimProbe records which claim mutation the hook chose. Both seams
// refuse unscripted work: whichever one is wrong for the case under test fails
// the test rather than returning a success the assertions would have to notice.
type poolAliasClaimProbe struct {
	plainClaimed string
	poolClaimed  string
	poolAlias    string
	poolActor    string
}

func TestHookClaimTakesOpenBeadParkedOnThePoolAlias(t *testing.T) {
	// The shape ci-c000 measured: assignee is the pool route name, status open,
	// and NO gc.routed_to, because a hand-assignment stamps no routing metadata.
	candidates := []beads.Bead{
		{ID: "hand-assigned", Status: "open", Assignee: "crew"},
	}
	output, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}

	var probe poolAliasClaimProbe
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(output), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, _ string) (beads.Bead, bool, error) {
			probe.plainClaimed = beadID
			t.Errorf("plain Claim ran for a pool-parked bead %s; bd refuses --claim on a bead assigned to another name", beadID)
			return beads.Bead{}, false, nil
		},
		PoolClaim: func(_ context.Context, _ string, _ []string, beadID, alias, assignee string) (beads.Bead, bool, error) {
			probe.poolClaimed, probe.poolAlias, probe.poolActor = beadID, alias, assignee
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress"}, true, nil
		},
		DrainAck: func(string, io.Writer) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", ".", hookClaimOptions{
		Assignee:           "crew-2",
		IdentityCandidates: []string{"crew-2"},
		RouteTargets:       []string{"crew"},
		JSON:               true,
	}, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim() = %d, want 0; stderr=%s", code, stderr.String())
	}
	if probe.poolClaimed != "hand-assigned" {
		t.Fatalf("PoolClaim bead = %q, want hand-assigned", probe.poolClaimed)
	}
	// The alias must be the name the bead is actually parked on: it is the
	// expected value of the compare-and-swap, so passing anything else turns the
	// atomic take into an unconditional steal.
	if probe.poolAlias != "crew" {
		t.Fatalf("PoolClaim alias = %q, want the pool route name crew", probe.poolAlias)
	}
	if probe.poolActor != "crew-2" {
		t.Fatalf("PoolClaim assignee = %q, want this slot crew-2", probe.poolActor)
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v (stdout=%s)", err, stdout.String())
	}
	if result.Action != "work" || result.BeadID != "hand-assigned" {
		t.Fatalf("result = %+v, want action=work on hand-assigned", result)
	}
}

func TestHookClaimLeavesInProgressPoolAliasBeadToItsHolder(t *testing.T) {
	// ga-80pen8: the bare pool name is ALSO a [[named_session]] holder's own
	// identity, so an in_progress bead under that name is the holder's live work,
	// not free pool work. A suffixed slot must leave it alone. `bd ready` already
	// filters in_progress out of the tier, so this is the second of two
	// independent gates -- kept because the candidate list can come from a
	// user-supplied work_query that owes no such filtering.
	candidates := []beads.Bead{
		{ID: "holder-live", Status: "in_progress", Assignee: "crew"},
	}
	output, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}

	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(output), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, _ string) (beads.Bead, bool, error) {
			t.Errorf("Claim ran on the holder's live bead %s", beadID)
			return beads.Bead{}, false, nil
		},
		PoolClaim: func(_ context.Context, _ string, _ []string, beadID, _, _ string) (beads.Bead, bool, error) {
			t.Errorf("PoolClaim ran on the holder's live bead %s", beadID)
			return beads.Bead{}, false, nil
		},
		DrainAck: func(string, io.Writer) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	// --drain-ack mirrors the real startup invocation (`gc hook --claim
	// --drain-ack --json`), which is also the only form whose drain exits 0.
	code := doHookClaim("query", ".", hookClaimOptions{
		Assignee:           "crew-2",
		IdentityCandidates: []string{"crew-2"},
		RouteTargets:       []string{"crew"},
		DrainAck:           true,
		JSON:               true,
	}, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim() = %d, want 0; stderr=%s", code, stderr.String())
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v (stdout=%s)", err, stdout.String())
	}
	if result.Action != "drain" {
		t.Fatalf("result = %+v, want drain; the holder's live bead is not claimable pool work", result)
	}
}

func TestHookClaimAdoptsOwnBareNameBeadWithoutTheTransfer(t *testing.T) {
	// The canonical singleton and the named holder both have GC_ALIAS == the bare
	// name, so for them a bare-name bead is own-identity work and must still be
	// promoted by the adoption path. Routing it through the pool transfer instead
	// would reassign the bead to itself and burn an extra write on every tick.
	candidates := []beads.Bead{
		{ID: "own-work", Status: "open", Assignee: "crew"},
	}
	output, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}

	var probe poolAliasClaimProbe
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(output), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			probe.plainClaimed = beadID
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress"}, true, nil
		},
		PoolClaim: func(_ context.Context, _ string, _ []string, beadID, _, _ string) (beads.Bead, bool, error) {
			t.Errorf("PoolClaim ran for own-identity bead %s", beadID)
			return beads.Bead{}, false, nil
		},
		DrainAck: func(string, io.Writer) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", ".", hookClaimOptions{
		Assignee:           "crew",
		IdentityCandidates: []string{"crew"},
		RouteTargets:       []string{"crew"},
		JSON:               true,
	}, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim() = %d, want 0; stderr=%s", code, stderr.String())
	}
	if probe.plainClaimed != "own-work" {
		t.Fatalf("plain Claim bead = %q, want own-work promoted by the adoption path", probe.plainClaimed)
	}
}

func TestHookClaimReportsRejectionWhenAnotherSlotWinsThePoolTransfer(t *testing.T) {
	candidates := []beads.Bead{
		{ID: "contested", Status: "open", Assignee: "crew"},
	}
	output, err := json.Marshal(candidates)
	if err != nil {
		t.Fatalf("marshal candidates: %v", err)
	}

	var rejectedBead, rejectedWinner string
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(output), nil },
		PoolClaim: func(_ context.Context, _ string, _ []string, beadID, _, _ string) (beads.Bead, bool, error) {
			return beads.Bead{ID: beadID, Assignee: "crew-3", Status: "in_progress"}, false, nil
		},
		EmitClaimRejected: func(beadID, existing, _ string) {
			rejectedBead, rejectedWinner = beadID, existing
		},
		DrainAck: func(string, io.Writer) error { return nil },
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", ".", hookClaimOptions{
		Assignee:           "crew-2",
		IdentityCandidates: []string{"crew-2"},
		RouteTargets:       []string{"crew"},
		DrainAck:           true,
		JSON:               true,
	}, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim() = %d, want 0; stderr=%s", code, stderr.String())
	}
	// A lost transfer is the arbitration working, not a fault -- but it must stay
	// observable, because a pool bead nobody can take looks identical to an idle
	// queue from outside.
	if rejectedBead != "contested" || rejectedWinner != "crew-3" {
		t.Fatalf("claim_rejected = (%q, %q), want (contested, crew-3)", rejectedBead, rejectedWinner)
	}
}
