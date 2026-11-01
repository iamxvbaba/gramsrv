package store

import (
	"context"
	"fmt"

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
	UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error)
	// UpdateBirthday updates the user's birthday (a zero Birthday clears it).
	UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error)
	UpdateEmojiStatusWithEvent(ctx context.Context, userID int64, status domain.UserEmojiStatus, event domain.UpdateEvent) (domain.User, domain.UpdateEvent, error)
	UpdatePersonalChannelWithDelivery(ctx context.Context, userID int64, channelID int64, effects DeliveryEffectsBuilder[UserDeliverySnapshot]) (domain.User, error)
}

type UserDeliverySnapshot struct {
	User            domain.User
	Usernames       []domain.Username
	UsernamesLoaded bool
}

type UserDeliveryPayloadBuilder func(UserDeliverySnapshot) ([]byte, error)

// UserDeliveryStore is the aggregate write boundary for non-PTS profile
// refreshes. The user row and edge_delivery_outbox row must commit or roll back
// together; the payload builder receives the updated user snapshot while the
// transaction is still open.
type UserDeliveryStore interface {
	UpdateProfileWithDelivery(ctx context.Context, userID int64, firstName, lastName, about string, build UserDeliveryPayloadBuilder, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, error)
	UpdateUsernameWithDelivery(ctx context.Context, userID int64, username string, build UserDeliveryPayloadBuilder, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, error)
	UpdateBirthdayWithDelivery(ctx context.Context, userID int64, birthday domain.Birthday, build UserDeliveryPayloadBuilder, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, error)
	UpdateColorWithDelivery(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor, build UserDeliveryPayloadBuilder, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, error)
}

func ValidateUserSelfDeliveryEffects(snapshot UserDeliverySnapshot, effects []DeliveryEffect) error {
	if snapshot.User.ID <= 0 || len(effects) != 1 {
		return fmt.Errorf("self user delivery requires exactly one effect")
	}
	effect := effects[0]
	if err := effect.Validate(); err != nil {
		return fmt.Errorf("self user delivery effect: %w", err)
	}
	if effect.Kind != DeliveryEffectAbsolute || effect.TargetUserID != snapshot.User.ID {
		return fmt.Errorf("self user delivery requires an absolute owner-targeted effect")
	}
	if effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 {
		return fmt.Errorf("self user delivery must not exclude a session")
	}
	if effect.RecoveryPolicy != OutboxRecoveryAbsoluteReload {
		return fmt.Errorf("self user delivery requires absolute reload recovery")
	}
	return nil
}

// UserPremiumDeliveryStore clears an expiry batch and commits one absolute
// updateUser effect per changed account in the same aggregate transaction.
type UserPremiumDeliveryStore interface {
	SweepExpiredPremiumWithDelivery(ctx context.Context, now int64, limit int, effects DeliveryEffectsBuilder[[]domain.User]) ([]domain.User, error)
}

type UserAudienceDeliverySnapshot struct {
	User     domain.User
	Audience []int64
}

type UserModerationDeliveryStore interface {
	SetVerifiedWithDelivery(ctx context.Context, userID int64, verified bool, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
	SetScamFakeWithDelivery(ctx context.Context, userID int64, scam, fake bool, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
	SetSupportWithDelivery(ctx context.Context, userID int64, support bool, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
}

// UserAdminDeliveryStore is the trusted-operator aggregate boundary for
// externally visible user facts. Unlike the account self-session boundary,
// these methods freeze the bounded known-viewer audience while the users row is
// locked and commit one absolute updateUser effect per viewer in the same
// transaction. A no-op must return without producing an effect.
type UserAdminDeliveryStore interface {
	AdminUpdateProfileWithDelivery(ctx context.Context, userID int64, firstName, lastName, about string, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
	AdminUpdateUsernameWithDelivery(ctx context.Context, userID int64, username string, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
	AdminUpdatePhoneWithDelivery(ctx context.Context, userID int64, phone string, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
	AdminUpdateColorWithDelivery(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
	AdminUpdateEmojiStatusWithDelivery(ctx context.Context, userID int64, status domain.UserEmojiStatus, effects DeliveryEffectsBuilder[UserAudienceDeliverySnapshot]) (domain.User, error)
}

func ValidateUserAudienceDeliveryEffects(snapshot UserAudienceDeliverySnapshot, effects []DeliveryEffect) error {
	if snapshot.User.ID <= 0 || len(snapshot.Audience) == 0 || len(effects) != len(snapshot.Audience) {
		return fmt.Errorf("user audience delivery requires one effect per frozen viewer")
	}
	want := make(map[int64]struct{}, len(snapshot.Audience))
	for _, viewerID := range snapshot.Audience {
		if viewerID <= 0 {
			return fmt.Errorf("user audience delivery contains invalid viewer")
		}
		want[viewerID] = struct{}{}
	}
	if len(want) != len(snapshot.Audience) {
		return fmt.Errorf("user audience delivery contains duplicate viewers")
	}
	seen := make(map[int64]struct{}, len(effects))
	for i := range effects {
		effect := effects[i]
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("user audience delivery effect %d: %w", i, err)
		}
		if effect.Kind != DeliveryEffectAbsolute {
			return fmt.Errorf("user audience delivery effect %d is not absolute", i)
		}
		if effect.ExcludeAuthKeyID != ([8]byte{}) || effect.ExcludeSessionID != 0 {
			return fmt.Errorf("user audience delivery effect %d must not exclude a session", i)
		}
		if _, ok := want[effect.TargetUserID]; !ok {
			return fmt.Errorf("user audience delivery effect %d has unexpected target", i)
		}
		if _, duplicate := seen[effect.TargetUserID]; duplicate {
			return fmt.Errorf("user audience delivery target %d is duplicated", effect.TargetUserID)
		}
		seen[effect.TargetUserID] = struct{}{}
	}
	return nil
}

func ValidateUserBatchDeliveryEffects(users []domain.User, effects []DeliveryEffect) error {
	if len(users) == 0 || len(effects) != len(users) {
		return fmt.Errorf("user batch delivery requires one effect per changed user")
	}
	want := make(map[int64]struct{}, len(users))
	for _, user := range users {
		if user.ID <= 0 {
			return fmt.Errorf("user batch delivery contains invalid user")
		}
		want[user.ID] = struct{}{}
	}
	seen := make(map[int64]struct{}, len(effects))
	for i := range effects {
		effect := effects[i]
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("user batch delivery effect %d: %w", i, err)
		}
		if effect.Kind != DeliveryEffectAbsolute {
			return fmt.Errorf("user batch delivery effect %d is not absolute", i)
		}
		if _, ok := want[effect.TargetUserID]; !ok {
			return fmt.Errorf("user batch delivery effect %d has unexpected target", i)
		}
		if _, duplicate := seen[effect.TargetUserID]; duplicate {
			return fmt.Errorf("user batch delivery target %d is duplicated", effect.TargetUserID)
		}
		seen[effect.TargetUserID] = struct{}{}
	}
	return nil
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

// UserCache caches viewer-independent base user profile data.
// Contact notes, privacy clipping, photo selection, and presence must not be written to this cache.
type UserCache interface {
	GetByIDs(ctx context.Context, ids []int64) (map[int64]domain.User, error)
	PutMany(ctx context.Context, users []domain.User) error
	Delete(ctx context.Context, ids []int64) error
}
