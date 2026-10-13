package main

// Tests for the SessionStart hook's inline-size budget (ci-j63574).
//
// Scope: writePrimePromptWithFormat, the one writer every `gc prime --hook`
// branch funnels through, driven for every hook format gc emits. The suite
// exists because a 16,844-byte liaison prompt delivered by hook reached the
// model as a 2KB preview, and nothing in gc could see it happen: the hook
// exited 0 and printed every byte. The invariant pinned here is that the
// model-visible part of the hook output never exceeds what Claude Code
// delivers inline, whatever the prompt size, and that an oversize prompt is
// written in full to a file the visible pointer names.
//
// The bound asserted is the MEASURED one (measuredInlineDeliveredBytes), not
// primeHookInlineBudgetBytes, so raising the budget past what was observed to
// arrive inline turns this suite red instead of moving both sides at once.
//
// Not represented: Claude Code's own persistence threshold, which lives in
// the provider and moves with its version. The measurement behind the bound
// is recorded at primeHookInlineBudgetBytes, and re-measuring it is a manual
// transcript survey, not something this suite can do.
//
// Run: go test ./cmd/gc/ -run 'TestPrimeHook' -count=1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
)

// measuredInlineDeliveredBytes is the largest SessionStart additionalContext
// observed reaching the model inline, from a survey of 1,291 city transcripts
// on 2026-10-13 (Claude Code 2.1.296). The smallest hook output Claude Code
// persisted to a file instead was 10,300 bytes.
const measuredInlineDeliveredBytes = 8651

