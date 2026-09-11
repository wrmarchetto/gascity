// Binds every dated bootstrap-policy row to its recorded per-entry decision.
//
// The key set is built from bootstrapPolicy rather than from
// test/test-resources.toml on purpose. The two are held equal by
// TestRepositoryLedgerMatchesCensusAndDocumentation, so either would do today
// -- but bootstrapPolicy is what doctor/test-policy-expiry-horizon in the city
// reads to compute the cliff, and a guard keyed on the other file would go
// green against the wrong side of a drift it is not the one detecting.
//
// Key spellings match the prefixes the validators already use in their problem
// messages, so a failure here and a failure from Validate name the same row
// the same way. The parsing and the reconciliation are tested against
// synthetic documents in internal/testpolicy/waiverreview.
//
// Run: go test ./internal/testpolicy/resourcecensus/ -run TestEveryDatedPolicyRowHasARecordedDecision
package resourcecensus

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/testpolicy/waiverreview"
)

func TestEveryDatedPolicyRowHasARecordedDecision(t *testing.T) {
	root := repositoryRoot(t)
	document, err := os.ReadFile(filepath.Join(root, waiverreview.RecordPath))
	if err != nil {
		t.Fatal(err)
	}
	decisions, err := waiverreview.Parse(string(document))
	if err != nil {
		t.Fatalf("parse %s: %v", waiverreview.RecordPath, err)
	}

	live := map[string]string{}
	add := func(key, expiry string) {
		if _, duplicate := live[key]; duplicate {
			t.Fatalf("two bootstrap policy rows share the key %q", key)
		}
		live[key] = expiry
	}
	for kind, rows := range map[string][]Baseline{
		"audit_baseline": bootstrapPolicy.AuditBaseline,
		"small_debt":     bootstrapPolicy.SmallDebt,
		"debt":           bootstrapPolicy.Debt,
	} {
		for _, row := range rows {
			add(fmt.Sprintf("%s scope=%s resource=%s", kind, row.Scope, row.Resource), row.Expires)
		}
	}
	for _, row := range bootstrapPolicy.Medium {
		add(fmt.Sprintf("medium package_dir=%s package_name=%s owner=%s", row.PackageDir, row.PackageName, row.Owner), row.Expires)
	}
	if len(live) == 0 {
		t.Fatal("the bootstrap policy declares no dated rows; delete this guard and its rows rather than leaving it passing vacuously")
	}
	if err := waiverreview.Check(waiverreview.CatalogResourceCensus, decisions, live); err != nil {
		t.Fatalf("waiver decision record is out of step with the bootstrap policy:\n%v", err)
	}
}
