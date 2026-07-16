package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"sort"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
	"time"
)

func (s *MessageStore) Create(ctx context.Context, msg domain.Message) (domain.Message, error) {
	if err := s.ensureOfficialSystemUser(ctx, msg); err != nil {
		return domain.Message{}, err
	}
	entities, err := encodeMessageEntities(msg.Entities)
	if err != nil {
		return domain.Message{}, err
	}
	if msg.Date == 0 {
		msg.Date = int(time.Now().Unix())
	}
	if msg.ID == 0 {
		msg.ID, err = s.boxIDs.NextBoxID(ctx, msg.OwnerUserID)
		if err != nil {
			return domain.Message{}, fmt.Errorf("allocate login message box id: %w", err)
		}
	}
	row, err := s.q.CreateMessage(ctx, sqlcgen.CreateMessageParams{
		OwnerUserID:  msg.OwnerUserID,
		BoxID:        int32(msg.ID),
		PeerType:     string(msg.Peer.Type),
		PeerID:       msg.Peer.ID,
		FromUserID:   msg.From.ID,
		MessageDate:  int32(msg.Date),
		Outgoing:     msg.Out,
		Body:         msg.Body,
		EntitiesJson: entities,
		Pts:          int32(msg.Pts),
	})
	if err != nil {
		return domain.Message{}, fmt.Errorf("create message: %w", err)
	}
	return messageFromCreateRow(row)
}

func (s *MessageStore) ensureOfficialSystemUser(ctx context.Context, msg domain.Message) error {
	return ensureOfficialSystemUserWithDB(ctx, s.db, msg)
}

