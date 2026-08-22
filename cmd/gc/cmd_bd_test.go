package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/gastownhall/gascity/internal/pgauth"
)

func TestExtractRigFlag(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantRig  string
		wantArgs []string
	}{
		{
			name:     "no rig flag",
			args:     []string{"list", "--limit", "5"},
			wantRig:  "",
			wantArgs: []string{"list", "--limit", "5"},
		},
		{
			name:     "rig flag with space",
			args:     []string{"--rig", "myproject", "list"},
			wantRig:  "myproject",
			wantArgs: []string{"list"},
		},
		{
			name:     "rig flag with equals",
			args:     []string{"--rig=myproject", "list"},
			wantRig:  "myproject",
			wantArgs: []string{"list"},
		},
		{
			name:     "rig flag in middle",
			args:     []string{"show", "--rig", "myproject", "BL-42"},
			wantRig:  "myproject",
			wantArgs: []string{"show", "BL-42"},
		},
		{
			name:     "empty args",
			args:     nil,
			wantRig:  "",
			wantArgs: nil,
		},
		{
			name:     "rig flag at end missing value",
			args:     []string{"list", "--rig"},
			wantRig:  "",
			wantArgs: []string{"list", "--rig"},
		},
	}

	origRigFlag := rigFlag
	defer func() { rigFlag = origRigFlag }()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rigFlag = ""
			gotRig, gotArgs := extractRigFlag(tt.args)
			if gotRig != tt.wantRig {
				t.Errorf("rig = %q, want %q", gotRig, tt.wantRig)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Fatalf("args len = %d, want %d; got %v", len(gotArgs), len(tt.wantArgs), gotArgs)
			}
			for i := range gotArgs {
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("args[%d] = %q, want %q", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestExtractRigFlagFallsBackToGlobal(t *testing.T) {
	origRigFlag := rigFlag
	defer func() { rigFlag = origRigFlag }()

	rigFlag = "from-global"
	gotRig, gotArgs := extractRigFlag([]string{"list"})
	if gotRig != "from-global" {
		t.Errorf("rig = %q, want %q", gotRig, "from-global")
	}
	if len(gotArgs) != 1 || gotArgs[0] != "list" {
		t.Errorf("args = %v, want [list]", gotArgs)
	}
}

func TestExtractBdScopeFlags(t *testing.T) {
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()

	cityFlag = ""
	rigFlag = ""
	gotCity, gotRig, gotArgs := extractBdScopeFlags([]string{"--city=/tmp/city", "--rig", "repo", "context", "--json"})
	if gotCity != "/tmp/city" {
		t.Fatalf("city = %q, want %q", gotCity, "/tmp/city")
	}
	if gotRig != "repo" {
		t.Fatalf("rig = %q, want %q", gotRig, "repo")
	}
	wantArgs := []string{"context", "--json"}
	if len(gotArgs) != len(wantArgs) {
		t.Fatalf("args len = %d, want %d; got %v", len(gotArgs), len(wantArgs), gotArgs)
	}
	for i := range wantArgs {
		if gotArgs[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %q, want %q", i, gotArgs[i], wantArgs[i])
		}
	}

	cityFlag = "/flag-city"
	rigFlag = "flag-rig"
	gotCity, gotRig, gotArgs = extractBdScopeFlags([]string{"list"})
	if gotCity != "/flag-city" {
		t.Fatalf("fallback city = %q, want %q", gotCity, "/flag-city")
	}
	if gotRig != "flag-rig" {
		t.Fatalf("fallback rig = %q, want %q", gotRig, "flag-rig")
	}
	if len(gotArgs) != 1 || gotArgs[0] != "list" {
		t.Fatalf("fallback args = %v, want [list]", gotArgs)
	}
}

func TestExtractBdDirectoryFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"short flag", []string{"create", "-C", "/tmp/packs", "--json"}, "/tmp/packs"},
		{"long flag space", []string{"create", "--directory", "/tmp/packs"}, "/tmp/packs"},
		{"long flag equals", []string{"create", "--directory=/tmp/packs"}, "/tmp/packs"},
		{"absent", []string{"create", "--json"}, ""},
		{"short flag at end no value", []string{"create", "-C"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractBdDirectoryFlag(tt.args); got != tt.want {
				t.Fatalf("extractBdDirectoryFlag(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestResolveBdScopeTarget(t *testing.T) {
	// Isolate cwd from any ambient `.beads/redirect` in the working tree
	// (e.g. when `make test` runs from a polecat/crew worktree, the worktree's
	// redirect resolves to a path outside the synthetic rig config below and
	// `rigFromRedirectedBeadsDir` rejects it). t.TempDir's ancestry is /tmp,
	// which is guaranteed to be free of city redirects.
	setCwd(t, t.TempDir())

	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(_ string, _ *config.City, _ execStoreTarget, beadID string) bool {
		return beadID == "projectwrenunity-0xk" || beadID == "projectwrenunity-abc"
	}
	cityDir := filepath.Join(t.TempDir(), "city")
	cfgForTest := func() *config.City {
		return &config.City{
			Workspace: config.Workspace{Name: "gascity"},
			Rigs: []config.Rig{
				{Name: "wren", Path: filepath.Join("rigs", "wren"), Prefix: "projectwrenunity"},
				{Name: "gascity", Path: filepath.Join("rigs", "gascity")},
			},
		}
	}

	tests := []struct {
		name         string
		rigName      string
		args         []string
		cityExplicit bool
		want         execStoreTarget
		wantError    string
	}{
		{
			name:    "explicit rig name",
			rigName: "wren",
			args:    []string{"list"},
			want: execStoreTarget{
				ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
				ScopeKind: "rig",
				Prefix:    "projectwrenunity",
				RigName:   "wren",
			},
		},
		{
			name:    "explicit rig name case insensitive",
			rigName: "Wren",
			args:    []string{"list"},
			want: execStoreTarget{
				ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
				ScopeKind: "rig",
				Prefix:    "projectwrenunity",
				RigName:   "wren",
			},
		},
		{
			name:    "auto-detect from bead prefix",
			rigName: "",
			args:    []string{"show", "projectwrenunity-0xk"},
			want: execStoreTarget{
				ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
				ScopeKind: "rig",
				Prefix:    "projectwrenunity",
				RigName:   "wren",
			},
		},
		{
			name:    "no rig falls back to city",
			rigName: "",
			args:    []string{"list"},
			want: execStoreTarget{
				ScopeRoot: cityDir,
				ScopeKind: "city",
				Prefix:    "ga",
			},
		},
		{
			name:      "unknown explicit rig errors",
			rigName:   "nonexistent",
			args:      []string{"show", "projectwrenunity-abc"},
			wantError: `rig "nonexistent" not found`,
		},
		{
			name:    "skips flags during auto-detect",
			rigName: "",
			args:    []string{"list", "--status", "open"},
			want: execStoreTarget{
				ScopeRoot: cityDir,
				ScopeKind: "city",
				Prefix:    "ga",
			},
		},
		{
			// gastownhall/gascity#3410: an explicit --city must pin the city
			// store and not be silently downgraded to a rig store, even when a
			// rig-prefixed bead id is present in the args.
			name:         "explicit city pins city over bead-prefix",
			rigName:      "",
			args:         []string{"show", "projectwrenunity-0xk"},
			cityExplicit: true,
			want: execStoreTarget{
				ScopeRoot: cityDir,
				ScopeKind: "city",
				Prefix:    "ga",
			},
		},
		{
			name:         "explicit city pins city for list",
			rigName:      "",
			args:         []string{"list", "--status", "open"},
			cityExplicit: true,
			want: execStoreTarget{
				ScopeRoot: cityDir,
				ScopeKind: "city",
				Prefix:    "ga",
			},
		},
		{
			// An explicit --rig still wins over an explicit --city.
			name:         "explicit rig wins over explicit city",
			rigName:      "wren",
			args:         []string{"list"},
			cityExplicit: true,
			want: execStoreTarget{
				ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
				ScopeKind: "rig",
				Prefix:    "projectwrenunity",
				RigName:   "wren",
			},
		},
		{
			name:    "-C routes to matching rig",
			rigName: "",
			args:    []string{"create", "-C", filepath.Join(cityDir, "rigs", "wren"), "--json"},
			want: execStoreTarget{
				ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
				ScopeKind: "rig",
				Prefix:    "projectwrenunity",
				RigName:   "wren",
			},
		},
		{
			name:    "--directory routes to matching rig",
			rigName: "",
			args:    []string{"create", "--directory", filepath.Join(cityDir, "rigs", "wren"), "--json"},
			want: execStoreTarget{
				ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
				ScopeKind: "rig",
				Prefix:    "projectwrenunity",
				RigName:   "wren",
			},
		},
		{
			name:    "-C outside known rigs falls back to city",
			rigName: "",
			args:    []string{"create", "-C", "/tmp/unknown-dir", "--json"},
			want: execStoreTarget{
				ScopeRoot: cityDir,
				ScopeKind: "city",
				Prefix:    "ga",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveBdScopeTarget(cfgForTest(), cityDir, tt.rigName, tt.args, tt.cityExplicit, io.Discard)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("resolveBdScopeTarget() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBdScopeTarget() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestResolveBdScopeTargetUsesRedirectedWorktreeRig(t *testing.T) {
	cityDir := t.TempDir()
	worktreeDir := filepath.Join(cityDir, ".gc", "worktrees", "frontend", "polecats", "polecat-1")
	rigDir := filepath.Join(cityDir, "rigs", "frontend")
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .beads): %v", err)
	}
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(rigDir): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "redirect"), []byte(filepath.Join(rigDir, ".beads")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(redirect): %v", err)
	}
	setCwd(t, worktreeDir)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "gascity"},
		Rigs:      []config.Rig{{Name: "frontend", Path: filepath.Join("rigs", "frontend"), Prefix: "fr"}},
	}
	got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"list"}, false, io.Discard)
	if err != nil {
		t.Fatalf("resolveBdScopeTarget() error = %v", err)
	}
	want := execStoreTarget{
		ScopeRoot: rigDir,
		ScopeKind: "rig",
		Prefix:    "fr",
		RigName:   "frontend",
	}
	if got != want {
		t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
	}
}

// TestResolveBdScopeTargetUsesGCRIGEnv covers the bug where GC_RIG env (set by
// the controller on every rig agent) was silently ignored, causing gc bd list to
// hit the city HQ database and return empty results instead of rig-scoped results.
// See gastownhall/gascity#gcy-6ul.
func TestResolveBdScopeTargetUsesGCRIGEnv(t *testing.T) {
	setCwd(t, t.TempDir())
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(_ string, _ *config.City, _ execStoreTarget, _ string) bool { return false }

	cityDir := filepath.Join(t.TempDir(), "city")
	cfg := &config.City{
		Workspace: config.Workspace{Name: "gascity"},
		Rigs: []config.Rig{
			{Name: "chatehr", Path: filepath.Join("rigs", "chatehr"), Prefix: "ch"},
			{Name: "wren", Path: filepath.Join("rigs", "wren"), Prefix: "projectwrenunity"},
		},
	}

	t.Run("GC_RIG env routes to rig when no flag and no bead-id args", func(t *testing.T) {
		t.Setenv("GC_RIG", "chatehr")
		var stderr bytes.Buffer
		got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"list", "--assignee=chatehr/gastown.refinery", "--status=open"}, false, &stderr)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{
			ScopeRoot: filepath.Join(cityDir, "rigs", "chatehr"),
			ScopeKind: "rig",
			Prefix:    "ch",
			RigName:   "chatehr",
		}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
		// A GC_RIG that names a bound rig is honored silently — the warning is
		// reserved for the unresolvable case, so routine rig agents stay quiet.
		if warn := stderr.String(); warn != "" {
			t.Fatalf("expected no warning for a valid GC_RIG, got %q", warn)
		}
	})

	t.Run("explicit --rig flag overrides GC_RIG env", func(t *testing.T) {
		t.Setenv("GC_RIG", "chatehr")
		got, err := resolveBdScopeTarget(cfg, cityDir, "wren", []string{"list"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{
			ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
			ScopeKind: "rig",
			Prefix:    "projectwrenunity",
			RigName:   "wren",
		}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
	})

	t.Run("bead-id prefix detection wins over GC_RIG env", func(t *testing.T) {
		t.Setenv("GC_RIG", "chatehr")
		// Restore bdBeadExists to return true for a wren bead
		origProbe2 := bdBeadExists
		defer func() { bdBeadExists = origProbe2 }()
		bdBeadExists = func(_ string, _ *config.City, target execStoreTarget, beadID string) bool {
			return beadID == "projectwrenunity-0xk" && target.RigName == "wren"
		}
		got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"show", "projectwrenunity-0xk"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{
			ScopeRoot: filepath.Join(cityDir, "rigs", "wren"),
			ScopeKind: "rig",
			Prefix:    "projectwrenunity",
			RigName:   "wren",
		}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
	})

	t.Run("unknown GC_RIG env falls through to city root and warns", func(t *testing.T) {
		t.Setenv("GC_RIG", "nonexistent-rig")
		var stderr bytes.Buffer
		got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"list"}, false, &stderr)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		// Must land on city root, not the (unknown) GC_RIG rig — the
		// deliberate cross-city fallthrough is preserved, NOT turned into an
		// error like --rig.
		if got.ScopeKind != "city" {
			t.Fatalf("resolveBdScopeTarget() ScopeKind = %q, want %q", got.ScopeKind, "city")
		}
		if got.ScopeRoot != cityDir {
			t.Fatalf("resolveBdScopeTarget() ScopeRoot = %q, want %q", got.ScopeRoot, cityDir)
		}
		// The discard must not be silent: warn on stderr, naming both the
		// offending value and the store actually answered. Without this a
		// stale/typo'd GC_RIG silently redirects the query while the identical
		// value via --rig exits 1.
		warn := stderr.String()
		if !strings.Contains(warn, "GC_RIG") || !strings.Contains(warn, "nonexistent-rig") {
			t.Fatalf("expected a warning naming the discarded GC_RIG value, got %q", warn)
		}
		if !strings.Contains(warn, "city") {
			t.Fatalf("expected the warning to name the store answered (city), got %q", warn)
		}
	})
}

func TestResolveBdScopeTargetErrorsOnForeignRedirect(t *testing.T) {
	cityDir := t.TempDir()
	worktreeDir := filepath.Join(cityDir, ".gc", "worktrees", "frontend", "polecats", "polecat-1")
	foreignDir := filepath.Join(t.TempDir(), "foreign")
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .beads): %v", err)
	}
	if err := os.MkdirAll(filepath.Join(foreignDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(foreign .beads): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "redirect"), []byte(filepath.Join(foreignDir, ".beads")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(redirect): %v", err)
	}
	setCwd(t, worktreeDir)
	cfg := &config.City{
		Workspace: config.Workspace{Name: "gascity"},
		Rigs:      []config.Rig{{Name: "frontend", Path: filepath.Join("rigs", "frontend"), Prefix: "fr"}},
	}
	_, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"list"}, false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "points outside declared city rigs") {
		t.Fatalf("resolveBdScopeTarget() error = %v, want foreign redirect error", err)
	}
}

