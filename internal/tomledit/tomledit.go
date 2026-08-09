// Package tomledit applies key-level edits to a TOML document while leaving
// every byte it did not edit exactly as authored.
//
// It exists because the encode half of a decode/encode round trip has no
// access to comments or key order. Decoding a document into a map and
// re-emitting it returns an alphabetized, comment-free file, which is how
// `gc agent suspend` came to delete a 132-line header out of
// agents/toolsmith/agent.toml. The prose in those files -- why an agent is
// city-scoped, which experiment was already run and refused -- has no other
// home and cannot be rederived from the values, so the durable write path has
// to be a patch rather than a re-render.
//
// # Scope, and why it stops here
//
// One key deep, scalar values only. The alternative a future editor will reach
// for is a full comment-preserving TOML document model; it is a much larger
// thing to get right and nothing here needs it. The durable agent.toml writers
// mutate exactly description, scope, provider, and suspended -- all root-table
// scalars. Widening this to nested tables or array values wants that real
// document model, not another special case bolted on.
//
// # Correctness net
//
// Every edit is verified before it is returned: the result is decoded and
// compared against the input's decoded form with the requested edits applied.
// A line scan that misreads the document therefore surfaces as ErrUnsupported
// instead of as a silently corrupted file, and that net is what lets the scan
// stay as simple as it is. Consequences of leaning on it, both deliberate:
//
//   - Multi-line strings are tracked (a line starting with "[" inside one
//     would otherwise read as a table header), but bracket nesting is not.
//     A multi-line array whose continuation line begins with "[" is refused
//     rather than mis-edited. No agent.toml takes that shape.
//   - A NaN float anywhere in the document makes the comparison unequal and
//     refuses the edit. Untested and unhandled on purpose: refusing is the
//     safe direction and no config in this repository carries one.
//
// Line endings: existing lines are copied through byte-for-byte, so a CRLF
// document keeps its endings, but an inserted line uses LF. Not worth a
// branch -- every city layout writes LF and a mixed document decodes the same.
//
// Invariants are pinned by tomledit_test.go: go test ./internal/tomledit/
package tomledit

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrUnsupported reports an edit this package declined to perform, either
// because the requested value is out of scope or because the verification
// pass could not confirm the edited document means what was asked for. The
// input document is never partially written: callers get an error and the
// original bytes are untouched.
var ErrUnsupported = errors.New("tomledit: unsupported edit")

// Delete is the edit value that removes a root key rather than assigning it.
//
// Spelled as a sentinel rather than reusing a nil map value so that a caller
// passing an unset `any` by accident gets an error instead of a silent
// deletion -- the failure this package exists to prevent.
var Delete any = deleteSentinel{}

type deleteSentinel struct{}

// isDelete tests the sentinel by type assertion rather than by `v == Delete`.
// Interface equality panics when both sides hold the same uncomparable
// dynamic type, and a caller passing a slice or map is exactly the mistake
// this package should report as ErrUnsupported instead of crashing on.
func isDelete(v any) bool {
	_, ok := v.(deleteSentinel)
	return ok
}

// rootAssign matches a root-table assignment at the head of a line. Dotted
// keys (`a.b = 1`) deliberately do not match: the bare-key character class
// excludes "." so the scan stops before the "=", and a dotted key is not a
// root scalar this package will edit.
var rootAssign = regexp.MustCompile(`^([A-Za-z0-9_-]+|"[^"]*"|'[^']*')[ \t]*=`)

// SetRootKeys returns src with each entry of edits applied to the document's
// root table. A value of [Delete] removes the key; any other value assigns it.
// Keys absent from the document are appended after the last root assignment,
// ahead of any trailing comment block, so a comment introducing the first
// sub-table keeps the table it describes.
//
// Returns ErrUnsupported when a value is not a scalar, when an edited key's
// existing assignment cannot be located unambiguously, or when the verify pass
// finds the result would differ from the intended document by more than the
// requested keys.
func SetRootKeys(src []byte, edits map[string]any) ([]byte, error) {
	if len(edits) == 0 {
		return append([]byte(nil), src...), nil
	}

	before, err := decode(src)
	if err != nil {
		return nil, err
	}
	want := make(map[string]any, len(before)+len(edits))
	for k, v := range before {
		want[k] = v
	}
	normalized := make(map[string]any, len(edits))
	for k, v := range edits {
		if isDelete(v) {
			delete(want, k)
			normalized[k] = Delete
			continue
		}
		scalar, err := normalizeScalar(k, v)
		if err != nil {
			return nil, err
		}
		want[k] = scalar
		normalized[k] = scalar
	}

	out, err := applyEdits(src, normalized)
	if err != nil {
		return nil, err
	}

	after, err := decode(out)
	if err != nil {
		return nil, fmt.Errorf("%w: edited document no longer parses: %w", ErrUnsupported, err)
	}
	if !reflect.DeepEqual(after, want) {
		return nil, fmt.Errorf("%w: edit would have changed the document beyond the requested keys; edit the file by hand", ErrUnsupported)
	}
	return out, nil
}

// HasContent reports whether doc carries anything an author wrote: a key, a
// table, or a comment. Callers that delete a document's last key use this to
// decide between removing the file and leaving a key-less one behind, since
// a surviving comment block is exactly the content this package protects.
func HasContent(doc []byte) bool {
	for _, ln := range strings.Split(string(doc), "\n") {
		if strings.TrimSpace(ln) != "" {
			return true
		}
	}
	return false
}

