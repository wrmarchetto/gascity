// Unit suite for the decision-record parser and the catalog reconciliation.
//
// Scope is this package's two functions against synthetic documents. It
// deliberately reads no repository file: the live catalogs are checked by
// waiver_review_test.go in internal/testpolicy/resourcecensus and
// internal/testutil/providerledger, and duplicating that here would make a
// green run here depend on whichever expiry the tree happens to carry.
//
// Every refusal below is one the repository would otherwise take as a
// recorded decision, so each test names the wrong conclusion it prevents
// rather than the input it feeds. The retire half matters as much as the
// retain half even though no entry has been retired yet: an unexercised
// branch is what lets the first real retirement be recorded against an entry
// that is still live.
//
// Run: go test ./internal/testpolicy/waiverreview/
package waiverreview

import (
	"strings"
	"testing"
)

func document(rows ...string) string {
	body := []string{Begin, header, separator}
	body = append(body, rows...)
	body = append(body, End)
	return "preamble\n\n" + strings.Join(body, "\n") + "\n\nepilogue\n"
}

const (
	retainRow = "| resourcecensus | `debt scope=untagged resource=tmux` | 2026-11-09 | retain | 2026-09-11 | the sweep proof starts a real server |"
	retireRow = "| providerledger | `runtime.builtin.gone internal/runtime/gone.New` | 2026-08-12 | retire | 2026-09-11 | the provider was deleted |"
)

func mustParse(t *testing.T, doc string) []Decision {
	t.Helper()
	decisions, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return decisions
}

func TestParseReadsEveryCellOfADecisionRow(t *testing.T) {
	t.Parallel()

	decisions := mustParse(t, document(retainRow))
	if len(decisions) != 1 {
		t.Fatalf("parsed %d decisions, want 1", len(decisions))
	}
	want := Decision{
		Catalog:  "resourcecensus",
		Key:      "debt scope=untagged resource=tmux",
		Expiry:   "2026-11-09",
		Decision: "retain",
		Reviewed: "2026-09-11",
		Basis:    "the sweep proof starts a real server",
	}
	if decisions[0] != want {
		t.Fatalf("decision = %+v, want %+v", decisions[0], want)
	}
}

func TestParseRejectsRowsThatWouldSilentlyDropAnEntry(t *testing.T) {
	t.Parallel()

	// Each of these is a row a permissive parser skips. Skipping one hands
	// the catalog guard a missing key, which it reports as a waiver nobody
	// reviewed -- a true statement about the record with a cause nowhere
	// near where the guard is looking.
	for name, row := range map[string]string{
		"not a table row": "debt scope=untagged resource=tmux retain",
		// Six cells and no leading pipe. The cell-count guard cannot see
		// this one -- Trim strips the trailing pipe and the split yields
		// exactly six -- so it is the only case that pins the row-shape
		// guard. The earlier "not a table row" case does not: it dies on
		// the cell count with the shape guard deleted.
		"six cells, no leading pipe": "resourcecensus | `k` | 2026-11-09 | retain | 2026-09-11 | basis |",
		"too few cells":              "| resourcecensus | `k` | 2026-11-09 | retain | 2026-09-11 |",
		"too many cells":             "| resourcecensus | `k` | 2026-11-09 | retain | 2026-09-11 | basis | extra |",
		"unquoted entry":             "| resourcecensus | k | 2026-11-09 | retain | 2026-09-11 | basis |",
		"empty quoted entry":         "| resourcecensus | `` | 2026-11-09 | retain | 2026-09-11 | basis |",
		"expiry not a date":          "| resourcecensus | `k` | soon | retain | 2026-09-11 | basis |",
		"reviewed not a date":        "| resourcecensus | `k` | 2026-11-09 | retain | yesterday | basis |",
		"impossible date":            "| resourcecensus | `k` | 2026-02-31 | retain | 2026-09-11 | basis |",
		"empty basis":                "| resourcecensus | `k` | 2026-11-09 | retain | 2026-09-11 |    |",
	} {
		if _, err := Parse(document(row)); err == nil {
			t.Errorf("%s: Parse unexpectedly succeeded", name)
		}
	}
}

