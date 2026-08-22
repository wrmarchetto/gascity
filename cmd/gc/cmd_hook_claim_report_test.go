// cmd_hook_claim_report_test.go pins that a committed claim is never silent:
// once the claim mutation has taken the bead, `gc hook --claim` tells the caller
// WHICH bead it now holds, on every exit path including the failing ones.
//
// The suite exists because the claim and the report of the claim are separated
// by four more store round trips (identity stamp, lifecycle emission, run-map
// publish, continuation preassign), and two of those windows previously ended in
// a bare `return 1` that wrote nothing to stdout. The store is mutated at that
// point and cannot be un-mutated: the bead is assigned and in_progress, so every
// ready query has already stopped offering it, while the session that owns it was
// handed an empty result and has no reason to believe it holds anything. That is
// the shape ci-gyj39 was filed on -- a session parked four hours on a P1 it was
// never told about -- and it is invisible from the outside, because the store
// reads exactly like a healthy claim.
//
// Every case here therefore injects its failure at a seam AFTER the claim
// mutation reports success, and asserts on stdout rather than on the exit code.
// The exit code is asserted too, but only as a control: a test that accepted any
// nonzero code would pass against a fix that silently swallowed the failure, and
// a test that asserted only the code would pass against today's empty stdout.
// The pairing is what separates "reported the bead AND the failure" from either
// half alone.
//
// Not covered here, deliberately: a claim mutation that did NOT commit. Nothing
// is owed to the caller in that case beyond the drain record, and
// cmd_hook_claim_test.go already owns those paths. Adding them here would blur
// what this file is for -- the invariant is about committed claims only.
//
// Run: go test ./cmd/gc/ -run HookClaimReport
package main

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

// decodeHookClaimReport parses the single JSON line the hook is expected to have
// written. It fails the test rather than returning an error because an
// unparseable or absent line IS the defect this file exists to catch, and a
// caller that had to check an error would let the empty case reach the field
// assertions as a zero-valued struct.
func decodeHookClaimReport(t *testing.T, stdout string) hookClaimJSONResult {
	t.Helper()
	line := strings.TrimSpace(stdout)
	if line == "" {
		t.Fatal("stdout is empty: the claim committed and the caller was told nothing")
	}
	var result hookClaimJSONResult
	if err := json.Unmarshal([]byte(line), &result); err != nil {
		t.Fatalf("decoding hook result %q: %v", line, err)
	}
	return result
}

// TestHookClaimReportNamesBeadAfterContinuationPreassignFails drives the
// continuation-group preassign failure, which is reachable only on a bead
// carrying both gc.root_bead_id and gc.continuation_group. The sibling assign is
// the failing seam rather than the sibling list because it fails AFTER the loop
// has already assigned earlier siblings, so it is the branch that leaves the most
// store state behind while reporting nothing.
func TestHookClaimReportNamesBeadAfterContinuationPreassignFails(t *testing.T) {
	candidate := beads.Bead{
		ID:     "work-1",
		Status: "open",
		Metadata: map[string]string{
			"gc.routed_to":          "route-1",
			"gc.root_bead_id":       "root-1",
			"gc.continuation_group": "review",
		},
	}
	query, err := json.Marshal([]beads.Bead{candidate})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(query), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress", Metadata: candidate.Metadata}, true, nil
		},
		ListContinuation: func(context.Context, string, []string, string, string) ([]beads.Bead, error) {
			return []beads.Bead{{ID: "sib-1", Status: "open", Metadata: candidate.Metadata}}, nil
		},
		AssignContinuation: func(context.Context, string, []string, string, string) error {
			return errors.New("store write refused")
		},
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

	report := decodeHookClaimReport(t, stdout.String())
	if report.Action != "work" || report.BeadID != "work-1" || report.Assignee != "worker-1" {
		t.Fatalf("report = %+v, want action=work bead_id=work-1 assignee=worker-1", report)
	}
	// The preassign never completed, so the hook must not claim siblings were
	// assigned. An empty list here and a populated one on the success path are
	// what stop a resumed session from skipping work it was told it already had.
	if len(report.ContinuationAssigned) != 0 {
		t.Errorf("continuation_assigned = %v, want empty after the preassign failed", report.ContinuationAssigned)
	}
	// The control: reporting the bead must not launder the failure into success.
	if code == 0 {
		t.Error("exit code = 0, want nonzero: the continuation preassign failed")
	}
	if !strings.Contains(stderr.String(), "preassigning continuation group") {
		t.Errorf("stderr = %q, want the preassign diagnostic", stderr.String())
	}
}

