package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Pins that a scope whose .beads/redirect names another store never derives a
// DataDir of its own.
//
// worktree-setup.sh writes .beads/redirect pointing at the rig's real .beads
// on every worktree it provisions, and that file is the recorded intent: this
// tree shares the parent's store. No Go path read it -- resolveManagedDolt-
// RuntimeLayout built DataDir from cityPath unconditionally -- so a worktree
// that reached this function got its own empty database, measured on
// city/toolsmith-codex-1 as a 32K .beads/dolt holding only .dolt/ and
// .doltcfg/ while .beads/redirect named the populated store.
//
// This is the narrower half of the two fixes for ci-spgmnv, and it holds even
// where scope resolution is legitimately a worktree: dolt_scope_watchdog.go's
// one-server-per-scope contract counts worktrees as real scopes, so the answer
// for those cannot be "refuse the scope", only "do not give it a second
// store".
func TestManagedDoltLayoutHonorsBeadsRedirect(t *testing.T) {
	shared := canonicalTestPath(t.TempDir())
	scope := canonicalTestPath(t.TempDir())
	writeBeadsRedirect(t, scope, filepath.Join(shared, ".beads"))

	layout, err := resolveManagedDoltRuntimeLayout(scope)
	if err != nil {
		t.Fatalf("resolveManagedDoltRuntimeLayout(%q): %v", scope, err)
	}
	want := normalizePathForCompare(filepath.Join(shared, ".beads", "dolt"))
	if layout.DataDir != want {
		t.Errorf("DataDir = %q, want the redirect target %q", layout.DataDir, want)
	}
}

// Without a redirect the layout is unchanged. Pinned beside the case above
// because a rewrite that redirected unconditionally, or that silently
// swallowed a read error into an empty path, would pass that test alone.
func TestManagedDoltLayoutWithoutRedirectUsesItsOwnScope(t *testing.T) {
	scope := canonicalTestPath(t.TempDir())

	layout, err := resolveManagedDoltRuntimeLayout(scope)
	if err != nil {
		t.Fatalf("resolveManagedDoltRuntimeLayout(%q): %v", scope, err)
	}
	want := normalizePathForCompare(filepath.Join(scope, ".beads", "dolt"))
	if layout.DataDir != want {
		t.Errorf("DataDir = %q, want the scope's own store %q", layout.DataDir, want)
	}
}

// An explicit GC_DOLT_DATA_DIR outranks the redirect.
//
// The env var is an operator's direct instruction about this one run; the
// redirect is a file left behind at provisioning time. Inverting that lets a
// stale redirect override a deliberate override, and the override is what the
// operator reaches for when the redirect is the thing that is wrong.
func TestManagedDoltLayoutPrefersExplicitDataDirOverRedirect(t *testing.T) {
	shared := canonicalTestPath(t.TempDir())
	scope := canonicalTestPath(t.TempDir())
	explicit := canonicalTestPath(t.TempDir())
	writeBeadsRedirect(t, scope, filepath.Join(shared, ".beads"))
	t.Setenv("GC_DOLT_DATA_DIR", explicit)

	layout, err := resolveManagedDoltRuntimeLayout(scope)
	if err != nil {
		t.Fatalf("resolveManagedDoltRuntimeLayout(%q): %v", scope, err)
	}
	if layout.DataDir != normalizePathForCompare(explicit) {
		t.Errorf("DataDir = %q, want the explicit override %q", layout.DataDir, explicit)
	}
}

// A relative redirect resolves against the scope, and a blank or unreadable
// one leaves the scope's own store in place.
//
// bd writes an absolute path today, so the relative case is defense rather
// than observed behavior -- recorded as such. The blank case is not
// hypothetical: a truncated write leaves a zero-length file, and treating
// that as a target would point DataDir at the filesystem root.
func TestManagedDoltLayoutRedirectEdgeCases(t *testing.T) {
	t.Run("relative target resolves against the scope", func(t *testing.T) {
		scope := canonicalTestPath(t.TempDir())
		writeBeadsRedirect(t, scope, filepath.Join("..", "sibling", ".beads"))
		layout, err := resolveManagedDoltRuntimeLayout(scope)
		if err != nil {
			t.Fatal(err)
		}
		want := normalizePathForCompare(filepath.Join(scope, "..", "sibling", ".beads", "dolt"))
		if layout.DataDir != want {
			t.Errorf("DataDir = %q, want %q", layout.DataDir, want)
		}
	})

	t.Run("blank redirect is ignored", func(t *testing.T) {
		scope := canonicalTestPath(t.TempDir())
		writeBeadsRedirect(t, scope, "   \n")
		layout, err := resolveManagedDoltRuntimeLayout(scope)
		if err != nil {
			t.Fatal(err)
		}
		want := normalizePathForCompare(filepath.Join(scope, ".beads", "dolt"))
		if layout.DataDir != want {
			t.Errorf("DataDir = %q, want the scope's own store %q", layout.DataDir, want)
		}
	})
}

func writeBeadsRedirect(t *testing.T, scope, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(scope, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Trailing newline on purpose: worktree-setup.sh writes it with `echo`, so
	// a reader that does not trim reproduces the field bug rather than a
	// synthetic one.
	if err := os.WriteFile(filepath.Join(scope, ".beads", "redirect"), []byte(target+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
