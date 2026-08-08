package dashboardbff

// Scope: the /api/account-quota reader — account inventory, the three
// observation states, rotation-context joins, and the degradations that must
// not take the endpoint down. The suite exists because every input this
// endpoint reads lives OUTSIDE the repo (~/.claude-homes), is written by
// scripts this repo does not own, and may be absent, partially written, or
// malformed at any moment.
//
// Staleness is deliberately NOT tested here: the Go side never classifies it.
// The threshold is applied against the browser's clock in
// shared/src/dashboard-quota.ts, whose Vitest suite pins it. A staleness
// assertion here would pass against a server-side classification that renders
// a stale value confidently the moment the page sits open.
//
//	go test ./internal/api/dashboardbff/ -run TestAccountQuota

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// quotaHomes builds a ~/.claude-homes-shaped tree: one accountN directory per
// id in accounts, a quota/ directory holding the raw bodies in files (keyed by
// account id), and rotation.json holding rotationBody. An empty body string
// means "write no file", which is how the never-observed and absent-rotation
// cases are set up.
func quotaHomes(t *testing.T, accounts []string, files map[string]string, rotationBody string) string {
	t.Helper()
	root := t.TempDir()
	for _, id := range accounts {
		if err := os.MkdirAll(filepath.Join(root, "account"+id), 0o755); err != nil {
			t.Fatalf("mkdir account%s: %v", id, err)
		}
	}
	if len(files) > 0 {
		if err := os.MkdirAll(filepath.Join(root, "quota"), 0o755); err != nil {
			t.Fatalf("mkdir quota: %v", err)
		}
		for id, body := range files {
			if body == "" {
				continue
			}
			path := filepath.Join(root, "quota", id+".json")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write %s: %v", path, err)
			}
		}
	}
	if rotationBody != "" {
		path := filepath.Join(root, "rotation.json")
		if err := os.WriteFile(path, []byte(rotationBody), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// getAccountQuota serves GET /api/account-quota against a plane whose homes
// root is dir and decodes the report. It fails the test on any non-200: the
// endpoint has no failure status by design — every degradation is in-band.
func getAccountQuota(t *testing.T, dir string, labels map[string]string) accountQuotaReport {
	t.Helper()
	p := New(Deps{AccountHomesDir: dir, AccountLabels: labels})
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/account-quota", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got accountQuotaReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	return got
}

func findAccount(t *testing.T, report accountQuotaReport, id string) accountQuotaEntry {
	t.Helper()
	for _, entry := range report.Accounts {
		if entry.Account == id {
			return entry
		}
	}
	t.Fatalf("account %q absent from report (have %v)", id, accountIDsOf(report))
	return accountQuotaEntry{}
}

func accountIDsOf(report accountQuotaReport) []string {
	ids := make([]string, 0, len(report.Accounts))
	for _, entry := range report.Accounts {
		ids = append(ids, entry.Account)
	}
	return ids
}

// A homes root that does not exist is the state of any machine that has never
// run the collector. It must render an empty tab, never a 500 — the dashboard
// is served by the supervisor and read while the city runs.
func TestAccountQuotaAbsentHomesRootServesEmptyReport(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-homes")
	p := New(Deps{AccountHomesDir: missing})
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/account-quota", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an absent homes root is a normal state, not a server error", rec.Code)
	}
	body := rec.Body.String()
	// Explicit [] rather than null: the SPA decoder requires arrays, and a
	// null here would surface in the browser as a decode error instead of an
	// empty tab.
	if !strings.Contains(body, `"accounts":[]`) {
		t.Errorf("accounts must serialize as []: %s", body)
	}
	if !strings.Contains(body, `"pool":[]`) {
		t.Errorf("pool must serialize as []: %s", body)
	}
	var got accountQuotaReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Rotation.Available {
		t.Error("rotation.available must be false when rotation.json is absent")
	}
	if got.Rotation.Reason == "" {
		t.Error("rotation.reason must say why the pool is unknown; a silent empty pool reads as 'no accounts rotate'")
	}
}