func TestParseRejectsARenamedOrReorderedHeader(t *testing.T) {
	t.Parallel()

	// The cells are positional, so a reordered header does not fail on its
	// own -- it silently re-reads the expiry column as the decision.
	reordered := "preamble\n" + Begin + "\n" +
		"| Catalog | Entry | Decision | Expiry | Reviewed | Basis |\n" +
		separator + "\n" + retainRow + "\n" + End + "\n"
	if _, err := Parse(reordered); err == nil {
		t.Fatal("Parse accepted a reordered header")
	}
	missingSeparator := "preamble\n" + Begin + "\n" + header + "\n" + retainRow + "\n" + End + "\n"
	if _, err := Parse(missingSeparator); err == nil {
		t.Fatal("Parse accepted a table with no separator row")
	}
}

func TestParseRejectsAMarkerSetItCannotBound(t *testing.T) {
	t.Parallel()

	for name, doc := range map[string]string{
		"no markers":     "preamble only\n",
		"no end":         Begin + "\n" + header + "\n" + separator + "\n",
		"reversed":       End + "\n" + header + "\n" + separator + "\n" + Begin + "\n",
		"twice":          document(retainRow) + document(retainRow),
		"header only":    Begin + "\n" + header + "\n" + End + "\n",
		"nothing at all": Begin + "\n" + End + "\n",
	} {
		if _, err := Parse(doc); err == nil {
			t.Errorf("%s: Parse unexpectedly succeeded", name)
		}
	}
}

func TestParseRejectsOneEntryDecidedTwice(t *testing.T) {
	t.Parallel()

	// Two rows for one entry make the record ambiguous while satisfying set
	// equality, so the contradiction survives a green gate.
	conflicting := "| resourcecensus | `debt scope=untagged resource=tmux` | 2026-11-09 | retire | 2026-09-11 | contradicts the row above |"
	if _, err := Parse(document(retainRow, conflicting)); err == nil {
		t.Fatal("Parse accepted two decisions for one entry")
	}
}

func TestParseKeepsSameKeyUnderDifferentCatalogs(t *testing.T) {
	t.Parallel()

	// The two catalogs mint their keys independently, so identity is the
	// pair. Deduplicating on the key alone would reject a legitimate record.
	twin := "| providerledger | `debt scope=untagged resource=tmux` | 2026-11-09 | retain | 2026-09-11 | same spelling, other catalog |"
	if decisions := mustParse(t, document(retainRow, twin)); len(decisions) != 2 {
		t.Fatalf("parsed %d decisions, want 2", len(decisions))
	}
}

