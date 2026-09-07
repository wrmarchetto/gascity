package main

import (
	"fmt"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
)

// Whether the nested session caps can express a bound on one provider's
// concurrency. They cannot, and the city that asked the question needs the
// negative pinned rather than restated.
//
// The three tiers are agent -> rig -> workspace (nestedCapLimits in
// pool_desired_state.go). None of them is keyed on the provider an agent
// resolves to, so a deployment whose scarce resource is a per-provider quota
// -- an account pool shared by some agents and not others -- has no cap that
// matches the shape of its constraint. The rejected alternative a reader
// reaches for is a per-agent cap on each affected agent, and
// TestPerAgentCapsDoNotBoundTheCrossTemplateSum below is why that does not
// work: per-agent caps bound each template and never their sum.
//
// These are characterization tests for a real gap, so three of them are
// written to FAIL the day a provider- or pool-scoped cap lands. That is
// deliberate: prose about a missing feature expires silently, and every
// step that could notice reports the file untouched.
//
// Each assertion was confirmed observable by perturbing its FIXTURE and
// watching it go red -- the agent cap raised to 9 and a workspace total added
// to the two spread waves, and for the two provider waves: the mirror alone
// by leaving the providers UNSWAPPED between the runs (3/2 then 3/2), the
// both-providers check by grouping one run's wave so a provider is shut out,
// and each half of the declaration-order pair by giving its run the other
// half's order. Deleting an assertion is not a mutation and was tried: it
// survives, as any removed assertion must when the others hold.
//
// Every fixture here is order-DETERMINISTIC on purpose. Admission spends the
// workspace budget first-come in cfg.Agents order, so a fixture that ranges
// a map to build that slice randomizes the one input the result keys on
// (ci-8ohm89).
//
// Run: go test ./cmd/gc/ -run 'CapsDoNotBound|CapRefuses|WorkspaceCap|CapsAdmitAWithin'

func intPtrProviderScope(n int) *int { return &n }

// providerAgent is poolAgent plus the Provider field, which the cap tiers
// ignore. Set here anyway so a future provider-aware tier makes these tests
// fail loudly instead of silently continuing to pass on a blind path.
func providerAgent(name, provider string, maxSess int) config.Agent {
	zero := 0
	return config.Agent{
		Name:              name,
		Provider:          provider,
		MaxActiveSessions: intPtrProviderScope(maxSess),
		MinActiveSessions: &zero,
	}
}

// TestPerAgentCapRefusesDemandAboveItsOwnCap is the positive control. The
// two negative results below mean nothing without it: a rig that admitted
// nothing at all would satisfy "the sum stayed under the ceiling" trivially.
func TestPerAgentCapRefusesDemandAboveItsOwnCap(t *testing.T) {
	cfg := &config.City{
		Agents: []config.Agent{providerAgent("toolsmith", "claude", 3)},
	}
	demand := map[string]int{"toolsmith": 9}

	total := admittedTotal(ComputePoolDesiredStates(cfg, nil, nil, demand))

	if total != 3 {
		t.Errorf("admitted %d of 9, want 3 (the agent's own cap refuses the rest)", total)
	}
}

// TestPerAgentCapsDoNotBoundTheCrossTemplateSum pins the gap. Nine templates
// each capped at 1 admit nine sessions, because the only tier that could
// have refused the tenth is the workspace total, which is unset.
//
// EXPECTED TO FAIL once a provider- or pool-scoped cap exists. When it does,
// delete it and assert the new bound instead.
func TestPerAgentCapsDoNotBoundTheCrossTemplateSum(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
	cfg := &config.City{}
	demand := make(map[string]int, len(names))
	for _, name := range names {
		cfg.Agents = append(cfg.Agents, providerAgent(name, "claude", 1))
		demand[name] = 1
	}

	total := admittedTotal(ComputePoolDesiredStates(cfg, nil, nil, demand))

	if total != len(names) {
		t.Errorf("admitted %d, want %d: per-agent caps bound each template "+
			"and not their sum, so a wave spread one-per-template is not "+
			"clipped. A different number means a new cap tier landed -- "+
			"replace this characterization test with an assertion on it",
			total, len(names))
	}
}

