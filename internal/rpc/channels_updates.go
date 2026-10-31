package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) onUpdatesGetChannelDifference(ctx context.Context, req *tg.UpdatesGetChannelDifferenceRequest) (tg.UpdatesChannelDifferenceClass, error) {
	if r.deps.Channels == nil {
		return &tg.UpdatesChannelDifferenceEmpty{Final: true, Pts: req.Pts, Timeout: 30}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
		return nil, err
	}
	if err := r.checkFrozenChannelParticipants(ctx, userID, channelID); err != nil {
		return nil, err
	}
	// difference-class catch-up FLOOD_WAIT (design Phase 2 / §10.3): after a nudge is
	// consumed the client triggers getChannelDifference, so a large group nudge at
	// full speed needs rate limiting to prevent a storm. No limit when no threshold
	// is configured. The participant gate must run before the rate-limit write so a
	// freeze rejection has no side effects.
	if err := r.checkCatchupRateLimit(ctx, userID, channelDifferenceRateLimitKeyPrefix); err != nil {
		return nil, err
	}
	r.trackChannelInterest(ctx, userID, channelID)
	diff, err := r.deps.Channels.GetDifference(ctx, userID, domain.ChannelDifferenceRequest{
		UserID:    userID,
		ChannelID: channelID,
		Pts:       req.Pts,
		Limit:     req.Limit,
		Force:     req.Force,
	})
	if err != nil {
		if errors.Is(err, domain.ErrPersistentTimestamp) {
			r.log.Debug("channel difference cursor rejected",
				zap.Int64("viewer_user_id", userID),
				zap.Int64("channel_id", channelID),
				zap.Int("request_pts", req.Pts))
			return nil, persistentTimestampInvalidErr()
		}
		r.log.Warn("load channel difference failed",
			zap.Int64("viewer_user_id", userID),
			zap.Int64("channel_id", channelID),
			zap.Int("request_pts", req.Pts),
			zap.Int("limit", req.Limit),
			zap.Error(err))
		return nil, channelInvalidErr(err)
	}
	diff, err = r.enrichChannelDifferenceStrict(ctx, userID, diff)
	if err != nil {
		r.log.Error("project durable channel difference users",
			zap.Int64("viewer_user_id", userID),
			zap.Int64("channel_id", channelID),
			zap.Error(err))
		return nil, internalErr()
	}
	if diff.Channel.Username != "" && diff.Self.Status != domain.ChannelMemberActive {
		// Telegram's public-channel passive delivery is enabled only after a
		// successful short-poll difference. The runtime subscription is renewed
		// by subsequent polls and never creates membership/dialog/read state.
		r.refreshPublicChannelSubscription(ctx, userID, channelID)
	}
	out := r.tgChannelDifference(ctx, userID, diff)
	if linked, ok := r.linkedDiscussionChat(ctx, userID, channelID); ok {
		switch value := out.(type) {
		case *tg.UpdatesChannelDifference:
			value.Chats = replaceTGChat(value.Chats, linked)
		case *tg.UpdatesChannelDifferenceTooLong:
			value.Chats = replaceTGChat(value.Chats, linked)
		}
	}
	return out, nil
}

func (r *Router) channelOperationUpdates(ctx context.Context, viewerUserID int64, res domain.CreateChannelResult) *tg.Updates {
	return r.channelOperationUpdatesWithPeerCache(ctx, viewerUserID, res, newViewerPeerCache(r))
}

