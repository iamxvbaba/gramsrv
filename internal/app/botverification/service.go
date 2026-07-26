// Package botverification implements the third-party bot verification use cases
// (core.telegram.org/api/bots/verification): the operator-curated icon catalogue,
// the verifier status a bot is granted, the marks a verifier applies to peers,
// and the application queue in front of them.
//
// This is the third-party badge (botVerification#f93cd45c projected onto
// user.bot_verification_icon / channel.bot_verification_icon /
// userFull.bot_verification / channelFull.bot_verification /
// chatInvite.bot_verification, with botInfo.verifier_settings advertising the
// verifier itself), never the platform checkmark. The two mechanisms never read
// or write each other's state: only the operator grants the platform flag (see
// app/verification), and a third-party verifier must never be able to mint one.
//
// Two invariants shape everything here:
//
//   - A mark is only worth as much as the verifier behind it. Verifier status is
//     granted per deployment rather than earned per peer, so every mutation
//     re-derives "this bot may verify right now" from stored settings instead of
//     trusting the caller, and the operator kill switch (BotVerifierSettings.Enabled)
//     is honoured on every write path.
//   - An icon that names no fetchable custom emoji document is worse than no
//     icon at all: clients resolve it through messages.getCustomEmojiDocuments, so
//     an unresolvable id renders as *nothing*. The peer then looks unverified while
//     the server insists it is marked, and nobody can tell the difference from the
//     outside. That is why the catalogue and every verifier grant are validated
//     against real documents through IconResolver, always before the write.
//
// The package owns no protocol or storage detail: peers are resolved through
// narrow ports the process wires to the existing app services, every durable
// mutation goes through store.BotVerificationStore, and the two decision paths
// (approve / revoke) move the mark inside the store's own transaction so an
// approved application can never exist without its mark.
package botverification

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

const (
	// defaultListLimit / maxListLimit bound one admin listing page. They match the
	// store's own clamps, so the service never asks for a page the backend would
	// silently shrink.
	defaultListLimit = 50
	maxListLimit     = 200
	// defaultApplicantLimit / maxApplicantLimit bound the applicant's own history
	// as rendered by a verifier bot's /status command.
	defaultApplicantLimit = 20
	maxApplicantLimit     = 100

	// defaultRequestRateLimit / defaultRequestRateWindow match the shipped
	// TELESRV_BOT_VERIFICATION_REQUEST_RATE_LIMIT / _WINDOW defaults.
	defaultRequestRateLimit  = 5
	defaultRequestRateWindow = 24 * time.Hour

	// requestRateLimitKeyPrefix namespaces the per-applicant creation budget in the
	// shared window limiter, so it cannot collide with the official verification
	// budget (verification:apply:) counted for the same user id.
	requestRateLimitKeyPrefix = "botverification:request:"

	// maxPeerTitleRunes bounds the stored title snapshot. 256 runes cannot exceed
	// the 1024-byte column CHECK even in the worst-case 4-bytes-per-rune encoding.
	maxPeerTitleRunes = 256
)

// ErrDisabled reports that third-party verification is switched off for this
// deployment. Every mutation refuses explicitly rather than pretending to
// succeed: an operator must never believe a mark was applied when it was not.
//
// Reads are deliberately not gated by the flag. The badges already granted keep
// rendering, exactly like a platform badge survives
// TELESRV_VERIFICATION_ENABLED=false, and blanking a specific verifier's marks is
// what the per-verifier kill switch is for. A deployment that wants the
// pre-feature wire shape leaves rpc.Deps.BotVerifications nil instead.
var ErrDisabled = errors.New("third-party bot verification is disabled")

// Store is the durable third-party verification boundary. It is an alias of
// store.BotVerificationStore rather than a copy, so the process-wide store
// satisfies it with no adapter and the two can never drift apart.
type Store = store.BotVerificationStore

// UserDirectory resolves viewer-independent account facts: whether a peer exists,
// whether it is a bot, and the title/username snapshot recorded on an
// application. users.Service satisfies it directly -- AdminUser is the existing
// "administrative truth about an account" reader, with no viewer projection and
// no privacy filtering, which is exactly what a verification check needs.
type UserDirectory interface {
	AdminUser(ctx context.Context, userID int64) (domain.User, bool, error)
}

// BotDirectory resolves bot ownership. bots.Service satisfies it directly.
//
// It answers two different questions with the same call: "is this caller allowed
// to act as this verifier bot?" on the RPC path, and "does this applicant control
// the bot it is filing for?" on the application path.
type BotDirectory interface {
	OwnsBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
}

// ChannelDirectory resolves channel facts and the applicant's rights on a
// channel. channels.Service satisfies it directly.
//
// GetParticipant is the channel aggregate's own answer about a membership, so
// this package never re-derives admin rights from a members table and cannot
// drift from the aggregate. Unlike the official verification flow this does not
// go through ListAdminedPublicChannels: a third-party mark is meaningful on a
// private channel too, and restricting applications to public peers would be a
// policy this feature does not have.
type ChannelDirectory interface {
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
	GetParticipant(ctx context.Context, userID, channelID, participantUserID int64) (domain.ChannelMember, error)
}

// PeerDirectory is the whole peer-resolution surface this service needs. It is
// the union of the three narrow ports so a process holding one aggregate facade
// can inject it once; the individual options exist because the shipped process
// has three separate services.
type PeerDirectory interface {
	UserDirectory
	BotDirectory
	ChannelDirectory
}

// PeerNotifier is the protocol edge hook invoked after a mark has already
// committed: it drops the cached peer projections and pushes the change to online
// clients. rpc.Router implements it (NotifyPeerBotVerification).
//
// A push failure never invalidates a committed mark, so it is logged rather than
// returned: retrying the mutation would be wrong, and the next authoritative peer
// read repairs the projection anyway.
type PeerNotifier interface {
	NotifyPeerBotVerification(ctx context.Context, peer domain.Peer) error
}

// IconResolver resolves custom emoji documents by id. files.Service satisfies it
// directly: GetDocuments is the same reader messages.getCustomEmojiDocuments
// answers from, which is the whole point -- the icon is validated against exactly
// the documents a client would be able to fetch.
type IconResolver interface {
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
}

// MarkApplier writes the mark from inside the store's decision transaction.
//
// store.BotVerificationStore satisfies it, and that is the default. It exists as
// its own port because of how the decision transaction works: the store hands the
// apply callback a context carrying its transaction, and the mark has to be
// written through *that* handle or "approved implies the mark exists" quietly
// degrades into two independent commits — a rolled back decision would leave the
// badge behind. Reaching a transaction handle is a storage detail the app layer
// must not know, so the process injects an implementation that pulls it out of the
// context, exactly like verification.PeerVerifier does for the platform flag.
//
// A backend whose decision is a single statement, and the in-memory backend (which
// rolls its own snapshot back), need no adapter at all: the plain store is correct
// there, which is why this port is optional.
type MarkApplier interface {
	GrantCustomVerification(ctx context.Context, mark domain.CustomVerification) (domain.CustomVerification, bool, error)
	RevokeCustomVerification(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error)
}

