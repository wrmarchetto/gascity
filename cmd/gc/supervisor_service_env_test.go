package main

// Scope: the supervisor service-file environment declaration --
// ${GC_HOME}/service-env.toml -- and the single ordered env set both
// service templates render from.
//
// Why this suite exists: `gc supervisor start` regenerates the service file
// on every invocation from the calling shell, so an install run from a lean
// shell used to drop whatever that shell did not export. The declaration
// turns the unit's environment into something a regeneration REPRODUCES
// rather than re-derives. These tests pin the reproduction property and the
// policy boundary around it -- the declaration restores values, it never
// widens which keys may persist.
//
// Delegated elsewhere: the doctor gate that compares the live unit against
// the declaration lives in doctor_supervisor_unit_env_test.go. The tiers
// below the declaration (shell scan, GC_SUPERVISOR_ENV, secrets.env,
// launchctl) are pinned in cmd_supervisor_test.go.
//
// Run: go test ./cmd/gc/ -run SupervisorServiceEnv -count=1

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/doctor"
	"github.com/gastownhall/gascity/internal/execenv"
)

// supervisorServiceEnvBaselineData is the fixed input for the byte-identity
// baseline below. Ordinary values only: the quoting change that came with
// EnvLines (strconv.Quote everywhere, where GC_HOME/PATH/XDG_RUNTIME_DIR
// were previously interpolated raw) is invisible for values holding no
// quote or backslash, and that is what makes the baseline meaningful.
func supervisorServiceEnvBaselineData() *supervisorServiceData {
	return &supervisorServiceData{
		GCPath:        "/home/u/.local/bin/gc",
		LogPath:       "/home/u/.gc/supervisor.log",
		GCHome:        "/home/u/.gc",
		XDGRuntimeDir: "/run/user/1000",
		Path:          "/usr/bin:/bin",
		ExtraEnv: []supervisorServiceEnvVar{
			{Name: execenv.UsageMetricsDisableEnv, Value: "1"},
			{Name: "HOME", Value: "/home/u"},
		},
		PortInUseExitCode: 3,
	}
}

// supervisorServiceEnvRecordedUnit is the systemd unit this template rendered
// before EnvLines replaced the four per-field Environment lines, captured
// from the running code at 996943c7e.
//
// It is here because the first install after that refactor runs against the
// live city, and installSupervisorSystemd treats any content change as cause
// for a warm refresh -- stop plus start of the supervisor. A refactor that
// shifted one byte would therefore bounce every managed session as a side
// effect nobody asked for. Byte identity is the property that makes the
// refactor free to land.
const supervisorServiceEnvRecordedUnit = `[Unit]
Description=Gas City machine supervisor

[Service]
Type=simple
# Signal only the main supervisor PID on stop. The systemd default
# (control-group) would cascade SIGTERM to tmux servers spawned by
# 'gc supervisor run' that live in this cgroup, killing one-per-bead
# session conversation history. The reconciler re-adopts tmux on start.
KillMode=process
ExecStart=/home/u/.local/bin/gc supervisor run
Restart=always
RestartSec=5s
# A duplicate supervisor exits with this code -- whether it lost the shared
# API port or found the control socket already answering. Restarting it would
# just crash-loop forever (ga-ceq, and ci-nncach where the socket-guard case
# returned a bare 1 and did exactly that, 5733 times).
RestartPreventExitStatus=3
StandardOutput=append:/home/u/.gc/supervisor.log
StandardError=append:/home/u/.gc/supervisor.log
Environment=GC_HOME="/home/u/.gc"
Environment=XDG_RUNTIME_DIR="/run/user/1000"
Environment=PATH="/usr/bin:/bin"
Environment=GC_SUPERVISOR_PRESERVE_SESSIONS_ON_SIGNAL="1"
Environment=GC_DISABLE_USAGE_METRICS="1"
Environment=HOME="/home/u"


[Install]
WantedBy=default.target
`

