// Package tomlcomments carries an authored TOML document's comments onto a
// freshly encoded one.
//
// It exists because the encode half of a decode/encode round trip has no
// access to comments. Every write path that mutates city.toml re-rendered the
// whole struct, so changing one bool emitted a normalized comment-free file:
// measured on this city's own city.toml, a no-op rewrite turned 151 lines into
// 55 and destroyed all 90 comment lines, among them a revert path and a
// documented absence carrying a measurement argument that cannot be rederived
// from any value in the file (bead ci-bzy4).
//
// # Why carrying comments and not patching values
//
// The obvious alternative is the one internal/tomledit takes: patch the
// authored bytes in place and never re-render. That package deliberately stops
// at root-table scalars, and city.toml is nested tables and arrays of tables,
// so patching it wants a full comment-preserving document model -- a much
// larger thing to get right, and one whose bugs write WRONG VALUES.
//
// Carrying comments inverts that risk. The encoder's output is already correct
// by construction; this package only ever INSERTS comment and blank lines into
// it, and a comment cannot change what a document decodes to. The one way a
// splice can alter meaning is landing inside a multi-line string, where the
// inserted text stops being a comment -- which is why every carry is verified
// (see below) rather than trusted.
//
// What this package does NOT do, and must not be extended to do: move, reorder,
// or reformat any line the encoder produced. The moment it edits a value line
// it inherits tomledit's risk profile without tomledit's narrow scope.
//
// # Anchoring, and why not by position
//
// Each comment run is anchored to the structural line that FOLLOWS it,
// identified by its TOML path -- `providers.claude` for a header,
// `providers.claude` + `command` for an assignment. The render is then walked
// and each run re-inserted above the line carrying the same path, wherever the
// encoder put it. A run whose anchor is absent from the render is dropped,
// which is the right answer when the mutation deleted the key the prose
// described.
//
// Array-of-tables elements are keyed by their `name` value, NOT by position.
// Position anchoring is what a future editor will reach for and it is wrong in
// both directions: deleting the first [[rigs]] element shifts the rest down, so
// the second rig's rationale lands on the first, and a reorder swaps two
// comments while leaving both in the file. Either produces a document that
// states something false in the operator's own voice, which is strictly worse
// than a dropped comment because it reads as authored. Elements with no `name`,
// or an array whose names are not unique, fall back to position -- accepting
// the misattribution risk only where there is no identity to key on.
//
// # Correctness net
//
// Every carry decodes its result and compares it against the decoded render it
// started from. Any divergence returns ErrUnsupported and no bytes, so a line
// scan that misreads the document surfaces as a refusal rather than a corrupted
// config. That net is what lets the scan stay as simple as it is.
//
// Callers fall back to the plain render on error, so this package failing costs
// comments and never correctness.
//
// Invariants are pinned by tomlcomments_test.go: go test ./internal/tomlcomments/
package tomlcomments

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ErrUnsupported reports a carry this package declined to perform, either
// because a document did not parse or because the verification pass could not
// confirm the result still means what the render meant. Callers get an error
// and no bytes; the render they passed in is untouched and remains safe to
// write.
var ErrUnsupported = errors.New("tomlcomments: unsupported carry")

// assignRe matches an assignment at the head of a line. Dotted keys (`a.b = 1`)
// deliberately do not match: the bare-key class excludes ".", so the scan stops
// before the "=" and the line is treated as a continuation, anchoring nothing.
// A dotted key therefore loses its comment rather than risking an anchor built
// from a path this scanner did not actually resolve.
var assignRe = regexp.MustCompile(`^([A-Za-z0-9_-]+|"[^"]*"|'[^']*')[ \t]*=`)

// Sentinels separating an anchor's parts. Control bytes rather than "." or "="
// so that a table named `a.b` and a key named `b` under table `a` cannot
// collide with each other.
const (
	elemSep   = "\x01"
	keySep    = "\x02"
	headerTag = "\x03header"
)

