// scripts/canary_fetch_test.go
//
// Scope: scripts/canary-fetch.sh, through its shell suite
// scripts/canary-fetch.test.sh with gc and bd stubbed.
// Run: go test ./scripts/ -run TestCanaryFetch
// hands off to: scripts/canary_eval_test.go, for what the evaluator reads.
package scripts_test

import "testing"

func TestCanaryFetchLeavesAWholeReadingOrNone(t *testing.T) {
	runShellSuite(t, "canary-fetch.test.sh")
}
