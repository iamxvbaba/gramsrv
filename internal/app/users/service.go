package users

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/app/userprojection"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ErrNotAuthorized 表示当前 auth_key 尚未登录。
var ErrNotAuthorized = errors.New("not authorized")

// ProfilePhotoProvider 批量返回用户当前头像（用于把 PhotoID/DCID/Stripped 富化到 domain.User）。
type ProfilePhotoProvider = userprojection.ProfilePhotoProvider

// Service 提供用户查询。
type Service struct {
	users     store.UserStore
	cache     store.UserCache
	contacts  store.ContactStore
	photos    ProfilePhotoProvider
	privacy   userprojection.PrivacyEvaluator
	freezes   userprojection.AccountFreezeProvider
	projector *userprojection.Projector
}

type usernameAvailabilityStore interface {
	CheckUsername(ctx context.Context, userID int64, username string) (bool, error)
}

type moderationFlagAudienceStore interface {
	ModerationFlagAudience(ctx context.Context, userID int64, limit int) ([]int64, error)
}

// Option 调整用户服务可选依赖。
type Option func(*Service)

// WithPhotoProvider 注入头像富化能力（缺省则用户不带头像）。
func WithPhotoProvider(p ProfilePhotoProvider) Option {
	return func(s *Service) { s.photos = p }
}

// WithBaseUserCache injects a viewer-independent user base cache.
func WithBaseUserCache(c store.UserCache) Option {
	return func(s *Service) { s.cache = c }
}

// WithContactStore enables viewer-specific contact name/phone projection.
func WithContactStore(c store.ContactStore) Option {
	return func(s *Service) { s.contacts = c }
}

// WithPrivacyEvaluator enables viewer-specific privacy projection.
func WithPrivacyEvaluator(p userprojection.PrivacyEvaluator) Option {
	return func(s *Service) { s.privacy = p }
}

func WithAccountFreezeProvider(p userprojection.AccountFreezeProvider) Option {
	return func(s *Service) { s.freezes = p }
}

const (
	minUsernameLen      = 5
	maxUsernameLen      = 32
	maxProfileNameRunes = 64
	// bio 长度双档，对齐 appConfig about_length_limit_default=70 /
	// about_length_limit_premium=140；客户端按 self premium flag 选档，
	// 服务端档位必须 ≥ 客户端宣告档位。
	maxProfileAboutRunes        = 70
	maxProfileAboutRunesPremium = 140
	maxBatchUsers               = 1000
)

// NewService 创建用户服务。
func NewService(users store.UserStore, opts ...Option) *Service {
	s := &Service{users: users}
	for _, opt := range opts {
		opt(s)
	}
	s.projector = userprojection.New(
		userprojection.WithContactStore(s.contacts),
		userprojection.WithPhotoProvider(s.photos),
		userprojection.WithPrivacyEvaluator(s.privacy),
		userprojection.WithAccountFreezeProvider(s.freezes),
	)
	return s
}

// loadSelf 加载当前用户但不富化头像（供内部校验路径使用，避免无谓的头像查询）。
func (s *Service) loadSelf(ctx context.Context, userID int64) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, ErrNotAuthorized
	}
	u, found, err := s.loadBaseUserByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, ErrNotAuthorized
	}
	return u, nil
}

// Self 返回当前登录的用户（带头像）。未登录返回 ErrNotAuthorized。
func (s *Service) Self(ctx context.Context, userID int64) (domain.User, error) {
	u, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	return s.projectOne(ctx, userID, u)
}

// ByID 返回指定用户。调用方必须已登录；access_hash 校验在 RPC 边界完成。
func (s *Service) ByID(ctx context.Context, currentUserID, userID int64) (domain.User, bool, error) {
	if currentUserID == 0 {
		return domain.User{}, false, ErrNotAuthorized
	}
	u, found, err := s.loadBaseUserByID(ctx, userID)
	if err != nil {
		return domain.User{}, false, err
	}
	if !found {
		return u, false, nil
	}
	u, err = s.projectOne(ctx, currentUserID, u)
	if err != nil {
		return domain.User{}, false, err
	}
	return u, true, nil
}

