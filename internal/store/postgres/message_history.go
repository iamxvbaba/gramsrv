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

func (s *MessageStore) GetByIDs(ctx context.Context, userID int64, ids []int) (domain.MessageList, error) {
	if userID == 0 || len(ids) == 0 {
		return domain.MessageList{}, nil
	}
	boxIDs := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			continue
		}
		boxIDs = append(boxIDs, int32(id))
	}
	if len(boxIDs) == 0 {
		return domain.MessageList{}, nil
	}
	rows, err := s.q.GetMessageBoxesByIDs(ctx, sqlcgen.GetMessageBoxesByIDsParams{
		OwnerUserID: userID,
		BoxIds:      boxIDs,
	})
	if err != nil {
		return domain.MessageList{}, fmt.Errorf("get messages by ids: %w", err)
	}
	out := domain.MessageList{
		Messages: make([]domain.Message, 0, len(rows)),
		Users:    make([]domain.User, 0, len(rows)*2),
	}
	seenUsers := map[int64]struct{}{}
	for _, row := range rows {
		msg, err := messageFromIDRow(row)
		if err != nil {
			return domain.MessageList{}, err
		}
		out.Messages = append(out.Messages, msg)
		appendUsersFromMessageIDRow(&out, seenUsers, row)
	}
	if err := s.enrichPrivateMessageReactions(ctx, s.db, userID, out.Messages); err != nil {
		return domain.MessageList{}, err
	}
	out.Hash = messageListHash(out.Messages)
	return out, nil
}

