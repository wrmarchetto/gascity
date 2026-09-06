package main

// Scope: the idle-scope reap decision in dolt_scope_idle.go and its wiring
// into the watchdog supervise loop -- the pure predicate, both /proc
// observers against real sockets and real processes, and the end-to-end
// supervision behavior with the scope config PRESENT throughout (so nothing
// here can pass on the scope-DELETION path in dolt_scope_watchdog_test.go,
// which owns that half).
//
// Run: go test ./cmd/gc -run 'ManagedDoltScope(Idle|Unused|Client|Claimant)'
//
// Delegated elsewhere: whether a reaped server comes back is
// assessExistingManagedDolt's contract (dolt_existing_managed.go), exercised
// by the recover-managed tests, not re-proved here.
//
// What this suite CANNOT represent: the production window is thirty minutes
// and every test here shrinks it to milliseconds, so it pins the decision and
// the wiring, never the calibration. It also cannot see a real `dolt` process
// -- the stand-in never binds a socket -- so the client-count observer is
// proved against sockets this test process owns instead.

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestManagedDoltScopeUnusedNeedsBothSignalsAbsent pins the conjunction: a
// scope is finished only when the connection count AND the claimant count
// both positively observed zero. The unknown rows are the ones that matter --
// an unobservable host must read as in-use, not as empty, or a box with no
// /proc reaps every server it supervises.
func TestManagedDoltScopeUnusedNeedsBothSignalsAbsent(t *testing.T) {
	cases := []struct {
		name string
		use  managedDoltScopeUse
		want bool
	}{
		{"no clients and no claimants is unused", managedDoltScopeUse{Clients: 0, Claimants: 0}, true},
		{"a client protects", managedDoltScopeUse{Clients: 1, Claimants: 0}, false},
		{"a claimant protects", managedDoltScopeUse{Clients: 0, Claimants: 1}, false},
		{"both present protects", managedDoltScopeUse{Clients: 3, Claimants: 2}, false},
		{"unknown clients protects", managedDoltScopeUse{Clients: managedDoltScopeUseUnknown, Claimants: 0}, false},
		{"unknown claimants protects", managedDoltScopeUse{Clients: 0, Claimants: managedDoltScopeUseUnknown}, false},
		{"both unknown protects", managedDoltScopeUse{Clients: managedDoltScopeUseUnknown, Claimants: managedDoltScopeUseUnknown}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := managedDoltScopeUnused(tc.use); got != tc.want {
				t.Errorf("managedDoltScopeUnused(%+v) = %v, want %v", tc.use, got, tc.want)
			}
		})
	}
}

