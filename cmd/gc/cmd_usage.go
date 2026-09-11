package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/usage"
	"github.com/gastownhall/gascity/internal/usageattr"
	"github.com/spf13/cobra"
)

// beadSessionMetadataKey is the work bead's own record of the session that
// claimed it. It is the ONLY link between a usage fact and a bead: model facts
// are attributed at run level and Fact.StepID is never filled, because per-step
// attribution was retired with the gc.active_work_bead session pointer
// (internal/worker/invocation_telemetry.go).
const beadSessionMetadataKey = "gc.session_id"

// usageSource records where one report's two inputs came from, so the reader can
// tell an unattributed row from an absent store.
type usageSource struct {
	UsagePath string
	BeadScope string // "city" or "rig/<name>"; empty when no store was consulted
}

type usageOptions struct {
	by      string
	since   string
	until   string
	top     int
	jsonOut bool
	rig     string
}

func newUsageCmd(stdout, stderr io.Writer) *cobra.Command {
	opts := usageOptions{by: string(usageattr.BySession), top: 25}
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "Per-session token and wall-clock accounting over a stated window",
		Long: `Account the recorded usage facts per session, per agent type, or per work bead.

Reads .gc/usage.jsonl (the local usage sink) and groups it on the actor axis,
which gc costs does not: gc costs rolls facts up by run id, the execution
rather than the agent that ran it. The bead column is joined through each work
bead's own gc.session_id in the bead store this command's scope resolves to --
the same scope gc bd would use -- so in a multi-rig city most sessions show as
"` + usageattr.UnattributedBeadKey + `" because their bead lives in another
rig's store.

EVERY READING SAYS WHETHER IT WAS OBSERVED. A dash is "not recorded", a zero is
a recorded zero, and they are different findings. This matters most for
wall-clock: compute facts are sparse, so most sessions have no wall-clock
reading at all, and the SPAN column -- the interval between a session's first
and last invocation -- is offered as an explicit LOWER BOUND on how long the
session was alive, never as its duration.

No cost or currency column exists. This city runs on Claude Code subscription
usage rather than metered API tokens, so a dollar figure would be fabricated;
the currencies here are tokens and wall-clock.`,
		Example: `  gc usage
  gc usage --by type --since 2026-09-03
  gc usage --by bead --since 168h --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts.rig, _ = cmd.Flags().GetString("rig")
			if doUsage(stdout, stderr, opts) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.by, "by", opts.by, "group by: session, type, or bead")
	cmd.Flags().StringVar(&opts.since, "since", "", "window start: RFC3339, YYYY-MM-DD, or a Go duration meaning that long ago")
	cmd.Flags().StringVar(&opts.until, "until", "", "window end, exclusive: RFC3339, YYYY-MM-DD, or a Go duration meaning that long ago")
	cmd.Flags().IntVar(&opts.top, "top", opts.top, "show only the N largest groups (0 shows all); totals always cover every group")
	cmd.Flags().BoolVar(&opts.jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func doUsage(stdout, stderr io.Writer, opts usageOptions) int {
	grouping, err := parseUsageGrouping(opts.by)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage: %v\n", err) //nolint:errcheck // best-effort stderr //nolint:errcheck
		return 1
	}
	now := time.Now()
	since, err := parseUsageBound(opts.since, now)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage: --since %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	until, err := parseUsageBound(opts.until, now)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage: --until %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !since.IsZero() && !until.IsZero() && !since.Before(until) {
		fmt.Fprintf(stderr, "gc usage: --since %s is not before --until %s; widen the window or swap the bounds\n", //nolint:errcheck // best-effort stderr
			since.Format(time.RFC3339), until.Format(time.RFC3339))
		return 1
	}

	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc usage: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	src := usageSource{UsagePath: filepath.Join(cityPath, ".gc", "usage.jsonl")}
	facts, warnings, err := usage.ReadFacts(src.UsagePath)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage: reading %s: %v\n", src.UsagePath, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	// Malformed lines are skipped by the reader, so surface them: a silently
	// partial read undercounts a window that later gets compared against another.
	for _, w := range warnings {
		fmt.Fprintf(stderr, "gc usage: %s\n", w) //nolint:errcheck // best-effort stderr
	}

	beadsOf := map[string][]string{}
	if grouping == usageattr.ByBead {
		beadsOf, src.BeadScope, err = usageBeadIndex(cityPath, opts.rig, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "gc usage: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}

	rep := usageattr.Aggregate(facts, usageattr.Options{
		By: grouping, Since: since, Until: until, BeadsOf: beadsOf, Top: opts.top,
	})
	if opts.jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(usageJSONView(rep, src)); err != nil {
			fmt.Fprintf(stderr, "gc usage: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	renderUsageText(stdout, rep, src)
	return 0
}

// usageBeadIndex builds session id -> bead ids from the bead store this command
// is scoped to, returning the scope label alongside it.
//
// The whole store is listed and filtered here rather than queried by key:
// ListQuery.Metadata matches key=value and there is no has-key form, and the
// values wanted are every session id there has ever been. Closed beads are
// included deliberately -- a session's work is almost always closed by the time
// anyone accounts for it.
func usageBeadIndex(cityPath, rigName string, stderr io.Writer) (map[string][]string, string, error) {
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		return nil, "", err
	}
	target, err := resolveBdScopeTarget(cfg, cityPath, rigName, nil, false, stderr)
	if err != nil {
		return nil, "", err
	}
	store, err := openStoreAtForCityWithConfig(target.ScopeRoot, cityPath, cfg)
	if err != nil {
		return nil, "", err
	}
	all, err := store.List(beads.ListQuery{IncludeClosed: true, AllowScan: true})
	if err != nil {
		return nil, "", fmt.Errorf("listing beads in %s: %w", usageScopeLabel(target), err)
	}
	index := map[string][]string{}
	for _, b := range all {
		session := strings.TrimSpace(b.Metadata[beadSessionMetadataKey])
		if session == "" {
			continue
		}
		index[session] = append(index[session], b.ID)
	}
	return index, usageScopeLabel(target), nil
}

func usageScopeLabel(target execStoreTarget) string {
	if strings.TrimSpace(target.RigName) != "" {
		return "rig/" + target.RigName
	}
	return "city"
}

func parseUsageGrouping(v string) (usageattr.Grouping, error) {
	switch usageattr.Grouping(strings.TrimSpace(v)) {
	case usageattr.BySession:
		return usageattr.BySession, nil
	case usageattr.ByType:
		return usageattr.ByType, nil
	case usageattr.ByBead:
		return usageattr.ByBead, nil
	}
	return "", fmt.Errorf("unknown --by %q: use session, type, or bead", v)
}

// parseUsageBound accepts an absolute instant or a lookback.
//
// A bare Go duration means "that long ago" rather than "that long after the
// epoch", because every window an operator asks for by duration is a trailing
// one. YYYY-MM-DD resolves to local midnight, matching how the operator reads a
// day boundary; an RFC3339 value is taken verbatim.
func parseUsageBound(v string, now time.Time) (time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return t, nil
	}
	if d, err := time.ParseDuration(v); err == nil {
		if d < 0 {
			d = -d
		}
		return now.Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("%q is not an RFC3339 instant, a YYYY-MM-DD date, or a Go duration such as 168h", v)
}

// --- text rendering ---

// usageAbsent is what an unrecorded reading prints. It is deliberately not "0",
// and not blank either: a blank cell reads as a formatting accident, and a zero
// reads as a measurement.
const usageAbsent = "-"

func renderUsageText(w io.Writer, rep usageattr.Report, src usageSource) {
	fmt.Fprintf(w, "grouping  %s\n", rep.Grouping)   //nolint:errcheck
	fmt.Fprintf(w, "window    requested %s .. %s\n", //nolint:errcheck
		usageBoundLabel(rep.Window.Since, "beginning of log"),
		usageBoundLabel(rep.Window.Until, "end of log"))
	if rep.Window.Observed {
		fmt.Fprintf(w, "          observed  %s .. %s\n", //nolint:errcheck
			rep.Window.FirstFactAt.Format(time.RFC3339),
			rep.Window.LastFactAt.Format(time.RFC3339))
	} else {
		fmt.Fprintf(w, "          observed  none -- no usage facts fell in this window\n") //nolint:errcheck
	}
	fmt.Fprintf(w, "facts     %d in window, %d outside, %d undated\n", //nolint:errcheck
		rep.Window.FactsInWindow, rep.Window.FactsOutsideWindow, rep.Window.FactsUndated)
	if src.UsagePath != "" {
		fmt.Fprintf(w, "source    %s\n", src.UsagePath) //nolint:errcheck
	}
	if rep.Grouping == usageattr.ByBead {
		fmt.Fprintf(w, "beads     joined through %s in the %s store\n", //nolint:errcheck
			beadSessionMetadataKey, usageScopeOrUnknown(src.BeadScope))
	}

	if !rep.Window.Observed {
		fmt.Fprintf(w, "\nno usage facts in this window.\n") //nolint:errcheck
		return
	}

	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "\nGROUP\tSESSIONS\tINVOCATIONS\tIN\tOUT\tCACHE_R\tCACHE_C\tWALL_S\tSPAN_S\tAGENT_TYPES") //nolint:errcheck
	for _, g := range rep.Groups {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			usageGroupLabel(g), len(g.Sessions),
			usageInt(g.Tokens.Observed, g.Tokens.Invocations),
			usageInt(g.Tokens.Observed, g.Tokens.InputTokens),
			usageInt(g.Tokens.Observed, g.Tokens.OutputTokens),
			usageInt(g.Tokens.Observed, g.Tokens.CacheReadTokens),
			usageInt(g.Tokens.Observed, g.Tokens.CacheCreationTokens),
			usageFloat(g.Wall.Observed, g.Wall.Seconds),
			usageFloat(g.Span.Observed, g.Span.Seconds),
			strings.Join(g.AgentTypes, ","))
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t\n", //nolint:errcheck
		rep.Sessions,
		usageInt(rep.Totals.Tokens.Observed, rep.Totals.Tokens.Invocations),
		usageInt(rep.Totals.Tokens.Observed, rep.Totals.Tokens.InputTokens),
		usageInt(rep.Totals.Tokens.Observed, rep.Totals.Tokens.OutputTokens),
		usageInt(rep.Totals.Tokens.Observed, rep.Totals.Tokens.CacheReadTokens),
		usageInt(rep.Totals.Tokens.Observed, rep.Totals.Tokens.CacheCreationTokens),
		usageFloat(rep.Totals.Wall.Observed, rep.Totals.Wall.Seconds),
		usageFloat(rep.Totals.Span.Observed, rep.Totals.Span.Seconds))
	tw.Flush() //nolint:errcheck

	if rep.GroupsOmitted > 0 {
		fmt.Fprintf(w, "\n%d more group(s) not shown (--top). TOTAL covers every group.\n", rep.GroupsOmitted) //nolint:errcheck
	}
	renderUsageResidue(w, rep)
	fmt.Fprintf(w, "\nWALL_S is awake seconds from compute facts; %s means none were recorded, not zero.\n", usageAbsent) //nolint:errcheck
	fmt.Fprintf(w, "SPAN_S is first-to-last invocation and is a LOWER BOUND on session duration, not wall-clock.\n")      //nolint:errcheck
}

func renderUsageResidue(w io.Writer, rep usageattr.Report) {
	r := rep.Residue
	if r.SuffixUnmatchedSessions > 0 {
		fmt.Fprintf(w, "\n%d session(s) have a name that does not end in their own session id (adhoc tags).\n", //nolint:errcheck
			r.SuffixUnmatchedSessions)
		fmt.Fprintf(w, "Their row is the session name verbatim: it is NOT trimmed to the nearest agent name.\n") //nolint:errcheck
	}
	if rep.Grouping != usageattr.ByBead {
		return
	}
	if r.SessionsWithNoBead > 0 {
		fmt.Fprintf(w, "\n%d session(s) are named by no bead in this store, shown as %q.\n", //nolint:errcheck
			r.SessionsWithNoBead, usageattr.UnattributedBeadKey)
	}
	if r.SessionsNamingSeveralBeads > 0 {
		fmt.Fprintf(w, "%d session(s) are named by more than one bead, so those rows OVERLAP.\n", //nolint:errcheck
			r.SessionsNamingSeveralBeads)
		fmt.Fprintf(w, "Each carries the whole session; TOTAL counts distinct sessions and does not double it.\n") //nolint:errcheck
	}
}

func usageGroupLabel(g usageattr.Group) string {
	if g.SharesSessions {
		return g.Key + " *"
	}
	return g.Key
}

func usageScopeOrUnknown(scope string) string {
	if strings.TrimSpace(scope) == "" {
		return "(unresolved)"
	}
	return scope
}

func usageBoundLabel(t time.Time, absent string) string {
	if t.IsZero() {
		return absent
	}
	return t.Format(time.RFC3339)
}

func usageInt(observed bool, v int) string {
	if !observed {
		return usageAbsent
	}
	return fmt.Sprintf("%d", v)
}

func usageFloat(observed bool, v float64) string {
	if !observed {
		return usageAbsent
	}
	return fmt.Sprintf("%.1f", v)
}

// --- JSON view ---

// usageJSONGroup mirrors a usageattr.Group with every reading nullable.
//
// Pointers, not values with an `observed` sibling flag: a consumer that sums
// wall_seconds over the rows gets a type error on null and a silent undercount
// on a zero, and the type error is the one that gets noticed.
type usageJSONGroup struct {
	Key                 string   `json:"key"`
	Sessions            []string `json:"sessions"`
	AgentTypes          []string `json:"agent_types"`
	SuffixUnmatched     bool     `json:"suffix_unmatched"`
	SharesSessions      bool     `json:"shares_sessions"`
	Invocations         *int     `json:"invocations"`
	InputTokens         *int     `json:"input_tokens"`
	OutputTokens        *int     `json:"output_tokens"`
	CacheReadTokens     *int     `json:"cache_read_tokens"`
	CacheCreationTokens *int     `json:"cache_creation_tokens"`
	WallSeconds         *float64 `json:"wall_seconds"`
	ComputeFacts        *int     `json:"compute_facts"`
	SpanSecondsLower    *float64 `json:"span_seconds_lower_bound"`
	FirstAt             string   `json:"first_at,omitempty"`
	LastAt              string   `json:"last_at,omitempty"`
}

type usageJSONWindow struct {
	RequestedSince     string `json:"requested_since,omitempty"`
	RequestedUntil     string `json:"requested_until,omitempty"`
	Observed           bool   `json:"observed"`
	ObservedFrom       string `json:"observed_from,omitempty"`
	ObservedTo         string `json:"observed_to,omitempty"`
	FactsInWindow      int    `json:"facts_in_window"`
	FactsOutsideWindow int    `json:"facts_outside_window"`
	FactsUndated       int    `json:"facts_undated"`
}

type usageJSONReport struct {
	SchemaVersion string           `json:"schema_version"`
	OK            bool             `json:"ok"`
	Grouping      string           `json:"grouping"`
	UsagePath     string           `json:"usage_path,omitempty"`
	BeadScope     string           `json:"bead_scope,omitempty"`
	BeadJoinKey   string           `json:"bead_join_key,omitempty"`
	Window        usageJSONWindow  `json:"window"`
	Sessions      int              `json:"sessions"`
	Groups        []usageJSONGroup `json:"groups"`
	GroupsOmitted int              `json:"groups_omitted"`
	Totals        usageJSONGroup   `json:"totals"`
	Residue       usageJSONResidue `json:"residue"`
}

type usageJSONResidue struct {
	SuffixUnmatchedSessions    int `json:"suffix_unmatched_sessions"`
	SessionsWithNoBead         int `json:"sessions_with_no_bead"`
	SessionsNamingSeveralBeads int `json:"sessions_naming_several_beads"`
}

// usageJSONView projects a report for machine consumers. Exported field names
// spell out what the text table abbreviates -- span_seconds_lower_bound rather
// than SPAN_S -- because a JSON consumer has no column legend to read.
func usageJSONView(rep usageattr.Report, src usageSource) usageJSONReport {
	out := usageJSONReport{
		SchemaVersion: "1",
		OK:            true,
		Grouping:      string(rep.Grouping),
		UsagePath:     src.UsagePath,
		BeadScope:     src.BeadScope,
		Sessions:      rep.Sessions,
		Window: usageJSONWindow{
			RequestedSince:     usageJSONTime(rep.Window.Since),
			RequestedUntil:     usageJSONTime(rep.Window.Until),
			Observed:           rep.Window.Observed,
			ObservedFrom:       usageJSONTime(rep.Window.FirstFactAt),
			ObservedTo:         usageJSONTime(rep.Window.LastFactAt),
			FactsInWindow:      rep.Window.FactsInWindow,
			FactsOutsideWindow: rep.Window.FactsOutsideWindow,
			FactsUndated:       rep.Window.FactsUndated,
		},
		GroupsOmitted: rep.GroupsOmitted,
		Groups:        make([]usageJSONGroup, 0, len(rep.Groups)),
		Residue: usageJSONResidue{
			SuffixUnmatchedSessions:    rep.Residue.SuffixUnmatchedSessions,
			SessionsWithNoBead:         rep.Residue.SessionsWithNoBead,
			SessionsNamingSeveralBeads: rep.Residue.SessionsNamingSeveralBeads,
		},
	}
	if rep.Grouping == usageattr.ByBead {
		out.BeadJoinKey = beadSessionMetadataKey
	}
	for _, g := range rep.Groups {
		out.Groups = append(out.Groups, usageJSONGroupOf(g))
	}
	out.Totals = usageJSONGroupOf(usageattr.Group{Key: "TOTAL", Reading: rep.Totals})
	return out
}

func usageJSONGroupOf(g usageattr.Group) usageJSONGroup {
	out := usageJSONGroup{
		Key:             g.Key,
		Sessions:        g.Sessions,
		AgentTypes:      g.AgentTypes,
		SuffixUnmatched: g.SuffixUnmatched,
		SharesSessions:  g.SharesSessions,
		FirstAt:         usageJSONTime(g.FirstAt),
		LastAt:          usageJSONTime(g.LastAt),
	}
	if g.Tokens.Observed {
		out.Invocations = usageIntPtr(g.Tokens.Invocations)
		out.InputTokens = usageIntPtr(g.Tokens.InputTokens)
		out.OutputTokens = usageIntPtr(g.Tokens.OutputTokens)
		out.CacheReadTokens = usageIntPtr(g.Tokens.CacheReadTokens)
		out.CacheCreationTokens = usageIntPtr(g.Tokens.CacheCreationTokens)
	}
	if g.Wall.Observed {
		out.WallSeconds = usageFloatPtr(g.Wall.Seconds)
		out.ComputeFacts = usageIntPtr(g.Wall.ComputeFacts)
	}
	if g.Span.Observed {
		out.SpanSecondsLower = usageFloatPtr(g.Span.Seconds)
	}
	return out
}

func usageIntPtr(v int) *int           { return &v }
func usageFloatPtr(v float64) *float64 { return &v }

func usageJSONTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}
