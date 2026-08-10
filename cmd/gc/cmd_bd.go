package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/gastownhall/gascity/internal/bdflags"
	"github.com/gastownhall/gascity/internal/beadmeta"
	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/spf13/cobra"
)

// heartbeatMetadataKey is the bead-metadata key freshened by the
// `gc bd heartbeat <issue-id>` subcommand alongside bd's own lease refresh.
// The gas-city-dashboard will read this exact key — with the `_at` suffix — to
// tell a live worker from a dead one (gastownhall/gascity#1855; reader tracked
// in dashboard #324). It is not redundant with the lease: bd holds leases in
// an ephemeral node-local table that is never committed, so a reader off the
// granting machine has nothing else to go on. Unrelated benchmark/test code
// writes the suffixless `gc.last_heartbeat` for a different purpose; do not
// unify them.
const heartbeatMetadataKey = beadmeta.LastHeartbeatAtMetadataKey

// bdHeartbeatNow supplies the timestamp stamped by `gc bd heartbeat`. It is a
// package var so tests can pin it to a fixed instant; the rewrite normalizes
// the result to UTC, so an injected non-UTC clock still produces a UTC stamp.
var bdHeartbeatNow = time.Now

// bdSilentFallbackExitCode is the exit code gc bd emits when it detects
// that bd silently fell back to on-disk auto-import mode (managed Dolt
// unreachable). Distinct from bd's own exits so operators and CI can
// tell the loud-fail apart from a real bd error. Covers both the
// bd update path (gastownhall/gascity#2080) and the bd close path
// (gastownhall/gascity#2079) because both subcommands flow through doBd.
const bdSilentFallbackExitCode = 4

const bdSilentFallbackUserMessage = "gc bd: managed Dolt unreachable; bd fell back to on-disk auto-import mode. If this command wrote data, that write was NOT persisted. Restart the managed Dolt server (or check connectivity) and retry. (See gastownhall/gascity#2080.)"

// bdStderrScanLimit caps how much of bd's stderr gc retains to scan for the
// silent-fallback marker. bd emits the marker pair while opening the store —
// before it runs the subcommand — so the marker, when present, always lands
// within the first chunk of stderr. Capping the retained prefix keeps memory
// bounded for bd subcommands that stream large stderr output.
const bdStderrScanLimit = 64 << 10 // 64 KiB

// headLimitedWriter retains only the first limit bytes written to it and
// discards the rest, so scanning bd's stderr for the silent-fallback marker
// never holds an unbounded copy of the stream. It always reports a full
// write so it is safe as an io.MultiWriter sink.
type headLimitedWriter struct {
	buf   []byte
	limit int
}

func (w *headLimitedWriter) Write(p []byte) (int, error) {
	if room := w.limit - len(w.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf = append(w.buf, p[:room]...)
	}
	return len(p), nil
}

func (w *headLimitedWriter) String() string { return string(w.buf) }

func newBdCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bd [bd-args...]",
		Short: "Run bd in the correct rig directory",
		Long: `Run a bd command routed to the correct rig directory.

When beads belong to a rig (not the city root), bd must run from the
rig directory to find the correct .beads database. This command resolves
the rig automatically from the --rig flag or by detecting the bead prefix
in the arguments.

Use --rig <name> to pin a specific rig store, or --city <path> to pin the
city (HQ) store. An explicit --city is a true scope override: it forces the
city store and disables rig auto-detection (GC_RIG, GC_BEADS_SCOPE_ROOT, cwd,
bead prefix), so a deliberate city-scoped query is never silently downgraded to
a rig store.

Inside a gc-managed agent session, GC_BEADS_SCOPE_ROOT -- the scope gc stamped
on the session from that agent's own config -- decides the store, ahead of cwd.
An agent whose work_dir is a worktree of another rig therefore reads and writes
the same store instead of reading its own and writing the worktree's.

All arguments after "gc bd" are forwarded to bd unchanged, except the
"heartbeat <issue-id>" subcommand (alias "hb"), which performs two writes so
a long-running worker keeps both halves of its claim alive — bd's own
"heartbeat" to push the claim lease forward, then
"update <issue-id> --set-metadata gc.last_heartbeat_at=<RFC3339 UTC now>"
for the dashboard, which cannot see the node-local lease. bd matches a lease on
holder = actor, so the refresh runs as the bead's own lease holder whenever that
is another identity form of the calling session — an alias, a runtime session
name, or a session bead id. A lease bd refuses stops the command before the
stamp. Also excepted is
"release-if-current <issue-id> <assignee>", which conditionally resets an
in-progress assignment only when the bead still has that assignee.

gc bd forces BD_EXPORT_AUTO=false to prevent bd's git auto-export hook
from wedging the wrapper after printing command output. If you need
auto-export behavior, invoke bd directly.`,
		Example: `  gc bd --rig my-project list
  gc bd --rig my-project create "New task"
  gc bd show my-project-abc          # auto-detects rig from bead prefix
  gc bd list --rig my-project -s open
  gc bd --city /path/to/city list    # pins the city (HQ) store, no rig auto-detect
  gc bd heartbeat my-project-abc     # refresh the claim lease + stamp gc.last_heartbeat_at
  gc bd release-if-current my-project-abc worker-1`,
		DisableFlagParsing: true,
		RunE: func(_ *cobra.Command, args []string) error {
			// Plumb doBd's numeric exit code through exitForCode so the
			// process exit code matches the documented contract above
			// (bdSilentFallbackExitCode = 4) and bd's own exit codes are
			// preserved. Returning errExit on any non-zero would collapse
			// every code to 1 and defeat the operator/CI signal the loud-
			// fail was meant to provide.
			return exitForCode(doBd(args, stdout, stderr))
		},
	}
	return cmd
}

