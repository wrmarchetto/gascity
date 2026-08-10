package beads

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncLogBuffer is a mutex-guarded log sink. A bare bytes.Buffer is NOT safe
// here: the store's own goroutines log (the reconcile loop and the stall
// watchdog both do), so their Write -- and the notifyLocked it runs, which
// reads buf.String() -- races against the test goroutine's waitFor
// registration and its settled-output String() read. Caught by -race in
// TestStallWatchdogSpeaksWhileTheReconcileGoroutineIsParked
// (caching_store_reconcile_stall_internal_test.go), the only test that runs
// StartReconciler's background goroutines under captureLog. Tests that only
// inspect settled output use String(); tests waiting on a line another
// goroutine has yet to write use waitFor. Those two stay the only exported
// reads -- no Reset, no Lines -- because no caller needs to clear or
// line-split captured output.
type syncLogBuffer struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	waiters []logWaiter
}

// logWaiter pairs a substring with a channel that Write and waitFor close
// once the substring is present. found is only ever closed, never sent to,
// so it is one-shot and safe for multiple selectors, and a waitFor call
// against an already-satisfied buffer returns an already-closed channel.
type logWaiter struct {
	substr string
	found  chan struct{}
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	b.notifyLocked()
	return n, err
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitFor returns a channel closed once the buffer holds substr, so a caller
// can select on it against a deadline.
//
// The alternative a reader reaches for first is polling String() with a short
// time.Sleep between reads. That is banned twice over: TESTING.md's
// "Asynchronous tests wait for facts, not elapsed time" (TESTING.md:154)
// forbids open-coded polling loops outright, and every time.Sleep is a
// fixed_sleep row in the resource census (test/test-resources.toml), which has
// no per-file exemption, so a polling waiter cannot land without a baseline
// bump. That same section carves out a safety deadline around the wait, as
// long as the deadline does not set the normal test duration -- the carve-out
// the select callers below rely on.
//
// Registration matches what is ALREADY buffered before it enqueues. The line
// being waited for is written by a store goroutine that may well beat the test
// to the buffer, and a waiter consulted only from Write would then never fire.
func (b *syncLogBuffer) waitFor(substr string) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	found := make(chan struct{})
	b.waiters = append(b.waiters, logWaiter{substr: substr, found: found})
	b.notifyLocked()
	return found
}

// notifyLocked closes the channel of every waiter whose substring is now
// present in buf, and drops matched waiters from b.waiters. It skips the
// O(buffer) buf.String() copy when b.waiters is empty -- the common case,
// since most Write calls have no pending waiter -- and test log output runs
// to a few KB, so the copy cost when a waiter IS pending is not worth
// avoiding further.
//
// There is no unregister/cancel path: a waiter whose select-side deadline
// expires stays in b.waiters with found never closed. Left unfixed because
// the leak is bounded by the per-test buffer lifetime -- captureLog drops
// the buffer at test end -- so it cannot accumulate across tests.
func (b *syncLogBuffer) notifyLocked() {
	if len(b.waiters) == 0 {
		return
	}
	out := b.buf.String()
	kept := b.waiters[:0]
	for _, w := range b.waiters {
		if strings.Contains(out, w.substr) {
			close(w.found)
			continue
		}
		kept = append(kept, w)
	}
	b.waiters = kept
}

// captureLog redirects the default logger's output to a buffer for the
// duration of the test.
func captureLog(t *testing.T) *syncLogBuffer {
	t.Helper()
	buf := &syncLogBuffer{}
	prev := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prev)
		log.SetFlags(prevFlags)
	})
	return buf
}

// newPrimedCacheForCadenceTest returns a CachingStore primed against an
// empty MemStore. The cache lock is held by the caller — tests drive the
// cadence helpers under the lock to mirror production call sites.
func newPrimedCacheForCadenceTest(t *testing.T) *CachingStore {
	t.Helper()
	cs := NewCachingStoreForTest(NewMemStore(), nil)
	if err := cs.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}
	return cs
}

