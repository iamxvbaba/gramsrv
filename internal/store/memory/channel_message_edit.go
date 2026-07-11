package memory

import (
	"context"
	"sort"
	"strings"

	"telesrv/internal/domain"
)

func (s *ChannelStore) EditChannelMessage(_ context.Context, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.ID <= 0 {
		return domain.EditChannelMessageResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditChannelMessageResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	idx, ok := s.findMessageIndexLocked(req.ChannelID, req.ID)
	if !ok || s.messages[req.ChannelID][idx].Deleted || s.messages[req.ChannelID][idx].Action != nil {
		return domain.EditChannelMessageResult{}, domain.ErrMessageIDInvalid
	}
	prevMsg := s.messages[req.ChannelID][idx]
	msg := prevMsg
	finalMedia := msg.Media
	if req.Media != nil {
		finalMedia = req.Media
	}
	finalRich := msg.RichMessage
	if req.SetRichMessage {
		finalRich = req.RichMessage
	}
	if strings.TrimSpace(req.Message) == "" && finalMedia.IsZero() && finalRich.IsZero() {
		return domain.EditChannelMessageResult{}, domain.ErrChannelInvalid
	}
	// WebPageResolve：频道链接预览就地替换（服务端内部，幂等守卫即授权）。只换 media、
	// 不碰 body/entities/edit_date，事件为 channel_web_page。
	if req.WebPageResolve {
		if req.Media == nil || !domain.IsPendingWebPageMedia(msg.Media, req.ExpectedWebPageID) {
			return domain.EditChannelMessageResult{}, domain.ErrMessageNotModified
		}
		pts := s.nextChannelPtsLocked(req.ChannelID)
		media := *req.Media
		msg.Media = &media
		msg.Pts = pts
		s.messages[req.ChannelID][idx] = msg
		channel.Pts = pts
		s.channels[req.ChannelID] = channel
		event := domain.ChannelUpdateEvent{
			ChannelID:    req.ChannelID,
			Type:         domain.ChannelUpdateWebPage,
			Pts:          pts,
			PtsCount:     1,
			Date:         msg.Date,
			Message:      cloneChannelMessage(msg),
			SenderUserID: req.UserID,
		}
		s.appendChannelEventLocked(event)
		return domain.EditChannelMessageResult{
			Channel:    channel,
			Message:    cloneChannelMessage(msg),
			Event:      cloneChannelEvent(event),
			Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
		}, nil
	}
	// participant todo 协作须满足与正常发消息相同的权限，禁言成员/无发帖权订阅者
	// 不得借 OthersCanComplete/Append 绕过发言限制（与 postgres 对齐）。
	participantTodoEdit := isChannelTodoParticipantEdit(req, msg) && canSendChannelMessage(channel, member)
	viaBotEditRequested := req.ViaBotEditBotID != 0
	viaBotEdit := viaBotEditRequested && msg.ViaBotID == req.ViaBotEditBotID
	if viaBotEditRequested && !viaBotEdit {
		return domain.EditChannelMessageResult{}, domain.ErrMessageAuthorRequired
	}
	if !viaBotEdit && msg.SenderUserID != req.UserID && !canEditChannelMessage(member) && !participantTodoEdit {
		return domain.EditChannelMessageResult{}, domain.ErrMessageAuthorRequired
	}
	richChanged := req.SetRichMessage && !richMessagesEqual(msg.RichMessage, req.RichMessage)
	if req.Media == nil && !req.SetReplyMarkup && !richChanged && msg.Body == req.Message && sameMessageEntities(msg.Entities, req.Entities) {
		return domain.EditChannelMessageResult{}, domain.ErrMessageNotModified
	}
	pts := s.nextChannelPtsLocked(req.ChannelID)
	msg.Body = req.Message
	msg.Entities = append([]domain.MessageEntity(nil), req.Entities...)
	if req.Media != nil {
		media := *req.Media
		msg.Media = &media
	}
	if req.SetReplyMarkup {
		msg.ReplyMarkup = cloneReplyMarkup(req.ReplyMarkup)
	}
	if req.SetRichMessage {
		msg.RichMessage = cloneRichMessage(req.RichMessage)
	}
	msg.EditDate = req.EditDate
	msg.Pts = pts
	s.messages[req.ChannelID][idx] = msg
	channel.Pts = pts
	s.channels[req.ChannelID] = channel
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdateEditMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.EditDate,
		Message:      cloneChannelMessage(msg),
		SenderUserID: req.UserID,
	}
	s.appendChannelEventLocked(event)
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID:   req.ChannelID,
		UserID:      req.UserID,
		Date:        req.EditDate,
		Type:        domain.ChannelAdminLogEditMessage,
		PrevMessage: ptrChannelMessage(prevMsg),
		NewMessage:  ptrChannelMessage(msg),
		Query:       msg.Body,
	})
	var serviceMsg domain.ChannelMessage
	var serviceEvent domain.ChannelUpdateEvent
	if req.TodoServiceAction != nil {
		action := cloneChannelMessageAction(req.TodoServiceAction)
		if action == nil {
			return domain.EditChannelMessageResult{}, domain.ErrChannelInvalid
		}
		replyTo := channelTodoServiceReply(msg)
		servicePts := s.nextChannelPtsLocked(req.ChannelID)
		serviceMsg = domain.ChannelMessage{
			ChannelID:    req.ChannelID,
			ID:           s.nextChannelMessageIDLocked(req.ChannelID),
			SenderUserID: req.UserID,
			From:         domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID},
			Date:         req.EditDate,
			Post:         channel.Broadcast,
			Silent:       msg.Silent,
			NoForwards:   msg.NoForwards || channel.NoForwards,
			ReplyTo:      replyTo,
			Action:       action,
			Pts:          servicePts,
		}
		serviceEvent = domain.ChannelUpdateEvent{
			ChannelID:    req.ChannelID,
			Type:         domain.ChannelUpdateNewMessage,
			Pts:          servicePts,
			PtsCount:     1,
			Date:         req.EditDate,
			Message:      cloneChannelMessage(serviceMsg),
			SenderUserID: req.UserID,
		}
		s.messages[req.ChannelID] = append(s.messages[req.ChannelID], serviceMsg)
		s.appendChannelEventLocked(serviceEvent)
		s.updateForumTopicTopMessageLocked(req.ChannelID, serviceMsg)
		channel.TopMessageID = serviceMsg.ID
		channel.Pts = servicePts
		s.channels[req.ChannelID] = channel
		for userID, member := range s.members[req.ChannelID] {
			if member.Status == domain.ChannelMemberActive {
				s.upsertChannelDialogLocked(userID, channel, serviceMsg, userID == req.UserID)
			}
		}
	}
	// 编辑对账 @ 集合：新增者补提及，被移除的实体提及删除；reply 隐式提及保留。
	// 仅在 body/entities 实际变化时执行：media-only 编辑（geolive/todo/poll）不带
	// MentionUserIDs，无条件对账会误删 caption 里仍未读的 @ 提及（与 postgres 对齐）。
	textChanged := prevMsg.Body != req.Message || !sameMessageEntities(prevMsg.Entities, req.Entities)
	if textChanged && (!channel.Broadcast || channel.Megagroup) {
		keep := make(map[int64]struct{}, len(req.MentionUserIDs)+1)
		for _, id := range req.MentionUserIDs {
			if id != 0 {
				keep[id] = struct{}{}
			}
		}
		if msg.ReplyTo != nil && msg.ReplyTo.MessageID > 0 {
			if target, ok := s.findMessageLocked(req.ChannelID, msg.ReplyTo.MessageID); ok && target.SenderUserID != 0 {
				keep[target.SenderUserID] = struct{}{}
			}
		}
		added := make([]int64, 0, len(req.MentionUserIDs))
		for _, id := range req.MentionUserIDs {
			if id == 0 {
				continue
			}
			if _, ok := s.mentions[id][req.ChannelID][msg.ID]; !ok {
				added = append(added, id)
			}
		}
		for userID, byChannel := range s.mentions {
			if _, ok := byChannel[req.ChannelID][msg.ID]; !ok {
				continue
			}
			if _, ok := keep[userID]; ok {
				continue
			}
			delete(byChannel[req.ChannelID], msg.ID)
			if dialogs := s.dialogs[userID]; dialogs != nil {
				if dialog, ok := dialogs[req.ChannelID]; ok {
					dialog.UnreadMentions = s.countChannelUnreadMentionsLocked(userID, req.ChannelID, 0)
					dialogs[req.ChannelID] = dialog
				}
			}
		}
		if len(added) > 0 {
			s.addChannelUnreadMentionsLocked(req.ChannelID, msg, req.UserID, added)
			for _, id := range added {
				if dialogs := s.dialogs[id]; dialogs != nil {
					if dialog, ok := dialogs[req.ChannelID]; ok {
						dialog.UnreadMentions = s.countChannelUnreadMentionsLocked(id, req.ChannelID, 0)
						dialogs[req.ChannelID] = dialog
					}
				}
			}
		}
	}
	return domain.EditChannelMessageResult{
		Channel:        channel,
		Message:        cloneChannelMessage(msg),
		Event:          cloneChannelEvent(event),
		ServiceMessage: cloneChannelMessage(serviceMsg),
		ServiceEvent:   cloneChannelEvent(serviceEvent),
		Recipients:     s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

func isChannelTodoParticipantEdit(req domain.EditChannelMessageRequest, msg domain.ChannelMessage) bool {
	if !req.AllowTodoParticipantMutation || req.SetReplyMarkup || req.Media == nil || req.Media.Kind != domain.MessageMediaKindTodo || req.Media.Todo == nil {
		return false
	}
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindTodo || msg.Media.Todo == nil {
		return false
	}
	if req.TodoServiceAction == nil {
		return false
	}
	switch req.TodoServiceAction.Type {
	case domain.ChannelActionTodoCompletions:
		if !msg.Media.Todo.OthersCanComplete {
			return false
		}
	case domain.ChannelActionTodoAppendTasks:
		if !msg.Media.Todo.OthersCanAppend {
			return false
		}
	default:
		return false
	}
	return msg.Body == req.Message && sameMessageEntities(msg.Entities, req.Entities)
}

func channelTodoServiceReply(msg domain.ChannelMessage) *domain.MessageReply {
	reply := &domain.MessageReply{
		Peer:      domain.Peer{Type: domain.PeerTypeChannel, ID: msg.ChannelID},
		MessageID: msg.ID,
	}
	if msg.ReplyTo != nil {
		reply.TopMessageID = msg.ReplyTo.TopMessageID
		reply.ForumTopic = msg.ReplyTo.ForumTopic
	}
	return reply
}

func (s *ChannelStore) UpdatePinnedMessage(_ context.Context, req domain.UpdateChannelPinnedMessageRequest) (domain.UpdateChannelPinnedMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canPinChannelMessages(channel, member) {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelAdminRequired
	}
	msg, ok := s.findMessageLocked(req.ChannelID, req.MessageID)
	if !ok || msg.Deleted {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrMessageIDInvalid
	}
	// 多置顶模型：pin/unpin 只翻转目标消息自身的 pinned flag，不影响其它
	// 置顶；与官方一致不设数量上限。
	if msg.Pinned == req.Pinned {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelNotModified
	}
	for i := range s.messages[req.ChannelID] {
		if s.messages[req.ChannelID][i].ID == req.MessageID {
			s.messages[req.ChannelID][i].Pinned = req.Pinned
			break
		}
	}
	pts := s.nextChannelPtsLocked(req.ChannelID)
	channel.PinnedMessageID = s.latestPinnedMessageIDLocked(req.ChannelID)
	channel.Pts = pts
	s.channels[req.ChannelID] = channel
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdatePinnedMessages,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		MessageIDs:   []int{req.MessageID},
		SenderUserID: req.UserID,
		Pinned:       req.Pinned,
	}
	s.appendChannelEventLocked(event)
	logMsg := msg
	logMsg.Pinned = req.Pinned
	s.appendChannelAdminLogLocked(domain.ChannelAdminLogEvent{
		ChannelID: req.ChannelID,
		UserID:    req.UserID,
		Date:      req.Date,
		Type:      domain.ChannelAdminLogUpdatePinned,
		Message:   ptrChannelMessage(logMsg),
		Query:     msg.Body,
	})
	return domain.UpdateChannelPinnedMessageResult{
		Channel:    channel,
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

// latestPinnedMessageIDLocked 返回最新置顶消息 id（channelFull.pinned_msg_id 缓存语义）。
func (s *ChannelStore) latestPinnedMessageIDLocked(channelID int64) int {
	latest := 0
	for _, m := range s.messages[channelID] {
		if m.Pinned && !m.Deleted && m.ID > latest {
			latest = m.ID
		}
	}
	return latest
}

// UnpinAllChannelMessages 清空全部置顶并以单条 channel 事件携带全部 id。
func (s *ChannelStore) UnpinAllChannelMessages(_ context.Context, req domain.UnpinAllChannelMessagesRequest) (domain.UpdateChannelPinnedMessageResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.UpdateChannelPinnedMessageResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !canPinChannelMessages(channel, member) {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelAdminRequired
	}
	cleared := []int(nil)
	for i := range s.messages[req.ChannelID] {
		if s.messages[req.ChannelID][i].Pinned && !s.messages[req.ChannelID][i].Deleted {
			s.messages[req.ChannelID][i].Pinned = false
			cleared = append(cleared, s.messages[req.ChannelID][i].ID)
		}
	}
	if len(cleared) == 0 {
		return domain.UpdateChannelPinnedMessageResult{}, domain.ErrChannelNotModified
	}
	sort.Ints(cleared)
	pts := s.nextChannelPtsLocked(req.ChannelID)
	channel.PinnedMessageID = 0
	channel.Pts = pts
	s.channels[req.ChannelID] = channel
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdatePinnedMessages,
		Pts:          pts,
		PtsCount:     1,
		Date:         req.Date,
		MessageIDs:   cleared,
		SenderUserID: req.UserID,
		Pinned:       false,
	}
	s.appendChannelEventLocked(event)
	return domain.UpdateChannelPinnedMessageResult{
		Channel:    channel,
		Event:      cloneChannelEvent(event),
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}

// ClearDanglingPinnedMessage 把指向已删除消息的置顶值清零（unpinAll 自愈）。
func (s *ChannelStore) ClearDanglingPinnedMessage(_ context.Context, channelID int64, messageID int) error {
	if channelID == 0 || messageID <= 0 {
		return domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, ok := s.channels[channelID]
	if !ok {
		return domain.ErrChannelInvalid
	}
	if channel.PinnedMessageID == messageID {
		channel.PinnedMessageID = 0
		s.channels[channelID] = channel
	}
	return nil
}

func canPinChannelMessages(channel domain.Channel, member domain.ChannelMember) bool {
	if member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.PinMessages) {
		return true
	}
	return channel.Megagroup && !channel.DefaultBannedRights.PinMessages && !member.BannedRights.PinMessages
}

func canEditChannelMessage(member domain.ChannelMember) bool {
	return member.Role == domain.ChannelRoleCreator || (member.Role == domain.ChannelRoleAdmin && member.AdminRights.EditMessages)
}
