package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	appsecret "telesrv/internal/app/secretchat"
	"telesrv/internal/domain"
)

// Private end-to-end encryption (Secret Chat / encrypted chat) handshake RPC
// handler. The state machine and DH validation belong to app/secretchat; this file
// only does authentication, input validation, TL conversion and online push
// orchestration.
//
// The handshake loop of requestEncryption / acceptEncryption / discardEncryption +
// updateEncryption online device targeting, durable offline getDifference
// compensation and loser-device convergence; qts message delivery (sendEncrypted
// etc.) is in encrypted_messages.go, design docs/secret-chat-module.md. The server
// is a blind relay and never touches the shared key or plaintext.

// secretChatErr maps app/secretchat + domain business errors to RPC_ERROR.
func secretChatErr(err error) error {
	switch {
	case errors.Is(err, appsecret.ErrGAInvalid):
		return dhGAInvalidErr()
	case errors.Is(err, domain.ErrSecretChatAlreadyAccepted):
		return encryptionAlreadyAcceptedErr()
	case errors.Is(err, domain.ErrSecretChatAlreadyDeclined):
		return encryptionAlreadyDeclinedErr()
	case errors.Is(err, domain.ErrSecretChatRandomIDDuplicate):
		return secretChatRandomIDDuplicateErr()
	case errors.Is(err, domain.ErrSecretChatNotFound):
		return chatIDInvalidErr()
	default:
		return internalErr()
	}
}

// secretChatRequireUser is the unified login gate for secret chat RPCs.
func (r *Router) secretChatRequireUser(ctx context.Context) (int64, error) {
	userID, ok, err := r.currentUserID(ctx)
	if err != nil {
		return 0, internalErr()
	}
	if !ok {
		return 0, authKeyUnregisteredErr()
	}
	return userID, nil
}

// businessAuthKeyIDFrom returns the int64 binding value of the current connection's business-view auth_key_id.
func businessAuthKeyIDFrom(ctx context.Context) (int64, bool) {
	id, ok := AuthKeyIDFrom(ctx)
	if !ok {
		return 0, false
	}
	return businessAuthKeyInt64(id), true
}

// pushUpdateEncryption is only the online accelerator for the authoritative
// encrypted_state_events fact committed with the secret-chat mutation. It
// never enters an account PTS/non-PTS lane. targetAuthKeyID=0 is account-wide;
// an accepted chat must use its exact bound device.
func (r *Router) pushUpdateEncryption(ctx context.Context, targetUserID, targetAuthKeyID int64, chat domain.SecretChat, logMessage string) {
	now := int(r.clock.Now().Unix())
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateEncryption{
			Chat: tgEncryptedChatForViewer(chat, targetUserID),
			Date: now,
		}},
		Users: r.tgUsersForIDs(ctx, targetUserID, []int64{chat.AdminUserID, chat.ParticipantUserID}),
		Chats: []tg.ChatClass{},
		Date:  now,
		Seq:   0,
	}
	if targetAuthKeyID == 0 {
		r.pushUserMessageTransient(ctx, targetUserID, logMessage, upd)
		return
	}
	if targeted, ok := r.deps.Sessions.(AuthKeyTargetedSessionPusher); ok {
		_, _ = targeted.PushToUserAuthKeyTransient(ctx, targetUserID, deviceAuthKeyBytes(targetAuthKeyID), proto.MessageFromServer, upd, r.cfg.OutboundPushTimeout)
		return
	}
	r.log.Error("secret chat targeted session binder unavailable",
		zap.String("update", logMessage),
		zap.Int64("target_user_id", targetUserID),
		zap.Int64("target_auth_key_id", targetAuthKeyID))
}

// pushAcceptedLoserDiscarded makes the participant account's online devices, other
// than the winning business auth key, delete the requested ghost. Offline/not-ready
// devices converge via the account-level state event added after accept.
func (r *Router) pushAcceptedLoserDiscarded(ctx context.Context, chat domain.SecretChat, date int) {
	if chat.ParticipantUserID == 0 || chat.ParticipantAuthKeyID == 0 {
		return
	}
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateEncryption{
			Chat: &tg.EncryptedChatDiscarded{ID: chat.ID, HistoryDeleted: true},
			Date: date,
		}},
		Users: r.tgUsersForIDs(ctx, chat.ParticipantUserID, []int64{chat.AdminUserID, chat.ParticipantUserID}),
		Chats: []tg.ChatClass{},
		Date:  date,
		Seq:   0,
	}
	if targeted, ok := r.deps.Sessions.(AuthKeyTargetedSessionPusher); ok {
		_, _ = targeted.PushToUserExceptBusinessAuthKey(ctx, chat.ParticipantUserID,
			deviceAuthKeyBytes(chat.ParticipantAuthKeyID), proto.MessageFromServer, upd, r.cfg.OutboundPushTimeout)
		return
	}
	r.log.Error("secret chat targeted session binder unavailable",
		zap.String("update", "secret chat accept loser discarded"),
		zap.Int64("target_user_id", chat.ParticipantUserID),
		zap.Int64("exclude_auth_key_id", chat.ParticipantAuthKeyID))
}

