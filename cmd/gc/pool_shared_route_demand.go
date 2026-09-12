// cmd/gc/pool_shared_route_demand.go
//
// Bounds total NEW-tier pool demand across templates that read the same claim
// route.
//
// Why this file exists: scale_check demand is computed per template, and two
// templates configured to claim the same route count the SAME beads. Each
// template's arithmetic is correct in isolation and the sum is wrong -- so no
// per-template cap, no in-flight ledger and no nested cap can see it.
// poolInFlightNewRequests already spends demand against a create that has not
// claimed yet, but only within the template that owns it.
//
// Measured on the running city 2026-09-12 (ci-fp7pzt): bead as-74qq, created
// 00:11:10Z on route astoria-sel4/lab.engineer, produced
// template_tick_summary pool_desired=1 work_requested=true for BOTH
// astoria-sel4/lab.engineer and astoria-sel4/lab.engineer-codex in the same
// tick at 00:11:31.005Z. The Claude slot claimed it at 00:11:41Z; the Codex
// session spawned, ran one tool call, found nothing and drained no_work at
// 00:11:56Z.
//
// WHAT THIS DOES NOT FIX, and the measurement is why it is written here rather
// than left to be rediscovered: over 2026-09-07..09-12 the city logged 571
// session.demand_claim_mismatch/no_work events. Only 64 are shared-route
// count-only pools. The other 507 are on templates with NO custom scale_check
// and a NAMED trigger bead -- including `governor`, a single-slot pool with no
// claim_routes at all, which cannot contend with anything and still produced
// 75. Naming the bead does not prevent a phantom spawn, so this bound is not
// the general cure for that symptom and a later reader must not read it as one.
// Tracked separately.
//
// The budget is the MAXIMUM member count, not the sum and not the minimum.
// Each member counts a possibly different UNION of routes -- the codex pool
// counts its own route as well as the shared one -- so the largest member
// count is the only figure guaranteed to cover every bead any member can see.
// A sum double-counts the shared beads, which is the defect; a minimum would
// strand work visible to one member alone.
package main

import (
	"strings"

	"github.com/gastownhall/gascity/internal/agentutil"
	"github.com/gastownhall/gascity/internal/config"
)

// sharedRouteDemandBudget tracks how much new-demand each shared-route group
// has left to spend this tick. A template serving no shared route is absent
// from the group map and is never bounded.
type sharedRouteDemandBudget struct {
	groupOf   map[string]int // template -> group index
	remaining []int          // group index -> units left
}

// newSharedRouteDemandBudget partitions templates into groups whose route sets
// overlap, and gives each group the largest count any member reported.
//
// Grouping is transitive by design: A shares a route with B and B with C puts
// all three in one group even when A and C share nothing directly. The
// alternative -- pairwise deduction -- has no stable answer when the pairs are
// processed in different orders, and the transitive form is what makes the
// budget independent of agent declaration order.
func newSharedRouteDemandBudget(cfg *config.City, scaleCheckCounts map[string]int) sharedRouteDemandBudget {
	budget := sharedRouteDemandBudget{groupOf: map[string]int{}}
	if cfg == nil || len(scaleCheckCounts) == 0 {
		return budget
	}
	routeGroup := map[string]int{}
	members := map[int][]string{}
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if agent.Suspended {
			continue
		}
		template := agent.QualifiedName()
		if _, ok := scaleCheckCounts[template]; !ok {
			continue
		}
		routes := poolDemandRouteTargets(agent)
		if len(routes) == 0 {
			continue
		}
		group := -1
		for _, route := range routes {
			if existing, ok := routeGroup[route]; ok {
				group = existing
				break
			}
		}
		if group < 0 {
			group = len(budget.remaining)
			budget.remaining = append(budget.remaining, 0)
		}
		for _, route := range routes {
			// A route already owned by a DIFFERENT group merges that group
			// into this one. Without the merge a template bridging two groups
			// leaves the earlier one bounded by a stale budget, and the beads
			// on the bridged route are counted twice again.
			if existing, ok := routeGroup[route]; ok && existing != group {
				for _, member := range members[existing] {
					budget.groupOf[member] = group
					members[group] = append(members[group], member)
				}
				for r, g := range routeGroup {
					if g == existing {
						routeGroup[r] = group
					}
				}
				delete(members, existing)
			}
			routeGroup[route] = group
		}
		budget.groupOf[template] = group
		members[group] = append(members[group], template)
	}
	for group, templates := range members {
		if len(templates) < 2 {
			// A group of one is exactly the unbounded case: nothing else reads
			// its routes, so its own count is already the truth. Dropping it
			// here keeps the common city -- where no agent declares
			// claim_routes at all -- on the pre-existing path with no budget
			// arithmetic in it.
			for _, template := range templates {
				delete(budget.groupOf, template)
			}
			continue
		}
		largest := 0
		for _, template := range templates {
			if count := scaleCheckCounts[template]; count > largest {
				largest = count
			}
		}
		budget.remaining[group] = largest
	}
	return budget
}

