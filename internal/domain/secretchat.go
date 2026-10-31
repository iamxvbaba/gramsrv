package domain

import "errors"

// SecretChatState is the server-authoritative state of the private end-to-end encrypted (Secret Chat / EncryptedChat) handshake state machine.
// The server is a blind relay: g_a/g_b/key_fingerprint/encrypted bytes are all stored opaquely
// and forwarded verbatim, never touching the DH shared secret or plaintext. Design in docs/secret-chat-module.md.
type SecretChatState string

const (
	// SecretChatStateRequested: requestEncryption has been accepted and is waiting for the recipient's acceptEncryption.
	// The same server state projects as encryptedChatWaiting for the initiator and encryptedChatRequested for the recipient.
	SecretChatStateRequested SecretChatState = "requested"
	// SecretChatStateNormal: acceptEncryption has persisted g_b/key_fingerprint and the handshake is established.
	SecretChatStateNormal SecretChatState = "normal"
	// SecretChatStateDiscarded: terminal state, triggered by either side's discardEncryption.
	SecretChatStateDiscarded SecretChatState = "discarded"
)

// Secret chat storage-layer errors (shared by the memory/postgres implementations, with the behavior contract pinned by storetest).
var (
	// ErrSecretChatNotFound: chat_id does not exist → CHAT_ID_INVALID / ENCRYPTION_ID_INVALID.
	ErrSecretChatNotFound = errors.New("secretchat: not found")
	// ErrSecretChatAlreadyAccepted: accepting an already-established secret chat → ENCRYPTION_ALREADY_ACCEPTED.
	ErrSecretChatAlreadyAccepted = errors.New("secretchat: already accepted")
	// ErrSecretChatAlreadyDeclined: accepting a discarded secret chat → ENCRYPTION_ALREADY_DECLINED.
	ErrSecretChatAlreadyDeclined = errors.New("secretchat: already declined")
	// ErrSecretChatRandomIDDuplicate: requestEncryption.random_id is already taken by a different intent or another
	// auth key. random_id is itself the chat_id, so allocating another ID to dodge a collision is forbidden.
	ErrSecretChatRandomIDDuplicate = errors.New("secretchat: random id duplicate")
)

// SecretChat is the server-authoritative state of one private secret chat (durable, surviving restarts).
// Field names align with TL encryptedChat*; ID is int32-scale and access_hash/admin_id/
// participant_id/key_fingerprint are int64. The binding dimension is device-level (the perm auth_key's int64 value).
type SecretChat struct {
	// ID is the chat_id and must equal requestEncryption.random_id bit for bit (non-zero int32); both sides share the same id.
	ID int
	// AdminAccessHash / ParticipantAccessHash differ between the two perspectives (TL "check sum depending on user ID").
	AdminAccessHash       int64
	ParticipantAccessHash int64
	// AdminUserID is the initiator and ParticipantUserID the recipient; roles are determined from these.
	AdminUserID       int64
	ParticipantUserID int64
	// AdminAuthKeyID / ParticipantAuthKeyID are the bound device's perm auth_key (the little-endian authKeyIDToInt64 value).
	// ParticipantAuthKeyID is 0 before accept (before the link is established the invite is visible to the participant at account level).
	AdminAuthKeyID       int64
	ParticipantAuthKeyID int64

	State SecretChatState

	// GA is the initiator's public key carried by requestEncryption (blindly stored, left-padded with zeros to 256B).
	// GB is the recipient's public key carried by acceptEncryption (persisted after accept).
	// Projection GAOrB: it is GB for the admin's perspective and GA for the participant's (pinned by the TL comments).
	GA []byte
	GB []byte
	// KeyFingerprint is the shared-key fingerprint computed by the recipient; the server relays the int64 verbatim and never recomputes it.
	KeyFingerprint int64

	// Layer is the secret chat's inner layer snapshot (not parsed); FolderID passes through the requested archive intent.
	Layer    int
	FolderID int
	// HistoryDeleted is whether discard asked the peer to delete the entire chat history.
	HistoryDeleted bool

	// RandomID is requestEncryption's int32 idempotency key (the same initiating device with the same random_id returns the same chat).
	RandomID int32
	// Date is the acceptance time (unix seconds).
	Date int
}

// Terminal reports whether the secret chat has been discarded.
func (c SecretChat) Terminal() bool {
	return c.State == SecretChatStateDiscarded
}

// HasParticipant reports whether userID is the initiator or recipient of this secret chat.
func (c SecretChat) HasParticipant(userID int64) bool {
	return userID != 0 && (userID == c.AdminUserID || userID == c.ParticipantUserID)
}

