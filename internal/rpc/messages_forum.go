package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/tg"
	"strings"
	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
	"unicode/utf8"
)

func (r *Router) onMessagesCreateForumTopic(ctx context.Context, req *tg.MessagesCreateForumTopicRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateForumTopicTitle(req.Title, req.TitleMissing); err != nil {
		return nil, err
	}
	if req.RandomID == 0 {
		return nil, randomIDEmptyErr()
	}
	peer, err := r.forumTopicPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, internalErr()
	}
	var sendAs *domain.Peer
	if req.SendAs != nil {
		sendAs, err = r.forumSendAsPeer(ctx, req.Peer, req.SendAs)
		if err != nil {
			return nil, err
		}
	}
	res, err := r.deps.Channels.CreateForumTopic(ctx, userID, domain.CreateChannelForumTopicRequest{
		UserID:       userID,
		ChannelID:    peer.ID,
		Title:        strings.TrimSpace(req.Title),
		TitleMissing: req.TitleMissing,
		IconColor:    req.IconColor,
		IconEmojiID:  req.IconEmojiID,
		RandomID:     req.RandomID,
		SendAs:       sendAs,
		Date:         int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, forumTopicError(err)
	}
	sendRes := domain.SendChannelMessageResult{
		Channel:    res.Channel,
		Message:    res.Message,
		Event:      res.Event,
		Recipients: res.Recipients,
		Duplicate:  res.Duplicate,
	}
	echoCache := newViewerPeerCache(r)
	updates := r.channelMessageUpdatesWithPeerCache(ctx, userID, sendRes, req.RandomID, echoCache)
	if !res.Duplicate {
		r.enqueueChannelMessageFanout(ctx, userID, sendRes, nil)
	}
	return updates, nil
}

func (r *Router) onMessagesEditForumTopic(ctx context.Context, req *tg.MessagesEditForumTopicRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if title, ok := req.GetTitle(); ok {
		if err := validateForumTopicTitle(title, false); err != nil {
			return nil, err
		}
	}
	if req.TopicID <= 0 || req.TopicID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.forumTopicPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, internalErr()
	}
	edit := domain.EditChannelForumTopicRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		TopicID:   req.TopicID,
		Date:      int(r.clock.Now().Unix()),
	}
	if title, ok := req.GetTitle(); ok {
		edit.Title = &title
	}
	if iconEmojiID, ok := req.GetIconEmojiID(); ok {
		edit.IconEmojiID = &iconEmojiID
	}
	if closed, ok := req.GetClosed(); ok {
		edit.Closed = &closed
	}
	if hidden, ok := req.GetHidden(); ok {
		edit.Hidden = &hidden
	}
	res, err := r.deps.Channels.EditForumTopic(ctx, userID, edit)
	if err != nil {
		return nil, forumTopicError(err)
	}
	sendRes := domain.SendChannelMessageResult{
		Channel:    res.Channel,
		Message:    res.Message,
		Event:      res.Event,
		Recipients: res.Recipients,
	}
	echoCache := newViewerPeerCache(r)
	updates := r.channelMessageUpdatesWithPeerCache(ctx, userID, sendRes, 0, echoCache)
	r.enqueueChannelMessageFanout(ctx, userID, sendRes, nil)
	return updates, nil
}

func (r *Router) onMessagesUpdatePinnedForumTopic(ctx context.Context, req *tg.MessagesUpdatePinnedForumTopicRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.TopicID <= 0 || req.TopicID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.forumTopicPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, internalErr()
	}
	res, err := r.deps.Channels.UpdatePinnedForumTopic(ctx, userID, domain.UpdateChannelForumTopicPinnedRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		TopicID:   req.TopicID,
		Pinned:    req.Pinned,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, forumTopicError(err)
	}
	updates := r.pinnedForumTopicUpdates(userID, res.Channel, res.Topic.TopicID, res.Topic.Pinned)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.pinnedForumTopicUpdates(viewerUserID, res.Channel, res.Topic.TopicID, res.Topic.Pinned)
	})
	return updates, nil
}

