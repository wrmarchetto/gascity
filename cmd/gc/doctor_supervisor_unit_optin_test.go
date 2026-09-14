package main

// Scope: the `supervisor-unit-optin-dropped` doctor check -- the gate that
// fails when a key named in ${GC_HOME}/secrets.env's GC_SUPERVISOR_ENV is
// absent from the installed systemd unit.
//
// Why this suite exists: ci-cblj0v is the failure it was written against, and
// the shape of that failure is what every test below preserves. Both halves of
// the Slack bridge crash-looped for a day because a unit regeneration emitted
// none of the three keys secrets.env opts in, and the one check that looked at
// the unit -- supervisor-unit-env-drift -- compares it against a declaration
// the SAME install writes, so it cannot go red on a thin render. The opt-in
// list is the only input here an install never rewrites, which is the entire
// reason this gate can fail where that one cannot.
//
// The fixture therefore never names a service or a bridge key. A gate that
// listed BRIDGE_SLACK_APP_TOKEN would be a hand-kept list, would pass a city
// whose next capability is opted in under some other name, and would put a
// role name in Go. The keys below are deliberately unrelated to anything this
// tree knows about.
//
// Delegated elsewhere: which tiers may supply a VALUE for an opted-in key is
// pinned in cmd_supervisor_test.go; the declaration comparison is pinned in
// doctor_supervisor_unit_env_test.go.
//
// Not represented here: launchd, and the shell's own GC_SUPERVISOR_ENV. The
// check reads systemd units and the secrets file only -- see the absence notes
// on supervisorUnitOptInCheck for why the shell channel stays ungated.
//
// Run: go test ./cmd/gc/ -run SupervisorUnitOptIn -count=1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
)

// writeSupervisorUnitOptInFixture writes a unit carrying optedInPresent as
// Environment lines and a secrets file opting in every key in optIn, and
// returns both paths. Tests vary only the gap between the two lists, so a red
// run is attributable to that gap and nothing else.
func writeSupervisorUnitOptInFixture(t *testing.T, optIn, optedInPresent []string, extraSecrets string) (unitPath, secretsPath string) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	data := supervisorServiceEnvBaselineData()
	for _, key := range optedInPresent {
		data.ExtraEnv = append(data.ExtraEnv, supervisorServiceEnvVar{Name: key, Value: "value-of-" + key})
	}
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	unitPath = filepath.Join(homeDir, "gascity-supervisor.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		t.Fatalf("writing unit: %v", err)
	}

	var sb strings.Builder
	if len(optIn) > 0 {
		sb.WriteString(supervisorServiceOptInEnv + "=" + strings.Join(optIn, ",") + "\n")
	}
	sb.WriteString(extraSecrets)
	writeSupervisorSecretsEnvFile(t, sb.String())
	return unitPath, supervisorSecretsEnvFilePath()
}

func runSupervisorUnitOptInCheck(t *testing.T, unitPath, secretsPath string) *doctor.CheckResult {
	t.Helper()
	return newSupervisorUnitOptInCheck(unitPath, secretsPath).Run(nil)
}

