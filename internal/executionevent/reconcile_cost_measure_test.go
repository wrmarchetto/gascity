//go:build measure

// internal/executionevent/reconcile_cost_measure_test.go
//
// Times the two candidate dominant costs in the patrol tick's
// reconcile_execution_completions phase against a LIVE city. It asserts
// nothing about timing and fails only when it cannot take the measurement --
// it is an instrument, not a test.
//
// WHY IT EXISTS RATHER THAN A `dolt sql` ONE-LINER. ci-sbdsjh's 12-30s
// estimate for the scan half came from timing one scan-shaped query through
// the `dolt sql` CLI: a fresh process per query, against on-disk committed
// data. The running city selects NativeDoltStore (`gc status --json` reports
// beads_store=NativeDoltStore, native_store_eligible=true), which holds a
// connection to the Dolt server and reads the working set. Process startup is
// most of a CLI invocation, so an estimate built out of 59 of them can be
// wrong by an order of magnitude in either direction, and the bead is
// explicit that a fix chosen on the wrong half moves the tick barely at all.
//
// WHY THIS PACKAGE AND NOT cmd/gc. The first version lived in cmd/gc so it
// could call the controller's own nativeDoltOpenEnvForScope. That package's
// TestMain calls clearProcessLiveEnvForTests() and sets the managed-Dolt TEST
// MODE flag, so a live measurement there is pointed at a fake server with an
// empty environment -- it reported "set GC_MEASURE_CITY" with the variable
// exported. Here the projector's own unexported currentSteps and
// completedFacts are reachable, which is better fidelity anyway: the timed
// code is the code, not a copy of it.
//
// WHERE THE ENV COMES FROM. gc projects the scoped Dolt env into every session
// it spawns (BEADS_DOLT_SERVER_PORT, BEADS_DOLT_AUTO_START and siblings, seen
// on the mayor's tmux session and on this one), so an agent session already
// holds the controller's own connection parameters. The whole environment is
// handed to OpenNativeDoltStoreAt, which reads only the keys in
// beads.nativeDoltOpenEnvKeys -- passing a superset avoids keeping a second
// copy of that list here, which would rot silently the next time a key is
// added.
//
// WHAT IT IS STILL NOT. The controller process. Same store constructor, same
// env, same three query shapes, but a different process with a cold
// connection and no sibling phase competing for the server. Every figure is a
// LOWER BOUND on the controller's cost. Record the store size with each
// reading: the same change reads differently at a different row count.
//
// Run it:
//
//	GC_MEASURE_CITY=/home/willie/projects/city \
//	  go test ./internal/executionevent/ -tags measure \
//	  -run TestMeasureReconcileExecutionCompletionsCost -v -count=1 -timeout 20m
package executionevent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/events"
)

// measureSpan accumulates one timed call shape.
type measureSpan struct {
	label   string
	calls   int
	rows    int
	total   time.Duration
	slowest time.Duration
	fastest time.Duration
}

func (s *measureSpan) add(d time.Duration, rows int) {
	s.calls++
	s.rows += rows
	s.total += d
	if d > s.slowest {
		s.slowest = d
	}
	if s.fastest == 0 || d < s.fastest {
		s.fastest = d
	}
}

func (s *measureSpan) report(t *testing.T) {
	if s.calls == 0 {
		t.Logf("%-30s no calls", s.label)
		return
	}
	t.Logf("%-30s calls=%-5d rows=%-7d total=%-10s mean=%-9s fastest=%-9s slowest=%s",
		s.label, s.calls, s.rows, s.total.Round(time.Millisecond),
		(s.total / time.Duration(s.calls)).Round(time.Microsecond),
		s.fastest.Round(time.Microsecond), s.slowest.Round(time.Millisecond))
}

