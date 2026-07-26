// Package verification implements the official platform verification use cases:
// the applicant flow filed through the built-in @verifybot, the reviewer flow
// driven from the admin panel, and the applicant-notification delivery cycle.
//
// This is the platform badge (user#b1b8cc83 verified / channel#d49f34c6
// verified), never the third-party botVerification mechanism. An approval here
// flips exactly one flag on one peer record.
//
// Everything that decides whether a target may carry the badge lives in this
// package, and it is evaluated twice: once when the application is filed and
// again, against a freshly loaded snapshot, at the moment of approval. A target
// that turned scam, lost its username, was frozen or was verified by another
// route between submission and review is refused at the second gate, so the
// review queue can never be used to launder a state the submission path forbids.
//
// The package owns no protocol or storage detail: peers are resolved and flagged
// through narrow ports the process wires to the existing app services, and every
// durable mutation goes through store.VerificationStore under the application's
// optimistic-locking version.
package verification

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
	// defaultListLimit / maxListLimit bound one review-queue page.
	defaultListLimit = 50
	maxListLimit     = 200
	// defaultEventLimit / maxEventLimit bound one application history page.
	defaultEventLimit = 50
	maxEventLimit     = 200
	// defaultApplicantLimit / maxApplicantLimit bound the applicant's own
	// history as rendered by the bot's /status command.
	defaultApplicantLimit = 20
	maxApplicantLimit     = 100
	// defaultNotifyBatch / maxNotifyBatch bound one notification cycle.
	defaultNotifyBatch = 50
	maxNotifyBatch     = 500

	// defaultRejectCooldown matches the shipped
	// TELESRV_VERIFICATION_REJECT_COOLDOWN default: a rejected applicant waits a
	// month before filing the same target again.
	defaultRejectCooldown = 720 * time.Hour
	// defaultApplyRateLimit / defaultApplyRateWindow match the shipped
	// TELESRV_VERIFICATION_APPLY_RATE_LIMIT / _WINDOW defaults.
	defaultApplyRateLimit  = 3
	defaultApplyRateWindow = 24 * time.Hour
	// defaultMaxActivePerUser matches TELESRV_VERIFICATION_MAX_ACTIVE_PER_USER.
	defaultMaxActivePerUser = 3

	// applyRateLimitKeyPrefix namespaces the per-applicant creation budget in the
	// shared window limiter.
	applyRateLimitKeyPrefix = "verification:apply:"

	// maxActiveScanLimit bounds the applicant-history page the active-application
	// cap is counted from. The cap itself is validated to be far below it.
	maxActiveScanLimit = 100
	// maxEligibleTargets bounds one target-picker answer, so an operator account
	// administering thousands of channels cannot turn the picker into a scan.
	maxEligibleTargets = 100
)

// Notification kinds written to the outbox by the store and handed to the
// ApplicantNotifier verbatim. They are declared here because the bot renders one
// message per kind and the two must agree.
const (
	NoticeKindSubmitted = "submitted"
	NoticeKindApproved  = "approved"
	NoticeKindRejected  = "rejected"
	NoticeKindCancelled = "cancelled"
	NoticeKindRevoked   = "revoked"
)

// ErrDisabled reports that official verification is switched off for this
// deployment. Every entry point refuses explicitly rather than answering with an
// empty result: a bot dialog and an admin panel must be able to tell "nothing to
// show" from "this deployment does not run verification". The notification cycle
// is the single exception, because a worker cadence is not a user action.
var ErrDisabled = errors.New("official verification is disabled")

// UserDirectory resolves viewer-independent account facts. users.Service
// satisfies it directly: AdminUser is the existing "administrative truth about
// an account" reader, which is exactly what an eligibility check needs (no
// viewer projection, no privacy filtering).
type UserDirectory interface {
	AdminUser(ctx context.Context, userID int64) (domain.User, bool, error)
}

// BotDirectory resolves the applicant's bots and their ownership.
// bots.Service satisfies it directly.
type BotDirectory interface {
	ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error)
	OwnsBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
}

// ChannelDirectory resolves channel facts and the applicant's administered
// public channels/supergroups. channels.Service satisfies it directly.
//
// ListAdminedPublicChannels is deliberately reused as the authority on control:
// it already returns only active creator/administrator memberships of public,
// non-deleted channels, so this package never re-derives admin rights and cannot
// drift from the channel aggregate's own answer.
type ChannelDirectory interface {
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
	ListAdminedPublicChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
}

// PeerDirectory is the whole peer-resolution surface this service needs. It is
// the union of the three narrow ports so a process that has one aggregate facade
// can inject it once; the individual options exist because the shipped process
// has three separate services.
type PeerDirectory interface {
	UserDirectory
	BotDirectory
	ChannelDirectory
}

// AccountFreezeProvider reports the durable account-level read-only state. It is
// the same port shape help/users/dialogs already use, so admin.Service satisfies
// it directly. Optional: without it a frozen account is not detectable here and
// the restriction check falls back to the deleted/scam/fake flags.
type AccountFreezeProvider interface {
	AccountFreeze(ctx context.Context, userID int64) (domain.AccountFreeze, bool, error)
}

// PeerVerifier flips the platform verification flag on a peer record. It is
// invoked from inside the store's decision transaction, so "approved" and
// "target carries the badge" commit together or not at all.
type PeerVerifier interface {
	SetUserVerified(ctx context.Context, userID int64, verified bool) error
	SetChannelVerified(ctx context.Context, channelID int64, verified bool) error
}

// PeerNotifier is the protocol edge hook invoked after a decision has already
// committed: it invalidates the cached peer projections and pushes the change to
// online clients. rpc.Router implements it. A push failure never invalidates the
// decision, so it is logged rather than returned.
type PeerNotifier interface {
	NotifyPeerVerified(ctx context.Context, peer domain.Peer) error
}