// GetByUID resolves one owner's box row by the indexed shared private_message_id.
func (s *MessageStore) GetByUID(ctx context.Context, userID, uid int64) (domain.Message, bool, error) {
	if userID == 0 || uid == 0 {
		return domain.Message{}, false, nil
	}
	row, err := s.q.GetMessageBoxByPrivateMessage(ctx, sqlcgen.GetMessageBoxByPrivateMessageParams{
		OwnerUserID:      userID,
		PrivateMessageID: uid,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Message{}, false, nil
	}
	if err != nil {
		return domain.Message{}, false, fmt.Errorf("get message by uid: %w", err)
	}
	if _, err := decodeReplyMarkup(row.ReplyMarkupJson); err != nil {
		return domain.Message{}, false, fmt.Errorf("get message by uid reply markup: %w", err)
	}
	return messageFromGetBoxRow(row), true, nil
}

func (s *MessageStore) ListByUser(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	addOffset := domain.ClampMessageHistoryAddOffset(filter.AddOffset)
	queryLimit := limit
	probeHasMore := !filter.NeedTotalCount && addOffset >= 0
	if probeHasMore {
		queryLimit++
	}
	savedPeerType := ""
	var savedPeerID int64
	if filter.SavedPeer.ID != 0 {
		savedPeerType = string(filter.SavedPeer.Type)
		savedPeerID = filter.SavedPeer.ID
	}
	// add_offset>=0 是 backward 热路径(初始加载/上滑翻页,占 getHistory 绝大多数)。
	// 走扁平静态查询 ListMessagesBackward:规划仅单 index scan + 2 LEFT JOIN,避免
	// ListMessagesByUser 大 CTE 把 4 个分支+total 全树规划(6.7ms→~1ms)。与 CTE
	// 的 backward 分支逐位等价。around/forward(add_offset<0,锚点跳转,较罕见)仍走
	// 原 CTE,逻辑零改动。total 在 backward 路径由 CountMessagesByUser 独立提供。
	var rows []sqlcgen.ListMessagesByUserRow
	if addOffset >= 0 {
		bw, err := s.q.ListMessagesBackward(ctx, sqlcgen.ListMessagesBackwardParams{
			OwnerUserID:   userID,
			HasPeer:       filter.HasPeer,
			PeerType:      string(filter.Peer.Type),
			PeerID:        filter.Peer.ID,
			Query:         filter.Query,
			MaxID:         pgInt32NonNegative(filter.MaxID),
			MinID:         pgInt32NonNegative(filter.MinID),
			PinnedOnly:    filter.PinnedOnly,
			MusicOnly:     filter.MusicOnly,
			SavedPeerType: savedPeerType,
			SavedPeerID:   savedPeerID,
			OffsetDate:    pgInt32NonNegative(filter.OffsetDate),
			OffsetID:      pgInt32NonNegative(filter.OffsetID),
			RowOffset:     pgInt32Bounded(addOffset),
			LimitCount:    int32(queryLimit),
		})
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("list messages (backward): %w", err)
		}
		rows = make([]sqlcgen.ListMessagesByUserRow, len(bw))
		for i := range bw {
			rows[i] = backwardRowToByUserRow(bw[i])
		}
		if filter.NeedTotalCount {
			total, err := s.q.CountMessagesByUser(ctx, sqlcgen.CountMessagesByUserParams{
				OwnerUserID:   userID,
				HasPeer:       filter.HasPeer,
				PeerType:      string(filter.Peer.Type),
				PeerID:        filter.Peer.ID,
				Query:         filter.Query,
				MaxID:         pgInt32NonNegative(filter.MaxID),
				MinID:         pgInt32NonNegative(filter.MinID),
				PinnedOnly:    filter.PinnedOnly,
				MusicOnly:     filter.MusicOnly,
				SavedPeerType: savedPeerType,
				SavedPeerID:   savedPeerID,
			})
			if err != nil {
				return domain.MessageList{}, fmt.Errorf("count messages: %w", err)
			}
			// 镜像原 CTE 的 CROSS JOIN total 语义:total_count 只随结果行下发,
			// paged 为空时不产出(out.Count 保持 0),故仅在有行时附着。
			for i := range rows {
				rows[i].TotalCount = total
			}
		}
	} else {
		var err error
		rows, err = s.q.ListMessagesByUser(ctx, sqlcgen.ListMessagesByUserParams{
			OwnerUserID:    userID,
			HasPeer:        filter.HasPeer,
			PeerType:       string(filter.Peer.Type),
			PeerID:         filter.Peer.ID,
			Query:          filter.Query,
			OffsetID:       pgInt32NonNegative(filter.OffsetID),
			OffsetDate:     pgInt32NonNegative(filter.OffsetDate),
			MaxID:          pgInt32NonNegative(filter.MaxID),
			MinID:          pgInt32NonNegative(filter.MinID),
			AddOffset:      pgInt32Bounded(addOffset),
			LimitCount:     int32(queryLimit),
			PinnedOnly:     filter.PinnedOnly,
			MusicOnly:      filter.MusicOnly,
			NeedTotalCount: filter.NeedTotalCount,
			SavedPeerType:  savedPeerType,
			SavedPeerID:    savedPeerID,
		})
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("list messages: %w", err)
		}
	}
	hasMore := false
	if probeHasMore && len(rows) > limit {
		hasMore = true
		rows = rows[:limit]
	}
	out := domain.MessageList{
		Messages: make([]domain.Message, 0, len(rows)),
		Users:    make([]domain.User, 0, len(rows)*2),
	}
	seenUsers := map[int64]struct{}{}
	for _, row := range rows {
		entities, err := decodeMessageEntities(row.EntitiesJson)
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("decode message entities: %w", err)
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
			return domain.MessageList{}, fmt.Errorf("decode message metadata: %w", err)
		}
		media, err := decodeMessageMedia(row.MediaJson)
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("decode message media: %w", err)
		}
		markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("decode message reply markup: %w", err)
		}
		rich, err := decodeRichMessage(row.RichMessageJson)
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("decode message rich message: %w", err)
		}
		out.Messages = append(out.Messages, domain.Message{
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
			Media:          media,
			ReplyMarkup:    markup,
			RichMessage:    rich,
			MediaUnread:    row.MediaUnread,
			ReactionUnread: row.ReactionUnread,
			ViaBotID:       row.ViaBotID,
			GroupedID:      row.GroupedID,
			Effect:         row.Effect,
			Pinned:         row.Pinned,
			SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
		})
		if filter.NeedTotalCount && out.Count == 0 {
			out.Count = int(row.TotalCount)
		}
		appendUserFromMessageRow(&out, seenUsers, row)
	}
	if !filter.NeedTotalCount {
		out.Count = len(out.Messages)
		if hasMore {
			out.Count++
		}
	}
	if err := s.enrichPrivateMessageReactions(ctx, s.db, userID, out.Messages); err != nil {
		return domain.MessageList{}, err
	}
	out.Hash = messageListHash(out.Messages)
	return out, nil
}

