package memory

import (
	"context"
	"sort"
	"telesrv/internal/domain"
)

func (s *ChannelStore) SetChannelDialogUnreadMark(_ context.Context, userID, channelID int64, unread bool) (bool, error) {
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
	changed := dialog.UnreadMark != unread
	dialog.UnreadMark = unread
	member := s.members[channelID][userID]
	member.UnreadMark = unread
	s.members[channelID][userID] = member
	if s.dialogs[userID] == nil {
		s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
	}
	s.dialogs[userID][channelID] = dialog
	return changed, nil
}

func (s *ChannelStore) ListChannelUnreadMarked(_ context.Context, userID int64) ([]domain.Peer, error) {
	if userID == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Peer, 0, len(s.dialogs[userID]))
	for channelID, dialog := range s.dialogs[userID] {
		if !dialog.UnreadMark {
			continue
		}
		if _, err := s.channelForMemberLocked(userID, channelID); err != nil {
			continue
		}
		out = append(out, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *ChannelStore) ReadChannelMessageContents(_ context.Context, req domain.ReadChannelMessageContentsRequest) (domain.ReadChannelMessageContentsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ReadChannelMessageContentsResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelMessageContentsResult{}, err
	}
	if len(req.IDs) == 0 {
		return domain.ReadChannelMessageContentsResult{Channel: channel}, nil
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.ReadChannelMessageContentsResult{}, domain.ErrChannelInvalid
	}
	wanted := make(map[int]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.ReadChannelMessageContentsResult{}, domain.ErrMessageIDInvalid
		}
		wanted[id] = struct{}{}
	}
	messages := make([]domain.ChannelMessage, 0, len(wanted))
	for _, msg := range s.messages[req.ChannelID] {
		if _, ok := wanted[msg.ID]; !ok {
			continue
		}
		if msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		messages = append(messages, cloneChannelMessage(msg))
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].ID > messages[j].ID })
	clearedSet := make(map[int]struct{})
	for _, msg := range messages {
		byUser := s.reactions[req.ChannelID][msg.ID]
		if len(byUser) == 0 {
			continue
		}
		for reactedUserID, rows := range byUser {
			changed := false
			for i := range rows {
				if rows[i].SenderUserID == req.UserID && rows[i].UserID != req.UserID && rows[i].Unread {
					rows[i].Unread = false
					changed = true
					clearedSet[msg.ID] = struct{}{}
				}
			}
			if changed {
				byUser[reactedUserID] = rows
			}
		}
	}
	cleared := make([]int, 0, len(clearedSet))
	for id := range clearedSet {
		cleared = append(cleared, id)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(cleared)))
	if len(cleared) > 0 {
		s.refreshChannelUnreadReactionsDialogLocked(req.UserID, req.ChannelID)
	}
	// Marking the viewport contents as read also flips mention unread state: the client has
	// already decremented the count locally, and if the server does not persist the badge it
	// resurrects on the next getDialogs.
	clearedMentions := make([]int, 0, len(messages))
	mentionFlipped := false
	for _, msg := range messages {
		mention, ok := s.mentions[req.UserID][req.ChannelID][msg.ID]
		if !ok || !mention.unread {
			continue
		}
		mention.unread = false
		s.mentions[req.UserID][req.ChannelID][msg.ID] = mention
		clearedMentions = append(clearedMentions, msg.ID)
		mentionFlipped = true
	}
	sort.Ints(clearedMentions)
	if mentionFlipped {
		if dialogs := s.dialogs[req.UserID]; dialogs != nil {
			dialog := dialogs[req.ChannelID]
			dialog.UserID = req.UserID
			dialog.ChannelID = req.ChannelID
			dialog.UnreadMentions = s.countChannelUnreadMentionsLocked(req.UserID, req.ChannelID, 0)
			dialogs[req.ChannelID] = dialog
		}
	}
	s.populateChannelMessageRepliesLocked(req.UserID, req.ChannelID, messages)
	s.populateChannelMessageReactionsLocked(req.UserID, channel, messages)
	return domain.ReadChannelMessageContentsResult{
		Channel:                         channel,
		Messages:                        messages,
		ClearedUnreadReactionMessageIDs: cleared,
		ClearedUnreadMentionMessageIDs:  clearedMentions,
	}, nil
}

