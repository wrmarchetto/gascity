package config

import (
	"fmt"
	"sort"
	"strings"
)

// Reasons an agent's declared option default is refused. Constants rather
// than inline strings because the doctor surface and the error text both
// render them, and a reworded literal in one place reads as a second
// distinct finding.
const (
	// OptionDefaultReasonUnknownKey is an option key the provider's schema
	// does not declare at all.
	OptionDefaultReasonUnknownKey = "not declared by provider"
	// OptionDefaultReasonUnknownValue is a value outside the key's declared
	// choices, including one differing only in case.
	OptionDefaultReasonUnknownValue = "not a declared choice"
	// OptionDefaultReasonEmpty is a key declared with an empty value.
	OptionDefaultReasonEmpty = "declared empty"
)

// InvalidAgentOptionDefault describes one agent option_defaults entry that
// the agent's resolved provider does not declare.
type InvalidAgentOptionDefault struct {
	Agent    string
	Provider string
	Key      string
	Value    string
	Reason   string
	// Choices are the declared values for Key, in schema order, and are
	// the remedy the error prints. Empty when Reason is
	// OptionDefaultReasonUnknownKey -- there is no key to offer values for.
	Choices []string
}

// InvalidAgentOptionDefaults returns every agent option_defaults entry whose
// key or value the agent's resolved provider does not declare.
//
// WHY THIS IS A LOAD-TIME REFUSAL. An option default reaches the launch
// command through ResolvedProvider.ResolveDefaultArgs, which looks the value
// up in the schema and emits NOTHING when the lookup misses. A misspelled
// effort or model therefore launches the agent on the provider's own default
// -- for the builtin claude profile that default is effort=max -- and no
// surface a reader would check says so. The failure mode is worst on exactly
// the value it hides: a city that pins efforts down to hold its quota gets
// silently put back on max by one transposed letter.
//
// The rejected alternative is making ResolveDefaultArgs error instead. It
// runs while composing a command for a session already being created, so the
// failure lands one spawn at a time and says nothing about the other agents
// carrying the same typo. The sibling ResolveOptions path already errors on
// the same value, and that split is how this defect previously showed up as
// half loud and half silent (ra-jbbv0, recorded in
// internal/worker/builtin/profiles.go).
//
// An empty declared value is refused with the rest. The schema declares
// {Value: ""} so a session-create UI can offer "Default", and an explicit
// per-session option may still be empty; a DECLARED default of "" is a
// different thing -- it records an opinion that resolves to no opinion, is
// indistinguishable from a typo, and lands on the same provider default.
//
// Not reported here: an agent naming a provider outside the catalog (that is
// ValidateProviderReferences, whose repair hint differs), and an agent
// declaring no model or effort at all. gc ships defaults for both, so
// requiring them is a city's policy to gate, not gc's to enforce.
func InvalidAgentOptionDefaults(cfg *City) []InvalidAgentOptionDefault {
	if cfg == nil {
		return nil
	}
	var found []InvalidAgentOptionDefault
	for i := range cfg.Agents {
		agent := &cfg.Agents[i]
		if len(agent.OptionDefaults) == 0 {
			continue
		}
		// start_command is the escape hatch: ResolveProvider returns a bare
		// ResolvedProvider with no OptionsSchema, so nothing in
		// option_defaults is reachable. Validating against the absent schema
		// would refuse every key on those agents.
		if agent.StartCommand != "" {
			continue
		}
		name := agentProviderName(cfg, agent)
		schema, ok := agentOptionsSchema(cfg, name)
		if !ok || len(schema) == 0 {
			continue
		}
		keys := make([]string, 0, len(agent.OptionDefaults))
		for key := range agent.OptionDefaults {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := agent.OptionDefaults[key]
			opt := findOption(schema, key)
			if opt == nil {
				found = append(found, InvalidAgentOptionDefault{
					Agent:    agent.QualifiedName(),
					Provider: name,
					Key:      key,
					Value:    value,
					Reason:   OptionDefaultReasonUnknownKey,
				})
				continue
			}
			reason := declaredOptionValueReason(opt, value)
			if reason == "" {
				continue
			}
			found = append(found, InvalidAgentOptionDefault{
				Agent:    agent.QualifiedName(),
				Provider: name,
				Key:      key,
				Value:    value,
				Reason:   reason,
				Choices:  declaredOptionChoices(opt),
			})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Agent != found[j].Agent {
			return found[i].Agent < found[j].Agent
		}
		return found[i].Key < found[j].Key
	})
	return found
}

