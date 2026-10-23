//go:build (linux && !android) || (darwin && !ios)

package productmetrics

import (
	"testing"

	"github.com/gastownhall/gascity/internal/testpolicy/parallelguard"
)

// processWideHazards name state every test in the process shares. A test
// reaching one, directly or through a package function, must run serially.
// Every other test calls t.Parallel first: the package's cost is fsync
// latency, which overlaps across goroutines (TESTING.md).
var processWideHazards = parallelguard.Hazards{
	"Setenv":                    "the process environment",
	"storageTempSequence":       "the process-wide temp-name counter",
	"closeOpenFileMatchingPath": "a raw close of a descriptor number production still owns",
	`"/proc/self/fd"`:           "the process descriptor table",
}

func TestEveryTopLevelTestRunsInParallelUnlessItReachesProcessWideState(t *testing.T) {
	t.Parallel()
	parallelguard.Check(t, processWideHazards)
}
