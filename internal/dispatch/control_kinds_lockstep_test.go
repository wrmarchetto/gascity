package dispatch

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// TestProcessControlCoversEveryControlKind keeps the ProcessControl switch and
// beadmeta.ControlKinds in lockstep: every declared control kind must have a
// switch case (the processor may fail on the minimal bead, but never with the
// unsupported-kind error), and a kind outside the vocabulary must hard-error.
// Adding a case to the switch without declaring the kind in beadmeta (or vice
// versa) fails here or in beadmeta.TestControlKindsExact.
func TestProcessControlCoversEveryControlKind(t *testing.T) {
	for _, kind := range beadmeta.ControlKinds {
		t.Run(kind, func(t *testing.T) {
			store := beads.NewMemStore()
			bead, err := store.Create(beads.Bead{
				Title:    "lockstep probe " + kind,
				Metadata: map[string]string{beadmeta.KindMetadataKey: kind},
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			_, err = ProcessControl(store, bead, ProcessOptions{})
			if err != nil && strings.Contains(err.Error(), "unsupported control bead kind") {
				t.Errorf("ProcessControl rejected declared control kind %q as unsupported: %v", kind, err)
			}
		})
	}

	// "check" is asserted alongside a nonsense string because it is the kind
	// most likely to be re-added by mistake: it was a dispatched control kind
	// until ci-zg0l, no compiler ever emitted it, and its retry-clone
	// implementation read as coverage of the live ralph path for months.
	// Restoring the switch case without a compiler that mints the bead puts
	// that decoy back, so the retired kind is pinned to the same hard error as
	// an unknown one. See internal/beadmeta/kindsets.go for the vocabulary.
	for _, kind := range []string{"not-a-control-kind", "check"} {
		t.Run("rejected/"+kind, func(t *testing.T) {
			store := beads.NewMemStore()
			bead, err := store.Create(beads.Bead{
				Title:    "lockstep probe " + kind,
				Metadata: map[string]string{beadmeta.KindMetadataKey: kind},
			})
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := ProcessControl(store, bead, ProcessOptions{}); err == nil || !strings.Contains(err.Error(), "unsupported control bead kind") {
				t.Errorf("ProcessControl(%q) error = %v, want unsupported-kind error", kind, err)
			}
		})
	}
}
