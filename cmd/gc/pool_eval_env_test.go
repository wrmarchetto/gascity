package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	gcruntime "github.com/gastownhall/gascity/internal/runtime"
)

// TestNamedSessionBackingPoolScaleCheckSeesItsRouteTargets drives the whole
// controller tick and reads GC_ROUTE_TARGETS from inside the scale_check
// subprocess, because that subprocess is the only place the defect is visible.
//
// The failure it pins is ci-43krjf: build_desired_state.go's named-session
// branch appended a poolEvalWork with no env, so the probe inherited the
// controller's own environment. A custom scale_check that iterates
// GC_ROUTE_TARGETS then served zero targets and the pool sized 0 against
// claimable work -- the ci-vk76d1 shape, with nothing red.
//
// The scale_check is a comparison, not a presence test, and the test seeds the
// controller environment with a WRONG value first. A presence test passes on
// the inherited variable, which is exactly the state the defect produces on a
// host where anything upstream exported one.
func TestNamedSessionBackingPoolScaleCheckSeesItsRouteTargets(t *testing.T) {
	t.Setenv(routeTargetsEnvKey, "inherited-not-computed")

	cityPath := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:         "lab",
			StartCommand: "true",
			// Emits the pool's demand only when the probe environment carries
			// the agent's own computed target list.
			ScaleCheck: `[ "$GC_ROUTE_TARGETS" = "lab" ] && echo 1 || echo 0`,
		}},
		NamedSessions: []config.NamedSession{{Template: "lab", Mode: "on_demand"}},
	}

	var stderr bytes.Buffer
	result := buildDesiredState("test-city", cityPath, time.Now(), cfg, gcruntime.NewFake(), beads.NewMemStore(), &stderr)

	if got := result.ScaleCheckCounts["lab"]; got != 1 {
		t.Fatalf("ScaleCheckCounts[lab] = %d, want 1; the named-session branch's scale_check probe did not receive %s (stderr: %s)",
			got, routeTargetsEnvKey, stderr.String())
	}
}

// TestGenericPoolScaleCheckSeesItsRouteTargets is the control for the test
// above, and it is not redundant with it.
//
// It is the SAME agent, the same scale_check and the same expected target
// list; the only difference is the [[named_session]] declaration. Without it a
// red above is unattributable -- a wrong expected spelling, a probe that never
// ran, and the env-less branch all produce ScaleCheckCounts = 0 and look
// identical from outside. This pins the comparison string and the probe
// machinery as correct, so the other test's red can only be the branch.
func TestGenericPoolScaleCheckSeesItsRouteTargets(t *testing.T) {
	t.Setenv(routeTargetsEnvKey, "inherited-not-computed")

	cityPath := t.TempDir()
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Agents: []config.Agent{{
			Name:         "lab",
			StartCommand: "true",
			ScaleCheck:   `[ "$GC_ROUTE_TARGETS" = "lab" ] && echo 1 || echo 0`,
		}},
	}

	var stderr bytes.Buffer
	result := buildDesiredState("test-city", cityPath, time.Now(), cfg, gcruntime.NewFake(), beads.NewMemStore(), &stderr)

	if got := result.ScaleCheckCounts["lab"]; got != 1 {
		t.Fatalf("ScaleCheckCounts[lab] = %d, want 1; the fixture's expected target list or the probe itself is wrong, so the named-session test above cannot attribute its result (stderr: %s)",
			got, stderr.String())
	}
}

// productionGoFilesInPackageDir returns the parsed non-test .go files beside
// the caller, so a structural guard reads the package instead of a list of
// paths restated in a test.
func productionGoFilesInPackageDir(t *testing.T) (*token.FileSet, []*ast.File) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%q): %v", name, err)
		}
		files = append(files, file)
	}
	return fset, files
}

// isNilLiteral reports whether an expression is the bare identifier nil, which
// is how an env-less construction spells itself when it is written out
// explicitly rather than omitted.
func isNilLiteral(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "nil"
}

// TestPoolEvalWorkIsNeverConstructedWithoutACommandEnv is the structural half,
// and it replaces a guard that could not see this defect.
//
// TestRouteTargetsEnvIsExportedAtEveryCommandEnvCallSite forbids direct
// controllerQueryRuntimeEnv calls outside two named functions. That gate is
// keyed on a CALL: a branch that calls neither function is not a violation of
// it, it is outside what it parses. build_desired_state.go's named-session
// branch was exactly that -- it constructed a poolEvalWork with no env field
// at all -- and the older gate stayed green (ci-43krjf). Its own header had
// already warned that "the wired call sites are not the guarantee -- the next
// one added is"; the next one added called nothing.
//
// So this one is keyed on the STRUCT being fed, not on who feeds it. Every
// poolEvalWork carries an env because evaluatePendingPools hands pw.env
// straight to the scale_check subprocess, where a missing map silently
// degrades to the controller's own environment rather than erroring.
//
// A bare `env: nil` is rejected as well as an omitted field. Writing the zero
// value out does not make it a decision, and the whole failure mode is that
// the nil is invisible downstream.
func TestPoolEvalWorkIsNeverConstructedWithoutACommandEnv(t *testing.T) {
	fset, files := productionGoFilesInPackageDir(t)

	found := 0
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			lit, isLit := n.(*ast.CompositeLit)
			if !isLit {
				return true
			}
			ident, isIdent := lit.Type.(*ast.Ident)
			if !isIdent || ident.Name != "poolEvalWork" {
				return true
			}
			found++
			for _, elt := range lit.Elts {
				kv, isKV := elt.(*ast.KeyValueExpr)
				if !isKV {
					continue
				}
				key, isKey := kv.Key.(*ast.Ident)
				if !isKey || key.Name != "env" {
					continue
				}
				if isNilLiteral(kv.Value) {
					t.Errorf("%s: poolEvalWork constructed with env: nil; its scale_check probe would inherit the controller environment and serve no %s",
						fset.Position(kv.Pos()), routeTargetsEnvKey)
				}
				return true
			}
			t.Errorf("%s: poolEvalWork constructed with no env field; build it with controllerAgentCommandEnv or its scale_check probe serves no %s",
				fset.Position(lit.Pos()), routeTargetsEnvKey)
			return true
		})
	}

	// Two: the named-session backing branch and the generic pool branch, both
	// in build_desired_state.go. A drift to zero means the parse stopped
	// matching and this gate has silently stopped gating.
	if found != 2 {
		t.Errorf("found %d production poolEvalWork constructions, want 2; update this count deliberately when one is added or removed", found)
	}
}

