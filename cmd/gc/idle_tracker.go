package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
)

// idleDeclineReason names a checkIdle evaluation that reached NO idle verdict
// at all, as opposed to one that decided the session is active. Every value
// means the same operational thing: while the condition holds, this session
// can never be idle-reaped.
type idleDeclineReason string

const (
	// idleDeclineReadFailed is a failed observe of the session.
	//
	// It does NOT cover a provider whose GetLastActivity itself errors:
	// worker.RuntimeHandle.LiveObservation drops that error
	// (`err == nil && !last.IsZero()`) and reports no timestamp, so a broken
	// provider read arrives here as idleDeclineNoActivity. The two are
	// indistinguishable at this layer by construction, and the message says
	// so rather than implying a diagnosis the code cannot make.
	idleDeclineReadFailed idleDeclineReason = "activity_read_failed"

	// idleDeclineNoActivity is an observe that succeeded and carried no
	// activity time -- the runtime is gone, the provider cannot report
	// activity, or its read failed and was swallowed upstream.
	idleDeclineNoActivity idleDeclineReason = "no_activity_timestamp"

	// idleDeclineDiscountedToZero is defense in depth and is UNREACHABLE
	// through runtime.DiscountPokeActivity's current contract: it returns
	// either the caller's activity (non-zero by the check above) or
	// pk.Prior, which Poke.Complete has already established is non-zero. It
	// carries no test for that reason; the guard stays so a future change to
	// that contract surfaces as a reported decline instead of a reaper that
	// silently stops firing.
	idleDeclineDiscountedToZero idleDeclineReason = "discounted_to_zero"
)

// idleDeclineRepeatInterval bounds how often ONE session's unchanged decline
// is reported.
//
// Unthrottled would print a line per reconciler tick for as long as the
// condition lasts -- hours, for a session whose runtime is gone. Reporting
// only the transition was rejected the other way: the supervisor log rotates,
// and a condition whose only record has rotated away is exactly the
// invisibility this reporting exists to end (ci-kjh8vc). So it repeats, at a
// cadence a human reading the log can live with.
const idleDeclineRepeatInterval = 10 * time.Minute

// idleCheck is one checkIdle evaluation.
//
// Decline and Report are separate because throttling must not be able to
// masquerade as "no problem": a throttled decline still reports its reason to
// any caller that asks, and only Report says whether the operator is owed a
// line for it now.
type idleCheck struct {
	// Idle reports that the session exceeded its configured idle timeout.
	// False whenever Decline is non-empty -- a decline reached no verdict.
	Idle bool
	// Decline names why no verdict was reached, or is empty when one was.
	Decline idleDeclineReason
	// Report is true when this decline is owed to the operator now.
	Report bool
	// Err is the underlying read error, set only for
	// idleDeclineReadFailed.
	Err error
}

// idleDeclineMessage renders one operator line for a reportable decline.
// display is the agent's display name and sessionName its runtime name; both
// appear because the remedy command takes the latter while the rest of the
// reconciler's stderr names the former.
func idleDeclineMessage(display, sessionName string, check idleCheck) string {
	// The wording names the PANE arm and not "the idle timeout" because the
	// reaper has two arms and only this one reads a provider. The transcript
	// arm still reaps a session whose pane read declined -- that is the case
	// it exists for -- so a message promising the session will not be reaped
	// would contradict the behavior
	// TestReconcileSessionBeads_StallArmStillReapsWhenThePaneArmDeclines
	// pins.
	msg := fmt.Sprintf(
		"session reconciler: the pane-activity idle arm cannot be evaluated for %s (session %s): %s -- that arm will not reap this session while this holds, and it is the only arm unless the agent configures stall_timeout; check the runtime with `gc session peek %s`",
		display, sessionName, check.Decline, sessionName,
	)
	if check.Err != nil {
		msg += fmt.Sprintf(": %v", check.Err)
	}
	return msg
}

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
	// checkIdle reports whether the agent has been idle longer than its
	// configured timeout, and -- when it could not reach that verdict at
	// all -- why. Queries sp.GetLastActivity(). template is the agent's
	// qualified template name and is used as a fallback lookup when the
	// session name is not registered directly (pool sessions).
	//
	// poke is the session's DURABLE poke record (session.Info.DurablePoke).
	// It is a parameter rather than something the tracker reads off sp
	// because the provider's own poke map belongs to whichever process sent
	// the keystrokes, and that is never this one -- see internal/runtime/
	// poke.go. Pass a zero Poke to mean "no keystroke delivery on record".
	checkIdle(sessionName, template string, sp runtime.Provider, now time.Time, poke runtime.Poke) idleCheck

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
	idle                       timeoutSet                   // pane/runtime activity timeouts
	stall                      timeoutSet                   // transcript-quiescence timeouts
	templateFallbackExemptions map[string]bool              // session name → skip template fallback, BOTH arms
	declines                   map[string]idleDeclineRecord // session name → last REPORTED decline
	readActivity               activityReader               // last-activity read, injected for tests
}

