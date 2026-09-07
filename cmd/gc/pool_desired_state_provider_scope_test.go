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
// These are characterization tests for a real gap, so two of them are
// written to FAIL the day a provider- or pool-scoped cap lands. That is
// deliberate: prose about a missing feature expires silently, and every
// step that could notice reports the file untouched.
//
// Each assertion was confirmed observable by perturbing its FIXTURE and
// watching it go red -- the agent cap raised to 9, a workspace total added
// to the two spread waves. Deleting an assertion is not a mutation and was
// tried: it survives, as any removed assertion must when the others hold.
//
// Run: go test ./cmd/gc/ -run 'CapsDoNotBound|CapRefuses|WorkspaceCapIsBlind|CapsAdmitAWithin'

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

// TestWorkspaceCapIsBlindToWhichProviderIsScarce pins why the workspace
// total is not the instrument either. A city whose Claude quota is scarce
// and whose Codex agents draw none of it cannot express "at most 5 Claude"
// with a workspace cap: MEASURED here, a cap of 5 against a ten-template
// wave splits its slots across BOTH providers, so it throttles the free
// work while still not bounding the scarce provider at any chosen number.
//
// Blindness is established by running the same wave twice with only the
// NAMES swapped between the two providers and requiring the split to mirror
// exactly. A single run cannot establish it -- whatever split it reports is
// also consistent with a provider-aware tier that happens to favor one side
// -- and the mirror is what rules that out.
func TestWorkspaceCapIsBlindToWhichProviderIsScarce(t *testing.T) {
	early := admitByProvider(t, "claude", "codex")
	late := admitByProvider(t, "codex", "claude")

	// The free provider gets slots too. This is the reason a workspace total
	// was rejected for the city that asked: Codex agents draw no Claude
	// quota, so every slot they take is throughput lost for no stability.
	if early["codex"] == 0 || late["claude"] == 0 {
		t.Errorf("expected the cap to admit both providers; got "+
			"%d/%d claude and %d/%d codex across the two runs",
			early["claude"], late["claude"], early["codex"], late["codex"])
	}
	// Mirrored under the swap: the split follows template naming, never the
	// provider. A tier that had become provider-aware would break this.
	if early["claude"] != late["codex"] || early["codex"] != late["claude"] {
		t.Errorf("swapping the providers between the same names changed the "+
			"split (%d/%d then %d/%d), so admission keys on the provider "+
			"after all -- re-derive the conclusion that no provider-scoped "+
			"cap exists before relying on it",
			early["claude"], early["codex"], late["claude"], late["codex"])
	}
}

// admitByProvider runs a ten-template wave under a workspace cap of 5, with
// `first` given the names that sort earlier ("a1".."a5") and `second` the
// later ones ("z1".."z5"), and reports how many sessions each provider got.
func admitByProvider(t *testing.T, first, second string) map[string]int {
	t.Helper()
	cfg := &config.City{
		Workspace: config.Workspace{MaxActiveSessions: intPtrProviderScope(5)},
	}
	demand := map[string]int{}
	// Ordered pairs, NOT a map literal. Go randomizes map iteration, so
	// ranging one here appended the ten templates in a random order, the cap
	// admitted whichever five came first, and the split this test compares
	// varied run to run -- measured failing 2 of 12 processes at -count=1.
	// The mirror assertion below is only meaningful against a fixed order.
	pairs := []struct{ prefix, provider string }{{"a", first}, {"z", second}}
	for i := 1; i <= 5; i++ {
		for _, pair := range pairs {
			name := fmt.Sprintf("%s%d", pair.prefix, i)
			cfg.Agents = append(cfg.Agents, providerAgent(name, pair.provider, 1))
			demand[name] = 1
		}
	}

	states := ComputePoolDesiredStates(cfg, nil, nil, demand)
	if total := admittedTotal(states); total != 5 {
		t.Fatalf("admitted %d, want 5 (the workspace total)", total)
	}
	byProvider := map[string]int{}
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
