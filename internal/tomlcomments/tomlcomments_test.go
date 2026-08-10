// Tests for carrying an authored TOML document's prose onto a re-render.
//
// The suite exists because the failure it pins is silent and unrecoverable:
// the write paths that call Carry previously emitted the encoder's output
// directly, and a city.toml comment block has no other home to be restored
// from. So the invariants here are about what SURVIVES a carry and, just as
// importantly, what must NOT be carried to the wrong place -- a rationale
// comment relocated onto a different rig is worse than a dropped one, because
// it reads as authored.
//
// Value fidelity is delegated to Carry's own verify pass rather than asserted
// per-case here: every carry decodes its result and compares it against the
// render it started from, so a splice that changed a value returns
// ErrUnsupported instead of bytes. TestCarryRefusesWhenValuesWouldChange
// pins that net exists.
//
// Run: go test ./internal/tomlcomments/
package tomlcomments

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// render round-trips src through the encoder the way the config write paths
// do, producing the comment-free normalized form Carry has to restore prose
// onto. Deliberately a decode into map[string]any rather than a struct: it
// keeps the fixtures in this file independent of internal/config's schema, so
// a field rename there cannot quietly turn these cases into no-ops.
func render(t *testing.T, src string) string {
	t.Helper()
	var values map[string]any
	if _, err := toml.Decode(src, &values); err != nil {
		t.Fatalf("decoding fixture: %v", err)
	}
	var buf strings.Builder
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(values); err != nil {
		t.Fatalf("encoding fixture: %v", err)
	}
	return buf.String()
}

func carry(t *testing.T, original, rendered string) string {
	t.Helper()
	out, err := Carry([]byte(original), []byte(rendered))
	if err != nil {
		t.Fatalf("Carry: %v", err)
	}
	return string(out)
}

// A no-op mutation must return the authored file unchanged. This is the exact
// shape of the bug (bead ci-bzy4): every city.toml write re-rendered the whole
// struct, so a suspend that changed one bool rewrote all 151 lines of the
// operator's file down to 55 and took 90 comment lines with it.
func TestCarryOntoUnchangedRenderReproducesOriginal(t *testing.T) {
	original := `# Why this city exists.
provider = "claude"

[providers]

# Rationale for the pool launcher, which cannot be rederived
# from the value below.
[providers.claude]
command = "claude-pool"  # not bare claude
ready_delay_ms = 0

# Absence recorded on purpose: no stall timeouts here.
`
	got := carry(t, original, render(t, original))
	if got != original {
		t.Errorf("carry onto an unchanged render must be byte-identical to the original\n--- got ---\n%s\n--- want ---\n%s", got, original)
	}
}

// A comment block introducing a nested table belongs to that table, not to the
// line that happens to precede it in the render.
func TestCarryKeepsCommentAboveNestedTableHeader(t *testing.T) {
	original := `[providers]
# The mayor's pin trades failover for stability.
[providers.claude-mayor]
command = "claude-1"
`
	got := carry(t, original, render(t, original))
	if !strings.Contains(got, "# The mayor's pin trades failover for stability.\n[providers.claude-mayor]") {
		t.Errorf("comment did not stay above its table header:\n%s", got)
	}
}

// Prose above an assignment inside a table is the common case for a cited
// constant or a corrected value, so it has to survive independently of the
// table header's own comment.
func TestCarryKeepsCommentAboveAssignment(t *testing.T) {
	original := `[[rigs]]
name = "gascity"
# Corrected by hand: the origin/HEAD probe recorded a feature branch.
default_branch = "main"
`
	got := carry(t, original, render(t, original))
	if !strings.Contains(got, "# Corrected by hand: the origin/HEAD probe recorded a feature branch.\ndefault_branch = \"main\"") {
		t.Errorf("comment did not stay above its assignment:\n%s", got)
	}
}

// A trailing note sits beside the value it annotates and is lost by the same
// encode the block comments are, so it is carried by the same pass.
func TestCarryKeepsTrailingComment(t *testing.T) {
	original := `[providers.claude]
ready_delay_ms = 0  # zero on purpose, the launcher execs immediately
`
	got := carry(t, original, render(t, original))
	if !strings.Contains(got, "ready_delay_ms = 0  # zero on purpose, the launcher execs immediately") {
		t.Errorf("trailing comment not carried:\n%s", got)
	}
}

