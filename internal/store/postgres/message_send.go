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
	if _, err := s.db.Exec(ctx, `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, country_code, verified, support, about, is_bot, bot_info_version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO UPDATE SET
  access_hash = EXCLUDED.access_hash,
  phone = EXCLUDED.phone,
  first_name = EXCLUDED.first_name,
  last_name = EXCLUDED.last_name,
  username = EXCLUDED.username,
  country_code = EXCLUDED.country_code,
  verified = EXCLUDED.verified,
  support = EXCLUDED.support,
  about = EXCLUDED.about,
  is_bot = EXCLUDED.is_bot,
  bot_info_version = EXCLUDED.bot_info_version,
  updated_at = now()
`, u.ID, u.AccessHash, u.Phone, u.FirstName, u.LastName, u.Username, u.CountryCode, u.Verified, u.Support, u.About, u.Bot, u.BotInfoVersion); err != nil {
		return fmt.Errorf("ensure official system user: %w", err)
	}
	return nil
}

func (s *MessageStore) SendPrivateText(ctx context.Context, req domain.SendPrivateTextRequest) (res domain.SendPrivateTextResult, err error) {
	for attempt := 0; attempt < 2; attempt++ {
		res, err = s.sendPrivateTextOnce(ctx, req)
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

func (s *MessageStore) sendPrivateTextOnce(ctx context.Context, req domain.SendPrivateTextRequest) (res domain.SendPrivateTextResult, err error) {
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
	mediaJSON, err := encodeMessageMedia(req.Media)
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

	if err := s.ensureBoxIDCountersBeforeSend(ctx, req, deliverRecipient); err != nil {
		return domain.SendPrivateTextResult{}, err
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
		SenderUserID:    req.SenderUserID,
		RecipientUserID: req.RecipientUserID,
		RandomID:        req.RandomID,
		MessageDate:     int32(req.Date),
		Body:            req.Message,
		TtlPeriod:       int32(ttlPeriod),
		ExpiresAt:       int32(expiresAt),
		EntitiesJson:    entities,
		MediaJson:       mediaJSON,
		ReplyMarkupJson: replyMarkupJSON,
		RichMessageJson: richMessageJSON,
		ViaBotID:        req.ViaBotID,
		GroupedID:       req.GroupedID,
		Effect:          req.Effect,
	}
	applyCreatePrivateMessageMetadata(&privateArg, senderMeta)
	pm, err := qtx.CreatePrivateMessage(ctx, privateArg)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 幂等重复：返回原消息盒；此时还没有分配 pts，重复发送不应制造额外事件。
			dup, dupErr := s.duplicateSendResult(ctx, req.SenderUserID, req.RecipientUserID, req.RandomID)
			if dupErr != nil {
				return domain.SendPrivateTextResult{}, dupErr
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
	if err := qtx.EnqueueDispatch(ctx, sqlcgen.EnqueueDispatchParams{
		TargetUserID:     req.SenderUserID,
		Pts:              int32(senderPts),
		EventType:        string(domain.UpdateEventNewMessage),
		ExcludeAuthKeyID: authKeyIDToInt64(req.OriginAuthKeyID),
		ExcludeSessionID: req.OriginSessionID,
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
		if err := qtx.EnqueueDispatch(ctx, sqlcgen.EnqueueDispatchParams{
			TargetUserID:     req.RecipientUserID,
			Pts:              int32(recipientPts),
			EventType:        string(domain.UpdateEventNewMessage),
			ExcludeAuthKeyID: 0,
			ExcludeSessionID: 0,
		}); err != nil {
			return domain.SendPrivateTextResult{}, fmt.Errorf("enqueue recipient dispatch: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("commit send message tx: %w", err)
	}
	committed = true
	return domain.SendPrivateTextResult{
		SenderMessage:    sender,
		RecipientMessage: recipient,
		SenderEvent:      eventFromMessage(sender),
		RecipientEvent:   eventFromMessage(recipient),
	}, nil
}

type boxIDCounterBumper interface {
	BumpBoxIDAtLeast(ctx context.Context, userID int64, floor int) error
}

type boxIDCounterEnsurer interface {
	EnsureBoxIDCounter(ctx context.Context, userID int64) error
}

func (s *MessageStore) ensureBoxIDCountersBeforeSend(ctx context.Context, req domain.SendPrivateTextRequest, deliverRecipient bool) error {
	ensurer, ok := s.boxIDs.(boxIDCounterEnsurer)
	if !ok {
		return nil
	}
	userIDs := []int64{req.SenderUserID}
	if deliverRecipient && req.RecipientUserID != req.SenderUserID {
		userIDs = append(userIDs, req.RecipientUserID)
	}
	for _, userID := range userIDs {
		if err := ensurer.EnsureBoxIDCounter(ctx, userID); err != nil {
			return fmt.Errorf("ensure box id counter for user %d: %w", userID, err)
		}
	}
	return nil
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

func (s *MessageStore) duplicateSendResult(ctx context.Context, senderUserID, recipientUserID, randomID int64) (domain.SendPrivateTextResult, error) {
	pm, err := s.q.GetPrivateMessageByRandomID(ctx, sqlcgen.GetPrivateMessageByRandomIDParams{
		SenderUserID: senderUserID,
		RandomID:     randomID,
	})
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("get duplicate private message: %w", err)
	}
	senderRow, err := s.q.GetMessageBoxByPrivateMessage(ctx, sqlcgen.GetMessageBoxByPrivateMessageParams{
		OwnerUserID:      senderUserID,
		PrivateMessageID: pm.ID,
	})
	if err != nil {
		return domain.SendPrivateTextResult{}, fmt.Errorf("get duplicate sender box: %w", err)
	}
	sender := messageFromGetBoxRow(senderRow)
	recipient := domain.Message{}
	if recipientUserID == senderUserID {
		recipient = sender
	}
	if recipientUserID != senderUserID {
		recipientRow, err := s.q.GetMessageBoxByPrivateMessage(ctx, sqlcgen.GetMessageBoxByPrivateMessageParams{
			OwnerUserID:      recipientUserID,
			PrivateMessageID: pm.ID,
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.SendPrivateTextResult{
					SenderMessage:  sender,
					SenderEvent:    eventFromMessage(sender),
					RecipientEvent: domain.UpdateEvent{},
				}, nil
			}
			return domain.SendPrivateTextResult{}, fmt.Errorf("get duplicate recipient box: %w", err)
		}
		recipient = messageFromGetBoxRow(recipientRow)
	}
	return domain.SendPrivateTextResult{
		SenderMessage:    sender,
		RecipientMessage: recipient,
		SenderEvent:      eventFromMessage(sender),
		RecipientEvent:   eventFromMessage(recipient),
	}, nil
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
		Out:         row.Outgoing,
		Body:        row.Body,
		Entities:    entities,
		Pts:         int(row.Pts),
	}, nil
}
