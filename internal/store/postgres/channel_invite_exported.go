package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

func (s *ChannelStore) ExportInvite(ctx context.Context, req domain.ExportChannelInviteRequest) (domain.ExportChannelInviteResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 {
		return domain.ExportChannelInviteResult{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ExportChannelInviteResult{}, fmt.Errorf("export channel invite: db does not support transactions")
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ExportChannelInviteResult{}, fmt.Errorf("begin export channel invite: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	channel, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	if !canExportChannelInvite(member) {
		return domain.ExportChannelInviteResult{}, domain.ErrChannelAdminRequired
	}
	if req.LegacyRevokePermanent {
		if _, err := tx.Exec(ctx, `
UPDATE channel_invites
SET revoked = true, updated_at = now()
WHERE channel_id = $1 AND admin_user_id = $2 AND permanent AND NOT revoked`, req.ChannelID, req.UserID); err != nil {
			return domain.ExportChannelInviteResult{}, fmt.Errorf("revoke permanent channel invite: %w", err)
		}
	}
	inviteID, err := randomPositiveInt64()
	if err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	hash, err := randomInviteHash()
	if err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	invite := domain.ChannelInvite{
		ChannelID:     req.ChannelID,
		InviteID:      inviteID,
		Hash:          hash,
		AdminUserID:   req.UserID,
		Title:         req.Title,
		Permanent:     req.ExpireDate == 0 && req.UsageLimit == 0 && !req.RequestNeeded && req.Title == "",
		RequestNeeded: req.RequestNeeded,
		ExpireDate:    req.ExpireDate,
		UsageLimit:    req.UsageLimit,
		Date:          req.Date,
	}
	if err := insertChannelInviteTx(ctx, tx, invite); err != nil {
		return domain.ExportChannelInviteResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ExportChannelInviteResult{}, fmt.Errorf("commit export channel invite: %w", err)
	}
	committed = true
	return domain.ExportChannelInviteResult{Channel: channel, Invite: invite}, nil
}

// EnsurePermanentInvite idempotently returns the (channel, admin) permanent invite that is not currently revoked, creating it if missing.
// An advisory lock serializes concurrent ensures for the same channel, preventing duplicate primary links.
func (s *ChannelStore) EnsurePermanentInvite(ctx context.Context, channelID, adminUserID int64, date int) (domain.ChannelInvite, error) {
	if channelID == 0 || adminUserID == 0 {
		return domain.ChannelInvite{}, domain.ErrChannelInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ChannelInvite{}, fmt.Errorf("ensure permanent channel invite: db does not support transactions")
	}
	if date == 0 {
		date = nowUnix()
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ChannelInvite{}, fmt.Errorf("begin ensure permanent channel invite: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, channelID); err != nil {
		return domain.ChannelInvite{}, fmt.Errorf("lock ensure permanent channel invite: %w", err)
	}
	_, member, err := s.getChannelForMember(ctx, tx, adminUserID, channelID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	if !canExportChannelInvite(member) {
		return domain.ChannelInvite{}, domain.ErrChannelAdminRequired
	}
	rows, err := tx.Query(ctx, `
SELECT channel_id, invite_id, hash, admin_user_id, title, permanent, revoked, request_needed,
       COALESCE(expire_date, 0), COALESCE(usage_limit, 0), usage_count, requested_count,
       EXTRACT(EPOCH FROM created_at)::int
FROM channel_invites
WHERE channel_id = $1 AND admin_user_id = $2 AND permanent AND NOT revoked
ORDER BY EXTRACT(EPOCH FROM created_at)::int ASC, hash ASC
LIMIT 1`, channelID, adminUserID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	var existing *domain.ChannelInvite
	for rows.Next() {
		invite, err := scanChannelInvite(rows)
		if err != nil {
			rows.Close()
			return domain.ChannelInvite{}, err
		}
		existing = &invite
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.ChannelInvite{}, err
	}
	if existing != nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.ChannelInvite{}, fmt.Errorf("commit ensure permanent channel invite: %w", err)
		}
		committed = true
		return *existing, nil
	}
	inviteID, err := randomPositiveInt64()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	hash, err := randomInviteHash()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	invite := domain.ChannelInvite{
		ChannelID:   channelID,
		InviteID:    inviteID,
		Hash:        hash,
		AdminUserID: adminUserID,
		Permanent:   true,
		Date:        date,
	}
	if err := insertChannelInviteTx(ctx, tx, invite); err != nil {
		return domain.ChannelInvite{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ChannelInvite{}, fmt.Errorf("commit ensure permanent channel invite: %w", err)
	}
	committed = true
	return invite, nil
}

func (s *ChannelStore) ListExportedInvites(ctx context.Context, req domain.ChannelInviteListRequest) (domain.ChannelInviteList, error) {
	if req.UserID == 0 || req.ChannelID == 0 || req.AdminUserID == 0 {
		return domain.ChannelInviteList{}, domain.ErrChannelInvalid
	}
	_, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelInviteList{}, err
	}
	if !canExportChannelInvite(member) {
		return domain.ChannelInviteList{}, domain.ErrChannelAdminRequired
	}
	var total int
	if err := s.db.QueryRow(ctx, `
SELECT COUNT(*)::int
FROM channel_invites
WHERE channel_id = $1 AND admin_user_id = $2 AND revoked = $3`, req.ChannelID, req.AdminUserID, req.Revoked).Scan(&total); err != nil {
		return domain.ChannelInviteList{}, err
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelInviteListLimit {
		limit = domain.MaxChannelInviteListLimit
	}
	rows, err := s.db.Query(ctx, `
SELECT channel_id, invite_id, hash, admin_user_id, title, permanent, revoked, request_needed,
       COALESCE(expire_date, 0), COALESCE(usage_limit, 0), usage_count, requested_count,
       EXTRACT(EPOCH FROM created_at)::int
FROM channel_invites
WHERE channel_id = $1
  AND admin_user_id = $2
  AND revoked = $3
  AND (($4::int = 0 AND $5::text = '') OR (EXTRACT(EPOCH FROM created_at)::int, hash) < ($4, $5))
ORDER BY EXTRACT(EPOCH FROM created_at)::int DESC, hash DESC
LIMIT $6`, req.ChannelID, req.AdminUserID, req.Revoked, req.OffsetDate, req.OffsetHash, limit)
	if err != nil {
		return domain.ChannelInviteList{}, err
	}
	defer rows.Close()
	invites := make([]domain.ChannelInvite, 0, limit)
	for rows.Next() {
		invite, err := scanChannelInvite(rows)
		if err != nil {
			return domain.ChannelInviteList{}, err
		}
		invites = append(invites, invite)
	}
	if err := rows.Err(); err != nil {
		return domain.ChannelInviteList{}, err
	}
	return domain.ChannelInviteList{Count: total, Invites: invites}, nil
}

func (s *ChannelStore) getPermanentInviteForAdmin(ctx context.Context, db sqlcgen.DBTX, channelID, adminUserID int64) (domain.ChannelInvite, bool, error) {
	row := db.QueryRow(ctx, `
SELECT channel_id, invite_id, hash, admin_user_id, title, permanent, revoked, request_needed,
       COALESCE(expire_date, 0), COALESCE(usage_limit, 0), usage_count, requested_count,
       EXTRACT(EPOCH FROM created_at)::int
FROM channel_invites
WHERE channel_id = $1 AND admin_user_id = $2 AND permanent AND NOT revoked
ORDER BY EXTRACT(EPOCH FROM created_at)::int ASC, hash ASC
LIMIT 1`, channelID, adminUserID)
	invite, err := scanChannelInvite(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelInvite{}, false, nil
	}
	if err != nil {
		return domain.ChannelInvite{}, false, err
	}
	return invite, true, nil
}

func (s *ChannelStore) GetExportedInvite(ctx context.Context, req domain.GetChannelInviteRequest) (domain.ChannelInvite, error) {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.ChannelInvite{}, domain.ErrInviteHashEmpty
	}
	_, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID)
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	if !canExportChannelInvite(member) {
		return domain.ChannelInvite{}, domain.ErrChannelAdminRequired
	}
	return s.getInviteByChannelHash(ctx, s.db, req.ChannelID, req.Hash, false)
}

func (s *ChannelStore) EditExportedInvite(ctx context.Context, req domain.EditChannelInviteRequest) (domain.EditChannelInviteResult, error) {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.EditChannelInviteResult{}, domain.ErrInviteHashEmpty
	}
	if req.Date == 0 {
		req.Date = nowUnix()
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.EditChannelInviteResult{}, fmt.Errorf("edit channel invite: db does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.EditChannelInviteResult{}, fmt.Errorf("begin edit channel invite: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, member, err := s.getChannelForMember(ctx, tx, req.UserID, req.ChannelID); err != nil {
		return domain.EditChannelInviteResult{}, err
	} else if !canExportChannelInvite(member) {
		return domain.EditChannelInviteResult{}, domain.ErrChannelAdminRequired
	}
	invite, err := s.getInviteByChannelHash(ctx, tx, req.ChannelID, req.Hash, true)
	if err != nil {
		return domain.EditChannelInviteResult{}, err
	}
	if req.Revoked {
		if invite.Revoked {
			return domain.EditChannelInviteResult{}, domain.ErrInviteRevokedMissing
		}
		if _, err := tx.Exec(ctx, `UPDATE channel_invites SET revoked = true, updated_at = now() WHERE channel_id = $1 AND invite_id = $2`, invite.ChannelID, invite.InviteID); err != nil {
			return domain.EditChannelInviteResult{}, fmt.Errorf("revoke channel invite: %w", err)
		}
		invite.Revoked = true
		result := domain.EditChannelInviteResult{Invite: invite}
		if invite.Permanent {
			newInvite, err := s.newPostgresReplacementInvite(invite, req.Date)
			if err != nil {
				return domain.EditChannelInviteResult{}, err
			}
			if err := insertChannelInviteTx(ctx, tx, newInvite); err != nil {
				return domain.EditChannelInviteResult{}, err
			}
			result.NewInvite = &newInvite
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.EditChannelInviteResult{}, fmt.Errorf("commit edit channel invite: %w", err)
		}
		committed = true
		return result, nil
	}
	if invite.Revoked {
		return domain.EditChannelInviteResult{}, domain.ErrInviteRevokedMissing
	}
	if invite.Permanent && ((req.HasExpireDate && req.ExpireDate > 0) || (req.HasUsageLimit && req.UsageLimit > 0) || (req.HasRequestNeeded && req.RequestNeeded)) {
		return domain.EditChannelInviteResult{}, domain.ErrInvitePermanent
	}
	if req.HasExpireDate {
		invite.ExpireDate = req.ExpireDate
	}
	if req.HasUsageLimit {
		invite.UsageLimit = req.UsageLimit
	}
	if req.HasRequestNeeded {
		invite.RequestNeeded = req.RequestNeeded
	}
	if req.HasTitle {
		invite.Title = req.Title
	}
	invite.Permanent = invite.ExpireDate == 0 && invite.UsageLimit == 0 && !invite.RequestNeeded && invite.Title == ""
	if _, err := tx.Exec(ctx, `
UPDATE channel_invites
SET title = $3,
    expire_date = NULLIF($4, 0),
    usage_limit = NULLIF($5, 0),
    request_needed = $6,
    permanent = $7,
    updated_at = now()
WHERE channel_id = $1 AND invite_id = $2`,
		invite.ChannelID, invite.InviteID, invite.Title, invite.ExpireDate, invite.UsageLimit, invite.RequestNeeded, invite.Permanent); err != nil {
		return domain.EditChannelInviteResult{}, fmt.Errorf("update channel invite: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.EditChannelInviteResult{}, fmt.Errorf("commit edit channel invite: %w", err)
	}
	committed = true
	return domain.EditChannelInviteResult{Invite: invite}, nil
}

func (s *ChannelStore) DeleteExportedInvite(ctx context.Context, req domain.DeleteChannelInviteRequest) error {
	if req.UserID == 0 || req.ChannelID == 0 || strings.TrimSpace(req.Hash) == "" {
		return domain.ErrInviteHashEmpty
	}
	if _, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID); err != nil {
		return err
	} else if !canExportChannelInvite(member) {
		return domain.ErrChannelAdminRequired
	}
	tag, err := s.db.Exec(ctx, `
WITH deleted AS (
    DELETE FROM channel_invites
    WHERE channel_id = $1 AND hash = $2
    RETURNING hash
)
DELETE FROM channel_invite_hashes h USING deleted d WHERE h.hash = d.hash`, req.ChannelID, strings.TrimSpace(req.Hash))
	if err != nil {
		return fmt.Errorf("delete channel invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrInviteRevokedMissing
	}
	return nil
}

func (s *ChannelStore) DeleteRevokedExportedInvites(ctx context.Context, req domain.DeleteRevokedChannelInvitesRequest) error {
	if req.UserID == 0 || req.ChannelID == 0 || req.AdminUserID == 0 {
		return domain.ErrChannelInvalid
	}
	if _, member, err := s.getChannelForMember(ctx, s.db, req.UserID, req.ChannelID); err != nil {
		return err
	} else if !canExportChannelInvite(member) {
		return domain.ErrChannelAdminRequired
	}
	limit := req.Limit
	if limit <= 0 || limit > domain.MaxChannelHideJoinRequests {
		limit = domain.MaxChannelHideJoinRequests
	}
	// channel_invites is an ordinary non-partitioned table; first select up to LIMIT revoked invites
	// to delete in a subquery carrying channel_id by primary key (channel_id, invite_id), then delete by primary key and cascade-clean channel_invite_hashes.
	if _, err := s.db.Exec(ctx, `
WITH victims AS (
    SELECT channel_id, invite_id FROM channel_invites
    WHERE channel_id = $1 AND admin_user_id = $2 AND revoked
    ORDER BY updated_at ASC
    LIMIT $3
), deleted AS (
    DELETE FROM channel_invites ci
    USING victims v
    WHERE ci.channel_id = v.channel_id AND ci.invite_id = v.invite_id
    RETURNING ci.hash
)
DELETE FROM channel_invite_hashes h USING deleted d WHERE h.hash = d.hash`, req.ChannelID, req.AdminUserID, limit); err != nil {
		return fmt.Errorf("delete revoked channel invites: %w", err)
	}
	return nil
}

func (s *ChannelStore) ListAdminsWithInvites(ctx context.Context, userID, channelID int64) ([]domain.ChannelAdminInviteCount, error) {
	if userID == 0 || channelID == 0 {
		return nil, domain.ErrChannelInvalid
	}
	if _, member, err := s.getChannelForMember(ctx, s.db, userID, channelID); err != nil {
		return nil, err
	} else if !canExportChannelInvite(member) {
		return nil, domain.ErrChannelAdminRequired
	}
	rows, err := s.db.Query(ctx, `
SELECT admin_user_id,
       COUNT(*) FILTER (WHERE NOT revoked)::int,
       COUNT(*) FILTER (WHERE revoked)::int
FROM channel_invites
WHERE channel_id = $1
GROUP BY admin_user_id
ORDER BY admin_user_id ASC`, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ChannelAdminInviteCount, 0)
	for rows.Next() {
		var count domain.ChannelAdminInviteCount
		if err := rows.Scan(&count.AdminUserID, &count.InvitesCount, &count.RevokedInvitesCount); err != nil {
			return nil, err
		}
		out = append(out, count)
	}
	return out, rows.Err()
}

func insertChannelInviteTx(ctx context.Context, tx pgx.Tx, invite domain.ChannelInvite) error {
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_invites (
    channel_id, invite_id, hash, admin_user_id, title, permanent, revoked, request_needed,
    expire_date, usage_limit, usage_count, requested_count, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,0),NULLIF($10,0),$11,$12,to_timestamp($13),to_timestamp($13))`,
		invite.ChannelID, invite.InviteID, invite.Hash, invite.AdminUserID, invite.Title,
		invite.Permanent, invite.Revoked, invite.RequestNeeded, invite.ExpireDate,
		invite.UsageLimit, invite.UsageCount, invite.RequestedCount, invite.Date); err != nil {
		return fmt.Errorf("insert channel invite: %w", err)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO channel_invite_hashes (hash, channel_id, invite_id)
VALUES ($1,$2,$3)
ON CONFLICT (hash) DO UPDATE SET channel_id = EXCLUDED.channel_id, invite_id = EXCLUDED.invite_id, updated_at = now()`,
		invite.Hash, invite.ChannelID, invite.InviteID); err != nil {
		return fmt.Errorf("insert channel invite hash: %w", err)
	}
	return nil
}

func (s *ChannelStore) getInviteByHash(ctx context.Context, db sqlcgen.DBTX, hash string) (domain.Channel, domain.ChannelInvite, error) {
	return s.getInviteByHashLocked(ctx, db, hash, false)
}

func (s *ChannelStore) getInviteByHashForUpdate(ctx context.Context, tx pgx.Tx, hash string) (domain.Channel, domain.ChannelInvite, error) {
	return s.getInviteByHashLocked(ctx, tx, hash, true)
}

func (s *ChannelStore) getInviteByHashLocked(ctx context.Context, db sqlcgen.DBTX, hash string, forUpdate bool) (domain.Channel, domain.ChannelInvite, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE OF i"
	}
	row := db.QueryRow(ctx, `
SELECT `+channelColumns+`,
       i.channel_id, i.invite_id, i.hash, i.admin_user_id, i.title, i.permanent, i.revoked, i.request_needed,
       COALESCE(i.expire_date, 0), COALESCE(i.usage_limit, 0), i.usage_count, i.requested_count, EXTRACT(EPOCH FROM i.created_at)::int
FROM channel_invite_hashes h
JOIN channel_invites i ON i.channel_id = h.channel_id AND i.invite_id = h.invite_id
JOIN channels c ON c.id = i.channel_id AND NOT c.deleted
WHERE h.hash = $1 AND NOT i.revoked`+lockClause, hash)
	var ch domain.Channel
	var invite domain.ChannelInvite
	var rights, reactionPolicy string
	var wallpaper *string
	dest := append(channelScanDest(&ch, &rights, &reactionPolicy, &wallpaper),
		&invite.ChannelID, &invite.InviteID, &invite.Hash, &invite.AdminUserID, &invite.Title,
		&invite.Permanent, &invite.Revoked, &invite.RequestNeeded, &invite.ExpireDate,
		&invite.UsageLimit, &invite.UsageCount, &invite.RequestedCount, &invite.Date,
	)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Channel{}, domain.ChannelInvite{}, domain.ErrInviteHashInvalid
		}
		return domain.Channel{}, domain.ChannelInvite{}, err
	}
	finishChannelScan(&ch, rights, reactionPolicy, wallpaper)
	return ch, invite, nil
}