func (s *MessageStore) ReadHistory(ctx context.Context, req domain.ReadHistoryRequest) (res domain.ReadHistoryResult, err error) {
	res = domain.ReadHistoryResult{OwnerUserID: req.OwnerUserID, Peer: req.Peer, MaxID: req.MaxID}
	if req.OwnerUserID == 0 {
		return res, fmt.Errorf("read history: missing owner user id")
	}
	if req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 {
		return res, fmt.Errorf("read history: invalid peer")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return res, fmt.Errorf("read history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin read history tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	// advisory lock 串行化与会话对端的并发写（peer 即私聊另一方 / 回执 sender），须在行锁前获取。
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID, req.Peer.ID); err != nil {
		return res, fmt.Errorf("lock read history users: %w", err)
	}

	state, err := qtx.GetDialogReadStateForUpdate(ctx, sqlcgen.GetDialogReadStateForUpdateParams{
		UserID:   req.OwnerUserID,
		PeerType: string(req.Peer.Type),
		PeerID:   req.Peer.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return res, nil
		}
		return res, fmt.Errorf("get dialog read state: %w", err)
	}
	oldRead := int(state.ReadInboxMaxID)
	maxAllocated, err := maxMessageBoxIDForDialog(ctx, tx, req.OwnerUserID, req.Peer)
	if err != nil {
		return res, fmt.Errorf("load dialog max box id: %w", err)
	}
	clampedFutureRead := false
	if oldRead > maxAllocated {
		if err := clampDialogReadInboxToMaxBox(ctx, tx, req.OwnerUserID, req.Peer, maxAllocated); err != nil {
			return res, fmt.Errorf("clamp dialog read inbox: %w", err)
		}
		oldRead = maxAllocated
		clampedFutureRead = true
		res.StillUnreadCount = 0
	}
	readMax := req.MaxID
	if readMax <= 0 {
		readMax = int(state.TopMessageID)
	}
	if readMax > int(state.TopMessageID) {
		readMax = int(state.TopMessageID)
	}
	res.MaxID = readMax
	advancesRead := readMax > oldRead
	if !advancesRead {
		if int(state.UnreadCount) > 0 {
			updated, err := qtx.UpdateDialogReadInbox(ctx, sqlcgen.UpdateDialogReadInboxParams{
				UserID:         req.OwnerUserID,
				PeerType:       string(req.Peer.Type),
				PeerID:         req.Peer.ID,
				ReadInboxMaxID: int32(readMax),
			})
			if err != nil {
				return res, fmt.Errorf("repair stale dialog unread count: %w", err)
			}
			res.MaxID = int(updated.ReadInboxMaxID)
			res.StillUnreadCount = int(updated.UnreadCount)
			if err := tx.Commit(ctx); err != nil {
				return res, fmt.Errorf("commit read history repair tx: %w", err)
			}
			committed = true
		} else if clampedFutureRead {
			if err := tx.Commit(ctx); err != nil {
				return res, fmt.Errorf("commit read history clamp tx: %w", err)
			}
			committed = true
		}
		return res, nil
	}

	candidate, candidateErr := qtx.LatestIncomingReadReceiptCandidate(ctx, sqlcgen.LatestIncomingReadReceiptCandidateParams{
		OwnerUserID:       req.OwnerUserID,
		PeerType:          string(req.Peer.Type),
		PeerID:            req.Peer.ID,
		OldReadInboxMaxID: int32(oldRead),
		NewReadInboxMaxID: int32(readMax),
	})
	if candidateErr != nil && !errors.Is(candidateErr, pgx.ErrNoRows) {
		return res, fmt.Errorf("load read receipt candidate: %w", candidateErr)
	}

	updated, err := qtx.UpdateDialogReadInbox(ctx, sqlcgen.UpdateDialogReadInboxParams{
		UserID:         req.OwnerUserID,
		PeerType:       string(req.Peer.Type),
		PeerID:         req.Peer.ID,
		ReadInboxMaxID: int32(readMax),
	})
	if err != nil {
		return res, fmt.Errorf("update dialog read inbox: %w", err)
	}
	readerPts, err := s.reservePts(ctx, tx, req.OwnerUserID)
	if err != nil {
		return res, fmt.Errorf("allocate read history pts: %w", err)
	}
	res.Changed = true
	res.MaxID = int(updated.ReadInboxMaxID)
	res.StillUnreadCount = int(updated.UnreadCount)
	res.InboxEvent = domain.UpdateEvent{
		UserID:           req.OwnerUserID,
		Type:             domain.UpdateEventReadHistoryInbox,
		Pts:              readerPts,
		PtsCount:         1,
		Date:             req.Date,
		Peer:             req.Peer,
		MaxID:            res.MaxID,
		StillUnreadCount: res.StillUnreadCount,
	}
	if err := appendUserUpdateEvent(ctx, tx, qtx, req.OwnerUserID, res.InboxEvent); err != nil {
		return res, fmt.Errorf("append read inbox event: %w", err)
	}
	if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
		TargetUserID:     req.OwnerUserID,
		Pts:              int32(readerPts),
		EventType:        string(domain.UpdateEventReadHistoryInbox),
		ExcludeAuthKeyID: authKeyIDToInt64(req.OriginAuthKeyID),
		ExcludeSessionID: req.OriginSessionID,
	}); err != nil {
		return res, fmt.Errorf("enqueue read inbox dispatch: %w", err)
	}

	if candidateErr == nil && candidate.SenderOwnerUserID != 0 && int(candidate.SenderBoxID) > 0 {
		if _, err := qtx.UpdateDialogReadOutbox(ctx, sqlcgen.UpdateDialogReadOutboxParams{
			UserID:          candidate.SenderOwnerUserID,
			PeerType:        string(domain.PeerTypeUser),
			PeerID:          req.OwnerUserID,
			ReadOutboxMaxID: candidate.SenderBoxID,
		}); err == nil {
			senderPts, err := s.reservePts(ctx, tx, candidate.SenderOwnerUserID)
			if err != nil {
				return res, fmt.Errorf("allocate read outbox pts: %w", err)
			}
			res.OutboxChanged = true
			res.OutboxUserID = candidate.SenderOwnerUserID
			res.OutboxEvent = domain.UpdateEvent{
				UserID:   candidate.SenderOwnerUserID,
				Type:     domain.UpdateEventReadHistoryOutbox,
				Pts:      senderPts,
				PtsCount: 1,
				Date:     req.Date,
				Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID},
				MaxID:    int(candidate.SenderBoxID),
			}
			if err := appendUserUpdateEvent(ctx, tx, qtx, candidate.SenderOwnerUserID, res.OutboxEvent); err != nil {
				return res, fmt.Errorf("append read outbox event: %w", err)
			}
			if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
				TargetUserID:     candidate.SenderOwnerUserID,
				Pts:              int32(senderPts),
				EventType:        string(domain.UpdateEventReadHistoryOutbox),
				ExcludeAuthKeyID: 0,
				ExcludeSessionID: 0,
			}); err != nil {
				return res, fmt.Errorf("enqueue read outbox dispatch: %w", err)
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return res, fmt.Errorf("update dialog read outbox: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit read history tx: %w", err)
	}
	committed = true
	return res, nil
}

