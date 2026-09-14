package main

// cmd/gc/doctor_supervisor_unit_optin.go
//
// The gate on the opt-in channel: it fails when a key the operator named in
// ${GC_HOME}/secrets.env's GC_SUPERVISOR_ENV is not reaching the service
// children.
//
// Why it reads the RUNNING supervisor's environment and not just the unit
// file: a service child inherits from the supervisor process, so the unit is
// only a prediction about the next start. `gc supervisor install` writes a
// correct unit and the bridge stays dead until something restarts the
// supervisor, and that window is exactly where the ci-cblj0v outage lived --
// a gate satisfied by the unit alone counts what is PRESENT rather than what
// was CONSUMED, and reports success for the whole of it. The unit is still
// read, because it separates the two remedies: a key missing from both needs
// a reinstall, a key in the unit but not in the process needs a restart, and
// telling an operator to reinstall when the unit is already right wastes the
// most expensive action in this city.
//
// Why it is separate from supervisor-unit-env-drift rather than a branch of
// it: that check compares the unit against the declaration, and both sides
// are written by one install from one producer, so a thin render produces a
// thin pair and it reports agreement. The opt-in list is operator-authored
// and no install rewrites it, which is the only reason this gate can fail
// where that one cannot.
//
// Absent on purpose: the shell's own GC_SUPERVISOR_ENV, the other channel
// supervisorServiceOptInKeys reads. A doctor check must return the same
// verdict for the unattended health patrol and for an operator at a terminal,
// and a shell-sourced input cannot -- the patrol would see an empty opt-in
// list and pass over a key the operator can see is missing. A shell-only
// opt-in is a per-install choice by construction and stays ungated.
//
// This gate is NOT the reason the ci-cblj0v outage ran for a day. The city's
// own doctor/slack-bridge-env-keys check was already ERROR throughout it,
// naming all three keys and the supervisor PID. Nothing read it: the
// doctor-findings sweep had not run since 2026-09-10 (filed separately). A
// check nobody collects is worth nothing however sharp, and adding this one
// does not fix that.

import (
	"fmt"
	"os"
	goruntime "runtime"
	"sort"
	"strings"

	"github.com/gastownhall/gascity/internal/doctor"
)

// supervisorUnitOptInCheck compares the durable GC_SUPERVISOR_ENV opt-in list
// against what the supervisor actually carries.
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
	// pid is the running supervisor, or 0 when it is not running or liveness
	// was established without the control socket. Zero degrades the check to
	// the unit file and says so, rather than passing.
	pid int
	// procRoot is "/proc" outside tests.
	procRoot string
}

func newSupervisorUnitOptInCheck(unitPath, secretsPath string, pid int, procRoot string) *supervisorUnitOptInCheck {
	return &supervisorUnitOptInCheck{unitPath: unitPath, secretsPath: secretsPath, pid: pid, procRoot: procRoot}
}

// newSupervisorUnitOptInCheckForHost builds the check against this host's real
// unit, secrets and proc paths, and against nothing at all off Linux, where
// there is neither a systemd unit nor a /proc to read.
func newSupervisorUnitOptInCheckForHost(pid int) *supervisorUnitOptInCheck {
	if goruntime.GOOS != "linux" {
		return newSupervisorUnitOptInCheck("", "", 0, "")
	}
	return newSupervisorUnitOptInCheck(
		supervisorSystemdServicePath(), supervisorSecretsEnvFilePath(), pid, "/proc")
}

func (c *supervisorUnitOptInCheck) Name() string { return "supervisor-unit-optin-dropped" }

// CanFix reports false. Neither repair is safe unattended: `gc supervisor
// install` warm-refreshes a running supervisor, and a restart stops every
// managed city. `gc doctor --fix` runs from the health patrol, so this stays
// a finding an operator acts on.
func (c *supervisorUnitOptInCheck) CanFix() bool { return false }

func (c *supervisorUnitOptInCheck) Fix(_ *doctor.CheckContext) error { return nil }

