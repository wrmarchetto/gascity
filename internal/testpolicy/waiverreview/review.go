// Package waiverreview binds the test-policy expiry catalogs to the per-entry
// decision record that justifies their shared expiry dates.
//
// The package exists because the decision, not the date, is the scarce thing.
// Two catalogs -- internal/testpolicy/resourcecensus and
// internal/testutil/providerledger -- park a whole set of entries behind one
// shared expiry, and on that date every entry fails at once and the push gate
// closes for everybody. The cliff is deliberate: it forces the set to be
// reviewed in one sitting rather than one entry at a time. What it does not
// force is that the review happen at all, and the only mechanical action
// available at the cliff -- moving the date -- extends entries nobody looked
// at while looking exactly like a review that concluded "retain".
//
// So the expiry a decision was taken against is recorded beside the decision,
// and Check refuses a catalog whose live expiry has moved away from the one
// its decision row cites. A bump without a re-recorded decision is then a red
// gate on the next run rather than a silent extension. Measured cost of not
// having this: at the 2026-08-12 providerledger cliff the same review was
// performed and authored twice independently to an identical patch-id, and a
// third bead asked for it again two days later (city beads ci-k5ey0,
// ci-qcw5p).
//
// The record itself is Markdown rather than TOML on purpose. Its readers are
// the next reviewer and whoever is staring at a closed push gate, not a
// program -- Check needs five columns of it and the sixth, the basis prose, is
// the entire point of the document and would be unreadable as a TOML string.
//
// Editing constraints:
//
//   - Decision text is a closed vocabulary (Retain, Retire). Widening it
//     widens what Check accepts, so add a case to Check at the same time.
//   - The header row is compared literally. Reordering the columns must fail
//     rather than silently re-interpret cells.
//   - This package reads no repository root and runs no git. Adding either
//     puts it in scripts/push-gate-always-run.manifest under that file's
//     regeneration rule.
//
// Invariants are verified by review_test.go here, and against the live
// catalogs by waiver_review_test.go in each of the two catalog packages.
package waiverreview

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// Begin opens the machine-checked decision table.
	Begin = "<!-- BEGIN TEST POLICY WAIVER DECISIONS -->"
	// End closes the machine-checked decision table.
	End = "<!-- END TEST POLICY WAIVER DECISIONS -->"

	// CatalogResourceCensus names the resource-census catalog's rows.
	CatalogResourceCensus = "resourcecensus"
	// CatalogProviderLedger names the runtime-provider ledger's waivers.
	CatalogProviderLedger = "providerledger"

	// Retain records an entry whose gap survived the review.
	Retain = "retain"
	// Retire records an entry the review removed from its catalog.
	Retire = "retire"

	// RecordPath is the decision record, repository-relative. It lives with
	// the contributor docs rather than beside either catalog because one
	// document covers both, and a copy under either package would become the
	// authority for that catalog alone and drift from the other.
	RecordPath = "engdocs/contributors/test-policy-waiver-review.md"

	header     = "| Catalog | Entry | Expiry | Decision | Reviewed | Basis |"
	separator  = "| --- | --- | --- | --- | --- | --- |"
	dateLayout = "2006-01-02"
)

// KnownCatalogs is the closed set of catalog names a decision row may name.
//
// Closed rather than open because an unrecognized name is the one failure
// neither catalog's own guard can see: each checks only the rows addressed to
// it, so a typo in the catalog cell drops a row out of both key sets and reads
// as a decision that was recorded.
var KnownCatalogs = []string{CatalogResourceCensus, CatalogProviderLedger}

// Decision is one recorded per-entry verdict.
type Decision struct {
	Catalog  string // one of KnownCatalogs
	Key      string // catalog-scoped entry identity, verbatim
	Expiry   string // the entry's expiry the verdict was taken against
	Decision string // Retain or Retire
	Reviewed string // the day the verdict was taken
	Basis    string // why, and for Retain what would close the gap
}