// ApplicantNotifier delivers a decision to the applicant as a message from the
// verifier bot. The bots service implements it.
//
// Delivery is best effort and happens after the decision has committed: an
// applicant who blocked the verifier bot must not be able to roll back a landed
// decision.
type ApplicantNotifier interface {
	SendVerificationDecision(ctx context.Context, recipientUserID int64, req domain.CustomVerificationRequest) error
}

// RateLimiter is the windowed limiter bounding application creation. It is an
// alias of store.RateLimiter rather than a copy, so the process-wide limiter
// satisfies it with no adapter.
type RateLimiter = store.RateLimiter

// Service is the third-party bot verification use-case layer.
type Service struct {
	store    Store
	users    UserDirectory
	bots     BotDirectory
	channels ChannelDirectory

	peers     PeerNotifier
	documents IconResolver
	applicant ApplicantNotifier
	marks     MarkApplier

	limiter       RateLimiter
	requestLimit  int
	requestWindow time.Duration

	enabled        bool
	maxPerVerifier int

	now func() time.Time
	log *zap.Logger
}

// Option adjusts optional service dependencies.
type Option func(*Service)

// WithStore injects the third-party verification store.
func WithStore(st Store) Option {
	return func(s *Service) { s.store = st }
}

// WithPeerDirectory injects one facade covering user, bot and channel resolution.
func WithPeerDirectory(dir PeerDirectory) Option {
	return func(s *Service) {
		if dir == nil {
			return
		}
		s.users = dir
		s.bots = dir
		s.channels = dir
	}
}

// WithUserDirectory injects the account reader (users.Service).
func WithUserDirectory(dir UserDirectory) Option {
	return func(s *Service) {
		if dir != nil {
			s.users = dir
		}
	}
}

// WithBotDirectory injects the bot ownership reader (bots.Service).
func WithBotDirectory(dir BotDirectory) Option {
	return func(s *Service) {
		if dir != nil {
			s.bots = dir
		}
	}
}

// WithChannelDirectory injects the channel reader (channels.Service).
func WithChannelDirectory(dir ChannelDirectory) Option {
	return func(s *Service) {
		if dir != nil {
			s.channels = dir
		}
	}
}

// WithPeerNotifier injects the projection-invalidation/push hook.
func WithPeerNotifier(notifier PeerNotifier) Option {
	return func(s *Service) {
		if notifier != nil {
			s.peers = notifier
		}
	}
}

// WithIconResolver injects the custom emoji document reader (files.Service).
func WithIconResolver(resolver IconResolver) Option {
	return func(s *Service) {
		if resolver != nil {
			s.documents = resolver
		}
	}
}

// WithMarkApplier injects the transaction-aware mark writer used by the decision
// callbacks. Without it the plain store is used; see MarkApplier for when that is
// not enough.
func WithMarkApplier(applier MarkApplier) Option {
	return func(s *Service) {
		if applier != nil {
			s.marks = applier
		}
	}
}

// WithApplicantNotifier injects the verifier-bot delivery port.
func WithApplicantNotifier(notifier ApplicantNotifier) Option {
	return func(s *Service) {
		if notifier != nil {
			s.applicant = notifier
		}
	}
}

// SetPeerNotifier installs the protocol-edge hook after construction. The router
// is built after the services it serves, so the push hook cannot be an option in
// every process; this mirrors verification.Service.SetPeerNotifier.
func (s *Service) SetPeerNotifier(notifier PeerNotifier) {
	if s == nil || notifier == nil {
		return
	}
	s.peers = notifier
}

// SetApplicantNotifier installs the verifier-bot delivery port after
// construction, for processes where the bots service is wired to this service in
// turn and cannot be passed as an option.
func (s *Service) SetApplicantNotifier(notifier ApplicantNotifier) {
	if s == nil || notifier == nil {
		return
	}
	s.applicant = notifier
}

// WithRateLimiter installs the application-creation budget. A non-positive limit
// or window disables the check, matching how the RPC edge treats its own limits.
func WithRateLimiter(limiter RateLimiter, limit int, window time.Duration) Option {
	return func(s *Service) {
		s.limiter = limiter
		s.requestLimit = limit
		s.requestWindow = window
	}
}

// WithEnabled toggles the feature's mutations.
func WithEnabled(enabled bool) Option {
	return func(s *Service) { s.enabled = enabled }
}

// WithMaxPerVerifier bounds how many peers one verifier may mark. Zero disables
// the bound, which only defers to the store's own
// domain.MaxCustomVerificationsPerVerifier: verifier status is granted per
// deployment, not earned per peer, so an unbounded verifier would be an unbounded
// badge printer.
func WithMaxPerVerifier(limit int) Option {
	return func(s *Service) {
		if limit >= 0 {
			s.maxPerVerifier = limit
		}
	}
}

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// WithLogger injects the service logger.
func WithLogger(log *zap.Logger) Option {
	return func(s *Service) {
		if log != nil {
			s.log = log
		}
	}
}

