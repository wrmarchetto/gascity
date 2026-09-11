package main

// Scope: the inbound external-message <system-reminder> must never instruct an
// agent to run a gc command the CLI cannot resolve.
//
// The suite exists because gs-77tn shipped exactly that. The reminder built in
// internal/api/handler_extmsg.go interpolates the conversation's provider name
// into "gc %s reply-current", as though a same-named CLI command existed for
// every provider. None ever has -- for any provider, in any ref: the token
// "reply-current" occurs in the tree only in the format string recommending
// it. The mayor ran the rendered command against a live Slack thread on
// 2026-09-08, got the usage banner, and the operator's question went
// unanswered.
//
// Why the gate lives here and not beside the handler: the only authority on
// what gc can run is the cobra tree that package main builds. It cannot be
// imported, so the reminder text travels the other way via
// api.ExtMsgNotifyReminderBranches.
//
// The gate deliberately drives a provider name nobody has ever registered.
// That is what makes it a statement about the INTERPOLATION rather than about
// today's provider list: a command path containing a free-form runtime string
// can never resolve, so any fix that keeps "gc <provider> ..." stays red no
// matter how many provider-named commands get added. A hand-kept list of
// provider names is how this shipped and would rot the same way again.
//
// Delegated elsewhere: whether the reminder's prose is the RIGHT advice is a
// design question this suite does not touch -- it judges resolvability only.
// Sanitization of the interpolated fields is pinned by
// internal/api/handler_extmsg_test.go.
//
// Run it with:
//
//	go test ./cmd/gc/ -run TestExtMsgNotifyReminder -count=1

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/api"
	"github.com/spf13/cobra"
)

// gcHintTokenTrim are the characters stripped from a scraped token before it
// is read as a command word, so a hint written as `gc status` or "gc status"
// is not missed on its punctuation.
const gcHintTokenTrim = "`'\"(),.:;"

// scrapeGCCommandPaths returns the command path of every "gc ..." invocation
// named anywhere in text, as token slices with flags and their values removed.
//
// It scans the whole block rather than the instruction lines alone: a hint may
// be added to any part of the reminder, and a scraper keyed on today's wording
// would silently stop seeing new ones. Callers therefore have to keep "gc" out
// of the interpolated free-text fields; api.ExtMsgNotifyReminderBranches does.
//
// Scanning stops at the first token starting with "-" because everything from
// there on is flags and their arguments, never command words.
func scrapeGCCommandPaths(text string) [][]string {
	var out [][]string
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		for i, field := range fields {
			if strings.Trim(field, gcHintTokenTrim) != "gc" {
				continue
			}
			var path []string
			for _, raw := range fields[i+1:] {
				word := strings.Trim(raw, gcHintTokenTrim)
				if word == "" || strings.HasPrefix(word, "-") {
					break
				}
				path = append(path, word)
			}
			if len(path) > 0 {
				out = append(out, path)
			}
		}
	}
	return out
}

// gcChildCommand returns parent's subcommand registered under name, matching
// aliases as well as canonical names because either resolves at the prompt.
func gcChildCommand(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name || child.HasAlias(name) {
			return child
		}
	}
	return nil
}

// resolveGCCommandPath walks path down the command tree and returns the
// deepest command reached with the number of tokens it consumed.
//
// Returning the count rather than a bool is what separates the two failures
// worth telling apart: zero consumed means the reminder named a command that
// does not exist, while a short walk over a runnable command is just the
// command's own positional arguments (gc mail send <agent>).
func resolveGCCommandPath(root *cobra.Command, path []string) (*cobra.Command, int) {
	current := root
	consumed := 0
	for _, name := range path {
		child := gcChildCommand(current, name)
		if child == nil {
			break
		}
		current = child
		consumed++
	}
	return current, consumed
}

// gcCommandPathResolves reports whether the CLI could actually run what the
// reminder named. Runnable() is required as well as a consumed token because
// a group node such as "gc extmsg" only prints help: telling an agent to run
// it wastes the same turn a missing command does.
func gcCommandPathResolves(root *cobra.Command, path []string) bool {
	resolved, consumed := resolveGCCommandPath(root, path)
	return consumed > 0 && resolved.Runnable()
}

