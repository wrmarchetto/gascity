// Freshness gates for the bd top-level command-name list in commands.go.
//
// The list exists to stop a gc verb from shadowing a bd subcommand, and it can
// only do that job while it matches the bd gc actually ships against. This
// suite re-derives the list from the beads module source at the version go.mod
// pins and fails on any difference, so a dependency bump that adds, renames or
// drops a subcommand cannot land without the name list moving with it.
//
// Nothing here shells out to bd or to go, and nothing skips. Absence of the
// module source is a FAILURE, not a skip: a gate that goes green when it
// cannot see the thing it checks converts a real collision into a clean run,
// which is the exact failure ci-mosn exists to close. That is also why the
// package imports the beads module (see bdCommandsBeadsModuleImport below) --
// it makes the source structurally present rather than merely likely.
//
// Delegated elsewhere: per-subcommand FLAG currency, which needs an installed
// binary and lives under the integration tag in freshness_test.go.
//
//	go test ./internal/bdflags/
package bdflags

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	// Blank-imported for its build-graph side effect, not for any symbol.
	// internal/bdflags itself has no reason to depend on beads, so without
	// this import `go test ./internal/bdflags/` need not download the module
	// and the derivation below would find no source to read. Importing the
	// module's root package makes the go command materialize the whole module
	// -- including cmd/bd, which is package main and cannot be imported --
	// into the module cache before this test builds.
	_ "github.com/steveyegge/beads"
)

// bdCommandsBeadsModulePath is the module whose cmd/bd package registers the
// subcommand names commands.go mirrors.
const bdCommandsBeadsModulePath = "github.com/steveyegge/beads"

// derivedCommand is one top-level registration recovered from bd's source.
type derivedCommand struct {
	name    string
	aliases []string
}

// bdSourceDerivation is what one read of bd's cmd/bd source yields: the
// commands whose names it could read, and the registrations it could not.
//
// The second field is the half that matters. A derivation that returned only
// what it understood would report a bd command it has gone blind to as no
// command at all, which is indistinguishable from the name being free -- the
// exact conclusion this whole gate exists to stop gc from drawing.
type bdSourceDerivation struct {
	// commands maps canonical name to aliases.
	commands map[string][]string

	// unresolvable maps the identifier registered on rootCmd to the reason
	// its name could not be read.
	unresolvable map[string]string
}

// TestSourcedBeadsVersionMatchesGoMod is the mechanical prompt a beads bump
// gets. The collision gate reads a hand-maintained name list, and the way that
// list rots is a dependency bump nobody thought to review it against -- which
// is how `bd heartbeat` arrived under an intercept of the same name and stayed
// unnoticed for two months (ci-ctkz). Failing here means the version moved:
// re-derive the list (this file's other tests do the checking) and update
// SourcedBeadsVersion to match.
func TestSourcedBeadsVersionMatchesGoMod(t *testing.T) {
	got := beadsVersionFromGoMod(t)
	if got != SourcedBeadsVersion {
		t.Errorf("go.mod requires beads %s but commands.go records SourcedBeadsVersion = %s.\n"+
			"A beads bump must be reviewed against the top-level command-name list: re-run `go test ./internal/bdflags/` after setting SourcedBeadsVersion = %q, and fix whatever the derivation reports.",
			got, SourcedBeadsVersion, got)
	}
}

// TestBdTopLevelCommandsMatchModuleSource pins the name list against the bd
// source it claims to mirror. It is the difference between the list being a
// prompt to a human and being a guarantee: a bump cannot be waved through by
// editing SourcedBeadsVersion alone, because every added, renamed, removed or
// re-aliased subcommand shows up here as a diff.
func TestBdTopLevelCommandsMatchModuleSource(t *testing.T) {
	derived := deriveBdTopLevelCommands(t).commands

	want := make(map[string][]string, len(bdTopLevelCommands))
	for k, v := range bdTopLevelCommands {
		want[k] = v
	}

	for name, aliases := range derived {
		declared, ok := want[name]
		if !ok {
			t.Errorf("bd registers top-level command %q (aliases %v) that commands.go does not list.\n"+
				"Add it to bdTopLevelCommands -- until then a gc verb of that name would shadow it silently.",
				name, aliases)
			continue
		}
		if !sameNames(declared, aliases) {
			t.Errorf("bd command %q has aliases %v in the module source, but commands.go lists %v.\n"+
				"An alias missing here is an alias gc's collision gate cannot see (ci-ctkz).",
				name, aliases, declared)
		}
		delete(want, name)
	}

	for name := range want {
		if _, handCarried := bdCommandsNotInModuleSource[name]; handCarried {
			continue
		}
		t.Errorf("commands.go lists top-level command %q, but beads %s registers no such command.\n"+
			"Remove it, or -- if bd resolves it some way the derivation cannot read -- move it to bdCommandsNotInModuleSource with a comment naming the registration.",
			name, SourcedBeadsVersion)
	}
}