// bdBeadExists reports whether a bead ID resolves in a candidate store. It is
// called only to decide which store a bd invocation is scoped to, so it takes
// the city config the caller already loaded: without it, every candidate probe
// re-loaded the whole city config inside the store open.
var bdBeadExists = func(cityPath string, cfg *config.City, target execStoreTarget, beadID string) bool {
	store, err := openStoreAtForCityWithConfig(target.ScopeRoot, cityPath, cfg)
	if err != nil {
		return false
	}
	bead, err := store.Get(beadID)
	return err == nil && strings.TrimSpace(bead.ID) != ""
}

func bdCommandEnv(cityPath string, cfg *config.City, target execStoreTarget) ([]string, error) {
	var overrides map[string]string
	var err error
	if target.ScopeKind == "rig" {
		overrides, err = bdRuntimeEnvForRigWithError(cityPath, cfg, target.ScopeRoot)
	} else {
		overrides, err = bdRuntimeEnvWithError(cityPath)
	}
	if err != nil {
		return nil, err
	}
	if target.ScopeKind != "rig" {
		overrides["GC_RIG"] = ""
		overrides["GC_RIG_ROOT"] = ""
		overrides["BEADS_DIR"] = filepath.Join(target.ScopeRoot, ".beads")
	}
	overrides["GC_STORE_ROOT"] = target.ScopeRoot
	overrides["GC_STORE_SCOPE"] = target.ScopeKind
	overrides["GC_BEADS_PREFIX"] = target.Prefix
	applyExportSuppressionEnv(overrides)
	return mergeRuntimeEnv(os.Environ(), overrides), nil
}

func warnExternalBdOverrideDrift(stderr io.Writer, cityPath string, target execStoreTarget) {
	resolved, ok, err := canonicalScopeDoltTarget(cityPath, target.ScopeRoot)
	if err != nil || !ok || !resolved.External {
		return
	}
	var drift []string
	if host := strings.TrimSpace(os.Getenv("GC_DOLT_HOST")); host != "" && host != strings.TrimSpace(resolved.Host) {
		drift = append(drift, fmt.Sprintf("GC_DOLT_HOST=%s (canonical %s)", host, strings.TrimSpace(resolved.Host)))
	}
	if port := strings.TrimSpace(os.Getenv("GC_DOLT_PORT")); port != "" && port != strings.TrimSpace(resolved.Port) {
		drift = append(drift, fmt.Sprintf("GC_DOLT_PORT=%s (canonical %s)", port, strings.TrimSpace(resolved.Port)))
	}
	if len(drift) == 0 {
		return
	}
	_, _ = fmt.Fprintf(stderr, "gc bd: warning: ignoring ambient Dolt host/port override for external target: %s\n", strings.Join(drift, ", "))
}

// parseBdHeartbeatArgs recognizes `heartbeat <issue-id>` and its bd-published
// alias `hb`, returning the issue id. ok reports that the args were CLAIMED by
// the heartbeat path (so a usage error is still ok=true); args for any other
// subcommand are left to the generic passthrough.
//
// The spellings come from bdHeartbeatVerb rather than from literals here, so
// they are bd's own alias group for the command gc wraps. gc used to intercept
// only the literal "heartbeat", so the two spellings of one bd command reached
// different code -- `hb` fell through to bd's real lease refresh while
// `heartbeat` did not (ci-ctkz). See bd_intercepts.go.
//
// No flags are accepted on either spelling, which costs `gc bd hb <id> --json`
// the passthrough it had before both spellings were claimed. Forwarding them
// would mean deciding what each flag does across two writes -- --json would
// emit two documents for one logical call, and --actor means different things
// to a lease holder-check and to a metadata write. A caller who wants bd's
// raw heartbeat with flags invokes bd directly, as the command's help already
// says for auto-export.
func parseBdHeartbeatArgs(bdArgs []string) (id string, ok bool, err error) {
	if len(bdArgs) == 0 || !bdHeartbeatVerb.claims(bdArgs[0]) {
		return "", false, nil
	}
	rest := bdArgs[1:]
	// A bead id never contains whitespace; reject any (leading, trailing, or
	// internal) rather than forwarding a malformed id that would break bd's
	// prefix-based rig auto-detection. Also reject empty and flag-shaped args.
	if len(rest) != 1 || rest[0] == "" || strings.HasPrefix(rest[0], "-") ||
		strings.IndexFunc(rest[0], unicode.IsSpace) >= 0 {
		return "", true, fmt.Errorf("usage: gc bd heartbeat <issue-id>")
	}
	return rest[0], true, nil
}

// bdHeartbeatIdentityForms lists the identities under which THIS session may
// legitimately hold a bd lease: its alias, its agent name, its runtime session
// name, and its session bead id. Any of the four can be the holder, because
// gc hook --claim takes the lease under whichever form the bead was already
// assigned to rather than under $BEADS_ACTOR.
//
// GC_TEMPLATE is deliberately ABSENT. It is set on every suffixed pool worker
// and holds the bare template name, which is ALSO the [[named_session]]
// holder's identity -- admitting it would let toolsmith-3 refresh a lease held
// by toolsmith, the same cross-session adoption gc hook --claim excludes it to
// prevent (ga-80pen8). Pinned by
// TestBdHeartbeatIdentityFormsExcludeTheBarePoolTemplate.
func bdHeartbeatIdentityForms() []string {
	return hookClaimIdentityCandidates(
		os.Getenv("GC_ALIAS"),
		os.Getenv("GC_AGENT"),
		os.Getenv("GC_SESSION_NAME"),
		os.Getenv("GC_SESSION_ID"),
	)
}

