package clienttelemetry

import (
	"context"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type Service struct {
	store store.ClientTelemetryStore
}

func NewService(telemetryStore store.ClientTelemetryStore) *Service {
	return &Service{store: telemetryStore}
}

func (s *Service) Record(ctx context.Context, userID int64, kind domain.ClientTelemetryKind, peer domain.Peer, subjectIDs []int64, payload any, createdAt time.Time) (domain.ClientTelemetryEvent, bool, error) {
	if s == nil || s.store == nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("client telemetry store is not configured")
	}
	event, err := domain.NewClientTelemetryEvent(
		userID, kind, peer, subjectIDs, payload, createdAt,
	)
	if err != nil {
		return domain.ClientTelemetryEvent{}, false, err
	}
	return s.store.CreateClientTelemetry(ctx, event)
}