func environMap() map[string]string {
	out := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func TestMeasureReconcileExecutionCompletionsCost(t *testing.T) {
	cityPath := strings.TrimSpace(os.Getenv("GC_MEASURE_CITY"))
	if cityPath == "" {
		cityPath = strings.TrimSpace(os.Getenv("GC_CITY"))
	}
	if cityPath == "" {
		t.Fatal("set GC_MEASURE_CITY (or run inside a gc session, which sets " +
			"GC_CITY); there is no default because pointing this at the wrong " +
			"city produces a plausible number about the wrong store")
	}

	opened, err := beads.OpenNativeDoltStoreAt(context.Background(), cityPath, environMap())
	if err != nil {
		t.Fatalf("opening native dolt store at %s: %v", cityPath, err)
	}
	store := beads.GraphStore{Store: opened}

	// --- (b) the journal read, first and three times ---
	//
	// First because the projector reads the journal before it touches any
	// store. Three times because the first read pays for the page cache and
	// the controller's steady state does not, so a single figure cannot tell
	// a 40MB disk read from a 40MB parse.
	journalPath := filepath.Join(cityPath, ".gc", "events.jsonl")
	if info, statErr := os.Stat(journalPath); statErr == nil {
		t.Logf("journal %s: %d bytes", journalPath, info.Size())
	} else {
		t.Logf("journal %s: cannot stat: %v", journalPath, statErr)
	}
	recorder, err := events.NewFileRecorder(journalPath, io.Discard)
	if err != nil {
		t.Fatalf("opening event journal %s: %v", journalPath, err)
	}
	journal := &measureSpan{label: "(b) completedFacts"}
	for range 3 {
		start := time.Now()
		found, listErr := completedFacts(recorder)
		elapsed := time.Since(start)
		if listErr != nil {
			t.Fatalf("reading completion journal: %v", listErr)
		}
		journal.add(elapsed, len(found))
	}

	// --- (a1) the root scan ---
	rootScan := &measureSpan{label: "(a1) root ListByMetadata"}
	start := time.Now()
	roots, err := store.ListByMetadata(
		map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
		0,
		beads.IncludeClosed,
		beads.WithBothTiers,
	)
	rootScan.add(time.Since(start), len(roots))
	if err != nil {
		t.Fatalf("listing workflow roots: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal("zero workflow roots: this connection is not reading the " +
			"city store the trace was taken against, so no timing below " +
			"would mean anything")
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })

	// The bead records "58 roots, and all of them graph.v2" as INFERRED
	// rather than joined. Joined here so the next reader does not inherit
	// the inference.
	graphV2 := make([]beads.Bead, 0, len(roots))
	closedRoots := 0
	for _, root := range roots {
		if root.Metadata[beadmeta.FormulaContractMetadataKey] != beadmeta.FormulaContractGraphV2 {
			continue
		}
		graphV2 = append(graphV2, root)
		if strings.EqualFold(strings.TrimSpace(root.Status), "closed") {
			closedRoots++
		}
	}
	t.Logf("roots: %d total, %d graph.v2, %d of those closed",
		len(roots), len(graphV2), closedRoots)

	// --- (a2) the per-root step scan, and (a3) the per-step Get behind it ---
	stepScan := &measureSpan{label: "(a2) currentSteps per root"}
	stepGet := &measureSpan{label: "(a3) per-step Get"}
	closedSteps := 0
	for _, root := range graphV2 {
		start = time.Now()
		definitions, stepErr := currentSteps(store, root.ID)
		stepScan.add(time.Since(start), len(definitions))
		if stepErr != nil {
			t.Logf("root %s: currentSteps: %v", root.ID, stepErr)
			continue
		}
		for _, definition := range definitions {
			start = time.Now()
			step, getErr := store.Get(definition.BeadID)
			stepGet.add(time.Since(start), 1)
			if getErr != nil {
				t.Logf("step %s: get: %v", definition.BeadID, getErr)
				continue
			}
			if strings.EqualFold(strings.TrimSpace(step.Status), "closed") {
				closedSteps++
			}
		}
	}
	t.Logf("steps: %d walked, %d closed (a closed step is what the phase "+
		"exists to project)", stepGet.calls, closedSteps)

	t.Log("--- reconcile_execution_completions, component costs ---")
	journal.report(t)
	rootScan.report(t)
	stepScan.report(t)
	stepGet.report(t)
	scanHalf := rootScan.total + stepScan.total + stepGet.total
	journalOne := journal.total / time.Duration(max(journal.calls, 1))
	t.Logf("%-30s %s", "(a) scan half, one pass", scanHalf.Round(time.Millisecond))
	t.Logf("%-30s %s", "(b) journal, one read", journalOne.Round(time.Millisecond))
	t.Logf("%-30s %s", "(a)+(b) one pass", (scanHalf + journalOne).Round(time.Millisecond))
	t.Logf("trace figure to compare against: 35812 / 35809 / 35440 ms over " +
		"three consecutive patrol ticks, 2026-09-08 07:13-07:15Z")
}

