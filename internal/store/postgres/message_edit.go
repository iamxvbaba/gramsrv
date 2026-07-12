package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

func (s *MessageStore) EditMessage(ctx context.Context, req domain.EditMessageRequest) (res domain.EditMessageResult, err error) {
	res = domain.EditMessageResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 {
		return res, fmt.Errorf("edit message: missing owner user id")
	}
	if req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 || req.ID <= 0 || req.ID > domain.MaxMessageBoxID {
		return res, domain.ErrMessageIDInvalid
	}

	if req.EditDate == 0 {
		req.EditDate = int(time.Now().Unix())
	}
	entities, err := encodeMessageEntities(req.Entities)
	if err != nil {
		return res, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return res, fmt.Errorf("edit message: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin edit message tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	// advisory lock 串行化与会话对端的并发写，须在行锁前获取，消除 AB-BA 死锁。
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID, req.Peer.ID); err != nil {
		return res, fmt.Errorf("lock edit message users: %w", err)
	}

	target, err := qtx.GetMessageBoxForEdit(ctx, sqlcgen.GetMessageBoxForEditParams{
		OwnerUserID: req.OwnerUserID,
		BoxID:       int32(req.ID),
		PeerType:    string(req.Peer.Type),
		PeerID:      req.Peer.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, domain.ErrMessageIDInvalid
		}
		return res, fmt.Errorf("get message for edit: %w", err)
	}
	targetRich, err := decodeRichMessage(target.RichMessageJson)
	if err != nil {
		return res, fmt.Errorf("decode target rich message: %w", err)
	}
	// 空文本只在目标消息携带媒体/rich（或本次写入媒体/rich）时合法（清空 caption）。
	if req.Message == "" && req.Media == nil && (target.MediaJson == "" || target.MediaJson == "{}") {
		if req.SetRichMessage {
			if req.RichMessage.IsZero() {
				return res, domain.ErrMessageEmpty
			}
		} else if targetRich.IsZero() {
			return res, domain.ErrMessageEmpty
		}
	}
	oldEntities, err := decodeMessageEntities(target.EntitiesJson)
	if err != nil {
		return res, fmt.Errorf("decode target entities: %w", err)
	}
	authorEdit := target.Outgoing && target.MessageSenderID == req.OwnerUserID && target.FromUserID == req.OwnerUserID
	viaBotEdit := req.ViaBotEditBotID != 0 && target.ViaBotID == req.ViaBotEditBotID
	if !authorEdit && !viaBotEdit && !req.WebPageResolve && !validTodoParticipantEdit(req, target, oldEntities) {
		return res, domain.ErrMessageAuthorRequired
	}
	richChanged := req.SetRichMessage && !richMessagesEqual(targetRich, req.RichMessage)
	if req.Media == nil && !req.SetReplyMarkup && !richChanged && target.Body == req.Message && target.HideEdited == req.HideEdited && sameMessageEntities(oldEntities, req.Entities) {
		return res, domain.ErrMessageNotModified
	}
	replyMarkupJSON, err := encodeReplyMarkup(req.ReplyMarkup)
	if err != nil {
		return res, fmt.Errorf("encode edit reply markup: %w", err)
	}
	richMessageJSON, err := encodeRichMessage(req.RichMessage)
	if err != nil {
		return res, fmt.Errorf("encode edit rich message: %w", err)
	}
	messageSenderID := target.MessageSenderID
	boxes, err := qtx.ListVisibleMessageBoxesByPrivateMessage(ctx, sqlcgen.ListVisibleMessageBoxesByPrivateMessageParams{
		OwnerUserIds:     privateMessageOwnerIDs(req.OwnerUserID, req.Peer.ID),
		MessageSenderID:  messageSenderID,
		PrivateMessageID: target.PrivateMessageID,
	})
	if err != nil {
		return res, fmt.Errorf("list visible edit boxes: %w", err)
	}
	if len(boxes) == 0 {
		return res, domain.ErrMessageIDInvalid
	}
	if req.WebPageResolve {
		// 链接预览就地替换：仅换 media（不碰 body/entities/edit_date，故不标记「已编辑」），
		// 逐 box reserve 账号 pts + 追加 web_page 事件 + dispatch。幂等守卫见下。
		targetMedia, err := decodeMessageMedia(target.MediaJson)
		if err != nil {
			return res, fmt.Errorf("decode target media for web page resolve: %w", err)
		}
		if !domain.IsPendingWebPageMedia(targetMedia, req.ExpectedWebPageID) {
			return res, domain.ErrMessageNotModified
		}
		mediaJSON, err := encodeMessageMedia(req.Media)
		if err != nil {
			return res, fmt.Errorf("encode resolved web page media: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media = $3
WHERE sender_user_id = $1 AND id = $2`, messageSenderID, target.PrivateMessageID, mediaJSON); err != nil {
			return res, fmt.Errorf("update private message web page media: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE message_boxes SET media = $3
WHERE message_sender_id = $1 AND private_message_id = $2`, messageSenderID, target.PrivateMessageID, mediaJSON); err != nil {
			return res, fmt.Errorf("update message box web page media: %w", err)
		}
		res.Edited = make([]domain.EditedMessageForUser, 0, len(boxes))
		for _, box := range boxes {
			pts, err := s.reservePts(ctx, tx, box.OwnerUserID)
			if err != nil {
				return res, fmt.Errorf("allocate web page resolve pts: %w", err)
			}
			if _, err := tx.Exec(ctx, `
UPDATE message_boxes SET pts = $3
WHERE owner_user_id = $1 AND box_id = $2`, box.OwnerUserID, box.BoxID, int32(pts)); err != nil {
				return res, fmt.Errorf("bump message box pts for web page: %w", err)
			}
			msg, err := messageFromVisibleBoxRow(box)
			if err != nil {
				return res, err
			}
			// box 行取自 media 替换前：用解析结果与新 pts 覆盖（链接预览不改文本/实体，
			// 故共享媒体索引无需重建）。
			msg.Media = req.Media
			msg.Pts = pts
			event := domain.UpdateEvent{
				UserID:   msg.OwnerUserID,
				Type:     domain.UpdateEventWebPage,
				Pts:      pts,
				PtsCount: 1,
				Date:     msg.Date,
				Message:  msg,
			}
			if err := appendUserUpdateEvent(ctx, tx, qtx, msg.OwnerUserID, event); err != nil {
				return res, fmt.Errorf("append web page event: %w", err)
			}
			if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
				TargetUserID:     msg.OwnerUserID,
				Pts:              int32(pts),
				EventType:        string(domain.UpdateEventWebPage),
				ExcludeAuthKeyID: authKeyIDToInt64(req.OriginAuthKeyID),
				ExcludeSessionID: req.OriginSessionID,
			}); err != nil {
				return res, fmt.Errorf("enqueue web page dispatch: %w", err)
			}
			res.Edited = append(res.Edited, domain.EditedMessageForUser{UserID: msg.OwnerUserID, Message: msg, Event: event})
		}
		if err := tx.Commit(ctx); err != nil {
			return res, fmt.Errorf("commit web page resolve tx: %w", err)
		}
		committed = true
		return res, nil
	}
	if err := qtx.UpdatePrivateMessageEdit(ctx, sqlcgen.UpdatePrivateMessageEditParams{
		SenderUserID:     messageSenderID,
		PrivateMessageID: target.PrivateMessageID,
		Body:             req.Message,
		EntitiesJson:     entities,
		EditDate:         int32(req.EditDate),
		HideEdited:       req.HideEdited,
		SetReplyMarkup:   req.SetReplyMarkup,
		ReplyMarkupJson:  replyMarkupJSON,
		SetRichMessage:   req.SetRichMessage,
		RichMessageJson:  richMessageJSON,
	}); err != nil {
		return res, fmt.Errorf("update private message edit: %w", err)
	}
	if req.Media != nil {
		// 媒体快照整体替换（live location 续报/停止）；先于 box 行编辑执行，
		// 让 UpdateMessageBoxEdit RETURNING 直接带回新媒体。
		mediaJSON, err := encodeMessageMedia(req.Media)
		if err != nil {
			return res, fmt.Errorf("encode edit message media: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE private_messages SET media = $3
WHERE sender_user_id = $1 AND id = $2`, messageSenderID, target.PrivateMessageID, mediaJSON); err != nil {
			return res, fmt.Errorf("update private message media: %w", err)
		}
		if _, err := tx.Exec(ctx, `
UPDATE message_boxes SET media = $3
WHERE message_sender_id = $1 AND private_message_id = $2`, messageSenderID, target.PrivateMessageID, mediaJSON); err != nil {
			return res, fmt.Errorf("update message box media: %w", err)
		}
	}
	res.Edited = make([]domain.EditedMessageForUser, 0, len(boxes))
	for _, box := range boxes {
		pts, err := s.reservePts(ctx, tx, box.OwnerUserID)
		if err != nil {
			return res, fmt.Errorf("allocate edit message pts: %w", err)
		}
		updated, err := qtx.UpdateMessageBoxEdit(ctx, sqlcgen.UpdateMessageBoxEditParams{
			OwnerUserID:     box.OwnerUserID,
			BoxID:           box.BoxID,
			Body:            req.Message,
			EntitiesJson:    entities,
			EditDate:        int32(req.EditDate),
			HideEdited:      req.HideEdited,
			Pts:             int32(pts),
			SetReplyMarkup:  req.SetReplyMarkup,
			ReplyMarkupJson: replyMarkupJSON,
			SetRichMessage:  req.SetRichMessage,
			RichMessageJson: richMessageJSON,
		})
		if err != nil {
			return res, fmt.Errorf("update message box edit: %w", err)
		}
		msg, err := messageFromUpdateEditRow(updated)
		if err != nil {
			return res, err
		}
		// 共享媒体索引(0118):编辑可换媒体、也可改文本链接实体 → 按编辑后有效媒体+实体逐 box 重建。
		if err := replaceMessageBoxMediaIndexTx(ctx, tx, msg.OwnerUserID, msg.Peer.ID, msg.ID, msg.Date, msg.Media, msg.Entities); err != nil {
			return res, err
		}
		event := domain.UpdateEvent{
			UserID:   msg.OwnerUserID,
			Type:     domain.UpdateEventEditMessage,
			Pts:      msg.Pts,
			PtsCount: 1,
			Date:     req.EditDate,
			Message:  msg,
		}
		if err := appendUserUpdateEvent(ctx, tx, qtx, msg.OwnerUserID, event); err != nil {
			return res, fmt.Errorf("append edit message event: %w", err)
		}
		dispatchAuthKeyID := [8]byte{}
		dispatchSessionID := int64(0)
		if msg.OwnerUserID == req.OwnerUserID {
			dispatchAuthKeyID = req.OriginAuthKeyID
			dispatchSessionID = req.OriginSessionID
		}
		if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
			TargetUserID:     msg.OwnerUserID,
			Pts:              int32(pts),
			EventType:        string(domain.UpdateEventEditMessage),
			ExcludeAuthKeyID: authKeyIDToInt64(dispatchAuthKeyID),
			ExcludeSessionID: dispatchSessionID,
		}); err != nil {
			return res, fmt.Errorf("enqueue edit message dispatch: %w", err)
		}
		res.Edited = append(res.Edited, domain.EditedMessageForUser{UserID: msg.OwnerUserID, Message: msg, Event: event})
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit edit message tx: %w", err)
	}
	committed = true
	return res, nil
}

func validTodoParticipantEdit(req domain.EditMessageRequest, target sqlcgen.GetMessageBoxForEditRow, oldEntities []domain.MessageEntity) bool {
	if !req.AllowTodoParticipantMutation || req.SetReplyMarkup || req.Media == nil || req.Media.Kind != domain.MessageMediaKindTodo || req.Media.Todo == nil {
		return false
	}
	if target.MessageSenderID == req.OwnerUserID {
		return false
	}
	if target.Body != req.Message || !sameMessageEntities(oldEntities, req.Entities) {
		return false
	}
	targetMedia, err := decodeMessageMedia(target.MediaJson)
	if err != nil || targetMedia == nil || targetMedia.Kind != domain.MessageMediaKindTodo || targetMedia.Todo == nil {
		return false
	}
	return true
}
