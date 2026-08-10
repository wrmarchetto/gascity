// Tests that rewriting a city's pack.toml keeps the prose its author wrote.
//
// Scope is the manifest write path only. The line-scan and anchoring invariants
// belong to internal/tomlcomments and the city.toml seam to internal/config;
// what is pinned here is that this writer calls the carry at all, because it
// encodes its own struct and resolves its own write path rather than going
// through config.WriteCityAndRigSiteBindingsForEdit -- so it was a second,
// separately-reachable instance of the same loss (bead ci-bzy4).
//
// Run: go test ./internal/importsvc/ -run PackManifestRewrite
package importsvc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

func TestPackManifestRewriteKeepsAuthoredComments(t *testing.T) {
	dir := t.TempDir()
	packPath := filepath.Join(dir, "pack.toml")
	original := `# This city's own pack. Not importable -- it names local paths.
[pack]
name = "city"
schema = 2

# The lab pack is local, so no version pin: ` + "`version`" + ` is for
# git-backed imports only.
[defaults.rig.imports.lab]
source = "packs/lab"
`
	if err := os.WriteFile(packPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := fsys.OSFS{}
	manifest, err := loadCityPackManifest(fs, dir)
	if err != nil {
		t.Fatalf("loadCityPackManifest: %v", err)
	}
	if err := writeCityPackManifest(fs, dir, manifest); err != nil {
		t.Fatalf("writeCityPackManifest: %v", err)
	}

	raw, err := os.ReadFile(packPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for i, ln := range strings.Split(original, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		if !strings.Contains(got, ln) {
			t.Errorf("comment from line %d was destroyed by the manifest rewrite: %q", i+1, ln)
		}
	}
	if !strings.Contains(got, `source = "packs/lab"`) {
		t.Errorf("the manifest rewrite lost a value it was supposed to carry:\n%s", got)
	}
}
