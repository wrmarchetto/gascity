package dashboardbff

import (
	"net/http"
	"strings"
)

// runtimeConfig is the wire shape the SPA fetches at boot via
// GET /api/city/{cityName}/config. It must match shared/src/snapshot/types.ts
// DashboardRuntimeConfig exactly (the frontend's decodeRuntimeConfig validates
// every field). enabledModules is always an explicit array (never null) and
// defaultView is null when unset. The optional maintainer field is omitted —
// the maintainer module was dropped from the SDK.
type runtimeConfig struct {
	CityName          string   `json:"cityName"`
	CityRoot          string   `json:"cityRoot"`
	UseFixtures       bool     `json:"useFixtures"`
	ReadOnly          bool     `json:"readOnly"`
	OperatorAlias     string   `json:"operatorAlias"`
	OperatorWireAlias string   `json:"operatorWireAlias"`
	DecisionLabel     string   `json:"decisionLabel"`
	EnabledModules    []string `json:"enabledModules"`
	DefaultView       *string  `json:"defaultView"`
	// NavLinks is always an explicit array, never null, for the same reason
	// enabledModules is: decodeRuntimeConfig validates the field's presence
	// and shape, so a nil slice would fail the decode of every install that
	// declares no links -- which is all of them by default.
	NavLinks []navLinkJSON `json:"navLinks"`
}

// navLinkJSON is the wire shape of one NavLink. It is a separate type from
// NavLink so the json tags live on the wire boundary rather than on the
// parser's domain type, matching how the rest of this file projects rather
// than re-exports.
type navLinkJSON struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

func (p *Plane) registerConfig() {
	p.mux.HandleFunc("GET /api/city/{cityName}/config", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("cityName")
		root, ok := p.resolveCityPath(name)
		if !ok {
			writeError(w, http.StatusNotFound, "unknown city")
			return
		}
		writeJSON(w, http.StatusOK, p.runtimeConfigFor(name, root))
	})
}

// runtimeConfigFor projects the operator/runtime config for a city. Operator
// identity falls back to neutral, non-identifying defaults so an unconfigured
// install never bakes in a specific human (ZERO hardcoded roles); the real
// values come from gc config/env via Deps.
func (p *Plane) runtimeConfigFor(name, root string) runtimeConfig {
	alias := firstNonEmpty(p.deps.OperatorAlias, "operator")
	wire := firstNonEmpty(p.deps.OperatorWireAlias, "human")
	label := firstNonEmpty(p.deps.DecisionLabel, "needs/"+alias)

	mods := p.deps.EnabledModules
	if mods == nil {
		mods = []string{}
	}

	var defaultView *string
	if v := strings.TrimSpace(p.deps.DefaultView); v != "" {
		defaultView = &v
	}

	// Built with an explicit zero length rather than a var declaration: a nil
	// slice marshals to null, which the frontend decoder refuses.
	navLinks := make([]navLinkJSON, 0, len(p.deps.NavLinks))
	for _, l := range p.deps.NavLinks {
		// A direct conversion, not a field-by-field literal: the two types
		// are identical but for the json tags, and should they ever diverge
		// this stops compiling rather than silently projecting a stale shape.
		navLinks = append(navLinks, navLinkJSON(l))
	}

	return runtimeConfig{
		CityName:          name,
		CityRoot:          root,
		UseFixtures:       false,
		ReadOnly:          p.deps.ReadOnly,
		OperatorAlias:     alias,
		OperatorWireAlias: wire,
		DecisionLabel:     label,
		EnabledModules:    mods,
		DefaultView:       defaultView,
		NavLinks:          navLinks,
	}
}