// TestHookClaimReportNamesBeadAfterCanonicalReadbackFails drives the window the
// claim path itself calls out: `bd update --claim` committed and its own identity
// check passed, then the canonical reload failed. The bead is ours and the store
// says so; only gc's view of it is incomplete.
//
// This case previously asserted the OPPOSITE -- that stdout stayed empty (the
// original TestDoHookClaimStopsAfterCommittedClaimReadbackFailure). Stopping was
// always right and still is; staying silent was the part that was wrong, because
// the id is the one thing gc does know here. It came back from the mutation.
func TestHookClaimReportNamesBeadAfterCanonicalReadbackFails(t *testing.T) {
	runner := func(string, string) (string, error) {
		return `[
			{"id":"work-1","status":"open","metadata":{"gc.routed_to":"route-1"}},
			{"id":"work-2","status":"open","metadata":{"gc.routed_to":"route-1"}}
		]`, nil
	}
	var attempts []string
	ops := hookClaimOps{
		Runner: runner,
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			attempts = append(attempts, beadID)
			return beads.Bead{ID: beadID, Assignee: assignee}, true, errors.New("canonical read failed")
		},
		DrainAck: func(string, io.Writer) error {
			t.Fatal("drain acknowledged after a committed claim")
			return nil
		},
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"route-1"},
		DrainAck:     true,
		JSON:         true,
	}, ops, &stdout, &stderr)

	report := decodeHookClaimReport(t, stdout.String())
	if report.Action != "work" || report.BeadID != "work-1" || report.Assignee != "worker-1" {
		t.Fatalf("report = %+v, want action=work bead_id=work-1 assignee=worker-1", report)
	}
	if code == 0 {
		t.Error("exit code = 0, want nonzero: the canonical readback failed")
	}
	// Pinned from the original suite: one committed claim must not be followed by
	// an attempt on the next candidate, which would strand the first.
	if got := strings.Join(attempts, ","); got != "work-1" {
		t.Errorf("claim attempts = %q, want only the committed work-1", got)
	}
	if !strings.Contains(stderr.String(), "claimed work-1 but loading canonical bead failed") {
		t.Errorf("stderr = %q, want the committed-claim diagnostic", stderr.String())
	}
}

// TestHookClaimReportStillStampsWhenContinuationPreassignFails pins that moving
// the identity stamp, the lifecycle emission and the run-map publish AFTER the
// report did not also move them after a `return`.
//
// The preassign-failure path returns nonzero, and it is the one path where those
// three writes now sit downstream of the failure rather than upstream of it. A
// committed, in_progress bead that never gets its gc.session_id stamp has no
// execution.step_started fact for a step that did start and no run map, and
// nothing reclaims an unstamped in_progress bead -- the projection gap is
// permanent for that bead. Reachable only by continuation-group beads, which is
// why it is cheap to leave broken and easy to miss.
//
// The test asserts each write happened rather than counting them, because the
// defect is a whole branch not executing, not a miscount.
func TestHookClaimReportStillStampsWhenContinuationPreassignFails(t *testing.T) {
	candidate := beads.Bead{
		ID:     "work-1",
		Status: "in_progress",
		Metadata: map[string]string{
			"gc.routed_to":          "route-1",
			"gc.root_bead_id":       "root-1",
			"gc.continuation_group": "review",
		},
	}
	query, err := json.Marshal([]beads.Bead{candidate})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	stamped := false
	runMapped := false
	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(query), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress", Metadata: candidate.Metadata}, true, nil
		},
		ListContinuation: func(context.Context, string, []string, string, string) ([]beads.Bead, error) {
			return []beads.Bead{{ID: "sib-1", Status: "open", Metadata: candidate.Metadata}}, nil
		},
		AssignContinuation: func(context.Context, string, []string, string, string) error {
			return errors.New("store write refused")
		},
		StampWorkMeta: func(context.Context, string, []string, string, string, map[string]string) error {
			stamped = true
			return nil
		},
		PublishRunMap: func(string, string, ...string) error {
			runMapped = true
			return nil
		},
		ResolveWorkBranch: func(string) string { return "fix/branch" },
		DrainAck: func(string, io.Writer) error {
			t.Fatal("drain acknowledged after a committed claim")
			return nil
		},
	}

	var stdout, stderr bytes.Buffer
	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"route-1"},
		Env:          []string{"GC_SESSION_ID=sess-1", "GC_SESSION_NAME=worker-1"},
		JSON:         true,
	}, ops, &stdout, &stderr)

	// The control: this must remain the FAILING path, or the test is asserting
	// the bookkeeping runs on a path where it always ran.
	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero: the continuation preassign failed")
	}
	if report := decodeHookClaimReport(t, stdout.String()); report.BeadID != "work-1" {
		t.Fatalf("report bead_id = %q, want work-1", report.BeadID)
	}
	if !stamped {
		t.Error("execution identity was never stamped: a committed in_progress bead is left with no gc.session_id")
	}
	if !runMapped {
		t.Error("run map was never published for a committed claim")
	}
}

