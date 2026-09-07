// cmd/gc/pool_claim_routes.go
//
// Template expansion for claim_routes on the reconciler side.
//
// claim_routes is documented as supporting the same Go template placeholders
// as work_query (internal/config/config.go, Agent.ClaimRoutes), and a
// rig-scoped pack agent uses that: packs/lab/agents/engineer-codex declares
// `claim_routes = ["{{.Rig}}/lab.engineer"]` so one definition serves every
// rig importing the pack. gc expands command fields at USE, not at load, so
// the merged config carries the literal template -- `gc config show` prints
// it unexpanded.
//
// The claim side already honors that (expandHookClaimRoutes in cmd_hook.go).
// The reconciler side did not: poolTemplateClaimsRoute compared the RAW value
// against a concrete route, never matched, and computePoolDesiredStates then
// emitted no resume request for the slot that was holding the shared-route
// work. A slot in that state is permanently undesired -- its drain-ack is
// honored, the reconciler retires it as orphaned, releaseOrphanedPoolAssignment
// reopens the bead with gc.routed_to intact, scale_check counts it again, and
// the pool respawns into the same loop.
//
// Why an adapter at each production call site rather than a cityPath
// parameter on ComputePoolDesiredStates: that function has 166 references in
// this package, almost all tests passing already-concrete routes. Widening its
// signature is a broad edit to upstream-owned code for no behavior those
// callers can observe. The rejected alternative was expanding inside
// poolTemplateClaimsRoute from agent.Dir alone -- that is a SECOND expander
// implementing a subset of the placeholder set, so it silently disagrees with
// the claim side for any route naming a path field, which is the exact class
// of bug this fixes.
//
// TestPoolClaimRoutesExpandedAtEveryComputeCallSite is what keeps this
// mechanical: a new ComputePoolDesiredStates call site in production code that
// does not route through here fails the build. It is not decoration -- the
// first draft of this fix wired two call sites and that gate found three more,
// including buildDesiredStateWithSessionBeads, which is the path the session
// reconciler actually runs.
package main

import (
	"io"

	"github.com/gastownhall/gascity/internal/config"
)

// expandAgentClaimRoutes returns cfg with every agent's claim_routes
// template-expanded, using the same context and the same expander as the
// claim side. cfg is never mutated: callers share it with the session
// reconciler and the desired-state builder.
//
// A config whose claim routes hold no template is returned as-is, so the
// common case allocates nothing.
func expandAgentClaimRoutes(cfg *config.City, cityPath, cityName string, stderr io.Writer) *config.City {
	if cfg == nil || !anyAgentClaimRouteIsTemplated(cfg) {
		return cfg
	}
	expanded := *cfg
	expanded.Agents = make([]config.Agent, len(cfg.Agents))
	copy(expanded.Agents, cfg.Agents)
	for i := range expanded.Agents {
		agent := &expanded.Agents[i]
		if len(agent.ClaimRoutes) == 0 {
			continue
		}
		routes := make([]string, 0, len(agent.ClaimRoutes))
		for _, route := range agent.ClaimRoutes {
			routes = append(routes, expandAgentCommandTemplate(
				cityPath, cityName, agent, cfg.Rigs, "claim_routes", route, stderr))
		}
		agent.ClaimRoutes = routes
	}
	return &expanded
}

// anyAgentClaimRouteIsTemplated reports whether expansion could change
// anything. It mirrors expandAgentCommandTemplate's own "{{" guard rather
// than re-testing each route there, so the decision to copy cfg is made once.
func anyAgentClaimRouteIsTemplated(cfg *config.City) bool {
	for i := range cfg.Agents {
		for _, route := range cfg.Agents[i].ClaimRoutes {
			if containsTemplateMarker(route) {
				return true
			}
		}
	}
	return false
}

func containsTemplateMarker(value string) bool {
	for i := 0; i+1 < len(value); i++ {
		if value[i] == '{' && value[i+1] == '{' {
			return true
		}
	}
	return false
}
