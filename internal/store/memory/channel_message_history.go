package memory

import (
	"context"
	"errors"
	"sort"
	"strings"
	"telesrv/internal/domain"
)

func (s *ChannelStore) ListChannelHistory(_ context.Context, viewerUserID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, _, err := s.channelForViewerLocked(viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	items := append([]domain.ChannelMessage(nil), s.messages[filter.ChannelID]...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	// 静态过滤（不含 offset 锚点的方向条件），结果保持 id 降序。
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	matched := make([]domain.ChannelMessage, 0, len(items))
	for _, msg := range items {
		if msg.Deleted {
			continue
		}
		if channel.Monoforum && msg.SavedPeer.ID != 0 {
			continue
		}
		if msg.ID <= member.AvailableMinID {
			continue
		}
		if filter.PinnedOnly && !msg.Pinned {
			continue
		}
		if filter.MusicOnly && !msg.Media.IsMusic() {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(msg.Body), query) {
			continue
		}
		if filter.SenderUserID != 0 && msg.SenderUserID != filter.SenderUserID {
			continue
		}
		if filter.MinDate > 0 && msg.Date <= filter.MinDate {
			continue
		}
		if filter.MaxDate > 0 && msg.Date >= filter.MaxDate {
			continue
		}
		if filter.MaxID > 0 && msg.ID > filter.MaxID {
			continue
		}
		if filter.MinID > 0 && msg.ID <= filter.MinID {
			continue
		}
		matched = append(matched, msg)
	}
	// add_offset 决定加载方向（对齐 postgres ListChannelHistory）：
	//   >= 0           backward：锚点更旧方向（不含锚点），先跳过 add_offset 条
	//   < 0 且 +limit>0 around：以锚点为中心，向更新取 -add_offset 条 + 向更旧（含锚点）取 limit+add_offset 条
	//   否则           forward：仅锚点更新方向
	newerThanAnchor := func(msg domain.ChannelMessage) bool {
		if filter.OffsetDate > 0 {
			return msg.Date >= filter.OffsetDate
		}
		if filter.OffsetID > 0 {
			return msg.ID > filter.OffsetID
		}
		return false
	}
	olderThanAnchor := func(msg domain.ChannelMessage, includeAnchor bool) bool {
		if filter.OffsetDate > 0 {
			return msg.Date < filter.OffsetDate
		}
		if filter.OffsetID > 0 {
			if includeAnchor {
				return msg.ID <= filter.OffsetID
			}
			return msg.ID < filter.OffsetID
		}
		return true
	}
	takeNewer := func(limit int) []domain.ChannelMessage {
		if limit <= 0 {
			return nil
		}
		// 升序收集锚点更新方向最近的 limit 条，再反转回降序。
		asc := make([]domain.ChannelMessage, 0, limit)
		for i := len(matched) - 1; i >= 0; i-- {
			if !newerThanAnchor(matched[i]) {
				continue
			}
			asc = append(asc, matched[i])
			if len(asc) == limit {
				break
			}
		}
		out := make([]domain.ChannelMessage, 0, len(asc))
		for i := len(asc) - 1; i >= 0; i-- {
			out = append(out, asc[i])
		}
		return out
	}
	takeOlder := func(skip, limit int, includeAnchor bool) ([]domain.ChannelMessage, bool) {
		if limit <= 0 {
			return nil, false
		}
		out := make([]domain.ChannelMessage, 0, limit)
		skipped := 0
		for _, msg := range matched {
			if !olderThanAnchor(msg, includeAnchor) {
				continue
			}
			if skipped < skip {
				skipped++
				continue
			}
			if len(out) == limit {
				return out, true
			}
			out = append(out, msg)
		}
		return out, false
	}
	addOffset := filter.AddOffset
	var page []domain.ChannelMessage
	hasMoreOlder := false
	switch {
	case addOffset < 0 && addOffset+limit > 0:
		fwdLimit := -addOffset
		if fwdLimit > limit {
			fwdLimit = limit
		}
		bwdLimit := limit + addOffset
		page = takeNewer(fwdLimit)
		older, more := takeOlder(0, bwdLimit, true)
		page = append(page, older...)
		hasMoreOlder = more
	case addOffset < 0:
		page = takeNewer(limit)
	default:
		page, hasMoreOlder = takeOlder(addOffset, limit, false)
	}
	out := make([]domain.ChannelMessage, 0, len(page))
	for _, msg := range page {
		out = append(out, cloneChannelMessage(msg))
	}
	count := len(out)
	if hasMoreOlder {
		count = len(out) + 1
	}
	s.populateChannelMessageRepliesLocked(viewerUserID, filter.ChannelID, out)
	s.populateChannelMessageReactionsLocked(viewerUserID, channel, out)
	extraChannels := []domain.Channel(nil)
	if channel.Monoforum && channel.LinkedMonoforumID != 0 {
		if parent, ok := s.channels[channel.LinkedMonoforumID]; ok && !parent.Deleted {
			extraChannels = append(extraChannels, cloneChannel(parent))
		}
	}
	return domain.ChannelHistory{
		Channel:  channel,
		Self:     member,
		Channels: extraChannels,
		Messages: out,
		Count:    count,
	}, nil
}

func (s *ChannelStore) SearchJoinedMessages(_ context.Context, viewerUserID int64, req domain.ChannelGlobalSearchRequest) (domain.ChannelHistory, error) {
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if viewerUserID == 0 || (query == "" && !req.MusicOnly) {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxChannelGlobalSearchLimit {
		req.Limit = domain.MaxChannelGlobalSearchLimit
	}
	type hit struct {
		channel domain.Channel
		message domain.ChannelMessage
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	hits := make([]hit, 0, req.Limit+1)
	for channelID, channel := range s.channels {
		if channel.Deleted {
			continue
		}
		if req.BroadcastsOnly && (!channel.Broadcast || channel.Megagroup) {
			continue
		}
		if req.GroupsOnly && !channel.Megagroup {
			continue
		}
		member, ok := s.members[channelID][viewerUserID]
		if !ok || member.Status != domain.ChannelMemberActive || member.BannedRights.ViewMessages {
			continue
		}
		if req.HasFolderID {
			dialog, ok := s.dialogs[viewerUserID][channelID]
			if !ok || dialog.FolderID != req.FolderID {
				continue
			}
		}
		for _, msg := range s.messages[channelID] {
			if msg.Deleted {
				continue
			}
			if query == "" && !req.MusicOnly || query != "" && strings.TrimSpace(msg.Body) == "" {
				continue
			}
			if member.AvailableMinID > 0 && msg.ID <= member.AvailableMinID {
				continue
			}
			if req.MinDate > 0 && msg.Date <= req.MinDate {
				continue
			}
			if req.MaxDate > 0 && msg.Date >= req.MaxDate {
				continue
			}
			if !channelGlobalSearchAfterCursor(msg, req) {
				continue
			}
			if req.MusicOnly && !msg.Media.IsMusic() {
				continue
			}
			if query != "" && !strings.Contains(strings.ToLower(msg.Body), query) {
				continue
			}
			hits = append(hits, hit{channel: channel, message: cloneChannelMessage(msg)})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		a, b := hits[i].message, hits[j].message
		if a.Date != b.Date {
			return a.Date > b.Date
		}
		if a.ChannelID != b.ChannelID {
			return a.ChannelID > b.ChannelID
		}
		return a.ID > b.ID
	})
	out := domain.ChannelHistory{Count: len(hits)}
	if out.Count > req.Limit {
		out.Count = req.Limit + 1
		hits = hits[:req.Limit]
	}
	channelSeen := make(map[int64]struct{}, len(hits))
	for _, h := range hits {
		out.Messages = append(out.Messages, h.message)
		if _, ok := channelSeen[h.channel.ID]; ok {
			continue
		}
		channelSeen[h.channel.ID] = struct{}{}
		out.Channels = append(out.Channels, h.channel)
	}
	s.populateChannelMessagesReactionsLocked(viewerUserID, out.Channels, out.Messages)
	return out, nil
}

func (s *ChannelStore) GetChannelMessages(_ context.Context, viewerUserID, channelID int64, ids []int) (domain.ChannelHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	// viewer 口径：公开频道非成员可预览读取(与 ListChannelHistory 一致 + 与 PG 实现对齐)，
	// 否则查看他人公开「个人频道」时 channels.getMessages 被拒、资料页整块不显示。
	channel, member, _, err := s.channelForViewerLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	if len(ids) == 0 {
		return domain.ChannelHistory{Channel: channel, Self: member}, nil
	}
	if len(ids) > domain.MaxGetMessageIDs {
		return domain.ChannelHistory{}, domain.ErrChannelInvalid
	}
	wanted := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.ChannelHistory{}, domain.ErrMessageIDInvalid
		}
		wanted[id] = struct{}{}
	}
	messages := make([]domain.ChannelMessage, 0, len(wanted))
	for _, msg := range s.messages[channelID] {
		if _, ok := wanted[msg.ID]; !ok {
			continue
		}
		if msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		messages = append(messages, cloneChannelMessage(msg))
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].ID > messages[j].ID })
	s.populateChannelMessageRepliesLocked(viewerUserID, channelID, messages)
	s.populateChannelMessageReactionsLocked(viewerUserID, channel, messages)
	return domain.ChannelHistory{Channel: channel, Self: member, Messages: messages, Count: len(messages)}, nil
}

