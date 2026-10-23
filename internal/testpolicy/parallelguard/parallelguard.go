// Package parallelguard derives, from a package's test source, which top-level
// tests reach process-wide state and so must run serially, and checks that
// every other top-level test calls t.Parallel first.
package parallelguard

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Hazards names process-wide state and why it is shared. A key is a bare
// identifier ("Setenv"), an import-path-qualified selector
// ("os/exec.Command"), or a quoted literal (`"/proc/self/fd"`). Assignments
// to package-level variables are hazards without being listed.
type Hazards map[string]string

// Check parses the test files in the working directory that match the
// current build and fails t for every top-level test on the wrong side of
// the derived serial set. It also fails if the calling test is not found or
// no test is derived serial, since either means the scan saw nothing.
func Check(t testing.TB, hazards Hazards) {
	t.Helper()
	check(t, ".", hazards)
}

func check(t testing.TB, dir string, hazards Hazards) {
	t.Helper()
	fset := token.NewFileSet()
	tests, declared, err := parsePackage(fset, dir)
	if err != nil {
		t.Fatal(err)
	}
	serial := Serial(tests, declared, hazards)
	caller := strings.SplitN(t.Name(), "/", 2)[0]
	found := false
	for _, file := range tests {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Name.Name == "TestMain" {
				continue
			}
			found = found || fn.Name.Name == caller
			hazard, mustBeSerial := serial[fn.Name.Name]
			parallel := StartsWithParallel(fn)
			if mustBeSerial && parallel {
				t.Errorf("%s (%s) reaches %s but calls t.Parallel", fn.Name.Name, fset.Position(fn.Pos()), hazard)
			}
			if !mustBeSerial && !parallel {
				t.Errorf("%s (%s): make t.Parallel() its first statement, or add the shared state it touches to the package's hazards",
					fn.Name.Name, fset.Position(fn.Pos()))
			}
		}
	}
	if !found || len(serial) == 0 {
		t.Fatalf("parsed %d test files, found %s %v, derived %d serial tests", len(tests), caller, found, len(serial))
	}
}

// parsePackage returns the build-matching test files in dir, and the names of
// the package-level variables declared across all its build-matching files.
func parsePackage(fset *token.FileSet, dir string) ([]*ast.File, map[string]bool, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	var tests []*ast.File
	declared := map[string]bool{}
	for _, name := range names {
		if match, err := build.Default.MatchFile(dir, filepath.Base(name)); err != nil || !match {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, nil, err
		}
		for name := range PackageVars(file) {
			declared[name] = true
		}
		if strings.HasSuffix(name, "_test.go") {
			tests = append(tests, file)
		}
	}
	return tests, declared, nil
}

// PackageVars returns the names of the package-level variables file declares.
func PackageVars(file *ast.File) map[string]bool {
	vars := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				if name.Name != "_" {
					vars[name.Name] = true
				}
			}
		}
	}
	return vars
}

// Serial maps each top-level test in files that reaches a hazard, directly or
// through a function or method declared in files, to that hazard. Methods
// are followed by name, so a name shared by two receivers over-approximates.
func Serial(files []*ast.File, declared map[string]bool, hazards Hazards) map[string]string {
	type body struct {
		block   *ast.BlockStmt
		imports map[string]string
	}
	bodies := map[string][]body{}
	for _, file := range files {
		imports := importNames(file)
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = append(bodies[fn.Name.Name], body{fn.Body, imports})
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
			for _, b := range bodies[name] {
				if hazard := firstHazard(b.block, b.imports, declared, hazards, reached); hazard != "" {
					reached[name] = hazard
					changed = true
					break
				}
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

// importNames maps each import's local name in file to its path.
func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = path
	}
	return names
}

func firstHazard(block *ast.BlockStmt, imports map[string]string, declared map[string]bool, hazards Hazards, reached map[string]string) string {
	hazard := ""
	assigns := func(expr ast.Expr) {
		if root := rootIdent(expr); root != nil && declared[root.Name] {
			hazard = "package var " + root.Name
		}
	}
	ast.Inspect(block, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				for _, lhs := range node.Lhs {
					assigns(lhs)
				}
			}
		case *ast.IncDecStmt:
			assigns(node.X)
		case *ast.SelectorExpr:
			if pkg, ok := node.X.(*ast.Ident); ok {
				if path, ok := imports[pkg.Name]; ok {
					if reason, ok := hazards[path+"."+node.Sel.Name]; ok {
						hazard = reason
					}
				}
			}
		case *ast.Ident:
			if reason, ok := hazards[node.Name]; ok {
				hazard = reason
			} else if reason, ok := reached[node.Name]; ok {
				hazard = reason
			}
		case *ast.BasicLit:
			if reason, ok := hazards[node.Value]; ok {
				hazard = reason
			}
		}
		return hazard == ""
	})
	return hazard
}

// rootIdent returns the variable an assignment target writes through:
// x for x, x.f, x[i] and *x.
func rootIdent(expr ast.Expr) *ast.Ident {
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		default:
			return nil
		}
	}
}

// StartsWithParallel reports whether fn's first statement is a call to a
// method named Parallel.
func StartsWithParallel(fn *ast.FuncDecl) bool {
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
