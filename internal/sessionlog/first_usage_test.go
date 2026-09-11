package sessionlog

import (
	"path/filepath"
	"testing"
)

// The paired cache report must read the invocation at the start of a
// transcript, not its tail: later turns include conversation growth and cannot
// answer whether a fresh spawn reused its startup prefix.
func TestExtractFirstUsageReturnsFirstInvocationAndCollapsesItsBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeTailJSONL(t, path, []map[string]any{
		{
			"type": "assistant",
			"uuid": "first-text",
			"message": map[string]any{
				"id": "msg-first", "model": "claude-opus-5",
				"usage": map[string]any{
					"input_tokens": 3, "cache_read_input_tokens": 100,
					"cache_creation_input_tokens": 200,
				},
			},
		},
		{
			"type": "assistant",
			"uuid": "first-tool",
			"message": map[string]any{
				"id": "msg-first", "model": "claude-opus-5",
				"usage": map[string]any{
					"input_tokens": 3, "cache_read_input_tokens": 100,
					"cache_creation_input_tokens": 200,
				},
			},
		},
		{
			"type": "assistant",
			"uuid": "later",
			"message": map[string]any{
				"id": "msg-later", "model": "claude-opus-5",
				"usage": map[string]any{
					"input_tokens": 4, "cache_read_input_tokens": 300,
					"cache_creation_input_tokens": 500,
				},
			},
		},
	})

	got, ok, err := ExtractFirstUsage(path)
	if err != nil {
		t.Fatalf("ExtractFirstUsage: %v", err)
	}
	if !ok {
		t.Fatal("ExtractFirstUsage reported no invocation")
	}
	if got.EntryUUID != "first-tool" || got.MessageID != "msg-first" {
		t.Fatalf("first invocation identity = %+v, want final block of msg-first", got)
	}
	if got.InputTokens != 3 || got.CacheReadTokens != 100 || got.CacheCreationTokens != 200 {
		t.Fatalf("first invocation usage = %+v, want 3/100/200", got)
	}
}
