package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
)

func (s *PasswordStore) HasBusinessAutomation(ctx context.Context, userID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM user_business_profiles
  WHERE user_id = $1
    AND (greeting_message <> '{}'::jsonb OR away_message <> '{}'::jsonb)
  UNION ALL
  SELECT 1
  FROM business_connected_bots
  WHERE owner_user_id = $1
)`, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check business automation: %w", err)
	}
	return exists, nil
}

func (s *PasswordStore) GetBusinessProfile(ctx context.Context, userID int64) (domain.BusinessProfile, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT
  COALESCE(work_hours::text, '{}')::text,
  COALESCE(location::text, '{}')::text,
  COALESCE(intro::text, '{}')::text,
  COALESCE(greeting_message::text, '{}')::text,
  COALESCE(away_message::text, '{}')::text,
  COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint
FROM user_business_profiles
WHERE user_id = $1`, userID)
	var workHoursJSON, locationJSON, introJSON, greetingJSON, awayJSON string
	profile := domain.BusinessProfile{UserID: userID}
	if err := row.Scan(&workHoursJSON, &locationJSON, &introJSON, &greetingJSON, &awayJSON, &profile.UpdatedAtUnix); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BusinessProfile{}, false, nil
		}
		return domain.BusinessProfile{}, false, fmt.Errorf("get business profile: %w", err)
	}
	if err := decodeNullableJSON(workHoursJSON, &profile.WorkHours); err != nil {
		return domain.BusinessProfile{}, false, fmt.Errorf("decode work hours: %w", err)
	}
	if err := decodeNullableJSON(locationJSON, &profile.Location); err != nil {
		return domain.BusinessProfile{}, false, fmt.Errorf("decode location: %w", err)
	}
	if err := decodeNullableJSON(introJSON, &profile.Intro); err != nil {
		return domain.BusinessProfile{}, false, fmt.Errorf("decode intro: %w", err)
	}
	if err := decodeNullableJSON(greetingJSON, &profile.Greeting); err != nil {
		return domain.BusinessProfile{}, false, fmt.Errorf("decode greeting: %w", err)
	}
	if err := decodeNullableJSON(awayJSON, &profile.Away); err != nil {
		return domain.BusinessProfile{}, false, fmt.Errorf("decode away: %w", err)
	}
	return profile, true, nil
}

func (s *PasswordStore) SaveBusinessProfile(ctx context.Context, profile domain.BusinessProfile) error {
	workHours, err := encodeNullableJSON(profile.WorkHours)
	if err != nil {
		return err
	}
	location, err := encodeNullableJSON(profile.Location)
	if err != nil {
		return err
	}
	intro, err := encodeNullableJSON(profile.Intro)
	if err != nil {
		return err
	}
	greeting, err := encodeNullableJSON(profile.Greeting)
	if err != nil {
		return err
	}
	away, err := encodeNullableJSON(profile.Away)
	if err != nil {
		return err
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO user_business_profiles (
  user_id, work_hours, location, intro, greeting_message, away_message, updated_at
) VALUES ($1,$2::jsonb,$3::jsonb,$4::jsonb,$5::jsonb,$6::jsonb,now())
ON CONFLICT (user_id) DO UPDATE SET
  work_hours = EXCLUDED.work_hours,
  location = EXCLUDED.location,
  intro = EXCLUDED.intro,
  greeting_message = EXCLUDED.greeting_message,
  away_message = EXCLUDED.away_message,
  updated_at = now()`, profile.UserID, string(workHours), string(location), string(intro), string(greeting), string(away)); err != nil {
		return fmt.Errorf("save business profile: %w", err)
	}
	return nil
}

func (s *PasswordStore) ListBusinessChatLinks(ctx context.Context, ownerUserID int64) ([]domain.BusinessChatLink, error) {
	rows, err := s.db.Query(ctx, `
SELECT slug, owner_user_id, link, message, COALESCE(entities::text, '[]')::text, title, views,
       COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
       COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint
FROM business_chat_links
WHERE owner_user_id = $1
ORDER BY created_at ASC, slug ASC
LIMIT $2`, ownerUserID, domain.MaxBusinessChatLinks)
	if err != nil {
		return nil, fmt.Errorf("list business chat links: %w", err)
	}
	defer rows.Close()
	out := make([]domain.BusinessChatLink, 0)
	for rows.Next() {
		link, err := scanBusinessChatLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan business chat links: %w", err)
	}
	return out, nil
}

func (s *PasswordStore) CreateBusinessChatLink(ctx context.Context, link domain.BusinessChatLink) (domain.BusinessChatLink, error) {
	return link, withTx(ctx, s.db, "create business chat link", func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*)::int FROM business_chat_links WHERE owner_user_id = $1`, link.OwnerUserID).Scan(&count); err != nil {
			return fmt.Errorf("count business chat links: %w", err)
		}
		if count >= domain.MaxBusinessChatLinks {
			return domain.ErrBusinessChatLinksTooMuch
		}
		entities, err := encodeMessageEntities(link.Entities)
		if err != nil {
			return err
		}
		row := tx.QueryRow(ctx, `
INSERT INTO business_chat_links (slug, owner_user_id, link, message, entities, title, views, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,now(),now())
RETURNING slug, owner_user_id, link, message, COALESCE(entities::text, '[]')::text, title, views,
          COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
          COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint`,
			link.Slug, link.OwnerUserID, link.Link, link.Message, string(entities), link.Title, link.Views)
		saved, err := scanBusinessChatLink(row)
		if err != nil {
			return fmt.Errorf("insert business chat link: %w", err)
		}
		link = saved
		return nil
	})
}