// TestSupervisorServiceEnvSystemdRenderMatchesRecordedUnit pins byte identity
// against the pre-EnvLines output. A diff here is not necessarily a bug, but
// it IS a warm refresh of the live supervisor on the next install, so it must
// be a deliberate edit with the baseline updated in the same commit.
func TestSupervisorServiceEnvSystemdRenderMatchesRecordedUnit(t *testing.T) {
	got, err := renderSupervisorTemplate(supervisorSystemdTemplate, supervisorServiceEnvBaselineData())
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	if got != supervisorServiceEnvRecordedUnit {
		t.Fatalf("systemd unit drifted from the recorded baseline.\n got: %q\nwant: %q",
			got, supervisorServiceEnvRecordedUnit)
	}
}

// TestSupervisorServiceEnvOptInsSurviveAnEmptyEnvironment pins the two opt-ins
// gs-cbhj was written against as properties of the generator rather than of
// the invocation. Both are unconditional today -- the preserve-sessions flag
// is a literal in EnvLines, the metrics opt-out is assigned after every env
// tier -- and this test is what turns "unconditional" from a reading of the
// code into something a later edit cannot quietly undo.
//
// The environment is stripped rather than merely lean: an assertion that only
// survives because the ambient shell happened to export the key proves
// nothing about the generator.
func TestSupervisorServiceEnvOptInsSurviveAnEmptyEnvironment(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("GC_SUPERVISOR_ENV", "")
	t.Setenv(execenv.UsageMetricsDisableEnv, "")
	t.Setenv(supervisorPreserveSessionsOnSignalEnv, "")

	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	env, err := parseSupervisorUnitEnvironment(unit)
	if err != nil {
		t.Fatalf("parseSupervisorUnitEnvironment: %v", err)
	}
	for _, key := range []string{
		supervisorPreserveSessionsOnSignalEnv,
		execenv.UsageMetricsDisableEnv,
	} {
		if env[key] != "1" {
			t.Fatalf("rendered unit %s = %q, want \"1\" (all env: %#v)", key, env[key], env)
		}
	}
}

// TestSupervisorServiceEnvDeclarationRoundTrips pins that what install writes
// is what the next install and the doctor gate read back, and that writing
// the same set twice produces identical bytes. Byte stability is what lets
// the gate compare sets without a re-render, and an unsorted map iteration is
// the usual way it is lost.
func TestSupervisorServiceEnvDeclarationRoundTrips(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	want := supervisorServiceEnvBaselineData().EnvLines()
	if err := writeSupervisorServiceEnvDeclaration(want); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	first, err := os.ReadFile(supervisorServiceEnvFilePath())
	if err != nil {
		t.Fatalf("reading declaration: %v", err)
	}
	// Rewrite the same set in a different input order. Rewriting it in the
	// SAME order would go green over an unsorted emitter, because EnvLines
	// itself is deterministic -- the order only varies when the resolved key
	// set changes, which is exactly when a human is reading the diff.
	shuffled := make([]supervisorServiceEnvVar, len(want))
	for i, item := range want {
		shuffled[len(want)-1-i] = item
	}
	if err := writeSupervisorServiceEnvDeclaration(shuffled); err != nil {
		t.Fatalf("rewriting declaration: %v", err)
	}
	second, err := os.ReadFile(supervisorServiceEnvFilePath())
	if err != nil {
		t.Fatalf("re-reading declaration: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("declaration is not byte-stable across rewrites:\nfirst:  %q\nsecond: %q", first, second)
	}

	got, err := loadSupervisorServiceEnvDeclaration()
	if err != nil {
		t.Fatalf("loadSupervisorServiceEnvDeclaration: %v", err)
	}
	for _, item := range want {
		if got[item.Name] != item.Value {
			t.Fatalf("declaration[%s] = %q, want %q", item.Name, got[item.Name], item.Value)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("declaration holds %d keys, want %d: %#v", len(got), len(want), got)
	}
}

// TestSupervisorServiceEnvDeclarationPermissions pins 0600 on the declaration.
// It carries the same provider credential VALUES the service file does, so a
// group- or world-readable declaration would hand out tokens the unit file is
// careful to keep at 0600.
func TestSupervisorServiceEnvDeclarationPermissions(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))

	if err := writeSupervisorServiceEnvDeclaration([]supervisorServiceEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-secret"},
	}); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	info, err := os.Stat(supervisorServiceEnvFilePath())
	if err != nil {
		t.Fatalf("stat declaration: %v", err)
	}
	if perm := info.Mode().Perm(); perm != supervisorServiceFileMode {
		t.Fatalf("declaration mode = %v, want %v", perm, supervisorServiceFileMode)
	}
}

