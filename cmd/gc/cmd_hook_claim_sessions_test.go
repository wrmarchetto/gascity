package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
)

// Scope: gc.claim_sessions, the claim-time record of EVERY session that
// claimed a bead. gc.session_id is overwritten by each reclaim, so a bead a
// Claude seat started and a Codex seat finished reads as Codex-only, and the
// epic review derived from it would run a single lane over mixed work
// (city docs/reviews/rollout/design.md section 7, "Recording attribution
// going forward").
//
// Every case drives doHookClaim end to end against claimSessionsCity, an
// in-memory store, rather than calling the patch builder: the defect this
// suite exists for is a reclaim LOSING an earlier claimant, which only shows
// across two claims through the real write path. The fake applies each patch
// the way bd does (key-wise overwrite), so a writer that replaced the list
// instead of extending it goes red here.
//
// Delegated elsewhere: whether the bd write itself lands (BdStore tests), and
// what the epic review does with the list (the city's epic-review.py scope,
// which owns lane selection). Not representable here: two claims racing on
// the same bead -- the claim CAS makes that a single winner, and the list
// read-modify-write rides on that winner's snapshot.
//
// Run: go test ./cmd/gc/ -run ClaimSessions

// claimSessionsCity is one work bead plus the session beads that may claim it.
// ReadSessionBead REFUSES a session it was not given, so a read the suite
// forgot to script is a read error the claim must survive, never a silent
// success.
type claimSessionsCity struct {
	bead     beads.Bead
	sessions map[string]beads.Bead
	writes   int
}

func newClaimSessionsCity(meta map[string]string) *claimSessionsCity {
	m := map[string]string{beadmeta.RoutedToMetadataKey: "toolsmith"}
	for k, v := range meta {
		m[k] = v
	}
	return &claimSessionsCity{
		bead:     beads.Bead{ID: "ci-work", Type: "task", Status: "open", Title: "t", Metadata: m},
		sessions: map[string]beads.Bead{},
	}
}

func (c *claimSessionsCity) copyBead() beads.Bead {
	b := c.bead
	b.Metadata = map[string]string{}
	for k, v := range c.bead.Metadata {
		b.Metadata[k] = v
	}
	return b
}

// release returns the bead to the pool the way gc bd release does: unowned
// and open, metadata kept.
func (c *claimSessionsCity) release() {
	c.bead.Assignee = ""
	c.bead.Status = "open"
}

func (c *claimSessionsCity) ops(t *testing.T) hookClaimOps {
	t.Helper()
	return hookClaimOps{
		Runner: func(string, string) (string, error) {
			out, err := json.Marshal([]beads.Bead{c.copyBead()})
			return string(out), err
		},
		Claim: func(_ context.Context, _ string, _ []string, id, assignee string) (beads.Bead, bool, error) {
			if id != c.bead.ID {
				return beads.Bead{}, false, fmt.Errorf("unscripted claim of %q", id)
			}
			if c.bead.Assignee != "" && c.bead.Assignee != assignee {
				return c.copyBead(), false, nil
			}
			c.bead.Assignee, c.bead.Status = assignee, "in_progress"
			return c.copyBead(), true, nil
		},
		ReadSessionBead: func(_ context.Context, _ string, _ []string, id, _ string) (beads.Bead, error) {
			s, ok := c.sessions[id]
			if !ok {
				return beads.Bead{}, fmt.Errorf("no session bead %q", id)
			}
			return s, nil
		},
		ResolveWorkBranch:      func(string) string { return "fix/ci-work" },
		ResolveSiblingBranches: func(context.Context, string, []string, beads.Bead) ([]hookClaimSiblingBranch, error) { return nil, nil },
		StampWorkMeta: func(_ context.Context, _ string, _ []string, id, _ string, patch map[string]string) error {
			if id != c.bead.ID {
				return fmt.Errorf("unscripted stamp of %q", id)
			}
			c.writes++
			for k, v := range patch {
				c.bead.Metadata[k] = v
			}
			return nil
		},
		ReadWorkMeta: func(context.Context, string, []string, string, string) (beads.Bead, error) {
			return c.copyBead(), nil
		},
		EmitExecutionStepStarted: func(beads.Bead, string, []string, string) {},
		EmitClaimRejected:        func(string, string, string) {},
		PublishRunMap:            noopPublishRunMap,
		DrainAck: func(reason string, _ io.Writer) error {
			return fmt.Errorf("unexpected drain (%s): the bead was claimable", reason)
		},
	}
}

