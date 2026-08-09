package dashboardspa

// Pins the deployment's visible product identity in the EMBEDDED bundle
// rather than in the frontend source that produces it.
//
// The source and the artifact are separate facts here. dist/ is committed and
// reaches users through //go:embed all:dist (embed.go), so a source edit that
// never made it through `make dashboard-build` looks exactly like success
// until someone loads the page. Asserting against
// web/frontend/src/components/Header.tsx would reproduce that blind spot; a
// test over distFS cannot pass unless the bundle was rebuilt.
//
// Written as a Go test and not a vitest case on purpose: the frontend vitest
// suite runs in no gate at all today (nothing in Makefile, .githooks, or the
// CI workflow invokes it -- see ci-w09j), so a pin placed there would never
// execute. `go test ./internal/api/dashboardspa/...` runs in make
// dashboard-check, and dashboard-ci separately fails if dist/ is stale.
//
// The expected string is written out here rather than imported from the
// frontend. A test that reads the same constant the implementation reads
// cannot catch that constant being dropped.
//
// Run: go test ./internal/api/dashboardspa/ -run TestBundleCarries

import (
	"io/fs"
	"strings"
	"testing"
)

const brandWordmark = "DBZ Dark Lab"

// The tab title is fully static -- no document.title writer exists anywhere
// in the SPA -- so it cannot name a city. The header's suffix, by contrast, is
// the live city switcher. That asymmetry is why the title is the bare
// wordmark and carries no " · <city>" half: this supervisor serves every city
// under /city/<name>/, so any baked-in suffix would be wrong for all but one
// of them. Upstream's "gas city · ds-research" was wrong for all of them.
const wantTitle = "<title>" + brandWordmark + "</title>"

func TestBundleCarriesBrandedTabTitle(t *testing.T) {
	idx, err := fs.ReadFile(distFS, "dist/index.html")
	if err != nil {
		t.Fatalf("reading embedded dist/index.html: %v", err)
	}
	if !strings.Contains(string(idx), wantTitle) {
		t.Errorf("embedded dist/index.html does not contain %q\n"+
			"remedy: edit web/frontend/index.html, then run "+
			"`make dashboard-build` and commit dist/", wantTitle)
	}
}

func TestBundleCarriesBrandedHeaderWordmark(t *testing.T) {
	// The wordmark is JSX, so it lands in a hashed asset chunk rather than in
	// index.html. Which chunk is a Vite bundling detail; searching the whole
	// asset tree keeps this from breaking on a rechunk.
	found, err := distContains(brandWordmark, "dist/assets")
	if err != nil {
		t.Fatalf("scanning embedded dist/assets: %v", err)
	}
	if !found {
		t.Errorf("no asset under dist/assets contains the header wordmark %q\n"+
			"remedy: edit web/frontend/src/components/Header.tsx, then run "+
			"`make dashboard-build` and commit dist/", brandWordmark)
	}
}

func TestBundleCarriesNoUpstreamWordmark(t *testing.T) {
	// Defense in depth against a third render site appearing, and against an
	// upstream merge restoring either of the two known ones. The two positive
	// tests above are the real guard -- they fail on ANY casing of a reverted
	// wordmark, which this one would not.
	//
	// Case-sensitive, and deliberately does NOT forbid title-case "Gas City".
	// That form is the SOFTWARE's name in prose (FirstRunNote.tsx: "the
	// ambient home for a Gas City workspace"), which is a correct reference
	// and must not be rebranded -- renaming it would misdescribe the software
	// this dashboard ships with. Only the lowercase wordmark is branding.
	// Nothing in dist/ carries the title-case form today because its only use
	// sits in AmbientHome.tsx, which no route imports and Vite therefore
	// tree-shakes out; forbidding it here would fail the day upstream routes
	// that page, for a correct reason.
	//
	// This test has already earned its place once. Vite copies HTML comments
	// out of web/frontend/index.html into dist/index.html verbatim -- it
	// strips JSX comments but not HTML ones -- so the first draft of the
	// comment above that <title>, which quoted the retired wordmark as
	// evidence, shipped the forbidden string into the bundle and failed here.
	// Describe the old wordmark in index.html; do not quote it.
	const upstreamWordmark = "gas city"
	found, err := distContains(upstreamWordmark, "dist")
	if err != nil {
		t.Fatalf("scanning embedded dist: %v", err)
	}
	if found {
		t.Errorf("embedded bundle still contains the upstream wordmark %q", upstreamWordmark)
	}
}

// distContains reports whether any regular file in the embedded bundle at or
// below root holds needle.
func distContains(needle, root string) (bool, error) {
	found := false
	err := fs.WalkDir(distFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(distFS, path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), needle) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	return found, err
}