// Parse reads every decision row from the document's checked block.
//
// Every line inside the markers must parse. A permissive parser that skipped
// what it did not recognize would turn a malformed row into a missing entry,
// and a missing entry is exactly what the catalog guards read as "this waiver
// was never reviewed" -- a confusing failure a long way from its cause.
func Parse(document string) ([]Decision, error) {
	body, err := block(document)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(body, "\n")
	var rows []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			rows = append(rows, strings.TrimSpace(line))
		}
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("decision block needs a header and separator row, found %d line(s)", len(rows))
	}
	if rows[0] != header {
		return nil, fmt.Errorf("decision header is %q, want %q", rows[0], header)
	}
	if rows[1] != separator {
		return nil, fmt.Errorf("decision separator is %q, want %q", rows[1], separator)
	}

	seen := make(map[string]bool, len(rows))
	decisions := make([]Decision, 0, len(rows)-2)
	for _, row := range rows[2:] {
		decision, err := parseRow(row)
		if err != nil {
			return nil, err
		}
		identity := decision.Catalog + "\x00" + decision.Key
		if seen[identity] {
			return nil, fmt.Errorf("decision for %s entry %q is recorded twice", decision.Catalog, decision.Key)
		}
		seen[identity] = true
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

// Check reconciles one catalog's live entries against the recorded decisions.
//
// live maps each entry's key to the expiry it currently carries. Retained
// entries must match it exactly in both directions, and each retained row's
// cited expiry must equal the live one -- that equality is what makes moving a
// shared date without re-recording the decisions a failing gate rather than a
// silent extension of entries nobody read.
//
// Retired rows are held to the opposite requirement: still present in the
// record as history, absent from the catalog. Deleting the entry is what makes
// a retirement real, so a row claiming one while its entry is still live is a
// record that disagrees with the code.
func Check(catalog string, decisions []Decision, live map[string]string) error {
	if !known(catalog) {
		return fmt.Errorf("catalog %q is not one of %s", catalog, strings.Join(KnownCatalogs, ", "))
	}
	var problems []string
	retained := make(map[string]bool, len(live))
	for _, decision := range decisions {
		if !known(decision.Catalog) {
			problems = append(problems, fmt.Sprintf("entry %q names unknown catalog %q; want one of %s",
				decision.Key, decision.Catalog, strings.Join(KnownCatalogs, ", ")))
			continue
		}
		if decision.Catalog != catalog {
			continue
		}
		expiry, present := live[decision.Key]
		switch decision.Decision {
		case Retain:
			retained[decision.Key] = true
			if !present {
				problems = append(problems, fmt.Sprintf("retained entry %q is no longer in the %s catalog; record it as %s or restore the entry",
					decision.Key, catalog, Retire))
				continue
			}
			if decision.Expiry != expiry {
				problems = append(problems, fmt.Sprintf("entry %q was reviewed against expiry %s but now carries %s; re-review it and record the decision against the new date",
					decision.Key, decision.Expiry, expiry))
			}
		case Retire:
			if present {
				problems = append(problems, fmt.Sprintf("entry %q is recorded as %s but is still in the %s catalog; delete the entry or record it as %s",
					decision.Key, Retire, catalog, Retain))
			}
		default:
			problems = append(problems, fmt.Sprintf("entry %q has unknown decision %q; want %s or %s",
				decision.Key, decision.Decision, Retain, Retire))
		}
	}
	var missing []string
	for key := range live {
		if !retained[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	for _, key := range missing {
		problems = append(problems, fmt.Sprintf("%s entry %q carries an expiry with no recorded decision; review it and add a row to the decision record",
			catalog, key))
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s", strings.Join(problems, "\n"))
}

func known(catalog string) bool {
	for _, candidate := range KnownCatalogs {
		if candidate == catalog {
			return true
		}
	}
	return false
}

func block(document string) (string, error) {
	if strings.Count(document, Begin) != 1 || strings.Count(document, End) != 1 {
		return "", fmt.Errorf("document needs exactly one %s and one %s", Begin, End)
	}
	start := strings.Index(document, Begin)
	stop := strings.Index(document, End)
	if stop < start {
		return "", fmt.Errorf("%s appears before %s", End, Begin)
	}
	return document[start+len(Begin) : stop], nil
}

func parseRow(row string) (Decision, error) {
	if !strings.HasPrefix(row, "|") || !strings.HasSuffix(row, "|") {
		return Decision{}, fmt.Errorf("decision row %q is not a table row", row)
	}
	cells := strings.Split(strings.Trim(row, "|"), "|")
	if len(cells) != 6 {
		return Decision{}, fmt.Errorf("decision row %q has %d cells, want 6", row, len(cells))
	}
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	key := cells[1]
	if !strings.HasPrefix(key, "`") || !strings.HasSuffix(key, "`") || len(key) < 3 {
		return Decision{}, fmt.Errorf("decision row %q must quote its entry in backticks", row)
	}
	decision := Decision{
		Catalog:  cells[0],
		Key:      strings.Trim(key, "`"),
		Expiry:   cells[2],
		Decision: cells[3],
		Reviewed: cells[4],
		Basis:    cells[5],
	}
	for name, value := range map[string]string{"expiry": decision.Expiry, "reviewed": decision.Reviewed} {
		if _, err := time.Parse(dateLayout, value); err != nil {
			return Decision{}, fmt.Errorf("decision row %q has a %s that is not YYYY-MM-DD: %q", row, name, value)
		}
	}
	if strings.TrimSpace(decision.Basis) == "" {
		return Decision{}, fmt.Errorf("decision row %q has an empty basis", row)
	}
	return decision, nil
}
