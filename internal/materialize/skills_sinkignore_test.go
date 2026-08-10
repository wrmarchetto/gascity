// Scope: the sink-local .gitignore that Run writes so materialized
// symlinks stop showing up as working-tree changes in a rig repository
// (ci-x4mv). Covers which names reach the file, which are kept out of it,
// that it is rewritten rather than appended, and that gc refuses to touch
// one it did not author.
//
// WHY THESE ASSERT FILE TEXT RATHER THAN `git status`. The invariant that
// actually matters is "git reports the rig tree clean, and still reports a
// hand-added project skill", and the honest test drives real git. The
// repository forbids it: test/test-resources.toml ratchets subprocess call
// sites, the `scope=all` audit row counts build-tagged files too, so ANY
// new process-spawning test file fails
// TestRepositoryLedgerMatchesCensusAndDocumentation, and raising a
// baseline is an explicit policy change under council review. So these
// tests pin the pattern set, and the git-level behavior was cross-checked
// by hand instead. Reproduce, from a scratch directory:
//
//	sk=.claude/skills
//	git init -q . && cp <repo>/.gitignore .
//	mkdir -p $sk/gascity-docs && echo x > $sk/gascity-docs/SKILL.md
//	ln -s /any/absolute/path $sk/core.gc-work
//	echo '{}' > $sk/.gc-skill-ownership.json
//	git add .gitignore $sk/gascity-docs && git commit -qm base
//	git status --short   # the two artifacts show as untracked
//	printf '/.gitignore\n/.gc-skill-ownership.json\n/core.gc-work\n' \
//	  > $sk/.gitignore
//	git status --short   # empty: nested file beats !/.claude/skills/
//	git ls-tree HEAD $sk # gascity-docs still tracked
//
// The recipe deliberately avoids naming git's index-listing subcommand:
// scripts/test-push-gate-select.sh text-greps every tracked *.go for that
// string to find packages that scan the repository root, and a mention in
// this comment alone would conscript internal/materialize into
// scripts/push-gate-always-run.manifest on a false premise.
//
// Confirmed 2026-08-10 against git in this repository's own .gitignore,
// whose `!/.claude/skills/` negation the sink file has to override, and
// separately that an escaped `/weird\[1]\*\?name` matches that literal
// name while leaving a `weird1XYname` decoy visible.
//
// Run with:
//
//	go test ./internal/materialize/ -run SinkIgnore
package materialize

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// readSinkIgnore returns the generated file's non-comment lines, which are
// the patterns git would act on.
func readSinkIgnore(t *testing.T, sink string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sink, sinkIgnoreFile))
	if err != nil {
		t.Fatalf("reading sink ignore: %v", err)
	}
	if !strings.HasPrefix(string(data), sinkIgnoreHeader) {
		t.Fatalf("generated file lost its ownership header:\n%s", data)
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// TestSinkIgnoreListsOnlyWhatThePassMaterialized pins the property that
// keeps this file from silently un-tracking real content: gc ignores what
// gc wrote, and nothing else.
//
// Three distinct exclusions are checked together because each one is a
// separate way to get it wrong. A project skill committed to the sink on
// purpose (gascity-docs in this repository) is not materialized and must
// not appear. A desired name blocked by user content is skipped, so gc
// wrote nothing there and must not hide it. And every emitted pattern is
// anchored with a leading '/', without which it would match the same name
// at any depth below the sink.
func TestSinkIgnoreListsOnlyWhatThePassMaterialized(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	sink := filepath.Join(t.TempDir(), "skills")
	mkSkill(t, src, "core.gc-work")
	mkSkill(t, src, "gc.mayor")
	mkSkill(t, src, "contested")
	// A checked-in project skill, and user content occupying a desired name.
	mkSkill(t, sink, "gascity-docs")
	mkSkill(t, sink, "contested")

	res, err := Run(Request{
		SinkDir: sink,
		Desired: []SkillEntry{
			{Name: "core.gc-work", Source: filepath.Join(src, "core.gc-work"), Origin: "core"},
			{Name: "gc.mayor", Source: filepath.Join(src, "gc.mayor"), Origin: "gc"},
			{Name: "contested", Source: filepath.Join(src, "contested"), Origin: "core"},
		},
		OwnedRoots: []string{src},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Guard against a vacuous run: the skip must actually have happened,
	// otherwise the "contested is absent" assertion below proves nothing.
	if len(res.Skipped) != 1 || res.Skipped[0].Name != "contested" {
		t.Fatalf("expected contested to be skipped, got %+v", res.Skipped)
	}

	lines := readSinkIgnore(t, sink)
	for _, want := range []string{"/core.gc-work", "/gc.mayor"} {
		if !slices.Contains(lines, want) {
			t.Errorf("materialized name missing from ignore list: %s in %v", want, lines)
		}
	}
	for _, unwanted := range []string{"/contested", "/gascity-docs"} {
		if slices.Contains(lines, unwanted) {
			t.Errorf("gc ignored content it does not own: %s in %v", unwanted, lines)
		}
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "/") {
			t.Errorf("unanchored pattern %q would match at any depth under the sink", line)
		}
	}
}

// TestSinkIgnoreDropsNamesNoLongerMaterialized pins the file as a rewrite
// rather than an append.
//
// An append-only file rots in a way that stays invisible until it does
// damage: once gc stops materializing a name -- pack removed, binding
// renamed -- a stale entry keeps ignoring that path, so a project skill
// later added at the freed name never appears in git status. The second
// pass here drops a name and the entry has to go with it.
func TestSinkIgnoreDropsNamesNoLongerMaterialized(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	sink := filepath.Join(t.TempDir(), "skills")
	mkSkill(t, src, "core.gc-work")
	mkSkill(t, src, "retired.skill")

	both := []SkillEntry{
		{Name: "core.gc-work", Source: filepath.Join(src, "core.gc-work"), Origin: "core"},
		{Name: "retired.skill", Source: filepath.Join(src, "retired.skill"), Origin: "retired"},
	}
	if _, err := Run(Request{SinkDir: sink, Desired: both, OwnedRoots: []string{src}}); err != nil {
		t.Fatal(err)
	}
	if lines := readSinkIgnore(t, sink); !slices.Contains(lines, "/retired.skill") {
		t.Fatalf("first pass never listed the name, so its removal proves nothing: %v", lines)
	}

	if _, err := Run(Request{SinkDir: sink, Desired: both[:1], OwnedRoots: []string{src}}); err != nil {
		t.Fatal(err)
	}
	lines := readSinkIgnore(t, sink)
	if slices.Contains(lines, "/retired.skill") {
		t.Errorf("stale entry survived the pass that stopped materializing it: %v", lines)
	}
	if !slices.Contains(lines, "/core.gc-work") {
		t.Errorf("rewrite dropped a name that is still materialized: %v", lines)
	}
}

// TestSinkIgnoreLeavesUnmanagedFileAlone pins that a .gitignore gc did not
// author is never rewritten.
//
// Ownership is claimed by the generated header, matching how the rest of
// this package decides what it may touch (a symlink target under an owned
// root). A developer who put rules here gets a warning, not a clobber --
// and the warning is asserted because the alternative, a silent skip,
// leaves the sink reporting dirty with nothing to explain why.
func TestSinkIgnoreLeavesUnmanagedFileAlone(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	sink := filepath.Join(t.TempDir(), "skills")
	mkSkill(t, src, "core.gc-work")
	if err := os.MkdirAll(sink, 0o755); err != nil {
		t.Fatal(err)
	}
	const userContent = "# mine\n/scratch\n"
	path := filepath.Join(sink, sinkIgnoreFile)
	if err := os.WriteFile(path, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := Run(Request{
		SinkDir:    sink,
		Desired:    []SkillEntry{{Name: "core.gc-work", Source: filepath.Join(src, "core.gc-work"), Origin: "core"}},
		OwnedRoots: []string{src},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != userContent {
		t.Errorf("user .gitignore was rewritten:\n%s", got)
	}
	if !slices.ContainsFunc(res.Warnings, func(w string) bool {
		return strings.Contains(w, sinkIgnoreFile)
	}) {
		t.Errorf("silent skip -- expected a warning naming %s, got %v", sinkIgnoreFile, res.Warnings)
	}
	// The skill itself must still materialize: a claimed .gitignore is a
	// bookkeeping refusal, not a reason to stop doing the actual work.
	if !slices.Contains(res.Materialized, "core.gc-work") {
		t.Errorf("materialization stopped over a bookkeeping file: %+v", res)
	}
}

// TestSinkIgnoreEscapesGlobMetacharacters pins that a name is emitted as a
// literal path, not a pattern.
//
// git reads \*?[ in a .gitignore line as glob syntax, so an unescaped name
// containing one both fails to cover the entry it was emitted for and
// matches paths gc never wrote. ']' is deliberately NOT escaped -- it is
// literal outside a bracket expression -- which is the detail this
// assertion exists to hold still, since "escape every metacharacter" is
// the plausible wrong version. The file header records the git run that
// confirmed the emitted form matches the literal name and leaves a decoy
// alone.
func TestSinkIgnoreEscapesGlobMetacharacters(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	sink := filepath.Join(t.TempDir(), "skills")
	const odd = "weird[1]*?name"
	mkSkill(t, src, odd)

	if _, err := Run(Request{
		SinkDir:    sink,
		Desired:    []SkillEntry{{Name: odd, Source: filepath.Join(src, odd), Origin: "city"}},
		OwnedRoots: []string{src},
	}); err != nil {
		t.Fatal(err)
	}
	lines := readSinkIgnore(t, sink)
	const want = `/weird\[1]\*\?name`
	if !slices.Contains(lines, want) {
		t.Errorf("want literal-escaped %q, got %v", want, lines)
	}
}

// TestSinkIgnoreCoversItsOwnBookkeepingFiles pins the three entries that
// are not skill names: the file itself, the ownership manifest, and the
// tmp-symlink form atomicSymlink renames through.
//
// Each is a file gc writes into a directory git reports on, so each is
// working-tree noise of exactly the kind this change removes. The tmp
// entry is a glob because its suffix is random per call, and a crashed
// pass is the only way one survives -- a stranded one would otherwise read
// as a hand-placed file forever.
func TestSinkIgnoreCoversItsOwnBookkeepingFiles(t *testing.T) {
	t.Parallel()
	src := t.TempDir()
	sink := filepath.Join(t.TempDir(), "skills")
	mkSkill(t, src, "core.gc-work")

	if _, err := Run(Request{
		SinkDir:    sink,
		Desired:    []SkillEntry{{Name: "core.gc-work", Source: filepath.Join(src, "core.gc-work"), Origin: "core"}},
		OwnedRoots: []string{src},
	}); err != nil {
		t.Fatal(err)
	}
	// Without a manifest on disk the manifest assertion below would pass
	// while covering a file that never appears.
	if _, err := os.Stat(filepath.Join(sink, ownershipManifestFile)); err != nil {
		t.Fatalf("manifest absent, coverage assertion would be vacuous: %v", err)
	}
	lines := readSinkIgnore(t, sink)
	for _, want := range []string{"/" + sinkIgnoreFile, "/" + ownershipManifestFile, "/.*.tmp.*"} {
		if !slices.Contains(lines, want) {
			t.Errorf("bookkeeping path not covered: %s in %v", want, lines)
		}
	}
}

// TestSinkIgnoreNotCreatedWhenNothingMaterialized pins that gc does not
// drop a file into a sink it put nothing in. A pass that materializes
// nothing has nothing to declare, and an empty generated file in an
// otherwise untouched directory is itself the working-tree noise this
// change exists to remove.
func TestSinkIgnoreNotCreatedWhenNothingMaterialized(t *testing.T) {
	t.Parallel()
	sink := filepath.Join(t.TempDir(), "skills")
	if _, err := Run(Request{SinkDir: sink}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(sink, sinkIgnoreFile)); !os.IsNotExist(err) {
		t.Errorf("ignore file created for an empty pass (stat err = %v)", err)
	}
}
