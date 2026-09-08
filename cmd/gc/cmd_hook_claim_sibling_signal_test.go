package main

// Scope: the sibling_branches field of the `gc hook --claim --json` record --
// the claim-time signal that unlanded work already exists for the condition
// the claimant just took. Three properties, one per failure this suite exists
// to catch: the signal appears and carries base AND state; it appears NOT AT
// ALL when there is nothing to point at; and it can never refuse a claim.
//
// Why the suite is built this way: the signal is a pointer an agent acts on,
// so the two ways it can be wrong are opposite. A missing signal repeats the
// measured failure (seven sessions, six sibling branches each, none
// consulted). A signal naming a branch that is not there, or naming one
// without saying how old it is, is worse than silence -- it invites an agent
// to build on dead work, which is why every case here asserts base and state,
// not existence. The wire NAMES are asserted against decoded raw JSON rather
// than through hookClaimJSONResult, because the consumer is an agent reading
// the field names: a struct-level decode agrees with a wrong `json:` tag.
//
// The no-refusal cases inject failure at the scan seam AFTER the claim
// mutation has committed, and assert on stdout and the exit code together. A
// case asserting only stdout would pass against a scan that turned a git
// hiccup into exit 1 -- which a startup wrapper reads as a failed claim and
// retries -- and one asserting only the code would pass against today's
// silence.
//
// Delegated elsewhere: how base and state are actually computed from a
// repository (cmd_hook_claim_sibling_branch_realgit_test.go, over real git --
// a canned seam here agrees with itself whatever the worktree holds), and
// every other field of the claim record (cmd_hook_claim_report_test.go).
//
// Run: go test ./cmd/gc/ -run HookClaimSibling
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// siblingSignalCandidate is the routed bead every case here claims. It carries
// a label because the label is what relates a claimed bead to its siblings;
// a bead with none is the documented no-signal case.
func siblingSignalCandidate() beads.Bead {
	return beads.Bead{
		ID:       "work-1",
		Status:   "open",
		Labels:   []string{"marker:queue-stalled"},
		Metadata: map[string]string{"gc.routed_to": "route-1"},
	}
}

// runSiblingSignalClaim drives one claim whose sibling scan is `resolve`, and
// returns stdout (for schema validation, which needs the raw buffer), the exit
// code and stderr.
func runSiblingSignalClaim(t *testing.T, resolve hookResolveSiblingBranchesFunc) (*bytes.Buffer, int, string) {
	t.Helper()
	candidate := siblingSignalCandidate()
	query, err := json.Marshal([]beads.Bead{candidate})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(query), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			return beads.Bead{
				ID:       beadID,
				Assignee: assignee,
				Status:   "in_progress",
				Labels:   candidate.Labels,
				Metadata: candidate.Metadata,
			}, true, nil
		},
		ResolveSiblingBranches: resolve,
		StampWorkMeta: func(context.Context, string, []string, string, string, map[string]string) error {
			return nil
		},
		PublishRunMap: func(string, string, ...string) error { return nil },
		DrainAck: func(string, io.Writer) error {
			t.Fatal("drain acknowledged after a committed claim")
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"route-1"},
		JSON:         true,
	}, ops, &stdout, &stderr)
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("stdout is empty: the claim committed and the caller was told nothing")
	}
	return &stdout, code, stderr.String()
}

// decodeRawClaimRecord parses the claim line into the untyped shape an agent
// reading the JSON actually sees.
func decodeRawClaimRecord(t *testing.T, stdout *bytes.Buffer) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &raw); err != nil {
		t.Fatalf("decoding claim record %q: %v", stdout.String(), err)
	}
	return raw
}

// TestHookClaimSiblingBranchesNameBaseAndState pins the signaling half: with
// unlanded sibling branches present, the record names each one AND the two
// facts that separate a live sibling from an abandoned one -- how far it has
// diverged (base, ahead) and when it last moved (last_commit), plus the state
// of the bead it belongs to.
//
// The assertion is per-field on the wire names rather than on a marshaled
// blob: a whole-record string comparison would pass against a field renamed
// on both sides of the code at once, which is exactly the drift the agent
// prompt cannot survive.
func TestHookClaimSiblingBranchesNameBaseAndState(t *testing.T) {
	stdout, code, stderr := runSiblingSignalClaim(t, func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) {
		return []hookClaimSiblingBranch{
			{
				Branch:     "fix/ci-aaaaaa-open-route-demand",
				BeadID:     "ci-aaaaaa",
				BeadStatus: "in_progress",
				Via:        "marker:queue-stalled",
				Base:       "8770533abcde",
				Tip:        "1111aaaa2222",
				Ahead:      3,
				LastCommit: "2026-09-08T10:00:00-05:00",
			},
			{
				Branch:     "fix/ci-bbbbbb-open-route-demand",
				BeadID:     "ci-bbbbbb",
				BeadStatus: "closed",
				Via:        hookSiblingViaSelf,
				Base:       "8770533abcde",
				Tip:        "3333cccc4444",
				Ahead:      1,
				LastCommit: "2026-08-27T02:11:00-05:00",
			},
		}, nil
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0: nothing failed, stderr=%q", code, stderr)
	}
	raw := decodeRawClaimRecord(t, stdout)
	branches, ok := raw["sibling_branches"].([]any)
	if !ok {
		t.Fatalf("sibling_branches missing or not an array in %q", stdout.String())
	}
	if len(branches) != 2 {
		t.Fatalf("sibling_branches has %d entries, want 2: %q", len(branches), stdout.String())
	}
	first, ok := branches[0].(map[string]any)
	if !ok {
		t.Fatalf("sibling_branches[0] is not an object: %q", stdout.String())
	}
	for field, want := range map[string]any{
		"branch":      "fix/ci-aaaaaa-open-route-demand",
		"bead_id":     "ci-aaaaaa",
		"bead_status": "in_progress",
		"via":         "marker:queue-stalled",
		"base":        "8770533abcde",
		"tip":         "1111aaaa2222",
		"ahead":       float64(3),
		"last_commit": "2026-09-08T10:00:00-05:00",
	} {
		if got := first[field]; got != want {
			t.Errorf("sibling_branches[0][%q] = %#v, want %#v", field, got, want)
		}
	}
	// The second entry is a CLOSED bead's branch. It is reported, not filtered:
	// an abandoned sibling is exactly what the claimant needs to recognize, and
	// suppressing it would leave the claimant unable to tell "no sibling" from
	// "a dead one". The state field is what carries the difference.
	second, ok := branches[1].(map[string]any)
	if !ok {
		t.Fatalf("sibling_branches[1] is not an object: %q", stdout.String())
	}
	if second["bead_status"] != "closed" || second["branch"] != "fix/ci-bbbbbb-open-route-demand" {
		t.Errorf("sibling_branches[1] = %#v, want the closed sibling reported with its state", second)
	}
}