// TestSupervisorServiceEnvLeanShellReproducesDeclaredValues is the property
// gs-cbhj asked for: a regeneration from a shell that exports none of the
// captured keys reproduces them instead of dropping them. Before the
// declaration this was the live failure -- `gc supervisor start` regenerates
// the unit unconditionally, so any agent session's lean environment could
// rewrite the operator's unit.
func TestSupervisorServiceEnvLeanShellReproducesDeclaredValues(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	// The lean shell: every captured key unset.
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("LANG", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	if err := writeSupervisorServiceEnvDeclaration([]supervisorServiceEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-recorded"},
		{Name: "CLAUDE_CONFIG_DIR", Value: "/home/u/.claude-homes/account3/.claude"},
		{Name: "LANG", Value: "en_US.UTF-8"},
	}); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}

	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	got := supervisorServiceEnvMap(data.ExtraEnv)
	for key, want := range map[string]string{
		"ANTHROPIC_AUTH_TOKEN": "sk-recorded",
		"CLAUDE_CONFIG_DIR":    "/home/u/.claude-homes/account3/.claude",
		"LANG":                 "en_US.UTF-8",
	} {
		if got[key] != want {
			t.Fatalf("ExtraEnv[%s] = %q, want restored %q (all env: %#v)", key, got[key], want, got)
		}
	}
}

// TestSupervisorServiceEnvShellWinsOverDeclaration pins the precedence: the
// declaration is the weakest tier, a record of the last install rather than
// an override. A stale recorded credential must never shadow the one the
// operator just exported.
func TestSupervisorServiceEnvShellWinsOverDeclaration(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-from-shell")

	if err := writeSupervisorServiceEnvDeclaration([]supervisorServiceEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-recorded"},
	}); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	if got := supervisorServiceEnvMap(data.ExtraEnv)["ANTHROPIC_AUTH_TOKEN"]; got != "sk-from-shell" {
		t.Fatalf("ExtraEnv[ANTHROPIC_AUTH_TOKEN] = %q, want shell value sk-from-shell", got)
	}
}

// TestSupervisorServiceEnvDeclarationCannotWidenPersistPolicy pins the
// boundary that keeps the declaration from becoming a second, unreviewed
// allowlist. The declaration decides WHETHER a value survives a lean shell;
// shouldPersistSupervisorEnv decides WHICH keys may persist at all, and the
// declaration is read through that gate exactly as the secrets file is.
//
// The consequence, recorded here because it is the surprising half: a key
// captured under a GC_SUPERVISOR_ENV opt-in that is no longer set does NOT
// come back. Honoring it would let a file written by an earlier install
// outvote the current opt-in, which is the direction that cannot be audited.
func TestSupervisorServiceEnvDeclarationCannotWidenPersistPolicy(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("GC_SUPERVISOR_ENV", "")
	t.Setenv("UNRELATED_SECRET", "")
	t.Setenv("ONCE_OPTED_IN", "")

	if err := writeSupervisorServiceEnvDeclaration([]supervisorServiceEnvVar{
		{Name: "UNRELATED_SECRET", Value: "do-not-persist"},
		{Name: "ONCE_OPTED_IN", Value: "opt-in-expired"},
	}); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	got := supervisorServiceEnvMap(data.ExtraEnv)
	for _, key := range []string{"UNRELATED_SECRET", "ONCE_OPTED_IN"} {
		if _, ok := got[key]; ok {
			t.Fatalf("declaration widened the persist policy with %s: %#v", key, got)
		}
	}
}

