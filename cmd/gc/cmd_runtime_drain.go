package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/session"
	"github.com/spf13/cobra"
)

// drainOps abstracts drain signal operations for testability.
type drainOps interface {
	setDrain(sessionName string) error
	clearDrain(sessionName string) error
	isDraining(sessionName string) (bool, error)
	drainStartTime(sessionName string) (time.Time, error)
	setDrainAck(sessionName string) error
	setDrainAckWithReason(sessionName, reason string) error
	isDrainAcked(sessionName string) (bool, error)
	drainAckOrigin(sessionName string) (session.DrainOrigin, string)
	setRestartRequested(sessionName string) error
	isRestartRequested(sessionName string) (bool, error)
	clearRestartRequested(sessionName string) error
	setDriftRestart(sessionName string) error
	isDriftRestart(sessionName string) (bool, error)
	clearDriftRestart(sessionName string) error
}

// providerDrainOps implements drainOps using runtime.Provider metadata.
type providerDrainOps struct {
	sp runtime.Provider
}

type runtimeDrainCheckJSON struct {
	SchemaVersion string `json:"schema_version"`
	OK            bool   `json:"ok"`
	Command       string `json:"command"`
	Session       string `json:"session"`
	Target        string `json:"target,omitempty"`
	Draining      bool   `json:"draining"`
}

type runtimeActionJSON struct {
	SchemaVersion string `json:"schema_version"`
	OK            bool   `json:"ok"`
	Command       string `json:"command"`
	Action        string `json:"action"`
	Session       string `json:"session"`
	Target        string `json:"target,omitempty"`
	Status        string `json:"status"`
}

func (o *providerDrainOps) setDrain(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_DRAIN", strconv.FormatInt(time.Now().Unix(), 10))
}

func (o *providerDrainOps) clearDrain(sessionName string) error {
	return errors.Join(
		o.sp.RemoveMeta(sessionName, "GC_DRAIN_ACK"),
		o.sp.RemoveMeta(sessionName, reconcilerDrainAckSourceKey),
		o.sp.RemoveMeta(sessionName, reconcilerDrainAckReasonKey),
		o.sp.RemoveMeta(sessionName, reconcilerDrainAckGenerationKey),
		o.sp.RemoveMeta(sessionName, "GC_DRAIN"),
	)
}

func (o *providerDrainOps) isDraining(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRAIN")
	if err != nil {
		return false, fmt.Errorf("reading GC_DRAIN: %w", err)
	}
	return val != "", nil
}

func (o *providerDrainOps) drainStartTime(sessionName string) (time.Time, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRAIN")
	if err != nil {
		return time.Time{}, fmt.Errorf("reading GC_DRAIN: %w", err)
	}
	if val == "" {
		return time.Time{}, fmt.Errorf("GC_DRAIN not set")
	}
	unix, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parsing GC_DRAIN timestamp %q: %w", val, err)
	}
	return time.Unix(unix, 0), nil
}

func (o *providerDrainOps) setDrainAck(sessionName string) error {
	return o.setDrainAckWithReason(sessionName, "")
}

// setDrainAckWithReason is setDrainAck carrying the agent's own reason for
// retiring ("no_work", "claims_errored", "stale_session" from gc hook --claim).
// The reason rides GC_DRAIN_REASON, the same key the reconciler's own drain-ack
// uses, because the two are never both set: GC_DRAIN_ACK_SOURCE is written in
// the same call and every reader of GC_DRAIN_REASON
// (reconcilerDrainAckMatchesSession, staleReconcilerDrainAck) gates on
// source == "reconciler" first.
//
// An empty reason still REMOVES the key rather than writing "": a stale reason
// from the reconciler's own earlier drain of this same runtime would otherwise
// survive under an agent source and be reported as the agent's.
func (o *providerDrainOps) setDrainAckWithReason(sessionName, reason string) error {
	writeReason := func() error {
		if trimmed := strings.TrimSpace(reason); trimmed != "" {
			return o.sp.SetMeta(sessionName, reconcilerDrainAckReasonKey, trimmed)
		}
		return o.sp.RemoveMeta(sessionName, reconcilerDrainAckReasonKey)
	}
	return joinDrainAckMutationErrors(
		writeReason(),
		o.sp.RemoveMeta(sessionName, reconcilerDrainAckGenerationKey),
		o.sp.SetMeta(sessionName, reconcilerDrainAckSourceKey, drainAckSourceAgentValue),
		o.sp.SetMeta(sessionName, "GC_DRAIN_ACK", "1"),
	)
}

