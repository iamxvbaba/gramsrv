package domain

import "time"

// UserIDSequenceBase 是普通用户 ID 的起始值。
//
// 取 2026-06-01 00:00:00 Asia/Shanghai 的 Unix 秒级时间戳。
// 777000 等兼容系统账号低于该区间，业务注册用户从这里开始递增。
const UserIDSequenceBase int64 = 1780243200

// PeerColor is a domain-only representation of Telegram peerColor.
// HasColor preserves explicit color=0, which is distinct from "color unset".
type PeerColor struct {
	HasColor          bool
	Color             int
	BackgroundEmojiID int64
}

// EmojiStatusCollectible is the immutable projection needed to render a
// collectible gift as an emoji status.  The source of truth remains the owned
// UniqueStarGift; users store an immutable snapshot so every user projection,
// online update and offline difference observes the same shape without an
// RPC-layer lookup.
type EmojiStatusCollectible struct {
	CollectibleID     int64  `json:"collectible_id"`
	DocumentID        int64  `json:"document_id"`
	Title             string `json:"title"`
	Slug              string `json:"slug"`
	PatternDocumentID int64  `json:"pattern_document_id"`
	CenterColor       int    `json:"center_color"`
	EdgeColor         int    `json:"edge_color"`
	PatternColor      int    `json:"pattern_color"`
	TextColor         int    `json:"text_color"`
}

// Empty reports whether no collectible status is present.
func (s EmojiStatusCollectible) Empty() bool {
	return s == (EmojiStatusCollectible{})
}

// Valid enforces the complete collectible status shape.  Partial snapshots
// are forbidden because clients would otherwise render a gradient without its
// model/pattern or be unable to resolve the collectible link.
func (s EmojiStatusCollectible) Valid() bool {
	if s.CollectibleID <= 0 || s.DocumentID <= 0 || s.PatternDocumentID <= 0 ||
		s.Title == "" || s.Slug == "" {
		return false
	}
	for _, color := range []int{s.CenterColor, s.EdgeColor, s.PatternColor, s.TextColor} {
		if color < 0 || color > 0xffffff {
			return false
		}
	}
	return true
}

// UserEmojiStatus is the protocol-neutral mutation value accepted by the user
// service/store boundary.  Exactly one of a normal document or a complete
// collectible snapshot may be active; the zero value clears the status.
type UserEmojiStatus struct {
	DocumentID  int64                  `json:"document_id"`
	Until       int                    `json:"until,omitempty"`
	Collectible EmojiStatusCollectible `json:"collectible,omitempty"`
}

func (s UserEmojiStatus) Empty() bool {
	return s.DocumentID == 0 && s.Collectible.Empty()
}

func (s UserEmojiStatus) Valid() bool {
	if s.Until < 0 {
		return false
	}
	if s.Empty() {
		return s.Until == 0
	}
	if s.DocumentID <= 0 {
		return false
	}
	if s.Collectible.Empty() {
		return true
	}
	return s.Collectible.Valid() && s.DocumentID == s.Collectible.DocumentID
}

// Empty reports whether no explicit color/profile color state is set.
func (c PeerColor) Empty() bool {
	return !c.HasColor && c.BackgroundEmojiID == 0
}

// User 是一个账号。第一阶段仅保留登录链路必须字段；
// access_hash 为任何 InputUser 校验所必须，不可省。
type User struct {
	ID          int64
	AccessHash  int64
	Phone       string
	FirstName   string
	LastName    string
	About       string
	Username    string
	CountryCode string
	Verified    bool
	Scam        bool
	Fake        bool
	Support     bool
	Contact     bool
	Mutual      bool
	CloseFriend bool
	// RestrictionReasons are transient, viewer-scoped unavailability reasons.
	// They are produced after loading the viewer-independent base user and must
	// never be persisted in users or the base-user cache.
	RestrictionReasons []UserRestrictionReason
	// ContactNote/ContactNoteEntities are transient viewer-scoped contact
	// projection fields. They must never be persisted into users or a
	// viewer-independent base-user cache.
	ContactNote         string
	ContactNoteEntities []MessageEntity
	// Bot 标识 bot 账号；置位时 BotInfoVersion 必须 ≥1（TDesktop 只认
	// user TL 是否携带 bot_info_version 字段，且与 bot flag 共用 bit14）。
	Bot            bool
	BotInfoVersion int
	// PremiumUntil 是会员到期 Unix 秒；0 表示非会员。premium 状态的唯一权威
	// 来源是该字段，由读取路径经 PremiumActiveAt 即时派生——到期后无需等
	// 后台 sweeper 翻转即停止下发 premium（sweeper 只负责清理与通知推送）。
	PremiumUntil int
	// EmojiStatusDocumentID / EmojiStatusUntil 是用户自定义 emoji status
	//（premium 专属，account.updateEmojiStatus）。DocumentID==0 表示未设置；
	// Until==0 表示永久。EmojiStatusCollectible 非零时 DocumentID 必须等于
	// collectible 的 model document id。
	EmojiStatusDocumentID  int64
	EmojiStatusUntil       int
	EmojiStatusCollectible EmojiStatusCollectible
	// Birthday 是用户公开生日（account.updateBirthday）。零值表示未设置。
	Birthday Birthday
	// PersonalChannelID 是资料页展示的「个人频道」（account.updatePersonalChannel）；
	// 0 表示未设置。资料投影时按它取频道对象与最新一帖。
	PersonalChannelID int64
	// LinkedCommunityID is the single Community containing this bot. Ordinary
	// users must keep it zero; the community aggregate enforces that invariant.
	LinkedCommunityID int64
	Color             PeerColor
	ProfileColor      PeerColor
	// Profile photo fields are filled by app-layer user projection. PhotoID==0 表示无头像。
	PhotoID       int64
	PhotoDCID     int
	PhotoStripped []byte
	PhotoPersonal bool
	PhotoHasVideo bool
	LastSeenAt    int
	Status        UserStatus
	// Deleted is the durable tombstone state. Deleted users remain addressable by
	// ID so historical messages can render "Deleted Account", but all profile
	// and reusable identity fields are cleared at the store boundary.
	Deleted         bool
	DeletedAt       int64
	DeletionSource  AccountDeletionSource
	DeletionReason  string
	CreatedAt       time.Time
	AccountDeleteAt time.Time
}

