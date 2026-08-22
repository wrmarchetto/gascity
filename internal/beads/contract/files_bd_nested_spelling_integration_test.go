//go:build integration

package contract

// Scope: the one assertion in this package that no hand-written fixture can
// make -- that the key EnsureCanonicalConfig deleted is the key a REAL bd
// resolves, in the spelling a real bd actually writes.
//
// This row exists because the defect it guards hid behind a green suite for
// exactly this reason. Every nested fixture in
// files_nested_key_deletion_test.go is a recorded copy of bd's output, and a
// recording goes stale silently: if bd changes its indentation or stops
// nesting, those tests keep passing against a shape nothing produces anymore.
// This row re-derives the shape from the binary each run, so the recording's
// expiry is observable.
//
// The fast-lane suite carries the deletion's behavioral coverage, so a machine
// without bd is not left with the fix unguarded -- what it loses is only the
// proof that bd's spelling has not moved.
//
// Run:
//   go test -tags integration ./internal/beads/contract/ \
//     -run BdNestedSpelling

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/fsys"
)

// runBd invokes bd with dir as its working directory, so bd's own upward walk
// for `.beads` resolves to the fixture and not to whatever store the test
// machine happens to sit inside. BEADS_DIR and BD_CONFIG_PATH are cleared for
// the same reason: an operator shell exporting either would silently redirect
// every write here into a real store.
func runBd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("bd", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BEADS_DIR=", "BD_CONFIG_PATH=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestEnsureCanonicalConfigClearsBdNestedSpellingPassword is the end-to-end
// proof: a password written by real bd is unreadable by real bd afterwards.
//
// The secret is derived from the per-run temp directory name rather than being
// a constant. A constant would also be reported gone by a test that read a
// leftover config from a previous run, or by one whose bd write silently landed
// somewhere else -- both look identical to success. Deriving the value from the
// salt is what makes "bd no longer resolves it" mean "our deletion removed the
// value we just wrote".
func TestEnsureCanonicalConfigClearsBdNestedSpellingPassword(t *testing.T) {
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skipf("bd not on PATH, so bd's config spelling cannot be re-derived: %v", err)
	}

	dir := t.TempDir()
	// The salt is the PARENT's name, not dir's own. t.TempDir() hands back
	// `<random>/001` -- a per-test sequence number under the random part -- so
	// filepath.Base(dir) is the constant "001" and would salt nothing. Measured
	// while writing this test, against exactly the leftover-value case the salt
	// exists to catch.
	secret := "gc-nested-" + filepath.Base(filepath.Dir(dir))
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(beadsDir, "config.yaml")
	// Non-empty on purpose, and this is the whole reproducer: bd's
	// updateNestedYamlKey returns early only when the document is EMPTY, so a
	// dotted key written into a populated file lands nested. Seeding this file
	// empty would produce the flat spelling and the test would pass without the
	// fix (engdocs/contributors/bd-config-unset-nested-key.md).
	if err := os.WriteFile(path, []byte("issue_prefix: xx\ndolt.mode: server\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	runBd(t, dir, "config", "set", "dolt.password", secret)

	seeded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Premise check, not a tautology: if a future bd writes this key flat, the
	// deletion below would pass on the flat path and prove nothing about the
	// nested one. Failing here says the recorded fixtures in
	// files_nested_key_deletion_test.go have expired, not that gc regressed.
	if strings.Contains(string(seeded), "dolt.password:") {
		t.Fatalf("bd wrote dolt.password FLAT; the nested-spelling premise this suite is built on has expired:\n%s", seeded)
	}
	if !strings.Contains(string(seeded), "password: "+secret) {
		t.Fatalf("bd did not write the secret into %s:\n%s", path, seeded)
	}

	changed, err := EnsureCanonicalConfig(fsys.OSFS{}, path, ConfigState{IssuePrefix: "gc"})
	if err != nil {
		t.Fatalf("EnsureCanonicalConfig() error = %v", err)
	}
	if !changed {
		t.Fatal("EnsureCanonicalConfig() reported no change against a bd-written nested dolt.password")
	}

	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(final), secret) {
		t.Fatalf("the secret is still in the git-tracked config:\n%s", final)
	}
	// bd's read path resolves BOTH spellings, which makes this the assertion
	// that matters: it fails if the deletion removed a copy bd was not reading.
	if got := runBd(t, dir, "config", "get", "dolt.password"); !strings.Contains(got, "not set") {
		t.Fatalf("bd still resolves dolt.password after canonicalization: %q", got)
	}
}
