package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
