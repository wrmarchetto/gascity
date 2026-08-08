package dashboardbff

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hand-typed Go /api structs are coupled to the SPA's response decoders in
// frontend/src/api/client.ts only by "// must match" comments. These tests are
// the cross-boundary guard: they assert each endpoint's JSON satisfies exactly
// the field+type contract the matching decoder enforces, so a Go struct that
// drifts from the TS decoder fails here instead of at runtime in the browser.
// (The complementary half — running these shapes through the real TS decoders —
// is a frontend Vitest follow-up.)

func wireGet(t *testing.T, p *Plane, path string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200 (body %s)", path, rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
	return m
}

func mustString(t *testing.T, m map[string]any, field string) {
	t.Helper()
	if _, ok := m[field].(string); !ok {
		t.Errorf("field %q must be a string, got %T", field, m[field])
	}
}

func mustBool(t *testing.T, m map[string]any, field string) {
	t.Helper()
	if _, ok := m[field].(bool); !ok {
		t.Errorf("field %q must be a bool, got %T", field, m[field])
	}
}

func mustArray(t *testing.T, m map[string]any, field string) {
	t.Helper()
	if _, ok := m[field].([]any); !ok {
		t.Errorf("field %q must be an array (never null), got %T", field, m[field])
	}
}

func mustObject(t *testing.T, m map[string]any, field string) {
	t.Helper()
	if _, ok := m[field].(map[string]any); !ok {
		t.Errorf("field %q must be an object, got %T", field, m[field])
	}
}

func mustStringOrNull(t *testing.T, m map[string]any, field string) {
	t.Helper()
	if v, present := m[field]; !present {
		t.Errorf("field %q must be present (string|null)", field)
	} else if _, isStr := v.(string); v != nil && !isStr {
		t.Errorf("field %q must be string or null, got %T", field, v)
	}
}

func contractPlane() *Plane {
	return New(Deps{Resolver: mapResolver{"alpha": "/srv/alpha"}})
}

func TestWireContractConfig(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/city/alpha/config")
	for _, f := range []string{"cityName", "cityRoot", "operatorAlias", "operatorWireAlias", "decisionLabel"} {
		mustString(t, m, f)
	}
	mustBool(t, m, "useFixtures")
	mustBool(t, m, "readOnly")
	mustArray(t, m, "enabledModules") // explicit [] for core-only, never null
	mustStringOrNull(t, m, "defaultView")
}

func TestWireContractHealth(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/health")
	mustBool(t, m, "ok")
	mustString(t, m, "ts")
}

func TestWireContractSystemHealth(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/health/system")
	mustObject(t, m, "admin")
	mustObject(t, m, "host")
	admin := m["admin"].(map[string]any)
	host := m["host"].(map[string]any)
	for _, field := range []string{"rss"} {
		mustObject(t, admin, field)
		mustString(t, admin[field].(map[string]any), "status")
	}
	for _, field := range []string{"load", "memory", "uptime"} {
		mustObject(t, host, field)
		mustString(t, host[field].(map[string]any), "status")
	}
}

func TestWireContractLocalTools(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/health/local-tools")
	for _, tool := range []string{"dolt", "beads", "gc"} {
		mustObject(t, m, tool)
		mustString(t, m[tool].(map[string]any), "status")
	}
}

func TestWireContractGitCommits(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/git/commits?view=recent-main")
	mustString(t, m, "view")
	mustArray(t, m, "items")
}

func TestWireContractBuilds(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/builds")
	mustArray(t, m, "items")
	mustStringOrNull(t, m, "source")
	mustBool(t, m, "failed_marker")
}

func TestWireContractSupervisorStatus(t *testing.T) {
	// No Start()/SupervisorBaseURL -> deterministic not-sampled-yet shape.
	m := wireGet(t, contractPlane(), "/api/city/alpha/supervisor-status")
	mustBool(t, m, "available")
	if _, present := m["status"]; !present {
		t.Error("supervisor-status must always carry a status field (object|null)")
	}
	if m["available"] == false {
		mustString(t, m, "reason")
	}
}

func TestWireContractDoltTrend(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/city/alpha/dolt-noms/trend")
	mustBool(t, m, "available")
	mustArray(t, m, "samples")
}

func TestWireContractRigStoreHealth(t *testing.T) {
	m := wireGet(t, contractPlane(), "/api/city/alpha/rig-store-health")
	mustBool(t, m, "available")
	mustArray(t, m, "rigs")
}