// AgentOptionDefaultsError reports agent option_defaults entries that no
// resolved provider schema declares.
type AgentOptionDefaultsError struct {
	Invalid []InvalidAgentOptionDefault
}

// Error formats every refused entry with the legal values for its key.
func (e *AgentOptionDefaultsError) Error() string {
	if e == nil || len(e.Invalid) == 0 {
		return "agent option_defaults carry values no provider declares"
	}
	var b strings.Builder
	b.WriteString("agent option_defaults are not declared by their provider:")
	for _, bad := range e.Invalid {
		fmt.Fprintf(&b, "\n- agent %q (provider %q): option_defaults.%s = %q is %s",
			bad.Agent, bad.Provider, bad.Key, bad.Value, bad.Reason)
		if len(bad.Choices) > 0 {
			fmt.Fprintf(&b, "; declared values: %s", strings.Join(bad.Choices, ", "))
		}
	}
	b.WriteString("\nFix the value in the agent's [option_defaults], or drop the key to " +
		"inherit the provider default.")
	if e.hasUnknownKey() {
		// The observed cause every time so far, and invisible in a diff: a
		// bare key written BELOW an [option_defaults] header binds into that
		// table, so an agent field lands as an option nothing reads.
		b.WriteString("\nA key that is meant to be an agent field belongs ABOVE the " +
			"[option_defaults] header: TOML binds every bare key after a table " +
			"header into that table.")
	}
	return b.String()
}

func (e *AgentOptionDefaultsError) hasUnknownKey() bool {
	for _, bad := range e.Invalid {
		if bad.Reason == OptionDefaultReasonUnknownKey {
			return true
		}
	}
	return false
}

// ValidateAgentOptionDefaults returns an error when any agent declares an
// option default its provider does not.
func ValidateAgentOptionDefaults(cfg *City) error {
	invalid := InvalidAgentOptionDefaults(cfg)
	if len(invalid) == 0 {
		return nil
	}
	return &AgentOptionDefaultsError{Invalid: invalid}
}

// agentProviderName reports the provider an agent will run on. Compose has
// already folded InheritedProvider into Provider (ApplyAgentDefaults), so the
// only remaining fallback is the workspace default -- which most agents in a
// real city take: imported-pack roles and implicit provider agents both
// arrive with an empty Provider.
func agentProviderName(cfg *City, agent *Agent) string {
	if name := strings.TrimSpace(agent.Provider); name != "" {
		return name
	}
	return strings.TrimSpace(cfg.Workspace.Provider)
}

// agentOptionsSchema returns the options schema a named provider resolves to,
// and whether the name resolved at all.
//
// It goes through resolveCustomProviderForValidation -- the same helper the
// provider-side option validation uses -- rather than reading
// cfg.ResolvedProviders directly, so a provider declared the Phase A legacy
// way (no base, name or command matching a builtin) contributes the builtin's
// schema here exactly as it does at spawn. Reading the cache alone would
// return an empty schema for those and silently exempt them.
func agentOptionsSchema(cfg *City, name string) ([]ProviderOption, bool) {
	if name == "" {
		return nil, false
	}
	if spec, ok := cfg.Providers[name]; ok {
		resolved, err := resolveCustomProviderForValidation(name, spec, cfg.Providers)
		if err != nil {
			// Chain resolution already failed the load in
			// BuildResolvedProviderCache; do not report it a second time
			// with a different remedy.
			return nil, false
		}
		return resolved.OptionsSchema, true
	}
	if builtin, ok := BuiltinProviders()[name]; ok {
		return builtin.OptionsSchema, true
	}
	return nil, false
}

// declaredOptionValueReason reports why a DECLARED default value is refused,
// or "" when the value is a declared choice.
func declaredOptionValueReason(opt *ProviderOption, value string) string {
	if value == "" {
		return OptionDefaultReasonEmpty
	}
	if findChoice(opt.Choices, value) == nil {
		return OptionDefaultReasonUnknownValue
	}
	return ""
}

// declaredOptionChoices lists an option's selectable values in schema order.
// The empty choice is omitted: it is selectable per session but, per
// InvalidAgentOptionDefaults, never a legal declared default, so printing it
// in a remedy line would name a value this check refuses.
func declaredOptionChoices(opt *ProviderOption) []string {
	var out []string
	for _, choice := range opt.Choices {
		if choice.Value == "" {
			continue
		}
		out = append(out, choice.Value)
	}
	return out
}