func ensureOfficialSystemUserWithDB(ctx context.Context, db sqlcgen.DBTX, msg domain.Message) error {
	if msg.Peer.Type != domain.PeerTypeUser && msg.From.Type != domain.PeerTypeUser {
		return nil
	}
	u, ok := domain.SystemUserByID(msg.Peer.ID)
	if !ok {
		u, ok = domain.SystemUserByID(msg.From.ID)
	}
	if !ok {
		return nil
	}
	if _, err := db.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, country_code, verified, support, about, is_bot, bot_info_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO NOTHING
`, u.ID, u.AccessHash, u.Phone, u.FirstName, u.LastName, u.Username, u.CountryCode, u.Verified, u.Support, u.About, u.Bot, u.BotInfoVersion); err != nil {
		return fmt.Errorf("ensure official system user: %w", err)
	}
	return nil
}

func (s *MessageStore) SendPrivateText(ctx context.Context, req domain.SendPrivateTextRequest) (res domain.SendPrivateTextResult, err error) {
	return s.sendPrivateTextWithHooks(ctx, req, privateSendTxHooks{})
}

type privateSendTxHooks struct {
	before func(context.Context, pgx.Tx, *domain.SendPrivateTextRequest) error
	after  func(context.Context, pgx.Tx, domain.SendPrivateTextResult) error
}

func (s *MessageStore) sendPrivateTextWithHooks(ctx context.Context, req domain.SendPrivateTextRequest, hooks privateSendTxHooks) (res domain.SendPrivateTextResult, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		res, err = s.sendPrivateTextOnce(ctx, req, hooks)
		if err == nil {
			return res, nil
		}
		if !isMessageBoxDuplicateKey(err) || attempt > 0 {
			return domain.SendPrivateTextResult{}, err
		}
		if recoverErr := s.bumpBoxIDCountersAfterDuplicate(ctx, req); recoverErr != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("%w; recover box id counters: %v", err, recoverErr)
		}
	}
	return domain.SendPrivateTextResult{}, err
}

func (s *MessageStore) sendPrivateTextOnce(ctx context.Context, req domain.SendPrivateTextRequest, hooks privateSendTxHooks) (res domain.SendPrivateTextResult, err error) {
	if req.SenderUserID == 0 || req.RecipientUserID == 0 {
		return domain.SendPrivateTextResult{}, fmt.Errorf("send private text: missing user id")
	}
	if req.RandomID == 0 {
		return domain.SendPrivateTextResult{}, fmt.Errorf("send private text: missing random id")
	}
	if req.Message == "" && req.Media.IsZero() {
		return domain.SendPrivateTextResult{}, fmt.Errorf("send private text: empty message")
	}
	if req.Date == 0 {
		req.Date = int(time.Now().Unix())
	}
	entities, err := encodeMessageEntities(req.Entities)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	// reply_markup（bot inline keyboard）随消息一并入双盒；普通用户发送恒 nil → "{}"。
	replyMarkupJSON, err := encodeReplyMarkup(req.ReplyMarkup)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	// rich_message（Layer 227 富文本）随消息一并入双盒；普通消息恒 nil → "{}"。
	richMessageJSON, err := encodeRichMessage(req.RichMessage)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	requestFingerprint, err := store.PrivateSendFingerprint(req)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	// 常见的 lost-response 重放在开事务和拿双方 advisory lock 之前直接返回；
	// 并发首次请求仍由事务内 unique conflict + qtx 兜底，不能只依赖本次预查。
	// RPC/app 已完成同一只读查询时可跳过这次重复 round-trip。
	if !req.IdempotencyPreflighted {
		if duplicate, found, err := s.duplicateSendResult(ctx, s.q, req, requestFingerprint); err != nil {
			return domain.SendPrivateTextResult{}, err
		} else if found {
			duplicate.Duplicate = true
			return duplicate, nil
		}
	}
	senderReply, recipientReply, err := s.resolvePrivateSendReply(ctx, req)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	senderMeta, err := messageMetadataParamsFrom(req.Silent, req.NoForwards, senderReply, req.Forward)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	recipientMeta, err := messageMetadataParamsFrom(req.Silent, req.NoForwards, recipientReply, req.Forward)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.SendPrivateTextResult{}, fmt.Errorf("send private text: db does not support transactions")
	}

	var recipientBoxID, recipientPts int
	selfMessage := req.RecipientUserID == req.SenderUserID
	deliverRecipient := !selfMessage && !req.RecipientBlocked
	if selfMessage {
		savedPeer := domain.SavedPeerForSelfChat(req.SenderUserID, req.Forward)
		senderMeta.SavedPeerType = string(savedPeer.Type)
		senderMeta.SavedPeerID = savedPeer.ID
	}

	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("begin send message tx: %w", err)
	}
	qtx := sqlcgen.New(tx)
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = tx.Rollback(ctx)
	}()

	// 事务级 advisory lock 串行化涉及收发双方的并发写，在任何行锁之前获取，消除 watermark/dialog
	// 行锁的 AB-BA 死锁（A↔B 反向并发 send/read/edit）。
	if err := lockUsersForUpdate(ctx, tx, req.SenderUserID, req.RecipientUserID); err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("lock send users: %w", err)
	}
	if hooks.before != nil {
		if err := hooks.before(ctx, tx, &req); err != nil {
			return domain.SendPrivateTextResult{}, err
		}
	}
	mediaJSON, err := encodeMessageMedia(req.Media)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	ttlPeriod := req.TTLPeriod
	if ttlPeriod == 0 {
		ttlPeriod, err = privateHistoryTTLPeriod(ctx, tx, req.SenderUserID, req.RecipientUserID)
		if err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("load private ttl: %w", err)
		}
	}
	expiresAt := 0
	if ttlPeriod > 0 {
		expiresAt = req.Date + ttlPeriod
	}

	privateArg := sqlcgen.CreatePrivateMessageParams{
		SenderUserID:       req.SenderUserID,
		RecipientUserID:    req.RecipientUserID,
		RandomID:           req.RandomID,
		RequestFingerprint: requestFingerprint,
		RecipientDelivered: deliverRecipient,
		MessageDate:        int32(req.Date),
		Body:               req.Message,
		TtlPeriod:          int32(ttlPeriod),
		ExpiresAt:          int32(expiresAt),
		EntitiesJson:       entities,
		MediaJson:          mediaJSON,
		ReplyMarkupJson:    replyMarkupJSON,
		RichMessageJson:    richMessageJSON,
		ViaBotID:           req.ViaBotID,
		GroupedID:          req.GroupedID,
		Effect:             req.Effect,
	}
	applyCreatePrivateMessageMetadata(&privateArg, senderMeta)
	pm, err := qtx.CreatePrivateMessage(ctx, privateArg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 预查与 INSERT 之间另一请求可能已提交。必须在当前 qtx 读取，
			// 不能持事务连接/advisory lock 再从 s.q 申请第二条池连接。
			dup, found, dupErr := s.duplicateSendResult(ctx, qtx, req, requestFingerprint)
			if dupErr != nil {
				return domain.SendPrivateTextResult{}, dupErr
			}
			if !found {
				return domain.SendPrivateTextResult{}, fmt.Errorf("duplicate private message disappeared after unique conflict")
			}
			dup.Duplicate = true
			return dup, nil
		}
		return domain.SendPrivateTextResult{}, fmt.Errorf("create private message: %w", err)
	}

	senderBoxID, err := s.boxIDs.NextBoxID(ctx, req.SenderUserID)
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("allocate sender box id: %w", err)
	}
	senderPts, err := s.reservePts(ctx, tx, req.SenderUserID)
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("allocate sender pts: %w", err)
	}
	if deliverRecipient {
		recipientBoxID, err = s.boxIDs.NextBoxID(ctx, req.RecipientUserID)
		if err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("allocate recipient box id: %w", err)
		}
		recipientPts, err = s.reservePts(ctx, tx, req.RecipientUserID)
		if err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("allocate recipient pts: %w", err)
		}
	}

	senderArg := sqlcgen.CreateMessageBoxParams{
		OwnerUserID:      req.SenderUserID,
		BoxID:            int32(senderBoxID),
		PrivateMessageID: pm.ID,
		MessageSenderID:  req.SenderUserID,
		PeerType:         string(domain.PeerTypeUser),
		PeerID:           req.RecipientUserID,
		FromUserID:       req.SenderUserID,
		MessageDate:      int32(req.Date),
		Outgoing:         true,
		Body:             req.Message,
		TtlPeriod:        int32(ttlPeriod),
		ExpiresAt:        int32(expiresAt),
		EntitiesJson:     entities,
		Pts:              int32(senderPts),
		MediaJson:        mediaJSON,
		ReplyMarkupJson:  replyMarkupJSON,
		RichMessageJson:  richMessageJSON,
		ViaBotID:         req.ViaBotID,
		GroupedID:        req.GroupedID,
		Effect:           req.Effect,
		// voice/round 在发送者自己的副本上也保持"未听"，直到对端
		// readMessageContents 触发 sender 侧清除；发给自己无人可听，恒已读。
		MediaUnread:    req.Media.HasUnreadPayload() && !selfMessage,
		ReactionUnread: false,
	}
	applyCreateMessageBoxMetadata(&senderArg, senderMeta)
	senderRow, err := qtx.CreateMessageBox(ctx, senderArg)
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("create sender box: %w", err)
	}
	sender := messageFromBoxRow(senderRow)
	sender.RandomID = req.RandomID
	// 共享媒体索引(0118):发送者侧 box 按媒体类别建索引(peer=收件人)。
	if err := insertMessageBoxMediaIndexTx(ctx, tx, req.SenderUserID, req.RecipientUserID, int(senderBoxID), req.Date, req.Media, req.Entities); err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	if err := qtx.UpsertOutboxDialog(ctx, sqlcgen.UpsertOutboxDialogParams{
		UserID:         req.SenderUserID,
		PeerType:       string(domain.PeerTypeUser),
		PeerID:         req.RecipientUserID,
		TopMessageID:   int32(sender.ID),
		TopMessageDate: int32(sender.Date),
	}); err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("upsert sender dialog: %w", err)
	}
	if err := appendNewMessageEvent(ctx, qtx, sender); err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	originUserID := req.OriginUserID
	if originUserID == 0 {
		originUserID = req.SenderUserID
	}
	senderExcludeAuthKeyID, senderExcludeSessionID := int64(0), int64(0)
	if originUserID == req.SenderUserID {
		senderExcludeAuthKeyID = authKeyIDToInt64(req.OriginAuthKeyID)
		senderExcludeSessionID = req.OriginSessionID
	}
	if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
		TargetUserID:     req.SenderUserID,
		Pts:              int32(senderPts),
		EventType:        string(domain.UpdateEventNewMessage),
		ExcludeAuthKeyID: senderExcludeAuthKeyID,
		ExcludeSessionID: senderExcludeSessionID,
	}); err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("enqueue sender dispatch: %w", err)
	}

	recipient := domain.Message{}
	if selfMessage {
		recipient = sender
	}
	if deliverRecipient {
		recipientArg := sqlcgen.CreateMessageBoxParams{
			OwnerUserID:      req.RecipientUserID,
			BoxID:            int32(recipientBoxID),
			PrivateMessageID: pm.ID,
			MessageSenderID:  req.SenderUserID,
			PeerType:         string(domain.PeerTypeUser),
			PeerID:           req.SenderUserID,
			FromUserID:       req.SenderUserID,
			MessageDate:      int32(req.Date),
			Outgoing:         false,
			Body:             req.Message,
			TtlPeriod:        int32(ttlPeriod),
			ExpiresAt:        int32(expiresAt),
			EntitiesJson:     entities,
			Pts:              int32(recipientPts),
			MediaJson:        mediaJSON,
			ReplyMarkupJson:  replyMarkupJSON,
			RichMessageJson:  richMessageJSON,
			ViaBotID:         req.ViaBotID,
			GroupedID:        req.GroupedID,
			Effect:           req.Effect,
			MediaUnread:      req.Media.HasUnreadPayload(),
			ReactionUnread:   false,
		}
		applyCreateMessageBoxMetadata(&recipientArg, recipientMeta)
		recipientRow, err := qtx.CreateMessageBox(ctx, recipientArg)
		if err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("create recipient box: %w", err)
		}
		recipient = messageFromBoxRow(recipientRow)
		recipient.RandomID = req.RandomID
		// 共享媒体索引(0118):收件人侧 box 按媒体类别建索引(peer=发送者)。
		if err := insertMessageBoxMediaIndexTx(ctx, tx, req.RecipientUserID, req.SenderUserID, int(recipientBoxID), req.Date, req.Media, req.Entities); err != nil {
			return domain.SendPrivateTextResult{}, err
		}
		if err := qtx.UpsertInboxDialog(ctx, sqlcgen.UpsertInboxDialogParams{
			UserID:         req.RecipientUserID,
			PeerType:       string(domain.PeerTypeUser),
			PeerID:         req.SenderUserID,
			TopMessageID:   int32(recipient.ID),
			TopMessageDate: int32(recipient.Date),
		}); err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("upsert recipient dialog: %w", err)
		}
		if err := appendNewMessageEvent(ctx, qtx, recipient); err != nil {
			return domain.SendPrivateTextResult{}, err
		}
		recipientExcludeAuthKeyID, recipientExcludeSessionID := int64(0), int64(0)
		if originUserID == req.RecipientUserID {
			recipientExcludeAuthKeyID = authKeyIDToInt64(req.OriginAuthKeyID)
			recipientExcludeSessionID = req.OriginSessionID
		}
		if err := enqueueDispatch(ctx, qtx, sqlcgen.EnqueueDispatchParams{
			TargetUserID:     req.RecipientUserID,
			Pts:              int32(recipientPts),
			EventType:        string(domain.UpdateEventNewMessage),
			ExcludeAuthKeyID: recipientExcludeAuthKeyID,
			ExcludeSessionID: recipientExcludeSessionID,
		}); err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("enqueue recipient dispatch: %w", err)
		}
	}

	receiptRecipientBoxID, receiptRecipientPts := recipientBoxID, recipientPts
	if selfMessage {
		receiptRecipientBoxID, receiptRecipientPts = sender.ID, sender.Pts
	}
	senderSnapshot, err := store.EncodePrivateSendSnapshot(sender)
	if err != nil {
		return domain.SendPrivateTextResult{}, err
	}
	tag, err := tx.Exec(ctx, `
UPDATE private_messages
SET sender_box_id = $3,
    sender_pts = $4,
    recipient_box_id = $5,
    recipient_pts = $6,
    sender_snapshot = $7::jsonb
WHERE sender_user_id = $1
  AND id = $2
  AND sender_box_id = 0
  AND sender_pts = 0
  AND sender_snapshot = '{}'::jsonb`, req.SenderUserID, pm.ID, sender.ID, sender.Pts, receiptRecipientBoxID, receiptRecipientPts, senderSnapshot)
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("save private send receipt: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return domain.SendPrivateTextResult{}, fmt.Errorf("save private send receipt: private message %d already has or lost its immutable receipt", pm.ID)
	}
	result := domain.SendPrivateTextResult{
		SenderMessage:    sender,
		RecipientMessage: recipient,
		SenderEvent:      eventFromMessage(sender),
		RecipientEvent:   eventFromMessage(recipient),
	}
	if hooks.after != nil {
		if err := hooks.after(ctx, tx, result); err != nil {
			return domain.SendPrivateTextResult{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("commit send message tx: %w", err)
	}
	committed = true
	return result, nil
}

// LookupPrivateSendReplay reads an existing receipt without permission checks, source/media
// resolution, locks or allocations.  The authenticated app/RPC layer supplies sender identity.
func (s *MessageStore) LookupPrivateSendReplay(ctx context.Context, lookup domain.PrivateSendReplayRequest) (domain.SendPrivateTextResult, bool, error) {
	if lookup.SenderUserID == 0 || lookup.RecipientUserID == 0 || lookup.RandomID == 0 {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("private send replay: invalid scope")
	}
	if err := store.ValidateSendFingerprint(lookup.IdempotencyFingerprint, "private send replay"); err != nil {
		return domain.SendPrivateTextResult{}, false, err
	}
	res, found, err := s.duplicateSendResult(ctx, s.q, domain.SendPrivateTextRequest{
		SenderUserID:           lookup.SenderUserID,
		RecipientUserID:        lookup.RecipientUserID,
		RandomID:               lookup.RandomID,
		IdempotencyFingerprint: lookup.IdempotencyFingerprint,
	}, lookup.IdempotencyFingerprint)
	if err != nil || !found {
		return domain.SendPrivateTextResult{}, found, err
	}
	res.Duplicate = true
	return res, true, nil
}

type boxIDCounterBumper interface {
	BumpBoxIDAtLeast(ctx context.Context, userID int64, floor int) error
}

func (s *MessageStore) bumpBoxIDCountersAfterDuplicate(ctx context.Context, req domain.SendPrivateTextRequest) error {
	bumper, ok := s.boxIDs.(boxIDCounterBumper)
	if !ok {
		return nil
	}
	userIDs := []int64{req.SenderUserID}
	if req.RecipientUserID != 0 && req.RecipientUserID != req.SenderUserID {
		userIDs = append(userIDs, req.RecipientUserID)
	}
	for _, userID := range userIDs {
		maxID, err := s.q.MaxMessageBoxID(ctx, userID)
		if err != nil {
			return fmt.Errorf("max message box id for %d: %w", userID, err)
		}
		if err := bumper.BumpBoxIDAtLeast(ctx, userID, int(maxID)); err != nil {
			return fmt.Errorf("bump box id for %d: %w", userID, err)
		}
	}
	return nil
}

func isMessageBoxDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "message_boxes")
}

func (s *MessageStore) duplicateSendResult(ctx context.Context, q *sqlcgen.Queries, req domain.SendPrivateTextRequest, requestFingerprint []byte) (domain.SendPrivateTextResult, bool, error) {
	pm, err := q.GetPrivateMessageByRandomID(ctx, sqlcgen.GetPrivateMessageByRandomIDParams{
		SenderUserID: req.SenderUserID,
		RandomID:     req.RandomID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.SendPrivateTextResult{}, false, nil
		}
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("get duplicate private message: %w", err)
	}
	if pm.SenderUserID != req.SenderUserID ||
		pm.RecipientUserID != req.RecipientUserID ||
		!store.SamePrivateSendFingerprint(pm.RequestFingerprint, requestFingerprint) {
		return domain.SendPrivateTextResult{}, false, domain.ErrMessageRandomIDDuplicate
	}
	if pm.SenderBoxID <= 0 || pm.SenderPts <= 0 {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf(
			"duplicate private message %d has invalid immutable sender receipt box=%d pts=%d",
			pm.ID, pm.SenderBoxID, pm.SenderPts,
		)
	}
	firstSender, err := store.DecodePrivateSendSnapshot([]byte(pm.SenderSnapshotJson))
	if err != nil {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("decode duplicate private message %d sender snapshot: %w", pm.ID, err)
	}
	if firstSender.ID != int(pm.SenderBoxID) || firstSender.UID != pm.ID || firstSender.RandomID != pm.RandomID ||
		firstSender.OwnerUserID != pm.SenderUserID || firstSender.Pts != int(pm.SenderPts) {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("duplicate private message %d sender snapshot disagrees with immutable receipt", pm.ID)
	}
	sender := firstSender
	currentRow, currentErr := q.GetMessageBoxByPrivateMessage(ctx, sqlcgen.GetMessageBoxByPrivateMessageParams{
		OwnerUserID:      pm.SenderUserID,
		PrivateMessageID: pm.ID,
	})
	if currentErr == nil {
		sender = messageFromGetBoxRow(currentRow)
		sender.RandomID = pm.RandomID
	} else if !errors.Is(currentErr, pgx.ErrNoRows) {
		return domain.SendPrivateTextResult{}, false, fmt.Errorf("get current duplicate private message %d sender box: %w", pm.ID, currentErr)
	}
	var replayDelete *domain.UpdateEvent
	if errors.Is(currentErr, pgx.ErrNoRows) {
		messageIDs, decodeErr := decodeEventMessageIDs(pm.SenderDeleteMessageIdsJson)
		if decodeErr != nil {
			return domain.SendPrivateTextResult{}, false, fmt.Errorf("decode duplicate private message %d delete ids: %w", pm.ID, decodeErr)
		}
		if pm.SenderDeletePts <= 0 || pm.SenderDeletePtsCount <= 0 || len(messageIDs) == 0 {
			return domain.SendPrivateTextResult{}, false, fmt.Errorf("duplicate private message %d sender box is absent without a durable delete receipt", pm.ID)
		}
		event := domain.UpdateEvent{
			UserID:     pm.SenderUserID,
			Type:       domain.UpdateEventDeleteMessages,
			Pts:        int(pm.SenderDeletePts),
			PtsCount:   int(pm.SenderDeletePtsCount),
			Date:       int(pm.SenderDeleteDate),
			MessageIDs: messageIDs,
		}
		replayDelete = &event
	}
	recipient := domain.Message{}
	if req.RecipientUserID == req.SenderUserID {
		recipient = sender
	}
	if req.RecipientUserID != req.SenderUserID && pm.RecipientDelivered {
		if pm.RecipientBoxID <= 0 || pm.RecipientPts <= 0 {
			return domain.SendPrivateTextResult{}, false, fmt.Errorf(
				"duplicate private message %d declares recipient delivery with invalid immutable receipt box=%d pts=%d",
				pm.ID, pm.RecipientBoxID, pm.RecipientPts,
			)
		}
		recipient = domain.Message{
			ID:          int(pm.RecipientBoxID),
			UID:         pm.ID,
			RandomID:    pm.RandomID,
			OwnerUserID: pm.RecipientUserID,
			Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: pm.SenderUserID},
			From:        domain.Peer{Type: domain.PeerTypeUser, ID: pm.SenderUserID},
			Date:        int(pm.MessageDate),
			Out:         false,
			Pts:         int(pm.RecipientPts),
		}
	}
	return domain.SendPrivateTextResult{
		SenderMessage:     sender,
		RecipientMessage:  recipient,
		SenderEvent:       eventFromMessage(firstSender),
		RecipientEvent:    eventFromMessage(recipient),
		ReplayDeleteEvent: replayDelete,
	}, true, nil
}

func (s *MessageStore) resolvePrivateSendReply(ctx context.Context, req domain.SendPrivateTextRequest) (*domain.MessageReply, *domain.MessageReply, error) {
	if req.ReplyTo == nil {
		return nil, nil, nil
	}
	if req.ReplyTo.StoryID > 0 {
		// story 回复（评论）：无源消息可查；story 作者就是会话对端（recipient），双盒同持。
		if req.ReplyTo.StoryID > domain.MaxStoryID {
			return nil, nil, domain.ErrReplyMessageIDInvalid
		}
		reply := &domain.MessageReply{
			StoryID: req.ReplyTo.StoryID,
			Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		}
		return cloneMessageReply(reply), cloneMessageReply(reply), nil
	}
	if req.ReplyTo.MessageID <= 0 || req.ReplyTo.MessageID > domain.MaxMessageBoxID {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	peer := req.ReplyTo.Peer
	if peer.ID == 0 {
		peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID}
	}
	if peer.Type != domain.PeerTypeUser || peer.ID != req.RecipientUserID {
		return nil, nil, domain.ErrReplyMessageIDInvalid
	}
	source, err := s.q.GetMessageBoxForReply(ctx, sqlcgen.GetMessageBoxForReplyParams{
		OwnerUserID: req.SenderUserID,
		PeerType:    string(peer.Type),
		PeerID:      peer.ID,
		BoxID:       int32(req.ReplyTo.MessageID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, domain.ErrReplyMessageIDInvalid
		}
		return nil, nil, fmt.Errorf("get reply message: %w", err)
	}
	senderReply := cloneMessageReply(req.ReplyTo)
	senderReply.MessageID = int(source.BoxID)
	senderReply.Peer = peer
	if req.SenderUserID == req.RecipientUserID {
		return senderReply, cloneMessageReply(senderReply), nil
	}

	recipientRow, err := s.q.GetMessageBoxByPrivateMessage(ctx, sqlcgen.GetMessageBoxByPrivateMessageParams{
		OwnerUserID:      req.RecipientUserID,
		PrivateMessageID: source.PrivateMessageID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return senderReply, nil, nil
		}
		return nil, nil, fmt.Errorf("get recipient reply message: %w", err)
	}
	recipientReply := cloneMessageReply(senderReply)
	recipientReply.MessageID = int(recipientRow.BoxID)
	recipientReply.Peer = domain.Peer{Type: domain.PeerTypeUser, ID: req.SenderUserID}
	return senderReply, recipientReply, nil
}

func appendNewMessageEvent(ctx context.Context, q *sqlcgen.Queries, msg domain.Message) error {
	boxID := int32(msg.ID)
	peerType := string(msg.Peer.Type)
	peerID := msg.Peer.ID
	if err := q.AppendUserUpdateEvent(ctx, sqlcgen.AppendUserUpdateEventParams{
		UserID:          msg.OwnerUserID,
		Pts:             int32(msg.Pts),
		PtsCount:        1,
		Date:            int32(msg.Date),
		EventType:       string(domain.UpdateEventNewMessage),
		EventPeers:      []byte("[]"),
		PeerSettings:    []byte("{}"),
		MessageIds:      []byte("[]"),
		DialogFilter:    []byte("{}"),
		FilterOrder:     []byte("[]"),
		FolderPeers:     []byte("[]"),
		StoryPayload:    []byte("{}"),
		ReactionPayload: []byte("{}"),
		MessageBoxID:    &boxID,
		PeerType:        &peerType,
		PeerID:          &peerID,
	}); err != nil {
		return fmt.Errorf("append new message event: %w", err)
	}
	return nil
}

// lockUsersForUpdate 在事务开始处用事务级 advisory lock 串行化所有涉及指定用户的并发写事务。
// advisory lock 与行锁处于独立锁空间，且按 user_id 升序获取，因此：① 不会与后续 dialog /
// watermark / box 行锁交叉成跨类型死锁；② 任意两个共享某用户的写事务（send/read/edit/delete 对
// 收发双方的并发操作）被完全串行化，从根上消除它们在 watermark 与 dialog 行上因加锁顺序相反
// 导致的 AB-BA 死锁——既包含本次 watermark 优化新引入的（user_update_watermarks FOR UPDATE），
// 也包含 dialog upsert 既有的反向行锁。advisory xact lock 在事务结束自动释放；同对用户本就竞争
// 这些行（天然串行），不额外降并发，不同用户集合的事务仍并行。**必须在任何行锁之前调用。**
func lockUsersForUpdate(ctx context.Context, tx pgx.Tx, userIDs ...int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	unique := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	for _, id := range unique {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", id); err != nil {
			return fmt.Errorf("advisory lock user %d: %w", id, err)
		}
	}
	return nil
}

func applyCreatePrivateMessageMetadata(arg *sqlcgen.CreatePrivateMessageParams, meta messageMetadataParams) {
	arg.Silent = meta.Silent
	arg.Noforwards = meta.Noforwards
	arg.ReplyToMsgID = meta.ReplyToMsgID
	arg.ReplyToPeerType = meta.ReplyToPeerType
	arg.ReplyToPeerID = meta.ReplyToPeerID
	arg.ReplyToTopID = meta.ReplyToTopID
	arg.ReplyToStoryID = meta.ReplyToStoryID
	arg.QuoteText = meta.QuoteText
	arg.QuoteEntitiesJson = meta.QuoteEntitiesJSON
	arg.QuoteOffset = meta.QuoteOffset
	arg.FwdFromPeerType = meta.FwdFromPeerType
	arg.FwdFromPeerID = meta.FwdFromPeerID
	arg.FwdFromName = meta.FwdFromName
	arg.FwdDate = meta.FwdDate
}

func applyCreateMessageBoxMetadata(arg *sqlcgen.CreateMessageBoxParams, meta messageMetadataParams) {
	arg.Silent = meta.Silent
	arg.Noforwards = meta.Noforwards
	arg.ReplyToMsgID = meta.ReplyToMsgID
	arg.ReplyToPeerType = meta.ReplyToPeerType
	arg.ReplyToPeerID = meta.ReplyToPeerID
	arg.ReplyToTopID = meta.ReplyToTopID
	arg.ReplyToStoryID = meta.ReplyToStoryID
	arg.QuoteText = meta.QuoteText
	arg.QuoteEntitiesJson = meta.QuoteEntitiesJSON
	arg.QuoteOffset = meta.QuoteOffset
	arg.FwdFromPeerType = meta.FwdFromPeerType
	arg.FwdFromPeerID = meta.FwdFromPeerID
	arg.FwdFromName = meta.FwdFromName
	arg.FwdDate = meta.FwdDate
	arg.FwdSavedFromPeerType = meta.FwdSavedFromPeerType
	arg.FwdSavedFromPeerID = meta.FwdSavedFromPeerID
	arg.FwdSavedFromMsgID = meta.FwdSavedFromMsgID
	arg.SavedPeerType = meta.SavedPeerType
	arg.SavedPeerID = meta.SavedPeerID
}

func privateHistoryTTLPeriod(ctx context.Context, db sqlcgen.DBTX, ownerUserID, peerUserID int64) (int, error) {
	if ownerUserID == 0 || peerUserID == 0 {
		return 0, nil
	}
	var period int
	err := db.QueryRow(ctx, `
SELECT COALESCE(NULLIF(d.ttl_period, 0), u.default_history_ttl_period, 0)::int
FROM users u
LEFT JOIN dialogs d
  ON d.user_id = u.id
 AND d.peer_type = 'user'
 AND d.peer_id = $2
WHERE u.id = $1
`, ownerUserID, peerUserID).Scan(&period)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if period < 0 {
		return 0, nil
	}
	return period, nil
}

func messageFromCreateRow(row sqlcgen.CreateMessageRow) (domain.Message, error) {
	entities, err := decodeMessageEntities(row.EntitiesJson)
	if err != nil {
		return domain.Message{}, fmt.Errorf("decode message entities: %w", err)
	}
	return domain.Message{
		ID:          int(row.BoxID),
		UID:         row.PrivateMessageID,
		OwnerUserID: row.OwnerUserID,
		Peer:        domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: row.FromUserID},
		Date:        int(row.MessageDate),
		EditDate:    int(row.EditDate),
		HideEdited:  row.HideEdited,
		Out:         row.Outgoing,
		Body:        row.Body,
		Entities:    entities,
		Pts:         int(row.Pts),
	}, nil
}
