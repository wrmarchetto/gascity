package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// upstreamProbeClassifyPaths splits a branch's changed paths for the
// retirement experiment. Returns (kept, reverted, behavioral): kept are test
// material the experiment leaves in place, reverted is everything else, and
// behavioral the reverted paths whose change can alter behavior.
func upstreamProbeClassifyPaths(repoDir, base string, paths []string) (kept, reverted, behavioral []string) {
	embeds := &upstreamProbeEmbedIndex{repoDir: repoDir}
	for _, p := range paths {
		if upstreamProbeTestPath(p) || upstreamProbeSweepSpec(repoDir, base, p, embeds) {
			kept = append(kept, p)
			continue
		}
		reverted = append(reverted, p)
		if !upstreamProbeBehaviorFree(repoDir, base, p, embeds) {
			behavioral = append(behavioral, p)
		}
	}
	return kept, reverted, behavioral
}

// upstreamProbeSweepSpec reports whether p is a mutation-sweep spec, which
// only the sweep driver reads, so it is test material like a _test.go. That
// is a _sweep.toml or .mutation-sweep.toml with a [sweep] table holding
// [sweep.gate], read at HEAD or at base when the branch deleted it, and
// reached by no //go:embed at HEAD.
func upstreamProbeSweepSpec(repoDir, base, p string, embeds *upstreamProbeEmbedIndex) bool {
	if !strings.HasSuffix(p, "_sweep.toml") && !strings.HasSuffix(p, ".mutation-sweep.toml") {
		return false
	}
	rev := "HEAD"
	if !upstreamProbeGitOK(repoDir, "cat-file", "-e", "HEAD:"+p) {
		rev = base
	}
	src, err := upstreamProbeGitOutput(repoDir, "show", rev+":"+p)
	if err != nil {
		return false
	}
	var doc map[string]any
	if _, err := toml.Decode(src, &doc); err != nil {
		return false
	}
	sweep, ok := doc["sweep"].(map[string]any)
	if !ok {
		return false
	}
	if _, ok := sweep["gate"].(map[string]any); !ok {
		return false
	}
	return embeds.unembedded(p)
}

// upstreamProbeBehaviorFree reports whether p's change between base and HEAD
// cannot alter behavior. Only two kinds of path qualify: a .go file whose
// code tokens, directives and cgo preamble are unchanged, and Markdown under
// engdocs/ or docs/ that no //go:embed pattern at HEAD reaches. Anything
// unreadable counts as behavior.
func upstreamProbeBehaviorFree(repoDir, base, p string, embeds *upstreamProbeEmbedIndex) bool {
	switch {
	case strings.HasSuffix(p, ".go"):
		baseSrc, baseErr := upstreamProbeGitOutput(repoDir, "show", base+":"+p)
		headSrc, headErr := upstreamProbeGitOutput(repoDir, "show", "HEAD:"+p)
		return baseErr == nil && headErr == nil &&
			upstreamProbeGoBehaviorFree([]byte(baseSrc), []byte(headSrc))
	case upstreamProbeDocsMarkdown(p):
		return embeds.unembedded(p)
	}
	return false
}

// upstreamProbeEmbedIndex reads the //go:embed patterns at HEAD once, on
// first use.
type upstreamProbeEmbedIndex struct {
	repoDir string
	read    bool
	embeds  []upstreamProbeEmbed
	err     error
}

// unembedded reports whether no //go:embed at HEAD reaches file. It is false
// when any pattern could not be resolved, so nothing is called unembedded on
// a partial reading.
func (x *upstreamProbeEmbedIndex) unembedded(file string) bool {
	if !x.read {
		x.embeds, x.err = upstreamProbeEmbedPatterns(x.repoDir, "HEAD")
		x.read = true
	}
	return x.err == nil && !upstreamProbeEmbedded(x.embeds, file)
}

func upstreamProbeDocsMarkdown(p string) bool {
	return strings.HasSuffix(p, ".md") &&
		(strings.HasPrefix(p, "engdocs/") || strings.HasPrefix(p, "docs/"))
}

// upstreamProbeGoBehaviorFree reports whether two versions of a Go file
// differ only in comments that are neither directives nor cgo preamble.
// Source that does not scan or parse is never behavior-free.
func upstreamProbeGoBehaviorFree(base, head []byte) bool {
	baseStream, ok := upstreamProbeGoStream(base)
	if !ok {
		return false
	}
	headStream, ok := upstreamProbeGoStream(head)
	if !ok {
		return false
	}
	if len(baseStream) != len(headStream) {
		return false
	}
	for i := range baseStream {
		if baseStream[i] != headStream[i] {
			return false
		}
	}
	return true
}

type upstreamProbeGoToken struct {
	tok token.Token
	lit string
}

