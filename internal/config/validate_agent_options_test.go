// internal/config/validate_agent_options_test.go
//
// Scope: the refusal contract for an agent [option_defaults] entry whose
// resolved provider does not declare the key or the value, plus the load-path
// wiring that makes the refusal reach a `gc` invocation.
//
// The suite exists because the failure it pins is invisible from the outside.
// A misspelled option default emits NO launch flag (TestResolveDefaultArgs...
// below pins that), so the session runs on the provider's own default -- for
// the builtin claude profile, effort=max -- and every surface a reader would
// check reports success. Only a refusal at load can catch a value nobody
// thought to list.
//
// Delegated elsewhere: the provider-side option_defaults refusal lives in
// options_test.go / resolved_cache_test.go; whether an agent NAMES a model and
// an effort at all is city policy and is gated in the city repo, not here --
// gc cannot require a value it also supplies a default for.
//
// Run: go test ./internal/config/ -run 'AgentOptionDefaults|ResolveDefaultArgs'
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// claudeOptionCity builds a city whose single agent selects a claude-family
// provider, with the agent's option_defaults supplied by the caller. The
// provider is declared with base = "builtin:claude" so the options schema
// under test is the real shipped one rather than a fixture that could drift
// from it.
func claudeOptionCity(t *testing.T, agentDefaults map[string]string) *City {
	t.Helper()
	cfg := &City{
		Workspace: Workspace{Name: "test", Provider: "claude"},
		Providers: map[string]ProviderSpec{
			"claude": {Base: strPtr("builtin:claude")},
		},
		Agents: []Agent{{
			Name:           "engineer",
			Provider:       "claude",
			OptionDefaults: agentDefaults,
		}},
	}
	if err := BuildResolvedProviderCache(cfg); err != nil {
		t.Fatalf("BuildResolvedProviderCache: %v", err)
	}
	return cfg
}

// TestAgentOptionDefaultsAcceptsDeclaredChoices pins the accepting half of the
// contract. Without it every refusal below is satisfiable by a validator that
// refuses everything, which would fail the city's own config on load.
func TestAgentOptionDefaultsAcceptsDeclaredChoices(t *testing.T) {
	cfg := claudeOptionCity(t, map[string]string{
		"model":           "opus-5",
		"effort":          "xhigh",
		"permission_mode": "unrestricted",
	})
	if err := ValidateAgentOptionDefaults(cfg); err != nil {
		t.Fatalf("declared choices must load: %v", err)
	}
}

// TestAgentOptionDefaultsRefusesUnrecognizedValues covers the four cases the
// bead names. Each is a value that reaches the launch path today and produces
// no flag there, so the agent silently runs the provider default.
func TestAgentOptionDefaultsRefusesUnrecognizedValues(t *testing.T) {
	cases := []struct {
		name     string
		defaults map[string]string
		wantIn   string
	}{
		{
			name:     "unknown model id",
			defaults: map[string]string{"model": "opus-6"},
			wantIn:   `"opus-6"`,
		},
		{
			name:     "unknown effort level",
			defaults: map[string]string{"effort": "xhgih"},
			wantIn:   `"xhgih"`,
		},
		{
			// The schema declares {Value: ""} so a session-create UI can
			// offer "Default", but a DECLARED empty default is
			// indistinguishable from a typo and lands on the same
			// provider default a typo lands on.
			name:     "empty value",
			defaults: map[string]string{"effort": ""},
			wantIn:   "declared empty",
		},
		{
			// Choice matching is exact, so this is the case a reader
			// predicts is already covered and is not: nothing else in
			// the load path looks at the value at all.
			name:     "case differs from a legal value",
			defaults: map[string]string{"effort": "High"},
			wantIn:   `"High"`,
		},
		{
			name:     "unknown key",
			defaults: map[string]string{"efort": "high"},
			wantIn:   "not declared by provider",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := claudeOptionCity(t, tc.defaults)
			err := ValidateAgentOptionDefaults(cfg)
			if err == nil {
				t.Fatal("expected a refusal, config loaded clean")
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantIn) {
				t.Errorf("error must name the offending value/reason %s: %v", tc.wantIn, err)
			}
			if !strings.Contains(msg, "engineer") {
				t.Errorf("error must name the agent: %v", err)
			}
		})
	}
}

// TestAgentOptionDefaultsErrorNamesEveryOffender pins that one load reports
// every bad entry. A validator returning the first error hides the second
// typo behind a fix-and-reload cycle per agent, and the city carries 126
// agents.
func TestAgentOptionDefaultsErrorNamesEveryOffender(t *testing.T) {
	cfg := claudeOptionCity(t, map[string]string{"model": "opus-6", "effort": "xhgih"})
	cfg.Agents = append(cfg.Agents, Agent{
		Name:           "technician",
		Provider:       "claude",
		OptionDefaults: map[string]string{"effort": "MAX"},
	})
	err := ValidateAgentOptionDefaults(cfg)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"opus-6", "xhgih", "MAX", "engineer", "technician"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name %q: %v", want, err)
		}
	}
}

// TestAgentOptionDefaultsErrorCarriesTheRemedy pins the house error-message
// rule: name the cause, the affected side, and what to write instead. The
// legal-choice list is the remedy, and it is derived from the same schema the
// refusal reads rather than restated in the message.
func TestAgentOptionDefaultsErrorCarriesTheRemedy(t *testing.T) {
	cfg := claudeOptionCity(t, map[string]string{"effort": "xhgih"})
	err := ValidateAgentOptionDefaults(cfg)
	if err == nil {
		t.Fatal("expected a refusal")
	}
	var optErr *AgentOptionDefaultsError
	if !errors.As(err, &optErr) {
		t.Fatalf("error must be an *AgentOptionDefaultsError, got %T", err)
	}
	for _, want := range []string{"low", "medium", "high", "xhigh"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("remedy must list the legal choice %q: %v", want, err)
		}
	}
}

