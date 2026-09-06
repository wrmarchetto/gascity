package main

// Idle-scope reaping for managed dolt sql-servers (ci-sptsk3).
//
// The scope watchdog next door reaps a server whose scope was DELETED. That
// leaves the complementary leak open: a scope that still exists on disk but
// that nothing will ever use again. Measured 2026-09-05 on the dev box, four
// worktree-scoped servers had been running 13-22 days for agent sessions that
// had long since ended -- 243MB RSS and four idle listeners, growing by one
// per retired or suspended slot with nothing to bound it.
//
// "Nobody will use this again" is decided from two independent observations,
// and BOTH must say unused before the server is reaped:
//
//   1. No established client connection to the server. This is the only
//      signal that sees a client which never stands in the scope: the
//      supervisor (`gc supervisor run`) was measured holding ~20 connections
//      to the city server from cwd /home/willie/gascity with no GC_* scope
//      variable in its environment at all, so it is invisible to (2).
//   2. No live process claiming the scope by PATH -- cwd inside it, or
//      GC_CITY/GC_CITY_PATH naming it. This is the signal that survives the
//      gap between two commands of a live session, which (1) cannot: the
//      managed config sets wait_timeout=30s, so a quiet session's sockets are
//      gone within half a minute of its last command.
//
// Rejected: connection count alone, the obvious one-liner. A live session
// legitimately holds zero connections between two commands, so on its own it
// reaps working servers on an idle beat.
//
// Rejected: keying liveness on the AGENT NAME that owns the slot. Measured on
// the same sweep, the 22-day orphan served /.gc/worktrees/city/toolsmith-1
// while a live agent named toolsmith-1 was working in
// /.gc/worktrees/gascity/toolsmith-1 -- same name, different tree. Anything
// reasoning by name concludes that server is in use. The scope PATH is the
// only safe key.
//
// Both observations degrade toward "in use" when the host cannot be read
// (no /proc, unreadable stat), so a degraded box never reaps a healthy
// server. On a host with no /proc the idle reap is therefore inert and only
// the scope-deletion reap remains.
//
// Rejected: asking gc's own session registry. Session beads carry worker_dir
// (internal/beadmeta/keys.go), which is exactly a path-keyed session-to-scope
// mapping, and it is the first thing a future editor will reach for. Two
// concrete failures. It is a query against the beads store, which for the
// city scope IS the server being supervised, so the watchdog would depend on
// the process it exists to kill and would read "no session" for every scope
// whose server had already wedged. And it is currently unable to answer for
// these very paths: the reconciler logs "not pruning worker_dir
// .../.gc/worktrees/city/bench-engineer: rig path unresolved" because no rig
// named `city` exists in city.toml, so a reaper built on it inherits that
// gate. The process table has neither problem and is the source of truth for
// what is running.
//
// A false reap is cheap and self-correcting, which is what makes the whole
// mechanism affordable: the beads provider calls `gc dolt-state
// recover-managed` before every operation (examples/bd/assets/scripts/
// gc-beads-bd.sh), and that path starts a fresh server whenever
// assessExistingManagedDolt reports the existing one is not reusable.

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// managedDoltScopeIdleReapEnv disables idle-scope reaping when set to
	// "0". Scope-DELETION reaping is unaffected -- that is the watchdog's
	// original job and has its own switch (GC_DOLT_SCOPE_WATCHDOG).
	managedDoltScopeIdleReapEnv = "GC_DOLT_SCOPE_IDLE_REAP"

	// managedDoltScopeIdleWindowEnv overrides the idle window in
	// milliseconds. A timing knob, in the same class as the watchdog's poll
	// interval override -- it selects no branch that is otherwise
	// unreachable, it only shrinks the wait so a test does not run for half
	// an hour.
	managedDoltScopeIdleWindowEnv = "GC_DOLT_SCOPE_IDLE_MS"

	// managedDoltScopeIdleDefaultWindow is how long both signals must read
	// unused before the server is terminated.
	//
	// The floor is set by signal (1): the managed config gives dolt
	// wait_timeout=30s and read_timeout_millis=15000 (cmd_dolt_config.go),
	// so a live session's sockets vanish within ~30s of its last command and
	// the connection signal reads "unused" for the whole of any longer think
	// time. Thirty minutes is ~60x that floor and longer than any single
	// agent turn measured on this city, so signal (2) is what actually
	// decides the common case and signal (1) never has to carry it alone.
	managedDoltScopeIdleDefaultWindow = 30 * time.Minute

	// procNetTCPStateEstablished is the st field value for ESTABLISHED in
	// /proc/net/tcp (Linux TCP_ESTABLISHED = 1, printed as two hex digits).
	// Deliberately NOT a set of "active-ish" states: a socket in FIN_WAIT or
	// TIME_WAIT belongs to a client that has already gone, and counting
	// those would hold a dead scope open for the kernel's linger window on
	// every poll.
	procNetTCPStateEstablished = "01"
)

// managedDoltScopeUse is one poll's evidence about whether a managed dolt
// server's scope is still in use.
//
// Clients counts established connections to the server; Claimants counts live
// processes that name the scope by path. managedDoltScopeUseUnknown in either
// field means the host could not be observed at all -- no /proc, an
// unreadable table -- and is NOT the same as a zero count.
type managedDoltScopeUse struct {
	Clients   int
	Claimants int
}

// managedDoltScopeUseUnknown marks a count that could not be observed. It is
// negative so that the "either signal is non-zero protects" rule in
// managedDoltScopeUnused covers unknown without a separate branch.
const managedDoltScopeUseUnknown = -1

