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
	Support     bool
	Contact     bool
	Mutual      bool
	CloseFriend bool
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
	// Until==0 表示永久。
	EmojiStatusDocumentID int64
	EmojiStatusUntil      int
	// Birthday 是用户公开生日（account.updateBirthday）。零值表示未设置。
	Birthday Birthday
	// PersonalChannelID 是资料页展示的「个人频道」（account.updatePersonalChannel）；
	// 0 表示未设置。资料投影时按它取频道对象与最新一帖。
	PersonalChannelID int64
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

// PremiumActiveAt 报告用户在 now（Unix 秒）时刻是否为有效会员。
// bot 永不为会员（官方语义；授予路径同样排除 bot，这里是双保险）。
func (u User) PremiumActiveAt(now int64) bool {
	return !u.Bot && u.PremiumUntil > 0 && int64(u.PremiumUntil) > now
}

// EmojiStatusActiveAt 报告用户在 now（Unix 秒）时刻是否有生效的 emoji status
// （已设置且未过期；Until==0 表示永久）。emoji status 是 premium 专属，到期
// 降级后即便列仍有残值也不再下发。
func (u User) EmojiStatusActiveAt(now int64) bool {
	if !u.PremiumActiveAt(now) || u.EmojiStatusDocumentID == 0 {
		return false
	}
	return u.EmojiStatusUntil == 0 || int64(u.EmojiStatusUntil) > now
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
