package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

type BootstrapUpdateJobStore interface {
	EnqueueLoginMessage(ctx context.Context, job domain.BootstrapUpdateJob) (domain.BootstrapUpdateJob, error)
	MarkReadyForSession(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64) (int, error)
	ClaimReady(ctx context.Context, limit int, leaseTimeout time.Duration) ([]domain.BootstrapUpdateJob, error)
	MarkPublished(ctx context.Context, id int64) error
	MarkFailed(ctx context.Context, id int64, lastError string) error
}