func (r *Router) onMessagesReorderPinnedForumTopics(ctx context.Context, req *tg.MessagesReorderPinnedForumTopicsRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := validateMessageIDVector(req.Order); err != nil {
		return nil, err
	}
	peer, err := r.forumTopicPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, internalErr()
	}
	res, err := r.deps.Channels.ReorderPinnedForumTopics(ctx, userID, domain.ReorderChannelPinnedForumTopicsRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		Order:     append([]int(nil), req.Order...),
		Force:     req.Force,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, forumTopicError(err)
	}
	updates := r.pinnedForumTopicsOrderUpdates(userID, res.Channel, res.Order)
	r.pushChannelUpdates(ctx, userID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.pinnedForumTopicsOrderUpdates(viewerUserID, res.Channel, res.Order)
	})
	return updates, nil
}

func (r *Router) onMessagesDeleteTopicHistory(ctx context.Context, req *tg.MessagesDeleteTopicHistoryRequest) (*tg.MessagesAffectedHistory, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.TopMsgID <= 0 || req.TopMsgID > domain.MaxMessageBoxID {
		return nil, messageIDInvalidErr()
	}
	peer, err := r.forumTopicPeer(ctx, req.Peer)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, internalErr()
	}
	res, err := r.deps.Channels.DeleteForumTopicHistory(ctx, userID, domain.DeleteChannelForumTopicHistoryRequest{
		UserID:    userID,
		ChannelID: peer.ID,
		TopicID:   req.TopMsgID,
		Date:      int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, forumTopicError(err)
	}
	if res.Event.Pts != 0 {
		r.enqueueChannelFanout(ctx, channelFanoutMessageBox, userID, res.Channel.ID, res.Event.Pts, res.Recipients, func(_ context.Context, viewerUserID int64) *tg.Updates {
			return &tg.Updates{
				Updates: []tg.UpdateClass{tgChannelUpdate(viewerUserID, res.Event)},
				Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, res.Channel)},
				Date:    res.Event.Date,
				Seq:     0,
			}
		})
		return &tg.MessagesAffectedHistory{Pts: res.Event.Pts, PtsCount: res.Event.PtsCount, Offset: res.Offset}, nil
	}
	return &tg.MessagesAffectedHistory{Pts: res.Channel.Pts, PtsCount: 0, Offset: res.Offset}, nil
}

func validateMessageIDVector(ids []int) error {
	if len(ids) > maxGetMessagesIDs {
		return limitInvalidErr()
	}
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return messageIDInvalidErr()
		}
	}
	return nil
}

func validateForumTopicTitle(title string, titleMissing bool) error {
	title = strings.TrimSpace(title)
	if title == "" && !titleMissing {
		return topicTitleEmptyErr()
	}
	if utf8.RuneCountInString(title) > maxForumTopicTitleLength {
		return limitInvalidErr()
	}
	return nil
}

func forumTopicError(err error) error {
	switch {
	case errors.Is(err, domain.ErrChannelForumMissing):
		return channelForumMissingErr()
	case errors.Is(err, domain.ErrMessageIDInvalid):
		return topicIDInvalidErr()
	case errors.Is(err, domain.ErrChannelNotModified):
		return tgerr400("CHAT_NOT_MODIFIED")
	default:
		return channelInvalidErr(err)
	}
}

func (r *Router) forumTopicPeer(ctx context.Context, input tg.InputPeerClass) (domain.Peer, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return domain.Peer{}, internalErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, userID, input)
	if err != nil {
		return domain.Peer{}, err
	}
	if peer.Type != domain.PeerTypeChannel {
		return domain.Peer{}, peerIDInvalidErr()
	}
	return peer, nil
}

