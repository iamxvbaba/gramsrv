// Package bots implements bot account business logic: the BotFather dialog state machine, bot creation, token
// management, and botInfo lookups. Bot login (auth.importBotAuthorization) validates the token directly in
// app/auth via store.BotStore and does not depend on this package.
package bots

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	telegramloginapp "telesrv/internal/app/telegramlogin"
	"telesrv/internal/domain"
	"telesrv/internal/links"
	"telesrv/internal/store"
)

// blockChecker reports whether userID has blocked blockedUserID (subset of store.ContactStore).
type blockChecker interface {
	IsBlocked(ctx context.Context, userID, blockedUserID int64) (bool, error)
}

type publicChannelUsernameResolver interface {
	ResolvePublicChannelUsername(ctx context.Context, viewerUserID int64, username string) (domain.Channel, bool, error)
}

type stickerSetCreator interface {
	CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error)
	ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (domain.StickerSet, []domain.Document, bool, error)
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
	AddStickerToSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error)
	RemoveStickerFromSet(ctx context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error)
}

type userStickerSetInstaller interface {
	InstallUserStickerSet(ctx context.Context, userID int64, setID int64, kind domain.StickerSetKind, archived bool, installedDate int) error
}

type aiChatGenerator interface {
	GenerateTextStream(ctx context.Context, req domain.AITextGenerationRequest, emit func(domain.AIComposeText) error) (domain.AIComposeText, error)
}

// premiumStorefront is the deliberately narrow surface used by the built-in
// @premiumbot. Price authority and entitlement state stay in app/premium.
type premiumStorefront interface {
	BotUserID() int64
	BotUsername() string
	Plans(ctx context.Context) ([]domain.PremiumPlan, error)
	Plan(ctx context.Context, months int) (domain.PremiumPlan, error)
	Balance(ctx context.Context, userID int64) (domain.StarsBalance, error)
	ActiveEntitlements(ctx context.Context, userID int64, now int) ([]domain.PremiumEntitlement, error)
	PurchaseHistory(ctx context.Context, userID int64, limit int) ([]domain.PremiumEntitlement, error)
}

// donationsSource is the built-in @premiumbot's crypto-donation surface
// (app/donations.Service satisfies it as-is): assign/fetch a user's
// permanent deposit address, list which chains it's actually live on, and
// show their own deposit history. Wired post-construction via
// SetDonationsSource for the same reason as premiumSource/starsClaimSource
// above -- donations.Service is also built after bots.Service in
// cmd/telesrv/main.go.
type donationsSource interface {
	Ready() bool
	AddressForUser(ctx context.Context, userID int64) (string, error)
	EnabledChains(ctx context.Context) ([]domain.DonationChain, error)
	UserDeposits(ctx context.Context, userID int64, limit int) ([]domain.DonationDeposit, error)
}

// verificationApplications is the applicant-side surface of official platform
// verification used by the built-in @verifybot (app/verification.Service
// satisfies it as-is).
//
// It is declared as a narrow port rather than taken as a concrete service for the
// usual reason plus one specific to this feature: every verification rule --
// ownership, public username, restrictions, already-verified, cooldown, rate
// limit, the status machine -- belongs to that service, and the bot must not be
// able to reach past it. Nothing here can write a peer's verified flag.
type verificationApplications interface {
	EligibleTargets(ctx context.Context, applicantUserID int64) ([]domain.VerificationTarget, error)
	StartDraft(ctx context.Context, req domain.SubmitVerificationApplicationRequest) (domain.VerificationApplication, bool, error)
	SaveDraft(ctx context.Context, applicantUserID, applicationID, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error)
	Submit(ctx context.Context, applicantUserID, applicationID, version int64) (domain.VerificationApplication, error)
	Cancel(ctx context.Context, applicantUserID, applicationID, version int64, reason string) (domain.VerificationApplication, error)
	Draft(ctx context.Context, applicantUserID int64) (domain.VerificationApplication, error)
	ApplicantApplications(ctx context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error)
	Application(ctx context.Context, applicationID int64) (domain.VerificationApplication, error)
}

// The third-party verification ports live in verifierbot.go
// (customVerifications, verifierBotTargets): they are the built-in @verifierbot's
// only way to reach the feature, and are kept next to the dialog that uses them.

