package extmsg

import (
	"context"
	"testing"

	"github.com/gastownhall/gascity/internal/beads"
)

// Pins the four properties an unattended transcript-retention sweep depends
// on, so that closing an aged transcript row stays a safe way to bound store
// growth.
//
// Why the suite exists: gc stores every external message as a bead and
// nothing prunes them (city bead ci-8vi9zz -- 101 of the city store's 118
// open beads were Slack transcript fossils on 2026-09-10). The sweep that
// bounds that growth CLOSES rows rather than deleting them, which only works
// because the read paths already treat a closed row as absent. Nothing stated
// that as a contract, so a read path added later that walked closed rows
// would make the sweep silently truncate history instead of bounding it.
//
// WHERE THE EXCLUSION ACTUALLY LIVES, because the obvious answer is wrong and
// was measured to be wrong. It is NOT the `item.Status == "closed"` guards in
// transcript_service.go: beads.ListQuery.Matches already drops closed rows
// unless a caller sets IncludeClosed, and no extmsg read sets it, so those
// guards never see a closed row for these queries. A mutation sweep on
// 2026-09-11 flipped each layer alone and both SURVIVED; only flipping both
// together reddens the tests below. The redundancy is worth keeping -- the
// query default is what a new read path inherits, the in-function guard is
// what survives someone passing IncludeClosed for an unrelated reason -- but
// it means neither layer alone can be pinned by a test at this altitude.
//
// What it does NOT cover: the sweep script itself, which is city-local
// (city repo, assets/scripts/extmsg-transcript-retention.py and its suite);
// and re-delivery deduplication, whose deliberate loss is recorded in
// TestClosedTranscriptRowNoLongerDeduplicatesItsProviderMessage below.
//
// Run: go test ./internal/extmsg/ -run TranscriptRetention

// appendRetentionFixture appends n live inbound entries and returns the bead
// id of each, indexed by sequence-1.
//
// Provider message ids are derived from the sequence rather than chosen
// independently, so a test that closes entry k and then re-appends "its"
// message cannot accidentally name a different row.
func appendRetentionFixture(t *testing.T, fabric Services, ref ConversationRef, n int) []string {
	t.Helper()
	ctx := context.Background()
	ids := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		rec, err := fabric.Transcript.Append(ctx, AppendTranscriptInput{
			Caller:            testAdapterCaller(),
			Conversation:      ref,
			Kind:              TranscriptMessageInbound,
			Provenance:        TranscriptProvenanceLive,
			ProviderMessageID: providerMessageIDForSequence(i),
			Text:              "entry",
			CreatedAt:         testNow(),
		})
		if err != nil {
			t.Fatalf("Append(%d): %v", i, err)
		}
		if rec.Sequence != int64(i) {
			t.Fatalf("Append(%d) got sequence %d", i, rec.Sequence)
		}
		ids = append(ids, rec.ID)
	}
	return ids
}

func providerMessageIDForSequence(seq int) string {
	return "retention-msg-" + string(rune('a'+seq-1))
}