// bdHeartbeatLeaseActor returns the actor gc must present to bd's lease refresh
// so the holder check matches, or "" to leave bd on its own default.
//
// bd matches a lease on holder = actor (HeartbeatIssueInTx:
// `WHERE issue_id = ? AND holder = ?`), and the holder is whoever claimed the
// bead -- which gc hook --claim deliberately sets to the bead's own assignee,
// since a bead can be pre-assigned by session bead id or session name. An agent
// whose $BEADS_ACTOR is its alias therefore could not heartbeat its own work
// when the claim went in under a different form of its identity (ci-eaon).
//
// Two rejected alternatives:
//
//   - Pass the assignee unconditionally. That lets any caller refresh any other
//     agent's lease, and it deletes the "learn to stop" signal -- bd's refusal
//     after a reclaim is the only thing that tells a worker its claim is gone.
//   - Forward a caller-supplied --actor. gc bd heartbeat accepts no flags on
//     purpose (see parseBdHeartbeatArgs), and a caller cannot know which
//     identity form the claim went in under anyway. Resolving that is the whole
//     job of this function.
func bdHeartbeatLeaseActor(assignee, ambientActor string, identities []string) string {
	assignee = strings.TrimSpace(assignee)
	// Already what bd would default to: forwarding it changes nothing, so leave
	// the argv the shape every other gc bd write has.
	if assignee == "" || assignee == strings.TrimSpace(ambientActor) {
		return ""
	}
	// Some other session's lease. Say nothing and let bd refuse.
	if !hookClaimHasIdentity(assignee, identities) {
		return ""
	}
	return assignee
}

// bdHeartbeatLeaseActorForBead resolves the lease actor for id by reading the
// bead's current assignee out of the store `gc bd` would route the heartbeat
// to, then applying bdHeartbeatLeaseActor.
//
// Fail-open by construction: every failure returns "", which leaves bd on
// $BEADS_ACTOR -- exactly the behavior every heartbeat had before this lookup
// existed. The three resolutions before the read run again inside doBdScoped a
// moment later and report their own errors there, so failures here stay silent
// rather than printing each one twice. The config load discards warnings for
// the same reason.
//
// The extra store read costs one bd subprocess per heartbeat. Deliberate at a
// cadence of minutes, and it is the only trustworthy source: the run-map
// (writeRunMap) knows which bead a session claimed but is documented
// unauthenticated best-effort telemetry that must not feed a trust decision,
// and which identity holds a lease is exactly that.
func bdHeartbeatLeaseActorForBead(cityName, rigName, id string, stderr io.Writer) string {
	cityPath, err := resolveBdCity(cityName)
	if err != nil {
		return ""
	}
	cfg, err := loadCityConfig(cityPath, io.Discard)
	if err != nil {
		return ""
	}
	target, err := resolveBdScopeTarget(cfg, cityPath, rigName, []string{"heartbeat", id}, cityName != "", io.Discard)
	if err != nil {
		return ""
	}
	store, err := openStoreAtForCityWithConfig(target.ScopeRoot, cityPath, cfg)
	if err != nil {
		return ""
	}
	bead, err := store.Get(id)
	if err != nil {
		// A missing bead needs no diagnostic: bd's own resolution error is
		// about to name it better than gc can.
		if !errors.Is(err, beads.ErrNotFound) {
			fmt.Fprintf(stderr, "gc bd heartbeat: cannot read %s to identify its lease holder (%v); refreshing as $BEADS_ACTOR. If bd refuses the refresh, repair the store read first: gc bd show %s\n", id, err, id) //nolint:errcheck // best-effort stderr
		}
		return ""
	}
	return bdHeartbeatLeaseActor(bead.Assignee, os.Getenv("BEADS_ACTOR"), bdHeartbeatIdentityForms())
}

// doBdHeartbeat performs the two writes `gc bd heartbeat <issue-id>` owes,
// each through the ordinary guarded passthrough.
//
//	heartbeat <issue-id>                                        (bd's own)
//	update <issue-id> --set-metadata gc.last_heartbeat_at=<now>
//
// Neither write subsumes the other. bd's `heartbeat` pushes lease_expires_at
// forward, which is what stops `bd reclaim` reverting the bead to ready and
// handing it to a second agent mid-work -- but bd keeps leases in an ephemeral
// node-local table that is never committed to Dolt, so no remote reader can
// see one. The gc.last_heartbeat_at stamp is the committed, cross-node half
// the dashboard reads (gastownhall/gascity#1855, reader dashboard #324).
//
// The lease goes first, and a refusal there returns without stamping. bd
// refuses a heartbeat exactly when the caller is no longer the owner -- the
// lease was already reclaimed, or the issue closed -- so stamping afterward
// would advertise a live worker on a bead bd has just said it does not hold.
// That refusal is the signal telling the worker to stop; absorbing it to keep
// the stamp landing would delete the only warning it gets.
//
// Going through doBdScoped twice re-resolves the config and re-opens the
// store. Deliberate: at a heartbeat cadence of minutes that costs nothing, and
// a bespoke path would have to re-implement the silent-fallback detection.
//
// The lease refresh carries `--actor=<holder>` when this session holds the bead
// under an identity form other than $BEADS_ACTOR -- see
// bdHeartbeatLeaseActorForBead for why that happens and when the flag is
// withheld (ci-eaon). The =-joined single argv element is load-bearing: a
// two-token `--actor <id>` would put a bead id into the args
// resolveBdScopeTarget scans, and its city-prefix probe accepts any existing
// city bead as the command's subject, so a heartbeat on a RIG bead held by a
// session-id holder would be re-scoped to the city store and fail to resolve.
// Every arg scanner in this file skips a token starting with "-". Measured, not
// assumed: TestResolveBdScopeTargetReadsATwoTokenFlagValueAsTheSubject. The
// constraint retires when that scan drops flag values, for which
// bdflags.Positionals is the tested vocabulary.
//
// One gap left open on purpose: the exact-ID collision guard covers the update
// only -- bdMutationWriteIDs switches on update/close/reopen/delete, and adding
// "heartbeat" means declaring its flags in the lint-shared internal/bdflags
// manifest. Exposure is bounded to an id colliding with another bead the caller
// ALSO holds, since bd matches the lease on holder = actor: the cost is one
// stray 5-minute lease.
func doBdHeartbeat(cityName, rigName, id string, stdout, stderr io.Writer) int {
	lease := []string{"heartbeat", id}
	if actor := bdHeartbeatLeaseActorForBead(cityName, rigName, id, stderr); actor != "" {
		lease = append(lease, "--actor="+actor)
	}
	if code := doBdScoped(cityName, rigName, lease, stdout, stderr); code != 0 {
		return code
	}
	stamp := bdHeartbeatNow().UTC().Format(time.RFC3339)
	return doBdScoped(cityName, rigName, []string{"update", id, "--set-metadata", heartbeatMetadataKey + "=" + stamp}, stdout, stderr)
}