// TestEveryRootRegistrationIsReadableOrHandCarried is the gate's blind-spot
// alarm. Every command bd registers on its root must either have its name read
// out of the source or be named by hand -- because a registration whose name
// nothing recovers is a name gc's collision check silently believes is free.
//
// Failing here does NOT mean the derivation is broken. It means bd registers a
// command in a shape the derivation cannot read, and someone has to say which
// name that is.
func TestEveryRootRegistrationIsReadableOrHandCarried(t *testing.T) {
	handCarried := make(map[string]string, len(bdCommandsNotInModuleSource))
	for name, cmd := range bdCommandsNotInModuleSource {
		if cmd.registration != "" {
			handCarried[cmd.registration] = name
		}
	}
	for ident, why := range deriveBdTopLevelCommands(t).unresolvable {
		if _, ok := handCarried[ident]; ok {
			continue
		}
		t.Errorf("beads %s registers %s on its root command but the derivation cannot read its name: %s.\n"+
			"Run `bd %s --help`-style discovery by hand, then add the name to bdCommandsNotInModuleSource with registration: %q. Until then gc's collision gate believes that name is free.",
			SourcedBeadsVersion, ident, why, "<the name it registers>", ident)
	}
}

// TestBdCommandsNotInModuleSourceStayNecessary keeps the hand-carried list from
// outliving its reason, in both directions. An entry whose registration became
// readable is no longer an exception and its aliases stop being checked against
// anything; an entry whose registration is gone from bd's source is a name gc
// is reserving against a command that no longer exists.
//
// The two cobra built-ins carry no registration and are exempt from the second
// check by construction: there is nothing in bd's source for them to lose.
func TestBdCommandsNotInModuleSourceStayNecessary(t *testing.T) {
	derived := deriveBdTopLevelCommands(t)
	for name, cmd := range bdCommandsNotInModuleSource {
		if aliases, ok := derived.commands[name]; ok {
			t.Errorf("bdCommandsNotInModuleSource lists %q, but the derivation now recovers it from bd's source (aliases %v).\n"+
				"Move it to bdTopLevelCommands so its aliases are checked against the source like every other entry.",
				name, aliases)
		}
		if cmd.registration == "" {
			continue
		}
		if _, ok := derived.unresolvable[cmd.registration]; !ok {
			t.Errorf("bdCommandsNotInModuleSource carries %q for registration %s, but beads %s registers no such unreadable identifier on its root command.\n"+
				"Either bd dropped the command -- remove the entry -- or it renamed the identifier, in which case update registration.",
				name, cmd.registration, SourcedBeadsVersion)
		}
	}
}

// TestAliasGroupReportsEverySpellingOfACommand pins the accessor the collision
// gate depends on, from both directions. Looking a command up by an ALIAS must
// return the whole group and not just the alias: a caller that intercepts "hb"
// has to be told "heartbeat" is the other spelling it owes, which is the half
// of the ci-ctkz bug that a canonical-name-only lookup would still miss.
func TestAliasGroupReportsEverySpellingOfACommand(t *testing.T) {
	tests := []struct {
		lookup string
		want   []string
	}{
		{"heartbeat", []string{"heartbeat", "hb"}},
		{"hb", []string{"heartbeat", "hb"}},
		{"close", []string{"close", "done"}},
		{"done", []string{"close", "done"}},
		{"list", []string{"list"}},
		{"help", []string{"help"}},
		{"release-if-current", nil},
		{"", nil},
	}
	for _, tt := range tests {
		got := AliasGroup(tt.lookup)
		if !sameNames(got, tt.want) {
			t.Errorf("AliasGroup(%q) = %v, want %v", tt.lookup, got, tt.want)
		}
	}
}

// TestCommandNamesCoversAliasesAndHandCarriedNames guards the flattening the
// collision gate scans. A canonical-only or source-only CommandNames would let
// a gc verb collide with an alias or with a cobra built-in and still pass.
func TestCommandNamesCoversAliasesAndHandCarriedNames(t *testing.T) {
	names := make(map[string]bool)
	for _, n := range CommandNames() {
		names[n] = true
	}
	for _, want := range []string{"heartbeat", "hb", "close", "done", "help", "completion", "send-metrics"} {
		if !names[want] {
			t.Errorf("CommandNames() omits %q", want)
		}
	}
	if names["release-if-current"] {
		t.Error("CommandNames() reports release-if-current; beads " + SourcedBeadsVersion +
			" registers no such command, and gc claims that name for a verb of its own")
	}
	if !sort.StringsAreSorted(CommandNames()) {
		t.Error("CommandNames() is not sorted")
	}
}

