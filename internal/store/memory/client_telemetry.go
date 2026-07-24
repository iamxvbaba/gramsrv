package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"telesrv/internal/domain"
)

type ClientTelemetryStore struct {
	mu            sync.Mutex
	nextID        int64
	byID          map[int64]domain.ClientTelemetryEvent
	byFingerprint map[[32]byte]int64
}

func NewClientTelemetryStore() *ClientTelemetryStore {
	return &ClientTelemetryStore{
		nextID: 1, byID: make(map[int64]domain.ClientTelemetryEvent),
		byFingerprint: make(map[[32]byte]int64),
	}
}

func (s *ClientTelemetryStore) CreateClientTelemetry(_ context.Context, event domain.ClientTelemetryEvent) (domain.ClientTelemetryEvent, bool, error) {
	if err := event.Validate(); err != nil || event.ID != 0 {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.byFingerprint[event.Fingerprint]; ok {
		return cloneClientTelemetry(s.byID[id]), false, nil
	}
	var hourly, daily int
	for _, existing := range s.byID {
		if existing.UserID != event.UserID ||
			existing.CreatedAt.After(event.CreatedAt) {
			continue
		}
		if !existing.CreatedAt.Before(event.CreatedAt.Add(-24 * time.Hour)) {
			daily++
		}
		if !existing.CreatedAt.Before(event.CreatedAt.Add(-time.Hour)) {
			hourly++
		}
	}
	if hourly >= domain.MaxClientTelemetryEventsPerHour ||
		daily >= domain.MaxClientTelemetryEventsPerDay {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryRateLimited
	}
	event.ID = s.nextID
	s.nextID++
	event = cloneClientTelemetry(event)
	s.byID[event.ID] = event
	s.byFingerprint[event.Fingerprint] = event.ID
	return cloneClientTelemetry(event), true, nil
}

func (s *ClientTelemetryStore) DeleteExpiredClientTelemetry(_ context.Context, olderThan time.Time, limit int) (int, error) {
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrClientTelemetryInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0)
	for id, event := range s.byID {
		if event.CreatedAt.Before(olderThan) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	for _, id := range ids {
		event := s.byID[id]
		delete(s.byFingerprint, event.Fingerprint)
		delete(s.byID, id)
	}
	return len(ids), nil
}

func (s *ClientTelemetryStore) Events() []domain.ClientTelemetryEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.ClientTelemetryEvent, 0, len(s.byID))
	for _, event := range s.byID {
		out = append(out, cloneClientTelemetry(event))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func cloneClientTelemetry(event domain.ClientTelemetryEvent) domain.ClientTelemetryEvent {
	event.SubjectIDs = append([]int64(nil), event.SubjectIDs...)
	event.Payload = append([]byte(nil), event.Payload...)
	return event
}
