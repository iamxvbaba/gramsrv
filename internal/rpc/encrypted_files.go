package rpc

import (
	"context"

	"go.uber.org/zap"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// 私聊密聊文件收发 RPC handler（P2）。盲中继：文件内容是客户端加密的 bytes，服务端
// 组装上传分片成 blob（location_key "enc:<id>"）、铸造 EncryptedFile 快照、原样转发，
// 永不解密。下载经 upload.getFile + inputEncryptedFileLocation 复用同 blob 链路。
// 设计见 docs/secret-chat-module.md §8。

// resolveInputEncryptedFile 把 InputEncryptedFile 解析为服务端文件快照：
//   - Empty → nil（无文件）。
//   - Uploaded / BigUploaded → 组装上传分片铸造新 EncryptedFile 并持久化元数据。
//   - InputEncryptedFile{id, access_hash} → 按 id 回查既有快照（resend 复用路径）。
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

// mintEncryptedFile 组装上传分片铸造 EncryptedFile 并持久化元数据快照。
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
	if r.deps.SecretChats == nil {
		return nil, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	fileRef, err := r.resolveInputEncryptedFile(ctx, userID, req.File)
	if err != nil {
		return nil, err
	}
	_, stored, err := r.deps.SecretChats.SendEncrypted(ctx, req.Peer.ChatID, userID, req.Peer.AccessHash, domain.SecretMessageDelivery{
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
	// 校验调用方是该密聊参与者（access_hash 匹配）。
	if _, _, _, err := r.resolveSecretChatPeer(ctx, userID, req.Peer); err != nil {
		return nil, err
	}
	fileRef, err := r.resolveInputEncryptedFile(ctx, userID, req.File)
	if err != nil {
		return nil, err
	}
	return tgEncryptedFile(fileRef), nil
}
