package postgres

import (
	"context"
	"errors"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

var (
	ErrChannelIDAllocatorMissing        = errors.New("postgres channel store: Redis channel id allocator is required")
	ErrChannelMessageIDAllocatorMissing = errors.New("postgres channel store: Redis channel message id allocator is required")
)

type missingChannelIDAllocator struct{}

func (missingChannelIDAllocator) NextChannelID(context.Context) (int64, error) {
	return 0, ErrChannelIDAllocatorMissing
}

func (missingChannelIDAllocator) CurrentChannelID(context.Context) (int64, error) {
	return 0, ErrChannelIDAllocatorMissing
}

type missingChannelMessageIDAllocator struct{}

func (missingChannelMessageIDAllocator) NextChannelMessageID(context.Context, int64) (int, error) {
	return 0, ErrChannelMessageIDAllocatorMissing
}

func (missingChannelMessageIDAllocator) CurrentChannelMessageID(context.Context, int64) (int, error) {
	return 0, ErrChannelMessageIDAllocatorMissing
}

const channelDialogQueryLimit = 500

const channelDialogCandidateLimit = 10000

const channelMemberFilterBatch = 1000

const retryableChannelTxAttempts = 3

// ChannelStore 用 PostgreSQL 实现 store.ChannelStore。
type ChannelStore struct {
	db              sqlcgen.DBTX
	ids             store.ChannelIDAllocator
	msgIDs          store.ChannelMessageIDAllocator
	log             *zap.Logger
	rowCache        *ChannelRowCache
	topMsgCache     *ChannelTopMessageCache
	memberCache     *ChannelMemberCache
	dialogCache     *ChannelDialogCache
	boostCache      *ChannelBoostCache
	differenceCache *ChannelDifferenceBaseCache
	// paidReactionStartingGrant mirrors app/stars lazy grant policy inside the
	// paid reaction settlement transaction.
	paidReactionStartingGrant int64
}

// ChannelStoreOption 调整 PostgreSQL ChannelStore 依赖。
type ChannelStoreOption func(*ChannelStore)

// WithChannelAllocators 注入 Redis-backed channel id / message id allocator。
func WithChannelAllocators(ids store.ChannelIDAllocator, msgIDs store.ChannelMessageIDAllocator) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.ids = ids
		s.msgIDs = msgIDs
	}
}

// WithChannelLogger 注入频道 store 日志器，用于追踪 channel pts 分配。
func WithChannelLogger(log *zap.Logger) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.log = log
	}
}

// WithChannelRowCache 注入「共享频道行」进程内缓存(由 ReadModelChangeListener 实时失效)。
// 传 nil 等于禁用，不影响任何读路径行为。
func WithChannelRowCache(cache *ChannelRowCache) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.rowCache = cache
	}
}

// WithChannelTopMessageCache injects the shared dialog-top message cache. The
// cache never contains viewer reaction/read overlays.
func WithChannelTopMessageCache(cache *ChannelTopMessageCache) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.topMsgCache = cache
	}
}

// WithChannelMemberCache 注入「频道成员/访问态」进程内缓存。
// 传 nil 等于禁用；事务内仍绕过，提交后由 read model listener 失效。
func WithChannelMemberCache(cache *ChannelMemberCache) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.memberCache = cache
	}
}

// WithChannelDialogCache 注入 viewer-scoped 频道 dialog 投影缓存。
// 传 nil 等于禁用；事务内仍绕过，避免写后读旧 dialog/read boundary。
func WithChannelDialogCache(cache *ChannelDialogCache) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.dialogCache = cache
	}
}

// WithChannelBoostCache 注入频道 boost 读投影缓存(SelfBoostsApplied + peer total)。
// 传 nil 等于禁用；事务内仍绕过，避免 send 权限判断读到旧值。
func WithChannelBoostCache(cache *ChannelBoostCache) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.boostCache = cache
	}
}

// WithChannelDifferenceBaseCache injects the shared immutable event/message
// page cache used by updates.getChannelDifference. Viewer access and overlays
// remain outside this cache.
func WithChannelDifferenceBaseCache(cache *ChannelDifferenceBaseCache) ChannelStoreOption {
	return func(s *ChannelStore) {
		s.differenceCache = cache
	}
}

