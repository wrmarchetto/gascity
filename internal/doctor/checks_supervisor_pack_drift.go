package doctor

import (
	"fmt"

	"github.com/gastownhall/gascity/internal/builtinpacks"
)

// SupervisorPackDriftCheck reports when the running supervisor executes
// bundled pack content that differs from the gc binary now on disk.
//
// Bundled packs are embedded in the gc binary and materialized into
// ~/.gc/cache/repos/<hash>/, keyed by builtinpacks.SyntheticCacheKeyComponent
// so that two binaries with different embedded pack content never overwrite
// one shared cache directory. That key is what keeps a mixed-version deploy
// from wedging, and it is deliberate — see SyntheticCacheKeyComponent. The
// cost it leaves behind is this check's subject: a supervisor that outlives
// a `go install` keeps resolving PACK_DIR to the cache directory ITS binary
// materialized, so every exec order runs that older script while
// `gc order show` — a fresh process on the new binary — reports the new
// cache directory. Nothing else in gc contrasts the two, and the divergence
// is invisible from either side alone.
//
// The failure it was written for (ci-aeaqrz): the renudge-stale-human-gates
// order kept running its pre-fix copy for 19 hours after the fix landed,
// dropping every repeat reminder for an open human gate. The order-firing
// doctor check saw only a nonzero exit, and the operator reading
// `gc order show` saw the fixed script.
//
// Deliberately NOT keyed on the gc binary's mtime or build id. A rebuild
// that leaves internal/bootstrap/packs/ untouched changes neither the
// executed scripts nor `gc order show`, and this city rebuilds gc several
// times a day: a check that fired on every rebuild would be yellow
// permanently and read as noise.
type SupervisorPackDriftCheck struct {
	supervisorRunning bool
	supervisorPID     int
	reportedHash      func() (string, bool)
	localHash         func() string
}

// NewSupervisorPackDriftCheck returns a check that contrasts the running
// supervisor's bundled-pack content hash against this binary's.
// supervisorRunning and supervisorPID come from the control-socket probe;
// reportedHash asks the running supervisor over that same socket and reports
// ok=false when it does not answer.
func NewSupervisorPackDriftCheck(supervisorRunning bool, supervisorPID int, reportedHash func() (string, bool)) *SupervisorPackDriftCheck {
	return &SupervisorPackDriftCheck{
		supervisorRunning: supervisorRunning,
		supervisorPID:     supervisorPID,
		reportedHash:      reportedHash,
		localHash:         builtinpacks.SyntheticCacheKeyComponent,
	}
}

// Name returns the check identifier.
func (c *SupervisorPackDriftCheck) Name() string { return "supervisor-pack-drift" }

// CanFix reports that this check does not support automatic remediation.
// Restarting the supervisor stops every managed city, so it stays an
// operator decision and must NEVER run unattended from `gc doctor --fix`.
func (c *SupervisorPackDriftCheck) CanFix() bool { return false }

// Fix is a no-op; CanFix returns false.
func (c *SupervisorPackDriftCheck) Fix(_ *CheckContext) error { return nil }

// restartHint is the operator's remedy. It stops every managed city, which
// is why it is a hint and not a Fix.
const restartHint = "restart the supervisor to adopt this binary's packs: `gc supervisor stop && gc supervisor start` (stops every managed city — operator only)"

// Run contrasts the supervisor's bundled-pack content hash with this
// binary's.
func (c *SupervisorPackDriftCheck) Run(_ *CheckContext) *CheckResult {
	r := &CheckResult{Name: c.Name()}
	if !c.supervisorRunning {
		r.Status = StatusOK
		r.Message = "supervisor not running — bundled pack drift check skipped"
		return r
	}

	local := c.localHash()
	if local == "" {
		// SyntheticCacheKeyComponent returns "" only when this binary's own
		// embedded pack set cannot be hashed. That is a build-integrity
		// failure, and reporting it as agreement would hide it.
		r.Status = StatusError
		r.Message = "cannot hash this binary's bundled packs — pack drift against the running supervisor cannot be established"
		return r
	}

	reported, ok := c.reportedHash()
	if !ok {
		r.Status = StatusError
		r.Message = fmt.Sprintf("supervisor %s does not report its bundled pack content, so it predates this check and is NOT the gc binary now on disk; its exec orders run its own older pack scripts while `gc order show` reports this binary's", supervisorLabel(c.supervisorPID))
		r.FixHint = restartHint
		return r
	}
	if reported != local {
		r.Status = StatusError
		r.Message = fmt.Sprintf("supervisor %s runs bundled pack content %s; this gc binary has %s — its exec orders run pack scripts that `gc order show` does not report", supervisorLabel(c.supervisorPID), reported, local)
		r.FixHint = restartHint
		return r
	}

	r.Status = StatusOK
	r.Message = fmt.Sprintf("supervisor runs this binary's bundled pack content (%s)", local)
	return r
}

// supervisorLabel names the supervisor with its PID when the socket probe
// recovered one. The service-manager and API liveness fallbacks in
// supervisorStatusWithOptions confirm a supervisor without a PID, so the
// message must stay readable at zero.
func supervisorLabel(pid int) string {
	if pid <= 0 {
		return "(PID unavailable)"
	}
	return fmt.Sprintf("(PID %d)", pid)
}
