package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 私聊密聊已读回执与 typing（P1）。已读/ typing 都是定向投给【对端绑定设备】的 update。
// P1：在线推送（已读 durable 离线补偿见后续 encrypted_state_events）。typing 是 transient。
// 设计见 docs/secret-chat-module.md §8。

// resolveSecretChatPeer 校验调用方是密聊参与者且 access_hash 匹配，返回密聊、对端 user、
// 对端绑定设备 auth_key。失败返回 CHAT_ID_INVALID。
func (r *Router) resolveSecretChatPeer(ctx context.Context, userID int64, peer tg.InputEncryptedChat) (domain.SecretChat, int64, int64, error) {
	chat, ok, err := r.deps.SecretChats.GetSecretChat(ctx, peer.ChatID)
	if err != nil {
		return domain.SecretChat{}, 0, 0, internalErr()
	}
	if !ok || !chat.HasParticipant(userID) || chat.AccessHashFor(userID) != peer.AccessHash {
		return domain.SecretChat{}, 0, 0, chatIDInvalidErr()
	}
	return chat, chat.PeerOf(userID), chat.PeerAuthKeyOf(userID), nil
}

// pushEncryptedPeerUpdate 把 update 定向投给对端绑定设备（transient=true 走 best-effort）。
func (r *Router) pushEncryptedPeerUpdate(ctx context.Context, peerUserID, peerAuthKeyID int64, update tg.UpdateClass, transient bool, logMessage string) {
	if peerUserID == 0 || peerAuthKeyID == 0 {
		return
	}
	now := int(r.clock.Now().Unix())
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    now,
		Seq:     0,
	}
	if targeted, ok := r.deps.Sessions.(AuthKeyTargetedSessionBinder); ok {
		devKey := deviceAuthKeyBytes(peerAuthKeyID)
		if transient {
			_, _ = targeted.PushToUserAuthKeyTransient(ctx, peerUserID, devKey, proto.MessageFromServer, upd, r.cfg.OutboundPushTimeout)
		} else {
			_, _ = targeted.PushToUserAuthKey(ctx, peerUserID, devKey, proto.MessageFromServer, upd)
		}
		return
	}
	if transient {
		r.pushUserMessageTransient(ctx, peerUserID, logMessage, upd)
	} else {
		r.pushUserMessage(ctx, peerUserID, logMessage, upd)
	}
}

func (r *Router) onMessagesReadEncryptedHistory(ctx context.Context, req *tg.MessagesReadEncryptedHistoryRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if r.deps.SecretChats == nil {
		return false, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return false, err
	}
	if req.MaxDate <= 0 || req.MaxDate > int(r.clock.Now().Unix())+1 {
		return false, maxDateInvalidErr()
	}
	chat, peerUserID, peerAuthKeyID, err := r.resolveSecretChatPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	now := int(r.clock.Now().Unix())
	// durable（设备定向）供离线对端 getDifference 补回已读回执。
	if err := r.deps.SecretChats.RecordReadEvent(ctx, chat.ID, peerUserID, peerAuthKeyID, req.MaxDate, now); err != nil {
		r.log.Debug("record read state event", zap.Error(err))
	}
	// 仅投对端绑定设备（发起方靠本地置 read_outbox）。
	r.pushEncryptedPeerUpdate(ctx, peerUserID, peerAuthKeyID, &tg.UpdateEncryptedMessagesRead{
		ChatID:  chat.ID,
		MaxDate: req.MaxDate,
		Date:    now,
	}, false, "secret chat read")
	return true, nil
}

func (r *Router) onMessagesSetEncryptedTyping(ctx context.Context, req *tg.MessagesSetEncryptedTypingRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	if r.deps.SecretChats == nil {
		return false, notImplementedErr()
	}
	userID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return false, err
	}
	chat, peerUserID, peerAuthKeyID, err := r.resolveSecretChatPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	// typing 是 transient；停止 typing（Typing=false）无对应 update，自动过期。
	if req.Typing {
		r.pushEncryptedPeerUpdate(ctx, peerUserID, peerAuthKeyID, &tg.UpdateEncryptedChatTyping{
			ChatID: chat.ID,
		}, true, "secret chat typing")
	}
	return true, nil
}