// AdminUser 返回 viewer 无关的账号基础事实，供管理用例做 dry-run 与审计。
func (s *Service) AdminUser(ctx context.Context, userID int64) (domain.User, bool, error) {
	if userID == 0 {
		return domain.User{}, false, nil
	}
	return s.loadBaseUserByID(ctx, userID)
}

// PrivacyBaseUsers returns viewer-independent bot/premium facts through the
// shared base-user read model. Privacy uses this as a batched cold loader behind
// its bounded process cache; no viewer projection is performed, avoiding a
// privacy -> users -> privacy recursion.
func (s *Service) PrivacyBaseUsers(ctx context.Context, userIDs []int64) ([]domain.User, error) {
	return s.loadBaseUsersByIDs(ctx, userIDs)
}

// ByIDs 批量返回指定用户。调用方必须已登录；缺失用户不会出现在结果中。
func (s *Service) ByIDs(ctx context.Context, currentUserID int64, userIDs []int64) ([]domain.User, error) {
	if currentUserID == 0 {
		return nil, ErrNotAuthorized
	}
	if len(userIDs) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, id := range userIDs {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) >= maxBatchUsers {
			break
		}
	}
	users, err := s.loadBaseUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return s.projectUsers(ctx, currentUserID, users)
}

// ByIDsForViewers 跨多个 viewer 批量投影同一组 user（fan-out 模板化）：base user 只加载一次，
// 隐私/改名/头像投影经 userprojection.ForViewers 压成 O(owner) 查询。返回 map[viewerID][]User，
// 每个切片与 ByIDs(viewer, ids) 字节等价——**唯一例外是 personal photo overlay**（ForViewers v1
// 跳过，客户端下次 getChannelDifference/getHistory 自愈）。供 channel fan-out 预热每 viewer 投影，
// 把 per-recipient 的 ByIDs(=ForViewer) 折叠成一次跨 viewer 投影。不做 ByIDs 的单 caller 鉴权
// （viewer 是 fan-out 收件人集合，非 RPC 调用方）。
func (s *Service) ByIDsForViewers(ctx context.Context, viewerUserIDs []int64, userIDs []int64) (map[int64][]domain.User, error) {
	if len(viewerUserIDs) == 0 || len(userIDs) == 0 {
		return map[int64][]domain.User{}, nil
	}
	ids := uniqueUserIDs(userIDs, maxBatchUsers)
	base, err := s.loadBaseUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	// projector 为 nil 时 ForViewers 返回各 viewer 的原始 base 副本（与 projectUsers 的 nil 分支一致）。
	return s.projector.ForViewers(ctx, viewerUserIDs, base)
}

// CheckUsername 校验当前用户是否可以占用 username。
func (s *Service) CheckUsername(ctx context.Context, userID int64, username string) (bool, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return false, err
	}
	username = normalizeUsername(username)
	if !validUsername(username) {
		return false, domain.ErrUsernameInvalid
	}
	return s.checkUsernameAvailable(ctx, self.ID, username)
}

func (s *Service) checkUsernameAvailable(ctx context.Context, selfID int64, username string) (bool, error) {
	if checker, ok := s.users.(usernameAvailabilityStore); ok {
		return checker.CheckUsername(ctx, selfID, username)
	}
	u, found, err := s.users.ByUsername(ctx, username)
	if err != nil {
		return false, err
	}
	return !found || u.ID == selfID, nil
}