func (r *Router) channelOperationUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, res domain.CreateChannelResult, cache *viewerPeerCache) *tg.Updates {
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	userIDs := make(map[int64]struct{}, len(res.Members)+4)
	channelIDs := make(map[int64]struct{})
	if res.Channel.CreatorUserID != 0 {
		userIDs[res.Channel.CreatorUserID] = struct{}{}
	}
	collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
	collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
	for _, member := range res.Members {
		if member.UserID != 0 {
			userIDs[member.UserID] = struct{}{}
		}
		if member.InviterUserID != 0 {
			userIDs[member.InviterUserID] = struct{}{}
		}
	}
	updates := make([]tg.UpdateClass, 0, 2)
	if res.Event.Pts != 0 {
		if update := tgChannelUpdate(viewerUserID, res.Event); update != nil {
			updates = append(updates, update)
		}
	}
	if res.Channel.ID != 0 {
		updates = append(updates, &tg.UpdateChannel{ChannelID: res.Channel.ID})
	}
	chats := []tg.ChatClass{tgChannelChat(viewerUserID, res.Channel, channelMemberForUser(res.Members, viewerUserID))}
	chats = append(chats, tgChannels(viewerUserID, cache.channelsForIDs(ctx, viewerUserID, peerIDsExcept(peerIDMapKeys(channelIDs), res.Channel.ID)))...)
	return &tg.Updates{
		Updates: updates,
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, peerIDMapKeys(userIDs))),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) tdesktopCreateChatUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, res domain.CreateChannelResult, cache *viewerPeerCache) *tg.Updates {
	updates := r.channelOperationUpdatesWithPeerCache(ctx, viewerUserID, res, cache)
	if updates == nil {
		return updates
	}
	self := channelMemberForUser(res.Members, viewerUserID)
	legacy := tgMigratedLegacyChat(viewerUserID, res.Channel, self)
	if legacy == nil {
		return updates
	}
	chats := make([]tg.ChatClass, 0, len(updates.Chats)+1)
	chats = append(chats, legacy)
	chats = append(chats, updates.Chats...)
	updates.Chats = chats
	return updates
}

func (r *Router) channelStateUpdates(viewerUserID int64, channel domain.Channel) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateChannel{ChannelID: channel.ID}},
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// appendChannelStateUpdates merges extra's updates/chats into dst (chats deduped by
// id). Used to layer the status update of a related channel into one response, e.g.
// the monoforum ChannelForbidden tombstone sent together with the parent channel's
// deletion.
func appendChannelStateUpdates(dst *tg.Updates, extra *tg.Updates) {
	if dst == nil || extra == nil {
		return
	}
	dst.Updates = append(dst.Updates, extra.Updates...)
	for _, ch := range extra.Chats {
		dst.Chats = appendUniqueTGChats(dst.Chats, ch)
	}
}

// channelStateUpdatesWithLinkedMonoforum builds one recipient's channel-state
// update. Peer-wide overlays are passed in already resolved rather than read
// here: this builder runs once per online recipient, so reads inside it would
// turn one state change into one query per member.
func (r *Router) channelStateUpdatesWithLinkedMonoforum(viewerUserID int64, channel domain.Channel, mono domain.Channel, includeMono bool, botVerificationIcon int64, usernames map[domain.Peer][]domain.Username) *tg.Updates {
	updates := r.channelStateUpdates(viewerUserID, channel)
	// When the parent broadcast channel has or had a linked monoforum, project it in
	// full (non-min) form. When Direct Messages is disabled, linked_monoforum_id is
	// hidden in the projection, and only the full channel object can overwrite the
	// client cache's old linked_monoforum_id, triggering applyMonoforumLinkedId(parent,0)
	// → the monoforum's MonoforumDisabled → the "direct messages disabled" footer; a
	// min parent channel does not overwrite cached fields, so the footer never
	// appears. Aligned with the official behavior (parent projected in full + hidden
	// linked id on disable). Normal channels (no linked monoforum) still use min and
	// are unaffected.
	if channel.LinkedMonoforumID != 0 {
		updates.Chats = []tg.ChatClass{tgChannelChat(viewerUserID, channel, nil)}
	}
	if includeMono {
		updates.Chats = appendUniqueTGChats(updates.Chats, tgChannelChat(viewerUserID, mono, nil))
	}
	// The third-party mark travels with the state mutation for the same reason
	// verified:flags.7 does -- it is part of how the peer identifies itself -- but it
	// lives in a read model rather than on domain.Channel, so it is stamped on here
	// instead of being projected from the row.
	applyBotVerificationIconToChannelChats(updates.Chats, channel.ID, botVerificationIcon)
	applyUsernamesFromRegistry(nil, updates.Chats, usernames)
	return updates
}