// TestHookClaimSiblingBranchesOmittedWhenNoneExist pins the empty half. The key
// must be ABSENT, not an empty array and not a placeholder entry: the schema
// forbids unknown properties and the prompt tells the agent to read the field
// only when it is there, so an always-present empty array trains the agent to
// skim past a field that sometimes matters.
func TestHookClaimSiblingBranchesOmittedWhenNoneExist(t *testing.T) {
	stdout, code, stderr := runSiblingSignalClaim(t, func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) {
		return nil, nil
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0, stderr=%q", code, stderr)
	}
	raw := decodeRawClaimRecord(t, stdout)
	if _, present := raw["sibling_branches"]; present {
		t.Errorf("sibling_branches present with no siblings: %q", stdout.String())
	}
	if raw["bead_id"] != "work-1" {
		t.Errorf("bead_id = %#v, want work-1: the claim must still be reported", raw["bead_id"])
	}
}

// TestHookClaimSiblingScanFailureCannotRefuseTheClaim pins the third
// acceptance condition at the seam most likely to break in production: the
// scan shells out to git and queries the store, so it fails whenever either
// is unavailable. The claim has already committed at that point, so the only
// safe outcome is a reported claim with no pointer.
func TestHookClaimSiblingScanFailureCannotRefuseTheClaim(t *testing.T) {
	stdout, code, stderr := runSiblingSignalClaim(t, func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) {
		return nil, errors.New("git unavailable")
	})
	if code != 0 {
		t.Errorf("exit code = %d, want 0: a failed sibling scan must not read as a failed claim", code)
	}
	raw := decodeRawClaimRecord(t, stdout)
	if raw["bead_id"] != "work-1" || raw["action"] != "work" {
		t.Errorf("record = %#v, want the committed claim reported", raw)
	}
	if _, present := raw["sibling_branches"]; present {
		t.Errorf("sibling_branches present after the scan failed: %q", stdout.String())
	}
	// The failure is not laundered into silence either: an operator debugging a
	// signal that never fires needs to see that the scan is erroring.
	if !strings.Contains(stderr, "sibling branches") {
		t.Errorf("stderr = %q, want the sibling-scan diagnostic", stderr)
	}
}

// TestHookClaimSiblingScanPanicCannotRefuseTheClaim is the same acceptance
// condition against the one failure an error return cannot express. The scan
// runs BEFORE the claim record reaches stdout, so an unrecovered panic there
// kills the process with the bead already assigned and in_progress and the
// owning session never told which bead it holds -- the ci-gyj39 shape, in a
// code path added by a signal that was required never to refuse anything.
func TestHookClaimSiblingScanPanicCannotRefuseTheClaim(t *testing.T) {
	stdout, code, stderr := runSiblingSignalClaim(t, func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) {
		panic("sibling scan blew up")
	})
	if code != 0 {
		t.Errorf("exit code = %d, want 0, stderr=%q", code, stderr)
	}
	raw := decodeRawClaimRecord(t, stdout)
	if raw["bead_id"] != "work-1" {
		t.Errorf("record = %#v, want the committed claim reported", raw)
	}
	if !strings.Contains(stderr, "sibling branches") {
		t.Errorf("stderr = %q, want the sibling-scan diagnostic", stderr)
	}
}

// TestHookClaimSiblingBranchesValidateAgainstResultSchema pins the field to
// the published wire contract in BOTH states. schemas/hook/result.schema.json
// sets additionalProperties:false, so a field emitted without a schema entry
// makes every validating consumer of `gc hook --claim --json` reject the
// record -- and nothing else in the tree validates this command's real output
// against that schema, so the drift is invisible until a consumer breaks.
//
// Both states are checked because they exercise different halves of the
// schema: the populated record tests the array's item shape, the empty one
// tests that omitting the key is still valid.
func TestHookClaimSiblingBranchesValidateAgainstResultSchema(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve hookResolveSiblingBranchesFunc
	}{
		{"with siblings", func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) {
			return []hookClaimSiblingBranch{{
				Branch:     "fix/ci-aaaaaa-open-route-demand",
				BeadID:     "ci-aaaaaa",
				BeadStatus: "in_progress",
				Via:        "marker:queue-stalled",
				Base:       "8770533abcde",
				Tip:        "1111aaaa2222",
				Ahead:      3,
				LastCommit: "2026-09-08T10:00:00-05:00",
			}}, nil
		}},
		{"without siblings", func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) {
			return nil, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, code, stderr := runSiblingSignalClaim(t, tc.resolve)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0, stderr=%q", code, stderr)
			}
			validateManagementJSONPayload(t, []string{"hook"}, stdout)
		})
	}
}
