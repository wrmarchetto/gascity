package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

func writeCensusLedger(t *testing.T, scopePath, toml string) {
	t.Helper()
	dir := filepath.Join(scopePath, "test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir test dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "test-resources.toml"), []byte(toml), 0o644); err != nil {
		t.Fatalf("write ledger: %v", err)
	}
}

// writeScopeIssuePrefix gives a scope directory the bd config that
// issuePrefixForScope reads, so the check can tell an id this scope could
// have minted from one it never could.
func writeScopeIssuePrefix(t *testing.T, scopePath, prefix string) {
	t.Helper()
	dir := filepath.Join(scopePath, ".beads")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("issue_prefix: "+prefix+"\n"), 0o644); err != nil {
		t.Fatalf("write bd config: %v", err)
	}
}

func TestCensusOwnerLivenessCheckSkipsScopeWithoutLedgerFile(t *testing.T) {
	cityDir := t.TempDir()
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		t.Fatal("newStore should not be called when no ledger file exists")
		return nil, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	if len(result.Details) != 0 {
		t.Fatalf("details = %v, want empty", result.Details)
	}
}

func TestCensusOwnerLivenessCheckOKWhenAllOwnerBeadsAlive(t *testing.T) {
	cityDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-alive-1"

[[debt]]
scope = "untagged"
resource = "fixed_sleep"
owner_bead = "ga-alive-2"
`)
	store := beads.NewMemStoreFrom(0, []beads.Bead{
		{ID: "ga-alive-1", Title: "alive one"},
		{ID: "ga-alive-2", Title: "alive two"},
	}, nil)
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK; message=%q details=%v", result.Status, result.Message, result.Details)
	}
}

func TestCensusOwnerLivenessCheckWarnsOnDanglingOwnerBead(t *testing.T) {
	cityDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-missing-1"
`)
	store := beads.NewMemStoreFrom(0, nil, nil)
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "dangling owner_bead=ga-missing-1") {
		t.Fatalf("details missing dangling owner_bead marker:\n%s", details)
	}
	if !strings.Contains(result.FixHint, "council review") {
		t.Fatalf("fix hint = %q, want mention of council review", result.FixHint)
	}
}

func TestCensusOwnerLivenessCheckDedupesRepeatedOwnerBeadAcrossRows(t *testing.T) {
	cityDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-missing-shared"

[[debt]]
scope = "untagged"
resource = "subprocess"
owner_bead = "ga-missing-shared"

[[medium]]
package_dir = "cmd/gc"
package_name = "main"
owner = "TestFoo"
resources = ["subprocess"]
owner_bead = "ga-missing-shared"

[[small_debt]]
scope = "all"
resource = "fixed_sleep"
owner_bead = "ga-missing-shared"
`)
	inner := beads.NewMemStoreFrom(0, nil, nil)
	spy := &censusGetCountingStore{Store: inner, counts: map[string]int{}}
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return spy, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	if got := spy.counts["ga-missing-shared"]; got != 1 {
		t.Fatalf("Get(ga-missing-shared) called %d times, want 1 (dedup across rows)", got)
	}
	details := strings.Join(result.Details, "\n")
	for _, want := range []string{"audit_baseline:", "debt:", "medium:", "small_debt:"} {
		if !strings.Contains(details, want) {
			t.Fatalf("details missing row category %q:\n%s", want, details)
		}
	}
	danglingCount := strings.Count(details, "dangling owner_bead=ga-missing-shared")
	if danglingCount != 1 {
		t.Fatalf("dangling owner_bead=ga-missing-shared appeared %d times, want exactly 1 finding line:\n%s", danglingCount, details)
	}
}

func TestCensusOwnerLivenessCheckSkipsOnStoreOpenFailure(t *testing.T) {
	cityDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-whatever"
`)
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return nil, errors.New("city offline")
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "skipped: opening bead store: city offline") {
		t.Fatalf("details missing store-open skip marker:\n%s", details)
	}
	if strings.Contains(details, "dangling") {
		t.Fatalf("store-open failure must not be reported as dangling:\n%s", details)
	}
	if result.FixHint != "fix bead store access, then rerun gc doctor" {
		t.Fatalf("fix hint = %q, want store-access hint", result.FixHint)
	}
}

func TestCensusOwnerLivenessCheckSkipsOnNonNotFoundGetError(t *testing.T) {
	cityDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-transient"
`)
	store := censusGetErrorStore{err: errors.New("connection reset")}
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "skipped: checking owner_bead ga-transient: connection reset") {
		t.Fatalf("details missing get-error skip marker:\n%s", details)
	}
	if strings.Contains(details, "dangling") {
		t.Fatalf("non-not-found Get error must not be reported as dangling:\n%s", details)
	}
}

func TestCensusOwnerLivenessCheckScansCityAndRigs(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-city-missing"
`)
	// The rig's owner_bead sits in the rig's own namespace so this test keeps
	// pinning scope enumeration rather than the foreign-prefix classification
	// that TestCensusOwnerLivenessCheckClassifiesPerScopePrefix owns.
	writeCensusLedger(t, rigDir, `
version = 1

[[debt]]
scope = "untagged"
resource = "fixed_sleep"
owner_bead = "rp-rig-missing"
`)
	cfg := &config.City{
		Rigs: []config.Rig{
			{Name: "repo", Prefix: "rp", Path: rigDir},
			{Name: "ghost", Path: ""},
		},
	}
	store := beads.NewMemStoreFrom(0, nil, nil)
	result := newCensusOwnerLivenessCheck(cfg, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	for _, want := range []string{
		"city: dangling owner_bead=ga-city-missing",
		"rig repo: dangling owner_bead=rp-rig-missing",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("details missing %q:\n%s", want, details)
		}
	}
}

