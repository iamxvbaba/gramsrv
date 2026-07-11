package rpc

import (
	"context"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

func (r *Router) tgStoriesAllStories(ctx context.Context, viewerUserID int64, list domain.StoryList) tg.StoriesAllStoriesClass {
	list = r.withStoryListPeerObjects(ctx, viewerUserID, list)
	out := tgStoriesAllStories(viewerUserID, list)
	if stories, ok := out.(*tg.StoriesAllStories); ok {
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, stories.Users, stories.Chats)
	}
	return out
}

func (r *Router) tgStoriesStories(ctx context.Context, viewerUserID int64, list domain.StoryList) *tg.StoriesStories {
	list = r.withStoryListPeerObjects(ctx, viewerUserID, list)
	out := tgStoriesStories(viewerUserID, list)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) tgStoriesPeerStories(ctx context.Context, viewerUserID int64, peerStories domain.PeerStories) *tg.StoriesPeerStories {
	peerStories = r.withPeerStoriesPeerObjects(ctx, viewerUserID, peerStories)
	out := tgStoriesPeerStories(viewerUserID, peerStories)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) tgStoryViewsList(ctx context.Context, viewerUserID int64, list domain.StoryViewList) *tg.StoriesStoryViewsList {
	users := r.domainUsersForIDs(ctx, viewerUserID, storyViewUserIDs(list.Views))
	peerUsers, peerChannels := r.storyPeerObjects(ctx, viewerUserID, storyViewPeers(list.Views))
	users = mergeDomainUsers(users, peerUsers...)
	out := tgStoryViewsList(viewerUserID, list, users)
	if len(peerChannels) > 0 {
		out.Chats = appendUniqueTGChats(out.Chats, tgChannels(viewerUserID, peerChannels)...)
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) tgStoryReactionsList(ctx context.Context, viewerUserID int64, list domain.StoryReactionList) *tg.StoriesStoryReactionsList {
	users := r.domainUsersForIDs(ctx, viewerUserID, storyViewUserIDs(list.Reactions))
	peerUsers, peerChannels := r.storyPeerObjects(ctx, viewerUserID, storyViewPeers(list.Reactions))
	users = mergeDomainUsers(users, peerUsers...)
	out := tgStoryReactionsList(viewerUserID, list, users)
	if len(peerChannels) > 0 {
		out.Chats = appendUniqueTGChats(out.Chats, tgChannels(viewerUserID, peerChannels)...)
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) applyStoryMaxIDsToChats(ctx context.Context, viewerUserID int64, out tg.MessagesChatsClass) tg.MessagesChatsClass {
	switch v := out.(type) {
	case *tg.MessagesChats:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, nil, v.Chats)
	case *tg.MessagesChatsSlice:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, nil, v.Chats)
	}
	return out
}

func (r *Router) tgMessagesDialogs(ctx context.Context, viewerUserID int64, list domain.DialogList) tg.MessagesDialogsClass {
	list = r.withDialogNotifySettings(ctx, viewerUserID, list)
	out := tgMessagesDialogs(viewerUserID, list)
	switch v := out.(type) {
	case *tg.MessagesDialogs:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	case *tg.MessagesDialogsSlice:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	}
	return out
}

func (r *Router) tgPeerDialogs(ctx context.Context, viewerUserID int64, list domain.DialogList, st domain.UpdateState) *tg.MessagesPeerDialogs {
	list = r.withDialogNotifySettings(ctx, viewerUserID, list)
	out := tgPeerDialogs(viewerUserID, list, st)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) tgContacts(ctx context.Context, viewerUserID int64, list domain.ContactList) tg.ContactsContactsClass {
	out := tgContacts(list)
	if contacts, ok := out.(*tg.ContactsContacts); ok {
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, contacts.Users, nil)
	}
	return out
}

func (r *Router) tgContactsFound(ctx context.Context, viewerUserID int64, res domain.UserSearchResult) *tg.ContactsFound {
	out := tgContactsFound(viewerUserID, res)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) tgResolvedUserPeerWithStories(ctx context.Context, viewerUserID int64, u domain.User) *tg.ContactsResolvedPeer {
	out := r.tgResolvedUserPeer(viewerUserID, u)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, nil)
	return out
}

