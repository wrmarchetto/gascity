// Package usageattr groups recorded usage facts into per-session, per-agent-type
// and per-bead accounts over an explicitly bounded window.
//
// It exists because the facts in .gc/usage.jsonl answer "how much" but not "on
// whose behalf": internal/usage keeps a stdlib-only Fact, and gc costs rolls
// those up by run id, which is the execution and not the actor. The lens this
// package adds is the actor one -- which agent type spent the tokens, and which
// work bead the session was holding while it did -- because that is the
// comparison a later efficiency change has to be measured against.
//
// Every reading it produces carries whether it was OBSERVED. That is the shape
// the whole package is built around, and it is not defensive programming: 1,533
// of 1,740 sessions in this city's log have model facts and no compute fact
// (measured 2026-09-10), so a report that renders missing wall-clock as 0.0 --
// which gc costs does -- publishes a measurement nobody took. A comparison run
// weeks later cannot tell that apart from a session that got faster.
//
// Attribution is deliberately lossy rather than clever. The agent type is the
// session name with the trailing "-<session id>" removed, derived from the two
// fields and never from a pattern over the id's shape; when the suffix does not
// match, the name is kept verbatim and flagged instead of being trimmed to
// something that looks like an agent. The bead link is read from the work bead's
// own gc.session_id, never from Fact.StepID, which no production emitter fills
// (see internal/worker/invocation_telemetry.go -- per-step attribution was
// retired with the gc.active_work_bead pointer).
//
// The rule for deriving a type matches the one the city console's
// tokens-by-role panel applies, on purpose. The two analytics are read side by
// side, and a second, better rule here would file the same rows under a
// different role name in each.
//
// Invariants, verified by usageattr_test.go:
//
//   - A reading is Observed only when a fact of that Kind fell in the window.
//     A measured zero stays Observed.
//   - Totals are accumulated over distinct sessions, never by summing Groups:
//     under ByBead a session naming two beads appears in both rows.
//   - Top truncates Groups and never Totals.
package usageattr

import (
	"sort"
	"time"

	"github.com/gastownhall/gascity/internal/agent"
	"github.com/gastownhall/gascity/internal/usage"
)

// UnattributedBeadKey is the group key for sessions that no bead in the
// consulted store names. It is a visible row rather than a silent drop: most
// sessions in a multi-rig city are held by beads in another rig's store, and a
// report that omitted them would read as though the work never happened.
const UnattributedBeadKey = "(no bead recorded)"

// Grouping selects the account a fact is attributed to.
type Grouping string

const (
	// BySession accounts per session id -- the finest grain the facts carry.
	BySession Grouping = "session"
	// ByType accounts per agent type derived from the session name.
	ByType Grouping = "type"
	// ByBead accounts per work bead, joined through the bead's gc.session_id.
	ByBead Grouping = "bead"
)

// Options bounds and shapes one aggregation.
//
// BeadsOf is injected rather than looked up here so the aggregation stays
// testable without a bead store, and so the store query stays at the CLI edge
// where the side effect belongs. A session absent from the map held no bead
// that this store knows of, which is not the same as having held none.
type Options struct {
	By      Grouping
	Since   time.Time           // inclusive; zero means no lower bound
	Until   time.Time           // exclusive; zero means no upper bound
	BeadsOf map[string][]string // session id -> bead ids naming it
	Top     int                 // keep the N largest groups; 0 keeps all
}

// Tokens is a model-fact reading. Observed false means no model fact landed in
// the window, and the counts must then be read as absent rather than as zero.
type Tokens struct {
	Observed            bool
	Invocations         int
	InputTokens         int
	OutputTokens        int
	CacheReadTokens     int
	CacheCreationTokens int
}

// Wall is a compute-fact reading -- awake wall-clock seconds. Observed false
// means no compute fact landed in the window. In this city that is the common
// case, not the exception.
type Wall struct {
	Observed     bool
	ComputeFacts int
	Seconds      float64
}

