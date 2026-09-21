package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

// Scope: the reconciler's transcript-quiescence probe -- attribution and the
// timestamp it reads. The stall arm's arithmetic is idle_tracker_stall_test.go
// and the reconciler's composition is session_reconciler_stall_test.go.
//
// Why this suite exists: the probe is the half of the stall arm that can be
// silently dead. If attribution never resolves, checkStalled sees a zero time
// forever, declines to reap, and every other test in the feature stays green
// while the defect it was written for runs unchecked. So these tests build a
// real on-disk transcript in the layout a Claude session actually writes --
// <observe root>/<workdir slug>/<session key>.jsonl -- rather than asserting
// against a stub.
//
//	go test ./cmd/gc/ -run TestSessionTranscriptActivity

// claudeProjectSlug mirrors the layout Claude Code writes under its projects
// root: the absolute work directory with every path separator and dot
// replaced by a dash. Derived here from the work directory the test itself
// passes in, so the fixture cannot drift from the input.
func claudeProjectSlug(workDir string) string {
	slug := strings.ReplaceAll(workDir, "/", "-")
	return strings.ReplaceAll(slug, ".", "-")
}

// writeKeyedTranscript lays down one transcript file and stamps its mtime.
func writeKeyedTranscript(t *testing.T, root, workDir, sessionKey string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(root, claudeProjectSlug(workDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q): %v", dir, err)
	}
	path := filepath.Join(dir, sessionKey+".jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"user\"}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("Chtimes(%q): %v", path, err)
	}
}

// TestSessionTranscriptActivity_ReadsTheKeyedTranscriptMtime pins the happy
// path against the real directory layout, including the observe-root merge:
// a city whose sessions write under a per-account provider home reaches them
// only through observe_paths, and a probe that consulted the provider default
// alone would return zero for every session in this city.
func TestSessionTranscriptActivity_ReadsTheKeyedTranscriptMtime(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workDir := t.TempDir()
	key := "af97db1a-ee48-41e7-ab72-01702c8580b9"
	want := time.Now().Add(-90 * time.Minute).Truncate(time.Second)
	writeKeyedTranscript(t, root, workDir, key, want)

	cfg := &config.City{}
	cfg.Daemon.ObservePaths = []string{root}
	info := sessionpkg.Info{WorkDir: workDir, SessionKey: key, Provider: "claude"}

	got := sessionTranscriptActivity(info, transcriptSearchPaths(cfg))
	if !got.Equal(want) {
		t.Fatalf("sessionTranscriptActivity = %v, want the transcript mtime %v", got, want)
	}
}

// TestSessionTranscriptActivity_AbsentTranscriptReadsZero pins the absent
// reading for the two ways attribution legitimately misses: no session key
// recorded for the session, and a key whose file does not exist. Both must be
// zero rather than a very old time, because checkStalled reaps on old and
// declines on zero.
func TestSessionTranscriptActivity_AbsentTranscriptReadsZero(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workDir := t.TempDir()
	cfg := &config.City{}
	cfg.Daemon.ObservePaths = []string{root}
	paths := transcriptSearchPaths(cfg)

	cases := []struct {
		name string
		info sessionpkg.Info
	}{
		{"no session key", sessionpkg.Info{WorkDir: workDir, Provider: "claude"}},
		{"key with no file", sessionpkg.Info{WorkDir: workDir, SessionKey: "11111111-2222-3333-4444-555555555555", Provider: "claude"}},
		{"no work dir", sessionpkg.Info{SessionKey: "11111111-2222-3333-4444-555555555555", Provider: "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionTranscriptActivity(tc.info, paths); !got.IsZero() {
				t.Fatalf("sessionTranscriptActivity = %v, want the zero time", got)
			}
		})
	}
}

// TestSessionTranscriptActivity_KeylessSessionDoesNotAdoptAStrangersTranscript
// pins the rejected fallback directly. A session with no recorded key sits in
// a work directory that still holds every predecessor's transcript, and a
// workdir-or-newest-file discovery would answer with one of those. The answer
// must be the absent reading instead: an unattributable session is one the
// stall arm knows nothing about, not one whose liveness is a stranger's.
func TestSessionTranscriptActivity_KeylessSessionDoesNotAdoptAStrangersTranscript(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workDir := t.TempDir()
	writeKeyedTranscript(t, root, workDir, "e417f504-dd2f-450d-8987-897a6e6d63dc", time.Now().Truncate(time.Second))

	cfg := &config.City{}
	cfg.Daemon.ObservePaths = []string{root}
	info := sessionpkg.Info{WorkDir: workDir, Provider: "claude"}

	if got := sessionTranscriptActivity(info, transcriptSearchPaths(cfg)); !got.IsZero() {
		t.Fatalf("sessionTranscriptActivity = %v for a keyless session, want the zero time (a stranger's transcript in the same work dir must not be adopted)", got)
	}
}

// TestSessionTranscriptActivity_IgnoresASiblingSessionsTranscript pins the
// attribution requirement that makes the stall arm safe to act on. Slots reuse
// a work directory across sessions, so the directory holds every predecessor's
// transcript. A probe that took the newest file there would hand this session
// a sibling's timestamps -- and since the arm's remedy is a kill, that reaps a
// live session on a dead one's evidence.
func TestSessionTranscriptActivity_IgnoresASiblingSessionsTranscript(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	workDir := t.TempDir()
	mine := "af97db1a-ee48-41e7-ab72-01702c8580b9"
	sibling := "e417f504-dd2f-450d-8987-897a6e6d63dc"
	mineAt := time.Now().Add(-9 * time.Hour).Truncate(time.Second)
	writeKeyedTranscript(t, root, workDir, mine, mineAt)
	writeKeyedTranscript(t, root, workDir, sibling, time.Now().Truncate(time.Second))

	cfg := &config.City{}
	cfg.Daemon.ObservePaths = []string{root}
	info := sessionpkg.Info{WorkDir: workDir, SessionKey: mine, Provider: "claude"}

	got := sessionTranscriptActivity(info, transcriptSearchPaths(cfg))
	if !got.Equal(mineAt) {
		t.Fatalf("sessionTranscriptActivity = %v, want this session's own transcript mtime %v (a fresh sibling in the same work dir must not count)", got, mineAt)
	}
}
