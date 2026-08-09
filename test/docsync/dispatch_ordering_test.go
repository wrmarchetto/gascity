// test/docsync/dispatch_ordering_test.go pins the one dispatch claim an
// operator acts on and cannot check from any CLI surface: routed pool work is
// claimed oldest-first, ahead of priority.
//
// The suite exists because that policy is invisible from outside. `bd ready`
// renders its rows priority-sorted, so an operator who raises a bead to P1 and
// watches it sit cannot tell a deliberate FIFO queue from a broken one. That
// is ci-q2vx: three lower-priority beads were claimed ahead of a ready P1 and
// the conclusion drawn, out loud, was that dispatch was broken. Prose alone did
// not hold the policy -- the same paragraph had already drifted to `--limit=1`
// while the code emitted `--limit=20`, so nothing here was trustworthy enough
// to settle the question.
//
// Scope is the ordering claim only, in both directions. Whether the rest of the
// predicate stays shared with the reconciler's count form belongs to
// TestPoolDemandPredicateSharedWithWorkQuery in internal/config; the generated
// operator-facing copy of the tier list is held by
// scripts/check-generated-docs-drift.sh.
//
// Run: go test ./test/docsync/ -run RoutedQueueOrdering
package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// routedQueueOrderingFlag is the ordering the pool tiers ask bd for. It is a
// policy choice, not a default: bd's own unflagged ready order is
// (priority, created_at, id), which this deliberately overrides.
const routedQueueOrderingFlag = "--sort oldest"

// routedQueueOrderingPolicyPhrase is the load-bearing clause of the dispatch
// doc's policy paragraph. Matching a short phrase rather than the whole
// sentence keeps ordinary rewording from failing the test while still failing
// if the policy itself is retracted.
const routedQueueOrderingPolicyPhrase = "FIFO before priority"

// orderedPoolTiers are the work_query tiers whose ordering the policy governs,
// keyed by the filter that identifies each one inside the generated shell.
//
// Both are listed because the pool-alias tier is the one that produced ci-q2vx
// and it is NOT the tier the policy paragraph was originally written for -- it
// inherited --sort oldest in 11f9dbd0a by copying its sibling's shape, with no
// commit body, comment or doc reconsidering the key. Checking the two together
// is what stops that silent inheritance from happening again in a third tier.
//
// The own-identity tiers (in_progress and ready, keyed on GC_SESSION_ID /
// GC_SESSION_NAME / GC_ALIAS) are deliberately absent: they pass no --sort at
// all and so keep bd's priority-first default. Adding them here would pin an
// ordering they do not have.
var orderedPoolTiers = []struct {
	name   string
	marker string
}{
	{name: "routed", marker: `--metadata-field "gc.routed_to=$target" --unassigned`},
	{name: "pool-alias", marker: `--assignee="$target"`},
}

func TestRoutedQueueOrderingPolicyMatchesEmittedWorkQuery(t *testing.T) {
	// Asserting the flag and the prose together is the whole point. Either
	// assertion alone passes while the pair lies: a code-only check lets the
	// doc keep promising FIFO after someone swaps the sort key, and a doc-only
	// check pins a sentence nothing implements. The bug being guarded is a
	// silent swap to strict priority, which starves this city's long P3 tail.
	docPath := filepath.Join(repoRoot(), "engdocs", "architecture", "dispatch.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("reading dispatch architecture doc: %v", err)
	}
	// The doc is hand-wrapped, so both the phrase and the flag straddle line
	// breaks. Collapsing whitespace compares meaning rather than the wrap.
	doc := strings.Join(strings.Fields(string(raw)), " ")

	agent := config.Agent{Name: "worker", Dir: "hello-world"}
	query := agent.EffectiveWorkQuery()

	// Scoping each assertion to its own tier's bd invocation is what keeps this
	// test honest. A bare strings.Contains over the whole script passes on any
	// ONE surviving --sort oldest, so dropping the flag from a single tier --
	// exactly the ci-q2vx shape -- would not fail it.
	for _, tier := range orderedPoolTiers {
		invocation, ok := bdInvocationContaining(query, tier.marker)
		if !ok {
			t.Errorf("work_query has no %s tier matching %q; this test can no longer see the tier it guards.\n"+
				"If the tier was renamed or restructured, update orderedPoolTiers.\nemitted query: %s",
				tier.name, tier.marker, query)
			continue
		}
		if !strings.Contains(invocation, routedQueueOrderingFlag) {
			t.Errorf("the %s tier emits no %q; it no longer claims oldest-first.\n"+
				"That is a policy change, not a refactor: update the routed-queue policy paragraph in\n"+
				"engdocs/architecture/dispatch.md in the same commit, and say in the commit body what\n"+
				"stops the backlog tail from starving.\ntier invocation: %s",
				tier.name, routedQueueOrderingFlag, invocation)
		}
	}

	if !strings.Contains(doc, routedQueueOrderingPolicyPhrase) {
		t.Errorf("engdocs/architecture/dispatch.md no longer states %q.\n"+
			"The pool tiers still emit %q, so deleting the sentence leaves the policy in force with nothing\n"+
			"recording it -- which is how ci-q2vx was read as a dispatch bug rather than a documented queue.\n"+
			"Restore the policy statement or change the ordering.",
			routedQueueOrderingPolicyPhrase, routedQueueOrderingFlag)
	}
	if !strings.Contains(doc, routedQueueOrderingFlag) {
		t.Errorf("engdocs/architecture/dispatch.md no longer shows the %q flag the pool tiers emit.\n"+
			"A reader cannot confirm the policy against the code without it.",
			routedQueueOrderingFlag)
	}
}

// bdInvocationContaining returns the single `bd ready` invocation within the
// generated work_query shell that carries marker, so an assertion lands on one
// tier instead of on whichever tier happens to still satisfy it.
//
// Splitting on the literal "bd ready" is enough because the generator emits
// each tier as one unbroken invocation on the shell's command list; there is no
// nesting or quoting that would put a stray "bd ready" inside another tier's
// arguments. A shell parser here would be more precise and would also be a
// second implementation of the generator, which is the thing under test.
func bdInvocationContaining(query, marker string) (string, bool) {
	for _, segment := range strings.Split(query, "bd ready") {
		if strings.Contains(segment, marker) {
			return segment, true
		}
	}
	return "", false
}