func (r *Router) linkedMonoforumForChannelState(ctx context.Context, userID int64, channel domain.Channel) (domain.Channel, bool) {
	if r.deps.Channels == nil || userID == 0 || !channel.BroadcastMessagesAllowed || channel.LinkedMonoforumID == 0 {
		return domain.Channel{}, false
	}
	mono, err := r.deps.Channels.GetJoinableChannel(ctx, userID, channel.LinkedMonoforumID)
	if err != nil || !mono.Monoforum || mono.LinkedMonoforumID != channel.ID {
		return domain.Channel{}, false
	}
	return mono, true
}

func (r *Router) channelMessageUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, res domain.SendChannelMessageResult, randomID int64, cache *viewerPeerCache) *tg.Updates {
	updates := r.channelMessageUpdatesWithPeerCacheAndUsernames(ctx, viewerUserID, res, randomID, cache, nil)
	if updates != nil {
		r.applyUsernamesToPeerObjects(ctx, updates.Users, updates.Chats)
	}
	return updates
}

func (r *Router) channelMessageUpdatesWithPeerCacheAndUsernames(ctx context.Context, viewerUserID int64, res domain.SendChannelMessageResult, randomID int64, cache *viewerPeerCache, usernames map[domain.Peer][]domain.Username) *tg.Updates {
	randomIDs := []int64(nil)
	includeMessageIDs := randomID != 0
	if includeMessageIDs {
		randomIDs = []int64{randomID}
	}
	return r.channelMessagesUpdatesWithPeerCacheAndUsernames(ctx, viewerUserID, []domain.SendChannelMessageResult{res}, randomIDs, includeMessageIDs, nil, cache, usernames)
}

func (r *Router) pushChannelDiscussionUpdate(ctx context.Context, originUserID int64, discussion *domain.SendChannelDiscussionResult) {
	if discussion == nil || discussion.Channel.ID == 0 || discussion.Event.Pts == 0 {
		return
	}
	res := domain.SendChannelMessageResult{
		Channel:        discussion.Channel,
		Message:        discussion.Message,
		Event:          discussion.Event,
		Recipients:     discussion.Recipients,
		MentionUserIDs: discussion.MentionUserIDs,
	}
	// The second-round fan-out of the discussion-group wiring (broadcast↔linked
	// megagroup) is also made asynchronous + cross-viewer projection warm-up (design
	// Phase 0/Phase 1). The cache is used only by this fan-out closure/warm-up and is
	// run serially by a single-shard worker, so there is no cross-goroutine race.
	r.enqueueChannelMessageFanout(ctx, originUserID, res, nil)
}

func (r *Router) channelMessagesUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, results []domain.SendChannelMessageResult, randomIDs []int64, includeMessageIDs bool, extraUserIDs []int64, cache *viewerPeerCache) *tg.Updates {
	updates := r.channelMessagesUpdatesWithPeerCacheAndUsernames(ctx, viewerUserID, results, randomIDs, includeMessageIDs, extraUserIDs, cache, nil)
	if updates != nil {
		r.applyUsernamesToPeerObjects(ctx, updates.Users, updates.Chats)
	}
	return updates
}

