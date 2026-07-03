package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"telesrv/internal/domain"
)

// UserStore 是 store.UserStore 的内存实现。ID 与 PG identity 使用同一业务起点。
type UserStore struct {
	mu     sync.RWMutex
	byID   map[int64]domain.User
	nextID int64
}

// NewUserStore 创建内存 UserStore。内置系统账号（777000 / BotFather / Stickers / ChatBot）
// 预置进表，与 postgres 的迁移种子保持双 store 行为一致。
func NewUserStore() *UserStore {
	s := &UserStore{byID: make(map[int64]domain.User), nextID: domain.UserIDSequenceBase}
	for _, id := range []int64{domain.OfficialSystemUserID, domain.BotFatherUserID, domain.StickersBotUserID, domain.ChatBotUserID} {
		if u, ok := domain.SystemUserByID(id); ok {
			s.byID[u.ID] = u
		}
	}
	return s
}

func (s *UserStore) ByID(_ context.Context, id int64) (domain.User, bool, error) {
	s.mu.RLock()
	u, ok := s.byID[id]
	s.mu.RUnlock()
	return u, ok, nil
}

func (s *UserStore) ByIDs(_ context.Context, ids []int64) ([]domain.User, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.User, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if u, ok := s.byID[id]; ok {
			out = append(out, u)
		}
	}
	return out, nil
}

