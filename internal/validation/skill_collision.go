// Package validation hosts startup-time validators that guard against
// configurations the Gas City runtime cannot safely materialize. The
// validators are pure: they take a parsed *config.City and return
// diagnostic structs, never touching I/O.
package validation

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// supportedSkillVendors lists the providers whose skill sinks the
// materializer writes under a scope root. Providers outside this set
// have no sink (see "Vendor mapping" in
// engdocs/proposals/skill-materialization.md), so their agent-local
// skills cannot collide.
var supportedSkillVendors = map[string]struct{}{
	"claude":   {},
	"codex":    {},
	"gemini":   {},
	"opencode": {},
	"mimocode": {},
}

// citySentinel is the ScopeRoot marker used for city-scoped groupings.
// The validator operates on an in-memory *config.City and does not know
// the filesystem path of the city root; callers that want to substitute
// a real path (e.g. the doctor check) can do so when formatting errors.
const citySentinel = "<city>"

// SkillCollision describes an agent-local skill name provided from different
// sources by two or more agents sharing the same scope root and vendor.
type SkillCollision struct {
	// ScopeRoot is the scope root the colliding agents materialize
	// into. For rig-scoped agents this is the rig's configured path
	// (which may be relative to the city root). For city-scoped
	// agents this is the sentinel "<city>".
	ScopeRoot string
	// Vendor is the provider whose sink the collision lands in
	// (one of "claude", "codex", "gemini", "opencode", "mimocode").
	Vendor string
	// SkillName is the colliding agent-local skill name.
	SkillName string
	// AgentNames lists, in sorted order, every agent providing the
	// same agent-local skill name into this (ScopeRoot, Vendor) sink.
	AgentNames []string
}

// ValidateSkillCollisions groups agents by (scope-root, vendor), then by skill
// name and canonical source. A name is unsafe only when agents would point the
// same sink entry at different sources. Multiple agent directories may
// intentionally symlink to one shared source, and are safe because every
// materialization pass converges on the same target. Returns one SkillCollision
// entry per name with more than one canonical source, or nil when there are no
// collisions.
//
// Scope-root derivation mirrors the spec:
//   - agent.Scope == "city" → scope root = city sentinel
//   - agent.Scope == "rig"  → scope root = rig path (from agent.Dir
//     looked up in cfg.Rigs); if no matching rig is found the agent's
//     Dir is used as-is (supports inline agents with a custom Dir)
//   - empty scope is treated as "rig" (the default)
//
// Agents whose provider is not in the skill-sink vendor set contribute
// nothing — they have no sink, so they cannot collide. Agents with no
// SkillsDir or whose SkillsDir holds no skills also contribute nothing.
//
// Collisions are returned sorted by (ScopeRoot, Vendor, SkillName) so
// tests and user-facing output are stable.
func ValidateSkillCollisions(cfg *config.City) []SkillCollision {
	if cfg == nil || len(cfg.Agents) == 0 {
		return nil
	}

	rigPath := make(map[string]string, len(cfg.Rigs))
	for _, rig := range cfg.Rigs {
		rigPath[rig.Name] = rig.Path
	}

	type bucketKey struct{ scope, vendor string }
	// buckets: scope+vendor → skillName → canonical source → agent names.
	buckets := make(map[bucketKey]map[string]map[string]map[string]struct{})

	for i := range cfg.Agents {
		a := &cfg.Agents[i]

		// Agent provider falls back to workspace provider when not
		// set per-agent — matches the effective-provider resolution
		// used throughout the binary. Without this fallback,
		// workspace-level "provider = claude" configs with
		// non-overriding agents would bypass collision detection.
		// TrimSpace mirrors cmd/gc/skill_integration.go's
		// effectiveAgentProvider so whitespace-only overrides don't
		// bypass either the materializer or this gate.
		vendor := strings.TrimSpace(a.Provider)
		if vendor == "" {
			vendor = cfg.Workspace.Provider
		}
		if _, ok := supportedSkillVendors[vendor]; !ok {
			continue
		}

		scope := scopeRootFor(a, rigPath)
		if scope == "" {
			// Rig-scoped agent without a resolvable rig — skip. It
			// can't contribute a concrete sink anyway.
			continue
		}

		sources := listAgentLocalSkillSources(a.SkillsDir)
		if len(sources) == 0 {
			continue
		}

		key := bucketKey{scope: scope, vendor: vendor}
		bucket := buckets[key]
		if bucket == nil {
			bucket = make(map[string]map[string]map[string]struct{})
			buckets[key] = bucket
		}
		for name, source := range sources {
			bySource := bucket[name]
			if bySource == nil {
				bySource = make(map[string]map[string]struct{})
				bucket[name] = bySource
			}
			agents := bySource[source]
			if agents == nil {
				agents = make(map[string]struct{})
				bySource[source] = agents
			}
			agents[a.QualifiedName()] = struct{}{}
		}
	}

	var collisions []SkillCollision
	for key, bucket := range buckets {
		for skillName, bySource := range bucket {
			if len(bySource) < 2 {
				continue
			}
			agentSet := make(map[string]struct{})
			for _, agents := range bySource {
				for agent := range agents {
					agentSet[agent] = struct{}{}
				}
			}
			names := make([]string, 0, len(agentSet))
			for n := range agentSet {
				names = append(names, n)
			}
			sort.Strings(names)
			collisions = append(collisions, SkillCollision{
				ScopeRoot:  key.scope,
				Vendor:     key.vendor,
				SkillName:  skillName,
				AgentNames: names,
			})
		}
	}

	sort.Slice(collisions, func(i, j int) bool {
		if collisions[i].ScopeRoot != collisions[j].ScopeRoot {
			return collisions[i].ScopeRoot < collisions[j].ScopeRoot
		}
		if collisions[i].Vendor != collisions[j].Vendor {
			return collisions[i].Vendor < collisions[j].Vendor
		}
		return collisions[i].SkillName < collisions[j].SkillName
	})
	return collisions
}