// RouterHooks are rpc-layer callbacks (injected lazily after the router is created via SetRouterHooks, breaking
// the router<->bots construction cycle; these capabilities all depend on the TL/connection layer boundary and
// cannot be implemented in the app layer):
//   - RevokeBotSessions: after a token revoke, revoke all of the bot's logged-in sessions (delete
//     authorization + force disconnect).
//   - PushBotCommandsChanged: after a command change, push updateBotCommands to the relevant online users
//     (an ephemeral update with no pts; offline users are covered by the bot_info_version bump).
//   - PushStickerSetsChanged: after @Stickers publishes, push updateStickerSets to the creator's currently
//     online sessions; offline clients are covered by the persisted install state + the next getAllStickers.
type RouterHooks interface {
	RevokeBotSessions(ctx context.Context, botUserID int64) error
	PushBotCommandsChanged(ctx context.Context, botUserID int64, commands []domain.BotCommand)
	PushStickerSetsChanged(ctx context.Context, userID int64, kind domain.StickerSetKind)
}

// TextDraftPusher pushes the transient text draft for @ChatBot's AI streaming reply, which the rpc layer
// converts into UpdateUserTyping/sendMessageTextDraftAction. It is independent of the normal bot hooks, to
// keep BotFather and @Stickers tests/dependencies from being polluted by the AI chat capability.
type TextDraftPusher interface {
	PushBotTextDraft(ctx context.Context, botUserID, userID, randomID int64, text string)
}

// replyLockStripes is the number of reply serialization stripes: BotFather replies for the same user go to the
// same stripe and run serially (atomic state-machine RMW + ordered replies), while different users run
// concurrently; the size is fixed and does not grow with the user count.
const replyLockStripes = 256

// Service provides bot account business logic.
type Service struct {
	users      store.UserStore
	bots       store.BotStore
	messages   store.MessageStore
	blocker    blockChecker
	channels   publicChannelUsernameResolver
	stickers   stickerSetCreator
	installer  userStickerSetInstaller
	aiChat     aiChatGenerator
	premium    premiumStorefront
	gifCatalog interface {
		ListGifCatalog(context.Context, bool) ([]domain.GifCatalogEntry, error)
		GetDocuments(context.Context, []int64) ([]domain.Document, error)
	}
	verification          verificationApplications
	customVerification    customVerifications
	verifierTargets       verifierBotTargets
	telegramLogin         *telegramloginapp.Service
	hooks                 RouterHooks
	textDrafts            TextDraftPusher
	userCache             store.UserCache
	cache                 *botProfileCache
	log                   *zap.Logger
	now                   func() time.Time
	chatBotStreamThrottle time.Duration
	publicBaseURL         string
	// botAPIURL is the public Bot API base URL shown with every token.
	botAPIURL           string
	gramsrvOperators    map[int64]bool
	gramsrvFreezes      gramsrvAccountFreezeManager
	donations           donationsSource
	gramsrvAdmin        gramsrvFreezeAdmin
	gramsrvAdminChatIDs map[int64]struct{}
	// dialogLimiter bounds how often one applicant can drive a service-bot dialog.
	// The verification service already rate-limits application creation; this is the
	// separate bound on dialog traffic itself, so a script cannot spin the state
	// machine (and its writes) even without ever submitting anything.
	dialogLimiter    store.RateLimiter
	dialogRateLimit  int
	dialogRateWindow time.Duration
	// replySeq is the fallback monotonic sequence for reply randomIDs when crypto/rand fails.
	replySeq   atomic.Int64
	replyLocks [replyLockStripes]sync.Mutex
}

// Option adjusts the bots service's optional dependencies.
type Option func(*Service)

// WithBotAPIURL makes BotFather show the public Bot API base URL next to a
// token, so bot owners know where to send requests on this server.
func WithBotAPIURL(url string) Option {
	return func(s *Service) { s.botAPIURL = strings.TrimRight(strings.TrimSpace(url), "/") }
}

