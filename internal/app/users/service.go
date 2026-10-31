package users

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"telesrv/internal/app/userprojection"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ErrNotAuthorized means the current auth_key is not logged in yet.
var (
	ErrNotAuthorized            = errors.New("not authorized")
	ErrSystemUserImmutable      = errors.New("system user identity is immutable")
	ErrBatchUsersLimit          = errors.New("batch users limit exceeded")
	ErrBatchViewerCells         = errors.New("batch viewer projection cell limit exceeded")
	ErrBatchUserMissing         = errors.New("batch user projection source is incomplete")
	ErrLastSeenBatchUnsupported = errors.New("last seen batch store unsupported")
)

// ProfilePhotoProvider returns current user photos in a batch (used to enrich PhotoID/DCID/Stripped into domain.User).
type ProfilePhotoProvider = userprojection.ProfilePhotoProvider

// Service provides user queries.
type Service struct {
	users           store.UserStore
	cache           store.UserCache
	contacts        store.ContactStore
	photos          ProfilePhotoProvider
	privacy         userprojection.PrivacyEvaluator
	freezes         userprojection.AccountFreezeProvider
	phones          store.CollectiblePhoneStore
	phoneProjection userprojection.CollectiblePhoneProvider
	projector       *userprojection.Projector
}

type usernameAvailabilityStore interface {
	CheckUsername(ctx context.Context, userID int64, username string) (bool, error)
}

type moderationFlagAudienceStore interface {
	ModerationFlagAudience(ctx context.Context, userID int64, limit int) ([]int64, error)
}

// Option adjusts the user service's optional dependencies.
type Option func(*Service)

// WithPhotoProvider injects photo enrichment (by default users carry no photo).
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

// WithCollectiblePhoneStore enables collectible +888 ownership aliases and
// their viewer-specific projection. Independent +888 login identities live in
// users.phone and take lookup precedence over this optional alias registry.
func WithCollectiblePhoneStore(p store.CollectiblePhoneStore) Option {
	return func(s *Service) {
		s.phones = p
		s.phoneProjection = p
	}
}

// WithCollectiblePhoneProvider overrides only response hydration while keeping
// the full store for collectible-number resolve/admin paths.
func WithCollectiblePhoneProvider(p userprojection.CollectiblePhoneProvider) Option {
	return func(s *Service) { s.phoneProjection = p }
}

const (
	minUsernameLen      = 5
	maxUsernameLen      = 32
	maxProfileNameRunes = 64
	// Two bio length tiers, aligned with appConfig about_length_limit_default=70 /
	// about_length_limit_premium=140; the client picks a tier by its self premium flag,
	// and the server tier must be ≥ the tier the client declares.
	maxProfileAboutRunes        = 70
	maxProfileAboutRunesPremium = 140
	maxBatchUsers               = 1000
	// A dense fan-out materializes one complete domain.User per viewer/owner
	// cell in both the result and the batch cache. Bound the retained graph.
	maxBatchViewerProjectionCells = 131072
)

// NewService creates the user service.
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
		userprojection.WithCollectiblePhoneProvider(s.phoneProjection),
	)
	return s
}

// loadSelf loads the current user without photo enrichment (for internal validation paths, to avoid needless photo queries).
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

// Self returns the current logged-in user (with photo). Returns ErrNotAuthorized when not logged in.
func (s *Service) Self(ctx context.Context, userID int64) (domain.User, error) {
	u, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	return s.projectOne(ctx, userID, u)
}

// ByID returns the given user. The caller must be logged in; access_hash validation happens at the RPC boundary.
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

// AdminUser returns viewer-independent account base facts for admin dry-run and audit cases.
func (s *Service) AdminUser(ctx context.Context, userID int64) (domain.User, bool, error) {
	if userID == 0 {
		return domain.User{}, false, nil
	}
	return s.loadBaseUserByID(ctx, userID)
}

// BotStatus returns only the immutable viewer-independent bot fact. Presence
// classification must not pay for contact/privacy/photo projection.
func (s *Service) BotStatus(ctx context.Context, userID int64) (bool, bool, error) {
	if userID == 0 {
		return false, false, nil
	}
	u, found, err := s.loadBaseUserByID(ctx, userID)
	if err != nil || !found {
		return false, found, err
	}
	return u.Bot, true, nil
}