// TestManagedDoltScopeIdleWindow pins the timing override, including that a
// malformed or non-positive value falls back to the production window rather
// than to something that reaps immediately.
func TestManagedDoltScopeIdleWindow(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", managedDoltScopeIdleDefaultWindow},
		{"250", 250 * time.Millisecond},
		{"0", managedDoltScopeIdleDefaultWindow},
		{"-5", managedDoltScopeIdleDefaultWindow},
		{"nonsense", managedDoltScopeIdleDefaultWindow},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv(managedDoltScopeIdleWindowEnv, tc.env)
			if got := managedDoltScopeIdleWindow(); got != tc.want {
				t.Errorf("idle window for %q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestManagedDoltScopeIdleReapEnabled(t *testing.T) {
	cases := []struct {
		env  string
		want bool
	}{
		{"", true},
		{"1", true},
		{"0", false},
		{" 0 ", false},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv(managedDoltScopeIdleReapEnv, tc.env)
			if got := managedDoltScopeIdleReapEnabled(); got != tc.want {
				t.Errorf("idle reap enabled for %q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

// TestManagedDoltScopeClientCountCountsEstablishedNotListening drives real
// sockets rather than a table fixture, because the defect this observer
// exists to avoid is a state-code confusion in /proc/net/tcp and a fixture
// would simply restate whichever code the implementation picked.
//
// It asserts on the DELTA from a baseline: the count is every established
// socket the pid owns, and a test binary may hold unrelated ones. Both
// endpoints of a loopback connection live in this same process, so accepting
// one connection moves the count by exactly two -- a figure that also proves
// the observer is not accidentally counting one endpoint twice or matching on
// the port instead of the inode.
func TestManagedDoltScopeClientCountCountsEstablishedNotListening(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc socket accounting is Linux-only")
	}
	self := os.Getpid()
	base := managedDoltScopeClientCount(self)
	if base < 0 {
		t.Fatalf("client count for this live process reported unknown (%d); /proc is required for this suite", base)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck

	// A listening socket is not a client. This is the assertion that dies if
	// the state filter ever widens to include 0A (LISTEN).
	if got := managedDoltScopeClientCount(self); got != base {
		t.Fatalf("a listening socket changed the client count: %d, want the %d baseline", got, base)
	}

	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}

	if got := waitForManagedDoltScopeClientCount(t, self, base+2); got != base+2 {
		t.Fatalf("established loopback pair gave client count %d, want %d (baseline %d + both endpoints)", got, base+2, base)
	}

	_ = client.Close()
	_ = server.Close()
	if got := waitForManagedDoltScopeClientCount(t, self, base); got != base {
		t.Fatalf("client count stayed at %d after both endpoints closed, want the %d baseline", got, base)
	}
}

func TestManagedDoltScopeClientCountUnknownForADeadProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc socket accounting is Linux-only")
	}
	// A pid that cannot be inspected must never read as an idle zero.
	if got := managedDoltScopeClientCount(unusedPIDForScopeIdleTest(t)); got != managedDoltScopeUseUnknown {
		t.Fatalf("client count for an absent pid = %d, want unknown (%d)", got, managedDoltScopeUseUnknown)
	}
	if got := managedDoltScopeClientCount(0); got != managedDoltScopeUseUnknown {
		t.Fatalf("client count for pid 0 = %d, want unknown (%d)", got, managedDoltScopeUseUnknown)
	}
}

// TestManagedDoltScopeClaimantCountKeysOnScopePathNotAgentName is the bead's
// central case (ci-sptsk3): the 22-day orphan served
// .gc/worktrees/city/toolsmith-1 while a live agent NAMED toolsmith-1 worked
// in .gc/worktrees/gascity/toolsmith-1. The two scopes share a basename and
// nothing else, so a process standing in one must not claim the other.
func TestManagedDoltScopeClaimantCountKeysOnScopePathNotAgentName(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc cwd and environ inspection is Linux-only")
	}
	root := t.TempDir()
	scope := filepath.Join(root, "city", "toolsmith-1")
	sibling := filepath.Join(root, "gascity", "toolsmith-1")
	for _, dir := range []string{scope, sibling} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	if got := managedDoltScopeClaimantCount(scope, nil); got != 0 {
		t.Fatalf("claimant count for an untouched scope = %d, want 0", got)
	}

	// Same basename, different tree: this must NOT claim the scope.
	startScopeIdleClaimant(t, sibling, nil)
	if got := managedDoltScopeClaimantCount(scope, nil); got != 0 {
		t.Fatalf("a process in the same-named sibling tree claimed the scope: count %d, want 0", got)
	}

	// Standing inside the scope claims it, and so does standing in a
	// subdirectory of it -- a command run below the scope resolves back to it.
	stander := startScopeIdleClaimant(t, scope, nil)
	if got := managedDoltScopeClaimantCount(scope, nil); got != 1 {
		t.Fatalf("a process standing in the scope gave claimant count %d, want 1", got)
	}
	nested := filepath.Join(scope, "nested", "deeper")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", nested, err)
	}
	startScopeIdleClaimant(t, nested, nil)
	if got := managedDoltScopeClaimantCount(scope, nil); got != 2 {
		t.Fatalf("a process in a subdirectory of the scope gave claimant count %d, want 2", got)
	}

	// The exclusion list is what keeps the watchdog and its own server from
	// claiming the scope they supervise forever; without it the reap can
	// never fire in production, where both inherit GC_CITY from the gc
	// process that started them.
	if got := managedDoltScopeClaimantCount(scope, []int{stander}); got != 1 {
		t.Fatalf("excluding one of two standers gave claimant count %d, want 1", got)
	}
}

// TestManagedDoltScopeClaimantCountSeesASessionAddressedToTheScope covers the
// client that never stands in the scope: gc stamps GC_CITY_PATH/GC_CITY on
// the sessions it launches, and a `gc ... --city <scope>` run from a sibling
// worktree was measured on this host (a nudge poll in city/bench-engineer
// addressed to the city root). Matching there is exact, not containment: a
// nested scope is a different scope.
func TestManagedDoltScopeClaimantCountSeesASessionAddressedToTheScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc cwd and environ inspection is Linux-only")
	}
	root := t.TempDir()
	scope := filepath.Join(root, "scope")
	elsewhere := filepath.Join(root, "elsewhere")
	nested := filepath.Join(scope, "nested")
	for _, dir := range []string{scope, elsewhere, nested} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	for _, key := range []string{"GC_CITY_PATH", "GC_CITY"} {
		t.Run(key, func(t *testing.T) {
			startScopeIdleClaimant(t, elsewhere, []string{key + "=" + scope})
			if got := managedDoltScopeClaimantCount(scope, nil); got != 1 {
				t.Fatalf("%s naming the scope from outside it gave claimant count %d, want 1", key, got)
			}
		})
	}

	// A nested path is a DIFFERENT scope, so an env naming it must not
	// claim this one. Containment here would make every parent city
	// permanently claimed by any worktree session below it.
	fresh := filepath.Join(root, "fresh")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", fresh, err)
	}
	startScopeIdleClaimant(t, elsewhere, []string{"GC_CITY_PATH=" + filepath.Join(fresh, "nested")})
	if got := managedDoltScopeClaimantCount(fresh, nil); got != 0 {
		t.Fatalf("GC_CITY_PATH naming a nested path claimed its parent scope: count %d, want 0", got)
	}
}