// UpdateUsername 修改当前用户的主 username。空字符串表示删除 username。
func (s *Service) UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	username = normalizeUsername(username)
	if username != "" {
		if !validUsername(username) {
			return domain.User{}, domain.ErrUsernameInvalid
		}
		ok, err := s.checkUsernameAvailable(ctx, self.ID, username)
		if err != nil {
			return domain.User{}, err
		}
		if !ok {
			return domain.User{}, domain.ErrUsernameOccupied
		}
	}
	if self.Username == username {
		return self, nil
	}
	u, err := s.users.UpdateUsername(ctx, self.ID, username)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// UpdateProfile 修改当前用户的基础资料。未设置的字段保持原值。
func (s *Service) UpdateProfile(ctx context.Context, userID int64, update domain.UserProfileUpdate) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	firstName := self.FirstName
	lastName := self.LastName
	about := self.About
	if update.HasFirstName {
		firstName = strings.TrimSpace(update.FirstName)
	}
	if update.HasLastName {
		lastName = strings.TrimSpace(update.LastName)
	}
	if update.HasAbout {
		about = strings.TrimSpace(update.About)
	}
	if firstName == "" || utf8.RuneCountInString(firstName) > maxProfileNameRunes || utf8.RuneCountInString(lastName) > maxProfileNameRunes {
		return domain.User{}, domain.ErrFirstNameInvalid
	}
	aboutLimit := maxProfileAboutRunes
	if self.PremiumActiveAt(time.Now().Unix()) {
		aboutLimit = maxProfileAboutRunesPremium
	}
	if utf8.RuneCountInString(about) > aboutLimit {
		return domain.User{}, domain.ErrAboutTooLong
	}
	if firstName == self.FirstName && lastName == self.LastName && about == self.About {
		return self, nil
	}
	u, err := s.users.UpdateProfile(ctx, self.ID, firstName, lastName, about)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// UpdateLastSeen records the latest visible account activity time.
func (s *Service) UpdateLastSeen(ctx context.Context, userID int64, lastSeenAt int) error {
	if userID == 0 {
		return ErrNotAuthorized
	}
	if lastSeenAt <= 0 {
		return nil
	}
	if err := s.users.UpdateLastSeen(ctx, userID, lastSeenAt); err != nil {
		return err
	}
	s.dropCachedUsers(ctx, userID)
	return nil
}

// PremiumActive 报告用户当前是否有效会员。走基础用户缓存路径、不做 viewer
// 投影，供限额双档判断（pin 上限、reaction 上限、bio 长度等）低成本调用。
func (s *Service) PremiumActive(ctx context.Context, userID int64) bool {
	if s == nil || userID == 0 {
		return false
	}
	u, found, err := s.loadBaseUserByID(ctx, userID)
	if err != nil || !found {
		return false
	}
	return u.PremiumActiveAt(time.Now().Unix())
}

// GrantPremium 授予/续期会员：未过期则在现有到期时间上累加 months 个月，
// 已过期或首次则从当前时刻起算（对齐官方续期语义）。months<=0 清除会员。
// bot 永不可成为会员。
func (s *Service) GrantPremium(ctx context.Context, userID int64, months int) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, ErrNotAuthorized
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrUserNotFound
	}
	if u.Bot {
		return domain.User{}, domain.ErrPremiumBotUnsupported
	}
	until := 0
	if months > 0 {
		now := time.Now()
		base := now
		if u.PremiumUntil > 0 && int64(u.PremiumUntil) > now.Unix() {
			base = time.Unix(int64(u.PremiumUntil), 0)
		}
		until = int(base.AddDate(0, months, 0).Unix())
	}
	updated, err := s.users.SetPremiumUntil(ctx, userID, until)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, updated)
	return updated, nil
}

// SetVerified 设置/取消用户认证标记。认证是账号基础事实，所有 user 投影统一消费该字段。
func (s *Service) SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, ErrNotAuthorized
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrUserNotFound
	}
	if u.Verified == verified {
		return u, nil
	}
	updated, err := s.users.SetVerified(ctx, userID, verified)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, updated)
	return updated, nil
}

