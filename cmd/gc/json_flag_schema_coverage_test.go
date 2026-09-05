// Pins the pairing between a command's --json flag and its checked-in result
// schema, for the whole built-in command tree.
//
// The suite exists because the two halves are declared in different places
// and neither notices the other: the flag is registered in Go, the schema is
// a file under schemas/, and the gate that joins them
// (resolveJSONContractDisposition in json_schema.go) runs before RunE at
// runtime only.
//
// Scope is the built-in tree only: the root is built with pack-command
// discovery off, so a pack's commands are a different question and are
// covered by the pass-through/warning tiers in json_schema_test.go.
package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// jsonFlagWithoutResultSchema is the exact set of runnable built-in commands
// that declare --json and have no schemas/<path>/result.schema.json, so every
// one of them answers a real --json invocation with json_unsupported.
//
// This is an exact set, not an allowlist: the test fails both when an entry
// appears that is not listed and when a listed entry acquires a schema. A
// plain allowlist would let the count grow silently, which is the failure mode
// that put formula version-check here in the first place.
//
// Measured at 35839700e, the commit that closed gs-hph: 125 commands paired,
// these 11 unpaired. A re-count is a -v run of this test, which logs both
// numbers. Closing an entry means writing its result schema and deleting its
// line.
var jsonFlagWithoutResultSchema = []string{
	"gc context list",
	"gc context show",
	"gc extmsg bind",
	"gc extmsg handoff",
	"gc extmsg unbind",
	"gc maintenance dolt-gc",
	"gc maintenance status",
	"gc perf run",
	"gc perf session-new",
	"gc runtime conformance",
	"gc runtime heartbeat",
}

func TestEveryJSONFlagHasAResultSchema(t *testing.T) {
	root := newRootCmdWithOptions(&bytes.Buffer{}, &bytes.Buffer{}, rootCommandOptions{})

	var unpaired []string
	var paired int
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		if !c.Runnable() || c.Flags().Lookup("json") == nil {
			return
		}
		path := commandPathWords(c)
		if _, err := readCommandSchema(c, path, jsonSchemaResultRole); err != nil {
			unpaired = append(unpaired, "gc "+strings.Join(path, " "))
			return
		}
		paired++
	}
	walk(root)
	slices.Sort(unpaired)
	t.Logf("paired=%d unpaired=%d", paired, len(unpaired))

	want := slices.Clone(jsonFlagWithoutResultSchema)
	slices.Sort(want)
	if !slices.Equal(unpaired, want) {
		t.Errorf("commands declaring --json with no result schema changed.\n got: %s\nwant: %s\n\n"+
			"A new entry means a --json flag that every real invocation refuses with json_unsupported:\n"+
			"add schemas/<command path>/result.schema.json. A missing entry means one was fixed:\n"+
			"delete its line from jsonFlagWithoutResultSchema.",
			strings.Join(unpaired, ", "), strings.Join(want, ", "))
	}
	// The set comparison alone counts what is present, not what was reached. A
	// collapse that drops only paired commands leaves the set intact and passes.
	// The floor catches that without making routine command removals brittle.
	const pairedFloor = 100
	if paired < pairedFloor {
		t.Fatalf("walked the command tree and paired only %d --json commands, under the floor of %d; "+
			"the walk is not reaching commands, or the root is being built with less than the built-in tree",
			paired, pairedFloor)
	}
}
