package main

import (
	"context"
	"encoding/json"
	"errors"
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
	// Collectible username and account rating pages. The bounds mirror the
	// use-case layer, so a table page costs the same whichever surface asks.
	collectibleListDefaultLimit = 50
	collectibleListMaxLimit     = 200
	collectibleTransferLimit    = 50
	ratingListDefaultLimit      = 50
	ratingListMaxLimit          = 200
	ratingEventLimit            = 50
)

// errReadNotFound reports a detail row that does not exist, so the API layer can
// answer 404 without importing the driver's sentinel.
var errReadNotFound = errors.New("read row not found")

// escapeLikePattern neutralises LIKE metacharacters in an operator query.
// Usernames legitimately contain '_', so an unescaped search for "crypto_" would
// silently match "cryptoX" instead of the name the operator typed.
func escapeLikePattern(value string) string {
	replaced := strings.ReplaceAll(value, `\`, `\\`)
	replaced = strings.ReplaceAll(replaced, "%", `\%`)
	return strings.ReplaceAll(replaced, "_", `\_`)
}

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
	Scam         bool
	Fake         bool
	PremiumUntil int64
	LastActiveAt time.Time
	DeviceCount  int
}

type AccountDetail struct {
	Account        AccountRow
	About          string
	LastSeenAt     int64
	Verified       bool
	Scam           bool
	Fake           bool
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
	ID                 int64
	AccessHash         int64
	CreatorUserID      int64
	Title              string
	About              string
	Username           string
	Broadcast          bool
	Megagroup          bool
	Forum              bool
	Monoforum          bool
	Verified           bool
	Scam               bool
	Fake               bool
	Gigagroup          bool
	Deleted            bool
	AntiSpam           bool
	ParticipantsHidden bool
	NoForwards         bool
	JoinToSend         bool
	JoinRequest        bool
	SlowmodeSeconds    int
	ParticipantsCount  int
	AdminsCount        int
	KickedCount        int
	BannedCount        int
	TopMessageID       int
	PinnedMessageID    int
	PTS                int
	Date               int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type ChannelDetail struct {
	Channel     ChannelRow
	ChannelJSON string
	AuditLogs   []AuditLogRow
}

type StarGiftRow struct {
	GiftID        int64 `json:"GiftID,string"`
	RevisionID    int64 `json:"RevisionID,string"`
	Revision      int
	Title         string
	Stars         int64 `json:"Stars,string"`
	ConvertStars  int64 `json:"ConvertStars,string"`
	Enabled       bool
	SortOrder     int
	DocumentID    int64 `json:"DocumentID,string"`
	SourceName    string
	SourceFormat  string
	AnimationSHA  string
	AnimationSize int64 `json:"AnimationSize,string"`
	Width         int
	Height        int
	FrameRate     float64
	ReceivedCount int64 `json:"ReceivedCount,string"`
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
	COALESCE(r.frozen, false), COALESCE(r.reason, ''), u.verified, u.scam, u.fake,
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
		if err := rows.Scan(&item.ID, &item.Phone, &item.Username, &item.FirstName, &item.LastName, &item.CreatedAt, &item.UpdatedAt, &item.Frozen, &item.Reason, &item.Verified, &item.Scam, &item.Fake, &item.PremiumUntil, &item.LastActiveAt, &item.DeviceCount, &item.Username); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type BotRow struct {
	ID          int64
	Username    string
	FirstName   string
	Verified    bool
	Scam        bool
	Fake        bool
	System      bool
	OwnerUserID int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type BotDetail struct {
	Bot           BotRow
	About         string
	Description   string
	OwnerUsername string
	AuditLogs     []AuditLogRow
}

// ListBots pages over live bot accounts (users.is_bot, not tombstoned) by
// descending id. Bots are excluded from ListAccounts, so this is the dedicated
// projection for them.
func (s *readStore) ListBots(ctx context.Context, beforeID int64, limit int) ([]BotRow, bool, error) {
	if limit <= 0 {
		limit = accountListDefaultLimit
	}
	if limit > accountListMaxLimit {
		limit = accountListMaxLimit
	}
	rows, err := s.pool.Query(ctx, `
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name, u.verified, u.scam, u.fake,
	COALESCE(b.owner_user_id, 0), u.created_at, u.updated_at
FROM users u
LEFT JOIN bots b ON b.bot_user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
WHERE u.is_bot AND u.deleted_at IS NULL AND ($1::bigint = 0 OR u.id < $1)
ORDER BY u.id DESC
LIMIT $2`, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list bots: %w", err)
	}
	defer rows.Close()
	out := make([]BotRow, 0, limit+1)
	for rows.Next() {
		var item BotRow
		if err := rows.Scan(&item.ID, &item.Username, &item.FirstName, &item.Verified, &item.Scam, &item.Fake, &item.OwnerUserID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, false, err
		}
		item.System = domain.IsSystemUserID(item.ID)
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

func (s *readStore) SearchBots(ctx context.Context, q string) ([]BotRow, error) {
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
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name, u.verified, u.scam, u.fake,
	COALESCE(b.owner_user_id, 0), u.created_at, u.updated_at
FROM users u
LEFT JOIN bots b ON b.bot_user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
WHERE u.is_bot AND u.deleted_at IS NULL AND (u.id = $1 OR lower(u.username) = $2 OR p.username_lower = $2)
ORDER BY u.id DESC
LIMIT $3`, id, username, accountSearchLimit)
	if err != nil {
		return nil, fmt.Errorf("search bots: %w", err)
	}
	defer rows.Close()
	out := make([]BotRow, 0)
	for rows.Next() {
		var item BotRow
		if err := rows.Scan(&item.ID, &item.Username, &item.FirstName, &item.Verified, &item.Scam, &item.Fake, &item.OwnerUserID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.System = domain.IsSystemUserID(item.ID)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *readStore) BotDetail(ctx context.Context, botUserID int64) (BotDetail, error) {
	var out BotDetail
	err := s.pool.QueryRow(ctx, `
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name, u.about, u.verified, u.scam, u.fake,
	COALESCE(b.owner_user_id, 0), COALESCE(b.description, ''),
	u.created_at, u.updated_at
FROM users u
LEFT JOIN bots b ON b.bot_user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
WHERE u.id = $1 AND u.is_bot AND u.deleted_at IS NULL`, botUserID).Scan(
		&out.Bot.ID, &out.Bot.Username, &out.Bot.FirstName, &out.About, &out.Bot.Verified, &out.Bot.Scam, &out.Bot.Fake,
		&out.Bot.OwnerUserID, &out.Description, &out.Bot.CreatedAt, &out.Bot.UpdatedAt,
	)
	if err != nil {
		return out, fmt.Errorf("get bot: %w", err)
	}
	out.Bot.System = domain.IsSystemUserID(out.Bot.ID)
	if out.Bot.OwnerUserID > 0 {
		var ownerUsername string
		if err := s.pool.QueryRow(ctx, `
SELECT COALESCE(NULLIF(u.username, ''), p.username_lower, '')
FROM users u
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id
WHERE u.id = $1`, out.Bot.OwnerUserID).Scan(&ownerUsername); err != nil && err != pgx.ErrNoRows {
			return out, fmt.Errorf("get bot owner: %w", err)
		} else {
			out.OwnerUsername = ownerUsername
		}
	}
	out.AuditLogs, err = s.auditLogs(ctx, botUserID)
	if err != nil {
		return out, err
	}
	return out, nil
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
	c.broadcast, c.megagroup, c.forum, c.monoforum, c.verified, c.scam, c.fake, c.gigagroup, c.deleted,
	c.antispam, c.participants_hidden, c.noforwards, c.join_to_send, c.join_request, c.slowmode_seconds,
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
	c.broadcast, c.megagroup, c.forum, c.monoforum, c.verified, c.scam, c.fake, c.gigagroup, c.deleted,
	c.antispam, c.participants_hidden, c.noforwards, c.join_to_send, c.join_request, c.slowmode_seconds,
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
	c.broadcast, c.megagroup, c.forum, c.monoforum, c.verified, c.scam, c.fake, c.gigagroup, c.deleted,
	c.antispam, c.participants_hidden, c.noforwards, c.join_to_send, c.join_request, c.slowmode_seconds,
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
		&item.Broadcast, &item.Megagroup, &item.Forum, &item.Monoforum, &item.Verified, &item.Scam, &item.Fake, &item.Gigagroup, &item.Deleted,
		&item.AntiSpam, &item.ParticipantsHidden, &item.NoForwards, &item.JoinToSend, &item.JoinRequest, &item.SlowmodeSeconds,
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
	COALESCE(r.frozen, false), COALESCE(r.reason, ''), u.verified, u.scam, u.fake,
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
		if err := rows.Scan(&item.ID, &item.Phone, &item.Username, &item.FirstName, &item.LastName, &item.CreatedAt, &item.UpdatedAt, &item.Frozen, &item.Reason, &item.Verified, &item.Scam, &item.Fake, &item.PremiumUntil, &item.LastActiveAt, &item.DeviceCount, &item.Username); err != nil {
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
	u.about, u.last_seen_at, u.verified, u.scam, u.fake, u.support, u.is_bot,
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
		&out.Account.CreatedAt, &out.Account.UpdatedAt, &out.About, &out.LastSeenAt, &out.Verified, &out.Scam, &out.Fake, &out.Support, &out.Bot,
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

// EmojiRow is a custom-emoji document projection for the admin emoji browser.
type EmojiRow struct {
	DocumentID int64 `json:"DocumentID,string"`
	Alt        string
	MimeType   string
	Size       int64
	SetTitle   string
	CreatedAt  time.Time
}

const emojiListDefaultLimit = 60
const emojiListMaxLimit = 200

func scanEmojiRows(rows pgx.Rows) ([]EmojiRow, error) {
	out := make([]EmojiRow, 0)
	for rows.Next() {
		var item EmojiRow
		if err := rows.Scan(&item.DocumentID, &item.Alt, &item.MimeType, &item.Size, &item.CreatedAt, &item.SetTitle); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

const emojiSelectColumns = `d.id,
	COALESCE((SELECT a->>'alt' FROM jsonb_array_elements(d.attributes) a WHERE a->>'kind' = 'custom_emoji' LIMIT 1), ''),
	d.mime_type, d.size, d.created_at,
	COALESCE((SELECT s.title FROM sticker_sets s WHERE s.emojis AND NOT s.deleted AND s.document_ids @> to_jsonb(d.id) LIMIT 1), '')`

// ListEmoji pages over custom-emoji documents by descending id.
func (s *readStore) ListEmoji(ctx context.Context, beforeID int64, limit int) ([]EmojiRow, bool, error) {
	if limit <= 0 {
		limit = emojiListDefaultLimit
	}
	if limit > emojiListMaxLimit {
		limit = emojiListMaxLimit
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+emojiSelectColumns+`
FROM documents d
WHERE d.attributes @> '[{"kind":"custom_emoji"}]'::jsonb
	AND ($1::bigint = 0 OR d.id < $1)
ORDER BY d.id DESC
LIMIT $2`, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list emoji: %w", err)
	}
	defer rows.Close()
	out, err := scanEmojiRows(rows)
	if err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// SearchEmoji finds custom-emoji documents by document id or emoticon substring.
func (s *readStore) SearchEmoji(ctx context.Context, q string) ([]EmojiRow, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	id := int64(-1)
	if n, err := strconv.ParseInt(q, 10, 64); err == nil {
		id = n
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+emojiSelectColumns+`
FROM documents d
WHERE d.attributes @> '[{"kind":"custom_emoji"}]'::jsonb
	AND (d.id = $1 OR EXISTS (
		SELECT 1 FROM jsonb_array_elements(d.attributes) a
		WHERE a->>'kind' = 'custom_emoji' AND a->>'alt' ILIKE '%' || $2 || '%'
	))
ORDER BY d.id DESC
LIMIT $3`, id, q, emojiListMaxLimit)
	if err != nil {
		return nil, fmt.Errorf("search emoji: %w", err)
	}
	defer rows.Close()
	return scanEmojiRows(rows)
}

// CollectibleUsernameRow is one collectible (Fragment-style) username asset with
// its holder resolved for display.
//
// Every int64 is tagged as a JSON string: asset ids, nanoton amounts and the
// optimistic-concurrency version all exceed the range a JSON number represents
// exactly, and a rounded id would address the wrong asset.
type CollectibleUsernameRow struct {
	ID                    int64 `json:"ID,string"`
	Username              string
	Status                string
	OwnerPeerType         string
	OwnerPeerID           int64 `json:"OwnerPeerID,string"`
	OwnerUsername         string
	OwnerName             string
	PurchaseDate          time.Time
	Currency              string
	Amount                int64 `json:"Amount,string"`
	CryptoCurrency        string
	CryptoAmount          int64 `json:"CryptoAmount,string"`
	URL                   string
	OriginalOwnerPeerType string
	OriginalOwnerPeerID   int64 `json:"OriginalOwnerPeerID,string"`
	OriginalOwnerUsername string
	TransferCount         int
	Version               int64 `json:"Version,string"`
	CreatedAt             time.Time
	UpdatedAt             time.Time
	// RegistryActive / RegistrySortOrder mirror the holder's username registry
	// row, so the panel can tell an owned-but-hidden name from an active one.
	RegistryActive    bool
	RegistrySortOrder int
}

// CollectibleUsernameTransferRow is one provenance log entry.
type CollectibleUsernameTransferRow struct {
	ID            int64 `json:"ID,string"`
	CollectibleID int64 `json:"CollectibleID,string"`
	Kind          string
	FromPeerType  string
	FromPeerID    int64 `json:"FromPeerID,string"`
	FromUsername  string
	ToPeerType    string
	ToPeerID      int64 `json:"ToPeerID,string"`
	ToUsername    string
	Currency      string
	Amount        int64 `json:"Amount,string"`
	Actor         string
	Reason        string
	CommandKey    string
	CreatedAt     time.Time
}

// CollectibleUsernameDetail is the asset plus its provenance log.
type CollectibleUsernameDetail struct {
	Asset     CollectibleUsernameRow
	Transfers []CollectibleUsernameTransferRow
}

const collectibleUsernameSelectColumns = `cu.id, cu.username, cu.status,
	cu.owner_peer_type, cu.owner_peer_id,
	COALESCE(NULLIF(ou.username, ''), NULLIF(oc.username, ''), '') AS owner_username,
	COALESCE(NULLIF(ou.first_name, ''), NULLIF(oc.title, ''), '') AS owner_name,
	cu.purchase_date, cu.currency, cu.amount, cu.crypto_currency, cu.crypto_amount, cu.url,
	cu.original_owner_peer_type, cu.original_owner_peer_id,
	COALESCE(NULLIF(gu.username, ''), NULLIF(gc.username, ''), '') AS original_owner_username,
	cu.transfer_count, cu.version, cu.created_at, cu.updated_at,
	COALESCE(pu.active, false), COALESCE(pu.sort_order, 0)`

// collectibleUsernameJoins resolves the current holder, the original holder and
// the holder's registry row. Owners are users or channels, so both sides are
// joined and the peer type decides which one contributes.
const collectibleUsernameJoins = `
FROM collectible_usernames cu
LEFT JOIN users ou ON cu.owner_peer_type = 'user' AND ou.id = cu.owner_peer_id
LEFT JOIN channels oc ON cu.owner_peer_type = 'channel' AND oc.id = cu.owner_peer_id
LEFT JOIN users gu ON cu.original_owner_peer_type = 'user' AND gu.id = cu.original_owner_peer_id
LEFT JOIN channels gc ON cu.original_owner_peer_type = 'channel' AND gc.id = cu.original_owner_peer_id
LEFT JOIN peer_usernames pu ON pu.collectible_id = cu.id`

func collectibleUsernameScanDest(item *CollectibleUsernameRow) []any {
	return []any{
		&item.ID, &item.Username, &item.Status,
		&item.OwnerPeerType, &item.OwnerPeerID, &item.OwnerUsername, &item.OwnerName,
		&item.PurchaseDate, &item.Currency, &item.Amount, &item.CryptoCurrency, &item.CryptoAmount, &item.URL,
		&item.OriginalOwnerPeerType, &item.OriginalOwnerPeerID, &item.OriginalOwnerUsername,
		&item.TransferCount, &item.Version, &item.CreatedAt, &item.UpdatedAt,
		&item.RegistryActive, &item.RegistrySortOrder,
	}
}

// ListCollectibleUsernames pages over collectible assets newest first, keyset by
// descending id. status/ownerUserID/q are optional filters; q matches a username
// prefix, which is how an operator looks a name up.
func (s *readStore) ListCollectibleUsernames(ctx context.Context, status string, ownerUserID, beforeID int64, q string, limit int) ([]CollectibleUsernameRow, bool, error) {
	if limit <= 0 {
		limit = collectibleListDefaultLimit
	}
	if limit > collectibleListMaxLimit {
		limit = collectibleListMaxLimit
	}
	status = strings.TrimSpace(status)
	query := escapeLikePattern(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "@")))
	rows, err := s.pool.Query(ctx, `
SELECT `+collectibleUsernameSelectColumns+collectibleUsernameJoins+`
WHERE ($1 = '' OR cu.status = $1)
	AND ($2::bigint = 0 OR (cu.owner_peer_type = 'user' AND cu.owner_peer_id = $2))
	AND ($3 = '' OR cu.username_lower LIKE $3 || '%')
	AND ($4::bigint = 0 OR cu.id < $4)
ORDER BY cu.id DESC
LIMIT $5`, status, ownerUserID, query, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list collectible usernames: %w", err)
	}
	defer rows.Close()
	out := make([]CollectibleUsernameRow, 0, limit+1)
	for rows.Next() {
		var item CollectibleUsernameRow
		if err := rows.Scan(collectibleUsernameScanDest(&item)...); err != nil {
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

// CollectibleUsernameDetail returns one asset with its provenance log. A missing
// asset reports errReadNotFound so the API answers 404 rather than 500.
func (s *readStore) CollectibleUsernameDetail(ctx context.Context, id int64) (CollectibleUsernameDetail, error) {
	var out CollectibleUsernameDetail
	err := s.pool.QueryRow(ctx, `
SELECT `+collectibleUsernameSelectColumns+collectibleUsernameJoins+`
WHERE cu.id = $1`, id).Scan(collectibleUsernameScanDest(&out.Asset)...)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, errReadNotFound
		}
		return out, fmt.Errorf("get collectible username: %w", err)
	}
	out.Transfers, err = s.collectibleUsernameTransfers(ctx, id)
	if err != nil {
		return out, err
	}
	return out, nil
}

func (s *readStore) collectibleUsernameTransfers(ctx context.Context, collectibleID int64) ([]CollectibleUsernameTransferRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT t.id, t.collectible_id, t.kind,
	t.from_peer_type, t.from_peer_id,
	COALESCE(NULLIF(fu.username, ''), NULLIF(fc.username, ''), '') AS from_username,
	t.to_peer_type, t.to_peer_id,
	COALESCE(NULLIF(tu.username, ''), NULLIF(tc.username, ''), '') AS to_username,
	t.currency, t.amount, t.actor, t.reason, COALESCE(t.command_key, ''), t.created_at
FROM collectible_username_transfers t
LEFT JOIN users fu ON t.from_peer_type = 'user' AND fu.id = t.from_peer_id
LEFT JOIN channels fc ON t.from_peer_type = 'channel' AND fc.id = t.from_peer_id
LEFT JOIN users tu ON t.to_peer_type = 'user' AND tu.id = t.to_peer_id
LEFT JOIN channels tc ON t.to_peer_type = 'channel' AND tc.id = t.to_peer_id
WHERE t.collectible_id = $1
ORDER BY t.id DESC
LIMIT $2`, collectibleID, collectibleTransferLimit)
	if err != nil {
		return nil, fmt.Errorf("list collectible username transfers: %w", err)
	}
	defer rows.Close()
	out := make([]CollectibleUsernameTransferRow, 0)
	for rows.Next() {
		var item CollectibleUsernameTransferRow
		if err := rows.Scan(
			&item.ID, &item.CollectibleID, &item.Kind,
			&item.FromPeerType, &item.FromPeerID, &item.FromUsername,
			&item.ToPeerType, &item.ToPeerID, &item.ToUsername,
			&item.Currency, &item.Amount, &item.Actor, &item.Reason, &item.CommandKey, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// AccountRatingRow is one user's composite rating projection with the account
// resolved for display. The score and every component are int64 decimal strings
// for the same exactness reason as the collectible amounts.
type AccountRatingRow struct {
	UserID            int64 `json:"UserID,string"`
	Username          string
	FirstName         string
	Level             int
	Stars             int64 `json:"Stars,string"`
	CurrentLevelStars int64 `json:"CurrentLevelStars,string"`
	NextLevelStars    int64 `json:"NextLevelStars,string"`
	HasNextLevel      bool
	StarsComponent    int64 `json:"StarsComponent,string"`
	ActivityComponent int64 `json:"ActivityComponent,string"`
	PenaltyComponent  int64 `json:"PenaltyComponent,string"`
	ManualComponent   int64 `json:"ManualComponent,string"`
	PendingStars      int64 `json:"PendingStars,string"`
	PendingDate       time.Time
	ComputedAt        time.Time
	UpdatedAt         time.Time
	Version           int64 `json:"Version,string"`
	// Computed is false for an account that has no stored projection yet. The
	// detail view still renders it, so the operator can trigger the first
	// recompute instead of facing a dead end.
	Computed bool
}

// AccountRatingEventRow is one contribution ledger entry.
type AccountRatingEventRow struct {
	ID         int64 `json:"ID,string"`
	UserID     int64 `json:"UserID,string"`
	Kind       string
	Amount     int64 `json:"Amount,string"`
	Reason     string
	Actor      string
	CommandKey string
	CreatedAt  time.Time
}

// AccountRatingDetail is the projection plus the ledger that explains it.
type AccountRatingDetail struct {
	Rating AccountRatingRow
	Events []AccountRatingEventRow
}

const accountRatingSelectColumns = `r.user_id,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS display_username,
	COALESCE(u.first_name, ''),
	r.level, r.stars, r.current_level_stars, r.next_level_stars,
	r.stars_component, r.activity_component, r.penalty_component, r.manual_component,
	r.pending_stars, r.pending_date, r.computed_at, r.updated_at, r.version`

const accountRatingJoins = `
FROM account_rating r
LEFT JOIN users u ON u.id = r.user_id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = r.user_id AND p.editable`

func scanAccountRatingRow(scan func(dest ...any) error, item *AccountRatingRow) error {
	// next_level_stars and pending_date are nullable: the first is NULL at the top
	// level, the second whenever no score is parked.
	var nextLevelStars *int64
	var pendingDate *time.Time
	if err := scan(
		&item.UserID, &item.Username, &item.FirstName,
		&item.Level, &item.Stars, &item.CurrentLevelStars, &nextLevelStars,
		&item.StarsComponent, &item.ActivityComponent, &item.PenaltyComponent, &item.ManualComponent,
		&item.PendingStars, &pendingDate, &item.ComputedAt, &item.UpdatedAt, &item.Version,
	); err != nil {
		return err
	}
	// A NULL next threshold is the maxed-out level: the TL flag is omitted, so the
	// panel must render "no next level" instead of a next level of zero.
	item.HasNextLevel = nextLevelStars != nil
	if nextLevelStars != nil {
		item.NextLevelStars = *nextLevelStars
	}
	if pendingDate != nil {
		item.PendingDate = pendingDate.UTC()
	}
	item.Computed = true
	return nil
}

// ListAccountRatings pages the leaderboard. Ordering and the keyset predicate
// mirror the rating store exactly -- (level DESC, stars DESC, user_id) with the
// cursor row resolved from beforeID -- so both surfaces page identically.
func (s *readStore) ListAccountRatings(ctx context.Context, minLevel int, userID, beforeID int64, limit int) ([]AccountRatingRow, bool, error) {
	if limit <= 0 {
		limit = ratingListDefaultLimit
	}
	if limit > ratingListMaxLimit {
		limit = ratingListMaxLimit
	}
	if minLevel < 0 {
		minLevel = 0
	}
	if minLevel > domain.MaxAccountRatingLevel {
		minLevel = domain.MaxAccountRatingLevel
	}
	rows, err := s.pool.Query(ctx, `
WITH cursor_row AS (
	SELECT level AS c_level, stars AS c_stars, user_id AS c_user_id
	FROM account_rating WHERE $3::bigint <> 0 AND user_id = $3
)
SELECT `+accountRatingSelectColumns+accountRatingJoins+`
LEFT JOIN cursor_row c ON true
WHERE r.level >= $1
	AND ($2::bigint = 0 OR r.user_id = $2)
	AND (
		c.c_user_id IS NULL
		OR r.level < c.c_level
		OR (r.level = c.c_level AND r.stars < c.c_stars)
		OR (r.level = c.c_level AND r.stars = c.c_stars AND r.user_id > c.c_user_id)
	)
ORDER BY r.level DESC, r.stars DESC, r.user_id
LIMIT $4`, minLevel, userID, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list account ratings: %w", err)
	}
	defer rows.Close()
	out := make([]AccountRatingRow, 0, limit+1)
	for rows.Next() {
		var item AccountRatingRow
		if err := scanAccountRatingRow(rows.Scan, &item); err != nil {
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

// AccountRatingDetail returns one user's projection with its contribution
// ledger.
//
// An account that exists but was never computed is answered with a zero-valued
// projection carrying Computed=false, because the recompute command lives on this
// very page: reporting "not found" for a real account would leave the operator
// with no way to create the first projection. Only an unknown account is a 404.
func (s *readStore) AccountRatingDetail(ctx context.Context, userID int64) (AccountRatingDetail, error) {
	var out AccountRatingDetail
	row := s.pool.QueryRow(ctx, `
SELECT `+accountRatingSelectColumns+accountRatingJoins+`
WHERE r.user_id = $1`, userID)
	err := scanAccountRatingRow(row.Scan, &out.Rating)
	switch {
	case err == nil:
	case errors.Is(err, pgx.ErrNoRows):
		placeholder, uncomputedErr := s.uncomputedAccountRating(ctx, userID)
		if uncomputedErr != nil {
			return out, uncomputedErr
		}
		out.Rating = placeholder
	default:
		return out, fmt.Errorf("get account rating: %w", err)
	}
	events, err := s.accountRatingEvents(ctx, userID)
	if err != nil {
		return out, err
	}
	out.Events = events
	return out, nil
}

// uncomputedAccountRating renders the projection an account would start from,
// derived through the same threshold policy the store persists, so the panel's
// level maths does not have to special-case a missing row.
func (s *readStore) uncomputedAccountRating(ctx context.Context, userID int64) (AccountRatingRow, error) {
	var row AccountRatingRow
	err := s.pool.QueryRow(ctx, `
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name
FROM users u
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
WHERE u.id = $1`, userID).Scan(&row.UserID, &row.Username, &row.FirstName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return row, errReadNotFound
		}
		return row, fmt.Errorf("get account for rating: %w", err)
	}
	level, current, next, hasNext := domain.AccountRatingLevelForStars(0)
	row.Level = level
	row.CurrentLevelStars = current
	row.NextLevelStars = next
	row.HasNextLevel = hasNext
	return row, nil
}

func (s *readStore) accountRatingEvents(ctx context.Context, userID int64) ([]AccountRatingEventRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, user_id, kind, amount, reason, actor, COALESCE(command_key, ''), created_at
FROM account_rating_events
WHERE user_id = $1
ORDER BY id DESC
LIMIT $2`, userID, ratingEventLimit)
	if err != nil {
		return nil, fmt.Errorf("list account rating events: %w", err)
	}
	defer rows.Close()
	out := make([]AccountRatingEventRow, 0)
	for rows.Next() {
		var item AccountRatingEventRow
		if err := rows.Scan(&item.ID, &item.UserID, &item.Kind, &item.Amount, &item.Reason, &item.Actor, &item.CommandKey, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
