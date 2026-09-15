package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// TestCmdSessionReset_ClearsCircuitBreaker verifies that running
// `gc session reset <identity>` clears a tripped session circuit breaker
// for the matching named session, so the supervisor will respawn the
// session on the next tick. This is the operator-facing remediation path
// the breaker's ERROR log message points at.
func TestCmdSessionReset_ClearsCircuitBreaker(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-reset-cb-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	const identity = "session-a"
	bead, err := store.Create(beads.Bead{
		Title:  "named session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                        identity,
			"template":                     "worker",
			"session_name":                 "s-gc-reset-cb-test",
			"state":                        "awake",
			namedSessionMetadataKey:        "true",
			namedSessionIdentityMetadata:   identity,
			sessionCircuitStateMetadata:    circuitOpen.String(),
			sessionCircuitRestartsMetadata: `["2026-04-10T12:00:00Z"]`,
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}

	// Trip the breaker by recording enough restarts inside
	// the rolling window with no progress events.
	cb := newSessionCircuitBreaker(sessionCircuitBreakerConfig{
		Window:      30 * time.Minute,
		MaxRestarts: 3,
	})
	restore := setSessionCircuitBreakerForTest(cb)
	defer restore()
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		cb.RecordRestart(identity, now.Add(time.Duration(i)*time.Second))
	}
	if !cb.IsOpen(identity, now.Add(time.Minute)) {
		t.Fatalf("precondition: expected breaker OPEN for %q after 4 restarts", identity)
	}

	lis, err := startControllerSocket(
		cityDir,
		controllerHostingStandalone,
		func() {},
		nil,
		nil,
		make(chan reloadRequest),
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	defer lis.Close()                              //nolint:errcheck
	defer os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{identity}, &stdout, &stderr, sessionResetOptions{}); code != 0 {
		t.Fatalf("cmdSessionReset = %d, want 0; stderr=%s", code, stderr.String())
	}

	if cb.IsOpen(identity, now.Add(time.Minute)) {
		t.Fatalf("breaker still OPEN for %q after `gc session reset %s`", identity, identity)
	}
	updated, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(session bead): %v", err)
	}
	if got := updated.Metadata[sessionCircuitStateMetadata]; got != "" {
		t.Fatalf("persisted circuit state = %q, want cleared", got)
	}
	if got := updated.Metadata[sessionCircuitRestartsMetadata]; got != "" {
		t.Fatalf("persisted restart history = %q, want cleared", got)
	}
	if got := updated.Metadata[sessionCircuitResetGenerationMetadata]; got == "" {
		t.Fatal("persisted reset generation is empty, want explicit reset generation")
	}
}