func doBd(args []string, stdout, stderr io.Writer) int {
	cityName, rigName, bdArgs := extractBdScopeFlags(args)

	if id, ok, err := parseBdHeartbeatArgs(bdArgs); ok {
		if err != nil {
			fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return doBdHeartbeat(cityName, rigName, id, stdout, stderr)
	}

	return doBdScoped(cityName, rigName, bdArgs, stdout, stderr)
}

// doBdScoped is the single guarded handoff to the bd binary, taking the scope
// already extracted from the caller's args so a multi-write subcommand can
// reuse it without re-serializing --city / --rig back into a string.
func doBdScoped(cityName, rigName string, bdArgs []string, stdout, stderr io.Writer) int {
	// Refuse a dropped --set-metadata pair before any store work, so nothing is
	// written and the exit code is honest. bd applies the subset and exits 0.
	if msg, mistyped := mistypedMetadataPairRefusal(bdArgs); mistyped {
		fmt.Fprint(stderr, msg) //nolint:errcheck // best-effort stderr
		return 1
	}

	cityPath, err := resolveBdCity(cityName)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	// Use the full config load path (includes pack expansion + site
	// binding overlay) so migrated rigs (path only in .gc/site.toml)
	// resolve to their bound path. A raw config.Load here would make
	// every already-migrated rig look unbound and fail the new guard
	// in resolveBdScopeTarget / bdRigScopeTarget.
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: loading config: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	target, err := resolveBdScopeTarget(cfg, cityPath, rigName, bdArgs, cityName != "", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if id, expectedAssignee, ok, err := parseBdReleaseIfCurrentArgs(bdArgs); ok || err != nil {
		if err != nil {
			fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
			return 1
		}
		return doBdReleaseIfCurrent(cityPath, cfg, target, id, expectedAssignee, stdout, stderr)
	}
	if provider := rawBeadsProviderForScope(target.ScopeRoot, cityPath); !providerUsesBdStoreContract(provider) {
		fmt.Fprintf(stderr, "gc bd: only supported for bd-backed beads providers (resolved %q for %s)\n", provider, target.ScopeRoot) //nolint:errcheck // best-effort stderr
		if hint := bdProviderMismatchHint(target.ScopeRoot, provider); hint != "" {
			fmt.Fprintf(stderr, "  hint: %s\n", hint) //nolint:errcheck // best-effort stderr
		}
		return 1
	}

	// Pre-flight exact-ID guard for write-mutating subcommands (gcy-g4o).
	// bd's fuzzy/substring resolver can silently match a longer ID that
	// contains the supplied ID as a substring (e.g. "gcy-dv7" → "gcy-wisp-dv78").
	// Verify via BdStore.Get — which already enforces an exact-ID match —
	// before forwarding any mutation to the bd subprocess.
	//
	// Fail-closed: if the arg scanner reports ambiguity (unrecognized
	// value-consuming flag), the command is rejected rather than forwarded
	// unguarded.
	//
	// Tradeoff: only a genuine ErrIDCollision (bd returned a *different* bead
	// than requested) blocks the write. ErrNotFound and store-unavailable are
	// non-fatal — the write falls through to bd, which will produce its own
	// error if the bead truly does not exist. This preserves correctness for
	// legitimate flows (heartbeat metadata writes, silent-fallback paths,
	// ephemeral/wisp rows, projection-lag writes) that proceed even when the
	// bead isn't yet visible through the read seam.
	//
	// Note: gc bd show (read passthrough) does NOT have this guard and still
	// substring-resolves. That is intentional — reads are non-destructive.
	//
	// guardStore/guardBeads capture the store this guard opens and the beads
	// it reads so the work-record close gate below can reuse them instead of
	// opening the store and re-fetching the same bead a second time.
	var (
		guardStore beads.Store
		guardBeads map[string]beads.Bead
	)
	if writeIDs, writeOK, ambiguous := bdMutationWriteIDs(bdArgs); writeOK {
		if ambiguous {
			fmt.Fprintf(stderr, "gc bd: cannot safely verify bead IDs (unrecognized flag in args %v); aborting to prevent substring-resolution mutation of the wrong bead\n", bdArgs) //nolint:errcheck // best-effort stderr
			return 1
		}
		if len(writeIDs) > 0 {
			store, storeErr := openStoreAtForCityWithConfig(target.ScopeRoot, cityPath, cfg)
			// Store-unavailable: we cannot verify, but we must not block
			// legitimate writes. Fall through; bd will error on actual problems.
			if storeErr == nil {
				guardStore = store
				guardBeads = make(map[string]beads.Bead, len(writeIDs))
				for _, id := range writeIDs {
					bead, getErr := store.Get(id)
					if errors.Is(getErr, beads.ErrIDCollision) {
						// bd resolved a different bead — block the write to prevent
						// mutating the wrong bead via substring resolution.
						fmt.Fprintf(stderr, "gc bd: bead %q resolved to a different bead ID (substring collision); aborting to prevent mutating the wrong bead\n", id) //nolint:errcheck // best-effort stderr
						return 1
					}
					if getErr == nil {
						guardBeads[id] = bead
					}
					// ErrNotFound or any other error: bead may be absent, ephemeral,
					// or the read seam differs from the write seam — fall through.
				}
			}
		}
	}

	// Work-record close gate (ADR-0009): a close routed through the SDK seam
	// must satisfy the typed work-record contract (gc.work_outcome present;
	// shipped ⇒ gc.work_commit reachable on gc.work_branch). Warn-only by default;
	// blocks the close only when GC_WORK_RECORD_ENFORCE is set. Reuses the
	// store/beads the write-ID guard above already opened and read, and the
	// config the caller already loaded.
	if runWorkRecordCloseGate(bdArgs, target.ScopeRoot, cityPath, cfg, guardStore, guardBeads, stderr) {
		return 1
	}

	reapStaleBdExportJSONL(target.ScopeRoot)
	warnExternalBdOverrideDrift(stderr, cityPath, target)

	// Resolve the same binary every other bd path in the tree resolves for
	// this scope: a scope bound to a complete storage binding pins the bd
	// build that speaks that backend, and the passthrough must honor the pin
	// or it hands the command to an ambient bd that rejects the bound
	// backend. Keying on the target scope rather than the city keeps a rig
	// that owns its binding on its pin, and keeps a rig that overrides the
	// city backend on the ambient bd its runtime env already implies.
	bdPath, err := resolveBdBinaryForScope(cityPath, target.ScopeRoot)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}

	cmd := exec.Command(bdPath, bdArgs...)
	cmd.Dir = target.ScopeRoot
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	// Tee stderr through a bounded head buffer alongside the operator's
	// pipe so we can scan it post-exec for bd's silent-fallback-to-on-disk
	// marker. Only stderr is teed: bd writes its auto-import banner there,
	// not to stdout. See gastownhall/gascity#2080 (update path) and #2079
	// (close path) — both go through this handoff.
	stderrScan := &headLimitedWriter{limit: bdStderrScanLimit}
	cmd.Stderr = io.MultiWriter(stderr, stderrScan)
	env, err := bdCommandEnv(cityPath, cfg, target)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	cmd.Env = workQueryEnvForDir(env, cmd.Dir)

	traceStart := time.Now()
	runErr := cmd.Run()
	traceExit := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			traceExit = exitErr.ExitCode()
		} else {
			traceExit = -1
		}
	}
	beads.TraceBDCall("go:gc-bd-passthrough", target.ScopeRoot, bdArgs, traceStart, traceExit, runErr)

	if runErr != nil {
		if traceExit > 0 {
			return traceExit
		}
		fmt.Fprintf(stderr, "gc bd: %v\n", runErr) //nolint:errcheck // best-effort stderr
		return 1
	}

	// bd exited 0 — but if its stderr shows the silent fallback to on-disk
	// auto-import, the managed Dolt server was unreachable and any write in
	// this command was dropped (managed Gas City sets BD_EXPORT_AUTO=false;
	// see applyExportSuppressionEnv in cmd/gc/bd_env.go). Surface that as a
	// hard error instead of a misleading exit 0. One check here covers the
	// whole bd-write-persistence quad (gastownhall/gascity#2079 / #2080 /
	// #2149 / #2150) because every bd subcommand routes through this
	// handoff. A non-zero bd exit is intentionally left to the block above:
	// the existing transport-retry classifier already handles the
	// timeout+marker case, and overriding a real bd exit code here would
	// mask it. (Root cause fixed upstream in beads post-#3691; this surfaces
	// the symptom for deployments still on stable bd builds.)
	if bdOutputIndicatesSilentFallback(stderrScan.String()) {
		fmt.Fprintln(stderr, bdSilentFallbackUserMessage) //nolint:errcheck // best-effort stderr
		return bdSilentFallbackExitCode
	}

	return 0
}

