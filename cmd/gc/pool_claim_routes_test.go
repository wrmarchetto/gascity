// Scope: claim_routes template expansion on the reconciler side, and the
// mechanical guarantee that no production call site can skip it.
//
// This suite exists because the sibling test that models the same intent --
// TestComputePoolDesiredStates_SharedRouteClaimResumesConcreteOwner in
// pool_desired_state_slot_orphan_test.go -- hardcodes an ALREADY-EXPANDED
// claim route ("astoria-sel4/lab.engineer"), so it passed while production,
// which holds the literal "{{.Rig}}/lab.engineer", could not reach the code it
// pins. Measured on the running city 2026-09-07: `gc config show` prints the
// unexpanded template four times, once per rig importing packs/lab.
//
// What this suite does NOT cover: whether the reconciler acts on the resume
// request it receives, and whether releaseOrphanedPoolAssignment preserves
// gc.routed_to. Those are the other two links in the loop this expansion
// breaks, and they live with the reconciler and the pool-session-name code.
//
// Run: go test ./cmd/gc/ -run 'ClaimRoute' -count=1
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

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

// TestExpandAgentClaimRoutesResolvesRigPlaceholderWithoutMutating pins both
// halves of the adapter's contract: the rig placeholder resolves from the
// agent's own Dir, and the caller's config is left alone.
//
// The no-mutation half is asserted rather than assumed because both production
// call sites hand the SAME *config.City to the session reconciler and the
// desired-state builder in the statements around it. An in-place expansion
// would be invisible here and would change what those two see.
func TestExpandAgentClaimRoutesResolvesRigPlaceholderWithoutMutating(t *testing.T) {
	cfg := &config.City{
		Rigs: []config.Rig{{Name: "astoria-sel4", Path: "/tmp/astoria-sel4"}},
		Agents: []config.Agent{{
			Name:              "lab.engineer-codex",
			Dir:               "astoria-sel4",
			MaxActiveSessions: intPtr(2),
			ClaimRoutes:       []string{"{{.Rig}}/lab.engineer"},
		}},
	}

	expanded := expandAgentClaimRoutes(cfg, "/tmp/city", "city", nil)

	if got := expanded.Agents[0].ClaimRoutes; len(got) != 1 || got[0] != "astoria-sel4/lab.engineer" {
		t.Fatalf("expanded claim routes = %#v, want [astoria-sel4/lab.engineer]", got)
	}
	if got := cfg.Agents[0].ClaimRoutes[0]; got != "{{.Rig}}/lab.engineer" {
		t.Errorf("input config was mutated: claim route = %q, want the template untouched", got)
	}
}

// TestExpandAgentClaimRoutesLeavesConcreteConfigIdentical pins the cheap path:
// a config with nothing to expand is returned as the SAME pointer.
//
// Asserted on identity, not on contents, because the point is that the common
// case allocates nothing. A contents-only assertion would pass over a copy.
func TestExpandAgentClaimRoutesLeavesConcreteConfigIdentical(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{{
			Name:              "lab.engineer-codex",
			Dir:               "dart",
			MaxActiveSessions: intPtr(2),
			ClaimRoutes:       []string{"dart/lab.engineer"},
		}},
	}

	if got := expandAgentClaimRoutes(cfg, "/tmp/city", "city", nil); got != cfg {
		t.Errorf("expandAgentClaimRoutes returned a copy for an untemplated config; want the input pointer")
	}
}

