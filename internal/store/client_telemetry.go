package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

type ClientTelemetryStore interface {
	CreateClientTelemetry(ctx context.Context, event domain.ClientTelemetryEvent) (domain.ClientTelemetryEvent, bool, error)
	DeleteExpiredClientTelemetry(ctx context.Context, olderThan time.Time, limit int) (int, error)
}
