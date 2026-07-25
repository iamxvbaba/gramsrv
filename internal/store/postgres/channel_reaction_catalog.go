package postgres

import (
	"context"
	"fmt"
	"strings"
	"telesrv/internal/domain"
)

func (s *ChannelStore) ListTopMessageReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 {
		return []domain.MessageReaction{}, nil
	}
	if limit > domain.MaxTopMessageReactions {
		limit = domain.MaxTopMessageReactions
	}
	rows, err := s.db.Query(ctx, `
SELECT reaction_type, reaction_value
FROM user_top_reactions
WHERE user_id = $1
ORDER BY reaction_count DESC, reaction_date DESC, updated_at DESC, reaction_type ASC, reaction_value ASC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list top message reactions: %w", err)
	}
	defer rows.Close()
	out := make([]domain.MessageReaction, 0, limit)
	for rows.Next() {
		var reactionType, reactionValue string
		if err := rows.Scan(&reactionType, &reactionValue); err != nil {
			return nil, err
		}
		if reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(reactionType), reactionValue); ok {
			out = append(out, reaction)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ChannelStore) ListRecentMessageReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error) {
	if userID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if limit <= 0 {
		return []domain.MessageReaction{}, nil
	}
	if limit > domain.MaxRecentMessageReactions {
		limit = domain.MaxRecentMessageReactions
	}
	rows, err := s.db.Query(ctx, `
SELECT reaction_type, reaction_value
FROM user_recent_reactions
WHERE user_id = $1
ORDER BY reaction_date DESC, updated_at DESC, reaction_type ASC, reaction_value ASC
LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent message reactions: %w", err)
	}
	defer rows.Close()
	out := make([]domain.MessageReaction, 0, limit)
	for rows.Next() {
		var reactionType, reactionValue string
		if err := rows.Scan(&reactionType, &reactionValue); err != nil {
			return nil, err
		}
		if reaction, ok := domain.MessageReactionFromValue(domain.MessageReactionType(reactionType), reactionValue); ok {
			out = append(out, reaction)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ChannelStore) RecordMessageReactionUse(ctx context.Context, userID int64, reactions []domain.MessageReaction, addToRecent bool, date int) error {
	if userID == 0 || len(reactions) == 0 {
		return nil
	}
	if date <= 0 {
		date = nowUnix()
	}
	for _, reaction := range reactions {
		if reaction.Type != domain.MessageReactionEmoji || strings.TrimSpace(reaction.Emoticon) == "" || len(reaction.Emoticon) > domain.MaxChannelReactionEmoticonLength {
			continue
		}
		if addToRecent {
			if _, err := s.db.Exec(ctx, `
INSERT INTO user_recent_reactions (user_id, reaction_type, reaction_value, reaction_date)
VALUES ($1,$2,$3,$4)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET reaction_date = EXCLUDED.reaction_date, updated_at = now()`,
				userID, string(reaction.Type), reaction.Value(), date); err != nil {
				return fmt.Errorf("record recent message reaction: %w", err)
			}
		}
		if _, err := s.db.Exec(ctx, `
INSERT INTO user_top_reactions (user_id, reaction_type, reaction_value, reaction_count, reaction_date)
VALUES ($1,$2,$3,1,$4)
ON CONFLICT (user_id, reaction_type, reaction_value)
DO UPDATE SET reaction_count = user_top_reactions.reaction_count + 1, reaction_date = EXCLUDED.reaction_date, updated_at = now()`,
			userID, string(reaction.Type), reaction.Value(), date); err != nil {
			return fmt.Errorf("record top message reaction: %w", err)
		}
	}
	return nil
}

func (s *ChannelStore) ClearRecentMessageReactions(ctx context.Context, userID int64) error {
	if userID == 0 {
		return domain.ErrChannelInvalid
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM user_recent_reactions WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clear recent message reactions: %w", err)
	}
	return nil
}
