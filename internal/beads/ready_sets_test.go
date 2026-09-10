package beads

import "testing"

// The exported ready-exclusion sets and the predicates that gate Ready() must
// be one set, not two. The gap this pins is not hypothetical for a reader --
// it is what a second copy always does: a label added to the predicate and not
// to the slice leaves `gc bd ready` forwarding an exclusion list that is
// missing exactly the family somebody just found noisy enough to exclude, and
// nothing about that view says it is incomplete.
func TestReadyExcludedLabelsDriveTheLabelPredicate(t *testing.T) {
	labels := ReadyExcludedLabels()
	if len(labels) == 0 {
		t.Fatal("ReadyExcludedLabels() is empty; the ready view would forward no exclusion at all")
	}
	for _, label := range labels {
		if !HasReadyExcludedLabel(Bead{Labels: []string{label}}) {
			t.Errorf("ReadyExcludedLabels() lists %q but HasReadyExcludedLabel does not exclude it", label)
		}
	}
	if HasReadyExcludedLabel(Bead{Labels: []string{"gc:not-an-exclusion"}}) {
		t.Error("HasReadyExcludedLabel excluded a label absent from the set; the predicate is not reading the set")
	}
}

func TestReadyExcludedTypesDriveTheTypePredicate(t *testing.T) {
	types := ReadyExcludedTypes()
	if len(types) == 0 {
		t.Fatal("ReadyExcludedTypes() is empty; the ready view would forward no type exclusion at all")
	}
	for _, typ := range types {
		if !IsReadyExcludedType(typ) {
			t.Errorf("ReadyExcludedTypes() lists %q but IsReadyExcludedType does not exclude it", typ)
		}
	}
	if IsReadyExcludedType("bug") {
		t.Error("IsReadyExcludedType excluded an ordinary work type")
	}
}

// A returned slice that aliases package state lets any caller mutate the
// exclusion set for every other caller in the process -- including the store
// Ready paths. Copy on return.
func TestReadyExcludedSetsAreNotAliasedToPackageState(t *testing.T) {
	first := ReadyExcludedLabels()
	if len(first) == 0 {
		t.Fatal("no labels to mutate")
	}
	first[0] = "mutated"
	if ReadyExcludedLabels()[0] == "mutated" {
		t.Error("ReadyExcludedLabels() returns package state; a caller can empty the exclusion set")
	}

	firstTypes := ReadyExcludedTypes()
	if len(firstTypes) == 0 {
		t.Fatal("no types to mutate")
	}
	firstTypes[0] = "mutated"
	if ReadyExcludedTypes()[0] == "mutated" {
		t.Error("ReadyExcludedTypes() returns package state")
	}
}