// Prose describing a key the mutation removed must go with it. Keeping it
// would leave the file asserting something about a value that is no longer
// there; the comment's whole subject is gone.
func TestCarryDropsCommentForRemovedKey(t *testing.T) {
	original := `[providers.claude]
command = "claude-pool"
# retention_ttl is set low because the store is local-only.
retention_ttl = "1h"
`
	rendered := `[providers.claude]
command = "claude-pool"
`
	got := carry(t, original, rendered)
	if strings.Contains(got, "retention_ttl") {
		t.Errorf("comment for a removed key was carried onto the new document:\n%s", got)
	}
}

// The misattribution case, and the reason array elements are keyed by name
// rather than by position. Removing the first [[rigs]] element shifts every
// later index down by one, so an index-anchored carry would move the second
// rig's rationale onto the first -- producing a file that states, in the
// operator's own voice, something false about the surviving rig.
func TestCarryKeysArrayElementsByNameNotPosition(t *testing.T) {
	original := `# dart runs the radiation harness, so it must not autoclose.
[[rigs]]
name = "dart"
prefix = "dt"

# gascity uses gs because upstream bead ids already use ga-.
[[rigs]]
name = "gascity"
prefix = "gs"
`
	rendered := `[[rigs]]
name = "gascity"
prefix = "gs"
`
	got := carry(t, original, rendered)
	if !strings.Contains(got, "# gascity uses gs because upstream bead ids already use ga-.") {
		t.Errorf("surviving element lost its own comment:\n%s", got)
	}
	if strings.Contains(got, "dart runs the radiation harness") {
		t.Errorf("removed element's comment was carried onto the surviving element:\n%s", got)
	}
}

// Reordering is the other way position anchoring goes wrong, and unlike a
// removal it leaves both comments in the file -- just swapped, which no
// reader would catch.
func TestCarryFollowsReorderedArrayElements(t *testing.T) {
	original := `# first is dart
[[rigs]]
name = "dart"

# second is gascity
[[rigs]]
name = "gascity"
`
	rendered := `[[rigs]]
name = "gascity"

[[rigs]]
name = "dart"
`
	got := carry(t, original, rendered)
	gascityAt := strings.Index(got, "# second is gascity")
	dartAt := strings.Index(got, "# first is dart")
	if gascityAt < 0 || dartAt < 0 {
		t.Fatalf("a comment was dropped entirely:\n%s", got)
	}
	if gascityAt > dartAt {
		t.Errorf("comments did not follow their elements through the reorder:\n%s", got)
	}
}

// A comment block after the last value has no following anchor to attach to,
// and it is exactly where a "why this is absent" note lives -- the one class
// of prose that cannot be rederived from any value in the file.
func TestCarryKeepsTrailingCommentBlockAtEOF(t *testing.T) {
	original := `provider = "claude"

# --- no [session] stall timeouts, deliberately (ci-zr5g) ---
# There is no threshold this city can currently defend.
`
	got := carry(t, original, render(t, original))
	if !strings.Contains(got, "# --- no [session] stall timeouts, deliberately (ci-zr5g) ---") {
		t.Errorf("EOF comment block was dropped:\n%s", got)
	}
	if !strings.HasSuffix(got, "# There is no threshold this city can currently defend.\n") {
		t.Errorf("EOF comment block did not land at the end:\n%q", got)
	}
}

