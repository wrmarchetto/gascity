// Package scripts_test pins the local parallel fan-out's handling of
// internal/productmetrics.
//
// The suite exists because that one package was, on its own, the entire
// remaining cost of a wide push. Measured 2026-08-09 (ci-qst5) on a
// GC_FAST_UNIT=1 -p 4 sweep: unit-core took 995s across 187 non-cmd/gc
// packages, of which internal/productmetrics alone was 887s -- 89% of the
// sweep, and 5.6x the next slowest package. ci-4w2t's scoped push gate
// deliberately does not help: roughly half of real pushes touch a package low
// enough in the import graph that the closure fans out to nearly every
// package, so those pushes still sweep productmetrics serially.
//
// The cost is filesystem sync latency, not sleeps and not CPU. The package's
// storage layer fsyncs after every create, rename, and unlink, and its 464
// tests drive those paths thousands of times against a real disk. Two
// measurements, this worktree, ext4 on nvme:
//
//	strace -c -w of the slowest single test  5046 fsync calls, 38.1s in fsync
//	whole package, fixtures on ext4 /tmp     254s
//	whole package, fixtures on tmpfs         8s
//
// So it is genuine, latency-bound work that overlaps well when run
// concurrently -- the case sharding is for. Sharding a package whose cost was
// a fixed sleep would only multiply the sleeping, which is why the fsync
// evidence above is recorded here rather than in a commit message alone.
//
// Run: go test ./scripts -run Productmetrics
//
// Scope: this file pins only the fan-out's job wiring. Round-robin shard
// selection itself belongs to scripts/test_go_test_shard_test.go, and the
// shared timeout-budget contract to scripts/test_local_parallel_budget_test.go.
package scripts_test

import (
	"regexp"
	"strings"
	"testing"
)

const productmetricsPackage = "internal/productmetrics"

// TestUnitSweepExcludesProductmetricsFromItsPackageList is the gate that keeps
// the sharding from being silently defeated. The unit-core job sweeps every
// non-cmd/gc package in one `go test`; leaving productmetrics in that list
// while also sharding it runs the package twice, once at full serial cost, so
// the fan-out's wall clock never improves and the shards look like pure waste.
//
// This checks both precise exclusions and the complete `go list ./...` input.
// Together they prevent a duplicate serial run without letting an over-broad
// replacement silently shrink the sweep.
func TestUnitSweepExcludesProductmetricsFromItsPackageList(t *testing.T) {
	command := unitCoreJobCommand(t)
	for _, excluded := range []string{"cmd/gc", productmetricsPackage} {
		needle := "'^github.com/gastownhall/gascity/" + excluded + "\\$'"
		if !strings.Contains(command, needle) {
			t.Errorf("unit-core package expression does not exclude %s; it is sharded separately and would run twice, once unsharded at full serial cost:\n%s", excluded, command)
		}
	}
	if !strings.Contains(command, "go list ./...") {
		t.Fatalf("unit-core job no longer starts from the complete package list, so its exclusions could silently become a partial sweep:\n%s", command)
	}
}

// TestProductmetricsShardsCoverEveryShardIndex pins that the fan-out registers
// a complete 1..total set. A loop that lost an index would drop those tests
// from the local gate entirely and still report every registered job green.
func TestProductmetricsShardsCoverEveryShardIndex(t *testing.T) {
	body := shellFunctionBody(t, localParallelScript(t), "add_productmetrics_shards")

	if !strings.Contains(body, "./scripts/test-go-test-shard ./"+productmetricsPackage+" ") {
		t.Fatalf("add_productmetrics_shards does not shard ./%s:\n%s", productmetricsPackage, body)
	}
	total := shardTotalDefault(t)
	if total < 2 {
		t.Fatalf("productmetrics shard total = %d, want at least 2; one shard is the serial run this exists to replace", total)
	}
	if !strings.Contains(body, `seq 1 "$productmetrics_total"`) {
		t.Fatalf("add_productmetrics_shards does not iterate 1..$productmetrics_total:\n%s", body)
	}
}

// TestProductmetricsShardsRunUnderTheUnitLaneGate pins GC_FAST_UNIT=1. The
// package moved out of the unit-core job, which sets it to 1; a shard running
// with 0 would select a different set of tests than the sweep it replaced,
// and the difference is invisible from a green run.
func TestProductmetricsShardsRunUnderTheUnitLaneGate(t *testing.T) {
	body := shellFunctionBody(t, localParallelScript(t), "add_productmetrics_shards")

	if !strings.Contains(body, "GC_FAST_UNIT=1") {
		t.Fatalf("productmetrics shards do not set GC_FAST_UNIT=1, so they no longer run the unit lane's test selection:\n%s", body)
	}
	if !strings.Contains(body, "GO_TEST_COUNT=1") {
		t.Fatalf("productmetrics shards omit GO_TEST_COUNT=1 and may report a cached result instead of a real run:\n%s", body)
	}
}

// TestProductmetricsShardsShareTheFanOutTimeoutBudget extends the ga-9au
// contract to the new jobs: every job in one fan-out contends for the same
// box, so a package slow enough to time out must time out for the same reason
// in any of them. Two independently maintained numbers are what drifted apart
// before.
func TestProductmetricsShardsShareTheFanOutTimeoutBudget(t *testing.T) {
	script := localParallelScript(t)
	body := shellFunctionBody(t, script, "add_productmetrics_shards")

	match := regexp.MustCompile(`GO_TEST_TIMEOUT=([^\s"]+)`).FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("add_productmetrics_shards sets no GO_TEST_TIMEOUT, so its shards inherit scripts/test-go-test-shard's own default:\n%s", body)
	}
	shard := resolveShellVars(t, script, match[1])
	if unitCore := goTestFlagValue(t, unitCoreJobCommand(t), "timeout"); unitCore != shard {
		t.Fatalf("unit-core -timeout = %q but productmetrics shards use GO_TEST_TIMEOUT=%q; the fan-out must share one budget", unitCore, shard)
	}
}

