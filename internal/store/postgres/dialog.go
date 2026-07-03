package postgres

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// DialogStore 用 PostgreSQL 实现 store.DialogStore。
type DialogStore struct {
	db sqlcgen.DBTX
	q  *sqlcgen.Queries
}

// NewDialogStore 基于 pgx 连接池（或事务）创建 DialogStore。
func NewDialogStore(db sqlcgen.DBTX) *DialogStore {
	return &DialogStore{db: db, q: sqlcgen.New(db)}
}

func (s *DialogStore) enrichDialogTopMessages(ctx context.Context, userID int64, messages []domain.Message) error {
	if len(messages) == 0 {
		return nil
	}
	return NewMessageStore(s.db).enrichPrivateMessageReactions(ctx, s.db, userID, messages)
}

func (s *DialogStore) ListByUser(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offsetPeerID := int64(0)
	if filter.HasOffsetPeer {
		offsetPeerID = filter.OffsetPeer.ID
	}
	folderParams := dialogFolderQueryParams(filter.Folder)
	summaryRows, err := s.q.ListDialogSummaryByUser(ctx, sqlcgen.ListDialogSummaryByUserParams{
		UserID:                 userID,
		HasFolderID:            filter.HasFolderID,
		FolderID:               pgInt32NonNegative(filter.FolderID),
		FolderExcludeArchived:  folderParams.excludeArchived,
		FolderExcludeRead:      folderParams.excludeRead,
		FolderExcludePeerTypes: folderParams.excludeTypes,
		FolderExcludePeerIds:   folderParams.excludeIDs,
		FolderIncludePeerTypes: folderParams.includeTypes,
		FolderIncludePeerIds:   folderParams.includeIDs,
		FolderPinnedPeerTypes:  folderParams.pinnedTypes,
		FolderPinnedPeerIds:    folderParams.pinnedIDs,
		FolderContacts:         folderParams.contacts,
		FolderNonContacts:      folderParams.nonContacts,
		PinnedOnly:             filter.PinnedOnly,
		ExcludePinned:          filter.ExcludePinned,
	})
	if err != nil {
		return domain.DialogList{}, fmt.Errorf("list dialog summary: %w", err)
	}
	summary := make([]domain.Dialog, 0, len(summaryRows))
	for _, row := range summaryRows {
		summary = append(summary, domain.Dialog{
			Peer:                  domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
			FolderID:              int(row.FolderID),
			TopMessage:            int(row.TopMessageID),
			TopMessageDate:        int(row.TopMessageDate),
			ReadInboxMaxID:        int(row.ReadInboxMaxID),
			ReadOutboxMaxID:       int(row.ReadOutboxMaxID),
			UnreadCount:           int(row.UnreadCount),
			UnreadMentions:        int(row.UnreadMentionsCount),
			UnreadReactions:       int(row.UnreadReactionsCount),
			TTLPeriod:             int(row.TtlPeriod),
			ThemeEmoticon:         row.ThemeEmoticon,
			HasScheduled:          row.HasScheduled,
			Pinned:                row.Pinned,
			PinnedOrder:           int(row.PinnedOrder),
			UnreadMark:            row.UnreadMark,
			PeerSettingsBarHidden: row.HiddenPeerSettingsBar,
		})
	}
	out := domain.DialogList{
		Dialogs: make([]domain.Dialog, 0, limit),
		Count:   len(summary),
		Hash:    dialogListHash(summary),
	}
	if len(summary) == 0 {
		return out, nil
	}
	rows, err := s.q.ListDialogsByUser(ctx, sqlcgen.ListDialogsByUserParams{
		UserID:                 userID,
		LimitCount:             int32(limit),
		HasFolderID:            filter.HasFolderID,
		FolderID:               pgInt32NonNegative(filter.FolderID),
		FolderExcludeArchived:  folderParams.excludeArchived,
		FolderExcludeRead:      folderParams.excludeRead,
		FolderExcludePeerTypes: folderParams.excludeTypes,
		FolderExcludePeerIds:   folderParams.excludeIDs,
		FolderIncludePeerTypes: folderParams.includeTypes,
		FolderIncludePeerIds:   folderParams.includeIDs,
		FolderPinnedPeerTypes:  folderParams.pinnedTypes,
		FolderPinnedPeerIds:    folderParams.pinnedIDs,
		FolderContacts:         folderParams.contacts,
		FolderNonContacts:      folderParams.nonContacts,
		PinnedOnly:             filter.PinnedOnly,
		ExcludePinned:          filter.ExcludePinned,
		OffsetID:               pgInt32NonNegative(filter.OffsetID),
		OffsetDate:             pgInt32NonNegative(filter.OffsetDate),
		HasOffsetPeer:          filter.HasOffsetPeer,
		OffsetPeerID:           offsetPeerID,
	})
	if err != nil {
		return domain.DialogList{}, fmt.Errorf("list dialogs: %w", err)
	}
	out.Messages = make([]domain.Message, 0, len(rows))
	out.Users = make([]domain.User, 0, len(rows))
	seenUsers := map[int64]struct{}{}
	for _, row := range rows {
		dialog := domain.Dialog{
			Peer: domain.Peer{
				Type: domain.PeerType(row.PeerType),
				ID:   row.PeerID,
			},
			FolderID:              int(row.FolderID),
			TopMessage:            int(row.TopMessageID),
			TopMessageDate:        int(row.TopMessageDate),
			ReadInboxMaxID:        int(row.ReadInboxMaxID),
			ReadOutboxMaxID:       int(row.ReadOutboxMaxID),
			UnreadCount:           int(row.UnreadCount),
			UnreadMentions:        int(row.UnreadMentionsCount),
			UnreadReactions:       int(row.UnreadReactionsCount),
			TTLPeriod:             int(row.TtlPeriod),
			ThemeEmoticon:         row.ThemeEmoticon,
			HasScheduled:          row.HasScheduled,
			Pinned:                row.Pinned,
			PinnedOrder:           int(row.PinnedOrder),
			UnreadMark:            row.UnreadMark,
			PeerSettingsBarHidden: row.HiddenPeerSettingsBar,
		}
		out.Dialogs = append(out.Dialogs, dialog)
		if row.PeerUserID != 0 {
			if _, ok := seenUsers[row.PeerUserID]; !ok {
				seenUsers[row.PeerUserID] = struct{}{}
				out.Users = append(out.Users, domain.User{
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
					Contact:               row.PeerContact,
					Mutual:                row.PeerMutual,
				})
			}
		}
		if row.MessageID != 0 {
			entities, err := decodeMessageEntities(row.MessageEntitiesJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message entities: %w", err)
			}
			media, err := decodeMessageMedia(row.MessageMediaJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message media: %w", err)
			}
			markup, err := decodeReplyMarkup(row.MessageReplyMarkupJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message reply markup: %w", err)
			}
			rich, err := decodeRichMessage(row.MessageRichMessageJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message rich message: %w", err)
			}
			// top message 必须带全量元数据：TDesktop 把 getDialogs 的消息
			// 先入缓存且不被后续 difference/getHistory 覆盖，缺 reply 等
			// 字段会让置顶/回复服务消息永久渲染成 "Deleted message"。
			silent, noforwards, reply, forward, err := messageMetadataFromFields(
				row.MessageSilent,
				row.MessageNoforwards,
				row.MessageReplyToMsgID,
				row.MessageReplyToPeerType,
				row.MessageReplyToPeerID,
				row.MessageReplyToTopID,
				row.MessageReplyToStoryID,
				row.MessageQuoteText,
				row.MessageQuoteEntitiesJson,
				row.MessageQuoteOffset,
				row.MessageFwdFromPeerType,
				row.MessageFwdFromPeerID,
				row.MessageFwdFromName,
				row.MessageFwdDate,
				row.MessageFwdSavedFromPeerType,
				row.MessageFwdSavedFromPeerID,
				row.MessageFwdSavedFromMsgID,
			)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message metadata: %w", err)
			}
			out.Messages = append(out.Messages, domain.Message{
				ID:             int(row.MessageID),
				UID:            row.MessagePrivateMessageID,
				OwnerUserID:    row.UserID,
				Peer:           dialog.Peer,
				From:           domain.Peer{Type: domain.PeerTypeUser, ID: row.MessageFromUserID},
				Date:           int(row.MessageDate),
				EditDate:       int(row.MessageEditDate),
				HideEdited:     row.MessageHideEdited,
				Out:            row.MessageOutgoing,
				Silent:         silent,
				NoForwards:     noforwards,
				Body:           row.MessageBody,
				Entities:       entities,
				ReplyTo:        reply,
				Forward:        forward,
				Media:          media,
				TTLPeriod:      int(row.MessageTtlPeriod),
				ExpiresAt:      int(row.MessageExpiresAt),
				MediaUnread:    row.MessageMediaUnread,
				ReactionUnread: row.MessageReactionUnread,
				ViaBotID:       row.MessageViaBotID,
				GroupedID:      row.MessageGroupedID,
				Effect:         row.MessageEffect,
				ReplyMarkup:    markup,
				RichMessage:    rich,
				Pinned:         row.MessagePinned,
				SavedPeer:      savedPeerFromFields(row.MessageSavedPeerType, row.MessageSavedPeerID),
			})
		}
	}
	if err := s.enrichDialogTopMessages(ctx, userID, out.Messages); err != nil {
		return domain.DialogList{}, err
	}
	return out, nil
}

