package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// stubTestingM stands in for testscript.TestingM so cleanupTestingM's own
// ordering can be driven without running a test binary inside a test binary.
type stubTestingM struct{ code int }

func (s stubTestingM) Run() int { return s.code }

// TestCleanupTestingMKillsTmuxServersWhileTheirSocketsStillExist pins cmd/gc's
// end-of-run half of ci-87655r: the ORDER of kill and remove.
//
// WHY cmd/gc SPECIFICALLY. tmuxtest.KillAllTestSessions documents itself as
// "call from TestMain before and after test runs to clean up orphans", and
// internal/runtime/tmux and test/integration both do. cmd/gc did neither
// while being a package that starts REAL tmux servers under -tags
// integration, so it was the one real-tmux package with no sweep at either
// end. The before-run half is covered by the orphan dir sweep, which now
// kills before it removes; this is the after-run half.
//
// THE ASSERTION IS THAT THE ROOT STILL EXISTS WHEN THE KILL RUNS, checked
// from inside the injected kill. That is the defect stated directly: removing
// first does not stop a server, it leaves it unreachable and alive with no
// socket left to address it by, and a kill wired after the removal can never
// find the sockets it is meant to reap. An after-the-fact check cannot see
// this -- once Run returns, the root is gone in both orderings.
//
// THAT THE KILL ACTUALLY KILLS is a different claim and is proven against a
// real tmux server in test/tmuxtest
// (TestSweepOrphanKillsTmuxServerBeforeRemovingItsSocketDir). Repeating it
// here would spend a real server, a subprocess and a sleep to re-prove
// someone else's property.
func TestCleanupTestingMKillsTmuxServersWhileTheirSocketsStillExist(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "tmux-sock")
	if err := os.WriteFile(socket, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var killedRoots []string
	var socketPresentAtKill []bool
	runner := cleanupTestingM{
		m:         stubTestingM{code: 7},
		killRoots: []string{root},
		paths:     []string{root},
		killServers: func(r string, _ io.Writer) int {
			killedRoots = append(killedRoots, r)
			_, err := os.Stat(socket)
			socketPresentAtKill = append(socketPresentAtKill, err == nil)
			return 0
		},
	}

	if code := runner.Run(); code != 7 {
		t.Fatalf("cleanupTestingM.Run() = %d, want the wrapped runner's 7", code)
	}
	if len(killedRoots) != 1 || killedRoots[0] != root {
		t.Fatalf("killed roots = %v, want exactly [%s]: the end-of-run sweep did not reach cmd/gc's own socket root", killedRoots, root)
	}
	if !socketPresentAtKill[0] {
		t.Fatal("the socket was already gone when the kill ran: removing before killing leaves the server unreachable and alive, which is the ci-87655r orphan")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("socket root %s survived cleanup (stat err=%v)", root, err)
	}
}