func TestManagedDoltScopeClaimantCountUnknownWithoutAScopePath(t *testing.T) {
	// An empty scope path is the shape the watchdog gets when it was spawned
	// with no city (the test harness does this). It must read unknown, so the
	// conjunction protects, rather than zero, which would reap.
	for _, path := range []string{"", "   "} {
		if got := managedDoltScopeClaimantCount(path, nil); got != managedDoltScopeUseUnknown {
			t.Fatalf("claimant count for scope path %q = %d, want unknown (%d)", path, got, managedDoltScopeUseUnknown)
		}
	}
}

// TestObserveManagedDoltScopeUseSkipsTheClaimantScanWhenClientsExist pins the
// laziness that makes the whole mechanism affordable: the claimant scan walks
// every entry in /proc, so it must not run while the server has clients. The
// observable proof is the unknown claimant count -- a real scan of this
// process's own scope would have returned a number.
func TestObserveManagedDoltScopeUseSkipsTheClaimantScanWhenClientsExist(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("/proc socket accounting is Linux-only")
	}
	scope := t.TempDir()
	self := os.Getpid()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close() //nolint:errcheck
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close() //nolint:errcheck
	server, err := listener.Accept()
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	defer server.Close() //nolint:errcheck
	waitForManagedDoltScopeClientCountAtLeast(t, self, 1)

	use := observeManagedDoltScopeUse(scope, self, self)
	if use.Clients <= 0 {
		t.Fatalf("observed clients = %d, want a positive count", use.Clients)
	}
	if use.Claimants != managedDoltScopeUseUnknown {
		t.Fatalf("claimant scan ran while clients existed: count %d, want unknown (%d)", use.Claimants, managedDoltScopeUseUnknown)
	}
	if managedDoltScopeUnused(use) {
		t.Fatal("a scope with an established client read as unused")
	}
}

