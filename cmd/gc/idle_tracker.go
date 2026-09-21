package main

import (
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// idleTracker checks for agents that have been idle longer than their
// configured timeout. Nil means idle checking is disabled (backward
// compatible). Follows the same nil-guard pattern as crashTracker.
//
// Timeouts may be registered two ways:
//   - Per session name (setTimeout) for sessions whose runtime names are
//     stable and knowable at controller startup — mainly configured named
//     sessions like mayor.
//   - Per agent template (setTimeoutForTemplate) for ephemeral pool agents
//     whose runtime session names are bead-derived and minted as work is
//     slung. Static slot enumeration (worker-1, worker-2, ...) does not
//     match those names, so a per-name registration silently misses every
//     pool session.
//
// checkIdle resolves a timeout by checking the session name first and
// falling back to the template — preserving named-session behavior while
// also covering bead-derived pool session names.
//
// The tracker carries TWO independent timeouts per session, because it
// carries two independent liveness signals. checkIdle measures runtime
// activity, which for a terminal provider is pane output; checkStalled
// measures how long the session's transcript has been quiescent. A Claude
// Code TUI renders a spinner for the whole of a turn, so pane output tracks
// turn-in-progress rather than agent liveness and a session hung mid-turn is
// never idle by that measure for as long as it hangs -- which is precisely
// the condition the idle reaper exists to break (ci-jvbkio). The caller ORs
// the two arms, so the stall arm can only ever reap more, never less.
type idleTracker interface {
	// checkIdle returns true if the agent has been idle longer than its
	// configured timeout. Queries sp.GetLastActivity(). template is the
	// agent's qualified template name and is used as a fallback lookup
	// when the session name is not registered directly (pool sessions).
	//
	// poke is the session's DURABLE poke record (session.Info.DurablePoke).
	// It is a parameter rather than something the tracker reads off sp
	// because the provider's own poke map belongs to whichever process sent
	// the keystrokes, and that is never this one -- see internal/runtime/
	// poke.go. Pass a zero Poke to mean "no keystroke delivery on record".
	checkIdle(sessionName, template string, sp runtime.Provider, now time.Time, poke runtime.Poke) bool

	// checkStalled returns true if the session's transcript has been
	// quiescent longer than its configured STALL timeout. The transcript
	// time arrives as a probe rather than a value for two reasons:
	// attributing a transcript to a session is provider-specific and needs
	// the session's Info plus the city's observe paths, neither of which
	// belongs behind this interface; and the probe costs a path resolution
	// and a stat per session per tick, which a session with no stall timeout
	// registered must not pay. checkStalled calls it ONLY after a timeout
	// resolves.
	//
	// A zero time from the probe means the transcript could not be
	// attributed at all -- unsupported provider, no session key, file not
	// yet written -- and is treated as NO READING rather than an infinitely
	// old one. The opposite reading would reap every session gc cannot see a
	// transcript for.
	checkStalled(sessionName, template string, lastTranscript func() time.Time, now time.Time) bool

	// setTimeout configures the idle timeout for a single session name.
	// Used for sessions whose runtime names are deterministic at startup
	// (configured named sessions). Duration of 0 clears the entry.
	setTimeout(sessionName string, timeout time.Duration)

	// setStallTimeout configures the transcript-quiescence timeout for a
	// single session name. Duration of 0 clears the entry.
	setStallTimeout(sessionName string, timeout time.Duration)

	// setTimeoutForTemplate configures the idle timeout for every session
	// belonging to an agent template. Used for ephemeral pool agents whose
	// runtime session names carry per-instance bead IDs and cannot be
	// enumerated up front. Duration of 0 clears the entry.
	setTimeoutForTemplate(template string, timeout time.Duration)

	// setStallTimeoutForTemplate is setTimeoutForTemplate for the
	// transcript-quiescence timeout. Duration of 0 clears the entry.
	setStallTimeoutForTemplate(template string, timeout time.Duration)

	// exemptTemplateFallbackForSession prevents one stable session from
	// inheriting the template timeout. Used for mode="always" named sessions
	// that share a template with pool siblings. ONE exemption covers BOTH
	// arms: a session that must never be idle-reaped must never be
	// stall-reaped either, and a per-arm exemption would let the newer arm
	// kill exactly the sessions the older exemption was written to protect.
	exemptTemplateFallbackForSession(sessionName string)
}

// timeoutSet is one signal's registry: per-session-name entries plus a
// per-agent-template fallback for bead-derived pool session names. Two of
// these sit side by side in memoryIdleTracker so the pane-activity and
// transcript-quiescence arms resolve independent durations through one copy
// of the lookup rules.
type timeoutSet struct {
	bySession  map[string]time.Duration
	byTemplate map[string]time.Duration
}

func newTimeoutSet() timeoutSet {
	return timeoutSet{
		bySession:  make(map[string]time.Duration),
		byTemplate: make(map[string]time.Duration),
	}
}

func (s timeoutSet) set(sessionName string, timeout time.Duration) {
	if timeout <= 0 {
		delete(s.bySession, sessionName)
		return
	}
	s.bySession[sessionName] = timeout
}

func (s timeoutSet) setTemplate(template string, timeout time.Duration) {
	if timeout <= 0 {
		delete(s.byTemplate, template)
		return
	}
	s.byTemplate[template] = timeout
}

// resolve returns the timeout for a session, preferring an explicit per-name
// entry and falling back to the template unless the session is exempt.
func (s timeoutSet) resolve(sessionName, template string, exempt bool) (time.Duration, bool) {
	timeout, ok := s.bySession[sessionName]
	if !ok && !exempt && template != "" {
		timeout, ok = s.byTemplate[template]
	}
	if !ok || timeout <= 0 {
		return 0, false
	}
	return timeout, true
}

// memoryIdleTracker is the production implementation of idleTracker.
type memoryIdleTracker struct {
	mu                         sync.Mutex
	idle                       timeoutSet      // pane/runtime activity timeouts
	stall                      timeoutSet      // transcript-quiescence timeouts
	templateFallbackExemptions map[string]bool // session name → skip template fallback, BOTH arms
}

// newIdleTracker creates an idle tracker. Returns nil if disabled.
// Callers check for nil before using.
func newIdleTracker() *memoryIdleTracker {
	return &memoryIdleTracker{
		idle:                       newTimeoutSet(),
		stall:                      newTimeoutSet(),
		templateFallbackExemptions: make(map[string]bool),
	}
}

func (m *memoryIdleTracker) setTimeout(sessionName string, timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle.set(sessionName, timeout)
}

func (m *memoryIdleTracker) setStallTimeout(sessionName string, timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stall.set(sessionName, timeout)
}

func (m *memoryIdleTracker) setTimeoutForTemplate(template string, timeout time.Duration) {
	if template == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle.setTemplate(template, timeout)
}

func (m *memoryIdleTracker) setStallTimeoutForTemplate(template string, timeout time.Duration) {
	if template == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stall.setTemplate(template, timeout)
}

func (m *memoryIdleTracker) exemptTemplateFallbackForSession(sessionName string) {
	if sessionName == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.templateFallbackExemptions[sessionName] = true
}

func (m *memoryIdleTracker) checkIdle(sessionName, template string, sp runtime.Provider, now time.Time, poke runtime.Poke) bool {
	timeout, ok := m.resolveTimeout(m.idle, sessionName, template)
	if !ok {
		return false
	}
	lastActivity, err := workerSessionTargetLastActivityWithConfig("", nil, sp, nil, sessionName)
	if err != nil || lastActivity.IsZero() {
		return false
	}
	// Discount gc's own keystroke echo. The provider already did this for a
	// poke IT sent, and that discount is idempotent here: a poke the provider
	// resolved returns its prior, which sits outside the echo window, so this
	// call leaves it alone. An incomplete poke discounts nothing.
	lastActivity = runtime.DiscountPokeActivity(lastActivity, poke, now)
	if lastActivity.IsZero() {
		return false
	}
	return now.Sub(lastActivity) > timeout
}

// resolveTimeout looks one arm's duration up under the shared lock. Passing
// the set by value is safe because timeoutSet holds maps that are only ever
// mutated through this same lock.
func (m *memoryIdleTracker) resolveTimeout(set timeoutSet, sessionName, template string) (time.Duration, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return set.resolve(sessionName, template, m.templateFallbackExemptions[sessionName])
}

func (m *memoryIdleTracker) checkStalled(sessionName, template string, lastTranscript func() time.Time, now time.Time) bool {
	timeout, ok := m.resolveTimeout(m.stall, sessionName, template)
	if !ok {
		return false
	}
	if lastTranscript == nil {
		return false
	}
	last := lastTranscript()
	// An unattributable transcript is an ABSENT reading, not an old one.
	// Reaping on it would kill every session whose provider gc cannot read a
	// transcript for.
	if last.IsZero() {
		return false
	}
	return now.Sub(last) > timeout
}
