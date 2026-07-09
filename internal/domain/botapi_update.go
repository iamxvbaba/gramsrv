package domain

// BotAPIUpdateKind is the Bot API delivery shape for a queued update.
type BotAPIUpdateKind string

const (
	BotAPIUpdateMessage       BotAPIUpdateKind = "message"
	BotAPIUpdateEditedMessage BotAPIUpdateKind = "edited_message"
)

// BotAPIUpdate is a durable Bot API update cursor. ID is the Bot API update_id
// and is global across all bots, matching Telegram Bot API's monotonic offset
// contract without reusing MTProto pts from user/channel logs.
type BotAPIUpdate struct {
	ID        int64
	BotUserID int64
	Kind      BotAPIUpdateKind
	Peer      Peer
	MessageID int
	SourcePts int
	Date      int
}

// EnqueueBotAPIUpdateRequest describes a message-like update that should be
// delivered to one bot via getUpdates.
type EnqueueBotAPIUpdateRequest struct {
	BotUserID int64
	Kind      BotAPIUpdateKind
	Peer      Peer
	MessageID int
	SourcePts int
	Date      int
}