// TestDumpGraphV2RootAndStepShape prints the metadata a graph.v2 root and one
// of its steps actually carry, on the live city store.
//
// It exists because every cheap bound on the reconcile pass depends on what
// the ROOT already knows about its steps: a root carrying its step count, or
// a settled marker, can be skipped without the per-root scan that costs 33 of
// the phase's 35 seconds. Guessing that from the writer side is how an
// optimization gets built against a key nobody writes, so this reads it off
// the rows the controller reads.
func TestDumpGraphV2RootAndStepShape(t *testing.T) {
	cityPath := strings.TrimSpace(os.Getenv("GC_MEASURE_CITY"))
	if cityPath == "" {
		cityPath = strings.TrimSpace(os.Getenv("GC_CITY"))
	}
	if cityPath == "" {
		t.Fatal("set GC_MEASURE_CITY or run inside a gc session")
	}
	opened, err := beads.OpenNativeDoltStoreAt(context.Background(), cityPath, environMap())
	if err != nil {
		t.Fatalf("opening native dolt store at %s: %v", cityPath, err)
	}
	store := beads.GraphStore{Store: opened}
	roots, err := store.ListByMetadata(
		map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
		0, beads.IncludeClosed, beads.WithBothTiers)
	if err != nil {
		t.Fatalf("listing workflow roots: %v", err)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].ID < roots[j].ID })
	shown := 0
	for _, root := range roots {
		if root.Metadata[beadmeta.FormulaContractMetadataKey] != beadmeta.FormulaContractGraphV2 {
			continue
		}
		keys := make([]string, 0, len(root.Metadata))
		for key := range root.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		t.Logf("root %s status=%s", root.ID, root.Status)
		for _, key := range keys {
			value := root.Metadata[key]
			if len(value) > 120 {
				value = value[:120] + "...(" + strconv.Itoa(len(root.Metadata[key])) + "B)"
			}
			t.Logf("    %-40s %s", key, value)
		}
		rows, listErr := store.ListByMetadata(
			map[string]string{beadmeta.RootBeadIDMetadataKey: root.ID},
			0, beads.IncludeClosed, beads.WithBothTiers)
		if listErr != nil {
			t.Fatalf("listing steps for %s: %v", root.ID, listErr)
		}
		t.Logf("  %d rows carry root_bead_id=%s", len(rows), root.ID)
		for _, row := range rows {
			if row.ID == root.ID {
				continue
			}
			stepKeys := make([]string, 0, len(row.Metadata))
			for key := range row.Metadata {
				stepKeys = append(stepKeys, key)
			}
			sort.Strings(stepKeys)
			t.Logf("  step %s status=%s", row.ID, row.Status)
			for _, key := range stepKeys {
				value := row.Metadata[key]
				if len(value) > 120 {
					value = value[:120] + "...(" + strconv.Itoa(len(row.Metadata[key])) + "B)"
				}
				t.Logf("      %-38s %s", key, value)
			}
			break
		}
		shown++
		if shown >= 2 {
			return
		}
	}
}