// idleDeclineRecord is the last decline reported to the operator for one
// session. It exists only to throttle the report; the decline itself is
// recomputed from scratch on every tick.
type idleDeclineRecord struct {
	reason idleDeclineReason
	at     time.Time
}

// activityReader is checkIdle's read of a session's last activity.
//
// It is a field rather than a direct call to the package-level helper so a
// test can drive the failed-read and no-timestamp branches with the read
// PRESENT AND WRONG. The alternative a future editor would reach for --
// passing a nil provider, or runtime.NewFailFake -- short-circuits in
// workerHandleForSessionTargetWithRuntimeHintsWithConfig ahead of the code
// under test, so the test would pin the short-circuit and checkIdle's own
// error mapping could be deleted with the suite still green.
type activityReader func(sp runtime.Provider, sessionName string) (time.Time, error)

// newIdleTracker creates an idle tracker. Returns nil if disabled.
// Callers check for nil before using.
func newIdleTracker() *memoryIdleTracker {
	return &memoryIdleTracker{
		idle:                       newTimeoutSet(),
		stall:                      newTimeoutSet(),
		templateFallbackExemptions: make(map[string]bool),
		declines:                   make(map[string]idleDeclineRecord),
		readActivity: func(sp runtime.Provider, sessionName string) (time.Time, error) {
			return workerSessionTargetLastActivityWithConfig("", nil, sp, nil, sessionName)
		},
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

func (m *memoryIdleTracker) checkIdle(sessionName, template string, sp runtime.Provider, now time.Time, poke runtime.Poke) idleCheck {
	timeout, ok := m.resolveTimeout(m.idle, sessionName, template)
	// No registered timeout is a configuration fact, NOT a decline: the
	// reaper is switched off for this session and there is nothing an
	// operator could act on. Reporting it would print a line per tick for
	// every session in a city that configures no idle timeouts at all.
	//
	// resolveTimeout already folds in the template fallback and its
	// per-session exemption, so this arm declines on the PANE timeout alone
	// -- a session armed only for stalls reaches this return, and its stall
	// arm is evaluated separately by the reconciler.
	if !ok {
		m.clearDecline(sessionName)
		return idleCheck{}
	}
	lastActivity, err := m.readActivity(sp, sessionName)
	if err != nil {
		return m.decline(sessionName, idleDeclineReadFailed, err, now)
	}
	if lastActivity.IsZero() {
		return m.decline(sessionName, idleDeclineNoActivity, nil, now)
	}
	// Discount gc's own keystroke echo. The provider already did this for a
	// poke IT sent, and that discount is idempotent here: a poke the provider
	// resolved returns its prior, which sits outside the echo window, so this
	// call leaves it alone. An incomplete poke discounts nothing.
	lastActivity = runtime.DiscountPokeActivity(lastActivity, poke, now)
	if lastActivity.IsZero() {
		return m.decline(sessionName, idleDeclineDiscountedToZero, nil, now)
	}
	m.clearDecline(sessionName)
	return idleCheck{Idle: now.Sub(lastActivity) > timeout}
}

// decline records a decline and reports whether the operator is owed a line
// for it now. The record advances only when the line is actually emitted, so
// the repeat interval measures time since the last REPORT rather than time
// since the last tick -- a per-tick record would reset the window on every
// tick and the repeat could never arrive.
func (m *memoryIdleTracker) decline(sessionName string, reason idleDeclineReason, err error, now time.Time) idleCheck {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev, seen := m.declines[sessionName]
	report := !seen || prev.reason != reason || !now.Before(prev.at.Add(idleDeclineRepeatInterval))
	if report {
		m.declines[sessionName] = idleDeclineRecord{reason: reason, at: now}
	}
	return idleCheck{Decline: reason, Report: report, Err: err}
}

// clearDecline forgets any throttled decline for a session that has since
// produced a verdict, so a condition that returns is reported at once rather
// than waiting out the repeat interval of the previous episode.
func (m *memoryIdleTracker) clearDecline(sessionName string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.declines, sessionName)
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
