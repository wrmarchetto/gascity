// test/docsync/dispatch_ordering_test.go pins the two dispatch claims an
// operator acts on and cannot check from any CLI surface: routed pool work is
// claimed oldest-first ahead of priority, and every ready bead on the queue is
// a candidate rather than only the head of a window.
//
// The suite exists because both are invisible from outside, and they present
// the same symptom. `bd ready` renders its rows priority-sorted, so an operator
// who raises a bead to P1 and watches it sit cannot tell a deliberate FIFO
// queue from a broken sort -- nor either of those from a bead that `bd` never
// returned to the claim at all. That is ci-q2vx: three lower-priority beads
// were claimed ahead of a ready P1 and the conclusion drawn, out loud, was that
// dispatch was broken. It turned out to be both -- a documented FIFO queue AND
// a 20-row candidate window hiding the P1 at row 21, fixed as ci-rzq2. Prose
// alone held neither: the same paragraph had already drifted to `--limit=1`
// while the code emitted `--limit=20`, so nothing here was trustworthy enough
// to settle the question.
//
// Scope is those two claims, in both directions. That the tiers' output then
// reaches the claim untruncated is behavioral and belongs to
// TestPoolTierCandidateWindowReachesTheQueueTail in internal/config, which
// executes the generated shell; whether the rest of the predicate stays shared
// with the reconciler's count form belongs to
// TestPoolDemandPredicateSharedWithWorkQuery; the generated operator-facing
// copy of the tier list is held by scripts/check-generated-docs-drift.sh.
//
// Run: go test ./test/docsync/ -run 'RoutedQueueOrdering|CandidateWindow'
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

// candidateWindowFlag is the unbounded row window the pool tiers ask bd for.
// Paired with routedQueueOrderingFlag it is the whole of the queue's contract:
// the sort decides which ready bead is taken first, and this decides nothing,
// deliberately.
const candidateWindowFlag = "--limit 0"

// candidateWindowPolicyPhrase is the load-bearing clause of the dispatch doc's
// window paragraph. As with the ordering phrase, a short match survives
// rewording and fails only if the claim itself is retracted.
const candidateWindowPolicyPhrase = "no candidate ceiling"

func TestPoolTierCandidateWindowMatchesEmittedWorkQuery(t *testing.T) {
	// The failure this guards is not a swap but a re-bounding: someone reads a
	// `--limit 0` on a pool tier as an oversight, caps it to keep the read
	// narrow, and every ready bead past the cap silently stops being claimable
	// while the doc still promises otherwise. Nothing an operator can run
	// distinguishes that from an idle queue.
	docPath := filepath.Join(repoRoot(), "engdocs", "architecture", "dispatch.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("reading dispatch architecture doc: %v", err)
	}
	doc := strings.Join(strings.Fields(string(raw)), " ")

	agent := config.Agent{Name: "worker", Dir: "hello-world"}
	query := agent.EffectiveWorkQuery()

	// Both tiers named in the doc's window paragraph are checked, for the same
	// reason orderedPoolTiers holds two: the pool-alias tier inherited its flags
	// by copying its sibling, so a re-bounding of one alone would be invisible
	// from the other's symptoms.
	for _, tier := range orderedPoolTiers {
		invocation, ok := bdInvocationContaining(query, tier.marker)
		if !ok {
			t.Errorf("work_query has no %s tier matching %q; this test can no longer see the tier it guards.\n"+
				"If the tier was renamed or restructured, update orderedPoolTiers.\nemitted query: %s",
				tier.name, tier.marker, query)
			continue
		}
		if !strings.Contains(invocation, candidateWindowFlag) {
			t.Errorf("the %s tier does not emit %q; it is bounding what is a claim candidate.\n"+
				"Ready work past the window is not returned by bd at all, so no Go-side reordering can\n"+
				"reach it and it becomes claimable only as the head drains -- ci-rzq2, measured at 41 ready\n"+
				"rows with a P1 at row 21. If a ceiling is genuinely needed, say in the commit body what\n"+
				"makes the hidden tail acceptable and rewrite the window paragraph in\n"+
				"engdocs/architecture/dispatch.md in the same commit.\ntier invocation: %s",
				tier.name, candidateWindowFlag, invocation)
		}
	}

	if !strings.Contains(doc, candidateWindowPolicyPhrase) {
		t.Errorf("engdocs/architecture/dispatch.md no longer states %q.\n"+
			"The pool tiers still emit %q, so deleting the claim leaves a reader with no way to tell an\n"+
			"unbounded queue from the 20-row window that preceded it.",
			candidateWindowPolicyPhrase, candidateWindowFlag)
	}
	if !strings.Contains(doc, candidateWindowFlag) {
		t.Errorf("engdocs/architecture/dispatch.md no longer shows the %q flag the pool tiers emit.\n"+
			"A reader cannot confirm the claim against the code without it.",
			candidateWindowFlag)
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
