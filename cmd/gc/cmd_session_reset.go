package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/worker"
	"github.com/spf13/cobra"
)

// sessionResetOptions carries the caller-selected behavior of
// "gc session reset". It replaced a variadic bool for JSON when --force
// arrived: two positional bools at a call site say nothing about which is
// which, and the one that suppresses a context-destroying refusal is not a
// parameter to get the wrong way round.
type sessionResetOptions struct {
	JSON bool
	// Force resets a session an operator is attached to, and resets one
	// whose attachment could not be observed. It is the only way past
	// sessionResetAttachmentVerdict.
	Force bool
}

// newSessionResetCmd creates the "gc session reset <id-or-alias>" command.
func newSessionResetCmd(stdout, stderr io.Writer) *cobra.Command {
	var opts sessionResetOptions
	cmd := &cobra.Command{
		Use:   "reset <session-id-or-alias>",
		Short: "Restart a session fresh while preserving the bead",
		Long: `Request a fresh restart for an existing session without closing its bead.

The controller stops the current runtime and starts the same session again with
fresh provider conversation state. Session identity, alias, mail, and queued
work remain attached to the existing session bead. For named sessions, reset
also clears any tripped named-session respawn circuit breaker before requesting
the fresh restart.

A session someone is attached to is REFUSED, because the restart discards the
provider conversation and an attached terminal is somebody reading it. Pass
--force to reset it anyway. A session whose attachment cannot be observed is
refused on the same terms.

Accepts a session ID (e.g., gc-42) or session alias (e.g., mayor).`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if cmdSessionReset(args, stdout, stderr, opts) != 0 {
				return errExit
			}
			return nil
		},
		ValidArgsFunction: completeSessionIDs,
	}
	cmd.Flags().BoolVar(&opts.JSON, "json", false, "emit JSONL")
	cmd.Flags().BoolVar(&opts.Force, "force", false, "reset even while a terminal is attached, or when attachment cannot be observed")
	return cmd
}

// sessionResetVerdict is sessionResetAttachmentVerdict's answer: whether to
// refuse, and the condition to name when refusing.
type sessionResetVerdict struct {
	Refuse bool
	Reason string
}

// sessionResetAttachmentVerdict decides whether an explicit reset may proceed,
// given what the attachment probe reported.
//
// Split out of the command so every branch is reachable without staging the
// race that produces observeErr -- the session bead disappearing between
// resolution and observation. It is also where the judgment lives, rather than
// scattered through the I/O path.
//
// AN UNREADABLE PROBE REFUSES. That is the half a reader is most likely to
// invert, and the incident is the argument: the config-drift restart path
// already refuses to act on a session it believes attached, its comment
// stating that "a single transient IsAttached false negative would destroy
// conversation context irreversibly", while this command had no check at all
// and destroyed exactly that (ci-6mp9hs). Treating "I could not tell" as
// detached rebuilds the hole one layer in.
//
// WHAT THIS DOES NOT CATCH, stated because the gap is real and is not being
// closed here: a probe that answers FALSE when a terminal is in fact attached.
// The verdict is only as good as its input, and narrowing the probe is a
// separate decision about the probe rather than about this command.
func sessionResetAttachmentVerdict(attached bool, observeErr error, force bool) sessionResetVerdict {
	if force {
		return sessionResetVerdict{}
	}
	if observeErr != nil {
		return sessionResetVerdict{Refuse: true, Reason: "attachment could not be observed: " + observeErr.Error()}
	}
	if attached {
		return sessionResetVerdict{Refuse: true, Reason: "a terminal is attached"}
	}
	return sessionResetVerdict{}
}

