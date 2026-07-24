package memory

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"telesrv/internal/domain"
	"time"
)

func (s *ChannelStore) SetAvailableReactions(_ context.Context, userID, channelID int64, policy domain.ChannelReactionPolicy) (domain.Channel, error) {
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
	if !canChangeChannelInfo(member) {
		return domain.Channel{}, domain.ErrChannelAdminRequired
	}
	channel.ReactionPolicy = copyChannelReactionPolicy(policy)
	s.channels[channelID] = channel
	return cloneChannel(channel), nil
}

func (s *ChannelStore) SetChannelMessageReactions(_ context.Context, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	if len(req.Reactions) > domain.MaxChannelMessageReactionsPerUser {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	for _, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
		}
	}
	req.Reactions = domain.TrimMessageReactionsToUserMax(req.Reactions, req.ReactionsPerUserMax)
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if len(req.Reactions) > 0 {
		selfBoostsApplied := 0
		if channel.Megagroup {
			selfBoostsApplied = s.selfBoostsAppliedLocked(req.UserID, req.ChannelID, req.Date)
		}
		if domain.ChannelBannedRightsBlockReactions(channel, member, selfBoostsApplied) {
			return domain.ChannelMessageReactionsResult{}, domain.ErrChannelWriteForbidden
		}
	}
	idx, ok := s.findMessageIndexLocked(req.ChannelID, req.MessageID)
	if !ok {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	msg := s.messages[req.ChannelID][idx]
	if msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	// 仅新增/替换受策略约束；空向量是撤销，策略收紧后也必须允许撤销存量 reaction。
	for _, reaction := range req.Reactions {
		if !channel.ReactionPolicy.AllowsReaction(reaction) {
			return domain.ChannelMessageReactionsResult{}, domain.ErrReactionInvalid
		}
	}
	if len(req.Reactions) > 0 {
		// 官方 REACTIONS_TOO_MANY 只挡「引入消息上尚不存在的新种类」：存量已超限
		//（管理员调低 reactions_limit / 部署前数据）时，重发自己的 reaction 或给
		// 已有种类投票必须放行，否则客户端点击合法 chip 也会收到 400。
		existing := make(map[string]struct{})
		final := make(map[string]struct{})
		for userID, rows := range s.reactions[req.ChannelID][req.MessageID] {
			for _, row := range rows {
				key := messageReactionKey(row.Reaction)
				existing[key] = struct{}{}
				if userID != req.UserID {
					final[key] = struct{}{}
				}
			}
		}
		newKind := false
		for _, reaction := range req.Reactions {
			key := messageReactionKey(reaction)
			if _, ok := existing[key]; !ok {
				newKind = true
			}
			final[key] = struct{}{}
		}
		if newKind && len(final) > channel.ReactionPolicy.UniqueReactionsLimit() {
			return domain.ChannelMessageReactionsResult{}, domain.ErrReactionsTooMany
		}
	}
	if s.reactions[req.ChannelID] == nil {
		s.reactions[req.ChannelID] = make(map[int]map[int64][]domain.ChannelMessagePeerReaction)
	}
	if s.reactions[req.ChannelID][req.MessageID] == nil {
		s.reactions[req.ChannelID][req.MessageID] = make(map[int64][]domain.ChannelMessagePeerReaction)
	}
	// 广播频道 reaction 匿名（官方语义），作者不收 unread 角标，不写 unread 簿记。
	unreadEligible := !channel.Broadcast || channel.Megagroup
	if len(req.Reactions) == 0 {
		delete(s.reactions[req.ChannelID][req.MessageID], req.UserID)
	} else {
		rows := make([]domain.ChannelMessagePeerReaction, 0, len(req.Reactions))
		for i, reaction := range req.Reactions {
			rows = append(rows, domain.ChannelMessagePeerReaction{
				ChannelID:    req.ChannelID,
				MessageID:    req.MessageID,
				SenderUserID: msg.SenderUserID,
				UserID:       req.UserID,
				Reaction:     reaction,
				Big:          req.Big,
				Unread:       unreadEligible && msg.SenderUserID != 0 && msg.SenderUserID != req.UserID,
				ChosenOrder:  i + 1,
				Date:         req.Date,
			})
		}
		s.reactions[req.ChannelID][req.MessageID][req.UserID] = rows
		if s.top[req.UserID] == nil {
			s.top[req.UserID] = make(map[string]domain.TopMessageReaction)
		}
		for _, reaction := range req.Reactions {
			key := messageReactionKey(reaction)
			row := s.top[req.UserID][key]
			row.UserID = req.UserID
			row.Reaction = reaction
			row.Count++
			row.Date = req.Date
			s.top[req.UserID][key] = row
		}
		if req.AddToRecent {
			if s.recent[req.UserID] == nil {
				s.recent[req.UserID] = make(map[string]domain.RecentMessageReaction)
			}
			for _, reaction := range req.Reactions {
				s.recent[req.UserID][messageReactionKey(reaction)] = domain.RecentMessageReaction{
					UserID:   req.UserID,
					Reaction: reaction,
					Date:     req.Date,
				}
			}
		}
	}
	if unreadEligible {
		s.refreshChannelUnreadReactionsDialogLocked(msg.SenderUserID, req.ChannelID)
	}
	reactions := s.channelMessageReactionsLocked(req.UserID, channel, req.MessageID)
	msg = cloneChannelMessage(msg)
	msg.Reactions = cloneChannelMessageReactionsPtr(&reactions)
	// sendReaction 实时推送走在线 viewer scope（rpc 层封顶），不预热全量成员列表。
	return domain.ChannelMessageReactionsResult{
		Channel:    cloneChannel(channel),
		Message:    msg,
		Messages:   []domain.ChannelMessage{msg},
		Reactions:  cloneChannelMessageReactions(reactions),
		Recipients: []int64{req.UserID},
	}, nil
}