func (s *ChannelStore) ListChannelUnreadMentions(_ context.Context, viewerUserID int64, filter domain.ChannelUnreadMentionsFilter) (domain.ChannelHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelUnreadMentionsLimit {
		limit = domain.MaxChannelUnreadMentionsLimit
	}
	filter.AddOffset = domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	base := make([]domain.ChannelMessage, 0, limit)
	for msgID, mention := range s.mentions[viewerUserID][filter.ChannelID] {
		if !mention.unread {
			continue
		}
		if filter.TopMsgID > 0 && mention.topID != filter.TopMsgID && !(filter.TopMsgID == 1 && mention.topID == 0) {
			continue
		}
		msg, ok := s.findMessageLocked(filter.ChannelID, msgID)
		if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		if filter.MaxID > 0 && msg.ID >= filter.MaxID {
			continue
		}
		if filter.MinID > 0 && msg.ID <= filter.MinID {
			continue
		}
		base = append(base, msg)
	}
	sort.SliceStable(base, func(i, j int) bool { return channelMessageLess(base[i], base[j]) })
	page := pageChannelMessageHistory(base, domain.ChannelRepliesFilter{
		OffsetID:   filter.OffsetID,
		OffsetDate: filter.OffsetDate,
		AddOffset:  filter.AddOffset,
		Limit:      limit,
		MaxID:      filter.MaxID,
		MinID:      filter.MinID,
	}, limit)
	out := make([]domain.ChannelMessage, 0, len(page))
	for _, msg := range page {
		out = append(out, cloneChannelMessage(msg))
	}
	s.populateChannelMessageRepliesLocked(viewerUserID, filter.ChannelID, out)
	s.populateChannelMessageReactionsLocked(viewerUserID, channel, out)
	return domain.ChannelHistory{Channel: channel, Messages: out, Count: len(base)}, nil
}

func (s *ChannelStore) ReadChannelMentions(_ context.Context, req domain.ReadChannelMentionsRequest) (domain.ReadChannelMentionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ReadChannelMentionsResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelMentionsResult{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelReadMentionsBatch {
		limit = domain.MaxChannelReadMentionsBatch
	}
	msgIDs := make([]int, 0, limit)
	for msgID, mention := range s.mentions[req.UserID][req.ChannelID] {
		if !mention.unread {
			continue
		}
		if req.TopMsgID > 0 && mention.topID != req.TopMsgID && !(req.TopMsgID == 1 && mention.topID == 0) {
			continue
		}
		msgIDs = append(msgIDs, msgID)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(msgIDs)))
	if len(msgIDs) > limit {
		msgIDs = msgIDs[:limit]
	}
	for _, msgID := range msgIDs {
		mention := s.mentions[req.UserID][req.ChannelID][msgID]
		mention.unread = false
		s.mentions[req.UserID][req.ChannelID][msgID] = mention
	}
	remaining := s.countChannelUnreadMentionsLocked(req.UserID, req.ChannelID, req.TopMsgID)
	if dialogs := s.dialogs[req.UserID]; dialogs != nil {
		dialog := dialogs[req.ChannelID]
		dialog.UnreadMentions = s.countChannelUnreadMentionsLocked(req.UserID, req.ChannelID, 0)
		dialog.UserID = req.UserID
		dialog.ChannelID = req.ChannelID
		dialogs[req.ChannelID] = dialog
	}
	offset := 0
	if remaining > 0 {
		offset = 1
	}
	return domain.ReadChannelMentionsResult{
		Channel:    channel,
		Cleared:    len(msgIDs),
		Remaining:  remaining,
		Offset:     offset,
		ChannelPts: channel.Pts,
	}, nil
}

