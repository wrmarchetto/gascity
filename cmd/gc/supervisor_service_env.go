package main

// cmd/gc/supervisor_service_env.go
//
// The supervisor service file's environment, as a declaration rather than a
// side effect of whichever shell last ran an install.
//
// Why this file has this shape: `gc supervisor start` regenerates the service
// file on every invocation (see the comment above its doSupervisorInstall
// call), so the unit's environment used to be whatever the calling shell
// happened to export. A start from a lean environment -- an agent session, a
// cron job, a bare ssh -- silently rewrote the operator's unit with fewer
// Environment lines, and the supervisor came back healthy with a capability
// quietly gone. Recording the resolved set and reading it back on the next
// install turns regeneration into reproduction.
//
// The set is produced in exactly one place, supervisorServiceData.EnvLines.
// Both service templates render from it and the install records it verbatim,
// so the doctor gate in doctor_supervisor_unit_env.go can compare a live unit
// against the declaration without a second list of expected keys. A
// hand-kept list of keys-not-to-forget is the failure gs-cbhj was written
// against; there is deliberately none here.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/supervisor"
)

// supervisorServiceEnvFileName is the declaration under GC_HOME that records
// the environment the last install rendered into the service file.
//
// It sits beside secrets.env rather than inside supervisor.toml, which was
// the obvious candidate: install has to WRITE this file, and supervisor.toml
// is hand-authored machine state whose comments carry the reasoning for the
// dashboard bind, the write-auth acknowledgement and the formula-ref pin. A
// TOML round-trip through an encoder drops every one of them.
const supervisorServiceEnvFileName = "service-env.toml"

// supervisorServiceEnvDeclarationHeader is prepended to the generated file.
// The remedy for an unwanted key is an edit here, not a flag: install unions
// this file with the live environment and never subtracts, so a key stays
// until someone deletes its line.
const supervisorServiceEnvDeclarationHeader = `# Environment the last 'gc supervisor install' rendered into the supervisor
# service file. Generated -- edit only to REMOVE a key you no longer want the
# service to carry, then rerun 'gc supervisor install'.
#
# Install reads this file and unions it with the calling shell, so a start
# from an environment that exports none of these reproduces them instead of
# dropping them. The shell wins on any key it does set; this file only fills
# what the shell left unset, and it can never widen which keys are allowed to
# persist (that gate is shouldPersistSupervisorEnv).
#
# 'gc doctor' fails when the installed unit disagrees with this file.
`

// supervisorServiceEnvDeclaration is the on-disk shape of the declaration.
type supervisorServiceEnvDeclaration struct {
	Env map[string]string `toml:"env"`
}

// supervisorServiceEnvFilePath returns the absolute path to the service
// environment declaration (${GC_HOME}/service-env.toml).
func supervisorServiceEnvFilePath() string {
	return filepath.Join(supervisor.DefaultHome(), supervisorServiceEnvFileName)
}

// EnvLines returns the complete, ordered environment the service file
// declares: the fixed entries the templates used to spell out one by one,
// then the resolved extras in their already-sorted order.
//
// This is the single producer of that set. The fixed entries are named here
// because something has to name them; what matters is that nothing else
// does, so the declaration, both templates and the doctor gate cannot drift
// apart from each other.
//
// Order is the order the templates emitted before this method existed, and
// supervisor_service_env_test.go pins the rendered bytes: installSupervisor-
// Systemd treats any content change as cause for a warm refresh, so a
// reordering here would bounce every managed session on the next install.
func (d *supervisorServiceData) EnvLines() []supervisorServiceEnvVar {
	out := make([]supervisorServiceEnvVar, 0, len(d.ExtraEnv)+4)
	out = append(out, supervisorServiceEnvVar{Name: "GC_HOME", Value: d.GCHome})
	if d.XDGRuntimeDir != "" {
		out = append(out, supervisorServiceEnvVar{Name: "XDG_RUNTIME_DIR", Value: d.XDGRuntimeDir})
	}
	out = append(out, supervisorServiceEnvVar{Name: "PATH", Value: d.Path})
	out = append(out, supervisorServiceEnvVar{Name: supervisorPreserveSessionsOnSignalEnv, Value: "1"})
	out = append(out, d.ExtraEnv...)
	return out
}

