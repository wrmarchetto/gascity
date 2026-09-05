//go:build !windows

package scripts_test

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestMakeTestUsesOwnedGoTempWrapper(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if want := "scripts/with-go-tmp scripts/go-test-observable test"; !strings.Contains(string(makefile), want) {
		t.Fatalf("make test no longer runs through the owned Go temp wrapper; missing %q", want)
	}
}

func TestWithGoTmpRemovesOwnedDirectoryOnTermination(t *testing.T) {
	cmd := exec.Command(filepath.Join(repoRoot(t), "scripts", "with-go-tmp"), "sh", "-c", `printf '%s\n' "$GOTMPDIR"; exec tail -f /dev/null`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start wrapper: %v", err)
	}

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("wrapped command did not report GOTMPDIR")
	}
	gotmpdir := scanner.Text()
	if filepath.Dir(gotmpdir) != "/var/tmp" {
		t.Fatalf("GOTMPDIR parent = %q, want /var/tmp", filepath.Dir(gotmpdir))
	}
	if _, err := os.Stat(gotmpdir); err != nil {
		t.Fatalf("stat owned GOTMPDIR before termination: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("terminate wrapper: %v", err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("wrapper exited successfully after SIGTERM, want signal status")
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait wrapper: %v", err)
		}
	}
	if _, err := os.Stat(gotmpdir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned GOTMPDIR still exists after termination: %v", err)
	}
}

// TestWithGoTmpPropagatesChildExitStatus pins the wrapper's other job. It owns
// the exit status of every `make` target routed through it -- `make test` above
// all -- so a swallowed status turns the project's quality gate into a
// no-op that reports success over a failing suite.
//
// The failure is invisible from the outside, which is why it needs a test
// rather than a reading: the inner runner still prints its own
// "observable go test: FAIL status=1" line and the failure details, so a log
// tail looks exactly like a caught failure while `echo $?` says 0. Measured
// 2026-09-05 on ci-7gg9ra: a real TestTypedClassCodecCensusRatchet failure
// rendered in full and `make test` still exited 0.
//
// Every case runs the real wrapper. A unit test of the shell fragment would
// not catch this, because the bug is in how bash scores the compound
// statement, not in the fragment's own logic.
func TestWithGoTmpPropagatesChildExitStatus(t *testing.T) {
	wrapper := filepath.Join(repoRoot(t), "scripts", "with-go-tmp")
	// 1 is what a failing `go test` returns and is therefore the case that
	// matters; 3 and 42 are there so a wrapper that hardcoded 1 still fails.
	for _, want := range []int{0, 1, 3, 42} {
		cmd := exec.Command(wrapper, "sh", "-c", "exit "+strconv.Itoa(want))
		err := cmd.Run()
		got := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("running wrapper for exit %d: %v", want, err)
			}
			got = exitErr.ExitCode()
		}
		if got != want {
			t.Errorf("with-go-tmp exited %d for a child that exited %d; a swallowed status makes every gate routed through this wrapper report success over a real failure", got, want)
		}
	}
}
