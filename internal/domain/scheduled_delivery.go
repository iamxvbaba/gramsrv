package domain

import "errors"

const ScheduleWhenOnline = 0x7ffffffe

var ErrScheduledClaimLost = errors.New("scheduled delivery claim lost")
var ErrScheduledLimit = errors.New("scheduled message limit reached")
var ErrScheduleInvalid = errors.New("invalid schedule")

// ScheduledDelivery fences one claimed scheduled message into the send
// transaction: the row lock, the normal message with its PTS/outbox, the
// sent transition and any repeat occurrence commit together.
type ScheduledDelivery struct {
	OwnerUserID int64
	ID          int
	Generation  int64
	// Payload encodes the owner's non-PTS scheduled-box updates while the send
	// transaction is still open. Stores commit it as one durable absolute
	// delivery in that transaction (v2 has no Core push); nil is rejected.
	Payload          ScheduledCompletionPayload `json:"-"`
	ExcludeAuthKeyID [8]byte
	ExcludeSessionID int64
}

// ScheduledCompletion is the scheduled-queue fact of one delivered message:
// the sent row (with SentMessageID) and, for a repeating message, the next
// occurrence.
type ScheduledCompletion struct {
	Sent ScheduledMessage
	Next *ScheduledMessage
}

// ScheduledCompletionPayload builds updateDeleteScheduledMessages(sent) and,
// for a repeat, updateNewScheduledMessage(next) for the owner.
type ScheduledCompletionPayload func(ScheduledCompletion) ([]byte, error)

func ValidScheduleRepeat(period int) bool {
	switch period {
	case 0, 86400, 604800, 1209600, 2592000, 7862400, 15724800, 31536000:
		return true
	}
	return false
}