// loadSupervisorServiceEnvDeclaration reads the declaration for the current
// GC_HOME. A missing file returns (nil, nil) -- the state of every machine
// before its first install under this change, and of every fresh install.
func loadSupervisorServiceEnvDeclaration() (map[string]string, error) {
	return loadSupervisorServiceEnvDeclarationAt(supervisorServiceEnvFilePath())
}

// loadSupervisorServiceEnvDeclarationAt reads the declaration at path.
//
// A malformed file is an error, deliberately unlike secrets.env, which warns
// and continues. A secrets file that fails to parse costs one credential; a
// declaration that fails to parse drops EVERY recorded key out of the
// regenerated unit, which is the exact silent loss this file exists to
// prevent. Callers fail closed on it.
func loadSupervisorServiceEnvDeclarationAt(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading supervisor service env declaration %s: %w", path, err)
	}
	var decl supervisorServiceEnvDeclaration
	if err := toml.Unmarshal(data, &decl); err != nil {
		return nil, fmt.Errorf("parsing supervisor service env declaration %s: %w "+
			"(fix or delete the file, then rerun 'gc supervisor install')", path, err)
	}
	if len(decl.Env) == 0 {
		return map[string]string{}, nil
	}
	return decl.Env, nil
}

// writeSupervisorServiceEnvDeclaration records env as the declaration for the
// current GC_HOME. It is written at the same 0600 as the service file itself:
// both carry provider credential values.
func writeSupervisorServiceEnvDeclaration(env []supervisorServiceEnvVar) error {
	path := supervisorServiceEnvFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating GC_HOME for %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+supervisorServiceEnvFileName+".*")
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // best-effort cleanup of an already-renamed or failed temp
	if _, err := tmp.WriteString(renderSupervisorServiceEnvDeclaration(env)); err != nil {
		tmp.Close() //nolint:errcheck // the write error is the one worth reporting
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Chmod(supervisorServiceFileMode); err != nil {
		tmp.Close() //nolint:errcheck // the chmod error is the one worth reporting
		return fmt.Errorf("setting mode on %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s into place: %w", path, err)
	}
	return nil
}

// renderSupervisorServiceEnvDeclaration formats env as the declaration file's
// contents. Keys are sorted rather than emitted in EnvLines order: the file
// is compared against itself across installs, and an ordering that tracked
// the templates would show a diff every time an extra key appeared.
func renderSupervisorServiceEnvDeclaration(env []supervisorServiceEnvVar) string {
	values := make(map[string]string, len(env))
	keys := make([]string, 0, len(env))
	for _, item := range env {
		if _, seen := values[item.Name]; !seen {
			keys = append(keys, item.Name)
		}
		values[item.Name] = item.Value
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(supervisorServiceEnvDeclarationHeader)
	b.WriteString("\n[env]\n")
	for _, key := range keys {
		fmt.Fprintf(&b, "%s = %s\n", key, strconv.Quote(values[key]))
	}
	return b.String()
}

// parseSupervisorUnitEnvironment extracts the Environment= assignments from a
// systemd unit. It is the read side of the same set EnvLines produces: the
// doctor gate compares what it returns against the declaration.
//
// Not handled, because the writer never emits it: systemd's multiple-
// assignments-per-line form (Environment=A=1 B=2) and its single-quoted
// values. A hand-edited unit using either parses as one oddly-valued entry,
// which fails the gate rather than passing it -- the safe direction, and the
// reason this stays a line parser instead of a systemd lexer.
func parseSupervisorUnitEnvironment(unit string) (map[string]string, error) {
	const prefix = "Environment="
	out := make(map[string]string)
	for _, line := range strings.Split(unit, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		assignment := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		name, value, ok := strings.Cut(assignment, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("unit line %q is not a NAME=VALUE assignment", line)
		}
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return nil, fmt.Errorf("unit line %q has an unreadable quoted value: %w", line, err)
			}
			value = unquoted
		}
		out[name] = value
	}
	return out, nil
}