// drainAckOrigin reports who acknowledged the drain, from the runtime metadata
// the acknowledging side wrote. Readable only while the runtime still exists,
// which is why the reconciler stamps the answer onto the session bead the tick
// it observes the ack rather than re-reading it at close time.
//
// A read error is not propagated: it means the runtime is gone, so the origin
// is genuinely unrecorded here, and the close must proceed either way. The
// caller distinguishes that from a real origin by DrainOriginUnrecorded, which
// renders as "origin not recorded" instead of naming an actor.
func (o *providerDrainOps) drainAckOrigin(sessionName string) (session.DrainOrigin, string) {
	source, err := o.sp.GetMeta(sessionName, reconcilerDrainAckSourceKey)
	if err != nil {
		return session.DrainOriginUnrecorded, ""
	}
	var origin session.DrainOrigin
	switch strings.TrimSpace(source) {
	case drainAckSourceAgentValue:
		origin = session.DrainOriginSelf
	case reconcilerDrainAckSourceValue:
		origin = session.DrainOriginReconciler
	default:
		return session.DrainOriginUnrecorded, ""
	}
	reason, err := o.sp.GetMeta(sessionName, reconcilerDrainAckReasonKey)
	if err != nil {
		return origin, ""
	}
	return origin, strings.TrimSpace(reason)
}

func (o *providerDrainOps) isDrainAcked(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRAIN_ACK")
	if err != nil {
		return false, fmt.Errorf("reading GC_DRAIN_ACK: %w", err)
	}
	return val == "1", nil
}

func (o *providerDrainOps) setRestartRequested(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_RESTART_REQUESTED", strconv.FormatInt(time.Now().Unix(), 10))
}

func (o *providerDrainOps) isRestartRequested(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_RESTART_REQUESTED")
	if err != nil {
		if runtime.IsSessionGone(err) {
			return false, nil
		}
		return false, fmt.Errorf("reading GC_RESTART_REQUESTED: %w", err)
	}
	return val != "", nil
}

func (o *providerDrainOps) clearRestartRequested(sessionName string) error {
	err := o.sp.RemoveMeta(sessionName, "GC_RESTART_REQUESTED")
	if runtime.IsSessionGone(err) {
		return nil
	}
	return err
}

func (o *providerDrainOps) setDriftRestart(sessionName string) error {
	return o.sp.SetMeta(sessionName, "GC_DRIFT_RESTART", "1")
}

func (o *providerDrainOps) isDriftRestart(sessionName string) (bool, error) {
	val, err := o.sp.GetMeta(sessionName, "GC_DRIFT_RESTART")
	if err != nil {
		return false, fmt.Errorf("reading GC_DRIFT_RESTART: %w", err)
	}
	return val == "1", nil
}

func (o *providerDrainOps) clearDriftRestart(sessionName string) error {
	return o.sp.RemoveMeta(sessionName, "GC_DRIFT_RESTART")
}

