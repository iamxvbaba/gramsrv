package rpc

import (
	"context"
	"sync"

	"github.com/gotd/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onMessagesSendReaction(ctx context.Context, req *tg.MessagesSendReactionRequest) (tg.UpdatesClass, error) {
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if reactions, ok := req.GetReaction(); ok && len(reactions) > maxReactionVector {
		return nil, limitInvalidErr()
	}
	userID, peer, err := r.reactionPeer(ctx, req.Peer, nil)
	if err != nil {
		return nil, err
	}
	reactions, err := domainMessageReactionsFromTL(req)
	if err != nil {
		return nil, err
	}
	reactions = r.normalizeDefaultReactionDocuments(ctx, reactions)
	// 官方语义（reactions_user_max_default/premium）：向量尾部是最新选择，
	// 超出每用户上限丢弃旧的而非报错；premium viewer 用 premium 档（appConfig
	// reactions_user_max_premium=3），否则客户端允许的多 reaction 会被静默裁剪。
	perUserMax := domain.MessageReactionsUserMax(r.viewerPremium(ctx, userID))
	reactions = domain.TrimMessageReactionsToUserMax(reactions, perUserMax)
	date := int(r.clock.Now().Unix())
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		res, err := r.deps.Channels.SetMessageReactions(ctx, userID, domain.SetChannelMessageReactionsRequest{
			UserID:              userID,
			ChannelID:           peer.ID,
			MessageID:           req.MsgID,
			Reactions:           reactions,
			Big:                 req.Big,
			AddToRecent:         req.GetAddToRecent(),
			Date:                date,
			ReactionsPerUserMax: perUserMax,
		})
		if err != nil {
			return nil, channelReactionErr(err)
		}
		updates := r.channelMessageReactionsUpdates(ctx, userID, res)
		// 官方保证消息作者收到 updateMessageReactions：作者进 explicit 收件人，
		// 不依赖在线 viewer 采样（fan-out 封顶后作者可能被挤出采样集）。
		ids := []int{res.Message.ID}
		r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, []int64{userID, res.Message.SenderUserID}, func(viewerUserID int64) *tg.Updates {
			return r.channelReactionsViewerUpdates(ctx, userID, viewerUserID, res, ids)
		})
		return updates, nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
		if len(reactions) == 0 && r.shouldSuppressTransientPrivateReactionClear(userID, peer, req.MsgID, date) {
			res, err := r.deps.Messages.GetMessageReactions(ctx, userID, domain.PrivateMessageReactionsRequest{
				OwnerUserID: userID,
				Peer:        peer,
				IDs:         []int{req.MsgID},
			})
			if err != nil {
				return nil, messageReactionErr(err)
			}
			return r.privateMessagesReactionsUpdates(ctx, userID, peer, res, []int{req.MsgID}), nil
		}
		if req.Big && len(reactions) > 0 {
			r.rememberTransientPrivateBigReaction(userID, peer, req.MsgID, date)
		}
		res, err := r.deps.Messages.SetMessageReactions(ctx, userID, domain.SetPrivateMessageReactionsRequest{
			UserID:              userID,
			Peer:                peer,
			MessageID:           req.MsgID,
			Reactions:           reactions,
			Big:                 req.Big,
			AddToRecent:         req.GetAddToRecent(),
			Date:                date,
			ReactionsPerUserMax: perUserMax,
		})
		if err != nil {
			return nil, messageReactionErr(err)
		}
		if len(reactions) == 0 {
			r.forgetTransientPrivateBigReaction(userID, peer, req.MsgID)
		}
		if err := r.recordMessageReactionUse(ctx, userID, reactions, req.GetAddToRecent(), date); err != nil {
			return nil, internalErr()
		}
		recordedEvents, err := r.recordPrivateMessageReactionEvents(ctx, userID, res)
		if err != nil {
			return nil, internalErr()
		}
		// reaction 事件占双方账号 pts 但 updateMessageReactions 不带 pts；
		// 在线直推必须附 pts 簿记，否则双方下一条带 pts 的更新被判空洞。
		updates := r.privateMessageReactionsUpdates(ctx, userID, peer, res)
		if updates != nil {
			updates.Updates = appendAuxPtsBookkeeping(updates.Updates, recordedEvents[userID])
		}
		r.pushUserUpdates(ctx, userID, updates)
		for _, msg := range res.Messages {
			if msg.OwnerUserID == 0 || msg.OwnerUserID == userID {
				continue
			}
			viewerPeer := msg.Peer
			viewerUpdates := r.privateMessageReactionsUpdates(ctx, msg.OwnerUserID, viewerPeer, res)
			if viewerUpdates != nil {
				viewerUpdates.Updates = appendAuxPtsBookkeeping(viewerUpdates.Updates, recordedEvents[msg.OwnerUserID])
			}
			r.pushUserUpdates(ctx, msg.OwnerUserID, viewerUpdates)
		}
		return updates, nil
	}
	return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
}