func (s *PasswordStore) UpdateBusinessChatLink(ctx context.Context, ownerUserID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error) {
	entities, err := encodeMessageEntities(input.Entities)
	if err != nil {
		return domain.BusinessChatLink{}, err
	}
	row := s.db.QueryRow(ctx, `
UPDATE business_chat_links
SET message = $3,
    entities = $4::jsonb,
    title = $5,
    updated_at = now()
WHERE owner_user_id = $1
  AND slug = $2
RETURNING slug, owner_user_id, link, message, COALESCE(entities::text, '[]')::text, title, views,
          COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
          COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint`, ownerUserID, slug, input.Message, string(entities), input.Title)
	link, err := scanBusinessChatLink(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BusinessChatLink{}, domain.ErrBusinessChatLinkNotFound
		}
		return domain.BusinessChatLink{}, fmt.Errorf("update business chat link: %w", err)
	}
	return link, nil
}

func (s *PasswordStore) DeleteBusinessChatLink(ctx context.Context, ownerUserID int64, slug string) (bool, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM business_chat_links WHERE owner_user_id = $1 AND slug = $2`, ownerUserID, slug)
	if err != nil {
		return false, fmt.Errorf("delete business chat link: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PasswordStore) ResolveBusinessChatLink(ctx context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error) {
	sqlText := `
SELECT slug, owner_user_id, link, message, COALESCE(entities::text, '[]')::text, title, views,
       COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
       COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint
FROM business_chat_links
WHERE slug = $1`
	if bumpViews {
		sqlText = `
UPDATE business_chat_links
SET views = views + 1,
    updated_at = now()
WHERE slug = $1
RETURNING slug, owner_user_id, link, message, COALESCE(entities::text, '[]')::text, title, views,
          COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
          COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint`
	}
	link, err := scanBusinessChatLink(s.db.QueryRow(ctx, sqlText, slug))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BusinessChatLink{}, false, nil
		}
		return domain.BusinessChatLink{}, false, fmt.Errorf("resolve business chat link: %w", err)
	}
	return link, true, nil
}

func (s *PasswordStore) ListQuickReplies(ctx context.Context, ownerUserID int64, includeTopMessages bool) (domain.QuickReplyList, error) {
	replies, err := s.listQuickReplies(ctx, ownerUserID)
	if err != nil {
		return domain.QuickReplyList{}, err
	}
	messages := make([]domain.QuickReplyMessage, 0, len(replies))
	if includeTopMessages {
		for _, reply := range replies {
			if reply.TopMessage == 0 {
				continue
			}
			msgs, err := s.GetQuickReplyMessages(ctx, ownerUserID, reply.ID, []int{reply.TopMessage})
			if err != nil {
				return domain.QuickReplyList{}, err
			}
			messages = append(messages, msgs.Messages...)
		}
	}
	return domain.QuickReplyList{
		OwnerUserID:  ownerUserID,
		QuickReplies: replies,
		Messages:     messages,
		Hash:         postgresQuickReplyListHash(replies),
	}, nil
}

func (s *PasswordStore) CheckQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcut string) (bool, error) {
	shortcut, err := domain.NormalizeQuickReplyShortcut(shortcut)
	if err != nil {
		return false, err
	}
	var id int
	err = s.db.QueryRow(ctx, `SELECT shortcut_id FROM quick_replies WHERE owner_user_id = $1 AND lower(shortcut) = lower($2)`, ownerUserID, shortcut).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("check quick reply shortcut: %w", err)
	}
	return false, nil
}

func (s *PasswordStore) SaveQuickReplyText(ctx context.Context, ownerUserID int64, shortcut string, msg domain.QuickReplyMessage) (domain.QuickReplyMutation, error) {
	shortcut, err := domain.NormalizeQuickReplyShortcut(shortcut)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	var mutation domain.QuickReplyMutation
	err = withTx(ctx, s.db, "save quick reply text", func(tx pgx.Tx) error {
		shortcutID, created, err := ensureQuickReplyShortcut(ctx, tx, ownerUserID, shortcut)
		if err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*)::int FROM quick_reply_messages WHERE owner_user_id = $1 AND shortcut_id = $2`, ownerUserID, shortcutID).Scan(&count); err != nil {
			return fmt.Errorf("count quick reply messages: %w", err)
		}
		if count >= domain.MaxQuickReplyMessages {
			return domain.ErrShortcutInvalid
		}
		var messageID int
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(message_id), 0)::int + 1 FROM quick_reply_messages WHERE owner_user_id = $1`, ownerUserID).Scan(&messageID); err != nil {
			return fmt.Errorf("allocate quick reply message id: %w", err)
		}
		entities, err := encodeMessageEntities(msg.Entities)
		if err != nil {
			return err
		}
		media, err := encodeMessageMedia(msg.Media)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO quick_reply_messages (owner_user_id, shortcut_id, message_id, random_id, message_date, body, entities, media, grouped_id, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8::jsonb,$9,now(),now())`,
			ownerUserID, shortcutID, messageID, msg.RandomID, msg.Date, msg.Message, string(entities), string(media), msg.GroupedID); err != nil {
			return fmt.Errorf("insert quick reply message: %w", err)
		}
		replies, err := listQuickRepliesTx(ctx, tx, ownerUserID)
		if err != nil {
			return err
		}
		message := domain.QuickReplyMessage{
			OwnerUserID: ownerUserID,
			ShortcutID:  shortcutID,
			ID:          messageID,
			RandomID:    msg.RandomID,
			Date:        msg.Date,
			Message:     msg.Message,
			Entities:    append([]domain.MessageEntity(nil), msg.Entities...),
			Media:       msg.Media,
			GroupedID:   msg.GroupedID,
		}
		reply := quickReplyByID(replies, shortcutID)
		kind := domain.QuickReplyMutationMessage
		if created {
			kind = domain.QuickReplyMutationNew
		}
		mutation = domain.QuickReplyMutation{
			Kind:       kind,
			List:       domain.QuickReplyList{OwnerUserID: ownerUserID, QuickReplies: replies, Messages: []domain.QuickReplyMessage{message}, Hash: postgresQuickReplyListHash(replies)},
			QuickReply: reply,
			ShortcutID: shortcutID,
			Message:    message,
		}
		return nil
	})
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	return mutation, nil
}

