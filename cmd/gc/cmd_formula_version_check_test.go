package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/formula"
)

// versionCheckFormulaName is the formula name every version-check test
// uses. Inlined as a constant to avoid an unparam lint on the helper:
// no test in this file needs a different name today.
const versionCheckFormulaName = "deploy"

const versionCheckFormulaBody = `formula = "deploy"
description = "Deploy flow"

[[steps]]
id = "build"
title = "Build"

[[steps]]
id = "ship"
title = "Ship"
needs = ["build"]
`

// writeVersionCheckCity sets up a minimal city with one formula on disk
// and returns the city dir + the on-disk content hash of the formula
// (so the test can stamp matching or deliberately-mismatching metadata
// onto the bead it later creates).
//
// Mirrors writeTutorialFormulaCity but additionally exposes the recipe
// hash because the version-check command's whole job is comparing
// bead-metadata-recorded hash to current-disk hash. The formula name
// and body are constants because every version-check test uses the
// same fixture; vary the bead-side data instead.
func writeVersionCheckCity(t *testing.T) (cityDir, diskHash string) {
	t.Helper()

	t.Setenv("GC_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("GC_SESSION", "fake")
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_DOLT", "skip")

	cityDir = t.TempDir()
	writeFile := func(rel, body string) {
		t.Helper()
		path := filepath.Join(cityDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	writeFile("city.toml", withBuiltinProviderAliasesTOMLForTest("[workspace]\nname = \"my-city\"\nprovider = \"claude\"\n", "claude"))
	writeFile("formulas/"+versionCheckFormulaName+".toml", versionCheckFormulaBody)

	recipe, err := formula.Compile(context.Background(), versionCheckFormulaName, []string{filepath.Join(cityDir, "formulas")}, nil)
	if err != nil {
		t.Fatalf("formula.Compile(%s): %v", versionCheckFormulaName, err)
	}
	if recipe.ContentHash == "" {
		t.Fatalf("formula.Compile(%s).ContentHash is empty; the version-check command relies on it being populated", versionCheckFormulaName)
	}
	return cityDir, recipe.ContentHash
}

// createVersionCheckBead opens the city's bead store and creates a
// molecule-like bead whose Ref points at formulaName and whose
// gc.formula_hash metadata is `hash`. Returns the created bead ID.
func createVersionCheckBead(t *testing.T, cityDir, formulaName, hash string) string {
	t.Helper()

	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	bead := beads.Bead{
		Title:  "version-check fixture",
		Type:   "molecule",
		Status: "open",
		Ref:    formulaName,
	}
	created, err := store.Create(bead)
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if hash != "" {
		if err := store.SetMetadata(created.ID, "gc.formula_hash", hash); err != nil {
			t.Fatalf("SetMetadata(gc.formula_hash): %v", err)
		}
	}
	return created.ID
}

func createVersionCheckBeadNamedByMetadata(t *testing.T, cityDir, formulaName, hash string) string {
	t.Helper()

	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	created, err := store.Create(beads.Bead{
		Title:  "metadata-named version-check fixture",
		Type:   "molecule",
		Status: "open",
	})
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if err := store.SetMetadata(created.ID, beadmeta.FormulaNameMetadataKey, formulaName); err != nil {
		t.Fatalf("SetMetadata(%s): %v", beadmeta.FormulaNameMetadataKey, err)
	}
	if err := store.SetMetadata(created.ID, beadmeta.FormulaHashMetadataKey, hash); err != nil {
		t.Fatalf("SetMetadata(%s): %v", beadmeta.FormulaHashMetadataKey, err)
	}
	return created.ID
}

// TestFormulaVersionCheck_MatchExitsZero covers the happy path: a bead
// whose gc.formula_hash matches the current on-disk formula. The
// command must print the "matches" line and return without error so
// the process exits 0.
func TestFormulaVersionCheck_MatchExitsZero(t *testing.T) {
	cityDir, diskHash := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, "deploy", diskHash)
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{beadID})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute on match: err = %v; stderr=%s", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "matches on-disk version") {
		t.Errorf("stdout = %q, want 'matches on-disk version'", got)
	}
	if !strings.Contains(got, "deploy") {
		t.Errorf("stdout = %q, want it to name the formula", got)
	}
}