// NewService creates the third-party verification service. It is enabled by
// default so the only switch is the configuration flag, and it is safe without
// dependencies: every mutation reports a configuration error instead of
// proceeding with a half-wired security check, and every projection read answers
// "no mark" instead of failing the response it is embedded in.
func NewService(opts ...Option) *Service {
	s := &Service{
		requestLimit:   defaultRequestRateLimit,
		requestWindow:  defaultRequestRateWindow,
		enabled:        true,
		maxPerVerifier: domain.MaxCustomVerificationsPerVerifier,
		now:            time.Now,
		log:            zap.NewNop(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.log == nil {
		s.log = zap.NewNop()
	}
	if s.requestLimit < 0 {
		s.requestLimit = 0
	}
	if s.requestWindow < 0 {
		s.requestWindow = 0
	}
	if s.maxPerVerifier < 0 {
		s.maxPerVerifier = 0
	}
	return s
}

// Enabled reports whether the feature's mutations are switched on.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Ready reports whether mutations are on and backed by a store.
func (s *Service) Ready() bool { return s.Enabled() && s.store != nil }

// writeStore is the gate every mutation passes: the feature flag first, then the
// store. A missing store is a wiring mistake and says so, rather than surfacing
// as a nil dereference on the first grant.
func (s *Service) writeStore() (Store, error) {
	if s == nil || !s.enabled {
		return nil, ErrDisabled
	}
	if s.store == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	return s.store, nil
}

// markApplier resolves the writer the decision callbacks use: the injected
// transaction-aware one when the process supplied it, otherwise the store itself.
func (s *Service) markApplier(st Store) MarkApplier {
	if s != nil && s.marks != nil {
		return s.marks
	}
	return st
}

// readStore is the gate for the operator-facing reads. Unlike writeStore it
// ignores the feature flag: an admin panel must still be able to show what was
// granted while the feature is switched off, which is also how it can be audited
// after a shutdown.
func (s *Service) readStore() (Store, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("bot verification store is not configured")
	}
	return s.store, nil
}

// ---------------------------------------------------------------------------
// Projection reads (hot serialisation path)
// ---------------------------------------------------------------------------

// PeerVerification returns the mark a peer is rendered with: the newest one when
// several verifiers marked it, resolved by the store so every response agrees and
// a peer never flickers between icons.
//
// This runs on every peer serialisation, so an unwired service answers
// domain.ErrCustomVerificationNotFound rather than a configuration error: the RPC
// edge treats both the same way (no badge), and "not found" is the truthful answer
// when nothing can be stored at all.
func (s *Service) PeerVerification(ctx context.Context, peer domain.Peer) (domain.CustomVerification, error) {
	if s == nil || s.store == nil {
		return domain.CustomVerification{}, domain.ErrCustomVerificationNotFound
	}
	return s.store.PeerVerification(ctx, peer)
}

// PeerVerificationBatch is the N+1-free variant used by the response-boundary
// overlay: peers without a mark are simply absent from the map.
func (s *Service) PeerVerificationBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer]domain.CustomVerification, error) {
	if s == nil || s.store == nil || len(peers) == 0 {
		return map[domain.Peer]domain.CustomVerification{}, nil
	}
	return s.store.PeerVerificationBatch(ctx, peers)
}

// VerifierSettings reads one bot's verifier status, enabled or not: the caller
// needs the disabled row too, to render the kill switch and to explain
// BOT_VERIFIER_FORBIDDEN. The projection edge is what refuses to advertise a
// disabled verifier in botInfo.
func (s *Service) VerifierSettings(ctx context.Context, botID int64) (domain.BotVerifierSettings, error) {
	if s == nil || s.store == nil {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	return s.store.BotVerifierSettings(ctx, botID)
}

// VerifierSettingsBatch resolves verifier status for several bots at once for the
// botInfo projection; bots without verifier status are absent from the map.
func (s *Service) VerifierSettingsBatch(ctx context.Context, botIDs []int64) (map[int64]domain.BotVerifierSettings, error) {
	if s == nil || s.store == nil || len(botIDs) == 0 {
		return map[int64]domain.BotVerifierSettings{}, nil
	}
	return s.store.BotVerifierSettingsBatch(ctx, botIDs)
}

// ---------------------------------------------------------------------------
// bots.setCustomVerification
// ---------------------------------------------------------------------------

// SetCustomVerification applies bots.setCustomVerification: a verifier bot adding
// or removing its own mark on a peer.
//
// Every check the RPC edge already made is made again here, in this order:
//
//  1. the bot has a live (enabled) verifier row -- domain.ErrVerifierForbidden
//     covers "never was a verifier" and "the operator switched it off" alike,
//     because a client may not tell the two apart;
//  2. the caller is the bot itself or its owner. The edge resolved this from the
//     TL constructor, but an edge is not a permission boundary: this service is
//     also driven from the bot dialog and the admin panel, and a check that only
//     exists at one caller is a check that will eventually be bypassed;
//  3. the target peer exists and is verifiable at all.
//
// The description is resolved through domain.BotVerifierSettings.DescriptionFor,
// which is the single place the "may this verifier write its own text" rule lives,
// and the icon always comes from the verifier's *settings* -- a verifier cannot
// pick an icon per peer, so it cannot smuggle in one the operator never approved.
//
// changed reports whether anything actually moved: re-applying an identical mark
// or revoking an absent one answers false with no error, which is what keeps a
// retrying client from seeing a spurious failure. The peer push only runs when
// something moved, and its failure is logged rather than returned: the data is
// already consistent and re-running the mutation would be wrong.
func (s *Service) SetCustomVerification(ctx context.Context, req domain.SetCustomVerificationRequest) (bool, error) {
	st, err := s.writeStore()
	if err != nil {
		return false, err
	}
	if err := req.Validate(); err != nil {
		return false, err
	}
	settings, err := s.enabledVerifier(ctx, st, req.VerifierBotID)
	if err != nil {
		return false, err
	}
	if err := s.checkVerifierCaller(ctx, req.VerifierBotID, req.CallerUserID); err != nil {
		return false, err
	}
	if !req.Enabled {
		// Only the peer's shape is checked on the way out (Validate did that): a
		// verifier must still be able to strip its mark from a peer that has since
		// been deleted or become unresolvable, which is exactly when stripping it
		// matters most.
		removed, err := st.RevokeCustomVerification(ctx, req.VerifierBotID, req.Peer)
		if err != nil {
			return false, err
		}
		if removed {
			s.notifyPeer(ctx, req.Peer, "revoke")
		}
		return removed, nil
	}
	if _, err := s.resolvePeer(ctx, req.Peer); err != nil {
		return false, err
	}
	description, err := settings.DescriptionFor(req.CustomDescription)
	if err != nil {
		return false, err
	}
	// The stored mark is compared before writing, so an idempotent re-apply is
	// reported as changed=false instead of burning a version and pushing an update
	// nobody can observe. The per-verifier bound is only spent on a *new* mark: an
	// existing one must stay re-describable even at the limit.
	existing, exists, err := s.markState(ctx, st, req.VerifierBotID, req.Peer)
	if err != nil {
		return false, err
	}
	if exists {
		if existing.IconDocumentID == settings.IconDocumentID && existing.Description == description {
			return false, nil
		}
	} else if err := s.checkVerifierQuota(ctx, st, req.VerifierBotID); err != nil {
		return false, err
	}
	if _, _, err := st.GrantCustomVerification(ctx, domain.CustomVerification{
		VerifierBotID:   req.VerifierBotID,
		Peer:            req.Peer,
		IconDocumentID:  settings.IconDocumentID,
		Description:     description,
		GrantedByUserID: req.CallerUserID,
	}); err != nil {
		return false, err
	}
	s.notifyPeer(ctx, req.Peer, "grant")
	return true, nil
}

// ---------------------------------------------------------------------------
// Icon catalogue
// ---------------------------------------------------------------------------

// UpsertIcon adds or updates a catalogue entry, keyed by custom emoji document id.
//
// The document is resolved before anything is written. An entry pointing at a
// document no client can fetch is the one failure mode of this feature that is
// invisible from the outside: the badge simply does not render, and the operator
// sees a marked peer that looks unmarked everywhere else.
func (s *Service) UpsertIcon(ctx context.Context, icon domain.VerificationIcon) (domain.VerificationIcon, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.VerificationIcon{}, err
	}
	icon.Name = strings.TrimSpace(icon.Name)
	if err := icon.Validate(); err != nil {
		return domain.VerificationIcon{}, err
	}
	if err := s.checkIconDocument(ctx, icon.DocumentID); err != nil {
		return domain.VerificationIcon{}, err
	}
	return st.UpsertVerificationIcon(ctx, icon)
}