func (r *Router) onMessagesGetForumTopics(ctx context.Context, req *tg.MessagesGetForumTopicsRequest) (*tg.MessagesForumTopics, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if req.Limit < 0 || utf8.RuneCountInString(req.Q) > maxMessageSearchQLength {
		return nil, limitInvalidErr()
	}
	// 官方客户端会请求超过单页上限的数量(telegram-tt 首屏 loadTopics 发 limit=500),对齐官方与
	// store 行为=钳到服务端单页上限,而非报 LIMIT_INVALID;客户端据响应里的 count 用 offset 翻页取其余。
	effectiveLimit := req.Limit
	if effectiveLimit > domain.MaxChannelForumTopicsLimit {
		effectiveLimit = domain.MaxChannelForumTopicsLimit
	}
	view, err := r.forumTopicPeerView(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if !view.Channel.Forum {
		return nil, channelForumMissingErr()
	}
	includeGeneral := effectiveLimit > 0 && forumTopicQueryMatchesGeneral(req.Q)
	list := domain.ChannelForumTopicList{}
	if r.deps.Channels != nil && effectiveLimit > 0 {
		limit := effectiveLimit
		if includeGeneral {
			limit--
		}
		if limit > 0 {
			list, err = r.deps.Channels.GetForumTopics(ctx, userID, domain.ChannelForumTopicFilter{
				ChannelID:   view.Channel.ID,
				Query:       req.Q,
				OffsetDate:  req.OffsetDate,
				OffsetID:    req.OffsetID,
				OffsetTopic: req.OffsetTopic,
				Limit:       limit,
			})
			if err != nil {
				return nil, forumTopicError(err)
			}
		}
	}
	return r.forumTopicsResponse(ctx, userID, view, list, includeGeneral), nil
}

func (r *Router) onMessagesGetForumTopicsByID(ctx context.Context, req *tg.MessagesGetForumTopicsByIDRequest) (*tg.MessagesForumTopics, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if len(req.Topics) == 0 {
		return nil, topicsEmptyErr()
	}
	if len(req.Topics) > maxForumTopicIDs {
		return nil, limitInvalidErr()
	}
	view, err := r.forumTopicPeerView(ctx, userID, req.Peer)
	if err != nil {
		return nil, err
	}
	if !view.Channel.Forum {
		return nil, channelForumMissingErr()
	}
	includeGeneral := false
	requestedIDs := make([]int, 0, len(req.Topics))
	ids := make([]int, 0, len(req.Topics))
	seen := make(map[int]struct{}, len(req.Topics))
	for _, topicID := range req.Topics {
		if topicID <= 0 || topicID > domain.MaxMessageBoxID {
			return nil, messageIDInvalidErr()
		}
		if _, ok := seen[topicID]; ok {
			continue
		}
		seen[topicID] = struct{}{}
		requestedIDs = append(requestedIDs, topicID)
		if topicID == forumGeneralTopicID {
			includeGeneral = true
			continue
		}
		ids = append(ids, topicID)
	}
	list := domain.ChannelForumTopicList{}
	if r.deps.Channels != nil && len(ids) > 0 {
		list, err = r.deps.Channels.GetForumTopicsByID(ctx, userID, view.Channel.ID, ids)
		if err != nil {
			return nil, forumTopicError(err)
		}
	}
	return r.forumTopicsByIDResponse(ctx, userID, view, list, includeGeneral, requestedIDs), nil
}

// forumTopicsByIDResponse keeps the request's unique ID order and returns one
// constructor for every requested topic. Telegram clients use
// forumTopicDeleted as a positive deletion/missing confirmation; silently
// omitting an ID leaves their local thread state stale and causes repeat reads.
func (r *Router) forumTopicsByIDResponse(ctx context.Context, userID int64, view domain.ChannelView, list domain.ChannelForumTopicList, includeGeneral bool, requestedIDs []int) *tg.MessagesForumTopics {
	response := r.forumTopicsResponse(ctx, userID, view, list, includeGeneral)
	live := make(map[int]tg.ForumTopicClass, len(response.Topics))
	for _, topic := range response.Topics {
		switch topic := topic.(type) {
		case *tg.ForumTopic:
			live[topic.ID] = topic
		case *tg.ForumTopicDeleted:
			live[topic.ID] = topic
		}
	}
	response.Topics = make([]tg.ForumTopicClass, 0, len(requestedIDs))
	for _, topicID := range requestedIDs {
		if topic, ok := live[topicID]; ok {
			response.Topics = append(response.Topics, topic)
		} else {
			response.Topics = append(response.Topics, &tg.ForumTopicDeleted{ID: topicID})
		}
	}
	response.Count = len(response.Topics)
	return response
}

func (r *Router) forumTopicPeerView(ctx context.Context, userID int64, peer tg.InputPeerClass) (domain.ChannelView, error) {
	p, err := r.checkedDomainPeerFromInputPeer(ctx, userID, peer)
	if err != nil {
		return domain.ChannelView{}, err
	}
	if p.Type != domain.PeerTypeChannel || p.ID == 0 {
		return domain.ChannelView{}, peerIDInvalidErr()
	}
	if r.deps.Channels == nil {
		return domain.ChannelView{}, nil
	}
	view, err := r.deps.Channels.GetChannel(ctx, userID, p.ID)
	if err != nil {
		return domain.ChannelView{}, channelInvalidErr(err)
	}
	return view, nil
}

func (r *Router) forumTopicsResponse(ctx context.Context, userID int64, view domain.ChannelView, list domain.ChannelForumTopicList, includeGeneral bool) *tg.MessagesForumTopics {
	if list.Channel.ID != 0 {
		view.Channel = list.Channel
	}
	if list.Dialog.ChannelID != 0 {
		view.Dialog = list.Dialog
	}
	channels := []domain.Channel{view.Channel}
	messages := []tg.MessageClass{}
	messageIDs := map[int]struct{}{}
	topics := []tg.ForumTopicClass{}
	userIDs := make([]int64, 0, len(list.Topics)+len(list.Messages)+1)
	count := list.Count
	if includeGeneral {
		count++
		general := domain.ChannelForumTopic{TopMessageID: view.Channel.TopMessageID}
		if r.deps.Channels != nil {
			if g, err := r.deps.Channels.GeneralForumTopic(ctx, userID, view.Channel.ID); err == nil {
				general = g
			}
		}
		topics = append(topics, tgForumGeneralTopic(userID, view, general))
		userIDs = append(userIDs, view.Channel.CreatorUserID)
		if general.TopMessageID > 0 && r.deps.Channels != nil {
			if history, err := r.deps.Channels.GetMessages(ctx, userID, view.Channel.ID, []int{general.TopMessageID}); err == nil {
				for _, msg := range history.Messages {
					if _, ok := messageIDs[msg.ID]; ok {
						continue
					}
					messageIDs[msg.ID] = struct{}{}
					if item := tgChannelMessage(userID, msg); item != nil {
						messages = append(messages, item)
					}
					userIDs = append(userIDs, msg.SenderUserID)
				}
				channels = append(channels, history.Channels...)
				for _, u := range history.Users {
					userIDs = append(userIDs, u.ID)
				}
			}
		}
	}
	for _, topic := range list.Topics {
		topics = append(topics, tgForumTopicFromDomain(userID, topic))
		userIDs = append(userIDs, topic.CreatorUserID)
	}
	for _, msg := range list.Messages {
		if _, ok := messageIDs[msg.ID]; ok {
			continue
		}
		messageIDs[msg.ID] = struct{}{}
		if item := tgChannelMessage(userID, msg); item != nil {
			messages = append(messages, item)
		}
		userIDs = append(userIDs, msg.SenderUserID)
		if msg.SendAs != nil && msg.SendAs.Type == domain.PeerTypeUser {
			userIDs = append(userIDs, msg.SendAs.ID)
		}
	}
	for _, u := range list.Users {
		userIDs = append(userIDs, u.ID)
	}
	return r.applyStoryMaxIDsToForumTopics(ctx, userID, &tg.MessagesForumTopics{
		Count:    count,
		Topics:   topics,
		Messages: messages,
		Chats:    tgChannels(userID, channels),
		Users:    r.tgUsersForIDs(ctx, userID, userIDs),
		Pts:      view.Channel.Pts,
	})
}

func tgForumGeneralTopic(viewerUserID int64, view domain.ChannelView, topic domain.ChannelForumTopic) *tg.ForumTopic {
	return &tg.ForumTopic{
		My:                   view.Channel.CreatorUserID == viewerUserID && viewerUserID != 0,
		ID:                   forumGeneralTopicID,
		Date:                 view.Channel.Date,
		Peer:                 &tg.PeerChannel{ChannelID: view.Channel.ID},
		Title:                "General",
		IconColor:            forumGeneralIconColor,
		TopMessage:           topic.TopMessageID,
		ReadInboxMaxID:       topic.ReadInboxMaxID,
		ReadOutboxMaxID:      topic.ReadOutboxMaxID,
		UnreadCount:          topic.UnreadCount,
		UnreadMentionsCount:  topic.UnreadMentionsCount,
		UnreadReactionsCount: topic.UnreadReactionsCount,
		FromID:               &tg.PeerUser{UserID: view.Channel.CreatorUserID},
		NotifySettings:       *tdesktop.NotifySettings(),
	}
}

func tgForumTopicFromDomain(viewerUserID int64, topic domain.ChannelForumTopic) *tg.ForumTopic {
	iconColor := topic.IconColor
	if iconColor == 0 {
		iconColor = domain.DefaultForumTopicIconColor
	}
	return &tg.ForumTopic{
		My:                   topic.CreatorUserID == viewerUserID && viewerUserID != 0,
		Closed:               topic.Closed,
		Pinned:               topic.Pinned,
		Hidden:               topic.Hidden,
		TitleMissing:         topic.TitleMissing,
		ID:                   topic.TopicID,
		Date:                 topic.Date,
		Peer:                 &tg.PeerChannel{ChannelID: topic.ChannelID},
		Title:                topic.Title,
		IconColor:            iconColor,
		IconEmojiID:          topic.IconEmojiID,
		TopMessage:           topic.TopMessageID,
		ReadInboxMaxID:       topic.ReadInboxMaxID,
		ReadOutboxMaxID:      topic.ReadOutboxMaxID,
		UnreadCount:          topic.UnreadCount,
		UnreadMentionsCount:  topic.UnreadMentionsCount,
		UnreadReactionsCount: topic.UnreadReactionsCount,
		UnreadPollVotesCount: topic.UnreadPollVotesCount,
		FromID:               &tg.PeerUser{UserID: topic.CreatorUserID},
		NotifySettings:       *tdesktop.NotifySettings(),
	}
}

func (r *Router) pinnedForumTopicUpdates(viewerUserID int64, channel domain.Channel, topicID int, pinned bool) *tg.Updates {
	update := &tg.UpdatePinnedForumTopic{
		Peer:    &tg.PeerChannel{ChannelID: channel.ID},
		TopicID: topicID,
	}
	update.SetPinned(pinned)
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func (r *Router) pinnedForumTopicsOrderUpdates(viewerUserID int64, channel domain.Channel, order []int) *tg.Updates {
	update := &tg.UpdatePinnedForumTopics{
		Peer:  &tg.PeerChannel{ChannelID: channel.ID},
		Order: append([]int(nil), order...),
	}
	if order != nil {
		update.SetOrder(append([]int(nil), order...))
	}
	return &tg.Updates{
		Updates: []tg.UpdateClass{update},
		Chats:   []tg.ChatClass{tgChannelChatMin(viewerUserID, channel)},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

func forumTopicQueryMatchesGeneral(query string) bool {
	query = strings.TrimSpace(strings.ToLower(query))
	return query == "" || strings.Contains("general", query)
}
