package domain

// BotAPIUpdateKind is the Bot API delivery shape for a queued update.
type BotAPIUpdateKind string

const (
	BotAPIUpdateMessage       BotAPIUpdateKind = "message"
	BotAPIUpdateEditedMessage BotAPIUpdateKind = "edited_message"
	BotAPIUpdateCallbackQuery BotAPIUpdateKind = "callback_query"
)

// BotCallbackQuery is the protocol-neutral payload shared by MTProto
// updateBotCallbackQuery and the HTTP Bot API CallbackQuery projection.
type BotCallbackQuery struct {
	ID            int64
	BotUserID     int64
	UserID        int64
	Peer          Peer
	MessageID     int
	ChatInstance  int64
	Data          []byte
	InlineMessage *BotInlineMessageID
}

// BotInlineMessageID is the domain-only shape of inputBotInlineMessageID64.
// It can be projected both to MTProto and to Bot API's opaque
// inline_message_id without leaking tg types into the store boundary.
type BotInlineMessageID struct {
	DCID       int
	OwnerID    int64
	ID         int
	AccessHash int64
}

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
	Callback  *BotCallbackQuery
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
	Callback  *BotCallbackQuery
}