// SetIconActive retires or restores a catalogue entry. Marks already granted with
// it keep rendering -- the icon id is denormalised onto the mark -- so retiring an
// entry stops new grants without silently blanking existing badges.
func (s *Service) SetIconActive(ctx context.Context, iconID int64, active bool) (domain.VerificationIcon, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.VerificationIcon{}, err
	}
	if iconID <= 0 {
		return domain.VerificationIcon{}, domain.ErrVerificationIconNotFound
	}
	return st.SetVerificationIconActive(ctx, iconID, active)
}

// Icons lists the catalogue, newest first.
func (s *Service) Icons(ctx context.Context, activeOnly bool, limit int) ([]domain.VerificationIcon, error) {
	st, err := s.readStore()
	if err != nil {
		return nil, err
	}
	return st.ListVerificationIcons(ctx, activeOnly, clampLimit(limit, defaultListLimit, maxListLimit))
}

// ---------------------------------------------------------------------------
// Verifier status (operator actions)
// ---------------------------------------------------------------------------

// GrantVerifier grants or updates a bot's verifier status.
//
// Everything is validated before the write, because a verifier row is the
// authority every later mark derives from:
//
//   - the subject is a real bot account and not a built-in system entity. A
//     system bot carries its identity by construction, and making one a
//     third-party verifier would let this feature reach into the platform's own
//     accounts;
//   - the icon exists in the operator's catalogue, is active, and is usable by
//     this bot (a reserved entry belongs to one verifier only);
//   - the icon's custom emoji document really exists, so the badge renders.
//
// The push afterwards is for the *bot itself*: its botInfo.verifier_settings just
// changed. The peers it has marked are deliberately not fanned out -- a verifier
// may hold up to domain.MaxCustomVerificationsPerVerifier marks, and turning one
// operator action into ten thousand pushes would be a self-inflicted stampede.
// Their projections converge on the next authoritative read (getUsers /
// getFullUser / getDifference), which is the same convergence the peer read-model
// version already guarantees.
func (s *Service) GrantVerifier(ctx context.Context, settings domain.BotVerifierSettings) (domain.BotVerifierSettings, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	settings.CompanyName = strings.TrimSpace(settings.CompanyName)
	settings.DefaultDescription = strings.TrimSpace(settings.DefaultDescription)
	settings.GrantedBy = strings.TrimSpace(settings.GrantedBy)
	settings.GrantReason = strings.TrimSpace(settings.GrantReason)
	if err := settings.Validate(); err != nil {
		return domain.BotVerifierSettings{}, err
	}
	if err := s.checkVerifierBot(ctx, settings.BotID); err != nil {
		return domain.BotVerifierSettings{}, err
	}
	if err := s.checkCatalogueIcon(ctx, st, settings.BotID, settings.IconDocumentID); err != nil {
		return domain.BotVerifierSettings{}, err
	}
	stored, err := st.UpsertBotVerifierSettings(ctx, settings)
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	s.notifyPeer(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: stored.BotID}, "grant_verifier")
	return stored, nil
}

// SetVerifierEnabled flips the operator kill switch. The verifier keeps its row
// and the marks it granted, but it can grant nothing new and stops advertising
// itself, so flipping the switch back restores exactly what was there.
//
// A no-op flip is detected before the write, so it neither burns a version nor
// pushes an update: an admin panel toggling to the current value must not look
// like a change in the audit trail.
func (s *Service) SetVerifierEnabled(ctx context.Context, botID int64, enabled bool) (domain.BotVerifierSettings, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	if botID <= 0 {
		return domain.BotVerifierSettings{}, domain.ErrVerifierNotFound
	}
	current, err := st.BotVerifierSettings(ctx, botID)
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	if current.Enabled == enabled {
		return current, nil
	}
	stored, err := st.SetBotVerifierEnabled(ctx, botID, enabled)
	if err != nil {
		return domain.BotVerifierSettings{}, err
	}
	// Only the verifier's own botInfo is pushed; see GrantVerifier for why its
	// marked peers are left to converge on their next authoritative read.
	s.notifyPeer(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: botID}, "set_verifier_enabled")
	return stored, nil
}

// RevokeVerifier removes verifier status entirely. Its marks cascade away with it
// in the store, because a mark whose verifier no longer exists has nothing to
// render; the applications survive as history, since they reference users.
func (s *Service) RevokeVerifier(ctx context.Context, botID int64) (bool, error) {
	st, err := s.writeStore()
	if err != nil {
		return false, err
	}
	if botID <= 0 {
		return false, domain.ErrVerifierNotFound
	}
	removed, err := st.DeleteBotVerifierSettings(ctx, botID)
	if err != nil {
		return false, err
	}
	if removed {
		s.notifyPeer(ctx, domain.Peer{Type: domain.PeerTypeUser, ID: botID}, "revoke_verifier")
	}
	return removed, nil
}

// Verifiers lists verifier bots for the admin panel.
func (s *Service) Verifiers(ctx context.Context, enabledOnly bool, limit int) ([]domain.BotVerifierSettings, error) {
	st, err := s.readStore()
	if err != nil {
		return nil, err
	}
	return st.ListBotVerifiers(ctx, enabledOnly, clampLimit(limit, defaultListLimit, maxListLimit))
}

// ---------------------------------------------------------------------------
// Granted marks
// ---------------------------------------------------------------------------

// Marks is the admin listing query over granted marks, with keyset paging.
func (s *Service) Marks(ctx context.Context, filter domain.CustomVerificationFilter) ([]domain.CustomVerification, error) {
	st, err := s.readStore()
	if err != nil {
		return nil, err
	}
	if filter.PeerType != "" && !markablePeerType(filter.PeerType) {
		return nil, domain.ErrCustomVerificationTargetInvalid
	}
	filter.Limit = clampLimit(filter.Limit, defaultListLimit, maxListLimit)
	return st.ListCustomVerifications(ctx, filter)
}

