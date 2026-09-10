package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Brief-redirect close gate (ci-tdk1lv). A worker reads its bead once, at claim
// time, and then works for tens of minutes without reading it again. When the
// mayor corrects the job mid-flight -- by editing the claimed bead's own title
// or description, the one channel guaranteed to be about the work in hand --
// nothing puts that edit in front of the worker, and it closes gc.outcome=pass
// against instructions the operator has explicitly withdrawn. Measured
// 2026-09-09: the redirect landed at 22:06:58Z and the holder closed at
// 22:38:08Z, having called bd on that same bead in between.
//
// The gate turns the close itself into the delivery point. gc stamps a digest
// of the brief at claim (beadmeta.BriefDigestMetadataKey, written once in
// hookClaimIdentityPatch), and a close whose stored brief no longer matches is
// refused with the CURRENT brief printed. Printing is the mechanism, not
// decoration: refusing alone tells the worker something changed and leaves it
// to go looking, which is the same round trip the redirect already lost.
//
// REJECTED: telling agents in their prompt to re-read the bead before closing.
// That is a rule that rots -- it survives exactly as long as the next prompt
// edit -- and the session it must reach is the one already ignoring an unread
// mail. REJECTED ALSO: deciding in Go which edits are "material" enough to
// refuse. Any such rule is a judgment call in Go (AGENTS.md), and the edit it
// misjudges is the one this gate exists for. Every edit refuses; the
// acknowledgement is one command.
//
// DELIBERATELY NOT GATED: a bead carrying no stamped digest. That is a bead
// claimed before this gate shipped, or never claimed through the hook at all,
// and it closes exactly as it did before -- which is why this gate needs no
// warn-only migration period, unlike the work-record gate beside it.
//
// COVERAGE, and it is this gate's binding limit. It fires only on closes that
// route through `gc bd`. `bd` is a separate binary on PATH, not a shim onto
// this dispatch, so a bare `bd close` never reaches here -- and bare `bd close`
// is what most of this city's agent prompts and formulas currently mandate.
// Counted 2026-09-09 over 2770 agent transcripts (Bash tool_use commands,
// command-position match): 579 `gc bd close` against 1017 bare `bd close`,
// and for the bench-engineer in the incident above, 2 against 92. The
// incident's own close was bare, so as the city stood this gate would not have
// caught the case it was written for.
//
// That other half has since landed (ci-ac97yz): ten close instructions across
// five agent templates and three formulas were routed at `gc bd close`, after
// measuring per agent that `gc bd` and bare `bd` resolve the same store from
// each one's own work_dir -- the verification this was blocked on, since a
// wrong scope does not error. A city doctor check now refuses the bare form in
// agent-facing text. What that does NOT reach is an agent typing `bd close`
// from habit rather than from a prompt, so this paragraph remains the gate's
// binding limit, just a smaller one.
//
// Also outside it, and deliberately: the dashboard API
// (internal/api/huma_handlers_beads.go), internal/dispatch, molecule autoclose,
// convoy close, sling, and gc's own bd-store-bridge all close beads without
// passing here. Those are control-plane closes made on a worker's behalf, by
// code that never read a brief and cannot be redirected by editing one.
//
// THE CACHE-LAG HAZARD WAS MEASURED AND DOES NOT APPLY HERE (ci-ac97yz). The
// comparison reads the description through gc's Go store while the mayor's
// edit arrives through a bd subprocess -- two seams -- and a read serving a
// cached pre-edit description would make current == claimed and pass the close
// silently on exactly the redirect this gate exists for. Run against the live
// city store on 2026-09-09: a description edited through `bd update -d` was
// read POST-edit by a fresh `gc bd close` 0.5 s later, which refused and
// printed the new text; metadata stamped 0.1 s earlier was likewise seen.
// Sub-second is the worst case for a stale read, so no delay sweep was run --
// a longer wait can only be fresher.
//
// The control arms are what let those refusals attribute anything. A bead with
// no stamped digest closed cleanly, and the same bead closed cleanly again once
// the new digest was acknowledged, so the rig produces both verdicts and
// "refused" is not this gate refusing everything.
//
// Corroborated, differently in kind rather than by a second look at the same
// thing: openStoreResultAtForCityWithConfig (cmd/gc/main.go) never wraps the
// store in beads.CachingStore. Only cmd/gc/api_state.go and
// dispatch_control_ready.go do, and both are long-lived processes -- which is
// where the 22-second reconcile lag observed on 2026-09-09 actually lives. A
// one-shot `gc bd` opens the backing store directly and has no cache to be
// stale.
//
// WHAT STILL BOUNDS IT. The probe bead was not held by a running agent, where
// the docstring's original experiment placed it. Holding changes no read seam
// -- store.Get is the same call, and a holder's cache lives in a different
// process -- but it was not measured. The negative also expires if `gc bd`
// ever grows a CachingStore on its open path; that, not the elapsed time, is
// the condition to re-check.
//
// Invariants pinned by brief_redirect_gate_test.go and, for the claim-time
// stamp-once rule, by cmd_hook_claim_brief_digest_test.go.

