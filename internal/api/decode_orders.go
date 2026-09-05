package api

import "github.com/gastownhall/gascity/internal/api/genclient"

// OrderHistoryView is the CLI-facing shape for `gc order history` rows. It
// mirrors the subset of fields the CLI formatter reads so cmd/gc/ never
// imports genclient directly.
type OrderHistoryView struct {
	BeadID     string
	Name       string
	ScopedName string
	Rig        string
	CreatedAt  string
	// Status is the run's lifecycle state as OrderRun.State() reports it.
	Status string
	// DispatchFailure is the reason a wisp run failed before cooking, empty
	// when there is none. The pointer the wire uses is flattened here so the
	// renderers treat a missing field and an empty one identically.
	DispatchFailure string
}

// orderHistoryViewFromGen translates one genclient.OrderHistoryEntry into an
// OrderHistoryView. Optional pointer fields are dereferenced safely.
func orderHistoryViewFromGen(g genclient.OrderHistoryEntry) OrderHistoryView {
	out := OrderHistoryView{
		BeadID:     g.BeadId,
		Name:       g.Name,
		ScopedName: g.ScopedName,
		CreatedAt:  g.CreatedAt,
		Status:     g.Status,
	}
	if g.Rig != nil {
		out.Rig = *g.Rig
	}
	if g.DispatchFailure != nil {
		out.DispatchFailure = *g.DispatchFailure
	}
	return out
}

// orderHistoryFromGenList translates the genclient list body into
// []OrderHistoryView. Returns an empty slice (never nil) when the body is
// missing or holds no entries so callers can uniformly format the empty case.
func orderHistoryFromGenList(body *genclient.OrderHistoryListBody) []OrderHistoryView {
	if body == nil || body.Entries == nil {
		return []OrderHistoryView{}
	}
	items := *body.Entries
	out := make([]OrderHistoryView, 0, len(items))
	for _, item := range items {
		out = append(out, orderHistoryViewFromGen(item))
	}
	return out
}