func (s *ChannelStore) ListStoryMessageForwards(_ context.Context, req domain.StoryMessageForwardListRequest) (domain.StoryMessageForwardList, error) {
	if req.ViewerUserID == 0 || req.Owner.ID == 0 || req.StoryID <= 0 || req.StoryID > domain.MaxStoryID {
		return domain.StoryMessageForwardList{}, domain.ErrStoryIDInvalid
	}
	if req.Owner.Type != domain.PeerTypeUser && req.Owner.Type != domain.PeerTypeChannel {
		return domain.StoryMessageForwardList{}, domain.ErrStoryPeerInvalid
	}
	if err := domain.ValidateStoryInteractionOffset(req.Offset, false); err != nil {
		return domain.StoryMessageForwardList{}, err
	}
	limit := clampStoryInteractionLimit(req.Limit)
	cursor := parseStoryInteractionCursor(req.Offset)
	s.mu.RLock()
	views := make([]domain.StoryView, 0)
	for channelID, channel := range s.channels {
		if channel.Deleted || strings.TrimSpace(channel.Username) == "" || (!channel.Broadcast && !channel.Megagroup) {
			continue
		}
		for _, msg := range s.messages[channelID] {
			if !messageSharesStory(msg, req.Owner, req.StoryID) {
				continue
			}
			views = append(views, domain.StoryView{
				Owner:   req.Owner,
				StoryID: req.StoryID,
				Date:    msg.Date,
				PublicForward: &domain.StoryPublicForward{
					Message: cloneChannelMessage(msg),
				},
			})
		}
	}
	s.mu.RUnlock()
	sortStoryViewsForList(views, req.ReactionsFirst, req.ForwardsFirst)
	page, nextOffset := pageStoryViews(views, limit, cursor, req.ReactionsFirst, req.ForwardsFirst)
	return domain.StoryMessageForwardList{Count: len(views), Forwards: page, NextOffset: nextOffset}, nil
}

