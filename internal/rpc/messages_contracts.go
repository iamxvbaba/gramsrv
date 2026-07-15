package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

type accountDefaultReactionService interface {
	SetDefaultReaction(ctx context.Context, userID int64, reaction domain.MessageReaction) (domain.AccountReactionSettings, error)
}

type accountReactionSettingsReader interface {
	GetReactionSettings(ctx context.Context, userID int64) (domain.AccountReactionSettings, error)
}

type accountPaidReactionPrivacyService interface {
	accountReactionSettingsReader
	SetPaidReactionPrivacy(ctx context.Context, userID int64, privacy domain.PaidReactionPrivacy) (domain.AccountReactionSettings, error)
}

type messageReactionUpdateRecorder interface {
	RecordMessageReactions(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error)
}

type messagePollUpdateRecorder interface {
	RecordMessagePoll(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error)
}

type messageReactionUsageRecorder interface {
	RecordMessageReactionUse(ctx context.Context, userID int64, reactions []domain.MessageReaction, addToRecent bool, date int) error
}

type channelParticipantReactionModerator interface {
	DeleteParticipantReaction(ctx context.Context, userID int64, req domain.DeleteChannelParticipantReactionRequest) (domain.ChannelMessageReactionsResult, error)
	DeleteParticipantReactions(ctx context.Context, userID int64, req domain.DeleteChannelParticipantReactionsRequest) (domain.DeleteChannelParticipantReactionsResult, error)
}

type scheduledMessagesService interface {
	ScheduleMessage(ctx context.Context, userID int64, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error)
	EditScheduledMessage(ctx context.Context, userID int64, req domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error)
	ListScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error)
	GetScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error)
	DeleteScheduledMessages(ctx context.Context, userID int64, filter domain.ScheduledMessageFilter, date int) ([]domain.ScheduledMessage, error)
	ClaimScheduledMessages(ctx context.Context, userID int64, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error)
	ClaimDueScheduledMessages(ctx context.Context, now, limit, leaseSeconds int) ([]domain.ScheduledMessage, error)
	MarkScheduledMessageSent(ctx context.Context, ownerUserID int64, id, sentMessageID, date int) error
	ReleaseScheduledMessage(ctx context.Context, ownerUserID int64, id int, errText string) error
	HasScheduledMessages(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
}

type historyTTLMessagesService interface {
	GetPrivateHistoryTTL(ctx context.Context, userID int64, peer domain.Peer) (int, error)
	SetPrivateHistoryTTL(ctx context.Context, userID int64, peer domain.Peer, period int) error
	DefaultHistoryTTL(ctx context.Context, userID int64) (int, error)
	SetDefaultHistoryTTL(ctx context.Context, userID int64, period int) error
	ClaimExpiredPrivateMessages(ctx context.Context, now, limit int) ([]domain.DeleteMessagesRequest, error)
}

type channelHistoryTTLService interface {
	SetHistoryTTL(ctx context.Context, userID, channelID int64, period int, date int) (domain.Channel, []int64, error)
	ClaimExpiredMessages(ctx context.Context, now, limit int) ([]domain.DeleteChannelMessagesRequest, error)
}

type globalSearchHit struct {
	date      int
	peerRank  int64
	messageID int
	message   tg.MessageClass
}

type forwardSource struct {
	body      string
	entities  []domain.MessageEntity
	media     *domain.MessageMedia
	forward   *domain.MessageForward
	from      domain.Peer
	date      int
	noForward bool
}
