package api

import (
	"context"
	"strings"

	"github.com/gastownhall/gascity/internal/events"
	"github.com/gastownhall/gascity/internal/session"
)

func (s *Server) watchSessionWorkerOperationSignals(ctx context.Context, info session.Info) <-chan struct{} {
	return s.watchWorkerOperationSignals(ctx, info.ID, info.SessionName)
}

// agentSessionID resolves a runtime session name to its session bead ID, or ""
// when no bead answers to it.
func (s *Server) agentSessionID(sessionName string) string {
	sessionName = strings.TrimSpace(sessionName)
	if s == nil || s.state == nil || sessionName == "" {
		return ""
	}
	store := s.state.CityBeadStore()
	if store == nil {
		return ""
	}
	id, err := s.resolveSessionIDWithConfig(store, sessionName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(id)
}

// watchAgentWorkerOperationSignals watches an agent's session, where
// sessionName comes from agentRuntimeSessionName.
func (s *Server) watchAgentWorkerOperationSignals(ctx context.Context, sessionName string) <-chan struct{} {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return nil
	}
	return s.watchWorkerOperationSignals(ctx, s.agentSessionID(sessionName), sessionName)
}

func (s *Server) watchWorkerOperationSignals(ctx context.Context, subjects ...string) <-chan struct{} {
	if s == nil || s.state == nil {
		return nil
	}
	ep := s.state.EventProvider()
	if ep == nil {
		return nil
	}

	watchSubjects := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		subject = strings.TrimSpace(subject)
		if subject == "" {
			continue
		}
		watchSubjects[subject] = struct{}{}
	}
	if len(watchSubjects) == 0 {
		return nil
	}

	afterSeq, err := ep.LatestSeq()
	if err != nil {
		return nil
	}
	watcher, err := ep.Watch(ctx, afterSeq)
	if err != nil {
		return nil
	}

	signals := make(chan struct{}, 1)
	go func() {
		defer close(signals)
		defer watcher.Close() //nolint:errcheck // best-effort cleanup
		for {
			event, err := watcher.Next()
			if err != nil {
				return
			}
			if !workerOperationEventMatchesSubjects(watchSubjects, event) {
				continue
			}
			select {
			case signals <- struct{}{}:
			default:
			}
		}
	}()
	return signals
}

func workerOperationEventMatchesSubjects(subjects map[string]struct{}, event events.Event) bool {
	if event.Type != events.WorkerOperation {
		return false
	}
	subject := strings.TrimSpace(event.Subject)
	if subject == "" {
		return false
	}
	_, ok := subjects[subject]
	return ok
}