// TestSupervisorServiceEnvDeclarationRespectsProviderCredOmit pins that the
// credential opt-out still wins over a declaration recorded before it was
// set. Without this the opt-out would silently stop working on any machine
// that had installed once with credentials present.
func TestSupervisorServiceEnvDeclarationRespectsProviderCredOmit(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv(supervisorOmitProviderCredsEnv, "1")

	if err := writeSupervisorServiceEnvDeclaration([]supervisorServiceEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "sk-recorded"},
	}); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	if _, ok := supervisorServiceEnvMap(data.ExtraEnv)["ANTHROPIC_AUTH_TOKEN"]; ok {
		t.Fatalf("declaration restored a provider credential while %s=1", supervisorOmitProviderCredsEnv)
	}
}

// TestSupervisorServiceEnvDeclarationRestoresXDGRuntimeDir covers the one
// fixed key that can genuinely vanish. GC_HOME and PATH are always resolvable
// and the two opt-in flags are literals, but XDG_RUNTIME_DIR comes straight
// from the invoking environment: a systemd unit regenerated from a cron- or
// ssh-style session loses the line, and with it the supervisor's runtime
// directory.
func TestSupervisorServiceEnvDeclarationRestoresXDGRuntimeDir(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("XDG_RUNTIME_DIR", "")

	if err := writeSupervisorServiceEnvDeclaration([]supervisorServiceEnvVar{
		{Name: "XDG_RUNTIME_DIR", Value: "/run/user/1000"},
	}); err != nil {
		t.Fatalf("writeSupervisorServiceEnvDeclaration: %v", err)
	}
	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	if data.XDGRuntimeDir != "/run/user/1000" {
		t.Fatalf("XDGRuntimeDir = %q, want restored /run/user/1000", data.XDGRuntimeDir)
	}
}

// TestSupervisorServiceEnvMissingDeclarationIsNotAnError pins the state every
// machine is in before its first install under this change: no declaration,
// behavior identical to before.
func TestSupervisorServiceEnvMissingDeclarationIsNotAnError(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")

	got, err := loadSupervisorServiceEnvDeclaration()
	if err != nil {
		t.Fatalf("loadSupervisorServiceEnvDeclaration with no file: %v", err)
	}
	if got != nil {
		t.Fatalf("missing declaration yielded %#v, want nil", got)
	}
	if _, err := buildSupervisorServiceData(); err != nil {
		t.Fatalf("buildSupervisorServiceData with no declaration: %v", err)
	}
}

// TestSupervisorServiceEnvMalformedDeclarationIsAnError pins fail-closed
// parsing, and it is deliberately the opposite of the secrets file's
// degrade-and-continue. A secrets file that fails to parse costs one
// credential; a declaration that fails to parse silently drops EVERY
// recorded key from the regenerated unit, which is the failure this whole
// file exists to prevent. doSupervisorInstall refuses on this error and the
// already-installed unit keeps running.
func TestSupervisorServiceEnvMalformedDeclarationIsAnError(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")

	path := supervisorServiceEnvFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("creating GC_HOME: %v", err)
	}
	if err := os.WriteFile(path, []byte("[env\nbroken = \n"), 0o600); err != nil {
		t.Fatalf("writing malformed declaration: %v", err)
	}
	if _, err := loadSupervisorServiceEnvDeclaration(); err == nil {
		t.Fatal("malformed declaration parsed without error")
	}
	if _, err := buildSupervisorServiceData(); err == nil {
		t.Fatal("buildSupervisorServiceData accepted a malformed declaration")
	}
}