func (s *MessageStore) DeleteHistory(ctx context.Context, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error) {
	res := domain.DeleteMessagesResult{OwnerUserID: req.OwnerUserID}
	if req.OwnerUserID == 0 {
		return res, fmt.Errorf("delete history: missing owner user id")
	}
	if req.Peer.Type != domain.PeerTypeUser || req.Peer.ID == 0 {
		return res, fmt.Errorf("delete history: invalid peer")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return res, fmt.Errorf("delete history: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin delete history tx: %w", err)
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
		return res, fmt.Errorf("lock delete history users: %w", err)
	}

	maxID := pgInt32NonNegative(req.MaxID)
	minDate := pgInt32NonNegative(req.MinDate)
	maxDate := pgInt32NonNegative(req.MaxDate)
	rows, err := qtx.DeleteMessageBoxesByPeerBatch(ctx, sqlcgen.DeleteMessageBoxesByPeerBatchParams{
		OwnerUserID: req.OwnerUserID,
		PeerType:    string(req.Peer.Type),
		PeerID:      req.Peer.ID,
		MaxID:       maxID,
		MinDate:     minDate,
		MaxDate:     maxDate,
		LimitCount:  int32(domain.MaxDeleteHistoryBatch),
	})
	if err != nil {
		return res, fmt.Errorf("delete message boxes by peer: %w", err)
	}
	deleted := deletedRowsFromPeerBatchRows(rows)
	if req.Revoke {
		if len(deleted) > 0 {
			peerRows, err := qtx.DeleteMessageBoxesByPrivateMessages(ctx, privateMessageDeleteParams(deleted))
			if err != nil {
				return res, fmt.Errorf("delete revoked private history boxes: %w", err)
			}
			deleted = append(deleted, deletedRowsFromPrivateRows(peerRows)...)
		}
		// 反查只覆盖"本批从我方实删的行"：我方此前已单向删除/清空过的
		// 消息不在本批，对端会永久残留。全量清史（max_id 不限）与按日期
		// 清史的 revoke 直接对对端 box 再扫一批；date 是共享属性、对端
		// 适用同一区间，box_id 上限则是 owner 私有序无法映射，部分
		// max_id 清史保持反查模型（官方 UI 无此入口）。
		if req.MaxID <= 0 && req.Peer.ID != req.OwnerUserID {
			peerSideRows, err := qtx.DeleteMessageBoxesByPeerBatch(ctx, sqlcgen.DeleteMessageBoxesByPeerBatchParams{
				OwnerUserID: req.Peer.ID,
				PeerType:    string(domain.PeerTypeUser),
				PeerID:      req.OwnerUserID,
				MaxID:       0,
				MinDate:     minDate,
				MaxDate:     maxDate,
				LimitCount:  int32(domain.MaxDeleteHistoryBatch),
			})
			if err != nil {
				return res, fmt.Errorf("delete revoked peer-side history boxes: %w", err)
			}
			deleted = append(deleted, deletedRowsFromPeerBatchRows(peerSideRows)...)
		}
	}
	res, err = s.finishDeleteMessagesTx(ctx, tx, qtx, req.OwnerUserID, req.OriginAuthKeyID, req.OriginSessionID, req.Date, deleted, req.JustClear)
	if err != nil {
		return res, err
	}
	more, err := qtx.HasDeletableMessageBoxByPeer(ctx, sqlcgen.HasDeletableMessageBoxByPeerParams{
		OwnerUserID: req.OwnerUserID,
		PeerType:    string(req.Peer.Type),
		PeerID:      req.Peer.ID,
		MaxID:       maxID,
		MinDate:     minDate,
		MaxDate:     maxDate,
	})
	if err != nil {
		return res, fmt.Errorf("check remaining history after delete: %w", err)
	}
	if !more && req.Revoke && req.MaxID <= 0 && req.Peer.ID != req.OwnerUserID {
		more, err = qtx.HasDeletableMessageBoxByPeer(ctx, sqlcgen.HasDeletableMessageBoxByPeerParams{
			OwnerUserID: req.Peer.ID,
			PeerType:    string(domain.PeerTypeUser),
			PeerID:      req.OwnerUserID,
			MaxID:       0,
			MinDate:     minDate,
			MaxDate:     maxDate,
		})
		if err != nil {
			return res, fmt.Errorf("check remaining peer history after revoke: %w", err)
		}
	}
	if more {
		res.Offset = 1
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit delete history tx: %w", err)
	}
	committed = true
	return res, nil
}