// deriveBdTopLevelCommands reads bd's cmd/bd package source and returns the
// canonical name and aliases of every command registered on its root command.
//
// It resolves `rootCmd.AddCommand(<ident>)` against package-level
// `var <ident> = &cobra.Command{...}` declarations. That shape covers every
// registration in beads today, and the narrowness is the point: a receiver
// other than rootCmd is a NESTED command whose name cannot be shadowed by an
// argv[0] intercept, so widening this to all AddCommand calls would report
// collisions that cannot happen.
//
// A registration whose name cannot be read is RETURNED, not dropped and not
// failed on here: whether it is a known oddity or a command the derivation has
// gone blind to is a question about bdCommandsNotInModuleSource, and
// TestEveryRootRegistrationIsReadableOrHandCarried is where it is asked. bd has
// two such registrations today.
func deriveBdTopLevelCommands(t *testing.T) bdSourceDerivation {
	t.Helper()

	dir := filepath.Join(beadsModuleDir(t), "cmd", "bd")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading beads cmd/bd at %s: %v", dir, err)
	}

	// Parsed file by file rather than with parser.ParseDir, which Go 1.25
	// deprecated. Its suggested replacement, golang.org/x/tools/go/packages,
	// is worse here for the reason this whole derivation is source-only: it
	// drives `go list`, and a subprocess that can be slow, sandboxed or
	// offline is a reason to skip.
	//
	// Build tags are deliberately NOT honored. Everything in the directory is
	// parsed, so a command registered only under some tag still contributes
	// its name -- the question this gate asks is whether a name could ever
	// resolve, and the safe error is to over-report bd's names, which costs a
	// gc verb a rename, not to under-report them, which costs a silent shadow.
	fset := token.NewFileSet()
	literals := make(map[string]*ast.CompositeLit)
	var registered []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", path, parseErr)
		}
		collectCobraLiterals(file, literals)
		registered = append(registered, collectRootRegistrations(t, fset, path, file)...)
	}
	if len(registered) == 0 {
		t.Fatalf("found no rootCmd.AddCommand registrations in %s; bd's registration shape has changed and this derivation is blind", dir)
	}

	out := bdSourceDerivation{
		commands:     make(map[string][]string, len(registered)),
		unresolvable: make(map[string]string),
	}
	for _, ident := range registered {
		lit, ok := literals[ident]
		if !ok {
			out.unresolvable[ident] = "no `" + ident + " = &cobra.Command{...}` binding in cmd/bd"
			continue
		}
		cmd, err := readCobraCommand(lit)
		if err != nil {
			out.unresolvable[ident] = err.Error()
			continue
		}
		// federationCmd is registered from two call sites; last write wins and
		// both name the same command.
		out.commands[cmd.name] = cmd.aliases
	}
	return out
}

// collectCobraLiterals indexes every `<ident> = &cobra.Command{...}` binding in
// file, whether declared with var or assigned with :=.
func collectCobraLiterals(file *ast.File, into map[string]*ast.CompositeLit) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch decl := n.(type) {
		case *ast.ValueSpec:
			for i, name := range decl.Names {
				if i < len(decl.Values) {
					if lit := cobraCommandLit(decl.Values[i]); lit != nil {
						into[name.Name] = lit
					}
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range decl.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(decl.Rhs) {
					continue
				}
				if lit := cobraCommandLit(decl.Rhs[i]); lit != nil {
					into[ident.Name] = lit
				}
			}
		}
		return true
	})
}

// collectRootRegistrations returns the identifiers passed to
// rootCmd.AddCommand in file, accepting both `AddCommand(x)` and
// `AddCommand(&x)` -- bd registers one hidden back-compat alias by address.
//
// An argument in neither shape fails the test on the spot rather than being
// ignored. It cannot be hand-carried like an unreadable identifier can, because
// there is no identifier to key the entry on; the only honest report is that
// the derivation has stopped seeing a registration and needs a human.
func collectRootRegistrations(t *testing.T, fset *token.FileSet, path string, file *ast.File) []string {
	t.Helper()
	var idents []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "AddCommand" {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "rootCmd" {
			return true
		}
		for _, arg := range call.Args {
			if unary, ok := arg.(*ast.UnaryExpr); ok && unary.Op == token.AND {
				arg = unary.X
			}
			ident, ok := arg.(*ast.Ident)
			if !ok {
				t.Errorf("rootCmd.AddCommand at %s:%d takes an argument that is neither an identifier nor the address of one, so neither its name nor a key to carry it under can be recovered.\n"+
					"Teach collectRootRegistrations that shape -- until then gc's collision gate believes whatever name it registers is free.",
					filepath.Base(path), fset.Position(arg.Pos()).Line)
				continue
			}
			idents = append(idents, ident.Name)
		}
		return true
	})
	return idents
}

