package main

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/config"
	sessionpkg "github.com/gastownhall/gascity/internal/session"
	"github.com/gastownhall/gascity/internal/worker"
	"github.com/spf13/cobra"
)

// usagePairSession is the evidence collected for one side of a paired
// first-invocation cache reading. TranscriptRoot is deliberately the configured
// root, rather than a guessed account number: that is the durable identity of
// the account-specific transcript store observed by this city.
type usagePairSession struct {
	ID              string
	Slot            string
	TranscriptRoot  string
	TranscriptPath  string
	CreatedAt       time.Time
	FirstInvocation worker.FirstInvocationUsage
}

// usagePairPeer is another session record, for the consecutiveness check.
// TranscriptRoot is empty when its transcript could not be placed.
type usagePairPeer struct {
	ID             string
	Slot           string
	CreatedAt      time.Time
	TranscriptRoot string
}

// usagePairReport contains the pairing predicate and the observed first-turn
// input accounting. It intentionally makes no cache-quality verdict: callers
// need the three raw figures to judge the experiment against its stated
// baseline.
type usagePairReport struct {
	Slot           string
	TranscriptRoot string
	Sessions       []usagePairReading
}

// usagePairReading is the reportable first-invocation token split for one
// session record.
type usagePairReading struct {
	SessionID           string    `json:"session_id"`
	TranscriptPath      string    `json:"transcript_path"`
	CreatedAt           time.Time `json:"created_at"`
	UncachedTokens      int       `json:"uncached_tokens"`
	CacheReadTokens     int       `json:"cache_read_tokens"`
	CacheCreationTokens int       `json:"cache_creation_tokens"`
}

type usagePairJSONReport struct {
	SchemaVersion  string             `json:"schema_version"`
	OK             bool               `json:"ok"`
	StableSlot     string             `json:"stable_slot"`
	TranscriptRoot string             `json:"transcript_root"`
	Sessions       []usagePairReading `json:"sessions"`
}

func newUsagePairCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "pair <first-session> <second-session>",
		Short: "Report first-invocation cache inputs for a verified session pair",
		Long: `Report uncached, cache-read, and cache-creation input tokens from the first invocation of two sessions.

The command refuses a pair unless the two records resolve to the same stable
agent slot and the same configured transcript-root account, and unless no other
spawn of that slot on that account was created between them. It reports the
earlier session first, with each creation time, whatever the argument order,
and without declaring whether the pair passed a cache experiment.`,
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if doUsagePair(stdout, stderr, args[0], args[1], jsonOut) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of a table")
	return cmd
}