// WithLogger injects the logger (defaults to zap.NewNop).
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// WithNow injects the clock (for tests).
func WithNow(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithBlockChecker injects the block-relationship lookup: BotFather sets RecipientBlocked based on it before
// replying, so that a user who has blocked BotFather no longer receives deliveries to their inbox (matching
// the rpc send-path semantics).
func WithBlockChecker(c blockChecker) Option {
	return func(s *Service) {
		if c != nil {
			s.blocker = c
		}
	}
}

// WithPublicChannelUsernameResolver injects a public channel username lookup, used for bot username
// pre-checks to avoid a bot and a public channel producing the same visible entry.
func WithPublicChannelUsernameResolver(c publicChannelUsernameResolver) Option {
	return func(s *Service) {
		if c != nil {
			s.channels = c
		}
	}
}

// WithUserCache injects the users base-profile cache: after bot metadata writes (first_name/about/
// bot_info_version bump) the bot's cache entry must be invalidated; otherwise getUsers returns the old
// first_name and old bot_info_version within the TTL -- the version bump is masked by the cache, the client
// cannot perceive the change and will not re-fetch getFullUser.
func WithUserCache(c store.UserCache) Option {
	return func(s *Service) {
		if c != nil {
			s.userCache = c
		}
	}
}

// WithStickerSetCreator injects sticker set creation/lookup capability for the built-in @Stickers bot.
func WithStickerSetCreator(c stickerSetCreator) Option {
	return func(s *Service) {
		if c != nil {
			s.stickers = c
		}
	}
}

// WithUserStickerSets injects per-user sticker set install-state write capability.
func WithUserStickerSets(c userStickerSetInstaller) Option {
	return func(s *Service) {
		if c != nil {
			s.installer = c
		}
	}
}

// SetDonationsSource injects the crypto-donation address/history access
// used by the built-in @premiumbot's /deposit command. See starsClaimSource's
// doc comment for why this is a post-hoc setter. Without it, /deposit
// answers that the feature is unavailable rather than crashing.
func (s *Service) SetDonationsSource(src donationsSource) {
	if s == nil || src == nil {
		return
	}
	s.donations = src
}

// WithAIChatGenerator injects the AI text generator used by the built-in @ChatBot.
func WithAIChatGenerator(g aiChatGenerator) Option {
	return func(s *Service) {
		if g != nil {
			s.aiChat = g
		}
	}
}

// WithPremium injects the Premium catalog/status service used by @premiumbot.
func WithPremium(p premiumStorefront) Option {
	return func(s *Service) {
		if p != nil {
			s.premium = p
		}
	}
}

// WithGifCatalog injects the curated catalog plus document resolver used by
// the inline-only @gif responder.
func WithGifCatalog(c interface {
	ListGifCatalog(context.Context, bool) ([]domain.GifCatalogEntry, error)
	GetDocuments(context.Context, []int64) ([]domain.Document, error)
}) Option {
	return func(s *Service) { s.gifCatalog = c }
}

// WithVerification injects the official verification service used by the
// built-in @verifybot. Without it the bot still answers, but every command
// reports that verification is unavailable rather than half-running the dialog.
func WithVerification(v verificationApplications) Option {
	return func(s *Service) {
		if v != nil {
			s.verification = v
		}
	}
}

// WithCustomVerification injects the third-party verification service used by the
// built-in @verifierbot. Without it the bot still answers, but every command
// reports that third-party verification is unavailable rather than half-running the
// dialog.
func WithCustomVerification(v customVerifications) Option {
	return func(s *Service) {
		if v != nil {
			s.customVerification = v
		}
	}
}

// WithVerifierTargets injects the directory of an applicant's own peers used by
// @verifierbot's subject picker. It is optional: with nothing injected the bot
// falls back to the official verification service's EligibleTargets, which
// enumerates exactly the same peers (only its eligibility verdicts, which answer a
// different question, are ignored).
func WithVerifierTargets(t verifierBotTargets) Option {
	return func(s *Service) {
		if t != nil {
			s.verifierTargets = t
		}
	}
}

// WithDialogRateLimiter bounds service-bot dialog traffic per user. A zero limit
// or a nil limiter disables the bound, which is what a deployment without Redis
// gets.
func WithDialogRateLimiter(limiter store.RateLimiter, limit int, window time.Duration) Option {
	return func(s *Service) {
		if limiter == nil || limit <= 0 || window <= 0 {
			return
		}
		s.dialogLimiter = limiter
		s.dialogRateLimit = limit
		s.dialogRateWindow = window
	}
}

// WithTelegramLogin injects the OIDC application service used by BotFather.
// BotFather never writes the login tables directly.
func WithTelegramLogin(login *telegramloginapp.Service) Option {
	return func(s *Service) {
		if login != nil {
			s.telegramLogin = login
		}
	}
}

// WithAIChatStreamThrottle adjusts the minimum interval between @ChatBot streaming draft pushes (for tests).
func WithAIChatStreamThrottle(d time.Duration) Option {
	return func(s *Service) {
		if d >= 0 {
			s.chatBotStreamThrottle = d
		}
	}
}

func WithPublicBaseURL(baseURL string) Option {
	return func(s *Service) {
		s.publicBaseURL = links.NormalizeBaseURL(baseURL)
	}
}

// invalidateUserCache clears the cache after the bot's users row changes (including a version bump).
// A failed invalidation only logs: the cache self-heals after at most the TTL, and the write path is not blocked.
func (s *Service) invalidateUserCache(ctx context.Context, botUserID int64) {
	if s.userCache == nil {
		return
	}
	if err := s.userCache.Delete(ctx, []int64{botUserID}); err != nil {
		s.log.Warn("invalidate bot user cache", zap.Int64("bot_user_id", botUserID), zap.Error(err))
	}
}

func (s *Service) invalidateBotProfileCache(botUserID int64) {
	if s.cache != nil {
		s.cache.delete(botUserID)
	}
}

func (s *Service) invalidateBotReadCaches(ctx context.Context, botUserID int64) {
	s.invalidateBotProfileCache(botUserID)
	s.invalidateUserCache(ctx, botUserID)
}

// InvalidateBotProfileReadModel lets ReadModelChangeListener invalidate this process's bot profile cache
// across instances on a user_base event (a bot write bumps bot_info_version).
func (s *Service) InvalidateBotProfileReadModel(userID int64) {
	if s == nil {
		return
	}
	s.invalidateBotProfileCache(userID)
}

// FlushBotProfileReadModel lets the listener flush the entire table on reconnect, covering user_base notifications lost during a disconnect window.
func (s *Service) FlushBotProfileReadModel() {
	if s == nil || s.cache == nil {
		return
	}
	s.cache.flush()
}

// SetRouterHooks injects rpc-layer callbacks (wired after the router is created, the same lazy injection
// style as P1's SetLifecycleObserver).
func (s *Service) SetRouterHooks(h RouterHooks) {
	if s != nil {
		s.hooks = h
	}
}

// SetTextDraftPusher injects the @ChatBot streaming draft push boundary.
func (s *Service) SetTextDraftPusher(p TextDraftPusher) {
	if s != nil {
		s.textDrafts = p
	}
}

func (s *Service) SetAIChatGenerator(g aiChatGenerator) {
	if s != nil {
		s.aiChat = g
	}
}

// SetPremium injects the Premium storefront after construction when startup
// ordering requires the message service to be built first.
func (s *Service) SetPremium(p premiumStorefront) {
	if s != nil && p != nil {
		s.premium = p
	}
}

// SetVerification injects the official verification service after construction.
// The bots service is built before the peer directories that service depends on,
// so in the shipped process this is the wiring order that actually exists (same
// deferred-injection pattern as SetRouterHooks).
func (s *Service) SetVerification(v verificationApplications) {
	if s != nil && v != nil {
		s.verification = v
	}
}

// SetCustomVerification injects the third-party verification service after
// construction. The bots service is built before the stores and directories that
// service depends on, so in the shipped process this is the wiring order that
// actually exists (same deferred-injection pattern as SetVerification).
func (s *Service) SetCustomVerification(v customVerifications) {
	if s != nil && v != nil {
		s.customVerification = v
	}
}

// SetVerifierTargets injects @verifierbot's subject directory after construction.
func (s *Service) SetVerifierTargets(t verifierBotTargets) {
	if s != nil && t != nil {
		s.verifierTargets = t
	}
}

// NewService creates the bots service.
func NewService(users store.UserStore, bots store.BotStore, messages store.MessageStore, opts ...Option) *Service {
	s := &Service{
		users:                 users,
		bots:                  bots,
		messages:              messages,
		cache:                 newBotProfileCache(botProfileCacheMaxEntries, botProfileCacheTTL),
		log:                   zap.NewNop(),
		now:                   time.Now,
		chatBotStreamThrottle: defaultChatBotStreamThrottle,
		publicBaseURL:         links.DefaultPublicBaseURL,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) botProfile(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	if s == nil || s.bots == nil || botUserID == 0 {
		return domain.BotProfile{}, false, nil
	}
	if s.cache != nil {
		return s.cache.getOrLoad(ctx, botUserID, func() (domain.BotProfile, bool, error) {
			return s.bots.GetBot(ctx, botUserID)
		})
	}
	return s.bots.GetBot(ctx, botUserID)
}

func (s *Service) botProfiles(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	if s == nil || s.bots == nil || len(botUserIDs) == 0 {
		return nil, nil
	}
	ids := uniqueBotUserIDs(botUserIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	if s.cache == nil {
		return s.loadBotProfiles(ctx, ids)
	}
	return s.cache.getMany(ctx, ids, s.loadBotProfiles)
}

func (s *Service) loadBotProfiles(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	if batch, ok := s.bots.(botBatchStore); ok {
		return batch.GetBots(ctx, botUserIDs)
	}
	out := make(map[int64]domain.BotProfile)
	for _, id := range uniqueBotUserIDs(botUserIDs) {
		profile, found, err := s.bots.GetBot(ctx, id)
		if err != nil {
			return nil, err
		}
		if found {
			out[id] = profile
		}
	}
	return out, nil
}

// BotInfo returns the bot's metadata (used to hydrate userFull.bot_info).
func (s *Service) BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error) {
	return s.botProfile(ctx, botUserID)
}

type botBatchStore interface {
	GetBots(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error)
}

// BotInfos returns bot metadata in a batch, so high-frequency enrichment paths like channel full info / participants avoid a per-bot point lookup.
func (s *Service) BotInfos(ctx context.Context, botUserIDs []int64) (map[int64]domain.BotProfile, error) {
	return s.botProfiles(ctx, botUserIDs)
}

func uniqueBotUserIDs(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
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

// CheckUsername validates bot username syntax and global visible-entry occupancy (users + public channels).
func (s *Service) CheckUsername(ctx context.Context, ownerUserID int64, username string) (bool, error) {
	if s == nil || s.users == nil || ownerUserID == 0 {
		return false, domain.ErrBotUsernameInvalid
	}
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	if !domain.ValidBotUsername(username) {
		return false, domain.ErrBotUsernameInvalid
	}
	if _, found, err := s.users.ByUsername(ctx, username); err != nil {
		return false, err
	} else if found {
		return false, nil
	}
	if s.channels != nil {
		if _, found, err := s.channels.ResolvePublicChannelUsername(ctx, ownerUserID, username); err != nil {
			return false, err
		} else if found {
			return false, nil
		}
	}
	return true, nil
}

// CreateBot creates a new bot account: a users row (is_bot, bot_info_version=1, no phone) + a bots row
// (owner, token). It returns the new account and the full token (one of the only paths that ever returns plaintext).
func (s *Service) CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error) {
	if s == nil || s.users == nil || s.bots == nil || ownerUserID == 0 {
		return domain.User{}, "", domain.ErrBotNameInvalid
	}
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > domain.MaxBotNameLength {
		return domain.User{}, "", domain.ErrBotNameInvalid
	}
	username = strings.TrimSpace(strings.TrimPrefix(username, "@"))
	if !domain.ValidBotUsername(username) {
		return domain.User{}, "", domain.ErrBotUsernameInvalid
	}
	ok, err := s.CheckUsername(ctx, ownerUserID, username)
	if err != nil {
		return domain.User{}, "", err
	}
	if !ok {
		return domain.User{}, "", domain.ErrUsernameOccupied
	}
	count, err := s.bots.CountBotsByOwner(ctx, ownerUserID)
	if err != nil {
		return domain.User{}, "", err
	}
	if count >= domain.MaxBotsPerOwner {
		return domain.User{}, "", domain.ErrBotsTooMany
	}
	accessHash, err := randomInt64()
	if err != nil {
		return domain.User{}, "", err
	}
	secret, err := randomTokenSecret()
	if err != nil {
		return domain.User{}, "", err
	}
	u, profile, err := s.bots.CreateBotAccount(ctx, domain.User{
		AccessHash:     accessHash,
		FirstName:      name,
		Username:       username,
		Bot:            true,
		BotInfoVersion: 1,
	}, domain.BotProfile{
		OwnerUserID: ownerUserID,
		TokenSecret: secret,
	})
	if err != nil {
		return domain.User{}, "", err
	}
	if s.cache != nil {
		s.cache.put(u.ID, profile, true)
	}
	return u, domain.FormatBotToken(u.ID, secret), nil
}

// ListOwnedBots returns the list of bot users managed by the current owner (excluding the BotFather seed).
func (s *Service) ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error) {
	owned, err := s.ownedBots(ctx, ownerUserID)
	if err != nil {
		return nil, err
	}
	out := make([]domain.User, 0, len(owned))
	for _, item := range owned {
		out = append(out, item.user)
	}
	return out, nil
}

// botAccountDeleter is the optional store capability used to permanently delete
// a user-created bot. Only the Postgres store implements it, so the memory store
// and other BotStore mocks are unaffected.
type botAccountDeleter interface {
	DeleteBotAccount(ctx context.Context, botUserID int64) (domain.User, error)
}

// DeleteBot permanently removes a user-created bot. System service bots are
// rejected. Live sessions are dropped and the bot's caches are invalidated so
// the deletion is visible immediately. Returns the tombstoned user.
func (s *Service) DeleteBot(ctx context.Context, botUserID int64) (domain.User, error) {
	if s == nil || s.bots == nil || botUserID == 0 {
		return domain.User{}, domain.ErrBotNotFound
	}
	if domain.IsSystemUserID(botUserID) {
		return domain.User{}, domain.ErrBotNotFound
	}
	deleter, ok := s.bots.(botAccountDeleter)
	if !ok {
		return domain.User{}, domain.ErrBotDeletionUnsupported
	}
	// Session revocation is part of the deletion invariant: a deleted bot must
	// never retain an authenticated connection. Fail closed before tombstoning
	// when the hook is unavailable or revocation fails.
	if s.hooks == nil {
		return domain.User{}, domain.ErrBotSessionsNotRevoked
	}
	if err := s.hooks.RevokeBotSessions(ctx, botUserID); err != nil {
		s.log.Warn("revoke bot sessions before delete", zap.Int64("bot_user_id", botUserID), zap.Error(err))
		return domain.User{}, domain.ErrBotSessionsNotRevoked
	}
	u, err := deleter.DeleteBotAccount(ctx, botUserID)
	if err != nil {
		return domain.User{}, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return u, nil
}

// ExportBotToken returns the bot token; when revoke=true it first rotates the secret and revokes logged-in sessions.
func (s *Service) ExportBotToken(ctx context.Context, ownerUserID, botUserID int64, revoke bool) (string, error) {
	if revoke {
		return s.RevokeBotToken(ctx, ownerUserID, botUserID)
	}
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", err
	}
	if !found || profile.OwnerUserID != ownerUserID || botUserID == domain.BotFatherUserID || profile.TokenSecret == "" {
		return "", domain.ErrBotNotFound
	}
	return domain.FormatBotToken(botUserID, profile.TokenSecret), nil
}

// AdminExportBotToken returns a non-system bot's current token without
// rotating it or applying the owner check used by the Telegram RPC. The admin
// layer is responsible for RBAC and for keeping the secret out of audit data.
func (s *Service) AdminExportBotToken(ctx context.Context, botUserID int64) (string, error) {
	if s == nil || s.bots == nil || botUserID <= 0 || domain.IsSystemUserID(botUserID) || botUserID == domain.BotFatherUserID {
		return "", domain.ErrBotNotFound
	}
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", err
	}
	if !found || profile.TokenSecret == "" {
		return "", domain.ErrBotNotFound
	}
	return domain.FormatBotToken(botUserID, profile.TokenSecret), nil
}

