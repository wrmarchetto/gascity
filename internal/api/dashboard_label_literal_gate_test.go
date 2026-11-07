package api

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

// dashboardSourceRoots are the two TypeScript trees whose production code is
// compiled into the SPA the supervisor embeds.
var dashboardSourceRoots = []string{
	"dashboardspa/web/frontend/src",
	"dashboardspa/web/shared/src",
}

// isDashboardProductionSource reports whether a path carries production
// behavior. Tests and fixtures are excluded on purpose: a test proving the rows
// are hidden has to name one, and a fixture is data an assertion reads, never
// policy the app applies.
func isDashboardProductionSource(rel string) bool {
	base := path.Base(rel)
	switch {
	case strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".test.tsx"):
		return false
	case strings.Contains(rel, "/fixtures/"):
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

	scanned, hits, err := scanDashboardSources(os.DirFS("."), dashboardSourceRoots, labels)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		t.Errorf("%s hardcodes the ready-excluded label %q; read it from GET /beads/label-policy instead (ci-zg9lbn)", h.path, h.label)
	}
	// A walk that matched no file passes every Contains check trivially, which
	// is exactly how this gate would go green after the SPA moved.
	if scanned < 50 {
		t.Fatalf("scanned only %d dashboard source files; the roots %v no longer hold the SPA sources", scanned, dashboardSourceRoots)
	}

	label := fmt.Sprintf("gc:planted-%x", time.Now().UnixNano())
	tree := fstest.MapFS{
		"src/app.tsx":        {Data: []byte("const hidden = '" + label + "'\n")},
		"src/views/clean.ts": {Data: []byte("export const x = 1\n")},
		"src/generated/gc-supervisor-client/index.ts": {Data: []byte("export const l = '" + label + "'\n")},
	}
	for _, tc := range []struct {
		name, gone string
		wantErr    bool
	}{
		{"generated client removed mid-walk is skipped", "src/generated/gc-supervisor-client", false},
		{"generated client present is not scanned", "", false},
		{"production dir removed mid-walk still fails the walk", "src/views", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fsys := vanishingDirFS{FS: tree, gone: tc.gone}
			scanned, hits, err := scanDashboardSources(fsys, []string{"src"}, []string{label})
			if tc.wantErr {
				if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("err = %v, want fs.ErrNotExist: only generated/ may vanish unreported", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("a regeneration racing the walk failed the gate: %v", err)
			}
			if scanned != 2 || len(hits) != 1 || hits[0].path != "src/app.tsx" {
				t.Fatalf("scanned %d files with hits %+v, want 2 files and one hit in src/app.tsx", scanned, hits)
			}
		})
	}
}

type labelHit struct{ path, label string }

// scanDashboardSources walks each root in fsys and reports every production
// source file holding one of labels. Returns (files scanned, hits, walk error).
func scanDashboardSources(fsys fs.FS, roots, labels []string) (int, []labelHit, error) {
	scanned := 0
	var hits []labelHit
	for _, root := range roots {
		if _, err := fs.Stat(fsys, root); err != nil {
			return 0, nil, fmt.Errorf("dashboard source root %s: %w (the gate cannot pass by scanning nothing)", root, err)
		}
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// generated/ is pruned whole, not filtered per file: its literals come
				// from openapi.json, and merge-gate runs generate:client, which
				// removes and rewrites it, beside this test (Makefile merge-gate).
				switch d.Name() {
				case "node_modules", "dist", "generated":
					return fs.SkipDir
				}
				return nil
			}
			if !isDashboardProductionSource(p) {
				return nil
			}
			scanned++
			src, readErr := fs.ReadFile(fsys, p)
			if readErr != nil {
				return readErr
			}
			for _, label := range labels {
				if strings.Contains(string(src), label) {
					hits = append(hits, labelHit{p, label})
				}
			}
			return nil
		})
		if err != nil {
			return 0, nil, fmt.Errorf("walking %s: %w", root, err)
		}
	}
	return scanned, hits, nil
}

// vanishingDirFS stands in for a directory another process removed after its
// parent was listed: gone still appears in the listing, but opening it or
// anything below it fails with fs.ErrNotExist.
type vanishingDirFS struct {
	fs.FS
	gone string
}

func (v vanishingDirFS) Open(name string) (fs.File, error) {
	if name == v.gone || strings.HasPrefix(name, v.gone+"/") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return v.FS.Open(name)
}