func (s *ChannelStore) ReadChannelHistory(_ context.Context, req domain.ReadChannelHistoryRequest) (domain.ReadChannelHistoryResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, _, readOnly, err := s.channelForViewerLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelHistoryResult{}, err
	}
	maxID := req.MaxID
	if maxID <= 0 || maxID > channel.TopMessageID {
		maxID = channel.TopMessageID
	}
	if readOnly {
		return domain.ReadChannelHistoryResult{
			ChannelID: req.ChannelID,
			MaxID:     maxID,
			ReadOnly:  true,
			Pts:       channel.Pts,
			Forum:     channel.Forum,
		}, nil
	}
	member := s.members[req.ChannelID][req.UserID]
	previous := member.ReadInboxMaxID
	changed := maxID > member.ReadInboxMaxID
	var outboxUpdates []domain.ChannelReadOutboxUpdate
	if changed {
		member.ReadInboxMaxID = maxID
		member.ReadInboxDate = req.Date
		member.UnreadMark = false
		s.members[req.ChannelID][req.UserID] = member
		s.readMarks[req.ChannelID] = s.readMarks[req.ChannelID].advance(req.UserID, maxID)
		outboxUpdates = s.advanceChannelReadOutboxLocked(req.ChannelID, req.UserID, previous, maxID)
	}
	dialog := s.dialogForUserLocked(req.UserID, channel)
	dialog.ReadInboxMaxID = member.ReadInboxMaxID
	dialog.UnreadCount = s.channelUnreadCountLocked(req.UserID, channel.ID, member.ReadInboxMaxID, dialog.TopMessageID)
	dialog.UnreadMark = false
	if s.dialogs[req.UserID] == nil {
		s.dialogs[req.UserID] = make(map[int64]domain.ChannelDialog)
	}
	s.dialogs[req.UserID][req.ChannelID] = dialog
	return domain.ReadChannelHistoryResult{
		ChannelID:        req.ChannelID,
		MaxID:            maxID,
		StillUnreadCount: dialog.UnreadCount,
		Changed:          changed,
		Pts:              channel.Pts,
		Forum:            channel.Forum,
		Dialog:           dialog,
		OutboxUpdates:    outboxUpdates,
	}, nil
}

func (s *ChannelStore) advanceChannelReadOutboxLocked(channelID, readerUserID int64, previous, maxID int) []domain.ChannelReadOutboxUpdate {
	if maxID <= previous {
		return nil
	}
	lowerID := previous
	if maxID-lowerID > domain.MaxChannelReadOutboxScanMessages {
		lowerID = maxID - domain.MaxChannelReadOutboxScanMessages
	}
	bySender := make(map[int64]int, domain.MaxChannelReadOutboxFanout)
	messages := s.messages[channelID]
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.ID <= lowerID {
			break
		}
		if msg.ID > maxID || msg.Deleted || msg.SenderUserID == 0 || msg.SenderUserID == readerUserID {
			continue
		}
		if _, ok := bySender[msg.SenderUserID]; ok {
			continue
		}
		bySender[msg.SenderUserID] = msg.ID
		if len(bySender) >= domain.MaxChannelReadOutboxFanout {
			break
		}
	}
	if len(bySender) == 0 {
		return nil
	}
	senderIDs := make([]int64, 0, len(bySender))
	for userID := range bySender {
		senderIDs = append(senderIDs, userID)
	}
	sort.Slice(senderIDs, func(i, j int) bool { return senderIDs[i] < senderIDs[j] })
	channel := s.channels[channelID]
	out := make([]domain.ChannelReadOutboxUpdate, 0, len(senderIDs))
	for _, userID := range senderIDs {
		maxForSender := bySender[userID]
		member, ok := s.members[channelID][userID]
		if !ok || member.Status != domain.ChannelMemberActive || maxForSender <= member.ReadOutboxMaxID {
			continue
		}
		member.ReadOutboxMaxID = maxForSender
		s.members[channelID][userID] = member
		dialog := s.dialogForUserLocked(userID, channel)
		if dialog.ReadOutboxMaxID < maxForSender {
			dialog.ReadOutboxMaxID = maxForSender
		}
		if s.dialogs[userID] == nil {
			s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
		}
		s.dialogs[userID][channelID] = dialog
		out = append(out, domain.ChannelReadOutboxUpdate{UserID: userID, MaxID: maxForSender})
	}
	return out
}

