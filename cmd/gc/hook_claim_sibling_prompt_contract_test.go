package main

// Scope: the join between the `sibling_branches` wire field and the shipped
// worker prompts that are its only consumer.
//
// Why this exists as its own suite: gc writes the field and nothing in gc
// reads it. An agent does, having been told to by a prompt -- so the field
// name lives in two places that no other test compares. Rename the `json:`
// tag and every schema, wire and behavior test in the tree still passes while
// the prompts point agents at a key that is no longer emitted, which is
// exactly the shape of failure the signal was built to prevent: a fact
// present in the system that nobody is told to read.
//
// The expected name is therefore taken by reflection FROM the struct tag the
// encoder uses, never written out as a literal here. A literal would agree
// with itself after a rename and pin nothing.
//
// Not covered here: the city-local prompt fragment that carries the same
// instruction to this city's agents. It lives in the city repository, outside
// this module, and is verified there by rendering the prompt.
//
// Run: go test ./cmd/gc/ -run HookClaimSiblingPromptContract
import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// siblingBranchesWireName is the JSON key the encoder actually emits for the
// claim record's sibling-branch field.
func siblingBranchesWireName(t *testing.T) string {
	t.Helper()
	field, ok := reflect.TypeOf(hookClaimJSONResult{}).FieldByName("SiblingBranches")
	if !ok {
		t.Fatal("hookClaimJSONResult has no SiblingBranches field")
	}
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if name == "" {
		t.Fatalf("SiblingBranches has no json name: tag=%q", field.Tag.Get("json"))
	}
	return name
}

// TestHookClaimSiblingPromptContractNamesTheEmittedField requires every core
// worker prompt to name the field gc emits, along with the three facts a
// claimant needs from it. `base`, `last_commit` and `via` are asserted
// because a prompt that mentions only the array trains an agent to note that
// a sibling exists without weighing whether it is alive or even related --
// the failure mode the acceptance condition on this signal singles out.
func TestHookClaimSiblingPromptContractNamesTheEmittedField(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("filepath.Abs(repo root): %v", err)
	}
	wire := siblingBranchesWireName(t)
	for _, rel := range []string{
		"internal/bootstrap/packs/core/assets/prompts/pool-worker.md",
		"internal/bootstrap/packs/core/assets/prompts/graph-worker.md",
	} {
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(repoRoot, rel))
			if err != nil {
				t.Fatalf("ReadFile(%s): %v", rel, err)
			}
			text := string(data)
			for _, want := range []string{wire, "`base`", "`last_commit`", "`via`"} {
				if !strings.Contains(text, want) {
					t.Fatalf("%s does not tell the agent to read %s", rel, want)
				}
			}
		})
	}
}
