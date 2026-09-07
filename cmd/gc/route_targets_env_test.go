package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// newRouteTargetsFixture builds a rig-scoped pool agent whose single
// claim_routes entry is a TEMPLATE, not a concrete route. The template is the
// point: ci-vk76d1's sibling gate was green while the outage ran because it
// compared against an already-expanded literal, so every assertion below is
// written to fail on a raw "{{.Rig}}" reaching the exported value.
func newRouteTargetsFixture(t *testing.T) (string, *config.City) {
	t.Helper()
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "dart")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "dart", Path: rigPath}},
		Agents: []config.Agent{{
			Name:        "lab.engineer-codex",
			Scope:       "rig",
			Dir:         "dart",
			ClaimRoutes: []string{"{{.Rig}}/lab.engineer"},
			ScaleCheck:  "true",
			WorkQuery:   "true",
		}},
	}
	return cityPath, cfg
}

// TestRouteTargetsEnvAgreesAcrossClaimAndScaleCheckPaths pins the whole point
// of GC_ROUTE_TARGETS: the session's claim env and the controller's
// scale_check probe env carry the SAME target list for the same agent.
//
// A query that has to be told its targets by a config literal is the drift
// this replaces (ci-vk76d1: 44 minutes of a sized-0 pool holding claimable
// work). Exporting the list from two places instead of one would trade that
// config drift for a code drift with no gate over it, so the equality is
// asserted rather than each path's value being checked alone.
func TestRouteTargetsEnvAgreesAcrossClaimAndScaleCheckPaths(t *testing.T) {
	cityPath, cfg := newRouteTargetsFixture(t)

	claimEnv, err := controllerWorkQueryEnv(cityPath, cfg, &cfg.Agents[0])
	if err != nil {
		t.Fatalf("controllerWorkQueryEnv() error = %v, want nil", err)
	}
	probeEnv, err := controllerAgentCommandEnv(cityPath, cfg, &cfg.Agents[0])
	if err != nil {
		t.Fatalf("controllerAgentCommandEnv() error = %v, want nil", err)
	}

	claim := claimEnv[routeTargetsEnvKey]
	probe := probeEnv[routeTargetsEnvKey]
	if claim == "" {
		t.Fatalf("%s absent from the claim work_query env", routeTargetsEnvKey)
	}
	if claim != probe {
		t.Fatalf("%s claim path = %q, scale_check path = %q; the two must derive from one function",
			routeTargetsEnvKey, claim, probe)
	}
}

// TestRouteTargetsEnvCarriesEveryTargetGCWillClaimOn pins that the exported
// list is not NARROWER than the set gc claims fresh unassigned routed work on.
// Narrower is the failure mode: a query serving a subset hides work gc would
// take, and nothing goes red on either side.
//
// The expectation is built the way cmd_hook.go builds claimOpts.RouteTargets,
// INCLUDING the two session-runtime fallbacks it appends -- the resolved agent
// name and GC_TEMPLATE. For a TEMPLATE agent both collapse to the primary
// target, and that collapse is what lets GC_ROUTE_TARGETS be a pure function
// of config. If RoutedToIdentity ever stops agreeing with QualifiedName for a
// template agent, this goes red instead of the export silently becoming a
// subset. The pool-instance case, where the resolved name does NOT collapse,
// is TestRouteTargetsEnvOmitsTheSlotSuffixedRuntimeSpelling.
func TestRouteTargetsEnvCarriesEveryTargetGCWillClaimOn(t *testing.T) {
	cityPath, cfg := newRouteTargetsFixture(t)
	a := &cfg.Agents[0]
	cityName := loadedCityName(cfg, cityPath)

	claimEnv, err := controllerWorkQueryEnv(cityPath, cfg, a)
	if err != nil {
		t.Fatalf("controllerWorkQueryEnv() error = %v, want nil", err)
	}
	exported := strings.Split(claimEnv[routeTargetsEnvKey], "\n")

	want := hookClaimAgentRouteTargets(
		a,
		expandHookClaimRoutes(cityPath, cityName, a, cfg.Rigs, nil),
		[]string{a.QualifiedName(), a.QualifiedName()},
	)
	if len(want) != len(exported) {
		t.Fatalf("%s = %q, want the %d claim targets %q",
			routeTargetsEnvKey, exported, len(want), want)
	}
	for i := range want {
		if want[i] != exported[i] {
			t.Fatalf("%s[%d] = %q, want %q (full: %q)", routeTargetsEnvKey, i, exported[i], want[i], exported)
		}
	}

	// The primary target and the expanded shared route, named concretely. The
	// equality above would still hold if both sides expanded to nothing.
	if exported[0] != "dart/lab.engineer-codex" {
		t.Errorf("%s[0] = %q, want the agent's own pool route dart/lab.engineer-codex", routeTargetsEnvKey, exported[0])
	}
	if len(exported) < 2 || exported[1] != "dart/lab.engineer" {
		t.Errorf("%s = %q, want the expanded claim route dart/lab.engineer", routeTargetsEnvKey, exported)
	}
	for _, target := range exported {
		if strings.Contains(target, "{{") {
			t.Errorf("%s = %q carries an unexpanded template; a query iterating it would match nothing",
				routeTargetsEnvKey, exported)
		}
	}
}

