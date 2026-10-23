package parallelguard

import (
	"go/ast"
	"go/parser"
	"go/token"
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
