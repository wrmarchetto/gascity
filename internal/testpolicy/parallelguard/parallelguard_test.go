package parallelguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSerialFollowsPackageHelpersToEveryHazardKind(t *testing.T) {
	t.Parallel()
	hazards := Hazards{
		"storageTempSequence":       "the process-wide temp-name counter",
		"closeOpenFileMatchingPath": "a raw close of a descriptor number production still owns",
		`"/proc/self/fd"`:           "the process descriptor table",
		"os/exec.Command":           "a fork that inherits every open descriptor",
	}
	// Indented so a line-anchored count of the suite's test functions skips
	// these fixture declarations.
	const tests = `package p
	import osexec "os/exec"
	var seam = func() {}
	var table = map[string]int{}
	const fdTable = "/proc/self/fd"
	var predict = func() uint64 { return storageTempSequence.Load() + 1 }
	type fixture struct{}
	func (fixture) start()   { osexec.Command("x") }
	func helper()   { closeOpenFileMatchingPath("x") }
	func indirect() { helper() }
	func swap[T any](target *T, value T) { *target = value }
	func TestCounter(t *testing.T)  { storageTempSequence.Load() }
	func TestHelper(t *testing.T)   { indirect() }
	func TestTable(t *testing.T)    { os.ReadDir("/proc/self/fd") }
	func TestEnv(t *testing.T)      { t.Run("x", func(t *testing.T) { t.Setenv("A", "") }) }
	func TestFork(t *testing.T)     { fixture{}.start() }
	func TestSeam(t *testing.T)     { prev := seam; seam = func() {}; _ = prev }
	func TestEntry(t *testing.T)    { table["k"]++ }
	func TestConst(t *testing.T)    { os.ReadDir(fdTable) }
	func TestPredict(t *testing.T)  { _ = predict() }
	func TestSwap(t *testing.T)     { swap(&seam, nil) }
	func TestForeign(t *testing.T)  { osexec.ErrDot = nil }
	func TestWrite(t *testing.T)    { atomicWrite() }
	func TestUnshared(t *testing.T) { t.Parallel(); seam := 1; helperFree(seam) }
	func TestOtherExec(t *testing.T) { t.Parallel(); exec.Command("x") }
	func TestRename(t *testing.T)   { t.Parallel(); rename() }
	func helperFree(int)            {}
	`
	// Production code may touch what a test must not: a listed counter, a
	// package var, a listed literal. Only a process-wide call counts there.
	const production = `package p
	import "os"
	var storageTempSequence atomic.Uint64
	var cache map[string]int
	func rename()      { storageTempSequence.Add(1); cache = nil; os.ReadDir("/proc/self/fd") }
	func atomicWrite() { rename(); os.Setenv("A", "") }
	`
	got := serialOf(t, hazards, tests, production)
	want := map[string]string{
		"TestCounter": hazards["storageTempSequence"],
		"TestHelper":  hazards["closeOpenFileMatchingPath"],
		"TestTable":   hazards[`"/proc/self/fd"`],
		"TestEnv":     "the process environment",
		"TestFork":    hazards["os/exec.Command"],
		"TestSeam":    "package var seam",
		"TestEntry":   "package var table",
		"TestConst":   hazards[`"/proc/self/fd"`],
		"TestPredict": hazards["storageTempSequence"],
		"TestSwap":    "package var seam",
		"TestForeign": "package var os/exec.ErrDot",
		"TestWrite":   "the process environment",
	}
	requireSerial(t, got, want)

	// Each process-wide call, reached through production code by a test
	// that lists no hazards at all.
	for call, reason := range map[string]string{
		`os.Setenv("A", "")`:                        "the process environment",
		`os.Unsetenv("A")`:                          "the process environment",
		`os.Clearenv()`:                             "the process environment",
		`os.Chdir("/")`:                             "the process working directory",
		`unix.Fchdir(0)`:                            "the process working directory",
		`syscall.Umask(0)`:                          "the process file-creation mask",
		`unix.Setrlimit(0, nil)`:                    "a process resource limit",
		`unix.Prlimit(0, 0, nil, nil)`:              "a process resource limit",
		`signal.Notify(nil)`:                        "process signal delivery",
		`signal.NotifyContext(nil)`:                 "process signal delivery",
		`signal.Ignore()`:                           "process signal delivery",
		`signal.Reset()`:                            "process signal delivery",
		`notifier.Notify(); unix.Getrlimit(0, nil)`: "",
	} {
		const tests = `package p
		func TestReach(t *testing.T) { t.Parallel(); reach() }
		`
		production := "package p\nimport \"os/signal\"\nfunc reach() { " + call + " }\n"
		want := map[string]string{"TestReach": reason}
		if reason == "" {
			want = map[string]string{}
		}
		requireSerial(t, serialOf(t, Hazards{}, tests, production), want)
	}
}

func serialOf(t *testing.T, hazards Hazards, tests, production string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	testFile, err := parser.ParseFile(fset, "p_test.go", tests, 0)
	if err != nil {
		t.Fatal(err)
	}
	productionFile, err := parser.ParseFile(fset, "p.go", production, 0)
	if err != nil {
		t.Fatal(err)
	}
	return Serial([]*ast.File{testFile}, []*ast.File{productionFile}, hazards)
}

func requireSerial(t *testing.T, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("Serial = %v, want %v", got, want)
	}
	for name, hazard := range want {
		if got[name] != hazard {
			t.Errorf("Serial[%s] = %q, want %q", name, got[name], hazard)
		}
	}
}

// recorder keeps check's verdicts instead of failing the test running it.
type recorder struct {
	testing.TB
	name     string
	failures []string
}

func (r *recorder) Helper()      {}
func (r *recorder) Name() string { return r.name }
func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	runtime.Goexit()
}

func TestCheckRefusesATestOnEitherWrongSide(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const source = `package p
	import "testing"
	func TestGuard(t *testing.T)               { t.Parallel() }
	func TestRight(t *testing.T)               { t.Setenv("A", "") }
	func TestSharedButParallel(t *testing.T)   { t.Parallel(); t.Setenv("A", "") }
	func TestUnsharedButSerial(t *testing.T)   { _ = 1 }
	func TestSharedInProduction(t *testing.T)  { t.Parallel(); write() }
	func TestCountedInProduction(t *testing.T) { t.Parallel(); count() }
	`
	const production = `package p
	import "os"
	func write() { os.Setenv("A", "") }
	func count() { counter.Add(1) }
	`
	for name, content := range map[string]string{"p_test.go": source, "p.go": production} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hazards := Hazards{"counter": "a counter the tests predict"}
	failures := checkFailures(t, dir, hazards)
	want := []string{"TestSharedButParallel", "TestUnsharedButSerial", "TestSharedInProduction"}
	if len(failures) != len(want) {
		t.Fatalf("check failures = %q, want one each for %v", failures, want)
	}
	for index, name := range want {
		if !strings.HasPrefix(failures[index], name+" ") {
			t.Errorf("failure %d = %q, want it to name %s", index, failures[index], name)
		}
	}
	// A scan of the wrong directory sees no tests, which must not read as
	// a package with nothing to refuse.
	if failures := checkFailures(t, t.TempDir(), hazards); len(failures) != 1 || !strings.HasPrefix(failures[0], "parsed 0 test files") {
		t.Errorf("check failures over an empty directory = %q, want one empty-scan refusal", failures)
	}
}

func checkFailures(t *testing.T, dir string, hazards Hazards) []string {
	r := &recorder{TB: t, name: "TestGuard"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		check(r, dir, hazards)
	}()
	<-done
	return r.failures
}
