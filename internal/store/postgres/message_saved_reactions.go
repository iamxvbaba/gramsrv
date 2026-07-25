package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *MessageStore) setSavedMessageTags(ctx context.Context, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("set saved message tags: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("begin set saved message tags tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, req.UserID); err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("lock saved message tag owner: %w", err)
	}

	var boxID int32
	if err := tx.QueryRow(ctx, `
SELECT box_id
FROM message_boxes
WHERE owner_user_id = $1
  AND box_id = $2
  AND peer_type = 'user'
  AND peer_id = $1
  AND NOT deleted
LIMIT 1
FOR UPDATE`, req.UserID, int32(req.MessageID)).Scan(&boxID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
		}
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("get saved message for tags: %w", err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM saved_message_reaction_tags
WHERE user_id = $1 AND message_box_id = $2`, req.UserID, boxID); err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("delete old saved message tags: %w", err)
	}
	for i, reaction := range req.Reactions {
		if !reaction.Valid() {
			return domain.PrivateMessageReactionsResult{}, domain.ErrReactionInvalid
		}
		if _, err := tx.Exec(ctx, `
INSERT INTO saved_message_reaction_tags (
  user_id, message_box_id, reaction_type, reaction_value, chosen_order
) VALUES ($1, $2, $3, $4, $5)`,
			req.UserID, boxID, string(reaction.Type), reaction.Value(), int32(i+1)); err != nil {
			return domain.PrivateMessageReactionsResult{}, fmt.Errorf("insert saved message tag: %w", err)
		}
	}
	rows, err := sqlcgen.New(tx).GetMessageBoxesByIDs(ctx, sqlcgen.GetMessageBoxesByIDsParams{
		OwnerUserID: req.UserID,
		BoxIds:      []int32{boxID},
	})
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("reload saved message tags box: %w", err)
	}
	if len(rows) != 1 {
		return domain.PrivateMessageReactionsResult{}, domain.ErrMessageIDInvalid
	}
	msg, err := messageFromIDRow(rows[0])
	if err != nil {
		return domain.PrivateMessageReactionsResult{}, err
	}
	messages := []domain.Message{msg}
	if err := s.enrichPrivateMessageReactions(ctx, tx, req.UserID, messages); err != nil {
		return domain.PrivateMessageReactionsResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.PrivateMessageReactionsResult{}, fmt.Errorf("commit saved message tags tx: %w", err)
	}
	committed = true
	reactions := domain.ChannelMessageReactions{AsTags: true}
	if messages[0].Reactions != nil {
		reactions = *messages[0].Reactions
	}
	return domain.PrivateMessageReactionsResult{
		Messages:  messages,
		Reactions: reactions,
	}, nil
}

func (s *MessageStore) enrichSavedMessageTags(ctx context.Context, db sqlcgen.DBTX, messages []domain.Message) error {
	ownerIDs := make([]int64, 0, len(messages))
	boxIDs := make([]int32, 0, len(messages))
	indexes := make(map[[2]int64]int, len(messages))
	for i := range messages {
		msg := messages[i]
		if msg.OwnerUserID == 0 ||
			msg.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: msg.OwnerUserID}) {
			continue
		}
		ownerIDs = append(ownerIDs, msg.OwnerUserID)
		boxIDs = append(boxIDs, int32(msg.ID))
		indexes[[2]int64{msg.OwnerUserID, int64(msg.ID)}] = i
	}
	if len(ownerIDs) == 0 {
		return nil
	}
	rows, err := db.Query(ctx, `
WITH wanted AS (
  SELECT user_id, message_box_id
  FROM unnest($1::bigint[], $2::int[]) AS w(user_id, message_box_id)
)
SELECT t.user_id, t.message_box_id, t.reaction_type, t.reaction_value, t.chosen_order
FROM saved_message_reaction_tags t
JOIN wanted w
  ON w.user_id = t.user_id
 AND w.message_box_id = t.message_box_id
ORDER BY t.user_id, t.message_box_id, t.chosen_order, t.reaction_type, t.reaction_value`,
		ownerIDs, boxIDs)
	if err != nil {
		return fmt.Errorf("load saved message tags: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			userID        int64
			messageBoxID  int32
			reactionType  string
			reactionValue string
			chosenOrder   int32
		)
		if err := rows.Scan(&userID, &messageBoxID, &reactionType, &reactionValue, &chosenOrder); err != nil {
			return fmt.Errorf("scan saved message tag: %w", err)
		}
		reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(reactionType), reactionValue)
		if !ok {
			continue
		}
		index, ok := indexes[[2]int64{userID, int64(messageBoxID)}]
		if !ok {
			continue
		}
		if messages[index].Reactions == nil {
			messages[index].Reactions = &domain.ChannelMessageReactions{
				AsTags:  true,
				Results: []domain.ChannelMessageReactionCount{},
				Recent:  []domain.ChannelMessagePeerReaction{},
			}
		}
		messages[index].Reactions.Results = append(messages[index].Reactions.Results, domain.ChannelMessageReactionCount{
			Reaction:    reaction,
			Count:       1,
			ChosenOrder: int(chosenOrder),
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("saved message tag rows: %w", err)
	}
	return nil
}

func (s *MessageStore) ListSavedReactionTags(ctx context.Context, req domain.SavedReactionTagsRequest) ([]domain.SavedReactionTag, error) {
	if req.UserID == 0 {
		return nil, domain.ErrReactionInvalid
	}
	if req.Limit <= 0 || req.Limit > domain.MaxSavedReactionTags {
		req.Limit = domain.MaxSavedReactionTags
	}
	savedPeerType := ""
	var savedPeerID int64
	if req.SavedPeer.ID != 0 {
		savedPeerType = string(req.SavedPeer.Type)
		savedPeerID = req.SavedPeer.ID
	}
	rows, err := s.db.Query(ctx, `
SELECT
  a.reaction_type,
  a.reaction_value,
  CASE WHEN $2 = '' THEN COALESCE(t.title, '') ELSE '' END AS title,
  COUNT(*)::int AS reaction_count
FROM saved_message_reaction_tags a
JOIN message_boxes m
  ON m.owner_user_id = a.user_id
 AND m.box_id = a.message_box_id
 AND NOT m.deleted
 AND m.peer_type = 'user'
 AND m.peer_id = a.user_id
LEFT JOIN user_saved_reaction_tags t
  ON t.user_id = a.user_id
 AND t.reaction_type = a.reaction_type
 AND t.reaction_value = a.reaction_value
WHERE a.user_id = $1
  AND ($2 = '' OR (m.saved_peer_type = $2 AND m.saved_peer_id = $3))
GROUP BY a.reaction_type, a.reaction_value, title
ORDER BY
  reaction_count DESC,
  CASE
    WHEN a.reaction_type = 'custom_emoji'
      THEN lpad(to_hex(a.reaction_value::bigint), 16, '0')
    ELSE substr(md5(replace(a.reaction_value, U&'\FE0F', '')), 1, 16)
  END DESC
LIMIT $4`, req.UserID, savedPeerType, savedPeerID, int32(req.Limit))
	if err != nil {
		return nil, fmt.Errorf("list saved reaction tags: %w", err)
	}
	defer rows.Close()
	out := make([]domain.SavedReactionTag, 0, req.Limit)
	for rows.Next() {
		var reactionType, reactionValue, title string
		var count int32
		if err := rows.Scan(&reactionType, &reactionValue, &title, &count); err != nil {
			return nil, fmt.Errorf("scan saved reaction tag: %w", err)
		}
		reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(reactionType), reactionValue)
		if !ok || count <= 0 {
			continue
		}
		out = append(out, domain.SavedReactionTag{
			UserID:   req.UserID,
			Reaction: reaction,
			Title:    title,
			Count:    int(count),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("saved reaction tag rows: %w", err)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Reaction.Key() > out[j].Reaction.Key()
	})
	return out, nil
}

func (s *MessageStore) UpsertSavedReactionTag(ctx context.Context, tag domain.SavedReactionTag) error {
	if tag.UserID == 0 || !tag.Reaction.Valid() || utf8.RuneCountInString(tag.Title) > 12 {
		return domain.ErrReactionInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return fmt.Errorf("update saved reaction tag title: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update saved reaction tag title tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := lockUsersForUpdate(ctx, tx, tag.UserID); err != nil {
		return fmt.Errorf("lock saved reaction tag owner: %w", err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM saved_message_reaction_tags a
  JOIN message_boxes m
    ON m.owner_user_id = a.user_id
   AND m.box_id = a.message_box_id
   AND NOT m.deleted
   AND m.peer_type = 'user'
   AND m.peer_id = a.user_id
  WHERE a.user_id = $1
    AND a.reaction_type = $2
    AND a.reaction_value = $3
)`, tag.UserID, string(tag.Reaction.Type), tag.Reaction.Value()).Scan(&exists); err != nil {
		return fmt.Errorf("check saved reaction tag assignment: %w", err)
	}
	if !exists {
		return domain.ErrReactionInvalid
	}
	if tag.Title == "" {
		if _, err := tx.Exec(ctx, `
DELETE FROM user_saved_reaction_tags
WHERE user_id = $1 AND reaction_type = $2 AND reaction_value = $3`,
			tag.UserID, string(tag.Reaction.Type), tag.Reaction.Value()); err != nil {
			return fmt.Errorf("delete saved reaction tag title: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `
INSERT INTO user_saved_reaction_tags (
  user_id, reaction_type, reaction_value, title, reaction_count
) VALUES ($1, $2, $3, $4, 0)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET title = EXCLUDED.title, reaction_count = 0, updated_at = now()`,
		tag.UserID, string(tag.Reaction.Type), tag.Reaction.Value(), tag.Title); err != nil {
		return fmt.Errorf("upsert saved reaction tag title: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit saved reaction tag title tx: %w", err)
	}
	committed = true
	return nil
}
