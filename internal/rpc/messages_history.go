package rpc

import (
	"context"
	"errors"
	"github.com/gotd/td/tg"
	"sort"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesSearchStickerSets(ctx context.Context, req *tg.MessagesSearchStickerSetsRequest) (tg.MessagesFoundStickerSetsClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if utf8.RuneCountInString(req.Q) > maxStickerSearchQLength {
		return nil, limitInvalidErr()
	}
	if req.Hash != 0 {
		return &tg.MessagesFoundStickerSetsNotModified{}, nil
	}
	return &tg.MessagesFoundStickerSets{
		Hash: 0,
		Sets: []tg.StickerSetCoveredClass{},
	}, nil
}

func (r *Router) onMessagesSearchStickers(ctx context.Context, req *tg.MessagesSearchStickersRequest) (tg.MessagesFoundStickersClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	if req.Offset < 0 || req.Offset > domain.MaxMessageBoxID || req.Limit < 0 || req.Limit > maxSearchResultsLimit {
		return nil, limitInvalidErr()
	}
	if utf8.RuneCountInString(req.Q) > maxStickerSearchQLength || utf8.RuneCountInString(req.Emoticon) > maxStickerSearchQLength {
		return nil, limitInvalidErr()
	}
	if len(req.LangCode) > maxStickerSearchLangs {
		return nil, limitInvalidErr()
	}
	for _, lang := range req.LangCode {
		if err := validateEmojiLangCode(lang); err != nil {
			return nil, err
		}
	}
	if req.Hash != 0 {
		return &tg.MessagesFoundStickersNotModified{}, nil
	}
	return &tg.MessagesFoundStickers{
		Hash:     0,
		Stickers: []tg.DocumentClass{},
	}, nil
}

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
	pinnedCount := 0
	mediaCounts := domain.MediaCategoryCounts{}
	if peer.Type == domain.PeerTypeChannel && r.deps.Channels != nil {
		// 只读 PinnedMessageID（Channel 字段）：走轻量 ResolveChannel，省 dialog/读态/boost 查询。
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, peer.ID)
		if err != nil {
			return nil, err
		}
		if view.Channel.PinnedMessageID > 0 {
			pinnedCount = 1
		}
		counts, err := r.mediaCountsForPeer(ctx, userID, peer)
		if err != nil {
			return nil, err
		}
		mediaCounts = counts
	}
	if peer.Type == domain.PeerTypeUser && r.deps.Messages != nil {
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
	return &tg.MessagesMessages{
		Messages: []tg.MessageClass{},
		Chats:    r.chatsForInputPeer(ctx, userID, req.Peer),
		Users:    []tg.UserClass{},
	}, nil
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
	// 解析讨论帖/回复作者的 user/channel 实体（store 不填 Users），否则客户端拿不到
	// 作者实体需额外 getUser 兜底。
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
	// forum 话题已读：req.MsgID 是话题 id，推进 per-topic 水位（不碰频道级，消除话题间已读串扰），
	// 向自己其它设备下发 DiscussionInbox、向话题内发送者下发 DiscussionOutbox 回执。不经
	// GetDiscussionMessage，避免话题根消息被裁剪/删除时整条已读 400（root 不存活不应阻塞标已读）。
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
		// 保守叠加：同时推进频道级 inbox 水位，保持 getDialogs/getPeerDialogs 会话总未读不退化。
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
	// 非 forum（频道-讨论组 linked comments）：只解析 target/root/boundary，禁止为了一个
	// 已读请求加载完整 discussion message、reply stats、reactions 与 unread aggregates。
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
	requestedIDs := make([]int, 0, len(ids))
	for _, input := range ids {
		id, ok := inputMessageBoxID(input)
		if !ok || id <= 0 || id > domain.MaxMessageBoxID {
			continue
		}
		requestedIDs = append(requestedIDs, id)
	}
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
	chats := r.chatsForMessageUpdates(ctx, userID, found)
	return &tg.MessagesMessages{
		Messages: out,
		Users:    r.usersForMessageUpdates(ctx, userID, found),
		Chats:    chats,
	}, nil
}

// onMessagesGetRichMessage 返回单条消息的完整富文本（Layer 227 richMessage）。消息列表
// 投影里已带 richMessage（tgMessage），本 RPC 是客户端按 (peer,id) 拉取完整富文本的入口。
// Phase 1 仅私聊：按 box id 从请求者自己的消息盒取出并投影；频道侧富文本留 Phase 2。
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
		// 频道富文本留 Phase 2（channel_message 富文本列已备未接线）。
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
	return &tg.MessagesMessages{
		Messages: out,
		Users:    r.usersForMessageUpdates(ctx, userID, found),
		Chats:    r.chatsForMessageUpdates(ctx, userID, found),
	}, nil
}