func messageFromBoxRow(row sqlcgen.CreateMessageBoxRow) domain.Message {
	entities, _ := decodeMessageEntities(row.EntitiesJson)
	media, _ := decodeMessageMedia(row.MediaJson)
	markup, _ := decodeReplyMarkup(row.ReplyMarkupJson)
	rich, _ := decodeRichMessage(row.RichMessageJson)
	silent, noforwards, reply, forward, _ := messageMetadataFromFields(
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
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
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
		Effect:         row.Effect,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}
}

func messageFromGetBoxRow(row sqlcgen.GetMessageBoxByPrivateMessageRow) domain.Message {
	entities, _ := decodeMessageEntities(row.EntitiesJson)
	media, _ := decodeMessageMedia(row.MediaJson)
	markup, _ := decodeReplyMarkup(row.ReplyMarkupJson)
	rich, _ := decodeRichMessage(row.RichMessageJson)
	silent, noforwards, reply, forward, _ := messageMetadataFromFields(
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
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
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
		Effect:         row.Effect,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}
}

func messageFromVisibleBoxRow(row sqlcgen.ListVisibleMessageBoxesByPrivateMessageRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode visible message entities: %w", err)
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
		return domain.Message{}, fmt.Errorf("decode visible message metadata: %w", err)
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode visible message media: %w", err)
	}
	markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode visible message reply markup: %w", err)
	}
	rich, err := decodeRichMessage(row.RichMessageJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode visible message rich message: %w", err)
	}
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
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
		Effect:         row.Effect,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}, nil
}

