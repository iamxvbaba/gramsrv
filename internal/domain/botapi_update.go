package domain

import "time"

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

// BotAPIEphemeralPayload is a self-contained 24-hour Bot API queue snapshot.
// Ordinary queued messages are reloaded from their durable message tables;
// ephemeral messages have no such table and therefore travel in this explicit
// envelope instead of overloading SourcePts or an ordinary message id. The
// public shape deliberately cannot represent random IDs, payload hashes,
// auth-key/session identifiers, or the originating device.
type BotAPIEphemeralPayload struct {
	Message BotAPIEphemeralMessage
	ReplyTo *BotAPIEphemeralMessage `json:",omitempty"`
}

type BotAPIEphemeralMessage struct {
	ID                 int
	Peer               Peer
	SenderUserID       int64
	ReceiverUserID     int64
	Date               int
	EditDate           int
	TopMessageID       int
	ReplyToEphemeralID int
	Content            EphemeralContent
	Version            uint64
	ExpiresAt          time.Time
}

func NewBotAPIEphemeralPayload(message EphemeralMessage) *BotAPIEphemeralPayload {
	payload := &BotAPIEphemeralPayload{Message: publicBotAPIEphemeralMessage(message)}
	if message.BotAPIReply != nil {
		reply := publicBotAPIEphemeralMessage(*message.BotAPIReply)
		payload.ReplyTo = &reply
	}
	return payload
}

func publicBotAPIEphemeralMessage(message EphemeralMessage) BotAPIEphemeralMessage {
	return BotAPIEphemeralMessage{
		ID: message.ID, Peer: message.Peer,
		SenderUserID: message.SenderUserID, ReceiverUserID: message.ReceiverUserID,
		Date: message.Date, EditDate: message.EditDate,
		TopMessageID: message.TopMessageID, ReplyToEphemeralID: message.ReplyToEphemeralID,
		Content: message.Content, Version: message.Version, ExpiresAt: message.ExpiresAt,
	}
}

func (m BotAPIEphemeralMessage) EphemeralMessage() EphemeralMessage {
	return EphemeralMessage{
		ID: m.ID, Peer: m.Peer,
		SenderUserID: m.SenderUserID, ReceiverUserID: m.ReceiverUserID,
		Date: m.Date, EditDate: m.EditDate,
		TopMessageID: m.TopMessageID, ReplyToEphemeralID: m.ReplyToEphemeralID,
		Content: m.Content, Version: m.Version, ExpiresAt: m.ExpiresAt,
	}
}

func (p BotAPIEphemeralPayload) EphemeralMessage() EphemeralMessage {
	message := p.Message.EphemeralMessage()
	if p.ReplyTo != nil {
		reply := p.ReplyTo.EphemeralMessage()
		message.BotAPIReply = &reply
	}
	return message
}

func (p BotAPIEphemeralPayload) Validate() error {
	if err := p.Message.Validate(); err != nil {
		return err
	}
	if p.Message.ReplyToEphemeralID == 0 {
		if p.ReplyTo != nil {
			return ErrEphemeralInvalid
		}
		return nil
	}
	if p.ReplyTo == nil || p.ReplyTo.Validate() != nil || p.ReplyTo.ID != p.Message.ReplyToEphemeralID ||
		p.ReplyTo.Peer != p.Message.Peer || p.ReplyTo.Date > p.Message.Date ||
		!sameEphemeralParticipantPair(p.Message.SenderUserID, p.Message.ReceiverUserID, p.ReplyTo.SenderUserID, p.ReplyTo.ReceiverUserID) {
		return ErrEphemeralInvalid
	}
	return nil
}

func sameEphemeralParticipantPair(firstSender, firstReceiver, secondSender, secondReceiver int64) bool {
	return (firstSender == secondSender && firstReceiver == secondReceiver) ||
		(firstSender == secondReceiver && firstReceiver == secondSender)
}

func (m BotAPIEphemeralMessage) Expired(now time.Time) bool {
	return !m.ExpiresAt.IsZero() && !now.Before(m.ExpiresAt)
}

func (m BotAPIEphemeralMessage) Validate() error {
	date := time.Unix(int64(m.Date), 0)
	if m.ID <= 0 || m.ID > MaxMessageBoxID || m.Peer.Type != PeerTypeChannel || m.Peer.ID <= 0 ||
		m.SenderUserID <= 0 || m.ReceiverUserID <= 0 || m.SenderUserID == m.ReceiverUserID ||
		m.Date <= 0 || m.Version == 0 || m.ExpiresAt.IsZero() || !m.ExpiresAt.After(date) ||
		m.ExpiresAt.Sub(date) > EphemeralMessageRetention+time.Second ||
		(m.EditDate != 0 && m.EditDate < m.Date) || m.TopMessageID < 0 || m.TopMessageID > MaxMessageBoxID ||
		m.ReplyToEphemeralID < 0 || m.ReplyToEphemeralID > MaxMessageBoxID || m.ReplyToEphemeralID == m.ID {
		return ErrEphemeralInvalid
	}
	return ValidateEphemeralContent(m.Content)
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
	Ephemeral *BotAPIEphemeralPayload
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
	Ephemeral *BotAPIEphemeralPayload
}