func (s *DialogStore) ListByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error) {
	if len(peers) == 0 {
		return domain.DialogList{}, nil
	}
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	for _, peer := range peers {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	if len(peerTypes) == 0 {
		return domain.DialogList{}, nil
	}
	rows, err := s.q.ListDialogsByPeers(ctx, sqlcgen.ListDialogsByPeersParams{
		UserID:    userID,
		PeerTypes: peerTypes,
		PeerIds:   peerIDs,
	})
	if err != nil {
		return domain.DialogList{}, fmt.Errorf("list dialogs by peers: %w", err)
	}
	out := domain.DialogList{
		Dialogs:  make([]domain.Dialog, 0, len(rows)),
		Messages: make([]domain.Message, 0, len(rows)),
		Users:    make([]domain.User, 0, len(rows)),
	}
	seenUsers := map[int64]struct{}{}
	for _, row := range rows {
		dialog := domain.Dialog{
			Peer:                  domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
			FolderID:              int(row.FolderID),
			TopMessage:            int(row.TopMessageID),
			TopMessageDate:        int(row.TopMessageDate),
			ReadInboxMaxID:        int(row.ReadInboxMaxID),
			ReadOutboxMaxID:       int(row.ReadOutboxMaxID),
			UnreadCount:           int(row.UnreadCount),
			UnreadMentions:        int(row.UnreadMentionsCount),
			UnreadReactions:       int(row.UnreadReactionsCount),
			TTLPeriod:             int(row.TtlPeriod),
			ThemeEmoticon:         row.ThemeEmoticon,
			HasScheduled:          row.HasScheduled,
			Pinned:                row.Pinned,
			PinnedOrder:           int(row.PinnedOrder),
			UnreadMark:            row.UnreadMark,
			PeerSettingsBarHidden: row.HiddenPeerSettingsBar,
		}
		out.Dialogs = append(out.Dialogs, dialog)
		if row.PeerUserID != 0 {
			if _, ok := seenUsers[row.PeerUserID]; !ok {
				seenUsers[row.PeerUserID] = struct{}{}
				out.Users = append(out.Users, domain.User{
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
					Contact:               row.PeerContact,
					Mutual:                row.PeerMutual,
				})
			}
		}
		if row.MessageID != 0 {
			entities, err := decodeMessageEntities(row.MessageEntitiesJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message entities: %w", err)
			}
			media, err := decodeMessageMedia(row.MessageMediaJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message media: %w", err)
			}
			markup, err := decodeReplyMarkup(row.MessageReplyMarkupJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message reply markup: %w", err)
			}
			rich, err := decodeRichMessage(row.MessageRichMessageJson)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message rich message: %w", err)
			}
			// top message 必须带全量元数据：TDesktop 把 getDialogs 的消息
			// 先入缓存且不被后续 difference/getHistory 覆盖，缺 reply 等
			// 字段会让置顶/回复服务消息永久渲染成 "Deleted message"。
			silent, noforwards, reply, forward, err := messageMetadataFromFields(
				row.MessageSilent,
				row.MessageNoforwards,
				row.MessageReplyToMsgID,
				row.MessageReplyToPeerType,
				row.MessageReplyToPeerID,
				row.MessageReplyToTopID,
				row.MessageReplyToStoryID,
				row.MessageQuoteText,
				row.MessageQuoteEntitiesJson,
				row.MessageQuoteOffset,
				row.MessageFwdFromPeerType,
				row.MessageFwdFromPeerID,
				row.MessageFwdFromName,
				row.MessageFwdDate,
				row.MessageFwdSavedFromPeerType,
				row.MessageFwdSavedFromPeerID,
				row.MessageFwdSavedFromMsgID,
			)
			if err != nil {
				return domain.DialogList{}, fmt.Errorf("decode message metadata: %w", err)
			}
			out.Messages = append(out.Messages, domain.Message{
				ID:             int(row.MessageID),
				UID:            row.MessagePrivateMessageID,
				OwnerUserID:    row.UserID,
				Peer:           dialog.Peer,
				From:           domain.Peer{Type: domain.PeerTypeUser, ID: row.MessageFromUserID},
				Date:           int(row.MessageDate),
				EditDate:       int(row.MessageEditDate),
				HideEdited:     row.MessageHideEdited,
				Out:            row.MessageOutgoing,
				Silent:         silent,
				NoForwards:     noforwards,
				Body:           row.MessageBody,
				Entities:       entities,
				ReplyTo:        reply,
				Forward:        forward,
				Media:          media,
				TTLPeriod:      int(row.MessageTtlPeriod),
				ExpiresAt:      int(row.MessageExpiresAt),
				MediaUnread:    row.MessageMediaUnread,
				ReactionUnread: row.MessageReactionUnread,
				ViaBotID:       row.MessageViaBotID,
				GroupedID:      row.MessageGroupedID,
				Effect:         row.MessageEffect,
				ReplyMarkup:    markup,
				RichMessage:    rich,
				Pinned:         row.MessagePinned,
				SavedPeer:      savedPeerFromFields(row.MessageSavedPeerType, row.MessageSavedPeerID),
			})
		}
	}
	if err := s.enrichDialogTopMessages(ctx, userID, out.Messages); err != nil {
		return domain.DialogList{}, err
	}
	out.Count = len(out.Dialogs)
	out.Hash = dialogListHash(out.Dialogs)
	return out, nil
}

