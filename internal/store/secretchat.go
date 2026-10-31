package store

import (
	"context"

	"telesrv/internal/domain"
)

// SecretChatStore persists the authoritative secret chat handshake state machine (dual
// memory/postgres implementations; the behavior contract is pinned by storetest). The server is
// a blind relay: g_a/g_b/key_fingerprint are stored opaquely as-is. Design: see
// docs/secret-chat-module.md.
type SecretChatStore interface {
	// CreateSecretChat inserts a secret chat in the requested state. chat_id must equal
	// requestEncryption.random_id; a primary-key/idempotency-key collision returns
	// domain.ErrSecretChatRandomIDDuplicate and must not allocate another ID.
	CreateSecretChat(ctx context.Context, chat domain.SecretChat) error
	// GetSecretChat fetches a secret chat by chat_id.
	GetSecretChat(ctx context.Context, chatID int) (domain.SecretChat, bool, error)
	// AcceptSecretChat atomically CAS-binds: only when requested and participant_auth_key_id is
	// unbound does it transition to normal, store g_b/key_fingerprint, and bind the accepting
	// device. A concurrent second accept, or an already-formed/discarded chat, returns
	// domain.ErrSecretChatAlreadyAccepted / ErrSecretChatAlreadyDeclined respectively; an absent
	// chat_id returns domain.ErrSecretChatNotFound.
	AcceptSecretChat(ctx context.Context, chatID int, participantAuthKeyID int64, gb []byte, keyFingerprint int64) (domain.SecretChat, error)
	// DiscardSecretChat transitions to discarded (idempotent); when already terminal it returns
	// the current snapshot with already=true; an absent chat_id returns domain.ErrSecretChatNotFound.
	DiscardSecretChat(ctx context.Context, chatID int, historyDeleted bool) (chat domain.SecretChat, already bool, err error)
	// ListActiveSecretChatsByAuthKey returns non-terminal secret chats bound to this device's
	// perm auth_key (as the initiating admin or the accepting participant), ordered by ascending
	// chat_id. Used on device logout / authorization revocation to cascade-discard and notify the
	// peer (avoiding a silent dead link where the peer keeps delivering to a dead auth_key).
	// authKeyID==0 returns nil.
	ListActiveSecretChatsByAuthKey(ctx context.Context, authKeyID int64) ([]domain.SecretChat, error)
}

// EncryptedQueueStore persists the secret chat qts delivery queue (device-level, dual
// memory/postgres implementations). updateNewEncryptedMessage delivers by the receiving
// device's qts sequence; qts allocation + queue write + reserved watermark advance complete in a
// single transaction (no gaps). Blind relay: bytes are stored as-is. Design: see
// docs/secret-chat-module.md §7.
type EncryptedQueueStore interface {
	// AppendEncryptedMessage allocates the next qts for the receiving device (first value 1) and
	// writes it to the queue, in the same transaction as the reserved_qts advance. Idempotent:
	// when the same (receiver_auth_key_id, chat_id, random_id) already exists it returns the
	// existing row + existing=true (no qts reallocation; a sender resending after a lost response
	// gets the same qts/date).
	AppendEncryptedMessage(ctx context.Context, msg domain.SecretChatMessage) (stored domain.SecretChatMessage, existing bool, err error)
	// ListEncryptedMessagesSince returns the receiving device's contiguous messages with qts > sinceQts (ascending qts, at most limit).
	ListEncryptedMessagesSince(ctx context.Context, receiverAuthKeyID int64, sinceQts, limit int) ([]domain.SecretChatMessage, error)
	// ReservedQts returns the receiving device's currently highest allocated qts (used by getState; returns 0 when no row).
	ReservedQts(ctx context.Context, receiverAuthKeyID int64) (int, error)
	// AckEncryptedMessages advances the device's confirmed_qts to maxQts (GREATEST idempotency,
	// regression ignored) and marks rows with qts<=maxQts as acked (receivedQueue).
	AckEncryptedMessages(ctx context.Context, receiverAuthKeyID int64, maxQts int) error

	// AppendStateEvent writes a qts-less durable secret chat state event (handshake/read, offline compensation).
	AppendStateEvent(ctx context.Context, ev domain.EncryptedStateEvent) (int64, error)
	// ListUndeliveredStateEvents returns a device's undelivered state events (account-level with
	// target_auth_key_id=0 or bound to this device), ordered by ascending id, at most limit.
	ListUndeliveredStateEvents(ctx context.Context, targetUserID, deviceAuthKeyID int64, limit int) ([]domain.EncryptedStateEvent, error)
	// MarkStateEventsDelivered records that a device has delivered these events (idempotent).
	MarkStateEventsDelivered(ctx context.Context, deviceAuthKeyID int64, eventIDs []int64) error

	// PutEncryptedFile persists a secret chat file metadata snapshot (written once after minting, idempotently overwritten).
	PutEncryptedFile(ctx context.Context, ownerUserID int64, ref domain.EncryptedFileRef) error
	// GetEncryptedFile looks up a file snapshot by id + access_hash (the inputEncryptedFile reuse path).
	GetEncryptedFile(ctx context.Context, id, accessHash int64) (domain.EncryptedFileRef, bool, error)
}