// hookVisibleContent extracts what the provider hands the model from one
// hook invocation's stdout, per wire format.
func hookVisibleContent(t *testing.T, format, stdout string) string {
	t.Helper()
	switch format {
	case "", hookOutputFormatClaude:
		return stdout
	case hookOutputFormatCodex, hookOutputFormatGemini:
		var got struct {
			HookSpecificOutput struct {
				AdditionalContext string `json:"additionalContext"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil {
			t.Fatalf("format %q: hook output is not JSON: %v; stdout=%q", format, err, stdout)
		}
		return got.HookSpecificOutput.AdditionalContext
	case hookOutputFormatAntigravity:
		var got struct {
			InjectSteps []struct {
				EphemeralMessage string `json:"ephemeralMessage"`
			} `json:"injectSteps"`
		}
		if err := json.Unmarshal([]byte(stdout), &got); err != nil || len(got.InjectSteps) != 1 {
			t.Fatalf("format %q: unexpected hook output: %v; stdout=%q", format, err, stdout)
		}
		return got.InjectSteps[0].EphemeralMessage
	}
	t.Fatalf("hookVisibleContent: no extractor for format %q", format)
	return ""
}

// saltedBody returns n bytes that begin and end with a per-run marker, so a
// spill file left by an earlier run, or a body cut anywhere, cannot match.
func saltedBody(t *testing.T, n int) (body, head, tail string) {
	t.Helper()
	salt := fmt.Sprintf("%x", time.Now().UnixNano())
	head = "HEAD-" + salt + "\n"
	tail = "\nTAIL-" + salt
	fill := n - len(head) - len(tail)
	if fill < 0 {
		t.Fatalf("saltedBody(%d) is smaller than its markers", n)
	}
	return head + strings.Repeat("p", fill) + tail, head, tail
}

func spillFiles(t *testing.T, cityPath string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(citylayout.RuntimeDataDir(cityPath), primeHookSpillDirName, "*"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
}

var allPrimeHookFormats = []string{
	"", hookOutputFormatClaude, hookOutputFormatCodex,
	hookOutputFormatGemini, hookOutputFormatAntigravity,
}

// The core invariant, swept across every wire format and across prompt sizes
// either side of the limit. 4096 sits well inside it, 9000 lies between the
// largest measured inline delivery and the smallest measured persist (so a
// budget raised into that gap goes red here), 16844 is the liaison prompt
// that was cut on 2026-10-13, and 200000 is far past any single read.
func TestPrimeHookOutputNeverExceedsMeasuredInlineLimit(t *testing.T) {
	for _, format := range allPrimeHookFormats {
		for _, size := range []int{4096, 9000, 16844, 200000} {
			t.Run(fmt.Sprintf("format=%q/size=%d", format, size), func(t *testing.T) {
				cityPath := t.TempDir()
				t.Setenv("GC_SESSION_NAME", "city--worker-1")
				body, head, tail := saltedBody(t, size)
				var stdout, stderr bytes.Buffer
				writePrimePromptWithFormat(&stdout, &stderr, cityPath, "city", "worker", body, true, format, false, "", nil)
				visible := hookVisibleContent(t, format, stdout.String())
				if len(visible) > measuredInlineDeliveredBytes {
					t.Fatalf("model-visible hook content is %d bytes, over the %d measured to arrive inline", len(visible), measuredInlineDeliveredBytes)
				}
				if stderr.Len() != 0 {
					t.Fatalf("stderr = %q, want silence on a successful write", stderr.String())
				}
				files := spillFiles(t, cityPath)
				if size < measuredInlineDeliveredBytes/2 {
					if !strings.Contains(visible, body) {
						t.Fatalf("a %d-byte prompt was not delivered inline", size)
					}
					if len(files) != 0 {
						t.Fatalf("an inline delivery left spill files %v", files)
					}
					return
				}
				if strings.Contains(visible, head) || strings.Contains(visible, tail) {
					t.Fatalf("visible content carries part of the prompt body, which reads as a delivered prompt; visible=%q", visible)
				}
				if len(files) != 1 {
					t.Fatalf("spill files = %v, want exactly one", files)
				}
				if !strings.Contains(visible, files[0]) {
					t.Fatalf("visible pointer does not name the spill file %q; visible=%q", files[0], visible)
				}
				data, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(string(data), body) {
					t.Fatalf("spill file does not open with the full prompt byte for byte (len %d, want >= %d)", len(data), len(body))
				}
				if !strings.Contains(string(data), "[city] worker") {
					t.Fatal("spill file lost the session beacon")
				}
			})
		}
	}
}

// The edges of the budget itself. These alone read the constant, because
// the property is about where the switch happens, and the measured bound is
// held by the sweep above.
func TestPrimeHookSpillSwitchesExactlyPastBudget(t *testing.T) {
	for _, tc := range []struct {
		visible int
		spilled bool
	}{
		{primeHookInlineBudgetBytes, false},
		{primeHookInlineBudgetBytes + 1, true},
	} {
		t.Run(fmt.Sprint(tc.visible), func(t *testing.T) {
			cityPath := t.TempDir()
			t.Setenv("GC_SESSION_NAME", "city--worker-1")
			// No beacon: an empty city name skips it, so the whole visible
			// content is the body and its size is exact.
			body, _, _ := saltedBody(t, tc.visible)
			var stdout, stderr bytes.Buffer
			writePrimePromptWithFormat(&stdout, &stderr, cityPath, "", "", body, true, hookOutputFormatCodex, false, "", nil)
			visible := hookVisibleContent(t, hookOutputFormatCodex, stdout.String())
			if got := len(spillFiles(t, cityPath)) == 1; got != tc.spilled {
				t.Fatalf("%d bytes: spilled=%v, want %v", tc.visible, got, tc.spilled)
			}
			if !tc.spilled && visible != body {
				t.Fatalf("%d bytes at the budget were not delivered verbatim", tc.visible)
			}
		})
	}
}

// The suppressed path carries no prompt, but its suffix -- step reminder,
// auto-handoff mail, unread mail -- is unbounded, so it is held to the same
// budget. A suite that only grew the prompt would pass over a 50KB handoff.
func TestPrimeHookSpillCoversOversizeContextSuffix(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_SESSION_NAME", "city--worker-1")
	suffix, head, tail := saltedBody(t, 50000)
	var stdout, stderr bytes.Buffer
	writePrimePromptWithFormat(&stdout, &stderr, cityPath, "city", "worker", "ALREADY-DELIVERED", true, hookOutputFormatCodex, true, suffix, nil)
	visible := hookVisibleContent(t, hookOutputFormatCodex, stdout.String())
	if len(visible) > measuredInlineDeliveredBytes {
		t.Fatalf("visible content is %d bytes", len(visible))
	}
	files := spillFiles(t, cityPath)
	if len(files) != 1 {
		t.Fatalf("spill files = %v, want one", files)
	}
	data, _ := os.ReadFile(files[0])
	if !strings.Contains(string(data), head) || !strings.Contains(string(data), tail) {
		t.Fatal("spill file does not carry the whole suffix")
	}
	if strings.Contains(string(data), "ALREADY-DELIVERED") {
		t.Fatal("suppression was lost on the spill path")
	}
}

// Two concurrent sessions of one pooled agent must not overwrite each other's
// prompt, so the file is keyed by session, not by agent.
func TestPrimeHookSpillIsPerSession(t *testing.T) {
	cityPath := t.TempDir()
	bodies := map[string]string{}
	for _, session := range []string{"city--worker-1", "city--worker-2"} {
		t.Setenv("GC_SESSION_NAME", session)
		body, _, _ := saltedBody(t, 20000)
		bodies[session] = body
		var stdout, stderr bytes.Buffer
		writePrimePromptWithFormat(&stdout, &stderr, cityPath, "city", "worker", body, true, "", false, "", nil)
	}
	files := spillFiles(t, cityPath)
	if len(files) != 2 {
		t.Fatalf("spill files = %v, want one per session", files)
	}
	for session, body := range bodies {
		path := filepath.Join(citylayout.RuntimeDataDir(cityPath), primeHookSpillDirName, session+".md")
		data, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(data), body) {
			t.Fatalf("session %s: spill file %s does not hold its own prompt (err=%v)", session, path, err)
		}
	}
}

// A spill that cannot be written must say so and fall back to the full
// content: Claude Code then shows its own preview, which is worse than the
// pointer but better than a pointer to a file that does not exist.
func TestPrimeHookSpillWriteFailureFallsBackLoudly(t *testing.T) {
	cityPath := t.TempDir()
	t.Setenv("GC_SESSION_NAME", "city--worker-1")
	// A regular file where the runtime dir must be makes MkdirAll fail for
	// real, with no switch in the code under test.
	if err := os.MkdirAll(filepath.Dir(citylayout.RuntimeDataDir(cityPath)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(citylayout.RuntimeDataDir(cityPath), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, _, _ := saltedBody(t, 20000)
	var stdout, stderr bytes.Buffer
	delivered := false
	writePrimePromptWithFormat(&stdout, &stderr, cityPath, "city", "worker", body, true, "", false, "", func() { delivered = true })
	if !strings.Contains(stdout.String(), body) {
		t.Fatal("a failed spill did not fall back to the full content")
	}
	if !strings.Contains(stderr.String(), "gc prime") || !strings.Contains(stderr.String(), primeHookSpillDirName) {
		t.Fatalf("stderr = %q, want a warning naming the spill directory", stderr.String())
	}
	if !delivered {
		t.Fatal("afterDelivery did not run although the full content was written to stdout")
	}
}

// Spill files are per session and sessions are many, so stale ones are swept
// on write. A sibling written inside the window must survive.
func TestPrimeHookSpillSweepsOnlyStaleSiblings(t *testing.T) {
	cityPath := t.TempDir()
	dir := filepath.Join(citylayout.RuntimeDataDir(cityPath), primeHookSpillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, "city--old.md")
	fresh := filepath.Join(dir, "city--live.md")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-primeHookSpillMaxAge - time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_SESSION_NAME", "city--worker-1")
	body, _, _ := saltedBody(t, 20000)
	var stdout, stderr bytes.Buffer
	writePrimePromptWithFormat(&stdout, &stderr, cityPath, "city", "worker", body, true, "", false, "", nil)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale spill file survived the sweep (err=%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("a fresh sibling was swept: %v", err)
	}
}