func (s *ChannelStore) ListMessageReadParticipants(_ context.Context, req domain.ChannelReadParticipantsRequest) (domain.ChannelReadParticipantsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelReadParticipantsResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	msg, found := s.findMessageLocked(req.ChannelID, req.MessageID)
	if !found || msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelReadParticipantsResult{}, domain.ErrMessageIDInvalid
	}
	result := domain.ChannelReadParticipantsResult{
		Channel: channel,
		Message: cloneChannelMessage(msg),
	}
	if !channel.Megagroup || channel.ParticipantsHidden || channel.ParticipantsCount > domain.MaxChannelReadParticipants {
		return result, nil
	}
	now := req.Date
	if now > 0 && msg.Date+domain.ChannelReadMarkExpirePeriod <= now {
		return result, nil
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelReadParticipants {
		limit = domain.MaxChannelReadParticipants
	}
	for _, reader := range s.members[req.ChannelID] {
		if reader.UserID == req.UserID || reader.Status != domain.ChannelMemberActive || reader.BannedRights.ViewMessages {
			continue
		}
		if reader.ReadInboxDate <= 0 {
			continue
		}
		if reader.AvailableMinID >= req.MessageID || reader.ReadInboxMaxID < req.MessageID {
			continue
		}
		result.Participants = append(result.Participants, domain.ChannelReadParticipant{
			UserID: reader.UserID,
			Date:   reader.ReadInboxDate,
		})
		if len(result.Participants) >= limit {
			break
		}
	}
	sort.Slice(result.Participants, func(i, j int) bool {
		if result.Participants[i].Date == result.Participants[j].Date {
			return result.Participants[i].UserID < result.Participants[j].UserID
		}
		return result.Participants[i].Date < result.Participants[j].Date
	})
	return result, nil
}

func (s *ChannelStore) channelThreadUnreadCountLocked(viewerUserID, channelID int64, rootID, readMaxID int) int {
	unread := 0
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= readMaxID || msg.SenderUserID == viewerUserID {
			continue
		}
		if channelReplyBelongsToRoot(msg, channelID, rootID) {
			unread++
			// Clamp to MaxDialogUnreadCount (P1-v), the same min(actual, cap) semantics as postgres's LIMIT subquery.
			if unread >= domain.MaxDialogUnreadCount {
				break
			}
		}
	}
	return unread
}

func (s *ChannelStore) channelUnreadCountLocked(viewerUserID, channelID int64, readMaxID, topID int) int {
	if viewerUserID == 0 || channelID == 0 || topID <= readMaxID {
		return 0
	}
	unread := 0
	for _, msg := range s.messages[channelID] {
		if msg.Deleted || msg.ID <= readMaxID || msg.ID > topID || msg.SenderUserID == viewerUserID {
			continue
		}
		unread++
		// Clamp to MaxDialogUnreadCount (P1-v), the same min(actual, cap) semantics as postgres's LIMIT subquery.
		if unread >= domain.MaxDialogUnreadCount {
			break
		}
	}
	return unread
}