type memoryPaidReaction struct {
	stars     int64
	anonymous bool
	date      int
}

// AddChannelMessagePaidReaction 累计 viewer 对一条广播频道消息的付费 reaction 星数（内存镜像）。
func (s *ChannelStore) AddChannelMessagePaidReaction(_ context.Context, req domain.SendChannelPaidReactionRequest) (domain.ChannelMessagePaidReactionResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	if req.Stars <= 0 || req.Stars > domain.MaxPaidReactionStarsPerRequest {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrChannelInvalid
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessagePaidReactionResult{}, err
	}
	if !channel.Broadcast || channel.Megagroup {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrReactionInvalid
	}
	msg, ok := s.findMessageLocked(req.ChannelID, req.MessageID)
	if !ok || msg.Deleted || msg.Action != nil || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessagePaidReactionResult{}, domain.ErrMessageIDInvalid
	}
	if s.paidReactions[req.ChannelID] == nil {
		s.paidReactions[req.ChannelID] = make(map[int]map[int64]memoryPaidReaction)
	}
	if s.paidReactions[req.ChannelID][req.MessageID] == nil {
		s.paidReactions[req.ChannelID][req.MessageID] = make(map[int64]memoryPaidReaction)
	}
	prev := s.paidReactions[req.ChannelID][req.MessageID][req.UserID]
	s.paidReactions[req.ChannelID][req.MessageID][req.UserID] = memoryPaidReaction{
		stars:     prev.stars + req.Stars,
		anonymous: req.Anonymous,
		date:      req.Date,
	}
	paid := s.aggregatePaidReactionsLocked(req.ChannelID, req.MessageID, req.UserID)
	outMsg := cloneChannelMessage(msg)
	reactions := s.channelMessageReactionsLocked(req.UserID, channel, req.MessageID)
	outMsg.Reactions = cloneChannelMessageReactionsPtr(&reactions)
	return domain.ChannelMessagePaidReactionResult{
		Channel:    cloneChannel(channel),
		Message:    outMsg,
		Paid:       paid,
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

func (s *ChannelStore) aggregatePaidReactionsLocked(channelID int64, messageID int, viewerUserID int64) domain.ChannelMessagePaidReactions {
	byUser := s.paidReactions[channelID][messageID]
	reactors := make([]domain.PaidReactor, 0, len(byUser))
	var out domain.ChannelMessagePaidReactions
	for userID, entry := range byUser {
		r := domain.PaidReactor{UserID: userID, Stars: entry.stars, Anonymous: entry.anonymous, My: userID == viewerUserID}
		out.TotalStars += entry.stars
		if r.My {
			out.MyStars = entry.stars
			out.MyAnonymous = entry.anonymous
		}
		reactors = append(reactors, r)
	}
	sort.Slice(reactors, func(i, j int) bool {
		if reactors[i].Stars != reactors[j].Stars {
			return reactors[i].Stars > reactors[j].Stars
		}
		return reactors[i].UserID < reactors[j].UserID
	})
	myInTop := false
	for i, r := range reactors {
		if i >= domain.MaxPaidReactionTopReactors {
			break
		}
		out.TopReactors = append(out.TopReactors, r)
		if r.My {
			myInTop = true
		}
	}
	if out.MyStars > 0 && !myInTop {
		for _, r := range reactors {
			if r.My {
				out.TopReactors = append(out.TopReactors, r)
				break
			}
		}
	}
	return out
}

func (s *ChannelStore) DeleteChannelParticipantReaction(_ context.Context, req domain.DeleteChannelParticipantReactionRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID || req.ParticipantUserID == 0 {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelAdminRequired
	}
	msg, ok := s.findMessageLocked(req.ChannelID, req.MessageID)
	if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	if s.reactions[req.ChannelID] != nil && s.reactions[req.ChannelID][req.MessageID] != nil {
		delete(s.reactions[req.ChannelID][req.MessageID], req.ParticipantUserID)
	}
	s.refreshChannelUnreadReactionsDialogLocked(msg.SenderUserID, req.ChannelID)
	reactions := s.channelMessageReactionsLocked(req.UserID, channel, req.MessageID)
	outMsg := cloneChannelMessage(msg)
	outMsg.Reactions = cloneChannelMessageReactionsPtr(&reactions)
	return domain.ChannelMessageReactionsResult{
		Channel:    cloneChannel(channel),
		Message:    outMsg,
		Messages:   []domain.ChannelMessage{outMsg},
		Reactions:  cloneChannelMessageReactions(reactions),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

func (s *ChannelStore) DeleteChannelParticipantReactions(_ context.Context, req domain.DeleteChannelParticipantReactionsRequest) (domain.DeleteChannelParticipantReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ParticipantUserID == 0 {
		return domain.DeleteChannelParticipantReactionsResult{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxDeleteParticipantReactionsBatch {
		req.Limit = domain.MaxDeleteParticipantReactionsBatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelParticipantReactionsResult{}, err
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelParticipantReactionsResult{}, domain.ErrChannelAdminRequired
	}
	type reactionMsg struct {
		id     int
		sender int64
		date   int
	}
	candidates := make([]reactionMsg, 0)
	for msgID, byUser := range s.reactions[req.ChannelID] {
		rows := byUser[req.ParticipantUserID]
		if len(rows) == 0 {
			continue
		}
		msg, ok := s.findMessageLocked(req.ChannelID, msgID)
		if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		item := reactionMsg{id: msgID, sender: msg.SenderUserID}
		for _, row := range rows {
			if row.Date > item.date {
				item.date = row.Date
			}
		}
		candidates = append(candidates, item)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].date != candidates[j].date {
			return candidates[i].date > candidates[j].date
		}
		return candidates[i].id > candidates[j].id
	})
	if len(candidates) > req.Limit {
		candidates = candidates[:req.Limit]
	}
	owners := make(map[int64]struct{})
	ids := make([]int, 0, len(candidates))
	for _, item := range candidates {
		if s.reactions[req.ChannelID] != nil && s.reactions[req.ChannelID][item.id] != nil {
			delete(s.reactions[req.ChannelID][item.id], req.ParticipantUserID)
		}
		if item.sender != 0 {
			owners[item.sender] = struct{}{}
		}
		ids = append(ids, item.id)
	}
	for ownerID := range owners {
		s.refreshChannelUnreadReactionsDialogLocked(ownerID, req.ChannelID)
	}
	messages := make([]domain.ChannelMessage, 0, len(ids))
	for _, id := range ids {
		msg, ok := s.findMessageLocked(req.ChannelID, id)
		if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		reactions := s.channelMessageReactionsLocked(req.UserID, channel, id)
		outMsg := cloneChannelMessage(msg)
		outMsg.Reactions = cloneChannelMessageReactionsPtr(&reactions)
		messages = append(messages, outMsg)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].ID > messages[j].ID })
	return domain.DeleteChannelParticipantReactionsResult{
		Channel:    cloneChannel(channel),
		Messages:   messages,
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		Deleted:    len(ids),
	}, nil
}

func (s *ChannelStore) GetChannelMessageReactions(_ context.Context, req domain.ChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	if len(req.IDs) > domain.MaxGetMessageIDs {
		return domain.ChannelMessageReactionsResult{}, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsResult{}, err
	}
	wanted := make(map[int]struct{}, len(req.IDs))
	for _, id := range req.IDs {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return domain.ChannelMessageReactionsResult{}, domain.ErrMessageIDInvalid
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
		item := cloneChannelMessage(msg)
		reactions := s.channelMessageReactionsLocked(req.UserID, channel, msg.ID)
		item.Reactions = cloneChannelMessageReactionsPtr(&reactions)
		messages = append(messages, item)
	}
	sort.Slice(messages, func(i, j int) bool { return messages[i].ID > messages[j].ID })
	res := domain.ChannelMessageReactionsResult{
		Channel:  cloneChannel(channel),
		Messages: messages,
	}
	if len(messages) == 1 {
		res.Message = messages[0]
		if messages[0].Reactions != nil {
			res.Reactions = cloneChannelMessageReactions(*messages[0].Reactions)
		}
	}
	return res, nil
}

func (s *ChannelStore) ListChannelMessageReactions(_ context.Context, req domain.ChannelMessageReactionsListRequest) (domain.ChannelMessageReactionsList, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 || req.MessageID > domain.MaxMessageBoxID {
		return domain.ChannelMessageReactionsList{}, domain.ErrChannelInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxChannelMessageReactionListLimit {
		req.Limit = domain.MaxChannelMessageReactionListLimit
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionsList{}, err
	}
	if channel.Broadcast && !channel.Megagroup {
		return domain.ChannelMessageReactionsList{}, domain.ErrChannelRightForbidden
	}
	msg, ok := s.findMessageLocked(req.ChannelID, req.MessageID)
	if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionsList{}, domain.ErrMessageIDInvalid
	}
	rows := s.channelMessageReactionRowsLocked(req.ChannelID, req.MessageID, req.UserID, req.Reaction)
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		if rows[i].UserID != rows[j].UserID {
			return rows[i].UserID > rows[j].UserID
		}
		return messageReactionKey(rows[i].Reaction) < messageReactionKey(rows[j].Reaction)
	})
	total := len(rows)
	if req.Offset != "" {
		if offset, ok := parseMemoryReactionOffset(req.Offset); ok {
			filtered := rows[:0]
			for _, row := range rows {
				if memoryReactionAfterOffset(row, offset) {
					filtered = append(filtered, row)
				}
			}
			rows = filtered
		}
	}
	next := ""
	if len(rows) > req.Limit {
		rows = rows[:req.Limit]
		next = memoryReactionOffset(rows[len(rows)-1])
	}
	return domain.ChannelMessageReactionsList{
		Channel:    cloneChannel(channel),
		Message:    cloneChannelMessage(msg),
		Count:      total,
		Reactions:  cloneChannelPeerReactions(rows),
		NextOffset: next,
	}, nil
}

