package scripts_test

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	textFileBusyRetryAttempts = 5
	textFileBusyRetryDelay    = 25 * time.Millisecond
)

// runCombinedOutputWithTextFileBusyRetry retries only the Linux pre-exec race
// from a concurrently-written fake executable. Product command failures are
// returned unchanged.
func runCombinedOutputWithTextFileBusyRetry(t *testing.T, newCommand func() *exec.Cmd) ([]byte, error) {
	t.Helper()
	for attempt := 0; ; attempt++ {
		output, err := newCommand().CombinedOutput()
		if err == nil || !isTextFileBusyPreExecError(err, output) || attempt == textFileBusyRetryAttempts {
			return output, err
		}

		timer := time.NewTimer(textFileBusyRetryDelay)
		<-timer.C
	}
}

func isTextFileBusyPreExecError(err error, output []byte) bool {
	return strings.Contains(strings.ToLower(err.Error()+"\n"+string(output)), "text file busy")
}
