package extmsg

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// Pins that every bead the extmsg fabric writes is gc bookkeeping rather than
// Ready work, on both the behavioral axis (drive the services, inspect what
// landed) and the source axis (every gc:extmsg- locator literal in this
// package is one beads.HasReadyExcludedLabel answers to).
//
// Why the suite exists: extmsg rows are type "task" with no assignee and no
// description, so nothing about their shape distinguishes them from work
// nobody has picked up. Until ci-fdr7cf a binding, a membership and a
// transcript-state row left behind by a deleted Slack adapter were counted by
// the city's unclaimable-work check as three claimable beads reaching no pool
// door -- permanent false positives padding the signal that exists to catch a
// real unroutable bead.
//
// beads.HasReadyExcludedLabel enumerates the family as literals rather than
// testing the gc:extmsg- prefix. The prefix test is what a future editor would
// reach for and it is the reason TestEveryExtmsgLocatorLabelIsReadyExcluded
// exists: internal/beads must stay a leaf (extmsg imports it, so it cannot
// import extmsg), which makes the literal list a second copy of a set this
// package owns. The source-axis test is what holds the copy in step -- a new
// gc:extmsg- family reddens it the moment the literal is written, before any
// bead of that family has ever been created.
//
// Run: go test ./internal/extmsg/ -run ReadyExclu

// extmsgLocatorPrefix is the shared prefix of every extmsg family locator
// label. Declared here and not in labels.go because it is the test's subject,
// not a value the fabric writes.
const extmsgLocatorPrefix = "gc:extmsg-"

// TestExtmsgFabricWritesNoReadyWork drives every extmsg service entry point
// that creates a bead and requires each resulting row to be ready-excluded.
//
// It asserts over the whole store rather than over a list of ids the calls
// returned: a service that mints a companion row nobody named -- the binding
// path creates a membership and a transcript-state row of its own, which is
// exactly how the ci-fdr7cf orphans arrived -- is then covered without the
// test having had to know about it.
func TestExtmsgFabricWritesNoReadyWork(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	fabric := NewServices(store)
	ref := testConversationRef()
	ctx := context.Background()

	sessionID := makeSessionBead(t, store, "gc-pl")

	binding, err := fabric.Bindings.Bind(ctx, testControllerCaller(), BindInput{
		Conversation: ref,
		SessionID:    sessionID,
		Now:          testNow(),
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if err := fabric.Delivery.Record(ctx, testControllerCaller(), DeliveryContextRecord{
		SessionID:         sessionID,
		Conversation:      ref,
		BindingGeneration: binding.BindingGeneration,
		LastPublishedAt:   testNow(),
		LastMessageID:     "msg-1",
	}); err != nil {
		t.Fatalf("Record(delivery): %v", err)
	}
	if _, err := fabric.Transcript.Append(ctx, AppendTranscriptInput{
		Caller:            testAdapterCaller(),
		Conversation:      ref,
		Kind:              TranscriptMessageInbound,
		Provenance:        TranscriptProvenanceLive,
		ProviderMessageID: "msg-1",
		Text:              "hello",
		CreatedAt:         testNow(),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	group, err := fabric.Groups.EnsureGroup(ctx, testControllerCaller(), EnsureGroupInput{
		RootConversation: ref,
		Mode:             GroupModeLauncher,
	})
	if err != nil {
		t.Fatalf("EnsureGroup: %v", err)
	}
	if _, err := fabric.Groups.UpsertParticipant(ctx, testControllerCaller(), UpsertParticipantInput{
		GroupID:   group.ID,
		Handle:    "alpha",
		SessionID: sessionID,
	}); err != nil {
		t.Fatalf("UpsertParticipant: %v", err)
	}

	rows, err := store.List(beads.ListQuery{AllowScan: true})
	if err != nil {
		t.Fatalf("list store: %v", err)
	}

	// The locator families actually observed, so a run that created nothing
	// cannot report agreement. Compared against a floor rather than an exact
	// set: which companion rows a call mints is the fabric's business, and
	// pinning the exact set here would make every such change a failure in a
	// suite that is not about them.
	families := map[string]bool{}
	for _, row := range rows {
		for _, label := range row.Labels {
			if strings.HasPrefix(label, extmsgLocatorPrefix) {
				families[label] = true
			}
		}
		if !beads.IsReadyExcludedBead(row) {
			t.Errorf("bead %s %q (type %q, labels %v) is Ready work; the extmsg fabric writes bookkeeping only",
				row.ID, row.Title, row.Type, row.Labels)
		}
	}
	if len(families) < 5 {
		t.Fatalf("observed extmsg locator families = %v, want at least 5; the drive above created almost nothing and the assertion above is vacuous",
			sortedKeys(families))
	}
}

// TestEveryExtmsgLocatorLabelIsReadyExcluded requires every gc:extmsg- string
// literal in this package's own source to be one beads.HasReadyExcludedLabel
// answers to.
//
// Reading the source is what makes this a gate rather than an allowlist. The
// alternative -- a hand-kept slice of the family names in this file -- agrees
// with beads.go by construction and rots at the next family added, which is
// the only failure either list has.
func TestEveryExtmsgLocatorLabelIsReadyExcluded(t *testing.T) {
	// Parsed file by file rather than with parser.ParseDir, which is
	// deprecated for ignoring build tags. Ignoring them is what this scan
	// wants -- a label written behind any tag is still a label the fabric can
	// write -- so the per-file walk keeps that behavior without the
	// deprecation, and go/packages would give the opposite one.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	labels := map[string]bool{}
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		ast.Inspect(file, func(node ast.Node) bool {
			lit, ok := node.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if strings.HasPrefix(value, extmsgLocatorPrefix) {
				labels[value] = true
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no non-test source files; the walk read nothing")
	}
	if len(labels) == 0 {
		t.Fatal("no gc:extmsg- literals found in this package's source; the scan read nothing and agrees with everything")
	}
	for _, label := range sortedKeys(labels) {
		if !beads.HasReadyExcludedLabel(beads.Bead{Labels: []string{label}}) {
			t.Errorf("label %q is written by this package but beads.HasReadyExcludedLabel does not hide it; add it to the switch in internal/beads/beads.go",
				label)
		}
	}
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for key := range set {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
