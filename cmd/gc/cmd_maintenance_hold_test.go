package main

// Unit suite for `gc maintenance hold` / `gc maintenance release`.
//
// Scope: argument refusals, and the ordering contract -- the hold is in force
// BEFORE the stop runs, and the stop runs while this process owns the
// managed-dolt lifecycle lock. Both are asserted from inside the stop
// stand-in, at the instant it is called, because a check after the command
// returns cannot tell "hold written then stop" from "stop then hold written".
//
// Delegated: stopping and restarting a real sql-server is
// dolt_maintenance_hold_integration_test.go (build tag integration).
//
// Run: go test ./cmd/gc -run 'MaintenanceHoldCmd|MaintenanceReleaseCmd' -count=1

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// stubHoldCommandSeams wires the command's ownership, stop and start seams.
// The stop stand-in records what it saw when called. Each stand-in fails the
// test if reached when the case did not script it.
type holdCommandProbe struct {
	stopCalls         int
	holdInForceAtStop bool
	lockFreeAtStop    bool
	startCalls        int
}

func stubHoldCommandSeams(t *testing.T, owned bool, stopErr, startErr error) *holdCommandProbe {
	t.Helper()
	probe := &holdCommandProbe{}
	prevOwned, prevStop, prevStart := maintenanceHoldLifecycleOwned, maintenanceHoldStopServer, maintenanceReleaseStartServer
	prevWait := maintenanceHoldLockWait
	maintenanceHoldLifecycleOwned = func(string) (bool, error) { return owned, nil }
	maintenanceHoldStopServer = func(cityPath string) error {
		probe.stopCalls++
		probe.holdInForceAtStop = isManagedDoltMaintenanceHold(managedDoltMaintenanceHoldRefusal(cityPath))
		lockFile, _, err := openManagedDoltLifecycleLock(cityPath)
		if err != nil {
			t.Fatalf("open lifecycle lock from stop stand-in: %v", err)
		}
		locked, err := tryManagedDoltLifecycleLock(lockFile)
		if err != nil {
			t.Fatalf("try lifecycle lock from stop stand-in: %v", err)
		}
		probe.lockFreeAtStop = locked
		if locked {
			releaseManagedDoltLifecycleLock(lockFile)
		} else {
			_ = lockFile.Close()
		}
		return stopErr
	}
	maintenanceReleaseStartServer = func(string) error {
		probe.startCalls++
		return startErr
	}
	maintenanceHoldLockWait = 2 * time.Second
	t.Cleanup(func() {
		maintenanceHoldLifecycleOwned, maintenanceHoldStopServer, maintenanceReleaseStartServer = prevOwned, prevStop, prevStart
		maintenanceHoldLockWait = prevWait
	})
	return probe
}

// holdCmdCity returns a city dir with its managed-dolt runtime layout pinned
// inside the temp dir, so the lifecycle lock never resolves to a real city's.
func holdCmdCity(t *testing.T) string {
	t.Helper()
	cityPath := t.TempDir()
	t.Setenv("GC_CITY_RUNTIME_DIR", "")
	t.Setenv("GC_DOLT_LOCK_FILE", "")
	t.Setenv("GC_PACK_STATE_DIR", cityPath+"/.gc/runtime/packs/dolt")
	return cityPath
}

// TestMaintenanceHoldCmdHoldsBeforeStoppingUnderLock pins the ordering
// contract: at the instant the server is stopped, the hold already refuses
// and this process owns the lifecycle lock. Either one missing reopens the
// window in which the patrol tick restarts the server just stopped.
func TestMaintenanceHoldCmdHoldsBeforeStoppingUnderLock(t *testing.T) {
	cityPath := holdCmdCity(t)
	probe := stubHoldCommandSeams(t, true, nil, nil)
	var stdout, stderr strings.Builder

	if code := cmdMaintenanceHold(cityPath, 30*time.Minute, "offline gc", &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if probe.stopCalls != 1 {
		t.Fatalf("stop calls = %d, want 1", probe.stopCalls)
	}
	if !probe.holdInForceAtStop {
		t.Fatal("hold was not in force when the server was stopped")
	}
	if probe.lockFreeAtStop {
		t.Fatal("lifecycle lock was free when the server was stopped; hold must own it")
	}
}

// TestMaintenanceHoldCmdStopFailureKeepsHold pins that a failed stop does NOT
// silently drop the hold. The server may be half-down, so lifting the hold
// would let a recover race the operator's next step.
func TestMaintenanceHoldCmdStopFailureKeepsHold(t *testing.T) {
	cityPath := holdCmdCity(t)
	stubHoldCommandSeams(t, true, errors.New("stop exploded"), nil)
	var stdout, stderr strings.Builder

	if code := cmdMaintenanceHold(cityPath, 30*time.Minute, "offline gc", &stdout, &stderr); code == 0 {
		t.Fatal("exit = 0 on stop failure, want nonzero")
	}
	if !isManagedDoltMaintenanceHold(managedDoltMaintenanceHoldRefusal(cityPath)) {
		t.Fatal("hold was dropped after a failed stop")
	}
	for _, want := range []string{"stop exploded", "gc maintenance release"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr %q does not name %q", stderr.String(), want)
		}
	}
}