// TestSupervisorServiceEnvUnitParserHandlesTemplateQuoting pins the reader
// against the shapes the writer actually emits plus the two systemd spellings
// a hand-edited unit can carry. A value holding '=' is the case a naive
// strings.Split(line, "=") gets wrong, and PATH-like values do not contain
// one, so nothing else in this tree would catch it.
func TestSupervisorServiceEnvUnitParserHandlesTemplateQuoting(t *testing.T) {
	unit := strings.Join([]string{
		"[Service]",
		`Environment=GC_HOME="/home/u/.gc"`,
		`Environment=UNQUOTED=/home/u`,
		`Environment=WITH_EQUALS="a=b=c"`,
		`Environment=WITH_QUOTE="say \"hi\""`,
		"# Environment=COMMENTED=\"no\"",
		"ExecStart=/home/u/.local/bin/gc supervisor run",
		"",
	}, "\n")

	got, err := parseSupervisorUnitEnvironment(unit)
	if err != nil {
		t.Fatalf("parseSupervisorUnitEnvironment: %v", err)
	}
	want := map[string]string{
		"GC_HOME":     "/home/u/.gc",
		"UNQUOTED":    "/home/u",
		"WITH_EQUALS": "a=b=c",
		"WITH_QUOTE":  `say "hi"`,
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d: %#v", len(got), len(want), got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("parsed[%s] = %q, want %q", key, got[key], value)
		}
	}
}

// TestSupervisorServiceEnvDeclarationMatchesRenderedUnit is the join between
// the two halves: the set install records and the set the unit carries must
// be the same set, derived from one call to EnvLines rather than from two
// lists that agree today. It is what lets the doctor gate compare a live unit
// against the declaration and mean something by a difference.
func TestSupervisorServiceEnvDeclarationMatchesRenderedUnit(t *testing.T) {
	data := supervisorServiceEnvBaselineData()
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	fromUnit, err := parseSupervisorUnitEnvironment(unit)
	if err != nil {
		t.Fatalf("parseSupervisorUnitEnvironment: %v", err)
	}
	fromLines := make(map[string]string)
	for _, item := range data.EnvLines() {
		fromLines[item.Name] = item.Value
	}
	if len(fromUnit) != len(fromLines) {
		t.Fatalf("unit carries %d env entries, EnvLines produced %d:\nunit:  %#v\nlines: %#v",
			len(fromUnit), len(fromLines), fromUnit, fromLines)
	}
	for key, value := range fromLines {
		if fromUnit[key] != value {
			t.Fatalf("unit[%s] = %q, EnvLines says %q", key, fromUnit[key], value)
		}
	}
}

// stubSupervisorInstallSystemd replaces the systemctl seams with recorders so
// installSupervisorSystemd can be driven without a user service manager. run
// decides what each systemctl invocation returns, which is how the rollback
// test below fails one specific call.
func stubSupervisorInstallSystemd(t *testing.T, run func(args ...string) error) {
	t.Helper()
	stubSupervisorSystemctlUserAvailable(t, true)
	stubSupervisorRunningPreserveSignalReady(t, true)
	oldRun := supervisorSystemctlRun
	oldActive := supervisorSystemctlActive
	oldLinger := supervisorLoginctlRun
	supervisorSystemctlRun = run
	supervisorSystemctlActive = func(string) bool { return false }
	supervisorLoginctlRun = func(...string) error { return nil }
	t.Cleanup(func() {
		supervisorSystemctlRun = oldRun
		supervisorSystemctlActive = oldActive
		supervisorLoginctlRun = oldLinger
	})
}

// TestSupervisorServiceEnvInstallRecordsTheDeclaration drives the real install
// function rather than the writer it calls. Without this the recording call
// site could be deleted with every other test in this file still green: the
// declaration would have a producer nothing exercises, and the first evidence
// of its absence would be a doctor warning on a machine that had just
// installed.
func TestSupervisorServiceEnvInstallRecordsTheDeclaration(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	stubSupervisorInstallSystemd(t, func(...string) error { return nil })

	data := supervisorServiceEnvBaselineData()
	data.LogPath = filepath.Join(homeDir, ".gc", "supervisor.log")
	var stdout, stderr bytes.Buffer
	if code := installSupervisorSystemd(data, &stdout, &stderr); code != 0 {
		t.Fatalf("installSupervisorSystemd = %d, want 0; stderr=%q", code, stderr.String())
	}

	unitPath := supervisorSystemdServicePath()
	declPath := supervisorServiceEnvFilePath()
	if _, err := os.Stat(declPath); err != nil {
		t.Fatalf("install wrote no declaration at %s: %v", declPath, err)
	}
	got := newSupervisorUnitEnvDriftCheck(unitPath, declPath).Run(nil)
	if got.Status != doctor.StatusOK {
		t.Fatalf("the gate rejects what install just wrote: %v %s %v", got.Status, got.Message, got.Details)
	}
}