// Carry returns rendered with the comment and blank-line layout of original
// re-applied. Comments whose anchor no longer exists in rendered are dropped.
//
// Returns ErrUnsupported when either document fails to parse or when the
// result would decode differently from rendered.
func Carry(original, rendered []byte) ([]byte, error) {
	renderedValues, err := decode(rendered)
	if err != nil {
		return nil, fmt.Errorf("%w: rendered document: %w", ErrUnsupported, err)
	}
	// The original is decoded purely as a trust check on the line scan. Its
	// values are never used -- rendered is the authority on those -- but a
	// document that does not parse is one whose table structure the scan may
	// have read wrongly, which is how a comment ends up under the wrong key.
	if _, err := decode(original); err != nil {
		return nil, fmt.Errorf("%w: original document: %w", ErrUnsupported, err)
	}
	originalDoc, err := scan(original)
	if err != nil {
		return nil, fmt.Errorf("%w: original document: %w", ErrUnsupported, err)
	}
	if len(originalDoc.runs) == 0 && len(originalDoc.trailing) == 0 && len(originalDoc.tail) == 0 {
		return append([]byte(nil), rendered...), nil
	}
	renderedDoc, err := scan(rendered)
	if err != nil {
		return nil, fmt.Errorf("%w: rendered document: %w", ErrUnsupported, err)
	}

	out := originalDoc.applyTo(renderedDoc)

	carriedValues, err := decode(out)
	if err != nil {
		return nil, fmt.Errorf("%w: carried document no longer parses: %w", ErrUnsupported, err)
	}
	if !reflect.DeepEqual(carriedValues, renderedValues) {
		return nil, fmt.Errorf("%w: carrying comments would have changed the document's values", ErrUnsupported)
	}
	return out, nil
}

func decode(src []byte) (map[string]any, error) {
	values := make(map[string]any)
	if len(strings.TrimSpace(string(src))) == 0 {
		return values, nil
	}
	if _, err := toml.Decode(string(src), &values); err != nil {
		return nil, err
	}
	return values, nil
}

// document is a scanned TOML file: its lines, the anchor each structural line
// carries, and the comment runs attached to those anchors.
type document struct {
	lines           []string
	endsWithNewline bool
	// anchorAt[i] is the anchor of line i, or "" when line i is a comment,
	// blank, or a continuation of a multi-line value.
	anchorAt []string
	// runs maps an anchor to the comment and blank lines immediately above it.
	runs map[string][]string
	// trailing maps an anchor to the same-line comment on its own line,
	// including the whitespace run in front of the "#".
	trailing map[string]string
	// tail is the comment and blank run after the last structural line, where
	// a "why this is absent" note lives.
	tail []string
}

// arrayElement records one [[array]] element while scanning, so its identity
// can be resolved after its `name` assignment has been seen. context is the
// element's own resolved path including its placeholder, which is what
// distinguishes an assignment in the element itself from one in a sub-table of
// it -- only the former can supply the element's name.
type arrayElement struct {
	arrayPath string
	context   string
	ordinal   int
	name      string
	hasName   bool
}

func scan(src []byte) (*document, error) {
	text := string(src)
	lines := strings.Split(text, "\n")
	endsWithNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if endsWithNewline {
		lines = lines[:len(lines)-1]
	}

	doc := &document{
		lines:           lines,
		endsWithNewline: endsWithNewline,
		anchorAt:        make([]string, len(lines)),
		runs:            make(map[string][]string),
		trailing:        make(map[string]string),
	}

	// Pass one: classify every line, resolving table context into paths whose
	// array elements are still placeholders (elemSep + element index).
	var elems []arrayElement
	// activeArray maps an array's dotted path to the index in elems of the
	// element currently open, so a sub-table header like [rigs.imports] can be
	// spliced onto the element it belongs to rather than onto the array itself.
	activeArray := make(map[string]int)
	// pending holds the comment and blank lines seen since the last structural
	// line, and elemOf the array element each structural line sits inside.
	context := ""
	currentElem := -1
	elemOf := make([]int, len(lines))
	inMultiline, delim := false, ""
	valueDepth := 0

	for i, ln := range lines {
		elemOf[i] = currentElem
		if inMultiline {
			if strings.Contains(ln, delim) {
				inMultiline = false
			}
			continue
		}
		if valueDepth > 0 {
			valueDepth += bracketDelta(ln)
			continue
		}
		trimmed := strings.TrimLeft(ln, " \t")
		switch {
		case strings.TrimSpace(trimmed) == "", strings.HasPrefix(trimmed, "#"):
			continue
		case strings.HasPrefix(trimmed, "["):
			path, isArray, ok := parseHeader(trimmed)
			if !ok {
				return nil, fmt.Errorf("line %d: unreadable table header", i+1)
			}
			resolved, elem := resolveHeader(path, isArray, &elems, activeArray)
			context = resolved
			currentElem = elem
			elemOf[i] = elem
			doc.anchorAt[i] = resolved + headerTag
		default:
			m := assignRe.FindStringSubmatch(trimmed)
			if m == nil {
				continue
			}
			key := unquoteKey(m[1])
			doc.anchorAt[i] = context + keySep + key
			if currentElem >= 0 && context == elems[currentElem].context && key == "name" {
				if v, ok := scalarString(ln); ok {
					elems[currentElem].name = v
					elems[currentElem].hasName = true
				}
			}
		}
		// A value may continue past this line either as a multi-line string or
		// as a multi-line array; both must stop the scan from reading their
		// continuation lines as structure.
		switch {
		case strings.Count(ln, `"""`)%2 == 1:
			inMultiline, delim = true, `"""`
		case strings.Count(ln, `'''`)%2 == 1:
			inMultiline, delim = true, `'''`
		default:
			if d := bracketDelta(ln); d > 0 {
				valueDepth = d
			}
		}
	}

	// Pass two: substitute element placeholders for name-keyed identities,
	// falling back to position for any array whose names are absent or not
	// unique.
	identity := elementIdentities(elems)
	for i := range doc.anchorAt {
		if doc.anchorAt[i] != "" {
			doc.anchorAt[i] = substituteElements(doc.anchorAt[i], identity)
		}
	}

	// Pass three: attach each comment and blank run to the structural line
	// below it, and the leftover run to tail.
	var pending []string
	for i, ln := range lines {
		anchor := doc.anchorAt[i]
		if anchor == "" {
			if isCommentOrBlank(ln) && !insideValue(doc, i) {
				pending = append(pending, ln)
			}
			continue
		}
		if len(pending) > 0 {
			doc.runs[anchor] = pending
			pending = nil
		}
		if at, ok := trailingCommentStart(ln); ok {
			doc.trailing[anchor] = ln[at:]
		}
	}
	doc.tail = pending
	return doc, nil
}

