// Package parallelguard derives which top-level tests of a package reach
// process-wide state, and so must run serially, and checks that every other
// one calls t.Parallel first. It reads only the package's own source: a call
// into another package, or a method that mutates its receiver, is not seen.
package parallelguard

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// Hazards names state a package's tests share, and why. A key is a bare
// identifier ("storageTempSequence"), an import-path-qualified selector
// ("os/exec.Command"), or a quoted literal (`"/proc/self/fd"`). Keys match in
// test files only, so production code may use what a test must not predict.
type Hazards map[string]string

// processWide names calls that change state every goroutine in the process
// shares. They match in production code too, and no package lists them.
var processWide = Hazards{
	"Setenv":                  "the process environment",
	"Unsetenv":                "the process environment",
	"Clearenv":                "the process environment",
	"Chdir":                   "the process working directory",
	"Fchdir":                  "the process working directory",
	"Umask":                   "the process file-creation mask",
	"Setrlimit":               "a process resource limit",
	"Prlimit":                 "a process resource limit",
	"os/signal.Notify":        "process signal delivery",
	"os/signal.NotifyContext": "process signal delivery",
	"os/signal.Ignore":        "process signal delivery",
	"os/signal.Reset":         "process signal delivery",
}

// Check parses the package in the working directory, as the current build
// selects it, and fails t for every top-level test on the wrong side of the
// derived serial set. It also fails if the calling test is not found or no
// test is derived serial, since either means the scan saw nothing.
func Check(t testing.TB, hazards Hazards) {
	t.Helper()
	check(t, ".", hazards)
}

func check(t testing.TB, dir string, hazards Hazards) {
	t.Helper()
	fset := token.NewFileSet()
	tests, production, err := parsePackage(fset, dir)
	if err != nil {
		t.Fatal(err)
	}
	serial := Serial(tests, production, hazards)
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

// parsePackage returns the build-matching files in dir, split into test
// files and production files.
func parsePackage(fset *token.FileSet, dir string) (tests, production []*ast.File, err error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, nil, err
	}
	for _, name := range names {
		if match, err := build.Default.MatchFile(dir, filepath.Base(name)); err != nil || !match {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			return nil, nil, err
		}
		if strings.HasSuffix(name, "_test.go") {
			tests = append(tests, file)
		} else {
			production = append(production, file)
		}
	}
	return tests, production, nil
}

// packageVars returns the names of the package-level variables file declares.
func packageVars(file *ast.File) map[string]bool {
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

// body is one function body or package-level initializer the walk follows.
type body struct {
	node    ast.Node
	imports map[string]string // local import name to path, for its file
	test    bool              // from a test file: Hazards and var writes count
}

// Serial maps each top-level test in tests that reaches a hazard to it,
// through any function, method or package-level initializer in either file
// set. A package-var write counts in tests only. Methods are followed by
// name, so a name shared by two receivers over-approximates.
func Serial(tests, production []*ast.File, hazards Hazards) map[string]string {
	declared := map[string]bool{}
	for _, files := range [][]*ast.File{tests, production} {
		for _, file := range files {
			maps.Copy(declared, packageVars(file))
		}
	}
	inTests := maps.Clone(processWide)
	maps.Copy(inTests, hazards)
	bodies := map[string][]body{}
	collectBodies(bodies, tests, true)
	collectBodies(bodies, production, false)
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
				matched, vars := processWide, map[string]bool(nil)
				if b.test {
					matched, vars = inTests, declared
				}
				if hazard := firstHazard(b.node, b.imports, vars, matched, reached); hazard != "" {
					reached[name] = hazard
					changed = true
					break
				}
			}
		}
	}
	serial := map[string]string{}
	for name, hazard := range reached {
		if strings.HasPrefix(name, "Test") {
			serial[name] = hazard
		}
	}
	return serial
}

// collectBodies adds each function body and each package-level const or var
// initializer in files to bodies, under the name it is reached by.
func collectBodies(bodies map[string][]body, files []*ast.File, test bool) {
	for _, file := range files {
		imports := importNames(file)
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Body != nil {
					bodies[decl.Name.Name] = append(bodies[decl.Name.Name], body{decl.Body, imports, test})
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					value, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, name := range value.Names {
						for _, initializer := range value.Values {
							bodies[name.Name] = append(bodies[name.Name], body{initializer, imports, test})
						}
					}
				}
			}
		}
	}
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

// firstHazard returns the first hazard node reaches, or "". A write to a
// package var counts only when vars is non-nil.
func firstHazard(
	node ast.Node, imports map[string]string, vars map[string]bool, hazards Hazards, reached map[string]string,
) string {
	hazard := ""
	writes := func(expr ast.Expr) {
		if name := writtenVar(expr, imports, vars); name != "" {
			hazard = "package var " + name
		}
	}
	ast.Inspect(node, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				for _, lhs := range node.Lhs {
					writes(lhs)
				}
			}
		case *ast.IncDecStmt:
			writes(node.X)
		case *ast.UnaryExpr:
			if node.Op == token.AND {
				writes(node.X)
			}
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

// writtenVar names the package var a write through expr changes, or "": x
// for x, x.f, x[i] and *x when vars holds x, and path.V for another
// package's V. A taken address is a write, since it can be written through.
func writtenVar(expr ast.Expr, imports map[string]string, vars map[string]bool) string {
	if vars == nil {
		return ""
	}
	selected := ""
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			if vars[e.Name] {
				return e.Name
			}
			if path, ok := imports[e.Name]; ok && selected != "" {
				return path + "." + selected
			}
			return ""
		case *ast.SelectorExpr:
			selected, expr = e.Sel.Name, e.X
		case *ast.IndexExpr:
			expr = e.X
		case *ast.StarExpr:
			expr = e.X
		case *ast.ParenExpr:
			expr = e.X
		default:
			return ""
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
