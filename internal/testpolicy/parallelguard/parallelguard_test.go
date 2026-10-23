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
		"Setenv":                    "the process environment",
		"storageTempSequence":       "the process-wide temp-name counter",
		"closeOpenFileMatchingPath": "a raw close of a descriptor number production still owns",
		`"/proc/self/fd"`:           "the process descriptor table",
		"os/exec.Command":           "a fork that inherits every open descriptor",
	}
	// Indented so a line-anchored count of the suite's test functions skips
	// these fixture declarations.
	const source = `package p
	import osexec "os/exec"
	var seam = func() {}
	var table = map[string]int{}
	type fixture struct{}
	func (fixture) start()   { osexec.Command("x") }
	func helper()   { closeOpenFileMatchingPath("x") }
	func indirect() { helper() }
	func TestCounter(t *testing.T)  { storageTempSequence.Load() }
	func TestHelper(t *testing.T)   { indirect() }
	func TestTable(t *testing.T)    { os.ReadDir("/proc/self/fd") }
	func TestEnv(t *testing.T)      { t.Run("x", func(t *testing.T) { t.Setenv("A", "") }) }
	func TestFork(t *testing.T)     { fixture{}.start() }
	func TestSeam(t *testing.T)     { prev := seam; seam = func() {}; _ = prev }
	func TestEntry(t *testing.T)    { table["k"]++ }
	func TestUnshared(t *testing.T) { t.Parallel(); seam := 1; helperFree(seam) }
	func TestOtherExec(t *testing.T) { t.Parallel(); exec.Command("x") }
	func helperFree(int)            {}
	`
	file, err := parser.ParseFile(token.NewFileSet(), "p_test.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := Serial([]*ast.File{file}, PackageVars(file), hazards)
	want := map[string]string{
		"TestCounter": hazards["storageTempSequence"],
		"TestHelper":  hazards["closeOpenFileMatchingPath"],
		"TestTable":   hazards[`"/proc/self/fd"`],
		"TestEnv":     hazards["Setenv"],
		"TestFork":    hazards["os/exec.Command"],
		"TestSeam":    "package var seam",
		"TestEntry":   "package var table",
	}
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
	func TestGuard(t *testing.T)            { t.Parallel() }
	func TestRight(t *testing.T)            { t.Setenv("A", "") }
	func TestSharedButParallel(t *testing.T) { t.Parallel(); t.Setenv("A", "") }
	func TestUnsharedButSerial(t *testing.T) { _ = 1 }
	`
	if err := os.WriteFile(filepath.Join(dir, "p_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	hazards := Hazards{"Setenv": "the process environment"}
	failures := checkFailures(t, dir, hazards)
	want := []string{"TestSharedButParallel", "TestUnsharedButSerial"}
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
