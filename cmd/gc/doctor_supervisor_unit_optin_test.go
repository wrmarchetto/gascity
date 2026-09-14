package main

// Scope: the `supervisor-unit-optin-dropped` doctor check -- the gate that
// fails when a key named in ${GC_HOME}/secrets.env's GC_SUPERVISOR_ENV is not
// reaching the supervisor's service children.
//
// Why this suite exists: ci-cblj0v, and specifically the shape of it a
// file-only gate cannot see. `gc supervisor install` writes a correct unit and
// every service stays dead until someone restarts the supervisor; a check
// satisfied by the unit alone is green for that entire window, which is the
// counts-what-is-PRESENT-rather-than-CONSUMED failure the root CLAUDE.md
// names. TestSupervisorUnitOptInCatchesTheRestartWindow is the test that
// exists because of it, and it was written after an adversarial review of an
// earlier file-only version of this check.
//
// The fixture never names a service or a bridge key. A gate listing
// BRIDGE_SLACK_APP_TOKEN would be a second copy of city.toml's required-key
// block, would pass a city whose next capability is opted in under some other
// name, and would put a role name in Go. The keys below are deliberately
// unrelated to anything this tree knows about.
//
// Delegated elsewhere: which tiers may supply a VALUE for an opted-in key is
// pinned in cmd_supervisor_test.go; the unit-versus-declaration comparison is
// pinned in doctor_supervisor_unit_env_test.go.
//
// Not represented here: launchd, and the shell's own GC_SUPERVISOR_ENV -- see
// the absence notes on supervisorUnitOptInCheck. Also not represented: a real
// /proc. The environ is a fixture file, so this suite cannot catch a change to
// the NUL framing /proc actually uses; TestSupervisorUnitOptInParsesRealProc
// reads this test process's own environ to cover that one byte-level claim.
//
// Run: go test ./cmd/gc/ -run SupervisorUnitOptIn -count=1

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
)

// optInFixture is one arrangement of the three artifacts the check reads.
type optInFixture struct {
	optIn      []string // keys named in the secrets file's GC_SUPERVISOR_ENV
	inUnit     []string // keys the installed unit carries
	inProc     []string // keys the running supervisor carries
	noProc     bool     // no running supervisor to measure
	valuesFor  []string // keys the secrets file has a value for
	unitAbsent bool     // no gc-installed unit on disk
}

// writeOptInFixture materializes one arrangement and returns the built check.
// Every test varies one axis of the fixture, so a red run is attributable to
// that axis and nothing else.
func writeOptInFixture(t *testing.T, f optInFixture) *supervisorUnitOptInCheck {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	unitPath := filepath.Join(homeDir, "gascity-supervisor.service")
	if !f.unitAbsent {
		data := supervisorServiceEnvBaselineData()
		for _, key := range f.inUnit {
			data.ExtraEnv = append(data.ExtraEnv, supervisorServiceEnvVar{Name: key, Value: "value-of-" + key})
		}
		unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
		if err != nil {
			t.Fatalf("renderSupervisorTemplate: %v", err)
		}
		if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
			t.Fatalf("writing unit: %v", err)
		}
	}

	var secrets strings.Builder
	if len(f.optIn) > 0 {
		secrets.WriteString(supervisorServiceOptInEnv + "=" + strings.Join(f.optIn, ",") + "\n")
	}
	for _, key := range f.valuesFor {
		secrets.WriteString(key + "=value-of-" + key + "\n")
	}
	writeSupervisorSecretsEnvFile(t, secrets.String())

	pid := 0
	procRoot := ""
	if !f.noProc {
		pid = 4242
		procRoot = filepath.Join(homeDir, "proc")
		procDir := filepath.Join(procRoot, strconv.Itoa(pid))
		if err := os.MkdirAll(procDir, 0o700); err != nil {
			t.Fatalf("creating proc fixture: %v", err)
		}
		// Same NUL framing /proc uses, trailing separator included.
		var environ strings.Builder
		for _, key := range append([]string{"HOME", "PATH"}, f.inProc...) {
			environ.WriteString(key + "=value-of-" + key + "\x00")
		}
		if err := os.WriteFile(filepath.Join(procDir, "environ"), []byte(environ.String()), 0o600); err != nil {
			t.Fatalf("writing environ fixture: %v", err)
		}
	}
	return newSupervisorUnitOptInCheck(unitPath, supervisorSecretsEnvFilePath(), pid, procRoot)
}

