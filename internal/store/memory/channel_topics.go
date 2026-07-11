package memory

import (
	"context"
	"sort"
	"strings"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) SetForum(_ context.Context, userID, channelID int64, enabled, tabs bool) (domain.Channel, error) {
	if userID == 0 || channelID == 0 {
		return domain.Channel{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return domain.Channel{}, err
	}
	member := s.members[channelID][userID]
	if !channel.Megagroup || channel.Broadcast {
		return domain.Channel{}, domain.ErrChannelNotModified
	}
	if member.Role != domain.ChannelRoleCreator {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	if enabled && channel.LinkedChatID != 0 {
		return domain.Channel{}, domain.ErrChatDiscussionUnallowed
	}
	prevForum := channel.Forum
	prevTabs := channel.ForumTabs
	channel.Forum = enabled
	channel.ForumTabs = enabled && tabs
	s.channels[channelID] = channel
	if prevForum != channel.Forum || prevTabs != channel.ForumTabs {
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channelID,
			UserID:    userID,
			Date:      int(time.Now().Unix()),
			Type:      domain.ChannelAdminLogToggleForum,
			PrevBool:  prevForum,
			NewBool:   enabled,
		})
	}
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetChannelViewForumAsMessages(_ context.Context, userID, channelID int64, enabled bool) (bool, error) {
	if userID == 0 || channelID == 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(userID, channelID)
	if err != nil {
		return false, nil
	}
	dialog := s.dialogForUserLocked(userID, channel)
	changed := dialog.ViewForumAsMessages != enabled
	dialog.ViewForumAsMessages = enabled
	if s.dialogs[userID] == nil {
		s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
	}
	s.dialogs[userID][channelID] = dialog
	return changed, nil
}

func (s *ChannelStore) CreateForumTopic(ctx context.Context, req domain.CreateChannelForumTopicRequest) (domain.CreateChannelForumTopicResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.RandomID == 0 {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	title := strings.TrimSpace(req.Title)
	if title == "" && !req.TitleMissing {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	if req.IconColor == 0 {
		req.IconColor = domain.DefaultForumTopicIconColor
	}
	s.mu.Lock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		s.mu.Unlock()
		return domain.CreateChannelForumTopicResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !channel.Forum || channel.Broadcast || !channel.Megagroup {
		s.mu.Unlock()
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelForumMissing
	}
	if !canSendChannelMessage(channel, member) {
		s.mu.Unlock()
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelWriteForbidden
	}
	selfBoostsApplied := 0
	if channel.Megagroup {
		now := req.Date
		if now == 0 {
			now = int(time.Now().Unix())
		}
		selfBoostsApplied = s.selfBoostsAppliedLocked(req.UserID, req.ChannelID, now)
	}
	if domain.ChannelBannedRightsBlockManageTopics(channel, member, selfBoostsApplied) {
		s.mu.Unlock()
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelWriteForbidden
	}
	if id, ok := s.randomToID[channelRandomKey{channelID: req.ChannelID, userID: req.UserID, randomID: req.RandomID}]; ok {
		if topic, ok := s.topics[req.ChannelID][id]; ok {
			msg, _ := s.findMessageLocked(req.ChannelID, id)
			event := s.eventForMessageLocked(req.ChannelID, id)
			recipients := s.activeMemberIDsLocked(req.ChannelID, 0, 0)
			s.mu.Unlock()
			return domain.CreateChannelForumTopicResult{
				Channel:    cloneChannel(channel),
				Topic:      cloneChannelForumTopic(topic),
				Message:    cloneChannelMessage(msg),
				Event:      cloneChannelEvent(event),
				Recipients: recipients,
				Duplicate:  true,
			}, nil
		}
	}
	s.mu.Unlock()

	res, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    req.UserID,
		ChannelID: req.ChannelID,
		RandomID:  req.RandomID,
		SendAs:    req.SendAs,
		Action: &domain.ChannelMessageAction{
			Type:         domain.ChannelActionTopicCreate,
			Title:        title,
			IconColor:    req.IconColor,
			IconEmojiID:  req.IconEmojiID,
			TitleMissing: req.TitleMissing,
		},
		Date: req.Date,
	})
	if err != nil {
		return domain.CreateChannelForumTopicResult{}, err
	}
	if res.Message.Action == nil || res.Message.Action.Type != domain.ChannelActionTopicCreate {
		return domain.CreateChannelForumTopicResult{}, domain.ErrChannelInvalid
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	channel = s.channels[req.ChannelID]
	if s.topics[req.ChannelID] == nil {
		s.topics[req.ChannelID] = make(map[int]domain.ChannelForumTopic)
	}
	topic, ok := s.topics[req.ChannelID][res.Message.ID]
	if !ok {
		topic = domain.ChannelForumTopic{
			ChannelID:       req.ChannelID,
			TopicID:         res.Message.ID,
			CreatorUserID:   req.UserID,
			Title:           title,
			IconColor:       req.IconColor,
			IconEmojiID:     req.IconEmojiID,
			TitleMissing:    req.TitleMissing,
			Date:            res.Message.Date,
			TopMessageID:    res.Message.ID,
			ReadInboxMaxID:  res.Message.ID,
			ReadOutboxMaxID: res.Message.ID,
		}
		s.topics[req.ChannelID][topic.TopicID] = topic
	}
	return domain.CreateChannelForumTopicResult{
		Channel:    cloneChannel(channel),
		Topic:      cloneChannelForumTopic(topic),
		Message:    cloneChannelMessage(res.Message),
		Event:      cloneChannelEvent(res.Event),
		Recipients: append([]int64(nil), res.Recipients...),
		Duplicate:  res.Duplicate,
	}, nil
}

func (s *ChannelStore) EditForumTopic(ctx context.Context, req domain.EditChannelForumTopicRequest) (domain.EditChannelForumTopicResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		s.mu.Unlock()
		return domain.EditChannelForumTopicResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	topic, ok := s.topics[req.ChannelID][req.TopicID]
	if !channel.Forum {
		s.mu.Unlock()
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelForumMissing
	}
	if !ok {
		s.mu.Unlock()
		return domain.EditChannelForumTopicResult{}, domain.ErrMessageIDInvalid
	}
	selfBoostsApplied := 0
	if channel.Megagroup {
		now := req.Date
		if now == 0 {
			now = int(time.Now().Unix())
		}
		selfBoostsApplied = s.selfBoostsAppliedLocked(req.UserID, req.ChannelID, now)
	}
	if !canManageForumTopic(channel, member, topic, req.UserID, selfBoostsApplied) {
		s.mu.Unlock()
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelAdminRequired
	}
	next := topic
	action := domain.ChannelMessageAction{Type: domain.ChannelActionTopicEdit}
	changed := false
	if req.Title != nil {
		title := strings.TrimSpace(*req.Title)
		if title == "" {
			s.mu.Unlock()
			return domain.EditChannelForumTopicResult{}, domain.ErrChannelInvalid
		}
		if next.Title != title {
			next.Title = title
			action.Title = title
			changed = true
		}
	}
	if req.IconEmojiID != nil && next.IconEmojiID != *req.IconEmojiID {
		next.IconEmojiID = *req.IconEmojiID
		action.IconEmojiID = *req.IconEmojiID
		action.IconEmojiIDSet = true
		changed = true
	}
	if req.Closed != nil && next.Closed != *req.Closed {
		next.Closed = *req.Closed
		action.Closed = boolPtr(*req.Closed)
		changed = true
	}
	if req.Hidden != nil && next.Hidden != *req.Hidden {
		next.Hidden = *req.Hidden
		action.Hidden = boolPtr(*req.Hidden)
		changed = true
	}
	s.mu.Unlock()
	if !changed {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelNotModified
	}

	res, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID:    req.UserID,
		ChannelID: req.ChannelID,
		ReplyTo: &domain.MessageReply{
			Peer:         domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID},
			MessageID:    req.TopicID,
			TopMessageID: req.TopicID,
		},
		Action: &action,
		Date:   req.Date,
	})
	if err != nil {
		return domain.EditChannelForumTopicResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	channel = s.channels[req.ChannelID]
	if _, ok := s.topics[req.ChannelID][req.TopicID]; !ok {
		return domain.EditChannelForumTopicResult{}, domain.ErrChannelForumMissing
	}
	next.TopMessageID = maxInt(next.TopMessageID, res.Message.ID)
	s.topics[req.ChannelID][req.TopicID] = next
	return domain.EditChannelForumTopicResult{
		Channel:    cloneChannel(channel),
		Topic:      cloneChannelForumTopic(next),
		Message:    cloneChannelMessage(res.Message),
		Event:      cloneChannelEvent(res.Event),
		Recipients: append([]int64(nil), res.Recipients...),
	}, nil
}