func joinDrainAckMutationErrors(errs ...error) error {
	var joined []error
	for _, err := range errs {
		if err == nil || drainAckMissingSessionBeadError(err) {
			continue
		}
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func drainAckMissingSessionBeadError(err error) bool {
	return runtime.IsSessionGone(err) || errors.Is(err, beads.ErrNotFound)
}

// newDrainOps creates a drainOps from a runtime.Provider.
func newDrainOps(sp runtime.Provider) drainOps {
	return &providerDrainOps{sp: sp}
}

// ---------------------------------------------------------------------------
// gc runtime drain <name>
// ---------------------------------------------------------------------------

func newRuntimeDrainCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "drain <name>",
		Short: "Signal a session to drain (wind down gracefully)",
		Long: `Signal a session to drain — wind down its current work gracefully.

Sets a GC_DRAIN metadata flag on the session. The agent should check
for drain status periodically (via "gc runtime drain-check") and finish
its current task before exiting. Pass a session alias or ID. Use
"gc runtime undrain" to cancel.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeDrain(args, jsonOutput, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func cmdRuntimeDrain(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "gc runtime drain: missing session alias or ID") //nolint:errcheck // best-effort stderr
		return 1
	}
	target, err := resolveSessionRuntimeTarget(args[0], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	dops := newDrainOps(sp)
	rec := openCityRecorder(stderr)
	return doRuntimeDrain(dops, sp, rec, target.display, target.sessionName, jsonOutput, stdout, stderr)
}

// doRuntimeDrain sets the drain signal on a session.
func doRuntimeDrain(dops drainOps, sp runtime.Provider, rec events.Recorder,
	targetName, sn string, jsonOutput bool, stdout, stderr io.Writer,
) int {
	running, err := workerSessionTargetRunningWithConfig("", nil, sp, nil, sn)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: observing %q: %v\n", targetName, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !running {
		fmt.Fprintf(stderr, "gc runtime drain: session %q is not running\n", targetName) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := dops.setDrain(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime drain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	rec.Record(events.Event{
		Type:    events.SessionDraining,
		Actor:   eventActor(),
		Subject: targetName,
	})
	if jsonOutput {
		if err := writeCLIJSONLine(stdout, runtimeActionJSON{
			SchemaVersion: "1",
			OK:            true,
			Command:       "runtime drain",
			Action:        "drain",
			Session:       sn,
			Target:        targetName,
			Status:        "draining",
		}); err != nil {
			fmt.Fprintf(stderr, "gc runtime drain: writing JSON: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Draining session '%s'\n", targetName) //nolint:errcheck // best-effort stdout
	return 0
}

// ---------------------------------------------------------------------------
// gc runtime undrain <name>
// ---------------------------------------------------------------------------

func newRuntimeUndrainCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "undrain <name>",
		Short: "Cancel drain on a session",
		Long: `Cancel a pending drain signal on a session.

Clears the GC_DRAIN and GC_DRAIN_ACK metadata flags, allowing the
session to continue normal operation. Pass a session alias or ID.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeUndrain(args, jsonOutput, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func cmdRuntimeUndrain(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "gc runtime undrain: missing session alias or ID") //nolint:errcheck // best-effort stderr
		return 1
	}
	target, err := resolveSessionRuntimeTarget(args[0], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	dops := newDrainOps(sp)
	rec := openCityRecorder(stderr)
	return doRuntimeUndrain(dops, sp, rec, target.display, target.sessionName, jsonOutput, stdout, stderr)
}

// doRuntimeUndrain clears the drain signal on a session.
func doRuntimeUndrain(dops drainOps, sp runtime.Provider, rec events.Recorder,
	targetName, sn string, jsonOutput bool, stdout, stderr io.Writer,
) int {
	running, err := workerSessionTargetRunningWithConfig("", nil, sp, nil, sn)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: observing %q: %v\n", targetName, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !running {
		fmt.Fprintf(stderr, "gc runtime undrain: session %q is not running\n", targetName) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := dops.clearDrain(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime undrain: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	rec.Record(events.Event{
		Type:    events.SessionUndrained,
		Actor:   eventActor(),
		Subject: targetName,
	})
	if jsonOutput {
		if err := writeCLIJSONLine(stdout, runtimeActionJSON{
			SchemaVersion: "1",
			OK:            true,
			Command:       "runtime undrain",
			Action:        "undrain",
			Session:       sn,
			Target:        targetName,
			Status:        "undrained",
		}); err != nil {
			fmt.Fprintf(stderr, "gc runtime undrain: writing JSON: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Undrained session '%s'\n", targetName) //nolint:errcheck // best-effort stdout
	return 0
}

// ---------------------------------------------------------------------------
// gc runtime drain-check
// ---------------------------------------------------------------------------

func newRuntimeDrainCheckCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "drain-check [name]",
		Short: "Check if a session is draining (exit 0 = draining)",
		Long: `Check if a session is currently draining.

Returns exit code 0 if draining, 1 if not. Designed for use in
conditionals: "if gc runtime drain-check; then finish-up; fi". Without
arguments, uses the current session context.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeDrainCheck(args, jsonOutput, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func cmdRuntimeDrainCheck(args []string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		target, err := resolveSessionRuntimeTarget(args[0], stderr)
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-check: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1                                                 // silent — same as current "not draining" behavior
		}
		sp, err := newSessionProvider()
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-check: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		dops := newDrainOps(sp)
		return doRuntimeDrainCheck(dops, target.display, target.sessionName, jsonOutput, stdout, stderr)
	}

	current, err := currentSessionRuntimeTarget()
	if err != nil {
		return 1 // not in agent context → not draining
	}
	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-check: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	dops := newDrainOps(sp)
	return doRuntimeDrainCheck(dops, current.display, current.sessionName, jsonOutput, stdout, stderr)
}

// doRuntimeDrainCheck returns 0 if the session is draining, 1 otherwise.
// Silent on stdout — designed for `if gc runtime drain-check; then ...`.
func doRuntimeDrainCheck(dops drainOps, targetName, sn string, jsonOutput bool, stdout, stderr io.Writer) int {
	draining, err := dops.isDraining(sn)
	if err != nil {
		return 1
	}
	if !draining {
		if jsonOutput {
			if err := writeCLIJSONLine(stdout, runtimeDrainCheckJSON{
				SchemaVersion: "1",
				OK:            true,
				Command:       "runtime drain-check",
				Session:       sn,
				Target:        targetName,
				Draining:      false,
			}); err != nil {
				fmt.Fprintf(stderr, "gc runtime drain-check: writing JSON: %v\n", err) //nolint:errcheck // best-effort stderr
				return 1
			}
		}
		return 1
	}
	if jsonOutput {
		if err := writeCLIJSONLine(stdout, runtimeDrainCheckJSON{
			SchemaVersion: "1",
			OK:            true,
			Command:       "runtime drain-check",
			Session:       sn,
			Target:        targetName,
			Draining:      true,
		}); err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-check: writing JSON: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// gc runtime drain-ack
// ---------------------------------------------------------------------------

func newRuntimeDrainAckCmd(stdout, stderr io.Writer) *cobra.Command {
	var jsonOutput bool
	var reason string
	cmd := &cobra.Command{
		Use:   "drain-ack [name]",
		Short: "Acknowledge drain — signal the controller to stop this session",
		Long: `Acknowledge a drain signal — tell the controller to stop this session.

Sets GC_DRAIN_ACK metadata on the session, then pokes the controller
socket so the reconciler considers the acknowledgement immediately
rather than on its next patrol tick. Call this after the session has
finished its current work in response to a drain signal.

Recording an acknowledgement is not the same as having it honored, and
this command reports only the former. The controller REFUSES an
acknowledgement from a session that still owns assigned work, leaving
that session active with state_reason=drain-ack-assigned-work and still
holding its pool slot. That decision is made afterwards, in a reconciler
tick, so read the session's state with "gc session list" to learn what
happened rather than treating this command's success as the outcome.

The acknowledgement records the session itself as the actor, so its
closed bead reads "agent retired itself" rather than naming the
reconciler. Pass --reason to say why; it is stored verbatim as
drain_ack_reason on the session bead.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdRuntimeDrainAck(args, reason, jsonOutput, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().StringVar(&reason, "reason", "", "Why this session is retiring (recorded on the closed session bead)")
	return cmd
}

func cmdRuntimeDrainAck(args []string, reason string, jsonOutput bool, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		target, err := resolveSessionRuntimeTarget(args[0], stderr)
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		sp, err := newSessionProvider()
		if err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		dops := newDrainOps(sp)
		return doRuntimeDrainAck(dops, target.cityPath, target.display, target.sessionName, reason, jsonOutput, stdout, stderr)
	}

	current, err := currentSessionRuntimeTarget()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	dops := newDrainOps(sp)
	return doRuntimeDrainAck(dops, current.cityPath, current.display, current.sessionName, reason, jsonOutput, stdout, stderr)
}

// ---------------------------------------------------------------------------
// gc runtime request-restart
// ---------------------------------------------------------------------------

func newRuntimeRequestRestartCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "request-restart",
		Short: "Request controller restart this session (waits to be killed)",
		Long: `Signal the controller to stop and restart this session.

Sets GC_RESTART_REQUESTED metadata on the session, then waits while the
controller stops the session on its next reconcile tick and restarts it
fresh. The wait keeps the agent idle so it does not consume more context
in the interim.

Under normal operation the controller SIGKILLs the process tree before
this command returns. If the controller accepts the stop handoff, the
runtime is already gone, or a SIGINT/SIGTERM is received, the command
exits 0 cleanly. If the controller has not acted within a bounded
timeout (max(5*PatrolInterval, 5min), capped at 30min) the command exits
1 with a diagnostic pointing at controller health.

This command is designed to be called from within a session context.
It emits a session.draining event before waiting.`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if cmdRuntimeRequestRestart(stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
}

func cmdRuntimeRequestRestart(stdout, stderr io.Writer) int {
	current, err := currentSessionRuntimeTarget()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	dops := newDrainOps(sp)
	store, storeErr := openCityStoreAt(current.cityPath)
	if storeErr != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: opening store: %v\n", storeErr) //nolint:errcheck // best-effort stderr
	}
	// Route the SESSION-class access (restart persist through the worker
	// boundary) to the session coordination-class store so a
	// [beads.classes.sessions] relocation reaches gc runtime request-restart.
	// The routing cfg is loaded refresh-free (the full cfg loads later, for
	// timeout/template resolution). Identity today, so byte-identical.
	var sessStore beads.Store
	if store != nil {
		routeCfg, _ := loadCityConfigWithoutBuiltinPackRefresh(current.cityPath, io.Discard)
		sessStore = cliSessionStore(store, routeCfg, current.cityPath)
	}
	rec := openCityRecorderAt(current.cityPath, stderr)
	cfg, _ := loadCityConfig(current.cityPath, stderr)
	var persistRestart func() error
	if store != nil {
		persistRestart = func() error {
			handle, err := workerHandleForSessionTargetWithConfig(current.cityPath, sessStore, sp, cfg, current.sessionName)
			if err != nil {
				return err
			}
			return handle.Reset(context.Background())
		}
	}
	_, pinned, err := sessionRestartableByController(sessStore, current.sessionName)
	if err != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: checking session type: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return doRuntimeRequestRestart(sigCtx, dops, sp, persistRestart, pinned, rec, current.display, current.sessionName,
		controllerRestartPollInterval, controllerRestartTimeout(cfg), stdout, stderr)
}

const controllerRestartPollInterval = 1 * time.Second

// controllerRestartTimeout computes the bounded timeout for waiting on the
// controller to act on a restart request: max(5*PatrolInterval, 5min), capped at 30min.
func controllerRestartTimeout(cfg *config.City) time.Duration {
	const floor = 5 * time.Minute
	const ceil = 30 * time.Minute
	patrol := 30 * time.Second
	if cfg != nil {
		patrol = cfg.Daemon.PatrolIntervalDuration()
	}
	d := 5 * patrol
	if d < floor {
		d = floor
	}
	if d > ceil {
		d = ceil
	}
	return d
}

// doRuntimeRequestRestart sets the restart-requested flag then polls until the
// controller accepts the stop handoff (exit 0), the context is canceled by a
// signal (exit 0), or the bounded timeout expires (exit 1 with diagnostic).
//
// pinned marks a kill-protected named session (pin_awake == "true"): the
// reconciler refuses to collaterally kill such a session on a bare runtime
// restart-requested flag, so for pinned sessions persistRestart (which lands
// continuation_reset_pending, the explicit-reset escape hatch) is mandatory
// rather than best-effort. See sessionRestartableByController.
func doRuntimeRequestRestart(ctx context.Context, dops drainOps, sp runtime.Provider, persistRestart func() error, pinned bool, rec events.Recorder,
	targetName, sn string, pollInterval, timeout time.Duration, stdout, stderr io.Writer,
) int {
	if err := dops.setRestartRequested(sn); err != nil {
		fmt.Fprintf(stderr, "gc runtime request-restart: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if pinned {
		if persistRestart == nil {
			fmt.Fprintf(stderr, "gc runtime request-restart: pinned session %q has no restart persistence available; not requesting restart\n", sn) //nolint:errcheck // best-effort stderr
			return 1
		}
		if err := persistRestart(); err != nil {
			fmt.Fprintf(stderr, "gc runtime request-restart: could not persist restart marker for pinned session %q; not requesting restart: %v\n", sn, err) //nolint:errcheck // best-effort stderr
			return 1
		}
	} else if persistRestart != nil {
		// Also persist the request through the worker boundary so it survives
		// tmux session death. Non-fatal here: the runtime flag above is primary.
		if err := persistRestart(); err != nil {
			fmt.Fprintf(stderr, "gc runtime request-restart: setting bead restart flag: %v\n", err) //nolint:errcheck // best-effort stderr
		}
	}
	rec.Record(events.Event{
		Type:    events.SessionDraining,
		Actor:   targetName,
		Subject: targetName,
		Message: "restart requested by session",
	})
	fmt.Fprintf(stdout, "Restart requested. Waiting up to %s for controller to stop this session...\n", timeout) //nolint:errcheck // best-effort stdout

	return waitForControllerRestart(ctx, dops, sp, sn, "gc runtime request-restart", pollInterval, timeout, stderr)
}

// waitForControllerRestart polls until the controller accepts the stop
// handoff (exit 0), the context is canceled by a signal (exit 0), or the
// bounded timeout expires (exit 1 with diagnostic).
//
// A cleared GC_RESTART_REQUESTED flag alone is not proof the controller acted:
// the reconciler's pinned-session collateral-skip clears the very same flag
// without killing the session (see pinnedConfiguredNamedSessionKillProtected
// in session_reconciler.go). sp confirms the session actually stopped before
// this reports success; while the flag is clear but the session is still
// running, polling continues until the deadline instead of returning early.
func waitForControllerRestart(ctx context.Context, dops drainOps, sp runtime.Provider, sn, command string, pollInterval, timeout time.Duration, stderr io.Writer) int {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	var lastPollErr error

	for {
		select {
		case <-ctx.Done():
			// Signal received; leave the flag set so the controller still acts on its next tick.
			fmt.Fprintf(stderr, "%s: signal received; restart request remains set; controller will stop this session on its next reconcile tick\n", command) //nolint:errcheck // best-effort stderr
			return 0
		case <-ticker.C:
			requested, err := dops.isRestartRequested(sn)
			switch {
			case err != nil:
				lastPollErr = err
			case !requested && !sp.IsRunning(sn):
				// The controller accepted the stop handoff or the runtime is already gone.
				return 0
			default:
				lastPollErr = nil
			}
			if time.Now().After(deadline) {
				if lastPollErr != nil {
					fmt.Fprintf(stderr, "%s: controller did not act within %s; last poll error: %v; check `gc dashboard` or `gc trace`\n", command, timeout, lastPollErr) //nolint:errcheck // best-effort stderr
				} else {
					fmt.Fprintf(stderr, "%s: controller did not act within %s; check `gc dashboard` or `gc trace`\n", command, timeout) //nolint:errcheck // best-effort stderr
				}
				return 1
			}
		}
	}
}

// drainAckPokeController is a mutable global test seam over pokeController.
// Tests that swap it MUST NOT call t.Parallel().
var drainAckPokeController = pokeController

// doRuntimeDrainAck sets the drain-ack flag on the session, then pokes the
// controller so the reconciler observes the drained state immediately instead
// of waiting for its next patrol tick.
//
// It reports what it DID, never what the controller will decide. The verdict is
// reached later, in a reconciler tick, in another process: the controller
// refuses an acknowledgement from a session that still owns assigned work,
// leaving it active with state_reason=drain-ack-assigned-work and still holding
// its pool slot. This command cannot see that, so it must not imply otherwise.
//
// It said "Controller poked for immediate stop." until ci-20ilrq. That reads as
// an outcome, and agents echoed it as "slot released" in their final turn while
// the controller was refusing them -- the false success that made the ci-fx4duc
// wedge present as a routing fault. ci-00hcfv records the same shape taking
// three separate investigations to see, because the tell was in the pane and
// nowhere else.
//
// Two rejected alternatives, both of which would let it report a real verdict:
//
//   - Poll the session bead for state_reason after poking. Measured cost: this
//     command runs at the end of EVERY session, and opening the city store here
//     provisions a managed Dolt server -- caught by cmd/gc's dolt leak guard
//     when TestDrainAckNoArgsFallsBackToCityPathEnv started leaking one. It
//     also blocks a closing turn on a store that this city does not auto-start.
//   - Pre-evaluate the reconciler's assigned-work predicate before setting the
//     marker. That duplicates the predicate in a second place, where it can
//     silently disagree with the one that actually decides.
//
// Both wait on the interaction ci-eqtxc0 is deciding: the Stop gate counts
// in_progress work only, so it ORDERS this acknowledgement in exactly the case
// the reconciler's {open, in_progress} predicate then refuses. Reporting
// honestly is correct regardless of how that is settled; verifying is not, so
// it waits.
func doRuntimeDrainAck(dops drainOps, cityPath, targetName, sn, reason string, jsonOutput bool, stdout, stderr io.Writer) int {
	if err := dops.setDrainAckWithReason(sn, reason); err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-ack: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := drainAckPokeController(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc runtime drain-ack: warning: poke failed: %v\n", err) //nolint:errcheck // best-effort stderr
	}
	if jsonOutput {
		if err := writeCLIJSONLine(stdout, runtimeActionJSON{
			SchemaVersion: "1",
			OK:            true,
			Command:       "runtime drain-ack",
			Action:        "drain-ack",
			Session:       sn,
			Target:        targetName,
			Status:        "acknowledged",
		}); err != nil {
			fmt.Fprintf(stderr, "gc runtime drain-ack: writing JSON: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	notConfirmed := fmt.Sprintf("Drain acknowledgement recorded and the controller was poked; its verdict is not confirmed here. The controller stops this session UNLESS it still owns assigned work, in which case the session stays active with state_reason=%s and keeps its pool slot. Check with `gc session list`.",
		session.DrainAckAssignedWorkReason)
	fmt.Fprintln(stdout, notConfirmed) //nolint:errcheck // best-effort stdout
	return 0
}