func (r *Router) channelMessagesUpdatesWithPeerCacheAndUsernames(ctx context.Context, viewerUserID int64, results []domain.SendChannelMessageResult, randomIDs []int64, includeMessageIDs bool, extraUserIDs []int64, cache *viewerPeerCache, usernames map[domain.Peer][]domain.Username) *tg.Updates {
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	updates := make([]tg.UpdateClass, 0, len(results)*2)
	userIDs := make(map[int64]struct{}, len(results)+len(extraUserIDs))
	for _, id := range extraUserIDs {
		if id != 0 {
			userIDs[id] = struct{}{}
		}
	}
	channelIDs := make(map[int64]struct{}, len(results))
	var channel domain.Channel
	date := 0
	for i, res := range results {
		if res.Channel.ID != 0 {
			channel = res.Channel
		}
		if includeMessageIDs && res.Message.ID != 0 && i < len(randomIDs) && randomIDs[i] != 0 {
			updates = append(updates, &tg.UpdateMessageID{ID: res.Message.ID, RandomID: randomIDs[i]})
		}
		if res.Event.Pts != 0 {
			event := projectChannelMentionForViewer(res.Event, res.MentionUserIDs, viewerUserID)
			if update := tgChannelUpdate(viewerUserID, event); update != nil {
				updates = append(updates, update)
			}
		}
		if res.Duplicate && res.ReplayDeleteEvent != nil {
			if update := tgChannelUpdate(viewerUserID, *res.ReplayDeleteEvent); update != nil {
				updates = append(updates, update)
			}
			if res.ReplayDeleteEvent.Date > date {
				date = res.ReplayDeleteEvent.Date
			}
		}
		collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
		if date == 0 {
			date = res.Event.Date
		}
		if date == 0 {
			date = res.Message.Date
		}
	}
	chats := []tg.ChatClass(nil)
	if channel.ID != 0 {
		chats = []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)}
		chats = r.appendLinkedDiscussionChat(ctx, viewerUserID, channel.ID, chats)
	}
	chats = append(chats, tgChannels(viewerUserID, cache.channelsForIDs(ctx, viewerUserID, peerIDsExcept(peerIDMapKeys(channelIDs), channel.ID)))...)
	if date == 0 {
		date = int(r.clock.Now().Unix())
	}
	out := &tg.Updates{
		Updates: updates,
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, peerIDMapKeys(userIDs))),
		Chats:   chats,
		Date:    date,
		Seq:     0,
	}
	applyUsernamesFromRegistry(out.Users, out.Chats, usernames)
	return out
}

func (r *Router) channelEditMessageUpdates(ctx context.Context, viewerUserID int64, res domain.EditChannelMessageResult) *tg.Updates {
	return r.channelEditMessageUpdatesWithPeerCache(ctx, viewerUserID, res, newViewerPeerCache(r))
}

func (r *Router) channelEditMessageUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, res domain.EditChannelMessageResult, cache *viewerPeerCache) *tg.Updates {
	updates := r.channelEditMessageUpdatesWithPeerCacheAndUsernames(ctx, viewerUserID, res, cache, nil)
	if updates != nil {
		r.applyUsernamesToPeerObjects(ctx, updates.Users, updates.Chats)
	}
	return updates
}

func (r *Router) channelEditMessageUpdatesWithPeerCacheAndUsernames(ctx context.Context, viewerUserID int64, res domain.EditChannelMessageResult, cache *viewerPeerCache, usernames map[domain.Peer][]domain.Username) *tg.Updates {
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	updates := make([]tg.UpdateClass, 0, 2)
	userIDs := make(map[int64]struct{}, 2)
	channelIDs := make(map[int64]struct{})
	if res.Event.Pts != 0 {
		if update := tgChannelUpdate(viewerUserID, res.Event); update != nil {
			updates = append(updates, update)
		}
		collectChannelUpdatePeerRefs(res.Event, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.Message, res.Channel.ID, userIDs, channelIDs)
	}
	if res.ServiceEvent.Pts != 0 {
		if update := tgChannelUpdate(viewerUserID, res.ServiceEvent); update != nil {
			updates = append(updates, update)
		}
		collectChannelUpdatePeerRefs(res.ServiceEvent, res.Channel.ID, userIDs, channelIDs)
		collectChannelMessagePeerRefs(res.ServiceMessage, res.Channel.ID, userIDs, channelIDs)
	}
	chats := []tg.ChatClass{tgChannelChatMin(viewerUserID, res.Channel)}
	chats = append(chats, tgChannels(viewerUserID, cache.channelsForIDs(ctx, viewerUserID, peerIDsExcept(peerIDMapKeys(channelIDs), res.Channel.ID)))...)
	out := &tg.Updates{
		Updates: updates,
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, peerIDMapKeys(userIDs))),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
	applyUsernamesFromRegistry(out.Users, out.Chats, usernames)
	return out
}

