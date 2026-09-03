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