func listRetentionSequences(t *testing.T, fabric Services, ref ConversationRef) []int64 {
	t.Helper()
	entries, err := fabric.Transcript.List(context.Background(), ListTranscriptInput{
		Caller:       testControllerCaller(),
		Conversation: ref,
		Limit:        100,
		Order:        TranscriptOrderAsc,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	got := make([]int64, 0, len(entries))
	for _, entry := range entries {
		got = append(got, entry.Sequence)
	}
	return got
}

// TestTranscriptRetentionClosedRowLeavesTheReadPath is the property the sweep
// IS: closing a row removes it from the transcript read path.
//
// Asserted through Transcript.List rather than by inspecting the bead,
// because List is what the API serves and a bead whose status moved while
// List still returned it would mean the sweep bounds nothing a reader sees.
func TestTranscriptRetentionClosedRowLeavesTheReadPath(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	fabric := NewServices(store)
	ref := testConversationRef()
	ids := appendRetentionFixture(t, fabric, ref, 5)

	if got := listRetentionSequences(t, fabric, ref); len(got) != 5 {
		t.Fatalf("before close: got %v, want 5 entries", got)
	}
	for _, id := range ids[:2] {
		if err := store.Close(id); err != nil {
			t.Fatalf("Close(%s): %v", id, err)
		}
	}

	got := listRetentionSequences(t, fabric, ref)
	want := []int64{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("after close: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("after close: got %v, want %v", got, want)
		}
	}
}

// TestTranscriptRetentionSurvivingTailStaysReadable pins that a sweep of the
// OLDEST rows costs only those rows.
//
// The ordering matters and is why this is separate from the test above: the
// read path walks sequence buckets from EarliestAvailableSequence upward and
// stops at the limit, so a bug that let a closed row consume a slot -- or
// that let an empty leading bucket end the walk -- would truncate the live
// tail while the count of dropped rows still looked right.
func TestTranscriptRetentionSurvivingTailStaysReadable(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	fabric := NewServices(store)
	ref := testConversationRef()
	ids := appendRetentionFixture(t, fabric, ref, 8)

	for _, id := range ids[:6] {
		if err := store.Close(id); err != nil {
			t.Fatalf("Close(%s): %v", id, err)
		}
	}

	got := listRetentionSequences(t, fabric, ref)
	if len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("surviving tail: got %v, want [7 8]", got)
	}
}

// TestTranscriptRetentionAppendSurvivesASweptHistory pins that a swept
// conversation is still appendable and does not renumber.
//
// Append takes its sequence from the state bead's next_sequence, never from
// the surviving rows. That is the whole reason a sweep is safe, and it is not
// obvious from the call site -- deriving the next sequence by scanning the
// rows is the simpler thing an editor would reach for, and under a sweep it
// would reissue sequences the closed rows already hold, colliding every
// swept row's bucket label and corrupting ordering for the live tail.
func TestTranscriptRetentionAppendSurvivesASweptHistory(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	fabric := NewServices(store)
	ref := testConversationRef()
	ids := appendRetentionFixture(t, fabric, ref, 4)

	for _, id := range ids {
		if err := store.Close(id); err != nil {
			t.Fatalf("Close(%s): %v", id, err)
		}
	}

	rec, err := fabric.Transcript.Append(context.Background(), AppendTranscriptInput{
		Caller:            testAdapterCaller(),
		Conversation:      ref,
		Kind:              TranscriptMessageOutbound,
		Provenance:        TranscriptProvenanceLive,
		ProviderMessageID: "retention-msg-after-sweep",
		Text:              "still live",
		CreatedAt:         testNow(),
	})
	if err != nil {
		t.Fatalf("Append after sweep: %v", err)
	}
	if rec.Sequence != 5 {
		t.Fatalf("Append after sweep: got sequence %d, want 5", rec.Sequence)
	}

	state, err := fabric.Transcript.State(context.Background(), testControllerCaller(), ref)
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state == nil {
		t.Fatal("State: conversation state went missing after a full sweep")
	}
	if state.NextSequence != 6 {
		t.Fatalf("State: got next_sequence %d, want 6", state.NextSequence)
	}
	if state.HydrationStatus != HydrationLiveOnly {
		t.Fatalf("State: got hydration_status %q, want %q", state.HydrationStatus, HydrationLiveOnly)
	}
}

// TestClosedTranscriptRowNoLongerDeduplicatesItsProviderMessage records the
// one capability a sweep DOES give up, so that it is a decision rather than a
// surprise.
//
// Append deduplicates on provider_message_id by looking for a live row
// carrying that message's locator label, and that lookup skips closed rows.
// A provider that re-delivers a message whose row was already swept therefore
// gets a second entry at a new sequence instead of the original back.
//
// Accepted rather than fixed: the retention window is 30 days and no adapter
// in this fabric re-delivers month-old messages, so closing the hole would
// mean teaching the dedupe path to read closed rows -- which is exactly the
// read-closed-rows behavior the sweep's safety rests on NOT existing. If a
// re-delivery window ever exceeds the retention window, this test is the
// place that says so, and it must be changed rather than merely deleted.
func TestClosedTranscriptRowNoLongerDeduplicatesItsProviderMessage(t *testing.T) {
	freezeTestClock(t)
	store := beads.NewMemStore()
	fabric := NewServices(store)
	ref := testConversationRef()
	ctx := context.Background()
	ids := appendRetentionFixture(t, fabric, ref, 1)

	// While the row is live, a re-delivery is idempotent.
	again, err := fabric.Transcript.Append(ctx, AppendTranscriptInput{
		Caller:            testAdapterCaller(),
		Conversation:      ref,
		Kind:              TranscriptMessageInbound,
		Provenance:        TranscriptProvenanceLive,
		ProviderMessageID: providerMessageIDForSequence(1),
		Text:              "entry",
		CreatedAt:         testNow(),
	})
	if err != nil {
		t.Fatalf("Append(redelivery, live): %v", err)
	}
	if again.Sequence != 1 {
		t.Fatalf("Append(redelivery, live): got sequence %d, want the original 1", again.Sequence)
	}

	if err := store.Close(ids[0]); err != nil {
		t.Fatalf("Close(%s): %v", ids[0], err)
	}

	swept, err := fabric.Transcript.Append(ctx, AppendTranscriptInput{
		Caller:            testAdapterCaller(),
		Conversation:      ref,
		Kind:              TranscriptMessageInbound,
		Provenance:        TranscriptProvenanceLive,
		ProviderMessageID: providerMessageIDForSequence(1),
		Text:              "entry",
		CreatedAt:         testNow(),
	})
	if err != nil {
		t.Fatalf("Append(redelivery, swept): %v", err)
	}
	if swept.Sequence != 2 {
		t.Fatalf("Append(redelivery, swept): got sequence %d, want a fresh 2", swept.Sequence)
	}
}
