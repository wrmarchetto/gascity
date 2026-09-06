package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scope: the structural half of ci-yulan1. It pins ONE rule across two
// packages -- every runtime.Config built from a ResolvedProvider's Env also
// sets DeclaredEnvKeys -- and nothing about hashing, which
// internal/runtime/fingerprint_declared_env_test.go owns.
//
// It exists because point fixes do not hold here. Since v6 a config-declared
// env key is a fingerprint input, so a config built from resolved.Env WITHOUT
// DeclaredEnvKeys hashes differently from the one the start path stored for
// the same session. Two such sites already existed and were found only by
// adversarial review, not by any test: startedConfigHashProvesACPTransport and
// its worker twin compared CoreFingerprint against a stored
// started_config_hash. A third site would be just as invisible -- both sides
// build cleanly, both produce a hash, and only the comparison is wrong.
//
// NO EXCEPTION LIST, deliberately. "This config is only launched with, never
// hashed" is a property of a site's CALLERS, not of the literal, and it stops
// being true the moment someone stores the hash. Setting the field where it is
// unread costs nothing, so the rule is cheaper to obey than to argue with.
//
// Run: go test ./cmd/gc/ -run ResolvedProviderEnvConfigsDeclare
func TestResolvedProviderEnvConfigsDeclareTheirEnvKeys(t *testing.T) {
	// Both packages that build a runtime.Config from a resolved provider.
	// Relative to cmd/gc, which is where this test runs.
	roots := []string{".", filepath.Join("..", "..", "internal", "api")}

	type site struct {
		file string
		line int
	}
	var missing []site
	scanned := 0

	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatalf("reading %s: %v", root, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(root, name)
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Config" {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "runtime" {
					return true
				}
				envIsResolved, declares := false, false
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
					case "Env":
						// A selector ending in `.Env` on an identifier named
						// `resolved` is the ResolvedProvider shape. Narrow on
						// purpose: an ambient env map (the merged session env)
						// is governed by envFingerprintAllow and must NOT
						// declare anything.
						if s, ok := kv.Value.(*ast.SelectorExpr); ok && s.Sel.Name == "Env" {
							if id, ok := s.X.(*ast.Ident); ok && id.Name == "resolved" {
								envIsResolved = true
							}
						}
					case "DeclaredEnvKeys":
						declares = true
					}
				}
				if envIsResolved {
					scanned++
					if !declares {
						missing = append(missing, site{path, fset.Position(lit.Pos()).Line})
					}
				}
				return true
			})
		}
	}

	// An EXACT count, not a floor. The matcher keys on an identifier spelled
	// `resolved`, so renaming that local at a site removes it from the matcher's
	// reach silently -- and a `scanned > 0` floor stays green while sites leave,
	// because the ones that remain still satisfy it. Measured: renaming
	// `resolved` to `rp` at the two worker_handle.go literals and deleting their
	// DeclaredEnvKeys lines dropped the count 5 -> 3 and passed under a floor.
	//
	// Adding or removing a site is therefore a deliberate edit to this number,
	// and the failure message says which way it moved.
	const wantSites = 5
	if scanned != wantSites {
		t.Errorf("found %d runtime.Config literals built from resolved.Env, want %d. "+
			"More: a new site was added -- confirm it sets DeclaredEnvKeys and bump "+
			"this number. FEWER: either a site was deleted, or its `resolved` local "+
			"was renamed and the matcher can no longer see it, which is the failure "+
			"this count exists to catch.", scanned, wantSites)
	}
	for _, m := range missing {
		t.Errorf("%s:%d builds a runtime.Config from resolved.Env without "+
			"DeclaredEnvKeys. Since v6 those keys are fingerprint inputs, so this "+
			"config hashes differently from the one the start path stored for the "+
			"same session. Add DeclaredEnvKeys: runtime.DeclaredEnvKeysOf(resolved.Env).",
			m.file, m.line)
	}
	t.Logf("checked %d runtime.Config literals built from resolved.Env", scanned)
}
