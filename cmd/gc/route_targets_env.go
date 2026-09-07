// cmd/gc/route_targets_env.go
//
// One source for the assignee spellings a controller-spawned agent command may
// serve, and the one place a controller-side command environment is built.
//
// gc already computes that set to claim with -- hookClaimAgentRouteTargets
// prepends the agent's own rig-qualified pool route to every claim_routes
// entry, and a config cannot remove it -- but never told the query what it
// was. A custom work_query therefore hardcoded a guess, and the guess being
// narrower than gc is invisible from both sides: demand counts zero, the pool
// sizes zero, and the tracker shows a ready assigned bead gc would have
// claimed. Measured on 2026-09-07, gs-olu held exactly that shape for 44
// minutes with nothing red anywhere (ci-vk76d1).
//
// GC_ROUTE_TARGETS closes that by exporting the set. A query iterates it
// instead of taking targets as arguments, so the next claim_routes entry --
// or a new agent with a custom query -- needs no second edit.
//
// WHY cityName IS DERIVED HERE rather than taken as a parameter. The claim
// path and the controller both already carry a cityName, and letting each pass
// its own is how one function starts producing two answers -- trading a config
// drift for a code drift with nothing over it. Deriving it from (cfg,
// cityPath) makes the value a pure function of config, which is what
// TestRouteTargetsEnvAgreesAcrossClaimAndScaleCheckPaths can then assert.
//
// WHY stderr IS ABSENT. A malformed claim_routes template is already reported
// on both paths by the expanders that own the field: expandHookClaimRoutes at
// the claim site (cmd_hook.go) and expandAgentClaimRoutes on the reconciler
// side (pool_claim_routes.go). A third diagnostic from the env builder would
// duplicate one of those on every hook invocation, so this passes a nil
// stderr, which expandAgentCommandTemplate treats as "report nothing" while
// still falling back to the raw route.
//
// THE ABSENCE. cmd_hook.go appends two session-runtime fallback spellings to
// the set gc actually claims on -- the resolved agent name and GC_TEMPLATE --
// and neither is part of this value. For a template agent the resolved name IS
// the primary target, so nothing is lost. For a pool instance resolved from a
// slot-suffixed name it is that slot spelling, "<rig>/<pool>-2", which the
// controller has neither a session nor a slot number to know: including it
// would make the two paths disagree for the same agent, which is the code
// drift this file exists to prevent. Nothing routes work to a slot spelling on
// purpose -- agentutil.NormalizePoolRouteTarget collapses it back to the base
// at the sling write site, because a bead carrying it is structurally
// invisible to the pool -- and a query that still wants the runtime spelling
// reads GC_AGENT from this same environment. Pinned by
// TestRouteTargetsEnvOmitsTheSlotSuffixedRuntimeSpelling.
package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// routeTargetsEnvKey names the newline-separated route-target list gc exports
// to every controller-spawned agent command. Newline-separated, not
// space-separated: a route target is a free-form config string, and a POSIX
// shell has no safe way to re-split a space-joined list without also
// word-splitting the targets themselves.
const routeTargetsEnvKey = "GC_ROUTE_TARGETS"

// agentRouteTargetsEnvValue returns the GC_ROUTE_TARGETS value for a: the
// routes on which gc hook --claim will take fresh unassigned routed work,
// newline-separated, in claim order.
func agentRouteTargetsEnvValue(cityPath string, cfg *config.City, a *config.Agent) string {
	if a == nil {
		return ""
	}
	var rigs []config.Rig
	if cfg != nil {
		rigs = cfg.Rigs
	}
	cityName := loadedCityName(cfg, cityPath)
	targets := hookClaimAgentRouteTargets(a, expandHookClaimRoutes(cityPath, cityName, a, rigs, nil), nil)
	return strings.Join(targets, "\n")
}

// controllerAgentCommandEnv returns the subprocess environment for any
// controller-spawned agent command -- scale_check, the work_query probe,
// on_boot, on_death: the bd runtime coordinates for the agent's scope plus the
// route targets that command is allowed to serve.
//
// It exists so no two of those paths can disagree about the second half. Every
// production caller building such an environment must route through here --
// enforced by TestRouteTargetsEnvIsExportedAtEveryCommandEnvCallSite.
//
// The lifecycle hooks are included rather than exempted. Nothing in on_boot or
// on_death reads GC_ROUTE_TARGETS today, and the variable is additive there;
// an exemption list is what rots, because the next path added lands on
// whichever side of it the author happened to read.
//
// The returned map is never nil once an agent is supplied:
// controllerQueryRuntimeEnv answers nil for any scope off the managed bd store
// contract, which is the ordinary state of a plain city directory, and a nil
// map is indistinguishable from an absent variable to the subprocess.
func controllerAgentCommandEnv(cityPath string, cfg *config.City, agentCfg *config.Agent) (map[string]string, error) {
	env, err := controllerQueryRuntimeEnv(cityPath, cfg, agentCfg)
	if err != nil {
		return nil, err
	}
	value := agentRouteTargetsEnvValue(cityPath, cfg, agentCfg)
	if value == "" {
		return env, nil
	}
	if env == nil {
		env = map[string]string{}
	}
	env[routeTargetsEnvKey] = value
	return env, nil
}
