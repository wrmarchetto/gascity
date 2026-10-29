package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// BuildResolvedProviderCache walks every custom provider's base chain,
// materializes a fully-merged ResolvedProvider per entry, and stores
// the result on cfg.ResolvedProviders. It replaces any previously-built
// cache atomically: on any chain-resolution error, cfg.ResolvedProviders
// is left untouched and the error is returned.
//
// The cache is built after compose + patch have populated cfg.Providers.
// Callers should invoke this once per config load (see LoadWithIncludes).
//
// Design invariants:
//   - Lookups must return deep-copied values so callers cannot poison
//     the shared cache by mutating returned slices/maps.
//   - Built-ins are NOT materialized into the cache (they are the chain
//     terminus). Lookups for built-in-only names still work via
//     BuiltinProviders() / ResolveProvider.
//   - Chain walk errors (cycles, unknown base, wrapper-resume missing)
//     are surfaced during cache build so they fail at config load, not
//     at session spawn.
func BuildResolvedProviderCache(cfg *City) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Providers) == 0 {
		cfg.ResolvedProviders = nil
		return nil
	}

	// Build into a local map; assign atomically at the end.
	next := make(map[string]ResolvedProvider, len(cfg.Providers))
	var errs []error
	for name, spec := range cfg.Providers {
		resolved, err := ResolveProviderChain(name, spec, cfg.Providers)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving provider %q: %w", name, err))
			continue
		}
		next[name] = resolved
	}
	if len(errs) > 0 {
		// Do not overwrite the existing cache on error.
		return errors.Join(errs...)
	}
	if err := ValidateCustomProviderOptions(cfg.Providers); err != nil {
		return err
	}
	cfg.ResolvedProviders = next
	return nil
}