// scopeRootFor returns the scope-root key for an agent. Empty scope is
// treated as rig-scoped per the spec. Rig-scoped agents resolve their
// rig by Dir against cfg.Rigs; if no rig matches, Dir is used as-is
// (inline-agent case). Rig-scoped agents with empty Dir collapse to
// the city sentinel — which is defensive; a well-formed expanded
// config always stamps Dir = rigName for rig-scoped agents.
func scopeRootFor(a *config.Agent, rigPath map[string]string) string {
	scope := a.Scope
	if scope == "" {
		scope = "rig"
	}
	switch scope {
	case "city":
		return citySentinel
	case "rig":
		if a.Dir == "" {
			// No rig stamped — fall back to city sentinel so we
			// at least bucket the agent somewhere. In practice
			// pack expansion sets Dir for rig-scoped agents.
			return citySentinel
		}
		if path, ok := rigPath[a.Dir]; ok && path != "" {
			return path
		}
		return a.Dir
	default:
		// Unknown scope values are validated elsewhere
		// (config.ValidateAgents). Treat as rig for best-effort
		// bucketing.
		return a.Dir
	}
}

// listAgentLocalSkillSources returns each skill name and its canonical source
// directory under the agent's local skills directory. A subdirectory counts as
// a skill if it contains a SKILL.md file (case-sensitive — matching the vendor
// convention and every existing caller). EvalSymlinks makes independent
// agent-local links to one shared source compare equal.
func listAgentLocalSkillSources(dir string) map[string]string {
	if dir == "" {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	sources := make(map[string]string)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		source := filepath.Join(dir, e.Name())
		if resolved, err := filepath.EvalSymlinks(source); err == nil {
			source = resolved
		}
		if abs, err := filepath.Abs(source); err == nil {
			source = abs
		}
		sources[e.Name()] = filepath.Clean(source)
	}
	return sources
}
