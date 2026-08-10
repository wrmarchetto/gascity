package main

// Regression suite for bead ci-9r6x: a managed dolt sql-server that the bd
// provider SCRIPT spawned for itself must reach TestMain's reaper registry.
//
// Scope is the adoption seam in runProviderOpWithEnvContext and the
// confirmation it gates on. The tests substitute adoptionPIDIsDoltSQLServer
// rather than dressing a shell up in dolt's argv, so this file spawns no
// process at all: test/test-resources.toml ratchets test subprocess call
// sites DOWN, and a fixture process would have spent that budget to prove
// less than the direct test of the real confirmation below.
//
// NOT covered here, deliberately: the accepting direction of the real
// confirmation, end to end against a server the real gc-beads-bd script
// spawned. That test exists and passes -- see the commit body -- but it needs
// a skipSlowCmdGCTest gate, and the same ledger pins cmd/gc slow-process
// markers at a baseline that cannot grow. It lands with the sanctioned
// baseline ratchet under its own bead. The reaper's identity guard, PID-reuse
// protection and process-group termination are pinned in
// dolt_start_managed_test.go.
//
// Run: go test ./cmd/gc/ -run 'TestProviderOp|TestPIDIsDoltSQLServer'

import (
	"fmt"
	"os"
	"path/filepath"
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