func TestCheckPassesWhenEveryLiveEntryCitesItsCurrentExpiry(t *testing.T) {
	t.Parallel()

	live := map[string]string{"debt scope=untagged resource=tmux": "2026-11-09"}
	if err := Check(CatalogResourceCensus, mustParse(t, document(retainRow, retireRow)), live); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestCheckFailsWhenTheSharedDateMovedWithoutAReReview(t *testing.T) {
	t.Parallel()

	// The defect this package exists for: bumping the shared expiry is the
	// only mechanical action available at the cliff, and a bump that leaves
	// the record untouched extends every entry nobody re-read.
	live := map[string]string{"debt scope=untagged resource=tmux": "2027-02-09"}
	err := Check(CatalogResourceCensus, mustParse(t, document(retainRow)), live)
	if err == nil {
		t.Fatal("Check accepted a moved expiry with a stale decision")
	}
	if !strings.Contains(err.Error(), "2026-11-09") || !strings.Contains(err.Error(), "2027-02-09") {
		t.Fatalf("error names neither date: %v", err)
	}
}

func TestCheckFailsWhenALiveEntryHasNoDecision(t *testing.T) {
	t.Parallel()

	live := map[string]string{
		"debt scope=untagged resource=tmux":       "2026-11-09",
		"debt scope=untagged resource=net_listen": "2026-11-09",
	}
	err := Check(CatalogResourceCensus, mustParse(t, document(retainRow)), live)
	if err == nil {
		t.Fatal("Check accepted a catalog entry with no recorded decision")
	}
	if !strings.Contains(err.Error(), "net_listen") {
		t.Fatalf("error does not name the undecided entry: %v", err)
	}
}

func TestCheckFailsWhenARetainedEntryLeftTheCatalog(t *testing.T) {
	t.Parallel()

	// A retained row whose entry is gone is a record describing a catalog
	// that no longer exists; the entry was retired and nobody said why.
	err := Check(CatalogResourceCensus, mustParse(t, document(retainRow)), map[string]string{})
	if err == nil {
		t.Fatal("Check accepted a retained decision for an absent entry")
	}
	// Named rather than merely non-nil: with this guard removed the expiry
	// comparison below it still fires, because an absent entry reports the
	// empty string as its expiry. A test that only asked for an error passed
	// with the guard deleted -- measured in the gs-xujz mutation sweep.
	if !strings.Contains(err.Error(), "is no longer in the") {
		t.Fatalf("error is not the absent-entry finding: %v", err)
	}
}

func TestCheckFailsWhenARetiredEntryIsStillLive(t *testing.T) {
	t.Parallel()

	// Deleting the entry is what makes a retirement real. Without this the
	// record can claim a waiver was retired while the gate still enforces it.
	live := map[string]string{"runtime.builtin.gone internal/runtime/gone.New": "2026-11-09"}
	err := Check(CatalogProviderLedger, mustParse(t, document(retireRow)), live)
	if err == nil {
		t.Fatal("Check accepted a retired decision for a live entry")
	}
	// With this guard removed the entry simply reads as undecided, so the
	// missing-decision sweep reports it and the suite stays green over a
	// record that contradicts the catalog. Same survivor as above.
	if !strings.Contains(err.Error(), "is still in the") {
		t.Fatalf("error is not the live-retired finding: %v", err)
	}
}

func TestCheckFailsOnACatalogNameNeitherGuardOwns(t *testing.T) {
	t.Parallel()

	// A typo here is invisible to set equality: the row leaves both catalogs'
	// key sets, so each guard reports it as an entry that was never reviewed
	// and nothing reports the row that was meant to cover it.
	stray := "| resourcecencus | `debt scope=untagged resource=tmux` | 2026-11-09 | retain | 2026-09-11 | misspelled catalog |"
	live := map[string]string{"debt scope=untagged resource=tmux": "2026-11-09"}
	err := Check(CatalogResourceCensus, mustParse(t, document(retainRow, stray)), live)
	if err == nil {
		t.Fatal("Check accepted a row naming an unknown catalog")
	}
	if !strings.Contains(err.Error(), "resourcecencus") {
		t.Fatalf("error does not name the unknown catalog: %v", err)
	}
}

func TestCheckFailsOnAnUnknownDecisionWord(t *testing.T) {
	t.Parallel()

	// "deferred" reads as a decision to a human and as nothing to the gate,
	// so an entry recorded that way would satisfy neither arm silently.
	row := "| resourcecensus | `debt scope=untagged resource=tmux` | 2026-11-09 | deferred | 2026-09-11 | not a verdict |"
	live := map[string]string{"debt scope=untagged resource=tmux": "2026-11-09"}
	err := Check(CatalogResourceCensus, mustParse(t, document(row)), live)
	if err == nil {
		t.Fatal("Check accepted an unknown decision word")
	}
	// An unrecognized word sets neither arm, so the entry also reports as
	// undecided. Without naming the finding, deleting the default arm left
	// this test green.
	if !strings.Contains(err.Error(), "unknown decision") {
		t.Fatalf("error is not the unknown-decision finding: %v", err)
	}
}

func TestCheckRefusesACatalogItDoesNotKnow(t *testing.T) {
	t.Parallel()

	if err := Check("nosuchcatalog", nil, nil); err == nil {
		t.Fatal("Check accepted an unknown catalog argument")
	}
}

func TestCheckIgnoresTheOtherCatalogsRows(t *testing.T) {
	t.Parallel()

	// Each catalog guard sees the whole record. Without this filter every
	// row of the sibling catalog would report as an undecided entry.
	live := map[string]string{"debt scope=untagged resource=tmux": "2026-11-09"}
	other := "| providerledger | `runtime.builtin.k8s internal/runtime/k8s.NewSeamBacked` | 2026-11-09 | retain | 2026-09-11 | needs a cluster |"
	if err := Check(CatalogResourceCensus, mustParse(t, document(retainRow, other)), live); err != nil {
		t.Fatalf("Check: %v", err)
	}
}
