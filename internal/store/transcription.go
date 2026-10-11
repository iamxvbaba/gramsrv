package store

import (
	"context"

	"telesrv/internal/domain"
)

// TranscriptionStore 持久化 voice→text 转写，使结果跨调用、跨进程重启复用。
type TranscriptionStore interface {
	GetTranscription(ctx context.Context, documentID int64) (domain.MessageTranscription, bool, error)
	// CreateTranscriptionPending 仅在无行时插入；created=false 表示已存在（由调用方重读分流）。
	CreateTranscriptionPending(ctx context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error)
	// ResetTranscriptionPending 把 failed 行改回 pending 并登记新的请求方；仅 failed 可重置。
	ResetTranscriptionPending(ctx context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error)
	MarkTranscriptionDone(ctx context.Context, documentID int64, text string) error
	MarkTranscriptionFailed(ctx context.Context, documentID int64, failure string) error
}
