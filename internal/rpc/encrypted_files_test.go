package rpc

import (
	"bytes"
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

// TestEncryptedFileDownloadRequiresCapability: a secret chat blob is only
// converted to the internal enc:<id> key after the id+access_hash metadata
// capability check succeeds; a wrong hash must not reach Files.GetFile.
func TestEncryptedFileDownloadRequiresCapability(t *testing.T) {
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
	ef := res.(*tg.EncryptedFile)
	files := f.router.deps.Files.(*fakeFiles)
	files.getFileFound = true
	files.getFileChunk = domain.FileChunk{MimeType: "application/octet-stream", Bytes: []byte{1, 2, 3}}

	got, err := f.router.onUploadGetFile(f.adminCtx(), &tg.UploadGetFileRequest{
		Location: &tg.InputEncryptedFileLocation{ID: ef.ID, AccessHash: ef.AccessHash},
		Offset:   0,
		Limit:    1024,
	})
	if err != nil {
		t.Fatalf("get encrypted file: %v", err)
	}
	file, ok := got.(*tg.UploadFile)
	if !ok || !bytes.Equal(file.Bytes, []byte{1, 2, 3}) {
		t.Fatalf("download = %T %+v", got, got)
	}
	if files.getFileCalls != 1 || files.getFileRequest.LocationKey != "enc:9001" {
		t.Fatalf("GetFile calls/key = %d/%q", files.getFileCalls, files.getFileRequest.LocationKey)
	}

	_, err = f.router.onUploadGetFile(f.adminCtx(), &tg.UploadGetFileRequest{
		Location: &tg.InputEncryptedFileLocation{ID: ef.ID, AccessHash: ef.AccessHash + 1},
		Offset:   0,
		Limit:    1024,
	})
	if !tgerr.Is(err, "LOCATION_INVALID") {
		t.Fatalf("wrong access hash err = %v", err)
	}
	if files.getFileCalls != 1 {
		t.Fatalf("wrong access hash reached blob store: calls=%d", files.getFileCalls)
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
