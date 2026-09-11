package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// dashboardSourceRoots are the two TypeScript trees whose production code is
// compiled into the SPA the supervisor embeds.
var dashboardSourceRoots = []string{
	"dashboardspa/web/frontend/src",
	"dashboardspa/web/shared/src",
}

// isDashboardProductionSource reports whether a path under a dashboard source
// root carries production behavior, as opposed to test or fixture data.
//
// Test files and fixture corpora are EXCLUDED on purpose, and the exclusion is
// not a loophole: a test that proves the transcript rows are hidden has to name
// one, and a fixture is data the assertion reads, never policy the app applies.
// The generated client is excluded for the opposite reason -- it is emitted
// from the OpenAPI spec, so any literal in it came from the Go side already.
func isDashboardProductionSource(rel string) bool {
	base := filepath.Base(rel)
	switch {
	case strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"):
		return false
	case strings.Contains(rel, "/fixtures/"), strings.Contains(rel, "/generated/"):
		return false
	case strings.HasSuffix(base, ".ts"), strings.HasSuffix(base, ".tsx"):
		return true
	}
	return false
}

// TestDashboardSourceHardcodesNoReadyExcludedLabel is the mechanical half of
// ci-zg9lbn. The board hides bookkeeping rows by asking the binary for the set
// (GET /beads/label-policy); this gate is what stops the next editor from
// typing the literals into a .tsx instead, which is the copy that rots -- gc
// adds external-messaging families over time and nothing on the TypeScript
// side would redden.
//
// A denylist of literals documents the intent; this gate is the guarantee,
// because it is derived from beads.ReadyExcludedLabels() and so covers the
// family nobody thought to list. Its Go-side sibling is
// TestEveryExtmsgLocatorLabelIsReadyExcluded in internal/extmsg, which holds
// the Go copy in step by scanning extmsg's own source.
func TestDashboardSourceHardcodesNoReadyExcludedLabel(t *testing.T) {
	labels := beads.ReadyExcludedLabels()
	if len(labels) == 0 {
		t.Fatal("beads.ReadyExcludedLabels() is empty; this gate would scan for nothing and pass vacuously")
	}

	scanned := 0
	for _, root := range dashboardSourceRoots {
		if _, err := os.Stat(root); err != nil {
			t.Fatalf("dashboard source root %s: %v (the gate cannot pass by scanning nothing)", root, err)
		}
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "dist" {
					return filepath.SkipDir
				}
				return nil
			}
			if !isDashboardProductionSource(filepath.ToSlash(path)) {
				return nil
			}
			scanned++
			src, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, label := range labels {
				if strings.Contains(string(src), label) {
					t.Errorf("%s hardcodes the ready-excluded label %q; read it from GET /beads/label-policy instead (ci-zg9lbn)", path, label)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	// A walk that matched no file passes every Contains check trivially, which
	// is exactly how this gate would go green after the SPA moved.
	if scanned < 50 {
		t.Fatalf("scanned only %d dashboard source files; the roots %v no longer hold the SPA sources", scanned, dashboardSourceRoots)
	}
}