const (
	transientPrivateBigReactionClearWindowSeconds = 3
	transientPrivateBigReactionMaxEntries         = 4096
)

type transientPrivateBigReactionKey struct {
	UserID    int64
	PeerID    int64
	MessageID int
}

type transientPrivateBigReactionEntry struct {
	ExpiresAt int
}

type transientPrivateBigReactionCache struct {
	mu      sync.Mutex
	entries map[transientPrivateBigReactionKey]transientPrivateBigReactionEntry
}

func transientPrivateBigReactionMapKey(userID int64, peer domain.Peer, messageID int) transientPrivateBigReactionKey {
	return transientPrivateBigReactionKey{
		UserID:    userID,
		PeerID:    peer.ID,
		MessageID: messageID,
	}
}

func (r *Router) rememberTransientPrivateBigReaction(userID int64, peer domain.Peer, messageID int, date int) {
	if peer.Type != domain.PeerTypeUser || userID == 0 || peer.ID == 0 || messageID <= 0 || date <= 0 {
		return
	}
	r.transientPrivateBigReactions.remember(transientPrivateBigReactionMapKey(userID, peer, messageID), date+transientPrivateBigReactionClearWindowSeconds, date)
}

func (r *Router) shouldSuppressTransientPrivateReactionClear(userID int64, peer domain.Peer, messageID int, date int) bool {
	return r.transientPrivateBigReactions.shouldSuppress(transientPrivateBigReactionMapKey(userID, peer, messageID), date)
}

func (r *Router) forgetTransientPrivateBigReaction(userID int64, peer domain.Peer, messageID int) {
	r.transientPrivateBigReactions.forget(transientPrivateBigReactionMapKey(userID, peer, messageID))
}

func (c *transientPrivateBigReactionCache) remember(key transientPrivateBigReactionKey, expiresAt int, now int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[transientPrivateBigReactionKey]transientPrivateBigReactionEntry)
	}
	if len(c.entries) >= transientPrivateBigReactionMaxEntries {
		c.pruneLocked(now)
	}
	if len(c.entries) >= transientPrivateBigReactionMaxEntries {
		c.dropOneLocked()
	}
	c.entries[key] = transientPrivateBigReactionEntry{ExpiresAt: expiresAt}
}

func (c *transientPrivateBigReactionCache) shouldSuppress(key transientPrivateBigReactionKey, now int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok {
		return false
	}
	if now > entry.ExpiresAt {
		delete(c.entries, key)
		return false
	}
	return true
}