func TestCmdSessionReset_ProviderConstructionFailureReturnsError(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "broken")

	cityDir := shortSocketTempDir(t, "gc-session-reset-provider-error-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	writeBuiltinImportsFixture(t, cityDir, "core")

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	if _, err := store.Create(beads.Bead{
		Title:  "manual session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:session-a"},
		Metadata: map[string]string{
			"alias":        "sky",
			"template":     "session-a",
			"session_name": "s-gc-reset-provider-error",
			"state":        "awake",
		},
	}); err != nil {
		t.Fatalf("create session bead: %v", err)
	}

	lis, err := startControllerSocket(
		cityDir,
		controllerHostingStandalone,
		func() {},
		nil,
		nil,
		make(chan reloadRequest),
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	defer lis.Close()                              //nolint:errcheck
	defer os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck

	oldBuild := buildSessionProviderByName
	buildSessionProviderByName = func(*config.City, string, config.SessionConfig, string, string) (runtime.Provider, error) {
		return nil, errors.New("injected provider failure")
	}
	t.Cleanup(func() { buildSessionProviderByName = oldBuild })

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{"sky"}, &stdout, &stderr, sessionResetOptions{}); code != 1 {
		t.Fatalf("cmdSessionReset = %d, want 1; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
	if got, want := stderr.String(), "gc session reset: constructing session provider: injected provider failure\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestCmdSessionKill_ClearsCircuitBreaker(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-kill-cb-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	fakeProvider := runtime.NewFake()
	oldBuild := buildSessionProviderByName
	buildSessionProviderByName = func(*config.City, string, config.SessionConfig, string, string) (runtime.Provider, error) {
		return fakeProvider, nil
	}
	t.Cleanup(func() { buildSessionProviderByName = oldBuild })

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	const identity = "session-a"
	const sessionName = "s-gc-kill-cb-test"
	bead, err := store.Create(beads.Bead{
		Title:  "named session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                        identity,
			"template":                     "worker",
			"session_name":                 sessionName,
			"state":                        "awake",
			namedSessionMetadataKey:        "true",
			namedSessionIdentityMetadata:   identity,
			sessionCircuitStateMetadata:    circuitOpen.String(),
			sessionCircuitRestartsMetadata: `["2026-04-10T12:00:00Z"]`,
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}
	if err := fakeProvider.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("fakeProvider.Start: %v", err)
	}
	if err := fakeProvider.SetMeta(sessionName, "GC_SESSION_ID", bead.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}

	cb := newSessionCircuitBreaker(sessionCircuitBreakerConfig{
		Window:      30 * time.Minute,
		MaxRestarts: 3,
	})
	restore := setSessionCircuitBreakerForTest(cb)
	defer restore()
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		cb.RecordRestart(identity, now.Add(time.Duration(i)*time.Second))
	}
	if !cb.IsOpen(identity, now.Add(time.Minute)) {
		t.Fatalf("precondition: expected breaker OPEN for %q after 4 restarts", identity)
	}

	lis, err := startControllerSocket(
		cityDir,
		controllerHostingStandalone,
		func() {},
		nil,
		nil,
		make(chan reloadRequest),
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	defer lis.Close()                              //nolint:errcheck
	defer os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0; stderr=%s", code, stderr.String())
	}

	if fakeProvider.IsRunning(sessionName) {
		t.Fatalf("session %q still running after kill", sessionName)
	}
	if cb.IsOpen(identity, now.Add(time.Minute)) {
		t.Fatalf("breaker still OPEN for %q after `gc session kill %s`", identity, identity)
	}
	updated, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(session bead): %v", err)
	}
	if got := updated.Metadata[sessionCircuitStateMetadata]; got != "" {
		t.Fatalf("persisted circuit state = %q, want cleared", got)
	}
	if got := updated.Metadata[sessionCircuitRestartsMetadata]; got != "" {
		t.Fatalf("persisted restart history = %q, want cleared", got)
	}
	if got := updated.Metadata[sessionCircuitResetGenerationMetadata]; got == "" {
		t.Fatal("persisted reset generation is empty, want explicit reset generation")
	}
}

// TestCmdSessionKill_SyncsBeadToAsleep is the regression guard for #3629:
// `gc session kill` must sync the bead to asleep + refresh synced_at at the
// CLI layer (cmdSessionKill), otherwise the bead retains its prior live state
// ("awake") and a later `gc session wake` short-circuits on the stale
// metadata and never starts a fresh runtime. The write lives in cmdSessionKill
// (not Manager.Kill) so the drain-ack async-stop path is unaffected.
func TestCmdSessionKill_SyncsBeadToAsleep(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-kill-asleep-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	fakeProvider := runtime.NewFake()
	oldBuild := buildSessionProviderByName
	buildSessionProviderByName = func(*config.City, string, config.SessionConfig, string, string) (runtime.Provider, error) {
		return fakeProvider, nil
	}
	t.Cleanup(func() { buildSessionProviderByName = oldBuild })

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	const identity = "session-a"
	const sessionName = "s-gc-kill-asleep-test"
	bead, err := store.Create(beads.Bead{
		Title:  "named session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                      identity,
			"template":                   "worker",
			"session_name":               sessionName,
			"state":                      "awake",
			namedSessionMetadataKey:      "true",
			namedSessionIdentityMetadata: identity,
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}
	if err := fakeProvider.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("fakeProvider.Start: %v", err)
	}
	if err := fakeProvider.SetMeta(sessionName, "GC_SESSION_ID", bead.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}

	lis, err := startControllerSocket(
		cityDir,
		controllerHostingStandalone,
		func() {},
		nil,
		nil,
		make(chan reloadRequest),
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	defer lis.Close()                              //nolint:errcheck
	defer os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0; stderr=%s", code, stderr.String())
	}

	updated, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(session bead): %v", err)
	}
	if got := updated.Metadata["state"]; got != string(session.StateAsleep) {
		t.Errorf("post-kill state = %q, want %q", got, session.StateAsleep)
	}
	if got := updated.Metadata["synced_at"]; got == "" {
		t.Error("post-kill synced_at is empty, want a refreshed timestamp")
	}
}

