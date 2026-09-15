package main

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
)

// Scope: the sender half of the durable poke record (ci-49vlf3) -- that a
// nudge delivered as KEYSTROKES leaves both halves of its poke on the session
// bead, and that a delivery which sent none leaves nothing.
//
// The value written here is read by a different binary (the controller's idle
// check, pinned in idle_tracker_poke_test.go). Testing each end against its own
// literal would agree over any pair of keys, matching or not, so the round trip
// is asserted through session.StampPokePatch / Info.DurablePoke -- the same
// codec both ends use.
//
//	go test ./cmd/gc/ -run NudgePokeStamp

// pokeReportingFake stands in for a keystroke-delivering runtime: it records a
// poke AS A SIDE EFFECT of delivering, the way tmux's beginPoke does, rather
// than serving a pre-baked answer. That ordering is half of what this suite
// checks -- a poke stamped from a record that predates the delivery is the
// stale-poke bug, not a pass.
//
// It REFUSES to invent a poke: a session with no scripted prior reports none,
// exactly as an out-of-band runtime does. A stand-in answering every session
// with a poke would hand a pass to the case this suite exists to separate.
//
// Both Nudge and NudgeNow are overridden. The worker boundary picks between
// them by delivery mode, and overriding only one would leave the other on the
// embedded Fake -- a delivery that silently recorded nothing, with the suite
// still green.
type pokeReportingFake struct {
	*runtime.Fake
	prior map[string]time.Time // session -> scripted genuine pre-poke activity

	mu    sync.Mutex
	pokes map[string]runtime.Poke
}

func newPokeReportingFake(f *runtime.Fake, prior map[string]time.Time) *pokeReportingFake {
	return &pokeReportingFake{Fake: f, prior: prior, pokes: map[string]runtime.Poke{}}
}

