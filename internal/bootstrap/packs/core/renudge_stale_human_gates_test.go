package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestRenudgeStaleHumanGatesReplacesPriorReminder proves the cooldown sweep
// leaves one open reminder per gate. The testscript runs two immediately
// eligible sweeps without waiting for wall time.
func TestRenudgeStaleHumanGatesReplacesPriorReminder(t *testing.T) {
	t.Parallel()
	testscript.Run(t, testscript.Params{
		Files: []string{filepath.Join("testdata", "renudge-stale-human-gates.txtar")},
		Setup: func(env *testscript.Env) error {
			stateDir := filepath.Join(env.WorkDir, "state")
			scriptDir := filepath.Join(env.WorkDir, "scripts")
			for _, dir := range []string{stateDir, scriptDir} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			for _, path := range []string{"assets/scripts/renudge-stale-human-gates.sh", "assets/scripts/_bd_trace.sh"} {
				data, err := PackFS.ReadFile(path)
				if err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(scriptDir, filepath.Base(path)), data, 0o755); err != nil {
					return err
				}
			}
			if err := os.Chmod(filepath.Join(env.WorkDir, "bin", "gc"), 0o755); err != nil {
				return err
			}
			env.Setenv("PATH", filepath.Join(env.WorkDir, "bin")+":"+env.Getenv("PATH"))
			env.Setenv("GC_PACK_STATE_DIR", stateDir)
			env.Setenv("GC_STALE_GATE_THRESHOLD", "0s")
			env.Setenv("GC_STALE_GATE_RENUDGE_INTERVAL", "0s")
			env.Setenv("RENUDGE_TEST_STATE", stateDir)
			return nil
		},
	})
}
