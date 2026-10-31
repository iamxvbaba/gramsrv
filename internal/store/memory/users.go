package memory

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// UserStore is the in-memory implementation of store.UserStore. Its ID uses the same business starting point as the PG identity.
type UserStore struct {
	mu               sync.RWMutex
	byID             map[int64]domain.User
	nextID           int64
	usernameRegistry *CollectibleUsernameStore
}

// NewUserStore creates an in-memory UserStore. The built-in system accounts are
// pre-populated, matching postgres migration seeds so both stores behave the same.
func NewUserStore() *UserStore {
	s := &UserStore{byID: make(map[int64]domain.User), nextID: domain.UserIDSequenceBase}
	for _, id := range domain.SystemUserIDs() {
		if u, ok := domain.SystemUserByID(id); ok {
			s.byID[u.ID] = u
		}
	}
	return s
}

// AttachUsernameRegistry gives the memory backend the same global username
// index the PostgreSQL stores share through peer_usernames.
func (s *UserStore) AttachUsernameRegistry(registry *CollectibleUsernameStore) {
	s.mu.Lock()
	s.usernameRegistry = registry
	s.mu.Unlock()
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
	// A bot/system account phone may be an empty string, so an empty query must report not-found (matching postgres behavior).
	if phone == "" {
		return domain.User{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.byID {
		if !u.Deleted && u.Phone == phone {
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
		if u.Deleted {
			continue
		}
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

func (s *UserStore) ByUsername(ctx context.Context, username string) (domain.User, bool, error) {
	username = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(username, "@")))
	if username == "" {
		return domain.User{}, false, nil
	}
	s.mu.RLock()
	registry := s.usernameRegistry
	for _, u := range s.byID {
		if !u.Deleted && strings.ToLower(u.Username) == username {
			s.mu.RUnlock()
			return u, true, nil
		}
	}
	s.mu.RUnlock()
	if registry != nil {
		if peer, ok := registry.activeUsernamePeer(username, domain.PeerTypeUser); ok {
			return s.ByID(ctx, peer.ID)
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
		if !u.Deleted && strings.ToLower(u.Username) == username && id != userID {
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
	registry := s.usernameRegistry
	s.mu.RUnlock()
	var usernameMatches map[int64]int
	if registry != nil {
		usernameMatches = registry.activeUsernameMatches(query, domain.PeerTypeUser)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	users := make([]domain.User, 0)
	for _, u := range s.byID {
		if u.ID == currentUserID || u.Deleted {
			continue
		}
		_, usernameMatch := usernameMatches[u.ID]
		if usernameMatch || userMatchesSearch(u, query, phoneQuery) {
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

func (s *UserStore) UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error) {
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	usernameLower := strings.ToLower(username)
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUsernameNotOccupied
	}
	if usernameLower != "" {
		for id, existing := range s.byID {
			if id != userID && strings.ToLower(existing.Username) == usernameLower {
				return domain.User{}, domain.ErrUsernameOccupied
			}
		}
	}
	if s.usernameRegistry != nil {
		if _, err := s.usernameRegistry.SetEditableUsername(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: userID}, username); err != nil {
			return domain.User{}, err
		}
	}
	u.Username = username
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdatePhone(_ context.Context, userID int64, phone string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	for id, existing := range s.byID {
		if id != userID && !existing.Deleted && existing.Phone == phone {
			return domain.User{}, domain.ErrPhoneNumberOccupied
		}
	}
	u.Phone = phone
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdateProfile(_ context.Context, userID int64, firstName, lastName, about string) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
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
	if !ok || u.Deleted {
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
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.PersonalChannelID = channelID
	s.byID[userID] = u
	return u, nil
}

// bumpBotInfoVersion increments the bot's bot_info_version (bot rows only) and returns the new
// value. It is called by same-package BotStore metadata updates, aligning with postgres' in-transaction bump.
func (s *UserStore) bumpBotInfoVersion(userID int64) (int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted || !u.Bot {
		return 0, false
	}
	u.BotInfoVersion++
	s.byID[userID] = u
	return u.BotInfoVersion, true
}

// updateBotProfile partially updates the bot's first_name/about (name/about of setBotInfo).
func (s *UserStore) updateBotProfile(userID int64, setName bool, name string, setAbout bool, about string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted || !u.Bot {
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

// SetPremiumUntil sets the Premium expiry to an absolute Unix second (0 = clear Premium).
func (s *UserStore) SetPremiumUntil(_ context.Context, userID int64, until int) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	if until < 0 {
		until = 0
	}
	u.PremiumUntil = until
	s.byID[userID] = u
	return u, nil
}

// SetVerified sets/clears the user's verified flag.
func (s *UserStore) SetVerified(_ context.Context, userID int64, verified bool) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.Verified = verified
	s.byID[userID] = u
	return u, nil
}

// SetSupport sets/clears the user's support flag (matching postgres semantics).
func (s *UserStore) SetSupport(_ context.Context, userID int64, support bool) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.Support = support
	s.byID[userID] = u
	return u, nil
}

// SetScamFake sets/clears the user's scam and fake flags (matching postgres semantics).
func (s *UserStore) SetScamFake(_ context.Context, userID int64, scam, fake bool) (domain.User, error) {
	if scam && fake {
		return domain.User{}, domain.ErrPeerModerationFlagsInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	u.Scam = scam
	u.Fake = fake
	s.byID[userID] = u
	return u, nil
}

// SweepExpiredPremium clears expired Premium rows and returns the cleaned-up users (matching postgres semantics).
func (s *UserStore) SweepExpiredPremium(_ context.Context, now int64, limit int) ([]domain.User, error) {
	if limit <= 0 {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.User, 0)
	for id, u := range s.byID {
		if u.Deleted || u.PremiumUntil <= 0 || int64(u.PremiumUntil) > now {
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

// UpdateEmojiStatus updates the user's custom emoji status (a zero value clears it).
func (s *UserStore) UpdateEmojiStatus(_ context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
		return domain.User{}, domain.ErrUserNotFound
	}
	if !status.Valid() {
		return domain.User{}, domain.ErrStarGiftCollectibleInvalid
	}
	u.EmojiStatusDocumentID = status.DocumentID
	u.EmojiStatusUntil = status.Until
	u.EmojiStatusCollectible = status.Collectible
	s.byID[userID] = u
	return u, nil
}

func (s *UserStore) UpdateColor(_ context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.byID[userID]
	if !ok || u.Deleted {
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
	if !ok || u.Deleted {
		return domain.ErrUsernameNotOccupied
	}
	if lastSeenAt > u.LastSeenAt {
		u.LastSeenAt = lastSeenAt
		s.byID[userID] = u
	}
	return nil
}

func (s *UserStore) UpdateLastSeenBatch(ctx context.Context, updates []store.UserLastSeenUpdate) error {
	latest := make(map[int64]int, len(updates))
	for _, update := range updates {
		if update.UserID == 0 || update.LastSeenAt <= 0 {
			continue
		}
		if current := latest[update.UserID]; update.LastSeenAt > current {
			latest[update.UserID] = update.LastSeenAt
		}
	}
	for userID, lastSeenAt := range latest {
		if err := s.UpdateLastSeen(ctx, userID, lastSeenAt); err != nil {
			return err
		}
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
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	s.byID[u.ID] = u
	return u, nil
}
