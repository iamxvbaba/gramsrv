package rpc

import (
	"context"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) enrichUpdateEvents(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent) []domain.UpdateEvent {
	if len(events) == 0 {
		return events
	}
	return r.enrichUpdateEventsWithPeerCache(ctx, viewerUserID, events, newViewerPeerCache(r))
}

func (r *Router) enrichUpdateEventsWithPeerCache(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent, cache *viewerPeerCache) []domain.UpdateEvent {
	if len(events) == 0 {
		return events
	}
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	out := append([]domain.UpdateEvent(nil), events...)
	refs := make([]updateEventPeerRefs, len(out))
	allUserIDs := make(map[int64]struct{})
	allChannelIDs := make(map[int64]struct{})
	for i := range out {
		if out[i].Type == domain.UpdateEventChannelState {
			if service, ok := r.deps.Channels.(ChannelAuthoritativeProjectionService); ok {
				views, err := service.GetChannelsAuthoritative(ctx, viewerUserID, []int64{out[i].Peer.ID})
				if err != nil {
					r.log.Warn("reload authoritative channel state event",
						zap.Int64("viewer_user_id", viewerUserID),
						zap.Int64("channel_id", out[i].Peer.ID),
						zap.Error(err))
				} else {
					out[i].Channels = out[i].Channels[:0]
					for _, view := range views {
						if view.Channel.ID != 0 {
							out[i].Channels = append(out[i].Channels, view.Channel)
						}
					}
				}
			}
		}
		if out[i].Type == domain.UpdateEventMessageReactions {
			out[i] = r.enrichMessageReactionEvent(ctx, viewerUserID, out[i])
		}
		if out[i].Type == domain.UpdateEventMessagePoll {
			out[i] = r.enrichMessagePollEvent(ctx, viewerUserID, out[i])
		}
		if out[i].Type == domain.UpdateEventDraftMessage {
			out[i] = r.enrichDraftMessageEvent(ctx, viewerUserID, out[i])
		}
		userIDs := make(map[int64]struct{})
		channelIDs := make(map[int64]struct{})
		addDomainPeerRef(out[i].Peer, 0, userIDs, channelIDs)
		for _, peer := range out[i].Peers {
			addDomainPeerRef(peer, 0, userIDs, channelIDs)
		}
		addDomainPeerRef(out[i].Story.Owner, 0, userIDs, channelIDs)
		for _, peer := range storyForwardPeers(out[i].Story) {
			addDomainPeerRef(peer, 0, userIDs, channelIDs)
		}
		collectMessagePeerRefs(out[i].Message, 0, userIDs, channelIDs)
		if message := out[i].EphemeralMessage; message != nil {
			collectEphemeralMessagePeerRefs(*message, userIDs, channelIDs)
			if message.BotAPIReply != nil {
				collectEphemeralMessagePeerRefs(*message.BotAPIReply, userIDs, channelIDs)
			}
		}
		if out[i].BotCallbackQuery != nil && out[i].BotCallbackQuery.UserID != 0 {
			userIDs[out[i].BotCallbackQuery.UserID] = struct{}{}
		}
		removeKnownChannelRefs(channelIDs, out[i].Channels)
		refs[i] = updateEventPeerRefs{userIDs: userIDs, channelIDs: channelIDs}
		for id := range userIDs {
			allUserIDs[id] = struct{}{}
		}
		for id := range channelIDs {
			allChannelIDs[id] = struct{}{}
		}
	}
	cache.usersForIDs(ctx, viewerUserID, mapKeys(allUserIDs))
	cache.channelsForIDs(ctx, viewerUserID, mapKeys(allChannelIDs))
	for i := range out {
		out[i].Users = r.withUsersPresence(mergeDomainUsers(out[i].Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(refs[i].userIDs))...))
		out[i].Channels = mergeDomainChannels(out[i].Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(refs[i].channelIDs))...)
	}
	return out
}

func collectEphemeralMessagePeerRefs(message domain.EphemeralMessage, userIDs, channelIDs map[int64]struct{}) {
	if message.SenderUserID != 0 {
		userIDs[message.SenderUserID] = struct{}{}
	}
	if message.ReceiverUserID != 0 {
		userIDs[message.ReceiverUserID] = struct{}{}
	}
	addDomainPeerRef(message.Peer, 0, userIDs, channelIDs)
	for _, entity := range message.Content.Entities {
		if entity.UserID != 0 {
			userIDs[entity.UserID] = struct{}{}
		}
	}
	if message.Content.Media != nil && message.Content.Media.Contact != nil && message.Content.Media.Contact.UserID != 0 {
		userIDs[message.Content.Media.Contact.UserID] = struct{}{}
	}
}