func messageSharesStory(msg domain.ChannelMessage, owner domain.Peer, storyID int) bool {
	return !msg.Deleted &&
		msg.Media != nil &&
		msg.Media.Kind == domain.MessageMediaKindStory &&
		msg.Media.Story != nil &&
		msg.Media.Story.Peer == owner &&
		msg.Media.Story.ID == storyID
}

func (s *ChannelStore) GetChannelMessageForInlineBot(_ context.Context, botID, channelID int64, id int) (domain.Channel, domain.ChannelMessage, bool, error) {
	if botID == 0 || channelID == 0 || id <= 0 || id > domain.MaxMessageBoxID {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, ok := s.channels[channelID]
	if !ok || channel.Deleted {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	msg, ok := s.findMessageLocked(channelID, id)
	if !ok || msg.Deleted || msg.Action != nil || msg.ViaBotID != botID {
		return domain.Channel{}, domain.ChannelMessage{}, false, nil
	}
	return channel, cloneChannelMessage(msg), true, nil
}

func (s *ChannelStore) GetDiscussionMessage(_ context.Context, viewerUserID, channelID int64, msgID int) (domain.ChannelDiscussionMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, member, err := s.channelAndMemberLocked(viewerUserID, channelID)
	if err != nil {
		return domain.ChannelDiscussionMessage{}, err
	}
	msg, ok := s.findMessageLocked(channelID, msgID)
	if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelDiscussionMessage{}, domain.ErrMessageIDInvalid
	}
	result := domain.ChannelDiscussionMessage{PostChannel: source, DiscussionChannel: source, Channels: []domain.Channel{source}}
	targetChannel := source
	targetMsg := msg
	targetMember := member
	if source.Broadcast {
		if msg.Discussion == nil || msg.Discussion.ChannelID == 0 || msg.Discussion.MessageID == 0 {
			return result, nil
		}
		linked, ok := s.channels[msg.Discussion.ChannelID]
		if !ok || linked.Deleted {
			return result, nil
		}
		linkedMsg, ok := s.findMessageLocked(linked.ID, msg.Discussion.MessageID)
		if !ok || linkedMsg.Deleted {
			return result, nil
		}
		targetChannel = linked
		targetMsg = linkedMsg
		if linkedMember, ok := s.members[linked.ID][viewerUserID]; ok {
			targetMember = linkedMember
		} else {
			targetMember = domain.ChannelMember{}
		}
		result.DiscussionChannel = linked
		result.Channels = []domain.Channel{source, linked}
	}
	items := []domain.ChannelMessage{cloneChannelMessage(targetMsg)}
	s.populateChannelMessageRepliesLocked(viewerUserID, targetChannel.ID, items)
	s.populateChannelMessageReactionsLocked(viewerUserID, targetChannel, items)
	if stats := s.channelMessageRepliesLocked(viewerUserID, targetChannel.ID, targetMsg); stats != nil {
		result.MaxID = stats.MaxID
	}
	result.ReadInboxMaxID = targetMember.ReadInboxMaxID
	result.ReadOutboxMaxID = targetMember.ReadOutboxMaxID
	result.UnreadCount = s.channelThreadUnreadCountLocked(viewerUserID, targetChannel.ID, targetMsg.ID, targetMember.ReadInboxMaxID)
	result.Messages = items
	return result, nil
}

// ResolveDiscussionReadTarget returns the minimal linked-thread mapping and
// current read boundary without building reply/reaction aggregates.
func (s *ChannelStore) ResolveDiscussionReadTarget(_ context.Context, viewerUserID, sourceChannelID int64, sourceMessageID, readMaxID int) (domain.ChannelDiscussionReadTarget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	source, sourceMember, err := s.channelAndMemberOrLinkedGuestLocked(viewerUserID, sourceChannelID)
	if err != nil {
		return domain.ChannelDiscussionReadTarget{}, err
	}
	msg, ok := s.findMessageLocked(sourceChannelID, sourceMessageID)
	if !ok || msg.Deleted || msg.ID <= sourceMember.AvailableMinID {
		return domain.ChannelDiscussionReadTarget{}, domain.ErrMessageIDInvalid
	}
	targetChannelID, rootID := sourceChannelID, sourceMessageID
	if source.Broadcast && msg.Discussion != nil {
		if msg.Discussion.ChannelID == 0 || msg.Discussion.MessageID == 0 {
			return domain.ChannelDiscussionReadTarget{}, domain.ErrMessageIDInvalid
		}
		linked, exists := s.channels[msg.Discussion.ChannelID]
		root, rootExists := s.findMessageLocked(msg.Discussion.ChannelID, msg.Discussion.MessageID)
		if !exists || linked.Deleted || !linked.Megagroup || linked.Broadcast || !rootExists || root.Deleted {
			return domain.ChannelDiscussionReadTarget{}, domain.ErrMessageIDInvalid
		}
		targetChannelID, rootID = linked.ID, root.ID
	}
	targetMember := sourceMember
	if targetChannelID != sourceChannelID {
		_, targetMember, err = s.channelAndMemberLocked(viewerUserID, targetChannelID)
		if errors.Is(err, domain.ErrChannelPrivate) {
			targetMember, _, err = s.linkedDiscussionGuestLocked(viewerUserID, s.channels[targetChannelID])
		}
	}
	if err != nil {
		return domain.ChannelDiscussionReadTarget{}, err
	}
	effectiveMaxID := readMaxID
	if target := s.channels[targetChannelID]; effectiveMaxID <= 0 || effectiveMaxID > target.TopMessageID {
		effectiveMaxID = target.TopMessageID
	}
	return domain.ChannelDiscussionReadTarget{
		ChannelID:   targetChannelID,
		RootID:      rootID,
		AlreadyRead: effectiveMaxID <= targetMember.ReadInboxMaxID && !targetMember.UnreadMark,
		Guest:       targetMember.Guest,
	}, nil
}

func (s *ChannelStore) findMessageLocked(channelID int64, id int) (domain.ChannelMessage, bool) {
	for _, msg := range s.messages[channelID] {
		if msg.ID == id {
			return msg, true
		}
	}
	return domain.ChannelMessage{}, false
}

func (s *ChannelStore) findMessageIndexLocked(channelID int64, id int) (int, bool) {
	for i, msg := range s.messages[channelID] {
		if msg.ID == id {
			return i, true
		}
	}
	return 0, false
}

func pageChannelMessageHistory(base []domain.ChannelMessage, filter domain.ChannelRepliesFilter, limit int) []domain.ChannelMessage {
	if limit <= 0 || len(base) == 0 {
		return nil
	}
	switch messageHistoryLoadType(filter.AddOffset, limit) {
	case messageHistoryLoadForward:
		return forwardChannelMessageHistory(base, filter, limit)
	case messageHistoryLoadAround:
		forwardLimit := -filter.AddOffset
		if forwardLimit > limit {
			forwardLimit = limit
		}
		backwardLimit := limit + filter.AddOffset
		if backwardLimit < 0 {
			backwardLimit = 0
		}
		page := make([]domain.ChannelMessage, 0, limit)
		page = append(page, forwardChannelMessageHistory(base, filter, forwardLimit)...)
		page = append(page, backwardChannelMessageHistory(base, filter, backwardLimit, true)...)
		sort.SliceStable(page, func(i, j int) bool { return channelMessageLess(page[i], page[j]) })
		return page
	default:
		start := filter.AddOffset
		if start < 0 {
			start = 0
		}
		candidates := backwardChannelMessageHistory(base, filter, limit+start, false)
		if start >= len(candidates) {
			return nil
		}
		return candidates[start:]
	}
}

func backwardChannelMessageHistory(base []domain.ChannelMessage, filter domain.ChannelRepliesFilter, limit int, includeOffset bool) []domain.ChannelMessage {
	if limit <= 0 {
		return nil
	}
	out := make([]domain.ChannelMessage, 0, limit)
	for _, msg := range base {
		if !channelMessageBeforeHistoryOffset(msg, filter, includeOffset) {
			continue
		}
		out = append(out, msg)
		if len(out) == limit {
			break
		}
	}
	return out
}

func forwardChannelMessageHistory(base []domain.ChannelMessage, filter domain.ChannelRepliesFilter, limit int) []domain.ChannelMessage {
	if limit <= 0 {
		return nil
	}
	out := make([]domain.ChannelMessage, 0, limit)
	for i := len(base) - 1; i >= 0; i-- {
		msg := base[i]
		if !channelMessageAfterHistoryOffset(msg, filter) {
			continue
		}
		out = append(out, msg)
		if len(out) == limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return channelMessageLess(out[i], out[j]) })
	return out
}

