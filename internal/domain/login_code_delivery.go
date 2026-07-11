package domain

import (
	"fmt"
	"math"
	"strings"
)

const officialLoginCodeMessageTemplate = `Login code: %s. Do not give this code to anyone, even if they say they are from Telegram!

This code can be used to log in to your Telegram account. We never ask it for anything else.

If you didn't request this code by trying to log in on another device, simply ignore this message.`

// LoginCodeDeliveryRequest describes one durable 777000 login-code delivery.
// PhoneCodeHash is an opaque idempotency token and must never be persisted in
// plaintext; store implementations persist only its SHA-256 digest.
type LoginCodeDeliveryRequest struct {
	UserID        int64
	PhoneCodeHash string
	Code          string
	Date          int
	// ExpiresAt is the unix second after which the compact idempotency receipt
	// may be reclaimed. It must cover the corresponding code's usable lifetime.
	ExpiresAt int64
}

// LoginCodeDeliveryResult returns the immutable first delivery. Created is
// false when the same phone_code_hash was already committed and replayed.
type LoginCodeDeliveryResult struct {
	Message Message
	Created bool
}

// OfficialLoginCodeMessage builds the account-visible incoming message from
// Telegram's official notification account. Persistence assigns ID, UID and
// Pts atomically.
func OfficialLoginCodeMessage(userID int64, code string, date int) (Message, error) {
	if userID <= 0 || IsSystemUserID(userID) || strings.TrimSpace(code) == "" || len(code) > 64 || date < 0 || date > math.MaxInt32 {
		return Message{}, fmt.Errorf("%w: user=%d code_length=%d date=%d", ErrLoginCodeDeliveryInvalid, userID, len(code), date)
	}
	body := fmt.Sprintf(officialLoginCodeMessageTemplate, code)
	codeOffset := len("Login code: ")
	return Message{
		OwnerUserID: userID,
		Peer:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		From:        Peer{Type: PeerTypeUser, ID: OfficialSystemUserID},
		Date:        date,
		Body:        body,
		Entities: []MessageEntity{
			{Type: MessageEntityBold, Offset: 0, Length: len("Login code:")},
			{Type: MessageEntityBold, Offset: codeOffset, Length: len(code)},
		},
	}, nil
}