// parseBdReleaseIfCurrentArgs recognizes gc's own `release-if-current
// <issue-id> <assignee>`, returning the pair. The verb is registered in
// bd_intercepts.go as a name bd does not use, which is what fails the build if
// a beads bump ever claims it.
func parseBdReleaseIfCurrentArgs(args []string) (id, expectedAssignee string, ok bool, err error) {
	if len(args) == 0 || !bdReleaseIfCurrentVerb.claims(args[0]) {
		return "", "", false, nil
	}
	if len(args) != 3 || invalidBdReleaseIfCurrentArg(args[1]) || invalidBdReleaseIfCurrentArg(args[2]) {
		return "", "", true, fmt.Errorf("usage: gc bd release-if-current <issue-id> <assignee>")
	}
	return args[1], args[2], true, nil
}

func invalidBdReleaseIfCurrentArg(value string) bool {
	return value == "" || strings.IndexFunc(value, unicode.IsSpace) >= 0
}

// bdMutationWriteIDs extracts all positional bead IDs from a bd write-mutation
// command (update, close, reopen, delete) and reports whether the scan was
// unambiguous.
//
// Returns:
//   - ids: all positional (non-flag) tokens after the subcommand; may be empty.
//   - ok: false if args is empty or the subcommand is not a write-mutation.
//   - ambiguous: true if the scanner encountered an unrecognized flag that
//     might consume the next argument as its value. In that case the caller
//     must fail-closed — forwarding the command unguarded risks the original
//     substring-resolution bug (gcy-g4o).
//
// The scanner has complete knowledge of every value-consuming flag for each
// subcommand (sourced from `bd <sub> --help`). Unknown flags that start with
// "-" and do not contain "=" are treated as potentially value-consuming, which
// triggers ambiguous=true. Boolean flags (no value) are fine to ignore.
// The "--" terminator is respected: everything after it is positional.
//
// All returned IDs must be verified via BdStore.Get (exact-ID guard) before
// the mutation is forwarded to the bd subprocess.
func bdMutationWriteIDs(args []string) (ids []string, ok bool, ambiguous bool) {
	if len(args) == 0 {
		return nil, false, false
	}
	sub := args[0]
	switch sub {
	case "update", "close", "reopen", "delete":
	default:
		return nil, false, false
	}

	// valueFlags is the complete set of flags that consume the next argument as
	// their value for this subcommand, in both long and short form.
	// Sourced from `bd <sub> --help` (2026-06-10).
	valueFlags := bdSubcmdValueFlags(sub)

	// boolFlags is the complete set of boolean (no-value) flags. Unknown flags
	// not in either set trigger ambiguous=true.
	boolFlags := bdSubcmdBoolFlags(sub)

	positional := false // true after "--"
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if positional {
			if arg != "" {
				ids = append(ids, arg)
			}
			continue
		}
		if arg == "--" {
			positional = true
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			// Positional token — a bead ID (or batch of IDs).
			if arg != "" {
				ids = append(ids, arg)
			}
			continue
		}
		// Flag token.
		// --flag=value form: value is embedded, no next-arg consumed.
		if strings.Contains(arg, "=") {
			continue
		}
		// Strip leading dashes to get the flag name for lookup.
		flagName := strings.TrimLeft(arg, "-")
		// Reconstruct the canonical long or short form for set membership.
		longForm := "--" + flagName
		shortForm := "-" + flagName // only meaningful when flagName is 1 char

		if valueFlags[longForm] || (len(flagName) == 1 && valueFlags[shortForm]) {
			// Known value-consuming flag: skip its value argument.
			i++
			continue
		}
		if boolFlags[longForm] || (len(flagName) == 1 && boolFlags[shortForm]) {
			// Known boolean flag: no value to skip.
			continue
		}
		// Unknown flag. It might consume a value argument that looks like a
		// bead ID. Fail-closed: report ambiguity so the caller can reject.
		return nil, true, true
	}
	return ids, true, false
}

