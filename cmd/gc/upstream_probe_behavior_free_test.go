package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestUpstreamProbeGoBehaviorFree(t *testing.T) {
	const body = "package p\n\n// Doc says one thing.\nfunc F() int { return 1 }\n"
	for _, tc := range []struct {
		name       string
		base, head string
		want       bool
	}{
		{
			name: "comment-only-change-is-behavior-free",
			base: body,
			head: strings.Replace(body, "one thing", "another thing", 1),
			want: true,
		},
		{
			name: "block-and-trailing-comment-change-is-behavior-free",
			base: "package p\n\nfunc F() int { return 1 /* a */ } // x\n",
			head: "package p\n\nfunc F() int { return 1 /* b */ } // y\n",
			want: true,
		},
		{
			name: "token-change-is-behavioral",
			base: body,
			head: strings.Replace(body, "return 1", "return 2", 1),
		},
		{
			name: "go-build-text-change-is-behavioral",
			base: "//go:build linux\n\npackage p\n",
			head: "//go:build darwin\n\npackage p\n",
		},
		{
			name: "plus-build-text-change-is-behavioral",
			base: "// +build linux\n\npackage p\n",
			head: "// +build darwin\n\npackage p\n",
		},
		{
			name: "deleting-blank-line-after-plus-build-is-behavioral",
			base: "// +build ignore\n\npackage p\n",
			head: "// +build ignore\npackage p\n",
		},
		{
			name: "inserting-blank-line-after-plus-build-is-behavioral",
			base: "// +build ignore\n// Package p is p.\npackage p\n",
			head: "// +build ignore\n\n// Package p is p.\npackage p\n",
		},
		{
			name: "comment-change-after-package-clause-of-plus-build-file-is-behavior-free",
			base: "// +build ignore\n\npackage p\n\n// F does a.\nfunc F() {}\n",
			head: "// +build ignore\n\npackage p\n\n// F does b.\nfunc F() {}\n",
			want: true,
		},
		{
			name: "header-comment-change-without-plus-build-is-behavior-free",
			base: "//go:build ignore\n\n// Package p does a.\npackage p\n",
			head: "//go:build ignore\n// Package p does b.\npackage p\n",
			want: true,
		},
		{
			name: "go-embed-text-change-is-behavioral",
			base: "package p\n\nimport _ \"embed\"\n\n//go:embed a.txt\nvar s string\n",
			head: "package p\n\nimport _ \"embed\"\n\n//go:embed b.txt\nvar s string\n",
		},
		{
			name: "go-generate-text-change-is-behavioral",
			base: "package p\n\n//go:generate stringer -type=A\n",
			head: "package p\n\n//go:generate stringer -type=B\n",
		},
		{
			name: "go-linkname-text-change-is-behavioral",
			base: "package p\n\nimport _ \"unsafe\"\n\n//go:linkname f runtime.a\nfunc f()\n",
			head: "package p\n\nimport _ \"unsafe\"\n\n//go:linkname f runtime.b\nfunc f()\n",
		},
		{
			name: "other-go-directive-change-is-behavioral",
			base: "package p\n\n// F is inlined.\nfunc F() {}\n",
			head: "package p\n\n//go:noinline\nfunc F() {}\n",
		},
		{
			name: "line-directive-change-is-behavioral",
			base: "package p\n\n//line a.go:10\nfunc F() {}\n",
			head: "package p\n\n//line a.go:20\nfunc F() {}\n",
		},
		{
			name: "block-line-directive-change-is-behavioral",
			base: "package p\n\n/*line a.go:10*/ func F() {}\n",
			head: "package p\n\n/*line a.go:20*/ func F() {}\n",
		},
		{
			name: "cgo-export-change-is-behavioral",
			base: "package p\n\nimport \"C\"\n\n//export F\nfunc F() {}\n",
			head: "package p\n\nimport \"C\"\n\n//export G\nfunc F() {}\n",
		},
		{
			name: "cgo-preamble-comment-change-is-behavioral",
			base: "package p\n\n// #include <stdio.h>\nimport \"C\"\n",
			head: "package p\n\n// #include <stdlib.h>\nimport \"C\"\n",
		},
		{
			name: "grouped-cgo-preamble-comment-change-is-behavioral",
			base: "package p\n\nimport (\n\t// #define N 1\n\t\"C\"\n)\n",
			head: "package p\n\nimport (\n\t// #define N 2\n\t\"C\"\n)\n",
		},
		{
			name: "detaching-cgo-preamble-is-behavioral",
			base: "package p\n\n// #define N 1\nimport \"C\"\n",
			head: "package p\n\n// #define N 1\n\nimport \"C\"\n",
		},
		{
			name: "comment-beside-cgo-import-but-not-preamble-is-behavior-free",
			base: "package p\n\n// #define N 1\nimport \"C\"\n\n// F does a.\nfunc F() {}\n",
			head: "package p\n\n// #define N 1\nimport \"C\"\n\n// F does b.\nfunc F() {}\n",
			want: true,
		},
		{
			name: "unparseable-source-is-behavioral",
			base: "package p\n\n// a\nfunc F( {\n",
			head: "package p\n\n// b\nfunc F( {\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamProbeGoBehaviorFree([]byte(tc.base), []byte(tc.head)); got != tc.want {
				t.Fatalf("upstreamProbeGoBehaviorFree = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestUpstreamProbeClassifyPaths classifies real git paths through
// `gc internal upstream-classify --head branch`. A later commit rewrites
// every path and embeds them all, so a HEAD read flips the answer.
func TestUpstreamProbeClassifyPaths(t *testing.T) {
	const goBase = "package p\n\n// Doc a.\nfunc F() {}\n"
	const goComment = "package p\n\n// Doc b.\nfunc F() {}\n"
	const spec = "[sweep]\nname = \"s\"\n\n[sweep.gate]\nargv = [\"go\", \"test\"]\n"
	const embedCmd = "package main\n\nimport \"embed\"\n\n//go:embed *.toml\nvar F embed.FS\n"
	for _, tc := range []struct {
		name    string
		base    map[string]string // committed on the base branch
		branch  map[string]string // committed on top of base
		deleted []string          // removed on top of base
		kept    []string          // changed paths kept as test material
		tests   []string          // subset of kept that are tests, not sweep specs
		want    []string          // behavioral subset of the reverted paths
	}{
		{
			name:   "sweep-spec-with-sweep-and-gate-tables-is-kept-but-not-a-test",
			branch: map[string]string{"cmd/gc/pool_sweep.toml": spec},
			kept:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:   "mutation-sweep-spec-is-kept-but-not-a-test",
			branch: map[string]string{"scripts/gate.mutation-sweep.toml": spec},
			kept:   []string{"scripts/gate.mutation-sweep.toml"},
		},
		{
			name:   "sweep-spec-name-without-sweep-table-is-behavioral",
			branch: map[string]string{"cmd/gc/pool_sweep.toml": "[gate]\nargv = [\"go\"]\n"},
			want:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:   "sweep-table-without-gate-table-is-behavioral",
			branch: map[string]string{"cmd/gc/pool_sweep.toml": "[sweep]\nname = \"s\"\n"},
			want:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:   "sweep-content-under-another-toml-name-is-behavioral",
			branch: map[string]string{"cmd/gc/pool.toml": spec, "cmd/gc/sweep.toml": spec},
			want:   []string{"cmd/gc/pool.toml", "cmd/gc/sweep.toml"},
		},
		{
			name:   "unparseable-sweep-spec-is-behavioral",
			branch: map[string]string{"cmd/gc/pool_sweep.toml": spec + "[sweep\n"},
			want:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:   "embedded-sweep-spec-is-behavioral",
			base:   map[string]string{"cmd/gc/embed.go": embedCmd},
			branch: map[string]string{"cmd/gc/pool_sweep.toml": spec},
			want:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:   "sweep-spec-is-behavioral-when-embeds-cannot-be-resolved",
			base:   map[string]string{"internal/x/embed.go": "package x\n\nimport \"embed\"\n\n//go:embed other/[\nvar F embed.FS\n"},
			branch: map[string]string{"cmd/gc/pool_sweep.toml": spec},
			want:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:    "deleted-sweep-spec-counts-by-its-base-version",
			base:    map[string]string{"cmd/gc/pool_sweep.toml": spec, "cmd/gc/old_sweep.toml": "[gate]\n"},
			deleted: []string{"cmd/gc/old_sweep.toml", "cmd/gc/pool_sweep.toml"},
			kept:    []string{"cmd/gc/pool_sweep.toml"},
			want:    []string{"cmd/gc/old_sweep.toml"},
		},
		{
			name:   "present-sweep-spec-is-judged-at-head-not-base",
			base:   map[string]string{"cmd/gc/pool_sweep.toml": spec, "cmd/gc/new_sweep.toml": "[gate]\n"},
			branch: map[string]string{"cmd/gc/pool_sweep.toml": "[gate]\n", "cmd/gc/new_sweep.toml": spec},
			kept:   []string{"cmd/gc/new_sweep.toml"},
			want:   []string{"cmd/gc/pool_sweep.toml"},
		},
		{
			name:   "named-test-paths-are-kept",
			branch: map[string]string{"x_test.go": "package p\n", "testdata/a.txt": "a\n"},
			kept:   []string{"testdata/a.txt", "x_test.go"},
			tests:  []string{"testdata/a.txt", "x_test.go"},
		},
		{
			name: "unembedded-file-of-any-extension-under-engdocs-and-docs-is-behavior-free",
			base: map[string]string{
				"engdocs/a.md":                           "a\n",
				"docs/b.md":                              "b\n",
				"docs/reference/schema/city-schema.json": "{}\n",
			},
			branch: map[string]string{
				"engdocs/a.md":                           "a2\n",
				"docs/b.md":                              "b2\n",
				"docs/new.md":                            "n\n",
				"docs/reference/schema/city-schema.json": "{\"a\": 1}\n",
				"docs/reference/schema/city-schema.txt":  "a\n",
				"engdocs/spec.toml":                      "a = 1\n",
			},
		},
		{
			name:   "file-outside-engdocs-and-docs-is-behavioral",
			branch: map[string]string{"README.md": "r\n", "internal/x/docs/a.md": "a\n", "internal/x/docs/a.json": "{}\n"},
			want:   []string{"README.md", "internal/x/docs/a.json", "internal/x/docs/a.md"},
		},
		{
			name: "docs-file-matched-by-embed-pattern-is-behavioral-whatever-its-extension",
			base: map[string]string{"embed.go": "package p\n\nimport \"embed\"\n\n//go:embed docs/reference/schema/*.json\nvar F embed.FS\n"},
			branch: map[string]string{
				"docs/reference/schema/city-schema.json": "{}\n",
				"docs/reference/schema/city-schema.txt":  "a\n",
			},
			want: []string{"docs/reference/schema/city-schema.json"},
		},
		{
			name: "markdown-matched-by-file-pattern-is-behavioral",
			base: map[string]string{"embed.go": "package p\n\nimport \"embed\"\n\n//go:embed docs/*.md\nvar F embed.FS\n"},
			branch: map[string]string{
				"docs/a.md":     "a\n",
				"docs/sub/b.md": "b\n",
			},
			want: []string{"docs/a.md"},
		},
		{
			name:   "markdown-matched-by-directory-pattern-is-behavioral",
			base:   map[string]string{"embed.go": "package p\n\nimport \"embed\"\n\n//go:embed engdocs\nvar F embed.FS\n"},
			branch: map[string]string{"engdocs/arch/a.md": "a\n"},
			want:   []string{"engdocs/arch/a.md"},
		},
		{
			name:   "markdown-matched-by-all-prefix-is-behavioral",
			base:   map[string]string{"embed.go": "package p\n\nimport \"embed\"\n\n//go:embed \"all:docs\"\nvar F embed.FS\n"},
			branch: map[string]string{"docs/_hidden/a.md": "a\n"},
			want:   []string{"docs/_hidden/a.md"},
		},
		{
			name:   "markdown-matched-relative-to-embedding-package-is-behavioral",
			base:   map[string]string{"docs/pkg/embed.go": "package pkg\n\nimport \"embed\"\n\n//go:embed *.md\nvar F embed.FS\n"},
			branch: map[string]string{"docs/pkg/a.md": "a\n", "docs/a.md": "a\n"},
			want:   []string{"docs/pkg/a.md"},
		},
		{
			name: "embed-added-by-the-branch-itself-is-behavioral",
			branch: map[string]string{
				"embed.go":  "package p\n\nimport \"embed\"\n\n//go:embed docs\nvar F embed.FS\n",
				"docs/a.md": "a\n",
			},
			want: []string{"docs/a.md", "embed.go"},
		},
		{
			// The bad pattern's package cannot reach engdocs/ or y.go, so only
			// the refusal to resolve partially makes either behavioral.
			name:   "unresolvable-embed-pattern-makes-markdown-and-go-comments-behavioral",
			base:   map[string]string{"internal/x/embed.go": "package x\n\nimport \"embed\"\n\n//go:embed other/[\nvar F embed.FS\n", "y.go": goBase},
			branch: map[string]string{"engdocs/a.md": "a\n", "y.go": goComment},
			want:   []string{"engdocs/a.md", "y.go"},
		},
		{
			name:   "embed-text-inside-a-string-literal-is-not-a-pattern",
			base:   map[string]string{"fixture_test.go": "package p\n\nconst s = `\n//go:embed engdocs\n`\n"},
			branch: map[string]string{"engdocs/a.md": "a\n"},
		},
		{
			name:   "comment-only-go-change-is-behavior-free",
			base:   map[string]string{"x.go": goBase},
			branch: map[string]string{"x.go": goComment},
		},
		{
			// The shape of internal/bootstrap: a directory embed reaching a
			// pack's Go file, whose bytes are shipped content.
			name: "comment-only-go-change-reached-by-embed-is-behavioral",
			base: map[string]string{
				"internal/b/b.go":                "package b\n\nimport \"embed\"\n\n//go:embed packs/**\nvar F embed.FS\n",
				"internal/b/packs/core/embed.go": goBase,
				"internal/b/x.go":                goBase,
			},
			branch: map[string]string{
				"internal/b/packs/core/embed.go": goComment,
				"internal/b/x.go":                goComment,
			},
			want: []string{"internal/b/packs/core/embed.go"},
		},
		{
			// Under docs/ too: Go source is judged as Go before as docs.
			name:   "added-go-file-is-behavioral-even-if-comment-only",
			branch: map[string]string{"x.go": goComment, "docs/x.go": goComment},
			want:   []string{"docs/x.go", "x.go"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoDir := t.TempDir()
			runUpstreamProbeCloseGateGit(t, repoDir, "init", "-q", "-b", "main")
			runUpstreamProbeCloseGateGit(t, repoDir, "config", "user.email", "fixture@example.invalid")
			runUpstreamProbeCloseGateGit(t, repoDir, "config", "user.name", "fixture")
			commit := func(files map[string]string, msg string) {
				t.Helper()
				for path, content := range files {
					full := filepath.Join(repoDir, path)
					if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				runUpstreamProbeCloseGateGit(t, repoDir, "add", "-A")
				runUpstreamProbeCloseGateGit(t, repoDir, "commit", "-q", "--allow-empty", "-m", msg)
			}
			commit(tc.base, "base")
			runUpstreamProbeCloseGateGit(t, repoDir, "tag", "base")
			for _, path := range tc.deleted {
				if err := os.Remove(filepath.Join(repoDir, path)); err != nil {
					t.Fatal(err)
				}
			}
			commit(tc.branch, "branch")
			runUpstreamProbeCloseGateGit(t, repoDir, "tag", "branch")

			paths := slices.Clone(tc.deleted)
			for path := range tc.branch {
				paths = append(paths, path)
			}
			slices.Sort(paths)
			later := map[string]string{}
			var patterns []string
			for _, path := range paths {
				later[path] = "later\n"
				if body, ok := tc.branch[path]; ok && strings.HasSuffix(path, ".go") {
					later[path] = body + "\nvar later = 1\n"
				}
				top, _, _ := strings.Cut(path, "/")
				if !slices.Contains(patterns, top) {
					patterns = append(patterns, top)
				}
			}
			later["zz_later.go"] = "package p\n\nimport \"embed\"\n\n//go:embed " + strings.Join(patterns, " ") + "\nvar Later embed.FS\n"
			commit(later, "later")

			kept, tests, reverted, behavioral := runUpstreamClassify(t, 0, append([]string{"--repo", repoDir, "--base", "base", "--head", "branch"}, paths...))
			wantReverted := []string{}
			for _, path := range paths {
				if !slices.Contains(tc.kept, path) {
					wantReverted = append(wantReverted, path)
				}
			}
			for _, check := range []struct {
				label     string
				got, want []string
			}{
				{"kept", kept, tc.kept},
				{"tests", tests, tc.tests},
				{"reverted", reverted, wantReverted},
				{"behavioral", behavioral, tc.want},
			} {
				if !slices.Equal(nonNilStrings(check.got), nonNilStrings(check.want)) {
					t.Errorf("%s = %q, want %q", check.label, check.got, check.want)
				}
			}
		})
	}

	repoDir := t.TempDir()
	runUpstreamProbeCloseGateGit(t, repoDir, "init", "-q", "-b", "main")
	runUpstreamProbeCloseGateGit(t, repoDir, "-c", "user.email=f@example.invalid", "-c", "user.name=f", "commit", "-q", "--allow-empty", "-m", "base")
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"missing-repo-refused", []string{"--base", "HEAD", "--head", "HEAD"}},
		{"missing-base-refused", []string{"--repo", repoDir, "--head", "HEAD"}},
		{"missing-head-refused", []string{"--repo", repoDir, "--base", "HEAD"}},
		{"unresolvable-base-refused", []string{"--repo", repoDir, "--base", "nope", "--head", "HEAD"}},
		{"unresolvable-head-refused", []string{"--repo", repoDir, "--base", "HEAD", "--head", "nope"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runUpstreamClassify(t, 1, tc.args)
		})
	}

	// Content selects the census, so a spec the name rule misses is named
	// here rather than reverted by the experiment.
	t.Run("every-tracked-sweep-spec-carries-a-spec-name", func(t *testing.T) {
		const root = "../.."
		out, err := upstreamProbeGitOutput(root, "ls-files", "-z", "--", "*.toml")
		if err != nil {
			t.Fatalf("listing tracked TOML in %s: %v", root, err)
		}
		var specs []string
		for _, rel := range strings.FieldsFunc(out, func(r rune) bool { return r == 0 }) {
			var doc map[string]any
			if _, err := toml.DecodeFile(filepath.Join(root, rel), &doc); err != nil {
				t.Fatalf("census cannot read %s: %v", rel, err)
			}
			if sweep, ok := doc["sweep"].(map[string]any); ok {
				if _, ok := sweep["gate"].(map[string]any); ok {
					specs = append(specs, rel)
				}
			}
		}
		if !slices.Contains(specs, "cmd/gc/upstream_probe_sweep_spec_sweep.toml") {
			t.Fatalf("census found %q, which lacks this test's own sweep spec", specs)
		}
		for _, rel := range specs {
			if !upstreamProbeSweepSpecName(rel) {
				t.Errorf("%s holds [sweep.gate] but fails the sweep spec name rule; "+
					"rename it to end in .mutation-sweep.toml or _sweep.toml", rel)
			}
		}
	})
}

// runUpstreamClassify runs the subcommand, failing unless it exits wantCode,
// and returns (kept, tests, reverted, behavioral). Every list must be present.
func runUpstreamClassify(t *testing.T, wantCode int, args []string) (kept, tests, reverted, behavioral []string) {
	t.Helper()
	clearGCEnv(t)
	t.Setenv("GC_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run(append([]string{"internal", "upstream-classify"}, args...), &stdout, &stderr); code != wantCode {
		t.Fatalf("exit %d, want %d: stderr=%q stdout=%q", code, wantCode, stderr.String(), stdout.String())
	}
	if wantCode != 0 {
		if strings.TrimSpace(stderr.String()) == "" || stdout.Len() != 0 {
			t.Fatalf("refusal must explain itself on stderr only: stderr=%q stdout=%q", stderr.String(), stdout.String())
		}
		return nil, nil, nil, nil
	}
	var doc struct {
		Kept       *[]string `json:"kept"`
		Tests      *[]string `json:"tests"`
		Reverted   *[]string `json:"reverted"`
		Behavioral *[]string `json:"behavioral"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil || doc.Kept == nil || doc.Tests == nil || doc.Reverted == nil || doc.Behavioral == nil {
		t.Fatalf("stdout lacks a kept, tests, reverted or behavioral list: %v %q", err, stdout.String())
	}
	return *doc.Kept, *doc.Tests, *doc.Reverted, *doc.Behavioral
}
