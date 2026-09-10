package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// An agent with no prompt_template must still be spawned with a worker prompt.
//
// WHY THIS SUITE EXISTS. `gc prime` falls back to the core pack's builtin
// worker prompt when an agent declares none, but the SPAWN path had no such
// fallback: resolveTemplate rendered the empty template to "" and shipped the
// beacon alone. That is not repaired by the SessionStart hook, because the
// beacon is a non-empty prompt, so promptDelivery marks the startup prompt
// DELIVERED and the hook's own `gc prime` output is then suppressed
// (managedSessionHookPromptAlreadyDelivered). The agent's whole behavioral
// specification is a one-line timestamp.
//
// The two halves are asserted separately and both are needed: proving the
// spawn prompt is beacon-only says nothing if the hook would have filled it
// in, and proving the hook suppresses says nothing if the spawn prompt were
// already complete.
//
// Run: go test ./cmd/gc/ -run SpawnBuiltinPrompt

// writeBuiltinPromptCity builds a fixture city whose core pack ships the
// builtin worker prompts, with one agent that declares no prompt_template.
func writeBuiltinPromptCity(t *testing.T) string {
	t.Helper()
	cityDir := t.TempDir()
	write := func(rel, data string) {
		path := filepath.Join(cityDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", path, err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatalf("WriteFile(%s): %v", path, err)
		}
	}
	write("pack.toml", "[pack]\nname = \"spawn-city\"\nschema = 2\n")
	write("city.toml", `
[providers.claude]
base = "builtin:claude"

[imports.core]
source = "packs/core"

[agent_defaults]
append_fragments = ["spawn-probe"]
`)
	write("template-fragments/spawn-probe.template.md",
		`{{ define "spawn-probe" }}SPAWN-PROBE-BODY{{ end }}`)
	write(".gc/site.toml", "workspace_name = \"spawn-city\"\n")
	write("packs/core/pack.toml", "[pack]\nname = \"core\"\nschema = 2\n")
	write("packs/core/assets/prompts/graph-worker.template.md",
		"# Graph Worker\n\nBUILTIN-GRAPH-WORKER-BODY\n")
	write("packs/core/assets/prompts/pool-worker.template.md",
		"# Pool Worker\n\nBUILTIN-POOL-WORKER-BODY\n")
	write("agents/bare-agent/agent.toml", "provider = \"claude\"\ndescription = \"no prompt_template\"\n")
	return cityDir
}

func TestSpawnBuiltinPromptReachesAgentWithNoPromptTemplate(t *testing.T) {
	cityDir := writeBuiltinPromptCity(t)
	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	var agentCfg config.Agent
	found := false
	for _, a := range cfg.Agents {
		if a.Name == "bare-agent" {
			agentCfg = a
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("bare-agent not in composed config")
	}
	if agentCfg.PromptTemplate != "" {
		t.Fatalf("fixture drifted: bare-agent has prompt_template %q, so this suite would prove nothing",
			agentCfg.PromptTemplate)
	}

	params := &agentBuildParams{
		fs: fsys.OSFS{},
		// Set exactly as buildAgentParams does in production: the builtin
		// prompt is resolved from the composed city, so a nil here would
		// make this suite pass over the defect it exists to catch.
		city:            cfg,
		cityName:        "spawn-city",
		cityPath:        cityDir,
		workspace:       &cfg.Workspace,
		providers:       cfg.Providers,
		lookPath:        func(string) (string, error) { return "/usr/bin/claude", nil },
		beaconTime:      testBeaconTime,
		packDirs:        cfg.PackDirs,
		globalFragments: cfg.Workspace.GlobalFragments,
		appendFragments: mergeFragmentLists(cfg.AgentDefaults.AppendFragments, cfg.AgentsDefaults.AppendFragments),
		beadNames:       make(map[string]string),
		stderr:          io.Discard,
	}
	tp, err := resolveTemplate(params, &agentCfg, agentCfg.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if !strings.Contains(tp.Prompt, "BUILTIN-GRAPH-WORKER-BODY") {
		t.Fatalf("spawned prompt carries no worker prompt, only the beacon: %q", tp.Prompt)
	}
	// The builtin has to arrive through the renderer, not a raw read, or the
	// city's append_fragments are dropped again on this path.
	if !strings.Contains(tp.Prompt, "SPAWN-PROBE-BODY") {
		t.Fatalf("spawned builtin prompt carries no city fragment: %q", tp.Prompt)
	}
}

// The SessionStart hook cannot be the repair: once the spawn payload counts as
// delivered, the hook suppresses its own prompt. Pinned here so a future
// reader does not "fix" the spawn path by relying on the hook.
func TestSpawnBuiltinPromptHookSuppressesAfterDelivery(t *testing.T) {
	t.Setenv(startupPromptDeliveredEnv, "1")
	t.Setenv(managedSessionHookEnv, "1")
	if !managedSessionHookPromptAlreadyDelivered(primeHookContext{HookEventName: "SessionStart"}) {
		t.Fatal("a managed SessionStart hook does not report the startup prompt already delivered; " +
			"the suppression this suite reasons about is gone and the spawn-path assertion above needs rereading")
	}
	var stdout bytes.Buffer
	writePrimePromptWithFormat(&stdout, "spawn-city", "bare-agent", "WORKER-PROMPT-BODY", true, "", true, "", nil)
	if strings.Contains(stdout.String(), "WORKER-PROMPT-BODY") {
		t.Fatalf("suppression is not in effect; hook output = %q", stdout.String())
	}
}