func (s *ChannelStore) FindChannelMessageReaction(_ context.Context, req domain.ChannelMessageReactionLookupRequest) (domain.ChannelMessageReactionLookup, bool, error) {
	if req.ViewerUserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 ||
		req.MessageID > domain.MaxMessageBoxID || req.ReactorUserID == 0 {
		return domain.ChannelMessageReactionLookup{}, false, domain.ErrChannelInvalid
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(req.ViewerUserID, req.ChannelID)
	if err != nil {
		return domain.ChannelMessageReactionLookup{}, false, err
	}
	message, ok := s.findMessageLocked(req.ChannelID, req.MessageID)
	if !ok || message.Deleted || message.ID <= member.AvailableMinID {
		return domain.ChannelMessageReactionLookup{}, false, domain.ErrMessageIDInvalid
	}
	rows := cloneChannelPeerReactions(s.reactions[req.ChannelID][req.MessageID][req.ReactorUserID])
	if len(rows) == 0 {
		return domain.ChannelMessageReactionLookup{
			Channel: cloneChannel(channel), Message: cloneChannelMessage(message),
		}, false, nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ChosenOrder != rows[j].ChosenOrder {
			return rows[i].ChosenOrder < rows[j].ChosenOrder
		}
		return messageReactionKey(rows[i].Reaction) < messageReactionKey(rows[j].Reaction)
	})
	return domain.ChannelMessageReactionLookup{
		Channel: cloneChannel(channel), Message: cloneChannelMessage(message),
		Reactions: rows,
	}, true, nil
}