func (s *PasswordStore) GetQuickReplyMessages(ctx context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error) {
	if err := s.ensureQuickReplyExists(ctx, ownerUserID, shortcutID); err != nil {
		return domain.QuickReplyMessages{}, err
	}
	args := []any{ownerUserID, shortcutID}
	filter := ""
	if len(ids) > 0 {
		ids32 := make([]int32, len(ids))
		for i, id := range ids {
			ids32[i] = int32(id)
		}
		filter = " AND message_id = ANY($3::int[])"
		args = append(args, ids32)
	}
	rows, err := s.db.Query(ctx, `
SELECT message_id, random_id, message_date, body, COALESCE(entities::text, '[]')::text,
       media::text, grouped_id, edit_date
FROM quick_reply_messages
WHERE owner_user_id = $1
  AND shortcut_id = $2`+filter+`
ORDER BY message_id ASC`, args...)
	if err != nil {
		return domain.QuickReplyMessages{}, fmt.Errorf("list quick reply messages: %w", err)
	}
	defer rows.Close()
	out := domain.QuickReplyMessages{OwnerUserID: ownerUserID, ShortcutID: shortcutID}
	for rows.Next() {
		msg, err := scanQuickReplyMessage(ownerUserID, shortcutID, rows)
		if err != nil {
			return domain.QuickReplyMessages{}, err
		}
		out.Messages = append(out.Messages, msg)
	}
	if err := rows.Err(); err != nil {
		return domain.QuickReplyMessages{}, fmt.Errorf("scan quick reply messages: %w", err)
	}
	var total int
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*)::int FROM quick_reply_messages WHERE owner_user_id = $1 AND shortcut_id = $2`, ownerUserID, shortcutID).Scan(&total); err != nil {
		return domain.QuickReplyMessages{}, fmt.Errorf("count quick reply messages: %w", err)
	}
	if len(ids) > 0 && len(out.Messages) != len(ids) {
		return domain.QuickReplyMessages{}, domain.ErrShortcutInvalid
	}
	out.Count = total
	out.Hash = postgresQuickReplyMessagesHash(out.Messages)
	return out, nil
}

func (s *PasswordStore) RenameQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcutID int, shortcut string) (domain.QuickReplyMutation, error) {
	shortcut, err := domain.NormalizeQuickReplyShortcut(shortcut)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	tag, err := s.db.Exec(ctx, `