// Span is the interval between the first and last model fact in the window.
//
// It is a LOWER BOUND on how long the session was alive, never its duration:
// everything before the first invocation and after the last one falls outside
// it. It exists because compute facts have all but stopped in this city -- 2,633
// on 2026-09-03 against single digits most days after -- leaving the invocation
// timestamps as the only latency signal the log still carries. Observed is false
// for a single fact, which bounds no interval; reporting 0 there would be
// indistinguishable from a session that finished inside one millisecond.
type Span struct {
	Observed bool
	Facts    int
	Seconds  float64
}

// Reading is one account's totals across all three measurements.
type Reading struct {
	Tokens Tokens
	Wall   Wall
	Span   Span
}

// Group is one account: a session, an agent type, or a bead.
type Group struct {
	Key             string
	Sessions        []string
	AgentTypes      []string
	SuffixUnmatched bool // the session name did not end in its own session id
	SharesSessions  bool // ByBead only: a session here also belongs to another bead
	Reading
	FirstAt time.Time
	LastAt  time.Time
}

// Window records what was asked for and what was actually covered. The two are
// kept apart because a request that overshoots the log's extent is only partly
// covered, and echoing the request as if it were the coverage overstates it.
type Window struct {
	Since              time.Time
	Until              time.Time
	Observed           bool // a fact fell inside; false means nothing was recorded here
	FirstFactAt        time.Time
	LastFactAt         time.Time
	FactsInWindow      int
	FactsOutsideWindow int
	FactsUndated       int // no emitter timestamp, so placeable in no window
}

// Residue counts what the attribution could not resolve. These are reported
// rather than repaired: each one is a case where a plausible repair would
// silently move traffic onto an account that did not incur it.
type Residue struct {
	SuffixUnmatchedSessions    int
	SessionsWithNoBead         int
	SessionsNamingSeveralBeads int
}

// Report is the result of one aggregation.
type Report struct {
	Grouping      Grouping
	Window        Window
	Groups        []Group
	GroupsOmitted int // dropped by Top; Totals still cover them
	Totals        Reading
	Residue       Residue
	Sessions      int
}

// sessionAccount accumulates one session before it is folded into groups.
type sessionAccount struct {
	id              string
	agentType       string
	suffixUnmatched bool
	tokens          Tokens
	wall            Wall
	firstModelAt    time.Time
	lastModelAt     time.Time
	modelFacts      int
	firstAt         time.Time
	lastAt          time.Time
}

// Aggregate folds facts into accounts under opts.
//
// Facts are assumed already de-duplicated by idempotency key -- usage.ReadFacts
// does that -- so this does no second pass; a caller that assembles facts some
// other way owns that. Ordering of the input is irrelevant: bounds are taken by
// comparison, not by position.
func Aggregate(facts []usage.Fact, opts Options) Report {
	if opts.By == "" {
		opts.By = BySession
	}
	rep := Report{
		Grouping: opts.By,
		Window:   Window{Since: opts.Since, Until: opts.Until},
	}

	accounts := map[string]*sessionAccount{}
	var order []string
	for _, f := range facts {
		if f.At == 0 {
			rep.Window.FactsUndated++
			continue
		}
		when := time.UnixMilli(f.At)
		if !inWindow(when, opts.Since, opts.Until) {
			rep.Window.FactsOutsideWindow++
			continue
		}
		rep.Window.FactsInWindow++
		spanBounds(&rep.Window.FirstFactAt, &rep.Window.LastFactAt, when)
		rep.Window.Observed = true

		acc := accounts[f.SessionID]
		if acc == nil {
			typ, matched := agentTypeOf(f.Worker, f.SessionID)
			acc = &sessionAccount{id: f.SessionID, agentType: typ, suffixUnmatched: !matched}
			accounts[f.SessionID] = acc
			order = append(order, f.SessionID)
		}
		spanBounds(&acc.firstAt, &acc.lastAt, when)
		switch f.Kind {
		case usage.KindModel:
			acc.tokens.Observed = true
			acc.tokens.Invocations++
			acc.tokens.InputTokens += f.InputTokens
			acc.tokens.OutputTokens += f.OutputTokens
			acc.tokens.CacheReadTokens += f.CacheReadTokens
			acc.tokens.CacheCreationTokens += f.CacheCreationTokens
			acc.modelFacts++
			spanBounds(&acc.firstModelAt, &acc.lastModelAt, when)
		case usage.KindCompute:
			acc.wall.Observed = true
			acc.wall.ComputeFacts++
			acc.wall.Seconds += f.WallSeconds
		}
	}
	rep.Sessions = len(accounts)

	// Totals come from the sessions, not from the groups. Under ByBead the
	// groups overlap, so summing rows would count a shared session twice.
	for _, id := range order {
		foldReading(&rep.Totals, accounts[id])
		if accounts[id].suffixUnmatched {
			rep.Residue.SuffixUnmatchedSessions++
		}
	}

	rep.Groups = buildGroups(order, accounts, opts, &rep.Residue)
	sortGroups(rep.Groups)
	if opts.Top > 0 && len(rep.Groups) > opts.Top {
		rep.GroupsOmitted = len(rep.Groups) - opts.Top
		rep.Groups = rep.Groups[:opts.Top]
	}
	return rep
}