// TestFormulaVersionCheck_DivergeReturnsErrExit covers the unhappy
// path: bead hash differs from disk. The command must print the
// "DIVERGES" line and return errExit so the process exits non-zero.
func TestFormulaVersionCheck_DivergeReturnsErrExit(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, "deploy", "deadbeefdeadbeefdeadbeefdeadbeef")
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{beadID})
	err := cmd.Execute()
	if !errors.Is(err, errExit) {
		t.Fatalf("Execute on diverge: err = %v, want errExit; stderr=%s", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "DIVERGES") {
		t.Errorf("stdout = %q, want 'DIVERGES'", got)
	}
	if !strings.Contains(got, "bead hash") || !strings.Contains(got, "disk hash") {
		t.Errorf("stdout = %q, want both bead/disk hashes shown to operators", got)
	}
}

// TestFormulaVersionCheck_DivergeShowsFormulaPath asserts the optional
// "formula path" line renders on divergence when the recipe was loaded
// from a real path. Without this, the diverge-path conditional Fprintf
// for the formula source goes uncovered.
func TestFormulaVersionCheck_DivergeShowsFormulaPath(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, "deploy", "deadbeefdeadbeefdeadbeefdeadbeef")
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{beadID})
	if err := cmd.Execute(); !errors.Is(err, errExit) {
		t.Fatalf("Execute on diverge: err = %v, want errExit; stderr=%s", err, stderr.String())
	}
	got := stdout.String()
	if !strings.Contains(got, "formula path:") {
		t.Errorf("stdout = %q, want formula-path line when recipe has a source", got)
	}
}

// TestFormulaVersionCheck_JSONOutput covers the --json branch of the
// switch. Asserts the structured payload contains every field the JSON
// schema promises so automated consumers can rely on it.
func TestFormulaVersionCheck_JSONOutput(t *testing.T) {
	cityDir, diskHash := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, "deploy", diskHash)
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{beadID, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute on json match: err = %v; stderr=%s", err, stderr.String())
	}

	var got formulaVersionCheckResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v\nstdout=%q", err, stdout.String())
	}
	if got.BeadID != beadID {
		t.Errorf("BeadID = %q, want %q", got.BeadID, beadID)
	}
	if got.FormulaName != "deploy" {
		t.Errorf("FormulaName = %q, want %q", got.FormulaName, "deploy")
	}
	if got.BeadHash != diskHash || got.DiskHash != diskHash {
		t.Errorf("hashes BeadHash=%q DiskHash=%q, both should equal %q", got.BeadHash, got.DiskHash, diskHash)
	}
	if !got.Match {
		t.Errorf("Match = false, want true (hashes are equal)")
	}
	if got.FormulaPath == "" {
		t.Errorf("FormulaPath empty; should carry the on-disk formula source for operator diagnostics")
	}
}

// TestFormulaVersionCheck_MissingFormulaHashErrors asserts the targeted
// error message when a bead was created before hash tracking. This
// guards the user-facing diagnostic that tells the operator the bead
// pre-dates the feature, rather than a confusing "compile failed".
func TestFormulaVersionCheck_MissingFormulaHashErrors(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	// hash="" → don't set gc.formula_hash metadata.
	beadID := createVersionCheckBead(t, cityDir, "deploy", "")
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{beadID})
	if err := cmd.Execute(); !errors.Is(err, errExit) {
		t.Fatalf("Execute on missing hash: err = %v, want errExit; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "gc.formula_hash") {
		t.Errorf("stderr = %q, want it to mention gc.formula_hash", stderr.String())
	}
}

func TestFormulaVersionCheck_UnnamedFormulaErrors(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	created, err := store.Create(beads.Bead{
		Title:  "unnamed-formula fixture",
		Type:   "molecule",
		Status: "open",
		// Ref deliberately empty.
	})
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}
	if err := store.SetMetadata(created.ID, "gc.formula_hash", "abc123"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{created.ID})
	if err := cmd.Execute(); !errors.Is(err, errExit) {
		t.Fatalf("Execute on a bead naming no formula: err = %v, want errExit; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Ref") || !strings.Contains(stderr.String(), beadmeta.FormulaNameMetadataKey) {
		t.Errorf("stderr = %q, want both places a formula name could have been recorded", stderr.String())
	}
}

// TestFormulaVersionCheck_BeadNotFoundErrors covers the store.Get
// error path — bead ID doesn't exist. The diagnostic should name
// the bead ID and wrap the store error so the operator can correlate.
func TestFormulaVersionCheck_BeadNotFoundErrors(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"does-not-exist-1234"})
	if err := cmd.Execute(); !errors.Is(err, errExit) {
		t.Fatalf("Execute on missing bead: err = %v, want errExit; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "reading bead") || !strings.Contains(stderr.String(), "does-not-exist-1234") {
		t.Errorf("stderr = %q, want it to name the missing bead id", stderr.String())
	}
}

