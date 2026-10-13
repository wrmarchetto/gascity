package main

// `gc maintenance hold` and `gc maintenance release` -- take the managed Dolt
// server offline for a bounded window, and bring it back (ci-lg7q7t).
//
// Unlike `status` and `dolt-gc` beside them, these run locally and never route
// through the supervisor API. The hold has to work with the bead store down,
// and it is the supervisor's own patrol tick that it must hold off.
//
// Ordering is the whole contract of `hold`: write the hold, THEN take the
// managed-dolt lifecycle lock, THEN stop. A start already in flight holds that
// lock (the provider script's start op and recover-managed both take it) and
// re-checks the hold inside it, so once `hold` owns the lock no later start
// can miss the hold, and the server it stops cannot be replaced. Stopping
// first and writing the hold after would leave a window in which the patrol
// tick restarts the server it just stopped.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Seams for the unit suite. Production wiring is fixed here and nowhere else.
var (
	maintenanceHoldLifecycleOwned   = managedDoltLifecycleOwned
	maintenanceHoldStopServer       = shutdownBeadsProvider
	maintenanceReleaseStartServer   = healthBeadsProvider
	maintenanceHoldLockWait         = 150 * time.Second
	maintenanceHoldLockPollInterval = 250 * time.Millisecond
)

func newMaintenanceHoldCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		holdFor time.Duration
		reason  string
	)
	cmd := &cobra.Command{
		Use:   "hold",
		Short: "Stop the managed Dolt server and keep every gc path from restarting it",
		Long: `Take the managed Dolt server offline for a bounded window, for offline
maintenance such as 'dolt gc --full' on the city database.

The hold is recorded first, then the server is stopped. Until it is released
or expires, every gc path that would start or recover the server refuses and
names the hold instead: the controller patrol tick, every gc bd / hook / mail
env build, and the bd connection-refused retry. Every agent bead command
therefore FAILS for the length of the window.

--for is required and capped at 4h, so a holder that crashes cannot leave the
city without beads past the window it asked for.`,
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc maintenance hold: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			return exitForCode(cmdMaintenanceHold(cityPath, holdFor, reason, stdout, stderr))
		},
	}
	cmd.Flags().DurationVar(&holdFor, "for", 0, "how long the hold lasts before it lapses on its own (max 4h)")
	cmd.Flags().StringVar(&reason, "reason", "", "why the server is held, shown in every refusal")
	_ = cmd.MarkFlagRequired("for")
	_ = cmd.MarkFlagRequired("reason")
	return cmd
}

func newMaintenanceReleaseCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:          "release",
		Short:        "Release a managed Dolt maintenance hold and restart the server",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc maintenance release: %v\n", err) //nolint:errcheck // best-effort stderr
				return errExit
			}
			return exitForCode(cmdMaintenanceRelease(cityPath, stdout, stderr))
		},
	}
}

