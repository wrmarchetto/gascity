package dolt_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestProviderScriptDoesNotClobberGCDirForItsChildren pins that
// gc-beads-bd.sh leaves GC_DIR exactly as gc set it.
//
// GC_DIR is gc's own session variable -- internal/runtime/runtime.go sets it
// to the session's work_dir, and cmd/gc/main.go's resolveContext feeds it to
// findCity() when no GC_CITY is present. findCity returns the NEAREST
// city.toml walking UP, so handing it a path one level below a directory that
// carries its own city.toml resolves that directory as the city. A city-repo
// worktree carries a tracked city.toml, which is exactly that shape.
//
// The script used GC_DIR as a scratch name for its own derived
// "$GC_CITY_PATH/.gc" path. Nothing exported it there and nothing needed to:
// the variable arrives already exported from gc, and assigning to an
// inherited exported variable keeps it exported, so every descendant -- the
// `gc dolt-state runtime-layout` helper call below, `gc dolt-state
// start-managed`, and the managed dolt server and scope watchdog it leaves
// running -- inherited GC_DIR=<city>/.gc. Measured 2026-09-06: a dolt
// sql-server and its watchdog had carried
// GC_DIR=<city>/.gc/worktrees/city/toolsmith-2/.gc for 15 hours (ci-ewyqum,
// carried forward from ci-8sk9am).
//
// WHY A GREP FOR THE SUFFIX FOUND NOTHING, which is what left this open: no
// Go code ever appends ".gc" to GC_DIR. The corruption is a NAME COLLISION in
// a shell script, and the only Go assignment is the correct one.
//
// The assertion is on what a CHILD SEES, not on the script's text. A lexical
// check that the script contains no "GC_DIR=" would pass the moment someone
// spelled the same clobber differently, and the property that matters is the
// environment a descendant gc actually resolves against.
func TestProviderScriptDoesNotClobberGCDirForItsChildren(t *testing.T) {
	script := filepath.Join(repoRoot(t), "..", "assets", "scripts", "gc-beads-bd.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("provider script not found: %v", err)
	}

	cityPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityPath, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The stub stands in for gc and REFUSES to answer, so the script takes its
	// documented fallback rather than depending on a real runtime layout. It
	// records the GC_DIR it was handed, which is the whole measurement -- a
	// stub that answered successfully would still record it, but would also
	// couple this case to the layout contract it has no business pinning.
	record := filepath.Join(t.TempDir(), "seen-gc-dir")
	stub := filepath.Join(t.TempDir(), "gc")
	stubBody := "#!/bin/sh\nprintf '%s' \"${GC_DIR-<unset>}\" > " + record + "\nexit 1\n"
	if err := os.WriteFile(stub, []byte(stubBody), 0o755); err != nil {
		t.Fatal(err)
	}

	// A sentinel that is not derivable from cityPath, so the assertion cannot
	// be satisfied by the script recomputing something that merely looks right.
	sentinel := filepath.Join(t.TempDir(), "the-session-work-dir")

	cmd := exec.Command("sh", script, "health")
	cmd.Env = append(filteredEnv(),
		"GC_CITY_PATH="+cityPath,
		"GC_DIR="+sentinel,
		"GC_BIN="+stub,
	)
	// Its exit status is not the subject: `health` against an empty fixture
	// legitimately fails, and the corruption happens before any op runs.
	_ = cmd.Run()

	seen, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the stub gc was never invoked, so this case measured nothing: %v", err)
	}
	if got := strings.TrimSpace(string(seen)); got != sentinel {
		t.Errorf("a child gc saw GC_DIR=%q, want %q\n"+
			"the provider script overwrote gc's session variable; a descendant "+
			"resolving a city from it walks up from <city>/.gc and can land on a "+
			"worktree's own city.toml", got, sentinel)
	}
}