// --- grouping ---

func buildGroups(order []string, accounts map[string]*sessionAccount, opts Options, res *Residue) []Group {
	byKey := map[string]*Group{}
	var keys []string
	shared := map[string]bool{}

	for _, id := range order {
		acc := accounts[id]
		var groupKeys []string
		switch opts.By {
		case ByType:
			groupKeys = []string{acc.agentType}
		case ByBead:
			beadIDs := opts.BeadsOf[id]
			if len(beadIDs) == 0 {
				res.SessionsWithNoBead++
				groupKeys = []string{UnattributedBeadKey}
			} else {
				groupKeys = beadIDs
				if len(beadIDs) > 1 {
					res.SessionsNamingSeveralBeads++
					for _, k := range beadIDs {
						shared[k] = true
					}
				}
			}
		default:
			groupKeys = []string{id}
		}
		for _, key := range groupKeys {
			g := byKey[key]
			if g == nil {
				g = &Group{Key: key}
				byKey[key] = g
				keys = append(keys, key)
			}
			g.Sessions = append(g.Sessions, id)
			g.AgentTypes = appendUnique(g.AgentTypes, acc.agentType)
			if acc.suffixUnmatched {
				g.SuffixUnmatched = true
			}
			foldReading(&g.Reading, acc)
			spanBounds(&g.FirstAt, &g.LastAt, acc.firstAt)
			spanBounds(&g.FirstAt, &g.LastAt, acc.lastAt)
		}
	}

	groups := make([]Group, 0, len(keys))
	for _, k := range keys {
		g := byKey[k]
		g.SharesSessions = shared[k]
		sort.Strings(g.AgentTypes)
		groups = append(groups, *g)
	}
	return groups
}

// foldReading adds one session account into a reading. Observed is OR-ed, never
// inferred from a nonzero total: a compute fact of 0.0 seconds is a measurement
// and has to survive the fold as one.
func foldReading(r *Reading, acc *sessionAccount) {
	if acc.tokens.Observed {
		r.Tokens.Observed = true
		r.Tokens.Invocations += acc.tokens.Invocations
		r.Tokens.InputTokens += acc.tokens.InputTokens
		r.Tokens.OutputTokens += acc.tokens.OutputTokens
		r.Tokens.CacheReadTokens += acc.tokens.CacheReadTokens
		r.Tokens.CacheCreationTokens += acc.tokens.CacheCreationTokens
	}
	if acc.wall.Observed {
		r.Wall.Observed = true
		r.Wall.ComputeFacts += acc.wall.ComputeFacts
		r.Wall.Seconds += acc.wall.Seconds
	}
	// Spans are summed per session, not taken across the group: two sessions of
	// an agent type that overlapped in time have two spans, and the interval
	// between the group's own first and last fact would count the idle gap
	// between unrelated sessions as work.
	if acc.modelFacts > 1 {
		r.Span.Observed = true
		r.Span.Facts += acc.modelFacts
		r.Span.Seconds += acc.lastModelAt.Sub(acc.firstModelAt).Seconds()
	}
}

