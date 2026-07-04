package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// UpdateEventStore 用 PostgreSQL 实现 store.UpdateEventStore。
type UpdateEventStore struct {
	db  sqlcgen.DBTX
	q   *sqlcgen.Queries
	log *zap.Logger
}

type UpdateEventStoreOption func(*UpdateEventStore)

// WithUpdateEventLogger 注入 durable update log 的日志器。
func WithUpdateEventLogger(log *zap.Logger) UpdateEventStoreOption {
	return func(s *UpdateEventStore) {
		s.log = log
	}
}

// NewUpdateEventStore 基于 pgx 连接池（或事务）创建 UpdateEventStore。
func NewUpdateEventStore(db sqlcgen.DBTX, opts ...UpdateEventStoreOption) *UpdateEventStore {
	s := &UpdateEventStore{db: db, q: sqlcgen.New(db)}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	return s
}

func (s *UpdateEventStore) Append(ctx context.Context, userID int64, event domain.UpdateEvent) error {
	_, err := s.append(ctx, userID, event, false, [8]byte{}, 0, false)
	return err
}

func (s *UpdateEventStore) AppendAllocated(ctx context.Context, userID int64, event domain.UpdateEvent) (domain.UpdateEvent, error) {
	return s.append(ctx, userID, event, false, [8]byte{}, 0, true)
}

// AppendAllocatedWithDispatch 在同一个 PG 事务里完成 pts 分配、durable event 写入与
// dispatch outbox 入队，返回带最终 pts 的事件。
func (s *UpdateEventStore) AppendAllocatedWithDispatch(ctx context.Context, userID int64, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, error) {
	return s.append(ctx, userID, event, true, excludeAuthKeyID, excludeSessionID, true)
}