// ValidateCustomProviderOptions validates provider options after applying the
// same structural inheritance rules the runtime uses for custom providers.
// This catches invalid schema defaults and option_defaults before they can
// silently degrade into missing launch flags.
func ValidateCustomProviderOptions(providers map[string]ProviderSpec) error {
	if len(providers) == 0 {
		return nil
	}

	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []error
	for _, name := range names {
		resolved, err := resolveCustomProviderForValidation(name, providers[name], providers)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolving provider %q: %w", name, err))
			continue
		}
		if err := validateResolvedProviderOptions(name, providers[name].OptionDefaults, resolved); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func resolveCustomProviderForValidation(name string, spec ProviderSpec, providers map[string]ProviderSpec) (ResolvedProvider, error) {
	if spec.Base != nil {
		if strings.TrimSpace(*spec.Base) == "" {
			return *specToResolved(name, &spec), nil
		}
		return ResolveProviderChain(name, spec, providers)
	}

	builtins := BuiltinProviders()
	if base, ok := builtins[name]; ok {
		merged := MergeProviderOverBuiltin(base, spec)
		return *specToResolved(name, &merged), nil
	}
	if base, ok := builtins[spec.Command]; ok {
		merged := MergeProviderOverBuiltin(base, spec)
		return *specToResolved(name, &merged), nil
	}
	return *specToResolved(name, &spec), nil
}

// declared is the provider's own option_defaults, before any base merge, so
// an empty value is reported once at the layer that wrote it rather than
// again against every provider inheriting it.
func validateResolvedProviderOptions(name string, declared map[string]string, resolved ResolvedProvider) error {
	if err := ValidateOptionsSchema(resolved.OptionsSchema); err != nil {
		return fmt.Errorf("provider %q options_schema: %w", name, err)
	}
	if err := emptyOptionDefaultsError(name, resolved.OptionsSchema, declared); err != nil {
		return fmt.Errorf("provider %q option_defaults: %w", name, err)
	}
	if err := ValidateOptionDefaults(resolved.OptionsSchema, resolved.EffectiveDefaults); err != nil {
		return fmt.Errorf("provider %q option_defaults: %w", name, err)
	}
	return nil
}

// emptyOptionDefaultsError refuses a declared empty value, which
// ValidateOptionDefaults accepts because the schema offers "" as a
// per-session "Default" choice. See InvalidAgentOptionDefaults for why a
// declared "" is refused at the agent level too.
func emptyOptionDefaultsError(name string, schema []ProviderOption, declared map[string]string) error {
	keys := make([]string, 0, len(declared))
	for key, value := range declared {
		if value == "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	errs := make([]error, 0, len(keys))
	for _, key := range keys {
		var choices []string
		if opt := findOption(schema, key); opt != nil {
			choices = declaredOptionChoices(opt)
		}
		errs = append(errs, fmt.Errorf("option_defaults key %q is %s (declared "+
			"values: %s). Name one in [providers.%s] option_defaults, or drop "+
			"the key to inherit its base's default",
			key, OptionDefaultReasonEmpty, strings.Join(choices, ", "), name))
	}
	return errors.Join(errs...)
}

// ResolvedProviderCached returns a deep-copied ResolvedProvider from
// the eager cache. If no cache entry exists for name, ok is false.
// Callers receive an independent copy — mutating returned slices/maps
// does not affect the cache or subsequent lookups.
//
// This is the runtime-facing read path. Pre-compose / quick-parse
// paths that operate on raw ProviderSpec before cache build must NOT
// call this; they should use RawProviderSpec reads.
func ResolvedProviderCached(cfg *City, name string) (ResolvedProvider, bool) {
	if cfg == nil || cfg.ResolvedProviders == nil {
		return ResolvedProvider{}, false
	}
	resolved, ok := cfg.ResolvedProviders[name]
	if !ok {
		return ResolvedProvider{}, false
	}
	return deepCopyResolvedProvider(resolved), true
}

// deepCopyResolvedProvider clones all slice and map fields so the
// caller's copy is independent of the cache entry.
func deepCopyResolvedProvider(r ResolvedProvider) ResolvedProvider {
	dup := r
	if r.Args != nil {
		dup.Args = append([]string(nil), r.Args...)
	}
	if r.ProcessNames != nil {
		dup.ProcessNames = append([]string(nil), r.ProcessNames...)
	}
	if r.PrintArgs != nil {
		dup.PrintArgs = append([]string(nil), r.PrintArgs...)
	}
	if r.Chain != nil {
		dup.Chain = append([]HopIdentity(nil), r.Chain...)
	}
	dup.Provenance = r.Provenance.clone()
	if r.Env != nil {
		dup.Env = make(map[string]string, len(r.Env))
		for k, v := range r.Env {
			dup.Env[k] = v
		}
	}
	if r.PermissionModes != nil {
		dup.PermissionModes = make(map[string]string, len(r.PermissionModes))
		for k, v := range r.PermissionModes {
			dup.PermissionModes[k] = v
		}
	}
	if r.EffectiveDefaults != nil {
		dup.EffectiveDefaults = make(map[string]string, len(r.EffectiveDefaults))
		for k, v := range r.EffectiveDefaults {
			dup.EffectiveDefaults[k] = v
		}
	}
	if r.OptionsSchema != nil {
		dup.OptionsSchema = make([]ProviderOption, len(r.OptionsSchema))
		for i, opt := range r.OptionsSchema {
			nopt := opt
			if opt.Choices != nil {
				nopt.Choices = make([]OptionChoice, len(opt.Choices))
				for j, c := range opt.Choices {
					nc := c
					if c.FlagArgs != nil {
						nc.FlagArgs = append([]string(nil), c.FlagArgs...)
					}
					if c.FlagAliases != nil {
						nc.FlagAliases = cloneStringSlices(c.FlagAliases)
					}
					nopt.Choices[j] = nc
				}
			}
			dup.OptionsSchema[i] = nopt
		}
	}
	return dup
}

// ErrProviderCacheNotBuilt is returned by strict cache-only lookups when
// the cache has not been materialized.
var ErrProviderCacheNotBuilt = errors.New("provider cache not built")
