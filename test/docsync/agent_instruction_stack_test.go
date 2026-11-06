package docsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentInstructionsStateTheCurrentForkPosture prevents the retired
// upstream-integration brief from returning to every worker's instruction stack.
func TestAgentInstructionsStateTheCurrentForkPosture(t *testing.T) {
	path := filepath.Join(repoRoot(), "AGENTS.md")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read AGENTS.md: %v", err)
	}
	text := string(contents)
	if !strings.Contains(text, "## Current fork posture") {
		t.Error("AGENTS.md is missing the current fork posture")
	}
	if strings.Contains(text, "## Current integration mission") {
		t.Error("AGENTS.md still presents the retired integration mission")
	}

	// The done-criterion describes what .githooks/pre-push runs, so it is
	// read against the hook rather than a copy of its behavior.
	hook, err := os.ReadFile(filepath.Join(repoRoot(), ".githooks", "pre-push"))
	if err != nil {
		t.Fatalf("read .githooks/pre-push: %v", err)
	}
	flat := strings.Join(strings.Fields(text), " ")
	hookScopes := strings.Contains(string(hook), `GC_SCOPED_TEST_PACKAGES="$scope" exec make test-fast-parallel`)
	docScopes := strings.Contains(flat, "runs the fast suite over the packages the push can reach")
	if hookScopes != docScopes {
		t.Errorf("AGENTS.md's done-criterion says the push gate scopes its suite: %t, but .githooks/pre-push scopes it: %t", docScopes, hookScopes)
	}
	if hookScopes && strings.Contains(flat, "runs the full fast suite for any push") {
		t.Error("AGENTS.md's done-criterion says the push gate runs the full fast suite, but .githooks/pre-push scopes it")
	}
}