// briefRedirectPrintLimit bounds how much of the current brief a refusal
// prints. A brief is authored for a worker to read, so the cap is generous
// enough that an ordinary bead prints whole and only a pathological one is
// cut; the cut always names `gc bd show` so the rest is one command away.
const briefRedirectPrintLimit = 6000

// beadBriefDigest fingerprints the instructions a worker was handed: the title
// and description together.
//
// The title's byte length is written into the hash ahead of the text so the
// boundary between the two fields cannot move without changing the digest. A
// bare separator does not achieve that -- a title may itself contain the
// separator, and moving that line into the description would then hash
// identically, which is exactly the shape a mayor's rewrite takes.
//
// Truncated to 16 hex characters. The digest detects an edit, it does not
// resist one: nothing here defends against a worker who wants to close against
// a superseded brief, since such a worker can simply stamp the current digest.
func beadBriefDigest(title, description string) string {
	sum := sha256.Sum256([]byte(strconv.Itoa(len(title)) + "\n" + title + description))
	return hex.EncodeToString(sum[:])[:16]
}

// runBriefRedirectCloseGate refuses a close whose bead's brief was edited after
// the claim that stamped it. Returns whether the close should be blocked.
//
// preFetched supplies beads an earlier guard in this same invocation already
// read, matching the sibling close gates; a nil store or an unreadable bead
// never blocks, because a gate that cannot see the brief has nothing to say
// about it.
func runBriefRedirectCloseGate(bdArgs []string, store beads.Store, preFetched map[string]beads.Bead, stderr io.Writer) bool {
	return evaluateBriefRedirectCloseGate(bdArgs, store, preFetched, stderr)
}

// evaluateBriefRedirectCloseGate is the store-driven core, split from its
// wrapper so the predicate is exercised against an in-memory store rather than
// through a bd subprocess.
func evaluateBriefRedirectCloseGate(bdArgs []string, store beads.Store, preFetched map[string]beads.Bead, stderr io.Writer) (block bool) {
	ids, ok := workRecordCloseTargets(bdArgs)
	if !ok || store == nil {
		return false
	}
	for _, id := range ids {
		bead, cached := preFetched[id]
		if !cached {
			var err error
			bead, err = store.Get(id)
			if err != nil {
				continue
			}
		}
		claimed := strings.TrimSpace(bead.Metadata[beadmeta.BriefDigestMetadataKey])
		if claimed == "" {
			continue
		}
		// The acknowledgement is a metadata write, so a close that stamps and
		// closes in one `bd update` must be judged on its projected metadata,
		// the same way the work-record gate judges gc.work_outcome.
		projected, err := applyWorkRecordUpdateMetadata(bead, bdArgs)
		if err == nil {
			claimed = strings.TrimSpace(projected.Metadata[beadmeta.BriefDigestMetadataKey])
		}
		current := beadBriefDigest(bead.Title, bead.Description)
		if claimed == current {
			continue
		}
		fmt.Fprint(stderr, briefRedirectRefusal(bead, current)) //nolint:errcheck // best-effort stderr
		block = true
	}
	return block
}

// briefRedirectRefusal renders the refusal: what happened, the brief as it
// stands now, and the two commands that resolve it either way. The remedy pair
// is stated in full because the wrong half of it is destructive -- a worker
// that acknowledges its way past a genuine redirect lands work the operator
// refused, which is the incident this gate was written for.
func briefRedirectRefusal(bead beads.Bead, current string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "gc bd: brief-redirect gate: close of %s refused: its title or description changed after it was claimed, so the instructions you worked from may have been superseded.\n", bead.ID)
	fmt.Fprintf(&b, "  --- %s as it stands now ---\n", bead.ID)
	fmt.Fprintf(&b, "  %s\n\n", bead.Title)
	body := bead.Description
	truncated := false
	if len(body) > briefRedirectPrintLimit {
		body, truncated = body[:briefRedirectPrintLimit], true
	}
	for _, line := range strings.Split(body, "\n") {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if truncated {
		fmt.Fprintf(&b, "  [truncated at %d bytes -- read the rest with: gc bd show %s]\n", briefRedirectPrintLimit, bead.ID)
	}
	fmt.Fprintf(&b, "  --- end %s ---\n", bead.ID)
	b.WriteString("Read it. If what you built still satisfies it, acknowledge and close again:\n")
	fmt.Fprintf(&b, "  gc bd update %s --set-metadata '%s=%s'\n", bead.ID, beadmeta.BriefDigestMetadataKey, current)
	b.WriteString("If it does not, do NOT close -- correct the work, or hand the bead back:\n")
	fmt.Fprintf(&b, "  gc bd release-if-current %s '%s'\n", bead.ID, bead.Assignee)
	return b.String()
}