// poolDemandRouteTargets returns every route an agent's demand and claim can
// read: its own pool-demand identity plus each configured claim route. Routes
// arrive here already expanded (expandAgentClaimRoutes); the literal
// "{{.Rig}}/..." form would group nothing, which is why the expansion is a
// precondition rather than something to repeat.
func poolDemandRouteTargets(agent *config.Agent) []string {
	if agent == nil {
		return nil
	}
	routes := make([]string, 0, len(agent.ClaimRoutes)+1)
	if own := strings.TrimSpace(agentutil.RoutedToIdentity(agent)); own != "" {
		routes = append(routes, own)
	}
	for _, route := range agent.ClaimRoutes {
		if route = strings.TrimSpace(route); route != "" {
			routes = append(routes, route)
		}
	}
	return routes
}

// charge spends units that are NOT subject to refusal: a create already in
// flight for this template is demand the previous tick already spent, and
// refusing it would orphan a session the reconciler just made.
//
// It is called for every group member BEFORE any member is offered a fresh
// create, because the loop below walks agents in declaration order and an
// in-flight create on a later-declared member would otherwise find the budget
// already taken by an earlier member's fresh create -- producing the same
// double spawn with the order reversed.
//
// The charge uses the raw scale count as its ceiling rather than the
// nested-cap-narrowed figure, which the pre-pass cannot know. When the nested
// caps would have reduced it further the group is charged slightly too much
// and funds one fewer fresh create; that direction is recoverable on the next
// tick, and the opposite direction is the phantom this file exists to stop.
func (b *sharedRouteDemandBudget) charge(template string, units int) {
	if units <= 0 {
		return
	}
	group, ok := b.groupOf[template]
	if !ok {
		return
	}
	if b.remaining[group] -= units; b.remaining[group] < 0 {
		b.remaining[group] = 0
	}
}

// cap narrows want to what template's shared-route group can still fund, and
// spends what it grants. A template in no group is returned unchanged.
func (b *sharedRouteDemandBudget) cap(template string, want int) int {
	if want <= 0 {
		return want
	}
	group, ok := b.groupOf[template]
	if !ok {
		return want
	}
	if b.remaining[group] < want {
		want = b.remaining[group]
	}
	if want < 0 {
		want = 0
	}
	b.remaining[group] -= want
	return want
}

// chargeInFlight spends every group member's already-in-flight creates before
// any member is offered a fresh one. See charge for why this must precede the
// loop rather than happen inside it.
func (b *sharedRouteDemandBudget) chargeInFlight(cfg *config.City, scaleCheckCounts map[string]int, inFlight map[string][]SessionRequest) {
	if len(b.groupOf) == 0 {
		return
	}
	for i := range cfg.Agents {
		template := cfg.Agents[i].QualifiedName()
		if _, ok := b.groupOf[template]; !ok {
			continue
		}
		b.charge(template, minInt(len(inFlight[template]), scaleCheckCounts[template]))
	}
}
