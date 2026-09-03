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
	// Verification review queue pages. The bounds mirror app/verification, so the
	// panel and the admin API page the queue identically.
	verificationListDefaultLimit = 50
	verificationListMaxLimit     = 200
	verificationEventLimit       = 100
	// Third-party bot verification pages. The bounds mirror app/botverification, so
	// the panel and the admin API page the verifier tables identically.
	botVerificationListDefaultLimit = 50
	botVerificationListMaxLimit     = 200
)

type StorageBackendRow struct {
	Backend        string
	PhysicalBytes  int64 `json:"PhysicalBytes,string"`
	LogicalBytes   int64 `json:"LogicalBytes,string"`
	ObjectCount    int64 `json:"ObjectCount,string"`
	ReferenceCount int64 `json:"ReferenceCount,string"`
}

type StorageStatsRow struct {
	PhysicalBytes  int64 `json:"PhysicalBytes,string"`
	LogicalBytes   int64 `json:"LogicalBytes,string"`
	ObjectCount    int64 `json:"ObjectCount,string"`
	ReferenceCount int64 `json:"ReferenceCount,string"`
	DocumentCount  int64 `json:"DocumentCount,string"`
	PhotoCount     int64 `json:"PhotoCount,string"`
	Backends       []StorageBackendRow
}

type BroadcastRow struct {
	ID                int64 `json:"ID,string"`
	Message           string
	TargetMode        string
	TargetCount       int64 `json:"TargetCount,string"`
	MaterializedCount int64 `json:"MaterializedCount,string"`
	SentCount         int64 `json:"SentCount,string"`
	FailedCount       int64 `json:"FailedCount,string"`
	EnumerationDone   bool
	CreatedBy         string
	CreatedAt         time.Time
}

func (s *readStore) ListBroadcasts(ctx context.Context, beforeID int64, limit int) ([]BroadcastRow, bool, error) {
	if limit <= 0 {
		limit = accountListDefaultLimit
	}
	if limit > accountListMaxLimit {
		limit = accountListMaxLimit
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, message, target_mode, target_count, materialized_count,
       sent_count, failed_count, enumeration_done, created_by, created_at
FROM broadcasts
WHERE $1::bigint = 0 OR id < $1
ORDER BY id DESC
LIMIT $2`, beforeID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list broadcasts: %w", err)
	}
	defer rows.Close()
	out := make([]BroadcastRow, 0, limit+1)
	for rows.Next() {
		var item BroadcastRow
		if err := rows.Scan(&item.ID, &item.Message, &item.TargetMode, &item.TargetCount, &item.MaterializedCount,
			&item.SentCount, &item.FailedCount, &item.EnumerationDone, &item.CreatedBy, &item.CreatedAt); err != nil {
			return nil, false, fmt.Errorf("scan broadcast: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("iterate broadcasts: %w", err)
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

var systemAccountIDs = []int64{
	domain.OfficialSystemUserID,
	domain.BotFatherUserID,
	domain.StickersBotUserID,
	domain.ChatBotUserID,
}

func (s *readStore) CountAccounts(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM users
WHERE NOT is_bot
  AND deleted_at IS NULL
  AND id <> ALL($1::bigint[])`, systemAccountIDs).Scan(&n); err != nil {
		return 0, fmt.Errorf("count accounts: %w", err)
	}
	return n, nil
}

// CountOnlineAccounts uses last_seen_at as the DB-side proxy for the live
// server's in-process presence tracker. The server persists that field while a
// session stays online, using the same five-minute window clients expect.
func (s *readStore) CountOnlineAccounts(ctx context.Context) (int64, error) {
	var n int64
	if err := s.pool.QueryRow(ctx, `
SELECT count(*)
FROM users
WHERE NOT is_bot
  AND deleted_at IS NULL
  AND id <> ALL($1::bigint[])
  AND last_seen_at > extract(epoch FROM now() - interval '5 minutes')::bigint`,
		systemAccountIDs).Scan(&n); err != nil {
		return 0, fmt.Errorf("count online accounts: %w", err)
	}
	return n, nil
}

type DashboardCounts struct {
	Users                int64
	OnlineUsers          int64
	Bots                 int64
	BroadcastChannels    int64
	Supergroups          int64
	StickerSets          int64
	EmojiSets            int64
	Gifs                 int64
	PendingReports       int64
	PendingVerifications int64
}

