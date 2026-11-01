package store

import (
	"context"

	"telesrv/internal/domain"
)

// MessageStore persists messages from the account's point of view.
type MessageStore interface {
	ScheduledMessageStore
	HistoryTTLStore

	Create(ctx context.Context, msg domain.Message) (domain.Message, error)
	SendPrivateText(ctx context.Context, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
	ForwardPrivateMessages(ctx context.Context, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error)
	ReadHistory(ctx context.Context, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error)
	ReadMessageContents(ctx context.Context, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error)
	GetOutboxReadDate(ctx context.Context, req domain.OutboxReadDateRequest) (int, error)
	SetMessageReactions(ctx context.Context, req domain.SetPrivateMessageReactionsRequest, effects DeliveryEffectsBuilder[domain.PrivateMessageReactionsResult]) (domain.PrivateMessageReactionsResult, error)
	GetMessageReactions(ctx context.Context, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error)
	ListSavedReactionTags(ctx context.Context, req domain.SavedReactionTagsRequest) ([]domain.SavedReactionTag, error)
	UpsertSavedReactionTag(ctx context.Context, tag domain.SavedReactionTag, effects DeliveryEffectsBuilder[domain.SavedReactionTag]) error
	VoteMessagePoll(ctx context.Context, req domain.VotePrivateMessagePollRequest, effects DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error)
	CloseMessagePoll(ctx context.Context, req domain.ClosePrivateMessagePollRequest, effects DeliveryEffectsBuilder[domain.PrivateMessagePollResult]) (domain.PrivateMessagePollResult, error)
	EditMessage(ctx context.Context, req domain.EditMessageRequest) (domain.EditMessageResult, error)
	PinPrivateMessage(ctx context.Context, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error)
	UnpinAllPrivateMessages(ctx context.Context, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error)
	DeleteMessages(ctx context.Context, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error)
	DeleteHistory(ctx context.Context, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error)
	GetByIDs(ctx context.Context, userID int64, ids []int) (domain.MessageList, error)
	ListByUser(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error)
	// SearchPrivateMedia returns messages in a private-chat dialog that belong to the given media category (shared media tab), paginated newest-first.
	SearchPrivateMedia(ctx context.Context, ownerUserID, peerID int64, req domain.MediaSearchRequest) (domain.MessageList, error)
	// CountPrivateMediaCategories returns exact counts for a private-chat dialog aggregated by base media category.
	CountPrivateMediaCategories(ctx context.Context, ownerUserID, peerID int64) (domain.MediaCategoryCounts, error)
	// Saved Messages sub-dialogs (self-chat sub-dialogs grouped by saved_peer).
	ListSavedDialogs(ctx context.Context, userID int64, filter domain.SavedDialogsFilter) (domain.SavedDialogList, error)
	ListPinnedSavedDialogs(ctx context.Context, userID int64) (domain.SavedDialogList, error)
	ListSavedDialogsByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.SavedDialogList, error)
	MutateSavedDialogs(ctx context.Context, mutation SavedDialogMutation, effects DeliveryEffectsBuilder[SavedDialogMutationSnapshot]) (SavedDialogMutationSnapshot, error)
	DeleteSavedHistory(ctx context.Context, req domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error)
}