type updateEventPeerRefs struct {
	userIDs    map[int64]struct{}
	channelIDs map[int64]struct{}
}

func (r *Router) enrichMessageReactionEvent(ctx context.Context, viewerUserID int64, event domain.UpdateEvent) domain.UpdateEvent {
	if r.deps.Messages == nil || event.Message.ID <= 0 {
		return event
	}
	peer := event.Message.Peer
	if peer.Type == "" || peer.ID == 0 {
		peer = event.Peer
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return event
	}
	res, err := r.deps.Messages.GetMessageReactions(ctx, viewerUserID, domain.PrivateMessageReactionsRequest{
		OwnerUserID: viewerUserID,
		Peer:        peer,
		IDs:         []int{event.Message.ID},
	})
	if err != nil {
		return event
	}
	for _, msg := range res.Messages {
		if msg.OwnerUserID == viewerUserID && msg.ID == event.Message.ID {
			msg.Pts = event.Pts
			event.Message = msg
			event.Peer = msg.Peer
			return event
		}
	}
	return event
}

// enrichMessagePollEvent 在 difference 重放时按 viewer 重载消息（media 含最新 poll 权威态与
// viewer 门控），与 reaction 事件 enrich 同构。
func (r *Router) enrichMessagePollEvent(ctx context.Context, viewerUserID int64, event domain.UpdateEvent) domain.UpdateEvent {
	if r.deps.Messages == nil || event.Message.ID <= 0 {
		return event
	}
	peer := event.Message.Peer
	if peer.Type == "" || peer.ID == 0 {
		peer = event.Peer
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return event
	}
	list, err := r.deps.Messages.GetMessages(ctx, viewerUserID, []int{event.Message.ID})
	if err != nil {
		return event
	}
	for _, msg := range list.Messages {
		if msg.OwnerUserID == viewerUserID && msg.ID == event.Message.ID {
			msg.Pts = event.Pts
			event.Message = msg
			event.Peer = msg.Peer
			return event
		}
	}
	return event
}

// enrichDraftMessageEvent 重放 draft_message 事件时按当前权威态填充草稿内容：
// 草稿是绝对状态，事件行不固化快照；草稿已删（或读取失败）时 Draft 置 nil → 下发 empty。
func (r *Router) enrichDraftMessageEvent(ctx context.Context, viewerUserID int64, event domain.UpdateEvent) domain.UpdateEvent {
	event.Draft = nil
	if r.deps.Dialogs == nil || event.Peer.ID == 0 {
		return event
	}
	draft, found, err := r.deps.Dialogs.GetDraft(ctx, viewerUserID, event.Peer, event.MaxID)
	if err != nil || !found {
		return event
	}
	event.Draft = &draft
	return event
}

func (r *Router) enrichChannelDifference(ctx context.Context, viewerUserID int64, diff domain.ChannelDifference) domain.ChannelDifference {
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, event := range diff.Events {
		collectChannelUpdatePeerRefs(event, diff.Channel.ID, userIDs, channelIDs)
	}
	for _, msg := range diff.NewMessages {
		collectChannelMessagePeerRefs(msg, diff.Channel.ID, userIDs, channelIDs)
	}
	for _, event := range diff.OtherUpdates {
		collectChannelUpdatePeerRefs(event, diff.Channel.ID, userIDs, channelIDs)
	}
	removeKnownChannelRefs(channelIDs, diff.Channels)
	cache := newViewerPeerCache(r)
	diff.Users = r.withUsersPresence(mergeDomainUsers(diff.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	diff.Channels = mergeDomainChannels(diff.Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))...)
	return diff
}

func (r *Router) enrichChannelHistory(ctx context.Context, viewerUserID int64, history domain.ChannelHistory) domain.ChannelHistory {
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, msg := range history.Messages {
		collectChannelMessagePeerRefs(msg, history.Channel.ID, userIDs, channelIDs)
	}
	for _, topic := range history.Topics {
		if topic.CreatorUserID != 0 {
			userIDs[topic.CreatorUserID] = struct{}{}
		}
	}
	removeKnownChannelRefs(channelIDs, history.Channels)
	cache := newViewerPeerCache(r)
	history.Users = r.withUsersPresence(mergeDomainUsers(history.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	history.Channels = mergeDomainChannels(history.Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))...)
	return history
}

