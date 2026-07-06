package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type ChatlistStore struct {
	db sqlcgen.DBTX
}

func NewChatlistStore(db sqlcgen.DBTX) *ChatlistStore {
	return &ChatlistStore{db: db}
}

func (s *ChatlistStore) CountInvites(ctx context.Context, ownerUserID int64, filterID int) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM chatlist_invites
WHERE owner_user_id = $1 AND filter_id = $2 AND NOT deleted`, ownerUserID, filterID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count chatlist invites: %w", err)
	}
	return count, nil
}

func (s *ChatlistStore) CountActiveInvites(ctx context.Context, ownerUserID int64, filterID int) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM chatlist_invites
WHERE owner_user_id = $1 AND filter_id = $2 AND NOT deleted AND NOT revoked`, ownerUserID, filterID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active chatlist invites: %w", err)
	}
	return count, nil
}

func (s *ChatlistStore) SaveInvite(ctx context.Context, invite domain.ChatlistInvite) (domain.ChatlistInvite, error) {
	peers, err := json.Marshal(invite.Peers)
	if err != nil {
		return domain.ChatlistInvite{}, fmt.Errorf("marshal chatlist invite peers: %w", err)
	}
	row := s.db.QueryRow(ctx, `
INSERT INTO chatlist_invites (owner_user_id, filter_id, slug, title, peers, revoked, deleted, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5::jsonb, $6, false,
        CASE WHEN $7::int > 0 THEN to_timestamp($7::int) ELSE now() END, now())
ON CONFLICT (slug) DO UPDATE SET
  title = EXCLUDED.title,
  peers = EXCLUDED.peers,
  revoked = EXCLUDED.revoked,
  deleted = false,
  updated_at = now()
WHERE chatlist_invites.owner_user_id = EXCLUDED.owner_user_id
  AND chatlist_invites.filter_id = EXCLUDED.filter_id
RETURNING id, owner_user_id, filter_id, slug, title, peers::text, revoked, deleted,
          EXTRACT(EPOCH FROM created_at)::int`, invite.OwnerUserID, invite.FilterID, invite.Slug, invite.Title, peers, invite.Revoked, invite.Date)
	out, err := scanChatlistInvite(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChatlistInvite{}, domain.ErrChatlistSlugOccupied
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ChatlistInvite{}, domain.ErrChatlistSlugOccupied
		}
		return domain.ChatlistInvite{}, fmt.Errorf("save chatlist invite: %w", err)
	}
	return out, nil
}

func (s *ChatlistStore) GetInvite(ctx context.Context, ownerUserID int64, filterID int, slug string) (domain.ChatlistInvite, bool, error) {
	invite, err := scanChatlistInvite(s.db.QueryRow(ctx, `
SELECT id, owner_user_id, filter_id, slug, title, peers::text, revoked, deleted,
       EXTRACT(EPOCH FROM created_at)::int
FROM chatlist_invites
WHERE owner_user_id = $1 AND filter_id = $2 AND slug = $3 AND NOT deleted`, ownerUserID, filterID, slug))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChatlistInvite{}, false, nil
		}
		return domain.ChatlistInvite{}, false, fmt.Errorf("get chatlist invite: %w", err)
	}
	return invite, true, nil
}

func (s *ChatlistStore) GetInviteBySlug(ctx context.Context, slug string) (domain.ChatlistInvite, bool, error) {
	invite, err := scanChatlistInvite(s.db.QueryRow(ctx, `
SELECT id, owner_user_id, filter_id, slug, title, peers::text, revoked, deleted,
       EXTRACT(EPOCH FROM created_at)::int
FROM chatlist_invites
WHERE slug = $1 AND NOT deleted AND NOT revoked`, slug))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChatlistInvite{}, false, nil
		}
		return domain.ChatlistInvite{}, false, fmt.Errorf("get chatlist invite by slug: %w", err)
	}
	return invite, true, nil
}

func (s *ChatlistStore) ListInvites(ctx context.Context, ownerUserID int64, filterID int) ([]domain.ChatlistInvite, error) {
	rows, err := s.db.Query(ctx, `
SELECT id, owner_user_id, filter_id, slug, title, peers::text, revoked, deleted,
       EXTRACT(EPOCH FROM created_at)::int
FROM chatlist_invites
WHERE owner_user_id = $1 AND filter_id = $2 AND NOT deleted
ORDER BY created_at ASC, slug ASC`, ownerUserID, filterID)
	if err != nil {
		return nil, fmt.Errorf("list chatlist invites: %w", err)
	}
	defer rows.Close()
	out := make([]domain.ChatlistInvite, 0)
	for rows.Next() {
		invite, err := scanChatlistInvite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, invite)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chatlist invites rows: %w", err)
	}
	return out, nil
}