// sortGroups orders by total token volume, then by key so the order is stable
// across runs. Ties are common -- a group with no model facts scores zero -- and
// an unstable order turns a diff of two reports into noise.
func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := tokenVolume(groups[i].Tokens), tokenVolume(groups[j].Tokens)
		if a != b {
			return a > b
		}
		return groups[i].Key < groups[j].Key
	})
}

func tokenVolume(t Tokens) int {
	return t.InputTokens + t.OutputTokens + t.CacheReadTokens + t.CacheCreationTokens
}

// --- agent type ---

// agentTypeOf derives an agent type from a session name, returning whether the
// name actually ended in its own session id.
//
// The session name is "<sanitized agent name>-<session id>" for a per-session
// agent and the bare sanitized name for a singleton, so the type is the name
// with that exact suffix removed -- derived from the two fields rather than
// matched against a `-ci-[a-z0-9]+$` shape, which would eat a segment from any
// agent whose own name happens to end id-shaped and file it under an agent that
// does not exist.
//
// When the suffix does not match, the name is returned whole with false. Trimming
// to the longest configured agent name that prefixes it would recover the adhoc
// sessions (`toolsmith-codex-adhoc-e1546d8930` ran in `ci-wisp-rkib0rl`), and is
// rejected: it would attribute a session to an agent on the strength of a shared
// prefix, and it would make this package's answer depend on the roster that
// happens to be configured when the report is run rather than on the record.
func agentTypeOf(worker, sessionID string) (string, bool) {
	if worker == "" {
		return "", false
	}
	if sessionID != "" && len(worker) > len(sessionID)+1 {
		if worker[len(worker)-len(sessionID)-1:] == "-"+sessionID {
			return agent.UnsanitizeQualifiedNameFromSession(worker[:len(worker)-len(sessionID)-1]), true
		}
	}
	// A singleton's session name IS its sanitized agent name, with no suffix to
	// strip and nothing unmatched about it.
	if worker == agent.SanitizeQualifiedNameForSession(agent.UnsanitizeQualifiedNameFromSession(worker)) &&
		!containsSessionTail(worker, sessionID) {
		return agent.UnsanitizeQualifiedNameFromSession(worker), true
	}
	return worker, false
}

// containsSessionTail reports whether a name carries a trailing segment that
// looks like a session tag without being this session's id -- the adhoc case.
// It exists so a singleton name (`mayor`) is not lumped in with a mismatched one
// (`toolsmith-codex-adhoc-e1546d8930`).
func containsSessionTail(worker, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	for i := len(worker) - 1; i >= 0; i-- {
		if worker[i] == '-' {
			return worker[i+1:] != sessionID && looksTagged(worker[i+1:])
		}
	}
	return false
}

// looksTagged reports whether a trailing segment is an opaque instance tag --
// long and alphanumeric with at least one digit. It is used only to decide
// whether an unmatched name is flagged, never to rewrite one, so a wrong answer
// costs a residue count and never moves tokens between agents.
func looksTagged(seg string) bool {
	if len(seg) < 8 {
		return false
	}
	digits := false
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		switch {
		case c >= '0' && c <= '9':
			digits = true
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		default:
			return false
		}
	}
	return digits
}

// --- small helpers ---

func inWindow(when, since, until time.Time) bool {
	if !since.IsZero() && when.Before(since) {
		return false
	}
	// Exclusive upper bound: two adjacent windows over the same log must neither
	// drop a fact on the seam nor count it twice.
	if !until.IsZero() && !when.Before(until) {
		return false
	}
	return true
}

func spanBounds(first, last *time.Time, when time.Time) {
	if when.IsZero() {
		return
	}
	if first.IsZero() || when.Before(*first) {
		*first = when
	}
	if last.IsZero() || when.After(*last) {
		*last = when
	}
}

func appendUnique(dst []string, v string) []string {
	for _, existing := range dst {
		if existing == v {
			return dst
		}
	}
	return append(dst, v)
}
