package store

import (
	"context"

	"telesrv/internal/domain"
)

// TranscriptionStore persists voice->text transcriptions so results are reused across calls and process restarts.
type TranscriptionStore interface {
	GetTranscription(ctx context.Context, documentID int64) (domain.MessageTranscription, bool, error)
	// CreateTranscriptionPending inserts only when no row exists; created=false means it already existed (the caller re-reads to branch).
	CreateTranscriptionPending(ctx context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error)
	// ResetTranscriptionPending changes a failed row back to pending and records the new requester; only failed rows can be reset.
	ResetTranscriptionPending(ctx context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error)
	MarkTranscriptionDone(ctx context.Context, documentID int64, text string) error
	MarkTranscriptionFailed(ctx context.Context, documentID int64, failure string) error
}
