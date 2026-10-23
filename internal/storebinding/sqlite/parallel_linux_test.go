package sqlite

import (
	"testing"

	"github.com/gastownhall/gascity/internal/testpolicy/parallelguard"
)

// processWideHazards name state every test in the process shares. Every
// other test calls t.Parallel first, so its fsync waits overlap. A fork
// shares the parent's open file descriptions until exec, and with them the
// OFD locks a concurrent writer fence holds or has just released.
var processWideHazards = parallelguard.Hazards{
	"Setenv":                 "the process environment",
	"Chdir":                  "the process working directory",
	"os/exec.Command":        "a fork that holds every open descriptor's OFD locks until exec",
	"os/exec.CommandContext": "a fork that holds every open descriptor's OFD locks until exec",
}

func TestEveryTopLevelTestRunsInParallelUnlessItReachesProcessWideState(t *testing.T) {
	t.Parallel()
	parallelguard.Check(t, processWideHazards)
}
