package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
	"telesrv/internal/domain"
)

// Saved Messages 分会话（saved dialogs）真实实现。
// 纯 saved 形态（无 parent_peer）按 self-chat 消息的 saved_peer 分组返回；
// parent_peer = monoforum topic list 仍是范围外，仅校验 parent channel 后
// 返回空（与既有 monoforum stub 行为一致，见 compatibility-matrix）。

func (r *Router) onMessagesGetSavedDialogs(ctx context.Context, req *tg.MessagesGetSavedDialogsRequest) (tg.MessagesSavedDialogsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if parent, hasParent, err := r.validateSavedHistoryParentPeer(ctx, userID, req.GetParentPeer); err != nil {
		return nil, err
	} else if hasParent {
		if req.Limit < 0 {
			return nil, limitInvalidErr()
		}
		offsetID := req.OffsetID
		if offsetID < 0 {
			offsetID = 0
		}
		mono, isMono, err := r.resolveMonoforumForAdmin(ctx, userID, parent)
		if err != nil {
			return nil, err
		}
		if !isMono {
			// 普通频道(非 monoforum)传 parent_peer:保持旧的良性空响应 + parent 上下文。
			return &tg.MessagesSavedDialogs{
				Dialogs:  []tg.SavedDialogClass{},
				Messages: []tg.MessageClass{},
				Chats:    r.savedHistoryChats(ctx, userID, true, parent, nil),
				Users:    []tg.UserClass{},
			}, nil
		}
		// parent_peer = monoforum:返回该频道私信的订阅者子会话列表(管理员视角)。
		return r.monoforumSavedDialogs(ctx, userID, mono, req.Limit, offsetID)
	}
	if req.Limit < 0 {
		return nil, limitInvalidErr()
	}
	offsetID := req.OffsetID
	if offsetID < 0 {
		offsetID = 0
	}
	offsetDate := req.OffsetDate
	if offsetDate < 0 {
		offsetDate = 0
	}
	filter := domain.SavedDialogsFilter{
		ExcludePinned: req.ExcludePinned,
		OffsetID:      offsetID,
		OffsetDate:    offsetDate,
		Limit:         req.Limit,
	}
	if req.OffsetPeer != nil {
		filter.OffsetPeer, _ = r.domainPeerFromInputPeer(userID, req.OffsetPeer)
	}
	if r.deps.Messages == nil {
		if req.Hash != 0 {
			return &tg.MessagesSavedDialogsNotModified{Count: 0}, nil
		}
		return &tg.MessagesSavedDialogs{
			Dialogs:  []tg.SavedDialogClass{},
			Messages: []tg.MessageClass{},
			Chats:    []tg.ChatClass{},
			Users:    []tg.UserClass{},
		}, nil
	}
	list, err := r.deps.Messages.GetSavedDialogs(ctx, userID, filter)
	if err != nil {
		return nil, internalErr()
	}
	if req.Hash != 0 && savedDialogsHash(list) == req.Hash {
		return &tg.MessagesSavedDialogsNotModified{Count: list.Count}, nil
	}
	return r.tgMessagesSavedDialogs(ctx, userID, list), nil
}

