package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type accountDefaultReactionService interface {
	SetDefaultReaction(ctx context.Context, userID int64, reaction domain.MessageReaction) (domain.AccountReactionSettings, error)
}

type accountReactionSettingsReader interface {
	GetReactionSettings(ctx context.Context, userID int64) (domain.AccountReactionSettings, error)
}

type accountPaidReactionPrivacyService interface {
	accountReactionSettingsReader
	SetPaidReactionPrivacy(ctx context.Context, userID int64, privacy domain.PaidReactionPrivacy, effects store.DeliveryEffectsBuilder[domain.AccountReactionSettings]) (domain.AccountReactionSettings, error)
}

type reliableUserEventRecorder interface {
	RecordReliableUserEvent(ctx context.Context, stateAuthKeyID [8]byte, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
}

type messageReactionUsageRecorder interface {
	RecordMessageReactionUse(ctx context.Context, userID int64, reactions []domain.MessageReaction, addToRecent bool, date int) error
}

type channelParticipantReactionModerator interface {
	DeleteParticipantReaction(ctx context.Context, userID int64, req domain.DeleteChannelParticipantReactionRequest) (domain.ChannelMessageReactionsResult, error)
	DeleteParticipantReactions(ctx context.Context, userID int64, req domain.DeleteChannelParticipantReactionsRequest) (domain.DeleteChannelParticipantReactionsResult, error)
}

type channelHistoryTTLService interface {
	SetHistoryTTL(ctx context.Context, userID, channelID int64, period int, date int) (domain.Channel, []int64, error)
	ClaimExpiredMessages(ctx context.Context, now, limit int) ([]domain.DeleteChannelMessagesRequest, error)
}

type globalSearchHit struct {
	channel   bool
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