// TestListRowEqualsGetRowForEveryGraphV2Step is the verification ci-sbdsjh
// requires before the per-step Get can be dropped in favor of the row
// ListByMetadata already returned.
//
// The failure it rules out is silent and worse than the slow tick it would be
// fixing: if a list row's Status came back empty where Get's says closed, the
// reconcile loop's closed-status filter would reject every step and lifecycle
// recovery would stop emitting, with nothing in the suite to show it. A fake
// store cannot answer this -- both sides would be the same in-memory map --
// so it runs against the store the city actually runs (bd contract, native
// Dolt).
//
// It compares the projected event rather than field-by-field equality of the
// whole bead: the event is what the reconcile path consumes, and a difference
// in a field nothing reads is not a defect.
func TestListRowEqualsGetRowForEveryGraphV2Step(t *testing.T) {
	cityPath := strings.TrimSpace(os.Getenv("GC_MEASURE_CITY"))
	if cityPath == "" {
		cityPath = strings.TrimSpace(os.Getenv("GC_CITY"))
	}
	if cityPath == "" {
		t.Fatal("set GC_MEASURE_CITY or run inside a gc session")
	}
	opened, err := beads.OpenNativeDoltStoreAt(context.Background(), cityPath, environMap())
	if err != nil {
		t.Fatalf("opening native dolt store at %s: %v", cityPath, err)
	}
	store := beads.GraphStore{Store: opened}
	roots, err := store.ListByMetadata(
		map[string]string{beadmeta.KindMetadataKey: beadmeta.KindWorkflow},
		0, beads.IncludeClosed, beads.WithBothTiers)
	if err != nil {
		t.Fatalf("listing workflow roots: %v", err)
	}
	compared, statusMismatch, eventMismatch := 0, 0, 0
	for _, root := range roots {
		if root.Metadata[beadmeta.FormulaContractMetadataKey] != beadmeta.FormulaContractGraphV2 {
			continue
		}
		rows, listErr := store.ListByMetadata(
			map[string]string{beadmeta.RootBeadIDMetadataKey: root.ID},
			0, beads.IncludeClosed, beads.WithBothTiers)
		if listErr != nil {
			t.Fatalf("listing steps for %s: %v", root.ID, listErr)
		}
		for _, listRow := range rows {
			if listRow.ID == root.ID {
				continue
			}
			getRow, getErr := store.Get(listRow.ID)
			if getErr != nil {
				t.Errorf("step %s: get: %v", listRow.ID, getErr)
				continue
			}
			compared++
			if !strings.EqualFold(strings.TrimSpace(listRow.Status), strings.TrimSpace(getRow.Status)) {
				statusMismatch++
				t.Errorf("step %s: status differs: list=%q get=%q",
					listRow.ID, listRow.Status, getRow.Status)
			}
			fromList, listOK := LifecycleEvent(events.ExecutionStepCompleted, root, listRow, "verify")
			fromGet, getOK := LifecycleEvent(events.ExecutionStepCompleted, root, getRow, "verify")
			// Compared by their projection key plus the topology, not by
			// struct equality: events.Event carries a json.RawMessage and is
			// not comparable. The key is what the reconcile pass dedupes on,
			// so a difference outside it cannot change what gets emitted.
			if listOK != getOK || completedFactKeyFor(fromList) != completedFactKeyFor(fromGet) {
				eventMismatch++
				t.Errorf("step %s: projected event differs:\n  list ok=%v %+v\n  get  ok=%v %+v",
					listRow.ID, listOK, fromList, getOK, fromGet)
			}
		}
	}
	if compared == 0 {
		t.Fatal("compared zero steps: this proves nothing about list/Get " +
			"equivalence, and a zero here is how a vacuous verification passes")
	}
	t.Logf("compared %d step rows: %d status mismatches, %d projected-event mismatches",
		compared, statusMismatch, eventMismatch)
}

// TestMeasureReconcilerPassCostAcrossTicks times three consecutive passes of
// one CompletedReconciler against the live city, which is the shape the
// controller runs: a held reconciler, one pass per patrol tick.
//
// Pass 1 is the full walk. Pass 2 is the walk that settles the roots pass 1
// emitted for (zero, on a city whose facts are all present, so pass 2 already
// settles). Pass 3 onward is the steady state, and its number is the one the
// tick period will follow. It is a LOWER BOUND on the controller's cost --
// different process, cold connection, no sibling phase competing -- and the
// operator's post-restart re-measurement of city:order-capacity and the tick
// period is what the bead's acceptance actually turns on.
func TestMeasureReconcilerPassCostAcrossTicks(t *testing.T) {
	cityPath := strings.TrimSpace(os.Getenv("GC_MEASURE_CITY"))
	if cityPath == "" {
		cityPath = strings.TrimSpace(os.Getenv("GC_CITY"))
	}
	if cityPath == "" {
		t.Fatal("set GC_MEASURE_CITY or run inside a gc session")
	}
	opened, err := beads.OpenNativeDoltStoreAt(context.Background(), cityPath, environMap())
	if err != nil {
		t.Fatalf("opening native dolt store at %s: %v", cityPath, err)
	}
	stores := []beads.GraphStore{{Store: opened}}
	journalPath := filepath.Join(cityPath, ".gc", "events.jsonl")
	recorder, err := events.NewFileRecorder(journalPath, io.Discard)
	if err != nil {
		t.Fatalf("opening event journal %s: %v", journalPath, err)
	}
	if info, statErr := os.Stat(journalPath); statErr == nil {
		t.Logf("journal %d bytes", info.Size())
	}

	// The recorder is the LIVE journal, so a pass that decided to emit would
	// append to it. On this city every fact is already present and pass 1
	// emits 0; the count is logged rather than asserted so a run on a city
	// with a genuinely stranded fact reports the repair instead of failing.
	reconciler := NewCompletedReconciler()
	for pass := 1; pass <= 3; pass++ {
		start := time.Now()
		emitted := reconciler.Reconcile(recorder, stores, "measure-reconcile")
		t.Logf("pass %d: %-9s emitted=%d", pass,
			time.Since(start).Round(time.Millisecond), emitted)
	}
	t.Log("before this change every pass was the pass-1 figure: 35812 / " +
		"35809 / 35440 ms measured in the controller's own trace over three " +
		"consecutive patrol ticks, 2026-09-08 07:13-07:15Z")
}
