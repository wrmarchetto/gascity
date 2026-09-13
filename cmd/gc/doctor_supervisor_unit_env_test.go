package main

// Scope: the `supervisor-unit-env-drift` doctor check -- the gate that fails
// when the installed systemd unit's environment disagrees with
// ${GC_HOME}/service-env.toml, the declaration the install rendered it from.
//
// Why this suite exists: gs-cbhj asked for a gate whose green has been seen
// to turn red, and specifically for the case nobody listed. Every test below
// mutates the UNIT rather than the check, because that is the artifact an
// older gc, a partial install, or a hand edit actually leaves behind. A test
// that asserted a fixed list of expected Environment lines would be the
// failure the bead was written against, so there is no such list here: both
// sides come from EnvLines through one writer and one parser.
//
// Delegated elsewhere: the declaration's own format, precedence and
// permissions are pinned in supervisor_service_env_test.go.
//
// Not represented here: launchd. The check reads systemd units only, so on
// macOS it reports OK and the plist's EnvironmentVariables dict is ungated.
// Adding that means a second artifact parser; see the absence note on
// supervisorUnitEnvDriftCheck.
//
// Run: go test ./cmd/gc/ -run SupervisorUnitEnvDrift -count=1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/execenv"
)

// writeSupervisorUnitEnvFixture renders a unit from the shared baseline data,
// writes it plus a matching declaration, and returns both paths. Every test
// below starts from this agreeing pair and breaks exactly one thing, so the
// difference between a red and a green run is attributable.
func writeSupervisorUnitEnvFixture(t *testing.T) (unitPath, declPath string) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	data := supervisorServiceEnvBaselineData()
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	unitPath = filepath.Join(homeDir, "gascity-supervisor.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		t.Fatalf("writing unit: %v", err)
	}
	if err := writeSupervisorServiceEnvDeclaration(data.EnvLines()); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	return unitPath, supervisorServiceEnvFilePath()
}

func runSupervisorUnitEnvDriftCheck(t *testing.T, unitPath, declPath string) *doctor.CheckResult {
	t.Helper()
	return newSupervisorUnitEnvDriftCheck(unitPath, declPath).Run(nil)
}

// dropUnitEnvironmentLine rewrites a unit with one Environment= line removed,
// which is what an install from a gc that no longer emits the key leaves on
// disk.
func dropUnitEnvironmentLine(t *testing.T, unitPath, key string) {
	t.Helper()
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading unit: %v", err)
	}
	var kept []string
	dropped := false
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "Environment="+key+"=") {
			dropped = true
			continue
		}
		kept = append(kept, line)
	}
	if !dropped {
		t.Fatalf("unit carried no Environment=%s= line to drop", key)
	}
	if err := os.WriteFile(unitPath, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
		t.Fatalf("rewriting unit: %v", err)
	}
}