// cobraCommandLit returns the composite literal behind e when e is a
// `&cobra.Command{...}` or `cobra.Command{...}` expression, else nil.
func cobraCommandLit(e ast.Expr) *ast.CompositeLit {
	if unary, ok := e.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		e = unary.X
	}
	lit, ok := e.(*ast.CompositeLit)
	if !ok {
		return nil
	}
	sel, ok := lit.Type.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Command" {
		return nil
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "cobra" {
		return nil
	}
	return lit
}

// readCobraCommand extracts the invoked name (the first word of Use) and the
// aliases from a cobra.Command literal.
func readCobraCommand(lit *ast.CompositeLit) (derivedCommand, error) {
	var cmd derivedCommand
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		switch key.Name {
		case "Use":
			use, ok := stringLitValue(kv.Value)
			if !ok {
				return derivedCommand{}, errNonLiteral("Use")
			}
			fields := strings.Fields(use)
			if len(fields) == 0 {
				return derivedCommand{}, errNonLiteral("Use")
			}
			cmd.name = fields[0]
		case "Aliases":
			aliases, ok := kv.Value.(*ast.CompositeLit)
			if !ok {
				return derivedCommand{}, errNonLiteral("Aliases")
			}
			for _, elt := range aliases.Elts {
				alias, ok := stringLitValue(elt)
				if !ok {
					return derivedCommand{}, errNonLiteral("Aliases")
				}
				cmd.aliases = append(cmd.aliases, alias)
			}
		}
	}
	if cmd.name == "" {
		return derivedCommand{}, errNonLiteral("Use")
	}
	return cmd, nil
}

type errNonLiteral string

func (e errNonLiteral) Error() string {
	return "field " + string(e) + " is not a string literal"
}

func stringLitValue(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

// beadsRequireRE matches the beads line in go.mod's require block, in both the
// grouped and single-line forms.
var beadsRequireRE = regexp.MustCompile(`(?m)^\s*(?:require\s+)?` + regexp.QuoteMeta(bdCommandsBeadsModulePath) + `\s+(v\S+)`)

// beadsVersionFromGoMod reads the required beads version straight out of
// go.mod. Deliberately a text read rather than `go list -m`: this runs on
// every ordinary test invocation, and a subprocess that can be slow, sandboxed
// or offline would be a reason to skip, which this gate must never do.
func beadsVersionFromGoMod(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "go.mod")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	m := beadsRequireRE.FindSubmatch(data)
	if m == nil {
		t.Fatalf("no %s require line in %s; the collision gate cannot tell which bd it is checking against", bdCommandsBeadsModulePath, path)
	}
	return string(m[1])
}

// beadsModuleDir locates the extracted beads module. The blank import above
// guarantees the go command has materialized it, so a miss here means the
// cache moved rather than that the module is absent -- reported as a failure
// with the search path named, never as a skip.
func beadsModuleDir(t *testing.T) string {
	t.Helper()
	version := beadsVersionFromGoMod(t)

	var roots []string
	if cache := strings.TrimSpace(os.Getenv("GOMODCACHE")); cache != "" {
		roots = append(roots, cache)
	}
	for _, gopath := range filepath.SplitList(build.Default.GOPATH) {
		if gopath != "" {
			roots = append(roots, filepath.Join(gopath, "pkg", "mod"))
		}
	}

	var tried []string
	for _, root := range roots {
		dir := filepath.Join(root, filepath.FromSlash(bdCommandsBeadsModulePath)+"@"+version)
		if fi, err := os.Stat(filepath.Join(dir, "cmd", "bd")); err == nil && fi.IsDir() {
			return dir
		}
		tried = append(tried, dir)
	}
	t.Fatalf("beads %s module source not found (looked in %v).\n"+
		"This gate reads bd's own source to check gc's intercepted verb names against it and must not pass without it: run `go mod download %s`.",
		version, tried, bdCommandsBeadsModulePath)
	return ""
}

// sameNames compares two name lists as sets, treating nil and empty as equal.
func sameNames(a, b []string) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	return reflect.DeepEqual(x, y)
}

// repoRoot walks up from this package to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod found walking up from the test's working directory")
		}
		dir = parent
	}
}