// The three presence states carry different operational meaning and must stay
// distinct on the wire. Collapsing "never observed" into "no limits" would
// claim the collector ran and found nothing, which is a different fact from
// "no session has run on that account since collection was wired".
func TestAccountQuotaKeepsThreeObservationStatesDistinct(t *testing.T) {
	root := quotaHomes(t, []string{"0", "1", "2"}, map[string]string{
		// account0: no file at all -> never observed.
		"1": `{"account":"1","observed_at":1786224387,"session_id":null,"rate_limits":null}`,
		"2": `{"account":"2","observed_at":1786224546,"session_id":"abc-123",` +
			`"rate_limits":{"five_hour":{"used_percentage":76,"resets_at":1786229400},` +
			`"seven_day":{"used_percentage":36,"resets_at":1786356000}}}`,
	}, `{"pool":["2"]}`)

	report := getAccountQuota(t, root, nil)

	never := findAccount(t, report, "0")
	if never.Observation.State != observationNeverObserved {
		t.Errorf("account0 state = %q, want %q", never.Observation.State, observationNeverObserved)
	}
	if never.Observation.ObservedAt != nil {
		t.Errorf("account0 observed_at = %v, want null", *never.Observation.ObservedAt)
	}

	noLimits := findAccount(t, report, "1")
	if noLimits.Observation.State != observationNoLimits {
		t.Errorf("account1 state = %q, want %q", noLimits.Observation.State, observationNoLimits)
	}
	if noLimits.Observation.ObservedAt == nil || *noLimits.Observation.ObservedAt != 1786224387 {
		t.Errorf("account1 observed_at = %v, want 1786224387 kept even with null rate_limits", noLimits.Observation.ObservedAt)
	}
	if noLimits.Observation.FiveHour != nil || noLimits.Observation.SevenDay != nil {
		t.Error("account1 must carry no windows: zeros would render as a healthy empty account")
	}

	observed := findAccount(t, report, "2")
	if observed.Observation.State != observationObserved {
		t.Errorf("account2 state = %q, want %q", observed.Observation.State, observationObserved)
	}
	if observed.Observation.SessionID != "abc-123" {
		t.Errorf("account2 session_id = %q, want abc-123", observed.Observation.SessionID)
	}
	// Verbatim passthrough. The server must not round, rescale, or age these:
	// the browser applies the staleness rule against its own clock.
	if observed.Observation.FiveHour == nil || observed.Observation.FiveHour.UsedPercentage != 76 ||
		observed.Observation.FiveHour.ResetsAt != 1786229400 {
		t.Errorf("account2 five_hour = %+v, want 76%% resetting at 1786229400 verbatim", observed.Observation.FiveHour)
	}
	if observed.Observation.SevenDay == nil || observed.Observation.SevenDay.UsedPercentage != 36 ||
		observed.Observation.SevenDay.ResetsAt != 1786356000 {
		t.Errorf("account2 seven_day = %+v, want 36%% resetting at 1786356000 verbatim", observed.Observation.SevenDay)
	}
}

// A half-written or corrupt file for one account must not hide the others, and
// must not read as "never observed" — that would claim no session has run
// there, when in fact one did and the record cannot be trusted.
func TestAccountQuotaUnreadableFileDoesNotPoisonSiblings(t *testing.T) {
	root := quotaHomes(t, []string{"0", "1"}, map[string]string{
		"0": `{"account":"0","observed_at":178622`, // torn write
		"1": `{"account":"1","observed_at":1786224387,"session_id":"s","rate_limits":null}`,
	}, `{"pool":[]}`)

	report := getAccountQuota(t, root, nil)

	broken := findAccount(t, report, "0")
	if broken.Observation.State != observationUnreadable {
		t.Errorf("account0 state = %q, want %q — a corrupt record is not an absent one",
			broken.Observation.State, observationUnreadable)
	}
	if broken.Observation.Reason == "" {
		t.Error("account0 reason must name the parse failure; a bare state gives the operator nothing to act on")
	}
	if sibling := findAccount(t, report, "1"); sibling.Observation.State != observationNoLimits {
		t.Errorf("account1 state = %q, want %q: one bad file must not degrade its siblings",
			sibling.Observation.State, observationNoLimits)
	}
}

