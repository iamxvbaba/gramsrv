package memory

import (
	"context"
	"strings"

	"telesrv/internal/domain"
)

// EditFactCheck 写入或删除一条消息的事实核查标签。
// 权限与频道消息编辑一致：作者本人或具备 edit_messages 权限的管理员。
func (s *ChannelStore) EditFactCheck(_ context.Context, req domain.EditFactCheckRequest) (domain.EditFactCheckResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.MessageID <= 0 {
		return domain.EditFactCheckResult{}, domain.ErrChannelInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.channelForMemberLocked(req.UserID, req.ChannelID)
	if err != nil {
		return domain.EditFactCheckResult{}, err
	}
	member := s.members[req.ChannelID][req.UserID]
	if !channel.Broadcast {
		return domain.EditFactCheckResult{}, domain.ErrChannelInvalid
	}
	idx, ok := s.findMessageIndexLocked(req.ChannelID, req.MessageID)
	if !ok || s.messages[req.ChannelID][idx].Deleted || s.messages[req.ChannelID][idx].Action != nil {
		return domain.EditFactCheckResult{}, domain.ErrMessageIDInvalid
	}
	msg := s.messages[req.ChannelID][idx]
	if msg.SenderUserID != req.UserID && !canEditChannelMessage(member) {
		return domain.EditFactCheckResult{}, domain.ErrFactCheckForbidden
	}
	var check *domain.FactCheck
	if strings.TrimSpace(req.Text) != "" {
		candidate := domain.FactCheck{
			NeedCheck: req.NeedCheck,
			Country:   strings.ToUpper(strings.TrimSpace(req.Country)),
			Text:      req.Text,
			Entities:  req.Entities,
		}
		if err := domain.ValidateFactCheck(candidate); err != nil {
			return domain.EditFactCheckResult{}, err
		}
		candidate.Hash = domain.FactCheckHash(req.ChannelID, req.MessageID, candidate)
		check = &candidate
	}
	date := req.Date
	if domain.FactChecksEqual(msg.FactCheck, check) {
		return domain.EditFactCheckResult{}, domain.ErrMessageNotModified
	}
	if date == 0 {
		date = msg.Date
	}
	pts := s.nextChannelPtsLocked(req.ChannelID)
	if check == nil {
		msg.FactCheck = nil
	} else {
		copied := *check
		copied.Entities = append([]domain.MessageEntity(nil), check.Entities...)
		msg.FactCheck = &copied
	}
	msg.Pts = pts
	s.messages[req.ChannelID][idx] = msg
	channel.Pts = pts
	s.channels[req.ChannelID] = channel
	event := domain.ChannelUpdateEvent{
		ChannelID:    req.ChannelID,
		Type:         domain.ChannelUpdateEditMessage,
		Pts:          pts,
		PtsCount:     1,
		Date:         date,
		Message:      cloneChannelMessage(msg),
		SenderUserID: req.UserID,
	}
	s.appendChannelEventLocked(event)
	return domain.EditFactCheckResult{
		Channel:    channel,
		Message:    cloneChannelMessage(msg),
		Event:      cloneChannelEvent(event),
		FactCheck:  msg.FactCheck,
		Recipients: s.activeMemberIDsLocked(req.ChannelID, 0, 0),
	}, nil
}