// bdSubcmdValueFlags returns the set of value-consuming flag names (in
// "--long" / "-s" form) for the given bd write-mutation subcommand. Backed
// by internal/bdflags, the single source of truth shared with the `gc
// lint` bd-flag validation check, so the two cannot drift apart.
func bdSubcmdValueFlags(sub string) map[string]bool {
	return bdflags.ValueFlags(sub)
}

// bdSubcmdBoolFlags returns the set of boolean (no-value) flag names for the
// given bd write-mutation subcommand. Backed by internal/bdflags, the
// single source of truth shared with the `gc lint` bd-flag validation
// check, so the two cannot drift apart.
func bdSubcmdBoolFlags(sub string) map[string]bool {
	return bdflags.BoolFlags(sub)
}

// bdMutationWriteID is a compatibility shim retained for callers that only
// need the first ID. Prefer bdMutationWriteIDs for new code.
func bdMutationWriteID(args []string) (string, bool) {
	ids, ok, ambiguous := bdMutationWriteIDs(args)
	if !ok || ambiguous || len(ids) == 0 {
		return "", false
	}
	return ids[0], true
}

func doBdReleaseIfCurrent(cityPath string, cfg *config.City, target execStoreTarget, id, expectedAssignee string, stdout, stderr io.Writer) int {
	store, err := openStoreAtForCityWithConfig(target.ScopeRoot, cityPath, cfg)
	if err != nil {
		fmt.Fprintf(stderr, "gc bd release-if-current: opening store: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	releaser, ok := store.(beads.ConditionalAssignmentReleaser)
	if !ok {
		fmt.Fprintf(stderr, "gc bd release-if-current: %v for %T\n", beads.ErrConditionalReleaseUnsupported, store) //nolint:errcheck // best-effort stderr
		return 1
	}
	released, err := releaser.ReleaseIfCurrent(id, expectedAssignee)
	if err != nil {
		if errors.Is(err, beads.ErrBDSilentFallback) {
			fmt.Fprintf(stderr, "gc bd release-if-current: %v\n", err) //nolint:errcheck // best-effort stderr
			fmt.Fprintln(stderr, bdSilentFallbackUserMessage)          //nolint:errcheck // best-effort stderr
			return bdSilentFallbackExitCode
		}
		fmt.Fprintf(stderr, "gc bd release-if-current: %v\n", err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if released {
		fmt.Fprintln(stdout, "released") //nolint:errcheck // best-effort stdout
		return 0
	}
	fmt.Fprintln(stdout, "skipped") //nolint:errcheck // best-effort stdout
	return 0
}

func resolveBdCity(cityName string) (string, error) {
	if strings.TrimSpace(cityName) != "" {
		return validateCityPath(cityName)
	}
	return resolveCity()
}

// extractBdScopeFlags extracts gc-owned --city/--rig flags from the raw
// argument list and returns the requested city, rig, and remaining bd args.
// It also falls back to cobra's persistent globals for "gc --city X --rig Y bd".
func extractBdScopeFlags(args []string) (string, string, []string) {
	var cityName string
	var rigName string
	var rest []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--city" && i+1 < len(args):
			cityName = args[i+1]
			i++
			continue
		case strings.HasPrefix(args[i], "--city="):
			cityName = strings.TrimPrefix(args[i], "--city=")
			continue
		case args[i] == "--rig" && i+1 < len(args):
			rigName = args[i+1]
			i++
			continue
		case strings.HasPrefix(args[i], "--rig="):
			rigName = strings.TrimPrefix(args[i], "--rig=")
			continue
		}
		rest = append(rest, args[i])
	}
	if cityName == "" && cityFlag != "" {
		cityName = cityFlag
	}
	if rigName == "" && rigFlag != "" {
		rigName = rigFlag
	}
	return cityName, rigName, rest
}

// extractRigFlag extracts --rig <name> from the argument list and returns
// the rig name and remaining args. Also checks the global rigFlag set by
// cobra's persistent flag parsing (for "gc --rig foo bd list" syntax).
func extractRigFlag(args []string) (string, []string) {
	_, rigName, rest := extractBdScopeFlags(args)
	return rigName, rest
}

// extractBdDirectoryFlag returns the -C / --directory value from bd passthrough
// args, or "" if not present. The flag is left in args so bd itself still sees it.
func extractBdDirectoryFlag(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "-C" || args[i] == "--directory") && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(args[i], "--directory="):
			return strings.TrimPrefix(args[i], "--directory=")
		}
	}
	return ""
}