func messageFromUpdateEditRow(row sqlcgen.UpdateMessageBoxEditRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode edited message entities: %w", err)
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
		return domain.Message{}, fmt.Errorf("decode edited message metadata: %w", err)
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode edited message media: %w", err)
	}
	markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode edited message reply markup: %w", err)
	}
	rich, err := decodeRichMessage(row.RichMessageJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode edited message rich message: %w", err)
	}
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
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
		Effect:         row.Effect,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}, nil
}

func messageFromIDRow(row sqlcgen.GetMessageBoxesByIDsRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode message entities: %w", err)
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
		return domain.Message{}, fmt.Errorf("decode message metadata: %w", err)
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode message media: %w", err)
	}
	markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode message reply markup: %w", err)
	}
	rich, err := decodeRichMessage(row.RichMessageJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode message rich message: %w", err)
	}
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
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
		Effect:         row.Effect,
		Pinned:         row.Pinned,
		SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
	}, nil
}

func eventFromMessage(msg domain.Message) domain.UpdateEvent {
	return domain.UpdateEvent{
		UserID:   msg.OwnerUserID,
		Type:     domain.UpdateEventNewMessage,
		Pts:      msg.Pts,
		PtsCount: 1,
		Date:     msg.Date,
		Message:  msg,
	}
}

