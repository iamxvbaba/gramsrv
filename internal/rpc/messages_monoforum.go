package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"

	"telesrv/internal/domain"
)

// resolveMonoforumForAdmin 解析 parent_peer 指向的 monoforum 虚拟频道,并校验当前用户是其母广播频道
// 的管理员/创建者(频道私信只有频道管理员可读/回复)。monoforum 是私有零成员频道,管理员并非其成员,
// 故走 store 的 membership-agnostic 解析,在母频道上做授权。
// 返回 (monoforum频道, isMonoforum, err):parent 是有效频道但非 monoforum 时返回 (零, false, nil),
// 由调用方回退良性空响应(兼容对普通频道传 parent_peer 的被动探测);是 monoforum 但非管理员→CHAT_ADMIN_REQUIRED。
func (r *Router) resolveMonoforumForAdmin(ctx context.Context, userID int64, parent domain.Peer) (domain.Channel, bool, error) {
	if r.deps.Channels == nil {
		return domain.Channel{}, false, notImplementedErr()
	}
	if parent.Type != domain.PeerTypeChannel || parent.ID == 0 {
		return domain.Channel{}, false, parentPeerInvalidErr()
	}
	mono, isAdmin, err := r.deps.Channels.ResolveMonoforumSend(ctx, userID, parent.ID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			// 非 monoforum 频道(或不存在):非错误,交由调用方回退良性空响应。
			return domain.Channel{}, false, nil
		}
		return domain.Channel{}, false, internalErr()
	}
	if !isAdmin {
		// 是 monoforum,但调用者不是母频道管理员:读私信列表/历史仅限管理员。
		return domain.Channel{}, false, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	return mono, true, nil
}

// monoforumSavedDialogs 返回 monoforum 的订阅者子会话列表(管理员视角的私信列表)。mono 已解析+鉴权。
func (r *Router) monoforumSavedDialogs(ctx context.Context, userID int64, mono domain.Channel, limit, offsetID int) (tg.MessagesSavedDialogsClass, error) {
	list, err := r.deps.Channels.ListMonoforumDialogs(ctx, domain.MonoforumDialogsFilter{
		MonoforumID: mono.ID,
		Limit:       limit,
		OffsetID:    offsetID,
	})
	if err != nil {
		return nil, internalErr()
	}
	dialogs := make([]tg.SavedDialogClass, 0, len(list.Dialogs))
	for _, d := range list.Dialogs {
		peer := tgPeer(d.SavedPeer)
		if peer == nil {
			continue
		}
		md := &tg.MonoForumDialog{
			Peer:            peer,
			TopMessage:      d.TopMessageID,
			ReadInboxMaxID:  d.ReadInboxMaxID,
			ReadOutboxMaxID: d.ReadOutboxMaxID,
			UnreadCount:     d.UnreadCount,
		}
		dialogs = append(dialogs, md)
	}
	messages := make([]tg.MessageClass, 0, len(list.Messages))
	for _, m := range list.Messages {
		if item := tgChannelMessage(userID, m); item != nil {
			messages = append(messages, item)
		}
	}
	return &tg.MessagesSavedDialogs{
		Dialogs:  dialogs,
		Messages: messages,
		Chats:    r.monoforumChats(ctx, userID, mono),
		Users:    r.monoforumSubscriberUsers(ctx, userID, list.Dialogs, list.Messages),
	}, nil
}

// monoforumSavedHistory 返回某订阅者在 monoforum 内的私信历史。mono 已解析+鉴权。
func (r *Router) monoforumSavedHistory(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, limit, offsetID int) (tg.MessagesMessagesClass, error) {
	if savedPeer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	hist, err := r.deps.Channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{
		MonoforumID: mono.ID,
		SavedPeer:   savedPeer,
		Limit:       limit,
		OffsetID:    offsetID,
	})
	if err != nil {
		return nil, internalErr()
	}
	messages := make([]tg.MessageClass, 0, len(hist.Messages))
	for _, m := range hist.Messages {
		if item := tgChannelMessage(userID, m); item != nil {
			messages = append(messages, item)
		}
	}
	return &tg.MessagesMessagesSlice{
		Count:    hist.Count,
		Messages: messages,
		Chats:    r.monoforumChats(ctx, userID, mono),
		Users:    r.monoforumSubscriberUsers(ctx, userID, nil, hist.Messages),
	}, nil
}

// monoforumChats 投影客户端 materialize monoforum 私信所需的频道:monoforum 自身(直接投影,管理员
// 非其成员故不能走可见性受限的 GetChannels)+ 母广播频道(管理员是其成员)。
func (r *Router) monoforumChats(ctx context.Context, userID int64, mono domain.Channel) []tg.ChatClass {
	chats := []tg.ChatClass{tgChannelChatForView(userID, domain.ChannelView{Channel: mono})}
	if mono.LinkedMonoforumID != 0 && r.deps.Channels != nil {
		if views, err := r.deps.Channels.GetChannels(ctx, userID, []int64{mono.LinkedMonoforumID}); err == nil {
			for _, view := range views {
				if view.Channel.ID != 0 {
					chats = append(chats, tgChannelChatForView(userID, view))
				}
			}
		}
	}
	return chats
}

