package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

const (
	accountSearchLimit      = 20
	accountListDefaultLimit = 50
	accountListMaxLimit     = 100
	channelSearchLimit      = 50
	channelListDefaultLimit = 50
	channelListMaxLimit     = 100
	messagePageLimit        = 100
)

type readStore struct {
	pool *pgxpool.Pool
}

func newReadStore(pool *pgxpool.Pool) *readStore {
	return &readStore{pool: pool}
}

type AccountRow struct {
	ID           int64
	Phone        string
	Username     string
	FirstName    string
	LastName     string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Frozen       bool
	Reason       string
	Verified     bool
	PremiumUntil int64
	LastActiveAt time.Time
	DeviceCount  int
}

type AccountDetail struct {
	Account        AccountRow
	About          string
	LastSeenAt     int64
	Verified       bool
	Support        bool
	Bot            bool
	StarsBalance   int64
	StarsGranted   bool
	Restriction    RestrictionRow
	HasRestriction bool
	Authorizations []AuthorizationRow
	AuditLogs      []AuditLogRow
}

type RestrictionRow struct {
	Frozen    bool
	Since     *time.Time
	Until     *time.Time
	AppealURL string
	Reason    string
	Actor     string
	CommandID string
	UpdatedAt time.Time
}

type AuthorizationRow struct {
	AuthKeyID       int64
	Hash            int64
	Layer           int
	DeviceModel     string
	Platform        string
	SystemVersion   string
	APIID           int
	AppVersion      string
	IP              string
	PasswordPending bool
	CreatedAt       time.Time
	ActiveAt        time.Time
}

type AuditLogRow struct {
	ID        int64
	CommandID string
	Actor     string
	Action    string
	DryRun    bool
	Reason    string
	Status    string
	Error     string
	Result    string
	CreatedAt time.Time
}

