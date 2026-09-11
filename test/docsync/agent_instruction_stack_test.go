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
}