func (s *ChannelStore) UpdatePinnedForumTopic(_ context.Context, req domain.UpdateChannelForumTopicPinnedRequest) (domain.UpdateChannelForumTopicPinnedResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelForumTopicPinnedResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	topic, ok := s.topics[req.ChannelID][req.TopicID]
	if !channel.Forum {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelForumMissing
	}
	if !ok {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrMessageIDInvalid
	}
	if !canPinChannelMessages(channel, member) {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelAdminRequired
	}
	if topic.Pinned == req.Pinned {
		return domain.UpdateChannelForumTopicPinnedResult{}, domain.ErrChannelNotModified
	}
	topic.Pinned = req.Pinned
	if req.Pinned && topic.PinnedOrder == 0 {
		topic.PinnedOrder = s.nextForumTopicPinnedOrderLocked(req.ChannelID)
	}
	if !req.Pinned {
		topic.PinnedOrder = 0
	}
	s.topics[req.ChannelID][req.TopicID] = topic
	return domain.UpdateChannelForumTopicPinnedResult{
		Channel:    cloneChannel(channel),
		Topic:      cloneChannelForumTopic(topic),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

func (s *ChannelStore) ReorderPinnedForumTopics(_ context.Context, req domain.ReorderChannelPinnedForumTopicsRequest) (domain.ReorderChannelPinnedForumTopicsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || len(req.Order) > domain.MaxChannelForumTopicIDs {
		return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReorderChannelPinnedForumTopicsResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !channel.Forum {
		return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrChannelForumMissing
	}
	if !canPinChannelMessages(channel, member) {
		return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrChannelAdminRequired
	}
	seen := make(map[int]struct{}, len(req.Order))
	order := make([]int, 0, len(req.Order))
	for _, id := range req.Order {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrMessageIDInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		topic, ok := s.topics[req.ChannelID][id]
		if !ok || !topic.Pinned {
			if req.Force {
				continue
			}
			return domain.ReorderChannelPinnedForumTopicsResult{}, domain.ErrMessageIDInvalid
		}
		seen[id] = struct{}{}
		order = append(order, id)
	}
	for i, id := range order {
		topic := s.topics[req.ChannelID][id]
		topic.PinnedOrder = len(order) - i
		s.topics[req.ChannelID][id] = topic
	}
	return domain.ReorderChannelPinnedForumTopicsResult{
		Channel:    cloneChannel(channel),
		Order:      append([]int(nil), order...),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

func (s *ChannelStore) DeleteForumTopicHistory(_ context.Context, req domain.DeleteChannelForumTopicHistoryRequest) (domain.DeleteChannelHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.TopicID <= 0 {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	topic, ok := s.topics[req.ChannelID][req.TopicID]
	if !channel.Forum {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelForumMissing
	}
	if !ok {
		return domain.DeleteChannelHistoryResult{}, domain.ErrMessageIDInvalid
	}
	if !canManageForumTopic(channel, member, topic, req.UserID, 0) && !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelAdminRequired
	}
	ids := make([]int, 0, domain.MaxDeleteHistoryBatch)
	for i := len(s.messages[req.ChannelID]) - 1; i >= 0; i-- {
		msg := s.messages[req.ChannelID][i]
		if msg.Deleted {
			continue
		}
		if msg.ID != req.TopicID && (msg.ReplyTo == nil || msg.ReplyTo.TopMessageID != req.TopicID) {
			continue
		}
		ids = append(ids, msg.ID)
		if len(ids) >= domain.MaxDeleteHistoryBatch {
			break
		}
	}
	deleted, event, channel, err := s.deleteChannelMessagesLocked(channel, member, ids, req.UserID, req.Date)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	offset := 0
	if s.topicHasVisibleMessagesLocked(req.ChannelID, req.TopicID) {
		offset = 1
	} else {
		delete(s.topics[req.ChannelID], req.TopicID)
	}
	return domain.DeleteChannelHistoryResult{
		Channel:    cloneChannel(channel),
		Event:      cloneChannelEvent(event),
		DeletedIDs: append([]int(nil), deleted...),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		Offset:     offset,
	}, nil
}

func (s *ChannelStore) ListForumTopics(_ context.Context, viewerUserID int64, filter domain.ChannelForumTopicFilter) (domain.ChannelForumTopicList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	if !channel.Forum {
		return domain.ChannelForumTopicList{}, domain.ErrChannelForumMissing
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelForumTopicsLimit {
		limit = domain.MaxChannelForumTopicsLimit
	}
	query := strings.TrimSpace(strings.ToLower(filter.Query))
	all := make([]domain.ChannelForumTopic, 0, len(s.topics[filter.ChannelID]))
	for _, topic := range s.topics[filter.ChannelID] {
		if topic.TopicID <= member.AvailableMinID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(topic.Title), query) {
			continue
		}
		if forumTopicBeforeOrAtOffset(topic, filter) {
			continue
		}
		all = append(all, s.topicWithViewerCountersLocked(viewerUserID, filter.ChannelID, topic, member))
	}
	sortForumTopics(all)
	count := len(all)
	if len(all) > limit {
		all = all[:limit]
	}
	messages := s.forumTopicRootMessagesLocked(filter.ChannelID, all, member.AvailableMinID)
	return domain.ChannelForumTopicList{
		Channel:  cloneChannel(channel),
		Dialog:   s.dialogForUserLocked(viewerUserID, channel),
		Topics:   all,
		Messages: messages,
		Count:    count,
	}, nil
}

func (s *ChannelStore) GetForumTopicsByID(_ context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelForumTopicList, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelForumTopicList{}, err
	}
	if !channel.Forum {
		return domain.ChannelForumTopicList{}, domain.ErrChannelForumMissing
	}
	wanted := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.ChannelForumTopicList{}, domain.ErrMessageIDInvalid
		}
		wanted[id] = struct{}{}
	}
	topics := make([]domain.ChannelForumTopic, 0, len(wanted))
	for id := range wanted {
		topic, ok := s.topics[channelID][id]
		if !ok || topic.TopicID <= member.AvailableMinID {
			continue
		}
		topics = append(topics, s.topicWithViewerCountersLocked(viewerUserID, channelID, topic, member))
	}
	sortForumTopics(topics)
	messages := s.forumTopicRootMessagesLocked(channelID, topics, member.AvailableMinID)
	return domain.ChannelForumTopicList{
		Channel:  cloneChannel(channel),
		Dialog:   s.dialogForUserLocked(viewerUserID, channel),
		Topics:   topics,
		Messages: messages,
		Count:    len(topics),
	}, nil
}

func (s *ChannelStore) ListChannelReplies(_ context.Context, viewerUserID int64, filter domain.ChannelRepliesFilter) (domain.ChannelHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, member, err := s.channelAndMemberOrLinkedGuestLocked(viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	root, ok := s.findMessageLocked(filter.ChannelID, filter.RootMessageID)
	if !ok || root.Deleted || root.ID <= member.AvailableMinID {
		return domain.ChannelHistory{}, domain.ErrMessageIDInvalid
	}
	targetChannel := source
	targetMember := member
	rootID := root.ID
	extraChannels := []domain.Channel(nil)
	if source.Broadcast {
		if root.Discussion == nil || root.Discussion.ChannelID == 0 || root.Discussion.MessageID == 0 {
			return domain.ChannelHistory{Channel: source, Count: 0}, nil
		}
		linked, ok := s.channels[root.Discussion.ChannelID]
		if !ok || linked.Deleted {
			return domain.ChannelHistory{Channel: source, Count: 0}, nil
		}
		targetChannel = linked
		rootID = root.Discussion.MessageID
		if linkedMember, ok := s.members[linked.ID][viewerUserID]; ok {
			targetMember = linkedMember
		} else {
			targetMember = domain.ChannelMember{}
		}
		extraChannels = append(extraChannels, source)
	}
	if targetRoot, ok := s.findMessageLocked(targetChannel.ID, rootID); !ok || targetRoot.Deleted {
		return domain.ChannelHistory{Channel: targetChannel, Channels: extraChannels, Count: 0}, nil
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelRepliesLimit {
		limit = domain.MaxChannelRepliesLimit
	}
	filter.AddOffset = domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	base := make([]domain.ChannelMessage, 0, limit)
	for _, msg := range s.messages[targetChannel.ID] {
		if msg.Deleted || msg.ID <= targetMember.AvailableMinID {
			continue
		}
		if !channelReplyBelongsToRoot(msg, targetChannel.ID, rootID) {
			continue
		}
		if filter.MaxID > 0 && msg.ID >= filter.MaxID {
			continue
		}
		if filter.MinID > 0 && msg.ID <= filter.MinID {
			continue
		}
		if filter.OffsetDate > 0 && msg.Date == 0 {
			continue
		}
		base = append(base, msg)
	}
	sort.SliceStable(base, func(i, j int) bool { return channelMessageLess(base[i], base[j]) })
	page := pageChannelMessageHistory(base, filter, limit)
	out := make([]domain.ChannelMessage, 0, len(page))
	for _, msg := range page {
		out = append(out, cloneChannelMessage(msg))
	}
	s.populateChannelMessageRepliesLocked(viewerUserID, targetChannel.ID, out)
	s.populateChannelMessageReactionsLocked(viewerUserID, targetChannel, out)
	topics := []domain.ChannelForumTopic(nil)
	if targetChannel.Forum {
		if topic, ok := s.topics[targetChannel.ID][rootID]; ok && !topic.Hidden {
			topic = s.topicWithViewerCountersLocked(viewerUserID, targetChannel.ID, topic, targetMember)
			topics = append(topics, cloneChannelForumTopic(topic))
		}
	}
	return domain.ChannelHistory{Channel: targetChannel, Channels: extraChannels, Topics: topics, Messages: out, Count: len(base)}, nil
}

func (s *ChannelStore) populateChannelMessageRepliesLocked(viewerUserID, channelID int64, messages []domain.ChannelMessage) {
	for i := range messages {
		messages[i].Replies = s.channelMessageRepliesLocked(viewerUserID, channelID, messages[i])
	}
}

func (s *ChannelStore) channelMessageRepliesLocked(viewerUserID, channelID int64, msg domain.ChannelMessage) *domain.ChannelMessageReplies {
	targetChannelID := channelID
	rootID := msg.ID
	stats := domain.ChannelMessageReplies{}
	if msg.Discussion != nil && msg.Discussion.ChannelID != 0 && msg.Discussion.MessageID != 0 {
		targetChannelID = msg.Discussion.ChannelID
		rootID = msg.Discussion.MessageID
		stats.Comments = true
		stats.ChannelID = msg.Discussion.ChannelID
	} else if channel, ok := s.channels[channelID]; ok && channel.Broadcast && channel.LinkedChatID != 0 && msg.Post {
		stats.Comments = true
		stats.ChannelID = channel.LinkedChatID
	}
	if rootID <= 0 {
		return nil
	}
	if member, ok := s.members[targetChannelID][viewerUserID]; ok {
		stats.ReadMaxID = member.ReadInboxMaxID
	}
	seenRecent := map[domain.Peer]struct{}{}
	for i := len(s.messages[targetChannelID]) - 1; i >= 0; i-- {
		reply := s.messages[targetChannelID][i]
		if reply.Deleted || !channelReplyBelongsToRoot(reply, targetChannelID, rootID) {
			continue
		}
		stats.Replies++
		if stats.MaxID == 0 || reply.ID > stats.MaxID {
			stats.MaxID = reply.ID
			stats.RepliesPts = reply.Pts
		}
		if len(stats.RecentRepliers) < 3 {
			peer := reply.From
			if peer.ID == 0 && reply.SenderUserID != 0 {
				peer = domain.Peer{Type: domain.PeerTypeUser, ID: reply.SenderUserID}
			}
			if peer.ID != 0 {
				if _, ok := seenRecent[peer]; !ok {
					seenRecent[peer] = struct{}{}
					stats.RecentRepliers = append(stats.RecentRepliers, peer)
				}
			}
		}
	}
	if stats.Comments && stats.RepliesPts == 0 {
		if root, ok := s.findMessageLocked(targetChannelID, rootID); ok {
			stats.RepliesPts = root.Pts
		}
	}
	if !stats.Comments && stats.Replies == 0 {
		return nil
	}
	return &stats
}

func canManageForumTopic(channel domain.Channel, member domain.ChannelMember, topic domain.ChannelForumTopic, userID int64, selfBoostsApplied int) bool {
	if domain.ChannelBannedRightsBlockManageTopics(channel, member, selfBoostsApplied) {
		return false
	}
	if topic.CreatorUserID == userID {
		return true
	}
	return canPinChannelMessages(channel, member)
}

func cloneChannelForumTopic(in domain.ChannelForumTopic) domain.ChannelForumTopic {
	return in
}

func (s *ChannelStore) updateForumTopicTopMessageLocked(channelID int64, msg domain.ChannelMessage) {
	if msg.ReplyTo == nil || !msg.ReplyTo.ForumTopic || msg.ReplyTo.TopMessageID <= 0 {
		return
	}
	topic, ok := s.topics[channelID][msg.ReplyTo.TopMessageID]
	if !ok {
		return
	}
	topic.TopMessageID = msg.ID
	topic.Date = msg.Date
	s.topics[channelID][topic.TopicID] = topic
}

func sortForumTopics(topics []domain.ChannelForumTopic) {
	sort.Slice(topics, func(i, j int) bool {
		a, b := topics[i], topics[j]
		if a.Pinned != b.Pinned {
			return a.Pinned
		}
		if a.PinnedOrder != b.PinnedOrder {
			return a.PinnedOrder > b.PinnedOrder
		}
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		return a.TopicID > b.TopicID
	})
}

func forumTopicBeforeOrAtOffset(topic domain.ChannelForumTopic, filter domain.ChannelForumTopicFilter) bool {
	if filter.OffsetDate == 0 && filter.OffsetID == 0 && filter.OffsetTopic == 0 {
		return false
	}
	offsetID := filter.OffsetTopic
	if offsetID == 0 {
		offsetID = filter.OffsetID
	}
	if filter.OffsetDate != 0 {
		if topic.Date < filter.OffsetDate {
			return false
		}
		if topic.Date > filter.OffsetDate {
			return true
		}
	}
	if offsetID == 0 {
		return false
	}
	return topic.TopicID >= offsetID
}

func (s *ChannelStore) forumTopicRootMessagesLocked(channelID int64, topics []domain.ChannelForumTopic, availableMinID int) []domain.ChannelMessage {
	if len(topics) == 0 {
		return nil
	}
	wanted := make(map[int]struct{}, len(topics))
	for _, topic := range topics {
		if topic.TopMessageID > 0 {
			wanted[topic.TopMessageID] = struct{}{}
		}
	}
	messages := make([]domain.ChannelMessage, 0, len(wanted))
	for _, msg := range s.messages[channelID] {
		if _, ok := wanted[msg.ID]; !ok {
			continue
		}
		if msg.Deleted || msg.ID <= availableMinID {
			continue
		}
		messages = append(messages, cloneChannelMessage(msg))
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].ID > messages[j].ID })
	return messages
}

func (s *ChannelStore) nextForumTopicPinnedOrderLocked(channelID int64) int {
	next := 1
	for _, topic := range s.topics[channelID] {
		if topic.PinnedOrder >= next {
			next = topic.PinnedOrder + 1
		}
	}
	return next
}

func cloneChannelMessageReplies(in *domain.ChannelMessageReplies) *domain.ChannelMessageReplies {
	if in == nil {
		return nil
	}
	out := *in
	out.RecentRepliers = append([]domain.Peer(nil), in.RecentRepliers...)
	return &out
}
