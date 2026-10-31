package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

const retryableMessageTxAttempts = 3

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
	msg, err := messageFromGetBoxRow(row)
	return msg, err == nil, err
}

func (s *MessageStore) ListByUser(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error) {
	if filter.CountOnly {
		total, err := s.countMessagesByUser(ctx, userID, filter)
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("count messages: %w", err)
		}
		return domain.MessageList{Count: int(total)}, nil
	}
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
	savedReactionKeys := postgresSavedReactionKeys(filter.SavedReactions)
	// add_offset>=0 is the backward hot path (initial load / upward scroll paging, the vast
	// majority of getHistory). It uses the flat static query ListMessagesBackward: the plan is only
	// a single index scan + 2 LEFT JOINs, avoiding the big ListMessagesByUser CTE planning all four
	// branches over the whole tree (6.7ms→~1ms). It is bit-for-bit equivalent to the CTE's backward
	// branch. around/forward (add_offset<0, anchor jump, rarer) still use the CTE. total is always
	// provided independently by CountMessagesByUser and does not depend on message rows (an empty
	// page still has a total).
	var rows []sqlcgen.ListMessagesByUserRow
	if addOffset >= 0 {
		bw, err := s.q.ListMessagesBackward(ctx, sqlcgen.ListMessagesBackwardParams{
			OwnerUserID:          userID,
			SenderUserID:         filter.SenderUserID,
			HasPeer:              filter.HasPeer,
			PeerType:             string(filter.Peer.Type),
			PeerID:               filter.Peer.ID,
			RestrictPeerIds:      filter.RestrictPeerIDs,
			PeerIds:              filter.PeerIDs,
			Query:                filter.Query,
			MinDate:              pgInt32NonNegative(filter.MinDate),
			MaxDate:              pgInt32NonNegative(filter.MaxDate),
			MaxID:                pgInt32NonNegative(filter.MaxID),
			MinID:                pgInt32NonNegative(filter.MinID),
			PinnedOnly:           filter.PinnedOnly,
			MusicOnly:            filter.MusicOnly,
			PhoneCallsOnly:       filter.PhoneCallsOnly,
			MissedPhoneCallsOnly: filter.MissedPhoneCallsOnly,
			SavedPeerType:        savedPeerType,
			SavedPeerID:          savedPeerID,
			SavedReactionKeys:    savedReactionKeys,
			OffsetDate:           pgInt32NonNegative(filter.OffsetDate),
			OffsetID:             pgInt32NonNegative(filter.OffsetID),
			RowOffset:            pgInt32Bounded(addOffset),
			LimitCount:           int32(queryLimit),
		})
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("list messages (backward): %w", err)
		}
		rows = make([]sqlcgen.ListMessagesByUserRow, len(bw))
		for i := range bw {
			rows[i] = backwardRowToByUserRow(bw[i])
		}

	} else {
		var err error
		rows, err = s.q.ListMessagesByUser(ctx, sqlcgen.ListMessagesByUserParams{
			OwnerUserID:          userID,
			SenderUserID:         filter.SenderUserID,
			HasPeer:              filter.HasPeer,
			PeerType:             string(filter.Peer.Type),
			PeerID:               filter.Peer.ID,
			RestrictPeerIds:      filter.RestrictPeerIDs,
			PeerIds:              filter.PeerIDs,
			Query:                filter.Query,
			MinDate:              pgInt32NonNegative(filter.MinDate),
			MaxDate:              pgInt32NonNegative(filter.MaxDate),
			OffsetID:             pgInt32NonNegative(filter.OffsetID),
			OffsetDate:           pgInt32NonNegative(filter.OffsetDate),
			MaxID:                pgInt32NonNegative(filter.MaxID),
			MinID:                pgInt32NonNegative(filter.MinID),
			AddOffset:            pgInt32Bounded(addOffset),
			LimitCount:           int32(queryLimit),
			PinnedOnly:           filter.PinnedOnly,
			MusicOnly:            filter.MusicOnly,
			PhoneCallsOnly:       filter.PhoneCallsOnly,
			MissedPhoneCallsOnly: filter.MissedPhoneCallsOnly,
			SavedPeerType:        savedPeerType,
			SavedPeerID:          savedPeerID,
			SavedReactionKeys:    savedReactionKeys,
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
			row.ReplyExternalJson,
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
			PaidStars:      row.PaidMessageStars,
			MediaUnread:    row.MediaUnread,
			ReactionUnread: row.ReactionUnread,
			ViaBotID:       row.ViaBotID,
			GroupedID:      row.GroupedID,
			Effect:         row.Effect,
			Pinned:         row.Pinned,
			SavedPeer:      savedPeerFromFields(row.SavedPeerType, row.SavedPeerID),
		})
		appendUserFromMessageRow(&out, seenUsers, row)
	}
	if filter.NeedTotalCount {
		total, err := s.countMessagesByUser(ctx, userID, filter)
		if err != nil {
			return domain.MessageList{}, fmt.Errorf("count messages: %w", err)
		}
		out.Count = int(total)
	} else {
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

func postgresSavedReactionKeys(reactions []domain.MessageReaction) []string {
	out := make([]string, 0, len(reactions))
	for _, reaction := range reactions {
		if reaction.Valid() {
			out = append(out, string(reaction.Type)+":"+reaction.Value())
		}
	}
	return out
}

func (s *MessageStore) ReadHistory(ctx context.Context, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error) {
	var lastErr error
	for attempt := 0; attempt < retryableMessageTxAttempts; attempt++ {
		res, err := s.readHistoryOnce(ctx, req)
		if err == nil || !isRetryablePostgresTxError(err) || ctx.Err() != nil {
			return res, err
		}
		lastErr = err
	}
	return domain.ReadHistoryResult{OwnerUserID: req.OwnerUserID, Peer: req.Peer, MaxID: req.MaxID}, lastErr
}

func (s *MessageStore) readHistoryOnce(ctx context.Context, req domain.ReadHistoryRequest) (res domain.ReadHistoryResult, err error) {
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

	// The advisory lock serializes concurrent writes with the conversation peer (peer being the other side of the private chat / the receipt sender) and must be acquired before row locks.
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID, req.Peer.ID); err != nil {
		return res, fmt.Errorf("lock read history users: %w", err)
	}
	// This transaction may append durable inbox and outbox receipts for two
	// different users. Acquire both append fences before the first INSERT so a
	// batched Egress completion cannot take the same lanes in the opposite order.
	if err := lockDispatchOutboxAppendFences(ctx, tx, []int64{req.OwnerUserID, req.Peer.ID}); err != nil {
		return res, fmt.Errorf("lock read history dispatch append fences: %w", err)
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

	// The advisory lock serializes concurrent writes with the conversation peer and must be acquired before row locks, eliminating the AB-BA deadlock.
	if err := lockUsersForUpdate(ctx, tx, req.OwnerUserID, req.Peer.ID); err != nil {
		return res, fmt.Errorf("lock delete history users: %w", err)
	}

	maxID := pgInt32NonNegative(req.MaxID)
	minDate := pgInt32NonNegative(req.MinDate)
	maxDate := pgInt32NonNegative(req.MaxDate)
	var anchors map[int64]historyClearAnchor
	// TDesktop's full Clear history form is fixed as just_clear + max_id=0 with no date range;
	// a deletion by date only destroys the selected interval and does not generate a history-clear
	// service message locally. Only a full clear keeps the top anchor across batches.
	fullJustClear := req.JustClear && req.MaxID <= 0 && req.MinDate <= 0 && req.MaxDate <= 0
	if fullJustClear {
		anchors = make(map[int64]historyClearAnchor, 2)
		if anchor, found, err := s.loadHistoryClearAnchor(ctx, qtx, req.OwnerUserID, req.Peer); err != nil {
			return res, err
		} else if found {
			anchors[req.OwnerUserID] = anchor
		}
		if req.Revoke && req.Peer.ID != req.OwnerUserID {
			peer := domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}
			if anchor, found, err := s.loadHistoryClearAnchor(ctx, qtx, req.Peer.ID, peer); err != nil {
				return res, err
			} else if found {
				anchors[req.Peer.ID] = anchor
			}
		}
	}
	ownerKeepBoxID := int32(0)
	if anchor, ok := anchors[req.OwnerUserID]; ok {
		ownerKeepBoxID = int32(anchor.boxID)
	}
	rows, err := qtx.DeleteMessageBoxesByPeerBatch(ctx, sqlcgen.DeleteMessageBoxesByPeerBatchParams{
		OwnerUserID: req.OwnerUserID,
		PeerType:    string(req.Peer.Type),
		PeerID:      req.Peer.ID,
		KeepBoxID:   ownerKeepBoxID,
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
		// max_id>0 is the owner-local box boundary, so the peer copies can only be deleted through
		// the logical private-message mapping. With max_id=0 both sides each scan a batch over the
		// same date range, avoiding linked delete and the two sides' kept anchors deleting each other.
		if req.MaxID > 0 && len(deleted) > 0 {
			peerRows, err := qtx.DeleteMessageBoxesByPrivateMessages(ctx, privateMessageDeleteParams(deleted))
			if err != nil {
				return res, fmt.Errorf("delete revoked private history boxes: %w", err)
			}
			deleted = append(deleted, deletedRowsFromPrivateRows(peerRows)...)
		}
		// The reverse lookup covers only "the rows actually deleted on our side in this batch":
		// messages we had already one-sidedly deleted/cleared earlier are not in this batch, and
		// the peer's copies would remain forever. For a full history clear (max_id unbounded) and a
		// date-range clear, revoke directly scans another batch on the peer's box; date is a shared
		// attribute, so the same range applies to the peer, whereas the box_id upper bound is the
		// owner's private ordering and cannot be mapped, so a partial max_id clear keeps the
		// reverse-lookup model (the official UI has no such entry point).
		if req.MaxID <= 0 && req.Peer.ID != req.OwnerUserID {
			peerKeepBoxID := int32(0)
			if anchor, ok := anchors[req.Peer.ID]; ok {
				peerKeepBoxID = int32(anchor.boxID)
			}
			peerSideRows, err := qtx.DeleteMessageBoxesByPeerBatch(ctx, sqlcgen.DeleteMessageBoxesByPeerBatchParams{
				OwnerUserID: req.Peer.ID,
				PeerType:    string(domain.PeerTypeUser),
				PeerID:      req.OwnerUserID,
				KeepBoxID:   peerKeepBoxID,
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
	res, err = s.finishDeleteMessagesTx(ctx, tx, qtx, req.OwnerUserID, req.OriginAuthKeyID, req.OriginSessionID, req.Date, deleted, anchors)
	if err != nil {
		return res, err
	}
	more, err := qtx.HasDeletableMessageBoxByPeer(ctx, sqlcgen.HasDeletableMessageBoxByPeerParams{
		OwnerUserID: req.OwnerUserID,
		PeerType:    string(req.Peer.Type),
		PeerID:      req.Peer.ID,
		KeepBoxID:   ownerKeepBoxID,
		MaxID:       maxID,
		MinDate:     minDate,
		MaxDate:     maxDate,
	})
	if err != nil {
		return res, fmt.Errorf("check remaining history after delete: %w", err)
	}
	if !more && req.Revoke && req.MaxID <= 0 && req.Peer.ID != req.OwnerUserID {
		peerKeepBoxID := int32(0)
		if anchor, ok := anchors[req.Peer.ID]; ok {
			peerKeepBoxID = int32(anchor.boxID)
		}
		more, err = qtx.HasDeletableMessageBoxByPeer(ctx, sqlcgen.HasDeletableMessageBoxByPeerParams{
			OwnerUserID: req.Peer.ID,
			PeerType:    string(domain.PeerTypeUser),
			PeerID:      req.OwnerUserID,
			KeepBoxID:   peerKeepBoxID,
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

func messageFromBoxRow(row sqlcgen.CreateMessageBoxRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, err
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, err
	}
	markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
	if err != nil {
		return domain.Message{}, err
	}
	rich, err := decodeRichMessage(row.RichMessageJson)
	if err != nil {
		return domain.Message{}, err
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
		row.ReplyExternalJson,
		row.FwdFromPeerType,
		row.FwdFromPeerID,
		row.FwdFromName,
		row.FwdDate,
		row.FwdSavedFromPeerType,
		row.FwdSavedFromPeerID,
		row.FwdSavedFromMsgID,
	)
	if err != nil {
		return domain.Message{}, err
	}
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
		PaidStars:      row.PaidMessageStars,
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

func messageFromGetBoxRow(row sqlcgen.GetMessageBoxByPrivateMessageRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, err
	}
	media, err := decodeMessageMedia(row.MediaJson)
	if err != nil {
		return domain.Message{}, err
	}
	markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
	if err != nil {
		return domain.Message{}, err
	}
	rich, err := decodeRichMessage(row.RichMessageJson)
	if err != nil {
		return domain.Message{}, err
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
		row.ReplyExternalJson,
		row.FwdFromPeerType,
		row.FwdFromPeerID,
		row.FwdFromName,
		row.FwdDate,
		row.FwdSavedFromPeerType,
		row.FwdSavedFromPeerID,
		row.FwdSavedFromMsgID,
	)
	if err != nil {
		return domain.Message{}, err
	}
	return domain.Message{
		Media:          media,
		ReplyMarkup:    markup,
		RichMessage:    rich,
		PaidStars:      row.PaidMessageStars,
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
		row.ReplyExternalJson,
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
		PaidStars:      row.PaidMessageStars,
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
		row.ReplyExternalJson,
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
		PaidStars:      row.PaidMessageStars,
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
		row.ReplyExternalJson,
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
		PaidStars:      row.PaidMessageStars,
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

// backwardRowToByUserRow adapts a ListMessagesBackward row (the flat backward hot path) into a
// ListMessagesByUserRow, thereby reusing ListByUser's existing decode/user-collection downstream
// logic. The two have identical columns and base; the total is queried independently by the caller
// when NeedTotalCount.
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
		ReplyExternalJson:             r.ReplyExternalJson,
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
		PaidMessageStars:              r.PaidMessageStars,
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

// countMessagesByUser shares page predicates but intentionally excludes page anchors.
func (s *MessageStore) countMessagesByUser(ctx context.Context, userID int64, filter domain.MessageFilter) (int32, error) {
	savedPeerType := ""
	var savedPeerID int64
	if filter.SavedPeer.ID != 0 {
		savedPeerType = string(filter.SavedPeer.Type)
		savedPeerID = filter.SavedPeer.ID
	}
	savedReactionKeys := postgresSavedReactionKeys(filter.SavedReactions)
	return s.q.CountMessagesByUser(ctx, sqlcgen.CountMessagesByUserParams{
		SenderUserID:         filter.SenderUserID,
		OwnerUserID:          userID,
		HasPeer:              filter.HasPeer,
		PeerType:             string(filter.Peer.Type),
		PeerID:               filter.Peer.ID,
		RestrictPeerIds:      filter.RestrictPeerIDs,
		PeerIds:              filter.PeerIDs,
		Query:                filter.Query,
		MinDate:              pgInt32NonNegative(filter.MinDate),
		MaxDate:              pgInt32NonNegative(filter.MaxDate),
		MaxID:                pgInt32NonNegative(filter.MaxID),
		MinID:                pgInt32NonNegative(filter.MinID),
		PinnedOnly:           filter.PinnedOnly,
		MusicOnly:            filter.MusicOnly,
		PhoneCallsOnly:       filter.PhoneCallsOnly,
		MissedPhoneCallsOnly: filter.MissedPhoneCallsOnly,
		SavedPeerType:        savedPeerType,
		SavedPeerID:          savedPeerID,
		SavedReactionKeys:    savedReactionKeys,
	})
}