// insideValue reports whether line i sits within a multi-line string, where a
// leading "#" is string content rather than a comment. Lifting such a line into
// a comment run would move part of a value into the document's prose.
//
// Answered by walking back to the nearest structural line -- the assignment
// that opened the value -- and counting multi-line delimiters between there and
// here: an odd count means the string is still open. Recomputed per line rather
// than recorded during the scan because the scan's own inMultiline flag is
// consumed by the time pass three runs, and a second flag threaded through
// three passes is easier to leave stale than this is to reread.
//
// Multi-line ARRAYS are deliberately not treated as inside a value: a comment
// line is legal inside one and belongs to the element below it, so carrying it
// is correct. Only strings capture their contents.
func insideValue(doc *document, i int) bool {
	for j := i - 1; j >= 0; j-- {
		if doc.anchorAt[j] == "" {
			continue
		}
		span := strings.Join(doc.lines[j:i], "\n")
		return strings.Count(span, `"""`)%2 == 1 || strings.Count(span, `'''`)%2 == 1
	}
	return false
}

// elementIdentities returns, for each element index, the string that replaces
// its placeholder. Names are used only when every element of that array has a
// distinct one; otherwise the whole array falls back to ordinals, because a
// mix would let one element's comment attach to another.
func elementIdentities(elems []arrayElement) []string {
	seen := make(map[string]map[string]int)
	usable := make(map[string]bool)
	for _, e := range elems {
		if _, ok := seen[e.arrayPath]; !ok {
			seen[e.arrayPath] = make(map[string]int)
			usable[e.arrayPath] = true
		}
		if !e.hasName {
			usable[e.arrayPath] = false
			continue
		}
		seen[e.arrayPath][e.name]++
		if seen[e.arrayPath][e.name] > 1 {
			usable[e.arrayPath] = false
		}
	}
	out := make([]string, len(elems))
	for i, e := range elems {
		if usable[e.arrayPath] {
			out[i] = "name=" + e.name
			continue
		}
		out[i] = fmt.Sprintf("#%d", e.ordinal)
	}
	return out
}

var elemPlaceholder = regexp.MustCompile(elemSep + `e([0-9]+)` + elemSep)

func substituteElements(anchor string, identity []string) string {
	return elemPlaceholder.ReplaceAllStringFunc(anchor, func(m string) string {
		idx := 0
		if _, err := fmt.Sscanf(m, elemSep+"e%d"+elemSep, &idx); err != nil || idx >= len(identity) {
			return m
		}
		return elemSep + identity[idx] + elemSep
	})
}

// resolveHeader turns a header's dotted path into a context string with an
// element placeholder spliced in for every component that names an array of
// tables, so [rigs.imports] anchors inside the [[rigs]] element it follows
// rather than onto the array itself.
func resolveHeader(path string, isArray bool, elems *[]arrayElement, activeArray map[string]int) (string, int) {
	parts := splitPath(path)
	resolved := ""
	elem := -1
	created := -1
	for i, part := range parts {
		if resolved != "" {
			resolved += "."
		}
		resolved += part
		last := i == len(parts)-1
		switch {
		case last && isArray:
			ordinal := 0
			for _, e := range *elems {
				if e.arrayPath == resolved {
					ordinal++
				}
			}
			*elems = append(*elems, arrayElement{arrayPath: resolved, ordinal: ordinal})
			idx := len(*elems) - 1
			activeArray[resolved] = idx
			elem = idx
			created = idx
			resolved += elemSep + fmt.Sprintf("e%d", idx) + elemSep
		default:
			if idx, ok := activeArray[resolved]; ok {
				elem = idx
				resolved += elemSep + fmt.Sprintf("e%d", idx) + elemSep
			}
		}
	}
	if created >= 0 {
		(*elems)[created].context = resolved
	}
	return resolved, elem
}