UPDATE quick_replies
SET shortcut = $3,
    updated_at = now()
WHERE owner_user_id = $1
  AND shortcut_id = $2`, ownerUserID, shortcutID, shortcut)
	if err != nil {
		return domain.QuickReplyMutation{}, quickReplyPGErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	list, err := s.ListQuickReplies(ctx, ownerUserID, true)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	return domain.QuickReplyMutation{Kind: domain.QuickReplyMutationList, List: list}, nil
}

func (s *PasswordStore) ReorderQuickReplies(ctx context.Context, ownerUserID int64, order []int) (domain.QuickReplyMutation, error) {
	err := withTx(ctx, s.db, "reorder quick replies", func(tx pgx.Tx) error {
		replies, err := listQuickRepliesTx(ctx, tx, ownerUserID)
		if err != nil {
			return err
		}
		if len(order) != len(replies) {
			return domain.ErrShortcutInvalid
		}
		seen := make(map[int]struct{}, len(order))
		byID := make(map[int]struct{}, len(replies))
		for _, reply := range replies {
			byID[reply.ID] = struct{}{}
		}
		for i, id := range order {
			if _, ok := byID[id]; !ok {
				return domain.ErrShortcutInvalid
			}
			if _, ok := seen[id]; ok {
				return domain.ErrShortcutInvalid
			}
			seen[id] = struct{}{}
			if _, err := tx.Exec(ctx, `UPDATE quick_replies SET sort_order = $3, updated_at = now() WHERE owner_user_id = $1 AND shortcut_id = $2`, ownerUserID, id, i+1); err != nil {
				return fmt.Errorf("update quick reply order: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	list, err := s.ListQuickReplies(ctx, ownerUserID, true)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	return domain.QuickReplyMutation{Kind: domain.QuickReplyMutationList, List: list}, nil
}

func (s *PasswordStore) DeleteQuickReplyShortcut(ctx context.Context, ownerUserID int64, shortcutID int) (domain.QuickReplyMutation, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM quick_replies WHERE owner_user_id = $1 AND shortcut_id = $2`, ownerUserID, shortcutID)
	if err != nil {
		return domain.QuickReplyMutation{}, fmt.Errorf("delete quick reply shortcut: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	_ = s.normalizeQuickReplyOrder(ctx, ownerUserID)
	list, err := s.ListQuickReplies(ctx, ownerUserID, true)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	return domain.QuickReplyMutation{Kind: domain.QuickReplyMutationDelete, List: list, ShortcutID: shortcutID}, nil
}

func (s *PasswordStore) DeleteQuickReplyMessages(ctx context.Context, ownerUserID int64, shortcutID int, ids []int) (domain.QuickReplyMutation, error) {
	if len(ids) == 0 {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	if err := s.ensureQuickReplyExists(ctx, ownerUserID, shortcutID); err != nil {
		return domain.QuickReplyMutation{}, err
	}
	ids32 := make([]int32, len(ids))
	for i, id := range ids {
		ids32[i] = int32(id)
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM quick_reply_messages WHERE owner_user_id = $1 AND shortcut_id = $2 AND message_id = ANY($3::int[])`, ownerUserID, shortcutID, ids32)
	if err != nil {
		return domain.QuickReplyMutation{}, fmt.Errorf("delete quick reply messages: %w", err)
	}
	if int(tag.RowsAffected()) != len(ids) {
		return domain.QuickReplyMutation{}, domain.ErrShortcutInvalid
	}
	list, err := s.ListQuickReplies(ctx, ownerUserID, true)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	return domain.QuickReplyMutation{Kind: domain.QuickReplyMutationIDs, List: list, ShortcutID: shortcutID, MessageIDs: append([]int(nil), ids...)}, nil
}

func (s *PasswordStore) ReserveBusinessAutomationDelivery(ctx context.Context, delivery domain.BusinessAutomationDelivery) (bool, error) {
	if delivery.OwnerUserID == 0 || delivery.PeerUserID == 0 || delivery.Kind == "" || delivery.TriggerMessageID == 0 {
		return false, domain.ErrBusinessProfileInvalid
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO business_automation_deliveries (
  owner_user_id, peer_user_id, kind, trigger_message_id, shortcut_id, sent_at, created_at
) VALUES ($1,$2,$3,$4,$5,$6,now())
ON CONFLICT (owner_user_id, peer_user_id, kind, trigger_message_id) DO NOTHING`,
		delivery.OwnerUserID, delivery.PeerUserID, string(delivery.Kind), delivery.TriggerMessageID, delivery.ShortcutID, delivery.SentAt)
	if err != nil {
		return false, fmt.Errorf("reserve business automation delivery: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *PasswordStore) LastBusinessAutomationDelivery(ctx context.Context, ownerUserID, peerUserID int64, kind domain.BusinessAutomationKind) (domain.BusinessAutomationDelivery, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT owner_user_id, peer_user_id, kind, trigger_message_id, shortcut_id, sent_at
FROM business_automation_deliveries
WHERE owner_user_id = $1
  AND peer_user_id = $2
  AND kind = $3
ORDER BY sent_at DESC, trigger_message_id DESC
LIMIT 1`, ownerUserID, peerUserID, string(kind))
	var out domain.BusinessAutomationDelivery
	var kindText string
	if err := row.Scan(&out.OwnerUserID, &out.PeerUserID, &kindText, &out.TriggerMessageID, &out.ShortcutID, &out.SentAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.BusinessAutomationDelivery{}, false, nil
		}
		return domain.BusinessAutomationDelivery{}, false, fmt.Errorf("last business automation delivery: %w", err)
	}
	out.Kind = domain.BusinessAutomationKind(kindText)
	return out, true, nil
}

func (s *PasswordStore) GetConnectedBusinessBot(ctx context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT owner_user_id, bot_user_id, COALESCE(recipients::text, '{}')::text, COALESCE(rights::text, '{}')::text,
       COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
       COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint
FROM business_connected_bots
WHERE owner_user_id = $1`, ownerUserID)
	bot, err := scanConnectedBusinessBot(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ConnectedBusinessBot{}, false, nil
		}
		return domain.ConnectedBusinessBot{}, false, fmt.Errorf("get connected business bot: %w", err)
	}
	return bot, true, nil
}

func (s *PasswordStore) SaveConnectedBusinessBot(ctx context.Context, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error) {
	recipients, err := json.Marshal(bot.Recipients)
	if err != nil {
		return domain.ConnectedBusinessBot{}, fmt.Errorf("encode connected business bot recipients: %w", err)
	}
	rights, err := json.Marshal(bot.Rights)
	if err != nil {
		return domain.ConnectedBusinessBot{}, fmt.Errorf("encode connected business bot rights: %w", err)
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO business_connected_bots (owner_user_id, bot_user_id, recipients, rights, created_at, updated_at)
VALUES ($1,$2,$3::jsonb,$4::jsonb,now(),now())
ON CONFLICT (owner_user_id) DO UPDATE SET
  bot_user_id = EXCLUDED.bot_user_id,
  recipients = EXCLUDED.recipients,
  rights = EXCLUDED.rights,
  updated_at = now()
RETURNING owner_user_id, bot_user_id, COALESCE(recipients::text, '{}')::text, COALESCE(rights::text, '{}')::text,
          COALESCE(EXTRACT(EPOCH FROM created_at), 0)::bigint,
          COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint`, bot.OwnerUserID, bot.BotUserID, string(recipients), string(rights))
	saved, err := scanConnectedBusinessBot(row)
	if err != nil {
		return domain.ConnectedBusinessBot{}, fmt.Errorf("save connected business bot: %w", err)
	}
	return saved, nil
}

func (s *PasswordStore) DeleteConnectedBusinessBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error) {
	deleted := false
	err := withTx(ctx, s.db, "delete connected business bot", func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM business_connected_bots WHERE owner_user_id = $1 AND bot_user_id = $2`, ownerUserID, botUserID)
		if err != nil {
			return fmt.Errorf("delete connected business bot: %w", err)
		}
		deleted = tag.RowsAffected() > 0
		if !deleted {
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM business_connected_bot_peer_states WHERE owner_user_id = $1`, ownerUserID); err != nil {
			return fmt.Errorf("delete connected business bot peer states: %w", err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return deleted, nil
}

func (s *PasswordStore) SetConnectedBusinessBotPaused(ctx context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error) {
	row := s.db.QueryRow(ctx, `
INSERT INTO business_connected_bot_peer_states (owner_user_id, peer_user_id, paused, disabled, created_at, updated_at)
VALUES ($1,$2,$3,false,now(),now())
ON CONFLICT (owner_user_id, peer_user_id) DO UPDATE SET
  paused = EXCLUDED.paused,
  updated_at = now()
RETURNING owner_user_id, peer_user_id, paused, disabled,
          COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint`, ownerUserID, peerUserID, paused)
	state, err := scanConnectedBusinessBotPeerState(row)
	if err != nil {
		return domain.ConnectedBusinessBotPeerState{}, fmt.Errorf("set connected business bot paused: %w", err)
	}
	return state, nil
}

func (s *PasswordStore) DisableConnectedBusinessBotForPeer(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error) {
	row := s.db.QueryRow(ctx, `
INSERT INTO business_connected_bot_peer_states (owner_user_id, peer_user_id, paused, disabled, created_at, updated_at)
VALUES ($1,$2,false,true,now(),now())
ON CONFLICT (owner_user_id, peer_user_id) DO UPDATE SET
  paused = false,
  disabled = true,
  updated_at = now()
RETURNING owner_user_id, peer_user_id, paused, disabled,
          COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint`, ownerUserID, peerUserID)
	state, err := scanConnectedBusinessBotPeerState(row)
	if err != nil {
		return domain.ConnectedBusinessBotPeerState{}, fmt.Errorf("disable connected business bot for peer: %w", err)
	}
	return state, nil
}

func (s *PasswordStore) GetConnectedBusinessBotPeerState(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error) {
	row := s.db.QueryRow(ctx, `
SELECT owner_user_id, peer_user_id, paused, disabled,
       COALESCE(EXTRACT(EPOCH FROM updated_at), 0)::bigint
FROM business_connected_bot_peer_states
WHERE owner_user_id = $1
  AND peer_user_id = $2`, ownerUserID, peerUserID)
	state, err := scanConnectedBusinessBotPeerState(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ConnectedBusinessBotPeerState{}, false, nil
		}
		return domain.ConnectedBusinessBotPeerState{}, false, fmt.Errorf("get connected business bot peer state: %w", err)
	}
	return state, true, nil
}

func (s *PasswordStore) listQuickReplies(ctx context.Context, ownerUserID int64) ([]domain.QuickReply, error) {
	return listQuickRepliesDB(ctx, s.db, ownerUserID)
}

func listQuickRepliesTx(ctx context.Context, tx pgx.Tx, ownerUserID int64) ([]domain.QuickReply, error) {
	return listQuickRepliesDB(ctx, tx, ownerUserID)
}

func listQuickRepliesDB(ctx context.Context, db interface {
	Query(context.Context, string, ...interface{}) (pgx.Rows, error)
}, ownerUserID int64) ([]domain.QuickReply, error) {
	rows, err := db.Query(ctx, `
SELECT qr.shortcut_id, qr.shortcut, qr.sort_order,
       COALESCE(MAX(qm.message_id), 0)::int AS top_message,
       COUNT(qm.message_id)::int AS message_count,
       COALESCE(EXTRACT(EPOCH FROM qr.created_at), 0)::bigint,
       COALESCE(EXTRACT(EPOCH FROM qr.updated_at), 0)::bigint
FROM quick_replies qr
LEFT JOIN quick_reply_messages qm
  ON qm.owner_user_id = qr.owner_user_id
 AND qm.shortcut_id = qr.shortcut_id
WHERE qr.owner_user_id = $1
GROUP BY qr.shortcut_id, qr.shortcut, qr.sort_order, qr.created_at, qr.updated_at
ORDER BY qr.sort_order ASC, qr.shortcut_id ASC
LIMIT $2`, ownerUserID, domain.MaxQuickReplies)
	if err != nil {
		return nil, fmt.Errorf("list quick replies: %w", err)
	}
	defer rows.Close()
	out := make([]domain.QuickReply, 0)
	for rows.Next() {
		var item domain.QuickReply
		item.OwnerUserID = ownerUserID
		if err := rows.Scan(&item.ID, &item.Shortcut, &item.SortOrder, &item.TopMessage, &item.Count, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan quick reply: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan quick replies: %w", err)
	}
	return out, nil
}

func ensureQuickReplyShortcut(ctx context.Context, tx pgx.Tx, ownerUserID int64, shortcut string) (int, bool, error) {
	var id int
	err := tx.QueryRow(ctx, `SELECT shortcut_id FROM quick_replies WHERE owner_user_id = $1 AND lower(shortcut) = lower($2)`, ownerUserID, shortcut).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("get quick reply shortcut: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*)::int FROM quick_replies WHERE owner_user_id = $1`, ownerUserID).Scan(&count); err != nil {
		return 0, false, fmt.Errorf("count quick replies: %w", err)
	}
	if count >= domain.MaxQuickReplies {
		return 0, false, domain.ErrQuickRepliesTooMuch
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(shortcut_id), 0)::int + 1 FROM quick_replies WHERE owner_user_id = $1`, ownerUserID).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("allocate quick reply shortcut id: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO quick_replies (owner_user_id, shortcut_id, shortcut, sort_order, created_at, updated_at)
VALUES ($1,$2,$3,$4,now(),now())`, ownerUserID, id, shortcut, count+1); err != nil {
		return 0, false, quickReplyPGErr(err)
	}
	return id, true, nil
}

func (s *PasswordStore) ensureQuickReplyExists(ctx context.Context, ownerUserID int64, shortcutID int) error {
	var id int
	err := s.db.QueryRow(ctx, `SELECT shortcut_id FROM quick_replies WHERE owner_user_id = $1 AND shortcut_id = $2`, ownerUserID, shortcutID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrShortcutInvalid
	}
	if err != nil {
		return fmt.Errorf("get quick reply shortcut: %w", err)
	}
	return nil
}

func (s *PasswordStore) normalizeQuickReplyOrder(ctx context.Context, ownerUserID int64) error {
	return withTx(ctx, s.db, "normalize quick reply order", func(tx pgx.Tx) error {
		replies, err := listQuickRepliesTx(ctx, tx, ownerUserID)
		if err != nil {
			return err
		}
		for i, reply := range replies {
			if _, err := tx.Exec(ctx, `UPDATE quick_replies SET sort_order = $3 WHERE owner_user_id = $1 AND shortcut_id = $2`, ownerUserID, reply.ID, i+1); err != nil {
				return fmt.Errorf("normalize quick reply order: %w", err)
			}
		}
		return nil
	})
}

func scanBusinessChatLink(row interface {
	Scan(dest ...any) error
}) (domain.BusinessChatLink, error) {
	var link domain.BusinessChatLink
	var entitiesJSON string
	if err := row.Scan(&link.Slug, &link.OwnerUserID, &link.Link, &link.Message, &entitiesJSON, &link.Title, &link.Views, &link.CreatedAt, &link.UpdatedAt); err != nil {
		return domain.BusinessChatLink{}, err
	}
	entities, err := decodeMessageEntities(entitiesJSON)
	if err != nil {
		return domain.BusinessChatLink{}, fmt.Errorf("decode business chat link entities: %w", err)
	}
	link.Entities = entities
	return link, nil
}

func scanQuickReplyMessage(ownerUserID int64, shortcutID int, row interface {
	Scan(dest ...any) error
}) (domain.QuickReplyMessage, error) {
	var msg domain.QuickReplyMessage
	var entitiesJSON, mediaJSON string
	msg.OwnerUserID = ownerUserID
	msg.ShortcutID = shortcutID
	if err := row.Scan(&msg.ID, &msg.RandomID, &msg.Date, &msg.Message, &entitiesJSON, &mediaJSON, &msg.GroupedID, &msg.EditDate); err != nil {
		return domain.QuickReplyMessage{}, fmt.Errorf("scan quick reply message: %w", err)
	}
	media, err := decodeMessageMedia(mediaJSON)
	if err != nil {
		return domain.QuickReplyMessage{}, fmt.Errorf("decode quick reply media: %w", err)
	}
	msg.Media = media
	entities, err := decodeMessageEntities(entitiesJSON)
	if err != nil {
		return domain.QuickReplyMessage{}, fmt.Errorf("decode quick reply entities: %w", err)
	}
	msg.Entities = entities
	return msg, nil
}

func scanConnectedBusinessBot(row interface {
	Scan(dest ...any) error
}) (domain.ConnectedBusinessBot, error) {
	var bot domain.ConnectedBusinessBot
	var recipientsJSON, rightsJSON string
	if err := row.Scan(&bot.OwnerUserID, &bot.BotUserID, &recipientsJSON, &rightsJSON, &bot.CreatedAtUnix, &bot.UpdatedAtUnix); err != nil {
		return domain.ConnectedBusinessBot{}, err
	}
	if recipientsJSON != "" && recipientsJSON != "{}" {
		if err := json.Unmarshal([]byte(recipientsJSON), &bot.Recipients); err != nil {
			return domain.ConnectedBusinessBot{}, fmt.Errorf("decode connected business bot recipients: %w", err)
		}
	}
	if rightsJSON != "" && rightsJSON != "{}" {
		if err := json.Unmarshal([]byte(rightsJSON), &bot.Rights); err != nil {
			return domain.ConnectedBusinessBot{}, fmt.Errorf("decode connected business bot rights: %w", err)
		}
	}
	return bot, nil
}

func scanConnectedBusinessBotPeerState(row interface {
	Scan(dest ...any) error
}) (domain.ConnectedBusinessBotPeerState, error) {
	var state domain.ConnectedBusinessBotPeerState
	if err := row.Scan(&state.OwnerUserID, &state.PeerUserID, &state.Paused, &state.Disabled, &state.UpdatedAtUnix); err != nil {
		return domain.ConnectedBusinessBotPeerState{}, err
	}
	return state, nil
}

func encodeNullableJSON[T any](value *T) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode business json: %w", err)
	}
	return raw, nil
}

