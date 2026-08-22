// bdstore_pool_claim_test.go pins BdStore.ReassignIfAssignee -- the
// compare-and-swap that decides which of several eligible actors takes a bead
// parked on a pool alias.
//
// The suite exists because the failure it guards is invisible from the outside: a
// pool-parked bead that cannot be taken looks exactly like an empty queue
// (ci-c000), and a swap that writes unconditionally looks exactly like a
// successful take until two sessions run the same bead. It asserts the exact bd
// argv, because the whole scheme's atomicity rests on --if-assignee being present
// and carrying the name the caller was handed -- no assertion on the returned
// bead can tell a guarded write from an unguarded one. Store-integration behavior
// (real bd, real Dolt) is delegated to the process-level suites under cmd/gc.
//
// Run: go test ./internal/beads/ -run ReassignIfAssignee
package beads_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

func TestReassignIfAssigneeGuardsTheWriteWithTheExpectedHolder(t *testing.T) {
	var commands []string
	runner := func(_, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		commands = append(commands, command)
		if command == "bd update bd-42 --if-assignee crew --assignee worker-2 --json" {
			return []byte(`[{"id":"bd-42","status":"open","assignee":"worker-2","issue_type":"task","created_at":"2025-01-15T10:30:00Z"}]`), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", command)
	}

	s := beads.NewBdStore("/city", runner)
	moved, ok, err := s.ReassignIfAssignee("bd-42", "crew", "worker-2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("ReassignIfAssignee ok = false, want true; moved=%+v", moved)
	}
	if moved.Assignee != "worker-2" {
		t.Fatalf("moved = %+v, want bd-42 reassigned to worker-2", moved)
	}
	// Without --if-assignee the second slot to arrive would overwrite the
	// winner's assignee and both would run the bead.
	if !strings.Contains(commands[0], "--if-assignee crew") {
		t.Fatalf("commands[0] = %q, want the swap guarded on crew", commands[0])
	}
	// The swap must NOT set status or claim: bd rejects --if-assignee combined
	// with --claim, so a caller that folded them together would silently stop
	// guarding the write.
	if strings.Contains(commands[0], "--claim") || strings.Contains(commands[0], "--status") {
		t.Fatalf("commands[0] = %q, want an assignee-only swap", commands[0])
	}
}

func TestReassignPoolClaimIfCurrentRecordsThePoolRouteAtomically(t *testing.T) {
	runner := func(_, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		const want = "bd update bd-42 --if-assignee crew --assignee worker-2 --set-metadata gc.routed_to=crew --json"
		if command != want {
			return nil, fmt.Errorf("command = %q, want %q", command, want)
		}
		return []byte(`[{"id":"bd-42","status":"open","assignee":"worker-2","metadata":{"gc.routed_to":"crew"}}]`), nil
	}

	s := beads.NewBdStore("/city", runner)
	moved, ok, err := s.ReassignPoolClaimIfCurrent("bd-42", "crew", "worker-2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("ReassignPoolClaimIfCurrent ok = false, want true; moved=%+v", moved)
	}
	if got := moved.Metadata["gc.routed_to"]; got != "crew" {
		t.Fatalf("moved gc.routed_to = %q, want crew", got)
	}
}

func TestReassignIfAssigneeReportsLostRaceWhenTheHolderAlreadyMoved(t *testing.T) {
	runner := func(_, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		switch command {
		case "bd update bd-42 --if-assignee crew --assignee worker-2 --json":
			return []byte(`assignee mismatch: bd-42 is held by "worker-1", expected "crew"`), fmt.Errorf("exit status 13")
		case "bd show --json bd-42":
			return []byte(`[{"id":"bd-42","status":"in_progress","assignee":"worker-1","issue_type":"task","created_at":"2025-01-15T10:30:00Z"}]`), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", command)
	}

	s := beads.NewBdStore("/city", runner)
	current, ok, err := s.ReassignIfAssignee("bd-42", "crew", "worker-2")
	if err != nil {
		t.Fatalf("a lost race is not an operational failure, got err=%v", err)
	}
	if ok {
		t.Fatal("ReassignIfAssignee ok = true, want false after a refused swap")
	}
	// The caller names the winner in bead.claim_rejected, so the readback has to
	// come back rather than being dropped for an empty bead.
	if current.Assignee != "worker-1" {
		t.Fatalf("current.Assignee = %q, want the winning claimant worker-1", current.Assignee)
	}
}

func TestReassignIfAssigneeSurfacesWriteFailureWhileStillOnTheExpectedHolder(t *testing.T) {
	runner := func(_, name string, args ...string) ([]byte, error) {
		command := name + " " + strings.Join(args, " ")
		switch command {
		case "bd update bd-42 --if-assignee crew --assignee worker-2 --json":
			return []byte("database is locked"), fmt.Errorf("exit status 1")
		case "bd show --json bd-42":
			return []byte(`[{"id":"bd-42","status":"open","assignee":"crew","issue_type":"task","created_at":"2025-01-15T10:30:00Z"}]`), nil
		}
		return nil, fmt.Errorf("unexpected command: %s", command)
	}

	s := beads.NewBdStore("/city", runner)
	_, ok, err := s.ReassignIfAssignee("bd-42", "crew", "worker-2")
	if ok {
		t.Fatal("ReassignIfAssignee ok = true, want false after a failed write")
	}
	// The bead is still on the expected holder, so nobody won it: reporting this
	// as a lost race would launder a write failure into "another slot has it" and
	// the bead would sit unclaimable while the hook drain-acks as if idle -- the
	// exact silence ci-c000 was filed for.
	if err == nil {
		t.Fatal("ReassignIfAssignee err = nil; a write failure with the bead unmoved must surface")
	}
	if !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("err = %v, want the underlying bd failure named", err)
	}
}
