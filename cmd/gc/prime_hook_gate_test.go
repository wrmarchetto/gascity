package main

// Tests for the SessionStart hook's managed-session gate across a session
// start that `gc session attach` drives (ci-j63574).
//
// Scope: primeHookHasLiveManagedSession, and doPrimeWithHookFormat end to end
// against a file-backed session bead. The suite exists because attach starts
// the runtime while the bead still reads `asleep` -- the state flips only
// after the provider is up -- and the attach start config carries no startup
// prompt, so the hook is the prompt's ONLY route. The gate rejected it on
// state alone, and the 2026-10-13 liaison launch f30b8a27 began with no
// prompt by any route. The invariant pinned here: a process carrying the
// session's incarnation token is admitted whatever the bead's lifecycle
// state, while the gate still refuses a process with no token, a wrong one,
// or an empty one matched against an empty bead field.
//
// Not represented: the real ordering inside Manager.ensureRunning, where the
// hook fires during sp.Start and confirmLiveSessionState runs after it. The
// bead is seeded `asleep` to stand for that window, because the window has
// no observable state of its own to drive.
//
// Run: go test ./cmd/gc/ -run 'TestPrimeHookGate' -count=1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
)

func seedPrimeGateCity(t *testing.T, prompt string) string {
	t.Helper()
	clearGCEnv(t)
	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "prompts", "worker.md"), []byte(prompt), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`
[workspace]
name = "gastown"

[[agent]]
name = "worker"
prompt_template = "prompts/worker.md"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_CITY", cityDir)
	return cityDir
}

// seedPrimeGateSession writes a session bead in the given state and token and
// points the process env at it, as the runtime start does.
func seedPrimeGateSession(t *testing.T, cityDir, state, beadToken, envToken string) {
	t.Helper()
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatal(err)
	}
	const sessionName = "gastown--worker"
	created, err := store.Create(beads.Bead{
		Title:  sessionName,
		Status: "open",
		Type:   sessionBeadType,
		Labels: []string{sessionBeadLabel, "agent:worker"},
		Metadata: map[string]string{
			"agent_name":     "worker",
			"session_name":   sessionName,
			"state":          state,
			"template":       "worker",
			"instance_token": beadToken,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_SESSION_ID", created.ID)
	t.Setenv("GC_SESSION_NAME", sessionName)
	t.Setenv("GC_TEMPLATE", "worker")
	t.Setenv("GC_AGENT", "worker")
	t.Setenv("GC_ALIAS", "worker")
	t.Setenv("GC_INSTANCE_TOKEN", envToken)
}

func TestPrimeHookGateAdmitsIncarnationTokenWhateverTheState(t *testing.T) {
	token := fmt.Sprintf("tok-%x", time.Now().UnixNano())
	for _, tc := range []struct {
		name      string
		state     string
		beadToken string
		envToken  string
		want      bool
	}{
		// The live states pass on state alone, token or not.
		{"active-no-token", "active", token, "", true},
		{"start-pending-no-token", "start-pending", token, "", true},
		// The attach-start window: not live yet, but this process IS the
		// incarnation the bead records.
		{"asleep-token-match", "asleep", token, token, true},
		{"suspended-token-match", "suspended", token, token, true},
		{"drained-token-match", "drained", token, token, true},
		// Still refused: the gate's original purpose.
		{"asleep-no-token", "asleep", token, "", false},
		{"asleep-wrong-token", "asleep", token, token + "-stale", false},
		{"asleep-both-empty", "asleep", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cityDir := seedPrimeGateCity(t, "unused\n")
			seedPrimeGateSession(t, cityDir, tc.state, tc.beadToken, tc.envToken)
			if got := primeHookHasLiveManagedSession(cityDir); got != tc.want {
				t.Fatalf("primeHookHasLiveManagedSession = %v, want %v", got, tc.want)
			}
		})
	}
}

// End to end through the hook entry point: the attach-start window must
// produce the prompt, not an empty hook. The prompt is salted so a stale
// fixture cannot satisfy the assertion.
func TestPrimeHookGateDeliversPromptDuringAttachStart(t *testing.T) {
	salt := fmt.Sprintf("%x", time.Now().UnixNano())
	prompt := "ATTACH-START-PROMPT-" + salt + "\n"
	cityDir := seedPrimeGateCity(t, prompt)
	seedPrimeGateSession(t, cityDir, "asleep", "tok-"+salt, "tok-"+salt)
	t.Setenv(managedSessionHookEnv, "1")
	t.Setenv("GC_HOOK_SOURCE", "startup")
	t.Setenv("GC_HOOK_EVENT_NAME", "SessionStart")
	t.Setenv(startupPromptDeliveredEnv, "")
	withPrimeHookStdin(t)

	var stdout, stderr bytes.Buffer
	if code := doPrimeWithHookFormat(nil, &stdout, &stderr, true, hookOutputFormatCodex, false); code != 0 {
		t.Fatalf("doPrimeWithHookFormat = %d; stderr=%q", code, stderr.String())
	}
	var got struct {
		HookSpecificOutput struct {
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("hook output is not JSON: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(got.HookSpecificOutput.AdditionalContext, strings.TrimSpace(prompt)) {
		t.Fatalf("hook during attach start did not deliver the prompt; context=%q", got.HookSpecificOutput.AdditionalContext)
	}
}
