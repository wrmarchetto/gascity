package main

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/suspensionstate"
	"github.com/gastownhall/gascity/internal/testpolicy/resourcecensus"
)

// censusOwnerLivenessCheck detects resource-census ledger rows
// (test/test-resources.toml) whose owner_bead no longer resolves in the
// scope's bead store. Detection only: it never repairs the ledger.
//
// An owner_bead whose id prefix is not the one the scope mints is reported
// as foreign rather than dangling. A store cannot resolve an id from
// another tracker's namespace at all, so "not found" there is not evidence
// of a deleted bead -- it is the only answer that reference could ever
// produce. Reporting it as dangling made the check warn permanently for a
// deployment whose ledger it inherited from another tracker, and a check
// that always warns is read as background noise, which is what the
// dangling finding is for.
//
// The tempting simpler fix -- dropping the rows so the check goes quiet --
// is wrong: the rows are live policy. Every ledger row is a resource
// budget the census gate enforces and TESTING.md documents, so deleting
// one erases debt the repository still owes while the owner_bead is only
// its provenance.
//
// The classification is deliberately conservative. It fires only when the
// scope's own prefix is known AND the id carries a different one; an
// unknown scope prefix keeps the dangling verdict, because guessing in
// that direction silences the real defect this check exists to catch.
type censusOwnerLivenessCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
}

// newCensusOwnerLivenessCheck constructs a censusOwnerLivenessCheck.
func newCensusOwnerLivenessCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *censusOwnerLivenessCheck {
	return &censusOwnerLivenessCheck{cfg: cfg, cityPath: cityPath, newStore: newStore}
}

// Name returns the check's identifier.
func (c *censusOwnerLivenessCheck) Name() string { return "census-owner-liveness" }

// CanFix reports that this check is detection-only.
func (c *censusOwnerLivenessCheck) CanFix() bool { return false }

// Fix is a no-op; this check never auto-repairs findings.
func (c *censusOwnerLivenessCheck) Fix(_ *doctor.CheckContext) error { return nil }

// Run scans the city and each non-suspended, path-bearing rig's
// resource-census ledger for owner_bead references that no longer resolve
// in that scope's bead store.
func (c *censusOwnerLivenessCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	var findings []string
	var skipped []string
	var foreign []string

	c.scanScope(&findings, &skipped, &foreign, "city", c.cityPath)
	if c.cfg != nil {
		suspState, _ := loadSuspensionState(fsys.OSFS{}, c.cityPath)
		for _, rig := range c.cfg.Rigs {
			if suspensionstate.EffectiveRigSuspended(suspState, rig.Name, rig.EffectiveSuspendedOnStart()) || strings.TrimSpace(rig.Path) == "" {
				continue
			}
			c.scanScope(&findings, &skipped, &foreign, "rig "+rig.Name, rig.Path)
		}
	}

	if len(findings) == 0 && len(skipped) == 0 && len(foreign) == 0 {
		return okCheck(c.Name(), "no dangling owner_bead references found in resource-census ledgers")
	}

	details := append([]string{}, findings...)
	details = append(details, skipped...)
	details = append(details, foreign...)
	sort.Strings(details)

	foreignNote := ""
	if len(foreign) > 0 {
		foreignNote = fmt.Sprintf(" (%d owner_bead reference(s) owned outside this scope's id namespace, which resolve nowhere here by construction)", len(foreign))
	}

	if len(findings) == 0 && len(skipped) == 0 {
		// Foreign references alone are a stable, correct state. Reporting
		// them as OK-with-details keeps them auditable in `gc doctor --json`
		// without spending the warning channel on a condition nobody can
		// act on.
		result := okCheck(c.Name(), "no dangling owner_bead references found in resource-census ledgers"+foreignNote)
		result.Details = details
		return result
	}

	if len(findings) == 0 {
		return warnCheck(c.Name(),
			fmt.Sprintf("census-owner-liveness check skipped %d scope(s)%s", len(skipped), foreignNote),
			"fix bead store access, then rerun gc doctor",
			details)
	}

	message := fmt.Sprintf("found %d dangling owner_bead reference(s) in resource-census ledgers", len(findings))
	if len(skipped) > 0 {
		message = fmt.Sprintf("%s (and skipped %d scope(s))", message, len(skipped))
	}
	message += foreignNote
	fixHint := "re-point the ledger row's owner_bead through council review (see TESTING.md), or fix bead store access and rerun gc doctor"
	return warnCheck(c.Name(), message, fixHint, details)
}

