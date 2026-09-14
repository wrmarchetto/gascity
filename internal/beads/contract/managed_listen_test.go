// Ownership tests for the managed-Dolt port check.
//
// Scope: pidHoldsListeningPort and the branch of validManagedRuntimeState
// that consumes it. The suite exists because the check it replaces -- a TCP
// dial -- could only ever answer "something answers on this port", while the
// invariant the caller actually needs is "the process this state file names
// is serving this port". The two differ exactly when a PID is reused, which
// no dial can detect.
//
// Two kinds of test here, and both are load-bearing. The fake-procfs cases
// pin the branch structure, including the cannot-answer cases that must fall
// back to the dial rather than report "not listening". The live case pins the
// PARSER against the kernel's actual /proc/net/tcp formatting, which a
// hand-built tree can never establish -- a fake tree only proves the parser
// agrees with whoever wrote the fixture.
//
// Run: go test ./internal/beads/contract/ -run ManagedPortOwnership
package contract

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// fakeProc builds a procfs tree: one /proc/net/tcp holding the given rows and
// an fd table per pid. Rows are (state, localPortHex, inode) triples written
// in the kernel's column layout, so a parser that counts columns wrongly
// fails here rather than silently reading the wrong field.
type procRow struct {
	state string
	port  int
	inode string
	v6    bool
}