// SetScamFake 设置/取消用户的 scam 与 fake 标记（bot 复用同一路径）。scam/fake
// 是账号基础事实，所有 user 投影统一消费；写后刷新基础缓存以便投影即时可见。
func (s *Service) SetScamFake(ctx context.Context, userID int64, scam, fake bool) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, ErrNotAuthorized
	}
	if scam && fake {
		return domain.User{}, domain.ErrPeerModerationFlagsInvalid
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrUserNotFound
	}
	if u.Scam == scam && u.Fake == fake {
		return u, nil
	}
	updated, err := s.users.SetScamFake(ctx, userID, scam, fake)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, updated)
	return updated, nil
}

// ModerationFlagAudience returns the bounded set of existing viewers that may
// need an immediate updateUser after SCAM/FAKE changes. This is an online
// accelerator only: it does not allocate PTS or create durable update events.
func (s *Service) ModerationFlagAudience(ctx context.Context, userID int64, limit int) ([]int64, error) {
	if userID == 0 {
		return nil, ErrNotAuthorized
	}
	if limit <= 0 || limit > 4096 {
		limit = 4096
	}
	audience, ok := s.users.(moderationFlagAudienceStore)
	if !ok {
		return []int64{userID}, nil
	}
	return audience.ModerationFlagAudience(ctx, userID, limit)
}

// SetSupport 设置/取消用户的 support 标记（官方客服账号）。写后刷新基础缓存。
func (s *Service) SetSupport(ctx context.Context, userID int64, support bool) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, ErrNotAuthorized
	}
	u, found, err := s.users.ByID(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, domain.ErrUserNotFound
	}
	if u.Support == support {
		return u, nil
	}
	updated, err := s.users.SetSupport(ctx, userID, support)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, updated)
	return updated, nil
}

// SweepExpiredPremium 清理到期会员（store 把过期行清 NULL）并失效用户缓存，
// 返回清理后的用户，供 RPC 层向本人在线 session 推 updateUser。premium 下发
// 正确性由读取路径即时派生保证，这里只做收尾与通知。
func (s *Service) SweepExpiredPremium(ctx context.Context, now int64, limit int) ([]domain.User, error) {
	users, err := s.users.SweepExpiredPremium(ctx, now, limit)
	if err != nil || len(users) == 0 {
		return users, err
	}
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		ids = append(ids, u.ID)
	}
	s.dropCachedUsers(ctx, ids...)
	return users, nil
}