// TestRouteTargetsEnvSurvivesAnUnmanagedStoreContract pins the empty-map case.
// controllerQueryRuntimeEnv returns a nil map for any scope not on the managed
// bd store contract, which is the ordinary state of a plain city directory. An
// env builder that only decorates the map it was given exports nothing there,
// and the pool sizing that reads it goes back to guessing -- silently, because
// a nil map and an absent variable are the same thing to the subprocess.
func TestRouteTargetsEnvSurvivesAnUnmanagedStoreContract(t *testing.T) {
	cityPath, cfg := newRouteTargetsFixture(t)
	// A file-backed beads provider is off the managed bd store contract, which
	// is what makes controllerQueryRuntimeEnv answer nil. Mirrors
	// TestControllerQueryRuntimeEnvReturnsNilForNonBD.
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")

	runtimeEnv, err := controllerQueryRuntimeEnv(cityPath, cfg, &cfg.Agents[0])
	if err != nil {
		t.Fatalf("controllerQueryRuntimeEnv() error = %v, want nil", err)
	}
	if runtimeEnv != nil {
		t.Fatalf("fixture city is on the managed bd store contract (env = %#v); this case needs the nil-map path and the fixture must be adjusted, not skipped", runtimeEnv)
	}

	probeEnv, err := controllerAgentCommandEnv(cityPath, cfg, &cfg.Agents[0])
	if err != nil {
		t.Fatalf("controllerAgentCommandEnv() error = %v, want nil", err)
	}
	if probeEnv[routeTargetsEnvKey] == "" {
		t.Fatalf("%s absent from a command env built over a nil runtime env", routeTargetsEnvKey)
	}
}

// TestRouteTargetsEnvIsExportedAtEveryCommandEnvCallSite is the mechanical half.
//
// The wired call sites are not the guarantee -- the next one added is. A
// production path that builds an agent command environment straight from
// controllerQueryRuntimeEnv exports no GC_ROUTE_TARGETS, and every existing
// test stays green because none of them reads the variable.
//
// The call sites are found by parsing the package rather than listed here: a
// list restated in a test is a copy of the thing it checks. The count is
// asserted too, so a parse that stops matching fails loudly instead of
// vacuously passing. Mirrors TestPoolClaimRoutesExpandedAtEveryComputeCallSite.
func TestRouteTargetsEnvIsExportedAtEveryCommandEnvCallSite(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(currentFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}

	// The two functions ALLOWED to call controllerQueryRuntimeEnv directly.
	//
	// controllerAgentCommandEnv is the builder: it is where the export happens.
	// controllerQueryEnv is exempt because it derives the shell-PREFIX subset
	// (host/port only, deliberately -- a credential serialized into a command
	// string is visible in a process listing) and builds no subprocess
	// environment at all, so it has nothing to export. Every other caller,
	// controllerWorkQueryEnv included, must go through the builder.
	allowed := map[string]bool{
		"controllerAgentCommandEnv": true,
		"controllerQueryEnv":        true,
	}

	fset := token.NewFileSet()
	found := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("ParseFile(%q): %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, isFn := decl.(*ast.FuncDecl)
			if !isFn {
				continue
			}
			enclosing := fn.Name.Name
			ast.Inspect(fn, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				ident, isIdent := call.Fun.(*ast.Ident)
				if !isIdent || ident.Name != "controllerQueryRuntimeEnv" {
					return true
				}
				found++
				if !allowed[enclosing] {
					t.Errorf("%s: %s calls controllerQueryRuntimeEnv directly; use controllerAgentCommandEnv or %s is never exported",
						fset.Position(call.Pos()), enclosing, routeTargetsEnvKey)
				}
				return true
			})
		}
	}

	// Two, not the one a read of the builder predicts. This assertion is what
	// found the on_boot and on_death environments -- neither is a probe, both
	// build a controller-side agent command environment, and a read of the
	// work_query path does not reach them. A drift to zero means the parse
	// stopped matching and the gate has silently stopped gating.
	if found != 2 {
		t.Errorf("found %d production controllerQueryRuntimeEnv call sites, want 2; update this count deliberately when one is added or removed", found)
	}
}

