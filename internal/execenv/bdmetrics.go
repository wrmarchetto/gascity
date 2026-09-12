package execenv

import "strings"

// BDMetricsDisableEnv is bd's own telemetry opt-out. bd checks it before any
// config file (resolveMetricsEnabled in beads cmd/bd/main.go), which is what
// makes it the right carrier here: it is the one consent signal that survives
// a rewritten HOME.
const BDMetricsDisableEnv = "BD_DISABLE_METRICS"

// BDMetricsDisabledEntry is the canonical child-environment assignment.
const BDMetricsDisabledEntry = BDMetricsDisableEnv + "=1"

// WithBDMetricsDefaultedOff returns a copy of environ carrying a bd telemetry
// opt-out, and is required of every child environment that names a HOME other
// than the one gc itself inherited.
//
// bd resolves its user-global config -- the file holding the operator's
// `bd metrics off` -- from $HOME. A child whose HOME gc rewrote therefore reads
// a config the operator has never seen, finds no opt-out, falls back to bd's
// shipped default of ENABLED, and writes that default into the substitute home
// so every later run agrees with it. Measured 2026-09-12 against bd
// 1.1.1-0.20260805093327: under HOME=<city path> a plain `bd version` queues a
// cli_command event and the detached `bd send-metrics` child POSTs it to the
// configured endpoint, while the operator's own `metrics.disabled: true` sits
// unread. The reproduction is TestWithBDMetricsDefaultedOff's counterpart in
// the bead record (ci-lf9auf), not a unit test -- it needs a real bd binary.
//
// DEFAULTED off, not forced off: an inherited BD_DISABLE_METRICS is kept at
// whatever value the parent set, including "0". Rewriting HOME destroys gc's
// ability to READ consent; it does not entitle gc to overrule consent the
// parent stated explicitly. Forcing the value -- the shape
// WithUsageMetricsDisabled uses for gc's own metrics, where gc owns the
// decision outright -- would silently cancel an operator's `bd metrics on`.
//
// The rejected alternative is to hand the child the operator's real home, or
// to copy the operator's bd config into the substitute one. Both undo the
// sandbox that motivated the rewrite: ConditionEnv substitutes HOME precisely
// so a gate script cannot reach the controller's .ssh and .gnupg, and a home
// that is real enough to carry bd's config is real enough to carry those.
func WithBDMetricsDefaultedOff(environ []string) []string {
	for _, entry := range environ {
		if key, _, ok := strings.Cut(entry, "="); ok && key == BDMetricsDisableEnv {
			return environ
		}
	}
	return append(environ[:len(environ):len(environ)], BDMetricsDisabledEntry)
}