func (s *DialogStore) Upsert(ctx context.Context, userID int64, dialog domain.Dialog) error {
	if err := s.q.UpsertDialog(ctx, sqlcgen.UpsertDialogParams{
		UserID:               userID,
		PeerType:             string(dialog.Peer.Type),
		PeerID:               dialog.Peer.ID,
		TopMessageID:         int32(dialog.TopMessage),
		TopMessageDate:       int32(dialog.TopMessageDate),
		ReadInboxMaxID:       int32(dialog.ReadInboxMaxID),
		ReadOutboxMaxID:      int32(dialog.ReadOutboxMaxID),
		UnreadCount:          int32(dialog.UnreadCount),
		UnreadMentionsCount:  int32(dialog.UnreadMentions),
		UnreadReactionsCount: int32(dialog.UnreadReactions),
		Pinned:               dialog.Pinned,
		UnreadMark:           dialog.UnreadMark,
	}); err != nil {
		return fmt.Errorf("upsert dialog: %w", err)
	}
	return nil
}

func (s *DialogStore) UpsertInbox(ctx context.Context, userID int64, dialog domain.Dialog) error {
	if err := s.q.UpsertInboxDialog(ctx, sqlcgen.UpsertInboxDialogParams{
		UserID:         userID,
		PeerType:       string(dialog.Peer.Type),
		PeerID:         dialog.Peer.ID,
		TopMessageID:   int32(dialog.TopMessage),
		TopMessageDate: int32(dialog.TopMessageDate),
	}); err != nil {
		return fmt.Errorf("upsert inbox dialog: %w", err)
	}
	return nil
}

