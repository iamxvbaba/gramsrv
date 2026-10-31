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
	return r.enrichPreparedUpdateEventsWithPeerCache(ctx, viewerUserID, r.prepareUpdateEventsForViewer(ctx, viewerUserID, events), cache)
}

func (r *Router) enrichUpdateEventsWithPeerCacheStrict(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent, cache *viewerPeerCache) ([]domain.UpdateEvent, error) {
	if len(events) == 0 {
		return events, nil
	}
	return r.enrichPreparedUpdateEventsWithPeerCacheStrict(ctx, viewerUserID, r.prepareUpdateEventsForViewer(ctx, viewerUserID, events), cache)
}

func (r *Router) prepareUpdateEventsForViewer(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent) []domain.UpdateEvent {
	out := append([]domain.UpdateEvent(nil), events...)
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
		if out[i].Type == domain.UpdateEventMessagePoll {
			out[i] = r.enrichMessagePollEvent(ctx, viewerUserID, out[i])
		}
		if out[i].Type == domain.UpdateEventDraftMessage {
			out[i] = r.enrichDraftMessageEvent(ctx, viewerUserID, out[i])
		}
	}
	return out
}

func (r *Router) enrichPreparedUpdateEventsWithPeerCache(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent, cache *viewerPeerCache) []domain.UpdateEvent {
	out, _ := r.enrichPreparedUpdateEvents(ctx, viewerUserID, events, cache, false)
	return out
}

func (r *Router) enrichPreparedUpdateEventsWithPeerCacheStrict(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent, cache *viewerPeerCache) ([]domain.UpdateEvent, error) {
	return r.enrichPreparedUpdateEvents(ctx, viewerUserID, events, cache, true)
}

func (r *Router) enrichPreparedUpdateEvents(ctx context.Context, viewerUserID int64, events []domain.UpdateEvent, cache *viewerPeerCache, strictUsers bool) ([]domain.UpdateEvent, error) {
	if len(events) == 0 {
		return events, nil
	}
	if cache == nil {
		cache = newViewerPeerCache(r)
	}
	out := append([]domain.UpdateEvent(nil), events...)
	refs := make([]updateEventPeerRefs, len(out))
	allUserIDs := make(map[int64]struct{})
	allChannelIDs := make(map[int64]struct{})
	for i := range out {
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
		collectDialogDraftPeerRefs(out[i].Draft, userIDs, channelIDs)
		if strictUsers {
			// Durable envelopes may contain raw base users that older event
			// constructors did not expose through payload refs. Their IDs are
			// expected, but their account-scoped fields must never survive.
			for _, user := range out[i].Users {
				if user.ID != 0 {
					userIDs[user.ID] = struct{}{}
				}
			}
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
	if strictUsers {
		if _, err := cache.usersForIDsStrict(ctx, viewerUserID, mapKeys(allUserIDs)); err != nil {
			return nil, err
		}
	} else {
		cache.usersForIDs(ctx, viewerUserID, mapKeys(allUserIDs))
	}
	cache.channelsForIDs(ctx, viewerUserID, mapKeys(allChannelIDs))
	for i := range out {
		if strictUsers {
			users, err := cache.usersForIDsStrict(ctx, viewerUserID, mapKeys(refs[i].userIDs))
			if err != nil {
				return nil, err
			}
			out[i].Users = users
		} else {
			out[i].Users = r.withUsersPresence(mergeDomainUsers(out[i].Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(refs[i].userIDs))...))
		}
		out[i].Channels = mergeDomainChannels(out[i].Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(refs[i].channelIDs))...)
	}
	return out, nil
}

func collectDialogDraftPeerRefs(draft *domain.DialogDraft, userIDs, channelIDs map[int64]struct{}) {
	if draft == nil {
		return
	}
	addDomainPeerRef(draft.Peer, 0, userIDs, channelIDs)
	for _, entity := range draft.Entities {
		if entity.UserID != 0 {
			userIDs[entity.UserID] = struct{}{}
		}
	}
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

// enrichMessagePollEvent reloads the message per viewer while replaying a difference
// (the media carries the latest authoritative poll state and the viewer gating),
// isomorphic to the reaction event enrich.
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

// enrichDraftMessageEvent fills in the draft content from the current authoritative
// state while replaying a draft_message event: a draft is absolute state and the event
// row does not freeze a snapshot; when the draft was deleted (or the read fails) Draft
// is set to nil -> an empty is emitted.
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
	out, _ := r.enrichChannelDifferenceUsers(ctx, viewerUserID, diff, false)
	return out
}

