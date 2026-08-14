package main

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"github.com/gastownhall/gascity/internal/builtinpacks"
	"github.com/gastownhall/gascity/internal/packman"
)

// resetSyntheticRepoValidationMemoForTest drops every memoized full
// validation so a test can count the validations a warm process performs
// from a known-empty starting point.
func resetSyntheticRepoValidationMemoForTest() {
	syntheticRepoFullyValidated.Range(func(k, _ any) bool {
		syntheticRepoFullyValidated.Delete(k)
		return true
	})
}

// TestFullSyntheticValidationRunsAtMostOncePerCachePerProcess pins the cost
// contract of the builtin-readiness path: the whole-tree read of a cached
// bundled pack happens at most once per (cache dir, commit) per process,
// however many times config is loaded.
//
// It is written as two consecutive loads of one already-warmed city because
// that is the shape every gc invocation takes -- the readiness pass fires
// during root command construction and again per config load, and its two
// predicates name overlapping source sets. Before the memo this counted four
// full validations across the two loads (core and bd, once through
// requiredBuiltinSourcesUsable and once through lockedBundledImportsUsable).
//
// The lower bound matters as much as the upper one. Asserting only "at most
// once" would stay green if the readiness path stopped validating content
// altogether, which is the Fast-only regression the memo exists to avoid --
// see the rejected alternatives in builtin_cache_validation.go.
//
// Scope: this counts the cmd/gc readiness call sites, which is what the memo
// covers. The pack-resolution sites in internal/config are already marker-only
// on their warm path and are not reachable through this seam.
func TestFullSyntheticValidationRunsAtMostOncePerCachePerProcess(t *testing.T) {
	clearGCEnv(t)
	city := t.TempDir()
	cityTOML := "name = \"cachecount\"\nprefix = \"cc\"\n\n[beads]\nprovider = \"bd\"\n"
	if err := os.WriteFile(filepath.Join(city, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	// Pinned [imports] plus packs.lock is what gc init writes, and it is what
	// puts core and bd on BOTH readiness predicates -- without the lock only
	// requiredBuiltinSourcesUsable runs and the cross-predicate duplication
	// this memo removes never appears.
	writeBuiltinImportsFixture(t, city, "core", "bd")

	// Warm first. A cold pass materializes the caches, and packman owes a full
	// validation immediately after every write; counting that would conflate
	// write-time validation with the per-invocation revalidation under test.
	if _, err := loadCityConfig(city, io.Discard); err != nil {
		t.Fatalf("warming load: %v", err)
	}

	var mu sync.Mutex
	counts := map[string]int{}
	realValidator := validateSyntheticRepoFull
	validateSyntheticRepoFull = func(dir, commit string) error {
		mu.Lock()
		counts[dir]++
		mu.Unlock()
		return realValidator(dir, commit)
	}
	t.Cleanup(func() { validateSyntheticRepoFull = realValidator })
	resetSyntheticRepoValidationMemoForTest()

	for i := 0; i < 2; i++ {
		if _, err := loadCityConfig(city, io.Discard); err != nil {
			t.Fatalf("load %d: %v", i+1, err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(counts) == 0 {
		t.Fatal("readiness path validated no bundled pack cache in full; " +
			"content drift would now go undetected until \"gc import install\"")
	}
	dirs := make([]string, 0, len(counts))
	for dir := range counts {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	for _, dir := range dirs {
		if counts[dir] != 1 {
			t.Errorf("full validation of %s ran %d times across two config loads, want 1", dir, counts[dir])
		}
	}
}

// TestSyntheticReadinessValidationRejectsEvictedCacheAfterMemoHit pins the
// half of the contract the memo must not swallow. Cache eviction is the
// failure the readiness pass exists to self-heal, and it happens to a tuple
// this process has already validated, so a memo that short-circuits on the
// tuple alone would report an absent cache as usable and the self-heal would
// never fire. The marker-only check ahead of the memo is what prevents that.
//
// Deliberately NOT asserted here: content drift inside a cache whose marker
// still validates. Catching that mid-process is exactly what once-per-process
// gives up, and pinning it would pin a behavior the design rejects.
func TestSyntheticReadinessValidationRejectsEvictedCacheAfterMemoHit(t *testing.T) {
	clearGCEnv(t)
	city := t.TempDir()
	materializeBuiltinPacksForTest(t, city)

	source, ok := builtinpacks.Source("core")
	if !ok {
		t.Fatal("core source not registered")
	}
	commit := bundledPackImportCommit()
	cacheDir, err := packman.RepoCachePath(source, commit)
	if err != nil {
		t.Fatalf("RepoCachePath(core): %v", err)
	}

	if err := validateSyntheticRepoForReadiness(cacheDir, commit); err != nil {
		t.Fatalf("readiness validation of a fresh cache: %v", err)
	}
	if err := os.RemoveAll(cacheDir); err != nil {
		t.Fatalf("evicting cache: %v", err)
	}
	if err := validateSyntheticRepoForReadiness(cacheDir, commit); err == nil {
		t.Error("readiness validation accepted an evicted cache after a memo hit")
	}
}