// TestComputePoolDesiredStatesResumesOwnerFromTemplatedClaimRoute is the
// regression this file was written for. It is the production configuration:
// the claim route is the LITERAL packs/lab declares, not the value a previous
// test hand-expanded.
//
// Without the adapter this fails with `requests = 0` -- poolTemplateClaimsRoute
// compares "{{.Rig}}/lab.engineer" against "astoria-sel4/lab.engineer", finds
// no match, and computePoolDesiredStates drops the slot that is holding the
// work. Verified by running it against the unadapted call: RED at 0 requests.
func TestComputePoolDesiredStatesResumesOwnerFromTemplatedClaimRoute(t *testing.T) {
	const (
		codexTemplate = "astoria-sel4/lab.engineer-codex"
		sharedRoute   = "astoria-sel4/lab.engineer"
		slotIdentity  = codexTemplate + "-1"
	)

	cfg := &config.City{
		Rigs: []config.Rig{{Name: "astoria-sel4", Path: "/tmp/astoria-sel4"}},
		Agents: []config.Agent{{
			Name:              "lab.engineer-codex",
			Dir:               "astoria-sel4",
			MaxActiveSessions: intPtr(2),
			ClaimRoutes:       []string{"{{.Rig}}/lab.engineer"},
		}},
	}
	work := []beads.Bead{
		workBead("shared-work", sharedRoute, slotIdentity, "in_progress", 5),
	}
	sessions := []beads.Bead{{
		ID:     "codex-slot-1",
		Status: "open",
		Type:   sessionBeadType,
		Metadata: map[string]string{
			"template":     codexTemplate,
			"session_name": "lab-engineer-codex-1",
			"alias":        slotIdentity,
			"state":        "active",
			"pool_slot":    "1",
		},
	}}

	result := ComputePoolDesiredStates(
		expandAgentClaimRoutes(cfg, "/tmp/city", "city", nil),
		work, sessionInfosFromBeads(sessions), map[string]int{codexTemplate: 0})

	if len(result) != 1 || len(result[0].Requests) != 1 {
		t.Fatalf("requests = %#v, want one resume request for the slot holding shared-route work", result)
	}
	request := result[0].Requests[0]
	if request.Template != codexTemplate {
		t.Errorf("template = %q, want %q", request.Template, codexTemplate)
	}
	if request.Tier != "resume" {
		t.Errorf("tier = %q, want resume", request.Tier)
	}
	if request.SessionBeadID != "codex-slot-1" {
		t.Errorf("SessionBeadID = %q, want codex-slot-1", request.SessionBeadID)
	}
	if request.WorkBeadID != "shared-work" {
		t.Errorf("WorkBeadID = %q, want shared-work", request.WorkBeadID)
	}
}

// TestPoolClaimRoutesExpandedAtEveryComputeCallSite is the mechanical half.
//
// The two fixed call sites are not the guarantee -- the next one added is. A
// production ComputePoolDesiredStates* call whose config argument is not an
// expandAgentClaimRoutes call reintroduces the defect silently, because every
// existing test passes concrete routes and none of them would go red.
//
// The call sites are found by parsing the package rather than by listing them
// here: a list restated in the test is a copy of the thing it checks, and
// deleting a call site would delete it from the expectation too. The count is
// asserted as well, so a parse that starts matching nothing fails loudly
// instead of vacuously passing.
func TestPoolClaimRoutesExpandedAtEveryComputeCallSite(t *testing.T) {
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
		ast.Inspect(file, func(n ast.Node) bool {
			call, isCall := n.(*ast.CallExpr)
			if !isCall {
				return true
			}
			ident, isIdent := call.Fun.(*ast.Ident)
			if !isIdent || !strings.HasPrefix(ident.Name, "ComputePoolDesiredStates") {
				return true
			}
			if ident.Name == "computePoolDesiredStates" {
				return true
			}
			found++
			if len(call.Args) == 0 {
				t.Errorf("%s: %s called with no arguments", fset.Position(call.Pos()), ident.Name)
				return true
			}
			inner, isInnerCall := call.Args[0].(*ast.CallExpr)
			if !isInnerCall {
				t.Errorf("%s: %s config argument is not a call; it must be expandAgentClaimRoutes(...) or claim_routes reach the reconciler unexpanded",
					fset.Position(call.Pos()), ident.Name)
				return true
			}
			innerIdent, isInnerIdent := inner.Fun.(*ast.Ident)
			if !isInnerIdent || innerIdent.Name != "expandAgentClaimRoutes" {
				t.Errorf("%s: %s config argument is %s(...), want expandAgentClaimRoutes(...)",
					fset.Position(call.Pos()), ident.Name, exprName(inner.Fun))
			}
			return true
		})
	}

	// Five, not the two a read of the reconciler predicts: cmd_start.go,
	// build_desired_state.go, and city_runtime.go three times (the control
	// dispatcher tick, the bead reconcile, and the demand-snapshot refresh).
	// The first draft of this fix wired only two of them and this assertion is
	// what found the other three -- the wrappers in pool_desired_state.go are
	// definition sites and correctly excluded, but the *Traced and
	// *WithDemandTraced variants are separate exported entry points and each
	// one is a real production path. A drift to zero means this parse stopped
	// matching and the gate has silently stopped gating.
	if found != 5 {
		t.Errorf("found %d production ComputePoolDesiredStates* call sites, want 5; update this count deliberately when one is added or removed", found)
	}
}

func exprName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return exprName(e.X) + "." + e.Sel.Name
	}
	return "<expr>"
}
