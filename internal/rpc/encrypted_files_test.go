package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// TestSendEncryptedFileFlow：sendEncryptedFile 铸造 EncryptedFile、随消息投递、返回
// SentEncryptedFile{date,file}，且 InputEncryptedFile 复用路径能回查同一文件。
func TestSendEncryptedFileFlow(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	f.sessions.reset()

	sent, err := f.router.onMessagesSendEncryptedFile(f.adminCtx(), &tg.MessagesSendEncryptedFileRequest{
		Peer:     tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		RandomID: 71717,
		Data:     []byte{0x01, 0x02},
		File:     &tg.InputEncryptedFileUploaded{ID: 555, Parts: 1, KeyFingerprint: 7},
	})
	if err != nil {
		t.Fatalf("sendEncryptedFile: %v", err)
	}
	sf, ok := sent.(*tg.MessagesSentEncryptedFile)
	if !ok || sf.Date == 0 {
		t.Fatalf("response = %T %+v, want SentEncryptedFile{date,file}", sent, sent)
	}
	ef, ok := sf.File.(*tg.EncryptedFile)
	if !ok || ef.ID == 0 {
		t.Fatalf("response file = %T, want non-empty EncryptedFile", sf.File)
	}

	// 推送给 participant 的 updateNewEncryptedMessage 携带 file。
	recs := f.sessions.records()
	if len(recs) != 1 {
		t.Fatalf("send push = %d, want 1", len(recs))
	}
	em, ok := encNewMessagePayload(t, recs[0]).Message.(*tg.EncryptedMessage)
	if !ok {
		t.Fatalf("pushed message = %T, want EncryptedMessage", encNewMessagePayload(t, recs[0]).Message)
	}
	if _, ok := em.File.(*tg.EncryptedFile); !ok {
		t.Fatalf("pushed message file = %T, want EncryptedFile", em.File)
	}

	// InputEncryptedFile 复用：按 id+access_hash 回查到同一文件。
	ref, found, err := f.router.deps.SecretChats.GetEncryptedFile(f.ctx, ef.ID, ef.AccessHash)
	if err != nil || !found || ref.ID != ef.ID {
		t.Fatalf("GetEncryptedFile reuse = found %v id %d err %v", found, ref.ID, err)
	}
}

// TestUploadEncryptedFile：uploadEncryptedFile 铸造并返回 EncryptedFile。
func TestUploadEncryptedFile(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)

	res, err := f.router.onMessagesUploadEncryptedFile(f.adminCtx(), &tg.MessagesUploadEncryptedFileRequest{
		Peer: tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash},
		File: &tg.InputEncryptedFileUploaded{ID: 888, Parts: 1, KeyFingerprint: 9},
	})
	if err != nil {
		t.Fatalf("uploadEncryptedFile: %v", err)
	}
	ef, ok := res.(*tg.EncryptedFile)
	if !ok || ef.ID == 0 {
		t.Fatalf("upload response = %T, want non-empty EncryptedFile", res)
	}
}

// TestEncryptedFileLocationKey：inputEncryptedFileLocation → "enc:<id>" 下载 key。
func TestEncryptedFileLocationKey(t *testing.T) {
	key, ok := fileLocationKey(&tg.InputEncryptedFileLocation{ID: 123, AccessHash: 456})
	if !ok || key != "enc:123" {
		t.Fatalf("location key = %q ok %v, want enc:123", key, ok)
	}
	if _, ok := fileLocationKey(&tg.InputEncryptedFileLocation{ID: 0}); ok {
		t.Fatal("id=0 must be rejected")
	}
}