// parseHeader reads a table header line, returning its dotted path and whether
// it declares an array of tables. Trailing comments are tolerated.
func parseHeader(trimmed string) (path string, isArray bool, ok bool) {
	body := trimmed
	if at, has := trailingCommentStart(body); has {
		body = strings.TrimRight(body[:at], " \t")
	}
	body = strings.TrimRight(body, " \t")
	switch {
	case strings.HasPrefix(body, "[[") && strings.HasSuffix(body, "]]"):
		return strings.TrimSpace(body[2 : len(body)-2]), true, true
	case strings.HasPrefix(body, "[") && strings.HasSuffix(body, "]"):
		return strings.TrimSpace(body[1 : len(body)-1]), false, true
	}
	return "", false, false
}

// splitPath splits a dotted header path on "." outside quotes, so a quoted key
// containing a dot stays one component.
func splitPath(path string) []string {
	var parts []string
	var cur strings.Builder
	var quote byte
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
			cur.WriteByte(c)
		case c == '.':
			parts = append(parts, unquoteKey(strings.TrimSpace(cur.String())))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, unquoteKey(strings.TrimSpace(cur.String())))
	return parts
}

// scalarString decodes an assignment line's value as a string, so an element
// keyed on `name` matches whether the author wrote it single- or double-quoted.
// Anything that is not a plain string yields ok=false and the element falls
// back to positional identity.
func scalarString(line string) (string, bool) {
	at := strings.Index(line, "=")
	if at < 0 {
		return "", false
	}
	var holder struct {
		V string `toml:"v"`
	}
	if _, err := toml.Decode("v = "+strings.TrimSpace(line[at+1:]), &holder); err != nil {
		return "", false
	}
	return holder.V, true
}

// bracketDelta returns the net change in array nesting depth contributed by a
// line, ignoring brackets inside strings. It is what keeps the continuation
// lines of a multi-line array from being read as table headers.
func bracketDelta(line string) int {
	depth := 0
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
			return depth
		case c == '[':
			depth++
		case c == ']':
			depth--
		}
	}
	return depth
}

func isCommentOrBlank(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

func unquoteKey(raw string) string {
	if len(raw) >= 2 {
		if (raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'') {
			return raw[1 : len(raw)-1]
		}
	}
	return raw
}

// trailingCommentStart returns the index at which a line's trailing comment
// begins, including the whitespace in front of the "#", so a note the author
// left beside a value is carried with it. Quoted "#" characters are skipped.
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

// applyTo emits target's lines with this document's comment runs re-inserted
// above the lines carrying the same anchors, and its tail appended.
//
// Only insertion happens here. A run's leading blank lines are dropped when the
// output already ends in one, because the encoder emits its own blank before
// most table headers and the authored run usually opens with the same blank --
// carrying both would grow a blank line on every write, which is a diff on a
// file nobody edited.
func (o *document) applyTo(target *document) []byte {
	out := make([]string, 0, len(target.lines)+len(o.tail))
	for i, ln := range target.lines {
		anchor := target.anchorAt[i]
		if anchor != "" {
			if run, ok := o.runs[anchor]; ok {
				out = append(out, trimLeadingBlanks(run, endsBlank(out))...)
			}
			if tc, ok := o.trailing[anchor]; ok {
				if _, has := trailingCommentStart(ln); !has {
					ln += tc
				}
			}
		}
		out = append(out, ln)
	}
	if len(o.tail) > 0 {
		out = append(out, trimLeadingBlanks(o.tail, endsBlank(out))...)
	}

	joined := strings.Join(out, "\n")
	if target.endsWithNewline || len(o.tail) > 0 {
		joined += "\n"
	}
	return []byte(joined)
}

// endsBlank reports whether the emitted output currently ends in a blank line,
// or is empty (where a leading blank would be equally spurious).
func endsBlank(out []string) bool {
	if len(out) == 0 {
		return true
	}
	return strings.TrimSpace(out[len(out)-1]) == ""
}

func trimLeadingBlanks(run []string, trim bool) []string {
	if !trim {
		return run
	}
	for len(run) > 0 && strings.TrimSpace(run[0]) == "" {
		run = run[1:]
	}
	return run
}
