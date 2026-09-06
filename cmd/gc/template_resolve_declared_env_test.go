package main

import (
	"io"
	"slices"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
)

// Scope: the provenance half of the ci-yulan1 fix -- that resolveTemplate marks
// the env keys a user DECLARED, and marks nothing it merely swept out of the
// controller's process environment. runtime.CoreFingerprint's use of that set is
// pinned separately in internal/runtime/fingerprint_declared_env_test.go; this
// suite cannot see a hashing regression and does not try to.
//
// It exists because the two ends are otherwise tested apart: resolveTemplate
// writes DeclaredEnvKeys and only the fingerprint reads it, so a producer that
// wrote the wrong set -- or stopped writing one -- passes every test on either
// side. The pins below are on the SET the fingerprint consumes, taken from the
// same TemplateParams the hash path receives.
//
// Run: go test ./cmd/gc/ -run DeclaredEnv

func declaredEnvBuildParams(t *testing.T, cityPath string, providers map[string]config.ProviderSpec, ws *config.Workspace) *agentBuildParams {
	t.Helper()
	return &agentBuildParams{
		cityName:   "city",
		cityPath:   cityPath,
		workspace:  ws,
		providers:  providers,
		lookPath:   func(string) (string, error) { return "/bin/echo", nil },
		fs:         fsys.OSFS{},
		beaconTime: time.Unix(0, 0),
		beadNames:  make(map[string]string),
		stderr:     io.Discard,
	}
}

