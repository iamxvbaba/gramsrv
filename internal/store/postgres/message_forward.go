package postgres

import (
	"context"
	"fmt"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

func cloneMessageForward(forward *domain.MessageForward) *domain.MessageForward {
	if forward == nil {
		return nil
	}
	clone := *forward
	return &clone
}

func (s *MessageStore) ForwardPrivateMessages(ctx context.Context, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error) {
	res := domain.ForwardPrivateMessagesResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 || req.ToUserID == 0 {
		return res, fmt.Errorf("forward private messages: missing user id")
	}
	if req.FromPeer.Type != domain.PeerTypeUser || req.FromPeer.ID == 0 {
		return res, fmt.Errorf("forward private messages: invalid source peer")
	}
	if len(req.MessageIDs) == 0 || len(req.MessageIDs) != len(req.RandomIDs) {
		return res, domain.ErrMessageIDInvalid
	}
	if len(req.MessageIDs) > domain.MaxForwardMessageIDs {
		return res, fmt.Errorf("forward private messages: too many ids: %d > %d", len(req.MessageIDs), domain.MaxForwardMessageIDs)
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	boxIDs := make([]int32, 0, len(req.MessageIDs))
	for i, id := range req.MessageIDs {
		if id <= 0 || id > domain.MaxMessageBoxID || req.RandomIDs[i] == 0 {
			return res, domain.ErrMessageIDInvalid
		}
		boxIDs = append(boxIDs, int32(id))
	}
	rows, err := s.q.GetMessageBoxesForForward(ctx, sqlcgen.GetMessageBoxesForForwardParams{
		OwnerUserID: req.OwnerUserID,
		PeerType:    string(req.FromPeer.Type),
		PeerID:      req.FromPeer.ID,
		BoxIds:      boxIDs,
	})
	if err != nil {
		return res, fmt.Errorf("get forward messages: %w", err)
	}
	if len(rows) != len(req.MessageIDs) {
		return res, domain.ErrMessageIDInvalid
	}
	res.SenderMessages = make([]domain.Message, 0, len(rows))
	res.RecipientMessages = make([]domain.Message, 0, len(rows))
	res.SenderEvents = make([]domain.UpdateEvent, 0, len(rows))
	res.RecipientEvents = make([]domain.UpdateEvent, 0, len(rows))
	res.Duplicates = make([]bool, 0, len(rows))
	for i, row := range rows {
		if int(row.BoxID) != req.MessageIDs[i] {
			return res, domain.ErrMessageIDInvalid
		}
		source, err := messageFromForwardRow(row)
		if err != nil {
			return res, err
		}
		if source.NoForwards {
			return res, domain.ErrChatForwardsRestricted
		}
		var forward *domain.MessageForward
		if !req.DropAuthor {
			forward = cloneMessageForward(source.Forward)
			if forward == nil {
				forward = &domain.MessageForward{From: source.From, Date: source.Date}
			}
			// 转发进 Saved Messages 必须带 saved_from_peer/msg_id 成对字段，
			// 客户端靠它提供"跳回原会话"。
			if req.ToUserID == req.OwnerUserID {
				forward.SavedFrom = req.FromPeer
				forward.SavedFromMsgID = req.MessageIDs[i]
			}
		}
		sent, err := s.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:     req.OwnerUserID,
			RecipientUserID:  req.ToUserID,
			RandomID:         req.RandomIDs[i],
			Message:          source.Body,
			Entities:         append([]domain.MessageEntity(nil), source.Entities...),
			Media:            source.Media,
			Silent:           req.Silent,
			NoForwards:       req.NoForwards,
			ReplyTo:          req.ReplyTo,
			Forward:          forward,
			Date:             req.Date,
			OriginAuthKeyID:  req.OriginAuthKeyID,
			OriginSessionID:  req.OriginSessionID,
			RecipientBlocked: req.RecipientBlocked,
		})
		if err != nil {
			return res, err
		}
		res.SenderMessages = append(res.SenderMessages, sent.SenderMessage)
		res.RecipientMessages = append(res.RecipientMessages, sent.RecipientMessage)
		res.SenderEvents = append(res.SenderEvents, sent.SenderEvent)
		res.RecipientEvents = append(res.RecipientEvents, sent.RecipientEvent)
		res.Duplicates = append(res.Duplicates, sent.Duplicate)
	}
	return res, nil
}

func messageFromForwardRow(row sqlcgen.GetMessageBoxesForForwardRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode forward message entities: %w", err)
	}
	silent, noforwards, reply, forward, err := messageMetadataFromFields(
		row.Silent,
		row.Noforwards,
		row.ReplyToMsgID,
		row.ReplyToPeerType,
		row.ReplyToPeerID,
		row.ReplyToTopID,
		row.ReplyToStoryID,
		row.QuoteText,
		row.QuoteEntitiesJson,
		row.QuoteOffset,
		row.FwdFromPeerType,
		row.FwdFromPeerID,
		row.FwdFromName,
		row.FwdDate,
		row.FwdSavedFromPeerType,
		row.FwdSavedFromPeerID,
		row.FwdSavedFromMsgID,
	)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode forward message metadata: %w", err)
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode forward message media: %w", err)
	}
	return domain.Message{
		Media:          media,
		ID:             int(row.BoxID),
		UID:            row.PrivateMessageID,
		OwnerUserID:    row.OwnerUserID,
		Peer:           domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		From:           domain.Peer{Type: domain.PeerTypeUser, ID: row.FromUserID},
		Date:           int(row.MessageDate),
		EditDate:       int(row.EditDate),
		HideEdited:     row.HideEdited,
		Out:            row.Outgoing,
		Silent:         silent,
		NoForwards:     noforwards,
		Body:           row.Body,
		Entities:       entities,
		ReplyTo:        reply,
		Forward:        forward,
		Pts:            int(row.Pts),
		TTLPeriod:      int(row.TtlPeriod),
		ExpiresAt:      int(row.ExpiresAt),
		MediaUnread:    row.MediaUnread,
		ReactionUnread: row.ReactionUnread,
		ViaBotID:       row.ViaBotID,
		GroupedID:      row.GroupedID,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}, nil
}