func TestCmdSessionKill_ClearsCircuitBreakerForAsleepNamedSession(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-kill-cb-asleep-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	const identity = "session-a"
	bead, err := store.Create(beads.Bead{
		Title:  "named session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                        identity,
			"template":                     "worker",
			"session_name":                 "s-gc-kill-cb-asleep-test",
			"state":                        string(session.StateAsleep),
			namedSessionMetadataKey:        "true",
			namedSessionIdentityMetadata:   identity,
			sessionCircuitStateMetadata:    circuitOpen.String(),
			sessionCircuitRestartsMetadata: `["2026-04-10T12:00:00Z"]`,
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}

	cb := newSessionCircuitBreaker(sessionCircuitBreakerConfig{
		Window:      30 * time.Minute,
		MaxRestarts: 3,
	})
	restore := setSessionCircuitBreakerForTest(cb)
	defer restore()
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		cb.RecordRestart(identity, now.Add(time.Duration(i)*time.Second))
	}
	if !cb.IsOpen(identity, now.Add(time.Minute)) {
		t.Fatalf("precondition: expected breaker OPEN for %q after 4 restarts", identity)
	}

	lis, err := startControllerSocket(
		cityDir,
		controllerHostingStandalone,
		func() {},
		nil,
		nil,
		make(chan reloadRequest),
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	defer lis.Close()                              //nolint:errcheck
	defer os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	if cb.IsOpen(identity, now.Add(time.Minute)) {
		t.Fatalf("breaker still OPEN for %q after `gc session kill %s`", identity, identity)
	}
	updated, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(session bead): %v", err)
	}
	if got := updated.Metadata[sessionCircuitStateMetadata]; got != "" {
		t.Fatalf("persisted circuit state = %q, want cleared", got)
	}
	if got := updated.Metadata[sessionCircuitRestartsMetadata]; got != "" {
		t.Fatalf("persisted restart history = %q, want cleared", got)
	}
	if got := updated.Metadata[sessionCircuitResetGenerationMetadata]; got == "" {
		t.Fatal("persisted reset generation is empty, want explicit reset generation")
	}
}

func TestCmdSessionKill_RecordsStoppedWhenCircuitBreakerResetFails(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-kill-cb-reset-fails-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	fakeProvider := runtime.NewFake()
	oldBuild := buildSessionProviderByName
	buildSessionProviderByName = func(*config.City, string, config.SessionConfig, string, string) (runtime.Provider, error) {
		return fakeProvider, nil
	}
	t.Cleanup(func() { buildSessionProviderByName = oldBuild })

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	const identity = "session-a"
	const sessionName = "s-gc-kill-cb-reset-fails-test"
	bead, err := store.Create(beads.Bead{
		Title:  "named session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                        identity,
			"template":                     "worker",
			"session_name":                 sessionName,
			"state":                        "awake",
			namedSessionMetadataKey:        "true",
			namedSessionIdentityMetadata:   identity,
			sessionCircuitStateMetadata:    circuitOpen.String(),
			sessionCircuitRestartsMetadata: `["2026-04-10T12:00:00Z"]`,
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}
	if err := fakeProvider.Start(context.Background(), sessionName, runtime.Config{Command: "true"}); err != nil {
		t.Fatalf("fakeProvider.Start: %v", err)
	}
	if err := fakeProvider.SetMeta(sessionName, "GC_SESSION_ID", bead.ID); err != nil {
		t.Fatalf("SetMeta(GC_SESSION_ID): %v", err)
	}

	lis := startFailingCircuitResetController(t, cityDir)
	defer lis.Close()                              //nolint:errcheck
	defer os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck

	var stdout, stderr bytes.Buffer
	if code := cmdSessionKill([]string{identity}, &stdout, &stderr); code != 0 {
		t.Fatalf("cmdSessionKill = %d, want 0; stderr=%s", code, stderr.String())
	}

	if fakeProvider.IsRunning(sessionName) {
		t.Fatalf("session %q still running after kill", sessionName)
	}
	if got := stderr.String(); !strings.Contains(got, "warning: clearing session circuit breaker") {
		t.Fatalf("stderr = %q, want circuit-breaker warning", got)
	}
	if got := stdout.String(); !strings.Contains(got, "Session "+bead.ID+" killed.") {
		t.Fatalf("stdout = %q, want killed message", got)
	}

	updated, err := store.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(session bead): %v", err)
	}
	if got := updated.Metadata[sessionCircuitStateMetadata]; got != circuitOpen.String() {
		t.Fatalf("persisted circuit state = %q, want unchanged after clear failure", got)
	}

	rec, err := events.NewFileRecorder(filepath.Join(cityDir, ".gc", "events.jsonl"), &bytes.Buffer{})
	if err != nil {
		t.Fatalf("NewFileRecorder(events): %v", err)
	}
	defer rec.Close() //nolint:errcheck
	recorded, err := rec.List(events.Filter{Type: events.SessionStopped, Subject: bead.ID})
	if err != nil {
		t.Fatalf("List(SessionStopped): %v", err)
	}
	if len(recorded) != 1 {
		t.Fatalf("SessionStopped events for %s = %d, want 1", bead.ID, len(recorded))
	}
}

