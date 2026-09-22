package main

import (
	"os"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
)

// Transcript-quiescence probe for the session reconciler's stall arm.
//
// The reconciler's older liveness signal is runtime activity, which for a
// terminal provider is pane output. A Claude Code TUI renders a spinner for
// the whole of a turn, so that counter tracks turn-in-progress rather than
// agent liveness: a session hung mid-turn stays fresh by it for as long as it
// hangs (ci-jvbkio). The transcript is the channel that actually goes quiet.
//
// Constraint for editors: this file must resolve a transcript it can ATTRIBUTE
// to the session, never the newest file in a work directory. A misattributed
// transcript makes the stall arm reap the wrong session, and the arm's whole
// remedy is a kill.

// transcriptSearchPaths returns the roots to look for session transcripts in.
// The city's observe_paths are merged over the provider defaults because no
// city session writes to a provider default root once per-agent provider homes
// are in play -- CLAUDE_CONFIG_DIR, CODEX_HOME and their equivalents move the
// transcript out from under a reader that never notices.
func transcriptSearchPaths(cfg *config.City) []string {
	if cfg == nil {
		return worker.MergeSearchPaths(nil)
	}
	return worker.MergeSearchPaths(cfg.Daemon.ObservePaths)
}

// sessionTranscriptActivity returns the time the session's transcript was last
// appended to, or the zero time when no transcript can be attributed to this
// session. The zero return is an ABSENT reading, not an old one; checkStalled
// declines to reap on it.
//
// Attribution goes through ResolveKeyedTranscriptPath, which matches on the
// provider's own session key and refuses ambiguous and workdir-only matches.
// The obvious simplification -- DiscoverPath, which falls back to the newest
// transcript in the work directory -- is REJECTED here: several sessions share
// a work directory over a slot's lifetime, so that fallback would hand this
// probe a sibling's transcript and the stall arm would kill a live session on
// a dead one's timestamps.
//
// The reading is the file's mtime rather than the timestamp of its last
// parsed entry. The transcript is append-per-entry, so mtime tracks the same
// events at one stat instead of a full read of a file that reaches tens of
// megabytes -- and this runs per session per reconciler tick. mtime is also
// provider-agnostic, where the last-entry timestamp field is not. The cost of
// the choice: a writer that rewrites the file without appending an entry, or
// any other toucher, reads as activity. That direction is the safe one -- it
// suppresses a reap, it does not cause one.
func sessionTranscriptActivity(info sessionpkg.Info, searchPaths []string) time.Time {
	path := sessionpkg.ResolveKeyedTranscriptPath(info, searchPaths)
	if path == "" {
		return time.Time{}
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return time.Time{}
	}
	return st.ModTime()
}