// TestCensusOwnerLivenessCheckReportsForeignOwnerBeadWithoutWarning pins the
// decision recorded on censusOwnerLivenessCheck: an owner_bead minted under
// another tracker's id prefix is unresolvable by construction, so it is a
// state of its own and not a defect. The ledger rows it anchors are live debt
// policy either way, which is why the ids stay in the ledger and the check
// changes instead.
func TestCensusOwnerLivenessCheckReportsForeignOwnerBeadWithoutWarning(t *testing.T) {
	cityDir := t.TempDir()
	writeScopeIssuePrefix(t, cityDir, "gs")
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-80po0c.2"
`)
	store := beads.NewMemStoreFrom(0, nil, nil)
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusOK {
		t.Fatalf("status = %v, want StatusOK; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, `city: foreign owner_bead=ga-80po0c.2 (prefix "ga" is not this scope's "gs")`) {
		t.Fatalf("details missing foreign owner_bead marker:\n%s", details)
	}
	if !strings.Contains(details, "audit_baseline: scope=all resource=subprocess") {
		t.Fatalf("foreign finding must still name its rows:\n%s", details)
	}
	// scripts/check-census-owner-liveness.sh files an alert bead for every
	// detail line matching "dangling owner_bead=". A foreign reference is a
	// standing, correct state, so it must not carry that substring.
	if strings.Contains(details, "dangling owner_bead=") {
		t.Fatalf("foreign reference must not read as dangling to the alert patrol:\n%s", details)
	}
	if !strings.Contains(result.Message, "1 owner_bead reference(s) owned outside this scope") {
		t.Fatalf("message = %q, want the foreign count stated", result.Message)
	}
}

func TestCensusOwnerLivenessCheckStillWarnsOnDanglingOwnScopeOwnerBead(t *testing.T) {
	cityDir := t.TempDir()
	writeScopeIssuePrefix(t, cityDir, "gs")
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-80po0c.2"

[[debt]]
scope = "untagged"
resource = "fixed_sleep"
owner_bead = "gs-deleted"
`)
	store := beads.NewMemStoreFrom(0, nil, nil)
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "city: dangling owner_bead=gs-deleted") {
		t.Fatalf("an id this scope mints and cannot resolve is still dangling:\n%s", details)
	}
	if !strings.Contains(details, "city: foreign owner_bead=ga-80po0c.2") {
		t.Fatalf("foreign reference must still be reported alongside the dangling one:\n%s", details)
	}
	if !strings.Contains(result.Message, "found 1 dangling owner_bead reference(s)") {
		t.Fatalf("message = %q, want only the dangling id counted as a finding", result.Message)
	}
	if !strings.Contains(result.Message, "1 owner_bead reference(s) owned outside this scope") {
		t.Fatalf("message = %q, want the foreign count stated too", result.Message)
	}
}

// TestCensusOwnerLivenessCheckTreatsUnknownScopePrefixAsDangling pins the
// conservative default. Without a resolvable scope prefix there is nothing to
// compare an id against, so classifying it as foreign would be a guess -- and
// a guess in that direction silences a real dangling reference.
func TestCensusOwnerLivenessCheckTreatsUnknownScopePrefixAsDangling(t *testing.T) {
	cityDir := t.TempDir()
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "ga-80po0c.2"
`)
	store := beads.NewMemStoreFrom(0, nil, nil)
	result := newCensusOwnerLivenessCheck(nil, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	if !strings.Contains(details, "dangling owner_bead=ga-80po0c.2") {
		t.Fatalf("unknown scope prefix must not classify anything as foreign:\n%s", details)
	}
}

func TestCensusOwnerLivenessCheckClassifiesPerScopePrefix(t *testing.T) {
	cityDir := t.TempDir()
	rigDir := t.TempDir()
	writeScopeIssuePrefix(t, cityDir, "ci")
	writeScopeIssuePrefix(t, rigDir, "gs")
	writeCensusLedger(t, cityDir, `
version = 1

[[audit_baseline]]
scope = "all"
resource = "subprocess"
owner_bead = "gs-rig-owned"
`)
	writeCensusLedger(t, rigDir, `
version = 1

[[debt]]
scope = "untagged"
resource = "fixed_sleep"
owner_bead = "gs-rig-owned"
`)
	cfg := &config.City{Rigs: []config.Rig{{Name: "repo", Path: rigDir}}}
	store := beads.NewMemStoreFrom(0, nil, nil)
	result := newCensusOwnerLivenessCheck(cfg, cityDir, func(string) (beads.Store, error) {
		return store, nil
	}).Run(&doctor.CheckContext{})

	if result.Status != doctor.StatusWarning {
		t.Fatalf("status = %v, want StatusWarning; message=%q details=%v", result.Status, result.Message, result.Details)
	}
	details := strings.Join(result.Details, "\n")
	// The same id is foreign to the city and native to the rig, so the
	// verdict has to be per scope rather than per id.
	if !strings.Contains(details, "city: foreign owner_bead=gs-rig-owned") {
		t.Fatalf("city scope must read gs- as foreign:\n%s", details)
	}
	if !strings.Contains(details, "rig repo: dangling owner_bead=gs-rig-owned") {
		t.Fatalf("rig scope mints gs- and must still report it dangling:\n%s", details)
	}
}

type censusGetCountingStore struct {
	beads.Store
	counts map[string]int
}

func (s *censusGetCountingStore) Get(id string) (beads.Bead, error) {
	s.counts[id]++
	return s.Store.Get(id)
}

type censusGetErrorStore struct {
	beads.Store
	err error
}

func (s censusGetErrorStore) Get(string) (beads.Bead, error) {
	return beads.Bead{}, s.err
}
