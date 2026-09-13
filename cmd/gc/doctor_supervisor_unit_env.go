package main

// cmd/gc/doctor_supervisor_unit_env.go
//
// The gate behind supervisor_service_env.go: it fails when the installed
// systemd unit's Environment set disagrees with the declaration the install
// rendered it from.
//
// Why a set comparison rather than named checks: the loss this catches is an
// Environment line that stopped being emitted, and the ones that matter are
// the ones nobody thought to list. Both sides here come from a single
// producer -- supervisorServiceData.EnvLines, written out by
// writeSupervisorServiceEnvDeclaration and read back by
// parseSupervisorUnitEnvironment -- so a key that exists at all is compared,
// and there is no list to keep current.

import (
	"fmt"
	"os"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/doctor"
)

// supervisorUnitEnvDriftCheck compares an installed systemd unit against the
// service environment declaration.
//
// Absent on purpose: the launchd half. On macOS the service file is a plist
// and its EnvironmentVariables dict would need a second artifact parser to
// read back; the install records the declaration on both platforms, but only
// systemd units are gated. Adding the plist side means writing that parser,
// not relaxing anything here -- and until it exists, a drifted plist is
// caught by nothing.
type supervisorUnitEnvDriftCheck struct {
	// unitPath is the installed systemd unit. Empty, or pointing at a file
	// that does not exist, means no gc-installed unit to compare.
	unitPath string
	// declPath is the service environment declaration under GC_HOME.
	declPath string
}

func newSupervisorUnitEnvDriftCheck(unitPath, declPath string) *supervisorUnitEnvDriftCheck {
	return &supervisorUnitEnvDriftCheck{unitPath: unitPath, declPath: declPath}
}

// newSupervisorUnitEnvDriftCheckForHost builds the check against this host's
// real unit and declaration paths, and against nothing at all off Linux,
// where there is no systemd unit to read.
func newSupervisorUnitEnvDriftCheckForHost() *supervisorUnitEnvDriftCheck {
	if goruntime.GOOS != "linux" {
		return newSupervisorUnitEnvDriftCheck("", "")
	}
	return newSupervisorUnitEnvDriftCheck(supervisorSystemdServicePath(), supervisorServiceEnvFilePath())
}

func (c *supervisorUnitEnvDriftCheck) Name() string { return "supervisor-unit-env-drift" }

// CanFix reports false. The only repair for this finding is a reinstall, and
// `gc supervisor install` warm-refreshes a running supervisor -- a stop and
// start that bounces every managed session. `gc doctor --fix` runs unattended
// from the health patrol, so this stays a finding an operator acts on.
func (c *supervisorUnitEnvDriftCheck) CanFix() bool { return false }

func (c *supervisorUnitEnvDriftCheck) Fix(_ *doctor.CheckContext) error { return nil }

// WarmupEligible reports false. `gc start` regenerates the service file and
// records the declaration on its way up, so a warm-up run of this check
// observes the pair install just wrote and can only ever be green. Its value
// is on demand, against a unit some later install or hand edit changed.
func (c *supervisorUnitEnvDriftCheck) WarmupEligible() bool { return false }

func (c *supervisorUnitEnvDriftCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	if strings.TrimSpace(c.unitPath) == "" {
		return okCheck(c.Name(), "no gc-installed systemd unit on this platform")
	}
	raw, err := os.ReadFile(c.unitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return okCheck(c.Name(), fmt.Sprintf("no gc-installed systemd unit at %s", c.unitPath))
		}
		return errorCheck(c.Name(),
			fmt.Sprintf("reading the supervisor unit %s: %v", c.unitPath, err),
			"make the unit readable, then rerun `gc doctor`", nil)
	}
	unitEnv, err := parseSupervisorUnitEnvironment(string(raw))
	if err != nil {
		return errorCheck(c.Name(),
			fmt.Sprintf("the supervisor unit %s has an unreadable environment: %v", c.unitPath, err),
			"repair the Environment= lines, then rerun `gc supervisor install`", nil)
	}
	declared, err := loadSupervisorServiceEnvDeclarationAt(c.declPath)
	if err != nil {
		return errorCheck(c.Name(), err.Error(),
			"fix or delete the declaration, then rerun `gc supervisor install`", nil)
	}
	if declared == nil {
		return warnCheck(c.Name(),
			fmt.Sprintf("%s predates the service environment declaration, so its %d Environment line(s) are ungated",
				c.unitPath, len(unitEnv)),
			"run `gc supervisor install` to record what the unit carries",
			nil)
	}
	if details := supervisorUnitEnvDifferences(unitEnv, declared); len(details) > 0 {
		return errorCheck(c.Name(),
			fmt.Sprintf("the supervisor unit %s disagrees with %s on %d environment key(s)",
				c.unitPath, c.declPath, len(details)),
			"rerun `gc supervisor install` to regenerate the unit from the declaration, "+
				"or edit the declaration if the unit is right",
			details)
	}
	return okCheck(c.Name(),
		fmt.Sprintf("the supervisor unit matches its %d declared environment key(s)", len(declared)))
}

// supervisorUnitEnvDifferences reports how the unit and the declaration
// disagree, one sorted line per key.
//
// Values are named only as "differs": both sides carry provider credentials,
// and doctor output gets pasted into beads, mail and terminal scrollback. The
// key name is enough to act on and the value is never the thing in doubt.
func supervisorUnitEnvDifferences(unitEnv, declared map[string]string) []string {
	keys := make([]string, 0, len(unitEnv)+len(declared))
	seen := make(map[string]bool, cap(keys))
	for key := range unitEnv {
		seen[key] = true
		keys = append(keys, key)
	}
	for key := range declared {
		if !seen[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	var out []string
	for _, key := range keys {
		unitValue, inUnit := unitEnv[key]
		declValue, inDecl := declared[key]
		switch {
		case inUnit && !inDecl:
			out = append(out, key+": present in the unit, not declared")
		case !inUnit && inDecl:
			out = append(out, key+": declared, missing from the unit")
		case unitValue != declValue:
			out = append(out, key+": declared and installed values differ")
		}
	}
	return out
}
