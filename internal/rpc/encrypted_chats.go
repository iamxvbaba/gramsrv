package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	appsecret "telesrv/internal/app/secretchat"
	"telesrv/internal/domain"
)

// 私聊端对端加密（Secret Chat / encrypted chat）握手 RPC handler。状态机与 DH 校验
// 归 app/secretchat；本文件只做鉴权、入参校验、TL 转换与在线推送编排。
//
// P0 范围：requestEncryption / acceptEncryption / discardEncryption 的握手闭环 +
// updateEncryption 在线推送（账号级 pushUserMessage，与 phone 同套）。设备级定向、
// durable 离线 getDifference 补偿、qts 消息投递（sendEncrypted 等）见 P1，
// 设计 docs/secret-chat-module.md。服务端是盲中继，永不接触共享密钥与明文。

// secretChatErr 把 app/secretchat + domain 业务错误映射为 RPC_ERROR。
func secretChatErr(err error) error {
	switch {
	case errors.Is(err, appsecret.ErrGAInvalid):
		return dhGAInvalidErr()
	case errors.Is(err, domain.ErrSecretChatAlreadyAccepted):
		return encryptionAlreadyAcceptedErr()
	case errors.Is(err, domain.ErrSecretChatAlreadyDeclined):
		return encryptionAlreadyDeclinedErr()
	case errors.Is(err, domain.ErrSecretChatNotFound):
		return chatIDInvalidErr()
	default:
		return internalErr()
	}
}

// secretChatRequireUser 是密聊 RPC 的统一登录闸门。
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

// businessAuthKeyIDFrom 返回当前连接业务视角 auth_key_id 的 int64 绑定值。
func businessAuthKeyIDFrom(ctx context.Context) (int64, bool) {
	id, ok := AuthKeyIDFrom(ctx)
	if !ok {
		return 0, false
	}
	return businessAuthKeyInt64(id), true
}

// pushUpdateEncryption 把 targetUserID 视角的 updateEncryption 推给其全部在线设备。
// P0 用账号级在线推送（设备级定向 + 离线补偿见 P1）。
func (r *Router) pushUpdateEncryption(ctx context.Context, targetUserID int64, chat domain.SecretChat, logMessage string) {
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
	r.pushUserMessage(ctx, targetUserID, logMessage, upd)
}

// recordEncryptionEventBestEffort 写入 durable updateEncryption 状态事件供离线设备
// getDifference 补偿。best-effort：失败仅记日志不阻断 RPC（chat 本身已 durable）。
func (r *Router) recordEncryptionEventBestEffort(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, date int) {
	if r.deps.SecretChats == nil {
		return
	}
	if err := r.deps.SecretChats.RecordEncryptionEvent(ctx, chatID, targetUserID, targetAuthKeyID, date); err != nil {
		r.log.Debug("record encryption state event", zap.Error(err))
	}
}

// discardSecretChatsForAuthKey 在设备登出 / 授权撤销时级联 discard 该 perm auth_key 绑定的
// 全部活跃密聊，并向对端推送 encryptedChatDiscarded（在线）+ 写 durable 事件（离线 getDifference
// 补偿）。ownerUserID 是被销毁设备的所有者，用于定位对端。best-effort：失败仅记日志，绝不阻断
// 登出/撤销。修复 P1：此前 onAuthLogOut 等不级联 discard，对端继续往死 auth_key 投递成静默死链
// （消息 acked=f / qts 永久积压，对端永看不到 discarded）。
func (r *Router) discardSecretChatsForAuthKey(ctx context.Context, businessAuthKeyID, ownerUserID int64) {
	if r.deps.SecretChats == nil || businessAuthKeyID == 0 || ownerUserID == 0 {
		return
	}
	discarded, err := r.deps.SecretChats.DiscardForAuthKey(ctx, businessAuthKeyID)
	if err != nil {
		// DiscardForAuthKey 出错也会返回已成功 discard 的部分，继续通知这部分对端。
		r.log.Debug("cascade discard secret chats for auth key", zap.Error(err))
	}
	now := int(r.clock.Now().Unix())
	for _, chat := range discarded {
		peer := chat.PeerOf(ownerUserID)
		if peer == 0 {
			continue
		}
		// 对端绑定设备已知则 device-level 定向，建链前（未绑定，0）则账号级。
		r.recordEncryptionEventBestEffort(ctx, chat.ID, peer, chat.PeerAuthKeyOf(ownerUserID), now)
		r.pushUpdateEncryption(ctx, peer, chat, "secret chat discarded on peer logout/revoke")
	}
}

func (r *Router) onMessagesRequestEncryption(ctx context.Context, req *tg.MessagesRequestEncryptionRequest) (tg.EncryptedChatClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
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
	// 自聊 / bot / 不存在统一 USER_ID_INVALID（bot 不支持密聊）。
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
	// 建链前邀请是账号级（targetAuthKeyID=0）：participant 所有设备（含离线）可见。
	r.recordEncryptionEventBestEffort(ctx, chat.ID, chat.ParticipantUserID, 0, chat.Date)
	// 推接受方全部在线设备 encryptedChatRequested（携 g_a）。离线设备经 getDifference 补回。
	r.pushUpdateEncryption(ctx, chat.ParticipantUserID, chat, "secret chat requested")
	// 发起方同步收 encryptedChatWaiting（无 g_a）。
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
	chat, err := r.deps.SecretChats.AcceptEncryption(ctx, req.Peer.ChatID, userID,
		participantAuthKeyID, req.Peer.AccessHash, req.GB, req.KeyFingerprint)
	if err != nil {
		return nil, secretChatErr(err)
	}
	// 建链完成定向发起方绑定设备（device-level）：离线发起方经 getDifference 补回成型态。
	r.recordEncryptionEventBestEffort(ctx, chat.ID, chat.AdminUserID, chat.AdminAuthKeyID, int(r.clock.Now().Unix()))
	// 推发起方全部在线设备 encryptedChat（GAOrB=g_b, key_fingerprint），发起方据此
	// 算共享密钥并比对指纹。
	r.pushUpdateEncryption(ctx, chat.AdminUserID, chat, "secret chat accepted")
	// 接受方同步收 encryptedChat（GAOrB=g_a）。
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
	chat, already, err := r.deps.SecretChats.DiscardEncryption(ctx, req.ChatID, userID, req.DeleteHistory)
	if err != nil {
		return false, secretChatErr(err)
	}
	if !already {
		// 推对端 encryptedChatDiscarded（history_deleted 决定对端是否删整个会话）。
		// 对端绑定设备已知则 device-level，建链前（未绑定）则账号级（同 requested 集合）。
		if peer := chat.PeerOf(userID); peer != 0 {
			r.recordEncryptionEventBestEffort(ctx, chat.ID, peer, chat.PeerAuthKeyOf(userID), int(r.clock.Now().Unix()))
			r.pushUpdateEncryption(ctx, peer, chat, "secret chat discarded")
		}
	}
	return true, nil
}