func (r *Router) onMessagesGetPinnedSavedDialogs(ctx context.Context) (tg.MessagesSavedDialogsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if r.deps.Messages == nil {
		return &tg.MessagesSavedDialogs{
			Dialogs:  []tg.SavedDialogClass{},
			Messages: []tg.MessageClass{},
			Chats:    []tg.ChatClass{},
			Users:    []tg.UserClass{},
		}, nil
	}
	list, err := r.deps.Messages.GetPinnedSavedDialogs(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	return r.tgMessagesSavedDialogs(ctx, userID, list), nil
}

func (r *Router) onMessagesGetSavedDialogsByID(ctx context.Context, req *tg.MessagesGetSavedDialogsByIDRequest) (tg.MessagesSavedDialogsClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if _, hasParent, err := r.validateSavedHistoryParentPeer(ctx, userID, req.GetParentPeer); err != nil {
		return nil, err
	} else if hasParent {
		return &tg.MessagesSavedDialogs{
			Dialogs:  []tg.SavedDialogClass{},
			Messages: []tg.MessageClass{},
			Chats:    []tg.ChatClass{},
			Users:    []tg.UserClass{},
		}, nil
	}
	if len(req.IDs) > maxDialogInputPeers {
		return nil, limitInvalidErr()
	}
	peers := make([]domain.Peer, 0, len(req.IDs))
	for _, input := range req.IDs {
		// 未知/已删除 peer 静默缺席（客户端用本接口补 stale 数据，
		// 部分命中是正常结果）；显式非法构造仍拒绝。
		peer, ok := r.domainPeerFromInputPeer(userID, input)
		if !ok {
			return nil, peerIDInvalidErr()
		}
		peers = append(peers, peer)
	}
	if r.deps.Messages == nil {
		return &tg.MessagesSavedDialogs{
			Dialogs:  []tg.SavedDialogClass{},
			Messages: []tg.MessageClass{},
			Chats:    []tg.ChatClass{},
			Users:    []tg.UserClass{},
		}, nil
	}
	list, err := r.deps.Messages.GetSavedDialogsByPeers(ctx, userID, peers)
	if err != nil {
		return nil, internalErr()
	}
	return r.tgMessagesSavedDialogs(ctx, userID, list), nil
}

func (r *Router) onMessagesToggleSavedDialogPin(ctx context.Context, req *tg.MessagesToggleSavedDialogPinRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	peers, err := r.dialogPeersFromInput(ctx, userID, []tg.InputDialogPeerClass{req.Peer})
	if err != nil {
		return false, err
	}
	if len(peers) != 1 {
		return false, peerIDInvalidErr()
	}
	pinned := req.GetPinned()
	if r.deps.Messages == nil {
		return true, nil
	}
	changed, err := r.deps.Messages.ToggleSavedDialogPin(ctx, userID, peers[0], pinned)
	if err != nil {
		if errors.Is(err, domain.ErrPinnedSavedDialogsTooMuch) {
			return false, savedPinnedTooMuchErr()
		}
		return false, internalErr()
	}
	if changed {
		date := int(r.clock.Now().Unix())
		var recorded domain.UpdateEvent
		if r.deps.Updates != nil {
			authKeyID, _ := AuthKeyIDFrom(ctx)
			sessionID, _ := SessionIDFrom(ctx)
			event, state, err := r.deps.Updates.RecordSavedDialogPinned(ctx, authKeyID, userID, peers[0], pinned, rawAuthKeyIDForOrigin(ctx), sessionID)
			if err != nil {
				return false, internalErr()
			}
			date = state.Date
			recorded = event
		}
		r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
		r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
			Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{&tg.UpdateSavedDialogPinned{
				Pinned: pinned,
				Peer:   tgDialogPeer(peers[0]),
			}}, recorded),
			Date: date,
			Seq:  0,
		})
	}
	return true, nil
}

func (r *Router) onMessagesReorderPinnedSavedDialogs(ctx context.Context, req *tg.MessagesReorderPinnedSavedDialogsRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if len(req.Order) > maxDialogInputPeers {
		return false, limitInvalidErr()
	}
	peers, err := r.dialogPeersFromInput(ctx, userID, req.Order)
	if err != nil {
		return false, err
	}
	if r.deps.Messages == nil {
		return true, nil
	}
	if err := r.deps.Messages.ReorderPinnedSavedDialogs(ctx, userID, peers, req.GetForce()); err != nil {
		if errors.Is(err, domain.ErrPinnedSavedDialogsTooMuch) {
			return false, savedPinnedTooMuchErr()
		}
		return false, internalErr()
	}
	date := int(r.clock.Now().Unix())
	var recorded domain.UpdateEvent
	if r.deps.Updates != nil {
		authKeyID, _ := AuthKeyIDFrom(ctx)
		sessionID, _ := SessionIDFrom(ctx)
		event, state, err := r.deps.Updates.RecordPinnedSavedDialogs(ctx, authKeyID, userID, peers, rawAuthKeyIDForOrigin(ctx), sessionID)
		if err != nil {
			return false, internalErr()
		}
		date = state.Date
		recorded = event
	}
	r.bookkeepAuxPtsForCurrentSession(ctx, recorded)
	update := &tg.UpdatePinnedSavedDialogs{}
	if len(peers) > 0 {
		update.SetOrder(tgDialogPeers(peers))
	}
	r.pushUserUpdatesIfNoReliableDispatch(ctx, userID, &tg.Updates{
		Updates: appendAuxPtsBookkeeping([]tg.UpdateClass{update}, recorded),
		Date:    date,
		Seq:     0,
	})
	return true, nil
}