// waitForScopeIdleCondition polls cond until it holds or the timeout passes,
// and reports whether it fired.
//
// Every wait in this suite is this same shape, so they share one call site
// rather than open-coding a loop each -- which also keeps the file's
// fixed-sleep footprint down to the waits that are genuinely "assert nothing
// happened for this long" and cannot be polled at all.
func waitForScopeIdleCondition(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitForManagedDoltScopeClientCount polls until the count reaches want or
// the deadline passes, returning the last reading. Socket teardown is not
// synchronous with Close, so a single sample after a close is a race.
func waitForManagedDoltScopeClientCount(t *testing.T, pid, want int) int {
	t.Helper()
	got := managedDoltScopeClientCount(pid)
	waitForScopeIdleCondition(5*time.Second, func() bool {
		got = managedDoltScopeClientCount(pid)
		return got == want
	})
	return got
}

func waitForManagedDoltScopeClientCountAtLeast(t *testing.T, pid, want int) {
	t.Helper()
	if !waitForScopeIdleCondition(5*time.Second, func() bool {
		return managedDoltScopeClientCount(pid) >= want
	}) {
		t.Fatalf("client count for pid %d never reached %d", pid, want)
	}
}

// startScopeIdleClaimant runs a real process with the given working directory
// and extra environment, and returns its pid. It is a shell blocked on a read
// of a pipe this test holds open, so it needs no external command beyond
// /bin/sh and dies when the test closes the pipe.
//
// A real process is the point: /proc/<pid>/environ is a snapshot of the
// process's INITIAL stack, so os.Setenv inside this test process would not
// appear there and a self-referential fixture would pin nothing.
func startScopeIdleClaimant(t *testing.T, dir string, extraEnv []string) int {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command("/bin/sh", "-c", `printf ok > "$1"; read line`, "sh", ready)
	cmd.Dir = dir
	cmd.Env = append(sanitizedBaseEnv(), extraEnv...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("claimant stdin pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start claimant in %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	if !waitForScopeIdleCondition(10*time.Second, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	}) {
		t.Fatalf("claimant in %s never reported ready", dir)
	}
	return cmd.Process.Pid
}

// unusedPIDForScopeIdleTest returns a pid that is not running. It reads the
// kernel's pid ceiling and picks a value above it, which no process can ever
// hold, rather than guessing at a high number that a busy host may have
// wrapped onto.
func unusedPIDForScopeIdleTest(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("/proc/sys/kernel/pid_max")
	if err != nil {
		t.Fatalf("read pid_max: %v", err)
	}
	ceiling, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse pid_max %q: %v", string(data), err)
	}
	return ceiling + 1
}

// TestManagedDoltScopeWatchdogReapsAScopeNothingIsUsing drives the whole
// production supervision loop with the scope config PRESENT for the entire
// run, so a pass cannot come from the scope-deletion path. The control half
// is the second subtest: an otherwise identical run with one real process
// standing in the scope must keep the server alive well past the window, or
// the first half would be satisfied by a watchdog that simply kills its
// server on a timer.
func TestManagedDoltScopeWatchdogReapsAScopeNothingIsUsing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("POSIX process semantics and /proc inspection required")
	}

	t.Run("idle scope is reaped, but not before the window", func(t *testing.T) {
		scope, logPath, doltPID, watchdogPID := startScopeIdleWatchdogHelper(t, nil)
		// The window is what separates this mechanism from a kill timer, so
		// assert it is actually waited out. Measured from AFTER the helper
		// returns, which is conservative: the watchdog's idle clock started
		// at its first poll, one 50ms interval into its life, which is
		// already past. A zero or ignored window reaps within a poll or two
		// of that, well before this check.
		time.Sleep(scopeIdleTestWindow / 2)
		if !pidAlive(doltPID) {
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("fake dolt pid %d was reaped inside the %s idle window; watchdog log:\n%s", doltPID, scopeIdleTestWindow, logData)
		}
		if !waitForScopeIdleCondition(20*time.Second, func() bool { return !pidAlive(doltPID) }) {
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("fake dolt pid %d still alive for unused scope %s; watchdog log:\n%s", doltPID, scope, logData)
		}
		if !waitForScopeIdleCondition(20*time.Second, func() bool { return !pidAlive(watchdogPID) }) {
			t.Fatalf("watchdog pid %d still alive after reaping its server", watchdogPID)
		}
		// The config must still be there: if it is not, this test proved the
		// scope-deletion path and nothing about idleness.
		if _, err := os.Stat(filepath.Join(scope, "dolt-config.yaml")); err != nil {
			t.Fatalf("scope config vanished during the run (%v); this run proves nothing about idle reaping", err)
		}
		logData, _ := os.ReadFile(logPath)
		if !strings.Contains(string(logData), "no client and no claimant") {
			t.Errorf("watchdog log missing the idle-scope termination decision; log:\n%s", logData)
		}
	})

	// The operator opt-out is production behavior, not a test affordance, and
	// a parser test alone leaves it unproved: reading the env into a variable
	// the loop never consults passes that test and reaps anyway. Drive the
	// real loop with it off.
	t.Run("the opt-out keeps an idle scope alive", func(t *testing.T) {
		scope, logPath, doltPID, _ := startScopeIdleWatchdogHelper(t, nil,
			"GC_TEST_MANAGED_DOLT_HELPER_SCOPE_IDLE_REAP=0")
		time.Sleep(3 * scopeIdleTestWindow)
		if !pidAlive(doltPID) {
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("fake dolt pid %d was reaped for idle scope %s with %s=0; watchdog log:\n%s",
				doltPID, scope, managedDoltScopeIdleReapEnv, logData)
		}
	})

	t.Run("a process standing in the scope protects it", func(t *testing.T) {
		scope, logPath, doltPID, _ := startScopeIdleWatchdogHelper(t, func(scope string) {
			startScopeIdleClaimant(t, scope, nil)
		})
		// Several windows' worth: the idle path needs one window plus a poll
		// to fire, so surviving many of them is the assertion.
		time.Sleep(3 * time.Second)
		if !pidAlive(doltPID) {
			logData, _ := os.ReadFile(logPath)
			t.Fatalf("fake dolt pid %d was reaped while a process stood in scope %s; watchdog log:\n%s", doltPID, scope, logData)
		}
	})
}