// TestSupervisorServiceEnvRollbackLeavesNoDeclaration pins the placement of
// the recording call: on the success path only. A rollback restores the
// PREVIOUS unit, so a declaration describing the abandoned render would leave
// the gate red over an install the operator never completed -- a finding
// about nothing, in the state where a real one matters most.
func TestSupervisorServiceEnvRollbackLeavesNoDeclaration(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	stubSupervisorInstallSystemd(t, func(args ...string) error {
		if len(args) >= 2 && args[1] == "enable" {
			return errors.New("injected systemctl enable failure")
		}
		return nil
	})

	data := supervisorServiceEnvBaselineData()
	data.LogPath = filepath.Join(homeDir, ".gc", "supervisor.log")
	var stdout, stderr bytes.Buffer
	if code := installSupervisorSystemd(data, &stdout, &stderr); code != 1 {
		t.Fatalf("installSupervisorSystemd = %d, want 1; stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(supervisorServiceEnvFilePath()); !os.IsNotExist(err) {
		t.Fatalf("a rolled-back install left a declaration behind (stat err: %v)", err)
	}
}

// TestSupervisorServiceEnvSecretsFileOptInReachesTheRenderedUnit closes the
// producer/consumer gap in the test above it. TestBuildSupervisorServiceData-
// HonorsSecretsFileOptIn asserts the resolver returns the key; this one
// asserts it survives EnvLines and the systemd template and comes back out of
// the parser the doctor gate reads units with.
//
// The two ends are worth separating because ci-cblj0v was diagnosed from the
// unit file, not from the resolver: a key resolved but never rendered is
// invisible to a suite that stops at ExtraEnv, and produces exactly the
// artifact the operator found -- a unit silently short by the keys that
// mattered.
//
// The key name is deliberately unrelated to any service this tree knows
// about. Pinning BRIDGE_SLACK_APP_TOKEN here would make the test a second
// copy of the city's required-key list and would put a role name in Go.
func TestSupervisorServiceEnvSecretsFileOptInReachesTheRenderedUnit(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("GC_HOME", filepath.Join(homeDir, ".gc"))
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("GC_SUPERVISOR_ENV", "")
	t.Setenv("WIDGET_API_TOKEN", "")

	writeSupervisorSecretsEnvFile(t, "GC_SUPERVISOR_ENV=WIDGET_API_TOKEN\nWIDGET_API_TOKEN=widget-value\n")

	data, err := buildSupervisorServiceData()
	if err != nil {
		t.Fatalf("buildSupervisorServiceData: %v", err)
	}
	unit, err := renderSupervisorTemplate(supervisorSystemdTemplate, data)
	if err != nil {
		t.Fatalf("renderSupervisorTemplate: %v", err)
	}
	env, err := parseSupervisorUnitEnvironment(unit)
	if err != nil {
		t.Fatalf("parseSupervisorUnitEnvironment: %v", err)
	}
	if env["WIDGET_API_TOKEN"] != "widget-value" {
		t.Fatalf("rendered unit WIDGET_API_TOKEN = %q, want %q (all env: %#v)",
			env["WIDGET_API_TOKEN"], "widget-value", env)
	}
	// The install records what it rendered, so the declaration must carry the
	// key too -- otherwise the next regeneration from a shell that lost the
	// secrets file would thin the unit and supervisor-unit-env-drift, whose
	// two sides an install writes together, would still report agreement.
	declLines := data.EnvLines()
	found := false
	for _, item := range declLines {
		if item.Name == "WIDGET_API_TOKEN" {
			found = true
		}
	}
	if !found {
		t.Fatalf("EnvLines omits the opted-in key: %#v", declLines)
	}
}