// tgMessagesSavedDialogs 把业务结果转 TL 输出：Full 映射 savedDialogs（已到
// 末尾），否则 savedDialogsSlice + 总数。
func (r *Router) tgMessagesSavedDialogs(ctx context.Context, userID int64, list domain.SavedDialogList) tg.MessagesSavedDialogsClass {
	dialogs := make([]tg.SavedDialogClass, 0, len(list.Dialogs))
	for _, d := range list.Dialogs {
		peer := tgPeer(d.Peer)
		if peer == nil {
			continue
		}
		sd := &tg.SavedDialog{Peer: peer, TopMessage: d.TopMessage}
		if d.Pinned {
			sd.SetPinned(true)
		}
		dialogs = append(dialogs, sd)
	}
	messages := make([]tg.MessageClass, 0, len(list.Messages))
	for _, msg := range list.Messages {
		if item := tgMessage(msg); item != nil {
			messages = append(messages, item)
		}
	}
	users, chats := r.savedDialogsProjection(ctx, userID, list)
	if list.Full {
		return &tg.MessagesSavedDialogs{
			Dialogs:  dialogs,
			Messages: messages,
			Chats:    chats,
			Users:    users,
		}
	}
	return &tg.MessagesSavedDialogsSlice{
		Count:    list.Count,
		Dialogs:  dialogs,
		Messages: messages,
		Chats:    chats,
		Users:    users,
	}
}

// savedDialogsProjection 投影子会话引用到的 users/chats：self 恒带（self-chat
// 上下文 + My Notes 子会话），saved peer / fwd 作者 / 跳回源会话批量补齐，
// hidden author 占位用户在库中不存在、按需合成。
func (r *Router) savedDialogsProjection(ctx context.Context, userID int64, list domain.SavedDialogList) ([]tg.UserClass, []tg.ChatClass) {
	userIDs := make([]int64, 0, len(list.Dialogs))
	channelIDs := make([]int64, 0)
	seenUsers := map[int64]struct{}{}
	seenChannels := map[int64]struct{}{}
	needHidden := false
	addPeer := func(p domain.Peer) {
		switch p.Type {
		case domain.PeerTypeUser:
			if p.ID == 0 || p.ID == userID {
				return
			}
			if p.ID == domain.SavedHiddenAuthorUserID {
				needHidden = true
				return
			}
			if _, ok := seenUsers[p.ID]; ok {
				return
			}
			seenUsers[p.ID] = struct{}{}
			userIDs = append(userIDs, p.ID)
		case domain.PeerTypeChannel:
			if p.ID == 0 {
				return
			}
			if _, ok := seenChannels[p.ID]; ok {
				return
			}
			seenChannels[p.ID] = struct{}{}
			channelIDs = append(channelIDs, p.ID)
		}
	}
	for _, d := range list.Dialogs {
		addPeer(d.Peer)
	}
	for _, msg := range list.Messages {
		if msg.Forward != nil {
			addPeer(msg.Forward.From)
			addPeer(msg.Forward.SavedFrom)
		}
	}
	users := make([]tg.UserClass, 0, len(userIDs)+2)
	if r.deps.Users != nil {
		if self, err := r.deps.Users.Self(ctx, userID); err == nil && self.ID != 0 {
			users = append(users, r.tgSelfUser(self))
		}
		if len(userIDs) > 0 {
			if found, err := r.deps.Users.ByIDs(ctx, userID, userIDs); err == nil {
				users = append(users, r.tgUsers(found)...)
			}
		}
	}
	if needHidden {
		users = append(users, tgSavedHiddenAuthorUser())
	}
	chats := make([]tg.ChatClass, 0, len(channelIDs))
	if r.deps.Channels != nil && len(channelIDs) > 0 {
		if views, err := r.deps.Channels.GetChannels(ctx, userID, channelIDs); err == nil {
			for _, view := range views {
				if view.Channel.ID == 0 {
					continue
				}
				chats = append(chats, tgChannelChatForView(userID, view))
			}
		}
	}
	return users, chats
}

// tgSavedHiddenAuthorUser 合成 hidden-author 占位用户（官方 user 2666000）。
// TDesktop/Android 都按 id 特判本地展示，名字仅作兜底。
func tgSavedHiddenAuthorUser() *tg.User {
	return &tg.User{ID: domain.SavedHiddenAuthorUserID, FirstName: "Anonymous"}
}

// savedDialogsHash 与 DrKLO Android SavedMessagesController 的请求 hash 算法
// 对齐：对返回序列逐项喂 pinned/abs(peerId)/top_message_id/top date；TDesktop
// hash 恒 0，不触发 notModified。
func savedDialogsHash(list domain.SavedDialogList) int64 {
	dateByID := make(map[int]int, len(list.Messages))
	for _, msg := range list.Messages {
		dateByID[msg.ID] = msg.Date
	}
	var hash uint64
	for _, d := range list.Dialogs {
		pinned := int64(0)
		if d.Pinned {
			pinned = 1
		}
		id := d.Peer.ID
		if id < 0 {
			id = -id
		}
		hash = tdesktopHashUpdate(hash, pinned)
		hash = tdesktopHashUpdate(hash, id)
		hash = tdesktopHashUpdate(hash, int64(d.TopMessage))
		hash = tdesktopHashUpdate(hash, int64(dateByID[d.TopMessage]))
	}
	return int64(hash)
}
