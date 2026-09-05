package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBdPreWriteCommandPassesMutationContext(t *testing.T) {
	city := t.TempDir()
	script := filepath.Join(city, "pre-write")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s|%s|%s\\n' \"$GC_CITY\" \"$GC_STORE_ROOT\" \"$GC_BD_ARGS_JSON\" >&2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if blocked := runBdPreWriteCommand(script, city, "/store", []string{"update", "ci-abc", "--add-label", "harness:astoria"}, &stderr); blocked {
		t.Fatalf("runBdPreWriteCommand blocked an accepting validator: %s", stderr.String())
	}
	for _, want := range []string{city, "/store", `["update","ci-abc","--add-label","harness:astoria"]`} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("validator context %q missing from %q", want, stderr.String())
		}
	}
}

func TestRunBdPreWriteCommandRefusesBeforeWrite(t *testing.T) {
	city := t.TempDir()
	script := filepath.Join(city, "pre-write")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'missing rendezvous: /city/bench-artifacts/ci-abc' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if blocked := runBdPreWriteCommand(script, city, city, []string{"update", "ci-abc", "--set-metadata", "gc.routed_to=operator"}, &stderr); !blocked {
		t.Fatal("runBdPreWriteCommand accepted a refusing validator")
	}
	if got := stderr.String(); !strings.Contains(got, "missing rendezvous") || !strings.Contains(got, "pre-write validation failed") {
		t.Fatalf("stderr = %q, want validator finding and refusal context", got)
	}
}

func TestRunBdPreWriteCommandResolvesCityRelativePath(t *testing.T) {
	city := t.TempDir()
	if err := os.MkdirAll(filepath.Join(city, "assets", "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(city, "assets", "scripts", "pre-write")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if blocked := runBdPreWriteCommand("assets/scripts/pre-write", city, city, []string{"update", "ci-abc"}, &stderr); blocked {
		t.Fatalf("city-relative validator was not run: %s", stderr.String())
	}
}

// TestBdPreWriteMutationCoversCreate pins the verb set the city validator
// sees. `create` is the one that matters: the bench-visibility gate shipped
// covering `update` alone, and every premature-visibility instance it was
// written to stop arrived through a create carrying the harness label and the
// route at once (ci-s7qh10). A table keyed on the verb, not on one example,
// because the hole was a missing table entry rather than a wrong branch.
func TestBdPreWriteMutationCoversCreate(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"create", []string{"create", "title", "-l", "harness:astoria"}, true},
		{"update", []string{"update", "ci-abc", "--add-label", "harness:astoria"}, true},
		{"close", []string{"close", "ci-abc", "--reason", "done"}, true},
		{"reopen", []string{"reopen", "ci-abc"}, true},
		{"delete", []string{"delete", "ci-abc"}, true},
		{"list is a read", []string{"list", "--status", "open"}, false},
		{"show is a read", []string{"show", "ci-abc"}, false},
		{"ready is a read", []string{"ready", "--claim"}, false},
		{"no verb", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutation, ambiguous := bdPreWriteMutation(tc.args)
			if mutation != tc.want {
				t.Errorf("bdPreWriteMutation(%v) mutation = %v, want %v", tc.args, mutation, tc.want)
			}
			if ambiguous {
				t.Errorf("bdPreWriteMutation(%v) reported ambiguity on a fully manifested argv", tc.args)
			}
		})
	}
}

// TestBdPreWriteMutationFiresBehindAGlobalFlag pins that a global value-flag
// ahead of the verb does not disarm the validator. Reading args[0] as the verb
// yields "bob" from `--actor bob update ...`, so the gate silently stops
// firing for exactly the authors who pass an explicit actor -- and the
// validator's own test for that shape passes while gc never invokes it.
func TestBdPreWriteMutationFiresBehindAGlobalFlag(t *testing.T) {
	prefixes := [][]string{
		{"--actor", "bob"},
		{"-C", "/some/dir"},
		{"--db", "/x/y.db"},
		{"--json"},
		{"--actor", "bob", "--json", "-C", "/d"},
	}
	for _, prefix := range prefixes {
		for _, tail := range [][]string{
			{"update", "ci-abc", "--add-label", "harness:astoria"},
			{"create", "title", "-l", "harness:astoria"},
		} {
			args := append(append([]string{}, prefix...), tail...)
			mutation, ambiguous := bdPreWriteMutation(args)
			if !mutation {
				t.Errorf("bdPreWriteMutation(%v) = false; the validator never sees this write", args)
			}
			if ambiguous {
				t.Errorf("bdPreWriteMutation(%v) reported ambiguity on a fully manifested argv", args)
			}
		}
	}
}

// TestBdPreWriteMutationReportsUnknownFlagAmbiguity pins the fail-closed half.
// An unrecognized flag may consume the next token as its value, so the
// validator's own parse of the argv can no longer be trusted to find the label
// or the route -- the caller must refuse rather than hand it an argv it will
// misread.
func TestBdPreWriteMutationReportsUnknownFlagAmbiguity(t *testing.T) {
	cases := [][]string{
		{"update", "ci-abc", "--not-a-flag", "harness:astoria"},
		{"create", "title", "--not-a-flag", "value", "-l", "harness:astoria"},
	}
	for _, args := range cases {
		mutation, ambiguous := bdPreWriteMutation(args)
		if !mutation || !ambiguous {
			t.Errorf("bdPreWriteMutation(%v) = (%v, %v), want (true, true)", args, mutation, ambiguous)
		}
	}
	// Control: the same unrecognized name in inline form consumes nothing, so
	// it cannot shift a positional and is not ambiguous.
	if _, ambiguous := bdPreWriteMutation([]string{"update", "ci-abc", "--not-a-flag=x"}); ambiguous {
		t.Error("inline-value unknown flag reported as ambiguous; it consumes no following token")
	}
}
