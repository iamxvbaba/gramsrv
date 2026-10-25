package store

import (
	"context"

	"telesrv/internal/domain"
)

// ScheduledMessageStore persists account-owned scheduled messages.
type ScheduledMessageStore interface {
	CreateScheduledMessage(ctx context.Context, req domain.ScheduleMessageRequest, effects DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error)
	EditScheduledMessage(ctx context.Context, req domain.EditScheduledMessageRequest, effects DeliveryEffectsBuilder[domain.ScheduledMessage]) (domain.ScheduledMessage, error)
	ListScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error)
	GetScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error)
	DeleteScheduledMessages(ctx context.Context, filter domain.ScheduledMessageFilter, date int, effects DeliveryEffectsBuilder[[]domain.ScheduledMessage]) ([]domain.ScheduledMessage, error)
	ClaimScheduledMessages(ctx context.Context, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error)
	ClaimDueScheduledMessages(ctx context.Context, now, limit, leaseSeconds int, onlineUserIDs []int64) ([]domain.ScheduledMessage, error)
	ReleaseScheduledMessage(ctx context.Context, ownerUserID int64, id int, generation int64, errText string) error
	HasScheduledMessages(ctx context.Context, ownerUserID int64, peer domain.Peer) (bool, error)
}

// HistoryTTLStore persists peer-level TTL settings and finds expired messages.
type HistoryTTLStore interface {
	GetPrivateHistoryTTL(ctx context.Context, ownerUserID int64, peer domain.Peer) (int, error)
	SetPrivateHistoryTTL(ctx context.Context, ownerUserID int64, peer domain.Peer, period int, effects DeliveryEffectsBuilder[domain.PrivateHistoryTTLResult]) error
	DefaultHistoryTTL(ctx context.Context, userID int64) (int, error)
	SetDefaultHistoryTTL(ctx context.Context, userID int64, period int) error
	ClaimExpiredPrivateMessages(ctx context.Context, now, limit int) ([]domain.DeleteMessagesRequest, error)
}

// ChannelHistoryTTLStore persists channel-level TTL settings and finds expired
// channel messages.
type ChannelHistoryTTLStore interface {
	SetChannelHistoryTTL(ctx context.Context, userID, channelID int64, period int, date int) (domain.Channel, []int64, error)
	ClaimExpiredChannelMessages(ctx context.Context, now, limit int) ([]domain.DeleteChannelMessagesRequest, error)
}