func (f *pokeReportingFake) recordPoke(name string) {
	p, ok := f.prior[name]
	if !ok {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pokes[name] = runtime.Poke{At: time.Now(), Prior: p}
}

func (f *pokeReportingFake) Nudge(name string, content []runtime.ContentBlock) error {
	if err := f.Fake.Nudge(name, content); err != nil {
		return err
	}
	f.recordPoke(name)
	return nil
}

func (f *pokeReportingFake) NudgeNow(name string, content []runtime.ContentBlock) error {
	if err := f.Fake.NudgeNow(name, content); err != nil {
		return err
	}
	f.recordPoke(name)
	return nil
}

func (f *pokeReportingFake) LastPoke(name string) (runtime.Poke, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pk, ok := f.pokes[name]
	return pk, ok
}

// TestNudgePokeStampRecordsKeystrokeDelivery drives the live
// `gc session nudge` path -- the one that stamped nothing at all before this
// fix -- and requires the delivered poke to land on the bead in a form the
// controller can act on.
func TestNudgePokeStampRecordsKeystrokeDelivery(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store, fake, nil)

	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{
		Template: "worker", Title: "Worker", Command: "claude", WorkDir: dir,
		Provider: "claude", ExtraMeta: map[string]string{"session_origin": "manual"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// The agent's last real turn, three hours before the nudge. Whole seconds,
	// because the stamp is RFC3339 and carries no sub-second part.
	wantPrior := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	sp := newPokeReportingFake(fake, map[string]time.Time{info.SessionName: wantPrior})

	target := nudgeTarget{cityPath: dir, sessionID: info.ID, sessionName: info.SessionName}
	before := time.Now().UTC().Truncate(time.Second)
	var stdout, stderr bytes.Buffer
	if code := deliverSessionNudgeWithWorker(target, store, sp, "check deploy status", nudgeDeliveryImmediate, false, &stdout, &stderr); code != 0 {
		t.Fatalf("deliverSessionNudgeWithWorker = %d, want 0; stderr: %s", code, stderr.String())
	}
	after := time.Now().UTC()

	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	pk := got.DurablePoke()
	if !pk.Complete() {
		t.Fatalf("DurablePoke = %+v after a keystroke delivery; the controller would discount nothing and the nudge would buy another full idle_timeout", pk)
	}
	if !pk.Prior.Equal(wantPrior) {
		t.Fatalf("DurablePoke.Prior = %v, want the pre-nudge activity %v", pk.Prior, wantPrior)
	}
	// At is bounded by the delivery, not compared to a literal: the value comes
	// from the runtime's own clock at send time, and pinning it to anything the
	// test computes would pass over a stamp taken from the wrong instant.
	if pk.At.Before(before) || pk.At.After(after) {
		t.Fatalf("DurablePoke.At = %v, want within the delivery window [%v, %v]", pk.At, before, after)
	}
	if got.LastNudgeDeliveredAt.IsZero() {
		t.Fatal("LastNudgeDeliveredAt is zero; the poke patch must not have displaced the delivery stamp")
	}
}

// TestNudgePokeStampSkipsKeystrokeFreeDelivery is the separation this record
// depends on. A runtime that reports no poke delivered out of band, so nothing
// echoed into the terminal and there is nothing to discount. Stamping one here
// would make the controller read genuine agent output as gc's own keystrokes
// and stop idle detection outright -- worse than the defect being fixed.
func TestNudgePokeStampSkipsKeystrokeFreeDelivery(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store, fake, nil)

	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{
		Template: "worker", Title: "Worker", Command: "claude", WorkDir: dir,
		Provider: "claude", ExtraMeta: map[string]string{"session_origin": "manual"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// runtime.Fake implements no PokeReporter at all -- the ACP/subprocess case.
	target := nudgeTarget{cityPath: dir, sessionID: info.ID, sessionName: info.SessionName}
	var stdout, stderr bytes.Buffer
	if code := deliverSessionNudgeWithWorker(target, store, fake, "check deploy status", nudgeDeliveryImmediate, false, &stdout, &stderr); code != 0 {
		t.Fatalf("deliverSessionNudgeWithWorker = %d, want 0; stderr: %s", code, stderr.String())
	}

	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if pk := got.DurablePoke(); pk.Complete() {
		t.Fatalf("DurablePoke = %+v after a delivery that sent no keystrokes; want none", pk)
	}
	if got.LastNudgeDeliveredAt.IsZero() {
		t.Fatal("LastNudgeDeliveredAt is zero; a keystroke-free delivery still records that it was delivered")
	}
}

// TestNudgePokeStampKeystrokeFreePreservesEarlierPoke is why
// stampNudgeDelivery gates on poke.Complete() instead of always folding the
// poke patch in. A session that got a keystroke nudge and is then reached over
// the hook transport must keep the earlier record: without the gate the second
// delivery writes the zero time over both halves, the controller has nothing
// left to discount, and the wedged session goes back to being immune.
//
// The mutation sweep for ci-49vlf3 found this one SURVIVING. The zero Poke
// round-trips through RFC3339 as a zero Poke, so every other test in this file
// passes with the gate removed -- only a SECOND delivery over an existing
// record can tell the two apart.
func TestNudgePokeStampKeystrokeFreePreservesEarlierPoke(t *testing.T) {
	t.Setenv("GC_BEADS", "file")
	dir := t.TempDir()
	store := openNudgeBeadStore(dir)
	fake := runtime.NewFake()
	mgr := newSessionManagerWithConfig(dir, store, fake, nil)

	info, err := mgr.CreateSession(context.Background(), session.CreateOptions{
		Template: "worker", Title: "Worker", Command: "claude", WorkDir: dir,
		Provider: "claude", ExtraMeta: map[string]string{"session_origin": "manual"},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	target := nudgeTarget{cityPath: dir, sessionID: info.ID, sessionName: info.SessionName}

	wantPrior := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	keystrokes := newPokeReportingFake(fake, map[string]time.Time{info.SessionName: wantPrior})

	var stdout, stderr bytes.Buffer
	if code := deliverSessionNudgeWithWorker(target, store, keystrokes, "first", nudgeDeliveryImmediate, false, &stdout, &stderr); code != 0 {
		t.Fatalf("keystroke delivery = %d, want 0; stderr: %s", code, stderr.String())
	}
	first, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get after keystroke delivery: %v", err)
	}
	if !first.DurablePoke().Complete() {
		t.Fatal("no poke after the keystroke delivery; this test's control step did not run")
	}

	// Second delivery over a runtime that reports no poke -- the hook/ACP case.
	stdout.Reset()
	stderr.Reset()
	if code := deliverSessionNudgeWithWorker(target, store, fake, "second", nudgeDeliveryImmediate, false, &stdout, &stderr); code != 0 {
		t.Fatalf("keystroke-free delivery = %d, want 0; stderr: %s", code, stderr.String())
	}

	got, err := mgr.Get(info.ID)
	if err != nil {
		t.Fatalf("Get after keystroke-free delivery: %v", err)
	}
	after := got.DurablePoke()
	if !after.Complete() {
		t.Fatalf("DurablePoke = %+v: a keystroke-free delivery erased the earlier record, and the controller can no longer discount anything", after)
	}
	if !after.At.Equal(first.DurablePoke().At) || !after.Prior.Equal(first.DurablePoke().Prior) {
		t.Fatalf("DurablePoke = %+v, want the earlier record %+v unchanged", after, first.DurablePoke())
	}
}

// TestNudgePokeStampIgnoresStalePoke pins the `since` bound. The queued-nudge
// poller is one long-lived process per session, so its runtime still holds the
// PREVIOUS delivery's poke. Re-stamping that pair would discount current
// activity against an instant from an earlier nudge.
func TestNudgePokeStampIgnoresStalePoke(t *testing.T) {
	t.Parallel()

	now := time.Now()
	stale := runtime.Poke{At: now.Add(-time.Hour), Prior: now.Add(-4 * time.Hour)}
	sp := newPokeReportingFake(runtime.NewFake(), nil)
	sp.pokes["s"] = stale

	if pk := deliveredKeystrokePoke(sp, "s", now.Add(-time.Minute)); pk.Complete() {
		t.Fatalf("deliveredKeystrokePoke = %+v for a poke older than this delivery; want none", pk)
	}
	if pk := deliveredKeystrokePoke(sp, "s", stale.At); !pk.Complete() {
		t.Fatal("deliveredKeystrokePoke rejected a poke recorded exactly at the delivery start; the bound must be inclusive or every fast delivery loses its poke")
	}
}
