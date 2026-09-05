package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/bdflags"
)

// bdPreWriteVerbs are the bd subcommands whose argv the city's configured
// pre_write_command must see before bd runs.
//
// `create` is here because it is how the defect a visibility validator exists
// for actually arrives. A city's bench bead becomes claimable the instant it
// carries both its harness label and its route, and an author sets the two as
// a checklist of fields AT CREATION -- so a validator wired only to `update`
// has never yet refused the shape it was written for (ci-s7qh10).
//
// DELIBERATELY ABSENT: `assign`, `label`, `set-state`, `edit`, `batch` and the
// other bd verbs that can also reach labels or metadata. internal/bdflags
// carries no flag manifest for them, so their argv cannot be scanned for the
// unknown-flag ambiguity below, and admitting one without a manifest would
// hand every validator an argv it silently misreads. Adding a verb here means
// adding its manifest first.
//
// Also absent, and for a different reason: `create -f/--file` and
// `create --graph` build many beads from a file this scan never opens. The
// verb is gated, so the validator is invoked, but the labels and metadata in
// that file are invisible to any argv-shaped validator.
var bdPreWriteVerbs = map[string]bool{
	"create": true,
	"update": true,
	"close":  true,
	"reopen": true,
	"delete": true,
}

// bdPreWriteMutation reports whether args is a bd write the city's pre-write
// validator must see, and whether an unrecognized flag leaves the argv unsafe
// for that validator to parse.
//
// The verb is located with bdflags.SplitGlobalFlags rather than read from
// args[0]: the naive read takes "bob" out of
// `bd --actor bob update <id> --add-label harness:x`, and every gate keyed off
// the verb then stops firing for exactly the authors who pass an explicit
// actor -- with the validator's own suite still green, because it parses the
// argv it is handed and is simply never handed this one.
func bdPreWriteMutation(args []string) (mutation bool, ambiguous bool) {
	verb, rest := bdflags.SplitGlobalFlags(args)
	if !bdPreWriteVerbs[verb] {
		return false, false
	}
	valueFlags := bdflags.ValueFlags(verb)
	boolFlags := bdflags.BoolFlags(verb)
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			continue
		}
		// An inline --flag=value consumes no following token, so an
		// unrecognized name in that form cannot shift a positional.
		if strings.Contains(arg, "=") {
			continue
		}
		name := strings.TrimLeft(arg, "-")
		long, short := "--"+name, "-"+name
		if valueFlags[long] || (len(name) == 1 && valueFlags[short]) {
			i++
			continue
		}
		if boolFlags[long] || (len(name) == 1 && boolFlags[short]) {
			continue
		}
		return true, true
	}
	return true, false
}

// runBdPreWriteCommand runs a city-configured validator before a gc bd write.
// The validator receives the exact bd argument vector plus the resolved city
// and store roots in its environment. A non-zero validator exit refuses the
// write before bd is invoked.
func runBdPreWriteCommand(command, cityPath, storeRoot string, bdArgs []string, stderr io.Writer) bool {
	return runBdPreWriteCommandWithEnv(command, cityPath, storeRoot, bdArgs, os.Environ(), stderr)
}

func runBdPreWriteCommandWithEnv(command, cityPath, storeRoot string, bdArgs, env []string, stderr io.Writer) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}
	if !filepath.IsAbs(command) {
		command = filepath.Join(cityPath, command)
	}
	encodedArgs, err := json.Marshal(bdArgs)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: pre-write validation failed: encoding command arguments: %v\n", err) //nolint:errcheck // best-effort stderr
		return true
	}
	cmd := exec.Command(command)
	cmd.Dir = cityPath
	cmd.Env = mergeRuntimeEnv(env, map[string]string{
		"GC_CITY":         cityPath,
		"GC_STORE_ROOT":   storeRoot,
		"GC_BD_ARGS_JSON": string(encodedArgs),
	})
	// A validator's stdout must not corrupt bd's stdout (especially --json), so
	// route both streams to stderr. Validators should emit only a refusal reason.
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(stderr, "gc bd: pre-write validation failed (%s): %v\n", command, err) //nolint:errcheck // best-effort stderr
		return true
	}
	return false
}
