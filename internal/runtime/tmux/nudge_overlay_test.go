// Package tmux test: overlay recognition for the nudge submit re-send.
//
// Scope: paneShowsEnterOverlay, driven by pane captures taken from the real
// TUIs the city runs -- Claude Code 2.1.268 and codex-cli 0.153.4 -- rather
// than from hand-written approximations of them. The fixtures live in
// testdata/panes/ and are unedited except that the probe's absolute directory
// was replaced with <probe-dir>.
//
// Why the fixtures are real: a hand-written "palette" is written by the same
// person writing the matcher, so it agrees with the matcher by construction and
// says nothing about whether either resembles the pane. The whole defect being
// guarded here is a predicate that reads a live pane wrongly.
//
// What this suite CANNOT represent, stated so the manual check is not mistaken
// for redundant: a capture is one frame. It cannot show that Enter advances a
// picker rather than submitting, and it cannot show a TUI changing its chrome
// in a future release. Those were established by driving the TUIs (recorded on
// ci-vkadhb and in the nudge_overlay.go header) and would have to be
// re-established against a new vendor build.
//
// Run:
//
//	go test ./internal/runtime/tmux/ -run Overlay
package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func paneFixture(t *testing.T, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "panes", name+".txt"))
	if err != nil {
		t.Fatalf("read pane fixture %s: %v", name, err)
	}
	// Mirrors CapturePaneLines, which splits capture-pane output on "\n".
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}

// TestPaneShowsEnterOverlayOnRealCaptures is the table the guard exists for.
// Both families appear on both sides so neither the positives nor the
// negatives can be satisfied by a rule that only knows one TUI.
func TestPaneShowsEnterOverlayOnRealCaptures(t *testing.T) {
	cases := []struct {
		fixture string
		want    bool
		why     string
	}{
		{"codex-palette", true, "codex's command palette, the state that walked /model to a committed change"},
		{"codex-palette-filtered", true, "the same palette narrowed to one entry -- still an overlay, and one Enter accepts it"},
		{"codex-trust-prompt", true, "the directory-trust prompt: no busy indicator, and the preselected entry is 'Yes, continue'"},
		{"claude-palette", true, "claude renders the same row shape, so one predicate covers both"},
		{"codex-idle", false, "an idle composer must stay re-sendable or the guard has disabled the loop"},
		{"claude-idle", false, "likewise for claude"},
		{"claude-draft-no-palette", false, "a long '/'-prefixed draft that matched no command: claude shows NO palette here, so Enter does mean submit"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			if got := paneShowsEnterOverlay(paneFixture(t, tc.fixture)); got != tc.want {
				t.Fatalf("paneShowsEnterOverlay = %v, want %v: %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestPaneShowsEnterOverlayIgnoresPathsAndProse pins the two shapes a bare
// "line contains a slash command" rule would swallow. Both are transcript
// content, and a false positive costs a skipped re-send rather than a wrong
// keystroke -- but a guard that fires on every pane holding a file path has
// silently turned the confirm loop back off for everybody.
func TestPaneShowsEnterOverlayIgnoresPathsAndProse(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"absolute path", "  /usr/bin/foo"},
		{"path with a trailing column", "  /usr/bin/foo   exists"},
		{"bare directory", "    /tmp"},
		{"command at column zero", "/model  choose what model to use"},
		{"single space before the description", "  /model choose what model to use"},
		{"uppercase token", "  /Model  choose what model to use"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if paneShowsEnterOverlay([]string{tc.line}) {
				t.Fatalf("paneShowsEnterOverlay matched %q, which is not a palette row", tc.line)
			}
		})
	}
}

// TestPaneShowsEnterOverlayEmptyPane pins the degenerate input. An empty or
// unreadable capture must not read as an overlay: that direction would suppress
// every re-send, which is the ga-bwm lost-Enter failure the loop exists for.
func TestPaneShowsEnterOverlayEmptyPane(t *testing.T) {
	for _, lines := range [][]string{nil, {}, {""}} {
		if paneShowsEnterOverlay(lines) {
			t.Fatalf("paneShowsEnterOverlay(%#v) = true, want false", lines)
		}
	}
}