func (r *Router) enrichChannelDifferenceStrict(ctx context.Context, viewerUserID int64, diff domain.ChannelDifference) (domain.ChannelDifference, error) {
	return r.enrichChannelDifferenceUsers(ctx, viewerUserID, diff, true)
}

func (r *Router) enrichChannelDifferenceUsers(ctx context.Context, viewerUserID int64, diff domain.ChannelDifference, strictUsers bool) (domain.ChannelDifference, error) {
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
	if strictUsers {
		for _, user := range diff.Users {
			if user.ID != 0 {
				userIDs[user.ID] = struct{}{}
			}
		}
	}
	removeKnownChannelRefs(channelIDs, diff.Channels)
	cache := newViewerPeerCache(r)
	if strictUsers {
		users, err := cache.usersForIDsStrict(ctx, viewerUserID, mapKeys(userIDs))
		if err != nil {
			return domain.ChannelDifference{}, err
		}
		diff.Users = users
	} else {
		diff.Users = r.withUsersPresence(mergeDomainUsers(diff.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	}
	diff.Channels = mergeDomainChannels(diff.Channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))...)
	return diff, nil
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
	// The Post/Discussion channels are already emitted separately by the conversion
	// layer, avoiding duplicates in chats.
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
	if r.messageUsersAreViewerProjected() {
		cache.primeUsers(viewerUserID, list.Users)
	}
	list.Users = r.withUsersPresence(mergeDomainUsers(list.Users, cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))...))
	return list
}

type viewerProjectedMessageUsers interface {
	ProjectsMessageUsersForViewer() bool
}

func (r *Router) messageUsersAreViewerProjected() bool {
	projected, ok := r.deps.Messages.(viewerProjectedMessageUsers)
	return ok && projected.ProjectsMessageUsersForViewer()
}

func (r *Router) preloadedMessageUsers(list domain.MessageList) []domain.User {
	if !r.messageUsersAreViewerProjected() {
		return nil
	}
	return list.Users
}

func collectMessagePeerRefs(msg domain.Message, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	addDomainPeerRef(msg.From, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(msg.Peer, currentChannelID, userIDs, channelIDs)
	if msg.Forward != nil {
		addDomainPeerRef(msg.Forward.From, currentChannelID, userIDs, channelIDs)
		addDomainPeerRef(msg.Forward.SavedFrom, currentChannelID, userIDs, channelIDs)
	}
	if msg.ViaBotID != 0 {
		userIDs[msg.ViaBotID] = struct{}{}
	}
	collectMessageEntityUserRefs(msg.Entities, userIDs)
	if msg.ReplyTo != nil {
		addDomainPeerRef(msg.ReplyTo.Peer, currentChannelID, userIDs, channelIDs)
		collectMessageEntityUserRefs(msg.ReplyTo.QuoteEntities, userIDs)
		if external := msg.ReplyTo.External; external != nil {
			addDomainPeerRef(external.From.From, currentChannelID, userIDs, channelIDs)
			collectMessagePeerRefs(domain.Message{Media: external.Media, Entities: external.Entities}, currentChannelID, userIDs, channelIDs)
		}
	}
	if msg.Media != nil && msg.Media.Contact != nil && msg.Media.Contact.UserID != 0 {
		userIDs[msg.Media.Contact.UserID] = struct{}{}
	}
	if msg.Media != nil && msg.Media.Giveaway != nil {
		for _, id := range msg.Media.Giveaway.Channels {
			if id != 0 && id != currentChannelID {
				channelIDs[id] = struct{}{}
			}
		}
	}
	collectServiceActionPeerRefs(msg.Media, currentChannelID, userIDs, channelIDs)
	collectPollMediaUserRefs(msg.Media, userIDs)
	collectTodoMediaUserRefs(msg.Media, userIDs)
	if msg.Reactions != nil {
		for _, reaction := range msg.Reactions.Recent {
			if reaction.UserID != 0 {
				userIDs[reaction.UserID] = struct{}{}
			}
		}
		if msg.Reactions.Paid != nil {
			for _, reactor := range msg.Reactions.Paid.TopReactors {
				if reactor.Anonymous && !reactor.My {
					continue
				}
				addDomainPeerRef(reactor.DisplayPeer(), currentChannelID, userIDs, channelIDs)
			}
		}
	}
}

// collectPollMediaUserRefs collects the poll's recent voters (public-poll avatar
// rendering needs the user entity).
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
	addDomainPeerRef(msg.SavedPeer, currentChannelID, userIDs, channelIDs)
	if msg.Forward != nil {
		addDomainPeerRef(msg.Forward.From, currentChannelID, userIDs, channelIDs)
		addDomainPeerRef(msg.Forward.SavedFrom, currentChannelID, userIDs, channelIDs)
	}
	if msg.ViaBotID != 0 {
		userIDs[msg.ViaBotID] = struct{}{}
	}
	collectMessageEntityUserRefs(msg.Entities, userIDs)
	if msg.ReplyTo != nil {
		addDomainPeerRef(msg.ReplyTo.Peer, currentChannelID, userIDs, channelIDs)
		collectMessageEntityUserRefs(msg.ReplyTo.QuoteEntities, userIDs)
		if external := msg.ReplyTo.External; external != nil {
			addDomainPeerRef(external.From.From, currentChannelID, userIDs, channelIDs)
			collectMessagePeerRefs(domain.Message{Media: external.Media, Entities: external.Entities}, currentChannelID, userIDs, channelIDs)
		}
	}
	if msg.Media != nil && msg.Media.Contact != nil && msg.Media.Contact.UserID != 0 {
		userIDs[msg.Media.Contact.UserID] = struct{}{}
	}
	collectPollMediaUserRefs(msg.Media, userIDs)
	collectTodoMediaUserRefs(msg.Media, userIDs)
	if msg.Replies != nil {
		for _, peer := range msg.Replies.RecentRepliers {
			addDomainPeerRef(peer, currentChannelID, userIDs, channelIDs)
		}
	}
	if msg.Action != nil {
		if msg.Action.InviterUserID != 0 {
			userIDs[msg.Action.InviterUserID] = struct{}{}
		}
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
		collectStarGiftUniquePeerRefs(msg.Action.StarGiftUnique, currentChannelID, userIDs, channelIDs)
	}
	if msg.Reactions != nil {
		for _, reaction := range msg.Reactions.Recent {
			if reaction.UserID != 0 {
				userIDs[reaction.UserID] = struct{}{}
			}
		}
		if msg.Reactions.Paid != nil {
			for _, reactor := range msg.Reactions.Paid.TopReactors {
				if reactor.Anonymous && !reactor.My {
					continue
				}
				addDomainPeerRef(reactor.DisplayPeer(), currentChannelID, userIDs, channelIDs)
			}
		}
	}
}