// reminderGateProviders are the provider names the gate renders with.
//
// "acmechat" is the load-bearing entry and is not a typo: it is a provider
// that has never been registered anywhere, which is the whole point. Provider
// arrives as a free-form string on the inbound conversation ref, so a reminder
// naming "gc <provider> ..." is unsatisfiable in general, and an arbitrary
// name is the only input that says so. "slack" and "discord" are carried for
// the record -- slack is the provider gs-77tn was measured on, discord the
// value hardcoded before d7373594b generalized it.
//
// "Telegram" is capitalized to drive the strings.ToLower in the handler; a
// provider whose command word differs from its display word would otherwise
// be scraped from the display half of the sentence.
var reminderGateProviders = []string{"slack", "discord", "Telegram", "acmechat"}

// TestExtMsgNotifyReminderNamesOnlyResolvableCommands is the gs-77tn gate:
// every gc command the inbound reminder names must resolve to a runnable node
// of the production command tree.
//
// The tree is built with a zero-value rootCommandOptions, so pack-discovered
// commands are excluded. That is deliberate. The reminder is emitted by the
// SDK for every city, and a command that exists only because one city happens
// to ship commands/<name>/run.sh is not something the SDK may assume.
func TestExtMsgNotifyReminderNamesOnlyResolvableCommands(t *testing.T) {
	root := newRootCmdWithOptions(io.Discard, io.Discard, rootCommandOptions{})
	for _, provider := range reminderGateProviders {
		for _, rendered := range api.ExtMsgNotifyReminderBranches(provider, "C0C0JPH5E2Y", "mayor") {
			for _, path := range scrapeGCCommandPaths(rendered) {
				if !gcCommandPathResolves(root, path) {
					t.Errorf("provider %q: reminder names %q, which gc cannot run:\n%s",
						provider, "gc "+strings.Join(path, " "), rendered)
				}
			}
		}
	}
}

// TestExtMsgNotifyReminderCommandScraperIsObservable proves the gate above can
// fail at all.
//
// Without this, a scraper that returns nothing -- a changed hint layout, a
// trimmed token, a regexp that stopped matching -- leaves the gate green while
// the reminder says whatever it likes. The synthetic block carries one command
// the CLI really has and one it does not, so the test pins both directions
// against the same production command tree the gate uses.
func TestExtMsgNotifyReminderCommandScraperIsObservable(t *testing.T) {
	const synthetic = "before\n  gc status --json\ntext\n  gc acmechat reply-current --body-file <path>\nafter"

	paths := scrapeGCCommandPaths(synthetic)
	if len(paths) != 2 {
		t.Fatalf("scraper found %d command paths in the synthetic block, want 2: %v", len(paths), paths)
	}

	root := newRootCmdWithOptions(io.Discard, io.Discard, rootCommandOptions{})
	if !gcCommandPathResolves(root, paths[0]) {
		t.Errorf("%q must resolve; the gate cannot distinguish good hints from bad ones", paths[0])
	}
	if gcCommandPathResolves(root, paths[1]) {
		t.Errorf("%q must not resolve; the gate would pass the gs-77tn defect", paths[1])
	}
}

// TestExtMsgNotifyReminderGateSeesEveryCommandLiteral pins the gate's coverage
// of the formatter against the formatter's own source.
//
// api.ExtMsgNotifyReminderBranches enumerates the reminder's conditional
// branches by hand, and a hint added under a condition it does not drive would
// never be rendered, never be scraped, and never be gated -- the gate would
// stay green over the next gs-77tn. So both sides are derived from the same
// place the code is: the command literals inside formatExtmsgNotifyReminder's
// body, and the strings that function actually produces. Every literal must be
// rendered by some branch and every rendered command must come from some
// literal.
//
// Format verbs are matched as wildcards, since "gc %s reply-current" in source
// is "gc slack reply-current" once rendered. Comments cannot pollute the count:
// the literals are read from the AST, not by scanning the file.
func TestExtMsgNotifyReminderGateSeesEveryCommandLiteral(t *testing.T) {
	literals := formatterCommandLiterals(t)
	rendered := map[string]bool{}
	for _, body := range api.ExtMsgNotifyReminderBranches("acmechat", "C0C0JPH5E2Y", "mayor") {
		for _, path := range scrapeGCCommandPaths(body) {
			rendered[strings.Join(path, " ")] = true
		}
	}

	for _, literal := range literals {
		if !anyRenderedMatches(literal, rendered) {
			t.Errorf("formatExtmsgNotifyReminder names %q but no branch of "+
				"api.ExtMsgNotifyReminderBranches renders it, so the gate never sees it",
				"gc "+strings.Join(literal, " "))
		}
	}
	for path := range rendered {
		if !matchesAnyLiteral(strings.Split(path, " "), literals) {
			t.Errorf("branch rendered %q, which matches no command literal in "+
				"formatExtmsgNotifyReminder -- the scraper is reading interpolated text",
				"gc "+path)
		}
	}
}