func (s *UserStore) ByPhone(_ context.Context, phone string) (domain.User, bool, error) {
	// bot/系统账号 phone 可为空串，空查询必须判未找到（与 postgres 行为一致）。
	if phone == "" {
		return domain.User{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.byID {
		if u.Phone == phone {
			return u, true, nil
		}
	}
	return domain.User{}, false, nil
}

func (s *UserStore) ByPhones(_ context.Context, phones []string) ([]domain.User, error) {
	if len(phones) == 0 {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	want := make(map[string]struct{}, len(phones))
	for _, phone := range phones {
		if phone != "" {
			want[phone] = struct{}{}
		}
	}
	out := make([]domain.User, 0, len(want))
	seenIDs := map[int64]struct{}{}
	for _, u := range s.byID {
		if _, ok := want[u.Phone]; !ok {
			continue
		}
		if _, ok := seenIDs[u.ID]; ok {
			continue
		}
		seenIDs[u.ID] = struct{}{}
		out = append(out, u)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *UserStore) ByUsername(_ context.Context, username string) (domain.User, bool, error) {
	username = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if username == "" {
		return domain.User{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.byID {
		if strings.ToLower(u.Username) == username {
			return u, true, nil
		}
	}
	return domain.User{}, false, nil
}

func (s *UserStore) CheckUsername(_ context.Context, userID int64, username string) (bool, error) {
	username = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if username == "" {
		return true, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, u := range s.byID {
		if strings.ToLower(u.Username) == username && id != userID {
			return false, nil
		}
	}
	return true, nil
}

func (s *UserStore) Search(_ context.Context, currentUserID int64, query, phoneQuery string, limit int) (domain.UserSearchResult, error) {
	if limit <= 0 {
		limit = 50
	}
	query = strings.ToLower(strings.TrimSpace(query))
	phoneQuery = strings.TrimSpace(phoneQuery)
	if query == "" {
		return domain.UserSearchResult{}, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]domain.User, 0)
	for _, u := range s.byID {
		if u.ID == currentUserID {
			continue
		}
		if userMatchesSearch(u, query, phoneQuery) {
			users = append(users, u)
		}
	}
	sort.SliceStable(users, func(i, j int) bool {
		return users[i].ID < users[j].ID
	})
	if len(users) > limit {
		users = users[:limit]
	}
	return domain.UserSearchResult{Results: users}, nil
}

func (s *UserStore) UpdateUsername(_ context.Context, userID int64, username string) (domain.User, error) {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	usernameLower := strings.ToLower(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUsernameNotOccupied
	}
	if usernameLower != "" {
		for id, existing := range s.byID {
			if id != userID && strings.ToLower(existing.Username) == usernameLower {
				return domain.User{}, domain.ErrUsernameOccupied
			}
		}
	}
	u.Username = username
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdateProfile(_ context.Context, userID int64, firstName, lastName, about string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUsernameNotOccupied
	}
	u.FirstName = firstName
	u.LastName = lastName
	u.About = about
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdateBirthday(_ context.Context, userID int64, birthday domain.Birthday) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.Birthday = birthday
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdatePersonalChannel(_ context.Context, userID int64, channelID int64) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.PersonalChannelID = channelID
	s.byID[userID] = u
	return u, nil
}

// bumpBotInfoVersion 递增 bot 的 bot_info_version（仅 bot 行），返回新值。供同包
// BotStore 元数据更新调用，与 postgres 的事务内 bump 对齐。
func (s *UserStore) bumpBotInfoVersion(userID int64) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || !u.Bot {
		return 0, false
	}
	u.BotInfoVersion++
	s.byID[userID] = u
	return u.BotInfoVersion, true
}

// updateBotProfile 部分更新 bot 的 first_name/about（setBotInfo 的 name/about）。
func (s *UserStore) updateBotProfile(userID int64, setName bool, name string, setAbout bool, about string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || !u.Bot {
		return false
	}
	if setName {
		u.FirstName = name
	}
	if setAbout {
		u.About = about
	}
	s.byID[userID] = u
	return true
}

// SetPremiumUntil 把会员到期时间设为绝对 Unix 秒（0 = 清除会员）。
func (s *UserStore) SetPremiumUntil(_ context.Context, userID int64, until int) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	if until < 0 {
		until = 0
	}
	u.PremiumUntil = until
	s.byID[userID] = u
	return u, nil
}

// SetVerified 设置/取消用户认证标记。
func (s *UserStore) SetVerified(_ context.Context, userID int64, verified bool) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.Verified = verified
	s.byID[userID] = u
	return u, nil
}

// SweepExpiredPremium 清空到期会员行并返回清理后的用户（与 postgres 语义一致）。
func (s *UserStore) SweepExpiredPremium(_ context.Context, now int64, limit int) ([]domain.User, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.User, 0)
	for id, u := range s.byID {
		if u.PremiumUntil <= 0 || int64(u.PremiumUntil) > now {
			continue
		}
		u.PremiumUntil = 0
		s.byID[id] = u
		out = append(out, u)
		if len(out) >= limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// UpdateEmojiStatus 更新用户自定义 emoji status（documentID=0 表示清除）。
func (s *UserStore) UpdateEmojiStatus(_ context.Context, userID int64, documentID int64, until int) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	if documentID == 0 {
		until = 0
	}
	u.EmojiStatusDocumentID = documentID
	u.EmojiStatusUntil = until
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdateColor(_ context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	if forProfile {
		u.ProfileColor = color
	} else {
		u.Color = color
	}
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdateLastSeen(_ context.Context, userID int64, lastSeenAt int) error {
	if lastSeenAt <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok {
		return domain.ErrUsernameNotOccupied
	}
	if lastSeenAt > u.LastSeenAt {
		u.LastSeenAt = lastSeenAt
		s.byID[userID] = u
	}
	return nil
}

func userMatchesSearch(u domain.User, query, phoneQuery string) bool {
	if phoneQuery != "" && strings.HasPrefix(u.Phone, phoneQuery) {
		return true
	}
	first := strings.ToLower(u.FirstName)
	last := strings.ToLower(u.LastName)
	username := strings.ToLower(u.Username)
	fullName := strings.TrimSpace(first + " " + last)
	return strings.Contains(first, query) ||
		strings.Contains(last, query) ||
		strings.Contains(fullName, query) ||
		strings.Contains(username, query)
}

func (s *UserStore) Create(_ context.Context, u domain.User) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	username := strings.ToLower(strings.TrimSpace(u.Username))
	if username != "" {
		for _, existing := range s.byID {
			if strings.ToLower(existing.Username) == username {
				return domain.User{}, domain.ErrUsernameOccupied
			}
		}
	}
	u.ID = s.nextID
	s.nextID++
	s.byID[u.ID] = u
	return u, nil
}