// RevokeBotToken generates a new token random segment and persists it; the old token can no longer log in
// immediately, and all sessions logged in with the old token are kicked (via the injected SessionRevoker).
func (s *Service) RevokeBotToken(ctx context.Context, ownerUserID, botUserID int64) (string, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", err
	}
	if !found || profile.OwnerUserID != ownerUserID || botUserID == domain.BotFatherUserID {
		return "", domain.ErrBotNotFound
	}
	secret, err := randomTokenSecret()
	if err != nil {
		return "", err
	}
	if err := s.bots.UpdateBotTokenSecret(ctx, botUserID, secret); err != nil {
		return "", err
	}
	s.invalidateBotProfileCache(botUserID)
	token := domain.FormatBotToken(botUserID, secret)
	// Revoke logged-in sessions: the old token can no longer log in, but established connections still hold an
	// auth_key and must be actively invalidated (delete authorization + disconnect), otherwise the old holder
	// continues to act as the bot. The secret has already been rotated and cannot be rolled back, so on failure
	// we still return the new token but surface ErrBotSessionsNotRevoked so the caller can honestly tell the
	// user that a retry is needed to ensure the old session is terminated, never falsely claiming the bleeding
	// has stopped.
	if s.hooks != nil {
		if err := s.hooks.RevokeBotSessions(ctx, botUserID); err != nil {
			s.log.Warn("revoke bot sessions", zap.Int64("bot_user_id", botUserID), zap.Error(err))
			return token, domain.ErrBotSessionsNotRevoked
		}
	}
	return token, nil
}