func startFailingCircuitResetController(t *testing.T, cityDir string) net.Listener {
	t.Helper()
	sockPath := controllerSocketPath(cityDir)
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll(controller socket dir): %v", err)
	}
	os.Remove(sockPath) //nolint:errcheck
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen(%q): %v", sockPath, err)
	}
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close() //nolint:errcheck
				scanner := bufio.NewScanner(conn)
				if !scanner.Scan() {
					return
				}
				line := scanner.Text()
				switch {
				case line == "ping":
					fmt.Fprintf(conn, "%d\n", os.Getpid()) //nolint:errcheck
				case strings.HasPrefix(line, sessionCircuitResetCommandPrefix):
					conn.Write([]byte(`{"outcome":"failed","error":"forced reset failure"}` + "\n")) //nolint:errcheck
				default:
					conn.Write([]byte("ok\n")) //nolint:errcheck
				}
			}(conn)
		}
	}()
	return lis
}

func TestCmdSessionReset_RequestsFreshRestartWithController(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-reset-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Create(beads.Bead{
		Title:  "manual session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                      "sky",
			"template":                   "worker",
			"session_name":               "s-gc-reset-test",
			"state":                      "awake",
			"session_key":                "original-key",
			"started_config_hash":        "hash-before-reset",
			"continuation_reset_pending": "",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}

	sockPath := controllerSocketPath(cityDir)
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen(%q): %v", sockPath, err)
	}
	defer lis.Close()         //nolint:errcheck
	defer os.Remove(sockPath) //nolint:errcheck

	commands := make(chan string, 3)
	errCh := make(chan error, 1)
	go func() {
		defer close(commands)
		for i := 0; i < 3; i++ {
			conn, err := lis.Accept()
			if err != nil {
				errCh <- err
				return
			}
			buf := make([]byte, 64)
			n, err := conn.Read(buf)
			if err != nil {
				conn.Close() //nolint:errcheck
				errCh <- err
				return
			}
			cmd := string(buf[:n])
			commands <- cmd
			reply := "ok\n"
			if cmd == "ping\n" {
				reply = "123\n"
			}
			if _, err := conn.Write([]byte(reply)); err != nil {
				conn.Close() //nolint:errcheck
				errCh <- err
				return
			}
			conn.Close() //nolint:errcheck
		}
	}()

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{"sky"}, &stdout, &stderr, sessionResetOptions{}); code != 0 {
		t.Fatalf("cmdSessionReset(controller) = %d, want 0; stderr=%s", code, stderr.String())
	}

	gotCommands := make([]string, 0, 3)
	deadline := time.After(2 * time.Second)
	for len(gotCommands) < 3 {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("controller socket: %v", err)
			}
		case cmd, ok := <-commands:
			if !ok {
				if len(gotCommands) != 3 {
					t.Fatalf("controller commands = %v, want ping, poke, poke", gotCommands)
				}
				break
			}
			gotCommands = append(gotCommands, cmd)
		case <-deadline:
			t.Fatalf("timed out waiting for controller pokes, got %v", gotCommands)
		}
	}
	wantExact := []string{"ping\n", "poke\n", "poke\n"}
	for i, want := range wantExact {
		if gotCommands[i] != want {
			t.Fatalf("controller command %d = %q, want %q", i, gotCommands[i], want)
		}
	}

	reloaded, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt(reload): %v", err)
	}
	got, err := reloaded.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", bead.ID, err)
	}
	if got.Metadata["restart_requested"] != "true" {
		t.Fatalf("restart_requested = %q, want true", got.Metadata["restart_requested"])
	}
	if got.Metadata["continuation_reset_pending"] != "true" {
		t.Fatalf("continuation_reset_pending = %q, want true", got.Metadata["continuation_reset_pending"])
	}
	if got.Metadata["session_key"] != "original-key" {
		t.Fatalf("session_key = %q, want original key preserved until reconcile", got.Metadata["session_key"])
	}
	if got.Metadata["started_config_hash"] != "hash-before-reset" {
		t.Fatalf("started_config_hash = %q, want original hash preserved until reconcile", got.Metadata["started_config_hash"])
	}
}