func TestLatencyP95EmptyWindowReturnsZero(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	p95, ok := cs.latencyP95Locked()
	if ok {
		t.Errorf("samplesEnough = true on empty window, want false")
	}
	if p95 != 0 {
		t.Errorf("p95 = %v on empty window, want 0", p95)
	}
}

func TestLatencyP95UnderfilledWindowReturnsZero(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := 0; i < cacheLatencyWindowSize-1; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	p95, ok := cs.latencyP95Locked()
	if ok {
		t.Errorf("samplesEnough = true with %d samples, want false", cacheLatencyWindowSize-1)
	}
	if p95 != 0 {
		t.Errorf("p95 = %v with underfilled window, want 0", p95)
	}
}

func TestLatencyP95FullWindowReturnsValue(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(time.Duration(i+1) * time.Second)
	}
	p95, ok := cs.latencyP95Locked()
	if !ok {
		t.Fatalf("samplesEnough = false with full window")
	}
	// Nearest-rank P95 of [1s, 2s, ..., 10s] = 10s.
	if p95 != 10*time.Second {
		t.Errorf("p95 = %v, want 10s", p95)
	}
}

func TestRecordLatencyRingBufferDropsOldest(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Saturate the window with low samples.
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
	}
	// Push a single high sample; the ring should still hold N samples,
	// and one of them is now 8s.
	cs.recordReconcileLatencyLocked(8 * time.Second)
	if got := len(cs.latencyWindow); got != cacheLatencyWindowSize {
		t.Errorf("len(latencyWindow) = %d, want %d", got, cacheLatencyWindowSize)
	}
	found := false
	for _, d := range cs.latencyWindow {
		if d == 8*time.Second {
			found = true
		}
	}
	if !found {
		t.Errorf("ring buffer did not retain newest 8s sample: %v", cs.latencyWindow)
	}
}

func TestAdaptiveCadencePromotesOnHighLatency(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	cs.recomputeCadenceLocked()

	if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalMedium {
		t.Errorf("interval = %v, want %v", got, cacheReconcileIntervalMedium)
	}
	if cs.stats.CadenceDriver != "latency" {
		t.Errorf("stats.CadenceDriver = %q, want %q", cs.stats.CadenceDriver, "latency")
	}
	if cs.stats.CurrentReconcileInterval != cacheReconcileIntervalMedium {
		t.Errorf("stats.CurrentReconcileInterval = %v, want %v",
			cs.stats.CurrentReconcileInterval, cacheReconcileIntervalMedium)
	}
	if cs.stats.LatencyP95Ms <= 0 {
		t.Errorf("stats.LatencyP95Ms = %v, want > 0 once window is full",
			cs.stats.LatencyP95Ms)
	}
}

func TestAdaptiveCadenceLowLatencyKeepsSmall(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Low samples (1s) — well under the 7.5s threshold.
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
	}
	cs.recomputeCadenceLocked()

	if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalSmall {
		t.Errorf("interval = %v, want %v", got, cacheReconcileIntervalSmall)
	}
	if cs.stats.CadenceDriver != "default" {
		t.Errorf("stats.CadenceDriver = %q, want %q", cs.stats.CadenceDriver, "default")
	}
}

func TestUpdateStatsPopulatesCadenceDiagnostics(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.updateStatsLocked()

	if cs.stats.CurrentReconcileInterval != cacheReconcileIntervalSmall {
		t.Errorf("stats.CurrentReconcileInterval = %v, want %v",
			cs.stats.CurrentReconcileInterval, cacheReconcileIntervalSmall)
	}
	if cs.stats.LatencyP95Ms != 0 {
		t.Errorf("stats.LatencyP95Ms = %v before full window, want 0",
			cs.stats.LatencyP95Ms)
	}
	if cs.stats.CadenceDriver != "default" {
		t.Errorf("stats.CadenceDriver = %q, want default", cs.stats.CadenceDriver)
	}
}

