//go:build (linux && !android) || (darwin && !ios)

package productmetrics

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/testutil"
)

// purgeHelperProgress prefixes the line a re-executed purge helper prints
// after its fixture and after each attempt. Other output does not count.
const purgeHelperProgress = "purge-helper-progress: "

// purgeHelperStall bounds the silence between two progress lines. One
// attempt is about 40 fsyncs, measured at up to 1.5s under shared disk load.
const purgeHelperStall = 4 * testutil.ExecRaceTimeout

// errPurgeHelperStalled is returned when a helper prints no progress line
// within the stall bound. The helper has been killed.
var errPurgeHelperStalled = errors.New("purge helper reported no progress")

// reportPurgeHelperProgress prints one progress line from inside a helper.
func reportPurgeHelperProgress(step string) {
	fmt.Fprintf(os.Stderr, "%s%s\n", purgeHelperProgress, step)
}

// watchProgress consumes lines until they close, calling kill once stall
// passes with no progress line. Not a wall-clock bound: a helper's work is a
// fixed fsync count whose latency swings over 10x with disk load on a shared
// host. Returns (last progress step or "none", every line read, stall error).
func watchProgress(lines <-chan string, stall time.Duration, kill func()) (string, string, error) {
	last, output := "none", strings.Builder{}
	timer := time.NewTimer(stall)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return last, output.String(), nil
			}
			output.WriteString(line + "\n")
			if step, found := strings.CutPrefix(line, purgeHelperProgress); found {
				last = step
				timer.Reset(stall)
			}
		case <-timer.C:
			kill()
			return last, output.String(), fmt.Errorf("%w for %v after %s", errPurgeHelperStalled, stall, last)
		}
	}
}

// watchPurgeHelper runs command under watchProgress and then waits for it.
// Returns (last progress step, combined output, stall or exit error).
func watchPurgeHelper(command *exec.Cmd, stall time.Duration) (string, string, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return "none", "", fmt.Errorf("pipe for purge helper output: %w", err)
	}
	defer reader.Close() //nolint:errcheck // read side, nothing to flush
	command.Stdout, command.Stderr = writer, writer
	startErr := command.Start()
	if closeErr := writer.Close(); startErr == nil && closeErr != nil {
		startErr = closeErr
	}
	if startErr != nil {
		return "none", "", fmt.Errorf("start purge helper: %w", startErr)
	}

	lines, done := make(chan string), make(chan struct{})
	defer close(done)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-done:
				return
			}
		}
	}()
	last, output, stallErr := watchProgress(lines, stall, func() { _ = command.Process.Kill() })
	waitErr := command.Wait()
	if stallErr != nil {
		return last, output, stallErr
	}
	return last, output, waitErr
}

// runPurgeHelper fails t unless command exits zero without stalling.
func runPurgeHelper(t *testing.T, name string, command *exec.Cmd) {
	t.Helper()
	last, output, err := watchPurgeHelper(command, purgeHelperStall)
	if err != nil {
		t.Fatalf("%s purge helper failed at its last progress step %q: %v\n%s", name, last, err, output)
	}
}

func TestPurgeHelperWatchBoundsSilenceNotTotalTime(t *testing.T) {
	t.Parallel()
	const helperEnvironment = "GC_PRODUCTMETRICS_WATCH_HELPER"
	switch os.Getenv(helperEnvironment) {
	case "fail":
		reportPurgeHelperProgress("fixture")
		fmt.Fprintln(os.Stderr, "boom")
		os.Exit(3)
	case "hang":
		reportPurgeHelperProgress("fixture")
		// Blocks until killed: nothing ever writes the pipe.
		reader, writer, err := os.Pipe()
		if err == nil {
			_, err = reader.Read(make([]byte, 1))
			_ = writer.Close()
		}
		fmt.Fprintf(os.Stderr, "hang helper woke: %v\n", err)
		os.Exit(4)
	}

	const stall = time.Second
	steady := make([]string, 15)
	for i := range steady {
		steady[i] = fmt.Sprintf("%sattempt %d", purgeHelperProgress, i+1)
	}
	noise := make([]string, 15)
	for i := range noise {
		noise[i] = "noise"
	}
	cases := []struct {
		name     string
		sent     []string
		wantLast string
		stalled  bool
	}{
		{name: "steady progress outlasting the bound passes", sent: steady, wantLast: "attempt 15"},
		{
			name: "silence after progress is killed naming the last step",
			sent: []string{purgeHelperProgress + "attempt 1"}, wantLast: "attempt 1", stalled: true,
		},
		{
			name: "output without the progress prefix does not reset the bound",
			sent: noise, wantLast: "none", stalled: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines, killed, stop := make(chan string), make(chan struct{}), make(chan struct{})
			defer close(stop)
			go func() {
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				for _, line := range tc.sent {
					select {
					case lines <- line:
					case <-stop:
						return
					}
					select {
					case <-ticker.C:
					case <-stop:
						return
					}
				}
				if !tc.stalled {
					close(lines)
				}
			}()
			began := time.Now()
			last, output, err := watchProgress(lines, stall, func() { close(killed) })
			elapsed := time.Since(began)
			if last != tc.wantLast {
				t.Errorf("last progress = %q, want %q (output %q)", last, tc.wantLast, output)
			}
			if got := errors.Is(err, errPurgeHelperStalled); got != tc.stalled {
				t.Errorf("stalled = %v (err %v), want %v", got, err, tc.stalled)
			}
			select {
			case <-killed:
				if !tc.stalled {
					t.Errorf("a helper that kept progressing was killed")
				}
			default:
				if tc.stalled {
					t.Errorf("a stalled helper was not killed")
				}
			}
			if !tc.stalled && elapsed <= stall {
				t.Errorf("passing case ran %v, not past the %v bound, so it proves nothing about total time", elapsed, stall)
			}
		})
	}

	t.Run("a real helper's nonzero exit and output reach the caller", func(t *testing.T) {
		t.Parallel()
		_, output, err := watchPurgeHelper(
			purgeHelperCommand("TestPurgeHelperWatchBoundsSilenceNotTotalTime", helperEnvironment, "fail"), purgeHelperStall)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 3 || !strings.Contains(output, "boom") {
			t.Errorf("err = %v, output %q: want exit status 3 carrying the helper's stderr", err, output)
		}
	})
	t.Run("a real helper that stalls is killed", func(t *testing.T) {
		t.Parallel()
		began := time.Now()
		_, _, err := watchPurgeHelper(
			purgeHelperCommand("TestPurgeHelperWatchBoundsSilenceNotTotalTime", helperEnvironment, "hang"), stall)
		if elapsed := time.Since(began); !errors.Is(err, errPurgeHelperStalled) || elapsed >= 30*stall {
			t.Errorf("err = %v after %v: want a stall error and the helper killed near the %v bound", err, elapsed, stall)
		}
	})
}
