// Scope: SupervisorPackDriftCheck's verdict for every relation between the
// running supervisor's bundled-pack content hash and this binary's.
//
// The suite exists because the drift it pins is invisible from either side
// alone: the supervisor and `gc order show` each report a self-consistent
// answer, and only contrasting them shows an exec order running a script the
// diagnostic command does not name. Every case therefore drives the two
// hashes to DIFFERENT literals rather than deriving one from the other — a
// test that fed both sides from builtinpacks.SyntheticCacheKeyComponent
// would agree with itself no matter what the check did.
//
// What it delegates: the socket round trip that obtains the supervisor's
// hash is cmd/gc's (TestHandleSupervisorConnPackHash), and the correctness
// of the hash itself is internal/builtinpacks'.
//
// Run: go test ./internal/doctor/ -run SupervisorPackDrift

package doctor

import (
	"strings"
	"testing"
)

// newPackDriftCheckForTest builds the check with both hash sources injected,
// so a case can state the supervisor's hash and this binary's independently.
// There is deliberately no flag or env switch for reaching the drift branch:
// the branch exists to catch a supervisor whose hash is present but WRONG,
// and a switch read before the comparison would pin the short-circuit
// instead.
//
// It always builds a RUNNING supervisor: the not-running case is the one
// case that must not reach the probe at all, so it constructs the check
// itself and asserts the probe stayed untouched.
func newPackDriftCheckForTest(pid int, reported string, reportedOK bool, local string) *SupervisorPackDriftCheck {
	c := NewSupervisorPackDriftCheck(true, pid, func() (string, bool) {
		return reported, reportedOK
	})
	c.localHash = func() string { return local }
	return c
}

func TestSupervisorPackDriftReportsMismatchedHashes(t *testing.T) {
	r := newPackDriftCheckForTest(4242, "sha256:aaaa", true, "sha256:bbbb").Run(nil)

	if r.Status != StatusError {
		t.Fatalf("status = %v, want StatusError; a supervisor running different pack content drops exec-order fixes silently", r.Status)
	}
	// Both hashes must appear: the operator's next move differs depending on
	// which side is stale, and a message naming only one cannot say.
	for _, want := range []string{"sha256:aaaa", "sha256:bbbb", "4242"} {
		if !strings.Contains(r.Message, want) {
			t.Errorf("message %q does not name %q", r.Message, want)
		}
	}
	if r.FixHint == "" {
		t.Error("FixHint is empty; the remedy is a supervisor restart and nothing else in gc says so")
	}
}

func TestSupervisorPackDriftReportsSupervisorThatCannotAnswer(t *testing.T) {
	// A supervisor that does not answer the probe predates it, which means
	// it is older than the binary on disk — the same defect as a mismatched
	// hash, and it must NOT read as agreement.
	r := newPackDriftCheckForTest(4242, "", false, "sha256:bbbb").Run(nil)

	if r.Status != StatusError {
		t.Fatalf("status = %v, want StatusError; an unanswered probe is evidence of an older supervisor, not of agreement", r.Status)
	}
	if !strings.Contains(r.Message, "4242") {
		t.Errorf("message %q does not name the supervisor PID", r.Message)
	}
	if r.FixHint == "" {
		t.Error("FixHint is empty")
	}
	// The mismatch branch would also produce StatusError here, quoting an
	// empty hash as the supervisor's answer. Pin the distinction: an
	// unanswered probe must say the supervisor did not answer, and must not
	// attribute pack content to it.
	if !strings.Contains(r.Message, "does not report") {
		t.Errorf("message %q does not say the supervisor failed to answer the probe", r.Message)
	}
	if strings.Contains(r.Message, "runs bundled pack content") {
		t.Errorf("message %q attributes pack content to a supervisor that reported none", r.Message)
	}
}

func TestSupervisorPackDriftAcceptsMatchingHashes(t *testing.T) {
	r := newPackDriftCheckForTest(4242, "sha256:aaaa", true, "sha256:aaaa").Run(nil)

	if r.Status != StatusOK {
		t.Fatalf("status = %v (%s), want StatusOK", r.Status, r.Message)
	}
	if !strings.Contains(r.Message, "sha256:aaaa") {
		t.Errorf("message %q does not name the agreed hash", r.Message)
	}
}

func TestSupervisorPackDriftSkipsWhenSupervisorIsDown(t *testing.T) {
	// With no supervisor there is no second binary to disagree with, and the
	// controller check already reports the outage. Probing anyway would add
	// a second red line for one problem.
	probed := false
	c := NewSupervisorPackDriftCheck(false, 0, func() (string, bool) {
		probed = true
		return "sha256:aaaa", true
	})
	c.localHash = func() string { return "sha256:bbbb" }

	r := c.Run(nil)
	if r.Status != StatusOK {
		t.Fatalf("status = %v (%s), want StatusOK when the supervisor is down", r.Status, r.Message)
	}
	if probed {
		t.Error("probed a supervisor that is not running")
	}
}

func TestSupervisorPackDriftRefusesToCompareAnUnhashableBinary(t *testing.T) {
	// SyntheticCacheKeyComponent returns "" when this binary's embedded pack
	// set cannot be hashed. Comparing "" against a supervisor that also
	// cannot hash its own would report agreement built from two failures.
	r := newPackDriftCheckForTest(4242, "", true, "").Run(nil)

	if r.Status != StatusError {
		t.Fatalf("status = %v (%s), want StatusError; two unhashable binaries must not read as agreement", r.Status, r.Message)
	}
}

func TestSupervisorPackDriftNamesSupervisorWithoutAPID(t *testing.T) {
	// supervisorStatusWithOptions confirms liveness through the service
	// manager and the HTTP API as well as the socket, and only the socket
	// yields a PID. The message has to stay readable at zero.
	r := newPackDriftCheckForTest(0, "sha256:aaaa", true, "sha256:bbbb").Run(nil)

	if r.Status != StatusError {
		t.Fatalf("status = %v, want StatusError", r.Status)
	}
	if strings.Contains(r.Message, "PID 0") {
		t.Errorf("message %q reports a PID of 0 as if it were a process", r.Message)
	}
}