func (s *ChannelStore) RecordMessageReactionUse(_ context.Context, userID int64, reactions []domain.MessageReaction, addToRecent bool, date int) error {
	if userID == 0 || len(reactions) == 0 {
		return nil
	}
	if date == 0 {
		date = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.top[userID] == nil {
		s.top[userID] = make(map[string]domain.TopMessageReaction)
	}
	if addToRecent && s.recent[userID] == nil {
		s.recent[userID] = make(map[string]domain.RecentMessageReaction)
	}
	for _, reaction := range reactions {
		if reaction.Type != domain.MessageReactionEmoji || strings.TrimSpace(reaction.Emoticon) == "" {
			continue
		}
		key := messageReactionKey(reaction)
		row := s.top[userID][key]
		row.UserID = userID
		row.Reaction = reaction
		row.Count++
		row.Date = date
		s.top[userID][key] = row
		if addToRecent {
			s.recent[userID][key] = domain.RecentMessageReaction{
				UserID:   userID,
				Reaction: reaction,
				Date:     date,
			}
		}
	}
	return nil
}

func (s *ChannelStore) ListTopMessageReactions(_ context.Context, userID int64, limit int) ([]domain.MessageReaction, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 {
		return []domain.MessageReaction{}, nil
	}
	if limit > domain.MaxTopMessageReactions {
		limit = domain.MaxTopMessageReactions
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]domain.TopMessageReaction, 0, len(s.top[userID]))
	for _, row := range s.top[userID] {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		if rows[i].Reaction.Type != rows[j].Reaction.Type {
			return rows[i].Reaction.Type < rows[j].Reaction.Type
		}
		return rows[i].Reaction.Value() < rows[j].Reaction.Value()
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]domain.MessageReaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Reaction)
	}
	return out, nil
}

