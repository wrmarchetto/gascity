package session

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// IdentityIndex maps an agent identity -- a qualified agent or pool instance
// name such as "myrig/engineer-1" -- to the open session bead answering to it.
// A pool session's runtime name is "<template>-<beadID>", which the identity
// alone cannot derive, so every status read resolves through this index.
type IdentityIndex struct {
	byAgentName    map[string]IdentityEntry
	byTemplateHint map[string]IdentityEntry
}

// IdentityEntry is one indexed session bead.
type IdentityEntry struct {
	BeadID      string
	SessionName string // raw session_name metadata, never derived
}

// NewIdentityIndex indexes the open rows of infos. A configured named session
// wins its keys over any other row, otherwise the first row in infos order
// wins. Pool-managed rows are indexed by their slotted instance identity only.
func NewIdentityIndex(infos []Info) IdentityIndex {
	x := IdentityIndex{
		byAgentName:    make(map[string]IdentityEntry),
		byTemplateHint: make(map[string]IdentityEntry),
	}
	for _, in := range infos {
		if in.Closed || in.SessionNameMetadata == "" {
			continue
		}
		entry := IdentityEntry{BeadID: in.ID, SessionName: in.SessionNameMetadata}
		isCanonicalNamed := strings.TrimSpace(in.ConfiguredNamedIdentity) != ""
		if agentName := AgentNameOf(in); agentName != "" {
			if IsPoolManagedInfo(in) && agentName == in.Template {
				if stamped := StampedPoolQualifiedIdentity(in); stamped != "" {
					agentName = stamped
				} else if !IsCanonicalPoolManagedInfoForTemplate(in, agentName) {
					agentName = ""
				}
			}
			if agentName != "" {
				if _, exists := x.byAgentName[agentName]; !exists || isCanonicalNamed {
					x.byAgentName[agentName] = entry
				}
			}
		}
		if IsPoolManagedInfo(in) {
			continue
		}
		if template := in.Template; template != "" {
			if _, exists := x.byTemplateHint[template]; !exists || isCanonicalNamed {
				x.byTemplateHint[template] = entry
			}
		}
		if commonName := in.CommonName; commonName != "" {
			if _, exists := x.byTemplateHint[commonName]; !exists {
				x.byTemplateHint[commonName] = entry
			}
		}
	}
	return x
}

// Lookup resolves identity, preferring the agent-name index over the
// template-hint index.
func (x IdentityIndex) Lookup(identity string) (IdentityEntry, bool) {
	if e, ok := x.byAgentName[identity]; ok {
		return e, true
	}
	e, ok := x.byTemplateHint[identity]
	return e, ok
}

// AgentNameOf returns the agent_name metadata, falling back to the legacy
// "agent:<name>" label.
func AgentNameOf(i Info) string {
	if i.AgentName != "" {
		return i.AgentName
	}
	for _, label := range i.Labels {
		if name, ok := strings.CutPrefix(label, "agent:"); ok {
			return name
		}
	}
	return ""
}

// OriginOf returns the session origin, inferring "named", "manual" or
// "ephemeral" from older beads that predate the session_origin stamp.
func OriginOf(i Info) string {
	if origin := strings.TrimSpace(i.SessionOrigin); origin != "" {
		return origin
	}
	if i.ConfiguredNamedSession {
		return "named"
	}
	if i.ManualSessionMetadata == "true" {
		return "manual"
	}
	if i.PoolManaged || strings.TrimSpace(i.PoolSlot) != "" || i.DependencyOnly {
		return "ephemeral"
	}
	if template := strings.TrimSpace(i.Template); template != "" {
		if ResolvePoolSlot(strings.TrimSpace(AgentNameOf(i)), template) > 0 {
			return "ephemeral"
		}
		if ResolvePoolSlot(strings.TrimSpace(i.SessionNameMetadata), template) > 0 {
			return "ephemeral"
		}
	}
	return ""
}

// IsPoolManagedInfo reports whether the controller's pool owns the session.
func IsPoolManagedInfo(i Info) bool {
	return OriginOf(i) == "ephemeral" || i.PoolManaged || strings.TrimSpace(i.PoolSlot) != ""
}

// ResolvePoolSlot parses the slot from "<template>-<n>", or the legacy
// "<template>-gc-<n>", returning 0 when agentName is neither.
func ResolvePoolSlot(agentName, template string) int {
	suffix, ok := strings.CutPrefix(agentName, template+"-")
	if !ok {
		return 0
	}
	if slot, err := strconv.Atoi(suffix); err == nil {
		return slot
	}
	if legacy, ok := strings.CutPrefix(suffix, "gc-"); ok {
		slot, _ := strconv.Atoi(legacy)
		return slot
	}
	return 0
}

// StampedPoolQualifiedIdentity derives "scope/name-slot" from a slotted
// pool-managed session, or "" when the session carries no usable slot.
func StampedPoolQualifiedIdentity(i Info) string {
	if !IsPoolManagedInfo(i) {
		return ""
	}
	slot, err := strconv.Atoi(strings.TrimSpace(i.PoolSlot))
	if err != nil || slot <= 0 {
		return ""
	}
	template := strings.TrimSpace(i.Template)
	if template == "" {
		return ""
	}
	scope, name := config.ParseQualifiedName(template)
	if name == "" {
		return ""
	}
	instance := fmt.Sprintf("%s-%d", name, slot)
	if scope != "" {
		return scope + "/" + instance
	}
	return instance
}

// InfoIdentifiesAsCanonical reports whether any identity field of i names
// canonical.
func InfoIdentifiesAsCanonical(i Info, canonical string) bool {
	canonical = strings.TrimSpace(canonical)
	if canonical == "" {
		return false
	}
	return strings.TrimSpace(i.AgentName) == canonical ||
		strings.TrimSpace(i.Alias) == canonical ||
		strings.TrimSpace(i.Title) == canonical ||
		slices.Contains(i.Labels, "agent:"+canonical)
}

// IsCanonicalPoolManagedInfoForTemplate is the session-shape companion to
// config.Agent.UsesCanonicalSingletonPoolIdentity: pool-managed, no pool
// slot, and identifying as template.
func IsCanonicalPoolManagedInfoForTemplate(i Info, template string) bool {
	template = strings.TrimSpace(template)
	if template == "" || !IsPoolManagedInfo(i) {
		return false
	}
	if strings.TrimSpace(i.PoolSlot) != "" {
		return false
	}
	return InfoIdentifiesAsCanonical(i, template)
}