// TestProductmetricsShardsRunInEveryModeThatSweptThePackage pins the two modes
// that previously covered productmetrics through unit-core. `full` is the
// merge baseline and must stay complete; `fast` is the push gate. A mode that
// lost the shards after the exclusion above landed would drop the package from
// that gate with no failing job to show for it.
func TestProductmetricsShardsRunInEveryModeThatSweptThePackage(t *testing.T) {
	script := localParallelScript(t)

	for _, mode := range []string{"fast", "full"} {
		if body := shellCaseArmBody(t, script, mode); !strings.Contains(body, "add_productmetrics_shards") {
			t.Errorf("mode %q no longer registers the productmetrics shards, but unit-core no longer sweeps the package either:\n%s", mode, body)
		}
	}
}

// TestProductmetricsShardsRegisterOncePerMode keeps a second registration
// from running every shard twice. The full and fast lanes own this package;
// the other modes must not register its shard fan-out.
func TestProductmetricsShardsRegisterOncePerMode(t *testing.T) {
	script := localParallelScript(t)

	for _, test := range []struct {
		mode string
		want int
	}{
		{mode: "fast", want: 1},
		{mode: "cmd-gc-process", want: 0},
		{mode: "integration", want: 0},
		{mode: "full", want: 1},
	} {
		t.Run(test.mode, func(t *testing.T) {
			arm := shellCaseArmBody(t, script, test.mode)
			if got := strings.Count(arm, "add_productmetrics_shards"); got != test.want {
				t.Errorf("%s mode registers productmetrics shards %d times, want %d:\n%s", test.mode, got, test.want, arm)
			}
		})
	}
}

// TestFastRunQueuesProductmetricsBeforeCmdGCShards keeps the productmetrics
// fan-out from being accidentally throttled when LOCAL_TEST_JOBS permits a
// wider run. With a ten-job cap, the unit-core job plus six cmd/gc shards
// leave only three slots for the six productmetrics jobs when cmd/gc is queued
// first, turning the intended disk-latency overlap into two waves. The two
// shard groups are independent, so queue the slower productmetrics jobs first.
func TestFastRunQueuesProductmetricsBeforeCmdGCShards(t *testing.T) {
	arm := shellCaseArmBody(t, localParallelScript(t), "fast")
	productmetrics := strings.Index(arm, "add_productmetrics_shards")
	cmdGC := strings.Index(arm, "add_cmd_gc_shards")
	if productmetrics < 0 || cmdGC < 0 {
		t.Fatalf("fast arm must register both shard groups:\n%s", arm)
	}
	if productmetrics > cmdGC {
		t.Fatalf("fast arm queues cmd/gc before productmetrics, leaving only three of ten default job slots for the six productmetrics shards:\n%s", arm)
	}
}

// TestScopedFastRunRoutesProductmetricsToItsShards pins the scoped-push path
// (ci-4w2t). .githooks/pre-push hands the runner the package args a push can
// reach; productmetrics must be split out of that list the way ./cmd/gc is, or
// the scoped unit sweep runs it unsharded and the narrow-push saving the scope
// exists for is spent on this one package.
func TestScopedFastRunRoutesProductmetricsToItsShards(t *testing.T) {
	script := localParallelScript(t)

	if !strings.Contains(script, `"./`+productmetricsPackage+`"`) {
		t.Fatalf("scripts/test-local-parallel never names ./%s, so a scoped run cannot route it to the shards", productmetricsPackage)
	}
	if !strings.Contains(script, "scoped_has_productmetrics") {
		t.Fatal("scripts/test-local-parallel tracks no scoped_has_productmetrics flag, so a scoped push that touches the package skips its shards")
	}
	arm := shellCaseArmBody(t, script, "fast")
	if !strings.Contains(arm, "scoped_has_productmetrics") {
		t.Fatalf("the fast arm ignores scoped_has_productmetrics:\n%s", arm)
	}
}

// shardTotalDefault reads the productmetrics shard count the runner uses when
// no operator override is present.
func shardTotalDefault(t *testing.T) int {
	t.Helper()
	script := localParallelScript(t)
	match := regexp.MustCompile(`productmetrics_total="\$\{PRODUCTMETRICS_TOTAL:-(\d+)\}"`).FindStringSubmatch(script)
	if match == nil {
		t.Fatal("scripts/test-local-parallel defines no PRODUCTMETRICS_TOTAL-defaulted shard count")
	}
	total := 0
	for _, digit := range match[1] {
		total = total*10 + int(digit-'0')
	}
	return total
}

// shellCaseArmBody extracts the body of a `case "$mode"` arm, from the
// two-space-indented `name)` label to its `;;`.
func shellCaseArmBody(t *testing.T, script, name string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?ms)^  ` + regexp.QuoteMeta(name) + `\)\n(.*?)^    ;;`)
	match := pattern.FindStringSubmatch(script)
	if match == nil {
		t.Fatalf("scripts/test-local-parallel defines no %q case arm", name)
	}
	return match[1]
}
