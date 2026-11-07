package parallelguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
	var cfg struct{ n int }
	var ptr = new(int)
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
	func TestField(t *testing.T)    { cfg.n = 1 }
	func TestDeref(t *testing.T)    { *ptr = 1 }
	func TestParen(t *testing.T)    { (*ptr)++ }
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
		"TestField":   "package var cfg",
		"TestDeref":   "package var ptr",
		"TestParen":   "package var ptr",
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

// requireSerial reports each wrong name on its own line, since the summary
// echoes every wanted name and so cannot attribute a failure to one.
func requireSerial(t *testing.T, got, want map[string]string) {
	t.Helper()
	if maps.Equal(got, want) {
		return
	}
	t.Errorf("Serial = %v, want %v", got, want)
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if got[name] != want[name] {
			t.Errorf("Serial[%s] = %q, want %q", name, got[name], want[name])
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[name]; !ok {
			t.Errorf("Serial[%s] = %q, want it absent", name, got[name])
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
	hazards := Hazards{"counter": "a counter the tests predict"}
	dir := writePackage(t, map[string]string{"p_test.go": source, "p.go": production})
	want := []string{"TestSharedButParallel", "TestUnsharedButSerial", "TestSharedInProduction"}
	refused := map[string]bool{}
	for _, failure := range checkFailures(t, dir, hazards) {
		name, _, _ := strings.Cut(failure, " ")
		if !slices.Contains(want, name) || refused[name] {
			t.Errorf("unexpected refusal %q", failure)
		}
		refused[name] = true
	}
	// One line per name: a sweep row expecting it is met by its own
	// mutation alone.
	for _, name := range want {
		if !refused[name] {
			t.Errorf("missing refusal for %s", name)
		}
	}

	// A scan that found no caller, or derived nothing serial, saw nothing
	// worth trusting. Each half of that refusal has a package of its own.
	const missingCaller = `package p
	import "testing"
	func TestRight(t *testing.T) { t.Setenv("A", "") }
	`
	const nothingSerial = `package p
	import "testing"
	func TestGuard(t *testing.T) { t.Parallel() }
	func TestPure(t *testing.T)  { t.Parallel(); _ = 1 }
	`
	for _, scan := range []struct {
		label   string
		files   map[string]string
		refusal string
	}{
		{"an empty directory", nil, "parsed 0 test files"},
		{"a package missing its caller", map[string]string{"p_test.go": missingCaller}, "found TestGuard false"},
		{"a package with nothing serial", map[string]string{"p_test.go": nothingSerial}, "derived 0 serial tests"},
	} {
		failures := checkFailures(t, writePackage(t, scan.files), hazards)
		if len(failures) != 1 || !strings.Contains(failures[0], scan.refusal) {
			t.Errorf("check failures over %s = %q, want one refusal containing %q", scan.label, failures, scan.refusal)
		}
	}
}

func writePackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
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