// TestWireContractRunDetailProgressTerminal guards the Go-derived
// progress.terminal flag on the run-detail wire: the SPA reads it (in place of
// the retired isTerminalProgress client fold) to drive ambient-event
// suppression, so the field must always be present and a bool.
func TestWireContractRunDetailProgressTerminal(t *testing.T) {
	dir := t.TempDir()
	writeEventLog(t, filepath.Join(dir, ".gc", "events.jsonl"),
		runDetailRootEvent(),
		runDetailStepEvent(2, "run1.1", "run1", "preflight", "in_progress"),
	)
	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": dir}}})
	p.Start(t.Context())
	defer p.Stop()

	m := wireGet(t, p, "/api/city/alpha/runs/run1/detail")
	progress, ok := m["progress"].(map[string]any)
	if !ok {
		t.Fatalf("progress must be an object, got %T", m["progress"])
	}
	mustBool(t, progress, "terminal")
}

// TestWireContractRunDetailStreamFrame is the cross-boundary guard for the P4
// SSE detail stream: the pushed frame body is the SAME FormulaRunDetail struct
// the GET serves, so a captured `data:` frame must satisfy the same field+type
// contract the TS decodeFormulaRunDetail enforces. It parses the first SSE frame
// off a real streaming connection and asserts the load-bearing fields the SPA
// hard-derefs, so a Go struct that drifts from the TS decoder fails here.
func TestWireContractRunDetailStreamFrame(t *testing.T) {
	dir := t.TempDir()
	writeEventLog(t, filepath.Join(dir, ".gc", "events.jsonl"),
		runDetailRootEvent(),
		runDetailStepEvent(2, "run1.1", "run1", "preflight", "in_progress"),
	)
	p := New(Deps{Resolver: fakeResolver{paths: map[string]string{"alpha": dir}}})
	p.Start(t.Context())
	defer p.Stop()

	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	resp, sc, closeStream := startDetailStream(t, srv)
	defer closeStream()
	_ = resp

	frame, ok := readSSEFrame(t, sc)
	if !ok {
		t.Fatal("no SSE frame to assert the wire contract against")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(frame.data), &m); err != nil {
		t.Fatalf("decode SSE frame body: %v; data=%q", err, frame.data)
	}
	// The same fields decodeFormulaRunDetail requires on the GET body.
	mustString(t, m, "runId")
	mustObject(t, m, "formula")
	mustObject(t, m, "formulaDetail")
	mustObject(t, m, "executionPath")
	mustObject(t, m, "snapshotEventSeq")
	mustObject(t, m, "completeness")
	mustArray(t, m, "stages")
	mustArray(t, m, "nodes")
	mustArray(t, m, "edges")
	mustArray(t, m, "lanes")
	progress, isObj := m["progress"].(map[string]any)
	if !isObj {
		t.Fatalf("progress must be an object, got %T", m["progress"])
	}
	mustObject(t, progress, "statusCounts")
	mustBool(t, progress, "terminal")
}

// TestSanitizeTerminalOutputStripsCSIAndControls is the regression guard for the
// broadened CSI grammar: SGR, intermediate-byte, and private CSI sequences are
// all removed whole, leaving no introducer/param/intermediate residue, and no
// ESC/control or bidi bytes survive.
func TestSanitizeTerminalOutputStripsCSIAndControls(t *testing.T) {
	in := "a\x1b[31mred\x1b[0m b\x1b[1$rc\x1b[>0cd\x07\u202ee"
	got := sanitizeTerminalOutput(in)
	for _, bad := range []string{"\x1b", "[31m", "[1$r", "[>0c", "$", ">", "\x07", "\u202e"} {
		if strings.Contains(got, bad) {
			t.Errorf("sanitized output retains %q: %q", bad, got)
		}
	}
	if got != "ared bcde" { // the literal space in " b" survives; only escapes/controls are stripped
		t.Errorf("sanitized = %q, want %q", got, "ared bcde")
	}
}

// TestWireContractAccountQuota pins the /api/account-quota body against what
// decodeAccountQuota in frontend/src/api/client.ts requires. The endpoint's
// whole point is that absent inputs degrade in place, so the contract is
// asserted on an EMPTY homes root: the shape a machine with no collector wired
// serves is the one most likely to reach a browser unnoticed.
func TestWireContractAccountQuota(t *testing.T) {
	p := New(Deps{AccountHomesDir: t.TempDir()})
	m := wireGet(t, p, "/api/account-quota")
	mustArray(t, m, "accounts")
	mustArray(t, m, "pool")
	mustObject(t, m, "rotation")
	mustString(t, m, "homes_dir")
	rotation, _ := m["rotation"].(map[string]any)
	mustBool(t, rotation, "available")
	mustString(t, rotation, "reason")
	if _, ok := m["unattributed_suspects"].(float64); !ok {
		t.Errorf("unattributed_suspects must be a number, got %T", m["unattributed_suspects"])
	}
}

