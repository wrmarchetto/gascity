package main

// Scope: the Go-side half of the mail-displacement fix -- that
// filterUnreadyHookCandidates drops mail from work_query output, so the demand
// side and the claim side share one predicate.
//
// Why this layer exists in addition to the config-side fix: the generated
// work_query is not the only producer of this output. A pack may supply its
// own work_query, and the legacy tiers this repo still emits are a second
// shape. filterUnreadyHookCandidates is the one funnel all of them pass
// through -- doHook (the `gc hook <agent>` demand probe, cmd_hook.go),
// doHookClaim (cmd_hook_claim.go), and both cross-store paths
// (hook_cross_store.go). Filtering here makes a message unable to create
// demand no matter which query produced it.
//
// It does NOT subsume the config-side fix and must not be mistaken for it:
// the shell ladder exits on its first hit, so by the time output reaches Go
// the routed work below the message was never fetched. Go can suppress the
// false demand (clause a); only the ladder change restores the hidden work
// (clause b). Both are required.
//
// Delegated elsewhere: the ladder's fallthrough is pinned in
// internal/config/workquery_message_displacement_test.go.
//
// Run: go test ./cmd/gc/ -run HookMessage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// TestHookMessageCandidatesAreNotDemand pins that a message bead in
// work_query output is not reported as work. This is the spawn-loop half:
// demand that the claim path refuses by construction
// (hookClaimCandidateIsMessage) must not reach the reconciler as "work
// exists", or the slot respawns forever at boot cadence.
func TestHookMessageCandidatesAreNotDemand(t *testing.T) {
	now := time.Now()
	out := filterUnreadyHookCandidates(
		`[{"id":"ci-wisp-p2gjmg","issue_type":"message","status":"open","assignee":"toolsmith"}]`, now)

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got) != 0 {
		t.Fatalf("message bead survived the hook filter and reads as demand: %s", out)
	}
}

// TestHookMessageDoesNotDisplaceRealWorkInSameBatch pins that filtering a
// message leaves every real bead in the batch untouched and in order. The
// #4419 comment records the original symptom precisely -- a message was
// returned "ahead of any real routed work waiting in the same batch" -- so
// order matters here, not just membership.
func TestHookMessageDoesNotDisplaceRealWorkInSameBatch(t *testing.T) {
	out := filterUnreadyHookCandidates(`[
		{"id":"ci-wisp-p2gjmg","issue_type":"message","status":"open","assignee":"toolsmith"},
		{"id":"ci-fh4o","issue_type":"bug","status":"open"},
		{"id":"ci-c0cu","issue_type":"feature","status":"open"}
	]`, time.Now())

	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 real beads after dropping the message, got %d: %s", len(got), out)
	}
	if got[0]["id"] != "ci-fh4o" || got[1]["id"] != "ci-c0cu" {
		t.Errorf("real work reordered or lost: %s", out)
	}
}

// TestHookMessagePredicateMatchesTheClaimPredicate is the anti-drift pin. The
// whole bug was two sides disagreeing about what counts as work: #4419 taught
// the claim to skip messages and left the demand side unchanged. If these two
// predicates are ever allowed to diverge again the loop returns in a new
// shape, so they are asserted against the same inputs here.
func TestHookMessagePredicateMatchesTheClaimPredicate(t *testing.T) {
	for _, tc := range []struct {
		typeField string
		isMessage bool
	}{
		{"message", true},
		{"Message", true}, // claim side is case-insensitive; demand must be too
		{" message ", true},
		{"bug", false},
		{"task", false},
		{"", false},
	} {
		claimSkips := hookClaimCandidateIsMessage(beads.Bead{ID: "x", Type: tc.typeField})
		if claimSkips != tc.isMessage {
			t.Fatalf("claim predicate disagrees with the table for %q", tc.typeField)
		}

		body := `[{"id":"x","issue_type":` + mustJSON(tc.typeField) + `,"status":"open"}]`
		demandDrops := strings.TrimSpace(filterUnreadyHookCandidates(body, time.Now())) == "[]"
		if demandDrops != claimSkips {
			t.Errorf("type %q: claim skips=%v but demand drops=%v -- the two sides disagree, which is the bug",
				tc.typeField, claimSkips, demandDrops)
		}
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