// resolveBdScopeTarget determines the canonical scope root for a bd command.
// Priority: explicit rig name > explicit city > bead prefix auto-detection > -C
// dir rig match > GC_RIG env > GC_BEADS_SCOPE_ROOT env > enclosing rig > city
// root.
//
// stderr receives a best-effort warning when a set-but-unresolvable GC_RIG is
// discarded (see the GC_RIG block below); pass io.Discard when the caller does
// not care.
func resolveBdScopeTarget(cfg *config.City, cityPath, rigName string, args []string, cityExplicit bool, stderr io.Writer) (execStoreTarget, error) {
	resolveRigPaths(cityPath, cfg.Rigs)
	if rigName != "" {
		rig, ok := rigByName(cfg, rigName)
		if !ok {
			return execStoreTarget{}, fmt.Errorf("rig %q not found", rigName)
		}
		if strings.TrimSpace(rig.Path) == "" {
			return execStoreTarget{}, fmt.Errorf("rig %q is declared but has no path binding — run `gc rig add <dir> --name %s` to bind it before scoping bd commands", rig.Name, rig.Name)
		}
		return bdRigScopeTarget(cityPath, rig), nil
	}

	cityTarget := bdCityScopeTarget(cityPath, cfg)

	// An explicit --city pins the city store, symmetric with explicit --rig:
	// a deliberate city scope must never be silently downgraded to a rig store
	// by bead-prefix / GC_RIG-env / cwd auto-detection below. Without this,
	// `gc bd --city <path> list` returned cwd/rig-scoped results, mis-scoping
	// scripts that trusted the flag. (gastownhall/gascity#3410)
	if cityExplicit {
		return cityTarget, nil
	}

	cityPrefix := config.EffectiveHQPrefix(cfg)
	if cityPrefix != "" {
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") || beadPrefix(cfg, arg) != cityPrefix {
				continue
			}
			if bdBeadExists(cityPath, cfg, cityTarget, arg) {
				return cityTarget, nil
			}
		}
	}

	// Auto-detect from bead IDs in args, but only accept candidates that
	// actually exist in the resolved rig store. This keeps hyphenated flag
	// values and other non-ID args from silently retargeting the command.
	// Unbound rigs are skipped so we don't alias them to the city store.
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if rig, ok := bdRigForArg(cfg, arg); ok {
			if strings.TrimSpace(rig.Path) == "" {
				continue
			}
			target := bdRigScopeTarget(cityPath, rig)
			if bdBeadExists(cityPath, cfg, target, arg) {
				return target, nil
			}
		}
	}

	// Honor -C / --directory passed to bd: if it names a path inside a
	// registered rig, use that rig's store. This lets `gc bd create -C
	// /path/to/packs-rig ...` route to the packs rig even when GC_RIG
	// or cwd point elsewhere. The flag stays in bdArgs so bd itself still
	// sees it and changes directory accordingly.
	if cdDir := extractBdDirectoryFlag(args); cdDir != "" {
		if rig, ok, err := resolveRigForDir(cfg, cityPath, cdDir); err != nil {
			return execStoreTarget{}, err
		} else if ok {
			return bdRigScopeTarget(cityPath, rig), nil
		}
	}

	// Honor GC_RIG env (set by the controller on every rig agent) when no
	// explicit --rig flag was given and no bead-ID in the args matched a
	// specific store. This is a weaker signal than an explicit flag or a
	// bead-prefix hit, but a stronger default than cwd: the controller sets
	// GC_RIG reliably, while cwd detection fails for polecat worktrees (they
	// live under .gc/worktrees/, not the configured rig path).
	// Priority: explicit --rig > bead-prefix detect > GC_RIG env > cwd > city.
	gcRigDiscarded := ""
	if gcRig := strings.TrimSpace(os.Getenv("GC_RIG")); gcRig != "" {
		if rig, ok := rigByName(cfg, gcRig); ok && strings.TrimSpace(rig.Path) != "" {
			return bdRigScopeTarget(cityPath, rig), nil
		}
		// GC_RIG names an unknown or unbound rig. Unlike an explicit --rig
		// (which exits 1 on the identical value), we do not error: falling
		// through to cwd/city keeps cross-city queries working from rig agents
		// whose GC_RIG names a rig this city does not bind. But the discard
		// must not be silent — a stale or typo'd GC_RIG would otherwise
		// redirect a query to a different store than the operator intended with
		// no diagnostic, while the same value via --rig fails loudly. Record it
		// and warn below, naming the store actually answered.
		gcRigDiscarded = gcRig
	}

	// Honor GC_BEADS_SCOPE_ROOT above cwd. gc stamps it on every session it
	// launches, from the agent's own config -- the city path for a city-scoped
	// agent, the rig root for a rig-scoped one (template_resolve.go, which also
	// clears GC_RIG for city agents, so the tier above cannot answer for them).
	// It states which store the session's WORK lives in; cwd only states where
	// the session happens to stand.
	//
	// For an agent whose work_dir is a worktree of some other rig those two
	// disagree, and the disagreement was silent and asymmetric (ci-qbkr): the
	// worktree's .beads/redirect routed every bd command carrying no bead id to
	// that rig, so `gc bd show <city-bead>` answered from the city store via the
	// prefix tier above while `gc bd create` filed into the rig and printed a
	// confident "Created issue: gs-5u8". An agent that verified its own setup
	// with a read therefore got a passing answer and filed into the other store
	// anyway, and the follow-up landed assigned to an agent in a store no such
	// agent reads -- zero pool demand, so it waited for a human to notice.
	//
	// This is the same var scopedBeadsProviderOverride (providers.go) compares
	// against the resolved scope root to decide the beads provider, so a target
	// that disagrees with it was also silently discarding the session's pinned
	// provider and re-peeking city.toml.
	//
	// Not a tier above GC_RIG: GC_RIG names one rig, which is the more specific
	// statement, and gc never emits the two in conflict. Operator shells carry
	// no GC_BEADS_SCOPE_ROOT at all, so `cd` into a rig and `gc bd list` is
	// untouched.
	target := cityTarget
	scopeRootHonored := false
	scopeRootDiscarded := ""
	if scopeRoot := strings.TrimSpace(os.Getenv("GC_BEADS_SCOPE_ROOT")); scopeRoot != "" {
		resolved := resolveStoreScopeRoot(cityPath, scopeRoot)
		if samePath(resolved, resolveStoreScopeRoot(cityPath, cityPath)) {
			// The city case is settled before resolveRigForDir gets a look
			// because that helper also follows a .beads/redirect found under
			// the path it is given. The city root holds the real store and
			// never a redirect, and letting a stray file retarget the one
			// scope that must stay fixed is the failure this tier exists to
			// end, not one to reintroduce.
			scopeRootHonored = true
		} else if rig, ok, rerr := resolveRigForDir(cfg, cityPath, resolved); rerr == nil && ok {
			target = bdRigScopeTarget(cityPath, rig)
			scopeRootHonored = true
		} else {
			// Names neither this city nor a bound rig. Mirrors the GC_RIG
			// discard directly above: fall through to cwd rather than error,
			// because a cross-city query from an agent stamped for another city
			// still has to work -- but do not do it silently.
			scopeRootDiscarded = scopeRoot
		}
	}
	if !scopeRootHonored {
		if rig, ok, err := bdRigFromCwd(cfg, cityPath); err != nil {
			return execStoreTarget{}, err
		} else if ok {
			// resolveRigForDir already skips unbound rigs, so rig.Path is
			// guaranteed non-empty here.
			target = bdRigScopeTarget(cityPath, rig)
		}
	}

	if scopeRootDiscarded != "" {
		fmt.Fprintf(stderr, "gc bd: warning: GC_BEADS_SCOPE_ROOT=%q names neither this city nor a bound rig; ignoring it and answering from the %s store instead\n", scopeRootDiscarded, scopeLabel(target)) //nolint:errcheck // best-effort stderr
	}
	if gcRigDiscarded != "" {
		fmt.Fprintf(stderr, "gc bd: warning: GC_RIG=%q does not name a bound rig in this city; ignoring it and answering from the %s store instead (the same value via --rig would exit 1)\n", gcRigDiscarded, scopeLabel(target)) //nolint:errcheck // best-effort stderr
	}
	return target, nil
}