// TestWireContractObservationStateSpellings pins all four state spellings as
// literals. Three consumers key on these exact strings and none of them can be
// reached from Go: the AccountObservationState union in
// shared/src/dashboard-quota.ts, the closed-set check in
// frontend/src/api/client.ts (which rejects an unknown state outright), and the
// OBSERVATION_TONE / OBSERVATION_LABEL lookup tables in routes/Accounts.tsx.
// Writing the literals here rather than comparing constants to themselves is
// the point — a test that reads the same constant the handler writes cannot
// notice the spelling changing under both of them.
func TestWireContractObservationStateSpellings(t *testing.T) {
	for _, tc := range []struct {
		got  observationState
		want string
	}{
		{observationNeverObserved, "never_observed"},
		{observationNoLimits, "no_limits"},
		{observationObserved, "observed"},
		{observationUnreadable, "unreadable"},
	} {
		if string(tc.got) != tc.want {
			t.Errorf("observation state = %q, want %q — the SPA's state union, decode allowlist and badge tables all key on this literal",
				tc.got, tc.want)
		}
	}
}

// TestWireContractAccountQuotaEntry pins the per-account row, including the
// nulls: the SPA tells the presence states apart by observed_at/window absence,
// so an omitted key (rather than an explicit null) would decode as undefined
// and silently take the never-observed branch for an observed account.
func TestWireContractAccountQuotaEntry(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "quota"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{"account":"7","observed_at":1786224546,"session_id":"s-1",` +
		`"rate_limits":{"five_hour":{"used_percentage":62.5,"resets_at":1786229400},` +
		`"seven_day":{"used_percentage":18,"resets_at":1786700000}}}`
	if err := os.WriteFile(filepath.Join(root, "quota", "7.json"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	m := wireGet(t, New(Deps{AccountHomesDir: root}), "/api/account-quota")
	accounts, _ := m["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %v, want exactly one row", accounts)
	}
	entry, isObj := accounts[0].(map[string]any)
	if !isObj {
		t.Fatalf("account row must be an object, got %T", accounts[0])
	}
	mustString(t, entry, "account")
	mustString(t, entry, "label")
	mustBool(t, entry, "in_pool")
	mustObject(t, entry, "observation")
	for _, field := range []string{"bound_sessions", "suspect_sessions"} {
		if _, ok := entry[field].(float64); !ok {
			t.Errorf("%s must be a number, got %T", field, entry[field])
		}
	}
	for _, field := range []string{"last_used_at", "cooldown_until"} {
		if value, present := entry[field]; !present {
			t.Errorf("field %q must be present (number|null)", field)
		} else if _, isNum := value.(float64); value != nil && !isNum {
			t.Errorf("field %q must be number or null, got %T", field, value)
		}
	}

	observation, _ := entry["observation"].(map[string]any)
	mustString(t, observation, "state")
	// The literal, not the constant. Every other test compares a decoded state
	// against the same observationState constant the handler wrote, so garbling
	// all four spellings passes the Go suite entirely — and the SPA keys its
	// state union, tone table and badge labels on these exact strings, so the
	// tab would ship an undefined tone and an empty badge on every row. This
	// assertion is the only place the two languages meet.
	if got := observation["state"]; got != "observed" {
		t.Errorf("observation.state = %v, want the literal %q that dashboard-quota.ts's union declares", got, "observed")
	}
	mustString(t, observation, "session_id")
	mustString(t, observation, "reason")
	if _, ok := observation["observed_at"].(float64); !ok {
		t.Errorf("observed_at must be a number on an observed record, got %T", observation["observed_at"])
	}
	for _, window := range []string{"five_hour", "seven_day"} {
		mustObject(t, observation, window)
		values, _ := observation[window].(map[string]any)
		for _, field := range []string{"used_percentage", "resets_at"} {
			if _, ok := values[field].(float64); !ok {
				t.Errorf("%s.%s must be a number, got %T", window, field, values[field])
			}
		}
	}
}