func (r *Router) channelDeleteMessagesUpdates(viewerUserID int64, channel domain.Channel, event domain.ChannelUpdateEvent) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, 1)
	if update := tgChannelUpdate(viewerUserID, event); update != nil {
		updates = append(updates, update)
	}
	return &tg.Updates{
		Updates: updates,
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) channelAvailableMessagesUpdates(viewerUserID int64, channel domain.Channel, availableMinID int) *tg.Updates {
	updates := make([]tg.UpdateClass, 0, 1)
	if channel.ID != 0 && availableMinID > 0 {
		updates = append(updates, &tg.UpdateChannelAvailableMessages{
			ChannelID:      channel.ID,
			AvailableMinID: availableMinID,
		})
	}
	return &tg.Updates{
		Updates: updates,
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// projectChannelMentionForViewer projects the global message snapshot into a
// viewer-specific mentioned/media_unread: a mentioned online member must see the @
// badge in the live push, not only after an offline difference or a session restart.
func projectChannelMentionForViewer(event domain.ChannelUpdateEvent, mentionUserIDs []int64, viewerUserID int64) domain.ChannelUpdateEvent {
	if viewerUserID == 0 || event.Message.ID == 0 || viewerUserID == event.Message.SenderUserID {
		return event
	}
	for _, id := range mentionUserIDs {
		if id != viewerUserID {
			continue
		}
		// The client's unread-mention check requires mentioned and media_unread to be
		// set together, regardless of whether the message carries media.
		event.Message.Mentioned = true
		event.Message.MediaUnread = true
		return event
	}
	return event
}

type channelUpdatesBuilder func(viewerUserID int64) *tg.Updates

func (r *Router) pushChannelReadOutboxUpdates(ctx context.Context, channelID int64, updates []domain.ChannelReadOutboxUpdate) {
	if r.deps.Sessions == nil || channelID == 0 || len(updates) == 0 {
		return
	}
	seen := make(map[int64]int, len(updates))
	for _, update := range updates {
		if update.UserID == 0 || update.MaxID <= 0 {
			continue
		}
		if seen[update.UserID] < update.MaxID {
			seen[update.UserID] = update.MaxID
		}
	}
	date := int(r.clock.Now().Unix())
	for userID, maxID := range seen {
		r.pushUserUpdates(ctx, userID, &tg.Updates{
			Updates: []tg.UpdateClass{&tg.UpdateReadChannelOutbox{ChannelID: channelID, MaxID: maxID}},
			Date:    date,
			Seq:     0,
		})
	}
}

func (r *Router) pushChannelUpdates(ctx context.Context, originUserID, channelID int64, recipients []int64, build channelUpdatesBuilder) {
	r.pushChannelUpdatesWithScope(ctx, channelFanoutMembers, originUserID, channelID, recipients, build)
}

func (r *Router) pushChannelViewerUpdates(ctx context.Context, originUserID, channelID int64, recipients []int64, build channelUpdatesBuilder) {
	r.pushChannelUpdatesWithScope(ctx, channelFanoutViewers, originUserID, channelID, recipients, build)
}

func (r *Router) pushChannelExplicitUpdates(ctx context.Context, originUserID, channelID int64, recipients []int64, build channelUpdatesBuilder) {
	r.pushChannelUpdatesWithScope(ctx, channelFanoutExplicit, originUserID, channelID, recipients, build)
}

func (r *Router) pushChannelUpdatesWithScope(ctx context.Context, scope channelFanoutScope, originUserID, channelID int64, recipients []int64, build channelUpdatesBuilder) {
	if r.deps.Sessions == nil || build == nil {
		return
	}
	explicit := recipients
	recipients = r.channelFanoutRecipients(ctx, scope, channelID, recipients)
	r.log.Debug("push channel updates fanout",
		zap.Int64("channel_id", channelID),
		zap.Int("scope", int(scope)),
		zap.Int64("origin_user_id", originUserID),
		zap.Int64s("explicit", explicit),
		zap.Int64s("recipients", recipients),
	)
	seen := make(map[int64]struct{}, len(recipients))
	pushed := false
	for _, userID := range recipients {
		if userID == 0 {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		updates := build(userID)
		if updates == nil {
			continue
		}
		r.pushUserUpdates(ctx, userID, updates)
		pushed = true
	}
	if !pushed && originUserID != 0 {
		updates := build(originUserID)
		if updates == nil {
			return
		}
		r.pushUserUpdates(ctx, originUserID, updates)
	}
}

func tgEmptyUpdates(date int) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{},
		Users:   []tg.UserClass{},
		Chats:   []tg.ChatClass{},
		Date:    date,
		Seq:     0,
	}
}