func (s *UpdateEventStore) append(ctx context.Context, userID int64, event domain.UpdateEvent, dispatch bool, excludeAuthKeyID [8]byte, excludeSessionID int64, allocate bool) (domain.UpdateEvent, error) {
	if event.PtsCount <= 0 {
		event.PtsCount = 1
	}
	beginner, ok := s.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		event, err := s.appendInTx(ctx, s.db, s.q, userID, event, dispatch, excludeAuthKeyID, excludeSessionID, allocate)
		if err != nil {
			s.logAppendFailure(ctx, userID, event, dispatch, "append", err)
			return domain.UpdateEvent{}, err
		}
		s.logAppendSuccess(userID, event, dispatch, excludeSessionID)
		return event, nil
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		s.log.Warn("update_event_append_failed",
			zap.String("scope", "user"),
			zap.Int64("user_id", userID),
			zap.Int("pts", event.Pts),
			zap.Int("pts_count", event.PtsCount),
			zap.String("event_type", string(event.Type)),
			zap.String("phase", "begin"),
			zap.Error(err),
			zap.Error(ctx.Err()),
		)
		return domain.UpdateEvent{}, fmt.Errorf("begin append update event: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	qtx := sqlcgen.New(tx)
	event, err = s.appendInTx(ctx, tx, qtx, userID, event, dispatch, excludeAuthKeyID, excludeSessionID, allocate)
	if err != nil {
		s.logAppendFailure(ctx, userID, event, dispatch, "append", err)
		return domain.UpdateEvent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		s.logAppendFailure(ctx, userID, event, dispatch, "commit", err)
		return domain.UpdateEvent{}, fmt.Errorf("commit append update event: %w", err)
	}
	committed = true
	s.logAppendSuccess(userID, event, dispatch, excludeSessionID)
	return event, nil
}

func (s *UpdateEventStore) appendInTx(ctx context.Context, db sqlcgen.DBTX, q *sqlcgen.Queries, userID int64, event domain.UpdateEvent, dispatch bool, excludeAuthKeyID [8]byte, excludeSessionID int64, allocate bool) (domain.UpdateEvent, error) {
	var err error
	if allocate || event.Pts == 0 {
		event.Pts, err = reserveUserPts(ctx, db, userID, event.PtsCount)
		if err != nil {
			return domain.UpdateEvent{}, fmt.Errorf("reserve user pts: %w", err)
		}
	} else if err := advanceUserPtsTo(ctx, db, userID, event.Pts, event.PtsCount); err != nil {
		return domain.UpdateEvent{}, err
	}
	event.UserID = userID
	if err := appendUserUpdateEvent(ctx, db, q, userID, event); err != nil {
		return domain.UpdateEvent{}, fmt.Errorf("append update event: %w", err)
	}
	if dispatch {
		if err := q.EnqueueDispatch(ctx, sqlcgen.EnqueueDispatchParams{
			TargetUserID:     userID,
			Pts:              int32(event.Pts),
			EventType:        string(event.Type),
			ExcludeAuthKeyID: authKeyIDToInt64(excludeAuthKeyID),
			ExcludeSessionID: excludeSessionID,
		}); err != nil {
			return domain.UpdateEvent{}, fmt.Errorf("enqueue dispatch: %w", err)
		}
	}
	return event, nil
}

func (s *UpdateEventStore) logAppendFailure(ctx context.Context, userID int64, event domain.UpdateEvent, dispatch bool, phase string, err error) {
	s.log.Warn("update_event_append_failed",
		zap.String("scope", "user"),
		zap.Int64("user_id", userID),
		zap.Int("pts", event.Pts),
		zap.Int("pts_count", event.PtsCount),
		zap.String("event_type", string(event.Type)),
		zap.String("phase", phase),
		zap.Bool("dispatch", dispatch),
		zap.Error(err),
		zap.Error(ctx.Err()),
	)
}

func (s *UpdateEventStore) logAppendSuccess(userID int64, event domain.UpdateEvent, dispatch bool, excludeSessionID int64) {
	s.log.Debug("update_event_appended",
		zap.String("scope", "user"),
		zap.Int64("user_id", userID),
		zap.Int("pts", event.Pts),
		zap.Int("pts_count", event.PtsCount),
		zap.String("event_type", string(event.Type)),
		zap.Bool("dispatch", dispatch),
		zap.Int64("exclude_session_id", excludeSessionID),
	)
}

func appendUserUpdateEvent(ctx context.Context, db sqlcgen.DBTX, q *sqlcgen.Queries, userID int64, event domain.UpdateEvent) error {
	var messageID *int32
	if event.Message.ID != 0 {
		id := int32(event.Message.ID)
		messageID = &id
	}
	var peerType *string
	var peerID *int64
	peer := event.Peer
	if peer.ID == 0 {
		peer = event.Message.Peer
	}
	if peer.ID != 0 {
		t := string(peer.Type)
		id := peer.ID
		peerType = &t
		peerID = &id
	}
	peers, err := encodeEventPeers(event.Peers)
	if err != nil {
		return err
	}
	settings, err := encodePeerSettings(event.Settings)
	if err != nil {
		return err
	}
	messageIDs, err := encodeEventMessageIDs(event.MessageIDs)
	if err != nil {
		return err
	}
	dialogFilter, err := encodeEventDialogFilter(event.DialogFilter)
	if err != nil {
		return err
	}
	filterOrder, err := encodeEventFilterOrder(event.FilterOrder)
	if err != nil {
		return err
	}
	folderPeers, err := encodeEventFolderPeers(event.FolderPeers)
	if err != nil {
		return err
	}
	storyPayload, err := encodeEventStory(event.Story)
	if err != nil {
		return err
	}
	reactionPayload, err := encodeEventReaction(event.Reaction)
	if err != nil {
		return err
	}
	if err := q.AppendUserUpdateEvent(ctx, sqlcgen.AppendUserUpdateEventParams{
		UserID:           userID,
		Pts:              int32(event.Pts),
		PtsCount:         int32(event.PtsCount),
		Date:             int32(event.Date),
		EventType:        string(event.Type),
		EventBool:        event.Bool,
		EventPeers:       peers,
		PeerSettings:     settings,
		MessageIds:       messageIDs,
		DialogFilter:     dialogFilter,
		FilterOrder:      filterOrder,
		FolderPeers:      folderPeers,
		StoryPayload:     storyPayload,
		ReactionPayload:  reactionPayload,
		MaxID:            pgInt32NonNegative(event.MaxID),
		StillUnreadCount: int32(event.StillUnreadCount),
		ChannelPts:       int32(event.ChannelPts),
		FilterID:         pgInt32NonNegative(event.FilterID),
		TagsEnabled:      event.TagsEnabled,
		FolderID:         pgInt32NonNegative(event.FolderID),
		MessageBoxID:     messageID,
		PeerType:         peerType,
		PeerID:           peerID,
	}); err != nil {
		return err
	}
	if err := appendQuickReplyPayload(ctx, db, userID, event); err != nil {
		return err
	}
	return nil
}

func appendQuickReplyPayload(ctx context.Context, db sqlcgen.DBTX, userID int64, event domain.UpdateEvent) error {
	switch event.Type {
	case domain.UpdateEventQuickReplies,
		domain.UpdateEventNewQuickReply,
		domain.UpdateEventDeleteQuickReply,
		domain.UpdateEventQuickReplyMessage,
		domain.UpdateEventDeleteQuickReplyMessages:
	default:
		return nil
	}
	replies, err := json.Marshal(event.QuickReplies)
	if err != nil {
		return fmt.Errorf("encode quick replies: %w", err)
	}
	message, err := json.Marshal(event.QuickReplyMessage)
	if err != nil {
		return fmt.Errorf("encode quick reply message: %w", err)
	}
	if _, err := db.Exec(ctx, `
UPDATE user_update_events
SET quick_replies = $3::jsonb,
    quick_reply_message = $4::jsonb
WHERE user_id = $1
  AND pts = $2`, userID, event.Pts, string(replies), string(message)); err != nil {
		return fmt.Errorf("save quick reply update payload: %w", err)
	}
	return nil
}

func (s *UpdateEventStore) hydrateQuickReplyEvent(ctx context.Context, event *domain.UpdateEvent) error {
	if event == nil {
		return nil
	}
	switch event.Type {
	case domain.UpdateEventQuickReplies,
		domain.UpdateEventNewQuickReply,
		domain.UpdateEventQuickReplyMessage,
		domain.UpdateEventDeleteQuickReply:
	default:
		return nil
	}
	var repliesJSON, messageJSON string
	if err := s.db.QueryRow(ctx, `
SELECT
  COALESCE(quick_replies::text, '[]')::text,
  COALESCE(quick_reply_message::text, '{}')::text
FROM user_update_events
WHERE user_id = $1
  AND pts = $2`, event.UserID, event.Pts).Scan(&repliesJSON, &messageJSON); err != nil {
		return fmt.Errorf("get quick reply update payload: %w", err)
	}
	if err := json.Unmarshal([]byte(repliesJSON), &event.QuickReplies); err != nil {
		return fmt.Errorf("decode quick replies: %w", err)
	}
	if err := json.Unmarshal([]byte(messageJSON), &event.QuickReplyMessage); err != nil {
		return fmt.Errorf("decode quick reply message: %w", err)
	}
	if event.Type == domain.UpdateEventNewQuickReply && event.QuickReply.ID == 0 {
		for _, item := range event.QuickReplies {
			if item.ID == event.MaxID {
				event.QuickReply = item
				break
			}
		}
	}
	return nil
}

func (s *UpdateEventStore) ListAfter(ctx context.Context, userID int64, pts, limit int) ([]domain.UpdateEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.q.ListUserUpdateEventsAfter(ctx, sqlcgen.ListUserUpdateEventsAfterParams{
		UserID:     userID,
		Pts:        int32(pts),
		LimitCount: int32(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("list update events: %w", err)
	}
	out := make([]domain.UpdateEvent, 0, len(rows))
	for _, row := range rows {
		entities, err := decodeMessageEntities(row.MessageEntitiesJson)
		if err != nil {
			return nil, fmt.Errorf("decode message entities: %w", err)
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
			return nil, fmt.Errorf("decode message metadata: %w", err)
		}
		peers, err := decodeEventPeers(row.EventPeersJson)
		if err != nil {
			return nil, fmt.Errorf("decode event peers: %w", err)
		}
		settings, err := decodePeerSettings(row.PeerSettingsJson)
		if err != nil {
			return nil, fmt.Errorf("decode peer settings: %w", err)
		}
		messageIDs, err := decodeEventMessageIDs(row.MessageIdsJson)
		if err != nil {
			return nil, fmt.Errorf("decode message ids: %w", err)
		}
		dialogFilter, err := decodeEventDialogFilter(row.DialogFilterJson)
		if err != nil {
			return nil, fmt.Errorf("decode dialog filter: %w", err)
		}
		filterOrder, err := decodeEventFilterOrder(row.FilterOrderJson)
		if err != nil {
			return nil, fmt.Errorf("decode filter order: %w", err)
		}
		folderPeers, err := decodeEventFolderPeers(row.FolderPeersJson)
		if err != nil {
			return nil, fmt.Errorf("decode folder peers: %w", err)
		}
		story, err := decodeEventStory(row.StoryPayloadJson)
		if err != nil {
			return nil, fmt.Errorf("decode story payload: %w", err)
		}
		reaction, err := decodeEventReaction(row.ReactionPayloadJson)
		if err != nil {
			return nil, fmt.Errorf("decode reaction payload: %w", err)
		}
		media, err := decodeMessageMedia(row.MediaJson)
		if err != nil {
			return nil, fmt.Errorf("decode message media: %w", err)
		}
		markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
		if err != nil {
			return nil, fmt.Errorf("decode message reply markup: %w", err)
		}
		rich, err := decodeRichMessage(row.RichMessageJson)
		if err != nil {
			return nil, fmt.Errorf("decode message rich message: %w", err)
		}
		event := domain.UpdateEvent{
			UserID:           row.UserID,
			Type:             domain.UpdateEventType(row.EventType),
			Pts:              int(row.Pts),
			PtsCount:         int(row.PtsCount),
			Date:             int(row.Date),
			Peer:             domain.Peer{Type: domain.PeerType(row.EventPeerType), ID: row.EventPeerID},
			Story:            story,
			Peers:            peers,
			Bool:             row.EventBool,
			Settings:         settings,
			MessageIDs:       messageIDs,
			MaxID:            int(row.MaxID),
			StillUnreadCount: int(row.StillUnreadCount),
			ChannelPts:       int(row.ChannelPts),
			FilterID:         int(row.FilterID),
			DialogFilter:     dialogFilter,
			FilterOrder:      filterOrder,
			FolderPeers:      folderPeers,
			TagsEnabled:      row.TagsEnabled,
			FolderID:         int(row.FolderID),
			Reaction:         reaction,
			Message: domain.Message{
				ID:             int(row.MessageID),
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
				TTLPeriod:      int(row.TtlPeriod),
				ExpiresAt:      int(row.ExpiresAt),
			},
			Users: usersFromUpdateEventRow(row),
		}
		if err := s.hydrateQuickReplyEvent(ctx, &event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

func (s *UpdateEventStore) FindNewMessageEvent(ctx context.Context, userID int64, messageBoxID int) (domain.UpdateEvent, bool, error) {
	if userID == 0 || messageBoxID <= 0 {
		return domain.UpdateEvent{}, false, nil
	}
	var pts int
	if err := s.db.QueryRow(ctx, `
SELECT pts
FROM user_update_events
WHERE user_id = $1
  AND event_type = $2
  AND message_box_id = $3
ORDER BY pts ASC
LIMIT 1`, userID, string(domain.UpdateEventNewMessage), messageBoxID).Scan(&pts); err != nil {
		if err == pgx.ErrNoRows {
			return domain.UpdateEvent{}, false, nil
		}
		return domain.UpdateEvent{}, false, fmt.Errorf("find new message update event: %w", err)
	}
	events, err := s.ListAfter(ctx, userID, pts-1, 1)
	if err != nil {
		return domain.UpdateEvent{}, false, err
	}
	if len(events) == 0 || events[0].Pts != pts {
		return domain.UpdateEvent{}, false, fmt.Errorf("hydrate new message update event: expected pts %d", pts)
	}
	return events[0], true, nil
}

// MaxContiguousPts 见 store.UpdateEventStore 接口说明。PG 写路径保证水位与
// durable event 同事务提交；缺行代表该账号还没有 update。
func (s *UpdateEventStore) MaxContiguousPts(ctx context.Context, userID int64) (int, error) {
	pts, err := s.q.GetUserUpdateWatermark(ctx, userID)
	if err == nil {
		return int(pts), nil
	}
	if err != pgx.ErrNoRows {
		return 0, fmt.Errorf("get update watermark: %w", err)
	}
	return 0, nil
}

// BatchByCursor 按 (user_id, pts) 一次性批量取多条账号事件，供 outbox worker 取代逐条 ListAfter。
// 返回顺序不保证与 cursors 一致，调用方按 (UserID,Pts) 自行索引。
func (s *UpdateEventStore) BatchByCursor(ctx context.Context, cursors []store.EventCursor) ([]domain.UpdateEvent, error) {
	if len(cursors) == 0 {
		return nil, nil
	}
	userIDs := make([]int64, len(cursors))
	ptsList := make([]int32, len(cursors))
	for i, c := range cursors {
		userIDs[i] = c.UserID
		ptsList[i] = int32(c.Pts)
	}
	rows, err := s.q.BatchListDispatchEvents(ctx, sqlcgen.BatchListDispatchEventsParams{
		UserIds: userIDs,
		PtsList: ptsList,
	})
	if err != nil {
		return nil, fmt.Errorf("batch list dispatch events: %w", err)
	}
	out := make([]domain.UpdateEvent, 0, len(rows))
	for _, row := range rows {
		entities, err := decodeMessageEntities(row.MessageEntitiesJson)
		if err != nil {
			return nil, fmt.Errorf("decode message entities: %w", err)
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
			return nil, fmt.Errorf("decode message metadata: %w", err)
		}
		peers, err := decodeEventPeers(row.EventPeersJson)
		if err != nil {
			return nil, fmt.Errorf("decode event peers: %w", err)
		}
		settings, err := decodePeerSettings(row.PeerSettingsJson)
		if err != nil {
			return nil, fmt.Errorf("decode peer settings: %w", err)
		}
		messageIDs, err := decodeEventMessageIDs(row.MessageIdsJson)
		if err != nil {
			return nil, fmt.Errorf("decode message ids: %w", err)
		}
		dialogFilter, err := decodeEventDialogFilter(row.DialogFilterJson)
		if err != nil {
			return nil, fmt.Errorf("decode dialog filter: %w", err)
		}
		filterOrder, err := decodeEventFilterOrder(row.FilterOrderJson)
		if err != nil {
			return nil, fmt.Errorf("decode filter order: %w", err)
		}
		folderPeers, err := decodeEventFolderPeers(row.FolderPeersJson)
		if err != nil {
			return nil, fmt.Errorf("decode folder peers: %w", err)
		}
		story, err := decodeEventStory(row.StoryPayloadJson)
		if err != nil {
			return nil, fmt.Errorf("decode story payload: %w", err)
		}
		reaction, err := decodeEventReaction(row.ReactionPayloadJson)
		if err != nil {
			return nil, fmt.Errorf("decode reaction payload: %w", err)
		}
		media, err := decodeMessageMedia(row.MediaJson)
		if err != nil {
			return nil, fmt.Errorf("decode message media: %w", err)
		}
		markup, err := decodeReplyMarkup(row.ReplyMarkupJson)
		if err != nil {
			return nil, fmt.Errorf("decode message reply markup: %w", err)
		}
		rich, err := decodeRichMessage(row.RichMessageJson)
		if err != nil {
			return nil, fmt.Errorf("decode message rich message: %w", err)
		}
		event := domain.UpdateEvent{
			UserID:           row.UserID,
			Type:             domain.UpdateEventType(row.EventType),
			Pts:              int(row.Pts),
			PtsCount:         int(row.PtsCount),
			Date:             int(row.Date),
			Peer:             domain.Peer{Type: domain.PeerType(row.EventPeerType), ID: row.EventPeerID},
			Story:            story,
			Peers:            peers,
			Bool:             row.EventBool,
			Settings:         settings,
			MessageIDs:       messageIDs,
			MaxID:            int(row.MaxID),
			StillUnreadCount: int(row.StillUnreadCount),
			ChannelPts:       int(row.ChannelPts),
			FilterID:         int(row.FilterID),
			DialogFilter:     dialogFilter,
			FilterOrder:      filterOrder,
			FolderPeers:      folderPeers,
			TagsEnabled:      row.TagsEnabled,
			FolderID:         int(row.FolderID),
			Reaction:         reaction,
			Message: domain.Message{
				ID:             int(row.MessageID),
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
				TTLPeriod:      int(row.TtlPeriod),
				ExpiresAt:      int(row.ExpiresAt),
			},
			Users: usersFromBatchDispatchRow(row),
		}
		if err := s.hydrateQuickReplyEvent(ctx, &event); err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}

func usersFromUpdateEventRow(row sqlcgen.ListUserUpdateEventsAfterRow) []domain.User {
	return mergeEventUsers(
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
		},
		domain.User{
			ID:                    row.FwdUserID,
			AccessHash:            row.FwdUserAccessHash,
			Phone:                 row.FwdUserPhone,
			FirstName:             row.FwdUserFirstName,
			LastName:              row.FwdUserLastName,
			Username:              row.FwdUserUsername,
			CountryCode:           row.FwdUserCountryCode,
			Verified:              row.FwdUserVerified,
			Support:               row.FwdUserSupport,
			Bot:                   row.FwdUserIsBot,
			BotInfoVersion:        int(row.FwdUserBotInfoVersion),
			PremiumUntil:          int(row.FwdUserPremiumUntil),
			EmojiStatusDocumentID: row.FwdUserEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.FwdUserEmojiStatusUntil),
		},
		domain.User{
			ID:                    row.ReplyUserID,
			AccessHash:            row.ReplyUserAccessHash,
			Phone:                 row.ReplyUserPhone,
			FirstName:             row.ReplyUserFirstName,
			LastName:              row.ReplyUserLastName,
			Username:              row.ReplyUserUsername,
			CountryCode:           row.ReplyUserCountryCode,
			Verified:              row.ReplyUserVerified,
			Support:               row.ReplyUserSupport,
			Bot:                   row.ReplyUserIsBot,
			BotInfoVersion:        int(row.ReplyUserBotInfoVersion),
			PremiumUntil:          int(row.ReplyUserPremiumUntil),
			EmojiStatusDocumentID: row.ReplyUserEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.ReplyUserEmojiStatusUntil),
		},
	)
}

// usersFromBatchDispatchRow 与 usersFromUpdateEventRow 等价，只是行类型为 BatchListDispatchEventsRow
// （两条查询列完全一致；改一处列时务必同步另一处）。
func usersFromBatchDispatchRow(row sqlcgen.BatchListDispatchEventsRow) []domain.User {
	return mergeEventUsers(
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
		},
		domain.User{
			ID:                    row.FwdUserID,
			AccessHash:            row.FwdUserAccessHash,
			Phone:                 row.FwdUserPhone,
			FirstName:             row.FwdUserFirstName,
			LastName:              row.FwdUserLastName,
			Username:              row.FwdUserUsername,
			CountryCode:           row.FwdUserCountryCode,
			Verified:              row.FwdUserVerified,
			Support:               row.FwdUserSupport,
			Bot:                   row.FwdUserIsBot,
			BotInfoVersion:        int(row.FwdUserBotInfoVersion),
			PremiumUntil:          int(row.FwdUserPremiumUntil),
			EmojiStatusDocumentID: row.FwdUserEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.FwdUserEmojiStatusUntil),
		},
		domain.User{
			ID:                    row.ReplyUserID,
			AccessHash:            row.ReplyUserAccessHash,
			Phone:                 row.ReplyUserPhone,
			FirstName:             row.ReplyUserFirstName,
			LastName:              row.ReplyUserLastName,
			Username:              row.ReplyUserUsername,
			CountryCode:           row.ReplyUserCountryCode,
			Verified:              row.ReplyUserVerified,
			Support:               row.ReplyUserSupport,
			Bot:                   row.ReplyUserIsBot,
			BotInfoVersion:        int(row.ReplyUserBotInfoVersion),
			PremiumUntil:          int(row.ReplyUserPremiumUntil),
			EmojiStatusDocumentID: row.ReplyUserEmojiStatusDocumentID,
			EmojiStatusUntil:      int(row.ReplyUserEmojiStatusUntil),
		},
	)
}

// mergeEventUsers 合并事件依赖用户，跳过 ID=0 并按 ID 去重。
func mergeEventUsers(items ...domain.User) []domain.User {
	users := make([]domain.User, 0, len(items))
	add := func(u domain.User) {
		if u.ID == 0 {
			return
		}
		for _, existing := range users {
			if existing.ID == u.ID {
				return
			}
		}
		users = append(users, u)
	}
	for _, item := range items {
		add(item)
	}
	return users
}

type eventPeerJSON struct {
	Type string `json:"type"`
	ID   int64  `json:"id"`
}

func encodeEventPeers(peers []domain.Peer) ([]byte, error) {
	if len(peers) == 0 {
		return []byte("[]"), nil
	}
	wire := make([]eventPeerJSON, 0, len(peers))
	for _, peer := range peers {
		if peer.ID == 0 {
			continue
		}
		wire = append(wire, eventPeerJSON{Type: string(peer.Type), ID: peer.ID})
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal event peers: %w", err)
	}
	return raw, nil
}

func decodeEventPeers(raw string) ([]domain.Peer, error) {
	if raw == "" {
		return nil, nil
	}
	var wire []eventPeerJSON
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return nil, err
	}
	out := make([]domain.Peer, 0, len(wire))
	for _, peer := range wire {
		if peer.ID == 0 {
			continue
		}
		out = append(out, domain.Peer{Type: domain.PeerType(peer.Type), ID: peer.ID})
	}
	return out, nil
}

func encodeEventMessageIDs(ids []int) ([]byte, error) {
	if len(ids) == 0 {
		return []byte("[]"), nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("marshal event message ids: %w", err)
	}
	return raw, nil
}

func decodeEventMessageIDs(raw string) ([]int, error) {
	if raw == "" {
		return nil, nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil, err
	}
	return ids, nil
}

func encodeEventDialogFilter(folder *domain.DialogFolder) ([]byte, error) {
	if folder == nil {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(folder)
	if err != nil {
		return nil, fmt.Errorf("marshal event dialog filter: %w", err)
	}
	return raw, nil
}

func decodeEventDialogFilter(raw string) (*domain.DialogFolder, error) {
	if raw == "" || raw == "{}" {
		return nil, nil
	}
	var folder domain.DialogFolder
	if err := json.Unmarshal([]byte(raw), &folder); err != nil {
		return nil, err
	}
	return &folder, nil
}

func encodeEventFilterOrder(order []int) ([]byte, error) {
	if len(order) == 0 {
		return []byte("[]"), nil
	}
	raw, err := json.Marshal(order)
	if err != nil {
		return nil, fmt.Errorf("marshal event filter order: %w", err)
	}
	return raw, nil
}

func decodeEventFilterOrder(raw string) ([]int, error) {
	if raw == "" {
		return nil, nil
	}
	var order []int
	if err := json.Unmarshal([]byte(raw), &order); err != nil {
		return nil, err
	}
	return order, nil
}

func encodeEventFolderPeers(peers []domain.FolderPeerUpdate) ([]byte, error) {
	if len(peers) == 0 {
		return []byte("[]"), nil
	}
	raw, err := json.Marshal(peers)
	if err != nil {
		return nil, fmt.Errorf("marshal event folder peers: %w", err)
	}
	return raw, nil
}

func decodeEventFolderPeers(raw string) ([]domain.FolderPeerUpdate, error) {
	if raw == "" {
		return nil, nil
	}
	var peers []domain.FolderPeerUpdate
	if err := json.Unmarshal([]byte(raw), &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

func encodeEventStory(story domain.Story) ([]byte, error) {
	if story.Owner.ID == 0 || story.ID == 0 {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(story)
	if err != nil {
		return nil, fmt.Errorf("marshal event story: %w", err)
	}
	return raw, nil
}

func decodeEventStory(raw string) (domain.Story, error) {
	if raw == "" || raw == "{}" || raw == "null" {
		return domain.Story{}, nil
	}
	var story domain.Story
	if err := json.Unmarshal([]byte(raw), &story); err != nil {
		return domain.Story{}, err
	}
	if story.Owner.ID == 0 || story.ID == 0 {
		return domain.Story{}, nil
	}
	return story, nil
}

func encodeEventReaction(reaction *domain.MessageReaction) ([]byte, error) {
	raw, err := encodeStoryReaction(reaction)
	if err != nil {
		return nil, fmt.Errorf("marshal event reaction: %w", err)
	}
	return raw, nil
}

func decodeEventReaction(raw string) (*domain.MessageReaction, error) {
	return decodeStoryReaction(raw)
}

type peerSettingsJSON struct {
	AddContact            bool   `json:"add_contact,omitempty"`
	BlockContact          bool   `json:"block_contact,omitempty"`
	ShareContact          bool   `json:"share_contact,omitempty"`
	NeedContactsException bool   `json:"need_contacts_exception,omitempty"`
	HiddenPeerSettingsBar bool   `json:"hidden_peer_settings_bar,omitempty"`
	BusinessBotID         int64  `json:"business_bot_id,omitempty"`
	BusinessBotManageURL  string `json:"business_bot_manage_url,omitempty"`
	BusinessBotPaused     bool   `json:"business_bot_paused,omitempty"`
	BusinessBotCanReply   bool   `json:"business_bot_can_reply,omitempty"`
}

func encodePeerSettings(settings domain.PeerSettings) ([]byte, error) {
	raw, err := json.Marshal(peerSettingsJSON{
		AddContact:            settings.AddContact,
		BlockContact:          settings.BlockContact,
		ShareContact:          settings.ShareContact,
		NeedContactsException: settings.NeedContactsException,
		HiddenPeerSettingsBar: settings.HiddenPeerSettingsBar,
		BusinessBotID:         settings.BusinessBotID,
		BusinessBotManageURL:  settings.BusinessBotManageURL,
		BusinessBotPaused:     settings.BusinessBotPaused,
		BusinessBotCanReply:   settings.BusinessBotCanReply,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal peer settings: %w", err)
	}
	return raw, nil
}

func decodePeerSettings(raw string) (domain.PeerSettings, error) {
	if raw == "" {
		return domain.PeerSettings{}, nil
	}
	var wire peerSettingsJSON
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return domain.PeerSettings{}, err
	}
	return domain.PeerSettings{
		AddContact:            wire.AddContact,
		BlockContact:          wire.BlockContact,
		ShareContact:          wire.ShareContact,
		NeedContactsException: wire.NeedContactsException,
		HiddenPeerSettingsBar: wire.HiddenPeerSettingsBar,
		BusinessBotID:         wire.BusinessBotID,
		BusinessBotManageURL:  wire.BusinessBotManageURL,
		BusinessBotPaused:     wire.BusinessBotPaused,
		BusinessBotCanReply:   wire.BusinessBotCanReply,
	}, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