// TestSupervisorUnitOptInSatisfiedUnitPasses is the control. Without it a red
// from any test below could just as well mean the fixture never agreed.
func TestSupervisorUnitOptInSatisfiedUnitPasses(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_CHANNEL_ID"}
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t, keys, keys,
		"WIDGET_API_TOKEN=t\nWIDGET_CHANNEL_ID=c\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
	if got.Status != doctor.StatusOK {
		t.Fatalf("unit carrying every opted-in key reported %v: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInCatchesDroppedKey is the ci-cblj0v reproduction: the
// secrets file opts three keys in, the unit carries none, and the check must
// go red naming all three. It is an ERROR rather than a warning because the
// operator declared the intent and the unit contradicts it -- the drift check
// beside this one was a WARN and a day of downtime went unreported.
func TestSupervisorUnitOptInCatchesDroppedKey(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_BOT_TOKEN", "WIDGET_CHANNEL_ID"}
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t, keys, nil,
		"WIDGET_API_TOKEN=t\nWIDGET_BOT_TOKEN=b\nWIDGET_CHANNEL_ID=c\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("unit missing every opted-in key reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
	details := strings.Join(got.Details, "\n")
	for _, key := range keys {
		if !strings.Contains(details, key) {
			t.Fatalf("details do not name the dropped key %s: %v", key, got.Details)
		}
	}
}

// TestSupervisorUnitOptInCatchesAPartialUnit requires red when the unit
// carries some opted-in keys and not others. A check asserting only "the unit
// has some opted-in keys" would pass this, and it is the state a regeneration
// from a half-exported shell actually produces.
func TestSupervisorUnitOptInCatchesAPartialUnit(t *testing.T) {
	keys := []string{"WIDGET_API_TOKEN", "WIDGET_CHANNEL_ID"}
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t, keys, keys[:1],
		"WIDGET_API_TOKEN=t\nWIDGET_CHANNEL_ID=c\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("partially satisfied unit reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
	details := strings.Join(got.Details, "\n")
	if strings.Contains(details, keys[0]) {
		t.Fatalf("details name the key the unit DOES carry: %v", got.Details)
	}
	if !strings.Contains(details, keys[1]) {
		t.Fatalf("details do not name the missing key %s: %v", keys[1], got.Details)
	}
}

// TestSupervisorUnitOptInDistinguishesMissingValue pins the two remedies
// apart. A key with a value on file is a stale unit that a reinstall fixes; a
// key with no value anywhere would survive a reinstall unchanged, and an
// operator told to reinstall would watch the finding come back.
func TestSupervisorUnitOptInDistinguishesMissingValue(t *testing.T) {
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t,
		[]string{"WIDGET_HAS_VALUE", "WIDGET_NO_VALUE"}, nil,
		"WIDGET_HAS_VALUE=t\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
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

// TestSupervisorUnitOptInNeverNamesAValue pins the redaction. Doctor output is
// pasted into beads, mail and scrollback, and every key this check reads is a
// credential by construction -- it exists to carry the ones gc will not
// persist by default.
func TestSupervisorUnitOptInNeverNamesAValue(t *testing.T) {
	const secret = "xapp-1-do-not-print-this"
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t,
		[]string{"WIDGET_API_TOKEN"}, nil, "WIDGET_API_TOKEN="+secret+"\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("reported %v, want error: %s %v", got.Status, got.Message, got.Details)
	}
	for _, text := range append([]string{got.Message}, got.Details...) {
		if strings.Contains(text, secret) {
			t.Fatalf("check output leaked the secret value: %q", text)
		}
	}
}

// TestSupervisorUnitOptInFailsClosedOnAnUnparseableSecretsFile is the hole
// this check would otherwise have. A malformed secrets.env makes the generator
// drop every key INCLUDING the opt-in list, so reading "no keys opted in" off
// one and passing would be green over the exact state that produces a short
// unit.
func TestSupervisorUnitOptInFailsClosedOnAnUnparseableSecretsFile(t *testing.T) {
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t,
		[]string{"WIDGET_API_TOKEN"}, []string{"WIDGET_API_TOKEN"}, "MALFORMED LINE WITHOUT EQUALS\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("unparseable secrets file reported %v, want error: %s %v",
			got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInPassesWithNoOptIns asserts the ordinary case: a city
// that opts nothing in has nothing to compare, and must not be nagged.
func TestSupervisorUnitOptInPassesWithNoOptIns(t *testing.T) {
	unitPath, secretsPath := writeSupervisorUnitOptInFixture(t, nil, nil, "ANTHROPIC_AUTH_TOKEN=sk\n")
	got := runSupervisorUnitOptInCheck(t, unitPath, secretsPath)
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
	got := runSupervisorUnitOptInCheck(t, unitPath, supervisorSecretsEnvFilePath())
	if got.Status != doctor.StatusOK {
		t.Fatalf("missing secrets file reported %v: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInPassesWithNoInstalledUnit asserts that a city with
// opt-ins but no gc-installed unit is not a finding: there is no artifact to
// disagree with yet.
func TestSupervisorUnitOptInPassesWithNoInstalledUnit(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	writeSupervisorSecretsEnvFile(t, supervisorServiceOptInEnv+"=WIDGET_API_TOKEN\nWIDGET_API_TOKEN=t\n")
	got := runSupervisorUnitOptInCheck(t,
		filepath.Join(homeDir, "absent.service"), supervisorSecretsEnvFilePath())
	if got.Status != doctor.StatusOK {
		t.Fatalf("absent unit reported %v: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitOptInDoesNotSelfRepair pins the deliberate absence of a
// fix. The repair is `gc supervisor install`, which warm-refreshes a running
// supervisor and bounces every managed session; `gc doctor --fix` runs
// unattended from the health patrol, so this must stay a finding.
func TestSupervisorUnitOptInDoesNotSelfRepair(t *testing.T) {
	if newSupervisorUnitOptInCheck("/nonexistent", "/nonexistent").CanFix() {
		t.Fatal("supervisor-unit-optin-dropped must not offer an automatic fix")
	}
}
