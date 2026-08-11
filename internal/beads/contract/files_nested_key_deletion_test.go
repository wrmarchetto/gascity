package contract

// Scope: EnsureCanonicalConfig's removal of keys bd may have written in NESTED
// spelling (`dolt:` / `  password:`) rather than flat (`dolt.password:`).
//
// This suite exists because the flat-only deletion it guards reported success
// while leaving a secret behind. `deprecatedConfigKeys` carries
// `dolt.password`, `.beads/config.yaml` is git-tracked, and bd chooses the
// spelling from whether the file was empty when the key was FIRST written --
// so the spelling that survived deletion was the one nobody wrote a fixture
// for. Every nested fixture here is therefore reproduced from real bd output
// (see the byte-for-byte note on doltNestedPasswordConfig), and the
// end-to-end proof that bd itself no longer resolves the key is delegated to
// the integration row in files_bd_nested_spelling_integration_test.go, which
// drives the real binary.
//
// The absence guards matter as much as the deletions: dolt.disable-event-flush
// is canonical in NESTED form (readDoltConfigFromRoot prefers it), so the
// nested-aware deletion must leave it alone, and a `password:` under any other
// section must survive.
//
// Run: go test ./internal/beads/contract/ -run Nested
//
// That pattern also picks up the pre-existing nested-spelling tests in
// files_test.go, which is wanted: they are what the exclusion above protects.

import (
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/gastownhall/gascity/internal/fsys"
)

// doltNestedPasswordConfig is byte-for-byte what
//
//	printf 'issue_prefix: xx\ndolt.mode: server\n' > .beads/config.yaml
//	bd config set dolt.password hunter2
//
// produced against bd 1.1.1-0.20260805093327-bf97b73749ac (the
// BD_CURRENT_VERSION `deps.env` pins), measured 2026-08-10. The FOUR-space
// indent is bd's, not a house style choice, and it is the reason the deletion
// must key off any indentation rather than the two spaces
// ensureFallbackNestedDoltDisableEventFlush writes.
const doltNestedPasswordConfig = "issue_prefix: xx\n" +
	"dolt.mode: server\n" +
	"dolt:\n" +
	"    password: hunter2\n"

// writeConfig seeds a fixture and returns its path. It deliberately does not
// hand back the fsys.FS it wrote through: every row here needs the real
// filesystem, so returning a value that is constant across the suite reads as
// a seam that does not exist (and golangci-lint's unparam rejects it).
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := (fsys.OSFS{}).WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// configRoot parses a fixture through the production reader so the node shapes
// the helpers see are the ones EnsureCanonicalConfig would hand them.
func configRoot(t *testing.T, body string) *yaml.Node {
	t.Helper()
	path := writeConfig(t, body)
	doc, err := readConfigDoc(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("readConfigDoc() error = %v", err)
	}
	return mappingRoot(doc)
}

func canonicalizeConfig(t *testing.T, path string, state ConfigState) (string, bool) {
	t.Helper()
	fs := fsys.OSFS{}
	changed, err := EnsureCanonicalConfig(fs, path, state)
	if err != nil {
		t.Fatalf("EnsureCanonicalConfig() error = %v", err)
	}
	data, err := fs.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data), changed
}

// TestEnsureCanonicalConfigDeletesNestedDottedPassword pins the defect: a
// nested dolt.password must not survive canonicalization.
//
// The assertion is on the secret VALUE, not on the key name. Asserting only
// `!strings.Contains(text, "dolt.password")` passes against the nested
// spelling without the deletion existing at all -- the nested form never
// contains that literal string -- which is how the flat-only deletion held a
// green suite.
func TestEnsureCanonicalConfigDeletesNestedDottedPassword(t *testing.T) {
	path := writeConfig(t, doltNestedPasswordConfig)

	text, changed := canonicalizeConfig(t, path, ConfigState{IssuePrefix: "gc"})

	if !changed {
		t.Fatal("EnsureCanonicalConfig() reported no change while a nested dolt.password was present")
	}
	if strings.Contains(text, "hunter2") {
		t.Fatalf("nested dolt.password survived canonicalization:\n%s", text)
	}
	if strings.Contains(text, "password") {
		t.Fatalf("nested dolt.password key survived canonicalization:\n%s", text)
	}
	// The dolt section itself is load-bearing -- disable-event-flush lives in
	// it -- so the deletion must take the leaf and not the block.
	dolt, ok, err := ReadDoltConfig(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("ReadDoltConfig() error = %v", err)
	}
	if !ok || dolt.DisableEventFlush == nil || !*dolt.DisableEventFlush {
		t.Fatalf("ReadDoltConfig() = (%+v, %v), want disable-event-flush true:\n%s", dolt, ok, text)
	}
	if !strings.Contains(text, "dolt.mode: server") {
		t.Fatalf("canonicalization dropped an unrelated key:\n%s", text)
	}
}