// redirectedWorktreeCity builds the fixture that reproduces ci-qbkr: a city
// holding two bound rigs, plus an agent work_dir under .gc/worktrees/ whose
// .beads/redirect points at the first rig's store. Returns the city root and
// that rig root, and leaves cwd inside the worktree.
//
// The redirect file is what makes cwd detection disagree with the agent's
// configured scope, so it is the whole point of the fixture -- a worktree
// without one resolves to the city by accident and every assertion below
// passes whether or not the implementation reads GC_BEADS_SCOPE_ROOT.
//
// The worktree path is deliberately not returned. Every caller wants cwd set
// to it, which the helper already does, and handing it back invited a caller
// to plant a second redirect there and split the fixture's one story in two.
func redirectedWorktreeCity(t *testing.T) (cityDir, rigDir string) {
	t.Helper()
	cityDir = t.TempDir()
	rigDir = filepath.Join(cityDir, "rigs", "gascity")
	worktreeDir := filepath.Join(cityDir, ".gc", "worktrees", "gascity", "toolsmith-2")
	for _, dir := range []string{filepath.Join(worktreeDir, ".beads"), rigDir, filepath.Join(cityDir, "rigs", "dart")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "redirect"), []byte(filepath.Join(rigDir, ".beads")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(redirect): %v", err)
	}
	setCwd(t, worktreeDir)
	return cityDir, rigDir
}

// redirectedWorktreeCityConfig declares two bound rigs. The second one exists
// only so the "rig scope root" assertions can name a WRONG answer that the
// resolver could plausibly give -- with a single rig, a test asserting the
// named rig cannot distinguish a resolver that read the scope root from one
// that followed a redirect to the only other candidate.
func redirectedWorktreeCityConfig() *config.City {
	return &config.City{
		Workspace: config.Workspace{Name: "gascity", Prefix: "ci"},
		Rigs: []config.Rig{
			{Name: "gascity", Path: filepath.Join("rigs", "gascity"), Prefix: "gs"},
			{Name: "dart", Path: filepath.Join("rigs", "dart"), Prefix: "da"},
		},
	}
}

// TestResolveBdScopeTargetHonorsAgentScopeRootOverRedirectedCwd pins that the
// scope gc itself stamped on the session (GC_BEADS_SCOPE_ROOT, written by
// template_resolve.go from the agent's config) outranks whatever store cwd
// happens to imply.
//
// ci-qbkr: a city-scoped agent whose work_dir is a rig worktree filed its
// follow-up beads into the RIG store. `gc bd show <city-bead>` resolved the
// city store via bead-prefix detection while `gc bd create` -- carrying no bead
// id -- fell all the way through to cwd, which the worktree's .beads/redirect
// maps to the rig. Reads and writes therefore disagreed, and the write half
// printed a confident "Created issue: gs-5u8". The bead landed assigned to an
// agent in a store no such agent reads, raising zero pool demand.
//
// The subtests assert the resolved target rather than any warning text: the
// defect was a silently wrong STORE, and a test pinning the diagnostic would
// stay green if the store were still wrong.
func TestResolveBdScopeTargetHonorsAgentScopeRootOverRedirectedCwd(t *testing.T) {
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(_ string, _ *config.City, _ execStoreTarget, _ string) bool { return false }

	t.Run("city scope root beats redirected cwd", func(t *testing.T) {
		cityDir, _ := redirectedWorktreeCity(t)
		t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
		var stderr bytes.Buffer
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"create", "follow-up", "--assignee", "toolsmith"}, false, &stderr)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{ScopeRoot: cityDir, ScopeKind: "city", Prefix: "ci"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
		// Honoring the configured scope is the correct answer here, not a
		// salvaged fallback, so it must not warn. A warning on every bd call
		// from an agent whose work_dir is a rig worktree is pure noise.
		if warn := stderr.String(); warn != "" {
			t.Fatalf("expected no warning when the scope root resolves, got %q", warn)
		}
	})

	t.Run("rig scope root beats city cwd", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		setCwd(t, cityDir)
		t.Setenv("GC_BEADS_SCOPE_ROOT", rigDir)
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"list"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
	})

	t.Run("explicit rig flag still wins", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "gascity", []string{"list"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
	})

	t.Run("GC_RIG still wins", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
		t.Setenv("GC_RIG", "gascity")
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"list"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
	})

	t.Run("bead prefix in args still wins", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
		origProbe2 := bdBeadExists
		defer func() { bdBeadExists = origProbe2 }()
		bdBeadExists = func(_ string, _ *config.City, target execStoreTarget, beadID string) bool {
			return beadID == "gs-5u8" && target.RigName == "gascity"
		}
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"show", "gs-5u8"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
	})

	// A rig scope root wins by path containment, which resolveRigForDir checks
	// before it walks for a .beads/redirect. Without this case the rig-scope-root
	// assertion above passes whether or not the tier can be retargeted by a
	// redirect planted under the root it was handed -- there is only one other
	// rig it could wrongly name, and nothing ever names it.
	t.Run("redirect under a rig scope root does not retarget it", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		otherRig := filepath.Join(cityDir, "rigs", "dart")
		if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o755); err != nil {
			t.Fatalf("MkdirAll(rig .beads): %v", err)
		}
		if err := os.WriteFile(filepath.Join(rigDir, ".beads", "redirect"), []byte(filepath.Join(otherRig, ".beads")+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(rig redirect): %v", err)
		}
		t.Setenv("GC_BEADS_SCOPE_ROOT", rigDir)
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"list"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v (the named rig, not the redirect target)", got, want)
		}
	})

	// Records a deliberate asymmetry with the cwd tier rather than leaving it to
	// be discovered: the cwd tier propagates resolveRigForDir's foreign-redirect
	// error (TestResolveBdScopeTargetErrorsOnForeignRedirect pins that), while
	// this tier downgrades the same condition to the discard path. A stamped
	// scope root is a hint about which store the session owns, not a command, so
	// a broken redirect under it must not take out every bd call in the session
	// -- cwd still gets its turn, and errors there on its own terms.
	t.Run("foreign redirect at the scope root discards instead of erroring", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		strayRoot := filepath.Join(cityDir, "stray")
		foreign := filepath.Join(t.TempDir(), "foreign")
		for _, dir := range []string{filepath.Join(strayRoot, ".beads"), filepath.Join(foreign, ".beads")} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("MkdirAll(%s): %v", dir, err)
			}
		}
		if err := os.WriteFile(filepath.Join(strayRoot, ".beads", "redirect"), []byte(filepath.Join(foreign, ".beads")+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(stray redirect): %v", err)
		}
		t.Setenv("GC_BEADS_SCOPE_ROOT", strayRoot)
		var stderr bytes.Buffer
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"list"}, false, &stderr)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v, want the scope root discarded", err)
		}
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v (cwd)", got, want)
		}
		if warn := stderr.String(); !strings.Contains(warn, "GC_BEADS_SCOPE_ROOT") {
			t.Fatalf("expected the discard to warn, got %q", warn)
		}
	})

	t.Run("scope root naming no known store falls through to cwd and warns", func(t *testing.T) {
		cityDir, rigDir := redirectedWorktreeCity(t)
		stray := filepath.Join(t.TempDir(), "not-a-city")
		t.Setenv("GC_BEADS_SCOPE_ROOT", stray)
		var stderr bytes.Buffer
		got, err := resolveBdScopeTarget(redirectedWorktreeCityConfig(), cityDir, "", []string{"list"}, false, &stderr)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		// Mirrors the GC_RIG discard tier: an unresolvable value does not
		// error, but it must not redirect the query silently either.
		want := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "gs", RigName: "gascity"}
		if got != want {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
		}
		warn := stderr.String()
		if !strings.Contains(warn, "GC_BEADS_SCOPE_ROOT") || !strings.Contains(warn, stray) {
			t.Fatalf("expected a warning naming the discarded scope root, got %q", warn)
		}
		if !strings.Contains(warn, `rig "gascity"`) {
			t.Fatalf("expected the warning to name the store answered, got %q", warn)
		}
	})
}

// TestResolveBdScopeTargetReadAndWriteAgreeUnderAgentScopeRoot pins the
// asymmetry that made ci-qbkr invisible: an agent that verified its own setup
// with a read got a passing answer and filed into the other store anyway.
//
// Both invocations are resolved from one unchanged environment, so the test
// fails if either half moves -- it cannot be satisfied by teaching writes and
// reads to agree on the WRONG store, because the expected target is the
// configured scope root computed by the fixture, not whatever the first call
// returned.
func TestResolveBdScopeTargetReadAndWriteAgreeUnderAgentScopeRoot(t *testing.T) {
	cityDir, _ := redirectedWorktreeCity(t)
	t.Setenv("GC_BEADS_SCOPE_ROOT", cityDir)
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(_ string, _ *config.City, target execStoreTarget, beadID string) bool {
		return beadID == "ci-qbkr" && target.ScopeKind == "city"
	}
	cfg := redirectedWorktreeCityConfig()
	want := execStoreTarget{ScopeRoot: cityDir, ScopeKind: "city", Prefix: "ci"}

	for _, args := range [][]string{
		{"show", "ci-qbkr"},
		{"list", "--json"},
		{"create", "follow-up", "--assignee", "toolsmith"},
	} {
		got, err := resolveBdScopeTarget(cfg, cityDir, "", args, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget(%v) error = %v", args, err)
		}
		if got != want {
			t.Fatalf("resolveBdScopeTarget(%v) = %#v, want %#v", args, got, want)
		}
	}
}

// redirectedWorktreeCityOnDisk extends redirectedWorktreeCity with the on-disk
// city.toml and city .beads that the full doBd path needs. The resolver tests
// above hand resolveBdScopeTarget a config built in memory; doBd loads one from
// the city root and then probes the resolved scope for a bd-backed provider, so
// a fixture without these two files fails at config load rather than at the
// assertion. The declared rigs mirror redirectedWorktreeCityConfig so both
// tiers of test read the same city.
func redirectedWorktreeCityOnDisk(t *testing.T) (cityDir, rigDir string) {
	t.Helper()
	cityDir, rigDir = redirectedWorktreeCity(t)
	for _, dir := range []string{filepath.Join(cityDir, ".beads"), filepath.Join(rigDir, ".beads")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("MkdirAll(%s): %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "gascity"
prefix = "ci"

[[rigs]]
name = "gascity"
path = "rigs/gascity"
prefix = "gs"

[[rigs]]
name = "dart"
path = "rigs/dart"
prefix = "da"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(city.toml): %v", err)
	}
	return cityDir, rigDir
}

// installRecordingBd puts a bd stand-in first on PATH that records the inputs
// bd resolves its store from -- its working directory and BEADS_DIR -- plus the
// forwarded args and gc's own scope annotations, and returns a reader for them.
//
// The stand-in records and exits 0 without opening a store or writing anything
// to stdout. Deliberate: the assertion is about what gc HANDS bd, so a stand-in
// that also answered would add a second thing that could be wrong. It accepts
// any argv rather than refusing unscripted input because there is nothing to
// script -- every bd subcommand reaches bd through the one handoff under test,
// and the args it received are themselves asserted, so a command gc mangled
// fails the check instead of passing silently.
func installRecordingBd(t *testing.T) func() map[string]string {
	t.Helper()
	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "bd-routing.txt")
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
  printf 'GC_RIG=%s\n' "${GC_RIG:-}"
  printf 'GC_STORE_ROOT=%s\n' "${GC_STORE_ROOT:-}"
  printf 'GC_STORE_SCOPE=%s\n' "${GC_STORE_SCOPE:-}"
  printf 'GC_BEADS_PREFIX=%s\n' "${GC_BEADS_PREFIX:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatalf("WriteFile(bd stand-in): %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_PATH", capture)
	return func() map[string]string {
		t.Helper()
		data, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("reading bd routing capture (bd stand-in never ran?): %v", err)
		}
		got := make(map[string]string)
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if key, value, ok := strings.Cut(line, "="); ok {
				got[key] = value
			}
		}
		return got
	}
}

// TestGcBdHandsBdTheAgentScopeStoreNotTheRedirectedWorktree pins the half of
// ci-qbkr its own tests left open. resolveBdScopeTarget naming the right store
// is necessary but not sufficient: two other values decide where the write
// lands, and neither is the resolver's return. doBdScoped sets the subprocess
// working directory from the target, and bdCommandEnv sets BEADS_DIR from it.
// Both carry weight. BEADS_DIR is what pins the store at all -- without it the
// provider derives its data root from GC_CITY_PATH, which is the city for a rig
// scope too (bdRuntimeEnvForRigWithErrorRecoveryContext states this) -- and the
// working directory is what bd's own .beads discovery walks up from, which is
// the exact walk the worktree's .beads/redirect hijacks. Drop or reorder either
// and the write goes back to the rig store with every resolver test green.
//
// ci-h1cu is that gap observed from outside: `gc bd create --assignee human`
// answered "Created issue: gs-3k3" from a city-scoped agent, so the follow-up
// landed in a store no city sweep reads and raised zero pool demand. No
// resolver test could see it, because the resolver was already returning the
// city.
//
// GC_RIG is asserted empty on the city leg for the same reason it is cleared:
// bd is not the only reader. A leaked GC_RIG is a stronger tier than the scope
// root in resolveBdScopeTarget itself, so any gc the subprocess invokes would
// route to the rig.
//
// Both directions run off one fixture so the test cannot be satisfied by
// hard-coding the city. The rig leg inverts the disagreement -- scope root on
// the rig, cwd at the city root -- and the expected store moves with the scope
// root, not with cwd and not with a constant.
func TestGcBdHandsBdTheAgentScopeStoreNotTheRedirectedWorktree(t *testing.T) {
	bdArgs := []string{"create", "follow-up", "--assignee", "toolsmith"}

	for _, tc := range []struct {
		name string
		// scopeRoot and cwd pick which of the fixture's two roots plays the
		// agent's stamped scope and which plays the directory it stands in.
		scopeRoot  func(cityDir, rigDir string) string
		cwd        func(cityDir, rigDir string) string
		wantRoot   func(cityDir, rigDir string) string
		wantScope  string
		wantPrefix string
		wantRig    string
	}{
		{
			name:       "city scope root, cwd redirected to the rig",
			scopeRoot:  func(cityDir, _ string) string { return cityDir },
			cwd:        nil, // the fixture already leaves cwd in the worktree
			wantRoot:   func(cityDir, _ string) string { return cityDir },
			wantScope:  "city",
			wantPrefix: "ci",
			wantRig:    "",
		},
		{
			name:       "rig scope root, cwd at the city root",
			scopeRoot:  func(_, rigDir string) string { return rigDir },
			cwd:        func(cityDir, _ string) string { return cityDir },
			wantRoot:   func(_, rigDir string) string { return rigDir },
			wantScope:  "rig",
			wantPrefix: "gs",
			wantRig:    "gascity",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			disableManagedDoltRecoveryForTest(t)
			resetFlags(t)
			origProbe := bdBeadExists
			defer func() { bdBeadExists = origProbe }()
			// No arg here is a bead id, so the probe must never decide the
			// store. Stubbing it false keeps a real store open out of the test
			// and makes an accidental prefix hit on the title a failure rather
			// than a silent retarget.
			bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }

			cityDir, rigDir := redirectedWorktreeCityOnDisk(t)
			if tc.cwd != nil {
				setCwd(t, tc.cwd(cityDir, rigDir))
			}
			// GC_CITY_PATH is what a gc-managed session actually carries, and
			// the test binary refuses the ambient cwd walk-up that would
			// otherwise find the city (ga-klo4gz).
			t.Setenv("GC_CITY_PATH", cityDir)
			t.Setenv("GC_BEADS_SCOPE_ROOT", tc.scopeRoot(cityDir, rigDir))
			readCapture := installRecordingBd(t)

			var stdout, stderr bytes.Buffer
			if code := doBd(bdArgs, &stdout, &stderr); code != 0 {
				t.Fatalf("doBd(%v) = %d, want 0; stderr=%q", bdArgs, code, stderr.String())
			}

			wantRoot := tc.wantRoot(cityDir, rigDir)
			got := readCapture()
			if !samePath(got["pwd"], wantRoot) {
				t.Errorf("bd cwd = %q, want %q", got["pwd"], wantRoot)
			}
			if want := filepath.Join(wantRoot, ".beads"); !samePath(got["BEADS_DIR"], want) {
				t.Errorf("BEADS_DIR = %q, want %q", got["BEADS_DIR"], want)
			}
			if want := strings.Join(bdArgs, " "); got["args"] != want {
				t.Errorf("bd args = %q, want %q", got["args"], want)
			}
			if !samePath(got["GC_STORE_ROOT"], wantRoot) {
				t.Errorf("GC_STORE_ROOT = %q, want %q", got["GC_STORE_ROOT"], wantRoot)
			}
			if got["GC_STORE_SCOPE"] != tc.wantScope {
				t.Errorf("GC_STORE_SCOPE = %q, want %q", got["GC_STORE_SCOPE"], tc.wantScope)
			}
			if got["GC_BEADS_PREFIX"] != tc.wantPrefix {
				t.Errorf("GC_BEADS_PREFIX = %q, want %q", got["GC_BEADS_PREFIX"], tc.wantPrefix)
			}
			if got["GC_RIG"] != tc.wantRig {
				t.Errorf("GC_RIG = %q, want %q", got["GC_RIG"], tc.wantRig)
			}
		})
	}
}

