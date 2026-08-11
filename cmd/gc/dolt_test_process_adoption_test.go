package main

// Regression suite for bead ci-9r6x: a managed dolt sql-server that the bd
// provider SCRIPT spawned for itself must reach TestMain's reaper registry.
//
// Scope is the adoption seam in runProviderOpWithEnvContext and the
// confirmation it gates on. Most tests here substitute
// adoptionPIDIsDoltSQLServer rather than dressing a shell up in dolt's argv,
// so they spawn no process at all: test/test-resources.toml ratchets test
// subprocess call sites DOWN, and a fixture process would have spent that
// budget to prove less than a direct test of the real confirmation.
//
// TestProviderOpAdoptsRealScriptSpawnedDolt is the deliberate exception. No
// substituted seam can show that the confirmation reads an actual
// `dolt sql-server` argv, so that one test starts a real server through the
// real script. It is kept out of the fast loop by skipSlowCmdGCTest, and it
// is what the slow_process_gate ledger row's 58 -> 59 ratchet accounts for.
// The reaper's identity guard, PID-reuse protection and process-group
// termination are pinned in dolt_start_managed_test.go.
//
// Run: go test ./cmd/gc/ -run 'TestProviderOp|TestPIDIsDoltSQLServer'

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// adoptionPID is the PID the stub provider scripts below record. It is a
// plausible but almost certainly unused number rather than a live process,
// which is the point: with the confirmation seam substituted, whether this
// PID is alive must not change what adoption does. The reaper skips dead
// entries on its own (reapManagedDoltTestProcesses checks pidAlive).
const adoptionPID = 424242

// withStubbedAdoptionConfirmation substitutes the dolt-server confirmation
// with one that answers only for wantPID and records that it was consulted.
//
// It REFUSES every other PID rather than answering true, so a test that
// somehow reached adoption with a different PID fails instead of quietly
// passing. The returned func reports whether the seam was called at all --
// the negative test needs that, because "nothing was registered" is equally
// true when the PID file was never read, and only the call proves the refusal
// is what stopped it.
func withStubbedAdoptionConfirmation(t *testing.T, wantPID int, answer bool) func() bool {
	t.Helper()
	called := false
	old := adoptionPIDIsDoltSQLServer
	adoptionPIDIsDoltSQLServer = func(pid int) bool {
		if pid != wantPID {
			t.Errorf("confirmation asked about pid %d, want %d", pid, wantPID)
			return false
		}
		called = true
		return answer
	}
	t.Cleanup(func() { adoptionPIDIsDoltSQLServer = old })
	return func() bool { return called }
}

// writeProviderPIDFileStub materializes a provider script that records pid in
// $GC_DOLT_PID_FILE and then exits with exitCode, which is what
// gc-beads-bd.sh does for a server it spawned itself
// (examples/bd/assets/scripts/gc-beads-bd.sh, the `echo "$server_pid" >
// "$PID_FILE"` after its nohup spawn).
func writeProviderPIDFileStub(t *testing.T, pid, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gc-beads-stub.sh")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %d > \"$GC_DOLT_PID_FILE\"\nexit %d\n", pid, exitCode)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write provider stub: %v", err)
	}
	return path
}

func providerStubEnv(cityPath, pidFile string) []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"GC_CITY_PATH=" + cityPath,
		"GC_DOLT_PID_FILE=" + pidFile,
	}
}

func registeredManagedDoltTestProcess(pid int) (managedDoltStartedProcess, bool) {
	value, ok := managedDoltTestProcessRegistry.Load(pid)
	if !ok {
		return managedDoltStartedProcess{}, false
	}
	started, ok := value.(managedDoltStartedProcess)
	return started, ok
}

// TestProviderOpAdoptsScriptSpawnedDoltPID pins the invariant the bead is
// about: after a provider op, the managed dolt server the script recorded is
// in the reaper's registry even though no Go code in this process spawned it.
//
// Before the fix the PID only ever existed in the script's PID file, because
// resolveProviderLifecycleGCBinary withholds GC_BIN from every *.test binary
// and the script then spawns dolt itself instead of delegating to
// `gc dolt-state start-managed`.
func TestProviderOpAdoptsScriptSpawnedDoltPID(t *testing.T) {
	withManagedDoltTestMode(t, true)
	clearManagedDoltTestProcessRegistry(t)
	t.Cleanup(func() { clearManagedDoltTestProcessRegistry(t) })
	withStubbedAdoptionConfirmation(t, adoptionPID, true)

	cityPath := t.TempDir()
	pidFile := filepath.Join(cityPath, "dolt.pid")
	script := writeProviderPIDFileStub(t, adoptionPID, 0)

	if err := runProviderOpWithEnv(script, providerStubEnv(cityPath, pidFile), "start"); err != nil {
		t.Fatalf("runProviderOpWithEnv(start) = %v, want nil", err)
	}

	started, ok := registeredManagedDoltTestProcess(adoptionPID)
	if !ok {
		t.Fatalf("pid %d recorded in %s is not registered with the test reaper; a dolt server the provider script spawned itself would leak unreaped (ci-9r6x)", adoptionPID, pidFile)
	}
	if started.CityPath != cityPath {
		t.Errorf("registered CityPath = %q, want %q", started.CityPath, cityPath)
	}
}

