// Tests for the text-preserving TOML key editor.
//
// The suite exists because the property under test is invisible to a decoded
// value: every case here would also pass against the decode/encode round trip
// this package replaced, if the assertions looked at keys instead of bytes.
// So the assertions are on bytes, and each fixture carries the structure that
// round trip destroyed -- comments between keys, a multi-line array, a
// sub-table after the root keys.
//
// Whether the callers actually route through this package is pinned next to
// them, in internal/configedit/configedit_test.go.
//
// Run: go test ./internal/tomledit/
package tomledit_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/tomledit"
)

const authored = `# Header prose that cannot be rederived from the values.
# Second header line.

# Why the template is a copy.
prompt_template = "/abs/prompt.md"
provider = "codex"

# --- scope ---

scope = "city"

pre_start = [
  "setup.sh one",
  "setup.sh two",
]

# See the pack for why model is named explicitly.
[option_defaults]
  model = "opus-5"
`

func TestInsertedKeyLeavesEveryOtherByteAlone(t *testing.T) {
	got, err := tomledit.SetRootKeys([]byte(authored), map[string]any{"suspended": true})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	want := strings.Replace(authored, "]\n\n# See the pack", "]\nsuspended = true\n\n# See the pack", 1)
	if string(got) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The insert lands after the last root assignment rather than immediately
// before the table header, so the comment introducing [option_defaults] keeps
// the table it describes. Pinned separately because both placements produce
// valid TOML and decode identically -- only the text tells them apart.
func TestInsertKeepsTrailingCommentAttachedToItsTable(t *testing.T) {
	got, err := tomledit.SetRootKeys([]byte(authored), map[string]any{"suspended": true})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	if !strings.Contains(string(got), "# See the pack for why model is named explicitly.\n[option_defaults]") {
		t.Fatalf("comment separated from its table:\n%s", got)
	}
}

func TestInsertThenDeleteRestoresOriginalBytes(t *testing.T) {
	suspended, err := tomledit.SetRootKeys([]byte(authored), map[string]any{"suspended": true})
	if err != nil {
		t.Fatalf("SetRootKeys insert: %v", err)
	}
	resumed, err := tomledit.SetRootKeys(suspended, map[string]any{"suspended": tomledit.Delete})
	if err != nil {
		t.Fatalf("SetRootKeys delete: %v", err)
	}
	if string(resumed) != authored {
		t.Fatalf("round trip:\n%s\nwant:\n%s", resumed, authored)
	}
}

func TestReplaceKeepsIndentAndTrailingComment(t *testing.T) {
	src := "  provider = \"codex\"   # rotation pool, not the bare binary\nscope = \"city\"\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"provider": "claude"})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	want := "  provider = \"claude\"   # rotation pool, not the bare binary\nscope = \"city\"\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A "#" inside a string value is part of the value, not the start of a
// comment. Getting this backwards would truncate the value and the verify
// pass would then refuse the edit, so the failure is a broken command rather
// than a corrupt file -- still worth pinning at the scanner.
func TestHashInsideStringValueIsNotTreatedAsComment(t *testing.T) {
	src := "note = \"tracked as #1234\"\nprovider = \"codex\"\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"provider": "claude"})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	if !strings.Contains(string(got), "note = \"tracked as #1234\"") {
		t.Fatalf("string value damaged:\n%s", got)
	}
}

// Only the assignment line goes; a comment above it is authored text and is
// not the editor's to remove.
func TestDeleteRemovesOnlyTheAssignmentLine(t *testing.T) {
	src := "# why this agent is muted\nsuspended = true\nprovider = \"codex\"\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"suspended": tomledit.Delete})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	want := "# why this agent is muted\nprovider = \"codex\"\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestDeleteOfAbsentKeyIsANoOp(t *testing.T) {
	got, err := tomledit.SetRootKeys([]byte(authored), map[string]any{"suspended": tomledit.Delete})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	if string(got) != authored {
		t.Fatalf("got:\n%s\nwant unchanged:\n%s", got, authored)
	}
}

// A same-named key inside a sub-table belongs to that table, so the root key
// is inserted rather than the sub-table entry being overwritten.
func TestSubTableKeyIsNotMistakenForARootKey(t *testing.T) {
	src := "provider = \"codex\"\n\n[option_defaults]\n  suspended = true\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"suspended": false})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	want := "provider = \"codex\"\nsuspended = false\n\n[option_defaults]\n  suspended = true\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBracketInsideMultilineStringIsNotATableHeader(t *testing.T) {
	src := "description = \"\"\"\n[not a table]\n\"\"\"\nprovider = \"codex\"\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"suspended": true})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	want := "description = \"\"\"\n[not a table]\n\"\"\"\nprovider = \"codex\"\nsuspended = true\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A document whose last line carries a value has no line to insert before, so
// the appended key takes a separate code path from every other insert here.
// The file's own convention wins: no trailing newline in, none out.
func TestInsertAppendsToADocumentWithNoTrailingNewline(t *testing.T) {
	got, err := tomledit.SetRootKeys([]byte("provider = \"codex\""), map[string]any{"suspended": true})
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	want := "provider = \"codex\"\nsuspended = true"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestAbsentAndEmptyDocumentsGainTheKey(t *testing.T) {
	for _, src := range []string{"", "\n", "  \n"} {
		got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"suspended": true})
		if err != nil {
			t.Fatalf("SetRootKeys(%q): %v", src, err)
		}
		if !strings.Contains(string(got), "suspended = true") {
			t.Fatalf("SetRootKeys(%q) = %q, want the key", src, got)
		}
	}
}

// Multiple inserts must be emitted in a stable order. A map iterated
// unsorted produces a different file on every run, which shows up as a
// spurious diff in whatever repository holds the config.
func TestMultiKeyInsertIsDeterministic(t *testing.T) {
	edits := map[string]any{"suspended": true, "provider": "claude", "scope": "city"}
	first, err := tomledit.SetRootKeys([]byte("# header\n"), edits)
	if err != nil {
		t.Fatalf("SetRootKeys: %v", err)
	}
	for i := 0; i < 20; i++ {
		again, err := tomledit.SetRootKeys([]byte("# header\n"), edits)
		if err != nil {
			t.Fatalf("SetRootKeys rerun %d: %v", i, err)
		}
		if string(again) != string(first) {
			t.Fatalf("rerun %d differs:\n%s\nfirst:\n%s", i, again, first)
		}
	}
}

// The scan reads a continuation line that starts with "[" as a table header
// and would wedge the new key into the middle of the array. Caught by the
// verify pass's re-parse, which is the cheaper half of the net.
func TestMisreadArrayIsRefusedRatherThanCorrupted(t *testing.T) {
	src := "matrix = [\n[1, 2],\n[3, 4],\n]\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"suspended": true})
	if !errors.Is(err, tomledit.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported; got %q", err, got)
	}
	if got != nil {
		t.Fatalf("refused edit returned bytes %q, want nil", got)
	}
}

// Reaches the OTHER half of the net -- the decoded-value comparison -- with a
// document the scan misreads into output that still parses.
//
// `\"""` is a legal way to write three quotes inside a multi-line basic
// string, and the scan's "closed by any line containing the delimiter" rule
// ends the string there. Everything after it reads as root table, so the
// `provider` INSIDE the string is recorded as the root assignment and the
// replacement lands in the string's text. That result is valid TOML with the
// wrong meaning, so only the comparison catches it.
//
// Constructed rather than reached through an injected fake scan: a real
// document proves the branch fires against real input, and a test-only seam
// would pin the seam instead.
func TestMisreadMultilineStringIsRefusedByValueComparison(t *testing.T) {
	src := "note = \"\"\"\nhe said \\\"\"\" hello\nprovider = \"wrong\"\n\"\"\"\nprovider = \"codex\"\n"
	got, err := tomledit.SetRootKeys([]byte(src), map[string]any{"provider": "claude"})
	if !errors.Is(err, tomledit.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported; got %q", err, got)
	}
	if got != nil {
		t.Fatalf("refused edit returned bytes %q, want nil", got)
	}
}

func TestNonScalarValueIsRefused(t *testing.T) {
	_, err := tomledit.SetRootKeys([]byte("provider = \"codex\"\n"), map[string]any{"pre_start": []string{"a"}})
	if !errors.Is(err, tomledit.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// A nil value is a caller mistake, not a shorthand for removal. Treating it
// as a delete would make an unset variable erase a key silently, which is the
// failure mode this package exists to prevent.
func TestNilValueIsRefusedRatherThanTreatedAsDelete(t *testing.T) {
	_, err := tomledit.SetRootKeys([]byte("suspended = true\n"), map[string]any{"suspended": nil})
	if !errors.Is(err, tomledit.ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

func TestMalformedDocumentSurfacesTheParseError(t *testing.T) {
	_, err := tomledit.SetRootKeys([]byte("provider = \n"), map[string]any{"suspended": true})
	if err == nil {
		t.Fatal("SetRootKeys on malformed TOML returned no error")
	}
}

func TestHasContentSeesCommentsButNotBlankDocuments(t *testing.T) {
	cases := []struct {
		doc  string
		want bool
	}{
		{"", false},
		{"\n\n  \t\n", false},
		{"# a header and nothing else\n", true},
		{"provider = \"codex\"\n", true},
	}
	for _, c := range cases {
		if got := tomledit.HasContent([]byte(c.doc)); got != c.want {
			t.Errorf("HasContent(%q) = %v, want %v", c.doc, got, c.want)
		}
	}
}