func (s *DialogStore) SaveDraft(ctx context.Context, userID int64, draft domain.DialogDraft) error {
	data, err := json.Marshal(draft)
	if err != nil {
		return fmt.Errorf("marshal dialog draft: %w", err)
	}
	if err := s.q.UpsertDialogDraft(ctx, sqlcgen.UpsertDialogDraftParams{
		UserID:       userID,
		PeerType:     string(draft.Peer.Type),
		PeerID:       draft.Peer.ID,
		TopMessageID: int32(draft.TopMessageID),
		Date:         int32(draft.Date),
		DraftJson:    data,
	}); err != nil {
		return fmt.Errorf("upsert dialog draft: %w", err)
	}
	return nil
}

func (s *DialogStore) GetDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (domain.DialogDraft, bool, error) {
	var data []byte
	err := s.db.QueryRow(ctx, `
SELECT draft
FROM dialog_drafts
WHERE user_id = $1 AND peer_type = $2 AND peer_id = $3 AND top_message_id = $4`,
		userID, string(peer.Type), peer.ID, int32(topMessageID)).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.DialogDraft{}, false, nil
	}
	if err != nil {
		return domain.DialogDraft{}, false, fmt.Errorf("get dialog draft: %w", err)
	}
	var draft domain.DialogDraft
	if err := json.Unmarshal(data, &draft); err != nil {
		return domain.DialogDraft{}, false, fmt.Errorf("decode dialog draft: %w", err)
	}
	return draft, true, nil
}

