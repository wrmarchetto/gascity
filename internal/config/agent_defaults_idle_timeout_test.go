// Scope: [agent_defaults] idle_timeout -- the city-wide inheritance of the
// per-agent idle timeout, and only that. The reaper this timeout arms is
// pinned in cmd/gc (see TestReconcileSessionBeads_DrainAckAssignedWorkWedge*),
// and duration-string validity belongs to validate_durations_test.go; neither
// is re-tested here.
//
// Why this suite exists: ci-07ebae. A drain-ack refused for assigned work
// leaves a live session believing it retired, and the only mechanism that
// ends that wedge unconditionally is the idle-timeout reaper. Arming it needs
// ONE setting -- this city carries 126 agents and zero [[patches]], so a
// per-agent idle_timeout is not a change anybody can make or keep correct,
// and a newly added agent would silently have no bound at all.
//
// Run: go test ./internal/config/ -run TestAgentDefaultsIdleTimeout

package config

import "testing"

func TestAgentDefaultsIdleTimeout_ExplicitAgentInherits(t *testing.T) {
	cfg := &City{
		Agents:        []Agent{{Name: "worker"}},
		AgentDefaults: AgentDefaults{IdleTimeout: "90m"},
	}
	ApplyAgentDefaults(cfg)

	if got := cfg.Agents[0].IdleTimeout; got != "90m" {
		t.Fatalf("IdleTimeout = %q, want 90m", got)
	}
}

func TestAgentDefaultsIdleTimeout_ExplicitOverrideWins(t *testing.T) {
	cfg := &City{
		Agents:        []Agent{{Name: "worker", IdleTimeout: "5m"}},
		AgentDefaults: AgentDefaults{IdleTimeout: "90m"},
	}
	ApplyAgentDefaults(cfg)

	if got := cfg.Agents[0].IdleTimeout; got != "5m" {
		t.Fatalf("IdleTimeout = %q, want the agent's own 5m", got)
	}
}

// The control dispatcher is `gc convoy control --serve --follow`: a process
// that is SUPPOSED to sit quiet between control beads, so pane activity is a
// meaningless liveness signal for it and an idle timeout would reap the one
// component that drives every formula. Every sibling default in
// ApplyAgentDefaults skips it for its own reasons; this one skips it because
// inheriting would be actively wrong, not merely useless.
func TestAgentDefaultsIdleTimeout_ControlDispatcherSkipped(t *testing.T) {
	cfg := &City{
		Agents:        []Agent{{Name: ControlDispatcherAgentName}},
		AgentDefaults: AgentDefaults{IdleTimeout: "90m"},
	}
	ApplyAgentDefaults(cfg)

	if got := cfg.Agents[0].IdleTimeout; got != "" {
		t.Fatalf("control dispatcher IdleTimeout = %q, want unset", got)
	}
}

// An absent default must leave every agent exactly as configured. Without
// this, a zero-value AgentDefaults that wrote "" over an agent's own timeout
// would disarm the reaper for the one agent that had asked for it, and the
// two tests above would both still pass.
func TestAgentDefaultsIdleTimeout_UnsetDefaultChangesNothing(t *testing.T) {
	cfg := &City{
		Agents: []Agent{{Name: "bare"}, {Name: "own", IdleTimeout: "2h"}},
	}
	ApplyAgentDefaults(cfg)

	if got := cfg.Agents[0].IdleTimeout; got != "" {
		t.Errorf("bare agent IdleTimeout = %q, want unset", got)
	}
	if got := cfg.Agents[1].IdleTimeout; got != "2h" {
		t.Errorf("own agent IdleTimeout = %q, want 2h", got)
	}
}