func decode(src []byte) (map[string]any, error) {
	values := make(map[string]any)
	if len(bytes.TrimSpace(src)) == 0 {
		return values, nil
	}
	if _, err := toml.Decode(string(src), &values); err != nil {
		return nil, fmt.Errorf("tomledit: parsing document: %w", err)
	}
	return values, nil
}

// normalizeScalar converts v to the Go type BurntSushi/toml decodes the
// corresponding TOML value into, so the verify pass compares like with like.
// Integers widen to int64 for that reason and not for range.
func normalizeScalar(key string, v any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, fmt.Errorf("%w: key %q: nil value; use tomledit.Delete to remove a key", ErrUnsupported, key)
	case bool, string, int64, float64:
		return t, nil
	case int:
		return int64(t), nil
	default:
		return nil, fmt.Errorf("%w: key %q: only bool, string, integer, and float values are supported, got %T", ErrUnsupported, key, v)
	}
}

// rootScan records where the root table ends and where each root key is
// assigned, both as indices into the caller's line slice.
type rootScan struct {
	end     int
	keyLine map[string]int
}

// scanRoot walks the document's root table. The scan stops at the first table
// header, tracking multi-line strings so a "[" inside one is not mistaken for
// that header. Only the first assignment of a duplicate key is recorded; TOML
// forbids duplicates, so a second one means the document does not parse and
// the caller's decode has already failed.
func scanRoot(lines []string) rootScan {
	s := rootScan{end: len(lines), keyLine: make(map[string]int)}
	inMultiline := false
	delim := ""
	for i, ln := range lines {
		if inMultiline {
			if strings.Contains(ln, delim) {
				inMultiline = false
			}
			continue
		}
		trimmed := strings.TrimLeft(ln, " \t")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") {
			s.end = i
			return s
		}
		if m := rootAssign.FindStringSubmatch(trimmed); m != nil {
			key := unquoteKey(m[1])
			if _, seen := s.keyLine[key]; !seen {
				s.keyLine[key] = i
			}
		}
		switch {
		case strings.Count(ln, `"""`)%2 == 1:
			inMultiline, delim = true, `"""`
		case strings.Count(ln, `'''`)%2 == 1:
			inMultiline, delim = true, `'''`
		}
	}
	return s
}

func unquoteKey(raw string) string {
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			return raw[1 : len(raw)-1]
		}
	}
	return raw
}

// applyEdits rewrites only the lines the edits touch. Splitting on "\n" leaves
// a trailing "" element for a newline-terminated document; rejoining restores
// the original bytes exactly, and the sentinel is never mistaken for a code
// line because it is empty.
func applyEdits(src []byte, edits map[string]any) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	scan := scanRoot(lines)

	replace := make(map[int]string)
	drop := make(map[int]bool)
	var appendKeys []string

	for _, key := range sortedKeys(edits) {
		value := edits[key]
		at, found := scan.keyLine[key]
		switch {
		case isDelete(value):
			if found {
				drop[at] = true
			}
		case found:
			rendered, err := renderAssignment(key, value, lines[at])
			if err != nil {
				return nil, err
			}
			replace[at] = rendered
		default:
			appendKeys = append(appendKeys, key)
		}
	}

	appended := make([]string, 0, len(appendKeys))
	for _, key := range appendKeys {
		rendered, err := renderAssignment(key, edits[key], "")
		if err != nil {
			return nil, err
		}
		appended = append(appended, rendered)
	}

	// Append ahead of the blank and comment lines that trail the root table:
	// those introduce whatever follows, and a key wedged between a comment and
	// its table steals the comment.
	insertAt := scan.end
	for insertAt > 0 {
		trimmed := strings.TrimSpace(lines[insertAt-1])
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			break
		}
		insertAt--
	}

	out := make([]string, 0, len(lines)+len(appended))
	for i, ln := range lines {
		if i == insertAt {
			out = append(out, appended...)
		}
		if drop[i] {
			continue
		}
		if r, ok := replace[i]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, ln)
	}
	// A document whose last line carries a value puts insertAt past every
	// index the loop visits, so the append has to happen here instead.
	if insertAt == len(lines) {
		out = append(out, appended...)
	}
	return []byte(strings.Join(out, "\n")), nil
}

func sortedKeys(edits map[string]any) []string {
	keys := make([]string, 0, len(edits))
	for k := range edits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// renderAssignment produces the replacement line for key = value, carrying
// over the indentation and any trailing comment of the line it replaces.
// Value formatting is delegated to the TOML encoder rather than hand-quoted
// so string escaping matches what a full re-render would have produced.
func renderAssignment(key string, value any, existing string) (string, error) {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(map[string]any{key: value}); err != nil {
		return "", fmt.Errorf("%w: key %q: %w", ErrUnsupported, key, err)
	}
	line := strings.TrimRight(buf.String(), "\n")
	if strings.Contains(line, "\n") {
		return "", fmt.Errorf("%w: key %q: value does not encode to a single line", ErrUnsupported, key)
	}
	indent := existing[:len(existing)-len(strings.TrimLeft(existing, " \t"))]
	if at, ok := trailingCommentStart(existing); ok {
		return indent + line + existing[at:], nil
	}
	return indent + line, nil
}

// trailingCommentStart returns the index at which a line's trailing comment
// begins, including the whitespace run in front of the "#", so a replaced
// assignment keeps a note the author left beside it. Quoted "#" characters
// are skipped; a "#" inside a multi-line string is not reachable here because
// only single-line assignments are ever replaced.
func trailingCommentStart(line string) (int, bool) {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			start := i
			for start > 0 && (line[start-1] == ' ' || line[start-1] == '\t') {
				start--
			}
			return start, true
		}
	}
	return 0, false
}