// backwardRowToByUserRow 把 ListMessagesBackward(扁平 backward 热路径)的行适配为
// ListMessagesByUserRow,从而复用 ListByUser 既有的解码/用户收集下游逻辑。两者列与
// base 完全一致,仅缺 TotalCount(由调用方按 NeedTotalCount 单独填,默认 0)。
func backwardRowToByUserRow(r sqlcgen.ListMessagesBackwardRow) sqlcgen.ListMessagesByUserRow {
	return sqlcgen.ListMessagesByUserRow{
		BoxID:                         r.BoxID,
		PrivateMessageID:              r.PrivateMessageID,
		OwnerUserID:                   r.OwnerUserID,
		PeerType:                      r.PeerType,
		PeerID:                        r.PeerID,
		FromUserID:                    r.FromUserID,
		MessageDate:                   r.MessageDate,
		TtlPeriod:                     r.TtlPeriod,
		ExpiresAt:                     r.ExpiresAt,
		EditDate:                      r.EditDate,
		HideEdited:                    r.HideEdited,
		Outgoing:                      r.Outgoing,
		Body:                          r.Body,
		EntitiesJson:                  r.EntitiesJson,
		Silent:                        r.Silent,
		Noforwards:                    r.Noforwards,
		ReplyToMsgID:                  r.ReplyToMsgID,
		ReplyToPeerType:               r.ReplyToPeerType,
		ReplyToPeerID:                 r.ReplyToPeerID,
		ReplyToTopID:                  r.ReplyToTopID,
		ReplyToStoryID:                r.ReplyToStoryID,
		QuoteText:                     r.QuoteText,
		QuoteEntitiesJson:             r.QuoteEntitiesJson,
		QuoteOffset:                   r.QuoteOffset,
		FwdFromPeerType:               r.FwdFromPeerType,
		FwdFromPeerID:                 r.FwdFromPeerID,
		FwdFromName:                   r.FwdFromName,
		FwdDate:                       r.FwdDate,
		FwdSavedFromPeerType:          r.FwdSavedFromPeerType,
		FwdSavedFromPeerID:            r.FwdSavedFromPeerID,
		FwdSavedFromMsgID:             r.FwdSavedFromMsgID,
		SavedPeerType:                 r.SavedPeerType,
		SavedPeerID:                   r.SavedPeerID,
		Pts:                           r.Pts,
		MediaJson:                     r.MediaJson,
		MediaUnread:                   r.MediaUnread,
		ReactionUnread:                r.ReactionUnread,
		Pinned:                        r.Pinned,
		ViaBotID:                      r.ViaBotID,
		GroupedID:                     r.GroupedID,
		Effect:                        r.Effect,
		ReplyMarkupJson:               r.ReplyMarkupJson,
		RichMessageJson:               r.RichMessageJson,
		PeerUserID:                    r.PeerUserID,
		PeerAccessHash:                r.PeerAccessHash,
		PeerPhone:                     r.PeerPhone,
		PeerFirstName:                 r.PeerFirstName,
		PeerLastName:                  r.PeerLastName,
		PeerUsername:                  r.PeerUsername,
		PeerCountryCode:               r.PeerCountryCode,
		PeerVerified:                  r.PeerVerified,
		PeerSupport:                   r.PeerSupport,
		PeerIsBot:                     r.PeerIsBot,
		PeerBotInfoVersion:            r.PeerBotInfoVersion,
		PeerPremiumUntil:              r.PeerPremiumUntil,
		PeerEmojiStatusDocumentID:     r.PeerEmojiStatusDocumentID,
		PeerEmojiStatusUntil:          r.PeerEmojiStatusUntil,
		PeerLastSeenAt:                r.PeerLastSeenAt,
		FromUserUserID:                r.FromUserUserID,
		FromUserAccessHash:            r.FromUserAccessHash,
		FromUserPhone:                 r.FromUserPhone,
		FromUserFirstName:             r.FromUserFirstName,
		FromUserLastName:              r.FromUserLastName,
		FromUserUsername:              r.FromUserUsername,
		FromUserCountryCode:           r.FromUserCountryCode,
		FromUserVerified:              r.FromUserVerified,
		FromUserSupport:               r.FromUserSupport,
		FromUserIsBot:                 r.FromUserIsBot,
		FromUserBotInfoVersion:        r.FromUserBotInfoVersion,
		FromUserPremiumUntil:          r.FromUserPremiumUntil,
		FromUserEmojiStatusDocumentID: r.FromUserEmojiStatusDocumentID,
		FromUserEmojiStatusUntil:      r.FromUserEmojiStatusUntil,
		FromUserLastSeenAt:            r.FromUserLastSeenAt,
	}
}