// UpdateEmojiStatus 更新当前用户 emoji status（premium 专属；零值清除）。
// 清除不要求会员（到期降级后客户端仍可显式清掉残留状态）。
func (s *Service) UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error) {
	self, err := s.validateEmojiStatusUpdate(ctx, userID, status)
	if err != nil {
		return domain.User{}, err
	}
	u, err := s.users.UpdateEmojiStatus(ctx, self.ID, status)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// UpdateEmojiStatusWithEvent uses the store's aggregate transaction when it
// is available. The bool reports whether the returned event was durably
// appended with dispatch; lightweight memory/test wiring falls back to the
// ordinary state write and lets the RPC's Updates service append the event.
func (s *Service) UpdateEmojiStatusWithEvent(ctx context.Context, userID int64, status domain.UserEmojiStatus, date int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, domain.UpdateEvent, bool, error) {
	self, err := s.validateEmojiStatusUpdate(ctx, userID, status)
	if err != nil {
		return domain.User{}, domain.UpdateEvent{}, false, err
	}
	writer, ok := s.users.(store.UserEmojiStatusEventStore)
	if !ok {
		u, err := s.users.UpdateEmojiStatus(ctx, self.ID, status)
		if err == nil {
			s.refreshCachedUsers(ctx, u)
		}
		return u, domain.UpdateEvent{}, false, err
	}
	event := domain.UpdateEvent{
		Type:        domain.UpdateEventUserEmojiStatus,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: self.ID},
		EmojiStatus: status,
		Date:        date,
		PtsCount:    1,
	}
	u, event, err := writer.UpdateEmojiStatusWithEvent(ctx, self.ID, status, event, excludeAuthKeyID, excludeSessionID)
	if err != nil {
		return domain.User{}, domain.UpdateEvent{}, false, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, event, true, nil
}

func (s *Service) validateEmojiStatusUpdate(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !status.Valid() {
		return domain.User{}, domain.ErrStarGiftCollectibleInvalid
	}
	if !status.Empty() && !self.PremiumActiveAt(time.Now().Unix()) {
		return domain.User{}, domain.ErrPremiumRequired
	}
	return self, nil
}

// UpdateBirthday 设置/清除用户生日（account.updateBirthday）。零值 Birthday 表示清除。
func (s *Service) UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if birthday.IsSet() {
		if !domain.ValidBirthday(birthday) {
			return domain.User{}, domain.ErrBirthdayInvalid
		}
	} else {
		birthday = domain.Birthday{} // 归一化为清除
	}
	u, err := s.users.UpdateBirthday(ctx, self.ID, birthday)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// UpdatePersonalChannel 设置/清除资料页个人频道（account.updatePersonalChannel）；
// channelID=0 表示清除。频道存在性与「调用者是其成员」由 RPC 层在调用前校验。
func (s *Service) UpdatePersonalChannel(ctx context.Context, userID int64, channelID int64) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	u, err := s.users.UpdatePersonalChannel(ctx, self.ID, channelID)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// UpdateColor updates the user's message accent or profile background color.
func (s *Service) UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	u, err := s.users.UpdateColor(ctx, self.ID, forProfile, color)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// ResolveUsername 解析 username 到用户；调用方必须已登录。
func (s *Service) ResolveUsername(ctx context.Context, currentUserID int64, username string) (domain.User, bool, error) {
	if _, err := s.loadSelf(ctx, currentUserID); err != nil {
		return domain.User{}, false, err
	}
	username = normalizeUsername(username)
	if !validUsername(username) {
		return domain.User{}, false, domain.ErrUsernameInvalid
	}
	u, found, err := s.users.ByUsername(ctx, username)
	if err != nil || !found {
		return u, found, err
	}
	s.putCachedUsers(ctx, u)
	u, err = s.projectOne(ctx, currentUserID, u)
	if err != nil {
		return domain.User{}, false, err
	}
	return u, true, nil
}

// ResolvePhone resolves a phone number only when the target's AddedByPhone
// privacy allows the current viewer. The evaluator is backed by owner-level
// privacy/contact snapshots in production, so this adds no per-rule SQL query.
func (s *Service) ResolvePhone(ctx context.Context, currentUserID int64, phone string) (domain.User, bool, error) {
	if _, err := s.loadSelf(ctx, currentUserID); err != nil {
		return domain.User{}, false, err
	}
	phone = normalizePhone(phone)
	if phone == "" {
		return domain.User{}, false, domain.ErrPhoneNotOccupied
	}
	u, found, err := s.users.ByPhone(ctx, phone)
	if err != nil || !found {
		return u, found, err
	}
	s.putCachedUsers(ctx, u)
	if s.privacy != nil && u.ID != currentUserID {
		allowed := false
		var err error
		if batch, ok := s.privacy.(userprojection.BatchPrivacyEvaluator); ok {
			visibility, batchErr := batch.CanSeeBatch(
				ctx,
				[]int64{u.ID},
				currentUserID,
				[]domain.PrivacyKey{domain.PrivacyKeyAddedByPhone},
			)
			err = batchErr
			allowed = visibility[u.ID][domain.PrivacyKeyAddedByPhone]
		} else {
			allowed, err = s.privacy.CanSee(ctx, u.ID, currentUserID, domain.PrivacyKeyAddedByPhone)
		}
		if err != nil {
			return domain.User{}, false, err
		}
		if !allowed {
			return domain.User{}, false, domain.ErrPhoneNotOccupied
		}
	}
	u, err = s.projectOne(ctx, currentUserID, u)
	if err != nil {
		return domain.User{}, false, err
	}
	return u, true, nil
}

func (s *Service) projectUsers(ctx context.Context, viewerUserID int64, users []domain.User) ([]domain.User, error) {
	if s == nil || s.projector == nil {
		return users, nil
	}
	return s.projector.ForViewer(ctx, viewerUserID, users)
}

func (s *Service) loadBaseUserByID(ctx context.Context, userID int64) (domain.User, bool, error) {
	users, err := s.loadBaseUsersByIDs(ctx, []int64{userID})
	if err != nil {
		return domain.User{}, false, err
	}
	if len(users) == 0 {
		return domain.User{}, false, nil
	}
	return users[0], true, nil
}

func (s *Service) loadBaseUsersByIDs(ctx context.Context, userIDs []int64) ([]domain.User, error) {
	ids := uniqueUserIDs(userIDs, maxBatchUsers)
	if len(ids) == 0 {
		return nil, nil
	}
	loaded := make(map[int64]domain.User, len(ids))
	misses := append([]int64(nil), ids...)
	if s.cache != nil {
		if cached, err := s.cache.GetByIDs(ctx, ids); err == nil && len(cached) > 0 {
			for id, u := range cached {
				if u.ID != 0 && u.EmojiStatusCollectible.Empty() {
					loaded[id] = u
				}
			}
			misses = make([]int64, 0, len(ids))
			for _, id := range ids {
				if _, ok := loaded[id]; !ok {
					misses = append(misses, id)
				}
			}
		}
	}
	if len(misses) > 0 {
		users, err := s.users.ByIDs(ctx, misses)
		if err != nil {
			return nil, err
		}
		for _, u := range users {
			if u.ID != 0 {
				loaded[u.ID] = u
			}
		}
		s.putCachedUsers(ctx, users...)
	}
	out := make([]domain.User, 0, len(ids))
	for _, id := range ids {
		if u, ok := loaded[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *Service) refreshCachedUsers(ctx context.Context, users ...domain.User) {
	ids := make([]int64, 0, len(users))
	for _, u := range users {
		if u.ID != 0 {
			ids = append(ids, u.ID)
		}
	}
	s.dropCachedUsers(ctx, ids...)
	s.putCachedUsers(ctx, users...)
}

func (s *Service) putCachedUsers(ctx context.Context, users ...domain.User) {
	if s.cache == nil || len(users) == 0 {
		return
	}
	cacheable := make([]domain.User, 0, len(users))
	for _, user := range users {
		// Collectible ownership may change inside the star-gift aggregate. Keep
		// these uncommon users on the authoritative store path so the database
		// lifecycle trigger can never be masked by a stale base-user cache entry.
		if user.ID != 0 && user.EmojiStatusCollectible.Empty() {
			cacheable = append(cacheable, user)
		}
	}
	if len(cacheable) > 0 {
		_ = s.cache.PutMany(ctx, cacheable)
	}
}

func (s *Service) dropCachedUsers(ctx context.Context, userIDs ...int64) {
	if s.cache == nil || len(userIDs) == 0 {
		return
	}
	_ = s.cache.Delete(ctx, userIDs)
}

func uniqueUserIDs(ids []int64, limit int) []int64 {
	if len(ids) == 0 {
		return nil
	}
	out := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func (s *Service) projectOne(ctx context.Context, viewerUserID int64, user domain.User) (domain.User, error) {
	if s == nil || s.projector == nil {
		return user, nil
	}
	return s.projector.One(ctx, viewerUserID, user)
}

func normalizeUsername(username string) string {
	username = strings.TrimSpace(username)
	username = strings.TrimPrefix(username, "@")
	return strings.TrimSpace(username)
}

func validUsername(username string) bool {
	if len(username) < minUsernameLen || len(username) > maxUsernameLen {
		return false
	}
	for i := 0; i < len(username); i++ {
		c := username[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		case c == '_':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func normalizePhone(phone string) string {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(phone))
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