func TestCmdSessionReset_ControllerClearFailureDoesNotQueueRestart(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-session-reset-clear-fail-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	bead, err := store.Create(beads.Bead{
		Title:  "generic named session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			"alias":                      "session-a",
			"template":                   "worker",
			"session_name":               "s-gc-reset-clear-fail",
			"state":                      "awake",
			"session_key":                "original-key",
			"started_config_hash":        "hash-before-reset",
			namedSessionMetadataKey:      "true",
			namedSessionIdentityMetadata: "session-a",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}

	sockPath := controllerSocketPath(cityDir)
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen(%q): %v", sockPath, err)
	}
	defer lis.Close()         //nolint:errcheck
	defer os.Remove(sockPath) //nolint:errcheck

	commands := make(chan string, 3)
	errCh := make(chan error, 1)
	go func() {
		defer close(commands)
		for i := 0; i < 3; i++ {
			conn, err := lis.Accept()
			if err != nil {
				errCh <- err
				return
			}
			buf := make([]byte, 256)
			n, err := conn.Read(buf)
			if err != nil {
				conn.Close() //nolint:errcheck
				errCh <- err
				return
			}
			cmd := string(buf[:n])
			commands <- cmd
			reply := "ok\n"
			if cmd == "ping\n" {
				reply = "123\n"
			} else if strings.HasPrefix(cmd, "session-circuit-reset:") {
				reply = `{"outcome":"failed","error":"clear failed"}` + "\n"
			}
			if _, err := conn.Write([]byte(reply)); err != nil {
				conn.Close() //nolint:errcheck
				errCh <- err
				return
			}
			conn.Close() //nolint:errcheck
		}
	}()

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{"session-a"}, &stdout, &stderr, sessionResetOptions{}); code != 1 {
		t.Fatalf("cmdSessionReset = %d, want 1; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), `clearing session circuit breaker for "session-a": clear failed`) {
		t.Fatalf("stderr = %q, want controller clear failure", stderr.String())
	}

	gotCommands := make([]string, 0, 3)
	deadline := time.After(2 * time.Second)
	for len(gotCommands) < 3 {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("controller socket: %v", err)
			}
		case cmd, ok := <-commands:
			if !ok {
				t.Fatalf("controller commands = %v, want ping, poke, reset", gotCommands)
			}
			gotCommands = append(gotCommands, cmd)
		case <-deadline:
			t.Fatalf("timed out waiting for controller commands, got %v", gotCommands)
		}
	}
	if gotCommands[0] != "ping\n" || gotCommands[1] != "poke\n" || !strings.HasPrefix(gotCommands[2], "session-circuit-reset:") {
		t.Fatalf("controller commands = %v, want ping, poke, reset", gotCommands)
	}

	reloaded, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt(reload): %v", err)
	}
	got, err := reloaded.Get(bead.ID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", bead.ID, err)
	}
	if got.Metadata["restart_requested"] == "true" {
		t.Fatalf("restart_requested = true, want no queued reset after controller clear failure")
	}
	if got.Metadata["continuation_reset_pending"] == "true" {
		t.Fatalf("continuation_reset_pending = true, want no queued reset after controller clear failure")
	}
}