// UserRestrictionReason is the protocol-neutral form of Telegram's
// restrictionReason. Platform "all" applies to TDesktop and official mobile
// clients; Text is intentionally server supplied and directly user-visible.
type UserRestrictionReason struct {
	Platform string
	Reason   string
	Text     string
}

func AccountFrozenRestrictionReasons() []UserRestrictionReason {
	return []UserRestrictionReason{{
		Platform: "all",
		Reason:   "frozen",
		Text:     "This account is frozen.",
	}}
}

// PremiumActiveAt 报告用户在 now（Unix 秒）时刻是否为有效会员。
// bot 永不为会员（官方语义；授予路径同样排除 bot，这里是双保险）。
func (u User) PremiumActiveAt(now int64) bool {
	return !u.Bot && u.PremiumUntil > 0 && int64(u.PremiumUntil) > now
}

// EmojiStatusActiveAt 报告用户在 now（Unix 秒）时刻是否有生效的 emoji status
// （已设置且未过期；Until==0 表示永久）。emoji status 是 premium 专属，到期
// 降级后即便列仍有残值也不再下发。
func (u User) EmojiStatusActiveAt(now int64) bool {
	if !u.PremiumActiveAt(now) || !u.EmojiStatus().Valid() || u.EmojiStatusDocumentID == 0 {
		return false
	}
	return u.EmojiStatusUntil == 0 || int64(u.EmojiStatusUntil) > now
}

// EmojiStatus returns the complete status snapshot carried by this user.
func (u User) EmojiStatus() UserEmojiStatus {
	return UserEmojiStatus{
		DocumentID:  u.EmojiStatusDocumentID,
		Until:       u.EmojiStatusUntil,
		Collectible: u.EmojiStatusCollectible,
	}
}

// DeletedTombstone strips every viewer-dependent or personally identifying
// field while preserving the immutable id and lifecycle audit facts.
func (u User) DeletedTombstone() User {
	if !u.Deleted {
		return u
	}
	return User{
		ID:              u.ID,
		AccessHash:      u.AccessHash,
		Deleted:         true,
		DeletedAt:       u.DeletedAt,
		DeletionSource:  u.DeletionSource,
		DeletionReason:  u.DeletionReason,
		CreatedAt:       u.CreatedAt,
		AccountDeleteAt: u.AccountDeleteAt,
		Status:          UserStatus{Kind: UserStatusEmpty},
	}
}

// UserStatusKind is a protocol-neutral account presence state.
type UserStatusKind int

const (
	UserStatusUnknown UserStatusKind = iota
	UserStatusOnline
	UserStatusOffline
	UserStatusRecently
	UserStatusLastWeek
	UserStatusLastMonth
	UserStatusEmpty
)

// UserStatus describes the currently visible presence state for a user.
//
// Expires and WasOnline are absolute Unix timestamps in seconds, matching
// Telegram's UserStatus semantics without leaking tg.* into domain.
type UserStatus struct {
	Kind      UserStatusKind
	Expires   int
	WasOnline int
}

// Birthday 是用户公开生日。Day/Month 为 0 表示未设置；Year 为 0 表示只填了月日不含年份。
type Birthday struct {
	Day   int
	Month int
	Year  int
}

// IsSet 报告生日是否已设置（必须有合法月日）。
func (b Birthday) IsSet() bool {
	return b.Day != 0 && b.Month != 0
}

// ValidBirthday 校验月/日（年份可选）是否在合法范围内。清除生日传零值即可（IsSet 为 false）。
func ValidBirthday(b Birthday) bool {
	if b.Month < 1 || b.Month > 12 || b.Day < 1 || b.Day > 31 {
		return false
	}
	if b.Year != 0 && (b.Year < 1900 || b.Year > 2100) {
		return false
	}
	return true
}

// UserProfileUpdate 描述 account.updateProfile 的可选字段更新。
type UserProfileUpdate struct {
	FirstName    string
	HasFirstName bool
	LastName     string
	HasLastName  bool
	About        string
	HasAbout     bool
}

// UserFullView is the app-layer personalized full user view consumed by RPC.
type UserFullView struct {
	User                   User
	ProfilePhoto           *Photo
	PersonalPhoto          *Photo
	FallbackPhoto          *Photo
	About                  string
	PhoneCallsAvailable    bool
	PhoneCallsPrivate      bool
	VideoCallsAvailable    bool
	VoiceMessagesForbidden bool
	ReadDatesPrivate       bool
}
