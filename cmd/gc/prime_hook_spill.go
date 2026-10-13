package main

// Holds the SessionStart hook's output under the size a provider delivers
// inline, by writing oversize context to a file and emitting a pointer.
//
// The hook is the fallback route for a startup prompt the launch did not
// deliver, and a prompt is exactly the content that outgrows a hook. Claude
// Code persists hook output past its inline limit to a file of its own and
// hands the model a 2KB preview, with nothing telling the model the rest is
// its working prompt (ci-j63574: a 16,844-byte liaison prompt lost
// everything after "## Your worklist"). gc cannot shrink a prompt without
// judging its content, so it moves the whole thing aside and says so. The
// size check sits in the writer every `gc prime --hook` branch funnels
// through, NOT in the prompt-rendering branches, because the context suffix
// (step reminder, handoff and unread mail) is unbounded too and is appended
// there.
//
// Invariant, pinned by prime_hook_spill_test.go: the model-visible part of
// any hook output is at most primeHookInlineBudgetBytes, and an oversize
// content is reachable byte for byte through the file the pointer names.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/citylayout"
	"github.com/gastownhall/gascity/internal/fsys"
)

const (
	// primeHookInlineBudgetBytes bounds the model-visible hook content.
	// Measured 2026-10-13 over 1,291 city transcripts, Claude Code 2.1.296:
	// the largest SessionStart context delivered inline was 8,651 bytes and
	// the smallest persisted to a preview was 10,300. The budget sits under
	// the largest observed delivery rather than at a guessed cap between the
	// two, because the cap is the provider's and moves with its version.
	// It applies to every hook format: other providers' limits are
	// unmeasured, and a pointer costs them one file read.
	primeHookInlineBudgetBytes = 8192
	// primeHookSpillDirName is the directory under the city runtime root.
	primeHookSpillDirName = "startup-prompts"
	// primeHookSpillMaxAge is how long a session's spill file outlives its
	// last write. Files are keyed per session and sessions are many, so
	// without a sweep the directory only grows. Seven days covers any
	// session that is still being resumed.
	primeHookSpillMaxAge = 7 * 24 * time.Hour
)

// fitPrimeHookContent returns content unchanged when it fits the inline
// budget. Otherwise it writes content to the session's spill file and
// returns a pointer to it. On a write failure it returns content unchanged
// with the error, so the caller can warn and still deliver the provider's
// own preview rather than a pointer to nothing.
func fitPrimeHookContent(cityPath, agentName, content string) (string, error) {
	if len(content) <= primeHookInlineBudgetBytes {
		return content, nil
	}
	if strings.TrimSpace(cityPath) == "" {
		return content, fmt.Errorf("no city path to hold %s/", primeHookSpillDirName)
	}
	dir := filepath.Join(citylayout.RuntimeDataDir(cityPath), primeHookSpillDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return content, fmt.Errorf("creating %s: %w", dir, err)
	}
	path := filepath.Join(dir, primeHookSpillKey(agentName)+".md")
	// 0600: the content can carry handoff and unread mail.
	if err := fsys.WriteFileAtomic(fsys.OSFS{}, path, []byte(content), 0o600); err != nil {
		return content, fmt.Errorf("writing %s: %w", path, err)
	}
	sweepStalePrimeHookSpills(dir, path)
	lines := strings.Count(content, "\n") + 1
	return fmt.Sprintf("Your startup prompt was NOT delivered in this message. "+
		"It is %d bytes, over the %d a SessionStart hook can deliver inline, "+
		"so gc wrote it in full to:\n\n    %s\n\n"+
		"Read that file now, all %d lines, before doing anything else -- it is "+
		"your working instructions, not background. If a read stops short, "+
		"continue from where it stopped until you reach the end.\n",
		len(content), primeHookInlineBudgetBytes, path, lines), nil
}

// primeHookSpillKey names the spill file for the running session. The session
// name comes first because a pooled agent runs several sessions at once and
// an agent-keyed file would hand one session another's prompt.
func primeHookSpillKey(agentName string) string {
	key := ""
	for _, candidate := range []string{os.Getenv("GC_SESSION_NAME"), os.Getenv("GC_SESSION_ID"), agentName} {
		if key = strings.TrimSpace(candidate); key != "" {
			break
		}
	}
	if key == "" {
		key = "default"
	}
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, key)
}

// sweepStalePrimeHookSpills removes spill files older than
// primeHookSpillMaxAge. Best effort: a failed sweep leaves an old file
// behind and costs nothing else, so it must not fail the delivery.
func sweepStalePrimeHookSpills(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-primeHookSpillMaxAge)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if path == keep || e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			_ = os.Remove(path)
		}
	}
}

// warnPrimeHookSpill reports a spill that could not be written. The hook
// still exits 0: a SessionStart hook that fails delivers nothing at all.
func warnPrimeHookSpill(stderr io.Writer, err error) {
	fmt.Fprintf(stderr, "gc prime: startup context is over the %d-byte inline hook limit "+ //nolint:errcheck // best-effort stderr
		"and could not be written to %s (%v); delivering it inline, where the provider may cut it to a preview. "+
		"Check that the city's .gc/runtime directory is writable.\n",
		primeHookInlineBudgetBytes, primeHookSpillDirName, err)
}