// A record that EXISTS but cannot be opened is not an absent one either. The
// suite told absent from unparseable but not absent from unreadable, so
// collapsing the non-ENOENT read error into never_observed passed every test —
// and the report would then claim no session had ever run on an account whose
// record is merely unopenable. Reachable in production: a half-broken writer
// leaves quota/<id>.json as a directory, or the file ends up mode 000.
func TestAccountQuotaUnopenableFileIsUnreadableNotNeverObserved(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, nil, "")
	// A directory where a file belongs. Chosen over chmod 000 because a test
	// running as root would read a 000 file happily and the case would go
	// silently untested.
	if err := os.MkdirAll(filepath.Join(root, "quota", "0.json"), 0o755); err != nil {
		t.Fatalf("mkdir quota/0.json: %v", err)
	}

	entry := findAccount(t, getAccountQuota(t, root, nil), "0")
	if entry.Observation.State != observationUnreadable {
		t.Errorf("state = %q, want %q: a record that exists and cannot be opened is not one that was never written",
			entry.Observation.State, observationUnreadable)
	}
	if entry.Observation.Reason == "" {
		t.Error("reason must name the read failure; never_observed would send the operator to the collector instead of the file")
	}
}

// A zeroed observed_at is the shape a collector writing an uninitialized struct
// produces. It has to be caught by value and not merely by presence: decoded
// into an int64 it is indistinguishable from a real timestamp, and the browser
// would age it against the epoch and render the level "as of 56y ago".
func TestAccountQuotaZeroObservedAtIsUnreadable(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, map[string]string{
		"0": `{"account":"0","observed_at":0,"session_id":"s",` +
			`"rate_limits":{"five_hour":{"used_percentage":9,"resets_at":1786229400}}}`,
	}, "")

	entry := findAccount(t, getAccountQuota(t, root, nil), "0")
	if entry.Observation.State != observationUnreadable {
		t.Errorf("state = %q, want %q when observed_at is zero", entry.Observation.State, observationUnreadable)
	}
	if entry.Observation.FiveHour != nil {
		t.Error("a record that cannot be aged must carry no windows")
	}
}

// The filename is the identity the dashboard looked the record up by. A record
// whose own account field disagrees cannot be attributed to either candidate,
// and reporting one account's levels under another's name is precisely the
// misread this tab exists to prevent — so the disagreement is surfaced, not
// resolved by preferring one side.
func TestAccountQuotaMismatchedAccountFieldIsUnreadable(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, map[string]string{
		"0": `{"account":"3","observed_at":1786224387,"session_id":"s","rate_limits":null}`,
	}, "")

	entry := findAccount(t, getAccountQuota(t, root, nil), "0")
	if entry.Observation.State != observationUnreadable {
		t.Errorf("state = %q, want %q when the record's account field is not its filename",
			entry.Observation.State, observationUnreadable)
	}
	if !strings.Contains(entry.Observation.Reason, "3") {
		t.Errorf("reason = %q, must name the conflicting id so the operator can find the bad writer", entry.Observation.Reason)
	}
}

// Every percentage is only interpretable next to the moment it was taken. A
// record with no observed_at cannot be aged, so it must not be rendered as a
// current reading — the staleness rule downstream has nothing to apply.
func TestAccountQuotaRecordWithoutObservedAtIsUnreadable(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, map[string]string{
		"0": `{"account":"0","session_id":"s","rate_limits":{"five_hour":{"used_percentage":9,"resets_at":1786229400}}}`,
	}, "")

	entry := findAccount(t, getAccountQuota(t, root, nil), "0")
	if entry.Observation.State != observationUnreadable {
		t.Errorf("state = %q, want %q when observed_at is missing", entry.Observation.State, observationUnreadable)
	}
	if entry.Observation.FiveHour != nil {
		t.Error("an unageable record must carry no windows: the browser would render it as current")
	}
}