// TestMaintenanceHoldCmdRefusesWithoutStopping pins every refusal: none may
// write a hold or reach the stop.
func TestMaintenanceHoldCmdRefusesWithoutStopping(t *testing.T) {
	cases := []struct {
		name    string
		owned   bool
		holdFor time.Duration
		reason  string
		prehold bool
	}{
		{name: "zero-duration", owned: true, holdFor: 0, reason: "r"},
		{name: "over-cap", owned: true, holdFor: maxManagedDoltMaintenanceHold + time.Second, reason: "r"},
		{name: "empty-reason", owned: true, holdFor: time.Minute, reason: "  "},
		{name: "not-managed", owned: false, holdFor: time.Minute, reason: "r"},
		{name: "already-held", owned: true, holdFor: time.Minute, reason: "r", prehold: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cityPath := holdCmdCity(t)
			probe := stubHoldCommandSeams(t, tc.owned, nil, nil)
			var before managedDoltMaintenanceHold
			if tc.prehold {
				before = writeHoldForTest(t, cityPath)
			}
			var stdout, stderr strings.Builder
			if code := cmdMaintenanceHold(cityPath, tc.holdFor, tc.reason, &stdout, &stderr); code == 0 {
				t.Fatal("exit = 0, want refusal")
			}
			if probe.stopCalls != 0 {
				t.Fatalf("stop calls = %d, want 0", probe.stopCalls)
			}
			after, present, _ := readManagedDoltMaintenanceHold(cityPath)
			if present != tc.prehold || after.Reason != before.Reason {
				t.Fatalf("hold file changed by a refused hold: present=%v hold=%+v", present, after)
			}
		})
	}
}

// TestMaintenanceHoldCmdMaximumDurationIsAccepted pins the cap as inclusive,
// so the refusal above is the boundary and not an off-by-one below it.
func TestMaintenanceHoldCmdMaximumDurationIsAccepted(t *testing.T) {
	cityPath := holdCmdCity(t)
	stubHoldCommandSeams(t, true, nil, nil)
	var stdout, stderr strings.Builder
	if code := cmdMaintenanceHold(cityPath, maxManagedDoltMaintenanceHold, "r", &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d at exactly the cap, stderr = %s", code, stderr.String())
	}
}

// TestMaintenanceReleaseCmdRemovesHoldThenStarts pins release: the hold is
// gone BEFORE the start runs (a start under the hold would refuse itself),
// and the start does run, so release completes the window rather than
// leaving recovery to the next patrol tick.
func TestMaintenanceReleaseCmdRemovesHoldThenStarts(t *testing.T) {
	cityPath := holdCmdCity(t)
	writeHoldForTest(t, cityPath)
	stubHoldCommandSeams(t, true, nil, nil)
	starts, heldStarts := 0, 0
	maintenanceReleaseStartServer = func(p string) error {
		starts++
		if managedDoltMaintenanceHoldRefusal(p) != nil {
			heldStarts++
		}
		return nil
	}
	var stdout, stderr strings.Builder

	if code := cmdMaintenanceRelease(cityPath, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if starts == 0 {
		t.Fatal("release never started the server")
	}
	if heldStarts != 0 {
		t.Fatalf("server start ran %d time(s) while the hold was still in force", heldStarts)
	}
	if _, err := os.Stat(managedDoltMaintenanceHoldPath(cityPath)); !os.IsNotExist(err) {
		t.Fatalf("hold file still present after release: %v", err)
	}
}

// TestMaintenanceReleaseCmdReportsFailedRestart pins that a restart failure
// is an exit-1 that names the cause, not a green release over a dead server.
func TestMaintenanceReleaseCmdReportsFailedRestart(t *testing.T) {
	cityPath := holdCmdCity(t)
	writeHoldForTest(t, cityPath)
	probe := stubHoldCommandSeams(t, true, nil, errors.New("no server for you"))
	var stdout, stderr strings.Builder

	if code := cmdMaintenanceRelease(cityPath, &stdout, &stderr); code == 0 {
		t.Fatal("exit = 0 with a failed restart, want nonzero")
	}
	if probe.startCalls != 1 || !strings.Contains(stderr.String(), "no server for you") {
		t.Fatalf("start calls = %d, stderr = %q", probe.startCalls, stderr.String())
	}
}
