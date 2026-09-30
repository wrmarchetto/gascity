package main

// cmd/gc/cmd_hook_stop_park_shape_test.go
//
// Does the parking shape the Stop gate ADVISES actually wake when the thing it
// waits on closes? The gate's own honored-claim predicate and its advice text
// were two independent statements of that shape, and nothing joined them, so
// the advice named a park -- status `blocked`, the trigger recorded in prose --
// that no work-query tier serves and no status transition ever leaves.
// astoria-sel4 as-fp8q sat in it from 15:47Z on 2026-09-30 with its trigger
// closed at 16:16:42Z until a human reopened it (city ci-qtuugn).
//
// Scope: the advice's shape against the REAL Tier 1 work query
// (config.Agent.EffectiveAssignedInProgressQuery), executed as a shell against
// a fake bd. Delegated elsewhere: whether the gate lets a parked turn END
// (cmd_hook_stop_test.go's TestStopGateAllowsClaimParkedOnOpenAssignedQuestion
// and TestStopGateParkedClaimProbeDrivesTheStoreCommands), and the tier's
// blocked_by enrichment in general
// (internal/config/workquery_inprogress_blocked_test.go).
//
//	go test ./cmd/gc/ -run TestStopGateParkShape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// parkShapeSessionID is the identity the parked claim is assigned to, and the
// only one the fake bd answers for.
const parkShapeSessionID = "sess-1"

// parkShapeFakeBd simulates a store holding ONE claim parked by the advised
// shape -- a row at stopGateParkShape.status carrying a "blocks" dependency on
// trigger-1 -- with that trigger at blockerStatus.
//
// The row's status comes from stopGateParkShape, and `list` honors the
// --status filter the way bd does, so this is the whole red: a shape whose
// status no tier queries is never listed, however its dependencies stand. A
// fake that ignored --status would serve the row for any shape at all and the
// test would pass against the park that lost as-fp8q.
//
// It REFUSES an unscripted subcommand rather than answering []. An always-empty
// stand-in makes "not served" the answer to every question, including the
// control below, and the two are indistinguishable from outside.
func parkShapeFakeBd(rowStatus, blockerStatus string) string {
	row := `[{"id":"wk-1","status":"` + rowStatus + `","assignee":"` + parkShapeSessionID + `","title":"parked work"}]`
	show := `[{"id":"wk-1","status":"` + rowStatus + `","dependencies":` +
		`[{"id":"trigger-1","status":"` + blockerStatus + `","dependency_type":"blocks"}]}]`
	return `#!/bin/sh
want=""
assignee=""
prev=""
for a in "$@"; do
  [ "$prev" = "--status" ] && want="$a"
  case "$a" in --assignee=*) assignee="${a#--assignee=}" ;; esac
  prev="$a"
done
case "$1" in
  list)
    if [ "$want" = "` + rowStatus + `" ] && [ "$assignee" = "` + parkShapeSessionID + `" ]; then
      printf '%s' '` + row + `'
    else
      printf '[]'
    fi
    ;;
  show)  printf '%s' '` + show + `' ;;
  query) printf '[]' ;;
  *) echo "fake bd: unscripted subcommand $1" >&2; exit 3 ;;
esac
`
}