// SetBotCommands overwrites the bot's default scope commands (the shared convergence point for
// bots.setBotCommands / BotFather /setcommands). It validates command names/descriptions/count; after a
// successful write (including the version bump) it pushes updateBotCommands to the relevant online users.
// Returns the bumped bot_info_version.
func (s *Service) SetBotCommands(ctx context.Context, botUserID int64, commands []domain.BotCommand) (int, error) {
	if len(commands) > domain.MaxBotCommands {
		return 0, domain.ErrBotCommandInvalid
	}
	clean := make([]domain.BotCommand, 0, len(commands))
	for _, c := range commands {
		cmd := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(c.Command, "/")))
		desc := strings.TrimSpace(c.Description)
		if !domain.ValidBotCommandName(cmd) || desc == "" || len(desc) > domain.MaxBotCommandDescriptionLen {
			return 0, domain.ErrBotCommandInvalid
		}
		clean = append(clean, domain.BotCommand{Command: cmd, Description: desc, Ephemeral: c.Ephemeral})
	}
	// Same-value short-circuit: bot frameworks commonly resend the identical command set unconditionally on
	// startup; skipping avoids a pointless bot_info_version bump (which drives every client to make another
	// getFullUser round) and redundant pushes. Non-atomic (a concurrent write after the read does not affect
	// correctness: either the other side already bumped, or we bump once more).
	cur, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, domain.ErrBotNotFound
	}
	if botCommandsEqual(cur.Commands, clean) {
		return 0, nil // no change; the caller ignores the returned version
	}
	version, err := s.bots.UpdateBotCommands(ctx, botUserID, clean)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	if s.hooks != nil {
		s.hooks.PushBotCommandsChanged(ctx, botUserID, clean)
	}
	return version, nil
}

