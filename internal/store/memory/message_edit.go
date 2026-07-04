package memory

import (
	"context"
	"sort"
	"telesrv/internal/domain"
	"time"
)

func (s *MessageStore) EditMessage(_ context.Context, req domain.EditMessageRequest) (domain.EditMessageResult, error) {
	res := domain.EditMessageResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 || req.Peer.ID == 0 || req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return res, domain.ErrMessageIDInvalid
	}
	if req.EditDate == 0 {
		req.EditDate = int(time.Now().Unix())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	targetIndex := -1
	var target domain.Message
	for i, msg := range s.m[req.OwnerUserID] {
		if msg.ID == req.ID && msg.Peer == req.Peer {
			targetIndex = i
			target = msg
			break
		}
	}
	if targetIndex < 0 {
		return res, domain.ErrMessageIDInvalid
	}
	authorEdit := target.Out && target.From.ID == req.OwnerUserID
	viaBotEdit := req.ViaBotEditBotID != 0 && target.ViaBotID == req.ViaBotEditBotID
	if !authorEdit && !viaBotEdit && !req.WebPageResolve && !validMemoryTodoParticipantEdit(req, target) {
		return res, domain.ErrMessageAuthorRequired
	}
	// WebPageResolve：服务端内部链接预览就地替换。幂等——仅当目标当前 media 仍是匹配 id 的
	// pending 占位才替换；只换 media、不碰 body/entities/edit_date，事件为 web_page。
	if req.WebPageResolve {
		if req.Media == nil || !domain.IsPendingWebPageMedia(target.Media, req.ExpectedWebPageID) {
			return res, domain.ErrMessageNotModified
		}
	}
	finalMedia := target.Media
	if req.Media != nil {
		finalMedia = req.Media
	}
	finalRich := target.RichMessage
	if req.SetRichMessage {
		finalRich = req.RichMessage
	}
	if req.Message == "" && finalMedia.IsZero() && finalRich.IsZero() {
		return res, domain.ErrMessageEmpty
	}
	richChanged := req.SetRichMessage && !richMessagesEqual(target.RichMessage, req.RichMessage)
	if req.Media == nil && !req.SetReplyMarkup && !richChanged && target.Body == req.Message && target.HideEdited == req.HideEdited && equalMessageEntities(target.Entities, req.Entities) {
		return res, domain.ErrMessageNotModified
	}
	messageSenderID := target.From.ID
	for userID, messages := range s.m {
		for i, msg := range messages {
			if msg.UID == target.UID && msg.From.ID == messageSenderID {
				if req.WebPageResolve {
					media := *req.Media
					msg.Media = &media
					msg.Pts = s.nextPtsLocked(userID)
					s.m[userID][i] = msg
					res.Edited = append(res.Edited, domain.EditedMessageForUser{
						UserID:  userID,
						Message: cloneMessage(msg),
						Event:   webPageEvent(msg),
					})
					continue
				}
				msg.Body = req.Message
				msg.Entities = append([]domain.MessageEntity(nil), req.Entities...)
				if req.Media != nil {
					media := *req.Media
					msg.Media = &media
				}
				if req.SetReplyMarkup {
					// 替换 markup（nil/空 = 清空键盘）；双盒一致。
					msg.ReplyMarkup = cloneReplyMarkup(req.ReplyMarkup)
				}
				if req.SetRichMessage {
					msg.RichMessage = cloneRichMessage(req.RichMessage)
				}
				msg.EditDate = req.EditDate
				msg.HideEdited = req.HideEdited
				msg.Pts = s.nextPtsLocked(userID)
				s.m[userID][i] = msg
				event := editMessageEvent(msg)
				res.Edited = append(res.Edited, domain.EditedMessageForUser{
					UserID:  userID,
					Message: cloneMessage(msg),
					Event:   event,
				})
			}
		}
	}
	if s.dialogs != nil {
		s.dialogs.mu.Lock()
		for userID := range s.dialogs.m {
			list := s.dialogs.m[userID]
			list.Messages = cloneMessages(s.m[userID])
			s.dialogs.m[userID] = list
		}
		s.dialogs.mu.Unlock()
	}
	sort.Slice(res.Edited, func(i, j int) bool { return res.Edited[i].UserID < res.Edited[j].UserID })
	return res, nil
}

func validMemoryTodoParticipantEdit(req domain.EditMessageRequest, target domain.Message) bool {
	if !req.AllowTodoParticipantMutation || req.SetReplyMarkup || req.Media == nil || req.Media.Kind != domain.MessageMediaKindTodo || req.Media.Todo == nil {
		return false
	}
	if target.From.ID == req.OwnerUserID {
		return false
	}
	if target.Body != req.Message || !equalMessageEntities(target.Entities, req.Entities) {
		return false
	}
	if target.Media == nil || target.Media.Kind != domain.MessageMediaKindTodo || target.Media.Todo == nil {
		return false
	}
	return true
}