func TestResetSessionCircuitBreakerOnControllerMalformedReply(t *testing.T) {
	cityDir := shortSocketTempDir(t, "gc-session-reset-malformed-")
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}
	sockPath := controllerSocketPath(cityDir)
	lis, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen(%q): %v", sockPath, err)
	}
	defer lis.Close()         //nolint:errcheck
	defer os.Remove(sockPath) //nolint:errcheck

	errCh := make(chan error, 1)
	go func() {
		conn, err := lis.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close() //nolint:errcheck
		scanner := bufio.NewScanner(conn)
		if !scanner.Scan() {
			errCh <- scanner.Err()
			return
		}
		if _, err := conn.Write([]byte("not-json\n")); err != nil {
			errCh <- err
		}
	}()

	err = resetSessionCircuitBreakerOnController(cityDir, "session-id", "rig-a/session-a")
	if err == nil {
		t.Fatal("resetSessionCircuitBreakerOnController = nil, want decode error")
	}
	if !strings.Contains(err.Error(), "decoding session circuit reset reply") {
		t.Fatalf("error = %v, want decode context", err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("controller socket: %v", err)
		}
	default:
	}
}

func writeGenericNamedSessionCityTOML(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}
	data := []byte(`[workspace]
name = "test-city"

[beads]
provider = "file"

[[agent]]
name = "session-a"
provider = "codex"
start_command = "echo"

[[named_session]]
template = "session-a"
`)
	if err := os.WriteFile(filepath.Join(dir, "city.toml"), data, 0o644); err != nil {
		t.Fatalf("WriteFile(city.toml): %v", err)
	}
}

// --- gc session reset against an ATTACHED session (ci-6mp9hs) ---
//
// THE INCIDENT. On 2026-09-15 a session was reset while an operator was
// attached to it on /dev/pts/8, mid-conversation, and the conversation was
// gone. `gc session reset` had no attachment check of any kind, while the
// config-drift restart path in cmd/gc/session_reconciler.go checks attachment
// FIRST and defers, its comment giving the reason: "a single transient
// IsAttached false negative would destroy conversation context irreversibly."
// Two paths, the same consequence, opposite treatment of the same risk.
//
// THE DECISION THESE TESTS PIN, recorded here because no spec settles it and
// this rig has no PM to ask (assets/scripts/ask-pm.py refused: city has no
// docs/roadmap.md): reset REFUSES an attached session and offers --force. The
// two populations the incident distinguishes are an operator who typed the
// reset and meant it, and an agent or order resetting a session someone else
// is attached to; nothing observable tells them apart, but --force does. The
// shape matches the precedent already in this tree (--force on gc convoy
// land, gc sling, gc stop) and in bd close's refusal of human-assigned beads.
//
// WHAT IS DELIBERATELY NOT PINNED: that a false NEGATIVE from the attachment
// probe is caught. It is not. An unobserved attachment still resets, exactly
// as it does on the drift path, and narrowing that is a separate decision
// about the probe rather than about this command.

// resetAttachmentFixture is one city holding one awake named session, with a
// Fake provider whose attachment state the caller sets.
//
// The provider is INJECTED rather than selected with GC_SESSION=fake: the test
// needs the same *Fake the command will observe through, so it can set
// attachment on it and read back which names it was asked about. A fake
// reached only by environment would be a different instance.
type resetAttachmentFixture struct {
	cityDir  string
	store    beads.Store
	beadID   string
	name     string
	identity string
	fake     *runtime.Fake
}