// TestSupervisorUnitEnvDriftAgreeingPairPasses is the control. Without it a
// red from any test below could just as well mean the fixture never agreed.
func TestSupervisorUnitEnvDriftAgreeingPairPasses(t *testing.T) {
	unitPath, declPath := writeSupervisorUnitEnvFixture(t)
	got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
	if got.Status != doctor.StatusOK {
		t.Fatalf("agreeing unit and declaration reported %v: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitEnvDriftCatchesDroppedOptIn drops each opt-in gs-cbhj
// names and requires red for both. These are the two the bead is written
// about, so their red is the one that has to be seen rather than reasoned
// about.
func TestSupervisorUnitEnvDriftCatchesDroppedOptIn(t *testing.T) {
	for _, key := range []string{
		execenv.UsageMetricsDisableEnv,
		supervisorPreserveSessionsOnSignalEnv,
	} {
		t.Run(key, func(t *testing.T) {
			unitPath, declPath := writeSupervisorUnitEnvFixture(t)
			dropUnitEnvironmentLine(t, unitPath, key)
			got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
			if got.Status != doctor.StatusError {
				t.Fatalf("dropping %s reported %v, want error: %s %v", key, got.Status, got.Message, got.Details)
			}
			if !strings.Contains(strings.Join(got.Details, "\n"), key) {
				t.Fatalf("details do not name the dropped key %s: %v", key, got.Details)
			}
		})
	}
}

// TestSupervisorUnitEnvDriftCatchesTheKeyNobodyListed drops a key that
// appears in no denylist anywhere in this tree. It is the whole argument for
// comparing sets instead of checking names: the gate has never heard of
// LOGNAME and still fails.
func TestSupervisorUnitEnvDriftCatchesTheKeyNobodyListed(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	data := supervisorServiceEnvBaselineData()
	data.ExtraEnv = append(data.ExtraEnv, supervisorServiceEnvVar{Name: "LOGNAME", Value: "u"})
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	unitPath := filepath.Join(homeDir, "gascity-supervisor.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		t.Fatalf("writing unit: %v", err)
	}
	if err := writeSupervisorServiceEnvDeclaration(data.EnvLines()); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	declPath := supervisorServiceEnvFilePath()

	if got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath); got.Status != doctor.StatusOK {
		t.Fatalf("control run with LOGNAME present reported %v: %s", got.Status, got.Message)
	}
	dropUnitEnvironmentLine(t, unitPath, "LOGNAME")
	got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("dropping LOGNAME reported %v, want error: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitEnvDriftCatchesUndeclaredUnitKey covers the other
// direction. A unit carrying a key the declaration does not have means
// something other than `gc supervisor install` wrote it, and reading that as
// benign would make the gate one-sided.
func TestSupervisorUnitEnvDriftCatchesUndeclaredUnitKey(t *testing.T) {
	unitPath, declPath := writeSupervisorUnitEnvFixture(t)
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading unit: %v", err)
	}
	injected := strings.Replace(string(raw),
		"\n[Install]", "Environment=HTTP_PROXY=\"http://10.0.0.1:3128\"\n\n[Install]", 1)
	if injected == string(raw) {
		t.Fatal("failed to inject an undeclared Environment line")
	}
	if err := os.WriteFile(unitPath, []byte(injected), 0o600); err != nil {
		t.Fatalf("rewriting unit: %v", err)
	}
	got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("undeclared unit key reported %v, want error: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitEnvDriftCatchesChangedValue pins that the gate compares
// values, not just key sets. A unit whose GC_HOME points somewhere else is
// running a different city's supervisor state while every key still matches.
func TestSupervisorUnitEnvDriftCatchesChangedValue(t *testing.T) {
	unitPath, declPath := writeSupervisorUnitEnvFixture(t)
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatalf("reading unit: %v", err)
	}
	edited := strings.Replace(string(raw), `Environment=GC_HOME="/home/u/.gc"`,
		`Environment=GC_HOME="/home/u/.gc-other"`, 1)
	if edited == string(raw) {
		t.Fatal("failed to edit the GC_HOME line")
	}
	if err := os.WriteFile(unitPath, []byte(edited), 0o600); err != nil {
		t.Fatalf("rewriting unit: %v", err)
	}
	got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("changed value reported %v, want error: %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorUnitEnvDriftRedactsValues pins that a failure names keys and
// never values. The declaration and the unit both carry provider
// credentials, and doctor output gets pasted into beads and mail.
func TestSupervisorUnitEnvDriftRedactsValues(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	data := supervisorServiceEnvBaselineData()
	data.ExtraEnv = append(data.ExtraEnv,
		supervisorServiceEnvVar{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-must-not-appear"})
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	unitPath := filepath.Join(homeDir, "gascity-supervisor.service")
	if err := os.WriteFile(unitPath, []byte(unit), 0o600); err != nil {
		t.Fatalf("writing unit: %v", err)
	}
	if err := writeSupervisorServiceEnvDeclaration(data.EnvLines()); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	dropUnitEnvironmentLine(t, unitPath, "ANTHROPIC_AUTH_TOKEN")

	got := runSupervisorUnitEnvDriftCheck(t, unitPath, supervisorServiceEnvFilePath())
	if got.Status != doctor.StatusError {
		t.Fatalf("dropped credential reported %v, want error", got.Status)
	}
	rendered := got.Message + "\n" + got.FixHint + "\n" + strings.Join(got.Details, "\n")
	if strings.Contains(rendered, "sk-must-not-appear") {
		t.Fatalf("check output leaked a credential value: %s", rendered)
	}
	if !strings.Contains(rendered, "ANTHROPIC_AUTH_TOKEN") {
		t.Fatalf("check output does not name the drifted key: %s", rendered)
	}
}

// TestSupervisorUnitEnvDriftWarnsWhenDeclarationIsAbsent pins the
// pre-migration state: every machine already carrying an installed unit has
// no declaration until its next install. That is a warn with the remedy, not
// an error -- and not an OK, which would make the whole gate skippable by
// deleting one file.
func TestSupervisorUnitEnvDriftWarnsWhenDeclarationIsAbsent(t *testing.T) {
	unitPath, declPath := writeSupervisorUnitEnvFixture(t)
	if err := os.Remove(declPath); err != nil {
		t.Fatalf("removing declaration: %v", err)
	}
	got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
	if got.Status != doctor.StatusWarning {
		t.Fatalf("absent declaration reported %v, want warning: %s", got.Status, got.Message)
	}
	if !strings.Contains(got.FixHint, "gc supervisor install") {
		t.Fatalf("fix hint does not name the remedy command: %q", got.FixHint)
	}
}

// TestSupervisorUnitEnvDriftErrorsOnMalformedDeclaration pins that an
// unparseable declaration is loud. It is also the state install refuses on,
// so doctor and install agree about what the file has to be.
func TestSupervisorUnitEnvDriftErrorsOnMalformedDeclaration(t *testing.T) {
	unitPath, declPath := writeSupervisorUnitEnvFixture(t)
	if err := os.WriteFile(declPath, []byte("[env\nbroken = \n"), 0o600); err != nil {
		t.Fatalf("writing malformed declaration: %v", err)
	}
	got := runSupervisorUnitEnvDriftCheck(t, unitPath, declPath)
	if got.Status != doctor.StatusError {
		t.Fatalf("malformed declaration reported %v, want error: %s", got.Status, got.Message)
	}
}

// TestSupervisorUnitEnvDriftPassesWithNoInstalledUnit pins the no-op case: a
// machine that never ran `gc supervisor install`, and every macOS machine,
// has nothing for this gate to compare.
func TestSupervisorUnitEnvDriftPassesWithNoInstalledUnit(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	got := runSupervisorUnitEnvDriftCheck(t,
		filepath.Join(homeDir, "absent.service"),
		supervisorServiceEnvFilePath())
	if got.Status != doctor.StatusOK {
		t.Fatalf("absent unit reported %v, want ok: %s", got.Status, got.Message)
	}
}

// TestSupervisorUnitEnvDriftDoesNotSelfRepair pins CanFix false. `gc doctor
// --fix` runs unattended from the health patrol, and the only repair for this
// finding is a reinstall, which warm-refreshes the live supervisor and bounces
// every managed session. The finding is for an operator to act on.
func TestSupervisorUnitEnvDriftDoesNotSelfRepair(t *testing.T) {
	if newSupervisorUnitEnvDriftCheck("", "").CanFix() {
		t.Fatal("supervisor-unit-env-drift must not offer an automatic fix")
	}
}
