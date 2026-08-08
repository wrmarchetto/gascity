package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/doctor"
)

// Session-spawn-rate thresholds.
//
// Derived from measurement, not chosen: the observed loop ran one session
// every 12-16s on three separate agents -- 4-5 creates/min, ~60 per 15
// minutes. The control case (an agent doing real work) had a median session
// lifetime of 236s, and healthy agents in the same city ran minutes to tens
// of minutes per session. The trip point sits an order of magnitude above
// normal cadence and an order of magnitude below the storm, so it needs no
// tuning to separate them.
//
// The window is 15 minutes rather than 1: a per-minute rate computed over one
// minute is dominated by sampling noise, and the storm is sustained for hours,
// so a wider window trades nothing for a much steadier signal. It also bounds
// how long the gate stays red after a storm ends -- see
// TestSessionSpawnRateOnlyCountsInsideTheWindow.
const (
	sessionSpawnRateWindow    = 15 * time.Minute
	sessionSpawnRateThreshold = 8 // creates per agent per window
)

// sessionSpawnRateCheck fails when a single agent creates sessions far faster
// than any real work could justify -- the signature of a spawn loop.
//
// Why a counter and not a smarter detector: every other symptom of this
// failure is invisible. The bead being worked never moves, so the queue reads
// healthy; the session is alive, so `gc status` says running; and the close
// reason is canonical display text shared by self-drain and reconciler
// retirement alike, so the session bead does not read as anomalous either.
// 140 sessions burned across two days before anyone noticed, each a full
// model launch. Session creates per agent per minute is the one number that
// would have shown it immediately, and nothing counted it.
//
// Deliberately CAUSE-AGNOSTIC. The loop that prompted this was unread mail
// creating demand the claim path refuses, but the identical shape was
// separately observed from a wedged bead (open, started_at set, no assignee),
// and a third agent's 110 cycles were never diagnosed. A detector keyed to
// any one cause would have missed the other two. This one keys on the cost.
//
// No --fix. The remedy is `gc agent suspend <agent>`, which takes an agent
// out of service and strands its work; that is an operator decision, not a
// mechanical repair. The check names the agent and the rate so the operator
// can make it.
type sessionSpawnRateCheck struct {
	cfg      *config.City
	cityPath string
	newStore func(string) (beads.Store, error)
	now      func() time.Time
}

func newSessionSpawnRateCheck(cfg *config.City, cityPath string, newStore func(string) (beads.Store, error)) *sessionSpawnRateCheck {
	return &sessionSpawnRateCheck{cfg: cfg, cityPath: cityPath, newStore: newStore, now: time.Now}
}

func (c *sessionSpawnRateCheck) Name() string { return "session-spawn-rate" }

func (c *sessionSpawnRateCheck) CanFix() bool { return false }

func (c *sessionSpawnRateCheck) Fix(_ *doctor.CheckContext) error { return nil }

// WarmupEligible keeps this out of `gc start`'s warm-up scan: at start there
// is no recent history to rate, and a blocking failure there would refuse to
// start a city over a storm that start itself has not caused yet.
func (c *sessionSpawnRateCheck) WarmupEligible() bool { return false }

// sessionBeadAgent returns the agent a session bead belongs to. The runtime
// stamps the agent in both the agent:<name> label and the agent_name
// metadata key; the label is canonical, metadata is the fallback for older
// beads written before the label existed. Title is deliberately NOT used --
// it holds the agent name today but is display text, not identity.
func sessionBeadAgent(b beads.Bead) string {
	for _, l := range b.Labels {
		if rest, ok := strings.CutPrefix(l, "agent:"); ok {
			if name := strings.TrimSpace(rest); name != "" {
				return name
			}
		}
	}
	return strings.TrimSpace(b.Metadata["agent_name"])
}

// countRecentSpawnsByAgent tallies session beads created inside the window.
// Beads with a zero CreatedAt are skipped rather than counted as "now": a
// missing timestamp is unknown, and treating it as recent would let a backlog
// of undated legacy session beads trip the gate on a quiet city.
func countRecentSpawnsByAgent(sessions []beads.Bead, since time.Time) map[string]int {
	counts := make(map[string]int)
	for _, b := range sessions {
		if b.CreatedAt.IsZero() || b.CreatedAt.Before(since) {
			continue
		}
		if agent := sessionBeadAgent(b); agent != "" {
			counts[agent]++
		}
	}
	return counts
}

func (c *sessionSpawnRateCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	res := &doctor.CheckResult{Name: c.Name()}
	if c.newStore == nil || strings.TrimSpace(c.cityPath) == "" {
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = "session spawn rate unknown: no city bead store configured"
		return res
	}
	store, err := c.newStore(c.cityPath)
	if err != nil {
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = fmt.Sprintf("session spawn rate unknown: opening city bead store: %v", err)
		return res
	}
	// Closed sessions are the whole point: a looping session is closed within
	// ~13s, so an open-only query would see almost none of the burn.
	//
	// This reads every session bead rather than only recent ones because
	// ListQuery has no since-filter -- CreatedBefore/UpdatedBefore both select
	// the wrong side. The population is bounded by session retention, not by
	// city age, and the windowing happens in countRecentSpawnsByAgent. If
	// retention ever grows enough for this scan to matter, add a CreatedAfter
	// to ListQuery rather than narrowing the window, which would blind the
	// gate to slower loops.
	sessions, err := store.List(beads.ListQuery{Type: sessionBeadType, IncludeClosed: true})
	if err != nil {
		res.Status = doctor.StatusWarning
		res.Severity = doctor.SeverityAdvisory
		res.Message = fmt.Sprintf("session spawn rate unknown: listing session beads: %v", err)
		return res
	}

	now := c.now()
	counts := countRecentSpawnsByAgent(sessions, now.Add(-sessionSpawnRateWindow))

	type agentRate struct {
		agent string
		count int
	}
	var storming []agentRate
	for agent, n := range counts {
		if n >= sessionSpawnRateThreshold {
			storming = append(storming, agentRate{agent: agent, count: n})
		}
	}
	sort.Slice(storming, func(i, j int) bool {
		if storming[i].count == storming[j].count {
			return storming[i].agent < storming[j].agent
		}
		return storming[i].count > storming[j].count
	})

	if len(storming) == 0 {
		res.Status = doctor.StatusOK
		res.Message = fmt.Sprintf("no agent above %d session creates per %s",
			sessionSpawnRateThreshold, sessionSpawnRateWindow)
		return res
	}

	res.Status = doctor.StatusError
	res.Severity = doctor.SeverityBlocking
	res.Message = fmt.Sprintf("%d agent(s) spawning sessions faster than work justifies (>= %d per %s)",
		len(storming), sessionSpawnRateThreshold, sessionSpawnRateWindow)
	res.FixHint = "check `gc hook <agent>` returns something `gc hook --claim` can actually claim; " +
		"`gc agent suspend <agent>` stops the burn while you look"
	details := make([]string, 0, len(storming))
	for _, s := range storming {
		details = append(details, fmt.Sprintf("%s: %d session creates in the last %s (%.1f/min)",
			s.agent, s.count, sessionSpawnRateWindow, float64(s.count)/sessionSpawnRateWindow.Minutes()))
	}
	res.Details = details
	return res
}
