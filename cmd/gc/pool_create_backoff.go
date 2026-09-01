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
	poolCreateFailureBackoffBase         = time.Minute
	poolCreateFailureBackoffCeiling      = 30 * time.Minute
	poolCreateFailureBackoffReset        = 2 * time.Hour
)

// poolCreateFailureBackoffActive reports whether a recent failed pool-session
// creation suppresses another attempt for the exact agent and triggering work
// bead. A closed session bead is the durable retry ledger: failures survive a
// controller restart without preventing unrelated work from using the pool.
func poolCreateFailureBackoffActive(sessFront *sessionpkg.Store, template, agent, trigger string, now time.Time) (bool, error) {
	if sessFront == nil || strings.TrimSpace(template) == "" || strings.TrimSpace(agent) == "" || strings.TrimSpace(trigger) == "" {
		return false, nil
	}
	rows, err := sessFront.ListAll(sessionpkg.ListAllOptions{IncludeClosed: true})
	if err != nil {
		return false, fmt.Errorf("listing failed-create history: %w", err)
	}
	for _, row := range rows {
		if strings.TrimSpace(row.Template) != template ||
			strings.TrimSpace(row.SessionOrigin) != "ephemeral" ||
			strings.TrimSpace(row.AgentName) != agent ||
			strings.TrimSpace(row.TriggerBeadID) != trigger ||
			strings.TrimSpace(row.CreateFailureClass) != poolCreateFailureClassAborted {
			continue
		}
		retryAfter, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(row.CreateRetryAfter))
		if parseErr == nil && now.Before(retryAfter) {
			return true, nil
		}
	}
	return false, nil
}

// recordPoolCreateFailureBackoff records a failed pre-creation attempt before
// its session bead is closed. The next create for the same agent and work bead
// observes retry_after from that closed row and uses exponential backoff rather
// than minting a new failed-create bead on every patrol tick.
func recordPoolCreateFailureBackoff(info sessionpkg.Info, sessFront *sessionpkg.Store, now time.Time, cause error) error {
	if sessFront == nil || !isPoolManagedSessionInfo(info) || strings.TrimSpace(info.TriggerBeadID) == "" {
		return nil
	}
	attempts := 1
	if rows, err := sessFront.ListAll(sessionpkg.ListAllOptions{IncludeClosed: true}); err == nil {
		for _, row := range rows {
			if strings.TrimSpace(row.Template) != strings.TrimSpace(info.Template) ||
				strings.TrimSpace(row.SessionOrigin) != "ephemeral" ||
				strings.TrimSpace(row.AgentName) != strings.TrimSpace(info.AgentName) ||
				strings.TrimSpace(row.TriggerBeadID) != strings.TrimSpace(info.TriggerBeadID) ||
				strings.TrimSpace(row.CreateFailureClass) != poolCreateFailureClassAborted {
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
		return fmt.Errorf("reading failed-create history for %s: %w", info.ID, err)
	}
	delay := poolCreateFailureBackoffDelay(attempts)
	patch := sessionpkg.MetadataPatch{
		poolCreateFailureClassMetadataKey:    poolCreateFailureClassAborted,
		poolCreateFailureAtMetadataKey:       now.UTC().Format(time.RFC3339),
		poolCreateFailureAttemptsMetadataKey: strconv.Itoa(attempts),
		poolCreateFailureRetryAfterMetadata:  now.UTC().Add(delay).Format(time.RFC3339),
		poolCreateFailureErrorMetadataKey:    formatLifecycleError(cause),
	}
	if err := sessFront.ApplyPatch(info.ID, patch); err != nil {
		return fmt.Errorf("recording failed-create backoff for %s: %w", info.ID, err)
	}
	return nil
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