// TestEnsureCanonicalConfigDeletesNestedDottedPasswordIsIdempotent pins that
// the deletion converges. A delete-then-rewrite that reports changed on every
// call would make the contract layer rewrite a git-tracked file forever.
func TestEnsureCanonicalConfigDeletesNestedDottedPasswordIsIdempotent(t *testing.T) {
	path := writeConfig(t, doltNestedPasswordConfig)
	state := ConfigState{IssuePrefix: "gc"}

	first, changed := canonicalizeConfig(t, path, state)
	if !changed {
		t.Fatal("first EnsureCanonicalConfig() reported no change")
	}
	second, changed := canonicalizeConfig(t, path, state)
	if changed {
		t.Fatalf("second EnsureCanonicalConfig() reported a change; config is not converged:\n%s", second)
	}
	if first != second {
		t.Fatalf("second pass rewrote the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestEnsureCanonicalConfigKeepsNestedDisableEventFlush is the absence guard.
//
// dolt.disable-event-flush is deliberately NOT nested-deleted:
// readDoltConfigFromRoot resolves the nested spelling FIRST, so nested is the
// canonical form and setNestedBool writes it. Widening the nested deletion to
// every key on the flat-deletion list -- the obvious "generalize this"
// edit -- would delete what the same function just wrote, and the value would
// silently revert to the true default on any config that had it false.
func TestEnsureCanonicalConfigKeepsNestedDisableEventFlush(t *testing.T) {
	disabled := false
	path := writeConfig(t, "issue_prefix: xx\ndolt:\n    disable-event-flush: false\n")

	text, _ := canonicalizeConfig(t, path, ConfigState{
		IssuePrefix: "gc",
		Dolt:        DoltConfig{DisableEventFlush: &disabled},
	})

	dolt, ok, err := ReadDoltConfig(fsys.OSFS{}, path)
	if err != nil {
		t.Fatalf("ReadDoltConfig() error = %v", err)
	}
	if !ok || dolt.DisableEventFlush == nil {
		t.Fatalf("ReadDoltConfig() = (%+v, %v), want a disable-event-flush value:\n%s", dolt, ok, text)
	}
	if *dolt.DisableEventFlush {
		t.Fatalf("nested disable-event-flush was deleted and reverted to the default:\n%s", text)
	}
}

// TestEnsureCanonicalConfigKeepsNestedLeafUnderUnrelatedSection pins that the
// deletion resolves section AND leaf, never the leaf name alone.
//
// `password` is a plausible leaf under any section. Matching bare `password:`
// at any indentation would clear an unrelated credential -- the same widening
// the upstream bd patch declined for `sync.remote`
// (engdocs/contributors/bd-config-unset-nested-key.md).
//
// Both passwords sit in ONE fixture on purpose. Asserting only that the
// unrelated one survives would pass with the nested deletion removed
// altogether; requiring the dolt one to go in the same file is what makes this
// pin discrimination rather than inaction.
func TestEnsureCanonicalConfigKeepsNestedLeafUnderUnrelatedSection(t *testing.T) {
	path := writeConfig(t, "issue_prefix: xx\n"+
		"sync:\n"+
		"    password: keep-me\n"+
		"dolt:\n"+
		"    password: delete-me\n")

	text, _ := canonicalizeConfig(t, path, ConfigState{IssuePrefix: "gc"})

	if !strings.Contains(text, "keep-me") {
		t.Fatalf("deletion reached a password under an unrelated section:\n%s", text)
	}
	if strings.Contains(text, "delete-me") {
		t.Fatalf("deletion missed the password under the dolt section:\n%s", text)
	}
}

// TestEnsureCanonicalConfigDeletesNestedDoltEndpointShadows covers the same
// duality for the endpoint keys.
//
// gc reads dolt.host/port/user FLAT only (readConfigStateFromRoot), while bd
// resolves either spelling, so a nested copy is invisible to gc and live to
// bd. It is inert only while the flat key is present -- viper resolves a
// duplicate to the flat one -- and goes LIVE the moment a later
// canonicalization with empty state deletes that flat key. Both branches are
// pinned here because the populated one is what makes the empty one reachable.
func TestEnsureCanonicalConfigDeletesNestedDoltEndpointShadows(t *testing.T) {
	nested := "issue_prefix: xx\n" +
		"dolt:\n" +
		"    host: shadow.example.com\n" +
		"    port: 3307\n" +
		"    user: shadow\n"

	t.Run("state_has_no_endpoint", func(t *testing.T) {
		path := writeConfig(t, nested)

		text, _ := canonicalizeConfig(t, path, ConfigState{IssuePrefix: "gc"})

		for _, forbidden := range []string{"shadow.example.com", "3307", "shadow"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("nested endpoint shadow %q survived with empty state:\n%s", forbidden, text)
			}
		}
	})

	t.Run("state_has_endpoint", func(t *testing.T) {
		path := writeConfig(t, nested)

		text, _ := canonicalizeConfig(t, path, ConfigState{
			IssuePrefix: "gc",
			DoltHost:    "db.example.com",
			DoltPort:    "4406",
			DoltUser:    "gc",
		})

		for _, forbidden := range []string{"shadow.example.com", "3307", "shadow"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("nested endpoint shadow %q survived beside the canonical flat key:\n%s", forbidden, text)
			}
		}
		for _, needle := range []string{"dolt.host: db.example.com", "dolt.user: gc"} {
			if !strings.Contains(text, needle) {
				t.Fatalf("canonical flat endpoint key %q missing:\n%s", needle, text)
			}
		}
	})
}

// TestEnsureCanonicalConfigFallbackDeletesNestedDottedPassword drives the same
// defect through the malformed-YAML line-rewrite path, which had its own
// flat-only assumption in topLevelConfigLine.
//
// The `: not yaml` line is what routes EnsureCanonicalConfig to the fallback;
// without it this test would exercise the YAML path and pass on the strength
// of the other fix.
func TestEnsureCanonicalConfigFallbackDeletesNestedDottedPassword(t *testing.T) {
	path := writeConfig(t, doltNestedPasswordConfig+": not yaml\n")

	text, changed := canonicalizeConfig(t, path, ConfigState{IssuePrefix: "gc"})

	if !changed {
		t.Fatal("fallback reported no change while a nested dolt.password was present")
	}
	if strings.Contains(text, "hunter2") || strings.Contains(text, "password") {
		t.Fatalf("nested dolt.password survived the malformed-YAML fallback:\n%s", text)
	}
	if !strings.Contains(text, ": not yaml") {
		t.Fatalf("fallback dropped the malformed line it is meant to preserve:\n%s", text)
	}
	// The emptied `dolt:` header is intentionally left for
	// ensureFallbackNestedDoltDisableEventFlush to repopulate; a bare `dolt:`
	// would bind the section to null.
	if !strings.Contains(text, "disable-event-flush: true") {
		t.Fatalf("fallback left the dolt section without its canonical member:\n%s", text)
	}
}

// TestEnsureCanonicalConfigFallbackDeletesNestedDottedPasswordIsIdempotent
// guards the fallback against the churn the YAML path is guarded against
// above. The fallback deletes and re-adds the nested disable-event-flush line
// on every call if the nested deletion set is widened to include it, so this
// converges only while that exclusion holds.
func TestEnsureCanonicalConfigFallbackDeletesNestedDottedPasswordIsIdempotent(t *testing.T) {
	path := writeConfig(t, doltNestedPasswordConfig+": not yaml\n")
	state := ConfigState{IssuePrefix: "gc"}

	first, changed := canonicalizeConfig(t, path, state)
	if !changed {
		t.Fatal("first fallback pass reported no change")
	}
	second, changed := canonicalizeConfig(t, path, state)
	if changed {
		t.Fatalf("second fallback pass reported a change; config is not converged:\n%s", second)
	}
	if first != second {
		t.Fatalf("second fallback pass rewrote the file:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// TestEnsureCanonicalConfigFallbackKeepsNestedLeafUnderUnrelatedSection is the
// fallback's copy of the section-scoping guard, and it carries the same
// keep-and-delete pair for the same reason. The line-based path is the easier
// of the two to widen by accident, because a leaf-name-only match is one string
// comparison.
//
// `host` is in the keep set as well as `password`: the endpoint keys are
// deleted from the dolt section unconditionally, which makes a leaf-name-only
// match there strictly more tempting than for the deprecated keys.
func TestEnsureCanonicalConfigFallbackKeepsNestedLeafUnderUnrelatedSection(t *testing.T) {
	path := writeConfig(t, "issue_prefix: xx\n"+
		"sync:\n"+
		"    password: keep-me\n"+
		"    host: keep-host\n"+
		"dolt:\n"+
		"    password: delete-me\n"+
		"    host: delete-host\n"+
		": not yaml\n")

	text, _ := canonicalizeConfig(t, path, ConfigState{IssuePrefix: "gc"})

	for _, needle := range []string{"keep-me", "keep-host"} {
		if !strings.Contains(text, needle) {
			t.Fatalf("fallback deletion reached %q under an unrelated section:\n%s", needle, text)
		}
	}
	for _, forbidden := range []string{"delete-me", "delete-host"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("fallback deletion missed %q under the dolt section:\n%s", forbidden, text)
		}
	}
}

// TestEnsureCanonicalConfigFallbackDeletesNestedPasswordUnderRepeatedDoltHeader
// pins the ordering inside the fallback rather than any single deletion.
//
// The rewrite loop keeps only the LAST of a repeated top-level key, so the
// first `dolt:` header here is dropped and its children are orphaned. A
// section-scoped deletion running after that loop cannot see which section the
// orphans belonged to and silently leaves the secret; running before it can.
// Two `dolt:` headers is exotic, but the fallback exists for exactly the files
// nothing else will parse.
func TestEnsureCanonicalConfigFallbackDeletesNestedPasswordUnderRepeatedDoltHeader(t *testing.T) {
	path := writeConfig(t, "issue_prefix: xx\n"+
		"dolt:\n"+
		"    password: hunter2\n"+
		"dolt:\n"+
		"    disable-event-flush: true\n"+
		": not yaml\n")

	text, _ := canonicalizeConfig(t, path, ConfigState{IssuePrefix: "gc"})

	if strings.Contains(text, "hunter2") || strings.Contains(text, "password") {
		t.Fatalf("nested dolt.password survived under a repeated section header:\n%s", text)
	}
}

// TestDeleteNestedDottedKeysPrunesEmptiedSection pins the helper directly,
// because EnsureCanonicalConfig cannot reach this state: setNestedBool puts
// disable-event-flush in the dolt section before the deletion runs, so the
// section is never emptied through that caller. A future caller with a
// different section would otherwise leave `<section>:` bound to null.
func TestDeleteNestedDottedKeysPrunesEmptiedSection(t *testing.T) {
	root := configRoot(t, "issue_prefix: xx\nsync:\n    remote: gone\n")

	if !deleteNestedDottedKeys(root, "sync.remote") {
		t.Fatal("deleteNestedDottedKeys() reported no change for a present nested key")
	}
	if findValue(root, "sync") != nil {
		t.Fatal("deleteNestedDottedKeys() left the emptied section behind")
	}
	if findValue(root, "issue_prefix") == nil {
		t.Fatal("deleteNestedDottedKeys() dropped an unrelated top-level key")
	}
	if deleteNestedDottedKeys(root, "sync.remote") {
		t.Fatal("deleteNestedDottedKeys() reported a change for an absent key")
	}
}

// TestDeleteNestedDottedKeysIgnoresUndottedAndScalarSections pins the two
// inputs that must be no-ops rather than errors: a key with no dot has no
// nested spelling at all (dolt_port, dolt_server_port), and a section holding
// a scalar is not a mapping to delete a leaf from.
func TestDeleteNestedDottedKeysIgnoresUndottedAndScalarSections(t *testing.T) {
	root := configRoot(t, "dolt_port: 4406\ndolt: scalar-not-a-section\n")

	if deleteNestedDottedKeys(root, "dolt_port", "dolt.password") {
		t.Fatal("deleteNestedDottedKeys() reported a change for an undotted key or a scalar section")
	}
	if findValue(root, "dolt_port") == nil || findValue(root, "dolt") == nil {
		t.Fatal("deleteNestedDottedKeys() removed a key it should not resolve")
	}
}
