package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/session/sessiontest"
	"github.com/gastownhall/gascity/internal/sessionlog"
)

// TestPreparedStartStaleResumeGuardSearchesObservePaths pins that the
// stale-resume guard in buildPreparedStart looks for a keyed transcript where
// the city says transcripts live (daemon.observe_paths), not only under the
// provider default root.
//
// A city that sets CLAUDE_CONFIG_DIR per account writes every transcript under
// that account's home, so the default ~/.claude/projects never holds one. A
// guard that probes the default alone judges every live resume key stale,
// clears it on the next wake, and the session restarts fresh with no
// session_key -- which is how the liaison lost its transcript and its Slack
// relay on 2026-10-12 (ci-1ibc32).
//
// HOME is a temp dir so the default root is empty and the transcript exists
// ONLY under the observe path. The absent row is the control: it proves the
// probe ran for this candidate (work_dir and provider resolve), so the kept row
// cannot pass merely because the guard was skipped.
func TestPreparedStartStaleResumeGuardSearchesObservePaths(t *testing.T) {
	cases := []struct {
		name          string
		writeUnderObs bool
		wantKeyKept   bool
		wantResetFlag string
	}{
		{name: "transcript under observe path keeps the key", writeUnderObs: true, wantKeyKept: true, wantResetFlag: ""},
		{name: "transcript absent everywhere clears the key", writeUnderObs: false, wantKeyKept: false, wantResetFlag: "true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("GC_HOME", filepath.Join(home, ".gc"))
			t.Setenv("USERPROFILE", "")

			workDir := filepath.Join(t.TempDir(), "city")
			if err := os.MkdirAll(workDir, 0o755); err != nil {
				t.Fatalf("mkdir workdir: %v", err)
			}
			// Salted per run so a transcript left by another test cannot
			// satisfy the lookup.
			key, err := sessionpkg.GenerateSessionKey()
			if err != nil {
				t.Fatalf("generating session key: %v", err)
			}
			observe := filepath.Join(home, ".claude-homes", "account2", ".claude", "projects")
			if tc.writeUnderObs {
				dir := filepath.Join(observe, sessionlog.ProjectSlug(workDir))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatalf("mkdir transcript dir: %v", err)
				}
				if err := os.WriteFile(filepath.Join(dir, key+".jsonl"), []byte("{}\n"), 0o600); err != nil {
					t.Fatalf("write transcript: %v", err)
				}
			}

			store := beads.NewMemStore()
			session, err := store.Create(beads.Bead{
				Title:  "liaison",
				Type:   sessionBeadType,
				Labels: []string{sessionBeadLabel},
				Metadata: map[string]string{
					"session_name":        "liaison",
					"template":            "liaison",
					"state":               "asleep",
					"work_dir":            workDir,
					"provider":            "claude",
					"provider_kind":       "claude",
					"session_key":         key,
					"started_config_hash": "cfg",
				},
			})
			if err != nil {
				t.Fatalf("Create(session): %v", err)
			}
			candidate := startCandidate{
				info: sessiontest.SeedBead(t, session),
				tp: TemplateParams{
					TemplateName: "liaison",
					SessionName:  "liaison",
					Command:      "claude",
				},
			}
			cfg := &config.City{Daemon: config.DaemonConfig{ObservePaths: []string{observe}}}
			if _, _, err := buildPreparedStart(candidate, cfg, store); err != nil {
				t.Fatalf("buildPreparedStart: %v", err)
			}

			got, err := store.Get(session.ID)
			if err != nil {
				t.Fatalf("Get(session): %v", err)
			}
			if kept := got.Metadata["session_key"] == key; kept != tc.wantKeyKept {
				t.Errorf("session_key = %q, kept=%v, want kept=%v", got.Metadata["session_key"], kept, tc.wantKeyKept)
			}
			if flag := got.Metadata["continuation_reset_pending"]; flag != tc.wantResetFlag {
				t.Errorf("continuation_reset_pending = %q, want %q", flag, tc.wantResetFlag)
			}
		})
	}
}