func (s *DialogStore) DeleteDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (bool, error) {
	changed, err := s.q.DeleteDialogDraft(ctx, sqlcgen.DeleteDialogDraftParams{
		UserID:       userID,
		PeerType:     string(peer.Type),
		PeerID:       peer.ID,
		TopMessageID: int32(topMessageID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("delete dialog draft: %w", err)
	}
	return changed, nil
}

func (s *DialogStore) ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	rows, err := s.q.ListDialogDrafts(ctx, sqlcgen.ListDialogDraftsParams{
		UserID:     userID,
		LimitCount: int32(clampDialogDraftLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("list dialog drafts: %w", err)
	}
	return decodeDialogDrafts(rows)
}

func (s *DialogStore) ClearDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error) {
	rows, err := s.q.ClearDialogDrafts(ctx, sqlcgen.ClearDialogDraftsParams{
		UserID:     userID,
		LimitCount: int32(clampDialogDraftLimit(limit)),
	})
	if err != nil {
		return nil, fmt.Errorf("clear dialog drafts: %w", err)
	}
	return decodeDialogDrafts(rows)
}

func (s *DialogStore) MarkRead(ctx context.Context, userID int64, peer domain.Peer, maxID int) (domain.ReadHistoryResult, error) {
	row, err := s.q.MarkDialogRead(ctx, sqlcgen.MarkDialogReadParams{
		UserID:   userID,
		PeerType: string(peer.Type),
		PeerID:   peer.ID,
		MaxID:    pgInt32NonNegative(maxID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ReadHistoryResult{OwnerUserID: userID, Peer: peer, MaxID: maxID}, nil
		}
		return domain.ReadHistoryResult{}, fmt.Errorf("mark dialog read: %w", err)
	}
	return domain.ReadHistoryResult{
		OwnerUserID:      row.UserID,
		Peer:             domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID},
		MaxID:            int(row.ReadInboxMaxID),
		StillUnreadCount: int(row.UnreadCount),
		Changed:          row.Changed,
	}, nil
}

func (s *DialogStore) SetPinned(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error) {
	row, err := s.q.SetDialogPinned(ctx, sqlcgen.SetDialogPinnedParams{
		UserID:   userID,
		PeerType: string(peer.Type),
		PeerID:   peer.ID,
		Pinned:   pinned,
	})
	if err != nil {
		return false, 0, fmt.Errorf("set dialog pinned: %w", err)
	}
	return row.Changed, int(row.FolderID), nil
}

func (s *DialogStore) ReorderPinned(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error) {
	peerTypes, peerIDs := peerArrays(order)
	changed := false
	if force {
		tag, err := s.db.Exec(ctx, `
WITH requested AS (
    SELECT ($3::text[])[i] AS peer_type, ($4::bigint[])[i] AS peer_id
    FROM generate_subscripts($4::bigint[], 1) AS g(i)
    WHERE i <= cardinality($3::text[])
)
UPDATE dialogs d
SET pinned = false, pinned_order = 0, updated_at = now()
WHERE d.user_id = $1
  AND d.pinned
  AND d.folder_id = $2::int
  AND NOT EXISTS (
      SELECT 1
      FROM requested r
      WHERE r.peer_type = d.peer_type
        AND r.peer_id = d.peer_id
  )`, userID, folderID, peerTypes, peerIDs)
		if err != nil {
			return false, fmt.Errorf("clear pinned dialogs not in order: %w", err)
		}
		if tag.RowsAffected() > 0 {
			changed = true
		}
	}
	if len(peerTypes) == 0 {
		return changed, nil
	}
	tag, err := s.db.Exec(ctx, `
WITH requested AS (
    SELECT ($3::text[])[i] AS peer_type, ($4::bigint[])[i] AS peer_id, i::int AS pos
    FROM generate_subscripts($4::bigint[], 1) AS g(i)
    WHERE i <= cardinality($3::text[])
),
deduped AS (
    SELECT DISTINCT ON (peer_type, peer_id)
        peer_type,
        peer_id,
        (cardinality($4::bigint[]) - pos + 1)::int AS ord
    FROM requested
    ORDER BY peer_type, peer_id, pos
)
UPDATE dialogs d
SET pinned = true, pinned_order = deduped.ord, updated_at = now()
FROM deduped
WHERE d.user_id = $1
  AND d.peer_type = deduped.peer_type
  AND d.peer_id = deduped.peer_id
  AND d.folder_id = $2::int
  AND (NOT d.pinned OR d.pinned_order IS DISTINCT FROM deduped.ord)`, userID, folderID, peerTypes, peerIDs)
	if err != nil {
		return false, fmt.Errorf("reorder pinned dialogs: %w", err)
	}
	if tag.RowsAffected() > 0 {
		changed = true
	}
	return changed, nil
}

func (s *DialogStore) SetUnreadMark(ctx context.Context, userID int64, peer domain.Peer, unread bool) (bool, error) {
	changed, err := s.q.SetDialogUnreadMark(ctx, sqlcgen.SetDialogUnreadMarkParams{
		UserID:   userID,
		PeerType: string(peer.Type),
		PeerID:   peer.ID,
		Unread:   unread,
	})
	if err != nil {
		return false, fmt.Errorf("set dialog unread mark: %w", err)
	}
	return changed, nil
}

func (s *DialogStore) ListUnreadMarked(ctx context.Context, userID int64) ([]domain.Peer, error) {
	rows, err := s.q.ListDialogUnreadMarks(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list dialog unread marks: %w", err)
	}
	out := make([]domain.Peer, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.Peer{Type: domain.PeerType(row.PeerType), ID: row.PeerID})
	}
	return out, nil
}

func (s *DialogStore) SetChatTheme(ctx context.Context, userID int64, peer domain.Peer, emoticon string) (bool, error) {
	if userID == 0 || peer.Type == "" || peer.ID == 0 {
		return false, nil
	}
	var current string
	err := s.db.QueryRow(ctx, `
SELECT theme_emoticon
FROM dialogs
WHERE user_id = $1 AND peer_type = $2 AND peer_id = $3`,
		userID, string(peer.Type), peer.ID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		if emoticon == "" {
			return false, nil
		}
		if _, err := s.db.Exec(ctx, `
INSERT INTO dialogs (user_id, peer_type, peer_id, theme_emoticon)
VALUES ($1, $2, $3, $4)`,
			userID, string(peer.Type), peer.ID, emoticon); err != nil {
			return false, fmt.Errorf("insert dialog chat theme: %w", err)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("get dialog chat theme: %w", err)
	}
	if current == emoticon {
		return false, nil
	}
	if _, err := s.db.Exec(ctx, `
UPDATE dialogs
SET theme_emoticon = $4, updated_at = now()
WHERE user_id = $1 AND peer_type = $2 AND peer_id = $3`,
		userID, string(peer.Type), peer.ID, emoticon); err != nil {
		return false, fmt.Errorf("update dialog chat theme: %w", err)
	}
	return true, nil
}

func (s *DialogStore) SetPeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error) {
	changed, err := s.q.SetPeerSettingsBarHidden(ctx, sqlcgen.SetPeerSettingsBarHiddenParams{
		UserID:   userID,
		PeerType: string(peer.Type),
		PeerID:   peer.ID,
	})
	if err != nil {
		return false, fmt.Errorf("set peer settings bar hidden: %w", err)
	}
	return changed, nil
}

func (s *DialogStore) PeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error) {
	hidden, err := s.q.GetPeerSettingsBarHidden(ctx, sqlcgen.GetPeerSettingsBarHiddenParams{
		UserID:   userID,
		PeerType: string(peer.Type),
		PeerID:   peer.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("get peer settings bar hidden: %w", err)
	}
	return hidden, nil
}

func (s *DialogStore) ListFolders(ctx context.Context, userID int64) (domain.DialogFolderList, error) {
	rows, err := s.q.ListDialogFolders(ctx, userID)
	if err != nil {
		return domain.DialogFolderList{}, fmt.Errorf("list dialog folders: %w", err)
	}
	tagsEnabled := false
	if enabled, err := s.q.GetDialogFolderTags(ctx, userID); err == nil {
		tagsEnabled = enabled
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.DialogFolderList{}, fmt.Errorf("get dialog folder tags: %w", err)
	}
	out := domain.DialogFolderList{
		TagsEnabled: tagsEnabled,
		Folders:     make([]domain.DialogFolder, 0, len(rows)),
	}
	for _, row := range rows {
		folder, err := decodeDialogFolder(row.FilterJson)
		if err != nil {
			return domain.DialogFolderList{}, fmt.Errorf("decode dialog folder %d: %w", row.FilterID, err)
		}
		folder.ID = int(row.FilterID)
		folder.IsChatlist = row.IsChatlist
		out.Folders = append(out.Folders, folder)
	}
	return out, nil
}

func (s *DialogStore) GetFolder(ctx context.Context, userID int64, folderID int) (domain.DialogFolder, bool, error) {
	row, err := s.q.GetDialogFolder(ctx, sqlcgen.GetDialogFolderParams{
		UserID:   userID,
		FilterID: pgInt32NonNegative(folderID),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DialogFolder{}, false, nil
		}
		return domain.DialogFolder{}, false, fmt.Errorf("get dialog folder: %w", err)
	}
	folder, err := decodeDialogFolder(row.FilterJson)
	if err != nil {
		return domain.DialogFolder{}, false, fmt.Errorf("decode dialog folder: %w", err)
	}
	folder.ID = int(row.FilterID)
	folder.IsChatlist = row.IsChatlist
	return folder, true, nil
}

func (s *DialogStore) UpsertFolder(ctx context.Context, userID int64, folder domain.DialogFolder) error {
	data, err := json.Marshal(folder)
	if err != nil {
		return fmt.Errorf("marshal dialog folder: %w", err)
	}
	if err := s.q.UpsertDialogFolder(ctx, sqlcgen.UpsertDialogFolderParams{
		UserID:     userID,
		FilterID:   pgInt32NonNegative(folder.ID),
		IsChatlist: folder.IsChatlist,
		FilterJson: data,
	}); err != nil {
		return fmt.Errorf("upsert dialog folder: %w", err)
	}
	return nil
}

func (s *DialogStore) DeleteFolder(ctx context.Context, userID int64, folderID int) error {
	if err := s.q.DeleteDialogFolder(ctx, sqlcgen.DeleteDialogFolderParams{
		UserID:   userID,
		FilterID: pgInt32NonNegative(folderID),
	}); err != nil {
		return fmt.Errorf("delete dialog folder: %w", err)
	}
	return nil
}

func (s *DialogStore) ReorderFolders(ctx context.Context, userID int64, order []int) error {
	if err := s.q.ReorderDialogFolders(ctx, sqlcgen.ReorderDialogFoldersParams{
		UserID:    userID,
		FilterIds: int32s(order),
	}); err != nil {
		return fmt.Errorf("reorder dialog folders: %w", err)
	}
	return nil
}

func (s *DialogStore) SetFolderTagsEnabled(ctx context.Context, userID int64, enabled bool) error {
	if err := s.q.SetDialogFolderTags(ctx, sqlcgen.SetDialogFolderTagsParams{
		UserID:      userID,
		TagsEnabled: enabled,
	}); err != nil {
		return fmt.Errorf("set dialog folder tags: %w", err)
	}
	return nil
}

func (s *DialogStore) EditPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error {
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	folderIDs := make([]int32, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, item := range peers {
		if item.Peer.Type == "" || item.Peer.ID == 0 {
			continue
		}
		if _, ok := seen[item.Peer]; ok {
			continue
		}
		seen[item.Peer] = struct{}{}
		peerTypes = append(peerTypes, string(item.Peer.Type))
		peerIDs = append(peerIDs, item.Peer.ID)
		folderIDs = append(folderIDs, pgInt32NonNegative(item.FolderID))
	}
	if len(peerTypes) == 0 {
		return nil
	}
	if err := s.q.EditDialogPeerFolders(ctx, sqlcgen.EditDialogPeerFoldersParams{
		UserID:    userID,
		PeerTypes: peerTypes,
		PeerIds:   peerIDs,
		FolderIds: folderIDs,
	}); err != nil {
		return fmt.Errorf("edit dialog peer folders: %w", err)
	}
	return nil
}

func (s *DialogStore) SetArchivePinned(ctx context.Context, userID int64, pinned bool) (bool, error) {
	changed, err := s.q.SetDialogArchivePinned(ctx, sqlcgen.SetDialogArchivePinnedParams{
		UserID: userID,
		Pinned: pinned,
	})
	if err != nil {
		return false, fmt.Errorf("set dialog archive pinned: %w", err)
	}
	return changed, nil
}

func (s *DialogStore) ArchivePinned(ctx context.Context, userID int64) (bool, error) {
	pinned, err := s.q.GetDialogArchivePinned(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 官方默认 archive folder 行在置顶区。
			return true, nil
		}
		return false, fmt.Errorf("get dialog archive pinned: %w", err)
	}
	return pinned, nil
}

func (s *DialogStore) CountArchiveUnread(ctx context.Context, userID int64) (int, int, error) {
	row, err := s.q.CountArchiveUnreadDialogs(ctx, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("count archive unread dialogs: %w", err)
	}
	return int(row.UnreadPeers), int(row.UnreadMessages), nil
}

type dialogFolderParams struct {
	contacts        bool
	nonContacts     bool
	excludeArchived bool
	excludeRead     bool
	includeTypes    []string
	includeIDs      []int64
	pinnedTypes     []string
	pinnedIDs       []int64
	excludeTypes    []string
	excludeIDs      []int64
}

func dialogFolderQueryParams(folder *domain.DialogFolder) dialogFolderParams {
	if folder == nil {
		return dialogFolderParams{}
	}
	includeTypes, includeIDs := folderPeerArrays(folder.IncludePeers)
	pinnedTypes, pinnedIDs := folderPeerArrays(folder.PinnedPeers)
	excludeTypes, excludeIDs := folderPeerArrays(folder.ExcludePeers)
	return dialogFolderParams{
		contacts:        folder.Contacts,
		nonContacts:     folder.NonContacts,
		excludeArchived: folder.ExcludeArchived,
		excludeRead:     folder.ExcludeRead,
		includeTypes:    includeTypes,
		includeIDs:      includeIDs,
		pinnedTypes:     pinnedTypes,
		pinnedIDs:       pinnedIDs,
		excludeTypes:    excludeTypes,
		excludeIDs:      excludeIDs,
	}
}

func folderPeerArrays(peers []domain.DialogFolderPeer) ([]string, []int64) {
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, item := range peers {
		peer := item.Peer
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	return peerTypes, peerIDs
}

func decodeDialogFolder(data string) (domain.DialogFolder, error) {
	if data == "" {
		return domain.DialogFolder{}, nil
	}
	var folder domain.DialogFolder
	if err := json.Unmarshal([]byte(data), &folder); err != nil {
		return domain.DialogFolder{}, err
	}
	return folder, nil
}

func decodeDialogDrafts(rows []string) ([]domain.DialogDraft, error) {
	out := make([]domain.DialogDraft, 0, len(rows))
	for _, row := range rows {
		draft, err := decodeDialogDraft(row)
		if err != nil {
			return nil, err
		}
		out = append(out, draft)
	}
	return out, nil
}

func decodeDialogDraft(data string) (domain.DialogDraft, error) {
	if data == "" {
		return domain.DialogDraft{}, nil
	}
	var draft domain.DialogDraft
	if err := json.Unmarshal([]byte(data), &draft); err != nil {
		return domain.DialogDraft{}, fmt.Errorf("decode dialog draft: %w", err)
	}
	if draft.Entities == nil {
		draft.Entities = []domain.MessageEntity{}
	}
	return draft, nil
}

func clampDialogDraftLimit(limit int) int {
	if limit <= 0 || limit > domain.MaxDialogDraftsPerUser {
		return domain.MaxDialogDraftsPerUser
	}
	return limit
}

func peerArrays(peers []domain.Peer) ([]string, []int64) {
	peerTypes := make([]string, 0, len(peers))
	peerIDs := make([]int64, 0, len(peers))
	seen := make(map[domain.Peer]struct{}, len(peers))
	for _, peer := range peers {
		if peer.Type == "" || peer.ID == 0 {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		peerTypes = append(peerTypes, string(peer.Type))
		peerIDs = append(peerIDs, peer.ID)
	}
	return peerTypes, peerIDs
}

func dialogListHash(dialogs []domain.Dialog) int64 {
	if len(dialogs) == 0 {
		return 0
	}
	h := fnv.New64a()
	var buf [47]byte
	for _, d := range dialogs {
		binary.LittleEndian.PutUint64(buf[:8], uint64(d.Peer.ID))
		binary.LittleEndian.PutUint32(buf[8:12], uint32(d.FolderID))
		binary.LittleEndian.PutUint32(buf[12:16], uint32(d.TopMessage))
		binary.LittleEndian.PutUint32(buf[16:20], uint32(d.TopMessageDate))
		binary.LittleEndian.PutUint32(buf[20:24], uint32(d.ReadInboxMaxID))
		binary.LittleEndian.PutUint32(buf[24:28], uint32(d.ReadOutboxMaxID))
		binary.LittleEndian.PutUint32(buf[28:32], uint32(d.UnreadCount))
		binary.LittleEndian.PutUint32(buf[32:36], uint32(d.UnreadMentions))
		binary.LittleEndian.PutUint32(buf[36:40], uint32(d.UnreadReactions))
		if d.Pinned {
			buf[40] = 1
		} else {
			buf[40] = 0
		}
		binary.LittleEndian.PutUint32(buf[41:45], uint32(d.PinnedOrder))
		if d.UnreadMark {
			buf[45] = 1
		} else {
			buf[45] = 0
		}
		if d.PeerSettingsBarHidden {
			buf[46] = 1
		} else {
			buf[46] = 0
		}
		_, _ = h.Write(buf[:])
		_, _ = h.Write([]byte(d.ThemeEmoticon))
	}
	return int64(h.Sum64())
}
