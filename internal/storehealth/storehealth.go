// Package storehealth computes the Dolt bead store health summary used
// by gc status and the /v0/status API. The summary is: store path on
// disk, raw size in bytes, the retained row count of the city store
// (including open and closed beads), a derived MB-per-row ratio, and a
// warning flag when the ratio exceeds the configured threshold.
//
// Design: ADR 0002 (docs/adr/0002-dolt-store-maintenance-runbook.md)
// and bead ga-d5y design D9.
package storehealth

import (
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/fsys"
)

// DefaultThresholdMB is the MB-per-row threshold above which maintenance
// is flagged overdue. 1 MB per row matches the bad case observed in
// production (.beads/dolt at ~11 GB with ~64 rows).
const DefaultThresholdMB = 1.0

// MinWarnSizeBytes is the absolute floor below which the ratio-based
// warning never fires, regardless of row count. A pure MB-per-row ratio
// degenerates at small denominators: a healthy young city with only a
// handful of live rows still carries Dolt's own baseline footprint
// (oldgen archives, system tables) well into the hundreds of MB, which
// would otherwise permanently trip the ratio threshold with nothing for
// maintenance to reclaim -- gc dolt compact's own commit-count gate
// correctly finds nothing to do, but the warning can never clear (#3374).
const MinWarnSizeBytes = 1_000_000_000 // 1 GB

// Health summarizes disk and maintenance health of the Dolt bead store.
// A pointer *Health is included in status payloads so "no data" (e.g.
// supervisor not running) is representable as nil rather than a
// confusing zero-valued block. The same idiom applies one level down at
// RowsMeasured: LiveRows alone cannot distinguish a genuinely empty
// store from a row count that failed or timed out, so a caller that
// fabricates LiveRows=0 on measurement failure makes an unmeasured
// store indistinguishable from a healthy one. RowsMeasured is that
// distinction; when false, RatioMB and Warning are never computed and
// LiveRows carries no meaning.
type Health struct {
	Path         string
	SizeBytes    int64
	LiveRows     int
	RowsMeasured bool
	RatioMB      float64
	Warning      bool
	ThresholdMB  float64
	LastGCAt     time.Time
	LastGCStatus string
}

// StorePath returns the canonical on-disk location of the Dolt store
// for a city rooted at cityPath.
func StorePath(cityPath string) string {
	metaPath := filepath.Join(cityPath, ".beads", "metadata.json")
	if state, ok, err := contract.LoadMetadataState(fsys.OSFS{}, metaPath); err == nil && ok {
		if strings.EqualFold(strings.TrimSpace(state.Backend), "doltlite") {
			return filepath.Join(cityPath, ".beads", "doltlite")
		}
	}
	return filepath.Join(cityPath, ".beads", "dolt")
}

// Compute builds a Health from measured inputs. Pure function — all
// I/O is performed by the caller via WalkSize and LastMaintenance.
//
// rowsMeasured tells Compute whether retainedRows is a real count or a
// caller's placeholder for "the count did not complete" (nil store,
// scan error, timeout). Callers MUST NOT pass rowsMeasured=true with a
// fabricated retainedRows value — doing so is exactly the defect this
// parameter exists to prevent: a failed measurement rendering
// byte-identically to a healthy, genuinely-empty store.
func Compute(cityPath string, sizeBytes int64, retainedRows int, rowsMeasured bool, lastGCAt time.Time, lastGCStatus string) Health {
	h := Health{
		Path:         StorePath(cityPath),
		SizeBytes:    sizeBytes,
		LiveRows:     retainedRows,
		RowsMeasured: rowsMeasured,
		ThresholdMB:  DefaultThresholdMB,
		LastGCAt:     lastGCAt,
		LastGCStatus: lastGCStatus,
	}
	if rowsMeasured && retainedRows > 0 {
		h.RatioMB = float64(sizeBytes) / (bytesPerMB * float64(retainedRows))
		h.Warning = sizeBytes > MinWarnSizeBytes && sizeBytes > int64(DefaultThresholdMB*bytesPerMB)*int64(retainedRows)
	}
	return h
}

// WalkSize returns the total size in bytes of path's contents,
// recursing into subdirectories. Missing paths and read errors are
// treated as zero bytes — a fresh city has no Dolt directory yet, and
// partial read failures during maintenance should not mask the rest
// of the status output.
func WalkSize(path string) int64 {
	var total int64
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		total += info.Size()
		return nil
	})
	return total
}