// TestProviderOpAdoptsScriptSpawnedDoltPIDWhenScriptFails covers the case the
// backstop exists for. A start that spawns a server and then fails -- a ready
// probe that times out, or this op's own context deadline killing the script
// mid-flight -- leaves the server running with nobody holding it. Adoption
// must therefore not be conditional on the op's exit status.
func TestProviderOpAdoptsScriptSpawnedDoltPIDWhenScriptFails(t *testing.T) {
	withManagedDoltTestMode(t, true)
	clearManagedDoltTestProcessRegistry(t)
	t.Cleanup(func() { clearManagedDoltTestProcessRegistry(t) })
	withStubbedAdoptionConfirmation(t, adoptionPID, true)

	cityPath := t.TempDir()
	pidFile := filepath.Join(cityPath, "dolt.pid")
	script := writeProviderPIDFileStub(t, adoptionPID, 1)

	if err := runProviderOpWithEnv(script, providerStubEnv(cityPath, pidFile), "start"); err == nil {
		t.Fatalf("runProviderOpWithEnv(start) = nil, want the stub's exit 1 to surface")
	}

	if _, ok := registeredManagedDoltTestProcess(adoptionPID); !ok {
		t.Fatalf("pid %d is not registered after a failed provider op; the server the script left behind would leak unreaped", adoptionPID)
	}
}

// TestProviderOpRegistersNothingWhenTheDoltConfirmationRefuses is the hazard
// the adoption path must not introduce. Everything the reaper holds it later
// SIGTERMs, and a PID file survives the server that wrote it -- after a crash
// it names a number the kernel is free to hand to anything. Registration must
// therefore be gated on the confirmation, not on the file merely parsing.
//
// The call assertion is load-bearing: an empty registry proves nothing on its
// own, because it is equally what a code path that never read the PID file
// would leave behind.
func TestProviderOpRegistersNothingWhenTheDoltConfirmationRefuses(t *testing.T) {
	withManagedDoltTestMode(t, true)
	clearManagedDoltTestProcessRegistry(t)
	t.Cleanup(func() { clearManagedDoltTestProcessRegistry(t) })
	confirmationCalled := withStubbedAdoptionConfirmation(t, adoptionPID, false)

	cityPath := t.TempDir()
	pidFile := filepath.Join(cityPath, "dolt.pid")
	script := writeProviderPIDFileStub(t, adoptionPID, 0)

	if err := runProviderOpWithEnv(script, providerStubEnv(cityPath, pidFile), "start"); err != nil {
		t.Fatalf("runProviderOpWithEnv(start) = %v, want nil", err)
	}

	if !confirmationCalled() {
		t.Fatalf("adoption never asked whether pid %d is a dolt server; the PID file at %s was not consulted at all", adoptionPID, pidFile)
	}
	if _, ok := registeredManagedDoltTestProcess(adoptionPID); ok {
		t.Fatalf("pid %d was registered after the confirmation refused it; the reaper would SIGTERM it", adoptionPID)
	}
}

// TestPIDIsDoltSQLServerRefusesALiveNonDoltProcess exercises the real
// confirmation, not the seam. The test binary is the one live process every
// run is guaranteed to have and is certainly not a managed dolt server, so it
// pins the refusal without spawning anything. The accepting direction is
// covered by TestProviderOpAdoptsRealScriptSpawnedDolt against a real server.
func TestPIDIsDoltSQLServerRefusesALiveNonDoltProcess(t *testing.T) {
	if pidIsDoltSQLServer(os.Getpid()) {
		t.Fatalf("pidIsDoltSQLServer(%d) = true for this test binary; the confirmation accepts any live process and would license the reaper to SIGTERM one", os.Getpid())
	}
	if pidIsDoltSQLServer(0) || pidIsDoltSQLServer(-1) {
		t.Fatalf("pidIsDoltSQLServer accepted a non-positive pid")
	}
}

