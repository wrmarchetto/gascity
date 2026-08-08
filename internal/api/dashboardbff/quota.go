// internal/api/dashboardbff/quota.go — GET /api/account-quota, the merged
// per-account rate-limit view the Accounts tab renders.
//
// The endpoint reads two files that live outside every repo and are written by
// tooling this repo does not own: one JSON record per account under
// <homes>/quota/<id>.json (written by the statusline collector on every render
// of a live session) and <homes>/rotation.json (written by claude-pool and the
// cap sweeper under a flock). Both may be absent, half-written, or malformed at
// any moment, so every failure here is IN-BAND: the handler has no error status
// and an unreadable input degrades one field rather than the response. A 500
// would take a tab out of a dashboard the operator is watching the city
// through.
//
// One endpoint, not two, so the tab makes a single request and cannot render a
// quota reading against a pool list fetched a poll apart.
//
// Editing constraints, in falling order of how quietly they break:
//
//   - This file must NOT classify staleness. used_percentage decays with the
//     rolling window, so a reading's meaning depends on the age at RENDER time,
//     not at response time. The server sends observed_at and resets_at verbatim
//     and shared/src/dashboard-quota.ts applies the threshold against the
//     browser clock. Classifying here would freeze "as of 2m ago" onto a page
//     that has been open an hour.
//   - Group names are configuration (Deps.AccountLabels), never constants. The
//     pool has changed membership before and the accounts outside it are
//     reserved for reasons this SDK has no business naming (ZERO hardcoded
//     roles, AGENTS.md).
//   - rotation.json's suspects map is keyed by SESSION name, not by account
//     (account-cap-sweep.py writes suspects[session] = {sightings, last_seen}).
//     Attribution goes through bindings. An account-keyed read compiles, runs,
//     and reports zero suspects forever.
//
// Verified by quota_test.go.

package dashboardbff

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// accountHomesDirName is the per-account home root under $HOME, and
// accountDirPrefix is the per-account directory prefix inside it. Both are the
// on-disk layout claude-pool creates; they are the account inventory's only
// source that does not depend on an account having been used or observed yet.
const (
	accountHomesDirName = ".claude-homes"
	accountDirPrefix    = "account"
	accountQuotaDirName = "quota"
	accountRotationFile = "rotation.json"
)

// safeAccountID matches an account id usable as a single path segment. Two of
// the four inventory sources (rotation.json's pool, the configured label map)
// are outside-the-repo input that reaches filepath.Join, so ids are validated
// and DROPPED rather than sanitized: a rewritten id would file one account's
// levels under another account's name, which is worse than omitting the row.
var safeAccountID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// observationState names which of the mutually exclusive presence states an
// account's quota record is in. They are distinct on the wire because they
// carry different operational meaning and the tab must not collapse them:
// "never observed" says no session has run there since collection was wired,
// while "unreadable" says one did and the record cannot be trusted.
type observationState string

const (
	observationNeverObserved observationState = "never_observed"
	observationNoLimits      observationState = "no_limits"
	observationObserved      observationState = "observed"
	observationUnreadable    observationState = "unreadable"
)

// accountQuotaWindow is one rate-limit window as the collector recorded it,
// passed through verbatim. resets_at is an absolute epoch and stays correct at
// any age; used_percentage is only meaningful next to the observed_at on the
// enclosing observation.
type accountQuotaWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// accountObservation is one account's quota record. ObservedAt is null only in
// the never_observed and unreadable states; the windows are null in every state
// but observed. Absent windows are NEVER zero-filled — a zero renders as a
// healthy idle account, which is the dangerous misread.
type accountObservation struct {
	State      observationState    `json:"state"`
	ObservedAt *int64              `json:"observed_at"`
	SessionID  string              `json:"session_id"`
	FiveHour   *accountQuotaWindow `json:"five_hour"`
	SevenDay   *accountQuotaWindow `json:"seven_day"`
	// Reason is empty unless State is unreadable, where it names the fault so
	// the operator can find the writer that produced it.
	Reason string `json:"reason"`
}

