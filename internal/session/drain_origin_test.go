package session

import (
	"strings"
	"testing"
)

// Scope: the drain-origin close-reason codec (drain_origin.go) -- the rendering
// half of ci-wxag. The wiring half (which reconciler path stamps which origin)
// is pinned in cmd/gc/session_reconciler_drain_origin_test.go; the metadata
// round-trip through session.Info is pinned in info_codec_test.go.
//
// Run: go test ./internal/session/ -run DrainOrigin

// TestDrainedCloseReasonSeparatesSelfFromReconciler pins the invariant ci-wxag
// exists for: a self-retired session and a reconciler-retired session must not
// render the same close reason.
//
// The expected strings are written out literally instead of being recomputed
// from DrainedCloseReason. A test that compared DrainedCloseReason(self) against
// DrainedCloseReason(reconciler) would pass against a body that ignored its
// origin argument entirely -- which is exactly the pre-fix behavior, where both
// paths reached one CanonicalCloseReason("drained") literal.
func TestDrainedCloseReasonSeparatesSelfFromReconciler(t *testing.T) {
	tests := []struct {
		name   string
		origin DrainOrigin
		reason string
		want   string
	}{
		{
			name:   "self with hook reason",
			origin: DrainOriginSelf,
			reason: "no_work",
			want:   "session drained: agent retired itself (self: no_work)",
		},
		{
			name:   "self without reason",
			origin: DrainOriginSelf,
			want:   "session drained: agent retired itself (self)",
		},
		{
			name:   "reconciler with drain reason",
			origin: DrainOriginReconciler,
			reason: "no-wake-reason",
			want:   "session retired: reconciler reclaimed the slot (reconciler: no-wake-reason)",
		},
		{
			name:   "reconciler without reason",
			origin: DrainOriginReconciler,
			want:   "session retired: reconciler reclaimed the slot (reconciler)",
		},
		{
			name:   "unrecorded origin claims neither actor",
			origin: DrainOriginUnrecorded,
			want:   "session drained: drain origin not recorded",
		},
		{
			name:   "unknown origin normalizes rather than passing through",
			origin: DrainOrigin("controller"),
			reason: "no_work",
			want:   "session drained: drain origin not recorded",
		},
	}
	for _, tc := range tests {
		got := DrainedCloseReason(tc.origin, tc.reason)
		if got != tc.want {
			t.Errorf("%s: DrainedCloseReason(%q, %q) = %q, want %q", tc.name, tc.origin, tc.reason, got, tc.want)
		}
	}
	// Cross-check the pinned literals so a future edit that collapses two
	// branches onto one string fails here even if both tables are updated
	// together. Keyed on the NORMALIZED origin, not the row name: the two
	// unrecorded rows are meant to render identically, and only differing
	// origins owe a differing string.
	seen := make(map[string]DrainOrigin, len(tests))
	for _, tc := range tests {
		origin := NormalizeDrainOrigin(string(tc.origin))
		if prior, dup := seen[tc.want]; dup && prior != origin {
			t.Errorf("origins %q and %q render the same close reason %q; the two must stay distinguishable", prior, origin, tc.want)
		}
		seen[tc.want] = origin
	}
}

// TestDrainedCloseReasonMeetsValidatorThreshold pins the 20-character floor bd
// enforces under validation.on-close=error. A reason-less origin is the short
// case, so the floor is checked with the reason omitted rather than with a
// representative reason that would pad every branch past it.
func TestDrainedCloseReasonMeetsValidatorThreshold(t *testing.T) {
	for _, origin := range []DrainOrigin{DrainOriginUnrecorded, DrainOriginSelf, DrainOriginReconciler} {
		got := DrainedCloseReason(origin, "")
		if trimmed := strings.TrimSpace(got); len(trimmed) < 20 {
			t.Errorf("DrainedCloseReason(%q, \"\") = %q (%d trimmed chars); want >=20", origin, got, len(trimmed))
		}
	}
}

// TestCanonicalDrainedCloseReasonNamesNoActor pins that the state-code-driven
// fallback stopped naming the reconciler. CanonicalCloseReason is reached by
// every close that lands in state "drained" without an origin -- the pool-slot
// free path, legacy beads -- and its old text asserted a decision it had no
// evidence for.
func TestCanonicalDrainedCloseReasonNamesNoActor(t *testing.T) {
	got := CanonicalCloseReason("drained")
	for _, actor := range []string{"reconciler", "agent", "self"} {
		if strings.Contains(got, actor) {
			t.Errorf("CanonicalCloseReason(\"drained\") = %q; must not name %q -- the state code alone does not identify the actor", got, actor)
		}
	}
	if got == DrainedCloseReason(DrainOriginSelf, "") || got == DrainedCloseReason(DrainOriginReconciler, "") {
		t.Errorf("CanonicalCloseReason(\"drained\") = %q collides with an origin-specific reason", got)
	}
}

func TestNormalizeDrainOriginRejectsUnknownValues(t *testing.T) {
	for raw, want := range map[string]DrainOrigin{
		"self":         DrainOriginSelf,
		"  self  ":     DrainOriginSelf,
		"reconciler":   DrainOriginReconciler,
		"":             DrainOriginUnrecorded,
		"Self":         DrainOriginUnrecorded,
		"controller":   DrainOriginUnrecorded,
		"agent":        DrainOriginUnrecorded,
		"self:no_work": DrainOriginUnrecorded,
	} {
		if got := NormalizeDrainOrigin(raw); got != want {
			t.Errorf("NormalizeDrainOrigin(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestDrainAckOriginPatchClearsStaleMarkers pins that the patch writes both keys
// unconditionally. Omitting a key on an empty value would leave a previous
// incarnation's origin on a reused bead, which is worse than no record: it names
// the wrong actor with full confidence.
func TestDrainAckOriginPatchClearsStaleMarkers(t *testing.T) {
	merged := DrainAckOriginPatch(DrainOriginUnrecorded, "").Apply(map[string]string{
		DrainOriginMetadataKey:    string(DrainOriginReconciler),
		DrainAckReasonMetadataKey: "config-drift",
	})
	if got := merged[DrainOriginMetadataKey]; got != "" {
		t.Errorf("drain_origin = %q, want cleared", got)
	}
	if got := merged[DrainAckReasonMetadataKey]; got != "" {
		t.Errorf("drain_ack_reason = %q, want cleared", got)
	}

	set := DrainAckOriginPatch(DrainOriginSelf, "  no_work  ")
	if got := set[DrainOriginMetadataKey]; got != "self" {
		t.Errorf("drain_origin = %q, want %q", got, "self")
	}
	if got := set[DrainAckReasonMetadataKey]; got != "no_work" {
		t.Errorf("drain_ack_reason = %q, want %q (trimmed)", got, "no_work")
	}
}
