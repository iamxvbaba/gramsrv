package store

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// EphemeralMessageStore is the short-lived authoritative state used for
// idempotency, callback, edit, delete and report lookups. Implementations must
// make Create atomic across the message ID and random-ID indexes.
type EphemeralMessageStore interface {
	CreateEphemeralMessage(ctx context.Context, message domain.EphemeralMessage) (stored domain.EphemeralMessage, created bool, err error)
	GetEphemeralMessage(ctx context.Context, peer domain.Peer, id int, now time.Time) (domain.EphemeralMessage, bool, error)
	EditEphemeralMessage(ctx context.Context, peer domain.Peer, id int, expectedVersion uint64, content domain.EphemeralContent, editDate int, now time.Time) (domain.EphemeralMessage, error)
	DeleteEphemeralMessage(ctx context.Context, peer domain.Peer, id int, expectedVersion uint64, now time.Time) (domain.EphemeralMessage, bool, error)
	PruneExpiredEphemeralMessages(ctx context.Context, now time.Time, limit int) (int, error)
	PutEphemeralCallbackAction(ctx context.Context, action domain.EphemeralCallbackAction) (bool, error)
	GetEphemeralCallbackAction(ctx context.Context, botUserID, queryID int64, now time.Time) (domain.EphemeralCallbackAction, bool, error)
}

// EphemeralReportStore is deliberately durable: transient messages disappear
// after 48 hours, while a submitted abuse report must retain review evidence.
type EphemeralReportStore interface {
	CreateEphemeralReport(ctx context.Context, report domain.EphemeralAbuseReport) (created bool, err error)
}

type EphemeralPushKind string

const (
	EphemeralPushNew      EphemeralPushKind = "new"
	EphemeralPushEdit     EphemeralPushKind = "edit"
	EphemeralPushDelete   EphemeralPushKind = "delete"
	EphemeralPushCallback EphemeralPushKind = "callback"
)

// EphemeralPush is a process-to-process online accelerator. It is deliberately
// not a durable event: Redis Pub/Sub and ready Layer 228 sessions are the only
// consumers, while EphemeralMessageStore remains the short-lived lookup truth.
type EphemeralPush struct {
	SourceID              string
	Kind                  EphemeralPushKind
	TargetUserID          int64
	TargetBusinessAuthKey [8]byte
	Message               domain.EphemeralMessage
	Callback              *domain.BotCallbackQuery
	Date                  int
}

type EphemeralPushBroker interface {
	PublishEphemeralPush(ctx context.Context, event EphemeralPush) error
	SubscribeEphemeralPushes(ctx context.Context, handle func(context.Context, EphemeralPush)) error
}