func TestAdaptiveCadenceDemotesAfterHysteresisWindow(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Promote first.
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	cs.recomputeCadenceLocked()
	if cs.adaptiveIntervalLocked() != cacheReconcileIntervalMedium {
		t.Fatalf("setup: did not promote to MEDIUM")
	}

	// Replace samples one at a time with low values; check that demotion
	// happens only after cacheLatencyWindowSize *consecutive* low cycles
	// (architect §3.2 hysteresis). For each cycle we record one new low
	// sample and run recomputeCadenceLocked.
	for i := 0; i < cacheLatencyWindowSize-1; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
		cs.recomputeCadenceLocked()
		if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalMedium {
			t.Fatalf("cycle %d: interval = %v, want still MEDIUM", i+1, got)
		}
	}

	// Cycle 10: should demote.
	cs.recordReconcileLatencyLocked(1 * time.Second)
	cs.recomputeCadenceLocked()
	if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalSmall {
		t.Errorf("after %d low cycles: interval = %v, want SMALL",
			cacheLatencyWindowSize, got)
	}
}

func TestAdaptiveCadenceSpikeResetsHysteresisCounter(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Promote.
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	cs.recomputeCadenceLocked()

	// Five low cycles — should not yet demote.
	for i := 0; i < 5; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
		cs.recomputeCadenceLocked()
	}
	// One spike — should reset the hysteresis counter.
	cs.recordReconcileLatencyLocked(8 * time.Second)
	cs.recomputeCadenceLocked()

	// Now another nine low cycles — would demote if counter weren't reset.
	for i := 0; i < cacheLatencyWindowSize-1; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
		cs.recomputeCadenceLocked()
		if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalMedium {
			t.Fatalf("post-spike cycle %d: interval = %v, want MEDIUM (spike must reset)",
				i+1, got)
		}
	}
}

func TestAdaptiveCadenceCompositionBeadCountAlone(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Synthesize 1500 beads via direct map population (this is internal
	// test territory — production traffic populates via Prime/reconcile).
	for i := 0; i < 1500; i++ {
		id := "x" + intToString(i)
		cs.beads[id] = Bead{ID: id, Status: "open"}
	}
	cs.recomputeCadenceLocked()

	if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalMedium {
		t.Errorf("interval = %v, want MEDIUM (bead count >= 1000)", got)
	}
	if cs.stats.CadenceDriver != "bead-count" {
		t.Errorf("stats.CadenceDriver = %q, want bead-count", cs.stats.CadenceDriver)
	}
}

func TestAdaptiveCadenceCompositionBoth(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := 0; i < 1500; i++ {
		id := "x" + intToString(i)
		cs.beads[id] = Bead{ID: id, Status: "open"}
	}
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	cs.recomputeCadenceLocked()

	if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalMedium {
		t.Errorf("interval = %v, want MEDIUM", got)
	}
	if cs.stats.CadenceDriver != "both" {
		t.Errorf("stats.CadenceDriver = %q, want both", cs.stats.CadenceDriver)
	}
}

func TestAdaptiveCadencePreservesLargeInterval(t *testing.T) {
	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := 0; i < 5500; i++ {
		id := "x" + intToString(i)
		cs.beads[id] = Bead{ID: id, Status: "open"}
	}
	// Latency irrelevant — LARGE bead count alone forces 120s.
	cs.recomputeCadenceLocked()

	if got := cs.adaptiveIntervalLocked(); got != cacheReconcileIntervalLarge {
		t.Errorf("interval = %v, want LARGE (%v) for >=5000 beads",
			got, cacheReconcileIntervalLarge)
	}
	if cs.stats.CadenceDriver != "bead-count" {
		t.Errorf("stats.CadenceDriver = %q, want bead-count", cs.stats.CadenceDriver)
	}
}

func TestAdaptiveCadenceLogsOnceOnPromote(t *testing.T) {
	logBuf := captureLog(t)

	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	cs.recomputeCadenceLocked() // first call: SMALL → MEDIUM transition

	// Subsequent calls with no state change must NOT re-emit the
	// transition log.
	for i := 0; i < 5; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
		cs.recomputeCadenceLocked()
	}

	out := logBuf.String()
	count := strings.Count(out, "cadence promoted small→medium")
	if count != 1 {
		t.Errorf("promote log emitted %d time(s), want exactly 1; output=%q",
			count, out)
	}
	if !strings.Contains(out, "driver=latency") {
		t.Errorf("promote log missing driver=latency; output=%q", out)
	}
	if !strings.Contains(out, "window=10") {
		t.Errorf("promote log missing window=10; output=%q", out)
	}
}