func (r *Router) enrichChannelDiscussion(ctx context.Context, viewerUserID int64, discussion domain.ChannelDiscussionMessage) domain.ChannelDiscussionMessage {
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, msg := range discussion.Messages {
		collectChannelMessagePeerRefs(msg, discussion.DiscussionChannel.ID, userIDs, channelIDs)
	}
	// Post/Discussion channel 已由转换层单独下发，避免重复进 chats。
	delete(channelIDs, discussion.PostChannel.ID)
	delete(channelIDs, discussion.DiscussionChannel.ID)
	removeKnownChannelRefs(channelIDs, discussion.Channels)
	cache := newViewerPeerCache(r)
	discussion.Users = r.withUsersPresence(mergeDomainUsers(discussion.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	discussion.Channels = mergeDomainChannels(discussion.Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))...)
	return discussion
}

func (r *Router) enrichMessageList(ctx context.Context, viewerUserID int64, list domain.MessageList) domain.MessageList {
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, msg := range list.Messages {
		collectMessagePeerRefs(msg, 0, userIDs, channelIDs)
	}
	cache := newViewerPeerCache(r)
	list.Users = r.withUsersPresence(mergeDomainUsers(list.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	return list
}

func collectMessagePeerRefs(msg domain.Message, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	addDomainPeerRef(msg.From, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(msg.Peer, currentChannelID, userIDs, channelIDs)
	if msg.Forward != nil {
		addDomainPeerRef(msg.Forward.From, currentChannelID, userIDs, channelIDs)
	}
	if msg.ViaBotID != 0 {
		userIDs[msg.ViaBotID] = struct{}{}
	}
	if msg.ReplyTo != nil {
		addDomainPeerRef(msg.ReplyTo.Peer, currentChannelID, userIDs, channelIDs)
	}
	if msg.Media != nil && msg.Media.Contact != nil && msg.Media.Contact.UserID != 0 {
		userIDs[msg.Media.Contact.UserID] = struct{}{}
	}
	if msg.Media != nil && msg.Media.ServiceAction != nil && msg.Media.ServiceAction.RequestedPeer != nil {
		for _, peer := range msg.Media.ServiceAction.RequestedPeer.Peers {
			addDomainPeerRef(peer, currentChannelID, userIDs, channelIDs)
		}
	}
	collectPollMediaUserRefs(msg.Media, userIDs)
	collectTodoMediaUserRefs(msg.Media, userIDs)
	if msg.Reactions != nil {
		for _, reaction := range msg.Reactions.Recent {
			if reaction.UserID != 0 {
				userIDs[reaction.UserID] = struct{}{}
			}
		}
	}
}

// collectPollMediaUserRefs 收集 poll recent voters（公开投票头像渲染需要 user 实体）。
func collectPollMediaUserRefs(media *domain.MessageMedia, userIDs map[int64]struct{}) {
	if media == nil || media.Poll == nil || media.Poll.Results == nil {
		return
	}
	for _, id := range media.Poll.Results.RecentVoters {
		if id != 0 {
			userIDs[id] = struct{}{}
		}
	}
}

func collectTodoMediaUserRefs(media *domain.MessageMedia, userIDs map[int64]struct{}) {
	if media == nil || media.Todo == nil {
		return
	}
	for _, completion := range media.Todo.Completions {
		if completion.CompletedBy != 0 {
			userIDs[completion.CompletedBy] = struct{}{}
		}
	}
}

func collectChannelUpdatePeerRefs(event domain.ChannelUpdateEvent, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	if event.SenderUserID != 0 {
		userIDs[event.SenderUserID] = struct{}{}
	}
	for _, id := range event.UserIDs {
		if id != 0 {
			userIDs[id] = struct{}{}
		}
	}
	for _, member := range []domain.ChannelMember{event.Previous, event.Participant} {
		if member.UserID != 0 {
			userIDs[member.UserID] = struct{}{}
		}
		if member.InviterUserID != 0 {
			userIDs[member.InviterUserID] = struct{}{}
		}
	}
	collectChannelMessagePeerRefs(event.Message, currentChannelID, userIDs, channelIDs)
}

func collectChannelMessagePeerRefs(msg domain.ChannelMessage, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	if msg.SenderUserID != 0 {
		userIDs[msg.SenderUserID] = struct{}{}
	}
	addDomainPeerRef(msg.From, currentChannelID, userIDs, channelIDs)
	if msg.SendAs != nil {
		addDomainPeerRef(*msg.SendAs, currentChannelID, userIDs, channelIDs)
	}
	if msg.Forward != nil {
		addDomainPeerRef(msg.Forward.From, currentChannelID, userIDs, channelIDs)
	}
	if msg.ViaBotID != 0 {
		userIDs[msg.ViaBotID] = struct{}{}
	}
	if msg.ReplyTo != nil {
		addDomainPeerRef(msg.ReplyTo.Peer, currentChannelID, userIDs, channelIDs)
	}
	if msg.Media != nil && msg.Media.Contact != nil && msg.Media.Contact.UserID != 0 {
		userIDs[msg.Media.Contact.UserID] = struct{}{}
	}
	collectPollMediaUserRefs(msg.Media, userIDs)
	collectTodoMediaUserRefs(msg.Media, userIDs)
	if msg.Action != nil {
		for _, id := range msg.Action.UserIDs {
			if id != 0 {
				userIDs[id] = struct{}{}
			}
		}
		if msg.Action.StarGift != nil {
			if id := msg.Action.StarGift.FromUserID; id != 0 && !msg.Action.StarGift.NameHidden {
				userIDs[id] = struct{}{}
			}
			if id := msg.Action.StarGift.PeerUserID; id != 0 {
				userIDs[id] = struct{}{}
			}
			if id := msg.Action.StarGift.PeerChannelID; id != 0 && id != currentChannelID {
				channelIDs[id] = struct{}{}
			}
		}
	}
	if msg.Reactions != nil {
		for _, reaction := range msg.Reactions.Recent {
			if reaction.UserID != 0 {
				userIDs[reaction.UserID] = struct{}{}
			}
		}
	}
}

func addDomainPeerRef(peer domain.Peer, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	switch peer.Type {
	case domain.PeerTypeUser:
		if peer.ID != 0 {
			userIDs[peer.ID] = struct{}{}
		}
	case domain.PeerTypeChannel:
		if peer.ID != 0 && peer.ID != currentChannelID {
			channelIDs[peer.ID] = struct{}{}
		}
	}
}

func removeKnownChannelRefs(channelIDs map[int64]struct{}, channels []domain.Channel) {
	if len(channelIDs) == 0 || len(channels) == 0 {
		return
	}
	for _, ch := range channels {
		if ch.ID != 0 {
			delete(channelIDs, ch.ID)
		}
	}
}

func (r *Router) domainUsersForIDs(ctx context.Context, currentUserID int64, ids []int64) []domain.User {
	if len(ids) == 0 {
		return nil
	}
	return newViewerPeerCache(r).usersForIDs(ctx, currentUserID, ids)
}

func mergeDomainUsers(base []domain.User, extra ...domain.User) []domain.User {
	out := make([]domain.User, 0, len(base)+len(extra))
	index := make(map[int64]int, len(base)+len(extra))
	appendOrReplace := func(u domain.User, replace bool) {
		if u.ID == 0 {
			if !replace {
				out = append(out, u)
			}
			return
		}
		if i, ok := index[u.ID]; ok {
			if replace {
				out[i] = u
			}
			return
		}
		index[u.ID] = len(out)
		out = append(out, u)
	}
	for _, u := range base {
		appendOrReplace(u, false)
	}
	for _, u := range extra {
		appendOrReplace(u, true)
	}
	return out
}

func mergeDomainChannels(base []domain.Channel, extra ...domain.Channel) []domain.Channel {
	out := append([]domain.Channel(nil), base...)
	seen := make(map[int64]struct{}, len(out)+len(extra))
	for _, ch := range out {
		if ch.ID != 0 {
			seen[ch.ID] = struct{}{}
		}
	}
	for _, ch := range extra {
		if ch.ID == 0 {
			continue
		}
		if _, ok := seen[ch.ID]; ok {
			continue
		}
		seen[ch.ID] = struct{}{}
		out = append(out, ch)
	}
	return out
}

func mapKeys(items map[int64]struct{}) []int64 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int64, 0, len(items))
	for id := range items {
		if id != 0 {
			out = append(out, id)
		}
	}
	return out
}