// scopeIdleTestWindow is the shrunken idle window the end-to-end runs use.
// It has to be long enough that half of it is a meaningful "not yet" check
// against process startup jitter, and short enough that the control subtest
// can outlast several of them without the suite crawling.
const scopeIdleTestWindow = 1500 * time.Millisecond

// startScopeIdleWatchdogHelper spawns a fake dolt server under a real scope
// watchdog whose city path is a fresh scope directory, with the idle window
// shrunk to a fraction of a second. beforeStart runs after the scope
// directory exists and before the watchdog is spawned, so a control can
// occupy the scope first; extraHelperEnv rides into the helper process, which
// re-exports the GC_TEST_ control vars for the watchdog child.
//
// Returns the scope dir, the watchdog log path, and both pids.
func startScopeIdleWatchdogHelper(t *testing.T, beforeStart func(scope string), extraHelperEnv ...string) (string, string, int, int) {
	t.Helper()
	scope := t.TempDir()
	fakeDoltDir := writeFakeDoltSQLServer(t)
	statePath := filepath.Join(scope, "state")
	configPath := filepath.Join(scope, "dolt-config.yaml")
	logPath := filepath.Join(scope, "dolt.log")
	if err := os.WriteFile(configPath, []byte("log_level: debug\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if beforeStart != nil {
		beforeStart(scope)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestManagedDoltScopeWatchdogHelper", "-test.v")
	cmd.Env = sanitizedBaseEnv(
		"GC_TEST_MANAGED_DOLT_HELPER=scope-watchdog",
		"GC_TEST_MANAGED_DOLT_HELPER_STATE="+statePath,
		"GC_TEST_MANAGED_DOLT_HELPER_CONFIG="+configPath,
		"GC_TEST_MANAGED_DOLT_HELPER_LOG="+logPath,
		"GC_TEST_MANAGED_DOLT_HELPER_FAKE_DOLT_DIR="+fakeDoltDir,
		"GC_TEST_MANAGED_DOLT_HELPER_CITY="+scope,
		"GC_TEST_MANAGED_DOLT_HELPER_SCOPE_WD_INTERVAL_MS=50",
		"GC_TEST_MANAGED_DOLT_HELPER_SCOPE_IDLE_MS="+strconv.Itoa(int(scopeIdleTestWindow/time.Millisecond)),
	)
	cmd.Env = append(cmd.Env, extraHelperEnv...)
	// Stand the helper IN the scope, which is what production does: gc
	// resolves the scope from where it is standing, so the watchdog and the
	// dolt server it exec's both inherit a cwd inside the scope and both
	// therefore look like claimants of it. Without this the harness cannot
	// represent the exclusion list at all -- dropping it from
	// observeManagedDoltScopeUse leaves the whole suite green (mutation M13).
	cmd.Dir = scope
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output)
	}
	doltPID, watchdogPID := readManagedDoltTestState(t, statePath)
	t.Cleanup(func() {
		cleanupManagedDoltTestPID(t, doltPID)
		cleanupManagedDoltTestPID(t, watchdogPID)
	})
	return scope, logPath, doltPID, watchdogPID
}
