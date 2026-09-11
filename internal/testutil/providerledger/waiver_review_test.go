// Binds this ledger's waived contract claims to their recorded per-entry
// decisions.
//
// Scope is the waiver set alone: proved and not-applicable claims carry no
// expiry, so nothing about them can go stale on a date. The parsing and the
// reconciliation itself are tested against synthetic documents in
// internal/testpolicy/waiverreview -- this file only builds the live key set
// and hands it over, because the part able to be wrong here is the key
// spelling, not the comparison.
//
// The key is the entry id plus the constructor rather than either alone. Two
// entries waive the same t3bridge constructor (runtime.builtin.t3bridge and
// the legacy gc-session-t3 branch of runtime.builtin.exec), so a key on the
// constructor would collapse two independent decisions into one; and an entry
// may hold a waived claim beside a proved one, so a key on the entry would
// claim a decision covering a claim that needs none.
//
// Run: go test ./internal/testutil/providerledger/ -run TestEveryWaiverHasARecordedDecision
package providerledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/testpolicy/waiverreview"
)

func TestEveryWaiverHasARecordedDecision(t *testing.T) {
	root := repoRoot(t)
	document, err := os.ReadFile(filepath.Join(root, waiverreview.RecordPath))
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := waiverreview.Parse(string(document))
	if err != nil {
		t.Fatalf("parse %s: %v", waiverreview.RecordPath, err)
	}

	live := map[string]string{}
	for _, entry := range Catalog() {
		for _, claim := range entry.Claims {
			if claim.Disposition != DispositionWaived || claim.Waiver == nil {
				continue
			}
			live[entry.ID+" "+renderSymbolRef(claim.Constructor)] = claim.Waiver.Expires.Format("2006-01-02")
		}
	}
	if len(live) == 0 {
		t.Fatal("the ledger declares no waivers; delete this guard and its rows rather than leaving it passing vacuously")
	}
	if err := waiverreview.Check(waiverreview.CatalogProviderLedger, decisions, live); err != nil {
		t.Fatalf("waiver decision record is out of step with the ledger:\n%v", err)
	}
}