// TestRouteTargetsEnvIsConstantAcrossFederatedHookStores pins GC_ROUTE_TARGETS
// as a per-AGENT value, not a per-store one.
//
// A city-scoped agent's hook federates one store per rig, and each entry's env
// is built from a per-rig VIEW of the agent (view.Dir = rigName) so bd reads
// the rig's store. Recomputing the route targets from that view expands
// "{{.Rig}}" against the rig being read instead of the agent's own scope, so
// the query would serve a different target list per store -- offering a bead
// gc then refuses to claim, which bounces the slot claim/drain/respawn. That
// is the inverse of the narrowing failure GC_ROUTE_TARGETS exists to close and
// bounces the pool the same way.
//
// The fix is hookIdentityEnvKeys, whose stated contract is exactly this: the
// values that must stay constant across every federated store attempt because
// the query always matches the agent's OWN identity regardless of which store
// it reads.
func TestRouteTargetsEnvIsConstantAcrossFederatedHookStores(t *testing.T) {
	cityPath := t.TempDir()
	rigPath := filepath.Join(cityPath, "rigs", "dart")
	if err := os.MkdirAll(rigPath, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "test-city"},
		Rigs:      []config.Rig{{Name: "dart", Path: rigPath}},
		Agents: []config.Agent{{
			Name:        "toolsmith",
			ClaimRoutes: []string{"{{.Rig}}/lab.engineer"},
		}},
	}
	a := &cfg.Agents[0]

	overrides, err := hookQueryEnv(cityPath, cfg, a)
	if err != nil {
		t.Fatalf("hookQueryEnv() error = %v, want nil", err)
	}
	own := overrides[routeTargetsEnvKey]
	if own == "" {
		t.Fatalf("%s absent from the agent's own hook store env", routeTargetsEnvKey)
	}

	stores := appendRigHookStores(nil, cityPath, cfg, a, overrides)
	if len(stores) != 1 {
		t.Fatalf("appendRigHookStores() = %d stores, want 1 for the single configured rig", len(stores))
	}
	for _, store := range stores {
		got, ok := lookupEnvValue(store.env, routeTargetsEnvKey)
		if !ok {
			t.Fatalf("%s absent from federated store %q", routeTargetsEnvKey, store.dir)
		}
		if got != own {
			t.Fatalf("federated store %q exports %s = %q, want the agent's own %q",
				store.dir, routeTargetsEnvKey, got, own)
		}
	}
}

// lookupEnvValue reads one key out of an exec-style environ slice, returning
// the LAST assignment because that is the one exec honors.
func lookupEnvValue(environ []string, key string) (string, bool) {
	prefix := key + "="
	value := ""
	found := false
	for _, entry := range environ {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
			found = true
		}
	}
	return value, found
}

// TestRouteTargetsEnvOmitsTheSlotSuffixedRuntimeSpelling pins the ONE
// documented absence, so it stays a decision rather than decaying into a gap
// nobody re-derives.
//
// resolveAgentIdentity answers a slot-suffixed input with a pool INSTANCE
// (deepCopyAgent sets PoolName to the base and Name to the slot), so
// cmd_hook.go's resolvedAgentName fallback is "<rig>/<pool>-2" while the
// primary route target stays the base. GC_ROUTE_TARGETS carries the base and
// not the slot: the controller has neither a session nor a slot number, and a
// value that differed between the two paths for one agent is exactly the code
// drift this whole change exists to prevent.
//
// The absence is safe because no writer routes work to a slot spelling on
// purpose -- agentutil.NormalizePoolRouteTarget collapses it at the sling
// write site -- and because a query wanting the runtime spelling reads
// GC_AGENT. This test fails if either half stops holding: if the slot spelling
// starts appearing in the export (the two paths have diverged) or if the base
// stops appearing (the export has gone narrower than the claim).
func TestRouteTargetsEnvOmitsTheSlotSuffixedRuntimeSpelling(t *testing.T) {
	cityPath, cfg := newRouteTargetsFixture(t)
	slots := 2
	cfg.Agents[0].MaxActiveSessions = &slots

	instance, ok := resolveAgentIdentity(cfg, "dart/lab.engineer-codex-2", "")
	if !ok {
		t.Fatal(`resolveAgentIdentity("dart/lab.engineer-codex-2") did not resolve; fixture is no longer a pool`)
	}
	if instance.QualifiedName() != "dart/lab.engineer-codex-2" {
		t.Fatalf("resolved QualifiedName = %q, want the slot spelling", instance.QualifiedName())
	}
	if instance.PoolName != "dart/lab.engineer-codex" {
		t.Fatalf("resolved PoolName = %q, want the base pool route", instance.PoolName)
	}

	env, err := controllerWorkQueryEnv(cityPath, cfg, &instance)
	if err != nil {
		t.Fatalf("controllerWorkQueryEnv() error = %v, want nil", err)
	}
	exported := strings.Split(env[routeTargetsEnvKey], "\n")
	for _, target := range exported {
		if target == instance.QualifiedName() {
			t.Fatalf("%s = %q carries the slot spelling; the controller cannot know it, so the two paths now disagree",
				routeTargetsEnvKey, exported)
		}
	}
	if exported[0] != instance.PoolName {
		t.Fatalf("%s[0] = %q, want the base pool route %q", routeTargetsEnvKey, exported[0], instance.PoolName)
	}
}
