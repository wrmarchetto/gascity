package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// workquery_candidate_window_test.go pins the one property the pool tiers'
// --limit must NOT have: it must not decide which ready beads are claim
// candidates at all. The sort key decides ORDER; nothing may decide candidacy.
//
// ci-rzq2 measured the failure on the city store: 41 ready rows on the
// toolsmith pool alias against a 20-row window, with a P1 created that evening
// sitting at row 21. bd never returned it, so no Go-side reordering could
// reach it and it became claimable only as the head drained. From outside the
// symptom is indistinguishable from a broken sort -- `bd ready` renders
// priority-first, so an operator sees a ready P1 sitting behind P3s -- which is
// why ci-q2vx first read it as an ordering bug.
//
// These tests EXECUTE the generated shell against a fake `bd` on PATH instead
// of matching the flag's spelling, because the truncation happens inside bd. A
// string assertion on the flag would stay green if a later edit reintroduced
// the ceiling further down the pipeline, as a jq slice on the tier's output.
//
// Delegated elsewhere: the FIFO sort key is
// TestRoutedQueueOrderingPolicyMatchesEmittedWorkQuery (test/docsync), which
// also holds the doc's copy of this window claim; that the worker first-row
// form and the reconciler count form derive from one predicate is
// TestPoolDemandPredicateSharedWithWorkQuery; the byte-for-byte generated
// shell is TestWorkQueryGolden.
//
// Run: go test ./internal/config/ -run CandidateWindow

// candidateWindowRoute is the pool-demand target every case below routes to.
// It doubles as the fake bd's tier marker, so it must contain no shell glob
// metacharacter.
const candidateWindowRoute = "hello-world/worker"

// candidateWindowQueueLen is longer than any window the tiers have ever asked
// for, so a surviving ceiling truncates the queue whatever its value.
const candidateWindowQueueLen = 25

// candidateWindowTailID is the row that must survive. It is the LAST row of a
// queue longer than the window -- the position ci-rzq2 measured as
// unreachable -- so an assertion on row count alone could not replace it: a
// window returning the right NUMBER of wrong rows would pass.
const candidateWindowTailID = "tail-p1"

// fakeBdWindowedReady returns a fake `bd` that answers exactly one ready tier,
// the one whose invocation carries marker, and honors bd's --limit contract on
// it. Every other invocation answers `[]`, so a row reaching the output can
// only have arrived through the tier under test.
//
// A positive limit is simulated as the harshest window possible, a single row,
// rather than as the 20 the code happens to emit today. The invariant is that
// the tier asks for NO ceiling: 20 and 2000 both hide the tail, only further
// out, so a fake that reproduced the current number would hand a pass to the
// next re-bounding. bd's own truncation is a prefix of the sorted result, which
// is what this reproduces.
func fakeBdWindowedReady(marker string, rows []string) string {
	if len(rows) == 0 {
		panic("fakeBdWindowedReady: no rows")
	}
	all := "[" + strings.Join(rows, ",") + "]"
	truncated := "[" + rows[0] + "]"
	return `#!/bin/sh
case "$1" in
  ready)
    case "$*" in
      *'` + marker + `'*)
        case "$*" in
          *"--limit 0"*|*"--limit=0"*) printf '%s' '` + all + `' ;;
          *) printf '%s' '` + truncated + `' ;;
        esac
        ;;
      *) printf '[]' ;;
    esac
    ;;
  *) printf '[]' ;;
esac
`
}

// routedReadyRow renders one ready row as the routed tier sees it: unassigned,
// carrying the canonical route in metadata.
func routedReadyRow(id string, priority int) string {
	return fmt.Sprintf(`{"id":%q,"issue_type":"task","status":"open","assignee":"","priority":%d,"metadata":{"gc.routed_to":%q}}`,
		id, priority, candidateWindowRoute)
}

// aliasReadyRow renders one ready row as the pool-alias tier sees it: parked on
// the pool's bare name by hand, with no routing metadata at all (ci-c000).
func aliasReadyRow(id string, priority int) string {
	return fmt.Sprintf(`{"id":%q,"issue_type":"task","status":"open","assignee":%q,"priority":%d}`,
		id, candidateWindowRoute, priority)
}

