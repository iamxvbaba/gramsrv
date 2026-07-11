package memory

import (
	"context"
	"telesrv/internal/domain"
)

func (s *ChannelStore) DeleteChannelMessages(_ context.Context, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || len(req.IDs) == 0 {
		return domain.DeleteChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	if len(req.IDs) > domain.MaxDeleteMessageIDs {
		return domain.DeleteChannelMessagesResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	refs := make(map[int]domain.ChannelDiscussionRef, len(req.IDs))
	for _, id := range req.IDs {
		if msg, ok := s.findMessageLocked(req.ChannelID, id); ok && !msg.Deleted && msg.Discussion != nil {
			refs[id] = *msg.Discussion
		}
	}
	deleted, event, channel, err := s.deleteChannelMessagesLocked(channel, member, req.IDs, req.UserID, req.Date)
	if err != nil {
		return domain.DeleteChannelMessagesResult{}, err
	}
	// 被删 broadcast post 的讨论组转发根级联删除（服务端动作，creator 权限）。
	var cascades []domain.ChannelCascadeDelete
	byChannel := make(map[int64][]int)
	for _, id := range deleted {
		if ref, ok := refs[id]; ok && ref.ChannelID != 0 && ref.MessageID != 0 {
			byChannel[ref.ChannelID] = append(byChannel[ref.ChannelID], ref.MessageID)
		}
	}
	for groupID, rootIDs := range byChannel {
		group, ok := s.channels[groupID]
		if !ok || group.Deleted {
			continue
		}
		systemMember := domain.ChannelMember{ChannelID: groupID, UserID: req.UserID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}
		groupDeleted, groupEvent, group, err := s.deleteChannelMessagesLocked(group, systemMember, rootIDs, req.UserID, req.Date)
		if err != nil || len(groupDeleted) == 0 {
			continue
		}
		cascades = append(cascades, domain.ChannelCascadeDelete{
			Channel:    group,
			Event:      cloneChannelEvent(groupEvent),
			Recipients: s.activeMemberIDsLocked(groupID, 0, 0),
		})
	}
	return domain.DeleteChannelMessagesResult{
		Channel:           channel,
		Event:             cloneChannelEvent(event),
		DeletedIDs:        append([]int(nil), deleted...),
		Recipients:        s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		DiscussionDeletes: cascades,
	}, nil
}

func (s *ChannelStore) DeleteChannelHistory(_ context.Context, req domain.DeleteChannelHistoryRequest) (domain.DeleteChannelHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	maxID := req.MaxID
	if maxID <= 0 || maxID > channel.TopMessageID {
		maxID = channel.TopMessageID
	}
	member := s.members[req.ChannelID][req.UserID]
	if !req.ForEveryone {
		appliedMinID := maxInt(member.AvailableMinID, maxID)
		member.AvailableMinID = appliedMinID
		member.ReadInboxMaxID = maxInt(member.ReadInboxMaxID, appliedMinID)
		member.UnreadMark = false
		s.members[req.ChannelID][req.UserID] = member
		s.deleteChannelUnreadMentionsUpToLocked(req.UserID, req.ChannelID, appliedMinID)
		if dialog, ok := s.dialogs[req.UserID][req.ChannelID]; ok {
			dialog.UnreadReactions = s.countChannelUnreadReactionsLocked(req.UserID, req.ChannelID, 0)
			s.dialogs[req.UserID][req.ChannelID] = dialog
		}
		if s.dialogs[req.UserID] == nil {
			s.dialogs[req.UserID] = make(map[int64]domain.ChannelDialog)
		}
		s.dialogs[req.UserID][req.ChannelID] = s.dialogForUserLocked(req.UserID, channel)
		return domain.DeleteChannelHistoryResult{Channel: channel, AvailableMinID: appliedMinID}, nil
	}
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelAdminRequired
	}
	// id=1 是建群服务消息，全员清空必须保留：它是清空后会话仅剩的
	// top message，没有它客户端会把 lastMessage 视为空并从聊天列表
	// 隐藏该会话（成员资格仍在，但会话条目对全员消失）。
	ids := make([]int, 0, domain.MaxDeleteHistoryBatch)
	for i := len(s.messages[req.ChannelID]) - 1; i >= 0; i-- {
		msg := s.messages[req.ChannelID][i]
		if msg.Deleted || msg.ID > maxID || msg.ID <= 1 {
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
	if len(deleted) == domain.MaxDeleteHistoryBatch {
		offset = 1
	}
	return domain.DeleteChannelHistoryResult{
		Channel:    channel,
		Event:      cloneChannelEvent(event),
		DeletedIDs: append([]int(nil), deleted...),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		Offset:     offset,
	}, nil
}

func (s *ChannelStore) DeleteChannelParticipantHistory(_ context.Context, req domain.DeleteChannelParticipantHistoryRequest) (domain.DeleteChannelHistoryResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ParticipantUserID == 0 {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.DeleteChannelHistoryResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canDeleteAnyChannelMessage(member) {
		return domain.DeleteChannelHistoryResult{}, domain.ErrChannelAdminRequired
	}
	// 同全员清空：id=1 建群服务消息不随发送者（创建者）历史一起删除，
	// 否则会话会因 lastMessage 为空从全员聊天列表隐藏。
	ids := make([]int, 0, domain.MaxDeleteHistoryBatch)
	for i := len(s.messages[req.ChannelID]) - 1; i >= 0; i-- {
		msg := s.messages[req.ChannelID][i]
		if msg.Deleted || msg.SenderUserID != req.ParticipantUserID || msg.ID <= 1 {
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
	if len(deleted) == domain.MaxDeleteHistoryBatch {
		offset = 1
	}
	return domain.DeleteChannelHistoryResult{
		Channel:    channel,
		Event:      cloneChannelEvent(event),
		DeletedIDs: append([]int(nil), deleted...),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		Offset:     offset,
	}, nil
}

func (s *ChannelStore) deleteChannelMessagesLocked(channel domain.Channel, member domain.ChannelMember, ids []int, actorUserID int64, date int) ([]int, domain.ChannelUpdateEvent, domain.Channel, error) {
	if len(ids) == 0 {
		return nil, domain.ChannelUpdateEvent{}, channel, nil
	}
	seen := make(map[int]struct{}, len(ids))
	deleted := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return nil, domain.ChannelUpdateEvent{}, channel, domain.ErrMessageIDInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		idx, ok := s.findMessageIndexLocked(channel.ID, id)
		if !ok || s.messages[channel.ID][idx].Deleted {
			continue
		}
		msg := s.messages[channel.ID][idx]
		if msg.SenderUserID != actorUserID && !canDeleteAnyChannelMessage(member) {
			return nil, domain.ChannelUpdateEvent{}, channel, domain.ErrChannelAdminRequired
		}
		if id <= 1 {
			// id=1 建群服务消息是清空后会话仅剩的兜底 top message，所有
			// 删除入口统一静默跳过（官方客户端对它禁用删除）。
			continue
		}
		msg.Deleted = true
		s.messages[channel.ID][idx] = msg
		deleted = append(deleted, id)
		s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
			ChannelID: channel.ID,
			UserID:    actorUserID,
			Date:      date,
			Type:      domain.ChannelAdminLogDeleteMessage,
			Message:   ptrChannelMessage(msg),
			Query:     msg.Body,
		})
	}
	if len(deleted) == 0 {
		return nil, domain.ChannelUpdateEvent{}, channel, nil
	}
	s.deleteChannelUnreadMentionsLocked(channel.ID, deleted)
	pts := s.nextChannelPtsNLocked(channel.ID, len(deleted))
	channel.Pts = pts
	channel.TopMessageID = s.topNonDeletedMessageIDLocked(channel.ID)
	// 删除即从置顶集合移除（读路径过滤 NOT deleted），重算最新置顶缓存。
	channel.PinnedMessageID = s.latestPinnedMessageIDLocked(channel.ID)
	s.channels[channel.ID] = channel
	for userID, member := range s.members[channel.ID] {
		if member.Status != domain.ChannelMemberActive {
			continue
		}
		if s.dialogs[userID] == nil {
			s.dialogs[userID] = make(map[int64]domain.ChannelDialog)
		}
		dialog := s.dialogForUserLocked(userID, channel)
		s.dialogs[userID][channel.ID] = dialog
	}
	event := domain.ChannelUpdateEvent{
		ChannelID:    channel.ID,
		Type:         domain.ChannelUpdateDeleteMessages,
		Pts:          pts,
		PtsCount:     len(deleted),
		Date:         date,
		MessageIDs:   append([]int(nil), deleted...),
		SenderUserID: actorUserID,
	}
	s.appendChannelEventLocked(event)
	for _, id := range deleted {
		msg, ok := s.findMessageLocked(channel.ID, id)
		if !ok || msg.RandomID == 0 || msg.SenderUserID == 0 {
			continue
		}
		key := channelMessageReplayKey{channelID: channel.ID, messageID: msg.ID}
		cloned := cloneChannelEvent(event)
		s.deleteReceipts[key] = &cloned
	}
	return deleted, event, channel, nil
}

func (s *ChannelStore) topNonDeletedMessageIDLocked(channelID int64) int {
	for i := len(s.messages[channelID]) - 1; i >= 0; i-- {
		if !s.messages[channelID][i].Deleted {
			return s.messages[channelID][i].ID
		}
	}
	return 0
}

func canDeleteAnyChannelMessage(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.DeleteMessages)
}