func newResetAttachmentFixture(t *testing.T, slug string) *resetAttachmentFixture {
	t.Helper()
	t.Setenv("GC_BEADS", "file")
	t.Setenv("GC_SESSION", "fake")

	cityDir := shortSocketTempDir(t, "gc-reset-attached-"+slug+"-")
	t.Setenv("GC_CITY", cityDir)
	writeGenericNamedSessionCityTOML(t, cityDir)
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o755); err != nil {
		t.Fatalf("MkdirAll(.gc): %v", err)
	}

	store, err := openCityStoreAt(cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt: %v", err)
	}
	const sessionName = "s-gc-reset-attached"
	const namedIdentity = "session-a"
	bead, err := store.Create(beads.Bead{
		Title:  "manual session",
		Type:   session.BeadType,
		Labels: []string{session.LabelSession, "template:worker"},
		Metadata: map[string]string{
			// The identity is the one writeGenericNamedSessionCityTOML
			// configures: a bead carrying the named-session markers resolves
			// through the config, so an alias with no [[named_session]] entry
			// fails resolution before the guard under test is ever reached --
			// and a refusal test would then pass on the wrong refusal.
			"alias":                      namedIdentity,
			"template":                   "session-a",
			"session_name":               sessionName,
			"state":                      "awake",
			namedSessionMetadataKey:      "true",
			namedSessionIdentityMetadata: namedIdentity,
			"restart_requested":          "",
			"continuation_reset_pending": "",
		},
	})
	if err != nil {
		t.Fatalf("store.Create(session bead): %v", err)
	}

	fake := runtime.NewFake()
	// The runtime has to be RUNNING before attachment means anything:
	// Manager.ObserveRuntimeForInfo reads IsAttached only for a live session,
	// because a terminal cannot be attached to a runtime that is not there.
	// A fixture that set attachment without starting the session would report
	// detached and every refusal case here would go green on the wrong path.
	if err := fake.Start(context.Background(), sessionName, runtime.Config{}); err != nil {
		t.Fatalf("fake.Start(%q): %v", sessionName, err)
	}
	oldBuild := buildSessionProviderByName
	buildSessionProviderByName = func(*config.City, string, config.SessionConfig, string, string) (runtime.Provider, error) {
		return fake, nil
	}
	t.Cleanup(func() { buildSessionProviderByName = oldBuild })

	lis, err := startControllerSocket(
		cityDir,
		controllerHostingStandalone,
		func() {},
		nil,
		nil,
		make(chan reloadRequest),
		make(chan convergenceRequest, 1),
		make(chan struct{}, 1),
		make(chan struct{}, 1),
	)
	if err != nil {
		t.Fatalf("startControllerSocket: %v", err)
	}
	t.Cleanup(func() {
		lis.Close()                              //nolint:errcheck
		os.Remove(controllerSocketPath(cityDir)) //nolint:errcheck
	})

	return &resetAttachmentFixture{
		cityDir:  cityDir,
		store:    store,
		beadID:   bead.ID,
		name:     sessionName,
		identity: namedIdentity,
		fake:     fake,
	}
}

// resetRequested reports whether the session bead carries the markers
// Manager.RequestFreshRestart writes. Reading the markers rather than
// counting provider calls is deliberate: the markers are what the reconciler
// acts on, so their absence is the only evidence that nothing was destroyed.
func (f *resetAttachmentFixture) resetRequested(t *testing.T) bool {
	t.Helper()
	reloaded, err := openCityStoreAt(f.cityDir)
	if err != nil {
		t.Fatalf("openCityStoreAt(reload): %v", err)
	}
	got, err := reloaded.Get(f.beadID)
	if err != nil {
		t.Fatalf("store.Get(session bead): %v", err)
	}
	return got.Metadata["restart_requested"] == "true" ||
		got.Metadata["continuation_reset_pending"] == "true"
}

// TestSessionResetRefusesWhileAnOperatorIsAttached: an attached session is not
// reset, and nothing about it is mutated on the way to the refusal.
//
// Constructed against the session bead's own markers rather than the command's
// exit status alone, because an exit status says only that the command
// declined to report success -- a reset that had already written
// restart_requested and then failed on something later would satisfy an
// exit-status assertion while the operator's context was already forfeit.
func TestSessionResetRefusesWhileAnOperatorIsAttached(t *testing.T) {
	f := newResetAttachmentFixture(t, "refuse")
	f.fake.SetAttached(f.name, true)

	var stdout, stderr bytes.Buffer
	code := cmdSessionReset([]string{f.identity}, &stdout, &stderr, sessionResetOptions{})
	if code == 0 {
		t.Fatalf("cmdSessionReset(attached) = 0, want nonzero; stdout=%s", stdout.String())
	}
	if f.resetRequested(t) {
		t.Fatal("attached session carries a fresh-restart marker: the refusal did not prevent the reset")
	}
	if msg := stderr.String(); !strings.Contains(msg, "--force") {
		t.Fatalf("refusal = %q, want it to name --force -- an operator who meant the reset must be told how to proceed", msg)
	}
}

// TestSessionResetWithForceProceedsWhileAttached: --force is the escape, and
// it works.
//
// Without this case a fix that refuses unconditionally passes the refusal
// test above and removes the command's whole purpose.
func TestSessionResetWithForceProceedsWhileAttached(t *testing.T) {
	f := newResetAttachmentFixture(t, "force")
	f.fake.SetAttached(f.name, true)

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{f.identity}, &stdout, &stderr, sessionResetOptions{Force: true}); code != 0 {
		t.Fatalf("cmdSessionReset(attached, --force) = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !f.resetRequested(t) {
		t.Fatal("--force did not request a fresh restart: the escape hatch does not reach the reset")
	}
}