func doUsagePair(stdout, stderr io.Writer, firstID, secondID string, jsonOut bool) int {
	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc usage pair: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage pair: loading city config: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	store, err := tryOpenCityStore()
	if err != nil || store == nil {
		if err == nil {
			err = fmt.Errorf("city session store unavailable")
		}
		fmt.Fprintf(stderr, "gc usage pair: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	front := cliSessionFrontDoor(store, cfg, cityPath)
	searchPaths := worker.MergeSearchPaths(cfg.Daemon.ObservePaths)
	first, err := resolveUsagePairSession(cityPath, cfg, front, searchPaths, firstID)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage pair: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	second, err := resolveUsagePairSession(cityPath, cfg, front, searchPaths, secondID)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage pair: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	peers, err := usagePairSlotPeers(cityPath, cfg, front, searchPaths, first, second)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage pair: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	report, err := buildUsagePairReport(first, second, peers)
	if err != nil {
		fmt.Fprintf(stderr, "gc usage pair: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if jsonOut {
		if err := json.NewEncoder(stdout).Encode(usagePairJSONReport{
			SchemaVersion:  "1",
			OK:             true,
			StableSlot:     report.Slot,
			TranscriptRoot: report.TranscriptRoot,
			Sessions:       report.Sessions,
		}); err != nil {
			fmt.Fprintf(stderr, "gc usage pair: writing report: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	renderUsagePairReport(stdout, report)
	return 0
}

func resolveUsagePairSession(cityPath string, cfg *config.City, front *sessionpkg.Store, searchPaths []string, identifier string) (usagePairSession, error) {
	ctx, ok := resolveSessionLogContext(cityPath, cfg, front, identifier)
	if !ok {
		return usagePairSession{}, fmt.Errorf("session %q could not be resolved", identifier)
	}
	info, err := front.Get(ctx.sessionID)
	if err != nil {
		return usagePairSession{}, fmt.Errorf("reading session %q: %w", identifier, err)
	}
	slot := usagePairStableSlot(info)
	if slot == "" {
		return usagePairSession{}, fmt.Errorf("session %q has no stable slot identity", identifier)
	}
	if strings.TrimSpace(ctx.sessionKey) == "" {
		return usagePairSession{}, fmt.Errorf("session %q has no session key for an exact transcript lookup", identifier)
	}
	path := resolveSessionKeyedLogPath(searchPaths, ctx)
	if !sessionLogPathFreshEnough(path, ctx.createdAt) {
		return usagePairSession{}, fmt.Errorf("session %q has no exact transcript under a configured root", identifier)
	}
	root := usagePairTranscriptRoot(searchPaths, path)
	if root == "" {
		return usagePairSession{}, fmt.Errorf("session %q transcript is outside every configured transcript-root account", identifier)
	}
	usage, found, err := worker.FirstInvocationUsageFromSearchPaths(searchPaths, path)
	if err != nil {
		return usagePairSession{}, fmt.Errorf("reading first invocation for session %q: %w", identifier, err)
	}
	if !found {
		return usagePairSession{}, fmt.Errorf("session %q has no first invocation with token usage", identifier)
	}
	return usagePairSession{
		ID:              info.ID,
		Slot:            slot,
		TranscriptRoot:  root,
		TranscriptPath:  path,
		CreatedAt:       info.CreatedAt,
		FirstInvocation: usage,
	}, nil
}

func usagePairStableSlot(info sessionpkg.Info) string {
	// Ad-hoc sessions deliberately receive a unique runtime name derived from
	// their template. The suffix identifies an instance, not a stable agent
	// slot, so pair those sessions by the configured template instead.
	agentName := strings.TrimSpace(info.AgentName)
	template := strings.TrimSpace(info.Template)
	if template != "" && strings.HasPrefix(agentName, template+"-adhoc-") {
		return template
	}
	for _, candidate := range []string{info.AgentName, info.Alias, info.Template} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func usagePairTranscriptRoot(searchPaths []string, path string) string {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return ""
	}
	best := ""
	for _, root := range searchPaths {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		absRoot, err := filepath.Abs(filepath.Clean(root))
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if len(absRoot) > len(best) {
			best = absRoot
		}
	}
	return best
}

func buildUsagePairReport(first, second usagePairSession, peers []usagePairPeer) (usagePairReport, error) {
	if first.ID == second.ID {
		return usagePairReport{}, fmt.Errorf("refusing one session twice: both arguments resolve to %q", first.ID)
	}
	if first.Slot != second.Slot {
		return usagePairReport{}, fmt.Errorf("refusing mismatched stable slot pair: %q versus %q", first.Slot, second.Slot)
	}
	if first.TranscriptRoot != second.TranscriptRoot {
		return usagePairReport{}, fmt.Errorf("refusing mismatched transcript-root account pair: %q versus %q", first.TranscriptRoot, second.TranscriptRoot)
	}
	if first.CreatedAt.IsZero() || second.CreatedAt.IsZero() || first.CreatedAt.Equal(second.CreatedAt) {
		return usagePairReport{}, fmt.Errorf("refusing a pair that cannot be ordered: %q created %s, %q created %s",
			first.ID, usagePairTime(first.CreatedAt), second.ID, usagePairTime(second.CreatedAt))
	}
	earlier, later := first, second
	if later.CreatedAt.Before(earlier.CreatedAt) {
		earlier, later = later, earlier
	}
	// A spawn of the slot between the two, on their account or one that cannot
	// be placed, means the later session is not the earlier one's successor.
	var successor *usagePairPeer
	for i, peer := range peers {
		if peer.ID == earlier.ID || peer.ID == later.ID || peer.Slot != earlier.Slot ||
			!peer.CreatedAt.After(earlier.CreatedAt) || !peer.CreatedAt.Before(later.CreatedAt) {
			continue
		}
		if peer.TranscriptRoot != "" && peer.TranscriptRoot != earlier.TranscriptRoot {
			continue
		}
		if successor == nil || peer.CreatedAt.Before(successor.CreatedAt) {
			successor = &peers[i]
		}
	}
	if successor != nil {
		account := "the same transcript root"
		if successor.TranscriptRoot == "" {
			account = "a transcript root that could not be placed"
		}
		return usagePairReport{}, fmt.Errorf("refusing a pair that is not consecutive: %q of slot %q was created %s, "+
			"between %q and %q, on %s. Remedy: gc usage pair %s %s",
			successor.ID, earlier.Slot, usagePairTime(successor.CreatedAt), earlier.ID, later.ID, account,
			earlier.ID, successor.ID)
	}
	return usagePairReport{
		Slot:           earlier.Slot,
		TranscriptRoot: earlier.TranscriptRoot,
		Sessions: []usagePairReading{
			usagePairReadingFrom(earlier),
			usagePairReadingFrom(later),
		},
	}, nil
}

func usagePairTime(t time.Time) string {
	if t.IsZero() {
		return "at no recorded time"
	}
	return t.UTC().Format(time.RFC3339)
}

func usagePairReadingFrom(session usagePairSession) usagePairReading {
	return usagePairReading{
		SessionID:           session.ID,
		TranscriptPath:      session.TranscriptPath,
		CreatedAt:           session.CreatedAt,
		UncachedTokens:      session.FirstInvocation.InputTokens,
		CacheReadTokens:     session.FirstInvocation.CacheReadTokens,
		CacheCreationTokens: session.FirstInvocation.CacheCreationTokens,
	}
}

func renderUsagePairReport(w io.Writer, report usagePairReport) {
	fmt.Fprintf(w, "stable slot       %s\n", report.Slot)                       //nolint:errcheck // best-effort stdout
	fmt.Fprintf(w, "transcript root   %s\n", report.TranscriptRoot)             //nolint:errcheck // best-effort stdout
	fmt.Fprintln(w, "\nSESSION\tCREATED\tUNCACHED\tCACHE_READ\tCACHE_CREATION") //nolint:errcheck // best-effort stdout
	for _, session := range report.Sessions {
		fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\n", session.SessionID, usagePairTime(session.CreatedAt), session.UncachedTokens, session.CacheReadTokens, session.CacheCreationTokens) //nolint:errcheck // best-effort stdout
	}
}

// usagePairSlotPeers lists the other sessions of the pair's slot created
// between the two, placing each under a transcript root where its transcript
// can be found. A listing that fails refuses the read: consecutiveness is the
// predicate, and an unchecked one is not a pass.
func usagePairSlotPeers(cityPath string, cfg *config.City, front *sessionpkg.Store, searchPaths []string, a, b usagePairSession) ([]usagePairPeer, error) {
	lo, hi := a.CreatedAt, b.CreatedAt
	if hi.Before(lo) {
		lo, hi = hi, lo
	}
	all, err := front.ListAll(sessionpkg.ListAllOptions{IncludeClosed: true})
	if err != nil {
		return nil, fmt.Errorf("listing sessions to check the pair is consecutive: %w", err)
	}
	var peers []usagePairPeer
	for _, info := range all {
		if info.ID == a.ID || info.ID == b.ID || usagePairStableSlot(info) != a.Slot ||
			!info.CreatedAt.After(lo) || !info.CreatedAt.Before(hi) {
			continue
		}
		peer := usagePairPeer{ID: info.ID, Slot: a.Slot, CreatedAt: info.CreatedAt}
		if ctx, ok := resolveSessionLogContext(cityPath, cfg, front, info.ID); ok {
			if path := resolveSessionKeyedLogPath(searchPaths, ctx); sessionLogPathFreshEnough(path, ctx.createdAt) {
				peer.TranscriptRoot = usagePairTranscriptRoot(searchPaths, path)
			}
		}
		peers = append(peers, peer)
	}
	return peers, nil
}
