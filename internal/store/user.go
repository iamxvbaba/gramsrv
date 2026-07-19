package store

import (
	"context"

	"telesrv/internal/domain"
)

// UserStore 持久化用户。实现见 store/memory（测试替身）、store/postgres。
type UserStore interface {
	ByID(ctx context.Context, id int64) (domain.User, bool, error)
	ByIDs(ctx context.Context, ids []int64) ([]domain.User, error)
	ByPhone(ctx context.Context, phone string) (domain.User, bool, error)
	ByPhones(ctx context.Context, phones []string) ([]domain.User, error)
	ByUsername(ctx context.Context, username string) (domain.User, bool, error)
	Search(ctx context.Context, currentUserID int64, query, phoneQuery string, limit int) (domain.UserSearchResult, error)
	UpdateProfile(ctx context.Context, userID int64, firstName, lastName, about string) (domain.User, error)
	UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error)
	UpdateLastSeen(ctx context.Context, userID int64, lastSeenAt int) error
	// Create 创建用户并返回分配了 ID 的副本。
	Create(ctx context.Context, u domain.User) (domain.User, error)
	// SetPremiumUntil 把会员到期时间设为绝对 Unix 秒（0 = 清除会员）。
	// 续期累加语义由 app 层先读后算，这里只落绝对值。
	SetPremiumUntil(ctx context.Context, userID int64, until int) (domain.User, error)
	// SetVerified 设置/取消用户认证标记。认证是用户基础事实，读取投影统一下发。
	SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error)
	// SweepExpiredPremium 把到期（premium_expires_at <= now）的会员行清空并
	// 返回清理后的用户（供推送 updateUser）；单次最多处理 limit 行。
	SweepExpiredPremium(ctx context.Context, now int64, limit int) ([]domain.User, error)
	// UpdateEmojiStatus 更新用户自定义 emoji status。零值清除；collectible
	// 必须是完整且与 DocumentID 一致的不可变快照。
	UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error)
	UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error)
	// UpdateBirthday 更新用户生日（零值 Birthday 表示清除）。
	UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error)
	// UpdatePersonalChannel 设置/清除资料页个人频道（channelID=0 表示清除）。
	UpdatePersonalChannel(ctx context.Context, userID int64, channelID int64) (domain.User, error)
}

// UserEmojiStatusEventStore is the aggregate write boundary used by the
// account RPC in durable deployments. The user snapshot, pts event and online
// dispatch row must commit or roll back together.
type UserEmojiStatusEventStore interface {
	UpdateEmojiStatusWithEvent(ctx context.Context, userID int64, status domain.UserEmojiStatus, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, domain.UpdateEvent, error)
}

// UserCache 缓存 viewer 无关的 users 表基础资料。
// 联系人备注、隐私裁剪、头像选择和 presence 不应写入该缓存。
type UserCache interface {
	GetByIDs(ctx context.Context, ids []int64) (map[int64]domain.User, error)
	PutMany(ctx context.Context, users []domain.User) error
	Delete(ctx context.Context, ids []int64) error
}