func (r *Router) tgResolvedChannelPeerWithStories(ctx context.Context, viewerUserID int64, view domain.ChannelView) *tg.ContactsResolvedPeer {
	out := tgResolvedChannelPeer(viewerUserID, view)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, nil, out.Chats)
	return out
}

func (r *Router) tgGlobalChannelMessages(ctx context.Context, viewerUserID int64, history domain.ChannelHistory) tg.MessagesMessagesClass {
	out := tgGlobalChannelMessages(viewerUserID, history)
	r.applyStoryMaxIDsToMessages(ctx, viewerUserID, out)
	return out
}

func (r *Router) tgMessagesMessages(ctx context.Context, viewerUserID int64, list domain.MessageList) tg.MessagesMessagesClass {
	out := tgMessagesMessages(viewerUserID, list)
	r.applyStoryMaxIDsToMessages(ctx, viewerUserID, out)
	return out
}

func (r *Router) tgChannelHistoryMessages(ctx context.Context, viewerUserID int64, history domain.ChannelHistory) tg.MessagesMessagesClass {
	out := tgChannelHistoryMessages(viewerUserID, history)
	if linked, ok := r.linkedDiscussionChat(ctx, viewerUserID, history.Channel.ID); ok {
		switch value := out.(type) {
		case *tg.MessagesChannelMessages:
			value.Chats = replaceTGChat(value.Chats, linked)
		case *tg.MessagesMessagesSlice:
			value.Chats = replaceTGChat(value.Chats, linked)
		case *tg.MessagesMessages:
			value.Chats = replaceTGChat(value.Chats, linked)
		}
	}
	r.applyStoryMaxIDsToMessages(ctx, viewerUserID, out)
	return out
}

func (r *Router) tgMessagesDiscussionMessage(ctx context.Context, viewerUserID int64, discussion domain.ChannelDiscussionMessage) *tg.MessagesDiscussionMessage {
	out := tgMessagesDiscussionMessage(viewerUserID, discussion)
	if linked, ok := r.linkedDiscussionChat(ctx, viewerUserID, discussion.PostChannel.ID); ok {
		out.Chats = replaceTGChat(out.Chats, linked)
	}
	// 用带 presence + self 标志的投影覆盖裸 tgUsers，防止 viewer 自己以 self=false
	// 进入 Users（Android putUsers 会覆盖 currentUser）。
	out.Users = r.tgUsersForViewer(viewerUserID, discussion.Users)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	return out
}

func (r *Router) applyStoryMaxIDsToForumTopics(ctx context.Context, viewerUserID int64, out *tg.MessagesForumTopics) *tg.MessagesForumTopics {
	if out != nil {
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	}
	return out
}

func (r *Router) applyStoryMaxIDsToMessageViews(ctx context.Context, viewerUserID int64, out *tg.MessagesMessageViews) *tg.MessagesMessageViews {
	if out != nil {
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	}
	return out
}

func (r *Router) applyStoryMaxIDsToMessageReactionsList(ctx context.Context, viewerUserID int64, out *tg.MessagesMessageReactionsList) *tg.MessagesMessageReactionsList {
	if out != nil {
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, out.Users, out.Chats)
	}
	return out
}

func (r *Router) tgGlobalSearchMessages(ctx context.Context, viewerUserID int64, limit int, private domain.MessageList, channel domain.ChannelHistory) tg.MessagesMessagesClass {
	out := tgGlobalSearchMessages(viewerUserID, limit, private, channel)
	r.applyStoryMaxIDsToMessages(ctx, viewerUserID, out)
	return out
}

func (r *Router) applyStoryMaxIDsToMessages(ctx context.Context, viewerUserID int64, out tg.MessagesMessagesClass) {
	switch v := out.(type) {
	case *tg.MessagesMessages:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	case *tg.MessagesMessagesSlice:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	case *tg.MessagesChannelMessages:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	}
}

