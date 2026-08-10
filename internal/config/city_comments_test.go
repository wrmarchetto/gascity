// Tests that a city.toml edit-rewrite keeps the prose its author wrote.
//
// The suite exists because the loss it pins was silent, total, and had no
// recovery path: every mutation re-rendered the whole struct, so `gc agent
// suspend` or `gc rig remove` emitted a normalized comment-free file over the
// operator's. Measured on this city's own city.toml, that turned 151 lines into
// 55 and destroyed all 90 comment lines (bead ci-bzy4).
//
// This is the seam-level half of the pair. The line-scan and anchoring
// invariants belong to internal/tomlcomments and are pinned there; what is
// tested here is that the write path actually calls it, against the encoder the
// write path really uses -- a City STRUCT, whose fields keep declaration order,
// not the alphabetized map render the lower suite constructs.
//
// Run: go test ./internal/config/ -run 'CityRewrite|CarryAuthored'
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// realCityTOML returns a checked-in city.toml carrying 90 comment lines across
// 151, including a revert path and a documented absence whose argument cannot
// be rederived from any value in the file.
//
// Read from internal/tomlcomments/testdata rather than copied into this
// package's own testdata: one authored document, one place to update it. The
// higher layer reaching into the lower layer's fixtures is the direction that
// already holds for the code (config imports tomlcomments), so it cannot
// become a cycle.
func realCityTOML(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "tomlcomments", "testdata", "city.toml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(src)
}

// writeFixtureCity lays out a city whose city.toml is the real authored
// fixture and returns the path to it.
func writeFixtureCity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cityPath := filepath.Join(dir, "city.toml")
	if err := os.WriteFile(cityPath, []byte(realCityTOML(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cityPath
}

// A mutation that changes nothing must leave the file byte-for-byte as
// authored. This is the strongest statement of the fix and the exact shape of
// the bug: the destruction did not need a mutation at all, only a command that
// reached the write path.
func TestCityRewriteWithNoMutationLeavesFileByteIdentical(t *testing.T) {
	cityPath := writeFixtureCity(t)
	original := realCityTOML(t)

	cfg, err := Load(fsys.OSFS{}, cityPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := WriteCityAndRigSiteBindingsForEdit(fsys.OSFS{}, cityPath, cfg); err != nil {
		t.Fatalf("WriteCityAndRigSiteBindingsForEdit: %v", err)
	}

	got, err := os.ReadFile(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("a no-op rewrite changed city.toml; first divergence at line %d\n--- got (%d lines, %d comments) ---\n%s",
			firstDivergentLine(string(got), original),
			len(strings.Split(string(got), "\n")), countComments(string(got)), got)
	}
}

// The load-bearing case: a real mutation must land AND the prose must survive
// it. Byte-identity cannot be asserted here (one line legitimately changes),
// so the assertions are the new value plus every comment line, which is what
// distinguishes a working carry from a write that was simply skipped.
func TestCityRewriteKeepsEveryCommentWhenAValueChanges(t *testing.T) {
	cityPath := writeFixtureCity(t)
	original := realCityTOML(t)

	cfg, err := Load(fsys.OSFS{}, cityPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found bool
	for i := range cfg.Rigs {
		if cfg.Rigs[i].Name == "gascity" {
			cfg.Rigs[i].DefaultBranch = "release"
			found = true
		}
	}
	if !found {
		t.Fatal("fixture no longer declares a gascity rig; pick another mutation target")
	}
	if err := WriteCityAndRigSiteBindingsForEdit(fsys.OSFS{}, cityPath, cfg); err != nil {
		t.Fatalf("WriteCityAndRigSiteBindingsForEdit: %v", err)
	}

	raw, err := os.ReadFile(cityPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, `default_branch = "release"`) {
		t.Error("the mutation did not reach the file")
	}
	for i, ln := range strings.Split(original, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		if !strings.Contains(got, ln) {
			t.Errorf("comment from line %d was destroyed by the rewrite: %q", i+1, ln)
		}
	}
	if want, have := countComments(original), countComments(got); want != have {
		t.Errorf("rewritten city.toml has %d comment lines, authored file had %d", have, want)
	}
}

// Carrying comments must never be able to fail a write. The comments are worth
// less than the mutation: an operator who cannot suspend an agent because the
// comment scanner tripped over their file is strictly worse off than one whose
// comments were dropped, which is the behavior every release before this had.
func TestCarryAuthoredCommentsFallsBackToRenderOnRefusal(t *testing.T) {
	rendered := []byte("[workspace]\nname = \"c\"\n")
	for _, tc := range []struct {
		name     string
		existing []byte
	}{
		{"unparsable existing file", []byte("this is not { toml ]\n")},
		{"empty existing file", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CarryAuthoredComments(tc.existing, rendered)
			if string(got) != string(rendered) {
				t.Errorf("CarryAuthoredComments = %q, want the render unchanged", got)
			}
		})
	}
}

func countComments(doc string) int {
	n := 0
	for _, ln := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			n++
		}
	}
	return n
}

func firstDivergentLine(got, want string) int {
	g := strings.Split(got, "\n")
	w := strings.Split(want, "\n")
	for i := 0; i < len(g) && i < len(w); i++ {
		if g[i] != w[i] {
			return i + 1
		}
	}
	return min(len(g), len(w)) + 1
}