// MaintenanceScanEvents bounds how far back LastMaintenance reads, in events.
//
// Why a bound exists at all: the probe filters on Type, and Type is not a
// prunable dimension for the archive reader (events.archiveOverlapsFilter), so
// an unbounded read gunzips and JSON-decodes every archive plus the whole
// active log -- once per event type, twice per call. On the city that found
// this (ci-euzkz1) that was ~1.3M events and ~1.44GB of JSON per
// `gc status --json`, costing 10.4-12.7s of CPU against a caller allowing 10s,
// and matching nothing at all because store maintenance had never run there.
//
// Why a SEQ bound and not a time window: a seq range prunes whole archives
// (archiveOverlapsFilter skips any archive whose LastSeq is at or below
// AfterSeq) without gunzipping them, and it is independent of how fast this
// city produces events. A time window would have to be chosen against an
// event RATE that varies per city and per day, and it would silently re-read
// everything on a quiet one.
//
// THE COST OF THE BOUND, stated because it is a real narrowing: a maintenance
// event older than the most recent MaintenanceScanEvents events is reported as
// absent rather than as an old timestamp. Both render the same way -- the
// caller omits the field on a zero time -- and both mean the same thing to an
// operator reading a health panel, which is that maintenance is overdue. What
// would NOT be acceptable is reporting a stale success as current, and the
// bound cannot do that: it only ever narrows toward "unknown".
const MaintenanceScanEvents = 100_000

// LastMaintenance returns the timestamp and status ("success" or
// "failed") of the most-recent store-maintenance event in provider.
// Zero time and empty status when no events, provider is nil, or the
// provider returns an error.
//
// The read is bounded to the newest MaintenanceScanEvents events; see that
// constant for why, and for what the bound gives up.
func LastMaintenance(ep events.Provider) (time.Time, string) {
	return lastMaintenanceWithin(ep, MaintenanceScanEvents)
}

// lastMaintenanceWithin is LastMaintenance with the scan bound injected.
//
// The bound is a parameter so the cost invariant can be driven at a size a
// test can build, rather than by adding a switch that production reads -- a
// switch would be evaluated before the code under test and would let the real
// bound be stubbed away with the suite still green. The exported entry point
// above is the only production caller and always passes the constant.
//
// maxEvents <= 0 reads unbounded.
func lastMaintenanceWithin(ep events.Provider, maxEvents uint64) (time.Time, string) {
	if ep == nil {
		return time.Time{}, ""
	}
	// A provider that cannot report its head seq is read unbounded rather than
	// not at all: the bound is an optimization, and a wrong guess at the head
	// would silently hide events. Small providers (fakes, fresh cities) have a
	// head below the bound and are unaffected either way.
	var afterSeq uint64
	if maxEvents > 0 {
		if latest, err := ep.LatestSeq(); err == nil && latest > maxEvents {
			afterSeq = latest - maxEvents
		}
	}
	var (
		latestTs     time.Time
		latestStatus string
	)
	for _, spec := range []struct {
		typ    string
		status string
	}{
		{events.StoreMaintenanceDone, "success"},
		{events.StoreMaintenanceFailed, "failed"},
	} {
		filter := events.Filter{Type: spec.typ, AfterSeq: afterSeq}
		// The tail path is what makes the bound real. A plain List prunes whole
		// archives on AfterSeq but still reads the active log end to end, so on
		// a city where these events never appear the cost stays proportional to
		// that log. The backward read stops at the seq floor instead, which is
		// the only shape that bounds the ABSENCE case -- and absence is the
		// normal case here, since a city that has never run store maintenance
		// has no such event anywhere.
		var (
			evts []events.Event
			err  error
		)
		if tp, ok := ep.(events.TailProvider); ok && afterSeq > 0 {
			evts, err = tp.ListTail(filter, 1)
		} else {
			evts, err = ep.List(filter)
		}
		if err != nil {
			continue
		}
		for _, e := range evts {
			if e.Ts.After(latestTs) {
				latestTs = e.Ts
				latestStatus = spec.status
			}
		}
	}
	return latestTs, latestStatus
}

const bytesPerMB = 1_000_000