func TestBdCommandEnvUsesCanonicalRigTarget(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	t.Setenv("GC_DOLT", "skip")
	_ = os.Unsetenv("BEADS_ACTOR")

	cityDir := t.TempDir()
	wantPort := strconv.Itoa(writeReachableManagedDoltState(t, cityDir))
	rigDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "config.yaml"), []byte(`issue_prefix: repo
gc.endpoint_origin: inherited_city
gc.endpoint_status: verified
dolt.auto-start: false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{Rigs: []config.Rig{{Name: "repo", Path: rigDir}}}
	envList, err := bdCommandEnv(cityDir, cfg, execStoreTarget{
		ScopeRoot: rigDir,
		ScopeKind: "rig",
		Prefix:    "repo",
		RigName:   "repo",
	})
	if err != nil {
		t.Fatalf("bdCommandEnv: %v", err)
	}
	env := listToMap(envList)
	if got := env["GC_DOLT_PORT"]; got != wantPort {
		t.Fatalf("GC_DOLT_PORT = %q, want %q", got, wantPort)
	}
	if got := env["BEADS_DOLT_SERVER_PORT"]; got != wantPort {
		t.Fatalf("BEADS_DOLT_SERVER_PORT = %q, want %q", got, wantPort)
	}
	if got := env["BEADS_DIR"]; got != filepath.Join(rigDir, ".beads") {
		t.Fatalf("BEADS_DIR = %q, want %q", got, filepath.Join(rigDir, ".beads"))
	}
	if got := env["GC_RIG"]; got != "repo" {
		t.Fatalf("GC_RIG = %q, want %q", got, "repo")
	}
	if got := env["GC_STORE_ROOT"]; got != rigDir {
		t.Fatalf("GC_STORE_ROOT = %q, want %q", got, rigDir)
	}
	if got := env["GC_STORE_SCOPE"]; got != "rig" {
		t.Fatalf("GC_STORE_SCOPE = %q, want %q", got, "rig")
	}
	if got := env["GC_BEADS_PREFIX"]; got != "repo" {
		t.Fatalf("GC_BEADS_PREFIX = %q, want %q", got, "repo")
	}
	if _, present := env["BEADS_ACTOR"]; present {
		t.Fatalf("BEADS_ACTOR = %q, want absent for direct gc bd env without explicit actor", env["BEADS_ACTOR"])
	}
}

func TestBdCommandEnvSurfacesPostgresProjectionError(t *testing.T) {
	clearAmbientPostgresEnv(t)
	t.Setenv("GC_BEADS", "bd")

	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, ".beads", "config.yaml"), []byte(`issue_prefix: demo
gc.endpoint_origin: managed_city
gc.endpoint_status: verified
dolt.auto-start: false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	rigDir := filepath.Join(cityDir, "rigs", "pg")
	writePGScopeFixture(t, rigDir, "")
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "config.yaml"), []byte(`issue_prefix: pg
gc.endpoint_origin: inherited_city
gc.endpoint_status: verified
dolt.auto-start: false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{Rigs: []config.Rig{{Name: "pg", Path: "rigs/pg", Prefix: "pg"}}}

	_, err := bdCommandEnv(cityDir, cfg, execStoreTarget{
		ScopeRoot: rigDir,
		ScopeKind: "rig",
		Prefix:    "pg",
		RigName:   "pg",
	})
	if err == nil {
		t.Fatal("bdCommandEnv err = nil, want postgres projection error")
	}
	if !errors.Is(err, pgauth.ErrNoPasswordResolvable) {
		t.Errorf("errors.Is(err, ErrNoPasswordResolvable) = false, want true; err=%v", err)
	}
}

func TestBdCommandRunnerForCityDoesNotDefaultBeadsActorWhenUnset(t *testing.T) {
	t.Setenv("GC_BEADS", "bd")
	t.Setenv("GC_DOLT", "skip")
	_ = os.Unsetenv("BEADS_ACTOR")

	origRunner := beadsExecCommandRunnerWithEnv
	t.Cleanup(func() { beadsExecCommandRunnerWithEnv = origRunner })

	var captured map[string]string
	beadsExecCommandRunnerWithEnv = func(env map[string]string) beads.CommandRunner {
		captured = map[string]string{}
		for key, value := range env {
			captured[key] = value
		}
		return func(_ string, _ string, _ ...string) ([]byte, error) {
			return []byte("ok"), nil
		}
	}

	cityPath := t.TempDir()
	runner := bdCommandRunnerForCity(cityPath)
	if _, err := runner(cityPath, "bd", "list", "--json"); err != nil {
		t.Fatalf("bd runner error = %v, want nil", err)
	}

	if _, present := captured["BEADS_ACTOR"]; present {
		t.Fatalf("BEADS_ACTOR = %q, want absent for normal bd runner without explicit actor", captured["BEADS_ACTOR"])
	}
}

func TestGcBdUsesProjectionNotAmbientEnv(t *testing.T) {
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	origProbe := bdBeadExists
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
		bdBeadExists = origProbe
	}()
	bdBeadExists = func(_ string, _ *config.City, _ execStoreTarget, beadID string) bool {
		return beadID == "repo-abc"
	}
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	wantPort := strconv.Itoa(writeReachableManagedDoltState(t, cityDir))
	rigDir := filepath.Join(cityDir, "repo")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[[rigs]]
name = "repo"
path = "repo"
prefix = "repo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "config.yaml"), []byte(`issue_prefix: repo
gc.endpoint_origin: inherited_city
gc.endpoint_status: verified
dolt.auto-start: false
`), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-env.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'GC_STORE_ROOT=%s\n' "${GC_STORE_ROOT:-}"
  printf 'GC_STORE_SCOPE=%s\n' "${GC_STORE_SCOPE:-}"
  printf 'GC_BEADS_PREFIX=%s\n' "${GC_BEADS_PREFIX:-}"
  printf 'GC_DOLT_HOST=%s\n' "${GC_DOLT_HOST:-}"
  printf 'GC_DOLT_PORT=%s\n' "${GC_DOLT_PORT:-}"
  printf 'BEADS_DOLT_SERVER_HOST=%s\n' "${BEADS_DOLT_SERVER_HOST:-}"
  printf 'BEADS_DOLT_SERVER_PORT=%s\n' "${BEADS_DOLT_SERVER_PORT:-}"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
  printf 'GC_RIG=%s\n' "${GC_RIG:-}"
  printf 'GC_RIG_ROOT=%s\n' "${GC_RIG_ROOT:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_DOLT_HOST", "")
	_ = os.Unsetenv("GC_DOLT_HOST")
	t.Setenv("GC_DOLT_PORT", "9999")
	t.Setenv("BEADS_DOLT_SERVER_HOST", "ambient-beads.example.com")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "9999")
	t.Setenv("BEADS_DIR", "/ambient/.beads")
	t.Setenv("GC_STORE_ROOT", "/ambient/store")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"show", "repo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stderr=%q", got, stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			got[key] = value
		}
	}
	if !samePath(got["pwd"], rigDir) {
		t.Fatalf("pwd = %q, want %q", got["pwd"], rigDir)
	}
	if got["args"] != "show repo-abc" {
		t.Fatalf("args = %q, want %q", got["args"], "show repo-abc")
	}
	if !samePath(got["GC_STORE_ROOT"], rigDir) {
		t.Fatalf("GC_STORE_ROOT = %q, want %q", got["GC_STORE_ROOT"], rigDir)
	}
	if got["GC_STORE_SCOPE"] != "rig" {
		t.Fatalf("GC_STORE_SCOPE = %q, want %q", got["GC_STORE_SCOPE"], "rig")
	}
	if got["GC_BEADS_PREFIX"] != "repo" {
		t.Fatalf("GC_BEADS_PREFIX = %q, want %q", got["GC_BEADS_PREFIX"], "repo")
	}
	if got["GC_DOLT_HOST"] != "" {
		t.Fatalf("GC_DOLT_HOST = %q, want empty for managed target", got["GC_DOLT_HOST"])
	}
	if got["GC_DOLT_PORT"] != wantPort {
		t.Fatalf("GC_DOLT_PORT = %q, want %q", got["GC_DOLT_PORT"], wantPort)
	}
	if got["BEADS_DOLT_SERVER_HOST"] != "" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want empty for managed target", got["BEADS_DOLT_SERVER_HOST"])
	}
	if got["BEADS_DOLT_SERVER_PORT"] != wantPort {
		t.Fatalf("BEADS_DOLT_SERVER_PORT = %q, want %q", got["BEADS_DOLT_SERVER_PORT"], wantPort)
	}
	if !samePath(got["BEADS_DIR"], filepath.Join(rigDir, ".beads")) {
		t.Fatalf("BEADS_DIR = %q, want %q", got["BEADS_DIR"], filepath.Join(rigDir, ".beads"))
	}
	if got["GC_RIG"] != "repo" {
		t.Fatalf("GC_RIG = %q, want %q", got["GC_RIG"], "repo")
	}
	if !samePath(got["GC_RIG_ROOT"], rigDir) {
		t.Fatalf("GC_RIG_ROOT = %q, want %q", got["GC_RIG_ROOT"], rigDir)
	}
}

func TestGcBdSuppressesBdAutoExportInChildEnv(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	// See TestResolveBdScopeTarget for rationale: isolate cwd so any
	// `.beads/redirect` in the ambient working tree doesn't surface here.
	setCwd(t, cityDir)
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeBuiltinImportsFixture(t, cityDir, "core", "bd")

	binDir := t.TempDir()
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
if [ "${BD_EXPORT_AUTO:-}" != "false" ]; then
  echo "BD_EXPORT_AUTO=${BD_EXPORT_AUTO:-}" >&2
  exit 73
fi
case "${1:-}" in
  show)
    printf '[{"id":"gc-1","title":"ok"}]\n'
    ;;
  update)
    printf '{"id":"gc-1","status":"in_progress"}\n'
    ;;
  *)
    exit 2
    ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("BD_EXPORT_AUTO", "true")

	for _, args := range [][]string{
		{"show", "gc-1", "--json"},
		{"update", "gc-1", "--claim", "--json"},
	} {
		var stdout, stderr bytes.Buffer
		if got := doBd(args, &stdout, &stderr); got != 0 {
			t.Fatalf("doBd(%v) = %d, want 0; stdout=%q stderr=%q", args, got, stdout.String(), stderr.String())
		}
		if strings.TrimSpace(stdout.String()) == "" {
			t.Fatalf("doBd(%v) produced empty stdout", args)
		}
		if stderr.String() != "" {
			t.Fatalf("doBd(%v) stderr = %q, want empty", args, stderr.String())
		}
	}
}

func TestGcBdReapsStaleBdExportJSONLBeforeDirectCommand(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	beadsDir := filepath.Join(cityDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("issue_prefix: gc\ngc.endpoint_origin: managed_city\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(beadsDir, "issues.jsonl")
	if err := os.WriteFile(jsonlPath, []byte(`{"_type":"issue","id":"gc-1"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
printf '[{"id":"gc-1","title":"ok"}]\n'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"show", "gc-1", "--json"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(jsonlPath); !os.IsNotExist(err) {
		t.Fatalf("issues.jsonl present after direct gc bd command; stat err = %v, want IsNotExist", err)
	}
}

func TestGcBdDoesNotAutoRouteHyphenatedFlagValue(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	origProbe := bdBeadExists
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
		bdBeadExists = origProbe
	}()
	cityFlag = ""
	rigFlag = ""
	bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "repo")
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[[rigs]]
name = "repo"
path = "repo"
prefix = "repo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// See TestResolveBdScopeTarget for rationale: isolate cwd so any
	// `.beads/redirect` in the ambient working tree doesn't surface here.
	setCwd(t, cityDir)

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-city-env.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'GC_STORE_ROOT=%s\n' "${GC_STORE_ROOT:-}"
  printf 'GC_STORE_SCOPE=%s\n' "${GC_STORE_SCOPE:-}"
  printf 'GC_BEADS_PREFIX=%s\n' "${GC_BEADS_PREFIX:-}"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"list", "--label", "repo-open"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stderr=%q", got, stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			got[key] = value
		}
	}
	if !samePath(got["pwd"], cityDir) {
		t.Fatalf("pwd = %q, want %q", got["pwd"], cityDir)
	}
	if got["args"] != "list --label repo-open" {
		t.Fatalf("args = %q, want %q", got["args"], "list --label repo-open")
	}
	if !samePath(got["GC_STORE_ROOT"], cityDir) {
		t.Fatalf("GC_STORE_ROOT = %q, want %q", got["GC_STORE_ROOT"], cityDir)
	}
	if got["GC_STORE_SCOPE"] != "city" {
		t.Fatalf("GC_STORE_SCOPE = %q, want %q", got["GC_STORE_SCOPE"], "city")
	}
	if got["GC_BEADS_PREFIX"] != "de" {
		t.Fatalf("GC_BEADS_PREFIX = %q, want %q", got["GC_BEADS_PREFIX"], "de")
	}
	if !samePath(got["BEADS_DIR"], filepath.Join(cityDir, ".beads")) {
		t.Fatalf("BEADS_DIR = %q, want %q", got["BEADS_DIR"], filepath.Join(cityDir, ".beads"))
	}
}

func TestGcBdRejectsGCBeadsFileOverride(t *testing.T) {
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	// Scrub inherited beads env (notably GC_BEADS_SCOPE_ROOT from a
	// gc agent's outer city) so the explicit GC_BEADS override below
	// is honored by configuredBeadsProviderValue. Without this, a leaked
	// GC_BEADS_SCOPE_ROOT disqualifies the override and the provider
	// resolution falls back to city.toml peek (which has no [beads]
	// section here) → defaults to "bd" → rejection never fires.
	clearInheritedBeadsEnv(t)
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// See TestResolveBdScopeTarget for rationale: isolate cwd so any
	// `.beads/redirect` in the ambient working tree doesn't surface here.
	setCwd(t, cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_BEADS", "file")
	// Clear any inherited scope pin so the GC_BEADS override applies to
	// this test's city. When run from a polecat session, the ambient
	// GC_BEADS_SCOPE_ROOT points at the rig repo and would suppress the
	// override before the provider check could fire.
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"list"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd() = %d, want non-zero", got)
	}
	if !strings.Contains(stderr.String(), "only supported for bd-backed beads providers") {
		t.Fatalf("stderr = %q, want provider error", stderr.String())
	}
}