// RevokeMark removes one verifier's mark from a peer on the operator's behalf.
// A repeated revoke reports changed=false rather than an error, so a panel retry
// is harmless.
func (s *Service) RevokeMark(ctx context.Context, verifierBotID int64, peer domain.Peer) (bool, error) {
	st, err := s.writeStore()
	if err != nil {
		return false, err
	}
	if verifierBotID <= 0 {
		return false, domain.ErrVerifierNotFound
	}
	// The peer is checked for shape only: an operator must still be able to strip a
	// mark from a peer that has since been deleted or become unresolvable, which is
	// exactly when stripping it matters most.
	if !markablePeer(peer) {
		return false, domain.ErrCustomVerificationTargetInvalid
	}
	removed, err := st.RevokeCustomVerification(ctx, verifierBotID, peer)
	if err != nil {
		return false, err
	}
	if removed {
		s.notifyPeer(ctx, peer, "revoke_mark")
	}
	return removed, nil
}

// ---------------------------------------------------------------------------
// Applications
// ---------------------------------------------------------------------------

// CreateRequest files an application with a verifier bot.
//
// The checks run in a fixed order, cheapest and most explanatory first, with the
// rate limit last so a refused application never costs the applicant budget and
// an attacker cannot exhaust a victim's window by probing:
//
//  1. the verifier exists and is enabled -- filing with a switched-off verifier
//     would produce a queue nobody can decide;
//  2. the applicant controls the target (owns the bot, is the channel's creator or
//     an administrator carrying change_info, or is the user target themselves);
//  3. this verifier has not already marked the peer, so the queue cannot be used
//     to re-apply for a badge the peer already carries;
//  4. the requested description is one this verifier is allowed to apply, so an
//     application cannot be filed in a shape that could never be approved;
//  5. the per-applicant creation budget.
//
// The stored title/username snapshot comes from the resolved peer, never from the
// request: an audit record whose subject a client could name freely would be
// worthless.
func (s *Service) CreateRequest(ctx context.Context, req domain.CustomVerificationRequest) (domain.CustomVerificationRequest, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	// A filed application is pending by definition and carries no decision; the
	// store enforces the same thing, and forcing it here keeps a caller from
	// smuggling a decided row past the queue.
	req.Status = domain.CustomVerificationPending
	req.DecidedBy = ""
	req.DecisionReason = ""
	req.ApprovedAt = time.Time{}
	req.RejectedAt = time.Time{}
	req.Reason = strings.TrimSpace(req.Reason)
	req.RequestedDescription = strings.TrimSpace(req.RequestedDescription)
	if err := req.Validate(); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	settings, err := s.enabledVerifier(ctx, st, req.VerifierBotID)
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	snapshot, err := s.resolvePeer(ctx, req.Peer)
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	controls, err := s.controls(ctx, req.ApplicantUserID, snapshot.peer)
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	if !controls {
		return domain.CustomVerificationRequest{}, domain.ErrVerificationNotOwner
	}
	// The (verifier, peer) slot is single-occupancy: the store refuses a second
	// pending application, and an existing mark makes an application pointless.
	// Both answer the same way, because from an applicant's side both mean "there is
	// already a verification for this pair".
	switch _, err := st.CustomVerification(ctx, req.VerifierBotID, snapshot.peer); {
	case err == nil:
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestExists
	case errors.Is(err, domain.ErrCustomVerificationNotFound):
	default:
		return domain.CustomVerificationRequest{}, err
	}
	// A description this verifier may not apply is refused now rather than at
	// approval time, where it would leave an application nobody can decide.
	if _, err := settings.DescriptionFor(req.RequestedDescription); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	if err := s.checkRequestRate(ctx, req.ApplicantUserID); err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	req.Peer = snapshot.peer
	req.PeerTitle = snapshot.title
	req.PeerUsername = snapshot.username
	return st.CreateCustomVerificationRequest(ctx, req)
}

