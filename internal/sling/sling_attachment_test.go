package sling

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
)

func TestRoutedStateWarnings(t *testing.T) {
	tests := []struct {
		name string
		bead beads.Bead
		want []string
	}{
		{
			name: "clean bead has no warnings",
			bead: beads.Bead{},
			want: nil,
		},
		{
			name: "assignee only",
			bead: beads.Bead{Assignee: "rig/polecat"},
			want: []string{`warning: bead bd-1 already assigned to "rig/polecat"`},
		},
		{
			name: "routed_to only",
			bead: beads.Bead{Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "rig/polecat"}},
			want: []string{`warning: bead bd-1 already routed to "rig/polecat"`},
		},
		{
			name: "blank routed_to metadata is ignored",
			bead: beads.Bead{Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "  "}},
			want: nil,
		},
		{
			name: "pool label only",
			bead: beads.Bead{Labels: []string{"pool:builders"}},
			want: []string{`warning: bead bd-1 already has pool label "pool:builders"`},
		},
		{
			name: "non-pool labels are ignored",
			bead: beads.Bead{Labels: []string{"kind:task", "priority:high"}},
			want: nil,
		},
		{
			name: "all three states, ordered assignee then routed_to then labels",
			bead: beads.Bead{
				Assignee: "rig/polecat",
				Metadata: map[string]string{beadmeta.RoutedToMetadataKey: "rig/deacon"},
				Labels:   []string{"kind:task", "pool:builders", "pool:reviewers"},
			},
			want: []string{
				`warning: bead bd-1 already assigned to "rig/polecat"`,
				`warning: bead bd-1 already routed to "rig/deacon"`,
				`warning: bead bd-1 already has pool label "pool:builders"`,
				`warning: bead bd-1 already has pool label "pool:reviewers"`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := routedStateWarnings(tt.bead, "bd-1")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("routedStateWarnings() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCheckBeadStateExplainsPoolSlotAssignmentWillBePreserved(t *testing.T) {
	maxSessions := 3
	pool := config.Agent{Name: "workers", MaxActiveSessions: &maxSessions}
	store := beads.NewMemStoreFrom(0, []beads.Bead{{
		ID:       "GC-42",
		Status:   "open",
		Assignee: "workers-1",
	}}, nil)

	result := CheckBeadStateWithOptions(store, "GC-42", pool, SlingDeps{
		Cfg: &config.City{Agents: []config.Agent{pool}},
	}, BeadCheckOptions{})
	if len(result.Warnings) != 1 {
		t.Fatalf("Warnings = %#v, want one preservation explanation", result.Warnings)
	}
	for _, want := range []string{"workers-1", "preserve", "workers"} {
		if !strings.Contains(result.Warnings[0], want) {
			t.Fatalf("warning = %q, want %q", result.Warnings[0], want)
		}
	}
}

// TestCheckBeadStateOffersReassignRemedyForCustomSlingQuery pins that an agent
// with a custom sling_query still gets the --reassign remedy when the bead it
// is slung already carries an assignee.
//
// The invariant is the REMEDY TEXT, not warning presence. The defect state
// already returned warnings on this path -- routedStateWarnings' bare "already
// assigned to" line -- so a test asserting len(Warnings) > 0 goes green over
// it, which is how this shipped: the IsCustomSlingQuery early return in
// CheckBeadStateWithOptions sits above every actionable message, so the one
// pool in this city declaring a custom sling_query was the only one that could
// never be told what to do. A mayor slinging an already-assigned bead at it
// got two bare lines on stderr, produced the routed-and-assigned shape
// doctor/bead-preflight calls blocking, and tried setting assignee and route
// equal before unsetting the route (ci-vk76d1, 2026-09-07).
//
// The remedy is real on this path rather than aspirational:
// shouldReopenForReassign gates reopenForReassign on opts.Reassign alone and
// never on the query shape, so --reassign clears the assignee and reopens the
// bead here just as it does for a built-in route.
//
// Run: go test ./internal/sling/ -run CustomSlingQuery
func TestCheckBeadStateOffersReassignRemedyForCustomSlingQuery(t *testing.T) {
	maxSessions := 2
	// Modeled on packs/lab/agents/engineer-codex: the query stamps a SHARED
	// route that is not the agent's own identity, which is why the branches
	// below the early return cannot be reused -- every one of them compares
	// against agentutil.RoutedToIdentity, a target this query never writes.
	pool := config.Agent{
		Name:              "lab.engineer-codex",
		MaxActiveSessions: &maxSessions,
		SlingQuery:        "bd update {} --set-metadata gc.routed_to=dart/lab.engineer",
	}
	if !IsCustomSlingQuery(pool) {
		t.Fatalf("IsCustomSlingQuery = false, want true; the case under test cannot be reached")
	}
	cfg := &config.City{Agents: []config.Agent{pool}}

	warningsFor := func(assignee string, opts BeadCheckOptions) []string {
		store := beads.NewMemStoreFrom(0, []beads.Bead{{
			ID:       "GC-77",
			Status:   "open",
			Assignee: assignee,
		}}, nil)
		return CheckBeadStateWithOptions(store, "GC-77", pool, SlingDeps{Cfg: cfg}, opts).Warnings
	}

	// Both halves are asserted separately: the pre-existing state report must
	// survive the fix, and the remedy must be added. Asserting only that some
	// warning names the assignee would pass on the bare line alone.
	t.Run("assigned-without-reassign-names-the-remedy", func(t *testing.T) {
		warnings := warningsFor("human", BeadCheckOptions{})
		var sawState, sawRemedy bool
		for _, w := range warnings {
			if strings.Contains(w, `already assigned to "human"`) {
				sawState = true
			}
			if strings.Contains(w, "--reassign") && strings.Contains(w, "human") {
				sawRemedy = true
			}
		}
		if !sawState {
			t.Errorf("warnings = %#v, want one reporting the existing assignee", warnings)
		}
		if !sawRemedy {
			t.Errorf("warnings = %#v, want one naming the assignee and the --reassign remedy", warnings)
		}
	})

	// Offering --reassign to a caller who already passed it reads as the flag
	// having been ignored, so the guidance is conditional. This case is what
	// stops a fix that appends the remedy unconditionally.
	t.Run("reassign-already-requested-offers-no-remedy", func(t *testing.T) {
		warnings := warningsFor("human", BeadCheckOptions{Reassign: true})
		if joined := strings.Join(warnings, "\n"); strings.Contains(joined, "--reassign") {
			t.Fatalf("warnings = %#v, want no --reassign guidance when it was already requested", warnings)
		}
	})

	// An unassigned bead has nothing to reassign. Without this case a fix
	// keyed on IsCustomSlingQuery alone rather than on the assignee passes,
	// and every clean sling at this pool gains a spurious remedy line.
	t.Run("unassigned-bead-offers-no-remedy", func(t *testing.T) {
		warnings := warningsFor("", BeadCheckOptions{})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %#v, want none for an unassigned, unrouted bead", warnings)
		}
	})
}
