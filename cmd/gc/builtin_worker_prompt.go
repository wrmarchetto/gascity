package main

import (
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/config"
)

// Builtin worker prompts: what an agent that declares no prompt_template gets.
//
// The core bootstrap pack ships two, and which one applies is a config
// question, not a caller question -- so both delivery paths (`gc prime` and
// the spawn path in resolveTemplate) ask here rather than each spelling out
// the formula_v2 and pool-instance conditions. They diverged once already:
// `gc prime` had the fallback and the spawn path had none, so a spawned
// session for such an agent received only its beacon (bead ci-d2d19d).
//
// Both prompts are `.template.md` so that the city's append_fragments reach
// them; a plain `.md` is inert to fragments in renderPromptWithMeta.

// builtinWorkerPromptPath returns the absolute path of the core pack's builtin
// worker prompt for agent a, or "" when no builtin applies -- the agent
// declares its own prompt_template, the city composes no core pack, or
// formula v1 is in force and the agent is not a pool agent. An empty result is
// a normal outcome, not an error: the caller falls back to its own default.
func builtinWorkerPromptPath(cfg *config.City, a *config.Agent) string {
	if cfg == nil || a == nil || strings.TrimSpace(a.PromptTemplate) != "" {
		return ""
	}
	coreDir := cfg.PackDirByName("core")
	if coreDir == "" {
		return ""
	}
	// Pool instances have Pool=nil after resolution, so isPoolInstance checks
	// the template agent by name rather than trusting the resolved copy.
	switch {
	case cfg.Daemon.FormulaV2Enabled():
		return filepath.Join(coreDir, "assets", "prompts", "graph-worker.template.md")
	case a.SupportsInstanceExpansion() || isPoolInstance(cfg, *a):
		return filepath.Join(coreDir, "assets", "prompts", "pool-worker.template.md")
	default:
		return ""
	}
}
