package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	sessionpkg "github.com/gastownhall/gascity/internal/session"
)

const (
	poolCreateFailureClassMetadataKey    = sessionpkg.MetadataCreateFailureClass
	poolCreateFailureAtMetadataKey       = sessionpkg.MetadataCreateFailureAt
	poolCreateFailureAttemptsMetadataKey = sessionpkg.MetadataCreateFailureAttempts
	poolCreateFailureRetryAfterMetadata  = sessionpkg.MetadataCreateRetryAfter
	poolCreateFailureErrorMetadataKey    = sessionpkg.MetadataCreateFailureError
	poolCreateFailureClassAborted        = "provider_start_aborted"
	poolCreateFailureClassClaimNoWork    = "claim_no_work"
	poolCreateFailureBackoffBase         = time.Minute
	poolCreateFailureBackoffCeiling      = 30 * time.Minute
	poolCreateFailureBackoffReset        = 2 * time.Hour
)

// poolCreateFailureBackoffActive reports whether a recent pool-session retry
// failure suppresses another attempt for the exact agent and work trigger. A
// custom scale_check only returns a count, not a work-bead ID; its no-work
// retry ledger is therefore keyed by the empty trigger for that pool identity.
// A closed session bead is durable across controller restarts without
// preventing unrelated triggered work from using the pool.
func poolCreateFailureBackoffActive(sessFront *sessionpkg.Store, template, agent, trigger string, now time.Time) (bool, error) {
	if sessFront == nil || strings.TrimSpace(template) == "" || strings.TrimSpace(agent) == "" {
		return false, nil
	}
	rows, err := sessFront.ListAll(sessionpkg.ListAllOptions{IncludeClosed: true})
	if err != nil {
		return false, fmt.Errorf("listing failed-create history: %w", err)
	}
	for _, row := range rows {
		class := strings.TrimSpace(row.CreateFailureClass)
		if strings.TrimSpace(row.Template) != template ||
			strings.TrimSpace(row.SessionOrigin) != "ephemeral" ||
			strings.TrimSpace(row.AgentName) != agent ||
			!isPoolCreateFailureBackoffClass(class) ||
			strings.TrimSpace(row.TriggerBeadID) != strings.TrimSpace(trigger) {
			continue
		}
		if class == poolCreateFailureClassAborted && strings.TrimSpace(trigger) == "" {
			continue
		}
		retryAfter, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(row.CreateRetryAfter))
		if parseErr == nil && now.Before(retryAfter) {
			return true, nil
		}
	}
	return false, nil
}

func isPoolCreateFailureBackoffClass(class string) bool {
	switch strings.TrimSpace(class) {
	case poolCreateFailureClassAborted, poolCreateFailureClassClaimNoWork:
		return true
	default:
		return false
	}
}

// recordPoolCreateFailureBackoff records a failed pre-creation attempt before
// its session bead is closed. The next create for the same agent and work bead
// observes retry_after from that closed row and uses exponential backoff rather
// than minting a new failed-create bead on every patrol tick.
func recordPoolCreateFailureBackoff(info sessionpkg.Info, sessFront *sessionpkg.Store, now time.Time, cause error) error {
	patch, err := poolSessionRetryBackoffPatch(info, sessFront, now, poolCreateFailureClassAborted, formatLifecycleError(cause))
	if err != nil || len(patch) == 0 {
		return err
	}
	if err := sessFront.ApplyPatch(info.ID, patch); err != nil {
		return fmt.Errorf("recording failed-create backoff for %s: %w", info.ID, err)
	}
	return nil
}

// poolNoWorkDrainBackoffPatch returns the terminal-session patch that throttles
// a completed pool create whose worker immediately drains with no_work. The
// finalizer applies this patch in the same close mutation, so a controller
// restart cannot observe the closed session without its retry boundary.
func poolNoWorkDrainBackoffPatch(info sessionpkg.Info, sessFront *sessionpkg.Store, now time.Time) (sessionpkg.MetadataPatch, error) {
	return poolSessionRetryBackoffPatch(info, sessFront, now, poolCreateFailureClassClaimNoWork, "hook drained with no_work")
}

func poolSessionRetryBackoffPatch(info sessionpkg.Info, sessFront *sessionpkg.Store, now time.Time, class, cause string) (sessionpkg.MetadataPatch, error) {
	if sessFront == nil || !isPoolManagedSessionInfo(info) {
		return nil, nil
	}
	if !isPoolCreateFailureBackoffClass(class) {
		return nil, fmt.Errorf("unknown pool retry class %q", class)
	}
	if class == poolCreateFailureClassAborted && strings.TrimSpace(info.TriggerBeadID) == "" {
		return nil, nil
	}
	attempts := 1
	if rows, err := sessFront.ListAll(sessionpkg.ListAllOptions{IncludeClosed: true}); err == nil {
		for _, row := range rows {
			if strings.TrimSpace(row.Template) != strings.TrimSpace(info.Template) ||
				strings.TrimSpace(row.SessionOrigin) != "ephemeral" ||
				strings.TrimSpace(row.AgentName) != strings.TrimSpace(info.AgentName) ||
				strings.TrimSpace(row.TriggerBeadID) != strings.TrimSpace(info.TriggerBeadID) ||
				strings.TrimSpace(row.CreateFailureClass) != strings.TrimSpace(class) {
				continue
			}
			failedAt, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(row.CreateFailureAt))
			if parseErr != nil || now.Sub(failedAt) > poolCreateFailureBackoffReset {
				continue
			}
			previous, parseErr := strconv.Atoi(strings.TrimSpace(row.CreateFailureAttempts))
			if parseErr == nil && previous >= attempts {
				attempts = previous + 1
			}
		}
	} else {
		return nil, fmt.Errorf("reading pool retry history for %s: %w", info.ID, err)
	}
	delay := poolCreateFailureBackoffDelay(attempts)
	return sessionpkg.MetadataPatch{
		poolCreateFailureClassMetadataKey:    strings.TrimSpace(class),
		poolCreateFailureAtMetadataKey:       now.UTC().Format(time.RFC3339),
		poolCreateFailureAttemptsMetadataKey: strconv.Itoa(attempts),
		poolCreateFailureRetryAfterMetadata:  now.UTC().Add(delay).Format(time.RFC3339),
		poolCreateFailureErrorMetadataKey:    strings.TrimSpace(cause),
	}, nil
}

func poolCreateFailureBackoffDelay(attempts int) time.Duration {
	if attempts <= 1 {
		return poolCreateFailureBackoffBase
	}
	delay := poolCreateFailureBackoffBase
	for i := 1; i < attempts && delay < poolCreateFailureBackoffCeiling; i++ {
		delay *= 2
	}
	if delay > poolCreateFailureBackoffCeiling {
		return poolCreateFailureBackoffCeiling
	}
	return delay
}
