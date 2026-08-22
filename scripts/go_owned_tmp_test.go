//go:build !windows

package scripts_test

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
