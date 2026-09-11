package tmux

import "testing"

// TestNudgeSubmitKeySequenceForFamilyHonorsTableEntry proves the lookup
// actually reads nudgeSubmitKeySequences rather than always returning the
// default — this is the mechanism a future claude-specific (or codex, per
// upstream #4706) fix would rely on.
func TestNudgeSubmitKeySequenceForFamilyHonorsTableEntry(t *testing.T) {
	orig := nudgeSubmitKeySequences
	nudgeSubmitKeySequences = map[string][]string{"testfam": {"Escape", "Enter"}}
	defer func() { nudgeSubmitKeySequences = orig }()

	got := nudgeSubmitKeySequenceForFamily("testfam")
	want := []string{"Escape", "Enter"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("nudgeSubmitKeySequenceForFamily(testfam) = %v, want %v", got, want)
	}
	// An unrelated family is unaffected by testfam's entry.
	if got := nudgeSubmitKeySequenceForFamily("claude"); len(got) != 1 || got[0] != "Enter" {
		t.Fatalf("nudgeSubmitKeySequenceForFamily(claude) = %v, want [Enter]", got)
	}
}
