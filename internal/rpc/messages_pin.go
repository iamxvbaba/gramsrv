package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// updatePrivatePinnedMessage 处理 messages.updatePinnedMessage 的私聊
// 分支（含 Saved Messages）。官方语义：非 pm_oneside 的 pin 双侧翻转并
// 生成 messageActionPinMessage 服务消息（reply_to 指向被置顶消息）；
// pm_oneside / Saved Messages / unpin 不生成服务消息；对端与本账号其它
// 设备经可靠投递收到 updatePinnedMessages（账号 pts）。
func (r *Router) updatePrivatePinnedMessage(ctx context.Context, userID int64, peer domain.Peer, req *tg.MessagesUpdatePinnedMessageRequest) (tg.UpdatesClass, error) {
	if r.deps.Messages == nil {
		return nil, notImplementedErr()
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.PinPrivateMessage(ctx, userID, domain.PinPrivateMessageRequest{
		OwnerUserID:     userID,
		Peer:            peer,
		MessageID:       req.ID,
		Pinned:          !req.Unpin,
		PmOneside:       req.PmOneside,
		Silent:          req.Silent,
		Date:            int(r.clock.Now().Unix()),
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, messageIDInvalidErr()
		}
		return nil, internalErr()
	}
	if res.Changed() {
		r.invalidateRPCProjectionForPeer(userID, peer)
		if !req.PmOneside && peer.ID != userID {
			r.invalidateRPCProjectionForPeer(peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
		}
	}
	self := res.Self()
	updates := []tg.UpdateClass(nil)
	if update := tgOtherUpdateFromEvent(self.Event); update != nil {
		updates = append(updates, update)
	}
	users := []tg.UserClass{}
	if res.Changed() && !req.Unpin && !req.PmOneside && peer.ID != userID {
		// 服务消息独立于置顶状态事务：置顶状态是真值源，服务消息失败
		// 不回滚置顶（与官方时间线装饰语义一致）。req.ID 即 owner 视角
		// box id（store 已校验存在），共享升级场景 Self() 可能为空仍需发。
		if sent, sendErr := r.sendPinServiceMessage(ctx, userID, peer.ID, req.ID, req.Silent); sendErr == nil && sent.SenderMessage.ID != 0 {
			if msg := tgMessage(sent.SenderMessage); msg != nil {
				updates = append(updates, &tg.UpdateNewMessage{
					Message:  msg,
					Pts:      sent.SenderEvent.Pts,
					PtsCount: sent.SenderEvent.PtsCount,
				})
			}
			users = r.usersForMessageUpdate(ctx, userID, sent.SenderMessage)
		}
	}
	if len(updates) == 0 {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	}
	return &tg.Updates{
		Updates: updates,
		Users:   users,
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}, nil
}

func (r *Router) sendPinServiceMessage(ctx context.Context, userID, peerUserID int64, pinnedBoxID int, silent bool) (domain.SendPrivateTextResult, error) {
	recipientBlocked, err := r.peerBlocksUser(ctx, userID, peerUserID)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	sessionID, _ := SessionIDFrom(ctx)
	return r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{
		SenderUserID:    userID,
		RecipientUserID: peerUserID,
		RandomID:        pinServiceMessageRandomID(userID, peerUserID, pinnedBoxID, r.clock.Now().UnixNano()),
		Media: &domain.MessageMedia{
			Kind:          domain.MessageMediaKindService,
			ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionPinMessage},
		},
		Silent: silent,
		// reply_to 指向被置顶消息（owner 视角 box id），发送路径自动
		// 翻译为对端视角；客户端凭此渲染"X 置顶了「…」"预览。
		ReplyTo:          &domain.MessageReply{MessageID: pinnedBoxID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peerUserID}},
		Date:             int(r.clock.Now().Unix()),
		OriginAuthKeyID:  rawAuthKeyIDForOrigin(ctx),
		OriginSessionID:  sessionID,
		RecipientBlocked: recipientBlocked,
	})
}

func pinServiceMessageRandomID(userID, peerUserID int64, pinnedBoxID int, nowNano int64) int64 {
	id := nowNano ^ (userID << 23) ^ (peerUserID << 11) ^ int64(pinnedBoxID)
	if id == 0 {
		return int64(pinnedBoxID) | 1
	}
	return id
}

// unpinAllPrivateMessages 处理 messages.unpinAllMessages 的私聊分支。
func (r *Router) unpinAllPrivateMessages(ctx context.Context, userID int64, peer domain.Peer) (*tg.MessagesAffectedHistory, error) {
	authKeyID, _ := AuthKeyIDFrom(ctx)
	if r.deps.Messages == nil {
		return r.affectedHistory(ctx, authKeyID, userID, 0)
	}
	sessionID, _ := SessionIDFrom(ctx)
	res, err := r.deps.Messages.UnpinAllPrivateMessages(ctx, userID, domain.UnpinAllPrivateMessagesRequest{
		OwnerUserID:     userID,
		Peer:            peer,
		Date:            int(r.clock.Now().Unix()),
		OriginAuthKeyID: rawAuthKeyIDForOrigin(ctx),
		OriginSessionID: sessionID,
	})
	if err != nil {
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return nil, messageIDInvalidErr()
		}
		return nil, internalErr()
	}
	self := res.Self()
	if self.Event.Pts == 0 {
		// 无置顶可清：返回当前账号 pts，避免客户端误判 pts 空洞。
		return r.affectedHistory(ctx, authKeyID, userID, 0)
	}
	r.invalidateRPCProjectionForPeer(userID, peer)
	if peer.Type == domain.PeerTypeUser && peer.ID != userID {
		r.invalidateRPCProjectionForPeer(peer.ID, domain.Peer{Type: domain.PeerTypeUser, ID: userID})
	}
	return &tg.MessagesAffectedHistory{
		Pts:      self.Event.Pts,
		PtsCount: self.Event.PtsCount,
		// Offset>0 时客户端按官方 affectedHistory 语义续发 unpinAll 清
		// 剩余批次。
		Offset: res.Offset,
	}, nil
}