func botCommandsEqual(a, b []domain.BotCommand) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Command != b[i].Command || a[i].Description != b[i].Description || a[i].Ephemeral != b[i].Ephemeral {
			return false
		}
	}
	return true
}

// GetBotCommands returns the bot's default scope commands.
func (s *Service) GetBotCommands(ctx context.Context, botUserID int64) ([]domain.BotCommand, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, domain.ErrBotNotFound
	}
	return profile.Commands, nil
}

// SetBotInfo updates the bot's name (users.first_name)/about (users.about)/description
// (bots.description) and returns the bumped bot_info_version.
func (s *Service) SetBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error) {
	if upd.SetName {
		upd.Name = strings.TrimSpace(upd.Name)
		if upd.Name == "" || utf8.RuneCountInString(upd.Name) > domain.MaxBotNameLength {
			return 0, domain.ErrBotInfoInvalid
		}
	}
	if upd.SetAbout && utf8.RuneCountInString(upd.About) > domain.MaxBotAboutLen {
		return 0, domain.ErrBotInfoInvalid
	}
	if upd.SetDescription && utf8.RuneCountInString(upd.Description) > domain.MaxBotDescriptionLen {
		return 0, domain.ErrBotInfoInvalid
	}
	if !upd.SetName && !upd.SetAbout && !upd.SetDescription {
		return 0, domain.ErrBotInfoInvalid
	}
	version, err := s.bots.UpdateBotInfo(ctx, botUserID, upd)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// GetBotInfo returns the bot's name/about/description (name=users.first_name,
// about=users.about, description=bots.description).
func (s *Service) GetBotInfo(ctx context.Context, botUserID int64) (name, about, description string, err error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		return "", "", "", domain.ErrBotNotFound
	}
	u, found, err := s.users.ByID(ctx, botUserID)
	if err != nil {
		return "", "", "", err
	}
	if !found {
		return "", "", "", domain.ErrBotNotFound
	}
	return u.FirstName, u.About, profile.Description, nil
}