// cmdSessionReset is the CLI entry point for "gc session reset".
//
// This command intentionally requires a managed controller. The controller owns
// the fresh restart lifecycle, including key rotation and immediate restart of
// already-desired sessions.
func cmdSessionReset(args []string, stdout, stderr io.Writer, opts sessionResetOptions) int {
	asJSON := opts.JSON
	store, code := openCityStore(stderr, "gc session reset")
	if store == nil {
		return code
	}

	cityPath, err := resolveCity()
	if err != nil {
		fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !cityUsesManagedReconciler(cityPath) {
		fmt.Fprintln(stderr, "gc session reset: a managed controller must be running") //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := pokeController(cityPath); err != nil {
		fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	cfg, _ := loadCityConfig(cityPath, stderr)

	// Every store consumer here is session-class (ID resolution, worker handle,
	// session-bead load), so route the whole flow through the session
	// coordination-class store for relocation-safety.
	sessStore := cliSessionStore(store, cfg, cityPath)
	sessionID, err := resolveSessionIDWithConfig(cityPath, cfg, sessStore, args[0])
	if err != nil {
		fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	sp, err := newSessionProvider()
	if err != nil {
		fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	handle, err := workerHandleForSessionWithConfig(cityPath, sessStore, sp, cfg, sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	// The attachment gate runs before every mutation below, and that ordering
	// is the point rather than tidiness: the circuit-breaker clear that
	// follows re-arms respawn for the session, so a gate placed after it would
	// refuse the reset and still have changed the session an operator is
	// sitting in. worker.ObserveHandle reuses the handle already built above
	// instead of re-resolving through workerSessionTargetAttachedWithConfig,
	// which would repeat the resolution for the same answer.
	//
	// NO sp.IsAttached(name) FALLBACK, unlike the reconciler's
	// sessionAttachedForConfigDrift. That fallback exists because the
	// reconciler holds a runtime session name its handle observation may not
	// cover; here the handle is the only name there is, so a second probe
	// would read the same source twice and count one signal as two.
	obs, observeErr := worker.ObserveHandle(context.Background(), handle)
	if verdict := sessionResetAttachmentVerdict(obs.Attached, observeErr, opts.Force); verdict.Refuse {
		fmt.Fprintf(stderr, "gc session reset: refusing to reset %s: %s\n", sessionID, verdict.Reason)                         //nolint:errcheck // best-effort stderr
		fmt.Fprintf(stderr, "  a reset discards the provider conversation, and an attached terminal is somebody reading it\n") //nolint:errcheck // best-effort stderr
		fmt.Fprintf(stderr, "  remedy: detach and re-run, or `gc session reset %s --force` if you meant it\n", args[0])        //nolint:errcheck // best-effort stderr
		return 1
	}

	bead, err := sessStore.Get(sessionID)
	if err != nil {
		fmt.Fprintf(stderr, "gc session reset: loading session %s: %v\n", sessionID, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	identity := namedSessionIdentity(bead)
	if identity != "" {
		if err := resetSessionCircuitBreakerOnController(cityPath, sessionID, identity); err != nil {
			fmt.Fprintf(stderr, "gc session reset: clearing session circuit breaker for %q: %v\n", identity, err) //nolint:errcheck // best-effort stderr
			return 1
		}
	}

	if err := handle.Reset(context.Background()); err != nil {
		fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	_ = pokeController(cityPath)

	if asJSON {
		if err := writeSessionActionJSON(stdout, sessionActionResult{
			Action:    "reset",
			SessionID: sessionID,
			Identity:  identity,
		}); err != nil {
			fmt.Fprintf(stderr, "gc session reset: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Session %s reset requested. Controller will restart it fresh.\n", sessionID) //nolint:errcheck // best-effort stdout
	return 0
}

func resetSessionCircuitBreakerAfterExplicitKill(cityPath string, store beads.Store, sessionID, identity string) error {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil
	}
	if strings.TrimSpace(cityPath) != "" && cityUsesManagedReconciler(cityPath) {
		if err := resetSessionCircuitBreakerOnController(cityPath, sessionID, identity); err != nil {
			return err
		}
		_ = pokeController(cityPath)
		return nil
	}
	return resetSessionCircuitBreakerState(store, sessionID, identity, defaultSessionCircuitBreaker())
}
