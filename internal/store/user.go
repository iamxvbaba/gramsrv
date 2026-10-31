package store

import (
	"context"

	"telesrv/internal/domain"
)

// UserStore persists users. See store/memory (test double) and store/postgres for implementations.
type UserStore interface {
	ByID(ctx context.Context, id int64) (domain.User, bool, error)
	ByIDs(ctx context.Context, ids []int64) ([]domain.User, error)
	ByPhone(ctx context.Context, phone string) (domain.User, bool, error)
	ByPhones(ctx context.Context, phones []string) ([]domain.User, error)
	ByUsername(ctx context.Context, username string) (domain.User, bool, error)
	Search(ctx context.Context, currentUserID int64, query, phoneQuery string, limit int) (domain.UserSearchResult, error)
	UpdateProfile(ctx context.Context, userID int64, firstName, lastName, about string) (domain.User, error)
	UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error)
	UpdatePhone(ctx context.Context, userID int64, phone string) (domain.User, error)
	UpdateLastSeen(ctx context.Context, userID int64, lastSeenAt int) error
	// Create creates a user and returns the copy with an ID assigned.
	Create(ctx context.Context, u domain.User) (domain.User, error)
	// SetPremiumUntil sets the Premium expiry to an absolute Unix timestamp (0 = clear Premium).
	// Renewal accumulation semantics are computed read-then-write in the app layer; here only the
	// absolute value is stored.
	SetPremiumUntil(ctx context.Context, userID int64, until int) (domain.User, error)
	// SetVerified sets/clears the user's verified flag. Verification is a base user fact, delivered uniformly by read projections.
	SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error)
	// SetScamFake sets/clears the user's scam and fake flags (bots reuse the same path).
	SetScamFake(ctx context.Context, userID int64, scam, fake bool) (domain.User, error)
	// SetSupport sets/clears the user's support flag (official support account).
	SetSupport(ctx context.Context, userID int64, support bool) (domain.User, error)
	// SweepExpiredPremium clears expired (premium_expires_at <= now) Premium rows and returns
	// the cleaned users (for pushing updateUser); at most limit rows per call.
	SweepExpiredPremium(ctx context.Context, now int64, limit int) ([]domain.User, error)
	// UpdateEmojiStatus updates the user's custom emoji status. A zero value clears it; a
	// collectible must be a complete immutable snapshot consistent with DocumentID.
	UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error)
	UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error)
	// UpdateBirthday updates the user's birthday (a zero Birthday clears it).
	UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error)
	// UpdatePersonalChannel sets/clears the profile personal channel (channelID=0 clears it).
	UpdatePersonalChannel(ctx context.Context, userID int64, channelID int64) (domain.User, error)
}

// UserLastSeenUpdate is one monotonic durable presence watermark. Writers must
// apply the maximum timestamp for duplicate user IDs and must never move the
// stored value backwards.
type UserLastSeenUpdate struct {
	UserID     int64
	LastSeenAt int
}

// UserLastSeenBatchStore is the optional production write boundary used by
// lifecycle presence batching. It deliberately remains separate from
// UserStore so narrow test stores and read-only projections do not acquire a
// fake batch capability accidentally.
type UserLastSeenBatchStore interface {
	UpdateLastSeenBatch(ctx context.Context, updates []UserLastSeenUpdate) error
}

// UserEmojiStatusEventStore is the aggregate write boundary used by the
// account RPC in durable deployments. The user snapshot, pts event and online
// dispatch row must commit or roll back together.
type UserEmojiStatusEventStore interface {
	UpdateEmojiStatusWithEvent(ctx context.Context, userID int64, status domain.UserEmojiStatus, event domain.UpdateEvent, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, domain.UpdateEvent, error)
}

// UserCache caches viewer-independent base user profile data.
// Contact notes, privacy clipping, photo selection, and presence must not be written to this cache.
type UserCache interface {
	GetByIDs(ctx context.Context, ids []int64) (map[int64]domain.User, error)
	PutMany(ctx context.Context, users []domain.User) error
	Delete(ctx context.Context, ids []int64) error
}
