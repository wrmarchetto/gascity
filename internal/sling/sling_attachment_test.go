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
