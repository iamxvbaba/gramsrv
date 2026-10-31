package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Private secret chat read receipts and typing (P1). Both read and typing are
// updates targeted at the [peer's bound device]. P1: online push (durable offline
// compensation for read lives in the later encrypted_state_events). typing is
// transient. Design: see docs/secret-chat-module.md §8.

// resolveSecretChatPeer verifies the caller is the bound device of a normal secret
// chat and the access_hash matches, returning the secret chat, the peer user, and
// the peer's bound-device auth_key. Returns CHAT_ID_INVALID on failure.
func (r *Router) resolveSecretChatPeer(ctx context.Context, userID int64, peer tg.InputEncryptedChat) (domain.SecretChat, int64, int64, error) {
	chat, ok, err := r.deps.SecretChats.GetSecretChat(ctx, peer.ChatID)
	if err != nil {
		return domain.SecretChat{}, 0, 0, internalErr()
	}
	deviceAuthKeyID, hasDevice := businessAuthKeyIDFrom(ctx)
	if !ok || !hasDevice || chat.State != domain.SecretChatStateNormal || !chat.HasParticipant(userID) ||
		chat.AuthKeyOf(userID) != deviceAuthKeyID || chat.AccessHashFor(userID) != peer.AccessHash {
		return domain.SecretChat{}, 0, 0, chatIDInvalidErr()
	}
	return chat, chat.PeerOf(userID), chat.PeerAuthKeyOf(userID), nil
}

// pushEncryptedPeerUpdate targets the update at the peer's bound device (transient=true goes best-effort).
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
	r.log.Error("secret chat targeted session binder unavailable",
		zap.String("update", logMessage),
		zap.Int64("target_user_id", peerUserID),
		zap.Int64("target_auth_key_id", peerAuthKeyID))
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
	// durable (device-targeted) so the offline peer can recover the read receipt via getDifference.
	if err := r.deps.SecretChats.RecordReadEvent(ctx, chat.ID, peerUserID, peerAuthKeyID, req.MaxDate, now); err != nil {
		r.log.Debug("record read state event", zap.Error(err))
	}
	// Only push to the peer's bound device (the initiator relies on a local read_outbox).
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
	// typing is transient; stopping typing (Typing=false) has no corresponding update and expires automatically.
	if req.Typing {
		r.pushEncryptedPeerUpdate(ctx, peerUserID, peerAuthKeyID, &tg.UpdateEncryptedChatTyping{
			ChatID: chat.ID,
		}, true, "secret chat typing")
	}
	return true, nil
}
