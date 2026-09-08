package main

// Scope: hookSiblingBeadsFrom -- which beads count as related to the one just
// claimed, and what relation is reported for each.
//
// Why this suite exists as its own file: this half was written and reviewed
// once with no test, and a mutation that reported EVERY relation as "self"
// survived the whole rest of the sibling-signal suite. Nothing downstream can
// catch it, because the branch scan reports whatever relation it is handed.
//
// The lister stand-in REFUSES a label no case scripted. One that answered
// every label with an empty slice would hand a pass to any query the
// implementation invents -- including querying nothing at all, which is the
// defect that makes the signal silently never fire.
//
// Not covered here, and deliberately: whether the production lister passes
// IncludeClosed and the per-label bound to the store. That is one statement in
// hookSiblingBeads, above a real bd subprocess, and driving it needs a live
// store. It is kept to one statement for exactly that reason.
//
// Run: go test ./cmd/gc/ -run HookClaimSiblingRelation
import (
	"errors"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// scriptedLister answers only the labels in script and fails the test on any
// other, so a query the suite never anticipated cannot pass silently.
func scriptedLister(t *testing.T, script map[string][]beads.Bead) hookSiblingLister {
	t.Helper()
	return func(label string) ([]beads.Bead, error) {
		found, ok := script[label]
		if !ok {
			t.Fatalf("unscripted label query %q", label)
		}
		return found, nil
	}
}

// TestHookClaimSiblingRelationNamesWhatFoundEachBead pins that the claimed
// bead is reported as its own prior attempt while a label-matched bead is
// reported under THAT label. A claimant uses the distinction to dismiss a
// branch related only by a state label, so collapsing the two -- reporting
// everything as "self", or everything as a label -- makes the field
// worthless while every other assertion in the sibling suite still passes.
func TestHookClaimSiblingRelationNamesWhatFoundEachBead(t *testing.T) {
	claimed := beads.Bead{
		ID:     "ci-claimed",
		Status: "in_progress",
		Labels: []string{"marker:queue-stalled", "hold:external"},
	}
	list := scriptedLister(t, map[string][]beads.Bead{
		"marker:queue-stalled": {{ID: "ci-sibling", Status: "closed"}},
		"hold:external":        {{ID: "ci-parked", Status: "open"}},
	})

	got, err := hookSiblingBeadsFrom(claimed, list)
	if err != nil {
		t.Fatalf("hookSiblingBeadsFrom: %v", err)
	}
	want := []hookSiblingBead{
		{Bead: claimed, Via: hookSiblingViaSelf},
		{Bead: beads.Bead{ID: "ci-sibling", Status: "closed"}, Via: "marker:queue-stalled"},
		{Bead: beads.Bead{ID: "ci-parked", Status: "open"}, Via: "hold:external"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d related beads (%+v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].Bead.ID != want[i].Bead.ID || got[i].Via != want[i].Via {
			t.Errorf("related[%d] = {%s via %s}, want {%s via %s}",
				i, got[i].Bead.ID, got[i].Via, want[i].Bead.ID, want[i].Via)
		}
	}
}

// TestHookClaimSiblingRelationQueriesNothingWithoutLabels pins the absence
// that keeps the scan off the critical path for ordinary work. The claim has
// already committed when this runs, so a store round trip here is time the
// owning session is not yet told which bead it holds -- and most beads carry
// no labels at all. The lister fails the test if it is called.
func TestHookClaimSiblingRelationQueriesNothingWithoutLabels(t *testing.T) {
	claimed := beads.Bead{ID: "ci-claimed", Status: "in_progress"}
	got, err := hookSiblingBeadsFrom(claimed, func(label string) ([]beads.Bead, error) {
		t.Fatalf("queried the store for label %q on a bead with no labels", label)
		return nil, nil
	})
	if err != nil {
		t.Fatalf("hookSiblingBeadsFrom: %v", err)
	}
	if len(got) != 1 || got[0].Bead.ID != "ci-claimed" || got[0].Via != hookSiblingViaSelf {
		t.Fatalf("got %+v, want only the claimed bead as its own prior attempt", got)
	}
}

// TestHookClaimSiblingRelationDeduplicatesByID covers the two ways one bead
// arrives twice: the claimed bead coming back from its own label query, and a
// bead carrying two of the claimed bead's labels. Both must appear once, and
// the FIRST relation must win -- the claimed bead stays "self" rather than
// being relabelled by whichever query happened to return it.
func TestHookClaimSiblingRelationDeduplicatesByID(t *testing.T) {
	claimed := beads.Bead{
		ID:     "ci-claimed",
		Status: "in_progress",
		Labels: []string{"marker:queue-stalled", "epic:timers"},
	}
	both := beads.Bead{ID: "ci-sibling", Status: "open"}
	list := scriptedLister(t, map[string][]beads.Bead{
		"marker:queue-stalled": {{ID: "ci-claimed", Status: "in_progress"}, both},
		"epic:timers":          {both},
	})

	got, err := hookSiblingBeadsFrom(claimed, list)
	if err != nil {
		t.Fatalf("hookSiblingBeadsFrom: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %+v, want the claimed bead and one sibling", got)
	}
	if got[0].Bead.ID != "ci-claimed" || got[0].Via != hookSiblingViaSelf {
		t.Errorf("related[0] = {%s via %s}, want the claimed bead via %s",
			got[0].Bead.ID, got[0].Via, hookSiblingViaSelf)
	}
	if got[1].Bead.ID != "ci-sibling" || got[1].Via != "marker:queue-stalled" {
		t.Errorf("related[1] = {%s via %s}, want ci-sibling via its first matching label",
			got[1].Bead.ID, got[1].Via)
	}
}

// TestHookClaimSiblingRelationSkipsBlankLabelsAndIDs pins that neither a
// blank label nor an id-less row reaches the branch scan. A blank label would
// be one wasted store query per claim; an id-less row would match every
// branch name once substring matching gets hold of it, turning the whole
// repository into a wall of fabricated pointers.
func TestHookClaimSiblingRelationSkipsBlankLabelsAndIDs(t *testing.T) {
	claimed := beads.Bead{
		ID:     "ci-claimed",
		Status: "in_progress",
		Labels: []string{"  ", "marker:queue-stalled"},
	}
	list := scriptedLister(t, map[string][]beads.Bead{
		"marker:queue-stalled": {{ID: "  ", Status: "open"}, {ID: "ci-sibling", Status: "open"}},
	})

	got, err := hookSiblingBeadsFrom(claimed, list)
	if err != nil {
		t.Fatalf("hookSiblingBeadsFrom: %v", err)
	}
	if len(got) != 2 || got[1].Bead.ID != "ci-sibling" {
		t.Fatalf("got %+v, want the claimed bead and ci-sibling only", got)
	}
}

// TestHookClaimSiblingRelationAbortsOnAListerFailure pins that a failed store
// query yields no candidate set rather than a partial one. A partial set
// reports "no sibling branch" while a sibling branch is on disk, which is
// indistinguishable from a clean city and is the exact state this signal
// exists to end.
func TestHookClaimSiblingRelationAbortsOnAListerFailure(t *testing.T) {
	claimed := beads.Bead{
		ID:     "ci-claimed",
		Status: "in_progress",
		Labels: []string{"marker:queue-stalled", "epic:timers"},
	}
	got, err := hookSiblingBeadsFrom(claimed, func(label string) ([]beads.Bead, error) {
		if label == "marker:queue-stalled" {
			return []beads.Bead{{ID: "ci-sibling", Status: "open"}}, nil
		}
		return nil, errors.New("store unreachable")
	})
	if err == nil {
		t.Fatalf("hookSiblingBeadsFrom returned %+v and no error after a failed query", got)
	}
	if got != nil {
		t.Errorf("returned %+v alongside the error, want nothing", got)
	}
}
