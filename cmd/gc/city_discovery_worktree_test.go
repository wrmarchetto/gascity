package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/citylayout"
)

// Pins that a city.toml inside another city's .gc/worktrees/ is not itself a
// city root.
//
// git worktree add of the city repo reproduces every tracked file at the
// worktree root, city.toml included, so a worktree is indistinguishable from a
// city to a marker-file check. findCity then resolved the scope to the
// worktree, and resolveManagedDoltRuntimeLayout derived DataDir from that
// scope -- one private, empty dolt server per worktree, plus a .beads/config
// rewritten with the enclosing RIG's issue prefix because bd init walks up
// when BEADS_DIR misses -- the hazard at initBeadsForDir in
// beads_provider_lifecycle.go.
//
// The discriminator is deliberately NOT "any path under a .gc/worktrees/".
// A PR clone or a per-bead worktree may legitimately hold its own store, and
// dolt_scope_watchdog.go's one-server-per-scope contract names worktrees as
// real scopes. What disqualifies this one is that an ANCESTOR is itself a city
// root and this path sits in that city's .gc/worktrees/ -- so the outer city
// already owns the scope.
func TestFindCitySkipsAWorktreeInsideAnotherCity(t *testing.T) {
	outer := canonicalTestPath(t.TempDir())
	if err := os.WriteFile(filepath.Join(outer, citylayout.CityConfigFile), []byte("[city]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The layout worktree-setup.sh builds: .gc/worktrees/<repo>/<slot>/, with
	// the repo's tracked city.toml reproduced at the slot root.
	slot := filepath.Join(outer, ".gc", "worktrees", "city", "toolsmith-2")
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slot, citylayout.CityConfigFile), []byte("[city]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCityWithOptions(slot, cityDiscoveryOptions{ceilingDirs: []string{outer}})
	if err != nil {
		t.Fatalf("findCityWithOptions(%q) errored: %v", slot, err)
	}
	if got != outer {
		t.Errorf("findCityWithOptions(%q) = %q, want the enclosing city %q", slot, got, outer)
	}
}

// A worktree-shaped path whose ancestors hold no city.toml is still a city.
//
// This is the case the fix must not break, and it is why the rule keys on an
// enclosing city root rather than on the ".gc/worktrees" path segment: a PR
// clone or a standalone checkout can sit at such a path and genuinely own its
// store. Without this test a blanket path-segment refusal passes the test
// above and silently strands those.
func TestFindCityKeepsAWorktreePathWithNoEnclosingCity(t *testing.T) {
	root := canonicalTestPath(t.TempDir())
	standalone := filepath.Join(root, ".gc", "worktrees", "city", "pr-clone")
	if err := os.MkdirAll(standalone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(standalone, citylayout.CityConfigFile), []byte("[city]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := findCityWithOptions(standalone, cityDiscoveryOptions{ceilingDirs: []string{root}})
	if err != nil {
		t.Fatalf("findCityWithOptions(%q) errored: %v", standalone, err)
	}
	if got != standalone {
		t.Errorf("findCityWithOptions(%q) = %q, want the path itself %q -- no ancestor is a city", standalone, got, standalone)
	}
}