// WarmupEligible reports true, unlike the drift check beside it. That one can
// only ever be green during warm-up because `gc start` writes both sides of
// its comparison on the way up; this one's opt-in side is operator-authored
// and its process side is the supervisor that start just produced, so a start
// that came up without an opted-in key is caught then rather than whenever
// someone next runs `gc doctor` by hand.
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

	unitEnv, unitErr := c.readUnitEnvironment()
	if unitErr != nil {
		return errorCheck(c.Name(), unitErr.Error(),
			"repair the unit, then rerun `gc supervisor install`", nil)
	}
	procEnv, procErr := c.readSupervisorEnvironment()

	// No running supervisor to measure. The unit is a prediction about the
	// next start and is reported as one: passing here would hide a short unit
	// until someone happened to look while the supervisor was up.
	if procEnv == nil {
		details := supervisorUnitOptInGaps(optIn, unitEnv, nil, entries)
		if len(details) == 0 {
			return okCheck(c.Name(), fmt.Sprintf(
				"the supervisor unit carries all %d opted-in key(s); the supervisor is not running, "+
					"so nothing was measured against a live process%s", len(optIn), procNote(procErr)))
		}
		return errorCheck(c.Name(),
			fmt.Sprintf("the supervisor unit %s is missing %d of the %d key(s) %s opts in",
				c.unitPath, len(details), len(optIn), c.secretsPath),
			"run `gc supervisor install` to regenerate the unit", details)
	}

	if details := supervisorUnitOptInGaps(optIn, unitEnv, procEnv, entries); len(details) > 0 {
		return errorCheck(c.Name(),
			fmt.Sprintf("supervisor pid %d is missing %d of the %d key(s) %s opts in, "+
				"so no service child inherits them", c.pid, len(details), len(optIn), c.secretsPath),
			"regenerate the unit with `gc supervisor install` where the detail says reinstall, "+
				"then have the operator restart the supervisor -- it stops every managed city, "+
				"so it is never an agent's to take",
			details)
	}
	return okCheck(c.Name(),
		fmt.Sprintf("supervisor pid %d carries all %d opted-in key(s)", c.pid, len(optIn)))
}

// procNote explains an unreadable environ without making it a finding. A
// supervisor reported as not running, and one whose environ cannot be read,
// are different states and an operator chasing the second needs to know which.
func procNote(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

// readUnitEnvironment parses the installed unit's Environment lines. A unit
// that is not there yields an empty set rather than an error: a city whose
// supervisor runs detached has no unit, and that is a legitimate state the
// process-side comparison still covers.
func (c *supervisorUnitOptInCheck) readUnitEnvironment() (map[string]string, error) {
	raw, err := os.ReadFile(c.unitPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("reading the supervisor unit %s: %w", c.unitPath, err)
	}
	env, err := parseSupervisorUnitEnvironment(string(raw))
	if err != nil {
		return nil, fmt.Errorf("the supervisor unit %s has an unreadable environment: %w", c.unitPath, err)
	}
	return env, nil
}

// readSupervisorEnvironment returns the KEY NAMES the running supervisor
// carries, as a set with empty values. Nil means there was nothing to read.
//
// Values are discarded at the point of parsing rather than filtered later.
// This reads a process that holds every credential the fleet uses, and a value
// that is never put in a map cannot be printed by a later edit to the
// reporting code.
func (c *supervisorUnitOptInCheck) readSupervisorEnvironment() (map[string]string, error) {
	if c.pid <= 0 || c.procRoot == "" {
		return nil, nil
	}
	path := fmt.Sprintf("%s/%d/environ", c.procRoot, c.pid)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read %s: %w", path, err)
	}
	out := make(map[string]string)
	for _, entry := range strings.Split(string(raw), "\x00") {
		key, _, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		out[key] = ""
	}
	return out, nil
}

// supervisorUnitOptInGaps reports each opted-in key that is not reaching the
// service children, one sorted line per key, naming which remedy it needs.
//
// procEnv nil means no live process was measured and unitEnv is the subject.
// Otherwise procEnv is the subject and unitEnv only classifies the remedy: a
// key the unit already carries needs a restart, not another reinstall.
//
// Values are never named. This output gets pasted into beads, mail and
// terminal scrollback, and every key here is a credential by construction --
// the list exists to carry the ones gc will not persist by default.
func supervisorUnitOptInGaps(optIn []string, unitEnv, procEnv, secrets map[string]string) []string {
	subject := procEnv
	if subject == nil {
		subject = unitEnv
	}
	var out []string
	for _, key := range optIn {
		if _, ok := subject[key]; ok {
			continue
		}
		switch {
		case procEnv == nil && strings.TrimSpace(secrets[key]) == "":
			out = append(out, key+": opted in, but no value on file to emit")
		case procEnv == nil:
			out = append(out, key+": opted in with a value on file, missing from the unit -- reinstall")
		default:
			if _, inUnit := unitEnv[key]; inUnit {
				out = append(out, key+": in the unit but not in the running supervisor -- restart")
				continue
			}
			if strings.TrimSpace(secrets[key]) == "" {
				out = append(out, key+": opted in, but no value on file to emit")
				continue
			}
			out = append(out, key+": missing from the unit and the running supervisor -- reinstall, then restart")
		}
	}
	sort.Strings(out)
	return out
}