func channelMessageBeforeHistoryOffset(msg domain.ChannelMessage, filter domain.ChannelRepliesFilter, includeOffset bool) bool {
	if filter.OffsetDate > 0 {
		if includeOffset {
			return msg.Date <= filter.OffsetDate
		}
		return msg.Date < filter.OffsetDate
	}
	if filter.OffsetID <= 0 {
		return true
	}
	if includeOffset {
		return msg.ID <= filter.OffsetID
	}
	return msg.ID < filter.OffsetID
}

func channelMessageAfterHistoryOffset(msg domain.ChannelMessage, filter domain.ChannelRepliesFilter) bool {
	if filter.OffsetDate > 0 {
		return msg.Date >= filter.OffsetDate
	}
	if filter.OffsetID <= 0 {
		return false
	}
	return msg.ID > filter.OffsetID
}

func channelMessageLess(a, b domain.ChannelMessage) bool {
	if a.Date != b.Date {
		return a.Date > b.Date
	}
	return a.ID > b.ID
}

func (s *ChannelStore) visibleTopMessageIDLocked(userID int64, channel domain.Channel) int {
	return s.visibleTopMessageIDForMemberLocked(channel, s.members[channel.ID][userID])
}

func (s *ChannelStore) visibleTopMessageIDForMemberLocked(channel domain.Channel, member domain.ChannelMember) int {
	for i := len(s.messages[channel.ID]) - 1; i >= 0; i-- {
		msg := s.messages[channel.ID][i]
		if !msg.Deleted && msg.ID > member.AvailableMinID {
			return msg.ID
		}
	}
	return 0
}

func (s *ChannelStore) topicHasVisibleMessagesLocked(channelID int64, topicID int) bool {
	for _, msg := range s.messages[channelID] {
		if msg.Deleted {
			continue
		}
		if msg.ID == topicID || (msg.ReplyTo != nil && msg.ReplyTo.TopMessageID == topicID) {
			return true
		}
	}
	return false
}