// upstreamProbeGoStream is the file's token stream with every comment
// dropped except directives and cgo preamble lines, which keep their place.
func upstreamProbeGoStream(src []byte) ([]upstreamProbeGoToken, bool) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		return nil, false
	}
	preamble := map[token.Pos]bool{}
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		for _, spec := range gen.Specs {
			imp, ok := spec.(*ast.ImportSpec)
			if !ok || imp.Path == nil {
				continue
			}
			if p, err := strconv.Unquote(imp.Path.Value); err != nil || p != "C" {
				continue
			}
			for _, group := range []*ast.CommentGroup{imp.Doc, gen.Doc} {
				if group == nil {
					continue
				}
				for _, c := range group.List {
					preamble[c.Slash] = true
				}
			}
		}
	}

	// A fresh FileSet gives the scanned file the parser's base, so a comment's
	// Pos means the same offset in both.
	file := token.NewFileSet().AddFile("", -1, len(src))
	// No error handler: the parse above already rejected any source the
	// scanner would.
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	var stream []upstreamProbeGoToken
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			if !upstreamProbeGoDirective(lit) && !preamble[pos] {
				continue
			}
		}
		stream = append(stream, upstreamProbeGoToken{tok: tok, lit: lit})
	}
	return stream, true
}

// upstreamProbeGoDirective covers the //go: family, //line, +build
// constraints and cgo's //export and gccgo's //extern (go/ast isDirective).
func upstreamProbeGoDirective(comment string) bool {
	for _, prefix := range []string{"//go:", "//line ", "/*line ", "//export ", "//extern "} {
		if strings.HasPrefix(comment, prefix) {
			return true
		}
	}
	return strings.HasPrefix(comment, "//") &&
		strings.HasPrefix(strings.TrimLeft(comment[2:], " \t"), "+build")
}

// upstreamProbeEmbed is one //go:embed pattern, relative to dir, the
// repository-relative directory of the package that declares it.
type upstreamProbeEmbed struct {
	dir     string
	pattern string
}

// upstreamProbeEmbedPatterns reads every //go:embed directive in the .go
// files of rev. Any file it cannot parse, or any pattern it cannot read, is
// an error, so no Markdown is called unembedded on a partial resolution.
func upstreamProbeEmbedPatterns(repoDir, rev string) ([]upstreamProbeEmbed, error) {
	out, err := upstreamProbeGitOutput(repoDir, "grep", "-l", "-z", "-F", "//go:embed", rev, "--", "*.go")
	if err != nil {
		// git grep exits 1, with no output, when nothing matches.
		if exitCode(err) == 1 {
			return nil, nil
		}
		return nil, err
	}
	var embeds []upstreamProbeEmbed
	for _, entry := range strings.Split(out, "\x00") {
		file := strings.TrimPrefix(entry, rev+":")
		if file == "" {
			continue
		}
		src, err := upstreamProbeGitOutput(repoDir, "show", rev+":"+file)
		if err != nil {
			return nil, err
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), file, src, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		dir := path.Dir(file)
		for _, group := range parsed.Comments {
			for _, c := range group.List {
				rest, ok := strings.CutPrefix(c.Text, "//go:embed")
				if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
					continue
				}
				patterns, err := upstreamProbeEmbedArgs(rest)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", file, err)
				}
				for _, pattern := range patterns {
					embeds = append(embeds, upstreamProbeEmbed{dir: dir, pattern: pattern})
				}
			}
		}
	}
	return embeds, nil
}

// upstreamProbeEmbedArgs splits a //go:embed argument list the way go/build
// does: space-separated, each optionally a Go double-quoted or raw string.
func upstreamProbeEmbedArgs(args string) ([]string, error) {
	var patterns []string
	for {
		args = strings.TrimLeft(args, " \t")
		if args == "" {
			break
		}
		var pattern string
		switch args[0] {
		case '"', '`':
			quoted, err := strconv.QuotedPrefix(args)
			if err != nil {
				return nil, fmt.Errorf("invalid quoted //go:embed pattern %s", args)
			}
			pattern, _ = strconv.Unquote(quoted)
			args = args[len(quoted):]
		default:
			end := strings.IndexAny(args, " \t")
			if end < 0 {
				end = len(args)
			}
			pattern, args = args[:end], args[end:]
		}
		if !fs.ValidPath(strings.TrimPrefix(pattern, "all:")) {
			return nil, fmt.Errorf("invalid //go:embed pattern %q", pattern)
		}
		if _, err := path.Match(strings.TrimPrefix(pattern, "all:"), ""); err != nil {
			return nil, fmt.Errorf("invalid //go:embed pattern %q: %w", pattern, err)
		}
		patterns = append(patterns, pattern)
	}
	if len(patterns) == 0 {
		return nil, errors.New("//go:embed with no pattern")
	}
	return patterns, nil
}

// upstreamProbeEmbedded reports whether file is reached by any embed: the
// pattern matches the file or one of its directories, which go:embed then
// embeds recursively. The hidden-file exclusion is ignored, so a file the
// all: prefix would be needed for still counts as embedded.
func upstreamProbeEmbedded(embeds []upstreamProbeEmbed, file string) bool {
	for _, e := range embeds {
		rel := file
		if e.dir != "." {
			var ok bool
			if rel, ok = strings.CutPrefix(file, e.dir+"/"); !ok {
				continue
			}
		}
		pattern := strings.TrimPrefix(e.pattern, "all:")
		for candidate := rel; ; {
			// Patterns were validated when read, so an error here is unreachable
			// and counts as embedded rather than as proof of absence.
			if matched, err := path.Match(pattern, candidate); err != nil || matched {
				return true
			}
			slash := strings.LastIndex(candidate, "/")
			if slash < 0 {
				break
			}
			candidate = candidate[:slash]
		}
	}
	return false
}