func (s *ChannelStore) getInviteByChannelHash(ctx context.Context, db sqlcgen.DBTX, channelID int64, hash string, forUpdate bool) (domain.ChannelInvite, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE"
	}
	row := db.QueryRow(ctx, `
SELECT channel_id, invite_id, hash, admin_user_id, title, permanent, revoked, request_needed,
       COALESCE(expire_date, 0), COALESCE(usage_limit, 0), usage_count, requested_count,
       EXTRACT(EPOCH FROM created_at)::int
FROM channel_invites
WHERE channel_id = $1 AND hash = $2`+lockClause, channelID, strings.TrimSpace(hash))
	invite, err := scanChannelInvite(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelInvite{}, domain.ErrInviteHashInvalid
	}
	return invite, err
}

func (s *ChannelStore) getInviteByID(ctx context.Context, db sqlcgen.DBTX, channelID, inviteID int64, forUpdate bool) (domain.ChannelInvite, error) {
	lockClause := ""
	if forUpdate {
		lockClause = " FOR UPDATE"
	}
	row := db.QueryRow(ctx, `
SELECT channel_id, invite_id, hash, admin_user_id, title, permanent, revoked, request_needed,
       COALESCE(expire_date, 0), COALESCE(usage_limit, 0), usage_count, requested_count,
       EXTRACT(EPOCH FROM created_at)::int
FROM channel_invites
WHERE channel_id = $1 AND invite_id = $2`+lockClause, channelID, inviteID)
	invite, err := scanChannelInvite(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChannelInvite{}, domain.ErrInviteHashInvalid
	}
	return invite, err
}

func scanChannelInvite(row rowScanner) (domain.ChannelInvite, error) {
	var invite domain.ChannelInvite
	err := row.Scan(
		&invite.ChannelID,
		&invite.InviteID,
		&invite.Hash,
		&invite.AdminUserID,
		&invite.Title,
		&invite.Permanent,
		&invite.Revoked,
		&invite.RequestNeeded,
		&invite.ExpireDate,
		&invite.UsageLimit,
		&invite.UsageCount,
		&invite.RequestedCount,
		&invite.Date,
	)
	return invite, err
}

func (s *ChannelStore) newPostgresReplacementInvite(old domain.ChannelInvite, date int) (domain.ChannelInvite, error) {
	inviteID, err := randomPositiveInt64()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	hash, err := randomInviteHash()
	if err != nil {
		return domain.ChannelInvite{}, err
	}
	if date == 0 {
		date = nowUnix()
	}
	return domain.ChannelInvite{
		ChannelID:   old.ChannelID,
		InviteID:    inviteID,
		Hash:        hash,
		AdminUserID: old.AdminUserID,
		Permanent:   old.Permanent,
		Date:        date,
	}, nil
}

func randomInviteHash() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("rand invite hash: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