func (s *readStore) DashboardCounts(ctx context.Context) (DashboardCounts, error) {
	var out DashboardCounts
	var err error
	if out.Users, err = s.CountAccounts(ctx); err != nil {
		return out, err
	}
	if out.OnlineUsers, err = s.CountOnlineAccounts(ctx); err != nil {
		return out, err
	}
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM users WHERE is_bot AND deleted_at IS NULL`).Scan(&out.Bots); err != nil {
		return out, fmt.Errorf("count bots: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE broadcast), count(*) FILTER (WHERE megagroup)
FROM channels WHERE NOT deleted AND NOT monoforum`).Scan(&out.BroadcastChannels, &out.Supergroups); err != nil {
		return out, fmt.Errorf("count channels: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE set_kind = 'stickers'), count(*) FILTER (WHERE set_kind = 'emoji')
FROM sticker_sets WHERE deleted = false`).Scan(&out.StickerSets, &out.EmojiSets); err != nil {
		return out, fmt.Errorf("count sticker sets: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
SELECT count(DISTINCT document_id) FROM user_sticker_collections WHERE kind = 'gif'`).Scan(&out.Gifs); err != nil {
		return out, fmt.Errorf("count gifs: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM moderation_cases WHERE status NOT IN ('resolved', 'dismissed')`).Scan(&out.PendingReports); err != nil {
		return out, fmt.Errorf("count pending moderation cases: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM verification_applications WHERE status IN ('submitted', 'in_review')`).Scan(&out.PendingVerifications); err != nil {
		return out, fmt.Errorf("count pending verification applications: %w", err)
	}
	return out, nil
}

// StorageStats is deliberately read-only and derives physical usage from
// content-addressed object keys. It does not claim that an unreferenced object
// is safe to delete; the complete media-reference graph is not yet available.
func (s *readStore) StorageStats(ctx context.Context) (StorageStatsRow, error) {
	rows, err := s.pool.Query(ctx, `
WITH objects AS (
    SELECT backend, object_key,
           MIN(size)::bigint AS min_size,
           MAX(size)::bigint AS max_size,
           COUNT(*)::bigint AS reference_count
    FROM file_blobs
    GROUP BY backend, object_key
)
SELECT backend,
       COALESCE(SUM(max_size), 0)::bigint AS physical_bytes,
       COALESCE(SUM(max_size * reference_count), 0)::bigint AS logical_bytes,
       COUNT(*)::bigint AS object_count,
       COALESCE(SUM(reference_count), 0)::bigint AS reference_count,
       COALESCE(bool_and(min_size = max_size), true) AS sizes_consistent
FROM objects
GROUP BY backend
ORDER BY backend`)
	if err != nil {
		return StorageStatsRow{}, fmt.Errorf("query storage backend stats: %w", err)
	}
	defer rows.Close()
	stats := StorageStatsRow{Backends: make([]StorageBackendRow, 0, 2)}
	for rows.Next() {
		var item StorageBackendRow
		var consistent bool
		if err := rows.Scan(&item.Backend, &item.PhysicalBytes, &item.LogicalBytes, &item.ObjectCount, &item.ReferenceCount, &consistent); err != nil {
			return StorageStatsRow{}, fmt.Errorf("scan storage backend stats: %w", err)
		}
		if !consistent {
			return StorageStatsRow{}, fmt.Errorf("file_blobs contains inconsistent sizes for shared %s object keys", item.Backend)
		}
		stats.PhysicalBytes += item.PhysicalBytes
		stats.LogicalBytes += item.LogicalBytes
		stats.ObjectCount += item.ObjectCount
		stats.ReferenceCount += item.ReferenceCount
		stats.Backends = append(stats.Backends, item)
	}
	if err := rows.Err(); err != nil {
		return StorageStatsRow{}, fmt.Errorf("iterate storage backend stats: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM documents`).Scan(&stats.DocumentCount); err != nil {
		return StorageStatsRow{}, fmt.Errorf("count storage documents: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM photos`).Scan(&stats.PhotoCount); err != nil {
		return StorageStatsRow{}, fmt.Errorf("count storage photos: %w", err)
	}
	return stats, nil
}

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

// AccountUsername is one collectible (Fragment-style) username a peer holds.
//
// Active mirrors the username#b4073647 flag: an inactive collectible is still
// owned, it just does not resolve publicly, and an operator has to be able to
// tell those two apart -- so the row is listed either way and carries the flag
// rather than being filtered out.
type AccountUsername struct {
	Username string
	Active   bool
}

type AccountRow struct {
	ID        int64
	Phone     string
	Username  string
	FirstName string
	LastName  string
	// Collectibles are the peer's collectible usernames in the order clients
	// project them. The editable slot in Username is never repeated here: it is a
	// different kind of row that a different RPC owns.
	Collectibles []AccountUsername
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

// accountCollectibleUsernamesColumn aggregates a peer's collectible usernames
// into one jsonb value, so a list page costs one indexed subquery per row instead
// of a second round trip per account.
//
// The object keys are the AccountUsername field names on purpose: pgx unmarshals
// jsonb straight into the Go value, and matching the field names keeps the panel's
// JSON shape identical to every other field on the row (PascalCase) instead of
// introducing one lowercase island. Ordering matches domain.SortUsernames for
// collectible rows -- stored order, then the name as a stable tiebreak.
const accountCollectibleUsernamesColumn = `COALESCE((
	SELECT jsonb_agg(jsonb_build_object('Username', pc.username, 'Active', pc.active)
		ORDER BY pc.sort_order, pc.username_lower)
	FROM peer_usernames pc
	WHERE pc.peer_type = 'user' AND pc.peer_id = u.id AND pc.collectible_id IS NOT NULL
), '[]'::jsonb)`

type AccountDetail struct {
	Account        AccountRow
	About          string
	LastSeenAt     int64
	Verified       bool
	Scam           bool
	Fake           bool
	Support        bool
	Bot            bool
	LoginEmail     string
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
	AuthKeyID       int64 `json:"AuthKeyID,string"`
	Hash            int64 `json:"Hash,string"`
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
	// Supply: zero-value (Limited=false) means unlimited, matching every
	// other reader of these same columns (tgStarGift, the auction monitor).
	Limited             bool
	SoldOut             bool
	AvailabilityTotal   int
	AvailabilityRemains int
	// Collectible pool -- a SEPARATE metric from Availability* above (that
	// one caps plain, not-yet-upgraded copies; this one caps how many can
	// ever be upgraded to a unique/NFT instance). Nil when the gift has no
	// published collectible pool at all. Mirrors openfragment/server.js's
	// own fetchStarGiftCatalog query -- same two-column "Sold"/"Issued"
	// split shown there.
	CollectibleIssued      *int
	CollectibleSupplyTotal *int
}

func (s *readStore) ListStarGifts(ctx context.Context) ([]StarGiftRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT c.gift_id, r.id, r.revision, r.title, r.stars, r.convert_stars,
       c.enabled, c.sort_order, r.document_id, r.source_name, r.source_format,
       encode(r.animation_sha256, 'hex'), d.size, r.width, r.height, r.frame_rate,
       (SELECT COUNT(*) FROM peer_star_gifts p WHERE p.gift_id = c.gift_id),
       r.created_by, c.updated_at,
       r.limited, r.sold_out, r.availability_total, c.availability_remains,
       cr.issued, cr.supply_total
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id = c.active_revision_id
JOIN documents d ON d.id = r.document_id
LEFT JOIN star_gift_collectible_revisions cr ON cr.id = c.collectible_revision_id
ORDER BY c.enabled DESC, c.sort_order, c.gift_id
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
			&row.Limited, &row.SoldOut, &row.AvailabilityTotal, &row.AvailabilityRemains,
			&row.CollectibleIssued, &row.CollectibleSupplyTotal,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// StarGiftAuctionRow is one row of the operator auction/scheduled-drop monitor.
// It is driven from the catalog, not from star_gift_auctions: the engine
// materializes an auction row lazily (ensureStarGiftAuction), so a freshly
// authored auction has a catalog revision but no auction row yet. Materialized
// reports whether that has happened; the Auction* fields are zero until it has.
type StarGiftAuctionRow struct {
	GiftID  int64 `json:"GiftID,string"`
	Title   string
	Stars   int64 `json:"Stars,string"`
	Enabled bool
	// Authoring parameters, as stored on the active catalog revision.
	IsAuction            bool
	Slug                 string
	AvailabilityTotal    int
	AvailabilityRemains  int
	GiftsPerRound        int
	AuctionStartDate     int
	AuctionRoundDuration int
	LockedUntilDate      int
	// Live engine state, valid only when Materialized.
	Materialized  bool
	Status        string
	StartDate     int
	EndDate       int
	RoundDuration int
	TotalRounds   int
	CurrentRound  int
	NextRoundAt   int
	GiftsLeft     int
	LastGiftNum   int
	MinBidAmount  int64 `json:"MinBidAmount,string"`
	// Bid aggregates over the still-active, non-refunded bids.
	ActiveBids int
	TopBid     int64 `json:"TopBid,string"`
	BidTotal   int64 `json:"BidTotal,string"`
	UpdatedAt  time.Time
}

// ListStarGiftAuctions returns every catalog gift that is either an auction or a
// scheduled drop (locked_until_date set), plus any gift that already has an
// auction row, so an auction whose revision was later replaced stays visible.
func (s *readStore) ListStarGiftAuctions(ctx context.Context) ([]StarGiftAuctionRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT c.gift_id, r.title, r.stars, c.enabled,
       r.auction, COALESCE(NULLIF(r.auction_slug, ''), a.slug, ''),
       r.availability_total, c.availability_remains, r.gifts_per_round,
       r.auction_start_date, r.auction_round_duration, r.locked_until_date,
       a.gift_id IS NOT NULL,
       COALESCE(a.status, ''), COALESCE(a.start_date, 0), COALESCE(a.end_date, 0),
       COALESCE(a.round_duration, 0), COALESCE(a.total_rounds, 0), COALESCE(a.current_round, 0),
       COALESCE(a.next_round_at, 0), COALESCE(a.gifts_left, 0), COALESCE(a.last_gift_num, 0),
       COALESCE(a.min_bid_amount, 0),
       COALESCE(b.active_bids, 0), COALESCE(b.top_bid, 0), COALESCE(b.bid_total, 0),
       c.updated_at
FROM star_gift_catalog c
JOIN star_gift_catalog_revisions r ON r.id = c.active_revision_id
LEFT JOIN star_gift_auctions a ON a.gift_id = c.gift_id
LEFT JOIN (
	SELECT gift_id, COUNT(*)::int AS active_bids, MAX(amount) AS top_bid, SUM(amount) AS bid_total
	FROM star_gift_auction_bids
	WHERE active AND NOT returned
	GROUP BY gift_id
) b ON b.gift_id = c.gift_id
WHERE r.auction OR r.locked_until_date > 0 OR a.gift_id IS NOT NULL
ORDER BY r.auction DESC, r.auction_start_date DESC, r.locked_until_date DESC, c.gift_id
LIMIT $1`, domain.MaxStarGiftCatalogSize)
	if err != nil {
		return nil, fmt.Errorf("list star gift auctions: %w", err)
	}
	defer rows.Close()
	out := make([]StarGiftAuctionRow, 0)
	for rows.Next() {
		var row StarGiftAuctionRow
		if err := rows.Scan(
			&row.GiftID, &row.Title, &row.Stars, &row.Enabled,
			&row.IsAuction, &row.Slug,
			&row.AvailabilityTotal, &row.AvailabilityRemains, &row.GiftsPerRound,
			&row.AuctionStartDate, &row.AuctionRoundDuration, &row.LockedUntilDate,
			&row.Materialized,
			&row.Status, &row.StartDate, &row.EndDate,
			&row.RoundDuration, &row.TotalRounds, &row.CurrentRound,
			&row.NextRoundAt, &row.GiftsLeft, &row.LastGiftNum,
			&row.MinBidAmount,
			&row.ActiveBids, &row.TopBid, &row.BidTotal,
			&row.UpdatedAt,
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
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
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
	LastName    string
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
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name, u.last_name, u.verified, u.scam, u.fake,
	COALESCE(b.owner_user_id, 0), u.created_at, u.updated_at
FROM users u
LEFT JOIN bots b ON b.bot_user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
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
		if err := rows.Scan(&item.ID, &item.Username, &item.FirstName, &item.LastName, &item.Verified, &item.Scam, &item.Fake, &item.OwnerUserID, &item.CreatedAt, &item.UpdatedAt); err != nil {
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
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name, u.last_name, u.verified, u.scam, u.fake,
	COALESCE(b.owner_user_id, 0), u.created_at, u.updated_at
FROM users u
LEFT JOIN bots b ON b.bot_user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
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
		if err := rows.Scan(&item.ID, &item.Username, &item.FirstName, &item.LastName, &item.Verified, &item.Scam, &item.Fake, &item.OwnerUserID, &item.CreatedAt, &item.UpdatedAt); err != nil {
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
SELECT u.id, COALESCE(NULLIF(u.username, ''), p.username_lower, ''), u.first_name, u.last_name, u.about, u.verified, u.scam, u.fake,
	COALESCE(b.owner_user_id, 0), COALESCE(b.description, ''),
	u.created_at, u.updated_at
FROM users u
LEFT JOIN bots b ON b.bot_user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
WHERE u.id = $1 AND u.is_bot AND u.deleted_at IS NULL`, botUserID).Scan(
		&out.Bot.ID, &out.Bot.Username, &out.Bot.FirstName, &out.Bot.LastName, &out.About, &out.Bot.Verified, &out.Bot.Scam, &out.Bot.Fake,
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
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
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
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id AND p.editable
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
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id AND p.editable
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
LEFT JOIN peer_usernames p ON p.peer_type = 'channel' AND p.peer_id = c.id AND p.editable
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
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS display_username,
	`+accountCollectibleUsernamesColumn+` AS collectibles
FROM users u
JOIN auth ON auth.user_id = u.id
LEFT JOIN account_restrictions r ON r.user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
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
		if err := rows.Scan(&item.ID, &item.Phone, &item.Username, &item.FirstName, &item.LastName, &item.CreatedAt, &item.UpdatedAt, &item.Frozen, &item.Reason, &item.Verified, &item.Scam, &item.Fake, &item.PremiumUntil, &item.LastActiveAt, &item.DeviceCount, &item.Username, &item.Collectibles); err != nil {
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
	COALESCE(ap.login_email, ''),
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS display_username,
	`+accountCollectibleUsernamesColumn+` AS collectibles
FROM users u
LEFT JOIN account_restrictions r ON r.user_id = u.id
LEFT JOIN stars_balances sb ON sb.user_id = u.id
LEFT JOIN account_passwords ap ON ap.user_id = u.id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = u.id AND p.editable
WHERE u.id = $1`, userID).Scan(
		&out.Account.ID, &out.Account.Phone, &out.Account.Username, &out.Account.FirstName, &out.Account.LastName,
		&out.Account.CreatedAt, &out.Account.UpdatedAt, &out.About, &out.LastSeenAt, &out.Verified, &out.Scam, &out.Fake, &out.Support, &out.Bot,
		&out.Account.Frozen, &out.Account.Reason, &out.Account.PremiumUntil, &out.StarsBalance, &out.StarsGranted, &out.LoginEmail, &out.Account.Username,
		&out.Account.Collectibles,
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

type StickerSetRow struct {
	// ID must round-trip through JSON as a string: these are Telegram-style
	// snowflake ids, well past JS's 2^53 safe-integer limit.
	ID        int64 `json:"ID,string"`
	ShortName string
	Title     string
	Count     int
	Kind      string
	SystemKey string
	Official  bool
	Archived  bool
	Installed bool
	SortOrder int
	CreatedAt time.Time
	// CoverDocumentID is the first document in the set, used as a small
	// thumbnail in the admin list. It is extracted from jsonb as text so it
	// never passes through a JavaScript number.
	CoverDocumentID string
}

// ListStickerSets lists non-system sticker/emoji sets. An empty kind lists all
// non-system kinds; "system" packs stay hidden because dice/default/internal
// resources are not hand-edited from this page.
func (s *readStore) ListStickerSets(ctx context.Context, kind string) ([]StickerSetRow, error) {
	kind = strings.TrimSpace(kind)
	switch kind {
	case "", string(domain.StickerSetKindStickers), string(domain.StickerSetKindEmoji), string(domain.StickerSetKindMasks):
	default:
		return nil, fmt.Errorf("invalid sticker set kind")
	}
	rows, err := s.pool.Query(ctx, `
SELECT id, short_name, title, count, set_kind, system_key, official, archived, installed, sort_order, created_at,
       COALESCE(document_ids->>0, '')
FROM sticker_sets
WHERE deleted = false AND set_kind <> 'system'
  AND ($1 = '' OR set_kind = $1)
ORDER BY set_kind, sort_order, id`, kind)
	if err != nil {
		return nil, fmt.Errorf("list sticker sets: %w", err)
	}
	defer rows.Close()
	out := make([]StickerSetRow, 0)
	for rows.Next() {
		var item StickerSetRow
		if err := rows.Scan(
			&item.ID, &item.ShortName, &item.Title, &item.Count, &item.Kind, &item.SystemKey,
			&item.Official, &item.Archived, &item.Installed, &item.SortOrder, &item.CreatedAt,
			&item.CoverDocumentID,
		); err != nil {
			return nil, fmt.Errorf("scan sticker set: %w", err)
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sticker sets: %w", err)
	}
	return out, nil
}

// StickerSetDocumentIDs returns document ids as strings, not int64, so the
// browser cannot silently round 18-19 digit Telegram document ids.
func (s *readStore) StickerSetDocumentIDs(ctx context.Context, setID int64) ([]string, error) {
	var raw string
	err := s.pool.QueryRow(ctx, `
SELECT document_ids::text
FROM sticker_sets
WHERE id = $1 AND deleted = false`, setID).Scan(&raw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("sticker set documents: %w", err)
	}
	var ids []int64
	if raw != "" && raw != "[]" {
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, fmt.Errorf("decode sticker set documents: %w", err)
		}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = strconv.FormatInt(id, 10)
	}
	return out, nil
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
// ListAccountRatings pages the leaderboard. query is a free-text operator search:
// it matches a username prefix (editable or collectible), a first/last name
// prefix, and -- when the term is numeric -- the user id, so an operator can find
// an account the same way they do on the accounts tab.
func (s *readStore) ListAccountRatings(ctx context.Context, minLevel int, userID, beforeID int64, limit int, query string) ([]AccountRatingRow, bool, error) {
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
	query = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	pattern := ""
	queryUserID := int64(0)
	if query != "" {
		pattern = strings.ToLower(escapeLikePattern(query)) + "%"
		if parsed, err := strconv.ParseInt(query, 10, 64); err == nil && parsed > 0 {
			queryUserID = parsed
		}
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
	AND ($5::text = '' OR (
		($6::bigint <> 0 AND r.user_id = $6)
		OR lower(COALESCE(u.username, '')) LIKE $5
		OR lower(COALESCE(u.first_name, '')) LIKE $5
		OR lower(COALESCE(u.last_name, '')) LIKE $5
		OR EXISTS (
			SELECT 1 FROM peer_usernames pu
			WHERE pu.peer_type = 'user' AND pu.peer_id = r.user_id
				AND pu.username_lower LIKE $5
		)
	))
	AND (
		c.c_user_id IS NULL
		OR r.level < c.c_level
		OR (r.level = c.c_level AND r.stars < c.c_stars)
		OR (r.level = c.c_level AND r.stars = c.c_stars AND r.user_id > c.c_user_id)
	)
ORDER BY r.level DESC, r.stars DESC, r.user_id
LIMIT $4`, minLevel, userID, beforeID, limit+1, pattern, queryUserID)
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

// Official platform verification review queue.
//
// The application record is the audit subject and is read here directly, with the
// applicant resolved through the same users/peer_usernames join every other view
// uses. target_verified is read from the live peer rather than from the
// submission snapshot: a reviewer has to see the badge as it is now, and the
// snapshot columns exist precisely because the live peer may have drifted.
//
// Every int64 is tagged as a JSON string. Application ids, peer ids and the
// optimistic-locking version all exceed the range a JSON number represents
// exactly, and a rounded version would send a decision against the wrong
// revision of the row.
type VerificationApplicationRow struct {
	ID                int64 `json:"ID,string"`
	ApplicantUserID   int64 `json:"ApplicantUserID,string"`
	ApplicantUsername string
	ApplicantName     string
	TargetType        string
	TargetID          int64 `json:"TargetID,string"`
	TargetTitle       string
	TargetUsername    string
	TargetVerified    bool
	Category          string
	Description       string
	OfficialWebsite   string
	SocialLinks       []string
	PressLinks        []string
	AdditionalNote    string
	Status            string
	ReviewerAdminID   string
	DecisionReason    string
	// InternalNote is operator-only: it is the reviewer handover note and is never
	// projected to the applicant. Every caller of this store already holds
	// verification.review.
	InternalNote  string
	CorrelationID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	SubmittedAt   time.Time
	ReviewedAt    time.Time
	Version       int64 `json:"Version,string"`
}

// VerificationEventRow is one entry of the immutable application history.
type VerificationEventRow struct {
	ID         int64 `json:"ID,string"`
	Kind       string
	FromStatus string
	ToStatus   string
	Actor      string
	Reason     string
	Note       string
	CreatedAt  time.Time
}

// VerificationApplicationDetail is the application, its history, and whether the
// applicant still controls the target.
type VerificationApplicationDetail struct {
	Application VerificationApplicationRow
	Events      []VerificationEventRow
	// ApplicantControlsTarget re-derives ownership from the live records, using the
	// same authorities the use-case layer does: the bots table for a bot, the
	// public-channel admin index for a channel or supergroup, identity for a user.
	// Control can be lost between submission and review, and approving a peer the
	// applicant no longer holds is exactly what the flag exists to prevent.
	ApplicantControlsTarget bool
}

const verificationSelectColumns = `va.id, va.applicant_user_id,
	COALESCE(NULLIF(au.username, ''), p.username_lower, '') AS applicant_username,
	TRIM(BOTH ' ' FROM COALESCE(au.first_name, '') || ' ' || COALESCE(au.last_name, '')) AS applicant_name,
	va.target_type, va.target_id, va.target_title, va.target_username,
	COALESCE(tu.verified, tc.verified, false) AS target_verified,
	va.category, va.description, va.official_website, va.social_links, va.press_links,
	va.additional_note, va.status, va.reviewer_admin_id, va.decision_reason,
	va.internal_note, va.correlation_id,
	va.created_at, va.updated_at, va.submitted_at, va.reviewed_at, va.version`

// verificationJoins resolves the applicant and the live target. Bots and users
// live in the user namespace, channels and supergroups in the channel one, so
// both sides are joined and the target type decides which one contributes.
const verificationJoins = `
FROM verification_applications va
LEFT JOIN users au ON au.id = va.applicant_user_id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = va.applicant_user_id AND p.editable
LEFT JOIN users tu ON va.target_type IN ('bot', 'user') AND tu.id = va.target_id
LEFT JOIN channels tc ON va.target_type IN ('channel', 'supergroup') AND tc.id = va.target_id`

func scanVerificationApplicationRow(scan func(dest ...any) error, item *VerificationApplicationRow) error {
	// submitted_at is NULL while the application is still a draft and reviewed_at
	// until a reviewer closes it.
	var submittedAt, reviewedAt *time.Time
	if err := scan(
		&item.ID, &item.ApplicantUserID, &item.ApplicantUsername, &item.ApplicantName,
		&item.TargetType, &item.TargetID, &item.TargetTitle, &item.TargetUsername, &item.TargetVerified,
		&item.Category, &item.Description, &item.OfficialWebsite, &item.SocialLinks, &item.PressLinks,
		&item.AdditionalNote, &item.Status, &item.ReviewerAdminID, &item.DecisionReason,
		&item.InternalNote, &item.CorrelationID,
		&item.CreatedAt, &item.UpdatedAt, &submittedAt, &reviewedAt, &item.Version,
	); err != nil {
		return err
	}
	if submittedAt != nil {
		item.SubmittedAt = submittedAt.UTC()
	}
	if reviewedAt != nil {
		item.ReviewedAt = reviewedAt.UTC()
	}
	if item.SocialLinks == nil {
		item.SocialLinks = []string{}
	}
	if item.PressLinks == nil {
		item.PressLinks = []string{}
	}
	return nil
}

// ListVerificationApplications pages the review queue newest first, keyset by
// descending id. status/targetType/reviewer are exact filters; q matches an
// application id, a target peer id, or a target/applicant username prefix, which
// is how an operator looks a case up from a report.
func (s *readStore) ListVerificationApplications(
	ctx context.Context,
	status, targetType, reviewer, q string,
	beforeID int64,
	limit int,
) ([]VerificationApplicationRow, bool, error) {
	if limit <= 0 {
		limit = verificationListDefaultLimit
	}
	if limit > verificationListMaxLimit {
		limit = verificationListMaxLimit
	}
	status = strings.TrimSpace(status)
	targetType = strings.TrimSpace(targetType)
	reviewer = strings.TrimSpace(reviewer)
	query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	pattern := ""
	queryID := int64(0)
	if query != "" {
		pattern = strings.ToLower(escapeLikePattern(query)) + "%"
		if parsed, err := strconv.ParseInt(query, 10, 64); err == nil && parsed > 0 {
			queryID = parsed
		}
	}
	rows, err := s.pool.Query(ctx, `
SELECT `+verificationSelectColumns+verificationJoins+`
WHERE ($1 = '' OR va.status = $1)
	AND ($2 = '' OR va.target_type = $2)
	AND ($3 = '' OR va.reviewer_admin_id = $3)
	AND ($4::bigint = 0 OR va.id < $4)
	AND ($5::text = '' OR (
		($6::bigint <> 0 AND (va.id = $6 OR va.target_id = $6 OR va.applicant_user_id = $6))
		OR lower(va.target_username) LIKE $5
		OR lower(va.target_title) LIKE $5
		OR lower(COALESCE(au.username, '')) LIKE $5
	))
ORDER BY va.id DESC
LIMIT $7`, status, targetType, reviewer, beforeID, pattern, queryID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list verification applications: %w", err)
	}
	defer rows.Close()
	out := make([]VerificationApplicationRow, 0, limit+1)
	for rows.Next() {
		var item VerificationApplicationRow
		if err := scanVerificationApplicationRow(rows.Scan, &item); err != nil {
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

// VerificationApplicationDetail returns one application with its history and the
// live ownership check. A missing application reports errReadNotFound so the API
// answers 404 rather than 500.
func (s *readStore) VerificationApplicationDetail(ctx context.Context, id int64) (VerificationApplicationDetail, error) {
	var out VerificationApplicationDetail
	row := s.pool.QueryRow(ctx, `
SELECT `+verificationSelectColumns+verificationJoins+`
WHERE va.id = $1`, id)
	// The single-row path reuses the list scanner, so one column order serves
	// both: a drift between them would silently mis-assign columns.
	err := scanVerificationApplicationRow(row.Scan, &out.Application)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, errReadNotFound
		}
		return out, fmt.Errorf("get verification application: %w", err)
	}
	out.Events, err = s.verificationApplicationEvents(ctx, id)
	if err != nil {
		return out, err
	}
	controls, err := s.applicantControlsVerificationTarget(
		ctx, out.Application.ApplicantUserID, out.Application.TargetType, out.Application.TargetID,
	)
	if err != nil {
		return out, err
	}
	out.ApplicantControlsTarget = controls
	return out, nil
}

func (s *readStore) verificationApplicationEvents(ctx context.Context, applicationID int64) ([]VerificationEventRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT id, kind, from_status, to_status, actor, reason, note, created_at
FROM verification_application_events
WHERE application_id = $1
ORDER BY id DESC
LIMIT $2`, applicationID, verificationEventLimit)
	if err != nil {
		return nil, fmt.Errorf("list verification application events: %w", err)
	}
	defer rows.Close()
	out := make([]VerificationEventRow, 0)
	for rows.Next() {
		var item VerificationEventRow
		if err := rows.Scan(
			&item.ID, &item.Kind, &item.FromStatus, &item.ToStatus,
			&item.Actor, &item.Reason, &item.Note, &item.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// VerificationStatusCounts is the queue summary above the list. Every modelled
// status is present with a zero, so the panel never has to tell "none" from
// "missing", and the counts are decimal strings for the same exactness reason as
// the ids.
func (s *readStore) VerificationStatusCounts(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, status := range []domain.VerificationStatus{
		domain.VerificationStatusDraft,
		domain.VerificationStatusSubmitted,
		domain.VerificationStatusInReview,
		domain.VerificationStatusApproved,
		domain.VerificationStatusRejected,
		domain.VerificationStatusCancelled,
	} {
		out[string(status)] = "0"
	}
	rows, err := s.pool.Query(ctx, `
SELECT status, count(*) FROM verification_applications GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count verification applications: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = strconv.FormatInt(count, 10)
	}
	return out, rows.Err()
}

// applicantControlsVerificationTarget re-derives ownership from the live records.
//
// The authorities are the ones app/verification uses, so the panel's answer and
// the approval path's answer cannot disagree: the bots table for a bot (minus
// BotFather, which nobody owns), the public-channel admin index for a channel or
// supergroup, and plain identity for a user account.
func (s *readStore) applicantControlsVerificationTarget(ctx context.Context, applicantUserID int64, targetType string, targetID int64) (bool, error) {
	if applicantUserID <= 0 || targetID <= 0 {
		return false, nil
	}
	switch domain.VerificationTargetType(targetType) {
	case domain.VerificationTargetUser:
		return applicantUserID == targetID, nil
	case domain.VerificationTargetBot:
		var owns bool
		err := s.pool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1 FROM bots b
	WHERE b.bot_user_id = $1 AND b.owner_user_id = $2 AND b.bot_user_id <> $3
)`, targetID, applicantUserID, domain.BotFatherUserID).Scan(&owns)
		if err != nil {
			return false, fmt.Errorf("check verification bot ownership: %w", err)
		}
		return owns, nil
	case domain.VerificationTargetChannel, domain.VerificationTargetSupergroup:
		var admins bool
		err := s.pool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1 FROM user_channel_member_index i
	WHERE i.user_id = $1 AND i.channel_id = $2
		AND i.status = 'active' AND i.role IN ('creator', 'admin')
		AND i.public_username AND NOT i.deleted
)`, applicantUserID, targetID).Scan(&admins)
		if err != nil {
			return false, fmt.Errorf("check verification channel ownership: %w", err)
		}
		return admins, nil
	default:
		return false, nil
	}
}

// Third-party bot verification (core.telegram.org/api/bots/verification).
//
// This is NOT the official platform badge read above. Official verification is a
// boolean on the peer that only the operator sets; third-party verification is an
// attributed mark granted by a verifier bot, carrying that verifier's own custom
// emoji icon and description. The two mechanisms own separate tables and neither
// reads the other's, which is why these queries never touch
// verification_applications or users.verified.
//
// Peer titles and usernames are resolved from the live peer rather than from the
// application's snapshot columns: an operator has to see the peer as it is now,
// and the snapshot is only the fallback for a peer that has since gone. Usernames
// come from the same users/channels + peer_usernames join every other view uses,
// with `AND p.editable` on the peer_usernames side -- a collectible username sits
// in the same table and is not the peer's own editable handle, so joining without
// the predicate would report somebody else's asset as the peer's name.
//
// Every int64 is tagged as a JSON string. Bot ids, peer ids, custom emoji document
// ids and the optimistic-locking version all exceed the range a JSON number
// represents exactly, and a rounded version would send a decision against the
// wrong revision of the row.

// BotVerifierRow is one verifier bot: its operator-granted settings, the catalogue
// name of the icon it marks with, and how many peers it has marked.
type BotVerifierRow struct {
	BotID                      int64 `json:"BotID,string"`
	BotUsername                string
	BotName                    string
	IconDocumentID             int64 `json:"IconDocumentID,string"`
	IconName                   string
	CompanyName                string
	DefaultDescription         string
	CanModifyCustomDescription bool
	Enabled                    bool
	GrantedBy                  string
	GrantReason                string
	// MarkCount is how many peers this verifier currently marks. It is the number
	// that would cascade away with a revocation, so it is counted rather than
	// estimated.
	MarkCount int64 `json:"MarkCount,string"`
	CreatedAt time.Time
	UpdatedAt time.Time
	Version   int64 `json:"Version,string"`
}

// VerificationIconRow is one catalogue entry.
type VerificationIconRow struct {
	ID         int64 `json:"ID,string"`
	DocumentID int64 `json:"DocumentID,string"`
	// OwnerBotID is 0 for a shared entry and a bot id when the operator reserved
	// the icon for one verifier.
	OwnerBotID       int64 `json:"OwnerBotID,string"`
	OwnerBotUsername string
	Name             string
	Active           bool
	// UsedByVerifiers is a plain number: it counts verifier rows pointing at this
	// document and can never approach the exactness limit an id can.
	UsedByVerifiers int `json:"UsedByVerifiers"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// CustomVerificationRow is one granted mark.
type CustomVerificationRow struct {
	ID                  int64 `json:"ID,string"`
	VerifierBotID       int64 `json:"VerifierBotID,string"`
	VerifierBotUsername string
	CompanyName         string
	PeerType            string
	PeerID              int64 `json:"PeerID,string"`
	PeerTitle           string
	PeerUsername        string
	// IconDocumentID is the icon the mark was granted with, denormalised at grant
	// time, so it keeps rendering even after the verifier changes its own.
	IconDocumentID int64 `json:"IconDocumentID,string"`
	Description    string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Version        int64 `json:"Version,string"`
}

// CustomVerificationRequestRow is one application filed with a verifier bot.
type CustomVerificationRequestRow struct {
	ID                   int64 `json:"ID,string"`
	VerifierBotID        int64 `json:"VerifierBotID,string"`
	VerifierBotUsername  string
	ApplicantUserID      int64 `json:"ApplicantUserID,string"`
	ApplicantUsername    string
	PeerType             string
	PeerID               int64 `json:"PeerID,string"`
	PeerTitle            string
	PeerUsername         string
	Reason               string
	RequestedDescription string
	Status               string
	DecidedBy            string
	DecisionReason       string
	// InternalNote is operator-only: it is the reviewer handover note and is never
	// projected to the applicant. Every caller of this store already holds
	// botverification.review.
	InternalNote  string
	CorrelationID string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ApprovedAt    time.Time
	RejectedAt    time.Time
	Version       int64 `json:"Version,string"`
}

// CustomVerificationRequestDetail is one application, the verifier behind it, and
// whether the mark is on the peer right now.
type CustomVerificationRequestDetail struct {
	Request  CustomVerificationRequestRow
	Verifier BotVerifierRow
	// MarkActive tells "approved" apart from "approved and since stripped by the
	// operator", which is the one thing the status alone cannot say.
	MarkActive bool
}

const botVerifierSelectColumns = `s.bot_id,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS bot_username,
	TRIM(BOTH ' ' FROM COALESCE(u.first_name, '') || ' ' || COALESCE(u.last_name, '')) AS bot_name,
	s.icon_document_id, COALESCE(i.name, '') AS icon_name,
	s.company_name, s.default_description, s.can_modify_custom_description,
	s.enabled, s.granted_by, s.grant_reason,
	(SELECT count(*) FROM custom_verifications cv WHERE cv.verifier_bot_id = s.bot_id) AS mark_count,
	s.created_at, s.updated_at, s.version`

// botVerifierJoins resolves the bot account behind the verifier row and the
// catalogue label of its icon. The icon join is by document id, not by catalogue
// id: the settings row stores the document, and an icon dropped from the catalogue
// must still leave the verifier readable.
const botVerifierJoins = `
FROM bot_verifier_settings s
LEFT JOIN users u ON u.id = s.bot_id
LEFT JOIN peer_usernames p ON p.peer_type = 'user' AND p.peer_id = s.bot_id AND p.editable
LEFT JOIN verification_icons i ON i.document_id = s.icon_document_id`

func scanBotVerifierRow(scan func(dest ...any) error, item *BotVerifierRow) error {
	return scan(
		&item.BotID, &item.BotUsername, &item.BotName,
		&item.IconDocumentID, &item.IconName,
		&item.CompanyName, &item.DefaultDescription, &item.CanModifyCustomDescription,
		&item.Enabled, &item.GrantedBy, &item.GrantReason, &item.MarkCount,
		&item.CreatedAt, &item.UpdatedAt, &item.Version,
	)
}

// ListBotVerifiers lists verifier bots, ordered by bot id so the table is stable
// across reloads. enabledOnly hides the ones the operator switched off.
func (s *readStore) ListBotVerifiers(ctx context.Context, enabledOnly bool, limit int) ([]BotVerifierRow, error) {
	limit = clampBotVerificationLimit(limit)
	rows, err := s.pool.Query(ctx, `
SELECT `+botVerifierSelectColumns+botVerifierJoins+`
WHERE NOT $1::boolean OR s.enabled
ORDER BY s.bot_id
LIMIT $2`, enabledOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("list bot verifiers: %w", err)
	}
	defer rows.Close()
	out := make([]BotVerifierRow, 0, limit)
	for rows.Next() {
		var item BotVerifierRow
		if err := scanBotVerifierRow(rows.Scan, &item); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// BotVerifier reads one verifier row, enabled or not: the panel needs the disabled
// one too, to render the kill switch. A missing row reports errReadNotFound.
func (s *readStore) BotVerifier(ctx context.Context, botID int64) (BotVerifierRow, error) {
	var out BotVerifierRow
	row := s.pool.QueryRow(ctx, `
SELECT `+botVerifierSelectColumns+botVerifierJoins+`
WHERE s.bot_id = $1`, botID)
	// The single-row path reuses the list scanner, so one column order serves both:
	// a drift between them would silently mis-assign columns.
	if err := scanBotVerifierRow(row.Scan, &out); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, errReadNotFound
		}
		return out, fmt.Errorf("get bot verifier: %w", err)
	}
	return out, nil
}

// ListVerificationIcons lists the icon catalogue newest first, with the number of
// verifiers each entry is currently configured on -- retiring an entry that
// verifiers still point at is the operator's decision to make knowingly.
func (s *readStore) ListVerificationIcons(ctx context.Context, activeOnly bool, limit int) ([]VerificationIconRow, error) {
	limit = clampBotVerificationLimit(limit)
	rows, err := s.pool.Query(ctx, `
SELECT i.id, i.document_id, i.owner_bot_id,
	COALESCE(NULLIF(u.username, ''), p.username_lower, '') AS owner_bot_username,
	i.name, i.active,
	(SELECT count(*) FROM bot_verifier_settings s WHERE s.icon_document_id = i.document_id) AS used_by_verifiers,
	i.created_at, i.updated_at
FROM verification_icons i
LEFT JOIN users u ON i.owner_bot_id <> 0 AND u.id = i.owner_bot_id
LEFT JOIN peer_usernames p ON i.owner_bot_id <> 0
	AND p.peer_type = 'user' AND p.peer_id = i.owner_bot_id AND p.editable
WHERE NOT $1::boolean OR i.active
ORDER BY i.id DESC
LIMIT $2`, activeOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("list verification icons: %w", err)
	}
	defer rows.Close()
	out := make([]VerificationIconRow, 0, limit)
	for rows.Next() {
		var item VerificationIconRow
		if err := rows.Scan(
			&item.ID, &item.DocumentID, &item.OwnerBotID, &item.OwnerBotUsername,
			&item.Name, &item.Active, &item.UsedByVerifiers,
			&item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// customVerificationPeerJoins resolves a marked or applied-for peer on both sides
// of the namespace. A third-party mark can sit on a user (bots included) or a
// channel, so both are joined and the row's peer_type decides which contributes.
// Each username side carries `AND editable`, so a collectible username parked in
// peer_usernames is never mistaken for the peer's own handle.
const customVerificationPeerJoins = `
LEFT JOIN users tu ON %[1]s.peer_type = 'user' AND tu.id = %[1]s.peer_id
LEFT JOIN peer_usernames tup ON %[1]s.peer_type = 'user'
	AND tup.peer_type = 'user' AND tup.peer_id = %[1]s.peer_id AND tup.editable
LEFT JOIN channels tc ON %[1]s.peer_type = 'channel' AND tc.id = %[1]s.peer_id
LEFT JOIN peer_usernames tcp ON %[1]s.peer_type = 'channel'
	AND tcp.peer_type = 'channel' AND tcp.peer_id = %[1]s.peer_id AND tcp.editable`

// ListCustomVerifications pages granted marks newest first, keyset by descending
// id. verifierBotID/peerType are exact filters; q matches a mark id, a peer id, or
// a peer username prefix, which is how an operator looks a badge up from a report.
func (s *readStore) ListCustomVerifications(
	ctx context.Context,
	verifierBotID int64,
	peerType, q string,
	beforeID int64,
	limit int,
) ([]CustomVerificationRow, bool, error) {
	limit = clampBotVerificationLimit(limit)
	peerType = strings.TrimSpace(peerType)
	pattern, queryID := botVerificationSearchTerms(q)
	rows, err := s.pool.Query(ctx, `
SELECT cv.id, cv.verifier_bot_id,
	COALESCE(NULLIF(vu.username, ''), vp.username_lower, '') AS verifier_bot_username,
	COALESCE(s.company_name, '') AS company_name,
	cv.peer_type, cv.peer_id,
	CASE cv.peer_type
		WHEN 'user' THEN TRIM(BOTH ' ' FROM COALESCE(tu.first_name, '') || ' ' || COALESCE(tu.last_name, ''))
		ELSE COALESCE(tc.title, '')
	END AS peer_title,
	CASE cv.peer_type
		WHEN 'user' THEN COALESCE(NULLIF(tu.username, ''), tup.username_lower, '')
		ELSE COALESCE(NULLIF(tc.username, ''), tcp.username_lower, '')
	END AS peer_username,
	cv.icon_document_id, cv.description, cv.created_at, cv.updated_at, cv.version
FROM custom_verifications cv
LEFT JOIN users vu ON vu.id = cv.verifier_bot_id
LEFT JOIN peer_usernames vp ON vp.peer_type = 'user' AND vp.peer_id = cv.verifier_bot_id AND vp.editable
LEFT JOIN bot_verifier_settings s ON s.bot_id = cv.verifier_bot_id`+
		fmt.Sprintf(customVerificationPeerJoins, "cv")+`
WHERE ($1::bigint = 0 OR cv.verifier_bot_id = $1)
	AND ($2::text = '' OR cv.peer_type = $2)
	AND ($3::bigint = 0 OR cv.id < $3)
	AND ($4::text = '' OR (
		($5::bigint <> 0 AND (cv.id = $5 OR cv.peer_id = $5 OR cv.verifier_bot_id = $5))
		OR lower(COALESCE(tu.username, '')) LIKE $4
		OR lower(COALESCE(tc.username, '')) LIKE $4
		OR lower(COALESCE(tc.title, '')) LIKE $4
	))
ORDER BY cv.id DESC
LIMIT $6`, verifierBotID, peerType, beforeID, pattern, queryID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list custom verifications: %w", err)
	}
	defer rows.Close()
	out := make([]CustomVerificationRow, 0, limit+1)
	for rows.Next() {
		var item CustomVerificationRow
		if err := rows.Scan(
			&item.ID, &item.VerifierBotID, &item.VerifierBotUsername, &item.CompanyName,
			&item.PeerType, &item.PeerID, &item.PeerTitle, &item.PeerUsername,
			&item.IconDocumentID, &item.Description,
			&item.CreatedAt, &item.UpdatedAt, &item.Version,
		); err != nil {
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

const customVerificationRequestSelectColumns = `r.id, r.verifier_bot_id,
	COALESCE(NULLIF(vu.username, ''), vp.username_lower, '') AS verifier_bot_username,
	r.applicant_user_id,
	COALESCE(NULLIF(au.username, ''), ap.username_lower, '') AS applicant_username,
	r.peer_type, r.peer_id,
	CASE r.peer_type
		WHEN 'user' THEN COALESCE(NULLIF(TRIM(BOTH ' ' FROM COALESCE(tu.first_name, '') || ' ' || COALESCE(tu.last_name, '')), ''), r.peer_title)
		ELSE COALESCE(NULLIF(tc.title, ''), r.peer_title)
	END AS peer_title,
	CASE r.peer_type
		WHEN 'user' THEN COALESCE(NULLIF(tu.username, ''), NULLIF(tup.username_lower, ''), r.peer_username)
		ELSE COALESCE(NULLIF(tc.username, ''), NULLIF(tcp.username_lower, ''), r.peer_username)
	END AS peer_username,
	r.reason, r.requested_description, r.status, r.decided_by, r.decision_reason,
	r.internal_note, r.correlation_id,
	r.created_at, r.updated_at, r.approved_at, r.rejected_at, r.version`

// customVerificationRequestJoins resolves the verifier bot, the applicant and the
// target peer. The peer title and username fall back to the application's snapshot
// columns: a peer deleted since it applied still has to render as something the
// reviewer recognises.
var customVerificationRequestJoins = `
FROM custom_verification_requests r
LEFT JOIN users vu ON vu.id = r.verifier_bot_id
LEFT JOIN peer_usernames vp ON vp.peer_type = 'user' AND vp.peer_id = r.verifier_bot_id AND vp.editable
LEFT JOIN users au ON au.id = r.applicant_user_id
LEFT JOIN peer_usernames ap ON ap.peer_type = 'user' AND ap.peer_id = r.applicant_user_id AND ap.editable` +
	fmt.Sprintf(customVerificationPeerJoins, "r")

func scanCustomVerificationRequestRow(scan func(dest ...any) error, item *CustomVerificationRequestRow) error {
	// approved_at is NULL until an approval and rejected_at until a rejection; the
	// table's CHECK constraints keep each in step with the status.
	var approvedAt, rejectedAt *time.Time
	if err := scan(
		&item.ID, &item.VerifierBotID, &item.VerifierBotUsername,
		&item.ApplicantUserID, &item.ApplicantUsername,
		&item.PeerType, &item.PeerID, &item.PeerTitle, &item.PeerUsername,
		&item.Reason, &item.RequestedDescription, &item.Status,
		&item.DecidedBy, &item.DecisionReason, &item.InternalNote, &item.CorrelationID,
		&item.CreatedAt, &item.UpdatedAt, &approvedAt, &rejectedAt, &item.Version,
	); err != nil {
		return err
	}
	if approvedAt != nil {
		item.ApprovedAt = approvedAt.UTC()
	}
	if rejectedAt != nil {
		item.RejectedAt = rejectedAt.UTC()
	}
	return nil
}

// ListCustomVerificationRequests pages the third-party review queue newest first,
// keyset by descending id. status/verifierBotID/peerType are exact filters; q
// matches an application id, a peer id, a verifier id, or a peer/applicant
// username prefix.
func (s *readStore) ListCustomVerificationRequests(
	ctx context.Context,
	status string,
	verifierBotID int64,
	peerType, q string,
	beforeID int64,
	limit int,
) ([]CustomVerificationRequestRow, bool, error) {
	limit = clampBotVerificationLimit(limit)
	status = strings.TrimSpace(status)
	peerType = strings.TrimSpace(peerType)
	pattern, queryID := botVerificationSearchTerms(q)
	rows, err := s.pool.Query(ctx, `
SELECT `+customVerificationRequestSelectColumns+customVerificationRequestJoins+`
WHERE ($1::text = '' OR r.status = $1)
	AND ($2::bigint = 0 OR r.verifier_bot_id = $2)
	AND ($3::text = '' OR r.peer_type = $3)
	AND ($4::bigint = 0 OR r.id < $4)
	AND ($5::text = '' OR (
		($6::bigint <> 0 AND (r.id = $6 OR r.peer_id = $6 OR r.verifier_bot_id = $6 OR r.applicant_user_id = $6))
		OR lower(r.peer_username) LIKE $5
		OR lower(r.peer_title) LIKE $5
		OR lower(COALESCE(tu.username, '')) LIKE $5
		OR lower(COALESCE(tc.username, '')) LIKE $5
		OR lower(COALESCE(au.username, '')) LIKE $5
	))
ORDER BY r.id DESC
LIMIT $7`, status, verifierBotID, peerType, beforeID, pattern, queryID, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list custom verification requests: %w", err)
	}
	defer rows.Close()
	out := make([]CustomVerificationRequestRow, 0, limit+1)
	for rows.Next() {
		var item CustomVerificationRequestRow
		if err := scanCustomVerificationRequestRow(rows.Scan, &item); err != nil {
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

// CustomVerificationRequestDetail returns one application with the verifier behind
// it and whether the mark is live. A missing application reports errReadNotFound so
// the API answers 404 rather than 500.
func (s *readStore) CustomVerificationRequestDetail(ctx context.Context, id int64) (CustomVerificationRequestDetail, error) {
	var out CustomVerificationRequestDetail
	row := s.pool.QueryRow(ctx, `
SELECT `+customVerificationRequestSelectColumns+customVerificationRequestJoins+`
WHERE r.id = $1`, id)
	if err := scanCustomVerificationRequestRow(row.Scan, &out.Request); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, errReadNotFound
		}
		return out, fmt.Errorf("get custom verification request: %w", err)
	}
	// The verifier may have been revoked since the application was filed, and the
	// application survives that (it references users, not the settings row). An
	// absent verifier is reported as a row carrying only its id, so the reviewer can
	// still see which bot it was.
	verifier, err := s.BotVerifier(ctx, out.Request.VerifierBotID)
	switch {
	case err == nil:
		out.Verifier = verifier
	case errors.Is(err, errReadNotFound):
		out.Verifier = BotVerifierRow{BotID: out.Request.VerifierBotID}
	default:
		return out, err
	}
	if err := s.pool.QueryRow(ctx, `
SELECT EXISTS (
	SELECT 1 FROM custom_verifications
	WHERE verifier_bot_id = $1 AND peer_type = $2 AND peer_id = $3
)`, out.Request.VerifierBotID, out.Request.PeerType, out.Request.PeerID).Scan(&out.MarkActive); err != nil {
		return out, fmt.Errorf("check custom verification mark: %w", err)
	}
	return out, nil
}

// CustomVerificationRequestCounts is the queue summary above the list. Every
// modelled status is present, so the panel never has to tell "zero" from "absent",
// and the values are decimal strings for the same exactness reason as the ids.
func (s *readStore) CustomVerificationRequestCounts(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, status := range []domain.CustomVerificationRequestStatus{
		domain.CustomVerificationPending,
		domain.CustomVerificationApproved,
		domain.CustomVerificationRejected,
		domain.CustomVerificationRevoked,
	} {
		out[string(status)] = "0"
	}
	rows, err := s.pool.Query(ctx, `
SELECT status, count(*) FROM custom_verification_requests GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("count custom verification requests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		out[status] = strconv.FormatInt(count, 10)
	}
	return out, rows.Err()
}

// botVerificationSearchTerms turns an operator query into the LIKE pattern and the
// optional exact id the list predicates use. LIKE metacharacters are escaped:
// usernames legitimately contain '_', so an unescaped search for "crypto_" would
// match "cryptoX" instead of the name that was typed.
func botVerificationSearchTerms(q string) (string, int64) {
	query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	if query == "" {
		return "", 0
	}
	pattern := strings.ToLower(escapeLikePattern(query)) + "%"
	queryID := int64(0)
	if parsed, err := strconv.ParseInt(query, 10, 64); err == nil && parsed > 0 {
		queryID = parsed
	}
	return pattern, queryID
}

func clampBotVerificationLimit(limit int) int {
	if limit <= 0 {
		return botVerificationListDefaultLimit
	}
	if limit > botVerificationListMaxLimit {
		return botVerificationListMaxLimit
	}
	return limit
}