// TestProviderOpAdoptsRealScriptSpawnedDolt is the end-to-end form of the
// ci-9r6x regression, and the only test in the package that exercises the
// spawn route the bug was actually about: the real gc-beads-bd script, a real
// dolt binary, and the production environment projection. The stubbed tests
// above substitute adoptionPIDIsDoltSQLServer, so they pin the wiring but
// never see an actual `dolt sql-server` argv -- which is the input the
// confirmation has to read correctly for any of that wiring to matter.
//
// It is also the only test that runs the real confirmation in its ACCEPTING
// direction. TestPIDIsDoltSQLServerRefusesALiveNonDoltProcess covers the
// refusal against this test binary; a confirmation hardwired to false would
// satisfy that one and leave the reaper permanently blind.
//
// This call site is the cmd/gc+untagged slow_process_gate occupant that the
// 58 -> 59 ledger ratchet accounts for across census.go, test-resources.toml
// and TESTING.md. Deleting the gate as redundant fails the ledger gate.
func TestProviderOpAdoptsRealScriptSpawnedDolt(t *testing.T) {
	skipSlowCmdGCTest(t, "starts a real managed dolt through the real gc-beads-bd script; run make test-cmd-gc-process for full coverage")
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt not installed")
	}

	withManagedDoltTestMode(t, true)
	clearManagedDoltTestProcessRegistry(t)
	t.Cleanup(func() { clearManagedDoltTestProcessRegistry(t) })

	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityPath, "city.toml"), []byte("[workspace]\nname = \"demo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	materializeBuiltinPacksForTest(t, cityPath)
	script := gcBeadsBdScriptPath(cityPath)

	// dolt init runs `git`-style identity checks against HOME, so a host
	// whose real HOME has no committer identity would fail the store setup
	// for a reason unrelated to adoption. Point both at a temp identity
	// instead of skipping on the host's git config.
	homeDir := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitConfig := filepath.Join(homeDir, ".gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[user]\n\tname = Test User\n\temail = test@example.com\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	providerEnv, err := providerLifecycleProcessEnvWithError(cityPath, "exec:"+script)
	if err != nil {
		t.Fatalf("providerLifecycleProcessEnvWithError: %v", err)
	}
	// Later entries win (os/exec dedups keeping the last), so appending
	// overrides what the projection inherited without disturbing the dolt
	// paths it computed. Rebuilding the slice by hand would drop those.
	providerEnv = append(providerEnv,
		"HOME="+homeDir,
		"GIT_CONFIG_GLOBAL="+gitConfig,
		"PATH="+strings.Join([]string{filepath.Dir(doltPath), os.Getenv("PATH")}, string(os.PathListSeparator)),
	)
	// The whole point of this test is the route the script takes when it
	// CANNOT delegate to `gc dolt-state start-managed`. If GC_BIN ever
	// reaches a test binary, the script delegates, the server is spawned by
	// a separate gc process, and this test would keep passing while covering
	// nothing. Fail loudly rather than silently changing what is under test.
	if got := runtimeEnvEntriesToMap(providerEnv)["GC_BIN"]; got != "" {
		t.Fatalf("GC_BIN = %q in a test binary, want empty; the script would delegate to the Go starter and this test would no longer cover the shell spawn route", got)
	}
	// Registered before the start so a start that spawns a server and then
	// fails its ready probe still gets stopped. Cleanups run LIFO, so this
	// stop precedes the registry clear above -- clearing first would strand
	// the server with neither the reaper nor this op holding it.
	t.Cleanup(func() {
		_ = runProviderOpWithEnv(script, providerEnv, "stop")
	})

	if err := runProviderOpWithEnv(script, providerEnv, "start"); err != nil {
		t.Fatalf("runProviderOpWithEnv(start) = %v, want nil", err)
	}

	pidFile := runtimeEnvEntriesToMap(providerEnv)["GC_DOLT_PID_FILE"]
	pid := managedDoltPIDFromProviderPIDFile(pidFile)
	if pid <= 0 {
		t.Fatalf("provider recorded no usable PID at %s", pidFile)
	}
	if !pidIsDoltSQLServer(pid) {
		t.Fatalf("pidIsDoltSQLServer(%d) = false for the server the provider just started; the confirmation cannot see a real managed dolt", pid)
	}

	if _, ok := registeredManagedDoltTestProcess(pid); !ok {
		t.Fatalf("real dolt sql-server pid %d spawned by the provider script is not registered with the test reaper (ci-9r6x)", pid)
	}
}