func decodeNullableJSON[T any](raw string, out **T) error {
	if raw == "" || raw == "{}" {
		*out = nil
		return nil
	}
	var value T
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return err
	}
	*out = &value
	return nil
}

func quickReplyByID(replies []domain.QuickReply, id int) domain.QuickReply {
	for _, reply := range replies {
		if reply.ID == id {
			return reply
		}
	}
	return domain.QuickReply{}
}

func postgresQuickReplyListHash(items []domain.QuickReply) int64 {
	h := fnv.New64a()
	for _, item := range items {
		postgresWriteHashInt(h, item.ID)
		postgresWriteHashString(h, item.Shortcut)
		postgresWriteHashInt(h, item.TopMessage)
		postgresWriteHashInt(h, item.Count)
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func postgresQuickReplyMessagesHash(items []domain.QuickReplyMessage) int64 {
	h := fnv.New64a()
	for _, item := range items {
		postgresWriteHashInt(h, item.ID)
		postgresWriteHashString(h, item.Message)
		postgresWriteHashInt(h, item.Date)
	}
	return int64(h.Sum64() & 0x7fffffffffffffff)
}

func postgresWriteHashInt(h interface{ Write([]byte) (int, error) }, v int) {
	_, _ = h.Write([]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

func postgresWriteHashString(h interface{ Write([]byte) (int, error) }, v string) {
	_, _ = h.Write([]byte(v))
	_, _ = h.Write([]byte{0})
}

func quickReplyPGErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "quick_replies_owner_shortcut_lower_idx") {
		return domain.ErrShortcutOccupied
	}
	return err
}

// EditQuickReplyMessage changes one quick reply message in place.
func (s *PasswordStore) EditQuickReplyMessage(ctx context.Context, ownerUserID int64, edit domain.QuickReplyEdit) (domain.QuickReplyMutation, error) {
	entities, err := encodeMessageEntities(edit.Entities)
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	var mutation domain.QuickReplyMutation
	err = withTx(ctx, s.db, "edit quick reply message", func(tx pgx.Tx) error {
		var tag pgconn.CommandTag
		if edit.Media != nil {
			media, err := encodeMessageMedia(edit.Media)
			if err != nil {
				return err
			}
			tag, err = tx.Exec(ctx, `
UPDATE quick_reply_messages SET body = $4, entities = $5::jsonb, media = $6::jsonb, edit_date = $7, updated_at = now()
WHERE owner_user_id = $1 AND shortcut_id = $2 AND message_id = $3`,
				ownerUserID, edit.ShortcutID, edit.ID, edit.Message, string(entities), string(media), edit.EditDate)
			if err != nil {
				return fmt.Errorf("edit quick reply message: %w", err)
			}
		} else {
			tag, err = tx.Exec(ctx, `
UPDATE quick_reply_messages SET body = $4, entities = $5::jsonb, edit_date = $6, updated_at = now()
WHERE owner_user_id = $1 AND shortcut_id = $2 AND message_id = $3`,
				ownerUserID, edit.ShortcutID, edit.ID, edit.Message, string(entities), edit.EditDate)
			if err != nil {
				return fmt.Errorf("edit quick reply message: %w", err)
			}
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrShortcutInvalid
		}
		msg, err := scanQuickReplyMessage(ownerUserID, edit.ShortcutID, tx.QueryRow(ctx, `
SELECT message_id, random_id, message_date, body, COALESCE(entities::text, '[]')::text, media::text, grouped_id, edit_date
FROM quick_reply_messages WHERE owner_user_id = $1 AND shortcut_id = $2 AND message_id = $3`, ownerUserID, edit.ShortcutID, edit.ID))
		if err != nil {
			return err
		}
		replies, err := listQuickRepliesTx(ctx, tx, ownerUserID)
		if err != nil {
			return err
		}
		mutation = domain.QuickReplyMutation{
			Kind:       domain.QuickReplyMutationMessage,
			List:       domain.QuickReplyList{OwnerUserID: ownerUserID, QuickReplies: replies, Messages: []domain.QuickReplyMessage{msg}, Hash: postgresQuickReplyListHash(replies)},
			QuickReply: quickReplyByID(replies, edit.ShortcutID),
			ShortcutID: edit.ShortcutID,
			Message:    msg,
		}
		return nil
	})
	if err != nil {
		return domain.QuickReplyMutation{}, err
	}
	return mutation, nil
}