// monoforumSubscriberUsers 投影订阅者用户(子会话 saved_peer + 消息发件人)。
func (r *Router) monoforumSubscriberUsers(ctx context.Context, userID int64, dialogs []domain.MonoforumDialog, messages []domain.ChannelMessage) []tg.UserClass {
	ids := make([]int64, 0, len(dialogs)+len(messages))
	seen := map[int64]struct{}{}
	add := func(id int64) {
		if id == 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, d := range dialogs {
		if d.SavedPeer.Type == domain.PeerTypeUser {
			add(d.SavedPeer.ID)
		}
	}
	for _, m := range messages {
		add(m.SenderUserID)
	}
	if len(ids) == 0 || r.deps.Users == nil {
		return []tg.UserClass{}
	}
	found, err := r.deps.Users.ByIDs(ctx, userID, ids)
	if err != nil {
		return []tg.UserClass{}
	}
	return r.tgUsers(found)
}

// monoforumReplyPresent 判断 sendMessage 的 reply_to 是否带 monoforum_peer_id(频道私信发送的唯一标志)。
// 普通发送恒不带,故据此 gate monoforum 分支,普通发送热路径零额外成本。
func monoforumReplyPresent(input tg.InputReplyToClass) bool {
	switch v := input.(type) {
	case *tg.InputReplyToMonoForum:
		return v != nil
	case *tg.InputReplyToMessage:
		if v == nil {
			return false
		}
		_, ok := v.GetMonoforumPeerID()
		return ok
	default:
		return false
	}
}

// monoforumReplyTargetPeer 从 reply_to 的 monoforum_peer_id 解析订阅者子会话 peer。
func (r *Router) monoforumReplyTargetPeer(userID int64, input tg.InputReplyToClass) (domain.Peer, bool) {
	var inputPeer tg.InputPeerClass
	switch v := input.(type) {
	case *tg.InputReplyToMonoForum:
		if v != nil {
			inputPeer = v.MonoforumPeerID
		}
	case *tg.InputReplyToMessage:
		if v != nil {
			if p, ok := v.GetMonoforumPeerID(); ok {
				inputPeer = p
			}
		}
	}
	if inputPeer == nil {
		return domain.Peer{}, false
	}
	return r.domainPeerFromInputPeer(userID, inputPeer)
}

// sendMonoforumMessage 处理向频道私信(monoforum)发送:订阅者发到自己的子会话,管理员回复到目标订阅者。
// saved_peer 来自 reply_to 的 monoforum_peer_id;管理员可写任意订阅者子会话,普通订阅者只能写自己的。
func (r *Router) sendMonoforumMessage(ctx context.Context, userID int64, peer domain.Peer, req *tg.MessagesSendMessageRequest, fingerprint []byte, preflighted bool) (tg.UpdatesClass, error) {
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if peer.Type != domain.PeerTypeChannel || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	mono, isAdmin, err := r.deps.Channels.ResolveMonoforumSend(ctx, userID, peer.ID)
	if err != nil {
		if errors.Is(err, domain.ErrChannelInvalid) {
			// 带 monoforum_peer_id 却不是 monoforum 频道。
			return nil, tgerr400("CHANNEL_MONOFORUM_UNSUPPORTED")
		}
		return nil, internalErr()
	}
	savedPeer, ok := r.monoforumReplyTargetPeer(userID, req.ReplyTo)
	if !ok || savedPeer.Type != domain.PeerTypeUser || savedPeer.ID == 0 {
		return nil, replyToMonoforumPeerInvalidErr()
	}
	if !isAdmin && savedPeer.ID != userID {
		// 普通订阅者只能写自己的子会话,不能写他人的。
		return nil, replyToMonoforumPeerInvalidErr()
	}
	res, err := r.deps.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
		MonoforumID:            mono.ID,
		SenderUserID:           userID,
		SavedPeer:              savedPeer,
		RandomID:               req.RandomID,
		IdempotencyFingerprint: fingerprint,
		IdempotencyPreflighted: preflighted,
		Message:                req.Message,
		Entities:               domainMessageEntities(req.Entities),
		Date:                   int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, messageSendErr(err)
	}
	return r.monoforumSendUpdates(ctx, userID, mono, savedPeer, res), nil
}

// monoforumSendUpdates 给发送者构造回声 Updates:updateMessageID(关联 random_id)+ updateNewChannelMessage
// (monoforum 走 channel pts)。另一方经 monoforum 频道的 getChannelDifference 收取该 durable 事件。
func (r *Router) monoforumSendUpdates(ctx context.Context, userID int64, mono domain.Channel, savedPeer domain.Peer, res domain.SendChannelMessageResult) tg.UpdatesClass {
	updates := make([]tg.UpdateClass, 0, 3)
	if res.Message.RandomID != 0 {
		updates = append(updates, &tg.UpdateMessageID{ID: res.Message.ID, RandomID: res.Message.RandomID})
	}
	newMsg := &tg.UpdateNewChannelMessage{Pts: res.Event.Pts, PtsCount: res.Event.PtsCount}
	if channelMsg := tgChannelMessage(userID, res.Message); channelMsg != nil {
		newMsg.Message = channelMsg
	} else {
		newMsg.Message = &tg.MessageEmpty{ID: res.Message.ID}
	}
	updates = append(updates, newMsg)
	date := int(r.clock.Now().Unix())
	if res.Duplicate && res.ReplayDeleteEvent != nil {
		if deleted := tgChannelUpdate(userID, *res.ReplayDeleteEvent); deleted != nil {
			updates = append(updates, deleted)
		}
		if res.ReplayDeleteEvent.Date > date {
			date = res.ReplayDeleteEvent.Date
		}
	}
	return &tg.Updates{
		Updates: updates,
		Chats:   r.monoforumChats(ctx, userID, mono),
		Users:   r.monoforumSubscriberUsers(ctx, userID, []domain.MonoforumDialog{{SavedPeer: savedPeer}}, []domain.ChannelMessage{res.Message}),
		Date:    date,
	}
}