// scopeLabel renders a store target for operator-facing diagnostics, e.g.
// `city` or `rig "packs"`.
func scopeLabel(t execStoreTarget) string {
	if t.ScopeKind == "rig" && strings.TrimSpace(t.RigName) != "" {
		return fmt.Sprintf("rig %q", t.RigName)
	}
	return t.ScopeKind
}

func bdRigForArg(cfg *config.City, arg string) (config.Rig, bool) {
	if prefix := beadPrefix(cfg, arg); prefix != "" {
		return findRigByPrefix(cfg, prefix)
	}
	return config.Rig{}, false
}

func bdRigFromCwd(cfg *config.City, cityPath string) (config.Rig, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return config.Rig{}, false, nil
	}
	return resolveRigForDir(cfg, cityPath, cwd)
}

func bdRigScopeTarget(cityPath string, rig config.Rig) execStoreTarget {
	return execStoreTarget{
		ScopeRoot: resolveStoreScopeRoot(cityPath, rig.Path),
		ScopeKind: "rig",
		Prefix:    rig.EffectivePrefix(),
		RigName:   rig.Name,
	}
}

func bdCityScopeTarget(cityPath string, cfg *config.City) execStoreTarget {
	return execStoreTarget{
		ScopeRoot: resolveStoreScopeRoot(cityPath, cityPath),
		ScopeKind: "city",
		Prefix:    config.EffectiveHQPrefix(cfg),
	}
}
