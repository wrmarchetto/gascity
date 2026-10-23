//go:build (linux && !android) || (darwin && !ios)

package productmetrics

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// processWideHazards name state every test in the process shares. A test
// reaching one, directly or through a package function, must run serially.
// Every other test calls t.Parallel first: the package's cost is fsync
// latency, which overlaps across goroutines (TESTING.md).
var processWideHazards = map[string]string{
	"Setenv":                    "the process environment",
	"storageTempSequence":       "the process-wide temp-name counter",
	"closeOpenFileMatchingPath": "a raw close of a descriptor number production still owns",
	`"/proc/self/fd"`:           "the process descriptor table",
}

func TestEveryTopLevelTestRunsInParallelUnlessItReachesProcessWideState(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if match, err := build.Default.MatchFile(".", name); err != nil || !match {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	serial := serialByHazard(files)
	found := false
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") {
				continue
			}
			found = found || fn.Name.Name == "TestEveryTopLevelTestRunsInParallelUnlessItReachesProcessWideState"
			hazard, mustBeSerial := serial[fn.Name.Name]
			parallel := startsWithParallel(fn)
			if mustBeSerial && parallel {
				t.Errorf("%s (%s) reaches %s but calls t.Parallel", fn.Name.Name, fset.Position(fn.Pos()), hazard)
			}
			if !mustBeSerial && !parallel {
				t.Errorf("%s (%s): make t.Parallel() its first statement, or add the shared state it touches to processWideHazards",
					fn.Name.Name, fset.Position(fn.Pos()))
			}
		}
	}
	if !found || len(serial) == 0 {
		t.Fatalf("parsed %d files, found this test %v, derived %d serial tests", len(files), found, len(serial))
	}
}

func TestSerialByHazardFollowsPackageHelpersToEveryHazardKind(t *testing.T) {
	t.Parallel()
	// Indented so a line-anchored count of the suite's test functions skips
	// these fixture declarations.
	const source = `package p
	func helper()   { closeOpenFileMatchingPath("x") }
	func indirect() { helper() }
	func TestCounter(t *testing.T)  { storageTempSequence.Load() }
	func TestHelper(t *testing.T)   { indirect() }
	func TestTable(t *testing.T)    { os.ReadDir("/proc/self/fd") }
	func TestEnv(t *testing.T)      { t.Run("x", func(t *testing.T) { t.Setenv("A", "") }) }
	func TestUnshared(t *testing.T) { t.Parallel(); helperFree() }
	func helperFree()               {}
	`
	file, err := parser.ParseFile(token.NewFileSet(), "p_test.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := serialByHazard([]*ast.File{file})
	want := map[string]string{
		"TestCounter": processWideHazards["storageTempSequence"],
		"TestHelper":  processWideHazards["closeOpenFileMatchingPath"],
		"TestTable":   processWideHazards[`"/proc/self/fd"`],
		"TestEnv":     processWideHazards["Setenv"],
	}
	if len(got) != len(want) {
		t.Fatalf("serialByHazard = %v, want %v", got, want)
	}
	for name, hazard := range want {
		if got[name] != hazard {
			t.Errorf("serialByHazard[%s] = %q, want %q", name, got[name], hazard)
		}
	}
}

// serialByHazard maps each top-level test that reaches a processWideHazards
// entry, directly or through a package-level function, to that hazard.
func serialByHazard(files []*ast.File) map[string]string {
	bodies := map[string]*ast.BlockStmt{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Body != nil {
				bodies[fn.Name.Name] = fn.Body
			}
		}
	}
	names := make([]string, 0, len(bodies))
	for name := range bodies {
		names = append(names, name)
	}
	sort.Strings(names)
	reached := map[string]string{}
	for changed := true; changed; {
		changed = false
		for _, name := range names {
			if _, done := reached[name]; done {
				continue
			}
			if hazard := firstHazard(bodies[name], reached); hazard != "" {
				reached[name] = hazard
				changed = true
			}
		}
	}
	tests := map[string]string{}
	for name, hazard := range reached {
		if strings.HasPrefix(name, "Test") {
			tests[name] = hazard
		}
	}
	return tests
}

func firstHazard(body *ast.BlockStmt, reached map[string]string) string {
	hazard := ""
	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.Ident:
			if reason, ok := processWideHazards[node.Name]; ok {
				hazard = reason
			} else if reason, ok := reached[node.Name]; ok {
				hazard = reason
			}
		case *ast.BasicLit:
			if reason, ok := processWideHazards[node.Value]; ok {
				hazard = reason
			}
		}
		return hazard == ""
	})
	return hazard
}

func startsWithParallel(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	statement, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := statement.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "Parallel"
}