// ApplicantNotifier delivers one queued outbox row as a @verifybot message. The
// bots service implements it; kind is the outbox row's kind (see NoticeKind*).
type ApplicantNotifier interface {
	SendVerificationNotice(ctx context.Context, recipientUserID int64, app domain.VerificationApplication, kind string) error
}

// RateLimiter is the windowed limiter used to bound application creation. It is
// an alias of store.RateLimiter rather than a copy, so the process-wide limiter
// satisfies it with no adapter and the two can never drift apart.
type RateLimiter = store.RateLimiter

// Service is the official verification use-case layer.
type Service struct {
	store    store.VerificationStore
	users    UserDirectory
	bots     BotDirectory
	channels ChannelDirectory
	freezes  AccountFreezeProvider

	verifier  PeerVerifier
	peers     PeerNotifier
	applicant ApplicantNotifier

	limiter     RateLimiter
	applyLimit  int
	applyWindow time.Duration

	enabled          bool
	allowUserTargets bool
	rejectCooldown   time.Duration
	maxActivePerUser int

	now func() time.Time
	log *zap.Logger
}

// Option adjusts optional service dependencies.
type Option func(*Service)

// WithStore injects the verification application store.
func WithStore(st store.VerificationStore) Option {
	return func(s *Service) { s.store = st }
}

// WithPeerDirectory injects one facade covering user, bot and channel
// resolution.
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

// WithAccountFreezeProvider injects the account freeze reader (admin.Service).
func WithAccountFreezeProvider(provider AccountFreezeProvider) Option {
	return func(s *Service) {
		if provider != nil {
			s.freezes = provider
		}
	}
}