// The inventory is a union of four independent sources, because no single one
// is complete: an in-pool account may never have been observed, and an
// observed account may have been dropped from the pool.
func TestAccountQuotaInventoryUnionsDirsPoolFilesAndLabels(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, map[string]string{
		// account9 has a quota file but no directory and no pool membership.
		"9": `{"account":"9","observed_at":1,"session_id":null,"rate_limits":null}`,
	}, `{"pool":["7"]}`) // account7 is pooled with neither dir nor file

	report := getAccountQuota(t, root, map[string]string{"8": "labeled only"})

	for _, id := range []string{"0", "7", "8", "9"} {
		entry := findAccount(t, report, id)
		if id == "7" && !entry.InPool {
			t.Error("account7 must be marked in_pool: it is in rotation.json's pool with no quota file")
		}
		if id == "9" && entry.InPool {
			t.Error("account9 must not be marked in_pool: it has a quota file but is absent from the pool")
		}
	}
	if len(report.Accounts) != 4 {
		t.Errorf("accounts = %v, want exactly the union of dirs/pool/files/labels", accountIDsOf(report))
	}
	// Sorted so the tab's row order does not shuffle between polls — readdir
	// order is not stable and neither is JSON map iteration.
	if got := accountIDsOf(report); got[0] != "0" || got[1] != "7" || got[2] != "8" || got[3] != "9" {
		t.Errorf("accounts = %v, want ascending id order", got)
	}
}

// Pool membership, and only pool membership, is derived from the file. The
// operator's group names arrive as configuration so no role name is compiled
// into Go and a moved pin needs no rebuild.
func TestAccountQuotaGroupsComeFromRotationAndConfigNotCode(t *testing.T) {
	root := quotaHomes(t, []string{"0", "1", "2", "3"}, nil, `{"pool":["2","3"]}`)

	report := getAccountQuota(t, root, map[string]string{"1": "orchestrator pin"})

	if len(report.Pool) != 2 || report.Pool[0] != "2" || report.Pool[1] != "3" {
		t.Errorf("pool = %v, want [2 3] in rotation.json order", report.Pool)
	}
	if !report.Rotation.Available {
		t.Errorf("rotation.available = false (%s), want true with a readable file", report.Rotation.Reason)
	}
	for _, id := range []string{"0", "1"} {
		if findAccount(t, report, id).InPool {
			t.Errorf("account%s must not be in_pool: it is absent from rotation.json's pool", id)
		}
	}
	if label := findAccount(t, report, "1").Label; label != "orchestrator pin" {
		t.Errorf("account1 label = %q, want the configured label", label)
	}
	if label := findAccount(t, report, "2").Label; label != "" {
		t.Errorf("account2 label = %q, want empty: unlabeled accounts must not inherit one", label)
	}
}

// rotation.json's suspects map is keyed by SESSION name, not by account
// (account-cap-sweep.py sets suspects[session] = {sightings, last_seen}), so
// attributing a suspect to an account requires the bindings join. Reading it
// as account-keyed would compile, run, and report zero suspects forever.
func TestAccountQuotaAttributesSuspectSessionsThroughBindings(t *testing.T) {
	root := quotaHomes(t, []string{"2", "3"}, nil, `{
		"pool": ["2","3"],
		"last_used": {"2": 1786223629.6242034, "3": 1786224440.83499},
		"cooldown_until": {"3": 1786230000.5},
		"suspects": {
			"worker-a": {"sightings": 2, "last_seen": 1786224000},
			"worker-gone": {"sightings": 1, "last_seen": 1786224000}
		},
		"bindings": {
			"worker-a": {"account": "3", "bound_at": 1786223000.0, "all_capped": false},
			"worker-b": {"account": "3", "bound_at": 1786223100.0, "all_capped": false},
			"worker-c": {"account": "2", "bound_at": 1786223200.0, "all_capped": false}
		}
	}`)

	report := getAccountQuota(t, root, nil)

	three := findAccount(t, report, "3")
	if three.SuspectSessions != 1 {
		t.Errorf("account3 suspect_sessions = %d, want 1 via the bindings join (worker-a)", three.SuspectSessions)
	}
	if three.BoundSessions != 2 {
		t.Errorf("account3 bound_sessions = %d, want 2", three.BoundSessions)
	}
	// Truncated to whole seconds: the wire carries epoch seconds, and the
	// sub-second precision claude-pool writes is noise at this resolution.
	if three.LastUsedAt == nil || *three.LastUsedAt != 1786224440 {
		t.Errorf("account3 last_used_at = %v, want 1786224440", three.LastUsedAt)
	}
	if three.CooldownUntil == nil || *three.CooldownUntil != 1786230000 {
		t.Errorf("account3 cooldown_until = %v, want 1786230000", three.CooldownUntil)
	}

	two := findAccount(t, report, "2")
	if two.SuspectSessions != 0 {
		t.Errorf("account2 suspect_sessions = %d, want 0", two.SuspectSessions)
	}
	if two.CooldownUntil != nil {
		t.Errorf("account2 cooldown_until = %v, want null when not cooling down", *two.CooldownUntil)
	}
	if two.BoundSessions != 1 {
		t.Errorf("account2 bound_sessions = %d, want 1", two.BoundSessions)
	}

	// worker-gone is a suspect with no binding, so no account owns it. It is
	// reported rather than dropped: a suspect nobody can attribute is exactly
	// the state an operator needs to see.
	if report.UnattributedSuspects != 1 {
		t.Errorf("unattributed_suspects = %d, want 1 (worker-gone has no binding)", report.UnattributedSuspects)
	}
}

