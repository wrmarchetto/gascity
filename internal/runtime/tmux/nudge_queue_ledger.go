package tmux

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/sessionlog"
)

// Reading a pane's own transcript to settle what happened to a nudge the
// pane itself could not witness.
//
// Every earlier attempt to confirm a nudge read the PANE, and the pane cannot
// answer this question. Three fixes in a row added another pane source
// (gascity#5012, cfdfd1d43, #5013) and the stall survived all three, because
// each shared the same structural assumption: that a message which has left
// the composer has been delivered. It has not -- it may be sitting in Claude
// Code's message queue, and a pane shows the same "Press up to edit queued
// messages" whoever queued it.
//
// The transcript is the one source that is not pane rendering. Claude Code
// writes its queue transitions there with the message text attached, so a
// verdict is matched on CONTENT and separates THIS nudge from the session's
// own traffic. sessionlog.ClaudeNudgeQueueState owns the reading. This file
// owns finding the file and is deliberately the thinner half.
//
// WHY THIS IS NOT AN UPWARD DEPENDENCY. The transcript path is resolved from
// the pane's own environment and working directory -- both tmux facts -- and
// sessionlog is already this package's neighbor for provider-family lookup.
// Nothing here reaches into internal/session or internal/worker.

// claudeTranscriptSearchPaths returns the session-log roots to search for a
// pane's transcript.
//
// The pane's own CLAUDE_CONFIG_DIR wins because this city gives every account
// seat a separate one (~/.claude-homes/account<N>/.claude) and the operator's
// interactive ~/.claude is a different tree entirely. Falling back to the
// default root is what keeps an ad hoc session outside the city readable.
func (t *Tmux) claudeTranscriptSearchPaths(session string) []string {
	if dir, err := t.GetEnvironment(session, "CLAUDE_CONFIG_DIR"); err == nil {
		if dir = strings.TrimSpace(dir); dir != "" {
			return []string{filepath.Join(dir, "projects")}
		}
	}
	return sessionlog.DefaultSearchPaths()
}

// claudeTranscriptPath resolves the transcript a pane is writing, or "" when
// it cannot be found.
//
// Resolution is by (config dir, working directory) and picks the most
// recently modified file, so a work tree holding several resumed transcripts
// resolves to the live one. That is AMBIGUOUS in principle -- two sessions
// sharing a work tree would share a candidate set -- and it is left
// unguarded here because the ambiguity cannot produce a false confirmation
// on its own: a verdict also has to match the nudge's exact text and post-
// date the paste, and a sibling session would have to be handed the same
// bytes within the same few seconds. Every city slot has its own worktree,
// which is what makes the pair distinctive.
//
// It reads the session's FIRST pane, not the pane the nudge was sent to.
// GetPaneWorkDir's own contract is pane 0, and resolving the agent pane's
// path would need a second lookup for a case nobody has: a multi-pane session
// whose agent is not pane 0 and whose panes sit in different directories.
// If one ever appears, the transcript simply does not resolve and the verdict
// is Unrecorded -- a missing confirmation, never a wrong one.
func (t *Tmux) claudeTranscriptPath(session string) string {
	workDir, err := t.GetPaneWorkDir(session)
	if err != nil || strings.TrimSpace(workDir) == "" {
		return ""
	}
	return sessionlog.FindSessionFileForProvider(t.claudeTranscriptSearchPaths(session), "claude", workDir)
}

// observeNudgeLedger reports what the pane's transcript says about a message
// gc has just typed into it.
//
// Best-effort by construction: an unresolvable transcript, an unreadable one,
// and one that simply has no record of the text all return
// NudgeQueueUnrecorded, which is the caller's status quo. The failure
// direction is one-way on purpose -- a broken observer must never manufacture
// a confirmation, and this whole path exists because a previous witness did.
func (t *Tmux) observeNudgeLedger(session, message string, since time.Time) sessionlog.NudgeQueueState {
	if !paneLedgerReadable(t.providerEnv(session)) {
		return sessionlog.NudgeQueueUnrecorded
	}
	path := t.claudeTranscriptPath(session)
	if path == "" {
		return sessionlog.NudgeQueueUnrecorded
	}
	state, err := sessionlog.ClaudeNudgeQueueState(path, message, since)
	if err != nil {
		return sessionlog.NudgeQueueUnrecorded
	}
	return state
}

// paneLedgerReadable reports whether a pane's provider writes the queue
// ledger this observer knows how to read.
//
// Only claude does. The submit-verify loop also covers codex, whose
// transcript lives elsewhere and whose queue semantics nobody has measured --
// and a codex pane in a worktree that once ran claude would otherwise resolve
// to that STALE transcript. Content and cutoff matching make a false verdict
// from it very unlikely rather than impossible, and "unlikely" is not the
// standard for a witness that overturns a doubt.
//
// An EMPTY provider is allowed through rather than refused. It means the pane
// carries no GC_PROVIDER at all (an ad hoc session, some test harnesses), the
// sniff that would resolve it costs more tmux calls than the read it guards,
// and the claude-shaped path resolution below simply finds nothing for a pane
// that is not claude.
func paneLedgerReadable(provider string) bool {
	family := sessionlog.ProviderFamily(provider)
	return family == "" || family == "claude"
}
