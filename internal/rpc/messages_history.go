package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"sort"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesGetSearchResultsCalendar(ctx context.Context, req *tg.MessagesGetSearchResultsCalendarRequest) (*tg.MessagesSearchResultsCalendar, error) {
	if req.OffsetID < 0 || req.OffsetID > domain.MaxMessageBoxID || req.OffsetDate < 0 {
		return nil, limitInvalidErr()
	}
	if err := r.validateSearchResultsPeer(ctx, req.Peer, req.GetSavedPeerID); err != nil {
		return nil, err
	}
	minDate := req.OffsetDate
	if minDate == 0 {
		minDate = int(r.clock.Now().Unix())
	}
	return &tg.MessagesSearchResultsCalendar{
		Count:    0,
		MinDate:  minDate,
		MinMsgID: req.OffsetID,
		Periods:  []tg.SearchResultsCalendarPeriod{},
		Messages: []tg.MessageClass{},
		Chats:    []tg.ChatClass{},
		Users:    []tg.UserClass{},
	}, nil
}

func (r *Router) onMessagesGetSearchResultsPositions(ctx context.Context, req *tg.MessagesGetSearchResultsPositionsRequest) (*tg.MessagesSearchResultsPositions, error) {
	if req.OffsetID < 0 || req.OffsetID > domain.MaxMessageBoxID || req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	if err := r.validateSearchResultsPeer(ctx, req.Peer, req.GetSavedPeerID); err != nil {
		return nil, err
	}
	return &tg.MessagesSearchResultsPositions{
		Count:     0,
		Positions: []tg.SearchResultPosition{},
	}, nil
}

func (r *Router) validateSearchResultsPeer(ctx context.Context, peer tg.InputPeerClass, savedPeer func() (tg.InputPeerClass, bool)) error {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return internalErr()
	}
	if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer); err != nil {
		return err
	}
	if savedPeer == nil {
		return nil
	}
	if input, ok := savedPeer(); ok && input != nil {
		if _, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input); err != nil {
			return err
		}
	}
	return nil
}

