package api

import (
	"reflect"
	"testing"

	"github.com/gastownhall/gascity/internal/api/genclient"
)

func TestOrderHistoryFromGenList_Valid(t *testing.T) {
	rig := "frontend"
	items := []genclient.OrderHistoryEntry{
		{BeadId: "fe-1", Name: "dolt-health", ScopedName: "dolt-health:rig:frontend", Rig: &rig, CreatedAt: "2026-04-22T12:00:00Z"},
		{BeadId: "ca-2", Name: "dolt-health", ScopedName: "dolt-health", CreatedAt: "2026-04-22T13:00:00Z"},
	}
	body := &genclient.OrderHistoryListBody{Entries: &items}

	got := orderHistoryFromGenList(body)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	want0 := OrderHistoryView{
		BeadID:     "fe-1",
		Name:       "dolt-health",
		ScopedName: "dolt-health:rig:frontend",
		Rig:        "frontend",
		CreatedAt:  "2026-04-22T12:00:00Z",
	}
	if got[0] != want0 {
		t.Errorf("got[0] = %+v, want %+v", got[0], want0)
	}
	want1 := OrderHistoryView{
		BeadID:     "ca-2",
		Name:       "dolt-health",
		ScopedName: "dolt-health",
		CreatedAt:  "2026-04-22T13:00:00Z",
	}
	if got[1] != want1 {
		t.Errorf("got[1] = %+v, want %+v", got[1], want1)
	}
}

func TestOrderHistoryFromGenList_Empty(t *testing.T) {
	t.Run("nil body", func(t *testing.T) {
		got := orderHistoryFromGenList(nil)
		if got == nil {
			t.Fatal("want non-nil slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("len = %d, want 0", len(got))
		}
	})
	t.Run("nil entries", func(t *testing.T) {
		body := &genclient.OrderHistoryListBody{}
		got := orderHistoryFromGenList(body)
		if got == nil || len(got) != 0 {
			t.Errorf("got = %+v, want empty non-nil", got)
		}
	})
	t.Run("empty entries slice", func(t *testing.T) {
		items := []genclient.OrderHistoryEntry{}
		body := &genclient.OrderHistoryListBody{Entries: &items}
		got := orderHistoryFromGenList(body)
		if got == nil || len(got) != 0 {
			t.Errorf("got = %+v, want empty non-nil", got)
		}
	})
}

func TestOrderHistoryFromGenList_PartialMissingFields(t *testing.T) {
	// Rig is an optional pointer on the wire; missing rig must decode to
	// an empty string rather than panicking.
	items := []genclient.OrderHistoryEntry{
		{BeadId: "ca-7", Name: "cron-sweep", ScopedName: "cron-sweep", CreatedAt: "2026-04-22T14:00:00Z"},
	}
	body := &genclient.OrderHistoryListBody{Entries: &items}

	got := orderHistoryFromGenList(body)

	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Rig != "" {
		t.Errorf("Rig = %q, want empty", got[0].Rig)
	}
	if got[0].BeadID != "ca-7" {
		t.Errorf("BeadID = %q, want ca-7", got[0].BeadID)
	}
}

// TestOrderHistoryFromGenCarriesEveryViewField is the mechanical half of the
// decode contract: it fails when a field is added to OrderHistoryView and left
// unwired in orderHistoryViewFromGen. That gap is invisible to every other
// test in the tree -- the CLI renderers construct OrderHistoryView values
// directly, so a field the decoder drops stays green on both ends while the
// operator sees an empty column against a live controller. ci-7gg9ra added
// Status and DispatchFailure through exactly this seam.
//
// A hand-kept list of expected fields would rot at the next field added, so
// the field set is read off the struct the code uses. A new field also has to
// be given a non-zero value in the fully-populated wire entry below, which is
// the point: the compiler cannot say a field is unwired, so the test names it.
func TestOrderHistoryFromGenCarriesEveryViewField(t *testing.T) {
	rig := "frontend"
	reason := "formula \"mol-digest\" not found"
	items := []genclient.OrderHistoryEntry{{
		BeadId:          "fe-1",
		Name:            "dolt-health",
		ScopedName:      "dolt-health:rig:frontend",
		Rig:             &rig,
		CreatedAt:       "2026-04-22T12:00:00Z",
		Status:          "failed",
		DispatchFailure: &reason,
	}}
	body := &genclient.OrderHistoryListBody{Entries: &items}

	got := orderHistoryFromGenList(body)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}

	v := reflect.ValueOf(got[0])
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		if v.Field(i).IsZero() {
			t.Errorf("OrderHistoryView.%s decoded zero from a fully-populated wire entry: either orderHistoryViewFromGen does not carry it, or this test's genclient.OrderHistoryEntry does not set it", typ.Field(i).Name)
		}
	}
}