// WithPeerVerifier injects the flag writer.
func WithPeerVerifier(verifier PeerVerifier) Option {
	return func(s *Service) {
		if verifier != nil {
			s.verifier = verifier
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

// WithApplicantNotifier injects the @verifybot delivery port.
func WithApplicantNotifier(notifier ApplicantNotifier) Option {
	return func(s *Service) {
		if notifier != nil {
			s.applicant = notifier
		}
	}
}

// SetPeerNotifier installs the protocol-edge hook after construction. The
// router is built after the services it serves, so the push hook cannot be an
// option in every process; this mirrors usernames.Service.SetPeerUsernameNotifier.
func (s *Service) SetPeerNotifier(notifier PeerNotifier) {
	if s == nil || notifier == nil {
		return
	}
	s.peers = notifier
}

// SetApplicantNotifier installs the @verifybot delivery port after
// construction, for processes where the bot service is wired to this service in
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
		s.applyLimit = limit
		s.applyWindow = window
	}
}

// WithEnabled toggles the feature.
func WithEnabled(enabled bool) Option {
	return func(s *Service) { s.enabled = enabled }
}

// WithAllowUserTargets opts plain user accounts in as verification subjects.
// Off by default: the official process verifies a public presence, and a private
// account has nothing to check.
func WithAllowUserTargets(allow bool) Option {
	return func(s *Service) { s.allowUserTargets = allow }
}

// WithRejectCooldown configures how long a rejected applicant/target pair must
// wait before re-filing. Zero disables the cooldown.
func WithRejectCooldown(cooldown time.Duration) Option {
	return func(s *Service) {
		if cooldown >= 0 {
			s.rejectCooldown = cooldown
		}
	}
}

// WithMaxActivePerUser bounds how many applications one applicant may keep open.
// Zero disables the cap.
func WithMaxActivePerUser(limit int) Option {
	return func(s *Service) {
		if limit >= 0 {
			s.maxActivePerUser = limit
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

// NewService creates the verification service. It is enabled by default so the
// only switch is the configuration flag, and it is safe without dependencies:
// every entry point reports a configuration error instead of proceeding with a
// half-wired security check.
func NewService(opts ...Option) *Service {
	s := &Service{
		applyLimit:       defaultApplyRateLimit,
		applyWindow:      defaultApplyRateWindow,
		enabled:          true,
		rejectCooldown:   defaultRejectCooldown,
		maxActivePerUser: defaultMaxActivePerUser,
		now:              time.Now,
		log:              zap.NewNop(),
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
	if s.rejectCooldown < 0 {
		s.rejectCooldown = 0
	}
	if s.maxActivePerUser < 0 {
		s.maxActivePerUser = 0
	}
	if s.applyLimit < 0 {
		s.applyLimit = 0
	}
	if s.applyWindow < 0 {
		s.applyWindow = 0
	}
	return s
}

// Enabled reports whether the feature is switched on.
func (s *Service) Enabled() bool { return s != nil && s.enabled }

// Ready reports whether the feature is on and backed by a store, which is what
// the notification worker gates on.
func (s *Service) Ready() bool { return s.Enabled() && s.store != nil }

// AllowsUserTargets reports whether plain user accounts may be filed, so the
// bot can shape its target picker without duplicating the policy.
func (s *Service) AllowsUserTargets() bool { return s != nil && s.allowUserTargets }

func (s *Service) verificationStore() (store.VerificationStore, error) {
	if s == nil || !s.enabled {
		return nil, ErrDisabled
	}
	if s.store == nil {
		return nil, fmt.Errorf("verification store is not configured")
	}
	return s.store, nil
}

// ---------------------------------------------------------------------------
// Applicant side
// ---------------------------------------------------------------------------

// EligibleTargets lists the peers the applicant controls together with whether
// each one can be filed right now. Ineligible candidates are returned with the
// domain reason rather than dropped, so the bot can explain "this channel is
// already verified" instead of silently hiding it.
//
// Ownership is not re-derived here: the candidates come from the ownership
// scoped listings themselves (owned bots, administered public channels), and the
// applicant's own account is only offered when user targets are enabled.
func (s *Service) EligibleTargets(ctx context.Context, applicantUserID int64) ([]domain.VerificationTarget, error) {
	st, err := s.verificationStore()
	if err != nil {
		return nil, err
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	states, err := s.candidateStates(ctx, applicantUserID)
	if err != nil {
		return nil, err
	}
	// The per-applicant open-application cap is a property of the applicant, not
	// of any one candidate: when it is reached every candidate is reported
	// ineligible with the same reason instead of failing later at StartDraft.
	capReached := false
	if err := s.checkActiveCap(ctx, st, applicantUserID); err != nil {
		if !errors.Is(err, domain.ErrVerificationRateLimited) {
			return nil, err
		}
		capReached = true
	}
	out := make([]domain.VerificationTarget, 0, len(states))
	for _, state := range states {
		target := state.target
		target.Eligible = true
		switch {
		case capReached:
			target.Eligible = false
			target.Reason = domain.ErrVerificationRateLimited.Error()
		default:
			if err := s.evaluateState(ctx, applicantUserID, state, true); err != nil {
				if !isVerificationPolicyError(err) {
					return nil, err
				}
				target.Eligible = false
				target.Reason = err.Error()
			} else if err := s.checkTargetSlot(ctx, st, applicantUserID, state.target.Type, state.target.ID); err != nil {
				if !isVerificationPolicyError(err) {
					return nil, err
				}
				target.Eligible = false
				target.Reason = err.Error()
			}
		}
		out = append(out, target)
	}
	return out, nil
}

// StartDraft opens (or resumes) the applicant's draft for a target.
//
// Only the draft-level payload bar is enforced here; the full official bar
// (category, description length, website, independent press links) is enforced
// by Submit, because the bot fills the form one step at a time. Every target
// state check runs before anything is written, and the stored target snapshot is
// taken from the resolved peer rather than from the request, so a client cannot
// write an arbitrary title or username into the audit record.
func (s *Service) StartDraft(ctx context.Context, req domain.SubmitVerificationApplicationRequest) (domain.VerificationApplication, bool, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if req.ApplicantUserID <= 0 || req.TargetID <= 0 || !req.TargetType.Valid() {
		return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationInvalid
	}
	if utf8.RuneCountInString(req.TargetTitle) > domain.MaxVerificationTitleLength ||
		len(req.CorrelationID) > domain.MaxVerificationCorrelationLen {
		return domain.VerificationApplication{}, false, domain.ErrVerificationApplicationInvalid
	}
	req.Draft = req.Draft.Normalize()
	// The links are validated by the domain (ValidateVerificationURL), which
	// refuses non-http(s) schemes, credentials and every non-public address range.
	// The server NEVER fetches a submitted link -- not at submission, not during
	// review, not from the admin panel. That is a deliberate anti-SSRF decision:
	// an applicant-controlled URL must never become an outbound request from
	// inside the deployment's network, so validation is the only thing done to it.
	if err := req.Draft.ValidateDraft(); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	// One open draft per applicant, which is also the store's contract: an
	// existing draft is resumed rather than replaced, and resuming spends no
	// creation budget.
	if existing, found, err := s.applicantDraft(ctx, st, req.ApplicantUserID); err != nil {
		return domain.VerificationApplication{}, false, err
	} else if found {
		return existing, false, nil
	}
	target, err := s.checkTarget(ctx, req.ApplicantUserID, req.TargetType, req.TargetID)
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	// The resolved kind wins over the requested one, so a supergroup filed as a
	// channel is stored (and rate-limited, and cooldown-tracked) under the kind it
	// actually is.
	req.TargetType = target.Type
	if err := s.checkTargetSlot(ctx, st, req.ApplicantUserID, req.TargetType, req.TargetID); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if err := s.checkActiveCap(ctx, st, req.ApplicantUserID); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if err := s.checkApplyRate(ctx, req.ApplicantUserID); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	req.TargetTitle = target.Title
	req.TargetUsername = target.Username
	return st.CreateVerificationDraft(ctx, req)
}

// SaveDraft rewrites the applicant-supplied payload of an own draft.
func (s *Service) SaveDraft(ctx context.Context, applicantUserID, applicationID, version int64, draft domain.VerificationDraftInput) (domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	draft = draft.Normalize()
	// Domain link validation only; the server never dereferences these URLs.
	if err := draft.ValidateDraft(); err != nil {
		return domain.VerificationApplication{}, err
	}
	app, err := s.ownedApplication(ctx, st, applicantUserID, applicationID, version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !app.Editable() {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	return st.SaveVerificationDraft(ctx, applicationID, version, draft)
}

// Submit moves the applicant's draft into the review queue.
//
// The complete official bar is checked here, and so is the target state: a draft
// may have been sitting in the bot dialog for days, and the peer it names can
// have changed hands, lost its username or been flagged in the meantime.
func (s *Service) Submit(ctx context.Context, applicantUserID, applicationID, version int64) (domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	app, err := s.ownedApplication(ctx, st, applicantUserID, applicationID, version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusSubmitted) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	if err := draftOf(app).ValidateForSubmission(); err != nil {
		return domain.VerificationApplication{}, err
	}
	if _, err := s.checkTarget(ctx, app.ApplicantUserID, app.TargetType, app.TargetID); err != nil {
		return domain.VerificationApplication{}, err
	}
	// The draft already occupies the target's single active slot, so only the
	// re-application cooldown is re-checked here.
	if err := s.checkCooldown(ctx, st, app.ApplicantUserID, app.TargetType, app.TargetID); err != nil {
		return domain.VerificationApplication{}, err
	}
	return st.SubmitVerificationApplication(ctx, applicationID, version)
}

// Cancel withdraws an active application on the applicant's behalf.
func (s *Service) Cancel(ctx context.Context, applicantUserID, applicationID, version int64, reason string) (domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > domain.MaxVerificationReasonLength {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	app, err := s.ownedApplication(ctx, st, applicantUserID, applicationID, version)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusCancelled) {
		return domain.VerificationApplication{}, domain.ErrVerificationStatusInvalid
	}
	return st.CancelVerificationApplication(ctx, applicationID, version, reason)
}

// Draft returns the applicant's open draft, if any.
func (s *Service) Draft(ctx context.Context, applicantUserID int64) (domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if applicantUserID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	return st.VerificationDraftForApplicant(ctx, applicantUserID)
}

// Application reads one application by id. This is the reviewer-side read: the
// applicant paths go through ownedApplication, which additionally scopes by
// applicant.
func (s *Service) Application(ctx context.Context, applicationID int64) (domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if applicationID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	return st.VerificationApplication(ctx, applicationID)
}

// ApplicantApplications returns the applicant's own history, newest first.
func (s *Service) ApplicantApplications(ctx context.Context, applicantUserID int64, limit int) ([]domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return nil, err
	}
	if applicantUserID <= 0 {
		return nil, domain.ErrVerificationApplicationInvalid
	}
	return st.VerificationApplicationsForApplicant(ctx, applicantUserID, clampLimit(limit, defaultApplicantLimit, maxApplicantLimit))
}

// ---------------------------------------------------------------------------
// Reviewer side
// ---------------------------------------------------------------------------

// List is the review-queue query with a bounded page size.
func (s *Service) List(ctx context.Context, filter domain.VerificationApplicationFilter) ([]domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return nil, err
	}
	filter.Limit = clampLimit(filter.Limit, defaultListLimit, maxListLimit)
	if filter.TargetType != "" && !filter.TargetType.Valid() {
		return nil, domain.ErrVerificationTargetInvalid
	}
	for _, status := range filter.Statuses {
		if !status.Valid() {
			return nil, domain.ErrVerificationStatusInvalid
		}
	}
	return st.ListVerificationApplications(ctx, filter)
}

// Counts is the queue summary rendered above the list.
func (s *Service) Counts(ctx context.Context) (domain.VerificationStatusCounts, error) {
	st, err := s.verificationStore()
	if err != nil {
		return nil, err
	}
	return st.VerificationStatusCounts(ctx)
}

// Events returns one application's immutable history, newest first.
func (s *Service) Events(ctx context.Context, applicationID int64, limit int) ([]domain.VerificationApplicationEvent, error) {
	st, err := s.verificationStore()
	if err != nil {
		return nil, err
	}
	if applicationID <= 0 {
		return nil, domain.ErrVerificationApplicationNotFound
	}
	return st.VerificationApplicationEvents(ctx, applicationID, clampLimit(limit, defaultEventLimit, maxEventLimit))
}

// Claim assigns a reviewer to a submitted application. The store enforces the
// version and the status transition atomically, so two reviewers opening the
// same row produce exactly one owner.
func (s *Service) Claim(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if err := decision.Validate(); err != nil {
		return domain.VerificationApplication{}, err
	}
	return st.ClaimVerificationApplication(ctx, decision)
}

// Approve grants the platform badge.
//
// The target snapshot is reloaded and every eligibility check is re-run before
// the decision is recorded: approval is the only path that flips the flag, so it
// is also the last place the invariants can still be enforced. The flag itself
// is set from inside the store transaction through PeerVerifier, so "approved"
// and "verified" commit together. The post-commit push is best effort: the data
// is already consistent, and a failed push must not turn a landed decision into
// an error the panel would retry.
func (s *Service) Approve(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, bool, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if err := decision.Validate(); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if s.verifier == nil {
		return domain.VerificationApplication{}, false, fmt.Errorf("verification peer verifier is not configured")
	}
	app, err := st.VerificationApplication(ctx, decision.ApplicationID)
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	// A retried approval of an application that is already approved is an
	// idempotent no-op: the decision, the flag and the single outbox row are
	// already durable, so nothing is written and nothing is notified again.
	if app.Status == domain.VerificationStatusApproved {
		return app, false, nil
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusApproved) {
		return domain.VerificationApplication{}, false, domain.ErrVerificationStatusInvalid
	}
	if app.Version != decision.Version {
		return domain.VerificationApplication{}, false, domain.ErrVerificationVersionConflict
	}
	if _, err := s.checkTarget(ctx, app.ApplicantUserID, app.TargetType, app.TargetID); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	stored, changed, err := st.DecideVerificationApplication(ctx, decision, true, func(ctx context.Context, decided domain.VerificationApplication) error {
		return s.applyVerified(ctx, decided.TargetType, decided.TargetID, true)
	})
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if changed {
		s.notifyPeer(ctx, stored.Target(), "approved")
	}
	return stored, changed, nil
}

// Reject closes an application against the applicant. A reason is mandatory:
// the audit trail must never contain a decision nobody can explain.
func (s *Service) Reject(ctx context.Context, decision domain.VerificationDecision) (domain.VerificationApplication, bool, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if err := decision.ValidateWithReason(); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	app, err := st.VerificationApplication(ctx, decision.ApplicationID)
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if app.Status == domain.VerificationStatusRejected {
		return app, false, nil
	}
	if !domain.CanTransitionVerificationStatus(app.Status, domain.VerificationStatusRejected) {
		return domain.VerificationApplication{}, false, domain.ErrVerificationStatusInvalid
	}
	if app.Version != decision.Version {
		return domain.VerificationApplication{}, false, domain.ErrVerificationVersionConflict
	}
	// A rejection touches no peer record, so no applyVerified callback is passed.
	return st.DecideVerificationApplication(ctx, decision, false, nil)
}

// Revoke clears the platform badge of a previously approved target. The
// application stays approved: it is history, and the revocation is its own audit
// event. The flag is cleared through the same in-transaction callback discipline
// as the approval that set it.
func (s *Service) Revoke(ctx context.Context, req domain.VerificationRevocation) (domain.VerificationApplication, bool, error) {
	st, err := s.verificationStore()
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if err := req.Validate(); err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if s.verifier == nil {
		return domain.VerificationApplication{}, false, fmt.Errorf("verification peer verifier is not configured")
	}
	// A built-in system account carries its badge by construction; revoking it
	// would desynchronise the seeded record from domain.SystemUserByID.
	if req.TargetType.PeerType() == domain.PeerTypeUser && domain.IsSystemUserID(req.TargetID) {
		return domain.VerificationApplication{}, false, domain.ErrVerificationTargetSystem
	}
	stored, changed, err := st.RevokeVerification(ctx, req, func(ctx context.Context, target domain.Peer) error {
		return s.applyVerifiedPeer(ctx, target, false)
	})
	if err != nil {
		return domain.VerificationApplication{}, false, err
	}
	if changed {
		s.notifyPeer(ctx, domain.Peer{Type: req.TargetType.PeerType(), ID: req.TargetID}, "revoked")
	}
	return stored, changed, nil
}

// TargetSnapshot returns the current state of a target for the review UI: the
// title and username as they are now, the badge state, and whether the peer
// would pass the checks today. Ownership is not part of the answer because the
// snapshot is reviewer-facing and carries no applicant.
func (s *Service) TargetSnapshot(ctx context.Context, targetType domain.VerificationTargetType, targetID int64) (domain.VerificationTarget, error) {
	if s == nil || !s.enabled {
		return domain.VerificationTarget{}, ErrDisabled
	}
	if targetID <= 0 || !targetType.Valid() {
		return domain.VerificationTarget{}, domain.ErrVerificationTargetInvalid
	}
	state, err := s.targetState(ctx, targetType, targetID)
	if err != nil {
		return domain.VerificationTarget{}, err
	}
	target := state.target
	target.Eligible = true
	// applicantUserID 0 skips the ownership probe; every other check still runs.
	if err := s.evaluateState(ctx, 0, state, false); err != nil {
		if !isVerificationPolicyError(err) {
			return domain.VerificationTarget{}, err
		}
		target.Eligible = false
		target.Reason = err.Error()
	}
	return target, nil
}

// ---------------------------------------------------------------------------
// Notification delivery
// ---------------------------------------------------------------------------

// RunNotificationCycle delivers one bounded batch of queued applicant
// notifications and returns how many were delivered.
//
// A single row's delivery failure is recorded on that row and the cycle
// continues: one applicant who blocked @verifybot must not stall the queue. The
// outbox row is the retry state, so a failed row is retried on the next cycle
// with its attempt counter advanced.
func (s *Service) RunNotificationCycle(ctx context.Context, limit int) (int, error) {
	if s == nil || !s.enabled || s.store == nil {
		return 0, nil
	}
	if s.applicant == nil {
		return 0, fmt.Errorf("verification applicant notifier is not configured")
	}
	pending, err := s.store.PendingVerificationNotifications(ctx, clampLimit(limit, defaultNotifyBatch, maxNotifyBatch))
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, notification := range pending {
		if err := ctx.Err(); err != nil {
			return delivered, err
		}
		if notification.ID <= 0 {
			continue
		}
		if notification.RecipientUserID <= 0 {
			s.failNotification(ctx, notification, errors.New("verification notification has no recipient"))
			continue
		}
		if err := s.applicant.SendVerificationNotice(ctx, notification.RecipientUserID, notification.Application, notification.Kind); err != nil {
			s.failNotification(ctx, notification, err)
			continue
		}
		if err := s.store.MarkVerificationNotificationDelivered(ctx, notification.ID); err != nil {
			s.log.Warn("mark verification notification delivered failed",
				zap.Int64("notification_id", notification.ID),
				zap.Int64("application_id", notification.ApplicationID),
				zap.Error(err))
			continue
		}
		delivered++
	}
	return delivered, nil
}

func (s *Service) failNotification(ctx context.Context, notification store.VerificationNotification, cause error) {
	s.log.Warn("verification notification delivery failed",
		zap.Int64("notification_id", notification.ID),
		zap.Int64("application_id", notification.ApplicationID),
		zap.Int64("recipient_user_id", notification.RecipientUserID),
		zap.String("kind", notification.Kind),
		zap.Int("attempts", notification.Attempts),
		zap.Error(cause))
	reason := truncateRunes(cause.Error(), domain.MaxVerificationReasonLength)
	if err := s.store.MarkVerificationNotificationFailed(ctx, notification.ID, reason); err != nil {
		s.log.Warn("mark verification notification failed",
			zap.Int64("notification_id", notification.ID),
			zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// Security checks
// ---------------------------------------------------------------------------

// targetState is a freshly loaded peer snapshot plus the derived restriction
// verdict. It exists so the checks can run either against a peer this package
// loaded by id or against one that came out of an ownership-scoped listing,
// without two copies of the rules.
type targetState struct {
	target     domain.VerificationTarget
	restricted bool
}

// checkTarget loads the target and runs every eligibility check against it.
// It is called on the submission path and again, unchanged, at approval time.
func (s *Service) checkTarget(ctx context.Context, applicantUserID int64, targetType domain.VerificationTargetType, targetID int64) (domain.VerificationTarget, error) {
	if targetID <= 0 || !targetType.Valid() {
		return domain.VerificationTarget{}, domain.ErrVerificationTargetInvalid
	}
	state, err := s.targetState(ctx, targetType, targetID)
	if err != nil {
		return domain.VerificationTarget{}, err
	}
	if err := s.evaluateState(ctx, applicantUserID, state, false); err != nil {
		return domain.VerificationTarget{}, err
	}
	return state.target, nil
}

// evaluateState runs the peer-state checks in a fixed order.
//
// The system-entity check comes first because a built-in account is nobody's
// property: without it a request naming @verifybot would be refused as "not
// owned", which hides the real reason. ownershipVerified skips the ownership
// probe for candidates that came from an ownership-scoped listing.
func (s *Service) evaluateState(ctx context.Context, applicantUserID int64, state targetState, ownershipVerified bool) error {
	target := state.target
	if !target.Type.Valid() || target.ID <= 0 {
		return domain.ErrVerificationTargetInvalid
	}
	if target.Type == domain.VerificationTargetUser && !s.allowUserTargets {
		return domain.ErrVerificationUserTargetsDisabled
	}
	if target.Type.PeerType() == domain.PeerTypeUser && domain.IsSystemUserID(target.ID) {
		return domain.ErrVerificationTargetSystem
	}
	if strings.TrimSpace(target.Username) == "" {
		return domain.ErrVerificationTargetNotPublic
	}
	if applicantUserID > 0 && !ownershipVerified {
		owned, err := s.controls(ctx, applicantUserID, target.Type, target.ID)
		if err != nil {
			return err
		}
		if !owned {
			return domain.ErrVerificationNotOwner
		}
	}
	if target.Verified {
		return domain.ErrVerificationTargetAlreadyVerified
	}
	if state.restricted {
		return domain.ErrVerificationTargetRestricted
	}
	return nil
}

// targetState resolves the current peer snapshot for a target.
func (s *Service) targetState(ctx context.Context, targetType domain.VerificationTargetType, targetID int64) (targetState, error) {
	switch targetType {
	case domain.VerificationTargetBot, domain.VerificationTargetUser:
		if s.users == nil {
			return targetState{}, fmt.Errorf("verification user directory is not configured")
		}
		user, found, err := s.users.AdminUser(ctx, targetID)
		if err != nil {
			return targetState{}, mapLookupError(err)
		}
		if !found || user.ID <= 0 {
			return targetState{}, domain.ErrVerificationTargetInvalid
		}
		// A plain account filed under the bot target type would slip past
		// TELESRV_VERIFICATION_ALLOW_USER_TARGETS, so the namespaces are pinned.
		if targetType == domain.VerificationTargetBot && !user.Bot {
			return targetState{}, domain.ErrVerificationTargetInvalid
		}
		frozen, err := s.frozen(ctx, user.ID)
		if err != nil {
			return targetState{}, err
		}
		return targetState{
			target: domain.VerificationTarget{
				Type:       targetType,
				ID:         user.ID,
				Title:      userTitle(user),
				Username:   user.Username,
				AccessHash: user.AccessHash,
				Verified:   user.Verified,
			},
			restricted: user.Deleted || user.Scam || user.Fake || frozen,
		}, nil
	case domain.VerificationTargetChannel, domain.VerificationTargetSupergroup:
		if s.channels == nil {
			return targetState{}, fmt.Errorf("verification channel directory is not configured")
		}
		channel, err := s.channels.GetChannelByID(ctx, targetID)
		if err != nil {
			return targetState{}, mapLookupError(err)
		}
		if channel.ID <= 0 {
			return targetState{}, domain.ErrVerificationTargetInvalid
		}
		// The broadcast/megagroup distinction is cosmetic here: both live in the
		// channel namespace and a group can be converted after submission, so the
		// stored kind is reported back rather than used as a gate.
		return targetState{
			target: domain.VerificationTarget{
				Type:       channelTargetType(channel),
				ID:         channel.ID,
				Title:      channel.Title,
				Username:   channel.Username,
				AccessHash: channel.AccessHash,
				Verified:   channel.Verified,
			},
			restricted: channel.Deleted || channel.Scam || channel.Fake,
		}, nil
	default:
		return targetState{}, domain.ErrVerificationTargetInvalid
	}
}

// controls reports whether the applicant controls the target.
//
// Bots are answered by the bot aggregate's own ownership check, channels and
// supergroups by the channel aggregate's administered-public listing, and a user
// target only by being that user: each authority stays where it belongs.
func (s *Service) controls(ctx context.Context, applicantUserID int64, targetType domain.VerificationTargetType, targetID int64) (bool, error) {
	switch targetType {
	case domain.VerificationTargetBot:
		if s.bots == nil {
			return false, fmt.Errorf("verification bot directory is not configured")
		}
		owned, err := s.bots.OwnsBot(ctx, applicantUserID, targetID)
		if err != nil {
			if isNotFoundError(err) {
				return false, nil
			}
			return false, err
		}
		return owned, nil
	case domain.VerificationTargetUser:
		return applicantUserID == targetID, nil
	case domain.VerificationTargetChannel, domain.VerificationTargetSupergroup:
		if s.channels == nil {
			return false, fmt.Errorf("verification channel directory is not configured")
		}
		channels, err := s.channels.ListAdminedPublicChannels(ctx, applicantUserID)
		if err != nil {
			return false, err
		}
		for _, channel := range channels {
			if channel.ID == targetID {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, domain.ErrVerificationTargetInvalid
	}
}

// candidateStates collects the applicant's own peers for the target picker.
func (s *Service) candidateStates(ctx context.Context, applicantUserID int64) ([]targetState, error) {
	out := make([]targetState, 0, 8)
	if s.bots != nil {
		bots, err := s.bots.ListOwnedBots(ctx, applicantUserID)
		if err != nil {
			return nil, err
		}
		for _, bot := range bots {
			if bot.ID <= 0 || domain.IsSystemUserID(bot.ID) {
				continue
			}
			frozen, err := s.frozen(ctx, bot.ID)
			if err != nil {
				return nil, err
			}
			out = append(out, targetState{
				target: domain.VerificationTarget{
					Type:       domain.VerificationTargetBot,
					ID:         bot.ID,
					Title:      userTitle(bot),
					Username:   bot.Username,
					AccessHash: bot.AccessHash,
					Verified:   bot.Verified,
				},
				restricted: bot.Deleted || bot.Scam || bot.Fake || frozen,
			})
			if len(out) >= maxEligibleTargets {
				return out, nil
			}
		}
	}
	if s.channels != nil {
		channels, err := s.channels.ListAdminedPublicChannels(ctx, applicantUserID)
		if err != nil {
			return nil, err
		}
		for _, channel := range channels {
			if channel.ID <= 0 {
				continue
			}
			out = append(out, targetState{
				target: domain.VerificationTarget{
					Type:       channelTargetType(channel),
					ID:         channel.ID,
					Title:      channel.Title,
					Username:   channel.Username,
					AccessHash: channel.AccessHash,
					Verified:   channel.Verified,
				},
				restricted: channel.Deleted || channel.Scam || channel.Fake,
			})
			if len(out) >= maxEligibleTargets {
				return out, nil
			}
		}
	}
	// The applicant's own account is only a candidate when the operator opted in.
	if s.allowUserTargets && s.users != nil && len(out) < maxEligibleTargets {
		state, err := s.targetState(ctx, domain.VerificationTargetUser, applicantUserID)
		switch {
		case err == nil:
			out = append(out, state)
		case isVerificationPolicyError(err):
		default:
			return nil, err
		}
	}
	return out, nil
}

// checkTargetSlot refuses a second live application on the same target and
// enforces the post-rejection cooldown.
func (s *Service) checkTargetSlot(ctx context.Context, st store.VerificationStore, applicantUserID int64, targetType domain.VerificationTargetType, targetID int64) error {
	active, err := st.ActiveVerificationApplicationForTarget(ctx, targetType, targetID)
	switch {
	case err == nil:
		if active.ID > 0 {
			return domain.ErrVerificationApplicationExists
		}
	case errors.Is(err, domain.ErrVerificationApplicationNotFound):
	default:
		return err
	}
	return s.checkCooldown(ctx, st, applicantUserID, targetType, targetID)
}

// checkCooldown enforces the configured wait after a rejection of the same
// applicant/target pair. The wait is measured from the decision, not from the
// submission, so a slow review never shortens it.
func (s *Service) checkCooldown(ctx context.Context, st store.VerificationStore, applicantUserID int64, targetType domain.VerificationTargetType, targetID int64) error {
	if s.rejectCooldown <= 0 {
		return nil
	}
	last, err := st.LastVerificationRejection(ctx, applicantUserID, targetType, targetID)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		return nil
	default:
		return err
	}
	if last.ID <= 0 {
		return nil
	}
	decidedAt := last.ReviewedAt
	if decidedAt.IsZero() {
		decidedAt = last.UpdatedAt
	}
	if decidedAt.IsZero() {
		return nil
	}
	if s.now().UTC().Sub(decidedAt.UTC()) < s.rejectCooldown {
		return domain.ErrVerificationCooldown
	}
	return nil
}

// checkActiveCap bounds how many applications one applicant may keep open.
func (s *Service) checkActiveCap(ctx context.Context, st store.VerificationStore, applicantUserID int64) error {
	if s.maxActivePerUser <= 0 {
		return nil
	}
	apps, err := st.VerificationApplicationsForApplicant(ctx, applicantUserID, maxActiveScanLimit)
	if err != nil {
		if errors.Is(err, domain.ErrVerificationApplicationNotFound) {
			return nil
		}
		return err
	}
	active := 0
	for _, app := range apps {
		if app.Status.Active() {
			active++
		}
	}
	if active >= s.maxActivePerUser {
		return domain.ErrVerificationRateLimited
	}
	return nil
}

// checkApplyRate spends one unit of the applicant's creation budget. It runs
// last among the creation checks, so a refused application never costs budget
// and an attacker cannot exhaust a victim's window by probing invalid targets.
func (s *Service) checkApplyRate(ctx context.Context, applicantUserID int64) error {
	if s.limiter == nil || s.applyLimit <= 0 || s.applyWindow <= 0 {
		return nil
	}
	allowed, retryAfter, err := s.limiter.Allow(ctx, applyRateLimitKeyPrefix+strconv.FormatInt(applicantUserID, 10), s.applyLimit, s.applyWindow)
	if err != nil {
		return err
	}
	if allowed {
		return nil
	}
	s.log.Debug("verification application rate limited",
		zap.Int64("applicant_user_id", applicantUserID),
		zap.Int("retry_after", retryAfter))
	return domain.ErrVerificationRateLimited
}

// frozen reports the account-level read-only state of a user-namespace target.
func (s *Service) frozen(ctx context.Context, userID int64) (bool, error) {
	if s.freezes == nil || userID <= 0 {
		return false, nil
	}
	freeze, found, err := s.freezes.AccountFreeze(ctx, userID)
	if err != nil {
		return false, err
	}
	return found && freeze.Frozen, nil
}

// applyVerified writes the platform flag. It runs inside the store's decision
// transaction, so an error here rolls the decision back rather than leaving an
// approved application over an unflagged peer.
func (s *Service) applyVerified(ctx context.Context, targetType domain.VerificationTargetType, targetID int64, verified bool) error {
	if !targetType.Valid() || targetID <= 0 {
		return domain.ErrVerificationTargetInvalid
	}
	return s.applyVerifiedPeer(ctx, domain.Peer{Type: targetType.PeerType(), ID: targetID}, verified)
}

func (s *Service) applyVerifiedPeer(ctx context.Context, peer domain.Peer, verified bool) error {
	if s.verifier == nil {
		return fmt.Errorf("verification peer verifier is not configured")
	}
	if peer.ID <= 0 {
		return domain.ErrVerificationTargetInvalid
	}
	switch peer.Type {
	case domain.PeerTypeUser:
		if domain.IsSystemUserID(peer.ID) {
			return domain.ErrVerificationTargetSystem
		}
		return s.verifier.SetUserVerified(ctx, peer.ID, verified)
	case domain.PeerTypeChannel:
		return s.verifier.SetChannelVerified(ctx, peer.ID, verified)
	default:
		return domain.ErrVerificationTargetInvalid
	}
}

// notifyPeer invalidates the cached peer projections and pushes the badge change
// to online clients. The decision has already committed, so a failure here is
// logged and swallowed: retrying the decision would be wrong, and the next
// authoritative peer read repairs the projection anyway.
func (s *Service) notifyPeer(ctx context.Context, peer domain.Peer, action string) {
	if s.peers == nil || peer.ID <= 0 {
		return
	}
	if err := s.peers.NotifyPeerVerified(ctx, peer); err != nil {
		s.log.Warn("verification peer notification failed",
			zap.String("action", action),
			zap.String("peer_type", string(peer.Type)),
			zap.Int64("peer_id", peer.ID),
			zap.Error(err))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ownedApplication loads an application and asserts that it belongs to the
// applicant and is at the expected version.
//
// A foreign application reports "not found" rather than "not owner": the
// applicant surface is a bot dialog, and it must not become an oracle for the
// existence of other people's applications.
func (s *Service) ownedApplication(ctx context.Context, st store.VerificationStore, applicantUserID, applicationID, version int64) (domain.VerificationApplication, error) {
	if applicantUserID <= 0 || applicationID <= 0 {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationInvalid
	}
	app, err := st.VerificationApplication(ctx, applicationID)
	if err != nil {
		return domain.VerificationApplication{}, err
	}
	if app.ID != applicationID || app.ApplicantUserID != applicantUserID {
		return domain.VerificationApplication{}, domain.ErrVerificationApplicationNotFound
	}
	if version <= 0 || app.Version != version {
		return domain.VerificationApplication{}, domain.ErrVerificationVersionConflict
	}
	return app, nil
}

// applicantDraft returns the applicant's resumable draft, if any.
func (s *Service) applicantDraft(ctx context.Context, st store.VerificationStore, applicantUserID int64) (domain.VerificationApplication, bool, error) {
	draft, err := st.VerificationDraftForApplicant(ctx, applicantUserID)
	switch {
	case err == nil:
	case errors.Is(err, domain.ErrVerificationApplicationNotFound):
		return domain.VerificationApplication{}, false, nil
	default:
		return domain.VerificationApplication{}, false, err
	}
	if draft.ID <= 0 || draft.Status != domain.VerificationStatusDraft {
		return domain.VerificationApplication{}, false, nil
	}
	return draft, true, nil
}

// draftOf rebuilds the applicant payload from a stored application so the
// submission bar is checked against exactly what would be reviewed.
func draftOf(app domain.VerificationApplication) domain.VerificationDraftInput {
	return domain.VerificationDraftInput{
		Category:        app.Category,
		Description:     app.Description,
		OfficialWebsite: app.OfficialWebsite,
		SocialLinks:     app.SocialLinks,
		PressLinks:      app.PressLinks,
		AdditionalNote:  app.AdditionalNote,
	}
}

func channelTargetType(channel domain.Channel) domain.VerificationTargetType {
	if channel.Megagroup || channel.Gigagroup {
		return domain.VerificationTargetSupergroup
	}
	return domain.VerificationTargetChannel
}

func userTitle(user domain.User) string {
	title := strings.TrimSpace(strings.TrimSpace(user.FirstName) + " " + strings.TrimSpace(user.LastName))
	return truncateRunes(title, domain.MaxVerificationTitleLength)
}

// isVerificationPolicyError reports whether the error is a domain verdict about
// the target rather than an infrastructure failure. The picker turns a verdict
// into an explanation and propagates everything else.
func isVerificationPolicyError(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, domain.ErrVerificationTargetInvalid),
		errors.Is(err, domain.ErrVerificationTargetNotPublic),
		errors.Is(err, domain.ErrVerificationTargetAlreadyVerified),
		errors.Is(err, domain.ErrVerificationTargetRestricted),
		errors.Is(err, domain.ErrVerificationTargetSystem),
		errors.Is(err, domain.ErrVerificationNotOwner),
		errors.Is(err, domain.ErrVerificationUserTargetsDisabled),
		errors.Is(err, domain.ErrVerificationApplicationExists),
		errors.Is(err, domain.ErrVerificationCooldown),
		errors.Is(err, domain.ErrVerificationRateLimited):
		return true
	default:
		return false
	}
}

// mapLookupError folds a peer aggregate's "no such peer" answers into the
// verification vocabulary and leaves real failures alone.
func mapLookupError(err error) error {
	if isNotFoundError(err) {
		return domain.ErrVerificationTargetInvalid
	}
	return err
}

func isNotFoundError(err error) bool {
	return errors.Is(err, domain.ErrUserNotFound) ||
		errors.Is(err, domain.ErrBotNotFound) ||
		errors.Is(err, domain.ErrChannelInvalid)
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
	runes := []rune(value)
	return string(runes[:max])
}
