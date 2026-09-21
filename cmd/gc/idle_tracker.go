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
	msg := fmt.Sprintf(
		"session reconciler: idle timeout cannot be evaluated for %s (session %s): %s -- the session will not be idle-reaped while this holds; check the runtime with `gc session peek %s`",
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

	// setTimeout configures the idle timeout for a single session name.
	// Used for sessions whose runtime names are deterministic at startup
	// (configured named sessions). Duration of 0 clears the entry.
	setTimeout(sessionName string, timeout time.Duration)

	// setTimeoutForTemplate configures the idle timeout for every session
	// belonging to an agent template. Used for ephemeral pool agents whose
	// runtime session names carry per-instance bead IDs and cannot be
	// enumerated up front. Duration of 0 clears the entry.
	setTimeoutForTemplate(template string, timeout time.Duration)

	// exemptTemplateFallbackForSession prevents one stable session from
	// inheriting the template timeout. Used for mode="always" named sessions
	// that share a template with pool siblings.
	exemptTemplateFallbackForSession(sessionName string)
}

// memoryIdleTracker is the production implementation of idleTracker.
type memoryIdleTracker struct {
	mu                         sync.Mutex
	timeouts                   map[string]time.Duration     // session name → idle timeout
	templateTimeouts           map[string]time.Duration     // agent template → idle timeout
	templateFallbackExemptions map[string]bool              // session name → skip template fallback
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
		timeouts:                   make(map[string]time.Duration),
		templateTimeouts:           make(map[string]time.Duration),
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
	if timeout <= 0 {
		delete(m.timeouts, sessionName)
		return
	}
	m.timeouts[sessionName] = timeout
}

func (m *memoryIdleTracker) setTimeoutForTemplate(template string, timeout time.Duration) {
	if template == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if timeout <= 0 {
		delete(m.templateTimeouts, template)
		return
	}
	m.templateTimeouts[template] = timeout
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
	m.mu.Lock()
	timeout, ok := m.timeouts[sessionName]
	exempt := m.templateFallbackExemptions[sessionName]
	if !ok && !exempt && template != "" {
		timeout, ok = m.templateTimeouts[template]
	}
	m.mu.Unlock()
	// No registered timeout is a configuration fact, NOT a decline: the
	// reaper is switched off for this session and there is nothing an
	// operator could act on. Reporting it would print a line per tick for
	// every session in a city that configures no idle timeouts at all.
	if !ok || timeout <= 0 {
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