// WithChannelStarsStartingGrant keeps paid reaction's atomic debit aligned
// with the configured app/stars lazy first-use grant.
func WithChannelStarsStartingGrant(amount int64) ChannelStoreOption {
	return func(s *ChannelStore) {
		if amount > 0 {
			s.paidReactionStartingGrant = amount
		} else {
			s.paidReactionStartingGrant = 0
		}
	}
}

// cacheActive 报告当前句柄是否可用频道行缓存：仅启用缓存且走连接池(非事务)时。
// 事务内(db != s.db)一律绕过缓存实时读，保证事务读己写。
func (s *ChannelStore) cacheActive(db sqlcgen.DBTX) bool {
	return s.rowCache != nil && db == s.db
}

func (s *ChannelStore) topMessageCacheActive(db sqlcgen.DBTX) bool {
	return s.topMsgCache != nil && db == s.db
}

func (s *ChannelStore) memberCacheActive(db sqlcgen.DBTX) bool {
	return s.memberCache != nil && db == s.db
}

func (s *ChannelStore) dialogCacheActive(db sqlcgen.DBTX) bool {
	return s.dialogCache != nil && db == s.db
}

func (s *ChannelStore) boostCacheActive(db sqlcgen.DBTX) bool {
	return s.boostCache != nil && db == s.db
}

// NewChannelStore 基于 pgx 连接池（或事务）创建 ChannelStore。
func NewChannelStore(db sqlcgen.DBTX, opts ...ChannelStoreOption) *ChannelStore {
	s := &ChannelStore{db: db, paidReactionStartingGrant: domain.DefaultStarsStartingGrant}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.ids == nil {
		s.ids = missingChannelIDAllocator{}
	}
	if s.msgIDs == nil {
		s.msgIDs = missingChannelMessageIDAllocator{}
	}
	return s
}

const channelColumns = `c.id, c.access_hash, c.creator_user_id, c.title, c.about, COALESCE(c.username, ''), c.verified, c.scam, c.fake, c.gigagroup,
c.broadcast, c.megagroup, c.forum, c.forum_tabs, c.autotranslation, c.restricted_sponsored, c.broadcast_messages_allowed, c.send_paid_messages_stars, c.noforwards, c.join_to_send, c.join_request, c.signatures, c.signature_profiles, c.pre_history_hidden, c.participants_hidden, c.antispam,
EXISTS (SELECT 1 FROM channel_invites ci WHERE ci.channel_id = c.id AND NOT ci.revoked) AS has_link,
c.linked_chat_id, c.linked_community_id, c.monoforum, c.linked_monoforum_id, c.slowmode_seconds, c.boosts_unrestrict, c.default_banned_rights::text,
c.available_reactions::text, c.color_set, c.color, c.color_background_emoji_id, c.profile_color_set, c.profile_color, c.profile_color_background_emoji_id, c.emoji_status_document_id, c.emoji_status_until,
c.wallpaper::text, c.participants_count, c.admins_count, c.kicked_count, c.banned_count, c.top_message_id, c.pinned_message_id, c.pts,
c.ttl_period, c.date, c.deleted, c.photo_id, c.photo_dc_id, c.photo_stripped,
c.active_call_id, c.active_call_access_hash, c.active_call_not_empty`

const channelMessageColumns = `channel_id, id, random_id, sender_user_id, from_peer_type, from_peer_id,
send_as_peer_type, send_as_peer_id, message_date, edit_date, post, silent, noforwards, body,
entities::text, reply_to::text, reply_to_msg_id, reply_to_peer_type, reply_to_peer_id, reply_to_top_id,
fwd_from::text, discussion_channel_id, discussion_message_id, action::text, pts, deleted, media::text,
reply_markup::text, rich_message::text, ttl_period, expires_at, views_count, post_author, pinned, via_bot_id, grouped_id, from_boosts_applied, saved_peer_type, saved_peer_id, paid_message_stars, suggested_post::text, show_message_sender, factcheck::text`

const channelForumTopicColumns = `channel_id, topic_id, creator_user_id, title, icon_color, icon_emoji_id,
title_missing, closed, hidden, pinned, pinned_order, date, top_message_id, read_inbox_max_id,
read_outbox_max_id, unread_count, unread_mentions_count, unread_reactions_count,
unread_poll_votes_count`

const (
	messageHistoryLoadBackward messageHistoryLoad = iota
	messageHistoryLoadForward
	messageHistoryLoadAround
)