func (s *ChannelStore) addChannelUnreadMentionsLocked(channelID int64, msg domain.ChannelMessage, senderUserID int64, userIDs []int64) {
	if len(userIDs) == 0 || msg.ID == 0 {
		return
	}
	seen := make(map[int64]struct{}, len(userIDs))
	written := 0
	topID := channelMentionTopID(msg)
	for _, userID := range userIDs {
		if userID == 0 || userID == senderUserID {
			continue
		}
		if _, ok := seen[userID]; ok {
			continue
		}
		seen[userID] = struct{}{}
		member, ok := s.members[channelID][userID]
		if !ok || member.Status != domain.ChannelMemberActive || member.BannedRights.ViewMessages {
			continue
		}
		if msg.ID <= member.AvailableMinID || msg.ID <= member.ReadInboxMaxID {
			continue
		}
		if s.mentions[userID] == nil {
			s.mentions[userID] = make(map[int64]map[int]memoryMention)
		}
		if s.mentions[userID][channelID] == nil {
			s.mentions[userID][channelID] = make(map[int]memoryMention)
		}
		s.mentions[userID][channelID][msg.ID] = memoryMention{topID: topID, unread: true}
		written++
		if written == domain.MaxChannelMentionRecipients {
			return
		}
	}
}

func (s *ChannelStore) countChannelUnreadMentionsLocked(userID, channelID int64, topMsgID int) int {
	count := 0
	for _, mention := range s.mentions[userID][channelID] {
		if !mention.unread {
			continue
		}
		if topMsgID == 0 || mention.topID == topMsgID || (topMsgID == 1 && mention.topID == 0) {
			count++
		}
	}
	return count
}

func (s *ChannelStore) deleteChannelUnreadMentionsLocked(channelID int64, ids []int) {
	if len(ids) == 0 {
		return
	}
	set := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	for userID, byChannel := range s.mentions {
		mentions := byChannel[channelID]
		if len(mentions) == 0 {
			continue
		}
		for id := range set {
			delete(mentions, id)
		}
		if len(mentions) == 0 {
			delete(byChannel, channelID)
		}
		if len(byChannel) == 0 {
			delete(s.mentions, userID)
		}
	}
}

func (s *ChannelStore) deleteChannelUnreadMentionsUpToLocked(userID, channelID int64, maxID int) {
	if maxID <= 0 || len(s.mentions[userID][channelID]) == 0 {
		return
	}
	for id := range s.mentions[userID][channelID] {
		if id <= maxID {
			delete(s.mentions[userID][channelID], id)
		}
	}
	if len(s.mentions[userID][channelID]) == 0 {
		delete(s.mentions[userID], channelID)
	}
	if len(s.mentions[userID]) == 0 {
		delete(s.mentions, userID)
	}
}

func channelMentionTopID(msg domain.ChannelMessage) int {
	if msg.ReplyTo == nil {
		return 0
	}
	if msg.ReplyTo.TopMessageID > 0 {
		return msg.ReplyTo.TopMessageID
	}
	return msg.ReplyTo.MessageID
}

func (s *ChannelStore) populateChannelMessageUnreadFlagsLocked(viewerUserID int64, messages []domain.ChannelMessage) {
	if viewerUserID == 0 || len(messages) == 0 {
		return
	}
	for i := range messages {
		if messages[i].ChannelID == 0 || messages[i].ID <= 0 {
			continue
		}
		mention, ok := s.mentions[viewerUserID][messages[i].ChannelID][messages[i].ID]
		if !ok {
			continue
		}
		messages[i].Mentioned = true
		messages[i].MediaUnread = mention.unread
	}
}

// clearChannelMentionsForUserLocked clears the user's mention state on leave/kick.
func (s *ChannelStore) clearChannelMentionsForUserLocked(channelID, userID int64) {
	if byChannel := s.mentions[userID]; byChannel != nil {
		delete(byChannel, channelID)
		if len(byChannel) == 0 {
			delete(s.mentions, userID)
		}
	}
	if dialogs := s.dialogs[userID]; dialogs != nil {
		if dialog, ok := dialogs[channelID]; ok {
			dialog.UnreadMentions = 0
			dialogs[channelID] = dialog
		}
	}
}