func appendUserFromMessageRow(out *domain.MessageList, seen map[int64]struct{}, row sqlcgen.ListMessagesByUserRow) {
	appendMessageUsers(out, seen,
		domain.User{
			ID:                    row.PeerUserID,
			AccessHash:            row.PeerAccessHash,
			Phone:                 row.PeerPhone,
			FirstName:             row.PeerFirstName,
			LastName:              row.PeerLastName,
			Username:              row.PeerUsername,
			CountryCode:           row.PeerCountryCode,
			Verified:              row.PeerVerified,
			Support:               row.PeerSupport,
			Bot:                   row.PeerIsBot,
			BotInfoVersion:        int(row.PeerBotInfoVersion),
			PremiumUntil:          int(row.PeerPremiumUntil),
			EmojiStatusDocumentID: row.PeerEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.PeerEmojiStatusUntil),
			LastSeenAt:            int(row.PeerLastSeenAt),
		},
		domain.User{
			ID:                    row.FromUserUserID,
			AccessHash:            row.FromUserAccessHash,
			Phone:                 row.FromUserPhone,
			FirstName:             row.FromUserFirstName,
			LastName:              row.FromUserLastName,
			Username:              row.FromUserUsername,
			CountryCode:           row.FromUserCountryCode,
			Verified:              row.FromUserVerified,
			Support:               row.FromUserSupport,
			Bot:                   row.FromUserIsBot,
			BotInfoVersion:        int(row.FromUserBotInfoVersion),
			PremiumUntil:          int(row.FromUserPremiumUntil),
			EmojiStatusDocumentID: row.FromUserEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.FromUserEmojiStatusUntil),
			LastSeenAt:            int(row.FromUserLastSeenAt),
		},
	)
}

func appendUsersFromMessageIDRow(out *domain.MessageList, seen map[int64]struct{}, row sqlcgen.GetMessageBoxesByIDsRow) {
	appendMessageUsers(out, seen,
		domain.User{
			ID:                    row.PeerUserID,
			AccessHash:            row.PeerAccessHash,
			Phone:                 row.PeerPhone,
			FirstName:             row.PeerFirstName,
			LastName:              row.PeerLastName,
			Username:              row.PeerUsername,
			CountryCode:           row.PeerCountryCode,
			Verified:              row.PeerVerified,
			Support:               row.PeerSupport,
			Bot:                   row.PeerIsBot,
			BotInfoVersion:        int(row.PeerBotInfoVersion),
			PremiumUntil:          int(row.PeerPremiumUntil),
			EmojiStatusDocumentID: row.PeerEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.PeerEmojiStatusUntil),
			LastSeenAt:            int(row.PeerLastSeenAt),
		},
		domain.User{
			ID:                    row.FromUserUserID,
			AccessHash:            row.FromUserAccessHash,
			Phone:                 row.FromUserPhone,
			FirstName:             row.FromUserFirstName,
			LastName:              row.FromUserLastName,
			Username:              row.FromUserUsername,
			CountryCode:           row.FromUserCountryCode,
			Verified:              row.FromUserVerified,
			Support:               row.FromUserSupport,
			Bot:                   row.FromUserIsBot,
			BotInfoVersion:        int(row.FromUserBotInfoVersion),
			PremiumUntil:          int(row.FromUserPremiumUntil),
			EmojiStatusDocumentID: row.FromUserEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.FromUserEmojiStatusUntil),
			LastSeenAt:            int(row.FromUserLastSeenAt),
		},
	)
}

func appendMessageUsers(out *domain.MessageList, seen map[int64]struct{}, users ...domain.User) {
	add := func(u domain.User) {
		if u.ID == 0 {
			return
		}
		if _, ok := seen[u.ID]; ok {
			return
		}
		seen[u.ID] = struct{}{}
		out.Users = append(out.Users, u)
	}
	for _, user := range users {
		add(user)
	}
}
