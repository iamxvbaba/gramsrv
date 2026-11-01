package rpc

import (
	"context"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// Private secret chat file send/receive RPC handler (P2). Blind relay: the file
// content is client-encrypted bytes; the server assembles upload parts into a
// blob (location_key "enc:<id>"), mints an EncryptedFile snapshot, forwards it
// verbatim, and never decrypts. Downloads go through upload.getFile +
// inputEncryptedFileLocation reusing the same blob chain. Design: see
// docs/secret-chat-module.md §8.

// resolveInputEncryptedFile resolves InputEncryptedFile into a server file snapshot:
//   - Empty → nil (no file).
//   - Uploaded / BigUploaded → assemble upload parts, mint a new EncryptedFile and
//     persist the metadata.
//   - InputEncryptedFile{id, access_hash} → look up the existing snapshot by id
//     (resend reuse path).
func (r *Router) resolveInputEncryptedFile(ctx context.Context, ownerUserID int64, input tg.InputEncryptedFileClass) (*domain.EncryptedFileRef, error) {
	switch f := input.(type) {
	case *tg.InputEncryptedFileEmpty, nil:
		return nil, nil
	case *tg.InputEncryptedFileUploaded:
		return r.mintEncryptedFile(ctx, ownerUserID, domain.UploadedFileRef{
			OwnerUserID: ownerUserID, FileID: f.ID, Parts: f.Parts, Big: false,
		}, f.KeyFingerprint)
	case *tg.InputEncryptedFileBigUploaded:
		return r.mintEncryptedFile(ctx, ownerUserID, domain.UploadedFileRef{
			OwnerUserID: ownerUserID, FileID: f.ID, Parts: f.Parts, Big: true,
		}, f.KeyFingerprint)
	case *tg.InputEncryptedFile:
		ref, found, err := r.deps.SecretChats.GetEncryptedFile(ctx, f.ID, f.AccessHash)
		if err != nil {
			return nil, internalErr()
		}
		if !found {
			return nil, fileEmptyErr()
		}
		return &ref, nil
	default:
		return nil, fileEmptyErr()
	}
}

// mintEncryptedFile assembles upload parts to mint an EncryptedFile and persists a metadata snapshot.
func (r *Router) mintEncryptedFile(ctx context.Context, ownerUserID int64, upload domain.UploadedFileRef, keyFingerprint int) (*domain.EncryptedFileRef, error) {
	if r.deps.Files == nil {
		return nil, notImplementedErr()
	}
	ref, err := r.deps.Files.CreateEncryptedFileFromUpload(ctx, upload, keyFingerprint)
	if err != nil {
		return nil, fileEmptyErr()
	}
	if err := r.deps.SecretChats.PutEncryptedFile(ctx, ownerUserID, ref); err != nil {
		r.log.Debug("put encrypted file metadata", zap.Error(err))
	}
	return &ref, nil
}

func (r *Router) onMessagesSendEncryptedFile(ctx context.Context, req *tg.MessagesSendEncryptedFileRequest) (tg.MessagesSentEncryptedMessageClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if len(req.Data) > domain.MaxSecretMessageDataBytes {
		return nil, dataTooLongErr()
	}
	if r.deps.SecretChats == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	deviceAuthKeyID, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, internalErr()
	}
	// Complete the chat/device authorization first, so an unauthorized or terminal-state request does not assemble the blob and write metadata before failing.
	if _, _, _, err := r.resolveSecretChatPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	fileRef, err := r.resolveInputEncryptedFile(ctx, userID, req.File)
	if err != nil {
		return nil, err
	}
	_, stored, err := r.deps.SecretChats.SendEncrypted(ctx, req.Peer.ChatID, userID, deviceAuthKeyID, req.Peer.AccessHash, domain.SecretMessageDelivery{
		RandomID:  req.RandomID,
		Bytes:     req.Data,
		IsService: false,
		File:      fileRef,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, secretChatErr(err)
	}
	r.pushEncryptedNewMessage(ctx, stored)
	return &tg.MessagesSentEncryptedFile{Date: stored.Date, File: tgEncryptedFile(stored.File)}, nil
}

func (r *Router) onMessagesUploadEncryptedFile(ctx context.Context, req *tg.MessagesUploadEncryptedFileRequest) (tg.EncryptedFileClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if r.deps.SecretChats == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	// Verify the caller is a participant of this secret chat (access_hash matches).
	if _, _, _, err := r.resolveSecretChatPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	fileRef, err := r.resolveInputEncryptedFile(ctx, userID, req.File)
	if err != nil {
		return nil, err
	}
	return tgEncryptedFile(fileRef), nil
}
