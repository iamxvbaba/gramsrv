package rpc

import (
	"context"
	"fmt"
	"sync"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"telesrv/internal/store"
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
	if peer.Type == domain.PeerTypeUser && peer.ID == userID &&
		len(reactions) > 0 && !r.viewerPremium(ctx, userID) {
		return nil, premiumAccountRequiredErr()
	}
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
		// Core returns only the caller projection. Reliable multi-session
		// delivery is owned by the channel aggregate and Egress lane.
		return updates, nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
		if err := r.requireAccountDelivery(userID, "messages.sendReaction"); err != nil {
			return nil, err
		}
		if peer.ID != userID && len(reactions) == 0 && r.shouldSuppressTransientPrivateReactionClear(userID, peer, req.MsgID, date) {
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
		if peer.ID != userID && req.Big && len(reactions) > 0 {
			r.rememberTransientPrivateBigReaction(userID, peer, req.MsgID, date)
		}
		// Delivery payloads are projected before the store transaction starts.
		// The builder passed into the aggregate is therefore a pure snapshot ->
		// opaque-bytes function and cannot deadlock on a second DB connection
		// while the reaction transaction holds both user rows.
		deliveryUsers := map[int64][]tg.UserClass{
			userID: r.tgUsersForIDs(ctx, userID, []int64{userID, peer.ID}),
		}
		if peer.ID != userID {
			deliveryUsers[peer.ID] = r.tgUsersForIDs(ctx, peer.ID, []int64{userID, peer.ID})
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
		}, r.privateReactionDeliveryEffects(ctx, userID, deliveryUsers, date))
		if err != nil {
			return nil, messageReactionErr(err)
		}
		if len(reactions) == 0 {
			r.forgetTransientPrivateBigReaction(userID, peer, req.MsgID)
		}
		// Saved Messages reactions are private tags, not ordinary reaction usage.
		if peer.ID != userID {
			if err := r.recordMessageReactionUse(ctx, userID, reactions, req.GetAddToRecent(), date); err != nil {
				return nil, internalErr()
			}
		}
		updates := r.privateMessageReactionsUpdates(ctx, userID, peer, res)
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

func (r *Router) channelMessageReactionsUpdates(ctx context.Context, viewerUserID int64, res domain.ChannelMessageReactionsResult) *tg.Updates {
	ids := []int{res.Message.ID}
	if res.Message.ID <= 0 && len(res.Messages) > 0 {
		ids = []int{res.Messages[0].ID}
	}
	return r.channelMessagesReactionsUpdates(ctx, viewerUserID, res, ids)
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
	userIDs := []int64{viewerUserID}
	if peer.Type == domain.PeerTypeUser && peer.ID != 0 {
		userIDs = append(userIDs, peer.ID)
	}
	for _, msg := range res.Messages {
		if msg.OwnerUserID != viewerUserID || msg.ID == 0 {
			continue
		}
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
	return privateMessagesReactionsUpdatesWithUsers(viewerUserID, peer, res, ids, r.tgUsersForIDs(ctx, viewerUserID, userIDs), int(r.clock.Now().Unix()))
}

// privateMessagesReactionsUpdatesWithUsers is the transaction-safe projection
// used by DeliveryEffectsBuilder. All mutable entity lookups happen before the
// aggregate transaction; this function only consumes its snapshot arguments.
func privateMessagesReactionsUpdatesWithUsers(viewerUserID int64, peer domain.Peer, res domain.PrivateMessageReactionsResult, ids []int, users []tg.UserClass, date int) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, len(ids))
	messagesByID := make(map[int]domain.Message, len(res.Messages))
	for _, msg := range res.Messages {
		if msg.OwnerUserID == viewerUserID && msg.ID != 0 {
			messagesByID[msg.ID] = msg
		}
	}
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
			Peer: outPeer, MsgID: id, Reactions: *converted,
		})
	}
	return &tg.Updates{
		Updates: updates,
		Users:   append([]tg.UserClass(nil), users...),
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}

func (r *Router) privateReactionDeliveryEffects(ctx context.Context, requestUserID int64, usersByViewer map[int64][]tg.UserClass, date int) store.DeliveryEffectsBuilder[domain.PrivateMessageReactionsResult] {
	excludeAuthKeyID, excludeSessionID := deliveryExclusionFromContext(ctx)
	return func(res domain.PrivateMessageReactionsResult) ([]store.DeliveryEffect, error) {
		seen := make(map[int64]struct{}, len(res.Messages))
		effects := make([]store.DeliveryEffect, 0, len(res.Messages))
		for _, msg := range res.Messages {
			if msg.OwnerUserID <= 0 || msg.ID <= 0 {
				continue
			}
			if _, ok := seen[msg.OwnerUserID]; ok {
				continue
			}
			users, ok := usersByViewer[msg.OwnerUserID]
			if !ok {
				return nil, fmt.Errorf("private reaction delivery users missing for owner %d", msg.OwnerUserID)
			}
			updates := privateMessagesReactionsUpdatesWithUsers(msg.OwnerUserID, msg.Peer, res, []int{msg.ID}, users, date)
			payload, err := encodeDeliveryUpdate(updates)
			if err != nil {
				return nil, fmt.Errorf("encode private reaction delivery for owner %d: %w", msg.OwnerUserID, err)
			}
			authKeyID, sessionID := [8]byte{}, int64(0)
			if msg.OwnerUserID == requestUserID {
				authKeyID, sessionID = excludeAuthKeyID, excludeSessionID
			}
			effects = append(effects, store.AbsoluteDeliveryEffect(store.DeliveryOutboxEnqueue{
				TargetUserID: msg.OwnerUserID, ExcludeAuthKeyID: authKeyID, ExcludeSessionID: sessionID,
				Payload: payload, RecoveryPolicy: store.OutboxRecoveryAbsoluteReload,
			}))
			seen[msg.OwnerUserID] = struct{}{}
		}
		return effects, nil
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
			// top_msg_id names a forum topic. In an ordinary group a reply
			// thread is not a topic, and setting it made clients ask for
			// forum topics of a non-forum chat (CHANNEL_FORUM_MISSING).
			if topID := channelMessageThreadRootID(msg); res.Channel.Forum && topID > 0 && topID != id {
				update.SetTopMsgID(topID)
			}
			if msg.SavedPeer.ID != 0 {
				update.SetSavedPeerID(tgPeer(msg.SavedPeer))
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