func (r *Router) onMessagesGetMessagesViews(ctx context.Context, req *tg.MessagesGetMessagesViewsRequest) (*tg.MessagesMessageViews, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(req.ID) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	for _, msgID := range req.ID {
		if msgID <= 0 || msgID > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
	}
	views := make([]tg.MessageViews, len(req.ID))
	peer, peerErr := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if peerErr != nil {
		return nil, peerErr
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil && len(req.ID) > 0 {
		viewCounters, err := r.deps.Channels.GetMessageViews(ctx, userID, domain.ChannelMessageViewsRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			IDs:       req.ID,
			Increment: req.Increment,
			Date:      int(r.clock.Now().Unix()),
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		for i, id := range req.ID {
			if count, ok := viewCounters.Views[id]; ok {
				views[i].SetViews(count)
			}
			if replies := tgChannelMessageReplies(viewCounters.Replies[id]); replies != nil {
				views[i].SetReplies(*replies)
			}
		}
		channels, users := r.messageViewPeerObjects(ctx, userID, viewCounters)
		return r.applyStoryMaxIDsToMessageViews(ctx, userID, &tg.MessagesMessageViews{
			Views: views,
			Chats: tgChannels(userID, channels),
			Users: r.tgUsersForViewer(userID, users),
		}), nil
	}
	return r.applyStoryMaxIDsToMessageViews(ctx, userID, &tg.MessagesMessageViews{
		Views: views,
		Chats: r.chatsForInputPeer(ctx, userID, req.Peer),
		Users: []tg.UserClass{},
	}), nil
}

func (r *Router) messageViewPeerObjects(ctx context.Context, viewerUserID int64, result domain.ChannelMessageViewsResult) ([]domain.Channel, []domain.User) {
	channels := []domain.Channel{}
	if result.Channel.ID != 0 {
		channels = append(channels, result.Channel)
	}
	userIDs := make(map[int64]struct{})
	channelIDs := make(map[int64]struct{})
	for _, peer := range result.Peers {
		addDomainPeerRef(peer, result.Channel.ID, userIDs, channelIDs)
	}
	for _, replies := range result.Replies {
		if replies == nil {
			continue
		}
		if replies.ChannelID != 0 && replies.ChannelID != result.Channel.ID {
			channelIDs[replies.ChannelID] = struct{}{}
		}
		for _, peer := range replies.RecentRepliers {
			addDomainPeerRef(peer, result.Channel.ID, userIDs, channelIDs)
		}
	}
	removeKnownChannelRefs(channelIDs, channels)
	cache := newViewerPeerCache(r)
	channels = mergeDomainChannels(channels, cache.channelsForIDs(ctx, viewerUserID, mapKeys(channelIDs))...)
	users := cache.usersForIDs(ctx, viewerUserID, mapKeys(userIDs))
	return channels, users
}

func (r *Router) onMessagesGetSearchCounters(ctx context.Context, req *tg.MessagesGetSearchCountersRequest) ([]tg.MessagesSearchCounter, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(req.Filters) > maxMessageSearchFilters {
		return nil, limitInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	needsPinned := false
	needsMedia := false
	for _, filter := range req.Filters {
		if filter == nil {
			continue
		}
		if _, ok := filter.(*tg.InputMessagesFilterPinned); ok {
			needsPinned = true
			continue
		}
		if len(mediaCategoriesForFilter(filter)) > 0 {
			needsMedia = true
		}
	}
	pinnedCount := 0
	mediaCounts := domain.MediaCategoryCounts{}
	if needsPinned {
		switch {
		case peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil:
			history, err := r.deps.Channels.GetHistory(ctx, userID, domain.ChannelHistoryFilter{
				ChannelID:      peer.ID,
				PinnedOnly:     true,
				NeedTotalCount: true,
				CountOnly:      true,
			})
			if err != nil {
				return nil, err
			}
			pinnedCount = history.Count
		case peer.Type == domain.PeerTypeUser && r.deps.Messages != nil:
			list, err := r.deps.Messages.Search(ctx, userID, domain.MessageFilter{
				HasPeer:        true,
				Peer:           peer,
				PinnedOnly:     true,
				Limit:          1,
				NeedTotalCount: true,
			})
			if err != nil {
				return nil, err
			}
			pinnedCount = list.Count
		}
	}
	if needsMedia {
		counts, err := r.mediaCountsForPeer(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
		mediaCounts = counts
	}
	counters := make([]tg.MessagesSearchCounter, 0, len(req.Filters))
	for _, filter := range req.Filters {
		if filter == nil {
			continue
		}
		count := 0
		if _, ok := filter.(*tg.InputMessagesFilterPinned); ok {
			count = pinnedCount
		} else if categories := mediaCategoriesForFilter(filter); len(categories) > 0 {
			count = mediaCounts.CountAny(categories)
		}
		counters = append(counters, tg.MessagesSearchCounter{Filter: filter, Count: count})
	}
	return counters, nil
}

func (r *Router) onMessagesGetReplies(ctx context.Context, req *tg.MessagesGetRepliesRequest) (tg.MessagesMessagesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	if err := validateHistoryBounds(req.OffsetID, req.AddOffset, req.Limit, req.MaxID, req.MinID); err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		replies, err := r.deps.Channels.GetReplies(ctx, userID, domain.ChannelRepliesFilter{
			ChannelID:     peer.ID,
			RootMessageID: req.MsgID,
			OffsetID:      req.OffsetID,
			OffsetDate:    req.OffsetDate,
			AddOffset:     req.AddOffset,
			Limit:         req.Limit,
			MaxID:         req.MaxID,
			MinID:         req.MinID,
			Hash:          req.Hash,
		})
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if req.Hash != 0 && replies.Hash == req.Hash {
			return &tg.MessagesMessagesNotModified{Count: replies.Count}, nil
		}
		return r.tgChannelHistoryMessages(ctx, userID, r.enrichChannelHistory(ctx, userID, replies)), nil
	}
	result := &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:    []tg.UserClass{},
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	return result, nil
}

func (r *Router) onMessagesGetDiscussionMessage(ctx context.Context, req *tg.MessagesGetDiscussionMessageRequest) (*tg.MessagesDiscussionMessage, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return nil, peerIDInvalidErr()
	}
	if r.deps.Channels == nil {
		return &tg.MessagesDiscussionMessage{Chats: r.chatsForInputPeer(ctx, userID, req.Peer)}, nil
	}
	discussion, err := r.deps.Channels.GetDiscussionMessage(ctx, userID, peer.ID, req.MsgID)
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	// Resolve the discussion post/reply author's user/channel entities (the store
	// does not fill Users); otherwise clients cannot get the author entity and
	// need an extra getUser fallback.
	discussion = r.enrichChannelDiscussion(ctx, userID, discussion)
	return r.tgMessagesDiscussionMessage(ctx, userID, discussion), nil
}

func (r *Router) onMessagesReadDiscussion(ctx context.Context, req *tg.MessagesReadDiscussionRequest) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if req.MsgID <= 0 || req.MsgID > domain.MaxMessageBoxID || req.ReadMaxID < 0 || req.ReadMaxID > domain.MaxMessageBoxID {
		return false, messageIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return false, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return false, peerIDInvalidErr()
	}
	if r.deps.Channels == nil {
		return true, nil
	}
	now := int(r.clock.Now().Unix())
	// forum topic read: req.MsgID is the topic id; advance the per-topic watermark
	// (leaving channel-level untouched, eliminating cross-talk between topics'
	// read state), send DiscussionInbox to the user's other devices and
	// DiscussionOutbox receipts to senders within the topic. It does not go
	// through GetDiscussionMessage, so a trimmed/deleted topic root message does
	// not 400 the whole read (a non-surviving root must not block marking read).
	topicRes, terr := r.deps.Channels.ReadTopicHistory(ctx, userID, domain.ReadChannelTopicHistoryRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		TopicID:   req.MsgID,
		MaxID:     req.ReadMaxID,
		Date:      now,
	})
	if terr == nil {
		if topicRes.Changed {
			if err := r.recordChannelDiscussionInbox(ctx, userID, peer.ID, topicRes.TopicID, topicRes.MaxID, topicRes.Pts); err != nil {
				return false, err
			}
			r.pushChannelDiscussionOutboxUpdates(ctx, peer.ID, topicRes.TopicID, topicRes.OutboxUpdates)
		}
		// Conservative overlay: also advance the channel-level inbox watermark so
		// the total unread of getDialogs/getPeerDialogs does not regress.
		if read, rerr := r.deps.Channels.ReadHistory(ctx, userID, domain.ReadChannelHistoryRequest{
			UserID:    userID,
			ChannelID: peer.ID,
			MaxID:     req.ReadMaxID,
			Date:      now,
		}); rerr == nil {
			if _, err := r.recordChannelReadInbox(ctx, userID, read); err != nil {
				return false, err
			}
			r.pushChannelReadOutboxUpdates(ctx, read.ChannelID, read.OutboxUpdates)
		}
		return topicRes.Changed, nil
	}
	if !errors.Is(terr, domain.ErrChannelForumMissing) {
		return false, channelInvalidErr(terr)
	}
	// Non-forum (channel-discussion-group linked comments): only resolve
	// target/root/boundary; never load a full discussion message, reply stats,
	// reactions and unread aggregates just for a read request.
	readChannelID := peer.ID
	if provider, ok := r.deps.Channels.(interface {
		ResolveDiscussionReadTarget(context.Context, int64, int64, int, int) (domain.ChannelDiscussionReadTarget, error)
	}); ok {
		target, resolveErr := provider.ResolveDiscussionReadTarget(ctx, userID, peer.ID, req.MsgID, req.ReadMaxID)
		if resolveErr != nil {
			return false, channelInvalidErr(resolveErr)
		}
		if target.AlreadyRead {
			return false, nil
		}
		if target.Guest {
			// Linked discussion guests have no channel_members/dialog row. Reading
			// comments is therefore an authorized, durable-state-free no-op.
			return false, nil
		}
		readChannelID = target.ChannelID
	} else {
		discussion, resolveErr := r.deps.Channels.GetDiscussionMessage(ctx, userID, peer.ID, req.MsgID)
		if resolveErr != nil {
			return false, channelInvalidErr(resolveErr)
		}
		if discussion.DiscussionChannel.ID != 0 {
			readChannelID = discussion.DiscussionChannel.ID
		}
	}
	read, err := r.deps.Channels.ReadHistory(ctx, userID, domain.ReadChannelHistoryRequest{
		UserID:    userID,
		ChannelID: readChannelID,
		MaxID:     req.ReadMaxID,
		Date:      now,
	})
	if err != nil {
		return false, channelInvalidErr(err)
	}
	if _, err := r.recordChannelReadInbox(ctx, userID, read); err != nil {
		return false, err
	}
	r.pushChannelReadOutboxUpdates(ctx, read.ChannelID, read.OutboxUpdates)
	return read.Changed, nil
}

