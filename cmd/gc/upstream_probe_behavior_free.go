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
)

// upstreamProbeBehavioralPaths returns the subset of paths whose change
// between base and HEAD in repoDir can alter behavior. Only two kinds of path
// are behavior-free: a .go file whose code tokens, directives and cgo
// preamble are unchanged, and Markdown under engdocs/ or docs/ that no
// //go:embed pattern at HEAD reaches. Anything unreadable counts as behavior.
func upstreamProbeBehavioralPaths(repoDir, base string, paths []string) []string {
	var behavioral []string
	var embeds []upstreamProbeEmbed
	var embedsErr error
	embedsRead := false
	for _, p := range paths {
		free := false
		switch {
		case strings.HasSuffix(p, ".go"):
			baseSrc, baseErr := upstreamProbeGitOutput(repoDir, "show", base+":"+p)
			headSrc, headErr := upstreamProbeGitOutput(repoDir, "show", "HEAD:"+p)
			free = baseErr == nil && headErr == nil &&
				upstreamProbeGoBehaviorFree([]byte(baseSrc), []byte(headSrc))
		case upstreamProbeDocsMarkdown(p):
			if !embedsRead {
				embeds, embedsErr = upstreamProbeEmbedPatterns(repoDir, "HEAD")
				embedsRead = true
			}
			free = embedsErr == nil && !upstreamProbeEmbedded(embeds, p)
		}
		if !free {
			behavioral = append(behavioral, p)
		}
	}
	return behavioral
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