func (s *ChannelStore) ListRecentMessageReactions(_ context.Context, userID int64, limit int) ([]domain.MessageReaction, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 {
		return []domain.MessageReaction{}, nil
	}
	if limit > domain.MaxRecentMessageReactions {
		limit = domain.MaxRecentMessageReactions
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]domain.RecentMessageReaction, 0, len(s.recent[userID]))
	for _, row := range s.recent[userID] {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		if rows[i].Reaction.Type != rows[j].Reaction.Type {
			return rows[i].Reaction.Type < rows[j].Reaction.Type
		}
		return rows[i].Reaction.Value() < rows[j].Reaction.Value()
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]domain.MessageReaction, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Reaction)
	}
	return out, nil
}

func (s *ChannelStore) ClearRecentMessageReactions(_ context.Context, userID int64) error {
	if userID == 0 {
		return domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recent, userID)
	return nil
}

func (s *ChannelStore) ListSavedReactionTags(_ context.Context, userID int64, limit int) ([]domain.SavedReactionTag, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 {
		return []domain.SavedReactionTag{}, nil
	}
	if limit > domain.MaxSavedReactionTags {
		limit = domain.MaxSavedReactionTags
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rows := make([]domain.SavedReactionTag, 0, len(s.savedTags[userID]))
	for _, row := range s.savedTags[userID] {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count != rows[j].Count {
			return rows[i].Count > rows[j].Count
		}
		if rows[i].Reaction.Type != rows[j].Reaction.Type {
			return rows[i].Reaction.Type < rows[j].Reaction.Type
		}
		return rows[i].Reaction.Value() < rows[j].Reaction.Value()
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *ChannelStore) UpsertSavedReactionTag(_ context.Context, tag domain.SavedReactionTag) error {
	if tag.UserID == 0 || tag.Reaction.Type != domain.MessageReactionEmoji || strings.TrimSpace(tag.Reaction.Emoticon) == "" {
		return domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.savedTags[tag.UserID] == nil {
		s.savedTags[tag.UserID] = make(map[string]domain.SavedReactionTag)
	}
	tag.Reaction.Emoticon = strings.TrimSpace(tag.Reaction.Emoticon)
	if tag.Count < 0 {
		tag.Count = 0
	}
	s.savedTags[tag.UserID][messageReactionKey(tag.Reaction)] = tag
	return nil
}

func (s *ChannelStore) ListChannelUnreadReactions(_ context.Context, viewerUserID int64, filter domain.ChannelUnreadReactionsFilter) (domain.ChannelHistory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channel, member, err := s.channelAndMemberLocked(viewerUserID, filter.ChannelID)
	if err != nil {
		return domain.ChannelHistory{}, err
	}
	limit := filter.Limit
	if limit <= 0 || limit > domain.MaxChannelUnreadReactionsLimit {
		limit = domain.MaxChannelUnreadReactionsLimit
	}
	filter.AddOffset = domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	base := make([]domain.ChannelMessage, 0, limit)
	for msgID, byUser := range s.reactions[filter.ChannelID] {
		msg, ok := s.findMessageLocked(filter.ChannelID, msgID)
		if !ok || msg.Deleted || msg.ID <= member.AvailableMinID {
			continue
		}
		if filter.TopMsgID > 0 && msg.ID != filter.TopMsgID && channelMentionTopID(msg) != filter.TopMsgID {
			continue
		}
		if filter.MaxID > 0 && msg.ID >= filter.MaxID {
			continue
		}
		if filter.MinID > 0 && msg.ID <= filter.MinID {
			continue
		}
		if !channelMessageHasUnreadReactionForUser(byUser, viewerUserID) {
			continue
		}
		base = append(base, msg)
	}
	sort.SliceStable(base, func(i, j int) bool { return channelMessageLess(base[i], base[j]) })
	page := pageChannelMessageHistory(base, domain.ChannelRepliesFilter{
		OffsetID:  filter.OffsetID,
		AddOffset: filter.AddOffset,
		Limit:     limit,
		MaxID:     filter.MaxID,
		MinID:     filter.MinID,
	}, limit)
	out := make([]domain.ChannelMessage, 0, len(page))
	for _, msg := range page {
		out = append(out, cloneChannelMessage(msg))
	}
	s.populateChannelMessageRepliesLocked(viewerUserID, filter.ChannelID, out)
	s.populateChannelMessageReactionsLocked(viewerUserID, channel, out)
	return domain.ChannelHistory{Channel: channel, Messages: out, Count: len(base)}, nil
}

func (s *ChannelStore) ReadChannelReactions(_ context.Context, req domain.ReadChannelReactionsRequest) (domain.ReadChannelReactionsResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ReadChannelReactionsResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.ReadChannelReactionsResult{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelReadReactionsBatch {
		limit = domain.MaxChannelReadReactionsBatch
	}
	msgIDs := make([]int, 0, limit)
	for msgID, byUser := range s.reactions[req.ChannelID] {
		msg, ok := s.findMessageLocked(req.ChannelID, msgID)
		if !ok || msg.Deleted {
			continue
		}
		if req.TopMsgID > 0 && msg.ID != req.TopMsgID && channelMentionTopID(msg) != req.TopMsgID {
			continue
		}
		if channelMessageHasUnreadReactionForUser(byUser, req.UserID) {
			msgIDs = append(msgIDs, msgID)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(msgIDs)))
	if len(msgIDs) > limit {
		msgIDs = msgIDs[:limit]
	}
	for _, msgID := range msgIDs {
		for reactedUserID, rows := range s.reactions[req.ChannelID][msgID] {
			changed := false
			for i := range rows {
				if rows[i].SenderUserID == req.UserID && rows[i].Unread {
					rows[i].Unread = false
					changed = true
				}
			}
			if changed {
				s.reactions[req.ChannelID][msgID][reactedUserID] = rows
			}
		}
	}
	remaining := s.countChannelUnreadReactionsLocked(req.UserID, req.ChannelID, req.TopMsgID)
	s.refreshChannelUnreadReactionsDialogLocked(req.UserID, req.ChannelID)
	offset := 0
	if remaining > 0 {
		offset = 1
	}
	return domain.ReadChannelReactionsResult{
		Channel:    channel,
		Cleared:    len(msgIDs),
		Remaining:  remaining,
		Offset:     offset,
		ChannelPts: channel.Pts,
	}, nil
}

func (s *ChannelStore) countChannelUnreadReactionsLocked(userID, channelID int64, topMsgID int) int {
	count := 0
	availableMinID := 0
	if member, ok := s.members[channelID][userID]; ok {
		availableMinID = member.AvailableMinID
	}
	for msgID, byUser := range s.reactions[channelID] {
		msg, ok := s.findMessageLocked(channelID, msgID)
		if !ok || msg.Deleted || msg.ID <= availableMinID {
			continue
		}
		if topMsgID > 0 && msg.ID != topMsgID && channelMentionTopID(msg) != topMsgID {
			continue
		}
		if channelMessageHasUnreadReactionForUser(byUser, userID) {
			count++
		}
	}
	return count
}

func channelMessageHasUnreadReactionForUser(byUser map[int64][]domain.ChannelMessagePeerReaction, userID int64) bool {
	for _, rows := range byUser {
		for _, row := range rows {
			if row.SenderUserID == userID && row.UserID != userID && row.Unread {
				return true
			}
		}
	}
	return false
}

func (s *ChannelStore) refreshChannelUnreadReactionsDialogLocked(userID, channelID int64) {
	if userID == 0 || channelID == 0 {
		return
	}
	channel, ok := s.channels[channelID]
	if !ok {
		return
	}
	member, ok := s.members[channelID][userID]
	if !ok || member.Status != domain.ChannelMemberActive || member.BannedRights.ViewMessages {
		return
	}
	dialog := s.dialogForUserLocked(userID, channel)
	dialog.UnreadReactions = s.countChannelUnreadReactionsLocked(userID, channelID, 0)
	if s.dialogs[userID] == nil {
		s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
	}
	s.dialogs[userID][channelID] = dialog
}

func copyChannelReactionPolicy(in domain.ChannelReactionPolicy) domain.ChannelReactionPolicy {
	in.Emoticons = append([]string(nil), in.Emoticons...)
	in.CustomEmojiIDs = append([]int64(nil), in.CustomEmojiIDs...)
	return in
}

func (s *ChannelStore) populateChannelMessageReactionsLocked(viewerUserID int64, channel domain.Channel, messages []domain.ChannelMessage) {
	if len(messages) == 0 || channel.ID == 0 {
		return
	}
	s.populateChannelMessageUnreadFlagsLocked(viewerUserID, messages)
	now := int(time.Now().Unix())
	for i := range messages {
		if messages[i].ChannelID != channel.ID || messages[i].ID <= 0 {
			continue
		}
		// poll 与 reaction 同点位 enrich：所有频道消息读路径经此填充 viewer 态。
		messages[i].Media = enrichPollMediaForViewer(s.polls, messages[i].Media, viewerUserID, now)
		reactions := s.channelMessageReactionsLocked(viewerUserID, channel, messages[i].ID)
		if len(reactions.Results) == 0 && len(reactions.Recent) == 0 {
			continue
		}
		messages[i].Reactions = cloneChannelMessageReactionsPtr(&reactions)
	}
}

func (s *ChannelStore) populateChannelMessagesReactionsLocked(viewerUserID int64, channels []domain.Channel, messages []domain.ChannelMessage) {
	if len(messages) == 0 {
		return
	}
	s.populateChannelMessageUnreadFlagsLocked(viewerUserID, messages)
	now := int(time.Now().Unix())
	channelsByID := make(map[int64]domain.Channel, len(channels))
	for _, ch := range channels {
		if ch.ID != 0 {
			channelsByID[ch.ID] = ch
		}
	}
	for i := range messages {
		messages[i].Media = enrichPollMediaForViewer(s.polls, messages[i].Media, viewerUserID, now)
		ch := channelsByID[messages[i].ChannelID]
		if ch.ID == 0 {
			ch = s.channels[messages[i].ChannelID]
		}
		if ch.ID == 0 {
			continue
		}
		reactions := s.channelMessageReactionsLocked(viewerUserID, ch, messages[i].ID)
		if len(reactions.Results) == 0 && len(reactions.Recent) == 0 {
			continue
		}
		messages[i].Reactions = cloneChannelMessageReactionsPtr(&reactions)
	}
}

type memoryReactionCursor struct {
	date         int
	userID       int64
	reactionType domain.MessageReactionType
	value        string
	legacyValue  bool
}

func (s *ChannelStore) channelMessageReactionsLocked(viewerUserID int64, channel domain.Channel, messageID int) domain.ChannelMessageReactions {
	rows := s.channelMessageReactionRowsLocked(channel.ID, messageID, viewerUserID, nil)
	out := domain.ChannelMessageReactions{
		CanSeeList: !channel.Broadcast || channel.Megagroup,
		Results:    []domain.ChannelMessageReactionCount{},
		Recent:     []domain.ChannelMessagePeerReaction{},
	}
	// 付费 reaction 与普通 reaction 分表：即便无普通 reaction 也要回显 ReactionPaid。
	if paid := s.aggregatePaidReactionsLocked(channel.ID, messageID, viewerUserID); paid.TotalStars > 0 {
		out.Paid = &paid
	}
	if len(rows) == 0 {
		return out
	}
	type aggregate struct {
		reaction    domain.MessageReaction
		count       int
		chosenOrder int
		latestDate  int
	}
	aggregates := make(map[string]*aggregate)
	for _, row := range rows {
		key := messageReactionKey(row.Reaction)
		item := aggregates[key]
		if item == nil {
			item = &aggregate{reaction: row.Reaction}
			aggregates[key] = item
		}
		item.count++
		if row.My && row.ChosenOrder > 0 {
			item.chosenOrder = row.ChosenOrder
		}
		if row.Date > item.latestDate {
			item.latestDate = row.Date
		}
	}
	items := make([]aggregate, 0, len(aggregates))
	for _, item := range aggregates {
		items = append(items, *item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		if items[i].latestDate != items[j].latestDate {
			return items[i].latestDate > items[j].latestDate
		}
		return messageReactionKey(items[i].reaction) < messageReactionKey(items[j].reaction)
	})
	for _, item := range items {
		out.Results = append(out.Results, domain.ChannelMessageReactionCount{
			Reaction:    item.reaction,
			Count:       item.count,
			ChosenOrder: item.chosenOrder,
		})
	}
	// 广播频道 reaction 匿名：与官方一致只下发计数，不暴露 recent 反应者身份。
	if channel.Broadcast && !channel.Megagroup {
		return out
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date > rows[j].Date
		}
		if rows[i].UserID != rows[j].UserID {
			return rows[i].UserID > rows[j].UserID
		}
		return messageReactionKey(rows[i].Reaction) < messageReactionKey(rows[j].Reaction)
	})
	if len(rows) > domain.MaxChannelMessageReactionRecent {
		rows = rows[:domain.MaxChannelMessageReactionRecent]
	}
	out.Recent = cloneChannelPeerReactions(rows)
	return out
}

func (s *ChannelStore) channelMessageReactionRowsLocked(channelID int64, messageID int, viewerUserID int64, filter *domain.MessageReaction) []domain.ChannelMessagePeerReaction {
	byMessage := s.reactions[channelID]
	if byMessage == nil {
		return nil
	}
	byUser := byMessage[messageID]
	if byUser == nil {
		return nil
	}
	rows := make([]domain.ChannelMessagePeerReaction, 0, len(byUser))
	for _, userRows := range byUser {
		for _, row := range userRows {
			if filter != nil && row.Reaction.Key() != filter.Key() {
				continue
			}
			row.My = row.UserID == viewerUserID
			rows = append(rows, row)
		}
	}
	return rows
}

func memoryReactionOffset(row domain.ChannelMessagePeerReaction) string {
	return strconv.Itoa(row.Date) + ":" + strconv.FormatInt(row.UserID, 10) + ":" + string(row.Reaction.Type) + ":" + row.Reaction.Value()
}

func messageReactionKey(reaction domain.MessageReaction) string {
	return reaction.Key()
}

func parseMemoryReactionOffset(offset string) (memoryReactionCursor, bool) {
	parts := strings.SplitN(offset, ":", 4)
	if len(parts) != 3 && len(parts) != 4 {
		return memoryReactionCursor{}, false
	}
	date, err := strconv.Atoi(parts[0])
	if err != nil || date < 0 {
		return memoryReactionCursor{}, false
	}
	userID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || userID < 0 {
		return memoryReactionCursor{}, false
	}
	if len(parts) == 3 {
		return memoryReactionCursor{date: date, userID: userID, value: parts[2], legacyValue: true}, true
	}
	return memoryReactionCursor{date: date, userID: userID, reactionType: domain.MessageReactionType(parts[2]), value: parts[3]}, true
}

func memoryReactionAfterOffset(row domain.ChannelMessagePeerReaction, cursor memoryReactionCursor) bool {
	if row.Date != cursor.date {
		return row.Date < cursor.date
	}
	if row.UserID != cursor.userID {
		return row.UserID < cursor.userID
	}
	if cursor.legacyValue {
		return row.Reaction.Value() > cursor.value
	}
	if row.Reaction.Type != cursor.reactionType {
		return row.Reaction.Type > cursor.reactionType
	}
	return row.Reaction.Value() > cursor.value
}

func cloneChannelMessageReactionsPtr(in *domain.ChannelMessageReactions) *domain.ChannelMessageReactions {
	if in == nil {
		return nil
	}
	out := cloneChannelMessageReactions(*in)
	return &out
}

func cloneChannelMessageReactions(in domain.ChannelMessageReactions) domain.ChannelMessageReactions {
	in.Results = append([]domain.ChannelMessageReactionCount(nil), in.Results...)
	in.Recent = cloneChannelPeerReactions(in.Recent)
	if in.Paid != nil {
		paid := *in.Paid
		paid.TopReactors = append([]domain.PaidReactor(nil), in.Paid.TopReactors...)
		in.Paid = &paid
	}
	return in
}

func cloneChannelPeerReactions(in []domain.ChannelMessagePeerReaction) []domain.ChannelMessagePeerReaction {
	if len(in) == 0 {
		return nil
	}
	return append([]domain.ChannelMessagePeerReaction(nil), in...)
}
