package rpc

import (
	"encoding/binary"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 密聊（Secret Chat）domain ↔ tg 投影。服务端是盲中继：g_a/g_b/key_fingerprint
// 原样回放。chat 双视角不对称（见 docs/secret-chat-module.md §3.6）：
//   - access_hash 双方不同（按 viewer 取 admin/participant 列）。
//   - GAOrB 对 admin 视角是 g_b、对 participant 视角是 g_a（TL 注释钉死）。
//   - pending 态：admin 看 encryptedChatWaiting（无 g_a），participant 看
//     encryptedChatRequested（携 g_a）。

// deviceAuthKeyBytes 把绑定维度的 int64 auth_key_id 转回 [8]byte（用于 SessionManager
// 按业务 auth_key 定向投递）。与 businessAuthKeyInt64 互逆（小端）。
func deviceAuthKeyBytes(id int64) [8]byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], uint64(id))
	return b
}

// tgEncryptedMessage 把 qts 队列里的不透明加密消息投影为 TL EncryptedMessage(Service)。
// 盲中继：Bytes 原样回放，不解密。
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

// tgEncryptedFile 把密聊文件快照投影为 TL EncryptedFile(Empty)。
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

// businessAuthKeyInt64 把 ctx 业务视角 auth_key_id（[8]byte）转成 DB/绑定维度的
// int64。必须与 store/postgres authKeyIDToInt64 同源（小端，MTProto 即 SHA1 低 64 位），
// 否则密聊绑定键与 update_states/authorizations 对不上。一致性见
// convert_encrypted_test.go。
func businessAuthKeyInt64(id [8]byte) int64 {
	return int64(binary.LittleEndian.Uint64(id[:]))
}

// tgEncryptedChatForViewer 把密聊投影为 viewerID 视角的 EncryptedChatClass。
func tgEncryptedChatForViewer(chat domain.SecretChat, viewerID int64) tg.EncryptedChatClass {
	if chat.Terminal() {
		return &tg.EncryptedChatDiscarded{
			HistoryDeleted: chat.HistoryDeleted,
			ID:             chat.ID,
		}
	}
	accessHash := chat.AccessHashFor(viewerID)
	if chat.State == domain.SecretChatStateNormal {
		// GAOrB：admin 看 g_b、participant 看 g_a。
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
	// requested 态：admin 视角 waiting（无 g_a），participant 视角 requested（携 g_a）。
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