func fakeProc(t *testing.T, rows []procRow, fds map[int][]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "net"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"tcp", "tcp6"} {
		body := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
		for i, r := range rows {
			if (name == "tcp6") != r.v6 {
				continue
			}
			addr := "0100007F"
			if r.v6 {
				addr = "00000000000000000000000001000000"
			}
			body += fmt.Sprintf("   %d: %s:%04X 00000000:0000 %s 00000000:00000000 00:00000000 00000000  1000        0 %s 1 0000000000000000 100 0 0 10 0\n",
				i, addr, r.port, r.state, r.inode)
		}
		if err := os.WriteFile(filepath.Join(root, "net", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for pid, inodes := range fds {
		dir := filepath.Join(root, strconv.Itoa(pid), "fd")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for i, inode := range inodes {
			if err := os.Symlink("socket:["+inode+"]", filepath.Join(dir, strconv.Itoa(i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func withProcRoot(t *testing.T, root string) {
	t.Helper()
	prev := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = prev })
}

func TestManagedPortOwnershipAnswersTrueForThePIDHoldingTheSocket(t *testing.T) {
	withProcRoot(t, fakeProc(t,
		[]procRow{{state: "0A", port: 31155, inode: "4242"}},
		map[int][]string{99: {"11", "4242"}}))

	holds, answered := pidHoldsListeningPort(99, 31155)
	if !answered || !holds {
		t.Fatalf("holds=%v answered=%v, want true/true", holds, answered)
	}
}

func TestManagedPortOwnershipRejectsAPortHeldByAnotherProcess(t *testing.T) {
	// The whole reason this check replaces a dial: the port answers, so a
	// dial would report reachable, but the state file names a PID that does
	// not own it. That is a recycled PID, and the caller must not trust the
	// rest of the state file.
	withProcRoot(t, fakeProc(t,
		[]procRow{{state: "0A", port: 31155, inode: "4242"}},
		map[int][]string{99: {"11"}, 100: {"4242"}}))

	holds, answered := pidHoldsListeningPort(99, 31155)
	if !answered {
		t.Fatal("answered=false; the socket table was readable, so the question was answerable")
	}
	if holds {
		t.Fatal("holds=true for a socket owned by a different PID")
	}
}

func TestManagedPortOwnershipIgnoresNonListeningSocketsOnThePort(t *testing.T) {
	// 01 is ESTABLISHED. A client connection from this process to the port
	// must not read as the process serving it, or a gc process that merely
	// talked to Dolt would vouch for a server that has since died.
	withProcRoot(t, fakeProc(t,
		[]procRow{{state: "01", port: 31155, inode: "4242"}},
		map[int][]string{99: {"4242"}}))

	holds, answered := pidHoldsListeningPort(99, 31155)
	if !answered {
		t.Fatal("answered=false; the socket table was readable")
	}
	if holds {
		t.Fatal("holds=true for an ESTABLISHED socket; only state 0A is a listener")
	}
}

func TestManagedPortOwnershipMatchesTheExactPort(t *testing.T) {
	// Guards the hex parse against a comparison that is not value-equality --
	// 31155 is 0x79B3 and 1971 is 0x07B3, which share a suffix.
	withProcRoot(t, fakeProc(t,
		[]procRow{{state: "0A", port: 1971, inode: "4242"}},
		map[int][]string{99: {"4242"}}))

	holds, answered := pidHoldsListeningPort(99, 31155)
	if !answered || holds {
		t.Fatalf("holds=%v answered=%v; a listener on 1971 is not a listener on 31155", holds, answered)
	}
}

func TestManagedPortOwnershipMatchesListenersOnIPv6(t *testing.T) {
	// Dolt binds v6 loopback on hosts where that is the default resolution of
	// "localhost"; reading only /proc/net/tcp would report the server absent.
	withProcRoot(t, fakeProc(t,
		[]procRow{{state: "0A", port: 31155, inode: "4242", v6: true}},
		map[int][]string{99: {"4242"}}))

	holds, answered := pidHoldsListeningPort(99, 31155)
	if !answered || !holds {
		t.Fatalf("holds=%v answered=%v, want true/true for a v6 listener", holds, answered)
	}
}

func TestManagedPortOwnershipDeclinesToAnswerWithoutASocketTable(t *testing.T) {
	// No procfs at all -- darwin, windows, a restricted container. The caller
	// must fall back to the dial, so this must report cannot-answer and NOT
	// "not listening", which would condemn a healthy server.
	withProcRoot(t, t.TempDir())

	if _, answered := pidHoldsListeningPort(99, 31155); answered {
		t.Fatal("answered=true with no socket table; the caller would skip its dial fallback")
	}
}

func TestManagedPortOwnershipDeclinesToAnswerWhenTheFDTableIsUnreadable(t *testing.T) {
	// The port has a listener but the PID's fd table cannot be read -- the
	// process exited between the two reads, or it runs as another user.
	// Attribution is impossible, so this is cannot-answer, not not-listening.
	//
	// The absent directory stands in for the unreadable one on purpose: a
	// chmod-000 fixture is vacuous when the suite runs as root, and would
	// pass there while testing nothing.
	withProcRoot(t, fakeProc(t,
		[]procRow{{state: "0A", port: 31155, inode: "4242"}},
		map[int][]string{100: {"4242"}}))

	if _, answered := pidHoldsListeningPort(99, 31155); answered {
		t.Fatal("answered=true with no fd table for the PID; the caller would skip its dial fallback")
	}
}

func TestManagedPortOwnershipSeesARealListenerInThisProcess(t *testing.T) {
	// Pins the parser against the kernel's own formatting. Every other case
	// here reads a tree this file wrote, so they agree with the fixture
	// rather than with /proc.
	if runtime.GOOS != "linux" {
		t.Skipf("no procfs socket table on %s", runtime.GOOS)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	holds, answered := pidHoldsListeningPort(os.Getpid(), port)
	if !answered || !holds {
		t.Fatalf("holds=%v answered=%v for a listener this process is holding on :%d", holds, answered, port)
	}
}

func TestValidManagedRuntimeStateSkipsTheDialWhenOwnershipAnswers(t *testing.T) {
	// The point of the change: a managed state file resolved on a host that
	// can attribute the socket must cost the Dolt server no accept at all.
	if runtime.GOOS != "linux" {
		t.Skipf("no procfs socket table on %s", runtime.GOOS)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	dials := 0
	prev := contractPortReachable
	contractPortReachable = func(host, port string) bool {
		dials++
		return prev(host, port)
	}
	t.Cleanup(func() { contractPortReachable = prev })

	city := t.TempDir()
	state := managedRuntimeState{
		Running: true,
		PID:     os.Getpid(),
		Port:    port,
		DataDir: filepath.Join(city, ".beads", "dolt"),
	}
	if !validManagedRuntimeState(state, city) {
		t.Fatal("state rejected for a listener this process holds")
	}
	if dials != 0 {
		t.Fatalf("%d dials; the socket table answered, so the server must see no accept", dials)
	}
}

func TestValidManagedRuntimeStateStillDialsWhenOwnershipCannotAnswer(t *testing.T) {
	// The fallback is what keeps every non-Linux host and every restricted
	// container on the behavior that shipped before the ownership check.
	withProcRoot(t, t.TempDir())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	dials := 0
	prev := contractPortReachable
	contractPortReachable = func(host, port string) bool {
		dials++
		return prev(host, port)
	}
	t.Cleanup(func() { contractPortReachable = prev })

	city := t.TempDir()
	state := managedRuntimeState{
		Running: true,
		PID:     os.Getpid(),
		Port:    port,
		DataDir: filepath.Join(city, ".beads", "dolt"),
	}
	if !validManagedRuntimeState(state, city) {
		t.Fatal("state rejected for a reachable listener")
	}
	if dials != 1 {
		t.Fatalf("%d dials, want 1; with no socket table the dial is the only reachability signal", dials)
	}
}
