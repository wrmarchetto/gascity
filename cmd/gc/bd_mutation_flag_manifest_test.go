// cmd/gc/bd_mutation_flag_manifest_test.go
//
// Pins that gc's bd write-mutation arg scanner admits every flag the bd it
// ships against actually accepts, and still refuses one it does not know.
//
// The scanner is fail-closed: an unrecognized flag might consume the next
// token, so it reports ambiguity and doBdScoped refuses the write outright.
// That posture is only correct while the flag manifest behind it is complete.
// When it is not, the refusal lands on a legitimate command and reads to the
// operator as a typo -- gs-9zu, where `gc bd update --if-assignee` was refused
// in every city configuring a [beads] pre_write_command, making bd's
// compare-and-swap guard unreachable through gc exactly where guarded writes
// were wanted.
//
// The cases below are the flags the manifest was missing, not a sample: each
// one was refused before internal/bdflags grew an entry for it. Both
// directions are pinned in the same suite on purpose. A manifest that admits
// everything would satisfy the first half and has deleted the guard.
//
// Delegated elsewhere: whether the manifest still matches bd's own
// registrations. That is a question about the bd source, gated per
// subcommand and without a bd binary by
// internal/bdflags/flags_source_test.go. This suite pins the consumer
// behavior a complete manifest is supposed to produce, which stays meaningful
// however the manifest is maintained.
//
//	go test ./cmd/gc/ -run TestBdMutationScannerAdmitsRealBdFlags
package main

import "testing"

// TestBdMutationScannerAdmitsRealBdFlags pins that a legitimate bd flag
// reaches bd rather than tripping the fail-closed arm, and that the value of
// a value-consuming flag is never mistaken for a bead id.
//
// Every case carries a bead id in a position where a misclassified flag would
// corrupt the result rather than merely widen it: a flag wrongly treated as
// boolean leaves its value to be read as an id, so `--if-assignee toolsmith-1`
// would hand the pre-flight guard "toolsmith-1" to verify. Asserting only
// `!ambiguous` would pass with that defect intact.
func TestBdMutationScannerAdmitsRealBdFlags(t *testing.T) {
	const id = "gs-9zu"
	cases := []struct {
		name string
		args []string
	}{
		// --- bd update's compare-and-swap guards (the gs-9zu refusal) ---
		{
			name: "--if-assignee value is not read as a bead id",
			args: []string{"update", "--if-assignee", "toolsmith-1", "-a", "toolsmith-2", id},
		},
		{
			name: "--if-status value is not read as a bead id",
			args: []string{"update", id, "-a", "x", "--if-status", "in_progress"},
		},
		{
			// --if-assignee '' is bd's "expected unassigned" guard, not an
			// omitted value. The empty token must be consumed as the flag's
			// value: read as a positional it is dropped by the scanner's
			// empty-token filter, so the id count would silently agree.
			name: "--if-assignee with the empty expected-unassigned value",
			args: []string{"update", "--if-assignee", "", "-a", "x", id},
		},
		{
			name: "--force reassign-fence bypass is boolean",
			args: []string{"update", "--force", "-a", "x", id},
		},

		// --- bd close's hidden --reason aliases ---
		// MarkHidden'd in bd's source (close.go), so no --help transcript
		// lists them and no derivation reading --help can see they consume a
		// value. They are ordinary flags to bd's parser regardless.
		{
			name: "close -m alias consumes its reason",
			args: []string{"close", "-m", "done", id},
		},
		{
			name: "close --message alias consumes its reason",
			args: []string{"close", "--message", "done", id},
		},
		{
			name: "close --resolution alias consumes its reason",
			args: []string{"close", "--resolution", "done", id},
		},
		{
			name: "close --comment alias consumes its reason",
			args: []string{"close", "--comment", "done", id},
		},

		// --- bd's global flags, accepted by every subcommand ---
		{
			name: "--database global consumes its value",
			args: []string{"update", "--database", "beads_other", "-a", "x", id},
		},
		{
			name: "--mem-profile global consumes its value",
			args: []string{"update", "--mem-profile", "/var/tmp/heap.out", "-a", "x", id},
		},
		{
			name: "--format hidden global consumes its value",
			args: []string{"update", "--format", "json", "-a", "x", id},
		},
		{
			name: "--cpu-profile global is boolean",
			args: []string{"update", "--cpu-profile", id},
		},
		{
			name: "--no-color global is boolean",
			args: []string{"update", "--no-color", id},
		},

		// --- per-subcommand gaps found in the same sweep ---
		{
			name: "delete --from-file consumes its value",
			args: []string{"delete", "--from-file", "/var/tmp/ids.txt", id},
		},
		{
			name: "reopen -r consumes its reason",
			args: []string{"reopen", "-r", "wrong call", id},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, ok, ambiguous := bdMutationWriteIDs(tc.args)
			if !ok {
				t.Fatalf("bdMutationWriteIDs(%q): ok = false, want true (write subcommand)", tc.args)
			}
			if ambiguous {
				t.Fatalf("bdMutationWriteIDs(%q): refused as ambiguous; internal/bdflags is missing a flag bd accepts, so gc refuses the write before bd runs", tc.args)
			}
			if !bdTestSlicesEqual(ids, []string{id}) {
				t.Fatalf("bdMutationWriteIDs(%q): ids = %q, want [%q]; a flag value was read as a bead id", tc.args, ids, id)
			}
		})
	}
}

// TestBdMutationScannerStillRefusesUnknownFlags is the other half of the
// acceptance for gs-9zu. Completing the manifest must not turn the scanner
// into one that admits anything: an unrecognized flag can still consume the
// next token, and the pre-flight id guard would then verify a bead the caller
// never named.
//
// Constructed so a scanner that dropped the fail-closed arm would report a
// plausible id rather than an error -- each argv places a bead-shaped token
// where the unknown flag's value belongs.
func TestBdMutationScannerStillRefusesUnknownFlags(t *testing.T) {
	cases := [][]string{
		{"update", "--if-owner", "toolsmith-1", "-a", "x", "gs-9zu"},
		{"close", "--unknown-future-flag", "gs-realbead", "gs-9zu"},
		{"update", "-z", "gs-realbead", "gs-9zu"},
		{"delete", "--no-such-global", "gs-realbead", "gs-9zu"},
	}
	for _, args := range cases {
		ids, ok, ambiguous := bdMutationWriteIDs(args)
		if !ok {
			t.Errorf("bdMutationWriteIDs(%q): ok = false, want true", args)
			continue
		}
		if !ambiguous {
			t.Errorf("bdMutationWriteIDs(%q): ambiguous = false, want true; an unknown flag was admitted and ids = %q may name a bead the caller never supplied", args, ids)
		}
	}
}