// TestAgentOptionDefaultsSkipsStartCommandAgents pins the one exemption.
// start_command clears the options schema (ResolveProvider step 1), so no
// option value in that agent is reachable and validating against an absent
// schema would refuse every key.
func TestAgentOptionDefaultsSkipsStartCommandAgents(t *testing.T) {
	cfg := claudeOptionCity(t, map[string]string{"effort": "xhgih"})
	cfg.Agents[0].StartCommand = "my-agent --run"
	if err := ValidateAgentOptionDefaults(cfg); err != nil {
		t.Fatalf("start_command agent has no reachable options schema: %v", err)
	}
}

// TestAgentOptionDefaultsUsesWorkspaceProviderFallback pins that an agent
// naming no provider is validated against the provider it will actually run.
// Most agents in a real city name none: the imported-pack roles and the
// implicit provider agents both arrive with an empty Provider and fall back to
// workspace.provider, so skipping them would exempt the majority of the fleet.
func TestAgentOptionDefaultsUsesWorkspaceProviderFallback(t *testing.T) {
	cfg := claudeOptionCity(t, nil)
	cfg.Agents[0].Provider = ""
	cfg.Agents[0].OptionDefaults = map[string]string{"effort": "xhgih"}
	if err := ValidateAgentOptionDefaults(cfg); err == nil {
		t.Fatal("an agent inheriting workspace.provider must still be validated")
	}
}

// TestAgentOptionDefaultsIgnoresUnknownProvider pins that this validator does
// not duplicate ValidateProviderReferences. An agent naming a provider outside
// the catalog is that check's finding; reporting it here too would make one
// typo produce two unrelated errors, and the repair hints differ.
func TestAgentOptionDefaultsIgnoresUnknownProvider(t *testing.T) {
	cfg := claudeOptionCity(t, map[string]string{"effort": "xhgih"})
	cfg.Agents[0].Provider = "no-such-provider"
	if err := ValidateAgentOptionDefaults(cfg); err != nil {
		t.Fatalf("unknown provider belongs to ValidateProviderReferences: %v", err)
	}
}

// TestLoadWithIncludesRefusesUnrecognizedAgentEffort is the one that matters
// operationally: the refusal has to reach a `gc` invocation, not just its own
// unit test. A validator that is never called from the load path is green in
// every run and silent in every city.
func TestLoadWithIncludesRefusesUnrecognizedAgentEffort(t *testing.T) {
	dir := t.TempDir()
	cityDir := filepath.Join(dir, "city")
	if err := os.MkdirAll(cityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cityDir, "city.toml", `
[workspace]
name = "test"
provider = "claude"

[providers.claude]
base = "builtin:claude"

[[agent]]
name = "engineer"
provider = "claude"
option_defaults = { model = "opus-5", effort = "xhgih" }
`)
	_, _, err := LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err == nil {
		t.Fatal("config load must fail on an unrecognized effort value")
	}
	if !strings.Contains(err.Error(), "xhgih") {
		t.Errorf("load error must name the offending value: %v", err)
	}
}

// TestLoadWithIncludesAcceptsDeclaredAgentEffort is the paired accepting case.
// Without it the test above passes on any load failure -- a missing provider
// catalog entry, a parse error in the fixture -- and would keep passing if the
// new refusal were deleted.
func TestLoadWithIncludesAcceptsDeclaredAgentEffort(t *testing.T) {
	dir := t.TempDir()
	cityDir := filepath.Join(dir, "city")
	if err := os.MkdirAll(cityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, cityDir, "city.toml", `
[workspace]
name = "test"
provider = "claude"

[providers.claude]
base = "builtin:claude"

[[agent]]
name = "engineer"
provider = "claude"
option_defaults = { model = "opus-5", effort = "xhigh" }
`)
	if _, _, err := LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml")); err != nil {
		t.Fatalf("a city naming declared choices must load: %v", err)
	}
}

// TestResolveDefaultArgsDropsUnrecognizedValueSilently is the defect this
// whole file exists for, pinned as behavior rather than described in prose.
//
// It is deliberately an ASSERTION OF THE BUG, and it expires: if the launch
// path is ever made loud, this test fails and the reader is told to reconsider
// whether the load-time refusal is still the only guard. That is the point --
// a comment saying "the launch path is silent" would rot in silence.
func TestResolveDefaultArgsDropsUnrecognizedValueSilently(t *testing.T) {
	cfg := claudeOptionCity(t, map[string]string{"effort": "xhgih"})
	resolved, ok := ResolvedProviderCached(cfg, "claude")
	if !ok {
		t.Fatal("provider cache must carry the claude entry")
	}
	mergeAgentOverrides(&resolved, &cfg.Agents[0])
	args := strings.Join(resolved.ResolveDefaultArgs(), " ")
	if strings.Contains(args, "--effort") {
		t.Fatalf("launch path is no longer silent on a bad value (args %q); "+
			"re-read ValidateAgentOptionDefaults' rationale", args)
	}
	if resolved.EffectiveDefaults["effort"] != "xhgih" {
		t.Fatalf("the bad value must still be in EffectiveDefaults, got %q",
			resolved.EffectiveDefaults["effort"])
	}
}