func TestAdaptiveCadenceLogsOnceOnDemote(t *testing.T) {
	logBuf := captureLog(t)

	cs := newPrimedCacheForCadenceTest(t)
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Promote (silent for this assertion — we'll filter by message).
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(8 * time.Second)
	}
	cs.recomputeCadenceLocked()

	// Drain to demote.
	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
		cs.recomputeCadenceLocked()
	}

	// More low cycles after the demote should not re-emit.
	for i := 0; i < 5; i++ {
		cs.recordReconcileLatencyLocked(1 * time.Second)
		cs.recomputeCadenceLocked()
	}

	out := logBuf.String()
	count := strings.Count(out, "cadence demoted medium→small")
	if count != 1 {
		t.Errorf("demote log emitted %d time(s), want exactly 1; output=%q",
			count, out)
	}
	// Scope the driver assertion to the demote line — the promote line
	// earlier in this test also contains "driver=latency", which would
	// mask a regression if we asserted against the whole buffer.
	demoteIdx := strings.Index(out, "cadence demoted medium→small")
	if demoteIdx < 0 {
		t.Fatalf("no demote line in output=%q", out)
	}
	demoteLine := out[demoteIdx:]
	if nl := strings.IndexByte(demoteLine, '\n'); nl >= 0 {
		demoteLine = demoteLine[:nl]
	}
	if !strings.Contains(demoteLine, "driver=latency") {
		t.Errorf("demote log missing driver=latency; line=%q output=%q", demoteLine, out)
	}
}

// slowListStore wraps a Store and injects a fixed delay on List calls
// that ask for AllowScan (the reconcile path). It mirrors
// reconcileRaceStore but uses a wall-clock sleep so Tmux-style timing
// shows up in the latency window.
type slowListStore struct {
	Store
	delay time.Duration
}

func (s *slowListStore) List(query ListQuery) ([]Bead, error) {
	if query.AllowScan {
		time.Sleep(s.delay)
	}
	return s.Store.List(query)
}

func TestRunReconciliationFeedsLatencyWindow(t *testing.T) {
	// 1.5 ms delay × 10 reconciles is fast enough to keep the test cheap
	// but large enough that ms-resolution P95 is non-zero.
	const reconcileDelay = 1500 * time.Microsecond

	cs := NewCachingStoreForTest(&slowListStore{Store: NewMemStore(), delay: reconcileDelay}, nil)
	if err := cs.Prime(t.Context()); err != nil {
		t.Fatalf("Prime: %v", err)
	}

	for i := 0; i < cacheLatencyWindowSize; i++ {
		cs.runReconciliation()
	}

	stats := cs.Stats()
	if stats.LatencyP95Ms <= 0 {
		t.Errorf("Stats().LatencyP95Ms = %v, want > 0 after %d reconciliations",
			stats.LatencyP95Ms, cacheLatencyWindowSize)
	}
	if stats.CurrentReconcileInterval == 0 {
		t.Errorf("Stats().CurrentReconcileInterval = 0, want non-zero")
	}
	if stats.CadenceDriver == "" {
		t.Errorf("Stats().CadenceDriver is empty, want a classification")
	}
}

// intToString avoids strconv import bloat in this test-only file. Tests
// only need short distinct IDs to populate the bead map.
func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
