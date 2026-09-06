package session

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/runtime"
)

// Pins the teardown name for a failed create whose rollback released its
// explicit session name.
//
// WHY THIS SUITE EXISTS. Suspend is the cleanup of last resort for a
// failed-create bead -- `gc stop` issues suspend on every session bead, and
// under a backing-store outage the reconciler cannot reap those beads at all,
// so nothing else clears the leaked process. It tears down whatever
// sessionName() resolves, and for an EXPLICITLY named session that resolution
// is wrong after a rollback: rollbackPendingCreateClears (cmd/gc,
// session_lifecycle_parallel.go) clears session_name to release the name
// claim, so sessionName() falls back to the synthetic sessionNameFor(id) and
// Stop targets a name no runtime ever had. The runtime under the original
// name keeps running, referenced by nothing (ci-wjwshz).
//
// THE ASSERTION IS ON THE NAME, NOT ON THE CALL COUNT. A suite that only
// checked "Stop was called" passes today, because Suspend already stops the
// synthetic name -- it is the wrong name that is the defect, so the case has
// to name which one.
//
// WHY NOT JUST STOP RESOLVING TO THE OLD NAME EVERYWHERE. Clearing
// session_name is what RELEASES the name for reuse, so by the time a late
// suspend runs, a different session may legitimately own it. Reaping by that
// name unguarded would kill a live sibling -- a worse fault than the leak it
// fixes. The second case below is that guard, and it is the reason the lookup
// is confined to the failed-create branch rather than added to sessionName().
//
// Run it:
//
//	go test ./internal/session/ -run FailedCreate

// failedCreateBead lands a bead in the post-rollback shape: failed-create,
// session_name cleared, and the released name recorded for cleanup.
func failedCreateBead(t *testing.T, m *Manager, store beads.Store, agent string) (string, string) {
	t.Helper()
	id := createTestSession(t, m, agent)
	b, err := store.Get(id)
	if err != nil {
		t.Fatalf("get bead: %v", err)
	}
	original := b.Metadata["session_name"]
	if original == "" {
		t.Fatalf("fixture did not get a session_name to release")
	}
	// Exactly what the rollback writes: the claim released, the name kept
	// only so the leaked runtime can still be found.
	for k, v := range map[string]string{
		"session_name":           "",
		RolledBackSessionNameKey: original,
		"state":                  string(StateFailedCreate),
	} {
		if err := store.SetMetadata(id, k, v); err != nil {
			t.Fatalf("set %s: %v", k, err)
		}
	}
	return id, original
}

func TestSuspendFailedCreateStopsTheRuntimeUnderTheNameRollbackReleased(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	m := NewManagerWithOptions(store, sp)

	id, original := failedCreateBead(t, m, store, "dog")
	if err := sp.Start(context.Background(), original, runtime.Config{}); err != nil {
		t.Fatalf("seeding the leaked runtime: %v", err)
	}

	if err := m.Suspend(id); err != nil {
		t.Fatalf("Suspend(failed-create) = %v, want nil (must not block gc stop)", err)
	}
	if sp.CountCalls("Stop", original) == 0 {
		t.Errorf("Suspend never stopped %q, the name the runtime actually has; "+
			"it reaped the synthetic fallback instead and the process leaks", original)
	}
	if sp.IsRunning(original) {
		t.Errorf("runtime %q still running after Suspend(failed-create)", original)
	}
}

func TestSuspendFailedCreateLeavesAReleasedNameAnotherLiveSessionHasTaken(t *testing.T) {
	store := beads.NewMemStore()
	sp := runtime.NewFake()
	m := NewManagerWithOptions(store, sp)

	id, original := failedCreateBead(t, m, store, "dog")

	// The release worked: a second, healthy session now owns that name and is
	// running under it. Reaping it would kill a live sibling.
	other := createTestSession(t, m, "cat")
	if err := store.SetMetadata(other, "session_name", original); err != nil {
		t.Fatalf("handing the released name to a live session: %v", err)
	}
	if err := sp.Start(context.Background(), original, runtime.Config{}); err != nil {
		t.Fatalf("seeding the live sibling's runtime: %v", err)
	}

	if err := m.Suspend(id); err != nil {
		t.Fatalf("Suspend(failed-create) = %v, want nil", err)
	}
	if !sp.IsRunning(original) {
		t.Errorf("Suspend killed %q, which a live session bead %s now owns -- "+
			"the released-name lookup must yield to a current claimant", original, other)
	}
}
