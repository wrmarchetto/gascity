package orders

import (
	"log"
	"sort"
	"time"
)

var runtimeHelpersLogf = log.Printf

// LastRunAcross returns a LastRunFunc reporting the most recent run time for a
// named order across a federation of order front doors (the dispatcher/CLI
// city + rig scopes). Each *Store performs its own MIXED orders+graph LastRun
// read (unioning its orders leg with its graph leg); the max across scopes wins.
// A per-scope error aborts and propagates. nil entries are skipped.
func LastRunAcross(stores []*Store) LastRunFunc {
	return func(name string) (time.Time, error) {
		var latest time.Time
		for _, s := range stores {
			if s == nil {
				continue
			}
			last, err := s.LastRun(name)
			if err != nil {
				return time.Time{}, err
			}
			if last.After(latest) {
				latest = last
			}
		}
		return latest, nil
	}
}

// RecentRunsAcross returns the newest bounded execution history across a
// federation of order front doors. Each scope contributes its newest limit
// rows; that is sufficient for the global newest limit, then the merged result
// is sorted by the canonical created-at/id order used by Store.RecentRuns.
func RecentRunsAcross(stores []*Store, name string, limit int) ([]OrderRun, error) {
	if limit <= 0 {
		return nil, nil
	}
	runs := make([]OrderRun, 0, len(stores)*limit)
	for _, store := range stores {
		if store == nil {
			continue
		}
		scopeRuns, err := store.RecentRuns(name, limit)
		if err != nil {
			return nil, err
		}
		runs = append(runs, scopeRuns...)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].ID > runs[j].ID
		}
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	if len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

// CursorAcross returns a CursorFunc merging the event seq cursor for a named
// order across a federation of order front doors. Each *Store performs its own
// MIXED orders+graph Cursor read; the max seq across scopes wins. nil entries
// are skipped.
func CursorAcross(stores []*Store) CursorFunc {
	return func(name string) uint64 {
		var latest uint64
		for _, s := range stores {
			if s == nil {
				continue
			}
			if seq := uint64(s.Cursor(name)); seq > latest {
				latest = seq
			}
		}
		return latest
	}
}