// IsAdmin reports whether userID is the initiator.
func (c SecretChat) IsAdmin(userID int64) bool {
	return userID != 0 && userID == c.AdminUserID
}

// PeerOf returns userID's peer in this secret chat; returns 0 when userID is not a participant.
func (c SecretChat) PeerOf(userID int64) int64 {
	switch userID {
	case c.AdminUserID:
		return c.ParticipantUserID
	case c.ParticipantUserID:
		return c.AdminUserID
	default:
		return 0
	}
}

// PeerAuthKeyOf returns the peer's bound device perm auth_key (int64) for userID; returns 0 when userID is not a participant
// or the peer is unbound. The admin's peer is the participant device and vice versa.
func (c SecretChat) PeerAuthKeyOf(userID int64) int64 {
	switch userID {
	case c.AdminUserID:
		return c.ParticipantAuthKeyID
	case c.ParticipantUserID:
		return c.AdminAuthKeyID
	default:
		return 0
	}
}

// AuthKeyOf returns the permanent auth key bound to userID itself; returns 0 when userID is not a participant or not yet bound.
// Every read/write authorization for an established secret chat must match both the user and this auth key, never relying on account identity alone.
func (c SecretChat) AuthKeyOf(userID int64) int64 {
	switch userID {
	case c.AdminUserID:
		return c.AdminAuthKeyID
	case c.ParticipantUserID:
		return c.ParticipantAuthKeyID
	default:
		return 0
	}
}

// AccessHashFor returns the access_hash from userID's perspective (different for the two sides); returns 0 for a non-participant.
func (c SecretChat) AccessHashFor(userID int64) int64 {
	switch userID {
	case c.AdminUserID:
		return c.AdminAccessHash
	case c.ParticipantUserID:
		return c.ParticipantAccessHash
	default:
		return 0
	}
}

// EncryptedFileRef is the server snapshot of a secret chat file (P2, blind relay: the content is encrypted bytes and is not decrypted).
type EncryptedFileRef struct {
	ID             int64
	AccessHash     int64
	Size           int64
	DCID           int
	KeyFingerprint int // int32 scale
}

// SecretChatMessage is one opaque encrypted message in the secret chat qts delivery queue (the payload of
// updateNewEncryptedMessage). Bytes is the client-encrypted DecryptedMessage, blindly stored and relayed by the server and never decrypted.
// Delivered on the qts sequence of the receiving device (ReceiverAuthKeyID).
type SecretChatMessage struct {
	ReceiverAuthKeyID int64
	Qts               int
	ReceiverUserID    int64
	ChatID            int
	RandomID          int64
	Date              int
	IsService         bool
	Bytes             []byte
	File              *EncryptedFileRef
}

// EncryptedStateEventType distinguishes the kinds of qts-less secret chat state events.
type EncryptedStateEventType int

const (
	// EncryptedStateEventEncryption: updateEncryption (handshake state change); rebuilt from the secret_chats
	// authoritative state when delivered (no snapshot is frozen).
	EncryptedStateEventEncryption EncryptedStateEventType = 1
	// EncryptedStateEventRead: updateEncryptedMessagesRead (a read receipt), carrying MaxDate.
	EncryptedStateEventRead EncryptedStateEventType = 2
)

// EncryptedStateEvent is a qts-less durable secret chat state event (for offline getDifference catch-up).
// TargetAuthKeyID=0 means account-level (visible to all devices); non-zero means targeted at a bound device.
type EncryptedStateEvent struct {
	ID              int64
	TargetUserID    int64
	TargetAuthKeyID int64
	ChatID          int
	Type            EncryptedStateEventType
	MaxDate         int
	Date            int
}

// SecretMessageDelivery is one encrypted message sendEncrypted* is to deliver to the peer device (assembled by the rpc layer,
// with the service assigning the receiving device's qts and persisting it).
type SecretMessageDelivery struct {
	RandomID  int64
	Bytes     []byte
	IsService bool
	File      *EncryptedFileRef
	Date      int
}

// MaxSecretMessageDataBytes bounds the opaque encrypted DecryptedMessage payload persisted in
// the device queue. Secret-chat media bytes travel through the file service, so a 1 MiB metadata
// envelope is already well above the payload emitted by the supported clients while keeping one
// request independent from the process-wide MTProto admission budget.
const MaxSecretMessageDataBytes = 1 << 20

// SecretChatRequest is requestEncryption's acceptance input (privacy/block/self/bot checks run first in the rpc layer).
type SecretChatRequest struct {
	AdminUserID       int64
	AdminAuthKeyID    int64
	ParticipantUserID int64
	RandomID          int32
	GA                []byte
	Date              int
}