// candidateWindowQueue builds a queue whose tail is the high-priority row. The
// priorities are inverted against the queue order on purpose: under the
// documented FIFO policy the P1 is claimed last, so its only route to a worker
// is being a candidate in the first place.
func candidateWindowQueue(row func(string, int) string) []string {
	rows := make([]string, 0, candidateWindowQueueLen)
	for i := 1; i < candidateWindowQueueLen; i++ {
		rows = append(rows, row(fmt.Sprintf("head-%02d", i), 3))
	}
	return append(rows, row(candidateWindowTailID, 1))
}

func TestPoolTierCandidateWindowReachesTheQueueTail(t *testing.T) {
	tiers := []struct {
		name   string
		marker string
		row    func(string, int) string
	}{
		// Both tiers are listed because they are separate bd invocations that
		// reached the same flag by copying each other's shape (ci-c000), and a
		// fix applied to one of them looks complete from the other's symptoms.
		{name: "routed", marker: "gc.routed_to=" + candidateWindowRoute, row: routedReadyRow},
		{name: "pool-alias", marker: "--assignee=" + candidateWindowRoute, row: aliasReadyRow},
	}
	for _, tier := range tiers {
		t.Run(tier.name, func(t *testing.T) {
			rows := candidateWindowQueue(tier.row)
			a := Agent{Name: "worker", Dir: "hello-world"}
			out := strings.TrimSpace(runEffectiveWorkQuery(t, a, nil, fakeBdWindowedReady(tier.marker, rows)))

			var got []struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("decoding %s tier output %q: %v", tier.name, out, err)
			}
			if len(got) != len(rows) {
				t.Fatalf("%s tier returned %d of %d ready rows.\n"+
					"The tier is asking bd for a bounded window, so everything past it is not a claim\n"+
					"candidate at all -- it becomes claimable only as the head drains (ci-rzq2).\n"+
					"Ask for --limit 0 and let filterUnreadyHookCandidates pick from the whole queue.\noutput: %s",
					tier.name, len(got), len(rows), out)
			}
			last := got[len(got)-1].ID
			if last != candidateWindowTailID {
				t.Fatalf("%s tier tail row = %q, want %q; the queue arrived reordered or truncated.\noutput: %s",
					tier.name, last, candidateWindowTailID, out)
			}
		})
	}
}

// TestMigrationTierCandidateWindowSurvivesAFullyFilteredHead is the sharper
// half of the same defect. The migration probe's routed_to filter runs in jq
// AFTER bd has applied the window, so a window filled entirely with rows the
// filter discards yields nothing even when a qualifying row sits just past it.
// The routed and pool-alias tiers at least return SOMETHING claimable from a
// bounded window; this one can return empty while ready work exists.
func TestMigrationTierCandidateWindowSurvivesAFullyFilteredHead(t *testing.T) {
	// Every head row carries gc.routed_to, which is exactly what
	// poolDemandMigrationFilterJQ discards: these roots have already been
	// backfilled and the canonical routed tier owns them now.
	rows := make([]string, 0, candidateWindowQueueLen)
	for i := 1; i < candidateWindowQueueLen; i++ {
		rows = append(rows, fmt.Sprintf(`{"id":%q,"issue_type":"task","status":"open","assignee":"","metadata":{"gc.kind":"workflow","gc.run_target":%q,"gc.routed_to":%q}}`,
			fmt.Sprintf("backfilled-%02d", i), candidateWindowRoute, candidateWindowRoute))
	}
	rows = append(rows, fmt.Sprintf(`{"id":%q,"issue_type":"task","status":"open","assignee":"","metadata":{"gc.kind":"workflow","gc.run_target":%q}}`,
		candidateWindowTailID, candidateWindowRoute))

	a := Agent{Name: "worker", Dir: "hello-world"}
	out := strings.TrimSpace(runEffectiveWorkQuery(t, a, nil,
		fakeBdWindowedReady("gc.run_target="+candidateWindowRoute, rows)))
	if !strings.Contains(out, candidateWindowTailID) {
		t.Fatalf("migration tier returned %q, want the one pre-backfill root %q.\n"+
			"A bounded window ahead of the jq routed_to filter reports an empty queue while ready\n"+
			"work exists, because the window can be consumed entirely by rows the filter discards.",
			out, candidateWindowTailID)
	}
}
