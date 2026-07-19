package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
)

func (s *MessageStore) GetPrivateHistoryTTL(ctx context.Context, ownerUserID int64, peer domain.Peer) (int, error) {
	if ownerUserID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return 0, fmt.Errorf("private history ttl: invalid peer")
	}
	return privateHistoryTTLPeriod(ctx, s.db, ownerUserID, peer.ID)
}

func (s *MessageStore) SetPrivateHistoryTTL(ctx context.Context, ownerUserID int64, peer domain.Peer, period int) error {
	if ownerUserID == 0 || peer.Type != domain.PeerTypeUser || peer.ID == 0 {
		return fmt.Errorf("set private history ttl: invalid peer")
	}
	if period < 0 {
		return fmt.Errorf("set private history ttl: invalid period")
	}
	return withTx(ctx, s.db, "set private history ttl", func(tx pgx.Tx) error {
		if err := lockUsersForUpdate(ctx, tx, ownerUserID, peer.ID); err != nil {
			return fmt.Errorf("lock private ttl users: %w", err)
		}
		if err := upsertPrivateDialogTTL(ctx, tx, ownerUserID, peer.ID, period); err != nil {
			return err
		}
		if peer.ID != ownerUserID {
			if err := upsertPrivateDialogTTL(ctx, tx, peer.ID, ownerUserID, period); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *MessageStore) DefaultHistoryTTL(ctx context.Context, userID int64) (int, error) {
	if userID == 0 {
		return 0, nil
	}
	var period int
	err := s.db.QueryRow(ctx, `SELECT COALESCE(default_history_ttl_period, 0)::int FROM users WHERE id = $1 AND deleted_at IS NULL`, userID).Scan(&period)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get default history ttl: %w", err)
	}
	if period < 0 {
		return 0, nil
	}
	return period, nil
}

func (s *MessageStore) SetDefaultHistoryTTL(ctx context.Context, userID int64, period int) error {
	if userID == 0 {
		return nil
	}
	if period < 0 {
		return fmt.Errorf("set default history ttl: invalid period")
	}
	tag, err := s.db.Exec(ctx, `
UPDATE users
SET default_history_ttl_period = $2,
    updated_at = now()
WHERE id = $1 AND deleted_at IS NULL
`, userID, period)
	if err != nil {
		return fmt.Errorf("set default history ttl: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set default history ttl: user not found")
	}
	return nil
}

func (s *MessageStore) ClaimExpiredPrivateMessages(ctx context.Context, now, limit int) ([]domain.DeleteMessagesRequest, error) {
	if now <= 0 || limit <= 0 {
		return nil, nil
	}
	if limit > domain.MaxDeleteHistoryBatch {
		limit = domain.MaxDeleteHistoryBatch
	}
	rows, err := s.db.Query(ctx, `
WITH due AS (
  SELECT DISTINCT ON (m.message_sender_id, m.private_message_id)
    m.owner_user_id,
    m.peer_id,
    m.box_id,
    m.expires_at
  FROM message_boxes m
  WHERE m.expires_at > 0
    AND m.expires_at <= $1
    AND NOT m.deleted
  ORDER BY m.message_sender_id, m.private_message_id, m.expires_at ASC, m.owner_user_id ASC
  LIMIT $2
)
SELECT owner_user_id, peer_id, box_id
FROM due
ORDER BY owner_user_id ASC, peer_id ASC, box_id ASC
`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim expired private messages: %w", err)
	}
	defer rows.Close()
	type key struct {
		owner int64
		peer  int64
	}
	index := make(map[key]int)
	out := make([]domain.DeleteMessagesRequest, 0)
	for rows.Next() {
		var owner, peer int64
		var id int
		if err := rows.Scan(&owner, &peer, &id); err != nil {
			return nil, fmt.Errorf("scan expired private message: %w", err)
		}
		k := key{owner: owner, peer: peer}
		pos, ok := index[k]
		if !ok {
			pos = len(out)
			index[k] = pos
			out = append(out, domain.DeleteMessagesRequest{
				OwnerUserID: owner,
				IDs:         make([]int, 0, 8),
				Revoke:      true,
				Date:        now,
			})
		}
		out[pos].IDs = append(out[pos].IDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired private messages: %w", err)
	}
	return out, nil
}

func upsertPrivateDialogTTL(ctx context.Context, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, ownerUserID, peerUserID int64, period int) error {
	_, err := db.Exec(ctx, `
INSERT INTO dialogs (user_id, peer_type, peer_id, ttl_period)
VALUES ($1, 'user', $2, $3)
ON CONFLICT (user_id, peer_type, peer_id) DO UPDATE SET
  ttl_period = EXCLUDED.ttl_period,
  updated_at = now()
`, ownerUserID, peerUserID, period)
	if err != nil {
		return fmt.Errorf("upsert private dialog ttl: %w", err)
	}
	return nil
}