func (c *transientPrivateBigReactionCache) forget(key transientPrivateBigReactionKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

func (c *transientPrivateBigReactionCache) pruneLocked(now int) {
	for key, entry := range c.entries {
		if now > entry.ExpiresAt {
			delete(c.entries, key)
		}
	}
}

func (c *transientPrivateBigReactionCache) dropOneLocked() {
	var oldestKey transientPrivateBigReactionKey
	oldestExpiresAt := int(^uint(0) >> 1)
	for key, entry := range c.entries {
		if entry.ExpiresAt < oldestExpiresAt {
			oldestKey = key
			oldestExpiresAt = entry.ExpiresAt
		}
	}
	delete(c.entries, oldestKey)
}

func (r *Router) recordMessageReactionUse(ctx context.Context, userID int64, reactions []domain.MessageReaction, addToRecent bool, date int) error {
	if len(reactions) == 0 || r.deps.Channels == nil {
		return nil
	}
	recorder, ok := r.deps.Channels.(messageReactionUsageRecorder)
	if !ok {
		return nil
	}
	return recorder.RecordMessageReactionUse(ctx, userID, reactions, addToRecent, date)
}

func (r *Router) recordPrivateMessageReactionEvents(ctx context.Context, requestUserID int64, res domain.PrivateMessageReactionsResult) (map[int64]domain.UpdateEvent, error) {
	if r.deps.Updates == nil {
		return nil, nil
	}
	recorder, ok := r.deps.Updates.(messageReactionUpdateRecorder)
	if !ok {
		return nil, nil
	}
	authKeyID, _ := AuthKeyIDFrom(ctx)
	events := make(map[int64]domain.UpdateEvent, len(res.Messages))
	for _, msg := range res.Messages {
		if msg.OwnerUserID == 0 || msg.ID == 0 {
			continue
		}
		eventAuthKeyID := [8]byte{}
		if msg.OwnerUserID == requestUserID {
			eventAuthKeyID = authKeyID
		}
		event, _, err := recorder.RecordMessageReactions(ctx, eventAuthKeyID, msg.OwnerUserID, msg)
		if err != nil {
			return nil, err
		}
		events[msg.OwnerUserID] = event
	}
	return events, nil
}

func (r *Router) channelMessageReactionsUpdates(ctx context.Context, viewerUserID int64, res domain.ChannelMessageReactionsResult) *tg.Updates {
	ids := []int{res.Message.ID}
	if res.Message.ID <= 0 && len(res.Messages) > 0 {
		ids = []int{res.Messages[0].ID}
	}
	return r.channelMessagesReactionsUpdates(ctx, viewerUserID, res, ids)
}

// channelReactionsViewerUpdates 为 fan-out 构造某 viewer 的 updateMessageReactions。
// res 内的聚合是请求者视角（chosen/My/unread 都是 per-viewer 字段）：
//   - 请求者本人：直接用；
//   - 消息作者：按作者视角重载（unread 角标与作者自己的 chosen 必须正确）；
//   - 其他 viewer：官方 min 语义——只下发计数与 recent 列表，客户端保留本地 chosen
//     （TDesktop 非 min 更新会用 chosen_order 直接覆盖本地 my 状态，串视角即"别人的
//     reaction 显示成我选的"）。
func (r *Router) channelReactionsViewerUpdates(ctx context.Context, requestUserID, viewerUserID int64, res domain.ChannelMessageReactionsResult, ids []int) *tg.Updates {
	if viewerUserID == requestUserID {
		return r.channelMessagesReactionsUpdates(ctx, viewerUserID, res, ids)
	}
	if viewerUserID != 0 && viewerUserID == reactionsResultSenderID(res) && r.deps.Channels != nil {
		reloaded, err := r.deps.Channels.GetMessageReactions(ctx, viewerUserID, domain.ChannelMessageReactionsRequest{
			UserID:    viewerUserID,
			ChannelID: res.Channel.ID,
			IDs:       append([]int(nil), ids...),
		})
		if err == nil && len(reloaded.Messages) > 0 {
			reloaded.Channel = res.Channel
			return r.channelMessagesReactionsUpdates(ctx, viewerUserID, reloaded, ids)
		}
	}
	updates := r.channelMessagesReactionsUpdates(ctx, viewerUserID, minifyChannelReactionsResult(res), ids)
	if updates != nil {
		for _, update := range updates.Updates {
			if reactions, ok := update.(*tg.UpdateMessageReactions); ok {
				reactions.Reactions.Min = true
			}
		}
	}
	return updates
}

// reactionsResultSenderID 取结果中消息作者（单消息场景；多消息 moderation 不做作者特判）。
func reactionsResultSenderID(res domain.ChannelMessageReactionsResult) int64 {
	if res.Message.ID != 0 {
		return res.Message.SenderUserID
	}
	if len(res.Messages) == 1 {
		return res.Messages[0].SenderUserID
	}
	return 0
}

// minifyChannelReactionsResult 深拷并清掉所有 per-viewer 字段（chosen/My/unread），
// 供 min 推送使用；不改原 res（请求者响应仍用全量视角）。
func minifyChannelReactionsResult(res domain.ChannelMessageReactionsResult) domain.ChannelMessageReactionsResult {
	scrub := func(in *domain.ChannelMessageReactions) *domain.ChannelMessageReactions {
		if in == nil {
			return nil
		}
		out := domain.ChannelMessageReactions{
			CanSeeList: in.CanSeeList,
			Results:    make([]domain.ChannelMessageReactionCount, 0, len(in.Results)),
			Recent:     make([]domain.ChannelMessagePeerReaction, 0, len(in.Recent)),
		}
		for _, item := range in.Results {
			item.ChosenOrder = 0
			out.Results = append(out.Results, item)
		}
		for _, item := range in.Recent {
			item.My = false
			item.Unread = false
			item.ChosenOrder = 0
			out.Recent = append(out.Recent, item)
		}
		return &out
	}
	out := res
	if scrubbed := scrub(res.Message.Reactions); scrubbed != nil {
		msg := res.Message
		msg.Reactions = scrubbed
		out.Message = msg
	}
	if len(res.Messages) > 0 {
		out.Messages = make([]domain.ChannelMessage, 0, len(res.Messages))
		for _, msg := range res.Messages {
			msg.Reactions = scrub(msg.Reactions)
			out.Messages = append(out.Messages, msg)
		}
	}
	if scrubbed := scrub(&res.Reactions); scrubbed != nil {
		out.Reactions = *scrubbed
	}
	return out
}

func (r *Router) privateMessageReactionsUpdates(ctx context.Context, viewerUserID int64, peer domain.Peer, res domain.PrivateMessageReactionsResult) *tg.Updates {
	ids := make([]int, 0, 1)
	for _, msg := range res.Messages {
		if msg.OwnerUserID == viewerUserID && msg.ID > 0 {
			ids = append(ids, msg.ID)
			break
		}
	}
	return r.privateMessagesReactionsUpdates(ctx, viewerUserID, peer, res, ids)
}

func (r *Router) privateMessagesReactionsUpdates(ctx context.Context, viewerUserID int64, peer domain.Peer, res domain.PrivateMessageReactionsResult, ids []int) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, len(ids))
	messagesByID := make(map[int]domain.Message, len(res.Messages))
	userIDs := []int64{viewerUserID}
	if peer.Type == domain.PeerTypeUser && peer.ID != 0 {
		userIDs = append(userIDs, peer.ID)
	}
	for _, msg := range res.Messages {
		if msg.OwnerUserID != viewerUserID || msg.ID == 0 {
			continue
		}
		messagesByID[msg.ID] = msg
		if msg.Peer.Type == domain.PeerTypeUser && msg.Peer.ID != 0 {
			userIDs = append(userIDs, msg.Peer.ID)
		}
		if msg.From.Type == domain.PeerTypeUser && msg.From.ID != 0 {
			userIDs = append(userIDs, msg.From.ID)
		}
		if msg.Reactions != nil {
			userIDs = append(userIDs, channelMessageReactionUserIDs(*msg.Reactions)...)
		}
	}
	userIDs = append(userIDs, channelMessageReactionUserIDs(res.Reactions)...)
	fallbackPeer := tgPeer(peer)
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			continue
		}
		msg, ok := messagesByID[id]
		outPeer := fallbackPeer
		reactions := domain.ChannelMessageReactions{
			CanSeeList: true,
			Results:    []domain.ChannelMessageReactionCount{},
			Recent:     []domain.ChannelMessagePeerReaction{},
		}
		if ok {
			outPeer = tgPeer(msg.Peer)
			if msg.Reactions != nil {
				reactions = *msg.Reactions
			}
		}
		if outPeer == nil {
			continue
		}
		converted := tgMessageReactions(viewerUserID, &reactions)
		if converted == nil {
			converted = &tg.MessageReactions{Results: []tg.ReactionCount{}}
		}
		updates = append(updates, &tg.UpdateMessageReactions{
			Peer:      outPeer,
			MsgID:     id,
			Reactions: *converted,
		})
	}
	return &tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForIDs(ctx, viewerUserID, userIDs),
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) channelMessagesReactionsUpdates(ctx context.Context, viewerUserID int64, res domain.ChannelMessageReactionsResult, ids []int) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, len(ids))
	messagesByID := make(map[int]domain.ChannelMessage, len(res.Messages)+1)
	if res.Message.ID != 0 {
		messagesByID[res.Message.ID] = res.Message
	}
	for _, msg := range res.Messages {
		if msg.ID != 0 {
			messagesByID[msg.ID] = msg
		}
	}
	userIDs := make([]int64, 0)
	for _, msg := range messagesByID {
		if msg.Reactions != nil {
			userIDs = append(userIDs, channelMessageReactionUserIDs(*msg.Reactions)...)
		}
	}
	userIDs = append(userIDs, channelMessageReactionUserIDs(res.Reactions)...)
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			continue
		}
		msg, ok := messagesByID[id]
		reactions := domain.ChannelMessageReactions{
			CanSeeList: !res.Channel.Broadcast || res.Channel.Megagroup,
			Results:    []domain.ChannelMessageReactionCount{},
			Recent:     []domain.ChannelMessagePeerReaction{},
		}
		if ok && msg.Reactions != nil {
			reactions = *msg.Reactions
		} else if ok && len(res.Reactions.Results) > 0 && res.Message.ID == id {
			reactions = res.Reactions
		}
		converted := tgMessageReactions(viewerUserID, &reactions)
		if converted == nil {
			converted = &tg.MessageReactions{Results: []tg.ReactionCount{}}
		}
		update := &tg.UpdateMessageReactions{
			Peer:      &tg.PeerChannel{ChannelID: res.Channel.ID},
			MsgID:     id,
			Reactions: *converted,
		}
		if ok {
			if topID := channelMessageThreadRootID(msg); topID > 0 && topID != id {
				update.SetTopMsgID(topID)
			}
		}
		updates = append(updates, update)
	}
	return &tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForIDs(ctx, viewerUserID, userIDs),
		Chats:   tgChannels(viewerUserID, []domain.Channel{res.Channel}),
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func channelMessageReactionUserIDs(reactions domain.ChannelMessageReactions) []int64 {
	out := make([]int64, 0, len(reactions.Recent))
	for _, item := range reactions.Recent {
		if item.UserID != 0 {
			out = append(out, item.UserID)
		}
	}
	return out
}