func collectMessageEntityUserRefs(entities []domain.MessageEntity, userIDs map[int64]struct{}) {
	for _, entity := range entities {
		if entity.UserID != 0 {
			userIDs[entity.UserID] = struct{}{}
		}
	}
}

func collectServiceActionPeerRefs(media *domain.MessageMedia, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	if media == nil || media.ServiceAction == nil {
		return
	}
	action := media.ServiceAction
	if action.RequestedPeer != nil {
		for _, peer := range action.RequestedPeer.Peers {
			addDomainPeerRef(peer, currentChannelID, userIDs, channelIDs)
		}
	}
	if gift := action.StarGift; gift != nil {
		if gift.FromUserID != 0 && !gift.NameHidden {
			userIDs[gift.FromUserID] = struct{}{}
		}
		if gift.PeerUserID != 0 {
			userIDs[gift.PeerUserID] = struct{}{}
		}
		if gift.PeerChannelID != 0 && gift.PeerChannelID != currentChannelID {
			channelIDs[gift.PeerChannelID] = struct{}{}
		}
		addDomainPeerRef(gift.To, currentChannelID, userIDs, channelIDs)
	}
	collectStarGiftUniquePeerRefs(action.StarGiftUnique, currentChannelID, userIDs, channelIDs)
}

func collectStarGiftUniquePeerRefs(action *domain.MessageStarGiftUniqueAction, currentChannelID int64, userIDs, channelIDs map[int64]struct{}) {
	if action == nil {
		return
	}
	if action.FromUserID != 0 {
		userIDs[action.FromUserID] = struct{}{}
	}
	addDomainPeerRef(action.Peer, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(action.Gift.Owner, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(action.Gift.OriginalOwner, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(action.Gift.ReleasedBy, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(action.Gift.ThemePeer, currentChannelID, userIDs, channelIDs)
	addDomainPeerRef(action.Gift.Host, currentChannelID, userIDs, channelIDs)
	if action.Gift.OriginalFromUserID != 0 && !action.Gift.OriginalNameHidden {
		userIDs[action.Gift.OriginalFromUserID] = struct{}{}
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