// seat is one pool slot's claim options, keyed on its session bead id.
func claimSessionsSeat(assignee, sessionID string) hookClaimOptions {
	return hookClaimOptions{
		Assignee:           assignee,
		IdentityCandidates: []string{assignee},
		RouteTargets:       []string{"toolsmith"},
		Env:                []string{"GC_SESSION_ID=" + sessionID, "GC_SESSION_NAME=" + assignee + "-" + sessionID},
		JSON:               true,
	}
}

// claim runs one hook tick for seat and returns stderr. The tick must report
// work: every case here holds a claimable or already-held bead.
func (c *claimSessionsCity) claim(t *testing.T, seat hookClaimOptions) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := doHookClaim("bd ready --json", "/tmp/store", seat, c.ops(t), &stdout, &stderr); code != 0 {
		t.Fatalf("claim by %s = %d; stdout=%s stderr=%s", seat.Assignee, code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"bead_id":"ci-work"`) {
		t.Fatalf("claim by %s reported %s, want ci-work", seat.Assignee, stdout.String())
	}
	return stderr.String()
}

// recorded decodes gc.claim_sessions, failing the test on a value no reader
// could parse.
func (c *claimSessionsCity) recorded(t *testing.T) []string {
	t.Helper()
	raw, ok := c.bead.Metadata[beadmeta.ClaimSessionsMetadataKey]
	if !ok {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("%s = %q is not a JSON string array: %v", beadmeta.ClaimSessionsMetadataKey, raw, err)
	}
	return out
}

// claudeSession is the Claude seat's session bead, ci-cl in every case that
// uses it.
func claudeSession(key string) beads.Bead {
	return beads.Bead{ID: "ci-cl", Type: "session", Metadata: map[string]string{
		"provider": "claude", "session_key": key,
	}}
}

func codexSession(id, key string) beads.Bead {
	return beads.Bead{ID: id, Type: "session", Metadata: map[string]string{
		"provider": "codex-toolsmith", "provider_kind": "codex", "session_key": key,
	}}
}

func TestClaimSessionsKeepsEveryClaimantAcrossAReclaim(t *testing.T) {
	// The design's own end-to-end case: a Claude seat claims, the bead goes
	// back to the pool, a Codex seat reclaims. gc.session_id now names only
	// the Codex session, so the Claude claimant survives ONLY in this list.
	// The session keys are distinct per seat so a writer that recorded the
	// wrong session's key cannot pass.
	city := newClaimSessionsCity(nil)
	city.sessions["ci-cl"] = claudeSession("key-claude-7f3a")
	city.sessions["ci-cx"] = codexSession("ci-cx", "key-codex-91be")

	city.claim(t, claimSessionsSeat("toolsmith-1", "ci-cl"))
	city.release()
	city.claim(t, claimSessionsSeat("toolsmith-2", "ci-cx"))

	want := []string{"ci-cl:claude:key-claude-7f3a", "ci-cx:codex:key-codex-91be"}
	if got := city.recorded(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q", beadmeta.ClaimSessionsMetadataKey, got, want)
	}
	if got := city.bead.Metadata[beadmeta.SessionIDMetadataKey]; got != "ci-cx" {
		t.Fatalf("gc.session_id = %q, want the last claimant ci-cx (fixture premise)", got)
	}
}

func TestClaimSessionsRecordsAnExistingAssignmentThatPredatesTheKey(t *testing.T) {
	// "For a new claim and an existing_assignment alike" (design section 7).
	// A bead claimed before this key shipped is already in_progress under its
	// seat, so the only path that will ever see it again is adoption.
	city := newClaimSessionsCity(nil)
	city.bead.Status, city.bead.Assignee = "in_progress", "toolsmith-1"
	city.sessions["ci-cl"] = claudeSession("key-adopt-4410")

	city.claim(t, claimSessionsSeat("toolsmith-1", "ci-cl"))

	want := []string{"ci-cl:claude:key-adopt-4410"}
	if got := city.recorded(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s after adoption = %q, want %q", beadmeta.ClaimSessionsMetadataKey, got, want)
	}
}

func TestClaimSessionsAdoptionTickWritesNothingOnceRecorded(t *testing.T) {
	// The claim re-runs on every hook tick through the adoption path. A list
	// re-appended or re-written each time is the bead.updated flood
	// stampHookClaimIdentity's compare-and-skip exists to prevent, so the
	// second tick must issue no write at all, not merely an idempotent one.
	city := newClaimSessionsCity(nil)
	city.sessions["ci-cl"] = claudeSession("key-tick-0c21")
	seat := claimSessionsSeat("toolsmith-1", "ci-cl")

	city.claim(t, seat)
	before := city.writes
	city.claim(t, seat)

	if city.writes != before {
		t.Fatalf("adoption tick issued %d write(s) after the claim was recorded, want 0", city.writes-before)
	}
	if got := city.recorded(t); len(got) != 1 {
		t.Fatalf("%s = %q after a re-tick, want exactly one entry", beadmeta.ClaimSessionsMetadataKey, got)
	}
}

func TestClaimSessionsFillsAKeylessEntryInPlace(t *testing.T) {
	// The session key is written by the provider's SessionStart hook, which
	// can lose that write and heal at a later prompt (ci-v5dmqg). A claim
	// that ran in the gap records the claimant without a key. The next tick
	// must complete that entry, not add a second one beside it -- a keyless
	// twin would read as a claimant whose transcript cannot be found.
	city := newClaimSessionsCity(nil)
	city.sessions["ci-cl"] = claudeSession("")
	seat := claimSessionsSeat("toolsmith-1", "ci-cl")

	city.claim(t, seat)
	if got, want := city.recorded(t), []string{"ci-cl:claude:"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keyless claim recorded %q, want %q", got, want)
	}
	city.sessions["ci-cl"] = claudeSession("key-healed-e5d0")
	city.claim(t, seat)

	if got, want := city.recorded(t), []string{"ci-cl:claude:key-healed-e5d0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after the key healed %s = %q, want %q", beadmeta.ClaimSessionsMetadataKey, got, want)
	}
}

func TestClaimSessionsKeepsEachConversationOfOneSession(t *testing.T) {
	// One session bead can host several conversations: a context reset
	// starts a new transcript under a new key. Both transcripts hold work on
	// this bead, so the earlier key must survive the later one.
	city := newClaimSessionsCity(nil)
	city.sessions["ci-cl"] = claudeSession("key-first-2b07")
	seat := claimSessionsSeat("toolsmith-1", "ci-cl")

	city.claim(t, seat)
	city.sessions["ci-cl"] = claudeSession("key-second-d9c4")
	city.claim(t, seat)

	want := []string{"ci-cl:claude:key-first-2b07", "ci-cl:claude:key-second-d9c4"}
	if got := city.recorded(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q", beadmeta.ClaimSessionsMetadataKey, got, want)
	}
}

func TestClaimSessionsNamesTheClaimantWhenItsSessionBeadIsUnreadable(t *testing.T) {
	// The session bead is where family and key come from, and its read is
	// best-effort. The claimant's id is still known from GC_SESSION_ID, and
	// an entry with an empty family reads as unknown downstream, which
	// selects both review lanes. Writing nothing would read as "never
	// claimed by a session", the less safe of the two.
	city := newClaimSessionsCity(nil)

	stderr := city.claim(t, claimSessionsSeat("toolsmith-1", "ci-ghost"))

	if got, want := city.recorded(t), []string{"ci-ghost::"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q; stderr=%s", beadmeta.ClaimSessionsMetadataKey, got, want, stderr)
	}
}

func TestClaimSessionsIgnoresASessionBeadAnsweringForAnotherID(t *testing.T) {
	// bd resolves ids by prefix, so a read for a vanished session can return
	// a colliding one. Recording ITS provider would attribute this claim to
	// whatever that other session runs.
	city := newClaimSessionsCity(nil)
	city.sessions["ci-cl"] = codexSession("ci-cl-other", "key-wrong-6e18")

	city.claim(t, claimSessionsSeat("toolsmith-1", "ci-cl"))

	if got, want := city.recorded(t), []string{"ci-cl::"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %q, want %q -- a prefix-colliding read must not supply the family", beadmeta.ClaimSessionsMetadataKey, got, want)
	}
}

func TestClaimSessionsFamilyComesFromTheResolvedProviderOnly(t *testing.T) {
	// provider_kind is stamped exactly when the resolved family differs from
	// the provider name (resolvedProviderFamilyMetadata), so its absence
	// means either "the name IS the family" or "there is no family". Only a
	// built-in provider name settles which. A prefix match on the name, the
	// cross-check sweep's provider_family(), would call a custom provider
	// named "claude-ish" Claude with nothing in gc's record to say so.
	cases := []struct {
		name string
		meta map[string]string
		want string
	}{
		{"kind wins over a custom name", map[string]string{"provider": "codex-toolsmith", "provider_kind": "codex"}, "codex"},
		{"built-in name with no kind", map[string]string{"provider": "claude"}, "claude"},
		{"custom name with no kind", map[string]string{"provider": "claude-ish"}, ""},
		{"no provider at all", map[string]string{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			city := newClaimSessionsCity(nil)
			meta := map[string]string{"session_key": "key-fam"}
			for k, v := range tc.meta {
				meta[k] = v
			}
			city.sessions["ci-s"] = beads.Bead{ID: "ci-s", Type: "session", Metadata: meta}

			city.claim(t, claimSessionsSeat("toolsmith-1", "ci-s"))

			want := []string{"ci-s:" + tc.want + ":key-fam"}
			if got := city.recorded(t); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s = %q, want %q", beadmeta.ClaimSessionsMetadataKey, got, want)
			}
		})
	}
}

func TestClaimSessionsNeverRewritesAnUnreadableRecord(t *testing.T) {
	// A value that does not decode may still hold claimants a human can read.
	// Resetting it, as the attempt log does on corruption, would drop them
	// and make mixed work look single-provider -- the exact under-report this
	// key exists to remove. Left alone, it reads as unknown downstream and
	// selects both lanes. The claim itself must still succeed.
	const corrupt = `ci-old:claude:k1,ci-cx:codex:k2`
	city := newClaimSessionsCity(map[string]string{beadmeta.ClaimSessionsMetadataKey: corrupt})
	city.sessions["ci-cl"] = claudeSession("key-after-3a90")

	stderr := city.claim(t, claimSessionsSeat("toolsmith-1", "ci-cl"))

	if got := city.bead.Metadata[beadmeta.ClaimSessionsMetadataKey]; got != corrupt {
		t.Fatalf("%s rewritten to %q, want the unreadable value %q left intact", beadmeta.ClaimSessionsMetadataKey, got, corrupt)
	}
	if !strings.Contains(stderr, beadmeta.ClaimSessionsMetadataKey) {
		t.Fatalf("an unreadable %s was skipped silently; stderr=%q", beadmeta.ClaimSessionsMetadataKey, stderr)
	}
	if got := city.bead.Metadata[beadmeta.SessionIDMetadataKey]; got != "ci-cl" {
		t.Fatalf("the rest of the identity stamp was lost with it: gc.session_id = %q", got)
	}
}

func TestClaimSessionsSkipsSessionlessAndControlClaims(t *testing.T) {
	// The same exclusions as gc.session_id, for the same reasons: no session
	// means no claimant to name, and control beads stay session-free by
	// graphroute's design. The control set is iterated from
	// beadmeta.ControlKinds so a kind added later is covered.
	t.Run("sessionless", func(t *testing.T) {
		city := newClaimSessionsCity(nil)
		seat := claimSessionsSeat("toolsmith-1", "ci-cl")
		seat.Env = nil
		city.claim(t, seat)
		if raw, ok := city.bead.Metadata[beadmeta.ClaimSessionsMetadataKey]; ok {
			t.Fatalf("sessionless claim recorded %s=%q", beadmeta.ClaimSessionsMetadataKey, raw)
		}
	})
	for _, kind := range beadmeta.ControlKinds {
		t.Run("control "+kind, func(t *testing.T) {
			city := newClaimSessionsCity(map[string]string{beadmeta.KindMetadataKey: kind})
			city.sessions["ci-cl"] = claudeSession("key-ctl")
			var stdout, stderr bytes.Buffer
			doHookClaim("bd ready --json", "/tmp/store", claimSessionsSeat("toolsmith-1", "ci-cl"), city.ops(t), &stdout, &stderr)
			if raw, ok := city.bead.Metadata[beadmeta.ClaimSessionsMetadataKey]; ok {
				t.Fatalf("control bead of kind %q recorded %s=%q", kind, beadmeta.ClaimSessionsMetadataKey, raw)
			}
		})
	}
}
