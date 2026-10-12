package main

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// TestNudgeDrainInjectRecoversSessionKeyAtPromptBoundary pins that a session
// whose SessionStart capture never landed gets its provider session id
// persisted at the next prompt boundary, from the id the provider hands every
// UserPromptSubmit hook on stdin.
//
// SessionStart is the only capture point gc prime --hook has, and it runs
// once. When the store is unreachable at that instant -- a supervisor restart
// with Dolt still coming up -- the capture is dropped silently and nothing
// retries it. A keyless session's history then resolves only by work_dir, so
// two keyless sessions sharing a work_dir make each other's transcript
// unavailable for good (ci-1ibc32, ci-v5dmqg: the liaison's Slack relay went
// dark that way).
//
// The test drives the real `nudge drain --inject` entry with the payload on a
// pipe stdin, the shape the default claude.json UserPromptSubmit hook
// delivers, so it fails if the capture is not wired into that path.
//
// The PostToolUse row is the deliberate absence. A mid-turn drain fires on
// every tool call, and a store read there would multiply the capture's cost
// by the tool calls per turn for a failure the next prompt already repairs.
// It must NOT capture, so nobody "completes" the recovery by moving it there.
//
// The provider id is minted per run, so a key left by another test cannot
// satisfy the assertion.
func TestNudgeDrainInjectRecoversSessionKeyAtPromptBoundary(t *testing.T) {
	cases := []struct {
		name      string
		hookEvent string
		wantKept  bool
	}{
		{name: "UserPromptSubmit captures", hookEvent: "UserPromptSubmit", wantKept: true},
		{name: "PostToolUse does not", hookEvent: "PostToolUse", wantKept: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityDir, store := primeCaptureTestStore(t)
			// A real session always has a city config, and the drain resolves
			// its own target through it before the capture runs.
			writeMinimalCityToml(t, cityDir)
			id := createCaptureSessionBead(t, store, "claude")
			t.Setenv("GC_SESSION_ID", id)
			t.Setenv("GC_ALIAS", "")
			isolateProviderSessionEnv(t)

			providerID, err := sessionpkg.GenerateSessionKey()
			if err != nil {
				t.Fatalf("generating provider id: %v", err)
			}
			payload := fmt.Sprintf(
				`{"session_id":%q,"hook_event_name":%q,"transcript_path":"/nonexistent/%s.jsonl","prompt":"hi"}`+"\n",
				providerID, tc.hookEvent, providerID)

			var stdout, stderr bytes.Buffer
			withTestStdin(t, payload, func() {
				cmdNudgeDrainWithFormat(nil, true, "claude", tc.hookEvent, &stdout, &stderr)
			})

			got := reloadSessionKey(t, cityDir, id)
			if kept := got == providerID; kept != tc.wantKept {
				t.Fatalf("session_key = %q after %s drain, captured=%v, want captured=%v (stderr=%q)",
					got, tc.hookEvent, kept, tc.wantKept, stderr.String())
			}
		})
	}
}

// TestNudgeDrainInjectSkipsCaptureWhenTargetAlreadyKeyed pins the cost gate.
// The capture re-opens the city store, which measured +150-300 ms on a ~1.2 s
// UserPromptSubmit hook, so the drain withholds it when the session bead it
// already read carries a key. A call on a keyed session is a silent no-op, so
// no store assertion can see the gate. The test counts calls instead.
//
// The gemini row is a provider that never delivers its id on hook stdin. It
// stays keyless for good, so without the family check it would pay the
// store open on every prompt only to be refused.
//
// The PostToolUse row is the per-tool-call absence. A mid-turn call would get
// an empty id and no-op silently, so only the counter can catch one.
//
// The foreign-target row covers GC_ALIAS resolving to a session other than
// GC_SESSION_ID. That bead's key says nothing about this session's, so the
// capture must still run.
func TestNudgeDrainInjectSkipsCaptureWhenTargetAlreadyKeyed(t *testing.T) {
	cases := []struct {
		name          string
		ownKey        string
		ownProvider   string
		aliasForeign  bool
		hookEvent     string
		wantCaptureCt int
	}{
		{name: "keyed own session skips", ownKey: "already-captured", wantCaptureCt: 0},
		{name: "keyless own session captures", ownKey: "", wantCaptureCt: 1},
		{name: "foreign keyed target still captures", ownKey: "", aliasForeign: true, wantCaptureCt: 1},
		{name: "keyless session mid-turn does not", ownKey: "", hookEvent: "PostToolUse", wantCaptureCt: 0},
		{name: "keyless provider without stdin ids skips", ownKey: "", ownProvider: "gemini", wantCaptureCt: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityDir, store := primeCaptureTestStore(t)
			writeMinimalCityToml(t, cityDir)
			provider := tc.ownProvider
			if provider == "" {
				provider = "claude"
			}
			own, err := store.Create(beads.Bead{
				Title: "own", Type: "session",
				Metadata: map[string]string{"provider_kind": provider, "session_key": tc.ownKey},
			})
			if err != nil {
				t.Fatalf("create own session: %v", err)
			}
			t.Setenv("GC_SESSION_ID", own.ID)
			t.Setenv("GC_ALIAS", "")
			if tc.aliasForeign {
				foreign, err := store.Create(beads.Bead{
					Title: "foreign", Type: "session",
					Metadata: map[string]string{"provider_kind": "claude", "session_key": "foreign-key"},
				})
				if err != nil {
					t.Fatalf("create foreign session: %v", err)
				}
				t.Setenv("GC_ALIAS", foreign.ID)
			}
			isolateProviderSessionEnv(t)

			calls := 0
			prev := nudgeDrainPersistSessionKey
			nudgeDrainPersistSessionKey = func(string, string, io.Writer) { calls++ }
			t.Cleanup(func() { nudgeDrainPersistSessionKey = prev })

			event := tc.hookEvent
			if event == "" {
				event = "UserPromptSubmit"
			}
			payload := `{"session_id":"provider-id","hook_event_name":"` + event + `"}` + "\n"
			var stdout, stderr bytes.Buffer
			withTestStdin(t, payload, func() {
				cmdNudgeDrainWithFormat(nil, true, "claude", event, &stdout, &stderr)
			})
			if calls != tc.wantCaptureCt {
				t.Fatalf("capture called %d time(s), want %d (stderr=%q)", calls, tc.wantCaptureCt, stderr.String())
			}
		})
	}
}
