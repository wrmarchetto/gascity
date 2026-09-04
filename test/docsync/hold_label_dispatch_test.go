// test/docsync/hold_label_dispatch_test.go pins the one claim in
// engdocs/contributors/hold-label-conventions.md that a reader ACTS on rather
// than merely reads: that a dispatch hold label records who should own a bead
// without telling a pool to start it, and that named enforcement points behind
// that promise still exist.
//
// The suite exists because that page is the only place the promise is written
// down, and a mayor reading it decides whether a decomposition can safely sit
// unworked. ci-t98zgv is what the absence costs: the lever had been mechanical
// for months, nothing said so, and the conclusion drawn -- out loud, in a rig's
// pm-log -- was that no lever existed at all, with four of five decomposed
// beads running to closure before the review that proposed them landed. Prose
// that is never read is a smaller failure than prose that is read and has since
// gone false, and this page is now in the second category by construction: it
// names Go identifiers in three packages.
//
// Scope is the doc-to-code join in both directions -- every canonical label
// value reaches the page, and every enforcement point the page cites is a real
// symbol at the path it claims. Whether those enforcement points actually
// suppress a spawn is behavioral and belongs to
// TestRecordedOwnerWithHoldRaisesNoSpawn in cmd/gc; the label vocabulary itself
// belongs to internal/beadmeta.
//
// Deliberately NOT asserted: the page's prose wording. Pinning sentences makes
// every edit a test failure and teaches editors to weaken the assertion rather
// than fix the drift. Only the identifiers and label values are pinned, because
// those are the parts that go false silently when code moves.
//
// Run: go test ./test/docsync/ -run HoldLabelDispatch
package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
)

const holdLabelDocPath = "engdocs/contributors/hold-label-conventions.md"

// holdLabelDispatchCitations are the enforcement points the "What a hold label
// does to dispatch" section sends a reader to. Each pair must hold in both
// directions: the identifier is named on the page, and it still exists at the
// path the page sends the reader to.
//
// The two shell builders are listed separately rather than folded into one
// entry because they cover different consumers -- flags for `bd`, a jq clause
// for the ephemeral tiers that have no such flag -- and a reader who finds only
// one would reasonably conclude the other tier is unguarded.
var holdLabelDispatchCitations = []struct {
	symbol string
	path   string
}{
	{"hasDispatchHoldLabel", "cmd/gc/pool_alias_demand.go"},
	{"excludeHoldLabelsShellArgs", "internal/config/workquery.go"},
	{"excludeHoldLabelsJQClause", "internal/config/workquery.go"},
	{"poolDesiredRequestIdentity", "cmd/gc/build_desired_state.go"},
	{"TestRecordedOwnerWithHoldRaisesNoSpawn", "cmd/gc/build_desired_state_recorded_owner_test.go"},
}

func readHoldLabelDoc(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(), holdLabelDocPath))
	if err != nil {
		t.Fatalf("read %s: %v", holdLabelDocPath, err)
	}
	return string(body)
}

// TestHoldLabelDispatchSectionSurvives pins the section's existence, because
// every other assertion in this file would pass vacuously against a page that
// had lost it -- the citations could all still be named in some other
// paragraph. It checks the heading and the sanctioned command, which is the
// smallest pair a reader cannot act without.
func TestHoldLabelDispatchSectionSurvives(t *testing.T) {
	doc := readHoldLabelDoc(t)
	for _, want := range []string{
		"## What a hold label does to dispatch",
		"bd set-state <id> hold=mayor",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("%s no longer contains %q.\n"+
				"This is the only written statement that recording an owner is separable from\n"+
				"starting work (ci-t98zgv). Removing it returns the city to the state where a\n"+
				"mayor concludes no such lever exists.", holdLabelDocPath, want)
		}
	}
}

// TestHoldLabelDispatchCitationsResolve is the direction that goes false on its
// own. Renaming any cited symbol, or moving it to another file, leaves the page
// pointing a reader at nothing while every test that exercises the RENAMED
// symbol stays green -- the rename is the one edit guaranteed not to touch this
// doc.
func TestHoldLabelDispatchCitationsResolve(t *testing.T) {
	doc := readHoldLabelDoc(t)
	for _, cite := range holdLabelDispatchCitations {
		t.Run(cite.symbol, func(t *testing.T) {
			if !strings.Contains(doc, cite.symbol) {
				t.Errorf("%s no longer cites %q.\n"+
					"The section's guarantee is only checkable by a reader who can reach the code\n"+
					"behind it; drop the citation only by dropping the claim it supports.",
					holdLabelDocPath, cite.symbol)
			}
			body, err := os.ReadFile(filepath.Join(repoRoot(), cite.path))
			if err != nil {
				t.Fatalf("%s cites %s in %s, which cannot be read: %v",
					holdLabelDocPath, cite.symbol, cite.path, err)
			}
			if !strings.Contains(string(body), cite.symbol) {
				t.Errorf("%s sends a reader to %s for %q, which is no longer there.\n"+
					"Update the page in the same commit as the rename, or the guarantee it states\n"+
					"becomes unverifiable without re-deriving it from scratch.",
					holdLabelDocPath, cite.path, cite.symbol)
			}
		})
	}
}

// TestHoldLabelDocNamesEveryDispatchHoldLabel derives the expected set from
// beadmeta rather than from a list kept here, so a third canonical value cannot
// be added in code while the page still describes two. The page's own "don't
// introduce a third without a new architecture decision" rule is what makes
// this cheap: the set is meant to be stable, so the test fires exactly when the
// decision is actually taken.
func TestHoldLabelDocNamesEveryDispatchHoldLabel(t *testing.T) {
	doc := readHoldLabelDoc(t)
	for _, label := range beadmeta.DispatchHoldLabels {
		if !strings.Contains(doc, label) {
			t.Errorf("%s does not mention %q, which the dispatcher excludes from pool demand.\n"+
				"A value enforced in code and absent from the taxonomy page is how the ad hoc\n"+
				"hold labels ga-tug8ry consolidated got started.", holdLabelDocPath, label)
		}
	}
}