// accountQuotaEntry is one account row: its observation plus the rotation
// context that explains whether anything is expected to be observing it.
type accountQuotaEntry struct {
	Account string `json:"account"`
	// Label is the operator-supplied group name for accounts held out of the
	// pool, empty when unconfigured. Not derived in Go — see the file header.
	Label  string `json:"label"`
	InPool bool   `json:"in_pool"`

	Observation accountObservation `json:"observation"`

	// LastUsedAt is when the rotation last handed this account to a session,
	// null when it never has. CooldownUntil is null unless the sweeper has
	// parked the account.
	LastUsedAt    *int64 `json:"last_used_at"`
	CooldownUntil *int64 `json:"cooldown_until"`
	// BoundSessions counts rotation bindings naming this account. These are
	// bindings ON RECORD, not proof of liveness: nothing prunes the map when a
	// session ends, so a high count means "has been used", not "is busy".
	BoundSessions int `json:"bound_sessions"`
	// SuspectSessions counts bound sessions the cap sweeper has seen the cap
	// marker on, joined through bindings (suspects is session-keyed).
	SuspectSessions int `json:"suspect_sessions"`
}

// accountRotationState reports whether the pool context could be read at all,
// so an empty pool is never mistaken for "nothing rotates".
type accountRotationState struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason"`
}

// accountQuotaReport is the GET /api/account-quota body. Accounts and Pool are
// always non-nil so the SPA decoder sees arrays, never null.
type accountQuotaReport struct {
	Accounts []accountQuotaEntry  `json:"accounts"`
	Pool     []string             `json:"pool"`
	Rotation accountRotationState `json:"rotation"`
	// HomesDir is the root actually read, so an empty tab says where it looked
	// instead of leaving the operator to guess.
	HomesDir string `json:"homes_dir"`
	// UnattributedSuspects counts suspect sessions with no binding, which
	// therefore belong to no row above. Reported rather than dropped: a suspect
	// nobody can attribute is a state worth seeing.
	UnattributedSuspects int `json:"unattributed_suspects"`
}

// quotaRecord is the on-disk <homes>/quota/<id>.json shape. Every field is a
// pointer because the three presence states are told apart by absence:
// rate_limits omitted or null is a real, distinct record.
type quotaRecord struct {
	Account    *string `json:"account"`
	ObservedAt *int64  `json:"observed_at"`
	SessionID  *string `json:"session_id"`
	RateLimits *struct {
		FiveHour *accountQuotaWindow `json:"five_hour"`
		SevenDay *accountQuotaWindow `json:"seven_day"`
	} `json:"rate_limits"`
}

// rotationRecord is the subset of <homes>/rotation.json this plane reads.
//
// Suspects values stay json.RawMessage because only key presence is used and
// the value shape belongs to account-cap-sweep.py; decoding it into a struct
// would make an upstream field change fail the WHOLE file's decode, taking the
// pool list down with it. (The map/array distinction still has to hold — a
// structurally different rotation.json is a real fault and fails loudly.)
type rotationRecord struct {
	Pool          []string                   `json:"pool"`
	LastUsed      map[string]float64         `json:"last_used"`
	CooldownUntil map[string]float64         `json:"cooldown_until"`
	Suspects      map[string]json.RawMessage `json:"suspects"`
	Bindings      map[string]struct {
		Account string `json:"account"`
	} `json:"bindings"`
}

// registerQuota wires GET /api/account-quota onto the plane mux. The route is
// not city-scoped: the account homes are a host-level resource shared by every
// city this supervisor serves.
func (p *Plane) registerQuota() {
	p.mux.HandleFunc("GET /api/account-quota", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, p.accountQuota())
	})
}

// accountHomesDir resolves the account-homes root: the configured override, or
// $HOME/.claude-homes. An empty HOME yields a relative name that simply will
// not exist, which the caller renders as an empty tab.
func (p *Plane) accountHomesDir() string {
	if dir := strings.TrimSpace(p.deps.AccountHomesDir); dir != "" {
		return dir
	}
	return filepath.Join(os.Getenv("HOME"), accountHomesDirName)
}