// PrivacyBaseUsers returns viewer-independent bot/premium facts through the
// shared base-user read model. Privacy uses this as a batched cold loader behind
// its bounded process cache; no viewer projection is performed, avoiding a
// privacy -> users -> privacy recursion.
func (s *Service) PrivacyBaseUsers(ctx context.Context, userIDs []int64) ([]domain.User, error) {
	return s.loadBaseUsersByIDs(ctx, userIDs)
}

// BaseUsersByIDs returns viewer-independent identities for bounded write/read
// validation paths without constructing full viewer projections.
func (s *Service) BaseUsersByIDs(ctx context.Context, userIDs []int64) ([]domain.User, error) {
	return s.loadBaseUsersByIDs(ctx, userIDs)
}

// ByIDs returns the given users in a batch. The caller must be logged in; missing users do not appear in the result.
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
		if len(ids) >= maxBatchUsers {
			return nil, fmt.Errorf("%w: more than %d unique owners", ErrBatchUsersLimit, maxBatchUsers)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	users, err := s.loadBaseUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return s.projectUsers(ctx, currentUserID, users)
}

// ByIDsForViewers projects the same user set across multiple viewers in a batch (fan-out
// templating): base users are loaded once, and privacy/rename/photo projection is collapsed
// into batch queries via userprojection.ForViewers. It returns map[viewerID][]User, each
// slice byte-equivalent to ByIDs(viewer, ids) and including the viewer-specific personal
// photo overlay. Used by channel fan-out to prewarm each viewer's projection, folding the
// per-recipient ByIDs(=ForViewer) into a single cross-viewer projection. It does not do
// ByIDs' single-caller authorization (the viewers are the fan-out recipient set, not the
// RPC caller).
func (s *Service) ByIDsForViewers(ctx context.Context, viewerUserIDs []int64, userIDs []int64) (map[int64][]domain.User, error) {
	if len(viewerUserIDs) == 0 || len(userIDs) == 0 {
		return map[int64][]domain.User{}, nil
	}
	ids := uniqueUserIDs(userIDs, 0)
	if len(ids) > maxBatchUsers {
		return nil, fmt.Errorf("%w: got %d unique owners, maximum %d", ErrBatchUsersLimit, len(ids), maxBatchUsers)
	}
	viewers := uniqueUserIDs(viewerUserIDs, 0)
	if !batchViewerProjectionCellsAllowed(len(viewers), len(ids)) {
		return nil, fmt.Errorf("%w: got %d viewers x %d owners, maximum %d cells", ErrBatchViewerCells, len(viewers), len(ids), maxBatchViewerProjectionCells)
	}
	base, err := s.loadBaseUsersByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	base, err = requireBatchBaseUsers(ids, base)
	if err != nil {
		return nil, err
	}
	// When projector is nil, ForViewers returns a raw base copy for each viewer (consistent with projectUsers' nil branch).
	return s.projector.ForViewers(ctx, viewers, base)
}

func batchViewerProjectionCellsAllowed(viewers, owners int) bool {
	if viewers <= 0 || owners <= 0 {
		return true
	}
	// Division avoids overflow from viewers*owners on hostile inputs.
	return viewers <= maxBatchViewerProjectionCells/owners
}

// requireBatchBaseUsers turns the fan-out projection API into a complete
// envelope contract. Deleted users remain durable tombstones and therefore
// still appear in base; a truly missing referenced user must fail closed rather
// than produce a message whose sender cannot be resolved. System users are
// protocol-local constants and do not require a backing users row.
func requireBatchBaseUsers(ids []int64, base []domain.User) ([]domain.User, error) {
	byID := make(map[int64]domain.User, len(base))
	for _, user := range base {
		if user.ID != 0 {
			byID[user.ID] = user
		}
	}
	out := make([]domain.User, 0, len(ids))
	for _, id := range ids {
		if user, ok := byID[id]; ok {
			out = append(out, user)
			continue
		}
		if system, ok := domain.SystemUserByID(id); ok {
			out = append(out, system)
			continue
		}
		return nil, fmt.Errorf("%w: user_id=%d", ErrBatchUserMissing, id)
	}
	return out, nil
}

