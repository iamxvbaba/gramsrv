package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// TestSendEncryptedFileFlow: sendEncryptedFile mints an EncryptedFile, delivers it
// with the message, returns SentEncryptedFile{date,file}, and the
// InputEncryptedFile reuse path can look up the same file.
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

	// The updateNewEncryptedMessage pushed to the participant carries the file.
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

	// InputEncryptedFile reuse: look up the same file by id+access_hash.
	ref, found, err := f.router.deps.SecretChats.GetEncryptedFile(f.ctx, ef.ID, ef.AccessHash)
	if err != nil || !found || ref.ID != ef.ID {
		t.Fatalf("GetEncryptedFile reuse = found %v id %d err %v", found, ref.ID, err)
	}
}

// TestUploadEncryptedFile: uploadEncryptedFile mints and returns an EncryptedFile.
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

func TestEncryptedDataLimit(t *testing.T) {
	f := newEncryptedFixture(t)
	chatID, _ := f.acceptChat(t)
	chat, _, _ := f.store.GetSecretChat(f.ctx, chatID)
	tooLong := make([]byte, domain.MaxSecretMessageDataBytes+1)

	_, err := f.router.onMessagesSendEncrypted(f.adminCtx(), &tg.MessagesSendEncryptedRequest{
		Peer: tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash}, RandomID: 1, Data: tooLong,
	})
	if !tgerr.Is(err, "DATA_TOO_LONG") {
		t.Fatalf("sendEncrypted oversized err = %v", err)
	}
	_, err = f.router.onMessagesSendEncryptedFile(f.adminCtx(), &tg.MessagesSendEncryptedFileRequest{
		Peer: tg.InputEncryptedChat{ChatID: chatID, AccessHash: chat.AdminAccessHash}, RandomID: 2, Data: tooLong,
		File: &tg.InputEncryptedFileUploaded{ID: 888, Parts: 1, KeyFingerprint: 9},
	})
	if !tgerr.Is(err, "DATA_TOO_LONG") {
		t.Fatalf("sendEncryptedFile oversized err = %v", err)
	}
}