// scanScope loads the resource-census ledger at path, if any, and checks
// each unique owner_bead it references against the scope's bead store.
// A missing ledger file is expected for almost every scope and is skipped
// silently; any other load error, store-open error, or non-not-found Get
// error is recorded as a skip with a reason rather than treated as a
// dangling finding. A not-found id carrying a prefix this scope does not
// mint lands in foreign rather than findings.
//
// The prefix comes from issuePrefixForScope, the same resolver the store
// itself is built with, rather than from the store's IDPrefix(): not every
// provider surfaces that accessor, and a check that silently loses its
// classification on one backend would report the same ledger two different
// ways depending on how the store happened to open.
func (c *censusOwnerLivenessCheck) scanScope(findings, skipped, foreign *[]string, label, path string) {
	if c.newStore == nil || strings.TrimSpace(path) == "" {
		return
	}

	ledgerPath := filepath.Join(path, "test", "test-resources.toml")
	ledger, err := resourcecensus.LoadLedger(ledgerPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		*skipped = append(*skipped, fmt.Sprintf("%s skipped: loading resource-census ledger: %v", label, err))
		return
	}

	rows := collectCensusOwnerBeadRows(ledger)
	if len(rows) == 0 {
		return
	}

	store, err := c.newStore(path)
	if err != nil {
		*skipped = append(*skipped, fmt.Sprintf("%s skipped: opening bead store: %v", label, err))
		return
	}

	scopePrefix := strings.TrimSpace(issuePrefixForScope(path, c.cityPath, c.cfg))

	ids := make([]string, 0, len(rows))
	for id := range rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		_, err := store.Get(id)
		switch {
		case err == nil:
			continue
		case errors.Is(err, beads.ErrNotFound):
			// Classified only after the read, never instead of it: an id
			// the store does resolve is alive whatever its prefix says.
			if idPrefix := beadIDPrefix(id); scopePrefix != "" && idPrefix != "" && idPrefix != scopePrefix {
				*foreign = append(*foreign, fmt.Sprintf("%s: foreign owner_bead=%s (prefix %q is not this scope's %q) rows=[%s]",
					label, id, idPrefix, scopePrefix, strings.Join(rows[id], "; ")))
				continue
			}
			*findings = append(*findings, fmt.Sprintf("%s: dangling owner_bead=%s rows=[%s]", label, id, strings.Join(rows[id], "; ")))
		default:
			*skipped = append(*skipped, fmt.Sprintf("%s skipped: checking owner_bead %s: %v", label, id, err))
		}
	}
}

// beadIDPrefix returns the namespace segment of a bead id -- the text
// before the first "-" -- or "" when the id carries no separator. Hierarchy
// suffixes are dotted, not hyphenated (ga-80po0c.2.1), so the first "-" is
// the whole namespace boundary.
func beadIDPrefix(id string) string {
	prefix, _, found := strings.Cut(strings.TrimSpace(id), "-")
	if !found {
		return ""
	}
	return prefix
}

// collectCensusOwnerBeadRows collects, per unique owner_bead, a
// human-readable descriptor of every ledger row that references it across
// all four row categories.
func collectCensusOwnerBeadRows(ledger resourcecensus.Ledger) map[string][]string {
	rows := map[string][]string{}

	addBaseline := func(category string, list []resourcecensus.Baseline) {
		for _, row := range list {
			id := strings.TrimSpace(row.OwnerBead)
			if id == "" {
				continue
			}
			desc := fmt.Sprintf("%s: scope=%s resource=%s", category, row.Scope, row.Resource)
			rows[id] = append(rows[id], desc)
		}
	}
	addBaseline("audit_baseline", ledger.AuditBaseline)
	addBaseline("debt", ledger.Debt)
	addBaseline("small_debt", ledger.SmallDebt)

	for _, row := range ledger.Medium {
		id := strings.TrimSpace(row.OwnerBead)
		if id == "" {
			continue
		}
		desc := fmt.Sprintf("medium: package_dir=%s package_name=%s owner=%s", row.PackageDir, row.PackageName, row.Owner)
		rows[id] = append(rows[id], desc)
	}

	return rows
}