// anyRenderedMatches reports whether some rendered command path matches the
// literal, treating format verbs in the literal as wildcards.
func anyRenderedMatches(literal []string, rendered map[string]bool) bool {
	for path := range rendered {
		if literalMatchesPath(literal, strings.Split(path, " ")) {
			return true
		}
	}
	return false
}

func matchesAnyLiteral(path []string, literals [][]string) bool {
	for _, literal := range literals {
		if literalMatchesPath(literal, path) {
			return true
		}
	}
	return false
}

// literalMatchesPath compares a source-literal command path against a rendered
// one. A literal token containing "%" is a format verb standing in for
// whatever got interpolated, so it matches any single rendered token.
func literalMatchesPath(literal, path []string) bool {
	if len(literal) != len(path) {
		return false
	}
	for i, want := range literal {
		if strings.Contains(want, "%") {
			continue
		}
		if want != path[i] {
			return false
		}
	}
	return true
}

// formatterCommandLiterals returns every gc command path written into a string
// literal inside formatExtmsgNotifyReminder.
//
// Adjacent literals joined by "+" are folded first. Go's concatenation is the
// reason: the reminder's format string is four literals in one "+" chain, and
// scraping them apart would truncate any command that straddles a break.
func formatterCommandLiterals(t *testing.T) [][]string {
	t.Helper()
	const (
		sourceFile = "internal/api/handler_extmsg.go"
		formatter  = "formatExtmsgNotifyReminder"
	)
	path := filepath.Join(repoRoot(t), sourceFile)
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", sourceFile, err)
	}

	var body *ast.BlockStmt
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == formatter && fn.Body != nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatalf("%s not found in %s -- the gate is pointed at the wrong function", formatter, sourceFile)
	}

	folded := map[ast.Node]bool{}
	var out [][]string
	seen := map[string]bool{}
	collect := func(text string) {
		for _, cmd := range scrapeGCCommandPaths(text) {
			key := strings.Join(cmd, " ")
			if !seen[key] {
				seen[key] = true
				out = append(out, cmd)
			}
		}
	}
	ast.Inspect(body, func(node ast.Node) bool {
		switch typed := node.(type) {
		case *ast.BinaryExpr:
			if typed.Op != token.ADD {
				return true
			}
			text, ok := foldStringConcat(typed, folded)
			if !ok {
				return true
			}
			collect(text)
			return false
		case *ast.BasicLit:
			if typed.Kind != token.STRING || folded[typed] {
				return true
			}
			if text, err := strconv.Unquote(typed.Value); err == nil {
				collect(text)
			}
		}
		return true
	})
	return out
}

// foldStringConcat joins a "+" chain of string literals into the single string
// the compiler would produce, recording each leaf in folded so the surrounding
// walk does not scrape it a second time. It reports false for any chain with a
// non-literal operand, leaving those to be walked normally.
func foldStringConcat(expr *ast.BinaryExpr, folded map[ast.Node]bool) (string, bool) {
	var leaves []*ast.BasicLit
	var walk func(ast.Expr) bool
	walk = func(node ast.Expr) bool {
		switch typed := node.(type) {
		case *ast.BinaryExpr:
			return typed.Op == token.ADD && walk(typed.X) && walk(typed.Y)
		case *ast.BasicLit:
			if typed.Kind != token.STRING {
				return false
			}
			leaves = append(leaves, typed)
			return true
		default:
			return false
		}
	}
	if !walk(expr) {
		return "", false
	}
	var joined strings.Builder
	for _, leaf := range leaves {
		text, err := strconv.Unquote(leaf.Value)
		if err != nil {
			return "", false
		}
		folded[leaf] = true
		joined.WriteString(text)
	}
	return joined.String(), true
}
