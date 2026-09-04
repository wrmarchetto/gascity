package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// renudgeTestscriptParams builds the testscript environment for one
// renudge-stale-human-gates scenario. Each txtar ships its own bin/gc
// stand-in, so the only shared setup is materializing the real script out of
// PackFS and collapsing both cadence windows to zero -- the sweeps have to be
// immediately eligible or a scenario would have to wait out wall time it
// cannot control.
func renudgeTestscriptParams(txtar string) testscript.Params {
	return testscript.Params{
		Files: []string{filepath.Join("testdata", txtar)},
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
	}
}

// TestRenudgeStaleHumanGatesReplacesPriorReminder proves the cooldown sweep
// leaves one open reminder per gate: on the second sweep the successor is
// delivered AND the predecessor is archived. The txtar asserts both facts by
// reminder identity rather than by counting open reminders, because a count
// of one is equally consistent with a sweep that sent nothing and archived
// nothing -- which is the ci-o34bax failure the suite has to be able to see.
//
// Its bin/gc refuses a non-positive --limit the way the real command does. An
// earlier stand-in accepted --limit 0, and that acceptance is the whole reason
// this suite stayed green while every repeat reminder in the fleet was being
// dropped before it was sent.
func TestRenudgeStaleHumanGatesReplacesPriorReminder(t *testing.T) {
	t.Parallel()
	testscript.Run(t, renudgeTestscriptParams("renudge-stale-human-gates.txtar"))
}

// TestRenudgeStaleHumanGatesDeliversDespiteLookupFailure pins the ordering
// invariant on its own: the predecessor snapshot is advisory, so no failure of
// it may suppress the reminder. This is deliberately separate from the flag
// value that triggered ci-o34bax -- pinning only the flag would leave the
// reminder behind any other lookup failure, and the flag was the trigger, not
// the defect. The stand-in fails the query for an unnamed reason for exactly
// that reason.
func TestRenudgeStaleHumanGatesDeliversDespiteLookupFailure(t *testing.T) {
	t.Parallel()
	testscript.Run(t, renudgeTestscriptParams("renudge-stale-human-gates-lookup-failure.txtar"))
}