func (r *Router) onMessagesSearchGlobal(ctx context.Context, req *tg.MessagesSearchGlobalRequest) (tg.MessagesMessagesClass, error) {
	if req.BroadcastsOnly && req.GroupsOnly {
		return &tg.MessagesMessages{}, nil
	}
	query := normalizeSearchQuery(req.Q)
	musicOnly := messagesSearchFilterMusic(req.Filter)
	if query == "" && !musicOnly {
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
	var private domain.MessageList
	if !req.BroadcastsOnly && !req.GroupsOnly && r.deps.Messages != nil {
		filter := domain.MessageFilter{
			Query:      query,
			OffsetID:   req.OffsetID,
			OffsetDate: req.OffsetRate,
			Limit:      limit + 1,
			MusicOnly:  musicOnly,
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
		return tgMessagesMessages(userID, r.enrichMessageList(ctx, userID, limitMessageList(private, limit))), nil
	}
	channelHistory, err := r.deps.Channels.SearchJoinedMessages(ctx, userID, domain.ChannelGlobalSearchRequest{
		Query:           query,
		BroadcastsOnly:  req.BroadcastsOnly,
		GroupsOnly:      req.GroupsOnly,
		MusicOnly:       musicOnly,
		HasFolderID:     hasFolderID,
		FolderID:        folderID,
		OffsetRate:      req.OffsetRate,
		OffsetChannelID: channelOffsetID,
		OffsetID:        req.OffsetID,
		MinDate:         req.MinDate,
		MaxDate:         req.MaxDate,
		Limit:           limit,
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	channelHistory = r.enrichChannelHistory(ctx, userID, channelHistory)
	if req.BroadcastsOnly || req.GroupsOnly {
		return r.tgGlobalChannelMessages(ctx, userID, limitChannelHistory(channelHistory, limit)), nil
	}
	return r.tgGlobalSearchMessages(ctx, userID, limit, private, channelHistory), nil
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
	// 全局搜索命中自己发的消息时 viewer 自己会出现在 users 里，须带 self 标志。
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

func (r *Router) messageFilterFromHistoryRequest(userID int64, req *tg.MessagesGetHistoryRequest) (domain.MessageFilter, bool) {
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok {
		return domain.MessageFilter{}, false
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
	}, true
}

func (r *Router) messageFilterFromSearchRequest(userID int64, req *tg.MessagesSearchRequest) domain.MessageFilter {
	limit := req.Limit
	if limit > 500 {
		limit = 500
	}
	filter := domain.MessageFilter{
		Query:          req.Q,
		OffsetID:       req.OffsetID,
		AddOffset:      domain.ClampMessageHistoryAddOffset(req.AddOffset),
		Limit:          limit,
		MaxID:          req.MaxID,
		MinID:          req.MinID,
		Hash:           req.Hash,
		MusicOnly:      messagesSearchFilterMusic(req.Filter),
		NeedTotalCount: req.OffsetID == 0 && req.MinDate == 0 && req.MaxDate == 0 && req.AddOffset >= 0 && req.Hash == 0,
	}
	if peer, ok := r.domainPeerFromInputPeer(userID, req.Peer); ok {
		filter.HasPeer = true
		filter.Peer = peer
	}
	return filter
}

func (r *Router) channelHistoryFilterFromSearchRequest(userID int64, req *tg.MessagesSearchRequest, channelID int64) (domain.ChannelHistoryFilter, bool) {
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	filter := domain.ChannelHistoryFilter{
		ChannelID:  channelID,
		Query:      req.Q,
		PinnedOnly: messagesSearchFilterPinned(req.Filter),
		MusicOnly:  messagesSearchFilterMusic(req.Filter),
		OffsetID:   req.OffsetID,
		AddOffset:  domain.ClampMessageHistoryAddOffset(req.AddOffset),
		Limit:      limit,
		MinDate:    req.MinDate,
		MaxDate:    req.MaxDate,
		MaxID:      req.MaxID,
		MinID:      req.MinID,
		Hash:       req.Hash,
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

func messagesSearchFilterChatPhotos(filter tg.MessagesFilterClass) bool {
	_, ok := filter.(*tg.InputMessagesFilterChatPhotos)
	return ok
}

func searchFilterNeedsMediaStore(filter tg.MessagesFilterClass) bool {
	switch filter.(type) {
	case nil, *tg.InputMessagesFilterEmpty:
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

// mediaCategoriesForFilter 把客户端共享媒体标签页过滤器映射为媒体索引的基础类别并集。
// 复合标签页(PhotoVideo / RoundVoice)映射为多个基础类别;返回 nil 表示该过滤器不走媒体索引。
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
