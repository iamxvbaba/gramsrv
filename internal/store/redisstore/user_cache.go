package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"telesrv/internal/domain"
)

const DefaultUserCacheTTL = 5 * time.Minute

// UserCache stores viewer-independent base user rows in Redis.
type UserCache struct {
	c   *redis.Client
	ttl time.Duration
}

func NewUserCache(c *redis.Client, ttl time.Duration) *UserCache {
	if ttl <= 0 {
		ttl = DefaultUserCacheTTL
	}
	return &UserCache{c: c, ttl: ttl}
}

func userBaseKey(id int64) string {
	return fmt.Sprintf("user:base:%d", id)
}

type userBaseValue struct {
	ID          int64  `json:"id"`
	AccessHash  int64  `json:"access_hash"`
	Phone       string `json:"phone"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	About       string `json:"about"`
	Username    string `json:"username"`
	CountryCode string `json:"country_code"`
	Verified    bool   `json:"verified"`
	Support     bool   `json:"support"`
	// scam / fake likewise must round-trip through the cache: losing them makes the cache-hit
	// path emit flagged accounts as ordinary accounts, causing the profile SCAM/FAKE flags to
	// intermittently disappear depending on cache hit/miss (same pitfall as the bot field).
	Scam bool `json:"scam,omitempty"`
	Fake bool `json:"fake,omitempty"`
	// The bot field must round-trip through the cache: losing it makes the cache-hit path emit
	// a bot as an ordinary user, polluting the client's local cache (TDesktop's bot flag is
	// irreversible).
	Bot            bool `json:"bot,omitempty"`
	BotInfoVersion int  `json:"bot_info_version,omitempty"`
	// premium / emoji status likewise must round-trip through the cache: losing them makes the
	// cache-hit path emit a Premium user as non-Premium, drifting state across paths (same
	// pitfall as the bot field).
	PremiumUntil           int                           `json:"premium_until,omitempty"`
	EmojiStatusDocumentID  int64                         `json:"emoji_status_document_id,omitempty"`
	EmojiStatusUntil       int                           `json:"emoji_status_until,omitempty"`
	EmojiStatusCollectible domain.EmojiStatusCollectible `json:"emoji_status_collectible,omitempty"`
	// birthday / personal channel likewise must round-trip through the cache: losing them on
	// the cache-hit path makes a just-saved birthday / personal channel revert to zero when the
	// profile is reopened (same pitfall as the bot/premium fields).
	BirthdayDay                   int   `json:"birthday_day,omitempty"`
	BirthdayMonth                 int   `json:"birthday_month,omitempty"`
	BirthdayYear                  int   `json:"birthday_year,omitempty"`
	PersonalChannelID             int64 `json:"personal_channel_id,omitempty"`
	ColorSet                      bool  `json:"color_set,omitempty"`
	Color                         int   `json:"color,omitempty"`
	ColorBackgroundEmojiID        int64 `json:"color_background_emoji_id,omitempty"`
	ProfileColorSet               bool  `json:"profile_color_set,omitempty"`
	ProfileColor                  int   `json:"profile_color,omitempty"`
	ProfileColorBackgroundEmojiID int64 `json:"profile_color_background_emoji_id,omitempty"`
	LastSeenAt                    int   `json:"last_seen_at"`
	// deleted must round-trip through the cache, the same pitfall as the bot/premium/scam fields
	// above: the first read after account deletion writes the tombstone into the cache, and if
	// that bit is lost, every subsequent hit turns the tombstone back into "a live user with an
	// empty name and LastSeenAt 0", and the client then renders it as a "recently online" stranger
	// instead of a Deleted Account. DeletionSource/Reason are likewise audit fields.
	Deleted        bool   `json:"deleted,omitempty"`
	DeletedAt      int64  `json:"deleted_at,omitempty"`
	DeletionSource string `json:"deletion_source,omitempty"`
	DeletionReason string `json:"deletion_reason,omitempty"`
}

func baseValueFromUser(u domain.User) userBaseValue {
	return userBaseValue{
		ID:                            u.ID,
		AccessHash:                    u.AccessHash,
		Phone:                         u.Phone,
		FirstName:                     u.FirstName,
		LastName:                      u.LastName,
		About:                         u.About,
		Username:                      u.Username,
		CountryCode:                   u.CountryCode,
		Verified:                      u.Verified,
		Support:                       u.Support,
		Scam:                          u.Scam,
		Fake:                          u.Fake,
		Bot:                           u.Bot,
		BotInfoVersion:                u.BotInfoVersion,
		PremiumUntil:                  u.PremiumUntil,
		EmojiStatusDocumentID:         u.EmojiStatusDocumentID,
		EmojiStatusUntil:              u.EmojiStatusUntil,
		EmojiStatusCollectible:        u.EmojiStatusCollectible,
		BirthdayDay:                   u.Birthday.Day,
		BirthdayMonth:                 u.Birthday.Month,
		BirthdayYear:                  u.Birthday.Year,
		PersonalChannelID:             u.PersonalChannelID,
		ColorSet:                      u.Color.HasColor,
		Color:                         u.Color.Color,
		ColorBackgroundEmojiID:        u.Color.BackgroundEmojiID,
		ProfileColorSet:               u.ProfileColor.HasColor,
		ProfileColor:                  u.ProfileColor.Color,
		ProfileColorBackgroundEmojiID: u.ProfileColor.BackgroundEmojiID,
		LastSeenAt:                    u.LastSeenAt,
		Deleted:                       u.Deleted,
		DeletedAt:                     u.DeletedAt,
		DeletionSource:                string(u.DeletionSource),
		DeletionReason:                u.DeletionReason,
	}
}

func (v userBaseValue) user() domain.User {
	return domain.User{
		ID:                     v.ID,
		AccessHash:             v.AccessHash,
		Phone:                  v.Phone,
		FirstName:              v.FirstName,
		LastName:               v.LastName,
		About:                  v.About,
		Username:               v.Username,
		CountryCode:            v.CountryCode,
		Verified:               v.Verified,
		Support:                v.Support,
		Scam:                   v.Scam,
		Fake:                   v.Fake,
		Bot:                    v.Bot,
		BotInfoVersion:         v.BotInfoVersion,
		PremiumUntil:           v.PremiumUntil,
		EmojiStatusDocumentID:  v.EmojiStatusDocumentID,
		EmojiStatusUntil:       v.EmojiStatusUntil,
		EmojiStatusCollectible: v.EmojiStatusCollectible,
		Birthday:               domain.Birthday{Day: v.BirthdayDay, Month: v.BirthdayMonth, Year: v.BirthdayYear},
		PersonalChannelID:      v.PersonalChannelID,
		Color: domain.PeerColor{
			HasColor:          v.ColorSet,
			Color:             v.Color,
			BackgroundEmojiID: v.ColorBackgroundEmojiID,
		},
		ProfileColor: domain.PeerColor{
			HasColor:          v.ProfileColorSet,
			Color:             v.ProfileColor,
			BackgroundEmojiID: v.ProfileColorBackgroundEmojiID,
		},
		LastSeenAt:     v.LastSeenAt,
		Deleted:        v.Deleted,
		DeletedAt:      v.DeletedAt,
		DeletionSource: domain.AccountDeletionSource(v.DeletionSource),
		DeletionReason: v.DeletionReason,
	}
}

func (s *UserCache) GetByIDs(ctx context.Context, ids []int64) (map[int64]domain.User, error) {
	if s == nil || s.c == nil || len(ids) == 0 {
		return map[int64]domain.User{}, nil
	}
	unique := uniqueCacheUserIDs(ids)
	if len(unique) == 0 {
		return map[int64]domain.User{}, nil
	}
	keys := make([]string, 0, len(unique))
	for _, id := range unique {
		keys = append(keys, userBaseKey(id))
	}
	values, err := s.c.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("redis mget user base: %w", err)
	}
	out := make(map[int64]domain.User, len(values))
	corruptKeys := make([]string, 0)
	for i, value := range values {
		if value == nil {
			continue
		}
		raw, ok := value.(string)
		if !ok {
			corruptKeys = append(corruptKeys, keys[i])
			continue
		}
		var decoded userBaseValue
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded.ID == 0 || decoded.ID != unique[i] {
			corruptKeys = append(corruptKeys, keys[i])
			continue
		}
		out[decoded.ID] = decoded.user()
	}
	if len(corruptKeys) > 0 {
		_ = s.c.Del(ctx, corruptKeys...).Err()
	}
	return out, nil
}

func (s *UserCache) PutMany(ctx context.Context, users []domain.User) error {
	if s == nil || s.c == nil || len(users) == 0 {
		return nil
	}
	pipe := s.c.Pipeline()
	writes := 0
	seen := make(map[int64]struct{}, len(users))
	for _, u := range users {
		if u.ID == 0 {
			continue
		}
		if _, ok := seen[u.ID]; ok {
			continue
		}
		seen[u.ID] = struct{}{}
		raw, err := json.Marshal(baseValueFromUser(u))
		if err != nil {
			return fmt.Errorf("marshal user base cache: %w", err)
		}
		pipe.Set(ctx, userBaseKey(u.ID), raw, s.ttl)
		writes++
	}
	if writes == 0 {
		return nil
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis set user base: %w", err)
	}
	return nil
}

func (s *UserCache) Delete(ctx context.Context, ids []int64) error {
	if s == nil || s.c == nil || len(ids) == 0 {
		return nil
	}
	unique := uniqueCacheUserIDs(ids)
	if len(unique) == 0 {
		return nil
	}
	keys := make([]string, 0, len(unique))
	for _, id := range unique {
		keys = append(keys, userBaseKey(id))
	}
	if err := s.c.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("redis delete user base: %w", err)
	}
	return nil
}

func uniqueCacheUserIDs(ids []int64) []int64 {
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
	}
	return out
}
