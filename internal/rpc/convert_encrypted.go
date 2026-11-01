package rpc

import (
	"encoding/binary"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Secret Chat domain <-> tg projection. The server is a blind relay: g_a/g_b/
// key_fingerprint are replayed verbatim. The chat is asymmetric between the two
// viewpoints (see docs/secret-chat-module.md §3.6):
//   - access_hash differs per side (taken from the admin/participant column by viewer).
//   - GAOrB is g_b from the admin viewpoint and g_a from the participant viewpoint
//     (pinned by the TL comments).
//   - pending state: the admin sees encryptedChatWaiting (no g_a), the participant
//     sees encryptedChatRequested (carries g_a).

// deviceAuthKeyBytes converts the binding-dimension int64 auth_key_id back to
// [8]byte (used by SessionManager to deliver to a specific business auth_key).
// It is the inverse of businessAuthKeyInt64 (little-endian).
func deviceAuthKeyBytes(id int64) [8]byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(id))
	return b
}

// tgEncryptedMessage projects the opaque encrypted message from the qts queue
// into the TL EncryptedMessage(Service). Blind relay: Bytes is replayed verbatim,
// not decrypted.
func tgEncryptedMessage(m domain.SecretChatMessage) tg.EncryptedMessageClass {
	if m.IsService {
		return &tg.EncryptedMessageService{
			RandomID: m.RandomID,
			ChatID:   m.ChatID,
			Date:     m.Date,
			Bytes:    append([]byte(nil), m.Bytes...),
		}
	}
	return &tg.EncryptedMessage{
		RandomID: m.RandomID,
		ChatID:   m.ChatID,
		Date:     m.Date,
		Bytes:    append([]byte(nil), m.Bytes...),
		File:     tgEncryptedFile(m.File),
	}
}

// tgEncryptedFile projects a secret chat file snapshot into TL EncryptedFile(Empty).
func tgEncryptedFile(ref *domain.EncryptedFileRef) tg.EncryptedFileClass {
	if ref == nil || ref.ID == 0 {
		return &tg.EncryptedFileEmpty{}
	}
	return &tg.EncryptedFile{
		ID:             ref.ID,
		AccessHash:     ref.AccessHash,
		Size:           ref.Size,
		DCID:           ref.DCID,
		KeyFingerprint: ref.KeyFingerprint,
	}
}

// businessAuthKeyInt64 converts the ctx business-view auth_key_id ([8]byte) into
// the DB/binding-dimension int64. It must share the same source as store/postgres
// authKeyIDToInt64 (little-endian, MTProto is the low 64 bits of SHA1); otherwise
// the secret chat binding key and update_states/authorizations will not line up.
// Consistency is covered in convert_encrypted_test.go.
func businessAuthKeyInt64(id [8]byte) int64 {
	return int64(binary.LittleEndian.Uint64(id[:]))
}

// tgEncryptedChatForViewer projects a secret chat into EncryptedChatClass from
// the viewerID viewpoint.
func tgEncryptedChatForViewer(chat domain.SecretChat, viewerID int64) tg.EncryptedChatClass {
	if chat.Terminal() {
		return &tg.EncryptedChatDiscarded{
			HistoryDeleted: chat.HistoryDeleted,
			ID:             chat.ID,
		}
	}
	accessHash := chat.AccessHashFor(viewerID)
	if chat.State == domain.SecretChatStateNormal {
		// GAOrB: the admin sees g_b, the participant sees g_a.
		gaOrB := chat.GA
		if chat.IsAdmin(viewerID) {
			gaOrB = chat.GB
		}
		return &tg.EncryptedChat{
			ID:             chat.ID,
			AccessHash:     accessHash,
			Date:           chat.Date,
			AdminID:        chat.AdminUserID,
			ParticipantID:  chat.ParticipantUserID,
			GAOrB:          append([]byte(nil), gaOrB...),
			KeyFingerprint: chat.KeyFingerprint,
		}
	}
	// requested state: the admin viewpoint is waiting (no g_a), the participant
	// viewpoint is requested (carries g_a).
	if chat.IsAdmin(viewerID) {
		return &tg.EncryptedChatWaiting{
			ID:            chat.ID,
			AccessHash:    accessHash,
			Date:          chat.Date,
			AdminID:       chat.AdminUserID,
			ParticipantID: chat.ParticipantUserID,
		}
	}
	requested := &tg.EncryptedChatRequested{
		ID:            chat.ID,
		AccessHash:    accessHash,
		Date:          chat.Date,
		AdminID:       chat.AdminUserID,
		ParticipantID: chat.ParticipantUserID,
		GA:            append([]byte(nil), chat.GA...),
	}
	if chat.FolderID != 0 {
		requested.FolderID = chat.FolderID
		requested.Flags.Set(0)
	}
	return requested
}
