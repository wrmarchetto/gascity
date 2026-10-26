package main

import (
	"slices"

	"github.com/gastownhall/gascity/internal/events"
)

// orderTickEvents answers every event-trigger order in one dispatch tick from
// one journal read. FileRecorder.List decodes the whole active events.jsonl
// whatever the filter, so a read per order paid a full scan each (ci-e4jp3a).
// An event recorded mid-tick is seen on the next tick, never lost.
type orderTickEvents struct {
	events.Provider
	types []string // event types the ring's event orders watch, sorted

	// The read starts at the lowest cursor the previous tick asked for, so a
	// ring that reaches its higher cursors first still reads once. A cursor
	// below it widens the read, and that tick pays a second scan.
	hint    uint64
	hasHint bool

	read  bool
	floor uint64         // evs holds every watched event with Seq > floor
	evs   []events.Event // seq order, as the provider returned it

	asked    bool
	minAsked uint64
}

func (m *memoryOrderDispatcher) newOrderTickEvents() *orderTickEvents {
	var types []string
	for _, a := range m.aa {
		if a.Trigger == "event" && a.On != "" && !slices.Contains(types, a.On) {
			types = append(types, a.On)
		}
	}
	slices.Sort(types)
	return &orderTickEvents{
		Provider: m.ep,
		types:    types,
		hint:     m.eventFloorHint,
		hasHint:  m.hasEventFloorHint,
	}
}

// keepEventFloorHint carries the tick's lowest asked-for cursor to the next
// tick. Cursors only advance, so it bounds every cursor the same orders ask
// for.
func (m *memoryOrderDispatcher) keepEventFloorHint(s *orderTickEvents) {
	m.eventFloorHint, m.hasEventFloorHint = s.minAsked, s.asked
}

// List serves a watched-type filter from the tick's shared read and passes
// any other filter straight to the provider.
func (s *orderTickEvents) List(f events.Filter) ([]events.Event, error) {
	if f.Type == "" || !slices.Contains(s.types, f.Type) {
		return s.Provider.List(f)
	}
	if !s.asked || f.AfterSeq < s.minAsked {
		s.minAsked, s.asked = f.AfterSeq, true
	}
	if !s.read || f.AfterSeq < s.floor {
		floor := f.AfterSeq
		if !s.read && s.hasHint && s.hint < floor {
			floor = s.hint
		}
		evs, err := s.Provider.List(events.Filter{Types: s.types, AfterSeq: floor})
		if err != nil {
			return nil, err
		}
		s.read, s.floor, s.evs = true, floor, evs
	}
	return events.ApplyFilter(s.evs, f), nil
}