// accountQuota assembles the report. It never returns an error: every input is
// optional and every fault is reported in-band on the field it affects.
func (p *Plane) accountQuota() accountQuotaReport {
	homes := p.accountHomesDir()
	rotation, rotationState := readRotation(filepath.Join(homes, accountRotationFile))

	pool := filterSafeIDs(rotation.Pool)
	inPool := make(map[string]bool, len(pool))
	for _, id := range pool {
		inPool[id] = true
	}

	quotaDir := filepath.Join(homes, accountQuotaDirName)
	boundSessions, suspectSessions, unattributed := attributeSessions(rotation)

	report := accountQuotaReport{
		Accounts:             []accountQuotaEntry{},
		Pool:                 pool,
		Rotation:             rotationState,
		HomesDir:             homes,
		UnattributedSuspects: unattributed,
	}
	for _, id := range accountInventory(homes, quotaDir, pool, p.deps.AccountLabels) {
		report.Accounts = append(report.Accounts, accountQuotaEntry{
			Account:         id,
			Label:           p.deps.AccountLabels[id],
			InPool:          inPool[id],
			Observation:     readObservation(filepath.Join(quotaDir, id+".json"), id),
			LastUsedAt:      epochSecondsOf(rotation.LastUsed, id),
			CooldownUntil:   epochSecondsOf(rotation.CooldownUntil, id),
			BoundSessions:   boundSessions[id],
			SuspectSessions: suspectSessions[id],
		})
	}
	return report
}

// accountInventory unions the four sources that can each know about an account
// the others do not: the per-account home directories (exists), the quota
// records (has been observed), the rotation pool (is scheduled), and the
// configured labels (is deliberately held out). Sorted so the tab's row order
// is stable across polls — neither readdir nor JSON map iteration is ordered.
//
// rotation.json's bindings and last_used are deliberately NOT sources here.
// They outlive the accounts they name (nothing prunes them), so an account
// retired from the machine would keep reappearing as a row with nothing in it.
func accountInventory(homes, quotaDir string, pool []string, labels map[string]string) []string {
	seen := map[string]bool{}
	add := func(id string) {
		if safeAccountID.MatchString(id) {
			seen[id] = true
		}
	}

	if entries, err := os.ReadDir(homes); err == nil {
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), accountDirPrefix) {
				add(strings.TrimPrefix(entry.Name(), accountDirPrefix))
			}
		}
	}
	if entries, err := os.ReadDir(quotaDir); err == nil {
		for _, entry := range entries {
			if name := entry.Name(); !entry.IsDir() && strings.HasSuffix(name, ".json") {
				add(strings.TrimSuffix(name, ".json"))
			}
		}
	}
	for _, id := range pool {
		add(id)
	}
	for id := range labels {
		add(id)
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return lessAccountID(ids[i], ids[j]) })
	return ids
}

// lessAccountID orders account ids the way an operator reads them. Plain
// sort.Strings would file account10 between account1 and account2, which in a
// capacity view is the kind of wrong-looking-but-not-wrong detail nobody comes
// back to fix. Ids are not required to be numeric (nothing constrains them
// beyond safeAccountID), so non-numeric ids fall back to lexicographic and sort
// after the numeric ones rather than interleaving with them.
func lessAccountID(a, b string) bool {
	na, aErr := strconv.Atoi(a)
	nb, bErr := strconv.Atoi(b)
	aNumeric, bNumeric := aErr == nil, bErr == nil
	switch {
	case aNumeric && bNumeric:
		return na < nb
	case aNumeric != bNumeric:
		return aNumeric
	default:
		return a < b
	}
}