func cmdMaintenanceHold(cityPath string, holdFor time.Duration, reason string, stdout, stderr io.Writer) int {
	const prefix = "gc maintenance hold"
	reason = strings.TrimSpace(reason)
	if holdFor <= 0 || holdFor > maxManagedDoltMaintenanceHold {
		fmt.Fprintf(stderr, "%s: --for %s is out of range; give a duration above 0 and at most %s, e.g. --for 30m\n", //nolint:errcheck // best-effort stderr
			prefix, holdFor, maxManagedDoltMaintenanceHold)
		return 1
	}
	if reason == "" {
		fmt.Fprintf(stderr, "%s: --reason is empty; say what the window is for, e.g. --reason 'offline dolt gc --full of hq'\n", prefix) //nolint:errcheck // best-effort stderr
		return 1
	}
	owned, err := maintenanceHoldLifecycleOwned(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: determine managed dolt ownership for %s: %v\n", prefix, cityPath, err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !owned {
		fmt.Fprintf(stderr, "%s: %s does not run a managed Dolt server (file backend, external endpoint, or a scope redirected elsewhere), so a hold would guard nothing\n", prefix, cityPath) //nolint:errcheck // best-effort stderr
		return 1
	}
	if err := managedDoltMaintenanceHoldRefusal(cityPath); err != nil {
		fmt.Fprintf(stderr, "%s: a hold is already in force, so this one was NOT taken: %v\n", prefix, err) //nolint:errcheck // best-effort stderr
		return 1
	}

	now := managedDoltMaintenanceHoldNow().UTC()
	hold := managedDoltMaintenanceHold{
		Holder:    managedDoltMaintenanceHolderIdentity(),
		Reason:    reason,
		SetAt:     now,
		ExpiresAt: now.Add(holdFor),
	}
	if err := writeManagedDoltMaintenanceHold(cityPath, hold); err != nil {
		fmt.Fprintf(stderr, "%s: write %s: %v\n", prefix, managedDoltMaintenanceHoldPath(cityPath), err) //nolint:errcheck // best-effort stderr
		return 1
	}

	stopErr := stopManagedDoltUnderLifecycleLock(cityPath)
	if stopErr != nil {
		fmt.Fprintf(stderr, "%s: the hold is recorded until %s, but stopping the managed Dolt server failed: %v\n"+ //nolint:errcheck // best-effort stderr
			"  The server may still be running. Do NOT start offline maintenance until it is down.\n"+
			"  Release the hold with: gc maintenance release\n",
			prefix, hold.ExpiresAt.Format(time.RFC3339), stopErr)
		return 1
	}
	fmt.Fprintf(stdout, "Managed Dolt is stopped and held until %s (%s).\n", hold.ExpiresAt.Format(time.RFC3339), hold.Reason) //nolint:errcheck // best-effort stdout
	fmt.Fprintln(stdout, "Every gc bead command fails until the hold is released: gc maintenance release")                     //nolint:errcheck // best-effort stdout
	return 0
}

// stopManagedDoltUnderLifecycleLock waits for the managed-dolt lifecycle lock,
// then stops the server while holding it. The wait outlasts the 120s start
// and recover op timeout (providerOpTimeout), so an in-flight start always
// finishes or dies before the lock is given up on.
func stopManagedDoltUnderLifecycleLock(cityPath string) error {
	lockFile, _, err := openManagedDoltLifecycleLock(cityPath)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(maintenanceHoldLockWait)
	for {
		locked, err := tryManagedDoltLifecycleLock(lockFile)
		if err != nil {
			_ = lockFile.Close()
			return err
		}
		if locked {
			break
		}
		if time.Now().After(deadline) {
			_ = lockFile.Close()
			return fmt.Errorf("managed dolt lifecycle lock still held after %s by an in-flight start or recover", maintenanceHoldLockWait)
		}
		time.Sleep(maintenanceHoldLockPollInterval)
	}
	defer releaseManagedDoltLifecycleLock(lockFile)
	return maintenanceHoldStopServer(cityPath)
}

func cmdMaintenanceRelease(cityPath string, stdout, stderr io.Writer) int {
	const prefix = "gc maintenance release"
	removed, err := removeManagedDoltMaintenanceHold(cityPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: remove %s: %v\n", prefix, managedDoltMaintenanceHoldPath(cityPath), err) //nolint:errcheck // best-effort stderr
		return 1
	}
	if !removed {
		fmt.Fprintln(stdout, "No managed Dolt maintenance hold was recorded.") //nolint:errcheck // best-effort stdout
	} else {
		fmt.Fprintln(stdout, "Maintenance hold released.") //nolint:errcheck // best-effort stdout
	}
	// Bring the server back now rather than leaving it to the next patrol
	// tick or agent call, so the release is observably complete when this
	// command returns.
	if err := maintenanceReleaseStartServer(cityPath); err != nil {
		fmt.Fprintf(stderr, "%s: the hold is gone, but the managed Dolt server did not come back: %v\n"+ //nolint:errcheck // best-effort stderr
			"  The controller retries on its next patrol tick. To retry now: gc start\n", prefix, err)
		return 1
	}
	fmt.Fprintln(stdout, "Managed Dolt is running.") //nolint:errcheck // best-effort stdout
	return 0
}