// runParkShapeTier executes the real assigned-in-progress tier against the
// fake bd and returns the ids it served.
//
// It runs the query through shellWorkQueryWithEnv, the same production helper
// every hook path uses, rather than building an exec.Cmd here. That is not
// only tidiness: test/test-resources.toml is an anti-growth ratchet over
// subprocess call sites in *_test.go, and a new one is a council policy change
// (TESTING.md, "Checked source-level resource ratchets"). Reaching for the
// production runner keeps this suite under the ratchet AND puts the shell
// behind the same timeout, env and stderr handling the gate itself gets.
//
// jq is assumed present rather than skipped over. Every tier of this query
// pipes through it, so a skip would turn a missing jq into a green run of a
// suite that tested nothing -- the sibling cmd/gc stop-gate probes make the
// same assumption.
func runParkShapeTier(t *testing.T, rowStatus, blockerStatus string) []string {
	t.Helper()

	dir := t.TempDir()
	bd := filepath.Join(dir, "bd")
	if err := os.WriteFile(bd, []byte(parkShapeFakeBd(rowStatus, blockerStatus)), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	agent := config.Agent{Name: "toolsmith", Dir: "city"}
	out, err := shellWorkQueryWithEnv(agent.EffectiveAssignedInProgressQuery(), dir, []string{
		"PATH=" + dir + ":" + os.Getenv("PATH"),
		"GC_SESSION_ID=" + parkShapeSessionID,
	})
	if err != nil {
		t.Fatalf("run work query: %v", err)
	}

	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("tier output is not a JSON array: %v (output %q)", err, out)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

// TestStopGateParkShapeIsServedOnceItsTriggerCloses is the sequence the advice
// promises, driven end to end: park the claim in the advised shape, close the
// bead it waits on, and require the city to hand it back.
//
// It fails on the shape the advice named before ci-qtuugn. That park is
// `blocked`, Tier 1 lists `--status in_progress` and Tiers 2-3 go through
// bd ready (open and deferred only), so the row is served by nobody whatever
// its trigger does -- which is the defect, not a detail of this rig.
func TestStopGateParkShapeIsServedOnceItsTriggerCloses(t *testing.T) {
	ids := runParkShapeTier(t, stopGateParkShape.status, "closed")
	if len(ids) != 1 || ids[0] != "wk-1" {
		t.Fatalf("a bead parked in the advised shape was NOT served after its "+
			"trigger closed, so the advice parks work permanently: served %v", ids)
	}
}

// TestStopGateParkShapeIsWithheldWhileItsTriggerIsOpen is the control, and it
// is not optional: a rig that reports "served" is worth nothing until it has
// been shown to report the opposite. Without it a tier that served every row
// unconditionally would satisfy the test above.
func TestStopGateParkShapeIsWithheldWhileItsTriggerIsOpen(t *testing.T) {
	ids := runParkShapeTier(t, stopGateParkShape.status, "open")
	if len(ids) != 0 {
		t.Fatalf("a bead parked in the advised shape was served while the bead "+
			"it waits on is still open: served %v", ids)
	}
}

// TestStopGateParkShapeStatusIsAStatusTheWorkQueryAsksFor is the assertion
// with a source outside itself: the status the advice tells a session to leave
// its bead at must be one the assigned tier actually asks bd for. Nothing else
// in this file establishes that -- the two sequence tests read the shape
// through a fake bd that this file also writes, so they pin the tier's
// dependency handling while taking the status on trust.
//
// It is the assertion that kills the park ci-qtuugn was filed about, and it
// kills it without running anything: `blocked` appears in no tier's query.
func TestStopGateParkShapeStatusIsAStatusTheWorkQueryAsksFor(t *testing.T) {
	agent := config.Agent{Name: "toolsmith", Dir: "city"}
	query := agent.EffectiveAssignedInProgressQuery()
	if !strings.Contains(query, "--status "+stopGateParkShape.status) {
		t.Fatalf("the advice parks beads at %q, a status the assigned work "+
			"query never asks bd for, so a bead parked as advised is served to "+
			"nobody however its dependencies stand: %s",
			stopGateParkShape.status, query)
	}
}

// TestStopGateParkShapeAdviceNamesTheShapeItHonors pins that the paragraph is
// RENDERED from stopGateParkShape rather than restating it.
//
// What it cannot catch, stated so it is not mistaken for more than it is:
// while the text is built from the struct these assertions are near-tautologies
// -- the body contains the field values because it was printed from them. They
// become load-bearing the moment someone hand-writes the paragraph, which is
// precisely the edit that produced the defect, so the value is in the future
// edit, not in today's run. The assertion with an independent source is the
// sibling above.
func TestStopGateParkShapeAdviceNamesTheShapeItHonors(t *testing.T) {
	reason := stopGateOutstandingReason(stopGateHoldingWork())
	_, body, found := strings.Cut(reason, "\n")
	if !found {
		t.Fatalf("block reason has no body after its header line: %q", reason)
	}

	if !strings.Contains(body, stopGateParkShape.metadataKey) {
		t.Errorf("advice never names %s, the key the gate reads to recognize a "+
			"park, so a reader cannot produce the shape: %q",
			stopGateParkShape.metadataKey, body)
	}
	if !strings.Contains(body, stopGateParkShape.status) {
		t.Errorf("advice never names the status a parked claim is left at, so a "+
			"reader must guess it: %q", body)
	}
	// The dependency is the wake -- the metadata alone records an intention
	// that no tier acts on. stopGateHasBlockingDependency requires the edge
	// and inProgressBlockedByEnrichmentScript reads it; advice that omitted it
	// would produce a bead served immediately instead of a parked one.
	//
	// "blocking dependency" and not the bare word "dependency": a mutation
	// sweep on this file deleted the whole instruction and SURVIVED the looser
	// check, because the sentence explaining that the dependency is the wake
	// still carried the word. The cost of the tighter phrase is that a rewrite
	// to some other spelling of the edge goes red; that red is a prompt to
	// re-read this comment, which is the cheaper failure.
	depAt := strings.Index(body, "blocking dependency")
	if depAt < 0 {
		t.Errorf("advice records the waited-on id but never says to add the "+
			"blocking dependency that actually withholds and wakes the bead: %q", body)
	}
	// Link first, record second -- the order the city's own ask-pm.py follows
	// and documents. A reader who records the metadata and then stops has a
	// bead that looks parked to a human and is served to the next session.
	if keyAt := strings.Index(body, stopGateParkShape.metadataKey); depAt >= 0 && depAt > keyAt {
		t.Errorf("advice names the metadata record before the dependency that "+
			"makes it mean anything: %q", body)
	}
}