// A malformed rotation.json must not hide the quota readings. The two files
// have independent writers, so one being broken says nothing about the other.
func TestAccountQuotaUnreadableRotationKeepsQuotaVisible(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, map[string]string{
		"0": `{"account":"0","observed_at":1786224387,"session_id":"s","rate_limits":null}`,
	}, `{"pool": [`)

	report := getAccountQuota(t, root, nil)

	if report.Rotation.Available {
		t.Error("rotation.available must be false when rotation.json will not parse")
	}
	if report.Rotation.Reason == "" {
		t.Error("rotation.reason must name the parse failure")
	}
	if len(report.Pool) != 0 {
		t.Errorf("pool = %v, want empty: an unparseable file yields no membership claim", report.Pool)
	}
	if entry := findAccount(t, report, "0"); entry.Observation.State != observationNoLimits {
		t.Errorf("account0 state = %q, want %q despite the broken rotation file",
			entry.Observation.State, observationNoLimits)
	}
}

// Account ids reach the filesystem as a path segment, and two of the four
// inventory sources (pool entries, label keys) are attacker-shaped input from
// outside the repo. Anything that is not a plain id is dropped rather than
// sanitized: a rewritten id would silently report one account's numbers under
// another's name.
func TestAccountQuotaDropsUnsafeAccountIDs(t *testing.T) {
	root := quotaHomes(t, []string{"0"}, nil,
		`{"pool":["2","../../etc/passwd","","with/slash","ok-2"]}`)

	report := getAccountQuota(t, root, map[string]string{"../escape": "nope"})

	for _, entry := range report.Accounts {
		switch entry.Account {
		case "0", "2", "ok-2":
		default:
			t.Errorf("unsafe account id %q survived into the report", entry.Account)
		}
	}
	if len(report.Pool) != 2 {
		t.Errorf("pool = %v, want only the two well-formed ids", report.Pool)
	}
}

// With no AccountHomesDir configured the plane reads the operator's own
// ~/.claude-homes, which is where the collector writes. Wiring this wrong
// yields a permanently empty tab on the machine that actually has data.
func TestAccountQuotaDefaultsToHomeClaudeHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude-homes", "account4"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	p := New(Deps{})
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/account-quota", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got accountQuotaReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Account != "4" {
		t.Errorf("accounts = %v, want [4] discovered under $HOME/.claude-homes", accountIDsOf(got))
	}
}

// Row order is what an operator scans, and lexicographic order files
// account10 between account1 and account2. Non-numeric ids are permitted by
// safeAccountID, so they must have a defined place rather than interleaving
// with the numbers.
func TestAccountQuotaOrdersIDsNumericallyThenLexically(t *testing.T) {
	root := quotaHomes(t, []string{"0", "2", "10", "spare"}, nil, "")

	got := accountIDsOf(getAccountQuota(t, root, nil))
	want := []string{"0", "2", "10", "spare"}
	if len(got) != len(want) {
		t.Fatalf("accounts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("accounts = %v, want %v", got, want)
			break
		}
	}
}

func TestParseAccountLabels(t *testing.T) {
	got := ParseAccountLabels("0=operator interactive, 1 = orchestrator pin ,,bogus,=empty,2=")
	want := map[string]string{"0": "operator interactive", "1": "orchestrator pin"}
	if len(got) != len(want) {
		t.Fatalf("ParseAccountLabels = %v, want %v", got, want)
	}
	for id, label := range want {
		if got[id] != label {
			t.Errorf("label[%q] = %q, want %q", id, got[id], label)
		}
	}
	if ParseAccountLabels("") != nil {
		t.Error("empty input must yield nil, not an empty map")
	}
}