// CheckUsername validates whether the current user can take the username.
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

// UpdateUsername changes the current user's main username. An empty string deletes the username.
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

// UpdateProfile changes the current user's base profile. Unset fields keep their original values.
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

// SetPhone force-sets the authoritative phone for the trusted admin path. It
// remains a non-PTS profile mutation because updateUserPhone and updateUser
// carry no pts/pts_count in every admitted exact layer.
func (s *Service) SetPhone(ctx context.Context, userID int64, phone string) (domain.User, error) {
	self, err := s.loadSelf(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if self.Bot || domain.IsSystemUserID(self.ID) {
		return domain.User{}, domain.ErrPhoneChangeForbidden
	}
	phone = domain.NormalizePhone(strings.TrimSpace(phone))
	if !domain.ValidPhone(phone) {
		return domain.User{}, domain.ErrPhoneNumberInvalid
	}
	if phone == self.Phone {
		return s.projectOne(ctx, self.ID, self)
	}
	if existing, found, err := s.users.ByPhone(ctx, phone); err != nil {
		return domain.User{}, err
	} else if found && existing.ID != self.ID {
		return domain.User{}, domain.ErrPhoneNumberOccupied
	}
	u, err := s.users.UpdatePhone(ctx, self.ID, phone)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return s.projectOne(ctx, self.ID, u)
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

// UpdateLastSeenBatch is the production lifecycle-presence write boundary. It
// requires a real batch-capable store: silently looping over UpdateLastSeen
// would recreate the exact per-account transaction fan-out this API exists to
// remove. The cache delete is part of batch completion; callers may retry the
// whole idempotent batch when Redis is temporarily unavailable.
func (s *Service) UpdateLastSeenBatch(ctx context.Context, updates []store.UserLastSeenUpdate) error {
	batch, ok := s.users.(store.UserLastSeenBatchStore)
	if !ok {
		return ErrLastSeenBatchUnsupported
	}
	latest := make(map[int64]int, len(updates))
	for _, update := range updates {
		if update.UserID == 0 || update.LastSeenAt <= 0 {
			continue
		}
		if current := latest[update.UserID]; update.LastSeenAt > current {
			latest[update.UserID] = update.LastSeenAt
		}
	}
	if len(latest) == 0 {
		return nil
	}
	merged := make([]store.UserLastSeenUpdate, 0, len(latest))
	userIDs := make([]int64, 0, len(latest))
	for userID, lastSeenAt := range latest {
		merged = append(merged, store.UserLastSeenUpdate{UserID: userID, LastSeenAt: lastSeenAt})
		userIDs = append(userIDs, userID)
	}
	if err := batch.UpdateLastSeenBatch(ctx, merged); err != nil {
		return err
	}
	if s.cache != nil {
		if err := s.cache.Delete(ctx, userIDs); err != nil {
			return fmt.Errorf("invalidate last seen batch user cache: %w", err)
		}
	}
	return nil
}

// PremiumActive reports whether the user is currently an active Premium member. It goes
// through the base-user cache path without viewer projection, for cheap calls in two-tier
// limit decisions (pin cap, reaction cap, bio length, etc.).
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

// GrantPremium grants/renews Premium: when not expired it adds months to the existing
// expiry time; when expired or first-time it starts from the current time (matching
// official renewal semantics). months<=0 clears Premium. A bot can never become Premium.
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

// SetVerified sets/clears a user's verified flag. Verification is an account base fact and is consumed uniformly by all user projections.
func (s *Service) SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error) {
	if userID == 0 {
		return domain.User{}, ErrNotAuthorized
	}
	if domain.IsSystemUserID(userID) && !verified {
		return domain.User{}, ErrSystemUserImmutable
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

// SetScamFake sets/clears a user's scam and fake flags (bots reuse the same path).
// scam/fake are account base facts consumed uniformly by all user projections; the base
// cache is refreshed after the write so the projection is immediately visible.
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

// SetSupport sets/clears a user's support flag (official support accounts). Refreshes the base cache after the write.
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

// SweepExpiredPremium clears expired Premium members (the store NULLs out expired rows)
// and invalidates the user cache, returning the cleaned-up users for the RPC layer to push
// updateUser to the owner's online sessions. Premium delivery correctness is guaranteed by
// the read path deriving it on the fly; this only does the cleanup and notification.
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

// UpdateEmojiStatus updates the current user's emoji status (Premium-only; a zero value
// clears it). Clearing does not require Premium (after an expiry downgrade the client can
// still explicitly clear leftover state).
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

// UpdateBirthday sets/clears the user's birthday (account.updateBirthday). A zero-value Birthday means clear.
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
		birthday = domain.Birthday{} // normalize to clear
	}
	u, err := s.users.UpdateBirthday(ctx, self.ID, birthday)
	if err != nil {
		return domain.User{}, err
	}
	s.refreshCachedUsers(ctx, u)
	return u, nil
}

// UpdatePersonalChannel sets/clears the profile personal channel
// (account.updatePersonalChannel); channelID=0 means clear. The channel's existence and
// "the caller is a member" are validated by the RPC layer before the call.
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

// ResolveUsername resolves a username to a user; the caller must be logged in.
func (s *Service) ResolveUsername(ctx context.Context, currentUserID int64, username string) (domain.User, bool, error) {
	if _, err := s.loadSelf(ctx, currentUserID); err != nil {
		return domain.User{}, false, err
	}
	username = normalizeUsername(username)
	_, reservedSystemUsername := domain.SystemUserByUsername(username)
	// Resolution covers both the editable username slot (5..32) and
	// Fragment-style collectible usernames (4..32). Keep the stricter
	// validUsername check on create/update paths; only lookup accepts the
	// collectible lower bound.
	if !reservedSystemUsername && !domain.ValidCollectibleUsername(username) {
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
	// A frozen account is presented to observers as a deleted tombstone: all of its usernames
	// (regular slots + collectible NFTs) no longer resolve to that user, and
	// contacts.resolveUsername reports USERNAME_NOT_OCCUPIED (the client shows "not found").
	// The occupancy check goes through the peer_usernames registry ownership relation, so
	// during the freeze it still reports occupied (USERNAME_OCCUPIED) while merely hiding the
	// projection; after unfreezing the tombstone disappears and resolution and occupancy
	// semantics recover immediately, with no data write-back.
	if u.Deleted {
		return domain.User{}, false, nil
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
	canonicalPhone := domain.NormalizePhone(phone)
	collectiblePhone := domain.NormalizeCollectiblePhone(phone)
	validCollectible := domain.ValidCollectiblePhone(collectiblePhone)
	if canonicalPhone == "" && !validCollectible {
		return domain.User{}, false, domain.ErrPhoneNotOccupied
	}
	var (
		u     domain.User
		found bool
		err   error
	)
	if canonicalPhone != "" {
		u, found, err = s.users.ByPhone(ctx, canonicalPhone)
	}
	collectibleExclusive := false
	if err != nil {
		return u, false, err
	}
	if !found && s.phones != nil && validCollectible {
		asset, assetErr := s.phones.CollectiblePhone(ctx, collectiblePhone)
		if assetErr == nil && asset.Owned() {
			u, found, err = s.loadBaseUserByID(ctx, asset.OwnerUserID)
			collectibleExclusive = asset.AlwaysVisible()
		} else if assetErr != nil && !errors.Is(assetErr, domain.ErrCollectiblePhoneNotFound) {
			return domain.User{}, false, assetErr
		}
	}
	if err != nil || !found {
		return u, found, err
	}
	s.putCachedUsers(ctx, u)
	if s.privacy != nil && u.ID != currentUserID && !collectibleExclusive {
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
	ids := uniqueUserIDs(userIDs, maxBatchUsers+1)
	if len(ids) > maxBatchUsers {
		return nil, fmt.Errorf("%w: more than %d unique owners", ErrBatchUsersLimit, maxBatchUsers)
	}
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

// InvalidateUsers drops viewer-independent user snapshots after an aggregate
// transaction updates users without passing through this service.
func (s *Service) InvalidateUsers(ctx context.Context, userIDs ...int64) {
	if s == nil {
		return
	}
	s.dropCachedUsers(ctx, userIDs...)
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
	return domain.NormalizeUsername(username)
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