// TestSupervisorUnitOptInSatisfiedSupervisorPasses is the control. Without it
// a red from any test below could just as well mean the fixture never agreed.
func TestSupervisorUnitOptInSatisfiedSupervisorPasses(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_CHANNEL_ID"}
	got := writeOptInFixture(t, optInFixture{
		optIn: keys, inUnit: keys, inProc: keys, valuesFor: keys,
	}).Run(nil)
	if got.Status != doctor.StatusOK {
		t.Fatalf("supervisor carrying every opted-in key reported %v: %s %v",
			got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInCatchesTheRestartWindow is the whole reason this
// check reads a process. The unit is CORRECT and the running supervisor still
// lacks the keys, which is the state `gc supervisor install` leaves behind
// until someone restarts -- and the state the ci-cblj0v outage sat in. A gate
// that read only the unit reports OK here, so this test is what separates the
// two designs.
func TestSupervisorUnitOptInCatchesTheRestartWindow(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_CHANNEL_ID"}
	got := writeOptInFixture(t, optInFixture{
		optIn: keys, inUnit: keys, inProc: nil, valuesFor: keys,
	}).Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("a correct unit with a stale supervisor reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
	details := strings.Join(got.Details, "\n")
	for _, key := range keys {
		if !strings.Contains(details, key) {
			t.Fatalf("details do not name %s: %v", key, got.Details)
		}
	}
	// The remedy must be a restart. Telling the operator to reinstall a unit
	// that is already right spends a warm refresh and leaves the fault.
	if !strings.Contains(details, "restart") {
		t.Fatalf("details do not name the restart remedy: %v", got.Details)
	}
	if strings.Contains(details, "reinstall") {
		t.Fatalf("details ask for a reinstall the unit does not need: %v", got.Details)
	}
}

// TestSupervisorUnitOptInCatchesDroppedKey is the ci-cblj0v reproduction
// proper: the secrets file opts three keys in and neither the unit nor the
// supervisor carries any of them.
func TestSupervisorUnitOptInCatchesDroppedKey(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_BOT_TOKEN", "WIDGET_CHANNEL_ID"}
	got := writeOptInFixture(t, optInFixture{optIn: keys, valuesFor: keys}).Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("supervisor missing every opted-in key reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
	details := strings.Join(got.Details, "\n")
	for _, key := range keys {
		if !strings.Contains(details, key) {
			t.Fatalf("details do not name the dropped key %s: %v", key, got.Details)
		}
	}
	if !strings.Contains(details, "reinstall") {
		t.Fatalf("details do not name the reinstall remedy: %v", got.Details)
	}
}

// TestSupervisorUnitOptInCatchesAPartialSupervisor requires red when some
// opted-in keys are present and others are not. A check asserting only "the
// supervisor has some opted-in keys" would pass this, and it is the state a
// regeneration from a half-exported shell actually produces -- ci-kqmb5p was
// two keys of three.
func TestSupervisorUnitOptInCatchesAPartialSupervisor(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_CHANNEL_ID"}
	got := writeOptInFixture(t, optInFixture{
		optIn: keys, inUnit: keys, inProc: keys[:1], valuesFor: keys,
	}).Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("partially satisfied supervisor reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
	details := strings.Join(got.Details, "\n")
	if strings.Contains(details, keys[0]) {
		t.Fatalf("details name the key the supervisor DOES carry: %v", got.Details)
	}
	if !strings.Contains(details, keys[1]) {
		t.Fatalf("details do not name the missing key %s: %v", keys[1], got.Details)
	}
}

// TestSupervisorUnitOptInDistinguishesMissingValue pins the two remedies
// apart in the no-process case. A key with a value on file is a stale unit a
// reinstall fixes; a key with no value anywhere survives a reinstall
// unchanged, and an operator told to reinstall would watch the finding return.
func TestSupervisorUnitOptInDistinguishesMissingValue(t *testing.T) {
	got := writeOptInFixture(t, optInFixture{
		optIn:     []string{"WIDGET_HAS_VALUE", "WIDGET_NO_VALUE"},
		valuesFor: []string{"WIDGET_HAS_VALUE"},
		noProc:    true,
	}).Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("reported %v, want error: %s %v", got.Status, got.Message, got.Details)
	}
	details := strings.Join(got.Details, "\n")
	if !strings.Contains(details, "WIDGET_HAS_VALUE: opted in with a value on file") {
		t.Fatalf("details do not distinguish the on-file key: %v", got.Details)
	}
	if !strings.Contains(details, "WIDGET_NO_VALUE: opted in, but no value on file") {
		t.Fatalf("details do not distinguish the valueless key: %v", got.Details)
	}
}

// TestSupervisorUnitOptInSaysWhenNothingWasMeasured pins the honest green. A
// satisfied unit with no supervisor running is not the same evidence as a
// satisfied supervisor, and a message that did not say so would be read as
// the stronger claim by whoever pastes it into a bead.
func TestSupervisorUnitOptInSaysWhenNothingWasMeasured(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN"}
	got := writeOptInFixture(t, optInFixture{
		optIn: keys, inUnit: keys, valuesFor: keys, noProc: true,
	}).Run(nil)
	if got.Status != doctor.StatusOK {
		t.Fatalf("satisfied unit with no supervisor reported %v: %s %v",
			got.Status, got.Message, got.Details)
	}
	if !strings.Contains(got.Message, "not running") {
		t.Fatalf("message does not disclose that no live process was measured: %q", got.Message)
	}
}

// TestSupervisorUnitOptInCatchesAShortUnitWithNoSupervisor asserts the gate
// still fires with nothing running. Deferring to "we cannot measure" here
// would make a short unit reportable only by someone who happened to look
// while the supervisor was up.
func TestSupervisorUnitOptInCatchesAShortUnitWithNoSupervisor(t *testing.T) {
	got := writeOptInFixture(t, optInFixture{
		optIn: []string{"WIDGET_API_TOKEN"}, valuesFor: []string{"WIDGET_API_TOKEN"}, noProc: true,
	}).Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("short unit with no supervisor reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInCatchesADetachedSupervisor covers the documented
// fallback where `gc` runs the supervisor detached and installs no unit. The
// process is the only artifact, and a missing unit must not excuse a missing
// key -- reading "no unit installed" as OK is how this gate would go green on
// exactly the cities that never get one.
func TestSupervisorUnitOptInCatchesADetachedSupervisor(t *testing.T) {
	got := writeOptInFixture(t, optInFixture{
		optIn:      []string{"WIDGET_API_TOKEN"},
		valuesFor:  []string{"WIDGET_API_TOKEN"},
		unitAbsent: true,
	}).Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("detached supervisor missing an opted-in key reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInNeverNamesAValue pins the redaction across every
// source and BOTH reporting branches. Doctor output is pasted into beads, mail
// and scrollback, and the process side is the supervisor, which holds every
// credential the fleet uses.
//
// The sub-tests are not redundant: the no-process branch builds its own detail
// strings, and a 2026-09-14 mutation sweep found it uncovered -- a version
// that interpolated the secret there passed the whole suite, because the only
// redaction test at the time ran with a live process and never reached those
// lines.
func TestSupervisorUnitOptInNeverNamesAValue(t *testing.T) {
	const secret = "xapp-1-do-not-print-this"
	for _, tc := range []struct {
		name   string
		pid    int
		hasDir bool
	}{
		{name: "live supervisor", pid: 4242, hasDir: true},
		{name: "no supervisor", pid: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			homeDir := t.TempDir()
			t.Setenv("HOME", homeDir)
			t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
			unitPath := filepath.Join(homeDir, "gascity-supervisor.service")
			if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o600); err != nil {
				t.Fatalf("writing unit: %v", err)
			}
			writeSupervisorSecretsEnvFile(t,
				supervisorServiceOptInEnv+"=WIDGET_API_TOKEN\nWIDGET_API_TOKEN="+secret+"\n")

			procRoot := ""
			if tc.hasDir {
				procRoot = filepath.Join(homeDir, "proc")
				if err := os.MkdirAll(filepath.Join(procRoot, strconv.Itoa(tc.pid)), 0o700); err != nil {
					t.Fatalf("creating proc fixture: %v", err)
				}
				if err := os.WriteFile(filepath.Join(procRoot, strconv.Itoa(tc.pid), "environ"),
					[]byte("WIDGET_OTHER="+secret+"\x00"), 0o600); err != nil {
					t.Fatalf("writing environ fixture: %v", err)
				}
			}

			got := newSupervisorUnitOptInCheck(
				unitPath, supervisorSecretsEnvFilePath(), tc.pid, procRoot).Run(nil)
			if got.Status != doctor.StatusError {
				t.Fatalf("reported %v, want error: %s %v", got.Status, got.Message, got.Details)
			}
			for _, text := range append([]string{got.Message}, got.Details...) {
				if strings.Contains(text, secret) {
					t.Fatalf("check output leaked the secret value: %q", text)
				}
			}
		})
	}
}

// TestSupervisorUnitOptInParsesRealProc reads this test process's own environ
// through the same code path. The fixtures above write their own NUL framing,
// so they agree with the parser by construction and would keep agreeing if
// /proc's format assumption were wrong; this is the one assertion that touches
// the real kernel interface.
func TestSupervisorUnitOptInParsesRealProc(t *testing.T) {
	t.Setenv("WIDGET_PROBE_KEY", "probe")
	c := newSupervisorUnitOptInCheck("", "", os.Getpid(), "/proc")
	env, err := c.readSupervisorEnvironment()
	if err != nil {
		t.Skipf("this platform has no readable /proc/<pid>/environ: %v", err)
	}
	// The probe key is set AFTER exec, so it is deliberately NOT expected --
	// /proc/<pid>/environ is the exec-time snapshot and os.Setenv does not
	// rewrite it. PATH is the key that must be there.
	if _, ok := env["PATH"]; !ok {
		t.Fatalf("parsed environ carries no PATH, so the NUL framing was misread: %d key(s)", len(env))
	}
	for key, val := range env {
		if val != "" {
			t.Fatalf("readSupervisorEnvironment retained a value for %s; it must keep key names only", key)
		}
	}
}

// TestSupervisorUnitOptInFailsClosedOnAnUnparseableSecretsFile is the hole
// this check would otherwise have. A malformed secrets.env makes the generator
// drop every key INCLUDING the opt-in list, so reading "no keys opted in" off
// one and passing would be green over the exact state that produces a short
// unit.
func TestSupervisorUnitOptInFailsClosedOnAnUnparseableSecretsFile(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	unitPath := filepath.Join(homeDir, "gascity-supervisor.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o600); err != nil {
		t.Fatalf("writing unit: %v", err)
	}
	writeSupervisorSecretsEnvFile(t, "MALFORMED LINE WITHOUT EQUALS\n")
	got := newSupervisorUnitOptInCheck(unitPath, supervisorSecretsEnvFilePath(), 0, "").Run(nil)
	if got.Status != doctor.StatusError {
		t.Fatalf("unparseable secrets file reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInPassesWithNoOptIns asserts the ordinary case: a city
// that opts nothing in has nothing to compare, and must not be nagged.
func TestSupervisorUnitOptInPassesWithNoOptIns(t *testing.T) {
	got := writeOptInFixture(t, optInFixture{valuesFor: []string{"ANTHROPIC_AUTH_TOKEN"}}).Run(nil)
	if got.Status != doctor.StatusOK {
		t.Fatalf("city with no opt-ins reported %v: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInPassesWithNoSecretsFile asserts that the absence of
// secrets.env -- the normal case on most machines -- is not a finding.
func TestSupervisorUnitOptInPassesWithNoSecretsFile(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	unitPath := filepath.Join(homeDir, "gascity-supervisor.service")
	if err := os.WriteFile(unitPath, []byte("[Service]\n"), 0o600); err != nil {
		t.Fatalf("writing unit: %v", err)
	}
	got := newSupervisorUnitOptInCheck(unitPath, supervisorSecretsEnvFilePath(), 0, "").Run(nil)
	if got.Status != doctor.StatusOK {
		t.Fatalf("missing secrets file reported %v: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInDoesNotSelfRepair pins the deliberate absence of a
// fix. Both remedies are unsafe unattended -- a reinstall warm-refreshes the
// supervisor, a restart stops every managed city -- and `gc doctor --fix` runs
// from the health patrol.
func TestSupervisorUnitOptInDoesNotSelfRepair(t *testing.T) {
	if newSupervisorUnitOptInCheck("/nonexistent", "/nonexistent", 0, "").CanFix() {
		t.Fatal("supervisor-unit-optin-dropped must not offer an automatic fix")
	}
}
