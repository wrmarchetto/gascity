package session

import "testing"

func TestIdentityIndexResolvesPoolIdentityToBeadNamedSession(t *testing.T) {
	x := NewIdentityIndex([]Info{
		{ID: "ci-a1", AgentName: "myrig/engineer", Template: "myrig/engineer", PoolManaged: true, PoolSlot: "1", SessionNameMetadata: "engineer-ci-a1"},
		{ID: "ci-b2", AgentName: "dispatcher", Template: "dispatcher", PoolManaged: true, SessionNameMetadata: "dispatcher-ci-b2"},
	})
	for identity, want := range map[string]string{
		"myrig/engineer-1": "engineer-ci-a1",
		"dispatcher":       "dispatcher-ci-b2",
	} {
		e, ok := x.Lookup(identity)
		if !ok || e.SessionName != want {
			t.Errorf("Lookup(%q) = %+v, %v; want session %q", identity, e, ok, want)
		}
	}
	if e, ok := x.Lookup("myrig/engineer"); ok {
		t.Errorf("Lookup(pool template) = %+v, want no entry: a pool row indexes only its slotted identity", e)
	}
}

func TestIdentityIndexCanonicalNamedSessionOverridesEarlierRow(t *testing.T) {
	x := NewIdentityIndex([]Info{
		{ID: "ga-first", AgentName: "dup", Template: "dup", SessionNameMetadata: "s-first"},
		{ID: "ga-named", AgentName: "dup", Template: "dup", ConfiguredNamedIdentity: "dup", SessionNameMetadata: "s-named"},
		{ID: "ga-later", AgentName: "dup", Template: "dup", SessionNameMetadata: "s-later"},
	})
	if e, _ := x.Lookup("dup"); e.BeadID != "ga-named" {
		t.Errorf("Lookup(dup) = %+v, want the configured named session ga-named", e)
	}
}

func TestIdentityIndexSkipsClosedAndUnnamedRows(t *testing.T) {
	x := NewIdentityIndex([]Info{
		{ID: "ga-closed", AgentName: "gone", Template: "gone", Closed: true, SessionNameMetadata: "s-gone"},
		{ID: "ga-noname", AgentName: "nameless", Template: "nameless"},
	})
	for _, identity := range []string{"gone", "nameless"} {
		if e, ok := x.Lookup(identity); ok {
			t.Errorf("Lookup(%q) = %+v, want no entry", identity, e)
		}
	}
}

func TestIdentityIndexAgentNameWinsOverTemplateHint(t *testing.T) {
	x := NewIdentityIndex([]Info{
		{ID: "ga-hint", AgentName: "scout-a", Template: "scout", SessionNameMetadata: "s-hint"},
		{ID: "ga-agent", AgentName: "scout", Template: "other", SessionNameMetadata: "s-agent"},
	})
	if e, _ := x.Lookup("scout"); e.BeadID != "ga-agent" {
		t.Errorf("Lookup(scout) = %+v, want the agent-name entry ga-agent", e)
	}
}