func (r *Router) tgStoryChangeUpdates(ctx context.Context, viewerUserID int64, peer domain.Peer, story domain.Story, randomID int64, includeStoryID bool, date int) tg.UpdatesClass {
	out, _ := tgStoryChangeUpdates(peer, story, randomID, includeStoryID, date).(*tg.Updates)
	peers := append([]domain.Peer{peer, story.Owner}, storyForwardPeers(story)...)
	out = r.withStoryUpdatePeerObjects(ctx, viewerUserID, out, peers...)
	r.appendStoryPrivacyUsers(ctx, viewerUserID, out, []domain.Story{story})
	return out
}

func (r *Router) tgStoryReactionUpdates(ctx context.Context, viewerUserID int64, peer domain.Peer, storyID int, reaction tg.ReactionClass, date int) tg.UpdatesClass {
	out, _ := tgStoryReactionUpdates(peer, storyID, reaction, date).(*tg.Updates)
	return r.withStoryUpdatePeerObjects(ctx, viewerUserID, out, peer)
}

func (r *Router) tgUpdatesDifference(ctx context.Context, viewerUserID int64, diff domain.UpdateDifference) tg.UpdatesDifferenceClass {
	out := tgUpdatesDifference(viewerUserID, diff)
	switch v := out.(type) {
	case *tg.UpdatesDifference:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	case *tg.UpdatesDifferenceSlice:
		r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, v.Users, v.Chats)
	}
	return out
}

func (r *Router) withStoryUpdatePeerObjects(ctx context.Context, viewerUserID int64, updates *tg.Updates, peers ...domain.Peer) *tg.Updates {
	if updates == nil {
		return nil
	}
	users, channels := r.storyPeerObjects(ctx, viewerUserID, peers)
	if len(users) > 0 {
		updates.Users = appendUniqueTGUsers(updates.Users, r.tgUsersForViewer(viewerUserID, users)...)
	}
	if len(channels) > 0 {
		updates.Chats = appendUniqueTGChats(updates.Chats, tgChannels(viewerUserID, channels)...)
	}
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, updates.Users, updates.Chats)
	return updates
}

func (r *Router) withStoryListPeerObjects(ctx context.Context, viewerUserID int64, list domain.StoryList) domain.StoryList {
	users, channels := r.storyPeerObjects(ctx, viewerUserID, storyListOwnerPeers(list))
	if len(users) > 0 {
		list.Users = mergeDomainUsers(list.Users, users...)
	}
	if privacyUsers := r.domainUsersForIDs(ctx, viewerUserID, storyListPrivacyUserIDs(list)); len(privacyUsers) > 0 {
		list.Users = mergeDomainUsers(list.Users, privacyUsers...)
	}
	if len(channels) > 0 {
		list.Channels = mergeDomainChannels(list.Channels, channels...)
	}
	return list
}

func (r *Router) withPeerStoriesPeerObjects(ctx context.Context, viewerUserID int64, peerStories domain.PeerStories) domain.PeerStories {
	peers := make([]domain.Peer, 0, len(peerStories.Stories)+1)
	peers = append(peers, peerStories.Peer)
	for _, story := range peerStories.Stories {
		peers = append(peers, story.Owner)
		peers = append(peers, storyForwardPeers(story)...)
	}
	users, channels := r.storyPeerObjects(ctx, viewerUserID, peers)
	if len(users) > 0 {
		peerStories.Users = mergeDomainUsers(peerStories.Users, users...)
	}
	if privacyUsers := r.domainUsersForIDs(ctx, viewerUserID, storyPrivacyUserIDs(peerStories.Stories)); len(privacyUsers) > 0 {
		peerStories.Users = mergeDomainUsers(peerStories.Users, privacyUsers...)
	}
	if len(channels) > 0 {
		peerStories.Channels = mergeDomainChannels(peerStories.Channels, channels...)
	}
	return peerStories
}

func (r *Router) appendStoryPrivacyUsers(ctx context.Context, viewerUserID int64, updates *tg.Updates, stories []domain.Story) {
	if updates == nil {
		return
	}
	users := r.domainUsersForIDs(ctx, viewerUserID, storyPrivacyUserIDs(stories))
	if len(users) == 0 {
		return
	}
	updates.Users = appendUniqueTGUsers(updates.Users, r.tgUsersForViewer(viewerUserID, users)...)
	r.applyStoryMaxIDsToPeerObjects(ctx, viewerUserID, updates.Users, updates.Chats)
}