// TestCityScopedFanOutProbesIsNeverPassedANilOwnEnv guards the other half of
// the same construction.
//
// The fan-out's own "city" leg takes ownEnv verbatim (pool_scale_check_fanout.go),
// so a nil there reproduces the poolEvalWork defect one level down and on the
// same probe. Only the RIG legs recover, because rigScopedProbeEnv rebuilds
// them through controllerAgentCommandEnv -- which means a city with no rigs
// configured has no leg that survives, and a city with rigs has a fan-out sum
// that is wrong by exactly its city term. Neither reports anything.
//
// Production only. The test file deliberately passes nil at
// pool_scale_check_fanout_test.go:55 to pin rigScopedProbeEnv's fallback, and
// that is the case being tested rather than a call site being written.
func TestCityScopedFanOutProbesIsNeverPassedANilOwnEnv(t *testing.T) {
	fset, files := productionGoFilesInPackageDir(t)

	const ownEnvArgIndex = 4

	found := 0
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			ident, isIdent := call.Fun.(*ast.Ident)
			if !isIdent || ident.Name != "cityScopedFanOutProbes" {
				return true
			}
			found++
			if len(call.Args) <= ownEnvArgIndex {
				t.Errorf("%s: cityScopedFanOutProbes called with %d arguments; this gate reads argument %d as ownEnv and must be updated with the signature",
					fset.Position(call.Pos()), len(call.Args), ownEnvArgIndex)
				return true
			}
			if isNilLiteral(call.Args[ownEnvArgIndex]) {
				t.Errorf("%s: cityScopedFanOutProbes passed a nil ownEnv; its city leg would run the scale_check with no %s",
					fset.Position(call.Args[ownEnvArgIndex].Pos()), routeTargetsEnvKey)
			}
			return true
		})
	}

	if found != 2 {
		t.Errorf("found %d production cityScopedFanOutProbes call sites, want 2; update this count deliberately when one is added or removed", found)
	}
}

// TestRigScopedNamedSessionBackingPoolScaleCheckSeesItsRouteTargets covers the
// half of the same branch that the city-scoped test above CANNOT reach, and
// the split is not cosmetic.
//
// evaluatePendingPools reads poolEvalWork.env only on the non-fan-out arm:
// with probes present it runs evaluatePoolFanOutSum over probe.env instead,
// and pw.env survives only as the host/port shell prefix. cityScopedFanOutProbes
// is called only when the agent is unrigged, so a city-scoped fixture proves
// the fan-out's ownEnv and says nothing about the struct field. A mutation
// sweep caught exactly that: dropping `env:` from the poolEvalWork literal left
// the city-scoped test green and only the AST guard red.
//
// A rig-scoped backing template takes the else arm, where pw.env IS the probe
// environment. Same assertion, different load-bearing value.
func TestRigScopedNamedSessionBackingPoolScaleCheckSeesItsRouteTargets(t *testing.T) {
	t.Setenv(routeTargetsEnvKey, "inherited-not-computed")

	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "dart")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "dart", Path: rigPath}},
		Agents: []config.Agent{{
			Name:         "lab",
			Dir:          "dart",
			StartCommand: "true",
			ScaleCheck:   `[ "$GC_ROUTE_TARGETS" = "dart/lab" ] && echo 1 || echo 0`,
			// A custom work_query keeps the generic cold-wake fallback out of
			// this fixture's demand, so the asserted count comes from the
			// scale_check subprocess alone.
			WorkQuery: "true",
		}},
		NamedSessions: []config.NamedSession{{Template: "lab", Dir: "dart", Mode: "on_demand"}},
	}

	var stderr bytes.Buffer
	result := buildDesiredState("test-city", cityPath, time.Now(), cfg, gcruntime.NewFake(), beads.NewMemStore(), &stderr)

	if got := result.ScaleCheckCounts["dart/lab"]; got != 1 {
		t.Fatalf("ScaleCheckCounts[dart/lab] = %d, want 1; poolEvalWork.env did not reach the scale_check probe (stderr: %s)",
			got, stderr.String())
	}
}