// discardSecretChatsForAuthKey cascades a discard of every active secret chat bound to the perm
// auth_key when a device logs out / an authorization is revoked, and pushes
// encryptedChatDiscarded to the peer (online fast path). The durable state event is already
// written by SecretChatService within the same boundary as the state mutation; ownerUserID is
// the owner of the destroyed device and is used to locate the peer.
// Fixes P1: onAuthLogOut and friends used not to cascade the discard, so the peer kept
// delivering to a dead auth_key, a silent dead link (messages acked=f / qts backlog forever, and
// the peer never saw discarded).
func (r *Router) discardSecretChatsForAuthKey(ctx context.Context, businessAuthKeyID, ownerUserID int64) {
	if r.deps.SecretChats == nil || businessAuthKeyID == 0 || ownerUserID == 0 {
		return
	}
	now := int(r.clock.Now().Unix())
	discarded, err := r.deps.SecretChats.DiscardForAuthKey(ctx, businessAuthKeyID, ownerUserID, now)
	if err != nil {
		// DiscardForAuthKey also returns the portion successfully discarded on error; keep notifying those peers.
		r.log.Debug("cascade discard secret chats for auth key", zap.Error(err))
	}
	for _, chat := range discarded {
		peer := chat.PeerOf(ownerUserID)
		if peer == 0 {
			continue
		}
		// If the peer's bound device is known, target device-level; before the link is established (unbound, 0), account-level.
		r.pushUpdateEncryption(ctx, peer, chat.PeerAuthKeyOf(ownerUserID), chat, "secret chat discarded on peer logout/revoke")
	}
}

func (r *Router) onMessagesRequestEncryption(ctx context.Context, req *tg.MessagesRequestEncryptionRequest) (tg.EncryptedChatClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	if req.RandomID == 0 || int(int32(req.RandomID)) != req.RandomID {
		return nil, secretChatRandomIDDuplicateErr()
	}
	if r.deps.SecretChats == nil || r.deps.Users == nil {
		return nil, notImplementedErr()
	}
	adminID, err := r.secretChatRequireUser(ctx)
	if err != nil {
		return nil, err
	}
	adminAuthKeyID, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, internalErr()
	}
	participant, found, err := r.userFromInput(ctx, adminID, req.UserID)
	if err != nil {
		return nil, internalErr()
	}
	// Self-chat / bot / nonexistent all map to USER_ID_INVALID (bots do not support secret chats).
	if !found || participant.ID == 0 || participant.ID == adminID || participant.Bot {
		return nil, userIDInvalidErr()
	}
	if blocked, err := r.peerBlocksUser(ctx, adminID, participant.ID); err != nil {
		return nil, err
	} else if blocked {
		return nil, userIsBlockedErr()
	}
	chat, err := r.deps.SecretChats.RequestEncryption(ctx, domain.SecretChatRequest{
		AdminUserID:       adminID,
		AdminAuthKeyID:    adminAuthKeyID,
		ParticipantUserID: participant.ID,
		RandomID:          int32(req.RandomID),
		GA:                req.GA,
		Date:              int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, secretChatErr(err)
	}
	// The account-level state event for the pre-handshake invitation is already committed within the
	// same boundary as the secret_chats write. Push encryptedChatRequested (carrying g_a) to every online
	// device of the acceptor; offline devices catch up through getDifference.
	r.pushUpdateEncryption(ctx, chat.ParticipantUserID, 0, chat, "secret chat requested")
	// The initiator synchronously receives encryptedChatWaiting (no g_a).
	return tgEncryptedChatForViewer(chat, adminID), nil
}

func (r *Router) onMessagesAcceptEncryption(ctx context.Context, req *tg.MessagesAcceptEncryptionRequest) (tg.EncryptedChatClass, error) {
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
	participantAuthKeyID, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return nil, internalErr()
	}
	now := int(r.clock.Now().Unix())
	chat, err := r.deps.SecretChats.AcceptEncryption(ctx, req.Peer.ChatID, userID,
		participantAuthKeyID, req.Peer.AccessHash, req.GB, req.KeyFingerprint, now)
	if err != nil {
		return nil, secretChatErr(err)
	}
	// The handshake-complete event and the participant loser convergence event are already committed
	// within the same boundary as the accept CAS. Push encryptedChat (GAOrB=g_b, key_fingerprint)
	// only to the initiator's bound device.
	r.pushUpdateEncryption(ctx, chat.AdminUserID, chat.AdminAuthKeyID, chat, "secret chat accepted")
	r.pushAcceptedLoserDiscarded(ctx, chat, now)
	// The acceptor synchronously receives encryptedChat (GAOrB=g_a).
	return tgEncryptedChatForViewer(chat, userID), nil
}

func (r *Router) onMessagesDiscardEncryption(ctx context.Context, req *tg.MessagesDiscardEncryptionRequest) (bool, error) {
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
	deviceAuthKeyID, ok := businessAuthKeyIDFrom(ctx)
	if !ok {
		return false, internalErr()
	}
	now := int(r.clock.Now().Unix())
	chat, already, err := r.deps.SecretChats.DiscardEncryption(ctx, req.ChatID, userID, deviceAuthKeyID, req.DeleteHistory, now)
	if err != nil {
		return false, secretChatErr(err)
	}
	if !already {
		// Push encryptedChatDiscarded to the peer (history_deleted decides whether the peer deletes the
		// whole conversation). The durable state event is already committed with the discard mutation.
		if peer := chat.PeerOf(userID); peer != 0 {
			r.pushUpdateEncryption(ctx, peer, chat.PeerAuthKeyOf(userID), chat, "secret chat discarded")
		}
	}
	return true, nil
}