// TestSessionResetStillResetsADetachedSession: the ordinary path is unchanged.
//
// The pair with the refusal test is what makes either mean anything. A guard
// that always refuses and a guard that never refuses each satisfy exactly one
// of them.
func TestSessionResetStillResetsADetachedSession(t *testing.T) {
	f := newResetAttachmentFixture(t, "detached")
	f.fake.SetAttached(f.name, false)

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{f.identity}, &stdout, &stderr, sessionResetOptions{}); code != 0 {
		t.Fatalf("cmdSessionReset(detached) = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !f.resetRequested(t) {
		t.Fatal("detached session was not reset: the guard is refusing sessions nobody is attached to")
	}
}

// TestSessionResetRefusalLeavesTheCircuitBreakerTripped: a refused reset has
// no side effects at all.
//
// This is not a redundant restatement of the refusal test. The command clears
// a tripped named-session respawn breaker BEFORE it asks for the restart, so a
// guard placed after that clear would refuse the reset and still have
// re-armed respawn for a session the operator is sitting in -- a mutation the
// operator never asked for and cannot see. The breaker is the only observable
// side effect on the path, so it is the one that pins the ordering.
func TestSessionResetRefusalLeavesTheCircuitBreakerTripped(t *testing.T) {
	f := newResetAttachmentFixture(t, "breaker")
	f.fake.SetAttached(f.name, true)

	cb := newSessionCircuitBreaker(sessionCircuitBreakerConfig{
		Window:      30 * time.Minute,
		MaxRestarts: 3,
	})
	restore := setSessionCircuitBreakerForTest(cb)
	defer restore()
	now := time.Date(2026, 9, 15, 5, 9, 0, 0, time.UTC)
	for i := 0; i < 4; i++ {
		cb.RecordRestart(f.identity, now.Add(time.Duration(i)*time.Second))
	}
	if !cb.IsOpen(f.identity, now.Add(time.Minute)) {
		t.Fatal("precondition: expected the breaker OPEN after 4 restarts")
	}

	var stdout, stderr bytes.Buffer
	if code := cmdSessionReset([]string{f.identity}, &stdout, &stderr, sessionResetOptions{}); code == 0 {
		t.Fatalf("cmdSessionReset(attached) = 0, want nonzero; stdout=%s", stdout.String())
	}
	if !cb.IsOpen(f.identity, now.Add(time.Minute)) {
		t.Fatal("refused reset cleared the respawn circuit breaker: the guard runs after a mutation it should precede")
	}
}

// TestSessionResetRefusesWhenAttachmentCannotBeObserved: an unreadable probe
// refuses rather than proceeding.
//
// Driven through the pure verdict rather than the command, because the only
// way to make the live probe error is to delete the session bead between
// resolution and observation -- a real race, but one that cannot be staged
// from outside the command without weakening something on the path to reach
// it. Splitting the judgment out is what makes the branch reachable at all.
//
// The direction is the whole point: "I could not tell" must read as attached.
// The opposite default is the one that destroyed a conversation.
func TestSessionResetRefusesWhenAttachmentCannotBeObserved(t *testing.T) {
	cases := []struct {
		name       string
		attached   bool
		observeErr error
		force      bool
		wantRefuse bool
	}{
		{name: "attached", attached: true, wantRefuse: true},
		{name: "detached", attached: false, wantRefuse: false},
		{name: "unobservable", observeErr: errors.New("session bead vanished"), wantRefuse: true},
		{name: "attached with force", attached: true, force: true, wantRefuse: false},
		{name: "unobservable with force", observeErr: errors.New("session bead vanished"), force: true, wantRefuse: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verdict := sessionResetAttachmentVerdict(tc.attached, tc.observeErr, tc.force)
			if verdict.Refuse != tc.wantRefuse {
				t.Fatalf("sessionResetAttachmentVerdict(%v, %v, force=%v).Refuse = %v, want %v",
					tc.attached, tc.observeErr, tc.force, verdict.Refuse, tc.wantRefuse)
			}
			if verdict.Refuse && strings.TrimSpace(verdict.Reason) == "" {
				t.Fatal("a refusal with no reason tells the operator nothing about which condition stopped it")
			}
		})
	}
}