// readObservation reads one account's quota record. id is the filename the
// record was found under and is authoritative for identity; a record claiming a
// different account is unreadable rather than silently filed under either one.
func readObservation(path, id string) accountObservation {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return accountObservation{State: observationNeverObserved}
		}
		return accountObservation{
			State:  observationUnreadable,
			Reason: fmt.Sprintf("reading %s: %v", filepath.Base(path), err),
		}
	}
	var record quotaRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return accountObservation{
			State:  observationUnreadable,
			Reason: fmt.Sprintf("parsing %s: %v", filepath.Base(path), err),
		}
	}
	if record.Account != nil && *record.Account != id {
		return accountObservation{
			State: observationUnreadable,
			Reason: fmt.Sprintf("%s records account %q; the two disagree, so neither can be trusted",
				filepath.Base(path), *record.Account),
		}
	}
	// A percentage with no timestamp cannot be aged, and an un-ageable reading
	// must not reach the browser: the staleness rule would have nothing to
	// apply and would render a possibly-hours-old number as current.
	if record.ObservedAt == nil || *record.ObservedAt <= 0 {
		return accountObservation{
			State:  observationUnreadable,
			Reason: fmt.Sprintf("%s carries no usable observed_at, so its levels cannot be aged", filepath.Base(path)),
		}
	}

	observation := accountObservation{ObservedAt: record.ObservedAt}
	if record.SessionID != nil {
		observation.SessionID = *record.SessionID
	}
	if record.RateLimits == nil {
		observation.State = observationNoLimits
		return observation
	}
	observation.State = observationObserved
	observation.FiveHour = record.RateLimits.FiveHour
	observation.SevenDay = record.RateLimits.SevenDay
	return observation
}

// readRotation reads the pool context. Absent and malformed are reported
// distinctly in the reason so an empty pool is never read as "nothing rotates".
func readRotation(path string) (rotationRecord, accountRotationState) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return rotationRecord{}, accountRotationState{
				Reason: fmt.Sprintf("%s does not exist, so pool membership is unknown", path),
			}
		}
		return rotationRecord{}, accountRotationState{Reason: fmt.Sprintf("reading %s: %v", path, err)}
	}
	var record rotationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return rotationRecord{}, accountRotationState{Reason: fmt.Sprintf("parsing %s: %v", path, err)}
	}
	return record, accountRotationState{Available: true}
}

// attributeSessions joins the session-keyed bindings and suspects maps onto
// accounts, returning per-account binding counts, per-account suspect counts,
// and the number of suspects no binding could place.
func attributeSessions(rotation rotationRecord) (bound, suspect map[string]int, unattributed int) {
	bound = make(map[string]int, len(rotation.Bindings))
	suspect = make(map[string]int)
	for _, binding := range rotation.Bindings {
		if safeAccountID.MatchString(binding.Account) {
			bound[binding.Account]++
		}
	}
	for session := range rotation.Suspects {
		binding, ok := rotation.Bindings[session]
		if !ok || !safeAccountID.MatchString(binding.Account) {
			unattributed++
			continue
		}
		suspect[binding.Account]++
	}
	return bound, suspect, unattributed
}

// epochSecondsOf truncates one of rotation.json's float epochs to whole
// seconds, returning nil when the account has no entry. Sub-second precision is
// noise at this resolution and the wire carries epoch seconds everywhere else.
func epochSecondsOf(values map[string]float64, id string) *int64 {
	value, ok := values[id]
	if !ok {
		return nil
	}
	seconds := int64(value)
	return &seconds
}

// filterSafeIDs drops ids that are not usable as a path segment, preserving
// input order (the pool's order is the operator's, not ours to sort).
func filterSafeIDs(ids []string) []string {
	safe := make([]string, 0, len(ids))
	for _, id := range ids {
		if safeAccountID.MatchString(id) {
			safe = append(safe, id)
		}
	}
	return safe
}

// ParseAccountLabels parses the DASHBOARD_ACCOUNT_LABELS form
// "0=operator interactive,1=orchestrator pin" into a label map. Entries without
// an "=", with an empty id, or with an empty label are dropped rather than
// stored blank, so a typo leaves the account unlabeled instead of labeled with
// nothing. Returns nil for empty input.
func ParseAccountLabels(spec string) map[string]string {
	labels := map[string]string{}
	for _, entry := range strings.Split(spec, ",") {
		id, label, found := strings.Cut(entry, "=")
		id, label = strings.TrimSpace(id), strings.TrimSpace(label)
		if !found || id == "" || label == "" {
			continue
		}
		labels[id] = label
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}