// TestHookClaimReportPrecedesBestEffortClaimTimeWrites pins the ORDER, not just
// the presence, of the report. The identity stamp, the lifecycle emission and the
// run-map publish are all best-effort follow-ups that each cost a store round
// trip; running them before the write is what put ~4 extra round trips between
// the mutation and the first byte the caller sees.
//
// Asserting order needs a recorded sequence rather than a "did stdout have
// content" check inside each seam: the seams write to the same buffer the
// assertion reads, so a check written that way passes as soon as ANY seam has run
// and cannot tell the first from the last.
func TestHookClaimReportPrecedesBestEffortClaimTimeWrites(t *testing.T) {
	candidate := beads.Bead{
		ID:       "work-1",
		Status:   "open",
		Metadata: map[string]string{"gc.routed_to": "route-1"},
	}
	query, err := json.Marshal([]beads.Bead{candidate})
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	var stdout, stderr bytes.Buffer
	var sequence []string
	note := func(step string) {
		if stdout.Len() > 0 && (len(sequence) == 0 || sequence[0] != "report") {
			sequence = append(sequence, "report")
		}
		sequence = append(sequence, step)
	}

	ops := hookClaimOps{
		Runner: func(string, string) (string, error) { return string(query), nil },
		Claim: func(_ context.Context, _ string, _ []string, beadID, assignee string) (beads.Bead, bool, error) {
			note("claim")
			return beads.Bead{ID: beadID, Assignee: assignee, Status: "in_progress", Metadata: candidate.Metadata}, true, nil
		},
		StampWorkMeta: func(context.Context, string, []string, string, string, map[string]string) error {
			note("stamp")
			return nil
		},
		PublishRunMap: func(string, string, ...string) error {
			note("runmap")
			return nil
		},
		ResolveWorkBranch: func(string) string { return "fix/branch" },
		DrainAck: func(string, io.Writer) error {
			t.Fatal("drain acknowledged after a committed claim")
			return nil
		},
	}

	code := doHookClaim("query", "/rig", hookClaimOptions{
		Assignee:     "worker-1",
		RouteTargets: []string{"route-1"},
		Env:          []string{"GC_SESSION_ID=sess-1", "GC_SESSION_NAME=worker-1"},
		JSON:         true,
	}, ops, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("doHookClaim = %d, want 0; stderr=%s", code, stderr.String())
	}

	report := decodeHookClaimReport(t, stdout.String())
	if report.BeadID != "work-1" {
		t.Fatalf("report bead_id = %q, want work-1", report.BeadID)
	}
	// The control against a vacuous pass: if no best-effort seam ran at all, the
	// sequence proves nothing about ordering.
	if len(sequence) < 3 {
		t.Fatalf("sequence = %v, want the claim plus at least two best-effort writes", sequence)
	}
	if got := sequence[0]; got != "claim" {
		t.Fatalf("sequence = %v, want the claim first", sequence)
	}
	if got := sequence[1]; got != "report" {
		t.Errorf("sequence = %v, want the report written before any best-effort claim-time write", sequence)
	}
}