// TestPerAgentCapsAdmitAWithinCeilingWaveUnchanged is the other half of the
// pair, and without it the test above cannot tell a brake from a throttle
// that clips everything. Five templates capped at 1 with five demand admit
// all five.
func TestPerAgentCapsAdmitAWithinCeilingWaveUnchanged(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	cfg := &config.City{}
	demand := make(map[string]int, len(names))
	for _, name := range names {
		cfg.Agents = append(cfg.Agents, providerAgent(name, "claude", 1))
		demand[name] = 1
	}

	total := admittedTotal(ComputePoolDesiredStates(cfg, nil, nil, demand))

	if total != len(names) {
		t.Errorf("admitted %d of %d, want all of them: a wave within the "+
			"ceiling must pass through untouched", total, len(names))
	}
}

// providerGroup is five templates sharing one name prefix and one provider.
// The two tests below are single-variable experiments over the same ten
// templates: one holds the declaration order fixed and swaps the providers,
// the other holds the providers fixed and swaps the declaration order.
// Building both waves from this one type is what keeps "only one thing
// changed" checkable by reading the call, not by diffing two loops.
type providerGroup struct {
	prefix   string // template name prefix -- "a" yields a1..a5
	provider string // the Provider field every template in the group carries
}

func (g providerGroup) agents() []config.Agent {
	out := make([]config.Agent, 0, 5)
	for i := 1; i <= 5; i++ {
		out = append(out, providerAgent(fmt.Sprintf("%s%d", g.prefix, i), g.provider, 1))
	}
	return out
}

// interleaveGroups alternates the two groups -- a1, z1, a2, z2, ... -- so a
// workspace cap of 5 lands partly on each provider (3/2). A lopsided split
// is a weaker mirror than a shared one, because 0 == 0 is satisfied by any
// rule that happens to starve the same side in both runs.
func interleaveGroups(first, second providerGroup) []config.Agent {
	a, b := first.agents(), second.agents()
	out := make([]config.Agent, 0, len(a)+len(b))
	for i := range a {
		out = append(out, a[i], b[i])
	}
	return out
}

// concatGroups declares each group's five templates consecutively, `first`
// ahead of `second`.
func concatGroups(first, second providerGroup) []config.Agent {
	return append(first.agents(), second.agents()...)
}

// TestWorkspaceCapIsBlindToWhichProviderIsScarce pins that admission never
// reads the Provider field. The same ten templates are admitted twice under
// the same declaration order with only the provider STRINGS swapped between
// the two name groups; the split must mirror exactly.
//
// A single run cannot establish this -- whatever split it reports is also
// consistent with a provider-aware tier that happens to favor one side --
// and the mirror is what rules that out. A tier capping claude at 2, say,
// would report 2/3 in the run where claude holds the early names and 0/5 in
// the run where it holds the late ones, which is not a mirror.
//
// EXPECTED TO FAIL once a provider- or pool-scoped cap exists. When it does,
// delete it and assert the new bound instead.
func TestWorkspaceCapIsBlindToWhichProviderIsScarce(t *testing.T) {
	early := admitByProvider(t, interleaveGroups(
		providerGroup{prefix: "a", provider: "claude"},
		providerGroup{prefix: "z", provider: "codex"},
	))
	late := admitByProvider(t, interleaveGroups(
		providerGroup{prefix: "a", provider: "codex"},
		providerGroup{prefix: "z", provider: "claude"},
	))

	// The free provider gets slots too, and this is the reason a workspace
	// total was rejected for the city that asked: Codex agents draw no Claude
	// quota, so every slot they take is throughput lost for no stability.
	// Deterministic only because the wave is interleaved -- see
	// TestWorkspaceCapSplitFollowsDeclarationOrder for what a grouped
	// declaration does to the same cap.
	if early["codex"] == 0 || late["claude"] == 0 {
		t.Errorf("expected the cap to admit both providers; got "+
			"%d/%d claude and %d/%d codex across the two runs",
			early["claude"], late["claude"], early["codex"], late["codex"])
	}
	// Mirrored under the swap: the split follows list position, never the
	// provider. A tier that had become provider-aware would break this.
	if early["claude"] != late["codex"] || early["codex"] != late["claude"] {
		t.Errorf("swapping the providers between the same names changed the "+
			"split (%d claude / %d codex, then %d / %d), so admission keys "+
			"on the provider after all -- re-derive the conclusion that no "+
			"provider-scoped cap exists before relying on it",
			early["claude"], early["codex"], late["claude"], late["codex"])
	}
}