func (s *ChatlistStore) DeleteInvite(ctx context.Context, ownerUserID int64, filterID int, slug string) (bool, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE chatlist_invites
SET deleted = true, updated_at = now()
WHERE owner_user_id = $1 AND filter_id = $2 AND slug = $3 AND NOT deleted`, ownerUserID, filterID, slug)
	if err != nil {
		return false, fmt.Errorf("delete chatlist invite: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *ChatlistStore) CountMemberships(ctx context.Context, userID int64) (int, error) {
	var count int
	if err := s.db.QueryRow(ctx, `
SELECT count(*)::int
FROM chatlist_memberships
WHERE user_id = $1`, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count chatlist memberships: %w", err)
	}
	return count, nil
}

func (s *ChatlistStore) SaveMembership(ctx context.Context, membership domain.ChatlistMembership) error {
	if membership.Date == 0 {
		membership.Date = nowUnix()
	}
	exec := func(ctx context.Context, db sqlcgen.DBTX) error {
		if _, err := db.Exec(ctx, `
DELETE FROM chatlist_memberships
WHERE user_id = $1 AND slug = $2 AND local_filter_id <> $3`, membership.UserID, membership.Slug, membership.LocalFilterID); err != nil {
			return fmt.Errorf("dedupe chatlist membership: %w", err)
		}
		if _, err := db.Exec(ctx, `
INSERT INTO chatlist_memberships (
  user_id, local_filter_id, owner_user_id, owner_filter_id, slug, hidden_updates, joined_at, updated_at
) VALUES (
  $1, $2, $3, $4, $5, $6,
  CASE WHEN $7::int > 0 THEN to_timestamp($7::int) ELSE now() END,
  now()
)
ON CONFLICT (user_id, local_filter_id) DO UPDATE SET
  owner_user_id = EXCLUDED.owner_user_id,
  owner_filter_id = EXCLUDED.owner_filter_id,
  slug = EXCLUDED.slug,
  hidden_updates = EXCLUDED.hidden_updates,
  updated_at = now()`, membership.UserID, membership.LocalFilterID, membership.OwnerUserID, membership.OwnerFilterID, membership.Slug, membership.HiddenUpdates, membership.Date); err != nil {
			return fmt.Errorf("save chatlist membership: %w", err)
		}
		return nil
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return exec(ctx, s.db)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin save chatlist membership: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if err := exec(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit save chatlist membership: %w", err)
	}
	committed = true
	return nil
}

func (s *ChatlistStore) GetMembershipBySlug(ctx context.Context, userID int64, slug string) (domain.ChatlistMembership, bool, error) {
	membership, err := scanChatlistMembership(s.db.QueryRow(ctx, `
SELECT user_id, local_filter_id, owner_user_id, owner_filter_id, slug, hidden_updates,
       EXTRACT(EPOCH FROM joined_at)::int
FROM chatlist_memberships
WHERE user_id = $1 AND slug = $2`, userID, slug))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChatlistMembership{}, false, nil
		}
		return domain.ChatlistMembership{}, false, fmt.Errorf("get chatlist membership by slug: %w", err)
	}
	return membership, true, nil
}

func (s *ChatlistStore) GetMembershipByLocalFilter(ctx context.Context, userID int64, localFilterID int) (domain.ChatlistMembership, bool, error) {
	membership, err := scanChatlistMembership(s.db.QueryRow(ctx, `
SELECT user_id, local_filter_id, owner_user_id, owner_filter_id, slug, hidden_updates,
       EXTRACT(EPOCH FROM joined_at)::int
FROM chatlist_memberships
WHERE user_id = $1 AND local_filter_id = $2`, userID, localFilterID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ChatlistMembership{}, false, nil
		}
		return domain.ChatlistMembership{}, false, fmt.Errorf("get chatlist membership by local filter: %w", err)
	}
	return membership, true, nil
}

func (s *ChatlistStore) DeleteMembershipByLocalFilter(ctx context.Context, userID int64, localFilterID int) (bool, error) {
	tag, err := s.db.Exec(ctx, `
DELETE FROM chatlist_memberships
WHERE user_id = $1 AND local_filter_id = $2`, userID, localFilterID)
	if err != nil {
		return false, fmt.Errorf("delete chatlist membership: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *ChatlistStore) SetMembershipHidden(ctx context.Context, userID int64, localFilterID int, hidden bool) (bool, error) {
	tag, err := s.db.Exec(ctx, `
UPDATE chatlist_memberships
SET hidden_updates = $3, updated_at = now()
WHERE user_id = $1 AND local_filter_id = $2 AND hidden_updates IS DISTINCT FROM $3`, userID, localFilterID, hidden)
	if err != nil {
		return false, fmt.Errorf("set chatlist membership hidden: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func scanChatlistInvite(row rowScanner) (domain.ChatlistInvite, error) {
	var invite domain.ChatlistInvite
	var peersJSON string
	if err := row.Scan(&invite.ID, &invite.OwnerUserID, &invite.FilterID, &invite.Slug, &invite.Title, &peersJSON, &invite.Revoked, &invite.Deleted, &invite.Date); err != nil {
		return domain.ChatlistInvite{}, err
	}
	if peersJSON != "" {
		if err := json.Unmarshal([]byte(peersJSON), &invite.Peers); err != nil {
			return domain.ChatlistInvite{}, fmt.Errorf("decode chatlist invite peers: %w", err)
		}
	}
	return invite, nil
}

func scanChatlistMembership(row rowScanner) (domain.ChatlistMembership, error) {
	var membership domain.ChatlistMembership
	if err := row.Scan(&membership.UserID, &membership.LocalFilterID, &membership.OwnerUserID, &membership.OwnerFilterID, &membership.Slug, &membership.HiddenUpdates, &membership.Date); err != nil {
		return domain.ChatlistMembership{}, err
	}
	return membership, nil
}
