// Repo-wide structural guard over every child environment that names its own
// HOME. It lives in execenv because that is where the remedy lives; it asserts
// nothing about this package.
//
// Run it with: go test ./internal/execenv/ -run TestEveryHomeRewriteDefaultsBDMetricsOff
package execenv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// homeLiteralExemptCallees are the string functions that INSPECT an existing
// entry rather than build one. A "HOME=" literal handed to one of these is a
// read, so it is not a rewrite and carries no obligation.
var homeLiteralExemptCallees = map[string]bool{
	"HasPrefix":  true,
	"TrimPrefix": true,
	"CutPrefix":  true,
}

// TestEveryHomeRewriteDefaultsBDMetricsOff pins the invariant that a child
// environment naming its own HOME also defaults bd's telemetry off.
//
// The invariant exists because bd reads the operator's `bd metrics off` out of
// $HOME and silently re-enables itself when it cannot find it (see
// WithBDMetricsDefaultedOff). The pairing is structural rather than a list of
// known call sites: gc grew four HOME-rewriting environments independently, in
// four packages, none of them aware of bd's config lookup, and a denylist would
// have named the three that existed when it was written. This fails on the
// fifth.
//
// WHAT IT CANNOT SEE, and why the coverage is still worth having: the match is
// on a string literal whose value starts with "HOME=", so an entry assembled
// through a variable or a helper in another package is invisible to it. Every
// HOME rewrite in the tree today is written as a literal, and a reviewer who
// reaches for an indirection to build one is far outside the shape this
// catches. It also reads the file, not the function -- a file with two env
// builders satisfies it when either one uses the helper. Both are deliberate:
// a per-function rule needs call-graph reasoning to survive a builder that
// delegates, and that machinery rots faster than the thing it guards.
func TestEveryHomeRewriteDefaultsBDMetricsOff(t *testing.T) {
	root := moduleRoot(t)

	var offenders []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // path comes from WalkDir over the module root
		if err != nil {
			return err
		}
		if !fileRewritesHome(t, path, src) {
			return nil
		}
		if strings.Contains(string(src), "WithBDMetricsDefaultedOff") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		offenders = append(offenders, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	if len(offenders) > 0 {
		t.Errorf("these files build a child environment with their own HOME but do not default bd telemetry off:\n  %s\n\n"+
			"bd reads the operator's metrics opt-out from $HOME, so a child under a substituted HOME finds no opt-out and "+
			"falls back to bd's shipped default of ENABLED.\n"+
			"Remedy: wrap the assembled environment in execenv.WithBDMetricsDefaultedOff(env) before handing it to exec.Cmd.",
			strings.Join(offenders, "\n  "))
	}
}

// fileRewritesHome reports whether src builds an environment entry naming its
// own HOME, ignoring literals that are only used to inspect an existing entry.
func fileRewritesHome(t *testing.T, path string, src []byte) bool {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}

	exempt := map[ast.Node]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !homeLiteralExemptCallees[sel.Sel.Name] {
			return true
		}
		for _, arg := range call.Args {
			exempt[arg] = true
		}
		return true
	})

	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || exempt[ast.Node(lit)] {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if strings.HasPrefix(value, "HOME=") {
			found = true
		}
		return true
	})
	return found
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod. The test needs the whole tree, and `go test` starts it in its own
// package directory.
func moduleRoot(t *testing.T) string {
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
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
