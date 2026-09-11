package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFirstInvocationUsageFromSearchPathsReturnsStartupTokenSplit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	const transcript = `{"type":"assistant","uuid":"first","message":{"id":"msg-first","usage":{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":200}}}
{"type":"assistant","uuid":"later","message":{"id":"msg-later","usage":{"input_tokens":4,"cache_read_input_tokens":300,"cache_creation_input_tokens":500}}}
`
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	usage, found, err := FirstInvocationUsageFromSearchPaths([]string{root}, path)
	if err != nil {
		t.Fatalf("FirstInvocationUsageFromSearchPaths: %v", err)
	}
	if !found {
		t.Fatal("FirstInvocationUsageFromSearchPaths did not find startup usage")
	}
	if usage.InputTokens != 3 || usage.CacheReadTokens != 100 || usage.CacheCreationTokens != 200 {
		t.Fatalf("startup usage = %+v, want 3/100/200", usage)
	}
}