// TestWorkspaceCapSplitFollowsDeclarationOrder is why the workspace total is
// not the instrument a per-provider quota needs. Blindness alone would still
// permit a stable, predictable split; this pins that the split is neither.
// Holding the providers fixed and moving the claude block from the front of
// cfg.Agents to the back takes claude from all 5 slots to none of them, so
// no value of the workspace cap expresses "at most N claude" -- the number
// claude actually receives is a property of list position.
//
// The rule is `for i := range cfg.Agents` over the scale_check tier in
// pool_desired_state.go: the budget is spent first-come in slice order.
// Declaration order is therefore the single input the split keys on, which
// is also why the fixture must never be built by ranging a map (see
// admitByProvider).
//
// EXPECTED TO FAIL once a provider- or pool-scoped cap exists.
func TestWorkspaceCapSplitFollowsDeclarationOrder(t *testing.T) {
	claude := providerGroup{prefix: "a", provider: "claude"}
	codex := providerGroup{prefix: "z", provider: "codex"}

	claudeFirst := admitByProvider(t, concatGroups(claude, codex))
	codexFirst := admitByProvider(t, concatGroups(codex, claude))

	if claudeFirst["claude"] != 5 || claudeFirst["codex"] != 0 {
		t.Errorf("claude declared first got %d/5 slots and codex %d/5, want "+
			"5 and 0: the cap is spent first-come in cfg.Agents order",
			claudeFirst["claude"], claudeFirst["codex"])
	}
	if codexFirst["claude"] != 0 || codexFirst["codex"] != 5 {
		t.Errorf("moving the claude block behind the codex block left claude "+
			"%d/5 and codex %d/5, want 0 and 5. Equal shares here would mean "+
			"admission gained an ordering-independent policy -- a workspace "+
			"cap that no longer keys on list position may well be able to "+
			"bound a provider, so re-derive the gap",
			codexFirst["claude"], codexFirst["codex"])
	}
}

// admitByProvider runs `agents` as a ten-template wave under a workspace cap
// of 5, one unit of demand each, and reports how many sessions each provider
// got. The caller supplies the slice already ordered, because that order is
// the variable under test.
//
// The rejected alternative, and the reason this signature takes a slice: the
// earlier fixture built its wave by ranging a two-entry map literal to
// interleave the groups. Go randomizes map iteration, so the one input the
// split keys on differed per run and the mirror assertion was a coin flip --
// measured failing 3 of 12 runs on clean main (ci-8ohm89). A fixture for an
// order-sensitive rule must never be built by ranging a map.
func admitByProvider(t *testing.T, agents []config.Agent) map[string]int {
	t.Helper()
	cfg := &config.City{
		Workspace: config.Workspace{MaxActiveSessions: intPtrProviderScope(5)},
		Agents:    agents,
	}
	demand := make(map[string]int, len(cfg.Agents))
	// Seeded to zero so a provider that was shut out reports 0 rather than
	// being absent, which an assertion reading the map cannot tell from a
	// provider the fixture forgot to declare.
	byProvider := map[string]int{}
	for i := range cfg.Agents {
		demand[cfg.Agents[i].QualifiedName()] = 1
		byProvider[cfg.Agents[i].Provider] = 0
	}

	states := ComputePoolDesiredStates(cfg, nil, nil, demand)
	if total := admittedTotal(states); total != 5 {
		t.Fatalf("admitted %d, want 5 (the workspace total)", total)
	}
	for _, state := range states {
		for i := range cfg.Agents {
			if cfg.Agents[i].QualifiedName() == state.Template {
				byProvider[cfg.Agents[i].Provider] += len(state.Requests)
			}
		}
	}
	return byProvider
}

func admittedTotal(states []PoolDesiredState) int {
	total := 0
	for _, state := range states {
		total += len(state.Requests)
	}
	return total
}
