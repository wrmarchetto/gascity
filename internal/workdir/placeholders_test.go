// Scope: the work_dir placeholder contract -- which template variables a
// work_dir (and every sibling field that shares PathContext) may name, and
// what happens when a config names one that only session_setup has.
//
// Why this suite exists: config.Agent's doc comments are the source text for
// docs/reference/config.md (go run ./cmd/genschema), so a comment that
// over-promises is published as the schema. The work_dir comment promised
// "the same template placeholders as session_setup" -- a set that includes
// Session, WorkDir and ConfigDir, none of which PathContext carries. An
// operator reaching for per-session work_dir isolation writes {{.Session}},
// gets a session that will not spawn, and the error names a Go type rather
// than the placeholder set. Seven more comments enumerate the set by hand and
// had all missed WorktreesRoot since it was added.
//
// Delegated elsewhere: expansion mechanics and rig/worktree-root resolution
// are pinned by workdir_test.go; the generated Markdown is checked for drift
// by scripts/check-generated-docs-drift.sh.
//
//	go test ./internal/workdir -run 'Placeholder|PathContext|SessionSetupOnly' -count=1
package workdir

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// placeholderDocMarker is the phrase a comment uses to claim that some config
// field expands PathContext. Every comment carrying it must also spell the
// full placeholder set.
const placeholderDocMarker = "PathContext"

// placeholderDocSources are the files whose comments make that claim. Kept as
// an explicit list rather than a repo-wide walk so a prose mention of the type
// in an unrelated design note does not become a build gate.
var placeholderDocSources = []string{
	filepath.Join("..", "config", "config.go"),
	filepath.Join("..", "..", "cmd", "gc", "probe_template.go"),
}

// TestPlaceholdersMatchesPathContextFields pins the helper against the struct
// it describes. Placeholders is derived by reflection rather than written out,
// so this asserts the derivation itself -- an unexported or embedded field
// appearing in PathContext must not silently enter the published set.
func TestPlaceholdersMatchesPathContextFields(t *testing.T) {
	typ := reflect.TypeOf(PathContext{})
	var want []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		want = append(want, f.Name)
	}
	if got := Placeholders(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Placeholders() = %v, want %v", got, want)
	}
}

// TestDocCommentsEnumerateEveryPathContextField is the mechanical gate that
// keeps the published schema honest. Every comment that claims a field expands
// PathContext must spell the full set inline, because the generated Markdown
// is all a config author ever reads -- a comment that says "see PathContext"
// publishes a dangling reference. Adding a field to PathContext therefore
// fails here until every such comment is updated.
//
// Matching is on the exact rendered parenthesis list rather than on a parsed
// prose enumeration: a checker that tried to understand the sentence would
// have to be taught every phrasing, and the first comment it failed to parse
// would pass vacuously.
func TestDocCommentsEnumerateEveryPathContextField(t *testing.T) {
	want := PlaceholderDocList()
	checked := 0

	for _, src := range placeholderDocSources {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", src, err)
		}
		for _, group := range file.Comments {
			text := group.Text()
			if !strings.Contains(text, placeholderDocMarker) {
				continue
			}
			checked++
			// Compare against whitespace-collapsed text: the list is a
			// sentence fragment and wraps at whatever column the
			// surrounding prose lands on. Pinning it to one physical
			// line would make the gate about formatting, which the
			// hooks already own, and would push these comments past 80
			// columns.
			if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
				t.Errorf("%s:%d: comment claims %s expansion but does not enumerate %s\ngot:\n%s",
					src, fset.Position(group.Pos()).Line, placeholderDocMarker, want, text)
			}
		}
	}

	if checked == 0 {
		t.Fatalf("no comment in %v mentions %q -- the marker phrase this gate keys on was renamed, so it now passes vacuously",
			placeholderDocSources, placeholderDocMarker)
	}
}

// TestWorkDirDocClaimsPathContextExpansion keeps the gate above pointed at the
// field it was written for. work_dir is what an operator reaches for when
// arranging per-session worktrees, so dropping its claim would let the
// enumeration silently leave the published schema while the gate still passed
// on the sibling fields.
func TestWorkDirDocClaimsPathContextExpansion(t *testing.T) {
	src := filepath.Join("..", "config", "config.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, src, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parsing %s: %v", src, err)
	}
	doc := agentFieldDoc(file, "WorkDir")
	if doc == "" {
		t.Fatalf("config.Agent.WorkDir has no doc comment in %s", src)
	}
	if !strings.Contains(doc, placeholderDocMarker) {
		t.Errorf("config.Agent.WorkDir doc no longer claims %s expansion:\n%s", placeholderDocMarker, doc)
	}
}

// TestWorkDirRejectsSessionSetupOnlyPlaceholders pins the failure the doc
// comment used to invite. Session, WorkDir and ConfigDir exist in the
// session_setup context (cmd/gc.SessionSetupContext) and NOT in PathContext,
// and each must fail closed with the placeholder named -- a work_dir that
// silently kept its braces would spawn every session into one literal
// "{{.Session}}" directory, which is the shared-tree hazard the placeholder
// was reached for to avoid.
func TestWorkDirRejectsSessionSetupOnlyPlaceholders(t *testing.T) {
	for _, name := range []string{"Session", "WorkDir", "ConfigDir"} {
		t.Run(name, func(t *testing.T) {
			agent := config.Agent{
				Name:    "toolsmith",
				WorkDir: ".gc/worktrees/gascity/{{." + name + "}}",
			}
			_, err := ResolveWorkDirPathStrict(t.TempDir(), "city", "toolsmith", agent, nil)
			if err == nil {
				t.Fatalf("ResolveWorkDirPathStrict() with {{.%s}} = nil error, want a failure naming the placeholder", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("ResolveWorkDirPathStrict() error = %v, want it to name %q", err, name)
			}
		})
	}
}

// agentFieldDoc returns the doc comment text of the named field on the
// config.Agent struct declaration in file, or "" when absent.
func agentFieldDoc(file *ast.File, field string) string {
	var doc string
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Agent" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, f := range st.Fields.List {
			if len(f.Names) == 0 || f.Names[0].Name != field || f.Doc == nil {
				continue
			}
			doc = f.Doc.Text()
		}
		return false
	})
	return doc
}