func storyViewUserIDs(views []domain.StoryView) []int64 {
	if len(views) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(views))
	ids := make([]int64, 0, len(views))
	for _, view := range views {
		if view.PublicForward != nil {
			if media := view.PublicForward.Message.Media; media != nil && media.Kind == domain.MessageMediaKindStory && media.Story != nil && media.Story.Peer.Type == domain.PeerTypeUser && media.Story.Peer.ID != 0 {
				if _, ok := seen[media.Story.Peer.ID]; !ok {
					seen[media.Story.Peer.ID] = struct{}{}
					ids = append(ids, media.Story.Peer.ID)
				}
			}
			if view.PublicForward.Message.SenderUserID != 0 {
				if _, ok := seen[view.PublicForward.Message.SenderUserID]; !ok {
					seen[view.PublicForward.Message.SenderUserID] = struct{}{}
					ids = append(ids, view.PublicForward.Message.SenderUserID)
				}
			}
			if view.PublicForward.Message.From.Type == domain.PeerTypeUser && view.PublicForward.Message.From.ID != 0 {
				if _, ok := seen[view.PublicForward.Message.From.ID]; !ok {
					seen[view.PublicForward.Message.From.ID] = struct{}{}
					ids = append(ids, view.PublicForward.Message.From.ID)
				}
			}
			if view.PublicForward.Message.SendAs != nil && view.PublicForward.Message.SendAs.Type == domain.PeerTypeUser && view.PublicForward.Message.SendAs.ID != 0 {
				if _, ok := seen[view.PublicForward.Message.SendAs.ID]; !ok {
					seen[view.PublicForward.Message.SendAs.ID] = struct{}{}
					ids = append(ids, view.PublicForward.Message.SendAs.ID)
				}
			}
			continue
		}
		if view.Repost != nil {
			continue
		}
		if view.ViewerID == 0 {
			continue
		}
		if _, ok := seen[view.ViewerID]; ok {
			continue
		}
		seen[view.ViewerID] = struct{}{}
		ids = append(ids, view.ViewerID)
	}
	return ids
}

func storyViewPeers(views []domain.StoryView) []domain.Peer {
	if len(views) == 0 {
		return nil
	}
	peers := make([]domain.Peer, 0, len(views))
	for _, view := range views {
		if view.PublicForward != nil {
			if view.PublicForward.Message.ChannelID != 0 {
				peers = append(peers, domain.Peer{Type: domain.PeerTypeChannel, ID: view.PublicForward.Message.ChannelID})
			}
			if media := view.PublicForward.Message.Media; media != nil && media.Kind == domain.MessageMediaKindStory && media.Story != nil && media.Story.Peer.ID != 0 {
				peers = append(peers, media.Story.Peer)
			}
			if view.PublicForward.Message.From.ID != 0 {
				peers = append(peers, view.PublicForward.Message.From)
			}
			if view.PublicForward.Message.SendAs != nil && view.PublicForward.Message.SendAs.ID != 0 {
				peers = append(peers, *view.PublicForward.Message.SendAs)
			}
			continue
		}
		if view.Repost == nil {
			continue
		}
		peers = append(peers, view.Repost.Owner)
		peers = append(peers, storyForwardPeers(*view.Repost)...)
	}
	return peers
}

func (r *Router) storyPeerObjects(ctx context.Context, viewerUserID int64, peers []domain.Peer) ([]domain.User, []domain.Channel) {
	if viewerUserID == 0 || len(peers) == 0 {
		return nil, nil
	}
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, peer := range peers {
		switch peer.Type {
		case domain.PeerTypeUser:
			if peer.ID != 0 {
				userIDs[peer.ID] = struct{}{}
			}
		case domain.PeerTypeChannel:
			if peer.ID != 0 {
				channelIDs[peer.ID] = struct{}{}
			}
		}
	}
	cache := newViewerPeerCache(r)
	return cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs)),
		cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))
}