// The incident shape exactly: an env key set in a provider's [env] block. Before
// the fix this key reached the session and contributed to no fingerprint, so
// editing it moved nothing.
func TestDeclaredEnvKeysCoverProviderEnvBlock(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	params := declaredEnvBuildParams(t, cityPath,
		map[string]config.ProviderSpec{"test": {
			Command:    "echo",
			PromptMode: "none",
			Env:        map[string]string{"CLAUDE_ACCOUNTS": "0 4"},
		}},
		&config.Workspace{Provider: "test"})

	agent := &config.Agent{Name: "runner"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if !slices.Contains(tp.DeclaredEnvKeys, "CLAUDE_ACCOUNTS") {
		t.Fatalf("provider [env] key missing from DeclaredEnvKeys: %v", tp.DeclaredEnvKeys)
	}
	if tp.Env["CLAUDE_ACCOUNTS"] != "0 4" {
		t.Fatalf("provider env value = %q, want %q", tp.Env["CLAUDE_ACCOUNTS"], "0 4")
	}
	cfg := templateParamsToConfig(tp)
	if !slices.Contains(cfg.DeclaredEnvKeys, "CLAUDE_ACCOUNTS") {
		t.Fatalf("DeclaredEnvKeys did not reach runtime.Config: %v", cfg.DeclaredEnvKeys)
	}
}

func TestDeclaredEnvKeysCoverWorkspaceAndAgentEnvBlocks(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	params := declaredEnvBuildParams(t, cityPath,
		map[string]config.ProviderSpec{"test": {Command: "echo", PromptMode: "none"}},
		&config.Workspace{Provider: "test", Env: map[string]string{"WORKSPACE_KEY": "w"}})

	agent := &config.Agent{Name: "runner", Env: map[string]string{"AGENT_KEY": "a"}}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	for _, key := range []string{"WORKSPACE_KEY", "AGENT_KEY"} {
		if !slices.Contains(tp.DeclaredEnvKeys, key) {
			t.Errorf("%s missing from DeclaredEnvKeys: %v", key, tp.DeclaredEnvKeys)
		}
	}
}

// The exclusion is the load-bearing half. PATH, HOME and the Claude auth vars
// arrive from processenv.ProviderProcessPassthroughEnv, not from anyone's config;
// admitting them would make a credential rotation or a supervisor restarted from
// a different shell a fleet-wide config-drift restart. GC_ identity vars are
// likewise gc's own injection, already governed by envFingerprintAllow.
func TestDeclaredEnvKeysExcludeAmbientPassthrough(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-secret")

	// The provider MUST declare something. With no layer declaring anything the
	// set is nil, every non-membership assertion below is true against an empty
	// slice, and the test cannot tell "ambient keys excluded" from "no set
	// produced at all" -- it passed against a producer that swept the WHOLE
	// merged env into the declared set, which is the provenance-blind rule this
	// design rejects.
	params := declaredEnvBuildParams(t, cityPath,
		map[string]config.ProviderSpec{"test": {
			Command:    "echo",
			PromptMode: "none",
			Env:        map[string]string{"DECLARED_ONE": "yes"},
		}},
		&config.Workspace{Provider: "test"})

	agent := &config.Agent{Name: "runner"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	// Confirm the ambient vars really are in the session env, so the assertion
	// below means "present and excluded" rather than "never arrived".
	for _, key := range []string{"PATH", "HOME", "CLAUDE_CODE_OAUTH_TOKEN"} {
		if tp.Env[key] == "" {
			t.Fatalf("%s absent from the resolved env -- this test would pass vacuously", key)
		}
	}
	if len(tp.Env) < 10 {
		t.Fatalf("resolved env has only %d keys; the ambient sweep did not run and "+
			"there is nothing here to exclude", len(tp.Env))
	}
	// EQUALITY, not non-membership. Listing forbidden keys can only catch the
	// ones somebody thought to list; the set is small and fully determined, so
	// state it exactly and let anything extra fail.
	if !slices.Equal(tp.DeclaredEnvKeys, []string{"DECLARED_ONE"}) {
		t.Errorf("DeclaredEnvKeys = %v, want exactly [DECLARED_ONE] out of %d env keys",
			tp.DeclaredEnvKeys, len(tp.Env))
	}
}

// An upstream's serving env is a resolved CREDENTIAL, and runtime.Config.Upstream
// documents that only the selected NAME is fingerprinted. A provider that also
// names one of those keys in its own [env] block must not smuggle the resolved
// value into the hash through the declared set -- so the upstream block's writes
// are subtracted, whatever declared them first.
func TestDeclaredEnvKeysExcludeUpstreamServingKeys(t *testing.T) {
	cityPath := t.TempDir()
	writeTemplateResolveCityConfig(t, cityPath, "file")
	params := declaredEnvBuildParams(t, cityPath,
		map[string]config.ProviderSpec{"test": {
			Command:    "echo",
			PromptMode: "none",
			// Declared first, then overwritten by the upstream block below.
			// RAW_UPSTREAM_KEY is declared HERE as well as written by the
			// upstream's raw env below. Without the collision the key was never
			// in the declared set, so its assertion held for every
			// implementation and the raw-env subtraction site had no test at
			// all -- deleting that `delete` left the whole package green.
			Env: map[string]string{
				"ANTHROPIC_API_KEY": "from-provider",
				"RAW_UPSTREAM_KEY":  "from-provider",
				"KEEP_ME":           "yes",
			},
			UpstreamEnv: config.UpstreamEnvBinding{BaseURL: "ANTHROPIC_BASE_URL", APIKey: "ANTHROPIC_API_KEY"},
		}},
		&config.Workspace{Provider: "test"})
	params.city = &config.City{Upstreams: map[string]config.UpstreamSpec{
		"gateway": {BaseURL: "https://gw.example", APIKey: "sk-resolved", Env: map[string]string{"RAW_UPSTREAM_KEY": "r"}},
	}}

	agent := &config.Agent{Name: "runner", Upstream: "gateway"}
	tp, err := resolveTemplate(params, agent, agent.QualifiedName(), nil)
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if tp.Env["ANTHROPIC_API_KEY"] != "sk-resolved" {
		t.Fatalf("upstream did not win the key: %q", tp.Env["ANTHROPIC_API_KEY"])
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "RAW_UPSTREAM_KEY"} {
		if slices.Contains(tp.DeclaredEnvKeys, key) {
			t.Errorf("upstream-written %s must not be declared: %v", key, tp.DeclaredEnvKeys)
		}
	}
	if !slices.Contains(tp.DeclaredEnvKeys, "KEEP_ME") {
		t.Errorf("subtracting upstream keys must not drop the provider's other keys: %v", tp.DeclaredEnvKeys)
	}
}