type ChannelRow struct {
	ID                int64
	AccessHash        int64
	CreatorUserID     int64
	Title             string
	About             string
	Username          string
	Broadcast         bool
	Megagroup         bool
	Forum             bool
	Monoforum         bool
	Verified          bool
	Deleted           bool
	ParticipantsCount int
	AdminsCount       int
	KickedCount       int
	BannedCount       int
	TopMessageID      int
	PinnedMessageID   int
	PTS               int
	Date              int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type ChannelDetail struct {
	Channel     ChannelRow
	ChannelJSON string
	AuditLogs   []AuditLogRow
}

type StarGiftRow struct {
	GiftID        int64
	RevisionID    int64
	Revision      int
	Title         string
	Stars         int64
	ConvertStars  int64
	Enabled       bool
	SortOrder     int
	DocumentID    int64
	SourceName    string
	SourceFormat  string
	AnimationSHA  string
	AnimationSize int64
	Width         int
	Height        int
	FrameRate     float64
	ReceivedCount int64
	CreatedBy     string
	UpdatedAt     time.Time
}

func (s *readStore) ListStarGifts(ctx context.Context) ([]StarGiftRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT c.gift_id, r.id, r.revision, r.title, r.stars, r.convert_stars,
       c.enabled, c.sort_order, r.document_id, r.source_name, r.source_format,
       encode(r.animation_sha256, 'hex'), d.size, r.width, r.height, r.frame_rate,
       (SELECT COUNT(*) FROM peer_star_gifts p WHERE p.gift_id = c.gift_id),
       r.created_by, c.updated_at
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id = c.active_revision_id
JOIN documents d ON d.id = r.document_id
ORDER BY c.sort_order, c.gift_id
LIMIT $1`, domain.MaxStarGiftCatalogSize)
	if err != nil {
		return nil, fmt.Errorf("list star gifts: %w", err)
	}
	defer rows.Close()
	out := make([]StarGiftRow, 0)
	for rows.Next() {
		var row StarGiftRow
		if err := rows.Scan(
			&row.GiftID, &row.RevisionID, &row.Revision, &row.Title, &row.Stars, &row.ConvertStars,
			&row.Enabled, &row.SortOrder, &row.DocumentID, &row.SourceName, &row.SourceFormat,
			&row.AnimationSHA, &row.AnimationSize, &row.Width, &row.Height, &row.FrameRate,
			&row.ReceivedCount, &row.CreatedBy, &row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *readStore) SearchAccounts(ctx context.Context, q string) ([]AccountRow, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	id := int64(-1)
	if n, err := strconv.ParseInt(q, 10, 64); err == nil {
		id = n
	}
	phone := strings.TrimPrefix(strings.ReplaceAll(q, " ", ""), "+")
	phoneRaw := strings.TrimSpace(q)
	username := strings.ToLower(strings.TrimPrefix(q, "@"))
	rows, err := s.pool.Query(ctx, `
WITH auth AS (
	SELECT user_id, max(active_at) AS last_active_at, count(*)::int AS device_count
	FROM authorizations
	GROUP BY user_id
)
SELECT u.id, u.phone, u.username, u.first_name, u.last_name, u.created_at, u.updated_at,
	COALESCE(r.frozen, false), COALESCE(r.reason, ''), u.verified,
	COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint,
	COALESCE(a.last_active_at, '0001-01-01 00:00:00+00'::timestamptz), COALESCE(a.device_count, 0)::int,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS display_username
FROM users u
LEFT JOIN account_restrictions r ON r.user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
LEFT JOIN auth a ON a.user_id = u.id
WHERE u.id = $1 OR u.phone = $2 OR u.phone = $3 OR lower(u.username) = $4 OR p.username_lower = $4
ORDER BY u.id
LIMIT $5`, id, phone, phoneRaw, username, accountSearchLimit)
	if err != nil {
		return nil, fmt.Errorf("search accounts: %w", err)
	}
	defer rows.Close()
	out := make([]AccountRow, 0)
	for rows.Next() {
		var item AccountRow
		if err := rows.Scan(&item.ID, &item.Phone, &item.Username, &item.FirstName, &item.LastName, &item.CreatedAt, &item.UpdatedAt, &item.Frozen, &item.Reason, &item.Verified, &item.PremiumUntil, &item.LastActiveAt, &item.DeviceCount, &item.Username); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *readStore) SearchChannels(ctx context.Context, q string) ([]ChannelRow, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	id := int64(-1)
	if n, err := strconv.ParseInt(q, 10, 64); err == nil {
		id = n
	}
	username := strings.ToLower(strings.TrimPrefix(q, "@"))
	rows, err := s.pool.Query(ctx, `
SELECT c.id, c.access_hash, c.creator_user_id, c.title, c.about,
	COALESCE(NULLIF(c.username, ''), p.username_lower, '') AS display_username,
	c.broadcast, c.megagroup, c.forum, c.monoforum, c.verified, c.deleted,
	c.participants_count, c.admins_count, c.kicked_count, c.banned_count,
	c.top_message_id, c.pinned_message_id, c.pts, c.date, c.created_at, c.updated_at
FROM channels c
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id
WHERE NOT c.deleted
	AND NOT c.monoforum
	AND (c.broadcast OR c.megagroup)
	AND (
		c.id = $1
		OR lower(COALESCE(c.username, '')) = $2
		OR p.username_lower = $2
		OR lower(c.title) LIKE '%' || $2 || '%'
	)
ORDER BY c.updated_at DESC, c.id DESC
LIMIT $3`, id, username, channelSearchLimit)
	if err != nil {
		return nil, fmt.Errorf("search channels: %w", err)
	}
	defer rows.Close()
	return scanChannelRows(rows)
}

func (s *readStore) ListChannels(ctx context.Context, beforeUpdatedUS, beforeID int64, limit int) ([]ChannelRow, bool, error) {
	if limit <= 0 {
		limit = channelListDefaultLimit
	}
	if limit > channelListMaxLimit {
		limit = channelListMaxLimit
	}
	rows, err := s.pool.Query(ctx, `
SELECT c.id, c.access_hash, c.creator_user_id, c.title, c.about,
	COALESCE(NULLIF(c.username, ''), p.username_lower, '') AS display_username,
	c.broadcast, c.megagroup, c.forum, c.monoforum, c.verified, c.deleted,
	c.participants_count, c.admins_count, c.kicked_count, c.banned_count,
	c.top_message_id, c.pinned_message_id, c.pts, c.date, c.created_at, c.updated_at
FROM channels c
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id
WHERE NOT c.deleted
	AND NOT c.monoforum
	AND (c.broadcast OR c.megagroup)
	AND ($1::bigint = 0 OR (c.updated_at, c.id) < (to_timestamp(($1::double precision) / 1000000.0), $2::bigint))
ORDER BY c.updated_at DESC, c.id DESC
LIMIT $3`, beforeUpdatedUS, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list channels: %w", err)
	}
	defer rows.Close()
	out, err := scanChannelRows(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (s *readStore) ChannelDetail(ctx context.Context, channelID int64) (ChannelDetail, error) {
	var out ChannelDetail
	var raw []byte
	err := s.pool.QueryRow(ctx, `
SELECT c.id, c.access_hash, c.creator_user_id, c.title, c.about,
	COALESCE(NULLIF(c.username, ''), p.username_lower, '') AS display_username,
	c.broadcast, c.megagroup, c.forum, c.monoforum, c.verified, c.deleted,
	c.participants_count, c.admins_count, c.kicked_count, c.banned_count,
	c.top_message_id, c.pinned_message_id, c.pts, c.date, c.created_at, c.updated_at,
	row_to_json(c)::jsonb
FROM channels c
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id
WHERE c.id = $1
	AND NOT c.deleted
	AND NOT c.monoforum
	AND (c.broadcast OR c.megagroup)`, channelID).Scan(channelScanDestWithRaw(&out.Channel, &raw)...)
	if err != nil {
		return out, fmt.Errorf("get channel: %w", err)
	}
	out.ChannelJSON = prettyJSON(raw)
	out.AuditLogs, err = s.channelAuditLogs(ctx, channelID)
	if err != nil {
		return out, err
	}
	return out, nil
}

type channelScanner interface {
	Scan(dest ...any) error
}

func scanChannelRows(rows pgx.Rows) ([]ChannelRow, error) {
	out := make([]ChannelRow, 0)
	for rows.Next() {
		var item ChannelRow
		if err := scanChannelRow(rows, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func scanChannelRow(row channelScanner, item *ChannelRow) error {
	return row.Scan(channelScanDest(item)...)
}

func channelScanDest(item *ChannelRow) []any {
	return []any{
		&item.ID, &item.AccessHash, &item.CreatorUserID, &item.Title, &item.About, &item.Username,
		&item.Broadcast, &item.Megagroup, &item.Forum, &item.Monoforum, &item.Verified, &item.Deleted,
		&item.ParticipantsCount, &item.AdminsCount, &item.KickedCount, &item.BannedCount,
		&item.TopMessageID, &item.PinnedMessageID, &item.PTS, &item.Date, &item.CreatedAt, &item.UpdatedAt,
	}
}

func channelScanDestWithRaw(item *ChannelRow, raw *[]byte) []any {
	dest := channelScanDest(item)
	return append(dest, raw)
}

func (s *readStore) ListAccounts(ctx context.Context, beforeActiveUS, beforeID int64, limit int) ([]AccountRow, bool, error) {
	if limit <= 0 {
		limit = accountListDefaultLimit
	}
	if limit > accountListMaxLimit {
		limit = accountListMaxLimit
	}
	rows, err := s.pool.Query(ctx, `
WITH auth AS (
	SELECT user_id, max(active_at) AS last_active_at, count(*)::int AS device_count
	FROM authorizations
	GROUP BY user_id
)
SELECT u.id, u.phone, u.username, u.first_name, u.last_name, u.created_at, u.updated_at,
	COALESCE(r.frozen, false), COALESCE(r.reason, ''), u.verified,
	COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint,
	auth.last_active_at, auth.device_count,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS display_username
FROM users u
JOIN auth ON auth.user_id = u.id
LEFT JOIN account_restrictions r ON r.user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
WHERE NOT u.is_bot
	AND ($1::bigint = 0 OR (auth.last_active_at, u.id) < (to_timestamp(($1::double precision) / 1000000.0), $2::bigint))
ORDER BY auth.last_active_at DESC, u.id DESC
LIMIT $3`, beforeActiveUS, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()
	out := make([]AccountRow, 0, limit+1)
	for rows.Next() {
		var item AccountRow
		if err := rows.Scan(&item.ID, &item.Phone, &item.Username, &item.FirstName, &item.LastName, &item.CreatedAt, &item.UpdatedAt, &item.Frozen, &item.Reason, &item.Verified, &item.PremiumUntil, &item.LastActiveAt, &item.DeviceCount, &item.Username); err != nil {
			return nil, false, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

func (s *readStore) AccountDetail(ctx context.Context, userID int64) (AccountDetail, error) {
	var out AccountDetail
	err := s.pool.QueryRow(ctx, `
SELECT u.id, u.phone, u.username, u.first_name, u.last_name, u.created_at, u.updated_at,
	u.about, u.last_seen_at, u.verified, u.support, u.is_bot,
	COALESCE(r.frozen, false), COALESCE(r.reason, ''),
	COALESCE(EXTRACT(EPOCH FROM u.premium_expires_at), 0)::bigint,
	COALESCE(sb.balance, 0)::bigint, COALESCE(sb.granted, false),
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS display_username
FROM users u
LEFT JOIN account_restrictions r ON r.user_id = u.id
LEFT JOIN stars_balances sb ON sb.user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
WHERE u.id = $1`, userID).Scan(
		&out.Account.ID, &out.Account.Phone, &out.Account.Username, &out.Account.FirstName, &out.Account.LastName,
		&out.Account.CreatedAt, &out.Account.UpdatedAt, &out.About, &out.LastSeenAt, &out.Verified, &out.Support, &out.Bot,
		&out.Account.Frozen, &out.Account.Reason, &out.Account.PremiumUntil, &out.StarsBalance, &out.StarsGranted, &out.Account.Username,
	)
	if err != nil {
		return out, fmt.Errorf("get account: %w", err)
	}
	out.Restriction, out.HasRestriction, err = s.restriction(ctx, userID)
	if err != nil {
		return out, err
	}
	out.Authorizations, err = s.authorizations(ctx, userID)
	if err != nil {
		return out, err
	}
	out.AuditLogs, err = s.auditLogs(ctx, userID)
	if err != nil {
		return out, err
	}
	return out, nil
}

func (s *readStore) restriction(ctx context.Context, userID int64) (RestrictionRow, bool, error) {
	var r RestrictionRow
	err := s.pool.QueryRow(ctx, `
SELECT frozen, frozen_since, frozen_until, appeal_url, reason, actor, command_id, updated_at
FROM account_restrictions
WHERE user_id = $1`, userID).Scan(
		&r.Frozen, &r.Since, &r.Until, &r.AppealURL,
		&r.Reason, &r.Actor, &r.CommandID, &r.UpdatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return RestrictionRow{}, false, nil
		}
		return RestrictionRow{}, false, fmt.Errorf("get restriction: %w", err)
	}
	return r, true, nil
}

func (s *readStore) authorizations(ctx context.Context, userID int64) ([]AuthorizationRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT auth_key_id, hash, layer, device_model, platform, system_version, api_id, app_version, ip, password_pending, created_at, active_at
FROM authorizations
WHERE user_id = $1
ORDER BY active_at DESC, created_at DESC
LIMIT 100`, userID)
	if err != nil {
		return nil, fmt.Errorf("list authorizations: %w", err)
	}
	defer rows.Close()
	out := make([]AuthorizationRow, 0)
	for rows.Next() {
		var a AuthorizationRow
		if err := rows.Scan(&a.AuthKeyID, &a.Hash, &a.Layer, &a.DeviceModel, &a.Platform, &a.SystemVersion, &a.APIID, &a.AppVersion, &a.IP, &a.PasswordPending, &a.CreatedAt, &a.ActiveAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *readStore) auditLogs(ctx context.Context, userID int64) ([]AuditLogRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, command_id, actor, action, dry_run, reason, status, error, result, created_at
FROM admin_audit_logs
WHERE target_user_id = $1
ORDER BY id DESC
LIMIT 30`, userID)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()
	out := make([]AuditLogRow, 0)
	for rows.Next() {
		var a AuditLogRow
		var result []byte
		if err := rows.Scan(&a.ID, &a.CommandID, &a.Actor, &a.Action, &a.DryRun, &a.Reason, &a.Status, &a.Error, &result, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Result = prettyJSON(result)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *readStore) channelAuditLogs(ctx context.Context, channelID int64) ([]AuditLogRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, command_id, actor, action, dry_run, reason, status, error, result, created_at
FROM admin_audit_logs
WHERE target_peer_type = 'channel' AND target_peer_id = $1
ORDER BY id DESC
LIMIT 30`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list channel audit logs: %w", err)
	}
	defer rows.Close()
	out := make([]AuditLogRow, 0)
	for rows.Next() {
		var a AuditLogRow
		var result []byte
		if err := rows.Scan(&a.ID, &a.CommandID, &a.Actor, &a.Action, &a.DryRun, &a.Reason, &a.Status, &a.Error, &result, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.Result = prettyJSON(result)
		out = append(out, a)
	}
	return out, rows.Err()
}

type MessageRow struct {
	OwnerUserID      int64
	BoxID            int
	PrivateMessageID int64
	MessageSenderID  int64
	PeerID           int64
	FromUserID       int64
	Date             int64
	Outgoing         bool
	Body             string
	PTS              int
	Deleted          bool
	Media            string
}

type GroupMessageRow struct {
	ChannelID    int64
	ID           int
	SenderUserID int64
	FromPeerType string
	FromPeerID   int64
	Date         int64
	Post         bool
	Body         string
	PTS          int
	Deleted      bool
	Media        string
	ViewsCount   int
	EditDate     int
	Pinned       bool
}

func (s *readStore) ListMessages(ctx context.Context, ownerUserID, peerID int64, beforeDate int64, beforeID int, limit int) ([]MessageRow, error) {
	if limit <= 0 || limit > messagePageLimit {
		limit = messagePageLimit
	}
	rows, err := s.pool.Query(ctx, `
SELECT owner_user_id, box_id, private_message_id, message_sender_id, peer_id, from_user_id,
	message_date, outgoing, body, pts, deleted, COALESCE(media, '{}'::jsonb)
FROM message_boxes
WHERE owner_user_id = $1 AND peer_type = 'user' AND peer_id = $2
	AND ($3::bigint = 0 OR (message_date, box_id) < ($3::bigint, $4::int))
ORDER BY message_date DESC, box_id DESC
LIMIT $5`, ownerUserID, peerID, beforeDate, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()
	out := make([]MessageRow, 0)
	for rows.Next() {
		var item MessageRow
		var media []byte
		if err := rows.Scan(&item.OwnerUserID, &item.BoxID, &item.PrivateMessageID, &item.MessageSenderID, &item.PeerID, &item.FromUserID, &item.Date, &item.Outgoing, &item.Body, &item.PTS, &item.Deleted, &media); err != nil {
			return nil, err
		}
		item.Media = prettyJSON(media)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *readStore) ListGroupMessages(ctx context.Context, channelID int64, beforeDate int64, beforeID int, limit int) ([]GroupMessageRow, error) {
	if limit <= 0 || limit > messagePageLimit {
		limit = messagePageLimit
	}
	rows, err := s.pool.Query(ctx, `
SELECT channel_id, id, sender_user_id, from_peer_type, from_peer_id,
	message_date, post, body, pts, deleted, COALESCE(media, '{}'::jsonb),
	views_count, edit_date, pinned
FROM channel_messages
WHERE channel_id = $1 AND NOT deleted
	AND ($2::bigint = 0 OR (message_date, id) < ($2::bigint, $3::int))
ORDER BY message_date DESC, id DESC
LIMIT $4`, channelID, beforeDate, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("list group messages: %w", err)
	}
	defer rows.Close()
	out := make([]GroupMessageRow, 0)
	for rows.Next() {
		var item GroupMessageRow
		var media []byte
		if err := rows.Scan(&item.ChannelID, &item.ID, &item.SenderUserID, &item.FromPeerType, &item.FromPeerID, &item.Date, &item.Post, &item.Body, &item.PTS, &item.Deleted, &media, &item.ViewsCount, &item.EditDate, &item.Pinned); err != nil {
			return nil, err
		}
		item.Media = prettyJSON(media)
		out = append(out, item)
	}
	return out, rows.Err()
}

type MessageDetail struct {
	Message      MessageRow
	MessageJSON  string
	DialogJSON   string
	PrivateJSON  string
	UpdateEvents []UpdateEventRow
	Outbox       []OutboxRow
}

type GroupMessageDetail struct {
	Message      GroupMessageRow
	MessageJSON  string
	ChannelJSON  string
	UpdateEvents []ChannelUpdateEventRow
}

type UpdateEventRow struct {
	PTS      int
	PTSCount int
	Type     string
	Date     int64
	JSON     string
}

type ChannelUpdateEventRow struct {
	PTS          int
	PTSCount     int
	Type         string
	MessageID    int
	Date         int64
	SenderUserID int64
	JSON         string
}

type OutboxRow struct {
	ID           int64
	TargetUserID int64
	PTS          int
	EventType    string
	Status       string
	Attempts     int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (s *readStore) MessageDetail(ctx context.Context, ownerUserID int64, msgID int) (MessageDetail, error) {
	var out MessageDetail
	var media []byte
	var messageJSON []byte
	err := s.pool.QueryRow(ctx, `
SELECT owner_user_id, box_id, private_message_id, message_sender_id, peer_id, from_user_id,
	message_date, outgoing, body, pts, deleted, COALESCE(media, '{}'::jsonb), row_to_json(mb)::jsonb
FROM message_boxes mb
WHERE owner_user_id = $1 AND box_id = $2`, ownerUserID, msgID).Scan(
		&out.Message.OwnerUserID, &out.Message.BoxID, &out.Message.PrivateMessageID, &out.Message.MessageSenderID, &out.Message.PeerID,
		&out.Message.FromUserID, &out.Message.Date, &out.Message.Outgoing, &out.Message.Body, &out.Message.PTS, &out.Message.Deleted,
		&media, &messageJSON,
	)
	if err != nil {
		return out, fmt.Errorf("get message: %w", err)
	}
	out.Message.Media = prettyJSON(media)
	out.MessageJSON = prettyJSON(messageJSON)
	out.DialogJSON, _ = s.rowJSON(ctx, `SELECT row_to_json(d)::jsonb FROM dialogs d WHERE user_id = $1 AND peer_type = 'user' AND peer_id = $2`, ownerUserID, out.Message.PeerID)
	out.PrivateJSON, _ = s.rowJSON(ctx, `SELECT row_to_json(pm)::jsonb FROM private_messages pm WHERE id = $1`, out.Message.PrivateMessageID)
	events, err := s.updateEvents(ctx, ownerUserID, msgID)
	if err != nil {
		return out, err
	}
	out.UpdateEvents = events
	outbox, err := s.outbox(ctx, ownerUserID, out.Message.PTS)
	if err != nil {
		return out, err
	}
	out.Outbox = outbox
	return out, nil
}

func (s *readStore) GroupMessageDetail(ctx context.Context, channelID int64, msgID int) (GroupMessageDetail, error) {
	var out GroupMessageDetail
	var media []byte
	var messageJSON []byte
	err := s.pool.QueryRow(ctx, `
SELECT channel_id, id, sender_user_id, from_peer_type, from_peer_id,
	message_date, post, body, pts, deleted, COALESCE(media, '{}'::jsonb),
	views_count, edit_date, pinned, row_to_json(cm)::jsonb
FROM channel_messages cm
WHERE channel_id = $1 AND id = $2`, channelID, msgID).Scan(
		&out.Message.ChannelID, &out.Message.ID, &out.Message.SenderUserID, &out.Message.FromPeerType, &out.Message.FromPeerID,
		&out.Message.Date, &out.Message.Post, &out.Message.Body, &out.Message.PTS, &out.Message.Deleted, &media,
		&out.Message.ViewsCount, &out.Message.EditDate, &out.Message.Pinned, &messageJSON,
	)
	if err != nil {
		return out, fmt.Errorf("get group message: %w", err)
	}
	out.Message.Media = prettyJSON(media)
	out.MessageJSON = prettyJSON(messageJSON)
	out.ChannelJSON, _ = s.rowJSON(ctx, `SELECT row_to_json(c)::jsonb FROM channels c WHERE id = $1`, channelID)
	events, err := s.channelUpdateEvents(ctx, channelID, msgID)
	if err != nil {
		return out, err
	}
	out.UpdateEvents = events
	return out, nil
}

func (s *readStore) rowJSON(ctx context.Context, sql string, args ...any) (string, error) {
	var raw []byte
	if err := s.pool.QueryRow(ctx, sql, args...).Scan(&raw); err != nil {
		return "", err
	}
	return prettyJSON(raw), nil
}

func (s *readStore) updateEvents(ctx context.Context, ownerUserID int64, msgID int) ([]UpdateEventRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT pts, pts_count, event_type, date, row_to_json(e)::jsonb
FROM user_update_events e
WHERE user_id = $1 AND (
	message_box_id = $2 OR message_ids @> $3::jsonb
)
ORDER BY pts DESC
LIMIT 20`, ownerUserID, msgID, fmt.Sprintf("[%d]", msgID))
	if err != nil {
		return nil, fmt.Errorf("list update events: %w", err)
	}
	defer rows.Close()
	out := make([]UpdateEventRow, 0)
	for rows.Next() {
		var e UpdateEventRow
		var raw []byte
		if err := rows.Scan(&e.PTS, &e.PTSCount, &e.Type, &e.Date, &raw); err != nil {
			return nil, err
		}
		e.JSON = prettyJSON(raw)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *readStore) channelUpdateEvents(ctx context.Context, channelID int64, msgID int) ([]ChannelUpdateEventRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT pts, pts_count, event_type, message_id, date, sender_user_id, row_to_json(e)::jsonb
FROM channel_update_events e
WHERE channel_id = $1 AND (
	message_id = $2 OR message_ids @> $3::jsonb
)
ORDER BY pts DESC
LIMIT 20`, channelID, msgID, fmt.Sprintf("[%d]", msgID))
	if err != nil {
		return nil, fmt.Errorf("list channel update events: %w", err)
	}
	defer rows.Close()
	out := make([]ChannelUpdateEventRow, 0)
	for rows.Next() {
		var e ChannelUpdateEventRow
		var raw []byte
		if err := rows.Scan(&e.PTS, &e.PTSCount, &e.Type, &e.MessageID, &e.Date, &e.SenderUserID, &raw); err != nil {
			return nil, err
		}
		e.JSON = prettyJSON(raw)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *readStore) outbox(ctx context.Context, targetUserID int64, pts int) ([]OutboxRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, target_user_id, pts, event_type, status, attempts, created_at, updated_at
FROM dispatch_outbox
WHERE target_user_id = $1 AND pts = $2
ORDER BY id DESC
LIMIT 20`, targetUserID, pts)
	if err != nil {
		return nil, fmt.Errorf("list outbox: %w", err)
	}
	defer rows.Close()
	out := make([]OutboxRow, 0)
	for rows.Next() {
		var row OutboxRow
		if err := rows.Scan(&row.ID, &row.TargetUserID, &row.PTS, &row.EventType, &row.Status, &row.Attempts, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func prettyJSON(raw []byte) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}
