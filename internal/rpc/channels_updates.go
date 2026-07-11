package rpc

import (
	"context"
	"errors"

	"github.com/gotd/td/tg"
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
	// difference 类 catch-up FLOOD_WAIT（设计 Phase 2 / §10.3）：nudge 被消费后客户端会触发
	// getChannelDifference，大群 nudge 全速前需限速防风暴。未配置阈值时不限速。
	if err := r.checkCatchupRateLimit(ctx, userID, channelDifferenceRateLimitKeyPrefix); err != nil {
		return nil, err
	}
	channelID, err := r.channelIDFromInput(ctx, userID, req.Channel)
	if err != nil {
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
			return nil, persistentTimestampInvalidErr()
		}
		return nil, channelInvalidErr(err)
	}
	diff = r.enrichChannelDifference(ctx, userID, diff)
	out := tgChannelDifference(userID, diff)
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

// appendChannelStateUpdates 把 extra 的 update/chat 合并进 dst(chats 按 id 去重)。用于在一次响应里
// 叠加关联频道的状态更新,例如随母频道删除一并下发的 monoforum ChannelForbidden 墓碑。
func appendChannelStateUpdates(dst *tg.Updates, extra *tg.Updates) {
	if dst == nil || extra == nil {
		return
	}
	dst.Updates = append(dst.Updates, extra.Updates...)
	for _, ch := range extra.Chats {
		dst.Chats = appendUniqueTGChats(dst.Chats, ch)
	}
}

func (r *Router) channelStateUpdatesWithLinkedMonoforum(viewerUserID int64, channel domain.Channel, mono domain.Channel, includeMono bool) *tg.Updates {
	updates := r.channelStateUpdates(viewerUserID, channel)
	// 母广播频道有/曾有关联 monoforum 时,按完整(非 min)形态下发。关闭 Direct Messages 时
	// linked_monoforum_id 在投影里被隐藏,只有完整频道对象才能覆盖客户端缓存里旧的 linked_monoforum_id,
	// 触发 applyMonoforumLinkedId(parent,0) → monoforum 的 MonoforumDisabled → 「已停用私信」停用页脚;
	// min 母频道不覆盖缓存字段,页脚出不来。与官方 disable(parent 完整下发 + 隐藏 linked id)一致。
	// 普通频道(无 linked monoforum)仍走 min,不受影响。
	if channel.LinkedMonoforumID != 0 {
		updates.Chats = []tg.ChatClass{tgChannelChat(viewerUserID, channel, nil)}
	}
	if includeMono {
		updates.Chats = appendUniqueTGChats(updates.Chats, tgChannelChat(viewerUserID, mono, nil))
	}
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
	randomIDs := []int64(nil)
	includeMessageIDs := randomID != 0
	if includeMessageIDs {
		randomIDs = []int64{randomID}
	}
	return r.channelMessagesUpdatesWithPeerCache(ctx, viewerUserID, []domain.SendChannelMessageResult{res}, randomIDs, includeMessageIDs, nil, cache)
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
	// 讨论组联动（broadcast↔linked megagroup）的第二轮 fan-out 也异步化 + 跨 viewer 投影预热
	// （设计 Phase 0/Phase 1）。cache 仅被本 fan-out 闭包/预热使用、由单分片 worker 串行执行，
	// 无跨 goroutine 竞态。
	r.enqueueChannelMessageFanout(ctx, originUserID, res, nil)
}

func (r *Router) channelMessagesUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, results []domain.SendChannelMessageResult, randomIDs []int64, includeMessageIDs bool, extraUserIDs []int64, cache *viewerPeerCache) *tg.Updates {
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
	return &tg.Updates{
		Updates: updates,
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, peerIDMapKeys(userIDs))),
		Chats:   chats,
		Date:    date,
		Seq:     0,
	}
}

func (r *Router) channelEditMessageUpdates(ctx context.Context, viewerUserID int64, res domain.EditChannelMessageResult) *tg.Updates {
	return r.channelEditMessageUpdatesWithPeerCache(ctx, viewerUserID, res, newViewerPeerCache(r))
}

func (r *Router) channelEditMessageUpdatesWithPeerCache(ctx context.Context, viewerUserID int64, res domain.EditChannelMessageResult, cache *viewerPeerCache) *tg.Updates {
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
	return &tg.Updates{
		Updates: updates,
		Users:   tgUsersForViewer(viewerUserID, cache.usersForIDs(ctx, viewerUserID, peerIDMapKeys(userIDs))),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
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

// projectChannelMentionForViewer 把全局消息快照按接收者投影 viewer-specific
// 的 mentioned/media_unread：被 @ 的在线成员必须在实时推送里就看到 @ 角标，
// 不能等到离线差量或重开会话才出现。
func projectChannelMentionForViewer(event domain.ChannelUpdateEvent, mentionUserIDs []int64, viewerUserID int64) domain.ChannelUpdateEvent {
	if viewerUserID == 0 || event.Message.ID == 0 || viewerUserID == event.Message.SenderUserID {
		return event
	}
	for _, id := range mentionUserIDs {
		if id != viewerUserID {
			continue
		}
		// 客户端的未读提及判定要求 mentioned 与 media_unread 同时置位，
		// 与消息是否含媒体无关。
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
