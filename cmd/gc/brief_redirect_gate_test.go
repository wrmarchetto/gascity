package main

import (
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Scope: the brief-redirect close gate (cmd/gc/brief_redirect_gate.go) and the
// claim-time stamp that arms it. The suite exists because the defect it closes
// is invisible to every other test in this tree: a session that read its bead
// at claim time and never re-read it closes happily against superseded
// instructions, and both ends -- the claim stamp and the close comparison --
// look correct in isolation. Rendering and store plumbing are delegated to the
// sibling close-gate suites; this one drives the predicate directly.
//
// Run: go test ./cmd/gc/ -run BriefRedirect

// briefRedirectBead builds an in-memory store holding one bead whose stamped
// claim-time digest is computed from origTitle/origDesc while the stored brief
// is title/desc. Passing the same pair for both is an unedited bead.
func briefRedirectBead(t *testing.T, origTitle, origDesc, title, desc string) beads.Store {
	t.Helper()
	return beads.NewMemStoreFrom(1, []beads.Bead{{
		ID:          "ci-brief",
		Type:        "task",
		Title:       title,
		Description: desc,
		Metadata: map[string]string{
			beadmeta.BriefDigestMetadataKey: beadBriefDigest(origTitle, origDesc),
		},
	}}, nil)
}

func TestBriefRedirectGateRefusesCloseAfterDescriptionEdit(t *testing.T) {
	// The measured case (ci-tdk1lv): the mayor appended a REDIRECT section to
	// the claimed bead's description and the holder closed gc.outcome=pass 31
	// minutes later. Refusing is only half the fix -- the refusal must carry
	// the superseding text, because a session mid-turn has no other way to
	// see it.
	const redirected = "build the thing\n\nREDIRECT: superseded, do NOT build this"
	store := briefRedirectBead(t, "attestation carrier", "build the thing",
		"attestation carrier", redirected)

	var stderr strings.Builder
	if !evaluateBriefRedirectCloseGate([]string{"close", "ci-brief"}, store, nil, &stderr) {
		t.Fatalf("close against an edited brief was accepted; stderr=%q", stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "REDIRECT: superseded, do NOT build this") {
		t.Fatalf("refusal does not print the current description, so the session never sees the redirect: %q", got)
	}
	if !strings.Contains(got, beadmeta.BriefDigestMetadataKey+"="+beadBriefDigest("attestation carrier", redirected)) {
		t.Fatalf("refusal does not name the acknowledgement command with the new digest: %q", got)
	}
}

func TestBriefRedirectGateRefusesCloseAfterTitleEdit(t *testing.T) {
	// The digest covers the title as well as the description. A title is
	// instruction-bearing -- "add X" retitled "remove X" is a redirect -- and
	// a digest over the description alone would pass that through silently.
	store := briefRedirectBead(t, "add the flag", "same body", "remove the flag", "same body")

	var stderr strings.Builder
	if !evaluateBriefRedirectCloseGate([]string{"close", "ci-brief"}, store, nil, &stderr) {
		t.Fatalf("close against an edited title was accepted; stderr=%q", stderr.String())
	}
	if got := stderr.String(); !strings.Contains(got, "remove the flag") {
		t.Fatalf("refusal does not print the current title: %q", got)
	}
}

func TestBriefRedirectGateAcceptsUneditedBrief(t *testing.T) {
	store := briefRedirectBead(t, "title", "body", "title", "body")

	var stderr strings.Builder
	if evaluateBriefRedirectCloseGate([]string{"close", "ci-brief"}, store, nil, &stderr) {
		t.Fatalf("close against an unchanged brief was refused: %s", stderr.String())
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("unchanged brief produced output: %q", got)
	}
}

func TestBriefRedirectGateIgnoresBeadWithNoStampedDigest(t *testing.T) {
	// A bead claimed before this gate shipped, or created and closed without
	// ever passing through the hook claim, carries no digest. It must close
	// exactly as before: this is what keeps the gate from needing a migration
	// or a warn-only period.
	store := beads.NewMemStoreFrom(1, []beads.Bead{{
		ID: "ci-brief", Type: "task", Title: "t", Description: "d",
	}}, nil)

	var stderr strings.Builder
	if evaluateBriefRedirectCloseGate([]string{"close", "ci-brief"}, store, nil, &stderr) {
		t.Fatalf("unstamped bead was refused: %s", stderr.String())
	}
}

func TestBriefRedirectGateAcceptsAcknowledgedDigestInTheSameUpdateClose(t *testing.T) {
	// The acknowledgement is a metadata write, and the documented worker close
	// form stamps metadata and closes in one `bd update`. Projecting that
	// update before comparing is what lets the acknowledgement land in the
	// same command instead of demanding a second round trip.
	const desc = "build the thing\n\nREDIRECT: build the other thing"
	store := briefRedirectBead(t, "t", "build the thing", "t", desc)
	ack := beadmeta.BriefDigestMetadataKey + "=" + beadBriefDigest("t", desc)

	var stderr strings.Builder
	args := []string{"update", "ci-brief", "--set-metadata", ack, "--status=closed"}
	if evaluateBriefRedirectCloseGate(args, store, nil, &stderr) {
		t.Fatalf("acknowledged close was refused: %s", stderr.String())
	}
}

func TestBriefRedirectGateRefusesTheUpdateStatusClosedForm(t *testing.T) {
	// `bd update --status=closed` is a close. A gate wired only to the `close`
	// verb would be bypassed by the form the worker formulas actually use.
	store := briefRedirectBead(t, "t", "old", "t", "new")

	var stderr strings.Builder
	args := []string{"update", "ci-brief", "--status=closed"}
	if !evaluateBriefRedirectCloseGate(args, store, nil, &stderr) {
		t.Fatalf("update --status=closed against an edited brief was accepted; stderr=%q", stderr.String())
	}
}

func TestBriefRedirectGateIgnoresNonCloseWrites(t *testing.T) {
	// An ordinary update must not be gated: a worker stamping gc.work_commit
	// mid-flight has not yet claimed the work is finished, and refusing there
	// would block the very metadata the other close gates require.
	store := briefRedirectBead(t, "t", "old", "t", "new")

	var stderr strings.Builder
	args := []string{"update", "ci-brief", "--set-metadata", "gc.work_outcome=shipped"}
	if evaluateBriefRedirectCloseGate(args, store, nil, &stderr) {
		t.Fatalf("non-close update was refused: %s", stderr.String())
	}
}

func TestBeadBriefDigestFramesTheTitleLength(t *testing.T) {
	// Concatenating title and description with a separator collides whenever
	// the boundary moves across a separator the title itself contains: with a
	// bare "\n" join, title "a\nb"/desc "c" and title "a"/desc "b\nc" hash
	// alike, and a mayor moving a line from title to description would slip
	// through. Length framing removes the collision.
	if beadBriefDigest("a\nb", "c") == beadBriefDigest("a", "b\nc") {
		t.Fatal("digest collides across a title/description boundary shift")
	}
}

func TestBriefRedirectRefusalTruncatesAnOversizeBrief(t *testing.T) {
	// The refusal prints the brief into the worker's context, so it needs a
	// bound. The truncation marker must name the command that shows the rest;
	// a silent cut would leave a redirect sentence invisible with nothing on
	// screen saying so.
	huge := strings.Repeat("x", briefRedirectPrintLimit+500)
	store := briefRedirectBead(t, "t", "old", "t", huge)

	var stderr strings.Builder
	if !evaluateBriefRedirectCloseGate([]string{"close", "ci-brief"}, store, nil, &stderr) {
		t.Fatalf("oversize edited brief was accepted; stderr=%q", stderr.String())
	}
	got := stderr.String()
	if len(got) > briefRedirectPrintLimit+2000 {
		t.Fatalf("refusal is unbounded at %d bytes", len(got))
	}
	if !strings.Contains(got, "gc bd show ci-brief") {
		t.Fatalf("truncated refusal does not point at the full brief: %q", got)
	}
}
