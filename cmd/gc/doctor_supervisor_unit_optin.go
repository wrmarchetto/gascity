package main

// cmd/gc/doctor_supervisor_unit_optin.go
//
// The gate on the opt-in channel: it fails when a key the operator named in
// ${GC_HOME}/secrets.env's GC_SUPERVISOR_ENV is absent from the installed
// systemd unit.
//
// Why this is separate from supervisor-unit-env-drift rather than a branch of
// it: that check compares the unit against the declaration, and both sides
// come from one producer -- an install that resolves a thin environment
// writes a thin unit AND a thin declaration, so the pair agrees and the gate
// is green over exactly the loss it was built to catch. The opt-in list is
// operator-authored and is never rewritten by an install, so it is the one
// input to this comparison that a bad install cannot move. The two checks are
// also independent in the other direction: the drift check returns early with
// a warning on a machine that predates the declaration, which is the state
// this city was in while the Slack bridge was down for a day (ci-cblj0v).
//
// Absent on purpose: the shell's own GC_SUPERVISOR_ENV, which is the other
// channel supervisorServiceOptInKeys reads. A doctor check must return the
// same verdict for the unattended health patrol and for an operator at a
// terminal, and a shell-sourced input cannot: the patrol would see an empty
// opt-in list and pass over a key the operator can see is missing. A
// shell-only opt-in is a per-install choice by construction and stays
// ungated; the durable channel is the one worth a gate, and is now the one
// that reaches the generator at all.

import (
	"fmt"
	"os"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/doctor"
)

// supervisorUnitOptInCheck compares the durable GC_SUPERVISOR_ENV opt-in list
// against the environment set of the installed systemd unit.
//
// Absent on purpose: the launchd half, for the same reason the drift check
// skips it -- reading a plist's EnvironmentVariables dict back needs a second
// artifact parser that does not exist yet.
type supervisorUnitOptInCheck struct {
	// unitPath is the installed systemd unit. Empty, or pointing at a file
	// that does not exist, means no gc-installed unit to compare.
	unitPath string
	// secretsPath is the dotenv file carrying the durable opt-in list.
	secretsPath string
}

func newSupervisorUnitOptInCheck(unitPath, secretsPath string) *supervisorUnitOptInCheck {
	return &supervisorUnitOptInCheck{unitPath: unitPath, secretsPath: secretsPath}
}

// newSupervisorUnitOptInCheckForHost builds the check against this host's real
// unit and secrets paths, and against nothing at all off Linux, where there is
// no systemd unit to read.
func newSupervisorUnitOptInCheckForHost() *supervisorUnitOptInCheck {
	if goruntime.GOOS != "linux" {
		return newSupervisorUnitOptInCheck("", "")
	}
	return newSupervisorUnitOptInCheck(supervisorSystemdServicePath(), supervisorSecretsEnvFilePath())
}

func (c *supervisorUnitOptInCheck) Name() string { return "supervisor-unit-optin-dropped" }

// CanFix reports false. The repair is `gc supervisor install`, which
// warm-refreshes a running supervisor -- a stop and start that bounces every
// managed session. `gc doctor --fix` runs unattended from the health patrol,
// so this stays a finding an operator acts on.
func (c *supervisorUnitOptInCheck) CanFix() bool { return false }

func (c *supervisorUnitOptInCheck) Fix(_ *doctor.CheckContext) error { return nil }

// WarmupEligible reports true, unlike the drift check beside it. That one can
// only ever be green during warm-up because `gc start` writes both sides of
// its comparison on the way up; this one's opt-in side is operator-authored
// and untouched by the install, so a start that resolved no value for an
// opted-in key is caught on the warm-up run rather than whenever someone next
// runs `gc doctor` by hand.
func (c *supervisorUnitOptInCheck) WarmupEligible() bool { return true }

func (c *supervisorUnitOptInCheck) Run(_ *doctor.CheckContext) *doctor.CheckResult {
	if strings.TrimSpace(c.unitPath) == "" {
		return okCheck(c.Name(), "no gc-installed systemd unit on this platform")
	}
	entries, err := supervisorSecretsEnvFileEntriesAt(c.secretsPath)
	if err != nil {
		return errorCheck(c.Name(),
			fmt.Sprintf("reading the supervisor secrets file %s: %v", c.secretsPath, err),
			"repair or remove the file, then rerun `gc doctor`", nil)
	}
	optIn := supervisorServiceExplicitEnvKeys(entries[supervisorServiceOptInEnv])
	if len(optIn) == 0 {
		return okCheck(c.Name(),
			fmt.Sprintf("%s opts no keys into the supervisor environment", c.secretsPath))
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
	if details := supervisorUnitOptInGaps(optIn, unitEnv, entries); len(details) > 0 {
		return errorCheck(c.Name(),
			fmt.Sprintf("the supervisor unit %s is missing %d of the %d key(s) %s opts in",
				c.unitPath, len(details), len(optIn), c.secretsPath),
			"run `gc supervisor install` to regenerate the unit, then ask Willie to restart "+
				"the supervisor so the services inherit it",
			details)
	}
	return okCheck(c.Name(),
		fmt.Sprintf("the supervisor unit carries all %d opted-in key(s)", len(optIn)))
}

// supervisorUnitOptInGaps reports each opted-in key the unit does not carry,
// one sorted line per key, naming whether a value is available to emit.
//
// The two cases need different remedies and are indistinguishable from the
// key name alone: a key the secrets file has a value for is a stale unit and a
// reinstall fixes it, while a key nothing supplies is a missing value and a
// reinstall would write the same short unit again. Values are never named --
// this output gets pasted into beads, mail and terminal scrollback, and the
// value is never the thing in doubt.
func supervisorUnitOptInGaps(optIn []string, unitEnv, secrets map[string]string) []string {
	var out []string
	for _, key := range optIn {
		if _, ok := unitEnv[key]; ok {
			continue
		}
		if strings.TrimSpace(secrets[key]) != "" {
			out = append(out, key+": opted in with a value on file, missing from the unit")
			continue
		}
		out = append(out, key+": opted in, but no value on file to emit")
	}
	sort.Strings(out)
	return out
}