// TestFormulaVersionCheck_FormulaNotOnDiskErrors covers the
// formula.Compile error path — bead refers to a formula whose on-disk
// file has been deleted. The error must wrap the formula name so the
// operator knows which formula to restore.
func TestFormulaVersionCheck_FormulaNotOnDiskErrors(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, "ghost-formula", "abc123")
	t.Chdir(cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	cmd := newFormulaVersionCheckCmd(&stdout, &stderr)
	cmd.SetArgs([]string{beadID})
	if err := cmd.Execute(); !errors.Is(err, errExit) {
		t.Fatalf("Execute with bead referring to absent formula: err = %v, want errExit; stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "ghost-formula") {
		t.Errorf("stderr = %q, want it to name ghost-formula so operators can correlate", stderr.String())
	}
}

// TestNewFormulaCmd_RegistersVersionCheckSubcommand is a regression
// guard against the cmd-tree wiring. newFormulaCmd is currently
// uncovered (it is exercised only by the actual gc invocation path);
// covering the subcommand-registration line here also asserts that a
// future refactor doesn't silently drop the version-check subcommand
// while leaving the function definition intact.
func TestNewFormulaCmd_RegistersVersionCheckSubcommand(t *testing.T) {
	cmd := newFormulaCmd(&bytes.Buffer{}, &bytes.Buffer{})
	var found bool
	for _, sub := range cmd.Commands() {
		if sub.Name() == "version-check" {
			found = true
			break
		}
	}
	if !found {
		var names []string
		for _, sub := range cmd.Commands() {
			names = append(names, sub.Name())
		}
		t.Fatalf("newFormulaCmd subcommands = %v, want one named %q", names, "version-check")
	}
}

// These tests exercise run rather than the command constructor because the
// assembled tree owns the terminal writers, JSON contract gate, and exit code.
func TestFormulaVersionCheckReportsMatchThroughAssembledCLI(t *testing.T) {
	cityDir, diskHash := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, versionCheckFormulaName, diskHash)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", cityDir, "formula", "version-check", beadID}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(version-check match) = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "matches on-disk version") {
		t.Errorf("stdout = %q, want a visible match verdict", stdout.String())
	}
}

func TestFormulaVersionCheckReportsDivergenceThroughAssembledCLI(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, versionCheckFormulaName, "deadbeefdeadbeefdeadbeefdeadbeef")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", cityDir, "formula", "version-check", beadID}, &stdout, &stderr); code != 1 {
		t.Fatalf("run(version-check divergence) = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); !strings.Contains(got, "DIVERGES") || !strings.Contains(got, "bead hash") || !strings.Contains(got, "disk hash") {
		t.Errorf("stdout = %q, want divergence verdict and both hashes", got)
	}
}

func TestFormulaVersionCheckMakesErrorsVisibleThroughAssembledCLI(t *testing.T) {
	cityDir, _ := writeVersionCheckCity(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", cityDir, "formula", "version-check", "does-not-exist-1234"}, &stdout, &stderr); code == 0 {
		t.Fatalf("run(version-check missing bead) = 0, want failure; stdout=%q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "does-not-exist-1234") {
		t.Errorf("stderr = %q, want visible diagnostic naming the unreadable bead", stderr.String())
	}
}

func TestFormulaVersionCheckUsesMetadataFormulaNameWhenRefIsEmpty(t *testing.T) {
	cityDir, diskHash := writeVersionCheckCity(t)
	beadID := createVersionCheckBeadNamedByMetadata(t, cityDir, versionCheckFormulaName, diskHash)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", cityDir, "formula", "version-check", beadID}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(version-check metadata name) = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), versionCheckFormulaName) {
		t.Errorf("stdout = %q, want formula name from metadata", stdout.String())
	}
}

func TestFormulaVersionCheckJSONPassesContractGate(t *testing.T) {
	cityDir, diskHash := writeVersionCheckCity(t)
	beadID := createVersionCheckBead(t, cityDir, versionCheckFormulaName, diskHash)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--city", cityDir, "formula", "version-check", beadID, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run(version-check --json) = %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	payload := validateManagementJSONPayload(t, []string{"formula", "version-check"}, &stdout)
	if payload["match"] != true || payload["disk_hash"] != diskHash {
		t.Errorf("payload = %v, want a matching verdict and disk hash %q", payload, diskHash)
	}
}