func storyListOwnerPeers(list domain.StoryList) []domain.Peer {
	peers := make([]domain.Peer, 0, len(list.Peers)+len(list.Stories))
	for _, item := range list.Peers {
		peers = append(peers, item.Peer)
		for _, story := range item.Stories {
			peers = append(peers, storyForwardPeers(story)...)
		}
	}
	for _, story := range list.Stories {
		peers = append(peers, story.Owner)
		peers = append(peers, storyForwardPeers(story)...)
	}
	return peers
}

func storyForwardPeers(story domain.Story) []domain.Peer {
	if story.Forward == nil || story.Forward.From.ID == 0 {
		return nil
	}
	switch story.Forward.From.Type {
	case domain.PeerTypeUser, domain.PeerTypeChannel:
		return []domain.Peer{story.Forward.From}
	default:
		return nil
	}
}

func storyListPrivacyUserIDs(list domain.StoryList) []int64 {
	stories := make([]domain.Story, 0, len(list.Stories))
	stories = append(stories, list.Stories...)
	for _, item := range list.Peers {
		stories = append(stories, item.Stories...)
	}
	return storyPrivacyUserIDs(stories)
}

func storyPrivacyUserIDs(stories []domain.Story) []int64 {
	if len(stories) == 0 {
		return nil
	}
	seen := make(map[int64]struct{})
	out := make([]int64, 0)
	for _, story := range stories {
		if !story.Out {
			continue
		}
		for _, id := range privacyRuleUserIDs(story.PrivacyRules) {
			if id == 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

func storyUpdateEventPeers(event domain.UpdateEvent) []domain.Peer {
	switch event.Type {
	case domain.UpdateEventStory:
		return append([]domain.Peer{event.Peer, event.Story.Owner}, storyForwardPeers(event.Story)...)
	case domain.UpdateEventReadStories, domain.UpdateEventSentStoryReaction:
		return []domain.Peer{event.Peer}
	case domain.UpdateEventNewStoryReaction:
		return append([]domain.Peer{event.Peer, event.Story.Owner}, storyForwardPeers(event.Story)...)
	default:
		return nil
	}
}

func appendUniqueTGUsers(base []tg.UserClass, extra ...tg.UserClass) []tg.UserClass {
	seen := make(map[int64]struct{}, len(base)+len(extra))
	out := make([]tg.UserClass, 0, len(base)+len(extra))
	appendOne := func(item tg.UserClass) {
		user, ok := item.(*tg.User)
		if !ok || user.ID == 0 {
			out = append(out, item)
			return
		}
		if _, exists := seen[user.ID]; exists {
			return
		}
		seen[user.ID] = struct{}{}
		out = append(out, item)
	}
	for _, item := range base {
		appendOne(item)
	}
	for _, item := range extra {
		appendOne(item)
	}
	return out
}

func appendUniqueTGChats(base []tg.ChatClass, extra ...tg.ChatClass) []tg.ChatClass {
	seen := make(map[int64]struct{}, len(base)+len(extra))
	out := make([]tg.ChatClass, 0, len(base)+len(extra))
	appendOne := func(item tg.ChatClass) {
		channel, ok := item.(*tg.Channel)
		if !ok || channel.ID == 0 {
			out = append(out, item)
			return
		}
		if _, exists := seen[channel.ID]; exists {
			return
		}
		seen[channel.ID] = struct{}{}
		out = append(out, item)
	}
	for _, item := range base {
		appendOne(item)
	}
	for _, item := range extra {
		appendOne(item)
	}
	return out
}

func (r *Router) applyStoryMaxIDsToPeerObjects(ctx context.Context, viewerUserID int64, users []tg.UserClass, chats []tg.ChatClass) {
	if r.deps.Stories == nil || viewerUserID == 0 || len(users)+len(chats) == 0 {
		return
	}
	peers := make([]domain.Peer, 0, len(users)+len(chats))
	seen := make(map[domain.Peer]struct{}, len(users)+len(chats))
	addPeer := func(peer domain.Peer) {
		if peer.ID == 0 {
			return
		}
		if _, ok := seen[peer]; ok {
			return
		}
		seen[peer] = struct{}{}
		peers = append(peers, peer)
	}
	for _, item := range users {
		if u, ok := item.(*tg.User); ok {
			addPeer(domain.Peer{Type: domain.PeerTypeUser, ID: u.ID})
		}
	}
	for _, item := range chats {
		if ch, ok := item.(*tg.Channel); ok {
			addPeer(domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID})
		}
	}
	recent, hidden := r.storyProjectionMaps(ctx, viewerUserID, peers)
	for _, item := range users {
		u, ok := item.(*tg.User)
		if !ok {
			continue
		}
		peer := domain.Peer{Type: domain.PeerTypeUser, ID: u.ID}
		if story, ok := recent[peer]; ok {
			u.SetStoriesMaxID(story)
		}
		if state, ok := hidden[peer]; ok {
			u.SetStoriesHidden(state)
		}
	}
	for _, item := range chats {
		ch, ok := item.(*tg.Channel)
		if !ok {
			continue
		}
		peer := domain.Peer{Type: domain.PeerTypeChannel, ID: ch.ID}
		if story, ok := recent[peer]; ok {
			ch.SetStoriesMaxID(story)
		}
		if state, ok := hidden[peer]; ok {
			ch.SetStoriesHidden(state)
		}
	}
}

func (r *Router) applyStoriesPinnedAvailableToUserFull(ctx context.Context, viewerUserID, ownerUserID int64, full *tg.UserFull) {
	if full != nil && r.storyPinnedAvailable(ctx, viewerUserID, domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}) {
		full.SetStoriesPinnedAvailable(true)
	}
}

func (r *Router) applyStoriesPinnedAvailableToChannelFull(ctx context.Context, viewerUserID int64, channelID int64, full *tg.ChannelFull) {
	if full != nil && r.storyPinnedAvailable(ctx, viewerUserID, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}) {
		full.SetStoriesPinnedAvailable(true)
	}
}