// Request reads one application by id.
func (s *Service) Request(ctx context.Context, requestID int64) (domain.CustomVerificationRequest, error) {
	st, err := s.readStore()
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	if requestID <= 0 {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	return st.CustomVerificationRequest(ctx, requestID)
}

// Requests is the review-queue query with a bounded page size.
func (s *Service) Requests(ctx context.Context, filter domain.CustomVerificationRequestFilter) ([]domain.CustomVerificationRequest, error) {
	st, err := s.readStore()
	if err != nil {
		return nil, err
	}
	for _, status := range filter.Statuses {
		if !status.Valid() {
			return nil, domain.ErrCustomVerificationRequestInvalid
		}
	}
	if filter.PeerType != "" && !markablePeerType(filter.PeerType) {
		return nil, domain.ErrCustomVerificationTargetInvalid
	}
	filter.Limit = clampLimit(filter.Limit, defaultListLimit, maxListLimit)
	return st.ListCustomVerificationRequests(ctx, filter)
}

// RequestCounts is the queue summary by status.
func (s *Service) RequestCounts(ctx context.Context) (map[domain.CustomVerificationRequestStatus]int64, error) {
	st, err := s.readStore()
	if err != nil {
		return nil, err
	}
	return st.CustomVerificationRequestCounts(ctx)
}

// ApplicantRequests returns an applicant's own history, newest first, for a
// verifier bot's /status command.
func (s *Service) ApplicantRequests(ctx context.Context, applicantUserID int64, limit int) ([]domain.CustomVerificationRequest, error) {
	st, err := s.readStore()
	if err != nil {
		return nil, err
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrCustomVerificationRequestInvalid
	}
	return st.CustomVerificationRequestsForApplicant(ctx, applicantUserID, clampLimit(limit, defaultApplicantLimit, maxApplicantLimit))
}

// PendingRequest returns the live application for a (verifier, peer) pair, if any.
func (s *Service) PendingRequest(ctx context.Context, verifierBotID int64, peer domain.Peer) (domain.CustomVerificationRequest, error) {
	st, err := s.readStore()
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	if verifierBotID <= 0 || !markablePeer(peer) {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	return st.PendingCustomVerificationRequest(ctx, verifierBotID, peer)
}

// Approve grants the mark an application asked for.
//
// The mark is applied by the store's own decision transaction through the apply
// callback, so "approved" and "the peer carries the mark" commit together or not
// at all -- an approved application without its mark is not a reachable state. The
// verifier's live status and the target's existence are re-checked here, against a
// freshly loaded snapshot: an application can sit in the queue for days, and a
// verifier switched off in the meantime must not be able to grant through the
// review path what the RPC path would refuse.
//
// A retried approval of an already-approved application is an idempotent no-op:
// no second grant, and no second notification to a peer's audience or to the
// applicant.
func (s *Service) Approve(ctx context.Context, requestID, version int64, decidedBy, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	current, err := s.decidableRequest(ctx, st, requestID, version, domain.CustomVerificationApproved)
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if current.Status == domain.CustomVerificationApproved {
		return current, false, nil
	}
	settings, err := s.enabledVerifier(ctx, st, current.VerifierBotID)
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if _, err := s.resolvePeer(ctx, current.Peer); err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	// As on the RPC path, the bound is only spent when the approval would create a
	// mark rather than update one.
	if _, exists, err := s.markState(ctx, st, current.VerifierBotID, current.Peer); err != nil {
		return domain.CustomVerificationRequest{}, false, err
	} else if !exists {
		if err := s.checkVerifierQuota(ctx, st, current.VerifierBotID); err != nil {
			return domain.CustomVerificationRequest{}, false, err
		}
	}
	description := s.approvedDescription(settings, current.RequestedDescription)
	applier := s.markApplier(st)
	stored, changed, err := st.DecideCustomVerificationRequest(ctx, requestID, version, domain.CustomVerificationApproved, decidedBy, reason, note,
		// The callback's ctx carries the decision's transaction, so the grant is
		// written through the applier rather than the pooled store: the mark and the
		// approval have to land or fail together.
		func(ctx context.Context, decided domain.CustomVerificationRequest) error {
			_, _, err := applier.GrantCustomVerification(ctx, domain.CustomVerification{
				VerifierBotID:  decided.VerifierBotID,
				Peer:           decided.Peer,
				IconDocumentID: settings.IconDocumentID,
				Description:    description,
				// The grant is attributed to the verifier bot: the decision was made
				// through its queue, not by the applicant who filed it.
				GrantedByUserID: decided.VerifierBotID,
			})
			return err
		})
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if changed {
		s.notifyPeer(ctx, stored.Peer, "approve")
		s.notifyApplicant(ctx, stored)
	}
	return stored, changed, nil
}

// Reject closes an application against the applicant. A reason is mandatory: the
// audit trail must never contain a decision nobody can explain, and the applicant
// is told what it was. No peer state changes, so only the applicant is notified.
func (s *Service) Reject(ctx context.Context, requestID, version int64, decidedBy, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if strings.TrimSpace(reason) == "" {
		return domain.CustomVerificationRequest{}, false, domain.ErrVerificationReasonRequired
	}
	current, err := s.decidableRequest(ctx, st, requestID, version, domain.CustomVerificationRejected)
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if current.Status == domain.CustomVerificationRejected {
		return current, false, nil
	}
	// No apply callback: a rejection never had a mark to move.
	stored, changed, err := st.DecideCustomVerificationRequest(ctx, requestID, version, domain.CustomVerificationRejected, decidedBy, reason, note, nil)
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if changed {
		s.notifyApplicant(ctx, stored)
	}
	return stored, changed, nil
}

// RevokeRequest withdraws a granted mark through the application it came from.
// The mark is removed inside the decision transaction, on the same discipline as
// the approval that granted it, and the application stays as history: revoked is
// reachable only from approved, so it keeps meaning "was verified once".
//
// A reason is mandatory for the same reason it is on rejection.
func (s *Service) RevokeRequest(ctx context.Context, requestID, version int64, decidedBy, reason, note string) (domain.CustomVerificationRequest, bool, error) {
	st, err := s.writeStore()
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if strings.TrimSpace(reason) == "" {
		return domain.CustomVerificationRequest{}, false, domain.ErrVerificationReasonRequired
	}
	current, err := s.decidableRequest(ctx, st, requestID, version, domain.CustomVerificationRevoked)
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if current.Status == domain.CustomVerificationRevoked {
		return current, false, nil
	}
	applier := s.markApplier(st)
	stored, changed, err := st.DecideCustomVerificationRequest(ctx, requestID, version, domain.CustomVerificationRevoked, decidedBy, reason, note,
		// Written through the decision's own transaction, like the approval above.
		func(ctx context.Context, decided domain.CustomVerificationRequest) error {
			// A mark that is already gone is not an error: the operator may have
			// stripped it directly, and the application still has to reach "revoked".
			_, err := applier.RevokeCustomVerification(ctx, decided.VerifierBotID, decided.Peer)
			return err
		})
	if err != nil {
		return domain.CustomVerificationRequest{}, false, err
	}
	if changed {
		s.notifyPeer(ctx, stored.Peer, "revoke_request")
		s.notifyApplicant(ctx, stored)
	}
	return stored, changed, nil
}

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

// enabledVerifier resolves a bot's live verifier status.
//
// "No row" and "switched off" both answer domain.ErrVerifierForbidden, which is
// what the TL edge reports as BOT_VERIFIER_FORBIDDEN: a caller must not be able to
// tell from the error whether a bot was ever a verifier. A stored row that no
// longer validates is refused too -- it could only produce a mark clients cannot
// render.
func (s *Service) enabledVerifier(ctx context.Context, st Store, botID int64) (domain.BotVerifierSettings, error) {
	if botID <= 0 {
		return domain.BotVerifierSettings{}, domain.ErrVerifierForbidden
	}
	settings, err := st.BotVerifierSettings(ctx, botID)
	if err != nil {
		if errors.Is(err, domain.ErrVerifierNotFound) {
			return domain.BotVerifierSettings{}, domain.ErrVerifierForbidden
		}
		return domain.BotVerifierSettings{}, err
	}
	if settings.BotID != botID || !settings.Enabled {
		return domain.BotVerifierSettings{}, domain.ErrVerifierForbidden
	}
	if err := settings.Validate(); err != nil {
		return domain.BotVerifierSettings{}, err
	}
	return settings, nil
}

// checkVerifierCaller asserts that the caller may act as this verifier bot: it is
// either the bot itself or its owner. A missing bot directory is a configuration
// error rather than a pass -- a security check that cannot run must not succeed.
func (s *Service) checkVerifierCaller(ctx context.Context, verifierBotID, callerUserID int64) error {
	if callerUserID <= 0 {
		return domain.ErrVerifierForbidden
	}
	if callerUserID == verifierBotID {
		return nil
	}
	if s.bots == nil {
		return fmt.Errorf("bot verification bot directory is not configured")
	}
	owned, err := s.bots.OwnsBot(ctx, callerUserID, verifierBotID)
	if err != nil {
		if isNotFoundError(err) {
			return domain.ErrVerifierForbidden
		}
		return err
	}
	if !owned {
		return domain.ErrVerifierForbidden
	}
	return nil
}

// checkVerifierBot asserts that a verifier candidate is a real bot account.
//
// A plain user account cannot be a verifier: botInfo.verifier_settings only
// exists on a bot, so a marked-up user account would carry a status no client can
// see. Built-in system accounts are refused as well -- they are seeded from
// domain.SystemUserByID and are not the operator's to hand out, and @verifybot in
// particular collecting third-party marks would blur the very distinction between
// the two verification mechanisms.
//
// The one exception is the built-in @verifierbot (domain.VerifierBotUserID),
// which exists for exactly this status: its seed deliberately carries no badge,
// because what makes it a verifier is the operator-granted settings row. Its
// identity is read from the seed rather than the directory, so granting works the
// same way whether or not the account has been materialised yet.
func (s *Service) checkVerifierBot(ctx context.Context, botID int64) error {
	if botID <= 0 {
		return domain.ErrBotNotFound
	}
	if system, ok := domain.SystemUserByID(botID); ok {
		if botID != domain.VerifierBotUserID || !system.Bot {
			return domain.ErrVerificationTargetSystem
		}
		return nil
	}
	if s.users == nil {
		return fmt.Errorf("bot verification user directory is not configured")
	}
	user, found, err := s.users.AdminUser(ctx, botID)
	if err != nil {
		if isNotFoundError(err) {
			return domain.ErrBotNotFound
		}
		return err
	}
	if !found || user.ID <= 0 || user.Deleted || !user.Bot {
		return domain.ErrBotNotFound
	}
	return nil
}

// checkCatalogueIcon asserts that a verifier's icon is one the operator approved
// and one a client can actually draw.
func (s *Service) checkCatalogueIcon(ctx context.Context, st Store, botID, documentID int64) error {
	if documentID <= 0 {
		return domain.ErrVerificationIconInvalid
	}
	icon, err := st.VerificationIconByDocument(ctx, documentID)
	if err != nil {
		return err
	}
	if !icon.Active {
		return domain.ErrVerificationIconInactive
	}
	// A reserved entry belongs to one verifier; for anybody else it does not exist.
	if !icon.UsableBy(botID) {
		return domain.ErrVerificationIconNotFound
	}
	return s.checkIconDocument(ctx, documentID)
}

// checkIconDocument asserts that an icon's custom emoji document exists.
//
// This is the trap the whole catalogue exists to avoid. Clients resolve the icon
// through messages.getCustomEmojiDocuments, so an id naming no fetchable document
// renders as nothing: the badge is invisible, the peer looks unverified, and the
// only place the mark exists is the database. A document that is not a custom
// emoji is refused for the same reason -- the badge slot draws emoji documents,
// and a sticker or a video would be just as invisible.
func (s *Service) checkIconDocument(ctx context.Context, documentID int64) error {
	if documentID <= 0 {
		return domain.ErrVerificationIconInvalid
	}
	if s.documents == nil {
		return fmt.Errorf("bot verification icon resolver is not configured")
	}
	documents, err := s.documents.GetDocuments(ctx, []int64{documentID})
	if err != nil {
		return err
	}
	for _, document := range documents {
		if document.ID != documentID {
			continue
		}
		if !document.IsCustomEmoji() {
			return domain.ErrVerificationIconInvalid
		}
		return nil
	}
	return domain.ErrVerificationIconInvalid
}

// markState loads this verifier's current mark on the peer. A missing mark is not
// an error here: "is there one already?" is a question both grant paths ask before
// they decide whether the per-verifier bound applies.
func (s *Service) markState(ctx context.Context, st Store, verifierBotID int64, peer domain.Peer) (domain.CustomVerification, bool, error) {
	mark, err := st.CustomVerification(ctx, verifierBotID, peer)
	switch {
	case err == nil:
		return mark, true, nil
	case errors.Is(err, domain.ErrCustomVerificationNotFound):
		return domain.CustomVerification{}, false, nil
	default:
		return domain.CustomVerification{}, false, err
	}
}

// checkVerifierQuota enforces the configured per-verifier bound. It is only
// consulted before a *new* mark: an existing mark stays re-describable at the
// limit, which is also how the store counts.
func (s *Service) checkVerifierQuota(ctx context.Context, st Store, verifierBotID int64) error {
	if s.maxPerVerifier <= 0 {
		return nil
	}
	count, err := st.CountCustomVerifications(ctx, verifierBotID)
	if err != nil {
		return err
	}
	if count >= s.maxPerVerifier {
		return domain.ErrCustomVerificationLimit
	}
	return nil
}

// checkRequestRate spends one unit of the applicant's creation budget. It runs
// last among the creation checks, so a refused application never costs budget.
func (s *Service) checkRequestRate(ctx context.Context, applicantUserID int64) error {
	if s.limiter == nil || s.requestLimit <= 0 || s.requestWindow <= 0 {
		return nil
	}
	allowed, retryAfter, err := s.limiter.Allow(ctx, requestRateLimitKeyPrefix+strconv.FormatInt(applicantUserID, 10), s.requestLimit, s.requestWindow)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	s.log.Debug("bot verification request rate limited",
		zap.Int64("applicant_user_id", applicantUserID),
		zap.Int("retry_after", retryAfter))
	return domain.ErrVerificationRateLimited
}

// decidableRequest loads an application and asserts that the requested decision
// is one it can still take.
//
// An application already in the target status is returned as-is so the caller can
// answer changed=false; anything else is checked against
// domain.CanTransitionCustomVerificationStatus and the optimistic version, which
// the store then re-checks inside its transaction. Checking here first is what
// makes the loser of a two-reviewer race see a version conflict instead of a
// mark applied twice.
func (s *Service) decidableRequest(ctx context.Context, st Store, requestID, version int64, status domain.CustomVerificationRequestStatus) (domain.CustomVerificationRequest, error) {
	if requestID <= 0 {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	if version <= 0 {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationVersionConflict
	}
	current, err := st.CustomVerificationRequest(ctx, requestID)
	if err != nil {
		return domain.CustomVerificationRequest{}, err
	}
	if current.ID != requestID {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestNotFound
	}
	if current.Status == status {
		return current, nil
	}
	if !domain.CanTransitionCustomVerificationStatus(current.Status, status) {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationRequestInvalid
	}
	if current.Version != version {
		return domain.CustomVerificationRequest{}, domain.ErrCustomVerificationVersionConflict
	}
	return current, nil
}

// approvedDescription resolves the text an approved mark carries.
//
// The applicant's requested description is honoured only while the verifier is
// allowed to write its own; if the operator revoked that permission after the
// application was filed, the operator default is applied instead. Refusing the
// approval would be worse: the application would be stuck in a state no reviewer
// could clear, over a permission the applicant never controlled.
func (s *Service) approvedDescription(settings domain.BotVerifierSettings, requested string) string {
	description, err := settings.DescriptionFor(requested)
	if err == nil {
		return description
	}
	return strings.TrimSpace(settings.DefaultDescription)
}

// resolvePeer loads the target peer and reports whether it can carry a mark at
// all, together with the title/username snapshot an application records.
func (s *Service) resolvePeer(ctx context.Context, peer domain.Peer) (peerSnapshot, error) {
	if !markablePeer(peer) {
		return peerSnapshot{}, domain.ErrCustomVerificationTargetInvalid
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		// A built-in account's identity is seeded, not granted: a third-party
		// verifier must not be able to decorate @verifybot or the service account.
		if domain.IsSystemUserID(peer.ID) {
			return peerSnapshot{}, domain.ErrCustomVerificationTargetInvalid
		}
		if s.users == nil {
			return peerSnapshot{}, fmt.Errorf("bot verification user directory is not configured")
		}
		user, found, err := s.users.AdminUser(ctx, peer.ID)
		if err != nil {
			return peerSnapshot{}, mapLookupError(err)
		}
		if !found || user.ID <= 0 || user.Deleted {
			return peerSnapshot{}, domain.ErrCustomVerificationTargetInvalid
		}
		return peerSnapshot{
			peer:     domain.Peer{Type: domain.PeerTypeUser, ID: user.ID},
			title:    userTitle(user),
			username: user.Username,
		}, nil
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return peerSnapshot{}, fmt.Errorf("bot verification channel directory is not configured")
		}
		channel, err := s.channels.GetChannelByID(ctx, peer.ID)
		if err != nil {
			return peerSnapshot{}, mapLookupError(err)
		}
		if channel.ID <= 0 || channel.Deleted {
			return peerSnapshot{}, domain.ErrCustomVerificationTargetInvalid
		}
		return peerSnapshot{
			peer:     domain.Peer{Type: domain.PeerTypeChannel, ID: channel.ID},
			title:    truncateRunes(strings.TrimSpace(channel.Title), maxPeerTitleRunes),
			username: channel.Username,
		}, nil
	default:
		return peerSnapshot{}, domain.ErrCustomVerificationTargetInvalid
	}
}

// peerSnapshot is a freshly resolved target: the canonical peer plus the
// identity fields an application stores for the audit trail.
type peerSnapshot struct {
	peer     domain.Peer
	title    string
	username string
}

// controls reports whether the applicant may file for this peer.
//
// Each authority stays where it belongs: bot ownership is the bot aggregate's
// answer, channel rights are the channel aggregate's membership, and a user
// target is only controlled by being that user. The channel branch accepts the
// creator and an active administrator carrying change_info -- the same right that
// governs the channel's public identity (title, username), which is what a badge
// on it is.
func (s *Service) controls(ctx context.Context, applicantUserID int64, peer domain.Peer) (bool, error) {
	if applicantUserID <= 0 {
		return false, domain.ErrCustomVerificationRequestInvalid
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if applicantUserID == peer.ID {
			return true, nil
		}
		if s.bots == nil {
			return false, fmt.Errorf("bot verification bot directory is not configured")
		}
		owned, err := s.bots.OwnsBot(ctx, applicantUserID, peer.ID)
		if err != nil {
			if isNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		return owned, nil
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return false, fmt.Errorf("bot verification channel directory is not configured")
		}
		channel, err := s.channels.GetChannelByID(ctx, peer.ID)
		if err != nil {
			if isNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		if channel.CreatorUserID == applicantUserID {
			return true, nil
		}
		member, err := s.channels.GetParticipant(ctx, applicantUserID, peer.ID, applicantUserID)
		if err != nil {
			if isNotMemberError(err) {
				return false, nil
			}
			return false, err
		}
		if member.Status != domain.ChannelMemberActive {
			return false, nil
		}
		switch member.Role {
		case domain.ChannelRoleCreator:
			return true, nil
		case domain.ChannelRoleAdmin:
			return member.AdminRights.ChangeInfo, nil
		default:
			return false, nil
		}
	default:
		return false, domain.ErrCustomVerificationTargetInvalid
	}
}

// ---------------------------------------------------------------------------
// Notifications
// ---------------------------------------------------------------------------

// notifyPeer drops the cached peer projections and pushes the mark change to
// online clients. The mutation has already committed, so a failure is logged and
// swallowed: retrying the mutation would be wrong, and the next authoritative peer
// read repairs the projection anyway.
func (s *Service) notifyPeer(ctx context.Context, peer domain.Peer, action string) {
	if s == nil || s.peers == nil || peer.ID <= 0 {
		return
	}
	if err := s.peers.NotifyPeerBotVerification(ctx, peer); err != nil {
		s.log.Warn("bot verification peer notification failed",
			zap.String("action", action),
			zap.String("peer_type", string(peer.Type)),
			zap.Int64("peer_id", peer.ID),
			zap.Error(err))
	}
}

// notifyApplicant delivers the decision to the applicant as a message from the
// verifier bot. Like the peer push it is best effort: an applicant who blocked
// the bot must not be able to undo a committed decision.
func (s *Service) notifyApplicant(ctx context.Context, req domain.CustomVerificationRequest) {
	if s == nil || s.applicant == nil || req.ApplicantUserID <= 0 {
		return
	}
	if err := s.applicant.SendVerificationDecision(ctx, req.ApplicantUserID, req); err != nil {
		s.log.Warn("bot verification applicant notification failed",
			zap.Int64("request_id", req.ID),
			zap.Int64("applicant_user_id", req.ApplicantUserID),
			zap.String("status", string(req.Status)),
			zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// markablePeer mirrors the store's peer_type CHECK: only users (including bots)
// and channels carry a third-party mark.
func markablePeer(peer domain.Peer) bool {
	return markablePeerType(peer.Type) && peer.ID > 0
}

func markablePeerType(peerType domain.PeerType) bool {
	return peerType == domain.PeerTypeUser || peerType == domain.PeerTypeChannel
}

// mapLookupError folds a peer aggregate's "no such peer" answers into this
// package's vocabulary and leaves real failures alone.
func mapLookupError(err error) error {
	if isNotFoundError(err) {
		return domain.ErrCustomVerificationTargetInvalid
	}
	return err
}

func isNotFoundError(err error) bool {
	return errors.Is(err, domain.ErrUserNotFound) ||
		errors.Is(err, domain.ErrBotNotFound) ||
		errors.Is(err, domain.ErrChannelInvalid)
}

// isNotMemberError reports the channel aggregate's "this account is not in that
// channel" answers, which mean "does not control it" rather than a failure.
func isNotMemberError(err error) bool {
	return isNotFoundError(err) ||
		errors.Is(err, domain.ErrUserNotParticipant) ||
		errors.Is(err, domain.ErrChannelAdminRequired)
}

func userTitle(user domain.User) string {
	title := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	return truncateRunes(title, maxPeerTitleRunes)
}

func clampLimit(limit, fallback, maximum int) int {
	if limit <= 0 {
		return fallback
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func truncateRunes(value string, max int) string {
	if max <= 0 || utf8.RuneCountInString(value) <= max {
		return value
	}
	return string([]rune(value)[:max])
}
