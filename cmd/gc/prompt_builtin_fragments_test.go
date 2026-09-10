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

// City-wide `[agent_defaults] append_fragments` must reach an agent whatever
// shape its prompt has: a `.template.md`, a plain `.md`, or no prompt_template
// at all (a builtin worker prompt from the core pack).
//
// WHY THIS SUITE EXISTS. Every pre-existing fragment test declares a
// `.template.md` prompt_template, so all three of them pass while two of the
// three shapes silently drop every city fragment. Measured against the live
// city 2026-09-10: 21 active agents -- every implicit `<provider>` agent in
// every rig, plus `core.control-dispatcher` and `analyst-fable` -- received
// none of the four declared city fragments, and nothing was printed on stderr
// because the append loop never ran to report a miss.
//
// The expected fragment name is read from the loaded config rather than
// written as a literal, so a fragment added to the fixture city cannot escape
// the assertion.
//
// Run: go test ./cmd/gc/ -run CityAppendFragmentsReach

// writeFragmentCity builds a fixture city declaring one city-wide fragment and
// one agent per prompt shape, and returns its path. The core pack carries the
// builtin worker prompts as plain `.md`, which is how the real core pack ships
// them.
func writeFragmentCity(t *testing.T) string {
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
	write("pack.toml", "[pack]\nname = \"frag-city\"\nschema = 2\n")
	write("city.toml", `
[providers.claude]
base = "builtin:claude"

[imports.core]
source = "packs/core"

[agent_defaults]
append_fragments = ["work-record-probe"]
`)
	write(".gc/site.toml", "workspace_name = \"frag-city\"\n")
	write("template-fragments/work-record-probe.template.md",
		`{{ define "work-record-probe" }}WORK-RECORD-PROBE-BODY{{ end }}`)

	write("packs/core/pack.toml", "[pack]\nname = \"core\"\nschema = 2\n")
	write("packs/core/assets/prompts/graph-worker.template.md", "# Graph Worker\n\nbuiltin graph body\n")
	write("packs/core/assets/prompts/pool-worker.template.md", "# Pool Worker\n\nbuiltin pool body\n")
	// Plain-suffixed copies of the same two prompts, under the names the core
	// pack used before the rename. Nothing in the fixture city selects them.
	// They exist so that reverting a `.template.md` path in the production
	// code fails the assertion for the RIGHT reason -- a prompt rendered with
	// no fragment appended -- instead of a missing-file error, which would
	// pass whatever the fragment behavior. Mutation-checked: without these,
	// the config.go row reads UNATTRIBUTABLE.
	write("packs/core/assets/prompts/graph-worker.md", "# Graph Worker\n\nbuiltin graph body\n")
	write("packs/core/assets/prompts/pool-worker.md", "# Pool Worker\n\nbuiltin pool body\n")

	write("agents/tmpl-agent/agent.toml",
		"provider = \"claude\"\nprompt_template = \"agents/tmpl-agent/prompt.template.md\"\n")
	write("agents/tmpl-agent/prompt.template.md", "template agent body")

	write("agents/plain-agent/agent.toml",
		"provider = \"claude\"\nprompt_template = \"agents/plain-agent/prompt.md\"\n")
	write("agents/plain-agent/prompt.md", "plain agent body")

	// No prompt_template: falls through to the core pack's builtin prompt.
	write("agents/bare-agent/agent.toml", "provider = \"claude\"\ndescription = \"no prompt_template\"\n")

	return cityDir
}

// cityAppendFragmentNames returns the fragment names the fixture city declares,
// read from the same config field the production code reads. A literal here
// would let a fragment be added to the city and skipped by the assertion.
func cityAppendFragmentNames(t *testing.T, cityDir string) []string {
	t.Helper()
	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}
	names := cfg.AgentDefaults.AppendFragments
	if len(names) == 0 {
		t.Fatalf("fixture city declares no [agent_defaults] append_fragments; the suite would be vacuous")
	}
	return names
}

func TestCityAppendFragmentsReachEveryPromptShape(t *testing.T) {
	clearGCEnv(t)
	cityDir := writeFragmentCity(t)
	if got := cityAppendFragmentNames(t, cityDir); len(got) != 1 || got[0] != "work-record-probe" {
		t.Fatalf("fixture append_fragments = %v, want exactly [work-record-probe]", got)
	}

	t.Setenv("GC_CITY", cityDir)
	t.Setenv("GC_ALIAS", "")
	t.Setenv("GC_AGENT", "")

	for _, tc := range []struct {
		agent string
		shape string
		want  bool
	}{
		{"tmpl-agent", ".template.md prompt_template", true},
		{"bare-agent", "no prompt_template -- builtin worker prompt", true},
		{"claude", "implicit provider agent -- core pool-worker prompt", true},
		// A hand-written plain `.md` stays inert on purpose, pinned by
		// TestRenderPromptPatchedPlainMarkdownStaysInert: a patched prompt
		// file is delivered verbatim, template syntax and all. The opt-in is
		// renaming the file to `.template.md`, which is exactly what the two
		// core builtins above now do. Asserted here rather than left out so
		// the asymmetry reads as a decision instead of a gap.
		{"plain-agent", "plain .md prompt_template", false},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := doPrimeWithMode([]string{tc.agent}, &stdout, &stderr, false, true); code != 0 {
				t.Fatalf("doPrime(%s) = %d, want 0; stderr=%q", tc.agent, code, stderr.String())
			}
			got := strings.Contains(stdout.String(), "WORK-RECORD-PROBE-BODY")
			if got != tc.want {
				t.Fatalf("%s (%s): city fragment present = %v, want %v.\nstdout=%q\nstderr=%q",
					tc.agent, tc.shape, got, tc.want, stdout.String(), stderr.String())
			}
		})
	}
}

// The spawn path renders its own prompt (resolveTemplate), so a fix applied
// only to `gc prime` would leave every real session unchanged.
func TestCityAppendFragmentsReachSpawnedPromptShapes(t *testing.T) {
	cityDir := writeFragmentCity(t)
	cfg, _, err := config.LoadWithIncludes(fsys.OSFS{}, filepath.Join(cityDir, "city.toml"))
	if err != nil {
		t.Fatalf("LoadWithIncludes: %v", err)
	}

	for _, name := range []string{"tmpl-agent", "claude"} {
		t.Run(name, func(t *testing.T) {
			var agentCfg config.Agent
			found := false
			for _, a := range cfg.Agents {
				if a.Name == name && a.Dir == "" {
					agentCfg = a
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("agent %q not in composed config", name)
			}
			params := &agentBuildParams{
				fs:              fsys.OSFS{},
				cityName:        "frag-city",
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
				t.Fatalf("resolveTemplate(%s): %v", name, err)
			}
			if !strings.Contains(tp.Prompt, "WORK-RECORD-PROBE-BODY") {
				t.Fatalf("%s: spawned prompt is missing the city-wide fragment: %q", name, tp.Prompt)
			}
		})
	}
}