func (r *Router) storyPinnedAvailable(ctx context.Context, viewerUserID int64, peer domain.Peer) bool {
	if r.deps.Stories == nil || viewerUserID == 0 || peer.ID == 0 {
		return false
	}
	available, err := r.storyPinnedCache.getOrLoad(ctx, viewerUserID, peer, func() (bool, error) {
		return r.deps.Stories.HasPinnedStories(ctx, viewerUserID, peer, int(r.clock.Now().Unix()))
	})
	if err != nil {
		r.log.Warn("project story pinned availability failed",
			zap.Int64("viewer_user_id", viewerUserID),
			zap.String("peer_type", string(peer.Type)),
			zap.Int64("peer_id", peer.ID),
			zap.Error(err))
		return false
	}
	return available
}

func (r *Router) storyProjectionFreshMaps(ctx context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]tg.RecentStory, map[domain.Peer]bool) {
	if r.deps.Stories == nil || viewerUserID == 0 || len(peers) == 0 {
		return nil, nil
	}
	recent := make(map[domain.Peer]tg.RecentStory, len(peers))
	hidden := make(map[domain.Peer]bool, len(peers))
	now := int(r.clock.Now().Unix())
	for start := 0; start < len(peers); start += domain.MaxStoryIDs {
		end := start + domain.MaxStoryIDs
		if end > len(peers) {
			end = len(peers)
		}
		chunk := peers[start:end]
		for _, peer := range chunk {
			hidden[peer] = false
		}
		projections, err := r.deps.Stories.GetPeerStoryProjections(ctx, viewerUserID, chunk, now)
		if err != nil {
			r.log.Warn("project story peer summaries failed",
				zap.Int64("viewer_user_id", viewerUserID),
				zap.Int("peer_count", len(chunk)),
				zap.Error(err))
			continue
		}
		for _, item := range projections {
			if story, ok := tgRecentStory(item.Recent); ok {
				recent[item.Peer] = story
			}
			hidden[item.Peer] = item.Hidden
		}
	}
	return recent, hidden
}

func tgRecentStory(in domain.RecentStory) (tg.RecentStory, bool) {
	if in.MaxID <= 0 && !in.Live {
		return tg.RecentStory{}, false
	}
	out := tg.RecentStory{Live: in.Live}
	if in.MaxID > 0 {
		out.SetMaxID(in.MaxID)
	}
	return out, true
}