func (r *Router) onMessagesGetOnlines(ctx context.Context, peer tg.InputPeerClass) (*tg.ChatOnlines, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	domainPeer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return nil, err
	}
	if domainPeer.Type == domain.PeerTypeChannel && domainPeer.ID != 0 {
		return &tg.ChatOnlines{Onlines: r.channelOnlineCount(ctx, userID, domainPeer.ID)}, nil
	}
	return &tg.ChatOnlines{Onlines: 1}, nil
}

func (r *Router) onMessagesGetMessages(ctx context.Context, ids []tg.InputMessageClass) (tg.MessagesMessagesClass, error) {
	if len(ids) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	if r.deps.Messages == nil || len(ids) == 0 {
		return &tg.MessagesMessages{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}

	out := make([]tg.MessageClass, 0, len(ids))
	trace := newGetMessagesInputTrace(ids)
	requestedIDs := trace.lookupIDs
	list, err := r.deps.Messages.GetMessages(ctx, userID, requestedIDs)
	if err != nil {
		return nil, internalErr()
	}
	foundByID := make(map[int]domain.Message, len(list.Messages))
	for _, msg := range list.Messages {
		foundByID[msg.ID] = msg
	}
	found := make([]domain.Message, 0, len(list.Messages))
	for _, input := range ids {
		id, ok := inputMessageBoxID(input)
		if !ok || id <= 0 || id > domain.MaxMessageBoxID {
			out = append(out, &tg.MessageEmpty{ID: id})
			continue
		}
		msg, ok := foundByID[id]
		if !ok {
			out = append(out, &tg.MessageEmpty{ID: id})
			continue
		}
		found = append(found, msg)
		out = append(out, tgMessage(msg))
	}
	r.maybeEnqueueExpiredPrivateWebPageResolves(found)
	chats := r.chatsForMessageUpdates(ctx, userID, found)
	result := &tg.MessagesMessages{
		Messages: out,
		Users:    r.usersForMessageUpdatesWithPreloaded(ctx, userID, found, r.preloadedMessageUsers(list)),
		Chats:    chats,
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	r.logPrivateGetMessagesTrace(ctx, trace, found, result)
	return result, nil
}

// onMessagesGetRichMessage returns the full rich text of a single message
// (Layer 227 richMessage). The message list projection already carries
// richMessage (tgMessage); this RPC is the client's entry point to fetch the full
// rich text by (peer,id). Phase 1 is private chats only: fetch by box id from the
// requester's own message box and project; channel-side rich text is left to
// Phase 2.
func (r *Router) onMessagesGetRichMessage(ctx context.Context, req *tg.MessagesGetRichMessageRequest) (tg.MessagesMessagesClass, error) {
	if r.deps.Messages == nil || req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return &tg.MessagesMessages{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if peer.Type == domain.PeerTypeChannel {
		// Channel rich text left to Phase 2 (the channel_message rich-text column
		// is prepared but not wired).
		return &tg.MessagesMessages{}, nil
	}
	list, err := r.deps.Messages.GetMessages(ctx, userID, []int{req.ID})
	if err != nil {
		return nil, internalErr()
	}
	out := make([]tg.MessageClass, 0, 1)
	found := make([]domain.Message, 0, 1)
	for _, msg := range list.Messages {
		if msg.ID != req.ID || msg.Peer != peer {
			continue
		}
		found = append(found, msg)
		out = append(out, tgMessage(msg))
	}
	if len(out) == 0 {
		out = append(out, &tg.MessageEmpty{ID: req.ID})
	}
	result := &tg.MessagesMessages{
		Messages: out,
		Users:    r.usersForMessageUpdatesWithPreloaded(ctx, userID, found, r.preloadedMessageUsers(list)),
		Chats:    r.chatsForMessageUpdates(ctx, userID, found),
	}
	r.applyPeerReadModelsToMessages(ctx, userID, result)
	return result, nil
}

func (r *Router) onMessagesSearchGlobal(ctx context.Context, req *tg.MessagesSearchGlobalRequest) (tg.MessagesMessagesClass, error) {
	if req.BroadcastsOnly && req.GroupsOnly {
		return &tg.MessagesMessages{}, nil
	}
	query := normalizeSearchQuery(req.Q)
	musicOnly := messagesSearchFilterMusic(req.Filter)
	communityInput, hasCommunity := req.GetCommunity()
	if !hasCommunity && req.Community != nil {
		communityInput, hasCommunity = req.Community, true
	}
	emptyCommunitySearch := query == "" && !musicOnly && hasCommunity && messagesSearchFilterEmpty(req.Filter)
	if query == "" && !musicOnly && !emptyCommunitySearch {
		return nil, searchQueryEmptyErr()
	}
	if utf8.RuneCountInString(query) > maxMessageSearchQLength {
		return nil, limitInvalidErr()
	}
	if !musicOnly && searchFilterNeedsMediaStore(req.Filter) {
		return &tg.MessagesMessages{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	var communityView *domain.CommunityView
	var communityScope domain.CommunitySearchScope
	if hasCommunity {
		view, err := r.communityFromInput(ctx, userID, communityInput)
		if err != nil {
			return nil, err
		}
		scope, err := r.deps.Communities.SearchScope(ctx, userID, view.Community.ID)
		if err != nil {
			return nil, communityErr(err)
		}
		communityView, communityScope = &view, scope
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelGlobalSearchLimit {
		limit = domain.MaxChannelGlobalSearchLimit
	}
	folderID, hasFolderID := req.GetFolderID()
	if hasFolderID && folderID < 0 {
		return nil, folderIDInvalidErr()
	}
	channelOffsetID, err := r.searchGlobalChannelOffsetID(ctx, userID, req.OffsetPeer)
	if err != nil {
		return nil, err
	}
	if emptyCommunitySearch {
		result := appendCommunitySearchChat(&tg.MessagesMessages{}, communityView)
		r.applyPeerReadModelsToMessages(ctx, userID, result)
		return result, nil
	}
	var private domain.MessageList
	if !req.BroadcastsOnly && !req.GroupsOnly && r.deps.Messages != nil {
		filter := domain.MessageFilter{
			Query:      query,
			OffsetID:   req.OffsetID,
			OffsetDate: req.OffsetRate,
			Limit:      limit + 1,
			MusicOnly:  musicOnly,
		}
		if communityView != nil {
			filter.RestrictPeerIDs = true
			filter.PeerIDs = communityScope.BotUserIDs
		}
		if req.MaxDate > 0 {
			filter.OffsetDate = req.MaxDate
		}
		if req.UsersOnly || !req.BroadcastsOnly && !req.GroupsOnly {
			private, err = r.deps.Messages.Search(ctx, userID, filter)
			if err != nil {
				return nil, internalErr()
			}
			private = r.enrichMessageList(ctx, userID, private)
		}
	}
	if req.UsersOnly || r.deps.Channels == nil {
		result := appendCommunitySearchChat(r.tgMessagesMessages(ctx, userID, r.enrichMessageList(ctx, userID, limitMessageList(private, limit))), communityView)
		return result, nil
	}
	channelHistory, err := r.deps.Channels.SearchJoinedMessages(ctx, userID, domain.ChannelGlobalSearchRequest{
		Query:              query,
		ChannelIDs:         communityScope.ChannelIDs,
		RestrictChannelIDs: communityView != nil,
		AllowPublicPreview: communityView != nil,
		BroadcastsOnly:     req.BroadcastsOnly,
		GroupsOnly:         req.GroupsOnly,
		MusicOnly:          musicOnly,
		HasFolderID:        hasFolderID,
		FolderID:           folderID,
		OffsetRate:         req.OffsetRate,
		OffsetChannelID:    channelOffsetID,
		OffsetID:           req.OffsetID,
		MinDate:            req.MinDate,
		MaxDate:            req.MaxDate,
		Limit:              limit,
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	channelHistory = r.enrichChannelHistory(ctx, userID, channelHistory)
	if req.BroadcastsOnly || req.GroupsOnly {
		return appendCommunitySearchChat(r.tgGlobalChannelMessages(ctx, userID, limitChannelHistory(channelHistory, limit)), communityView), nil
	}
	return appendCommunitySearchChat(r.tgGlobalSearchMessages(ctx, userID, limit, private, channelHistory), communityView), nil
}

func appendCommunitySearchChat(result tg.MessagesMessagesClass, view *domain.CommunityView) tg.MessagesMessagesClass {
	if result == nil || view == nil {
		return result
	}
	chat := tgCommunityChat(*view)
	switch out := result.(type) {
	case *tg.MessagesMessages:
		out.Chats = appendUniqueTGChats(out.Chats, chat)
	case *tg.MessagesMessagesSlice:
		out.Chats = appendUniqueTGChats(out.Chats, chat)
	case *tg.MessagesChannelMessages:
		out.Chats = appendUniqueTGChats(out.Chats, chat)
	}
	return result
}

func limitMessageList(list domain.MessageList, limit int) domain.MessageList {
	if limit <= 0 {
		limit = domain.MaxChannelGlobalSearchLimit
	}
	if len(list.Messages) > limit {
		list.Messages = list.Messages[:limit]
		list.Count = limit + 1
	}
	return list
}

func limitChannelHistory(history domain.ChannelHistory, limit int) domain.ChannelHistory {
	if limit <= 0 {
		limit = domain.MaxChannelGlobalSearchLimit
	}
	if len(history.Messages) > limit {
		history.Messages = history.Messages[:limit]
		history.Count = limit + 1
	}
	return history
}

func tgGlobalChannelMessages(viewerUserID int64, history domain.ChannelHistory) tg.MessagesMessagesClass {
	out := tgChannelHistoryMessages(viewerUserID, history)
	if slice, ok := out.(*tg.MessagesMessagesSlice); ok && len(history.Messages) > 0 {
		slice.SetNextRate(history.Messages[len(history.Messages)-1].Date)
	}
	return out
}

func tgGlobalSearchMessages(viewerUserID int64, limit int, private domain.MessageList, channel domain.ChannelHistory) tg.MessagesMessagesClass {
	if limit <= 0 {
		limit = domain.MaxChannelGlobalSearchLimit
	}
	hits := make([]globalSearchHit, 0, len(private.Messages)+len(channel.Messages))
	for _, msg := range private.Messages {
		item := tgMessage(msg)
		if item == nil {
			continue
		}
		hits = append(hits, globalSearchHit{
			date:      msg.Date,
			peerRank:  msg.Peer.ID,
			messageID: msg.ID,
			message:   item,
		})
	}
	for _, msg := range channel.Messages {
		item := tgChannelMessage(viewerUserID, msg)
		if item == nil {
			continue
		}
		hits = append(hits, globalSearchHit{
			date:      msg.Date,
			peerRank:  msg.ChannelID,
			messageID: msg.ID,
			message:   item,
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.date != b.date {
			return a.date > b.date
		}
		if a.peerRank != b.peerRank {
			return a.peerRank > b.peerRank
		}
		return a.messageID > b.messageID
	})
	hasMore := private.Count > len(private.Messages) || channel.Count > len(channel.Messages) || len(hits) > limit
	if len(hits) > limit {
		hits = hits[:limit]
	}
	messages := make([]tg.MessageClass, 0, len(hits))
	for _, hit := range hits {
		messages = append(messages, hit.message)
	}
	// When a global search hits a message the viewer sent, the viewer appears in
	// users and must carry the self flag.
	users := append(tgUsersForViewer(viewerUserID, private.Users), tgUsersForViewer(viewerUserID, channel.Users)...)
	chats := tgChannels(viewerUserID, channel.Channels)
	if hasMore {
		out := &tg.MessagesMessagesSlice{
			Count:    limit + 1,
			Messages: messages,
			Chats:    chats,
			Users:    users,
		}
		if len(hits) > 0 {
			out.SetNextRate(hits[len(hits)-1].date)
		}
		return out
	}
	return &tg.MessagesMessages{Messages: messages, Chats: chats, Users: users}
}

func (r *Router) messageFilterFromHistoryRequest(ctx context.Context, userID int64, req *tg.MessagesGetHistoryRequest) (domain.MessageFilter, error) {
	if err := validateMessageReadBounds(req.Limit, req.OffsetID, req.MaxID, req.MinID); err != nil {
		return domain.MessageFilter{}, err
	}
	peer, err := r.checkedMessageReadPeer(ctx, userID, req.Peer, false)
	if err != nil {
		return domain.MessageFilter{}, err
	}
	limit := req.Limit
	if limit > 50 {
		limit = 50
	}
	return domain.MessageFilter{
		HasPeer:    true,
		Peer:       peer,
		OffsetID:   req.OffsetID,
		OffsetDate: req.OffsetDate,
		AddOffset:  domain.ClampMessageHistoryAddOffset(req.AddOffset),
		Limit:      limit,
		MaxID:      req.MaxID,
		MinID:      req.MinID,
		Hash:       req.Hash,
	}, nil
}

func (r *Router) messageFilterFromSearchRequest(ctx context.Context, userID int64, req *tg.MessagesSearchRequest) (domain.MessageFilter, error) {
	if err := validateMessageReadBounds(req.Limit, req.OffsetID, req.MaxID, req.MinID); err != nil {
		return domain.MessageFilter{}, err
	}
	limit := req.Limit
	if limit > 500 {
		limit = 500
	}
	filter := domain.MessageFilter{
		Query:          req.Q,
		OffsetID:       req.OffsetID,
		MinDate:        req.MinDate,
		MaxDate:        req.MaxDate,
		AddOffset:      domain.ClampMessageHistoryAddOffset(req.AddOffset),
		Limit:          limit,
		CountOnly:      req.Limit == 0,
		MaxID:          req.MaxID,
		MinID:          req.MinID,
		Hash:           req.Hash,
		MusicOnly:      messagesSearchFilterMusic(req.Filter),
		NeedTotalCount: req.OffsetID == 0 && req.MinDate == 0 && req.MaxDate == 0 && req.AddOffset >= 0 && req.Hash == 0,
	}
	if phoneCalls, ok := req.Filter.(*tg.InputMessagesFilterPhoneCalls); ok {
		filter.PhoneCallsOnly = true
		filter.MissedPhoneCallsOnly = phoneCalls.Missed
	}
	if empty, ok := req.Peer.(*tg.InputPeerEmpty); !ok || empty == nil {
		peer, err := r.checkedMessageReadPeer(ctx, userID, req.Peer, false)
		if err != nil {
			return domain.MessageFilter{}, err
		}
		filter.HasPeer = true
		filter.Peer = peer
	}
	_, hasFrom := req.GetFromID()
	if req.FromID != nil || hasFrom {
		from, err := r.checkedMessageReadPeer(ctx, userID, req.FromID, true)
		if err != nil {
			return domain.MessageFilter{}, err
		}
		filter.SenderUserID = from.ID
	}
	savedReactions, hasSavedReactions := req.GetSavedReaction()
	// An empty optional vector carries no reaction-filtering semantics. Some TL
	// clients emit flags.3 with a zero-length vector on ordinary peer searches.
	// Keep the wire presence intact at the TL edge, but only apply Saved
	// Messages scope and reaction validation when the vector has values.
	hasSavedReactionFilter := hasSavedReactions && len(savedReactions) > 0
	savedPeerInput, hasSavedPeer := req.GetSavedPeerID()
	if hasSavedReactionFilter || hasSavedPeer {
		if !filter.HasPeer ||
			filter.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: userID}) {
			return domain.MessageFilter{}, peerIDInvalidErr()
		}
	}
	if hasSavedPeer {
		if savedPeerInput == nil {
			return domain.MessageFilter{}, peerIDInvalidErr()
		}
		savedPeer, err := r.checkedMessageReadPeer(ctx, userID, savedPeerInput, false)
		if err != nil {
			return domain.MessageFilter{}, err
		}
		filter.SavedPeer = savedPeer
	}
	if hasSavedReactionFilter {
		if len(savedReactions) > maxReactionVector {
			return domain.MessageFilter{}, reactionInvalidErr()
		}
		seen := make(map[string]struct{}, len(savedReactions))
		for _, item := range savedReactions {
			reaction, err := domainMessageReactionFromTL(item)
			if err != nil {
				return domain.MessageFilter{}, err
			}
			if _, ok := seen[reaction.Key()]; ok {
				continue
			}
			seen[reaction.Key()] = struct{}{}
			filter.SavedReactions = append(filter.SavedReactions, reaction)
		}
	}
	return filter, nil
}

func (r *Router) channelHistoryFilterFromSearchRequest(userID int64, req *tg.MessagesSearchRequest, channelID int64) (domain.ChannelHistoryFilter, bool) {
	limit := req.Limit
	countOnly := limit == 0
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	filter := domain.ChannelHistoryFilter{
		ChannelID:  channelID,
		Query:      req.Q,
		PinnedOnly: messagesSearchFilterPinned(req.Filter),
		MusicOnly:  messagesSearchFilterMusic(req.Filter),
		NeedTotalCount: countOnly ||
			(req.OffsetID == 0 && req.MinDate == 0 && req.MaxDate == 0 && req.AddOffset >= 0 && req.Hash == 0),
		CountOnly: countOnly,
		OffsetID:  req.OffsetID,
		AddOffset: domain.ClampMessageHistoryAddOffset(req.AddOffset),
		Limit:     limit,
		MinDate:   req.MinDate,
		MaxDate:   req.MaxDate,
		MaxID:     req.MaxID,
		MinID:     req.MinID,
		Hash:      req.Hash,
	}
	if req.FromID != nil {
		from, ok := r.domainPeerFromInputPeer(userID, req.FromID)
		if !ok || from.Type != domain.PeerTypeUser || from.ID == 0 {
			return domain.ChannelHistoryFilter{}, false
		}
		filter.SenderUserID = from.ID
	}
	return filter, true
}

func messagesSearchFilterPinned(filter tg.MessagesFilterClass) bool {
	_, ok := filter.(*tg.InputMessagesFilterPinned)
	return ok
}

func messagesSearchFilterMusic(filter tg.MessagesFilterClass) bool {
	_, ok := filter.(*tg.InputMessagesFilterMusic)
	return ok
}

func messagesSearchFilterEmpty(filter tg.MessagesFilterClass) bool {
	_, ok := filter.(*tg.InputMessagesFilterEmpty)
	return ok
}

func messagesSearchFilterChatPhotos(filter tg.MessagesFilterClass) bool {
	_, ok := filter.(*tg.InputMessagesFilterChatPhotos)
	return ok
}

func searchFilterNeedsMediaStore(filter tg.MessagesFilterClass) bool {
	switch filter.(type) {
	case nil, *tg.InputMessagesFilterEmpty, *tg.InputMessagesFilterPhoneCalls:
		return false
	case *tg.InputMessagesFilterPhotos,
		*tg.InputMessagesFilterVideo,
		*tg.InputMessagesFilterPhotoVideo,
		*tg.InputMessagesFilterDocument,
		*tg.InputMessagesFilterMusic,
		*tg.InputMessagesFilterURL,
		*tg.InputMessagesFilterGif,
		*tg.InputMessagesFilterVoice,
		*tg.InputMessagesFilterRoundVoice,
		*tg.InputMessagesFilterRoundVideo,
		*tg.InputMessagesFilterPoll:
		return true
	default:
		return false
	}
}

// mediaCategoriesForFilter maps a client shared-media tab filter to the union of
// base media-index categories. Composite tabs (PhotoVideo / RoundVoice) map to
// several base categories; returning nil means the filter does not use the media
// index.
func mediaCategoriesForFilter(filter tg.MessagesFilterClass) []domain.MediaCategory {
	switch filter.(type) {
	case *tg.InputMessagesFilterPhotos:
		return []domain.MediaCategory{domain.MediaCategoryPhoto}
	case *tg.InputMessagesFilterVideo:
		return []domain.MediaCategory{domain.MediaCategoryVideo}
	case *tg.InputMessagesFilterPhotoVideo:
		return []domain.MediaCategory{domain.MediaCategoryPhoto, domain.MediaCategoryVideo}
	case *tg.InputMessagesFilterDocument:
		return []domain.MediaCategory{domain.MediaCategoryFile}
	case *tg.InputMessagesFilterMusic:
		return []domain.MediaCategory{domain.MediaCategoryMusic}
	case *tg.InputMessagesFilterURL:
		return []domain.MediaCategory{domain.MediaCategoryURL}
	case *tg.InputMessagesFilterGif:
		return []domain.MediaCategory{domain.MediaCategoryGif}
	case *tg.InputMessagesFilterVoice:
		return []domain.MediaCategory{domain.MediaCategoryVoice}
	case *tg.InputMessagesFilterRoundVideo:
		return []domain.MediaCategory{domain.MediaCategoryRoundVideo}
	case *tg.InputMessagesFilterRoundVoice:
		return []domain.MediaCategory{domain.MediaCategoryVoice, domain.MediaCategoryRoundVideo}
	case *tg.InputMessagesFilterPoll:
		return []domain.MediaCategory{domain.MediaCategoryPoll}
	default:
		return nil
	}
}

func messagesNotModifiedOrEmpty(hash int64) tg.MessagesMessagesClass {
	if hash != 0 {
		return &tg.MessagesMessagesNotModified{Count: 0}
	}
	return &tg.MessagesMessages{}
}
