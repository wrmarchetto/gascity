package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

// TestUpstreamProbeBehavioralPaths classifies real git paths. The branch
// writes each changed path, and embeds are the //go:embed lines at HEAD.
func TestUpstreamProbeBehavioralPaths(t *testing.T) {
	const goBase = "package p\n\n// Doc a.\nfunc F() {}\n"
	const goComment = "package p\n\n// Doc b.\nfunc F() {}\n"
	for _, tc := range []struct {
		name   string
		base   map[string]string // committed on the base branch
		branch map[string]string // committed on top of base
		want   []string          // behavioral subset of branch paths
	}{
		{
			name:   "unembedded-engdocs-and-docs-markdown-is-behavior-free",
			base:   map[string]string{"engdocs/a.md": "a\n", "docs/b.md": "b\n"},
			branch: map[string]string{"engdocs/a.md": "a2\n", "docs/b.md": "b2\n", "docs/new.md": "n\n"},
		},
		{
			name:   "markdown-outside-engdocs-and-docs-is-behavioral",
			branch: map[string]string{"README.md": "r\n", "internal/x/docs/a.md": "a\n"},
			want:   []string{"README.md", "internal/x/docs/a.md"},
		},
		{
			name:   "other-extension-under-docs-is-behavioral",
			branch: map[string]string{"docs/a.txt": "a\n", "engdocs/spec.toml": "a = 1\n"},
			want:   []string{"docs/a.txt", "engdocs/spec.toml"},
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
			// The bad pattern's package cannot reach engdocs/, so only the
			// refusal to resolve partially makes the Markdown behavioral.
			name:   "unresolvable-embed-pattern-makes-every-markdown-behavioral",
			base:   map[string]string{"internal/x/embed.go": "package x\n\nimport \"embed\"\n\n//go:embed other/[\nvar F embed.FS\n"},
			branch: map[string]string{"engdocs/a.md": "a\n"},
			want:   []string{"engdocs/a.md"},
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
			name:   "added-go-file-is-behavioral-even-if-comment-only",
			branch: map[string]string{"x.go": goComment},
			want:   []string{"x.go"},
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
			commit(tc.branch, "branch")

			var paths []string
			for path := range tc.branch {
				paths = append(paths, path)
			}
			slices.Sort(paths)
			got := upstreamProbeBehavioralPaths(repoDir, "base", paths)
			want := tc.want
			if want == nil {
				want = []string{}
			}
			if got == nil {
				got = []string{}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("behavioral = %q, want %q", got, want)
			}
		})
	}
}