// managedDoltScopeUnused reports whether both signals say the scope is
// finished. Any non-zero count protects, and unknown is non-zero by
// construction, so a scope is reaped only on two positive observations of
// absence.
func managedDoltScopeUnused(use managedDoltScopeUse) bool {
	return use.Clients == 0 && use.Claimants == 0
}

// managedDoltScopeIdleReapEnabled reports whether idle-scope reaping is on.
// Default on; opt out with GC_DOLT_SCOPE_IDLE_REAP=0.
func managedDoltScopeIdleReapEnabled() bool {
	return strings.TrimSpace(os.Getenv(managedDoltScopeIdleReapEnv)) != "0"
}

// managedDoltScopeIdleWindow resolves the idle window, honoring the
// millisecond test override when it parses to a positive value.
func managedDoltScopeIdleWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv(managedDoltScopeIdleWindowEnv))
	if raw == "" {
		return managedDoltScopeIdleDefaultWindow
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return managedDoltScopeIdleDefaultWindow
	}
	return time.Duration(ms) * time.Millisecond
}

// managedDoltScopeClientCount counts established connections served by the
// dolt process itself, by socket inode rather than by port.
//
// Going through the process's own fds rather than a port number means the
// watchdog needs no argv or config change to learn the port, and the answer
// stays correct if the server ever rebinds. Returns
// managedDoltScopeUseUnknown when the fd directory or both /proc/net tables
// cannot be read.
func managedDoltScopeClientCount(doltPID int) int {
	if doltPID <= 0 {
		return managedDoltScopeUseUnknown
	}
	inodes, ok := socketInodesForPID(doltPID)
	if !ok {
		return managedDoltScopeUseUnknown
	}
	if len(inodes) == 0 {
		return 0
	}
	count := 0
	read := false
	for _, path := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		read = true
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 || fields[3] != procNetTCPStateEstablished {
				continue
			}
			if _, ok := inodes[fields[9]]; ok {
				count++
			}
		}
	}
	if !read {
		return managedDoltScopeUseUnknown
	}
	return count
}

// socketInodesForPID collects the socket inodes open by a process. The bool
// is false when /proc/<pid>/fd could not be listed at all, which the caller
// reports as unknown rather than as zero.
func socketInodesForPID(pid int) (map[string]struct{}, bool) {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, false
	}
	inodes := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
			continue
		}
		inodes[strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")] = struct{}{}
	}
	return inodes, true
}

// managedDoltScopeClaimantCount counts live processes that claim scopePath:
// standing in it (cwd inside the scope) or addressed to it (GC_CITY_PATH or
// GC_CITY naming it exactly). Processes in exclude are skipped.
//
// The exclusion list is load-bearing, not hygiene. The watchdog and the dolt
// server it supervises are both spawned with the environment of the gc
// process that started them, so both were measured carrying
// GC_CITY=<their own scope>. Without the exclusion every scope claims itself
// forever and the reap can never fire.
//
// cwd matching is containment (a subdirectory counts) because a command run
// anywhere below the scope resolves back to it; env matching is exact,
// because GC_CITY names one scope and a nested scope is a different one.
//
// A process whose cwd and environ are both unreadable -- another user's, or
// one that exited mid-scan -- is skipped rather than counted or treated as
// unknown. That leans toward reaping, and it is safe only because it is one
// half of a conjunction: a foreign-user process that is actually using this
// server still holds a socket, and managedDoltScopeClientCount sees the
// socket from the SERVER's side without ever touching the client's /proc
// entry. Returns managedDoltScopeUseUnknown only when /proc itself cannot be
// listed.
func managedDoltScopeClaimantCount(scopePath string, exclude []int) int {
	if strings.TrimSpace(scopePath) == "" {
		return managedDoltScopeUseUnknown
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return managedDoltScopeUseUnknown
	}
	skip := make(map[int]struct{}, len(exclude))
	for _, pid := range exclude {
		skip[pid] = struct{}{}
	}
	count := 0
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := skip[pid]; ok {
			continue
		}
		if processClaimsDoltScope(pid, scopePath) {
			count++
		}
	}
	return count
}

// processClaimsDoltScope reports whether one process stands in, or is
// addressed to, scopePath. cwd is checked first because it is a single
// readlink; the environment is read only when the cheap check misses.
func processClaimsDoltScope(pid int, scopePath string) bool {
	procDir := filepath.Join("/proc", strconv.Itoa(pid))
	if cwd, err := os.Readlink(filepath.Join(procDir, "cwd")); err == nil {
		if pathWithinOrSame(cwd, scopePath) {
			return true
		}
	}
	data, err := os.ReadFile(filepath.Join(procDir, "environ"))
	if err != nil {
		return false
	}
	want := normalizePathForCompare(strings.TrimSpace(scopePath))
	for _, entry := range strings.Split(string(data), "\x00") {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key != "GC_CITY_PATH" && key != "GC_CITY" {
			continue
		}
		if normalizePathForCompare(strings.TrimSpace(value)) == want {
			return true
		}
	}
	return false
}

// observeManagedDoltScopeUse gathers both signals for one decision, cheap
// one first. The claimant scan walks every entry in /proc and reads an
// environment per process, so it runs only when the connection count has
// already said zero -- which, over the life of a busy server, is almost
// never.
func observeManagedDoltScopeUse(scopePath string, doltPID, watchdogPID int) managedDoltScopeUse {
	use := managedDoltScopeUse{Clients: managedDoltScopeClientCount(doltPID)}
	if use.Clients != 0 {
		// Any client at all, or an unobservable host, settles the decision;
		// the expensive half would change nothing.
		use.Claimants = managedDoltScopeUseUnknown
		return use
	}
	use.Claimants = managedDoltScopeClaimantCount(scopePath, []int{doltPID, watchdogPID})
	return use
}
