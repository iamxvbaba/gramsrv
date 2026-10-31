package rpc

import (
	"context"
	"sync"

	"github.com/iamxvbaba/td/tg"
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
	// Official semantics (reactions_user_max_default/premium): the tail of the
	// vector is the newest choice, and exceeding the per-user limit drops the old
	// ones rather than erroring; a premium viewer uses the premium tier (appConfig
	// reactions_user_max_premium=3), otherwise the multi-reaction the client allows
	// would be silently trimmed.
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
		// The official server guarantees the message author receives
		// updateMessageReactions: the author goes into the explicit recipients and
		// does not rely on online viewer sampling (after the fan-out cap the author
		// could be pushed out of the sample set).
		ids := []int{res.Message.ID}
		r.pushChannelViewerUpdates(ctx, userID, res.Channel.ID, []int64{userID, res.Message.SenderUserID}, func(viewerUserID int64) *tg.Updates {
			return r.channelReactionsViewerUpdates(ctx, userID, viewerUserID, res, ids)
		})
		return updates, nil
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
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
		// Saved Messages reactions are private tags, not ordinary reaction usage.
		if peer.ID != userID {
			if err := r.recordMessageReactionUse(ctx, userID, reactions, req.GetAddToRecent(), date); err != nil {
				return nil, internalErr()
			}
		}
		updates := r.privateMessageReactionsUpdates(ctx, userID, peer, res)
		r.pushUserUpdates(ctx, userID, updates)
		for _, msg := range res.Messages {
			if msg.OwnerUserID == 0 || msg.OwnerUserID == userID {
				continue
			}
			viewerPeer := msg.Peer
			viewerUpdates := r.privateMessageReactionsUpdates(ctx, msg.OwnerUserID, viewerPeer, res)
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

func (r *Router) channelMessageReactionsUpdates(ctx context.Context, viewerUserID int64, res domain.ChannelMessageReactionsResult) *tg.Updates {
	ids := []int{res.Message.ID}
	if res.Message.ID <= 0 && len(res.Messages) > 0 {
		ids = []int{res.Messages[0].ID}
	}
	return r.channelMessagesReactionsUpdates(ctx, viewerUserID, res, ids)
}

// channelReactionsViewerUpdates builds a viewer's updateMessageReactions for
// fan-out. The aggregate inside res is the requester's view (chosen/My/unread are
// all per-viewer fields):
//   - the requester itself: use it directly;
//   - the message author: reload from the author's view (the unread badge and the
//     author's own chosen must be correct);
//   - other viewers: official min semantics -- send only the counts and the
//     recent list, and let the client keep its local chosen (a non-min TDesktop
//     update overwrites the local my state with chosen_order directly; view
//     leakage means "someone else's reaction shows as mine").
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

// reactionsResultSenderID gets the message author from the result (single-message case; multi-message moderation has no author special-case).
func reactionsResultSenderID(res domain.ChannelMessageReactionsResult) int64 {
	if res.Message.ID != 0 {
		return res.Message.SenderUserID
	}
	if len(res.Messages) == 1 {
		return res.Messages[0].SenderUserID
	}
	return 0
}

// minifyChannelReactionsResult deep-copies and clears all per-viewer fields
// (chosen/My/unread) for min pushes; it does not modify the original res (the
// requester's response still uses the full view).
func minifyChannelReactionsResult(res domain.ChannelMessageReactionsResult) domain.ChannelMessageReactionsResult {
	scrub := func(in *domain.ChannelMessageReactions) *domain.ChannelMessageReactions {
		if in == nil {
			return nil
		}
		out := domain.ChannelMessageReactions{
			CanSeeList: in.CanSeeList,
			AsTags:     in.AsTags,
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