// SetBotMenuButton sets the bot's menu button (per-bot global) and returns the new bot_info_version.
func (s *Service) SetBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (int, error) {
	switch button.Type {
	case domain.BotMenuButtonDefault, domain.BotMenuButtonCommands:
		button.Text, button.URL = "", ""
	case domain.BotMenuButtonWebView:
		button.Text = strings.TrimSpace(button.Text)
		button.URL = strings.TrimSpace(button.URL)
		if button.Text == "" || len(button.Text) > domain.MaxBotMenuButtonTextLen ||
			button.URL == "" || len(button.URL) > domain.MaxBotMenuButtonURLLen {
			return 0, domain.ErrBotMenuButtonInvalid
		}
		// Force https (matching the official BUTTON_URL_INVALID): the menu button URL is delivered via
		// userFull.bot_info.menu_button to every interacting user's client webview entry, so reject non-https
		// schemes like javascript:/file:/intent:, preventing bot poisoning.
		if u, err := url.Parse(button.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			return 0, domain.ErrBotMenuButtonInvalid
		}
	default:
		return 0, domain.ErrBotMenuButtonInvalid
	}
	version, err := s.bots.UpdateBotMenuButton(ctx, botUserID, button)
	if err != nil {
		return 0, err
	}
	if button.Type == domain.BotMenuButtonWebView {
		if _, _, err := s.EnsureMenuBotApp(ctx, botUserID, button); err != nil {
			return 0, err
		}
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// GetBotMenuButton returns the bot's menu button.
func (s *Service) GetBotMenuButton(ctx context.Context, botUserID int64) (domain.BotMenuButton, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return domain.BotMenuButton{}, err
	}
	if !found {
		return domain.BotMenuButton{}, domain.ErrBotNotFound
	}
	return profile.MenuButton, nil
}

// SetInlinePlaceholder sets the inline mode placeholder; an empty string disables inline mode.
func (s *Service) SetInlinePlaceholder(ctx context.Context, botUserID int64, placeholder string) (int, error) {
	placeholder = strings.TrimSpace(placeholder)
	if utf8.RuneCountInString(placeholder) > domain.MaxBotInlinePlaceholderLen {
		return 0, domain.ErrBotInlinePlaceholderInvalid
	}
	version, err := s.bots.SetBotInlinePlaceholder(ctx, botUserID, placeholder)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// SetInlineGeo sets whether the bot can receive the user's location in inline queries.
func (s *Service) SetInlineGeo(ctx context.Context, botUserID int64, enabled bool) (int, error) {
	version, err := s.bots.SetBotInlineGeo(ctx, botUserID, enabled)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// SetJoinGroups sets whether the bot can be added to groups (allow=true -> bot_nochats=false).
func (s *Service) SetJoinGroups(ctx context.Context, botUserID int64, allow bool) (int, error) {
	version, err := s.bots.SetBotNochats(ctx, botUserID, !allow)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// SetPrivacy sets the bot's in-group privacy mode (enabled=true, privacy on -> bot_chat_history=false,
// i.e. the bot only sees commands/replies; enabled=false, privacy off -> bot_chat_history=true, it sees all messages).
func (s *Service) SetPrivacy(ctx context.Context, botUserID int64, enabled bool) (int, error) {
	version, err := s.bots.SetBotChatHistory(ctx, botUserID, !enabled)
	if err != nil {
		return 0, err
	}
	s.invalidateBotReadCaches(ctx, botUserID)
	return version, nil
}

// CanSendMessage reports whether botUserID has explicit permission to initiate
// direct messages with userID.
func (s *Service) CanSendMessage(ctx context.Context, userID, botUserID int64) (bool, error) {
	if s == nil || s.bots == nil || userID == 0 || botUserID == 0 || userID == botUserID {
		return false, nil
	}
	return s.bots.CanBotSendMessage(ctx, botUserID, userID)
}

// AllowSendMessage records an explicit user grant for botUserID to message userID.
func (s *Service) AllowSendMessage(ctx context.Context, userID, botUserID int64, fromRequest bool) (bool, error) {
	if s == nil || s.bots == nil || userID == 0 || botUserID == 0 || userID == botUserID {
		return false, domain.ErrBotNotFound
	}
	return s.bots.AllowBotSendMessage(ctx, botUserID, userID, fromRequest)
}

// OwnsBot reports whether ownerUserID is the owner of botUserID (not BotFather itself).
func (s *Service) OwnsBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error) {
	profile, found, err := s.botProfile(ctx, botUserID)
	if err != nil {
		return false, err
	}
	return found && profile.OwnerUserID == ownerUserID && botUserID != domain.BotFatherUserID, nil
}

// tokenSecretAlphabet matches the official token random-segment character set.
const tokenSecretAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

func randomTokenSecret() (string, error) {
	raw := make([]byte, domain.BotTokenSecretLength)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	out := make([]byte, len(raw))
	for i, b := range raw {
		out[i] = tokenSecretAlphabet[int(b)%len(tokenSecretAlphabet)]
	}
	return string(out), nil
}

func randomInt64() (int64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("rand: %w", err)
	}
	v := int64(uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7]))
	return v, nil
}