// A key added by the mutation has no comment in the original, and must not
// inherit the run belonging to whatever line now follows it.
func TestCarryLeavesAddedKeyUncommented(t *testing.T) {
	original := `[[agent]]
name = "toolsmith"
# scope is city because the tree it edits is not a rig.
scope = "city"
`
	rendered := `[[agent]]
name = "toolsmith"
suspended = true
scope = "city"
`
	got := carry(t, original, rendered)
	want := `[[agent]]
name = "toolsmith"
suspended = true
# scope is city because the tree it edits is not a rig.
scope = "city"
`
	if got != want {
		t.Errorf("added key took a comment that belongs to another line\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// A "#" line inside a multi-line string is string content, not a comment.
// Lifting it out would relocate part of a value into the document's prose --
// the one splice this package can make that changes what a line MEANS rather
// than only where it sits.
func TestCarryDoesNotLiftCommentsOutOfMultilineStrings(t *testing.T) {
	original := `[workspace]
description = """
first line
# this is string content, not prose
last line
"""
`
	got := carry(t, original, render(t, original))
	for _, ln := range strings.Split(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") && strings.Contains(ln, "string content") {
			t.Errorf("a line inside a multi-line string was carried out as a comment:\n%s", got)
		}
	}
}

// A continuation line of a multi-line array can begin with "[", which a scan
// that only looked at the first character would read as a table header. The
// path context would then be nonsense for every line after it, so the symptom
// is not a corrupt array but a comment silently dropped further down the file.
func TestCarryTracksMultilineArraysAsValues(t *testing.T) {
	original := `[providers.claude]
args = [
  ["--flag", "x"],
]
# command is the pool launcher, not bare claude
command = "claude-pool"
`
	got := carry(t, original, render(t, original))
	if !strings.Contains(got, "# command is the pool launcher, not bare claude") {
		t.Errorf("comment after a multi-line array was dropped, so the array was read as a table header:\n%s", got)
	}
}

// The verify pass is what lets the line scan stay as simple as it is, so its
// existence is pinned rather than assumed. A rendered document the scan would
// have to corrupt to satisfy must come back as an error and never as bytes.
func TestCarryRefusesWhenValuesWouldChange(t *testing.T) {
	if _, err := Carry([]byte("a = 1\n"), []byte("this is not toml = = =\n")); err == nil {
		t.Error("Carry accepted a rendered document that does not parse")
	}
	if _, err := Carry([]byte("not { valid ] toml\n"), []byte("a = 1\n")); err == nil {
		t.Error("Carry accepted an original document that does not parse")
	}
}

// An original with nothing to carry must return the render untouched rather
// than an error: a freshly scaffolded city.toml has no prose yet, and every
// caller falls back to the render on error, so a spurious error here would
// look like success while quietly disabling the whole mechanism.
func TestCarryFromCommentlessOriginalReturnsRender(t *testing.T) {
	rendered := "a = 1\n\n[t]\nb = 2\n"
	got := carry(t, "a = 0\n", rendered)
	if got != rendered {
		t.Errorf("carry from a commentless original changed the render\n--- got ---\n%q\n--- want ---\n%q", got, rendered)
	}
}

// Not one comment line may be lost on the shape that motivated the bead: a
// real city.toml carrying 90 comment lines across 151. Kept as a checked-in
// fixture rather than a constructed document so the structure under test --
// map-valued [providers] tables interleaved with [[rigs]] arrays that own
// [rigs.imports] sub-tables -- is one an operator actually authored.
//
// The assertion is survival, not byte-identity, because render() here decodes
// into a map and BurntSushi sorts map keys: every table and key comes back
// alphabetized, which no production caller produces (they encode a struct, so
// fields keep declaration order). That makes this the harder case for
// anchoring rather than the fixture's real one -- every comment has to track a
// line that MOVED. Byte-identity against the encoder the write path actually
// uses is pinned at the seam instead, by
// TestCityRewritePreservesAuthoredComments in internal/config.
func TestCarryLosesNoCommentFromRealCityTOMLFixture(t *testing.T) {
	path := filepath.Join("testdata", "city.toml")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	original := string(src)
	got := carry(t, original, render(t, original))

	for i, ln := range strings.Split(original, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(ln), "#") {
			continue
		}
		if !strings.Contains(got, ln) {
			t.Errorf("fixture line %d was lost by the carry: %q", i+1, ln)
		}
	}
	if want, got := countCommentLines(original), countCommentLines(got); want != got {
		t.Errorf("carried document has %d comment lines, fixture has %d", got, want)
	}
}

func countCommentLines(doc string) int {
	n := 0
	for _, ln := range strings.Split(doc, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), "#") {
			n++
		}
	}
	return n
}

// ErrUnsupported must be matchable by callers, which all branch on it to fall
// back to the plain render.
func TestErrUnsupportedIsMatchable(t *testing.T) {
	_, err := Carry([]byte("a = 1\n"), []byte("= broken\n"))
	if !errors.Is(err, ErrUnsupported) {
		t.Errorf("Carry error does not wrap ErrUnsupported: %v", err)
	}
}
