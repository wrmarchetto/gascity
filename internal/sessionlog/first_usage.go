package sessionlog

import (
	"bufio"
	"encoding/json"
	"os"
)

// ExtractFirstUsage returns the first model invocation recorded in a Claude
// transcript. The boolean is false when no usage-bearing invocation exists.
//
// Claude writes one entry per content block, so all adjacent entries bearing
// the first provider message ID belong to one invocation. The final block is
// returned to match ExtractTailUsage's collapse convention.
func ExtractFirstUsage(path string) (TailUsage, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return TailUsage{}, false, err
	}
	defer f.Close() //nolint:errcheck // read-only

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 256*1024), 8*1024*1024)
	var first TailUsage
	for scanner.Scan() {
		u, ok := usageFromTranscriptLine(scanner.Bytes())
		if !ok {
			continue
		}
		if first.EntryUUID == "" {
			first = u
			// An unkeyed entry cannot have sibling content blocks identified
			// reliably, so it alone is the first observable invocation.
			if first.MessageID == "" {
				return first, true, nil
			}
			continue
		}
		if u.MessageID == first.MessageID {
			first = u
			continue
		}
		return first, true, nil
	}
	if err := scanner.Err(); err != nil {
		return TailUsage{}, false, err
	}
	if first.EntryUUID == "" {
		return TailUsage{}, false, nil
	}
	return first, true, nil
}

// ExtractFirstUsageFromSearchPaths reads a first invocation only after the
// transcript path has been verified under a configured search root.
func ExtractFirstUsageFromSearchPaths(searchPaths []string, path string) (TailUsage, bool, error) {
	safePath, err := validateSearchPathFile(searchPaths, path)
	if err != nil {
		return TailUsage{}, false, err
	}
	return ExtractFirstUsage(safePath)
}

func usageFromTranscriptLine(line []byte) (TailUsage, bool) {
	var entry tailEntry
	if err := json.Unmarshal(line, &entry); err != nil || entry.Type != "assistant" || entry.UUID == "" || len(entry.Message) == 0 {
		return TailUsage{}, false
	}
	var msg assistantMessage
	if err := json.Unmarshal(unwrapJSONString(entry.Message), &msg); err != nil || msg.Usage == nil {
		return TailUsage{}, false
	}
	u := TailUsage{
		EntryUUID:           entry.UUID,
		MessageID:           msg.ID,
		Model:               msg.Model,
		InputTokens:         msg.Usage.InputTokens,
		OutputTokens:        msg.Usage.OutputTokens,
		CacheReadTokens:     msg.Usage.CacheReadInputTokens,
		CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
	}
	if u.InputTokens <= 0 && u.OutputTokens <= 0 && u.CacheReadTokens <= 0 && u.CacheCreationTokens <= 0 {
		return TailUsage{}, false
	}
	return u, true
}
