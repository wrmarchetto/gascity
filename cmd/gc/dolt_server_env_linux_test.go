//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

// TestManagedDoltServerIsNotAnySessionsRuntime pins that the managed Dolt
// server and its scope watchdog are city infrastructure, never a session's
// runtime, even when a session's command is what started them.
//
// Whichever process first finds the server down starts it, so the spawn
// inherits that caller's environment. In ci-iqnqzu the control dispatcher's
// GC_SESSION_ID rode into the server, the proctable scan then classified the
// server as that session's root runtime, and the reconciler SIGTERMed the
// city's only bead store when the session was resumed (dolt.log, 20:56:02
// 2026-10-12). Asserting on doltServerEnv's output directly would pin a key
// list. Writing that output into a fake procfs and driving the real scanner
// instead fails on whatever key the scanner keys on, including one added
// after this test was written.
func TestManagedDoltServerIsNotAnySessionsRuntime(t *testing.T) {
	// Salted per run so a stale fixture or a real session cannot satisfy it.
	sessionID := fmt.Sprintf("ci-test-%d", time.Now().UnixNano())
	caller := []string{
		"PATH=/usr/bin",
		"HOME=/home/test",
		"GC_CITY_PATH=/city",
		"GC_SESSION_ID=" + sessionID,
		"GC_SESSION_NAME=core__control-dispatcher-" + sessionID,
		"GC_SESSION_ORIGIN=ephemeral",
		"GC_RUNTIME_EPOCH=2",
		"GC_ALIAS=core.control-dispatcher",
		"GC_AGENT=core.control-dispatcher",
		"GC_TEMPLATE=core.control-dispatcher",
	}
	env := doltServerEnv("", caller)

	// Shape measured on the live host: the server is reparented to the user
	// systemd instance, which carries no GC_SESSION_ID.
	const parentPID, serverPID = 8908, 4118279
	root := t.TempDir()
	writeFakeProc(t, root, parentPID, 1, "systemd", []string{"HOME=/home/test"})
	writeFakeProc(t, root, serverPID, parentPID, "dolt", env)
	restore := proctable.SetScanRootForTesting(root)
	defer restore()

	for _, id := range []string{sessionID, ""} {
		got, err := proctable.ScanBySessionID(id)
		if err != nil {
			t.Fatalf("ScanBySessionID(%q): %v", id, err)
		}
		for _, rt := range got {
			if rt.PID == serverPID {
				t.Fatalf("ScanBySessionID(%q) claims the managed Dolt server "+
					"as session %q's runtime, so the orphan sweep would kill "+
					"the bead store. Spawn env: %v", id, rt.SessionID, env)
			}
		}
	}
	if proctable.IsScanRoot(serverPID) {
		t.Fatalf("IsScanRoot(managed Dolt server) = true. Spawn env: %v", env)
	}
	for _, kv := range env {
		if kv == "GC_CITY_PATH=/city" {
			return
		}
	}
	t.Fatalf("doltServerEnv dropped GC_CITY_PATH, which is city scope, "+
		"not session identity: %v", env)
}

func writeFakeProc(t *testing.T, root string, pid, ppid int, comm string, env []string) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stat := fmt.Sprintf("%d (%s) S %d 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0",
		pid, comm, ppid)
	files := map[string]string{
		"environ": strings.Join(env, "\x00") + "\x00",
		"stat":    stat,
		"comm":    comm + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