func TestGcBdRejectsNonBdProvider(t *testing.T) {
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "file"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	// See TestResolveBdScopeTarget for rationale: isolate cwd so any
	// `.beads/redirect` in the ambient working tree doesn't surface here.
	setCwd(t, cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"list"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd() = %d, want non-zero", got)
	}
	if !strings.Contains(stderr.String(), "only supported for bd-backed beads providers") {
		t.Fatalf("stderr = %q, want provider error", stderr.String())
	}
}

// TestGcBdRejectsStaleFileMarkerWithDiagnosticHint asserts the error when
// a scope has a stale .gc/beads.json (file-store marker) but no
// .beads/metadata.json (bd-store marker): gc rejects with a hint that
// names the offending marker and suggests the fix. Regression for the
// post-#899 behavior change where stale migration artifacts silently
// reclassified rigs as file-backed with no diagnostic.
func TestGcBdRejectsStaleFileMarkerWithDiagnosticHint(t *testing.T) {
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "legacy-rig")
	if err := os.MkdirAll(filepath.Join(rigDir, ".gc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "bd"

[[rigs]]
name = "legacy-rig"
path = "legacy-rig"
prefix = "lg"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".gc", "beads.json"), []byte(`{"seq":1,"beads":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"--rig", "legacy-rig", "list"}, &stdout, &stderr); got == 0 {
		t.Fatalf("doBd() = %d, want non-zero", got)
	}
	out := stderr.String()
	if !strings.Contains(out, `resolved "file"`) {
		t.Fatalf("stderr = %q, want named provider in error", out)
	}
	if !strings.Contains(out, ".gc/beads.json") {
		t.Fatalf("stderr = %q, want named marker in hint", out)
	}
	if !strings.Contains(out, ".beads/metadata.json") {
		t.Fatalf("stderr = %q, want named fix in hint", out)
	}
}

func TestGcBdAllowsRigPassthroughForBdBackedRigUnderFileCity(t *testing.T) {
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "frontend")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "file"

[[rigs]]
name = "frontend"
path = "frontend"
prefix = "fe"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "metadata.json"), []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"embedded","dolt_database":"fe"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-mixed-provider.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_CITY_PATH", cityDir)

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"--rig", "frontend", "list"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "only supported for bd-backed beads providers") {
		t.Fatalf("stderr = %q, want rig passthrough instead of provider gate", stderr.String())
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			got[key] = value
		}
	}
	if !samePath(got["pwd"], rigDir) {
		t.Fatalf("pwd = %q, want %q", got["pwd"], rigDir)
	}
	if got["args"] != "list" {
		t.Fatalf("args = %q, want %q", got["args"], "list")
	}
	if !samePath(got["BEADS_DIR"], filepath.Join(rigDir, ".beads")) {
		t.Fatalf("BEADS_DIR = %q, want %q", got["BEADS_DIR"], filepath.Join(rigDir, ".beads"))
	}
}

func runRawBDFromDir(t *testing.T, bdPath, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(bdPath, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("raw bd %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func parseCreatedBeadID(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, "{")
	if idx < 0 {
		t.Fatalf("create output missing JSON: %s", out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out[idx:]), &created); err != nil {
		t.Fatalf("parse create JSON: %v\n%s", err, out)
	}
	if created.ID == "" {
		t.Fatalf("create output missing id: %s", out)
	}
	return created.ID
}

func TestBdRigWorktreeStoreConsistentAcrossRawBdGcBdAndProviderStore(t *testing.T) {
	clearInheritedBeadsEnv(t)
	resetFlags(t)
	setEnv := func(values map[string]string) {
		for key, value := range values {
			t.Setenv(key, value)
		}
	}

	bdPath := waitTestRealBDPath(t)
	doltPath, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt not installed")
	}
	setEnv(map[string]string{
		"PATH": strings.Join([]string{filepath.Dir(bdPath), filepath.Dir(doltPath), os.Getenv("PATH")}, string(os.PathListSeparator)),
	})

	cityPath := t.TempDir()
	rigPath, err := writeManagedBdWaitTestCityScaffold(cityPath)
	if err != nil {
		t.Fatalf("writeManagedBdWaitTestCityScaffold: %v", err)
	}
	const projectID = "gc-rig-worktree-consistency-test"
	setupQueries := append(seedDatabaseProjectIDQueries(projectID),
		"CALL DOLT_ADD('.')",
		"CALL DOLT_COMMIT('-m', 'test: seed rig worktree identity', '--author', 'gascity-test <test@gascity.local>')")
	feRepoDir := filepath.Join(t.TempDir(), "fe")
	_, port, _, cleanupDolt := startPasswordedDoltServer(t, feRepoDir, setupQueries...)
	defer cleanupDolt()
	// Cover the fe server's own repo root (the actual live-process dir, not
	// just cityPath) and the relocated dolt identity HOME (ga-7dgcg6).
	requireNoLeakedDoltAfterForPaths(t, cityPath, feRepoDir, os.Getenv("HOME"))

	for _, scope := range []struct {
		name     string
		root     string
		prefix   string
		database string
		origin   contract.EndpointOrigin
	}{
		// Keep the city database intentionally unprovisioned. A worktree-routing
		// regression must fail against hq instead of reaching the rig's fe store.
		{name: "city", root: cityPath, prefix: "gc", database: "hq", origin: contract.EndpointOriginCityCanonical},
		{name: "rig", root: rigPath, prefix: "fe", database: "fe", origin: contract.EndpointOriginExplicit},
	} {
		if err := os.MkdirAll(filepath.Join(scope.root, ".beads"), 0o700); err != nil {
			t.Fatalf("MkdirAll(%s .beads): %v", scope.name, err)
		}
		if err := os.WriteFile(filepath.Join(scope.root, ".beads", ".env"), []byte("BEADS_DOLT_PASSWORD=secret\n"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s .beads/.env): %v", scope.name, err)
		}
		if err := contract.WriteProjectIdentity(fsys.OSFS{}, scope.root, projectID); err != nil {
			t.Fatalf("WriteProjectIdentity(%s): %v", scope.name, err)
		}
		if _, err := contract.EnsureCanonicalMetadata(fsys.OSFS{}, filepath.Join(scope.root, ".beads", "metadata.json"), contract.MetadataState{
			Database:     "dolt",
			Backend:      "dolt",
			DoltMode:     "server",
			DoltDatabase: scope.database,
		}); err != nil {
			t.Fatalf("EnsureCanonicalMetadata(%s): %v", scope.name, err)
		}
		if err := ensureCanonicalScopeConfigState(fsys.OSFS{}, scope.root, contract.ConfigState{
			IssuePrefix:    scope.prefix,
			EndpointOrigin: scope.origin,
			EndpointStatus: contract.EndpointStatusVerified,
			DoltHost:       "127.0.0.1",
			DoltPort:       strconv.Itoa(port),
			DoltUser:       "root",
			DoltMode:       "server",
		}); err != nil {
			t.Fatalf("ensureCanonicalScopeConfigState(%s): %v", scope.name, err)
		}
	}

	setEnv(map[string]string{
		"BEADS_DOLT_PASSWORD": "secret",
		"GC_BEADS":            "bd",
		"GC_CITY":             cityPath,
		"GC_CITY_PATH":        cityPath,
	})
	nativeEnv, err := nativeDoltOpenEnvForScope(cityPath, nil, rigPath)
	if err != nil {
		t.Fatalf("nativeDoltOpenEnvForScope(rig): %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	nativeStorage, err := beads.OpenNativeStorage(ctx, rigPath, nativeEnv)
	if err != nil {
		t.Fatalf("OpenNativeStorage(rig): %v", err)
	}
	if err := nativeStorage.SetConfig(ctx, "issue_prefix", "fe"); err != nil {
		_ = nativeStorage.Close()
		t.Fatalf("SetConfig(issue_prefix): %v", err)
	}
	if err := nativeStorage.Close(); err != nil {
		t.Fatalf("close native fixture storage: %v", err)
	}

	// Database identity is authoritative for this direct fixture. Keep the
	// optional bd-context cross-check strict and fast without replacing the real
	// raw bd and gc bd processes exercised below.
	originalRunner := beadsExecCommandRunnerWithEnv
	beadsExecCommandRunnerWithEnv = func(map[string]string) beads.CommandRunner {
		return func(string, string, ...string) ([]byte, error) {
			return nil, errors.New("bd context unavailable in direct-Dolt fixture")
		}
	}
	t.Cleanup(func() { beadsExecCommandRunnerWithEnv = originalRunner })

	worktreeDir := filepath.Join(cityPath, ".gc", "worktrees", "frontend", "polecats", "polecat-1")
	if err := os.MkdirAll(filepath.Join(worktreeDir, ".beads"), 0o755); err != nil {
		t.Fatalf("MkdirAll(worktree .beads): %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, ".beads", "redirect"), []byte(filepath.Join(rigPath, ".beads")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(redirect): %v", err)
	}

	providerResult, err := openStoreResultAtForCity(rigPath, cityPath)
	if err != nil {
		t.Fatalf("openStoreResultAtForCity(rig): %v", err)
	}
	if got, want := providerResult.Diagnostic.Store, beads.BeadsStoreNameNativeDoltStore; got != want {
		t.Fatalf("provider store = %q, want %q; diagnostic: %+v", got, want, providerResult.Diagnostic)
	}
	providerStore := providerResult.Store
	defer func() {
		if err := closeBeadStoreHandle(providerStore); err != nil {
			t.Errorf("close provider store: %v", err)
		}
	}()

	rawID := parseCreatedBeadID(t, runRawBDFromDir(t, bdPath, worktreeDir, "create", "--json", "raw worktree bead", "-t", "task"))
	if got, err := providerStore.Get(rawID); err != nil {
		t.Fatalf("providerStore.Get(rawID): %v", err)
	} else if got.ID != rawID {
		t.Fatalf("providerStore.Get(rawID).ID = %q, want %q", got.ID, rawID)
	}

	setCwd(t, worktreeDir)
	setEnv(map[string]string{
		"GC_CITY_PATH": "",
	})
	t.Setenv("GC_DOLT_PORT", "9999")
	var stdout, stderr bytes.Buffer
	if code := doBd([]string{"show", rawID}, &stdout, &stderr); code != 0 {
		t.Fatalf("gc bd show rawID from worktree = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), rawID) {
		t.Fatalf("gc bd show output missing raw id %q from worktree:\n%s", rawID, stdout.String())
	}

	providerBead, err := providerStore.Create(beads.Bead{Title: "provider worktree bead", Type: "task"})
	if err != nil {
		t.Fatalf("providerStore.Create: %v", err)
	}
	if rawShow := runRawBDFromDir(t, bdPath, worktreeDir, "show", "--json", providerBead.ID); !strings.Contains(rawShow, providerBead.ID) {
		t.Fatalf("raw bd show missing provider-created bead %q from worktree:\n%s", providerBead.ID, rawShow)
	}

	stdout.Reset()
	stderr.Reset()
	if code := doBd([]string{"create", "--json", "gc worktree bead", "-t", "task"}, &stdout, &stderr); code != 0 {
		t.Fatalf("gc bd create from worktree = %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	gcID := parseCreatedBeadID(t, stdout.String())
	if rawShow := runRawBDFromDir(t, bdPath, worktreeDir, "show", "--json", gcID); !strings.Contains(rawShow, gcID) {
		t.Fatalf("raw bd show missing gc-created bead %q from worktree:\n%s", gcID, rawShow)
	}
}

func TestFreshManagedBdCityInitSeedsPinnedHQDatabaseAndKeepsGCPrefix(t *testing.T) {
	cityPath := setupFreshManagedBdWaitTestCity(t)
	bdPath := waitTestRealBDPath(t)

	cmd := exec.Command("dolt", "sql", "-q", "show tables")
	cmd.Dir = filepath.Join(cityPath, ".beads", "dolt", "hq")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt sql show tables in hq: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "config") {
		t.Fatalf("hq database missing bead schema tables:\n%s", out)
	}

	rawDir := filepath.Join(cityPath, "fresh-nested")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(rawDir): %v", err)
	}
	rawID := parseCreatedBeadID(t, runRawBDFromDir(t, bdPath, rawDir, "create", "--json", "fresh city bead", "-t", "task"))
	if got := beadPrefix(nil, rawID); got != "gc" {
		t.Fatalf("raw city bead prefix = %q, want %q", got, "gc")
	}
	providerStore, err := openStoreAtForCity(cityPath, cityPath)
	if err != nil {
		t.Fatalf("openStoreAtForCity(city): %v", err)
	}
	providerBead, err := providerStore.Create(beads.Bead{Title: "fresh provider city bead", Type: "task"})
	if err != nil {
		t.Fatalf("providerStore.Create: %v", err)
	}
	if got := beadPrefix(nil, providerBead.ID); got != "gc" {
		t.Fatalf("provider city bead prefix = %q, want %q", got, "gc")
	}
}

func listToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func TestResolveBdScopeTargetUsesEnclosingRig(t *testing.T) {
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }

	cityDir := filepath.Join(t.TempDir(), "city")
	rigDir := filepath.Join(cityDir, "frontend")
	if err := os.MkdirAll(filepath.Join(rigDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "demo"},
		Rigs:      []config.Rig{{Name: "frontend", Path: "frontend", Prefix: "fr"}},
	}
	setCwd(t, filepath.Join(rigDir, "nested"))

	got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"context", "--json"}, false, io.Discard)
	if err != nil {
		t.Fatalf("resolveBdScopeTarget() error = %v", err)
	}
	want := execStoreTarget{
		ScopeRoot: rigDir,
		ScopeKind: "rig",
		Prefix:    "fr",
		RigName:   "frontend",
	}
	if got != want {
		t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
	}
}

func TestResolveBdScopeTargetRoutesExistingCityBeadFromRigCwd(t *testing.T) {
	origProbe := bdBeadExists
	defer func() { bdBeadExists = origProbe }()
	bdBeadExists = func(_ string, _ *config.City, target execStoreTarget, beadID string) bool {
		return target.ScopeKind == "city" && beadID == "mc-city1"
	}

	cityDir := filepath.Join(t.TempDir(), "city")
	rigDir := filepath.Join(cityDir, "frontend")
	if err := os.MkdirAll(filepath.Join(rigDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config.City{
		Workspace: config.Workspace{Name: "maintainer-city", Prefix: "mc"},
		Rigs:      []config.Rig{{Name: "frontend", Path: "frontend", Prefix: "fr"}},
	}
	setCwd(t, filepath.Join(rigDir, "nested"))

	got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"show", "mc-city1"}, false, io.Discard)
	if err != nil {
		t.Fatalf("resolveBdScopeTarget() error = %v", err)
	}
	want := execStoreTarget{
		ScopeRoot: cityDir,
		ScopeKind: "city",
		Prefix:    "mc",
	}
	if got != want {
		t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, want)
	}
}

func TestGcBdRespectsRawCityFlag(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	origProbe := bdBeadExists
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
		bdBeadExists = origProbe
	}()
	bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	setCwd(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-city.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'GC_STORE_ROOT=%s\n' "${GC_STORE_ROOT:-}"
  printf 'GC_STORE_SCOPE=%s\n' "${GC_STORE_SCOPE:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_CITY_PATH", "")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"--city", cityDir, "context", "--json"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stderr=%q", got, stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			got[key] = value
		}
	}
	if !samePath(got["pwd"], cityDir) {
		t.Fatalf("pwd = %q, want %q", got["pwd"], cityDir)
	}
	if got["args"] != "context --json" {
		t.Fatalf("args = %q, want %q", got["args"], "context --json")
	}
	if !samePath(got["GC_STORE_ROOT"], cityDir) {
		t.Fatalf("GC_STORE_ROOT = %q, want %q", got["GC_STORE_ROOT"], cityDir)
	}
	if got["GC_STORE_SCOPE"] != "city" {
		t.Fatalf("GC_STORE_SCOPE = %q, want %q", got["GC_STORE_SCOPE"], "city")
	}
}

func TestGcBdUsesEnclosingRigWhenNoFlag(t *testing.T) {
	t.Skip("ga-klo4gz: this test's purpose is exercising resolveContextFromDir's " +
		"ambient cwd walk-up (step 10), which is now unconditionally refused inside " +
		"test binaries; an explicit GC_CITY/GC_CITY_PATH/GC_CITY_ROOT override would " +
		"make it a no-op test rather than a fix")

	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	origProbe := bdBeadExists
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
		bdBeadExists = origProbe
	}()
	bdBeadExists = func(string, *config.City, execStoreTarget, string) bool { return false }
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "frontend")
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[[rigs]]
name = "frontend"
path = "frontend"
prefix = "fr"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	setCwd(t, rigDir)

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-rig.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'pwd=%s\n' "$PWD"
  printf 'args=%s\n' "$*"
  printf 'GC_STORE_ROOT=%s\n' "${GC_STORE_ROOT:-}"
  printf 'GC_STORE_SCOPE=%s\n' "${GC_STORE_SCOPE:-}"
  printf 'GC_RIG=%s\n' "${GC_RIG:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_CITY_PATH", "")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"context", "--json"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stderr=%q", got, stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			got[key] = value
		}
	}
	if !samePath(got["pwd"], rigDir) {
		t.Fatalf("pwd = %q, want %q", got["pwd"], rigDir)
	}
	if got["args"] != "context --json" {
		t.Fatalf("args = %q, want %q", got["args"], "context --json")
	}
	if !samePath(got["GC_STORE_ROOT"], rigDir) {
		t.Fatalf("GC_STORE_ROOT = %q, want %q", got["GC_STORE_ROOT"], rigDir)
	}
	if got["GC_STORE_SCOPE"] != "rig" {
		t.Fatalf("GC_STORE_SCOPE = %q, want %q", got["GC_STORE_SCOPE"], "rig")
	}
	if got["GC_RIG"] != "frontend" {
		t.Fatalf("GC_RIG = %q, want %q", got["GC_RIG"], "frontend")
	}
}

func TestGcBdWarnsOnExternalOverrideDrift(t *testing.T) {
	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "repo")
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[[rigs]]
name = "repo"
path = "repo"
prefix = "repo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "config.yaml"), []byte(`issue_prefix: repo
gc.endpoint_origin: explicit
gc.endpoint_status: unverified
dolt.auto-start: false
dolt.host: 127.0.0.1
dolt.port: 3307
`), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	capture := filepath.Join(t.TempDir(), "gc-bd-external-env.txt")
	script := filepath.Join(binDir, "bd")
	if err := os.WriteFile(script, []byte(`#!/bin/sh
set -eu
{
  printf 'GC_DOLT_HOST=%s\n' "${GC_DOLT_HOST:-}"
  printf 'GC_DOLT_PORT=%s\n' "${GC_DOLT_PORT:-}"
} > "${CAPTURE_PATH}"
`), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("CAPTURE_PATH", capture)
	t.Setenv("GC_BEADS", "bd")
	t.Setenv("GC_BEADS_SCOPE_ROOT", "")
	t.Setenv("GC_DOLT_PORT", "9999")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"--city", cityDir, "--rig", "repo", "show", "repo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd() = %d, want 0; stderr=%q", got, stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	got := listToMap(strings.Split(strings.TrimSpace(string(data)), "\n"))
	if got["GC_DOLT_PORT"] != "3307" {
		t.Fatalf("GC_DOLT_PORT = %q, want canonical 3307", got["GC_DOLT_PORT"])
	}
	if !strings.Contains(stderr.String(), "warning: ignoring ambient Dolt host/port override for external target") {
		t.Fatalf("stderr = %q, want ignored-override warning", stderr.String())
	}
	if !strings.Contains(stderr.String(), "GC_DOLT_PORT=9999 (canonical 3307)") {
		t.Fatalf("stderr = %q, want canonical drift detail", stderr.String())
	}
}

// silentFallbackFakeBdScript builds a fake `bd` shell script that emits the
// silent-fallback marker pair on stderr ("auto-importing ... into empty
// database") and exits 0 — the exact shape bd produces when it loses the
// managed Dolt server and falls back to opening the on-disk store. doBd
// should treat this as a hard failure regardless of bd's exit code.
const silentFallbackFakeBdScript = `#!/bin/sh
echo "auto-importing 220929 bytes from .beads/issues.jsonl into empty database... auto-imported 123 issues" >&2
echo "$@"
exit 0
`

// silentFallbackTestSetup writes a fake bd binary that emits the silent-
// fallback marker, prepends it to PATH, and configures a minimal city as a
// bd-backed scope (via GC_CITY_PATH) so doBd will dispatch through it.
func silentFallbackTestSetup(t *testing.T, fakeBdScript string) {
	t.Helper()

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	t.Cleanup(func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	})
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	port := strconv.Itoa(writeReachableManagedDoltState(t, cityDir))

	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := "issue_prefix: demo\n" +
		"gc.endpoint_origin: city_canonical\n" +
		"gc.endpoint_status: verified\n" +
		"dolt.auto-start: false\n" +
		"dolt.host: 127.0.0.1\n" +
		"dolt.port: " + port + "\n"
	// writeReachableManagedDoltState already creates .beads, but don't rely
	// on that side-effect — make the directory explicit before writing.
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, ".beads", "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(fakeBdScript), 0o755); err != nil {
		t.Fatal(err)
	}
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+origPath)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_DOLT_PORT", port)
}

// TestGcBdSurfacesSilentFallbackAsLoudError_UpdatePath pins the #2080 fix:
// when bd's update path silently falls back to the on-disk store, gc bd must
// convert that into a non-zero exit with an operator-facing message instead
// of letting the silent write loss reach the operator as success.
func TestGcBdSurfacesSilentFallbackAsLoudError_UpdatePath(t *testing.T) {
	silentFallbackTestSetup(t, silentFallbackFakeBdScript)

	var stdout, stderr bytes.Buffer
	got := doBd([]string{"update", "demo-abc", "--set-metadata", "k=v"}, &stdout, &stderr)
	if got != bdSilentFallbackExitCode {
		t.Fatalf("doBd(update) = %d, want %d (silent-fallback exit code); stderr=%q",
			got, bdSilentFallbackExitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "managed Dolt unreachable") {
		t.Fatalf("stderr missing loud-fail message; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "auto-importing") {
		t.Fatalf("original bd stderr not passed through; stderr=%q", stderr.String())
	}
}

// TestGcBdSurfacesSilentFallbackAsLoudError_ClosePath pins the #2079 half of
// the bd-write-persistence quad: bd close goes through the same doBd
// handoff, so the silent-fallback detection must fire identically. Pre-fix,
// gc bd close would have exited 0 even when the close never persisted to
// JSONL (the behavior #2079 documents).
func TestGcBdSurfacesSilentFallbackAsLoudError_ClosePath(t *testing.T) {
	silentFallbackTestSetup(t, silentFallbackFakeBdScript)

	var stdout, stderr bytes.Buffer
	got := doBd([]string{"close", "demo-abc", "-r", "duplicate"}, &stdout, &stderr)
	if got != bdSilentFallbackExitCode {
		t.Fatalf("doBd(close) = %d, want %d (silent-fallback exit code); stderr=%q",
			got, bdSilentFallbackExitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "managed Dolt unreachable") {
		t.Fatalf("stderr missing loud-fail message; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "auto-importing") {
		t.Fatalf("original bd stderr not passed through; stderr=%q", stderr.String())
	}
}

func TestGcBdSurfacesSilentFallbackAsLoudError_ReleaseIfCurrentPath(t *testing.T) {
	silentFallbackTestSetup(t, silentFallbackFakeBdScript)

	var stdout, stderr bytes.Buffer
	got := doBd([]string{"release-if-current", "demo-abc", "worker-1"}, &stdout, &stderr)
	if got != bdSilentFallbackExitCode {
		t.Fatalf("doBd(release-if-current) = %d, want %d (silent-fallback exit code); stderr=%q stdout=%q",
			got, bdSilentFallbackExitCode, stderr.String(), stdout.String())
	}
	if !strings.Contains(stderr.String(), "managed Dolt unreachable") {
		t.Fatalf("stderr missing loud-fail message; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "auto-importing") {
		t.Fatalf("original bd stderr not passed through; stderr=%q", stderr.String())
	}
	if strings.Contains(stdout.String(), "released") {
		t.Fatalf("release-if-current reported success despite silent fallback; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

// TestGcBdHappyPathExitsZeroWithoutFallbackMarker is the inverse: a clean
// bd run that produces no auto-import marker must NOT be converted into the
// loud-fail. This guards against false positives where bd's stderr happens
// to contain unrelated content.
func TestGcBdHappyPathExitsZeroWithoutFallbackMarker(t *testing.T) {
	// Fake bd that exits 0 with normal output and an unrelated stderr line.
	const happyPathFakeBdScript = `#!/bin/sh
echo "some normal bd output"
echo "some unrelated stderr line" >&2
exit 0
`
	silentFallbackTestSetup(t, happyPathFakeBdScript)

	var stdout, stderr bytes.Buffer
	got := doBd([]string{"list"}, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("doBd(list) = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.Contains(stderr.String(), "managed Dolt unreachable") {
		t.Fatalf("loud-fail message fired on a happy-path run; stderr=%q", stderr.String())
	}
}

// TestGcBdProcessExitCodeMatchesSilentFallbackContract pins the process-
// level exit code contract that the bdSilentFallbackExitCode = 4 doc
// comment promises operators and CI. PR #2327 review found the previous
// RunE used `return errExit` on any non-zero, which collapsed every code
// to 1 in commandExitCode — defeating the operator/CI signal the loud-
// fail was meant to provide. Plumbing doBd's numeric code through
// exitForCode ensures the process exit code matches what doBd computed.
func TestGcBdProcessExitCodeMatchesSilentFallbackContract(t *testing.T) {
	silentFallbackTestSetup(t, silentFallbackFakeBdScript)

	var stdout, stderr bytes.Buffer
	got := run([]string{"bd", "update", "demo-abc", "--set-metadata", "k=v"}, &stdout, &stderr)
	if got != bdSilentFallbackExitCode {
		t.Fatalf("run(bd update) = %d, want %d (silent-fallback exit code); stderr=%q",
			got, bdSilentFallbackExitCode, stderr.String())
	}
}

// TestGcBdProcessExitCodePreservesBdNonZero is the inverse case: when bd
// returns a non-zero code that isn't the silent-fallback case (e.g., bd
// itself rejected the command), gc bd must preserve bd's exit code rather
// than collapsing it to 1. exitForCode encodes ≥2 codes via
// commandExitError so commandExitCode reads them back faithfully.
func TestGcBdProcessExitCodePreservesBdNonZero(t *testing.T) {
	const bdRejectsScript = `#!/bin/sh
echo "bd: simulated usage error" >&2
exit 3
`
	silentFallbackTestSetup(t, bdRejectsScript)

	var stdout, stderr bytes.Buffer
	got := run([]string{"bd", "list"}, &stdout, &stderr)
	if got != 3 {
		t.Fatalf("run(bd list) = %d, want 3 (preserved bd exit code); stderr=%q",
			got, stderr.String())
	}
}

// TestBdOutputIndicatesSilentFallback covers the marker-detection helper
// directly with table-driven cases so the source-of-truth for what counts
// as "silent fallback" is unit-pinned.
func TestBdOutputIndicatesSilentFallback(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty", "", false},
		{"single marker only auto-importing", "auto-importing 100 bytes from foo", false},
		{"single marker only into-empty-database", "into empty database", false},
		{"both markers same line", "auto-importing 100 bytes into empty database", true},
		{"both markers reversed order", "into empty database <- auto-importing 100 bytes", true},
		{"both markers across newlines", "auto-importing 100 bytes\n  into empty database\n  done", true},
		{"case insensitive uppercase", "AUTO-IMPORTING INTO EMPTY DATABASE", true},
		{"case insensitive mixed", "Auto-Importing 200 bytes Into Empty Database", true},
		{"unrelated transport error", "dial tcp 127.0.0.1:3306: connect: connection refused", false},
		{"unrelated server-unreachable error", "server unreachable", false},
		{"both markers buried in long output", "starting bd\n... \nauto-importing 220929 bytes from .beads/issues.jsonl into empty database... \n... \nauto-imported 123 issues\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bdOutputIndicatesSilentFallback(tt.input); got != tt.want {
				t.Errorf("bdOutputIndicatesSilentFallback(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestHeadLimitedWriter pins the bounded-prefix behavior used to scan bd's
// stderr: writes past the limit are reported as fully consumed (so it is
// safe behind io.MultiWriter) but only the first limit bytes are retained.
func TestHeadLimitedWriter(t *testing.T) {
	t.Run("retains only the first limit bytes of an oversized write", func(t *testing.T) {
		w := &headLimitedWriter{limit: 5}
		n, err := w.Write([]byte("abcdefgh"))
		if err != nil || n != 8 {
			t.Fatalf("Write = (%d, %v), want (8, nil)", n, err)
		}
		if got := w.String(); got != "abcde" {
			t.Fatalf("String() = %q, want %q", got, "abcde")
		}
	})
	t.Run("accumulates across writes and stops at the limit", func(t *testing.T) {
		w := &headLimitedWriter{limit: 5}
		for _, chunk := range []string{"ab", "cdef", "ghi"} {
			if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
				t.Fatalf("Write(%q) = (%d, %v), want (%d, nil)", chunk, n, err, len(chunk))
			}
		}
		if got := w.String(); got != "abcde" {
			t.Fatalf("String() = %q, want %q", got, "abcde")
		}
	})
	t.Run("zero limit retains nothing but still consumes the write", func(t *testing.T) {
		w := &headLimitedWriter{limit: 0}
		n, err := w.Write([]byte("xyz"))
		if err != nil || n != 3 {
			t.Fatalf("Write = (%d, %v), want (3, nil)", n, err)
		}
		if got := w.String(); got != "" {
			t.Fatalf("String() = %q, want empty", got)
		}
	})
}

// captureBdInvocations installs a fake bd that appends one line per
// invocation, so a test can assert on a SEQUENCE of forwarded commands rather
// than only the last one. extraScript is appended to the recorder and may exit
// non-zero to simulate a bd failure. Returns the capture file path.
func captureBdInvocations(t *testing.T, extraScript string) string {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "gc-bd-args.txt")
	silentFallbackTestSetup(t, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"${CAPTURE_PATH}\"\n"+extraScript)
	t.Setenv("CAPTURE_PATH", capture)
	return capture
}

// readBdInvocations returns the forwarded arg-lines recorded by
// captureBdInvocations. A missing file means bd was never invoked.
func readBdInvocations(t *testing.T, capture string) []string {
	t.Helper()
	data, err := os.ReadFile(capture)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// bdWriteInvocations drops the read-only `show` calls gc makes on its own
// behalf -- the exact-ID collision guard resolves every mutation's bead ID
// through `bd show --json` before forwarding it -- so a test can assert the
// exact sequence of WRITES without pinning how many reads the guard needs.
// Only reads are dropped: an unexpected write still fails the assertion,
// which is the property these tests are about.
func bdWriteInvocations(t *testing.T, capture string) []string {
	t.Helper()
	var writes []string
	for _, call := range readBdInvocations(t, capture) {
		if strings.HasPrefix(call, "show ") {
			continue
		}
		writes = append(writes, call)
	}
	return writes
}

// TestGcBdHeartbeatRefreshesLeaseBeforeStampingLiveness pins BOTH writes that
// `gc bd heartbeat <id>` owes, and their order.
//
// The lease refresh is bd's own `heartbeat` subcommand. It is the only thing
// that pushes lease_expires_at forward, and therefore the only thing that
// stops `bd reclaim` from reverting a bead to ready and handing it to a second
// agent while the first is still working it. ci-ctkz is what happens without
// it: gc rewrote `heartbeat` into the metadata write alone, so the command an
// agent was told to call for liveness reported success while the claim lease
// it was named after quietly lapsed.
//
// The gc.last_heartbeat_at stamp is a separate contract and is NOT redundant
// with the lease: bd keeps leases in an ephemeral node-local table that is
// never committed to Dolt, so the dashboard (gastownhall/gascity#1855, reader
// dashboard #324) cannot see one. Dropping either write loses a distinct
// consumer.
//
// The order is asserted rather than left incidental. The lease is the half
// that prevents a concurrent writer, so it goes first; see the sibling test
// for what a failure there must suppress.
func TestGcBdHeartbeatRefreshesLeaseBeforeStampingLiveness(t *testing.T) {
	origNow := bdHeartbeatNow
	t.Cleanup(func() { bdHeartbeatNow = origNow })
	// Pin the clock to a non-UTC zone to prove the stamp normalizes to UTC.
	fixed := time.Date(2026, 5, 31, 12, 0, 0, 0, time.FixedZone("PST", -8*3600))
	bdHeartbeatNow = func() time.Time { return fixed }

	capture := captureBdInvocations(t, "")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"heartbeat", "demo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(heartbeat) = %d, want 0; stderr=%q", got, stderr.String())
	}

	calls := bdWriteInvocations(t, capture)
	if len(calls) != 2 {
		t.Fatalf("bd writes = %q, want exactly 2 (lease refresh then liveness stamp)", calls)
	}
	if calls[0] != "heartbeat demo-abc" {
		t.Errorf("first bd write = %q, want %q (the lease refresh must come first)", calls[0], "heartbeat demo-abc")
	}
	const prefix = "update demo-abc --set-metadata " + heartbeatMetadataKey + "="
	stamp, ok := strings.CutPrefix(calls[1], prefix)
	if !ok {
		t.Fatalf("second bd write = %q, want prefix %q", calls[1], prefix)
	}
	parsed, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("heartbeat stamp %q is not valid RFC3339: %v", stamp, err)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		t.Fatalf("heartbeat stamp %q is not UTC (zone offset %d)", stamp, offset)
	}
	if want := fixed.UTC().Format(time.RFC3339); stamp != want {
		t.Fatalf("heartbeat stamp = %q, want %q", stamp, want)
	}
}

// TestGcBdHeartbeatAliasTakesTheSamePath pins that `hb` is not a back door.
// bd publishes `hb` as an alias of `heartbeat`, and before ci-ctkz the two
// spellings did entirely different things through gc: `heartbeat` was
// intercepted and rewritten to a metadata-only update, while `hb` fell through
// the generic passthrough to bd's real lease refresh. Which half of the
// contract a worker got depended on how it spelled the command.
func TestGcBdHeartbeatAliasTakesTheSamePath(t *testing.T) {
	capture := captureBdInvocations(t, "")

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"hb", "demo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(hb) = %d, want 0; stderr=%q", got, stderr.String())
	}

	calls := bdWriteInvocations(t, capture)
	if len(calls) != 2 {
		t.Fatalf("bd writes for the alias = %q, want the same 2 as the canonical spelling", calls)
	}
	// The alias is normalized to bd's canonical subcommand name on the way
	// out, so the forwarded command does not depend on the caller's spelling.
	if calls[0] != "heartbeat demo-abc" {
		t.Errorf("first bd write = %q, want %q", calls[0], "heartbeat demo-abc")
	}
	if !strings.HasPrefix(calls[1], "update demo-abc --set-metadata "+heartbeatMetadataKey+"=") {
		t.Errorf("second bd write = %q, want the liveness stamp", calls[1])
	}
}

// TestGcBdHeartbeatFailedLeaseSuppressesLivenessStamp pins the ordering's
// whole point. bd refuses a heartbeat when the caller is no longer the owner
// -- the lease was already reclaimed, or the issue closed -- precisely so the
// worker learns to stop. Stamping gc.last_heartbeat_at after that refusal
// would report the worker alive on a bead it has just been told it no longer
// holds, which is the one reading that key exists to rule out. The refusal
// must also reach the caller as a non-zero exit rather than being absorbed.
func TestGcBdHeartbeatFailedLeaseSuppressesLivenessStamp(t *testing.T) {
	capture := captureBdInvocations(t, "if [ \"$1\" = heartbeat ]; then exit 3; fi\n")

	// Assert bd's own exit code survives rather than merely "non-zero": gc bd
	// documents that it plumbs bd's codes through (and reserves 4 for the
	// silent-fallback loud-fail), so collapsing a refusal to 1 would destroy
	// the distinction a caller uses to tell them apart.
	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"heartbeat", "demo-abc"}, &stdout, &stderr); got != 3 {
		t.Fatalf("doBd(heartbeat) = %d, want 3 (bd's own exit code for the refused lease refresh); stderr=%q", got, stderr.String())
	}

	calls := bdWriteInvocations(t, capture)
	if len(calls) != 1 {
		t.Fatalf("bd writes = %q, want exactly 1 (the refused lease refresh, no liveness stamp)", calls)
	}
	if calls[0] != "heartbeat demo-abc" {
		t.Errorf("bd write = %q, want %q", calls[0], "heartbeat demo-abc")
	}
}

// leaseHolderFakeBdScript is a bd stand-in that enforces bd's REAL ownership
// rule instead of accepting every heartbeat: HeartbeatIssueInTx matches
// `WHERE issue_id = ? AND holder = ?`, the holder being whoever claimed the
// bead and the actor coming from --actor or $BEADS_ACTOR. $FAKE_LEASE_HOLDER is
// that holder, and it is also the assignee `show --json` reports, because
// gc hook --claim takes the lease under the identity the bead was already
// assigned to.
//
// The refusal branch is the whole point of the stand-in. One that exited 0 for
// every heartbeat would hand a pass to the exact defect these tests pin --
// gc presenting an actor that is not the holder -- because from outside bd the
// only difference is the exit code. Anything other than the three scripted
// subcommands exits 64 rather than 0 for the same reason.
const leaseHolderFakeBdScript = `
if [ "$1" = show ]; then
	printf '[{"id":"%s","status":"in_progress","assignee":"%s"}]\n' "$FAKE_BEAD_ID" "$FAKE_LEASE_HOLDER"
	exit 0
fi
if [ "$1" = heartbeat ]; then
	actor="$BEADS_ACTOR"
	for arg in "$@"; do
		case "$arg" in --actor=*) actor="${arg#--actor=}" ;; esac
	done
	if [ "$actor" != "$FAKE_LEASE_HOLDER" ]; then
		echo "Error: heartbeat $2: issue already claimed by $FAKE_LEASE_HOLDER" >&2
		exit 3
	fi
	exit 0
fi
if [ "$1" = update ]; then
	exit 0
fi
echo "bd stand-in: unscripted subcommand $1" >&2
exit 64
`

// heartbeatSessionIdentity is the set of identity env vars gc bd heartbeat
// reads to decide whether a bead's lease holder is a form of THIS session.
// alias covers GC_ALIAS, GC_AGENT and $BEADS_ACTOR together, which is how a
// managed session is actually launched (session.RuntimeEnvWithSessionContext).
type heartbeatSessionIdentity struct {
	sessionID   string
	sessionName string
	alias       string
}

// heartbeatLeaseHolderSetup installs leaseHolderFakeBdScript for one bead and
// pins every identity env var the actor decision reads. Pinning all of them is
// required, not tidiness: this suite runs inside a managed agent session whose
// own GC_ALIAS / GC_SESSION_ID would otherwise decide the outcome, and a test
// that passes because of the developer's environment proves nothing.
func heartbeatLeaseHolderSetup(t *testing.T, beadID, holder string, id heartbeatSessionIdentity) string {
	t.Helper()
	capture := captureBdInvocations(t, leaseHolderFakeBdScript)
	t.Setenv("FAKE_BEAD_ID", beadID)
	t.Setenv("FAKE_LEASE_HOLDER", holder)
	t.Setenv("GC_SESSION_ID", id.sessionID)
	t.Setenv("GC_SESSION_NAME", id.sessionName)
	t.Setenv("GC_ALIAS", id.alias)
	t.Setenv("GC_AGENT", id.alias)
	t.Setenv("BEADS_ACTOR", id.alias)
	return capture
}

// TestGcBdHeartbeatRefreshesALeaseHeldUnderThisSessionsSessionID pins ci-eaon.
// A bead pre-assigned by session bead id -- what continuation preassignment and
// slinging to a named session both produce -- takes its lease under that id,
// because gc hook --claim deliberately claims as the bead's own assignee rather
// than $BEADS_ACTOR. The working agent's $BEADS_ACTOR is its alias, so bd's
// holder check refused the refresh and the lease lapsed under a live worker.
//
// Both halves are asserted. The lease refresh must carry the holder's identity,
// and the gc.last_heartbeat_at stamp must then land: ci-ctkz put the lease
// refresh FIRST, so a refusal there also cost such a bead the dashboard stamp
// it used to get, which is what made this a regression rather than a
// nice-to-have.
func TestGcBdHeartbeatRefreshesALeaseHeldUnderThisSessionsSessionID(t *testing.T) {
	origNow := bdHeartbeatNow
	t.Cleanup(func() { bdHeartbeatNow = origNow })
	fixed := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	bdHeartbeatNow = func() time.Time { return fixed }

	capture := heartbeatLeaseHolderSetup(t, "demo-abc", "ci-ll22", heartbeatSessionIdentity{
		sessionID:   "ci-ll22",
		sessionName: "toolsmith-ci-ll22",
		alias:       "toolsmith-2",
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"heartbeat", "demo-abc"}, &stdout, &stderr); got != 0 {
		t.Fatalf("doBd(heartbeat) = %d, want 0 (the holder is this session under another identity form); stderr=%q", got, stderr.String())
	}

	calls := bdWriteInvocations(t, capture)
	if len(calls) != 2 {
		t.Fatalf("bd writes = %q, want 2 (lease refresh as the holder, then the liveness stamp)", calls)
	}
	// The =-joined form is load-bearing, so it is asserted literally rather
	// than by substring: a two-token `--actor ci-ll22` puts a city-prefixed
	// bead id into the args resolveBdScopeTarget scans, which would pin the
	// city store and mis-scope a heartbeat on a RIG bead.
	if want := "heartbeat demo-abc --actor=ci-ll22"; calls[0] != want {
		t.Errorf("lease refresh = %q, want %q", calls[0], want)
	}
	// Parsing the remainder as a bare instant also pins that the stamp write
	// carries no --actor: its audit trail stays this session's $BEADS_ACTOR,
	// which is who is really writing it.
	prefix := "update demo-abc --set-metadata " + heartbeatMetadataKey + "="
	stamp, ok := strings.CutPrefix(calls[1], prefix)
	if !ok {
		t.Fatalf("second bd write = %q, want prefix %q", calls[1], prefix)
	}
	if want := fixed.UTC().Format(time.RFC3339); stamp != want {
		t.Fatalf("heartbeat stamp = %q, want exactly %q", stamp, want)
	}
}

// TestGcBdHeartbeatWillNotBorrowAnotherSessionsLeaseHolder is the negative half
// of ci-eaon, and the reason the assignee is not passed unconditionally. Doing
// that would let any caller refresh any other agent's lease, and it would
// delete the "learn to stop" signal -- bd's refusal after a reclaim is the only
// thing that tells a worker its claim is gone. So a holder that is not one of
// this session's identity forms must reach bd as no --actor at all, and bd's
// refusal must survive as the exit code with no liveness stamp behind it.
func TestGcBdHeartbeatWillNotBorrowAnotherSessionsLeaseHolder(t *testing.T) {
	capture := heartbeatLeaseHolderSetup(t, "demo-abc", "ci-someone-else", heartbeatSessionIdentity{
		sessionID:   "ci-ll22",
		sessionName: "toolsmith-ci-ll22",
		alias:       "toolsmith-2",
	})

	var stdout, stderr bytes.Buffer
	if got := doBd([]string{"heartbeat", "demo-abc"}, &stdout, &stderr); got != 3 {
		t.Fatalf("doBd(heartbeat) = %d, want 3 (bd's refusal for a lease this session does not hold); stderr=%q", got, stderr.String())
	}

	calls := bdWriteInvocations(t, capture)
	if len(calls) != 1 {
		t.Fatalf("bd writes = %q, want exactly 1 (the refused lease refresh, no liveness stamp)", calls)
	}
	if calls[0] != "heartbeat demo-abc" {
		t.Errorf("lease refresh = %q, want %q with no --actor", calls[0], "heartbeat demo-abc")
	}
}

// TestResolveBdScopeTargetReadsATwoTokenFlagValueAsTheSubject is the evidence
// behind the =-joined `--actor=<holder>` form doBdHeartbeat builds, rather than
// leaving that choice as a claim in a comment.
//
// Scope auto-detection scans EVERY non-flag arg and accepts the first one that
// looks like -- and exists as -- a bead, with the city prefix probed before any
// rig. A lease holder is frequently a session bead id in the CITY store, so
// passing it as a separate token would re-scope a heartbeat on a RIG bead to
// the city and lose the bead. Joining it into one flag-shaped token is what
// keeps it out of the scan.
//
// When this test starts failing because resolveBdScopeTarget learned to skip
// flag VALUES -- bdflags.Positionals is the tested vocabulary for that -- the
// constraint on doBdHeartbeat's argv is gone and its comment goes with it.
func TestResolveBdScopeTargetReadsATwoTokenFlagValueAsTheSubject(t *testing.T) {
	setCwd(t, t.TempDir())
	origProbe := bdBeadExists
	t.Cleanup(func() { bdBeadExists = origProbe })
	// Every probed id exists in the store being probed: the question here is
	// which args are OFFERED to the probe, not which beads are real.
	bdBeadExists = func(_ string, _ *config.City, _ execStoreTarget, _ string) bool { return true }

	cityDir := filepath.Join(t.TempDir(), "city")
	cfg := &config.City{
		Workspace: config.Workspace{Name: "demo", Prefix: "demo"},
		Rigs:      []config.Rig{{Name: "gascity", Path: filepath.Join("rigs", "gascity"), Prefix: "gs"}},
	}
	rigTarget := execStoreTarget{
		ScopeRoot: filepath.Join(cityDir, "rigs", "gascity"),
		ScopeKind: "rig",
		Prefix:    "gs",
		RigName:   "gascity",
	}

	t.Run("two tokens: the holder wins over the bead being heartbeaten", func(t *testing.T) {
		got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"heartbeat", "gs-abc", "--actor", "demo-sess1"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		if got.ScopeKind != "city" {
			t.Fatalf("resolveBdScopeTarget() = %#v, want the city scope; the hazard doBdHeartbeat's =-joined --actor avoids has disappeared, so relax that comment", got)
		}
	})

	t.Run("one joined token: the bead being heartbeaten still wins", func(t *testing.T) {
		got, err := resolveBdScopeTarget(cfg, cityDir, "", []string{"heartbeat", "gs-abc", "--actor=demo-sess1"}, false, io.Discard)
		if err != nil {
			t.Fatalf("resolveBdScopeTarget() error = %v", err)
		}
		if got != rigTarget {
			t.Fatalf("resolveBdScopeTarget() = %#v, want %#v", got, rigTarget)
		}
	})
}

// TestBdHeartbeatLeaseActorOverridesOnlyForThisSessionsOwnIdentity pins the
// decision itself, away from the city/bd harness. Each row is a holder form
// gc hook --claim can leave on a bead.
func TestBdHeartbeatLeaseActorOverridesOnlyForThisSessionsOwnIdentity(t *testing.T) {
	identities := []string{"toolsmith-2", "toolsmith-ci-ll22", "ci-ll22"}
	for _, tc := range []struct {
		name       string
		assignee   string
		ambient    string
		identities []string
		want       string
	}{
		{
			name:       "holder is already the ambient actor",
			assignee:   "toolsmith-2",
			ambient:    "toolsmith-2",
			identities: identities,
			want:       "",
		},
		{
			name:       "holder is this session's bead id",
			assignee:   "ci-ll22",
			ambient:    "toolsmith-2",
			identities: identities,
			want:       "ci-ll22",
		},
		{
			name:       "holder is this session's runtime name",
			assignee:   "toolsmith-ci-ll22",
			ambient:    "toolsmith-2",
			identities: identities,
			want:       "toolsmith-ci-ll22",
		},
		{
			name:       "holder is another session",
			assignee:   "ci-someone-else",
			ambient:    "toolsmith-2",
			identities: identities,
			want:       "",
		},
		{
			name:       "bead has no holder",
			assignee:   "",
			ambient:    "toolsmith-2",
			identities: identities,
			want:       "",
		},
		{
			// A caller outside any managed session knows no identity forms, so
			// it can never claim a holder is itself.
			name:       "session identity is unknown",
			assignee:   "ci-ll22",
			ambient:    "",
			identities: nil,
			want:       "",
		},
		{
			// Padding round-tripped through the store must not read as a
			// different identity and manufacture a redundant --actor.
			name:       "holder differs from the ambient actor only by padding",
			assignee:   "  toolsmith-2  ",
			ambient:    "toolsmith-2",
			identities: identities,
			want:       "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := bdHeartbeatLeaseActor(tc.assignee, tc.ambient, tc.identities); got != tc.want {
				t.Fatalf("bdHeartbeatLeaseActor(%q, %q, %q) = %q, want %q", tc.assignee, tc.ambient, tc.identities, got, tc.want)
			}
		})
	}
}

// TestBdHeartbeatIdentityFormsExcludeTheBarePoolTemplate pins a documented
// ABSENCE. GC_TEMPLATE is set on every suffixed pool worker and holds the bare
// template name, which is ALSO the [[named_session]] holder's identity --
// admitting it would let toolsmith-3 refresh a lease held by toolsmith, the
// cross-session adoption gc hook --claim excludes it to prevent (ga-80pen8).
func TestBdHeartbeatIdentityFormsExcludeTheBarePoolTemplate(t *testing.T) {
	t.Setenv("GC_ALIAS", "toolsmith-3")
	t.Setenv("GC_AGENT", "toolsmith-3")
	t.Setenv("GC_SESSION_NAME", "toolsmith-ci-ll22")
	t.Setenv("GC_SESSION_ID", "ci-ll22")
	t.Setenv("GC_TEMPLATE", "toolsmith")

	want := []string{"toolsmith-3", "toolsmith-ci-ll22", "ci-ll22"}
	if got := bdHeartbeatIdentityForms(); !slices.Equal(got, want) {
		t.Fatalf("bdHeartbeatIdentityForms() = %q, want %q (GC_TEMPLATE=%q must not appear)", got, want, "toolsmith")
	}
}

// TestParseBdHeartbeatArgs covers the arg edge cases without the full city/bd
// harness: exactly one issue id is required, both spellings are recognized,
// and non-heartbeat commands are not claimed so the generic bd passthrough
// stays intact.
func TestParseBdHeartbeatArgs(t *testing.T) {
	t.Run("rejects wrong arity, flag-as-id, or whitespace id", func(t *testing.T) {
		for _, args := range [][]string{
			{"heartbeat"},
			{"heartbeat", "demo-abc", "extra"},
			{"heartbeat", "--flag"},
			{"heartbeat", ""},
			{"heartbeat", "  "},        // all-whitespace
			{"heartbeat", " demo-abc"}, // leading space
			{"heartbeat", "demo-abc "}, // trailing space
			{"heartbeat", "demo abc"},  // internal space
			{"hb"},
			{"hb", "demo-abc", "extra"},
		} {
			id, ok, err := parseBdHeartbeatArgs(args)
			if err == nil {
				t.Fatalf("parseBdHeartbeatArgs(%q) = (%q, %v, nil), want usage error", args, id, ok)
			}
			if !ok {
				t.Fatalf("parseBdHeartbeatArgs(%q) reported ok=false with an error; the caller would forward a malformed heartbeat to bd", args)
			}
		}
	})
	t.Run("accepts a clean id under both spellings", func(t *testing.T) {
		for _, sub := range []string{"heartbeat", "hb"} {
			id, ok, err := parseBdHeartbeatArgs([]string{sub, "demo-abc"})
			if err != nil || !ok || id != "demo-abc" {
				t.Fatalf("parseBdHeartbeatArgs(%q demo-abc) = (%q, %v, %v), want (demo-abc, true, nil)", sub, id, ok, err)
			}
		}
	})
	t.Run("does not claim non-heartbeat args", func(t *testing.T) {
		for _, args := range [][]string{
			{"list", "-s", "open"},
			{"update", "demo-abc", "--claim"},
			nil,
		} {
			id, ok, err := parseBdHeartbeatArgs(args)
			if ok || err != nil {
				t.Fatalf("parseBdHeartbeatArgs(%q) = (%q, %v, %v), want (\"\", false, nil)", args, id, ok, err)
			}
		}
	})
}

// TestBdMutationWriteID covers the compatibility shim (first-ID extraction).
func TestBdMutationWriteID(t *testing.T) {
	t.Run("extracts id from write subcommands", func(t *testing.T) {
		cases := []struct {
			args []string
			want string
		}{
			{[]string{"update", "gcy-dv7", "--title", "x"}, "gcy-dv7"},
			{[]string{"update", "--title", "x", "gcy-dv7"}, "gcy-dv7"},
			{[]string{"close", "gcy-dv7"}, "gcy-dv7"},
			{[]string{"close", "--reason", "done", "gcy-dv7"}, "gcy-dv7"},
			{[]string{"close", "--force", "--json", "gcy-dv7"}, "gcy-dv7"},
			{[]string{"reopen", "gcy-dv7"}, "gcy-dv7"},
			{[]string{"delete", "--force", "gcy-dv7"}, "gcy-dv7"},
			{[]string{"delete", "--force", "--json", "gcy-dv7"}, "gcy-dv7"},
			// double-dash separator
			{[]string{"update", "--", "gcy-dv7"}, "gcy-dv7"},
		}
		for _, tc := range cases {
			got, ok := bdMutationWriteID(tc.args)
			if !ok || got != tc.want {
				t.Errorf("bdMutationWriteID(%q) = (%q, %v), want (%q, true)", tc.args, got, ok, tc.want)
			}
		}
	})
	t.Run("returns false for read or unrecognized subcommands", func(t *testing.T) {
		for _, args := range [][]string{
			{"show", "gcy-dv7"},
			{"list", "-s", "open"},
			{"query", "gcy-dv7"},
			{},
			{"create", "new task"},
		} {
			if _, ok := bdMutationWriteID(args); ok {
				t.Errorf("bdMutationWriteID(%q) returned ok=true, want false", args)
			}
		}
	})
	// Regression: short ID "gcy-dv7" must NOT be confused for "gcy-wisp-dv78"
	// by the caller — the returned token is the exact string in args.
	t.Run("returns the exact supplied token (gcy-g4o regression)", func(t *testing.T) {
		got, ok := bdMutationWriteID([]string{"update", "gcy-dv7", "--status", "open"})
		if !ok || got != "gcy-dv7" {
			t.Errorf("bdMutationWriteID: got (%q, %v), want (\"gcy-dv7\", true)", got, ok)
		}
	})
}

// TestBdMutationWriteIDs covers the full scanner used by the pre-flight guard.
func TestBdMutationWriteIDs(t *testing.T) {
	type result struct {
		ids       []string
		ok        bool
		ambiguous bool
	}
	cases := []struct {
		name string
		args []string
		want result
	}{
		// --- Basic extraction ---
		{
			name: "single id, no flags",
			args: []string{"close", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "id before flags",
			args: []string{"update", "gcy-dv7", "--title", "x"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "id after long value flag",
			args: []string{"update", "--title", "new title", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},

		// --- Short flags (previously broken) ---
		{
			name: "short -s value flag before id",
			args: []string{"update", "-s", "closed", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -a value flag before id",
			args: []string{"update", "-a", "alice", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -t value flag before id",
			args: []string{"update", "-t", "task", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -p value flag before id",
			args: []string{"update", "-p", "P2", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -d value flag before id",
			args: []string{"update", "-d", "description text", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -e value flag before id",
			args: []string{"update", "-e", "60", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -r reason flag for close before id",
			args: []string{"close", "-r", "done", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "short -C directory flag before id",
			args: []string{"close", "-C", "/some/path", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},

		// --- --flag=value form ---
		{
			name: "--flag=value does not consume next token",
			args: []string{"update", "--status=closed", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "--title=value with id after",
			args: []string{"update", "--title=new title", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},

		// --- Message / notes flags whose values may contain bead tokens ---
		{
			name: "notes value contains bead token",
			args: []string{"update", "--notes", "see gcy-dv7 for context", "gcy-real"},
			want: result{ids: []string{"gcy-real"}, ok: true},
		},
		{
			name: "append-notes value contains bead token",
			args: []string{"update", "--append-notes", "related: gcy-dv7", "gcy-real"},
			want: result{ids: []string{"gcy-real"}, ok: true},
		},

		// --- Batch IDs ---
		{
			name: "batch close multiple ids",
			args: []string{"close", "id1", "id2", "id3"},
			want: result{ids: []string{"id1", "id2", "id3"}, ok: true},
		},
		{
			name: "batch delete multiple ids with --force",
			args: []string{"delete", "--force", "id1", "id2", "id3"},
			want: result{ids: []string{"id1", "id2", "id3"}, ok: true},
		},
		{
			name: "batch update with flags interspersed",
			args: []string{"update", "--status", "closed", "id1", "id2"},
			want: result{ids: []string{"id1", "id2"}, ok: true},
		},

		// --- Double-dash terminator ---
		{
			name: "double-dash: everything after is positional",
			args: []string{"update", "--", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "double-dash: multiple ids after",
			args: []string{"close", "--force", "--", "id1", "id2"},
			want: result{ids: []string{"id1", "id2"}, ok: true},
		},

		// --- Fail-closed: ambiguous unknown flags ---
		// gcy-g4o demonstrated break: `close --session gcy-realbead gcy-dv7`
		// Previously the hand-rolled scanner lacked --session → returned
		// "gcy-realbead" as the ID, leaving gcy-dv7 unguarded. Now --session
		// is in the known set so it is correctly handled, but an *unknown*
		// value flag must trigger ambiguous.
		{
			name: "known --session flag handled correctly for close",
			args: []string{"close", "--session", "sess-id-abc", "gcy-dv7"},
			want: result{ids: []string{"gcy-dv7"}, ok: true},
		},
		{
			name: "unknown value flag triggers fail-closed",
			args: []string{"close", "--unknown-future-flag", "gcy-realbead", "gcy-dv7"},
			want: result{ok: true, ambiguous: true},
		},
		{
			name: "unknown short flag triggers fail-closed",
			args: []string{"update", "-z", "something", "gcy-dv7"},
			want: result{ok: true, ambiguous: true},
		},

		// --- Non-write subcommands ---
		{
			name: "show is not a write command",
			args: []string{"show", "gcy-dv7"},
			want: result{ok: false},
		},
		{
			name: "list is not a write command",
			args: []string{"list", "-s", "open"},
			want: result{ok: false},
		},
		{
			name: "empty args",
			args: []string{},
			want: result{ok: false},
		},

		// --- No IDs supplied (e.g. "last touched" fallback) ---
		{
			name: "update with no ids (last-touched fallback)",
			args: []string{"update", "--status", "closed"},
			want: result{ids: nil, ok: true},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ids, ok, ambiguous := bdMutationWriteIDs(tc.args)
			if ok != tc.want.ok {
				t.Errorf("ok = %v, want %v", ok, tc.want.ok)
			}
			if ambiguous != tc.want.ambiguous {
				t.Errorf("ambiguous = %v, want %v", ambiguous, tc.want.ambiguous)
			}
			if !bdTestSlicesEqual(ids, tc.want.ids) {
				t.Errorf("ids = %v, want %v", ids, tc.want.ids)
			}
		})
	}
}

func bdTestSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestParseBdReleaseIfCurrentArgs(t *testing.T) {
	id, assignee, ok, err := parseBdReleaseIfCurrentArgs([]string{"release-if-current", "gc-abc", "worker-1"})
	if err != nil {
		t.Fatalf("parseBdReleaseIfCurrentArgs unexpected error: %v", err)
	}
	if !ok || id != "gc-abc" || assignee != "worker-1" {
		t.Fatalf("parseBdReleaseIfCurrentArgs = (%q, %q, %v), want gc-abc worker-1 true", id, assignee, ok)
	}
	if _, _, ok, err := parseBdReleaseIfCurrentArgs([]string{"list"}); ok || err != nil {
		t.Fatalf("non-release command parsed as release: ok=%v err=%v", ok, err)
	}
	for _, args := range [][]string{
		{"release-if-current"},
		{"release-if-current", "gc-abc"},
		{"release-if-current", "gc-abc", "worker-1", "extra"},
		{"release-if-current", "", "worker-1"},
		{"release-if-current", "gc abc", "worker-1"},
		{"release-if-current", "gc-abc", "worker 1"},
	} {
		if _, _, ok, err := parseBdReleaseIfCurrentArgs(args); !ok || err == nil {
			t.Fatalf("parseBdReleaseIfCurrentArgs(%q) = ok=%v err=%v, want release usage error", args, ok, err)
		}
	}
}

func TestDoBdReleaseIfCurrentUpdatesOnlyMatchingAssignment(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"demo\"\n\n[beads]\nprovider = \"file\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	created, err := store.Create(beads.Bead{Title: "work", Assignee: "worker-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	target := execStoreTarget{ScopeRoot: cityDir, ScopeKind: "city", Prefix: "gc"}
	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "worker-2", &stdout, &stderr); got != 0 {
		t.Fatalf("doBdReleaseIfCurrent wrong assignee = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "skipped" {
		t.Fatalf("wrong-assignee output = %q, want skipped", stdout.String())
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after skipped release: %v", err)
	}
	if got.Status != "in_progress" || got.Assignee != "worker-1" {
		t.Fatalf("skipped release mutated bead: %+v", got)
	}

	stdout.Reset()
	stderr.Reset()
	if got := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "worker-1", &stdout, &stderr); got != 0 {
		t.Fatalf("doBdReleaseIfCurrent matching assignee = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "released" {
		t.Fatalf("matching-assignee output = %q, want released", stdout.String())
	}
	got, err = store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if got.Status != "open" || got.Assignee != "" {
		t.Fatalf("released bead = %+v, want open and unassigned", got)
	}
}

func TestDoBdReleaseIfCurrentReassignsRoutedWorkToItsPool(t *testing.T) {
	cityDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\nname = \"demo\"\n\n[beads]\nprovider = \"file\"\n"), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	store, err := openStoreAtForCity(cityDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity: %v", err)
	}
	created, err := store.Create(beads.Bead{
		Title:    "routed work",
		Assignee: "session-1",
		Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "worker-pool"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Update(created.ID, beads.UpdateOpts{Status: strPtr("in_progress")}); err != nil {
		t.Fatalf("Update status: %v", err)
	}

	target := execStoreTarget{ScopeRoot: cityDir, ScopeKind: "city", Prefix: "gc"}
	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, created.ID, "session-1", &stdout, &stderr); got != 0 {
		t.Fatalf("doBdReleaseIfCurrent = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "released" {
		t.Fatalf("release output = %q, want released", stdout.String())
	}
	got, err := store.Get(created.ID)
	if err != nil {
		t.Fatalf("Get after release: %v", err)
	}
	if got.Status != "open" || got.Assignee != "worker-pool" {
		t.Fatalf("released routed bead = %+v, want open and assigned to pool", got)
	}
}

func TestDoBdReleaseIfCurrentWorksForBdStoreFallback(t *testing.T) {
	clearInheritedBeadsEnv(t)
	origCityFlag := cityFlag
	origRigFlag := rigFlag
	defer func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	}()
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	rigDir := filepath.Join(cityDir, "frontend")
	if err := os.MkdirAll(rigDir, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(`[workspace]
name = "demo"

[beads]
provider = "file"

[[rigs]]
name = "frontend"
path = "frontend"
prefix = "fe"
`), 0o644); err != nil {
		t.Fatalf("write city.toml: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigDir, ".beads", "metadata.json"), []byte(`{"database":"dolt","backend":"dolt","dolt_mode":"embedded","dolt_database":"fe"}`), 0o644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}
	fakeBin := t.TempDir()
	sqlLog := filepath.Join(fakeBin, "sql.log")
	fakeBD := "#!/bin/sh\n" +
		"if [ \"$1\" = \"show\" ] && [ \"$2\" = \"--json\" ] && [ \"$3\" = \"fe-abc\" ]; then\n" +
		"  printf '[{\"id\":\"fe-abc\",\"title\":\"work\",\"status\":\"in_progress\",\"assignee\":\"worker-1\"}]\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"if [ \"$1\" = \"sql\" ] && [ \"$2\" = \"--json\" ]; then\n" +
		"  printf '%s\\n' \"$3\" > " + strconv.Quote(sqlLog) + "\n" +
		"  printf '{\"rows_affected\":1,\"schema_version\":1}\\n'\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf 'unexpected bd args:' >&2\n" +
		"printf ' %s' \"$@\" >&2\n" +
		"printf '\\n' >&2\n" +
		"exit 2\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "bd"), []byte(fakeBD), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	setCwd(t, cityDir)
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_BEADS_FORCE_FALLBACK", "1")

	store, err := openStoreAtForCity(rigDir, cityDir)
	if err != nil {
		t.Fatalf("openStoreAtForCity(rig): %v", err)
	}
	if _, ok := underlyingPolicyStoreForTest(store).(*beads.BdStore); !ok {
		t.Fatalf("openStoreAtForCity returned %T, want policy-wrapped *beads.BdStore", underlyingPolicyStoreForTest(store))
	}

	target := execStoreTarget{ScopeRoot: rigDir, ScopeKind: "rig", Prefix: "fe"}
	var stdout, stderr bytes.Buffer
	if got := doBdReleaseIfCurrent(cityDir, nil, target, "fe-abc", "worker-1", &stdout, &stderr); got != 0 {
		t.Fatalf("doBdReleaseIfCurrent = %d, want 0; stderr=%q", got, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "released" {
		t.Fatalf("output = %q, want released", stdout.String())
	}
	query, err := os.ReadFile(sqlLog)
	if err != nil {
		t.Fatalf("read SQL log: %v", err)
	}
	wantQuery := "UPDATE issues SET status = 'open', assignee = '', updated_at = CURRENT_TIMESTAMP WHERE id = 'fe-abc' AND status = 'in_progress' AND assignee = 'worker-1'"
	if strings.TrimSpace(string(query)) != wantQuery {
		t.Fatalf("SQL query = %q, want %q", strings.TrimSpace(string(query)), wantQuery)
	}
}

// TestGcBdPassthroughResolvesBdBinary pins the binary the `gc bd`
// passthrough execs. A city whose scope carries a complete storage binding
// runs the bd its workspace PATH pins — an ambient bd that cannot speak the
// bound backend would reject every command. A city with neither a binding
// nor a pin keeps the ambient lookup.
func TestGcBdPassthroughResolvesBdBinary(t *testing.T) {
	t.Run("complete binding runs the workspace-pinned bd", func(t *testing.T) {
		cityDir := newGcBdBinaryProbeCity(t)

		pinDir := t.TempDir()
		writeGcBdProbeScript(t, filepath.Join(pinDir, "bd"), "pinned-bd")
		cityTOML := "[workspace]\nname = \"demo\"\n\n[workspace.env]\nPATH = " +
			strconv.Quote(pinDir+string(os.PathListSeparator)+"$PATH") + "\n"
		if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityTOML), 0o644); err != nil {
			t.Fatal(err)
		}
		metadata := []byte(`{"backend":"postgres","storage_endpoint":"postgres://beads@db.example.test:5432","storage_database":"beads_pg"}`)
		if err := os.WriteFile(scopeMetadataJSONPath(cityDir), metadata, 0o600); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		if got := doBd([]string{"show", "gc-1"}, &stdout, &stderr); got != 0 {
			t.Fatalf("doBd() = %d, want 0; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "pinned-bd" {
			t.Fatalf("executed bd = %q, want workspace-pinned %q", got, "pinned-bd")
		}
	})

	t.Run("no binding falls back to ambient bd", func(t *testing.T) {
		cityDir := newGcBdBinaryProbeCity(t)
		if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte("[workspace]\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		if got := doBd([]string{"show", "gc-1"}, &stdout, &stderr); got != 0 {
			t.Fatalf("doBd() = %d, want 0; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "ambient-bd" {
			t.Fatalf("executed bd = %q, want ambient %q", got, "ambient-bd")
		}
	})

	// A city-scoped command reads the city's own binding, so a half-written
	// one is that command's fault and must stay fatal — the rig-scope
	// tolerance below it must not soften the scope that owns the binding.
	t.Run("partial city binding still fails the city scope", func(t *testing.T) {
		cityDir := newGcBdBinaryProbeCity(t)
		writeGcBdProbeCityTOML(t, cityDir, t.TempDir())
		if err := os.WriteFile(scopeMetadataJSONPath(cityDir), []byte(partialStorageBindingJSON), 0o600); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		if got := doBd([]string{"list"}, &stdout, &stderr); got != 1 {
			t.Fatalf("doBd() = %d, want 1; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
		}
		if want := "partial beads storage binding"; !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr = %q, want it to name the %q", stderr.String(), want)
		}
	})
}

// TestGcBdPassthroughResolvesBdBinaryForRigScope pins the binary the
// passthrough execs for `gc bd --rig`, a form the command's own help
// documents. The scope the command targets decides the binary, because that
// is the scope whose store the command reads and writes: a rig carrying its
// own complete binding runs the pinned build that speaks it, and a rig that
// overrides the city backend keeps the ambient lookup even when the city is
// bound — its store is not the bound one, and its runtime env carries no
// BD_BIN.
func TestGcBdPassthroughResolvesBdBinaryForRigScope(t *testing.T) {
	t.Run("rig carrying its own complete binding runs the workspace-pinned bd", func(t *testing.T) {
		cityDir := newGcBdBinaryProbeCity(t)
		pinDir := t.TempDir()
		writeGcBdProbeScript(t, filepath.Join(pinDir, "bd"), "pinned-bd")
		writeGcBdProbeCityTOML(t, cityDir, pinDir, "frontend")
		writeGcBdProbeRig(t, cityDir, "frontend", completeStorageBindingJSON)

		var stdout, stderr bytes.Buffer
		if got := doBd([]string{"--rig", "frontend", "list"}, &stdout, &stderr); got != 0 {
			t.Fatalf("doBd(--rig frontend) = %d, want 0; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "pinned-bd" {
			t.Fatalf("executed bd = %q, want workspace-pinned %q", got, "pinned-bd")
		}
	})

	t.Run("doltlite rig survives a partial city binding", func(t *testing.T) {
		cityDir := newGcBdBinaryProbeCity(t)
		pinDir := t.TempDir()
		writeGcBdProbeScript(t, filepath.Join(pinDir, "bd"), "pinned-bd")
		writeGcBdProbeCityTOML(t, cityDir, pinDir, "dl")
		// Half-written city provisioning state: storage_database never
		// landed. scopeHasCompleteStorageBinding rejects it, but the rig
		// below never reads that binding.
		if err := os.WriteFile(scopeMetadataJSONPath(cityDir), []byte(partialStorageBindingJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		writeGcBdProbeRig(t, cityDir, "dl", `{"backend":"doltlite"}`)

		var stdout, stderr bytes.Buffer
		if got := doBd([]string{"--rig", "dl", "list"}, &stdout, &stderr); got != 0 {
			t.Fatalf("doBd(--rig dl) = %d, want 0; a city-level binding fault must not take a doltlite rig offline; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "ambient-bd" {
			t.Fatalf("executed bd = %q, want ambient %q", got, "ambient-bd")
		}
	})

	t.Run("doltlite rig in a bound city keeps the ambient bd", func(t *testing.T) {
		cityDir := newGcBdBinaryProbeCity(t)
		pinDir := t.TempDir()
		writeGcBdProbeScript(t, filepath.Join(pinDir, "bd"), "pinned-bd")
		writeGcBdProbeCityTOML(t, cityDir, pinDir, "dl")
		if err := os.WriteFile(scopeMetadataJSONPath(cityDir), []byte(completeStorageBindingJSON), 0o600); err != nil {
			t.Fatal(err)
		}
		writeGcBdProbeRig(t, cityDir, "dl", `{"backend":"doltlite"}`)

		var stdout, stderr bytes.Buffer
		if got := doBd([]string{"--rig", "dl", "list"}, &stdout, &stderr); got != 0 {
			t.Fatalf("doBd(--rig dl) = %d, want 0; stdout=%q stderr=%q", got, stdout.String(), stderr.String())
		}
		if got := strings.TrimSpace(stdout.String()); got != "ambient-bd" {
			t.Fatalf("executed bd = %q, want ambient %q: a doltlite rig's runtime env carries no BD_BIN, so the passthrough must not exec the city's pin", got, "ambient-bd")
		}
	})
}

// completeStorageBindingJSON and partialStorageBindingJSON are the two
// storage-binding shapes scopeHasCompleteStorageBinding distinguishes: all
// three fields non-empty, and the half-written state it fails closed on.
const (
	completeStorageBindingJSON = `{"backend":"postgres","storage_endpoint":"postgres://beads@db.example.test:5432","storage_database":"beads_pg"}`
	partialStorageBindingJSON  = `{"backend":"postgres","storage_endpoint":"postgres://beads@db.example.test:5432"}`
)

// writeGcBdProbeCityTOML writes a city.toml pinning pinDir ahead of the
// ambient PATH and declaring the named rigs, plus the .gc/site.toml bindings
// that give them their paths under rigs/<name>.
func writeGcBdProbeCityTOML(t *testing.T, cityDir, pinDir string, rigNames ...string) {
	t.Helper()
	cityTOML := "[workspace.env]\nPATH = " +
		strconv.Quote(pinDir+string(os.PathListSeparator)+"$PATH") + "\n"
	siteTOML := "workspace_name = \"demo\"\n"
	for _, name := range rigNames {
		cityTOML += "\n[[rigs]]\nname = " + strconv.Quote(name) +
			"\nprefix = " + strconv.Quote(name) + "\n"
		siteTOML += "\n[[rig]]\nname = " + strconv.Quote(name) +
			"\npath = " + strconv.Quote(filepath.Join(cityDir, "rigs", name)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(cityDir, "city.toml"), []byte(cityTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cityDir, ".gc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cityDir, ".gc", "site.toml"), []byte(siteTOML), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeGcBdProbeRig stages rigs/<name> carrying the given
// .beads/metadata.json.
func writeGcBdProbeRig(t *testing.T, cityDir, name, metadata string) {
	t.Helper()
	rigDir := filepath.Join(cityDir, "rigs", name)
	if err := os.MkdirAll(filepath.Join(rigDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scopeMetadataJSONPath(rigDir), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gcBdAmbientProbeIdentity is what the bd on the ambient PATH prints, so a
// passthrough test can tell it apart from a workspace-pinned build.
const gcBdAmbientProbeIdentity = "ambient-bd"

// newGcBdBinaryProbeCity returns a city whose only bd on the ambient PATH
// announces gcBdAmbientProbeIdentity, so a passthrough test can tell which
// binary actually ran. The caller writes city.toml.
func newGcBdBinaryProbeCity(t *testing.T) string {
	t.Helper()
	disableManagedDoltRecoveryForTest(t)

	origCityFlag := cityFlag
	origRigFlag := rigFlag
	t.Cleanup(func() {
		cityFlag = origCityFlag
		rigFlag = origRigFlag
	})
	cityFlag = ""
	rigFlag = ""

	cityDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cityDir, ".beads"), 0o700); err != nil {
		t.Fatal(err)
	}
	// See TestResolveBdScopeTarget: isolate cwd so any `.beads/redirect` in
	// the ambient working tree doesn't surface here.
	setCwd(t, cityDir)
	writeBuiltinImportsFixture(t, cityDir, "core", "bd")

	ambientDir := t.TempDir()
	writeGcBdProbeScript(t, filepath.Join(ambientDir, "bd"), gcBdAmbientProbeIdentity)
	t.Setenv("PATH", ambientDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GC_CITY_PATH", cityDir)
	t.Setenv("GC_BEADS", "bd")
	return cityDir
}

// writeGcBdProbeScript writes a stand-in bd that announces which binary ran.
func writeGcBdProbeScript(t *testing.T, path, identity string) {
	t.Helper()
	script := "#!/bin/sh\necho " + identity + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
