package rpc

import (
	"context"
	"time"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"

	"telesrv/internal/domain"
	"telesrv/internal/geoip"
	"telesrv/internal/sfu"
	"telesrv/internal/store"
	"telesrv/internal/turnsrv"
	"telesrv/internal/updatecdn"
)

// Following the "consumer defines the interface" convention, this file defines the
// business service interfaces Router depends on in the rpc package. app/* Service
// types implement them; gRPC clients can implement them too when going
// microservices, without changing the rpc layer. Interface methods use only domain
// and basic types, never a concrete app package -- this is the rpc<->business
// contract boundary.

// AuthService abstracts the login/registration business.
type AuthService interface {
	BindTempAuthKey(ctx context.Context, sessionID int64, binding domain.TempAuthKeyBinding) (domain.TempAuthKeyBindingResult, error)
	ResolveAuthKey(ctx context.Context, authKeyID [8]byte) ([8]byte, bool, error)
	UserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error)
	PendingPasswordUserID(ctx context.Context, authKeyID [8]byte) (int64, bool, error)
	CompletePasswordSignIn(ctx context.Context, authKeyID [8]byte, expectedUserID int64) error
	SendCode(ctx context.Context, phone string) (string, error)
	CodeDelivery(ctx context.Context, phoneCodeHash string) (domain.AuthCodeDelivery, bool, error)
	ResendCode(ctx context.Context, phone, phoneCodeHash string) (string, error)
	CancelCode(ctx context.Context, phone, phoneCodeHash string) error
	SignIn(ctx context.Context, a domain.Authorization, phone, phoneCodeHash, code string) (domain.User, domain.Message, bool, error)
	// SignInWithEmail handles auth.signIn with email_verification (the login-email path).
	SignInWithEmail(ctx context.Context, a domain.Authorization, phone, phoneCodeHash, code string) (domain.User, domain.Message, bool, error)
	SignUp(ctx context.Context, a domain.Authorization, phone, phoneCodeHash, firstName, lastName string) (domain.User, domain.Message, error)
	AcceptLoginToken(ctx context.Context, a domain.Authorization, userID int64) (domain.Authorization, error)
	// BindVerifiedLogin binds a user already verified by an external strong factor (passkey), completing authorization directly.
	BindVerifiedLogin(ctx context.Context, a domain.Authorization, userID int64) (domain.User, error)
	// SignInBot validates the bot token and binds authorization
	// (auth.importBotAuthorization); a validation failure returns
	// domain.ErrBotTokenInvalid.
	SignInBot(ctx context.Context, a domain.Authorization, token string) (domain.User, error)
	LogOut(ctx context.Context, authKeyID [8]byte) error
	Authorization(ctx context.Context, authKeyID [8]byte) (domain.Authorization, bool, error)
	AuthKeyClientInfo(ctx context.Context, authKeyID [8]byte) (domain.AuthKeyClientInfo, bool, error)
	UpdateAuthKeyClientInfo(ctx context.Context, authKeyID [8]byte, info domain.AuthKeyClientInfo) error
	ListAuthorizations(ctx context.Context, userID int64) ([]domain.Authorization, error)
	ResetAuthorization(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	ResetAuthorizations(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

type AuthDeliveryReportService interface {
	ReportMissingCode(ctx context.Context, req domain.AuthMissingCodeReportRequest) (domain.AuthDeliveryReport, bool, error)
}

type ClientTelemetryService interface {
	Record(ctx context.Context, userID int64, kind domain.ClientTelemetryKind, peer domain.Peer, subjectIDs []int64, payload any, createdAt time.Time) (domain.ClientTelemetryEvent, bool, error)
}

// SessionBinder abstracts online binding of session to user after login.
//
// The full identity of an MTProto session is raw auth_key_id + session_id. Every
// method that locates a single session must carry both values; falling back to a
// query by session_id alone is forbidden, otherwise two different auth keys reusing
// the same random session_id would cause cross-account binding, exclusion or push
// ambiguity.
type SessionBinder interface {
	BindAuthKeyForSession(rawAuthKeyID [8]byte, sessionID int64, authKeyID [8]byte)
	AuthKeyIDForSession(rawAuthKeyID [8]byte, sessionID int64) ([8]byte, bool)
	BindUserForAuthKey(rawAuthKeyID [8]byte, sessionID, userID int64)
	UserIDResolvedForAuthKey(rawAuthKeyID [8]byte, sessionID int64) (userID int64, resolved bool)
	UnbindAuthKey(authKeyID [8]byte) int
	SetReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64, receives bool)
	PushToSessionForAuthKey(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg tg.UpdatesClass) error
	// excludeAuthKeyID/excludeSessionID must be both zero (no exclusion) or both nonzero (exact exclusion).
	PushToUserExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass) (int, error)
}

// RawAuthKeySessionBinder, after a successful auth.bindTempAuthKey, switches all
// sessions established on the same raw temporary key to the canonical permanent
// identity at once. Updating only the current session would leave other connections
// started concurrently permanently stuck on the raw identity.
type RawAuthKeySessionBinder interface {
	BindAuthKeyForRawAuthKey(rawAuthKeyID [8]byte, authKeyID [8]byte) int
}

// RawAuthKeyMetadataProvider exposes the raw key protocol expiry determined at
// handshake. 0 means a permanent key; a positive value means a temporary key. Router
// only uses it to decide whether cached==raw can take the permanent fast path; the
// actual protocol-expiry rejection is still done by mtprotoedge.
type RawAuthKeyMetadataProvider interface {
	AuthKeyExpiresAtForSession(rawAuthKeyID [8]byte, sessionID int64) (expiresAt int, found bool)
}

// ImmediateSessionPusher is an optional pre-login signal direct-push capability. It
// bypasses the post-login updates-ready queue and may only be used for handshake
// messages that unlock the login flow itself, such as updateLoginToken.
type ImmediateSessionPusher interface {
	PushToSessionForAuthKeyImmediate(ctx context.Context, rawAuthKeyID [8]byte, sessionID int64, t proto.MessageType, msg tg.UpdatesClass) error
}

// SessionUpdatesStateProvider exposes the connection's current updates receive state
// (optional capability). Used for the idempotent short-circuit when setting
// receivesUpdates per RPC; when not implemented, every call takes the full set path
// (idempotent, just extra overhead).
type SessionUpdatesStateProvider interface {
	ReceivesUpdatesForAuthKey(rawAuthKeyID [8]byte, sessionID int64) bool
}

// SessionUpdatesActivationProvider serializes the expensive transition from an
// authenticated physical session to updates-ready. A successful claim belongs
// to exactly one physical connection generation; the token prevents a delayed
// callback from an old connection clearing a replacement's claim.
//
// This capability gates only membership synchronization and readiness. Each
// updates.getState/getDifference delivery keeps its own cursor commit and must
// never be coalesced with another RPC.
type SessionUpdatesActivationProvider interface {
	BeginSessionUpdatesActivation(rawAuthKeyID [8]byte, sessionID int64) (token uint64, ok bool)
	EndSessionUpdatesActivation(rawAuthKeyID [8]byte, sessionID int64, token uint64)
}

// SessionBootstrapProbeProvider makes the durable bootstrap-job readiness
// lookup a one-shot per physical connection generation. A successful probe
// includes an authoritative zero-row result. Failed delivery work releases the
// claim so a later delivered baseline can retry; replacement connections own
// independent state and reject completion from an older generation.
type SessionBootstrapProbeProvider interface {
	BeginSessionBootstrapProbe(rawAuthKeyID [8]byte, sessionID int64) (token uint64, ok bool)
	EndSessionBootstrapProbe(rawAuthKeyID [8]byte, sessionID int64, token uint64, success bool)
}

// ClientLayerBinder pushes the negotiated TL layer down to the connection
// immediately (optional capability). It is called as soon as invokeWithLayer is
// observed at the Dispatch entry, so that pending flush / concurrent push triggered
// during the same request handler already degrades at the correct layer; when not
// implemented, the connection layer can only rely on the fallback refresh after
// Dispatch returns, and pushes during the first RPC of a reconnecting old client
// would miss the downgrade.
type ClientLayerBinder interface {
	SetClientLayerForAuthKey(rawAuthKeyID [8]byte, sessionID int64, layer int)
}

// AuthKeyLayerBinder seeds an inherited auth-key default into every live
// session that still has no explicit invokeWithLayer observation. Implementors
// must not overwrite an explicit per-session profile; the returned count is
// diagnostic only.
//
// Router uses this optional capability after auth.bindTempAuthKey normalizes a
// raw temporary key to its permanent identity. Keeping it separate from
// ClientLayerBinder makes the source distinction explicit: inherited defaults
// are mutable initialization state, while an explicit session observation is a
// correction for exactly one logical session.
type AuthKeyLayerBinder interface {
	SeedInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int) int
}

// BusinessAuthKeyLayerBinder seeds unknown live sessions across every raw PFS
// key currently normalized to the same permanent/business auth key.
type BusinessAuthKeyLayerBinder interface {
	SeedInheritedLayerForBusinessAuthKey(authKeyID [8]byte, layer int) int
}

// AuthKeyLayerRefresher is the identity-normalization variant used after
// auth.bindTempAuthKey. It may replace an inherited raw-key shadow with the
// permanent key's default, but must still preserve every explicit session
// profile.
type AuthKeyLayerRefresher interface {
	RefreshInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte, layer int) int
}

// AuthKeyInheritedLayerClearer removes only the mutable inherited default for
// a physical/raw key. Explicit per-session invokeWithLayer profiles are wire
// evidence and must survive. Router uses this when a temp key binds to a
// permanent key whose durable Layer is authoritative but unsupported by this
// binary; retaining the raw key's older inherited default would silently
// downgrade naked RPCs instead of asking the client to correct explicitly.
type AuthKeyInheritedLayerClearer interface {
	ClearInheritedLayerForRawAuthKey(rawAuthKeyID [8]byte) int
}

// ActiveSessionLayerEvidenceProvider exposes the live connection's explicit
// profile when auth.bindTempAuthKey runs after the Router's bounded exact-
// profile retention window. It is deliberately session-scoped and must never
// report an inherited profile as explicit evidence.
type ActiveSessionLayerEvidenceProvider interface {
	ExplicitLayerEvidenceForAuthKey(rawAuthKeyID [8]byte, sessionID int64) (layer int, msgID int64, ok bool)
}

// SessionTerminator exposes the ability to forcibly disconnect active connections by
// business auth_key (optional). Authorization revocation (a kicked device) must
// disconnect: outbound pushes encrypt with the key the connection holds and do not
// re-check authorization, and the authorization cache of a perm-key connection only
// re-checks the authorization table on disconnect+reconnect.
type SessionTerminator interface {
	CloseSessionsForBusinessAuthKey(authKeyID [8]byte) int
}

// RawSessionTerminator exposes forcible disconnect by the connection's actual raw
// auth_key (optional). When a temp auth key is revoked, Router first finds the raw
// temp keys of the same authorization from the temp->perm cache, then uses this
// interface to kick active connections that have not fully resolved to a business
// key, avoiding waiting for the cache TTL or the next frame to expire them.
type RawSessionTerminator interface {
	CloseSessionsForRawAuthKeyExcept(authKeyID [8]byte, exceptSessionID int64) int
}

// BestEffortSessionBinder is a short-timeout push interface with exact exclusion of
// the current device by raw auth_key_id; not used for RPC result/ack.
type BestEffortSessionBinder interface {
	PushToUserExceptAuthKeySessionBestEffort(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error)
}

// TransientSessionBinder pushes short-lived updates that do not write the durable
// log (typing / presence). Key difference from ordinary pushes: when the target
// session is not ready it is skipped and not queued -- transient data cannot be
// recovered by getDifference, and once ready it is rebuilt from the getState
// snapshot / the next state change, so hoarding stale transient data is pointless.
type TransientSessionBinder interface {
	PushToUserTransientExceptAuthKeySession(ctx context.Context, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error)
}

// AuthKeyTargetedSessionBinder targets an update at the ready connection of a user's
// [device bound to a specific business auth_key] (secret chat device-level
// delivery). SessionManager implements it; it must be wired when secret chats are
// enabled, failing closed when missing, and must never fall back to account-level
// push. Not-ready connections are skipped and not queued (offline recovery via
// difference).
type AuthKeyTargetedSessionBinder interface {
	PushToUserAuthKey(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg tg.UpdatesClass) (int, error)
	PushToUserAuthKeyTransient(ctx context.Context, userID int64, businessAuthKeyID [8]byte, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error)
	PushToUserExceptBusinessAuthKey(ctx context.Context, userID int64, excludeBusinessAuthKeyID [8]byte, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error)
}

// SemanticTransientSessionBinder filters by generated exact-profile metadata
// instead of a hard-coded minimum layer. A newly generated profile therefore
// becomes eligible automatically when it has a wire constructor for semantic.
type SemanticTransientSessionBinder interface {
	PushToUserTransientCompatible(ctx context.Context, userID int64, semantic tlprofile.SemanticID, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error)
	PushToUserAuthKeyTransientCompatible(ctx context.Context, userID int64, businessAuthKeyID [8]byte, semantic tlprofile.SemanticID, t proto.MessageType, msg tg.UpdatesClass, timeout time.Duration) (int, error)
}

// OnlineUserProvider exposes a bounded runtime snapshot for best-effort fanout.
type OnlineUserProvider interface {
	IsUserOnline(userID int64) bool
	OnlineUserIDsForCandidates(candidateUserIDs []int64, limit int) []int64
	TrackChannelInterest(rawAuthKeyID [8]byte, sessionID, userID int64, channelIDs []int64)
	ClearChannelInterest(rawAuthKeyID [8]byte, sessionID, userID int64)
	OnlineChannelUserIDs(channelID int64, limit int) []int64
	// ChannelMembershipGeneration / SetSessionChannelMemberships are used as a pair:
	// the caller samples the revision before reading the persistent member list and
	// carries it back on commit; when an incremental Add/Remove happens in between,
	// Set takes the merge path and stays not-ready, and the next RPC retries a full
	// sync (preventing a full replace from overwriting an incremental join/leave in
	// the window).
	ChannelMembershipGeneration(rawAuthKeyID [8]byte, sessionID int64) int64
	SetSessionChannelMemberships(rawAuthKeyID [8]byte, sessionID, userID int64, channelIDs []int64, expectedGen int64)
	AddUserChannelMembership(userID, channelID int64)
	RemoveUserChannelMembership(userID, channelID int64)
	OnlineChannelMemberUserIDs(channelID int64, limit int) []int64
}

// ChannelSubscriptionProvider is the bounded process-local implementation of
// Telegram's public-channel short-poll subscription. A successful
// updates.getChannelDifference refresh from one session enables passive channel
// updates for the whole user account until the subscription expires.
type ChannelSubscriptionProvider interface {
	RefreshChannelSubscription(rawAuthKeyID [8]byte, sessionID, userID, channelID int64, ttl time.Duration)
	OnlineChannelSubscriberUserIDs(channelID int64, limit int) []int64
	OnlineChannelSubscriberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64
}

// ChannelNudgeProvider exposes "the remaining user ids after excluding the delivered
// set from a channel's online members", used for the UpdateChannelTooLong nudge to
// >cap online members (P0-8). SessionManager implements it; tests/unwired fakes may
// omit it (when the type-assert fails the nudge is skipped, without affecting full
// payload delivery).
type ChannelNudgeProvider interface {
	OnlineChannelMemberUserIDsExcluding(channelID int64, exclude map[int64]struct{}, limit int) []int64
}

// ChannelFanoutRecoverySessionProvider snapshots the process-local online joined-channel index in
// stable channel-id order. It is used only after every keyed fan-out mailbox is saturated. The
// fixed recovery actor accepts the temporary 8*C id slice so one sweep never repeatedly scans the
// SessionManager index or holds its global lock while sorting/database work runs.
type ChannelFanoutRecoverySessionProvider interface {
	OnlineChannelIDsSnapshot() []int64
}

// ChannelFanoutRecoveryPtsProvider reloads the authoritative channel pts after in-memory fan-out
// saturation. Production channels.Service implements it through the channel store; keeping this
// separate from ChannelsService avoids burdening lightweight RPC fakes that never run the worker.
type ChannelFanoutRecoveryPtsProvider interface {
	MaxChannelPtsBatch(ctx context.Context, channelIDs []int64) (map[int64]int, error)
}

// RateLimiter abstracts rate limiting of high-frequency RPC write operations.
type RateLimiter interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
	AllowN(ctx context.Context, key string, cost, limit int, window time.Duration) (allowed bool, retryAfterSeconds int, err error)
}

// UsersService abstracts user queries.
type UsersService interface {
	Self(ctx context.Context, userID int64) (domain.User, error)
	ByID(ctx context.Context, currentUserID, userID int64) (domain.User, bool, error)
	ByIDs(ctx context.Context, currentUserID int64, userIDs []int64) ([]domain.User, error)
}

// BaseUserBotStatusProvider exposes the immutable, viewer-independent bot bit
// without constructing a full user projection. Production users.Service
// implements it through the shared base-user Redis read model.
type BaseUserBotStatusProvider interface {
	BotStatus(ctx context.Context, userID int64) (bot bool, found bool, err error)
}

type CollectiblePhoneService interface {
	CollectiblePhone(ctx context.Context, phone string) (domain.CollectiblePhone, error)
}

type UserProjectionFactInvalidator interface {
	InvalidateAccountFreezeFact(userID int64)
	InvalidateCollectiblePhoneFact(userID int64)
}

// TelegramLoginService is the domain-only boundary shared by the MTProto RPC
// edge and the public OIDC provider. PostgreSQL remains authoritative for all
// consent transitions; the RPC layer only projects domain state to TL.
type TelegramLoginService interface {
	ValidateMessageButton(ctx context.Context, botUserID int64, rawURL string) (normalizedURL, domainName string, err error)
	AuthorizeMessageButton(ctx context.Context, params domain.TelegramLoginMessageButtonAuthorization) (domain.TelegramLoginMessageButtonResult, error)
	RequestByDeepLink(ctx context.Context, deepLink string) (domain.TelegramLoginRequest, error)
	RequestByDeepLinkForOrigin(ctx context.Context, deepLink, inAppOrigin string) (domain.TelegramLoginRequest, error)
	CheckMatchCode(ctx context.Context, deepLink, selected string) (bool, error)
	Approve(ctx context.Context, deepLink string, identity domain.TelegramLoginIdentitySnapshot, writeAllowed, phoneShared bool, matchCode string) (domain.TelegramLoginRequest, domain.TelegramLoginWebAuthorization, error)
	FinalizeRedirectByDeepLink(ctx context.Context, deepLink string) (string, error)
	FinalizeInAppRedirectByDeepLink(ctx context.Context, deepLink string) (string, error)
	Decline(ctx context.Context, deepLink string, userID int64) (domain.TelegramLoginRequest, error)
	ListWebAuthorizations(ctx context.Context, userID int64) ([]domain.TelegramLoginWebAuthorization, error)
	RevokeWebAuthorization(ctx context.Context, userID, hash int64) error
	RevokeAllWebAuthorizations(ctx context.Context, userID int64) (int64, error)
}

// BatchViewerUsersResolver is an optional UsersService capability: project the same
// set of users across multiple viewers in one shot (fan-out templating, folding the
// per-recipient ByIDs(=ForViewer) into an O(owner) query). The result is byte-equal
// to ByIDs(viewer, ids) per viewer, including the viewer-specific personal photo
// overlay. Paths declared to need fan-out warm-up must have this capability; when
// missing or failing, online fan-out fails closed and must not switch to a
// per-recipient query within the same request.
type BatchViewerUsersResolver interface {
	ByIDsForViewers(ctx context.Context, viewerUserIDs []int64, userIDs []int64) (map[int64][]domain.User, error)
}

// SparseBatchViewerUsersResolver projects only the explicitly supplied
// viewer->user edges. Local Durable Outbox uses this instead of widening one
// claim into viewers x union(users).
type SparseBatchViewerUsersResolver interface {
	ByIDsForViewerUserIDs(ctx context.Context, userIDsByViewer map[int64][]int64) (map[int64][]domain.User, error)
}

// BotsService abstracts bot metadata queries and management (bots.* RPC +
// userFull.bot_info hydrate). Write methods return the bumped bot_info_version (the
// client refetches from it).
type BotsService interface {
	BotInfo(ctx context.Context, botUserID int64) (domain.BotProfile, bool, error)
	OwnsBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
	CheckUsername(ctx context.Context, ownerUserID int64, username string) (bool, error)
	CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error)
	ListOwnedBots(ctx context.Context, ownerUserID int64) ([]domain.User, error)
	ExportBotToken(ctx context.Context, ownerUserID, botUserID int64, revoke bool) (string, error)
	SetBotCommands(ctx context.Context, botUserID int64, commands []domain.BotCommand) (int, error)
	GetBotCommands(ctx context.Context, botUserID int64) ([]domain.BotCommand, error)
	SetBotInfo(ctx context.Context, botUserID int64, upd domain.BotInfoUpdate) (int, error)
	GetBotInfo(ctx context.Context, botUserID int64) (name, about, description string, err error)
	SetBotMenuButton(ctx context.Context, botUserID int64, button domain.BotMenuButton) (int, error)
	GetBotMenuButton(ctx context.Context, botUserID int64) (domain.BotMenuButton, error)
	SetInlinePlaceholder(ctx context.Context, botUserID int64, placeholder string) (int, error)
	CanSendMessage(ctx context.Context, userID, botUserID int64) (bool, error)
	AllowSendMessage(ctx context.Context, userID, botUserID int64, fromRequest bool) (bool, error)
	UpsertBotApp(ctx context.Context, botUserID int64, app domain.BotApp) (domain.BotApp, int, error)
	GetBotAppByID(ctx context.Context, appID, accessHash int64) (domain.BotApp, bool, error)
	GetBotAppByShortName(ctx context.Context, botUserID int64, shortName string) (domain.BotApp, bool, error)
	GetMainBotApp(ctx context.Context, botUserID int64) (domain.BotApp, bool, error)
	ListBotApps(ctx context.Context, botUserID int64) ([]domain.BotApp, error)
	GetBotAppSettings(ctx context.Context, botUserID int64) (domain.BotAppSettings, bool, error)
	UpsertBotAppSettings(ctx context.Context, botUserID int64, settings domain.BotAppSettings) (int, error)
	ListBotAppPreviewMedia(ctx context.Context, botUserID, appID int64) ([]domain.BotAppPreviewMedia, error)
	UpsertBotAppPreviewMedia(ctx context.Context, media domain.BotAppPreviewMedia) (domain.BotAppPreviewMedia, int, error)
	DeleteBotAppPreviewMedia(ctx context.Context, botUserID, appID, mediaID int64) (int, error)
	ReorderBotAppPreviewMedia(ctx context.Context, botUserID, appID int64, mediaIDs []int64) (int, error)
	UpsertAttachMenuBot(ctx context.Context, botUserID int64, bot domain.BotAttachMenuBot) (int, error)
	GetAttachMenuBot(ctx context.Context, botUserID int64) (domain.BotAttachMenuBot, bool, error)
	ListAttachMenuBots(ctx context.Context) ([]domain.BotAttachMenuBot, error)
	GetAttachMenuState(ctx context.Context, userID, botUserID int64) (domain.BotAttachMenuState, bool, error)
	SetAttachMenuState(ctx context.Context, state domain.BotAttachMenuState) (domain.BotAttachMenuState, error)
	SaveRequestedWebViewButton(ctx context.Context, button domain.BotRequestedWebViewButton) (domain.BotRequestedWebViewButton, error)
	GetRequestedWebViewButton(ctx context.Context, botUserID, userID int64, reqID string) (domain.BotRequestedWebViewButton, bool, error)
	DeleteRequestedWebViewButton(ctx context.Context, botUserID, userID int64, reqID string) error
	SetBotEmojiStatusPermission(ctx context.Context, botUserID, userID int64, allowed bool) error
	BotEmojiStatusPermission(ctx context.Context, botUserID, userID int64) (bool, error)

	// Bot XTR invoices. The stored row is the settlement authority: in this
	// layer payments.sendPaymentForm carries no purpose, so the price is
	// resolved from the invoice instead of the request.
	CreateBotInvoice(ctx context.Context, invoice domain.BotInvoice) (domain.BotInvoice, error)
	BotInvoiceByMessage(ctx context.Context, botUserID, chatID int64, messageID int) (domain.BotInvoice, bool, error)

	BotInvoiceByCharge(ctx context.Context, chargeID string) (domain.BotInvoice, bool, error)

	PutWebViewCustomMethodQuery(ctx context.Context, botUserID, userID int64, method, paramsJSON string) (domain.BotWebViewCustomMethodQuery, error)
}

// ServiceBotCallbacks answers inline-button clicks for the built-in bots that run
// inside this process (@verifybot and friends); app/bots implements it.
//
// It exists because the ordinary callback path cannot serve them: an internal bot
// has no MTProto session to receive updateBotCallbackQuery and no Bot API consumer
// to drain the update queue, so pushing the query at it and waiting could only
// ever end in BOT_RESPONSE_TIMEOUT after the full 25-second window. A bot claimed
// here is answered synchronously instead, by the responder that owns it.
//
// OnCallbackQuery reports handled=false when the bot is not one of the responder's
// own, which the edge treats as invalid callback data. The answer is final:
// nothing is registered in the shared callback registry for it, so no external
// setBotCallbackAnswer can overwrite or spoof it.
//
// A nil Deps.ServiceBotCallbacks keeps the edge behaviour exactly as it was:
// every callback is pushed to the bot's session and waited on.
type ServiceBotCallbacks interface {
	HandlesBot(botUserID int64) bool
	OnCallbackQuery(ctx context.Context, query domain.BotCallbackQuery) (domain.BotCallbackAnswer, bool, error)
}

// ServiceBotInlineResults answers built-in inline bots synchronously while the
// edge still registers a fresh query_id for messages.sendInlineBotResult.
type ServiceBotInlineResults interface {
	HandlesInlineBot(botUserID int64) bool
	OnInlineQuery(ctx context.Context, botUserID, userID int64, query, offset string) (domain.BotInlineResults, bool, error)
}

// UserIdentityService is the profile extension capability of UsersService, used for username/phone resolution.
type UserIdentityService interface {
	CheckUsername(ctx context.Context, userID int64, username string) (bool, error)
	UpdateProfile(ctx context.Context, userID int64, update domain.UserProfileUpdate) (domain.User, error)
	UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error)
	UpdateBirthday(ctx context.Context, userID int64, birthday domain.Birthday) (domain.User, error)
	UpdatePersonalChannel(ctx context.Context, userID int64, channelID int64) (domain.User, error)
	ResolveUsername(ctx context.Context, currentUserID int64, username string) (domain.User, bool, error)
	ResolvePhone(ctx context.Context, currentUserID int64, phone string) (domain.User, bool, error)
}

// UserPremiumService is the Premium extension capability of UsersService:
// grant/renew, expiry cleanup (PremiumSweeper) and emoji status (Premium-only).
// Design: see docs/premium-module.md.
type UserPremiumService interface {
	GrantPremium(ctx context.Context, userID int64, months int) (domain.User, error)
	SweepExpiredPremium(ctx context.Context, now int64, limit int) ([]domain.User, error)
	UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error)
}

// UserEmojiStatusDurableService exposes the aggregate state+event write used
// by account.updateEmojiStatus. The bool is false for lightweight stores that
// require the RPC Updates service to append the event separately.
type UserEmojiStatusDurableService interface {
	UpdateEmojiStatusWithEvent(ctx context.Context, userID int64, status domain.UserEmojiStatus, date int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.User, domain.UpdateEvent, bool, error)
}

// UserColorService is the personal palette extension capability of UsersService.
// Used by account.updateColor to persist the current account's message bubble accent
// or profile background color.
type UserColorService interface {
	UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error)
}

// UserPremiumStatusService exposes a lightweight Premium check (base user cache path,
// no viewer projection), for low-cost calls by the dual-tier limits (reaction cap
// etc.).
type UserPremiumStatusService interface {
	PremiumActive(ctx context.Context, userID int64) bool
}

// AccountService abstracts account settings queries.
type AccountService interface {
	SendChangePhoneCode(ctx context.Context, userID int64, authKeyID [8]byte, sessionID int64, phone string) (string, domain.AuthCodeDelivery, error)
	ChangePhone(ctx context.Context, userID int64, authKeyID, originRawAuthKeyID [8]byte, sessionID int64, phone, phoneCodeHash, code string, date int) (domain.PhoneChangeResult, error)
	GetPassword(ctx context.Context, userID int64) (domain.PasswordSettings, error)
	GetPasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck) (domain.PrivatePasswordSettings, error)
	UpdatePasswordSettings(ctx context.Context, userID int64, check domain.PasswordCheck, input domain.PasswordInputSettings) error
	CheckPassword(ctx context.Context, userID int64, check domain.PasswordCheck) error
	IssueTmpPassword(ctx context.Context, userID int64, authKeyID [8]byte, check domain.PasswordCheck, period int, now time.Time) (domain.TmpPassword, error)
	CheckTmpPassword(ctx context.Context, userID int64, authKeyID [8]byte, tmpPassword []byte, now time.Time) error
	RevenueWithdrawalPasswordState(ctx context.Context, userID int64) (domain.RevenueWithdrawalPasswordState, error)
	RequestPasswordRecovery(ctx context.Context, userID int64) (string, error)
	CheckRecoveryPassword(ctx context.Context, userID int64, code string) error
	RecoverPassword(ctx context.Context, userID int64, code string, input *domain.PasswordInputSettings) error
	ConfirmPasswordEmail(ctx context.Context, userID int64, code string) error
	ResendPasswordEmail(ctx context.Context, userID int64) error
	CancelPasswordEmail(ctx context.Context, userID int64) error
	// Login email (independent of the 2FA recovery email): authed goes through userID, login flow/reset goes through phone.
	SendLoginEmailCode(ctx context.Context, userID int64, phone, phoneCodeHash, email string, setup bool) (string, int, error)
	VerifyLoginEmail(ctx context.Context, userID int64, phone, phoneCodeHash, code string, setup bool) (string, error)
	SetLoginEmail(ctx context.Context, userID int64, email string) error
	LoginEmail(ctx context.Context, userID int64) (string, bool, error)
	LoginEmailByPhone(ctx context.Context, phone string) (string, bool, error)
	ClearLoginEmail(ctx context.Context, userID int64) error
	ResetPassword(ctx context.Context, userID int64) (domain.PasswordResetResult, error)
	DeclinePasswordReset(ctx context.Context, userID int64) error
	SaveMusic(ctx context.Context, userID int64, req domain.SaveMusicRequest) (bool, error)
	ListSavedMusicIDs(ctx context.Context, userID int64, limit int) ([]int64, error)
	ListSavedMusic(ctx context.Context, userID int64, offset, limit int) (domain.SavedMusicList, error)
	GetSavedMusicByIDs(ctx context.Context, userID int64, ids []int64) (domain.SavedMusicList, error)
}

// AccountBusinessAutomationService is an optional extension for account business
// automation. It exposes only domain DTOs, avoiding Telegram TL types crossing the
// rpc boundary.
type AccountBusinessAutomationService interface {
	GetBusinessProfile(ctx context.Context, userID int64) (domain.BusinessProfile, bool, error)
	UpdateBusinessWorkHours(ctx context.Context, userID int64, hours *domain.BusinessWorkHours) (domain.BusinessProfile, error)
	UpdateBusinessLocation(ctx context.Context, userID int64, location *domain.BusinessLocation) (domain.BusinessProfile, error)
	UpdateBusinessIntro(ctx context.Context, userID int64, intro *domain.BusinessIntro) (domain.BusinessProfile, error)
	UpdateBusinessGreetingMessage(ctx context.Context, userID int64, greeting *domain.BusinessGreetingMessage) (domain.BusinessProfile, error)
	UpdateBusinessAwayMessage(ctx context.Context, userID int64, away *domain.BusinessAwayMessage) (domain.BusinessProfile, error)
	ListBusinessChatLinks(ctx context.Context, userID int64) ([]domain.BusinessChatLink, error)
	CreateBusinessChatLink(ctx context.Context, userID int64, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error)
	EditBusinessChatLink(ctx context.Context, userID int64, slug string, input domain.BusinessChatLinkInput) (domain.BusinessChatLink, error)
	DeleteBusinessChatLink(ctx context.Context, userID int64, slug string) (bool, error)
	ResolveBusinessChatLink(ctx context.Context, slug string, bumpViews bool) (domain.BusinessChatLink, bool, error)
	ListQuickReplies(ctx context.Context, userID int64) (domain.QuickReplyList, error)
	CheckQuickReplyShortcut(ctx context.Context, userID int64, shortcut string) (bool, error)
	SaveQuickReplyText(ctx context.Context, userID int64, shortcut string, msg domain.QuickReplyMessage) (domain.QuickReplyMutation, error)
	GetQuickReplyMessages(ctx context.Context, userID int64, shortcutID int, ids []int) (domain.QuickReplyMessages, error)
	RenameQuickReplyShortcut(ctx context.Context, userID int64, shortcutID int, shortcut string) (domain.QuickReplyMutation, error)
	ReorderQuickReplies(ctx context.Context, userID int64, order []int) (domain.QuickReplyMutation, error)
	DeleteQuickReplyShortcut(ctx context.Context, userID int64, shortcutID int) (domain.QuickReplyMutation, error)
	DeleteQuickReplyMessages(ctx context.Context, userID int64, shortcutID int, ids []int) (domain.QuickReplyMutation, error)
	GetConnectedBusinessBot(ctx context.Context, ownerUserID int64) (domain.ConnectedBusinessBot, bool, error)
	SaveConnectedBusinessBot(ctx context.Context, ownerUserID int64, bot domain.ConnectedBusinessBot) (domain.ConnectedBusinessBot, error)
	DeleteConnectedBusinessBot(ctx context.Context, ownerUserID, botUserID int64) (bool, error)
	SetConnectedBusinessBotPaused(ctx context.Context, ownerUserID, peerUserID int64, paused bool) (domain.ConnectedBusinessBotPeerState, error)
	DisableConnectedBusinessBotForPeer(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, error)
	GetConnectedBusinessBotPeerState(ctx context.Context, ownerUserID, peerUserID int64) (domain.ConnectedBusinessBotPeerState, bool, error)
}

// PrivacyService owns account privacy rule storage/evaluation.
type PrivacyService interface {
	GetRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey) (domain.PrivacyRules, error)
	SetRules(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, rules []domain.PrivacyRule) (domain.PrivacyRules, error)
	AddAllowUser(ctx context.Context, ownerUserID int64, key domain.PrivacyKey, targetUserID int64) (domain.PrivacyRules, bool, error)
	CanSee(ctx context.Context, ownerUserID, viewerUserID int64, key domain.PrivacyKey) (bool, error)
}

// HelpService abstracts startup config and the country dialing-code catalog.
type HelpService interface {
	GetAppConfig(ctx context.Context, userID int64, hash int) (domain.AppConfig, bool, error)
	GetCountries(ctx context.Context, langCode string, hash int) (domain.CountriesList, bool, error)
}

// AccountFreezeService exposes the account-level read-only fact used by the
// central RPC mutation gate. It is domain-only and shared with app/help.
type AccountFreezeService interface {
	AccountFreeze(ctx context.Context, userID int64) (domain.AccountFreeze, bool, error)
}

// AccountFreezeNotificationService owns the durable non-PTS notification
// queue. It is intentionally separate from AccountFreezeService so hot
// read-only gates can use a versioned fact cache without disabling queue
// consumption.
type AccountFreezeNotificationService interface {
	ClaimAccountFreezeNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountFreezeNotification, error)
	CompleteAccountFreezeNotification(ctx context.Context, id, version int64, now time.Time) error
}

// AccountDeletionNotificationService owns the durable tombstone queue. Deletion
// fans out to every viewer who ever shared a contact or a dialog with the
// account, so it is a queue rather than a synchronous fan-out.
type AccountDeletionNotificationService interface {
	ClaimAccountDeletionNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountDeletionNotification, error)
	CompleteAccountDeletionNotification(ctx context.Context, id int64, now time.Time) error
}

// UpdatesService abstracts update state queries.
type UpdatesService interface {
	GetState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, error)
	CurrentState(ctx context.Context, userID int64) (domain.UpdateState, error)
	ConfirmedState(ctx context.Context, authKeyID [8]byte, userID int64) (domain.UpdateState, bool, error)
	ObserveDifferenceRequest(ctx context.Context, authKeyID [8]byte, userID int64, from domain.UpdateState) (domain.UpdateState, error)
	GetDifference(ctx context.Context, authKeyID [8]byte, userID int64, from domain.UpdateState) (domain.UpdateDifference, error)
	CommitDeliveredState(ctx context.Context, authKeyID [8]byte, userID int64, state domain.UpdateState, mode domain.UpdateStateCommitMode) error
	ClearAuthKey(ctx context.Context, authKeyID [8]byte) error
	RecordNewMessage(ctx context.Context, authKeyID [8]byte, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error)
	PublishNewMessage(ctx context.Context, userID int64, msg domain.Message) (domain.UpdateEvent, domain.UpdateState, error)
	RecordStory(ctx context.Context, stateAuthKeyID [8]byte, userID int64, story domain.Story, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordStoryFanout(ctx context.Context, userID int64, story domain.Story) (domain.UpdateEvent, domain.UpdateState, error)
	RecordReadStories(ctx context.Context, stateAuthKeyID [8]byte, userID int64, read domain.StoryReadResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordSentStoryReaction(ctx context.Context, stateAuthKeyID [8]byte, userID int64, reaction domain.StoryReactionResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordNewStoryReaction(ctx context.Context, stateAuthKeyID [8]byte, ownerUserID int64, reaction domain.StoryReactionResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordQuickReplyMutation(ctx context.Context, stateAuthKeyID [8]byte, userID int64, mutation domain.QuickReplyMutation, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordReadHistory(ctx context.Context, stateAuthKeyID [8]byte, userID int64, read domain.ReadHistoryResult, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordContactsReset(ctx context.Context, stateAuthKeyID [8]byte, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelState(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogPinned(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, folderID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPinnedDialogs(ctx context.Context, stateAuthKeyID [8]byte, userID int64, folderID int, order []domain.Peer, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordSavedDialogPinned(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, pinned bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPinnedSavedDialogs(ctx context.Context, stateAuthKeyID [8]byte, userID int64, order []domain.Peer, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogUnreadMark(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, unread bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPeerSettings(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, settings domain.PeerSettings, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordPeerStoryBlocked(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, blocked bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogFilter(ctx context.Context, stateAuthKeyID [8]byte, userID int64, folderID int, folder *domain.DialogFolder, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogFilterOrder(ctx context.Context, stateAuthKeyID [8]byte, userID int64, order []int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDialogFiltersReload(ctx context.Context, stateAuthKeyID [8]byte, userID int64, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordFolderPeers(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peers []domain.FolderPeerUpdate, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelViewForumAsMessages(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, enabled bool, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordChannelDiscussionInbox(ctx context.Context, stateAuthKeyID [8]byte, userID, channelID int64, topicID, maxID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
	RecordDraftMessage(ctx context.Context, stateAuthKeyID [8]byte, userID int64, peer domain.Peer, topMsgID int, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
}

// UserEmojiStatusUpdatesService is the optional durable settings-update
// extension used by account.updateEmojiStatus. Keeping it separate preserves
// lightweight test/service implementations of the core UpdatesService.
type UserEmojiStatusUpdatesService interface {
	RecordUserEmojiStatus(ctx context.Context, stateAuthKeyID [8]byte, userID int64, status domain.UserEmojiStatus, excludeAuthKeyID [8]byte, excludeSessionID int64) (domain.UpdateEvent, domain.UpdateState, error)
}

// ContactsService abstracts contact-list queries.
type ContactsService interface {
	GetContacts(ctx context.Context, userID int64, hash int64) (domain.ContactList, bool, error)
	ContactIDs(ctx context.Context, userID int64, hash int64) ([]int, bool, error)
	AddContact(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error)
	AcceptContact(ctx context.Context, userID, contactUserID int64) (domain.Contact, error)
	ImportContacts(ctx context.Context, userID int64, inputs []domain.ContactInput) (domain.ImportContactsResult, error)
	Search(ctx context.Context, userID int64, query string, limit int) (domain.UserSearchResult, error)
	DeleteContacts(ctx context.Context, userID int64, contactUserIDs []int64) (int, error)
	EditCloseFriends(ctx context.Context, userID int64, contactUserIDs []int64) (domain.CloseFriendsEditResult, error)
	UpdateContactNote(ctx context.Context, userID, contactUserID int64, note string, entities []domain.MessageEntity) (domain.Contact, error)
	SetPersonalPhoto(ctx context.Context, userID, contactUserID int64, photo domain.Photo, date int) (domain.Contact, error)
	ClearPersonalPhoto(ctx context.Context, userID, contactUserID int64, date int) (domain.Contact, error)
	PersonalPhotos(ctx context.Context, userID int64, contactUserIDs []int64) (map[int64]domain.ProfilePhotoRef, error)
	GetPeerSettings(ctx context.Context, userID int64, peer domain.Peer) (domain.PeerSettings, error)
	MutateBlocklist(ctx context.Context, mutation store.BlocklistMutation, effects store.DeliveryEffectsBuilder[store.BlocklistMutationSnapshot]) (store.BlocklistMutationSnapshot, error)
	IsBlocked(ctx context.Context, userID, peerUserID int64) (bool, error)
	GetBlocked(ctx context.Context, userID int64, offset, limit int) (domain.BlockedContactList, error)
}

// DialogsService abstracts dialog list queries.
type DialogsService interface {
	GetDialogsHash(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogHashCheck, error)
	GetDialogs(ctx context.Context, userID int64, filter domain.DialogFilter) (domain.DialogList, error)
	GetPeerDialogs(ctx context.Context, userID int64, peers []domain.Peer) (domain.DialogList, error)
	SaveDraft(ctx context.Context, userID int64, draft domain.DialogDraft) (bool, error)
	GetDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (domain.DialogDraft, bool, error)
	DeleteDraft(ctx context.Context, userID int64, peer domain.Peer, topMessageID int) (bool, error)
	ListDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	ClearDrafts(ctx context.Context, userID int64, limit int) ([]domain.DialogDraft, error)
	TogglePinned(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, int, error)
	ToggleArchivePinned(ctx context.Context, userID int64, pinned bool) (bool, error)
	ReorderPinned(ctx context.Context, userID int64, folderID int, order []domain.Peer, force bool) (bool, error)
	MarkUnread(ctx context.Context, userID int64, peer domain.Peer, unread bool) (bool, error)
	UnreadMarks(ctx context.Context, userID int64) ([]domain.Peer, error)
	HidePeerSettingsBar(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	PeerSettingsBarHidden(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	GetDialogFolders(ctx context.Context, userID int64) (domain.DialogFolderList, error)
	SaveDialogFolder(ctx context.Context, userID int64, folder domain.DialogFolder) error
	DeleteDialogFolder(ctx context.Context, userID int64, folderID int) error
	ReorderDialogFolders(ctx context.Context, userID int64, order []int) error
	ToggleDialogFolderTags(ctx context.Context, userID int64, enabled bool) error
	EditPeerFolders(ctx context.Context, userID int64, peers []domain.FolderPeerUpdate) error
}

// ChatlistsService abstracts the Telegram shared folders / chatlists business.
type ChatlistsService interface {
	ExportInvite(ctx context.Context, userID int64, filterID int, title string, peers []domain.DialogFolderPeer, date int) (domain.DialogFolder, domain.ChatlistInvite, error)
	ListInvites(ctx context.Context, userID int64, filterID int) ([]domain.ChatlistInvite, error)
	EditInvite(ctx context.Context, userID int64, filterID int, slug string, title *string, peers *[]domain.DialogFolderPeer, revoke bool) (domain.ChatlistInvite, error)
	DeleteInvite(ctx context.Context, userID int64, filterID int, slug string) (domain.DialogFolder, bool, error)
	CheckInvite(ctx context.Context, userID int64, slug string) (domain.ChatlistInvitePreview, error)
	JoinInvite(ctx context.Context, userID int64, slug string, peers []domain.DialogFolderPeer, date int) (domain.ChatlistJoinResult, error)
	GetUpdates(ctx context.Context, userID int64, localFilterID int) (domain.ChatlistUpdates, error)
	JoinUpdates(ctx context.Context, userID int64, localFilterID int, peers []domain.DialogFolderPeer, date int) (domain.ChatlistJoinResult, error)
	HideUpdates(ctx context.Context, userID int64, localFilterID int) error
	Leave(ctx context.Context, userID int64, localFilterID int, peers []domain.DialogFolderPeer, date int) (domain.ChatlistLeaveResult, error)
	LeaveSuggestions(ctx context.Context, userID int64, localFilterID int) ([]domain.DialogFolderPeer, error)
}

// MessagesService abstracts message history, search and read state.
type MessagesService interface {
	SendPrivateText(ctx context.Context, userID int64, req domain.SendPrivateTextRequest) (domain.SendPrivateTextResult, error)
	SetChatTheme(ctx context.Context, userID int64, req domain.SetPrivateChatThemeRequest) (domain.SetPrivateChatThemeResult, error)
	ForwardPrivateMessages(ctx context.Context, userID int64, req domain.ForwardPrivateMessagesRequest) (domain.ForwardPrivateMessagesResult, error)
	GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error)
	GetHistory(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error)
	Search(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error)
	SearchPrivateMedia(ctx context.Context, userID, peerID int64, req domain.MediaSearchRequest) (domain.MessageList, error)
	CountPrivateMediaCategories(ctx context.Context, userID, peerID int64) (domain.MediaCategoryCounts, error)
	ReadHistory(ctx context.Context, userID int64, req domain.ReadHistoryRequest) (domain.ReadHistoryResult, error)
	ReadMessageContents(ctx context.Context, userID int64, req domain.ReadMessageContentsRequest) (domain.ReadMessageContentsResult, error)
	GetOutboxReadDate(ctx context.Context, userID int64, req domain.OutboxReadDateRequest) (int, error)
	SetMessageReactions(ctx context.Context, userID int64, req domain.SetPrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error)
	GetMessageReactions(ctx context.Context, userID int64, req domain.PrivateMessageReactionsRequest) (domain.PrivateMessageReactionsResult, error)
	SavedReactionTags(ctx context.Context, userID int64, savedPeer domain.Peer, limit int) ([]domain.SavedReactionTag, error)
	UpdateSavedReactionTag(ctx context.Context, userID int64, tag domain.SavedReactionTag) error
	VoteMessagePoll(ctx context.Context, userID int64, req domain.VotePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error)
	CloseMessagePoll(ctx context.Context, userID int64, req domain.ClosePrivateMessagePollRequest) (domain.PrivateMessagePollResult, error)
	ListUnreadReactionMessages(ctx context.Context, userID int64, peer domain.Peer, limit int) ([]domain.Message, error)
	ReadPeerReactions(ctx context.Context, userID int64, peer domain.Peer) (int, error)
	EditMessage(ctx context.Context, userID int64, req domain.EditMessageRequest) (domain.EditMessageResult, error)
	PinPrivateMessage(ctx context.Context, userID int64, req domain.PinPrivateMessageRequest) (domain.PinPrivateMessageResult, error)
	UnpinAllPrivateMessages(ctx context.Context, userID int64, req domain.UnpinAllPrivateMessagesRequest) (domain.PinPrivateMessageResult, error)
	DeleteMessages(ctx context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error)
	DeleteHistory(ctx context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error)
	GetSavedDialogs(ctx context.Context, userID int64, filter domain.SavedDialogsFilter) (domain.SavedDialogList, error)
	GetPinnedSavedDialogs(ctx context.Context, userID int64) (domain.SavedDialogList, error)
	GetSavedDialogsByPeers(ctx context.Context, userID int64, peers []domain.Peer) (domain.SavedDialogList, error)
	ToggleSavedDialogPin(ctx context.Context, userID int64, peer domain.Peer, pinned bool) (bool, error)
	ReorderPinnedSavedDialogs(ctx context.Context, userID int64, order []domain.Peer, force bool) error
	DeleteSavedHistory(ctx context.Context, userID int64, req domain.DeleteSavedHistoryRequest) (domain.DeleteSavedHistoryResult, error)
}

// PrivateNoForwardsService is an optional messages capability used by the
// private-user branch of messages.toggleNoForwards and userFull projection.
type PrivateNoForwardsService interface {
	GetPrivateNoForwards(ctx context.Context, userID, peerUserID int64) (domain.PrivateNoForwardsState, error)
	TogglePrivateNoForwards(ctx context.Context, userID int64, req domain.TogglePrivateNoForwardsRequest) (domain.TogglePrivateNoForwardsResult, error)
}

// TranslationService owns read-only translation and the durable per-account
// peer preference. It only exposes domain values to the RPC edge.
type TranslationService interface {
	Translate(ctx context.Context, req domain.TranslationRequest) (domain.TranslationResult, error)
	SetPeerDisabled(ctx context.Context, userID int64, peer domain.Peer, disabled bool) (bool, error)
	PeerDisabled(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
}

// AlbumGroupService is an optional, production-required MessagesService capability:
// sendMultiMedia durably reserves the grouped_id for the whole batch of random_ids
// before parsing any media or storing the first message. Defining it separately
// avoids forcing lightweight test doubles that never trigger sendMultiMedia to
// implement unrelated methods.
type AlbumGroupService interface {
	ReserveAlbumGroup(ctx context.Context, userID int64, req domain.AlbumGroupReservationRequest) (int64, error)
}

// StoriesService abstracts story read, read-state, viewing and reaction state.
type StoriesService interface {
	CreateStory(ctx context.Context, userID int64, req domain.StoryCreateRequest) (domain.StoryCreateResult, error)
	GetAllStories(ctx context.Context, viewerUserID int64, hidden bool, now, limit int) (domain.StoryList, error)
	GetAllStoriesPage(ctx context.Context, viewerUserID int64, hidden bool, now int, cursor domain.StoryListCursor, limit int) (domain.StoryList, error)
	GetAllStoriesDigest(ctx context.Context, viewerUserID int64, hidden bool, now int) (domain.StoryListDigest, error)
	ListOwnerActiveStories(ctx context.Context, userID int64, owner domain.Peer, now, limit int) (domain.StoryList, error)
	GetPeerStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (domain.PeerStories, error)
	GetStoriesByID(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, now int) (domain.StoryList, error)
	GetStoriesArchive(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error)
	GetPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, offsetID, limit, now int) (domain.StoryList, error)
	HasPinnedStories(ctx context.Context, viewerUserID int64, peer domain.Peer, now int) (bool, error)
	ListReadStates(ctx context.Context, viewerUserID int64) ([]domain.StoryReadState, error)
	GetPeerMaxIDs(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.RecentStory, error)
	GetPeerHiddenStates(ctx context.Context, viewerUserID int64, peers []domain.Peer) (map[domain.Peer]bool, error)
	GetPeerStoryProjections(ctx context.Context, viewerUserID int64, peers []domain.Peer, now int) ([]domain.PeerStoryProjection, error)
	ReadStories(ctx context.Context, viewerUserID int64, peer domain.Peer, maxID, date int) (domain.StoryReadResult, error)
	IncrementViews(ctx context.Context, viewerUserID int64, peer domain.Peer, ids []int, date int) (int, error)
	SendReaction(ctx context.Context, viewerUserID int64, peer domain.Peer, storyID int, reaction *domain.MessageReaction, date int) (domain.StoryReactionResult, error)
	GetStoryViewsList(ctx context.Context, viewerUserID int64, req domain.StoryViewListRequest) (domain.StoryViewList, error)
	GetStoryReactionsList(ctx context.Context, viewerUserID int64, req domain.StoryReactionListRequest) (domain.StoryReactionList, error)
	GetStoryPublicForwards(ctx context.Context, viewerUserID int64, req domain.StoryPublicForwardListRequest) (domain.StoryPublicForwardList, error)
	CanViewStoryStats(ctx context.Context, userID int64, peer domain.Peer) error
	ListStoryViewerIDs(ctx context.Context, userID int64, owner domain.Peer, storyID, limit int) ([]int64, error)
	EditStory(ctx context.Context, userID int64, req domain.StoryEditRequest) (domain.StoryEditResult, error)
	DeleteStories(ctx context.Context, userID int64, peer domain.Peer, ids []int, date int) (domain.StoryMutationResult, error)
	TogglePinned(ctx context.Context, userID int64, peer domain.Peer, ids []int, pinned bool, date int) (domain.StoryMutationResult, error)
	TogglePinnedToTop(ctx context.Context, userID int64, peer domain.Peer, ids []int) error
	TogglePeerStoriesHidden(ctx context.Context, viewerUserID int64, peer domain.Peer, hidden bool) error
	CanSendStory(ctx context.Context, viewerUserID int64, peer domain.Peer) (int, error)
}

// ChannelsService abstracts supergroup/channel business.
type ChannelsService interface {
	CreateMegagroupFromCreateChat(ctx context.Context, userID int64, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	CreateChannel(ctx context.Context, userID int64, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	GetChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	// ResolveChannel is the lightweight version of GetChannel (access check only +
	// Channel/Self, skipping dialog/boost), for resolution paths like inputPeerFor
	// that only need access_hash / channel flags.
	ResolveChannel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	GetChannels(ctx context.Context, userID int64, channelIDs []int64) ([]domain.ChannelView, error)
	GetJoinableChannel(ctx context.Context, userID, channelID int64) (domain.Channel, error)
	GetParticipants(ctx context.Context, userID, channelID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.ChannelParticipantList, error)
	GetParticipant(ctx context.Context, userID, channelID, participantUserID int64) (domain.ChannelMember, error)
	FutureCreatorAfterLeave(ctx context.Context, userID, channelID int64) (domain.ChannelMember, error)
	InviteToChannel(ctx context.Context, userID, channelID int64, userIDs []int64, date int) (domain.CreateChannelResult, error)
	JoinChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
	LeaveChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
	EditTitle(ctx context.Context, userID int64, req domain.EditChannelTitleRequest) (domain.EditChannelTitleResult, error)
	// EditFactCheck writes/deletes a message's Layer 231 fact-check label; empty text means delete.
	EditFactCheck(ctx context.Context, userID int64, req domain.EditFactCheckRequest) (domain.EditFactCheckResult, error)
	// GetFactChecks batch-reads messages' fact-check labels.
	GetFactChecks(ctx context.Context, userID, channelID int64, messageIDs []int) (domain.FactCheckList, error)
	SetWallpaper(ctx context.Context, userID int64, req domain.SetChannelWallpaperRequest) (domain.SetChannelWallpaperResult, error)
	EditAbout(ctx context.Context, userID int64, req domain.EditChannelAboutRequest) (domain.Channel, error)
	EditAdmin(ctx context.Context, userID int64, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error)
	TransferOwnership(ctx context.Context, userID int64, req domain.TransferChannelOwnershipRequest) (domain.TransferChannelOwnershipResult, error)
	EditMemberRank(ctx context.Context, userID int64, req domain.EditChannelMemberRankRequest) (domain.EditChannelAdminResult, error)
	EditBanned(ctx context.Context, userID int64, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error)
	EditDefaultBannedRights(ctx context.Context, userID int64, req domain.EditChannelDefaultBannedRightsRequest) (domain.Channel, error)
	DeleteChannel(ctx context.Context, userID int64, req domain.DeleteChannelRequest) (domain.DeleteChannelResult, error)
	CheckUsername(ctx context.Context, userID, channelID int64, username string) (bool, error)
	UpdateUsername(ctx context.Context, userID int64, req domain.UpdateChannelUsernameRequest) (domain.Channel, error)
	ListAdminedPublicChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListCommunityLinkableChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListStoryPostableChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ListSendAsChannels(ctx context.Context, userID int64) ([]domain.Channel, error)
	ResolvePublicUsername(ctx context.Context, userID int64, username string) (domain.Channel, bool, error)
	SearchPublicChannels(ctx context.Context, userID int64, query string, limit int) (domain.PublicChannelSearchResult, error)
	SetSignatures(ctx context.Context, userID, channelID int64, enabled, profiles bool) (domain.Channel, error)
	SetPhoto(ctx context.Context, userID, channelID int64, photo *domain.Photo, date int) (domain.SetChannelPhotoResult, error)
	SetPreHistoryHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetParticipantsHidden(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetForum(ctx context.Context, userID, channelID int64, enabled, tabs bool) (domain.Channel, error)
	SetAutotranslation(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetRestrictedSponsored(ctx context.Context, userID, channelID int64, restricted bool) (domain.Channel, error)
	SetPaidMessagesPrice(ctx context.Context, userID, channelID int64, stars int64, broadcastMessagesAllowed bool) (domain.ChannelPaidMessagesPriceResult, error)
	SetAntiSpam(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetSlowMode(ctx context.Context, userID, channelID int64, seconds int) (domain.Channel, error)
	SetBoostsToUnblockRestrictions(ctx context.Context, userID, channelID int64, boosts int) (domain.Channel, error)
	SetNoForwards(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetJoinToSend(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetJoinRequest(ctx context.Context, userID, channelID int64, enabled bool) (domain.Channel, error)
	SetAvailableReactions(ctx context.Context, userID, channelID int64, policy domain.ChannelReactionPolicy) (domain.Channel, error)
	SetColor(ctx context.Context, userID, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error)
	SetEmojiStatus(ctx context.Context, userID, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error)
	ListAdminLog(ctx context.Context, userID int64, req domain.ChannelAdminLogRequest) (domain.ChannelAdminLogResult, error)
	GetChannelForChangeInfo(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
	SaveDefaultSendAs(ctx context.Context, userID int64, req domain.SaveChannelDefaultSendAsRequest) (domain.ChannelView, error)
	GetMessageViews(ctx context.Context, userID int64, req domain.ChannelMessageViewsRequest) (domain.ChannelMessageViewsResult, error)
	SetMessageReactions(ctx context.Context, userID int64, req domain.SetChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error)
	GetMessageReactions(ctx context.Context, userID int64, req domain.ChannelMessageReactionsRequest) (domain.ChannelMessageReactionsResult, error)
	VoteMessagePoll(ctx context.Context, userID int64, req domain.VoteChannelMessagePollRequest) (domain.ChannelMessagePollResult, error)
	CloseMessagePoll(ctx context.Context, userID int64, req domain.CloseChannelMessagePollRequest) (domain.ChannelMessagePollResult, error)
	ListMessageReactions(ctx context.Context, userID int64, req domain.ChannelMessageReactionsListRequest) (domain.ChannelMessageReactionsList, error)
	FindMessageReaction(ctx context.Context, userID int64, req domain.ChannelMessageReactionLookupRequest) (domain.ChannelMessageReactionLookup, bool, error)
	TopReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error)
	RecentReactions(ctx context.Context, userID int64, limit int) ([]domain.MessageReaction, error)
	ClearRecentReactions(ctx context.Context, userID int64) error
	GetPremiumBoostStatus(ctx context.Context, userID, channelID int64, now int) (domain.PremiumBoostStatus, error)
	ListPremiumBoosts(ctx context.Context, userID, channelID int64, gifts bool, offset string, limit, now int) (domain.PremiumBoostList, error)
	GetPremiumMyBoosts(ctx context.Context, userID int64, now, premiumUntil int) (domain.PremiumMyBoosts, error)
	ApplyPremiumBoost(ctx context.Context, userID, channelID int64, slots []int, now, premiumUntil int) (domain.PremiumMyBoosts, error)
	GetPremiumUserBoosts(ctx context.Context, userID, channelID, targetUserID int64, now int) (domain.PremiumBoostList, error)
	ReadMessageContents(ctx context.Context, userID int64, req domain.ReadChannelMessageContentsRequest) (domain.ReadChannelMessageContentsResult, error)
	GetMessageAuthor(ctx context.Context, userID int64, req domain.GetChannelMessageAuthorRequest) (domain.GetChannelMessageAuthorResult, error)
	CreateForumTopic(ctx context.Context, userID int64, req domain.CreateChannelForumTopicRequest) (domain.CreateChannelForumTopicResult, error)
	EditForumTopic(ctx context.Context, userID int64, req domain.EditChannelForumTopicRequest) (domain.EditChannelForumTopicResult, error)
	UpdatePinnedForumTopic(ctx context.Context, userID int64, req domain.UpdateChannelForumTopicPinnedRequest) (domain.UpdateChannelForumTopicPinnedResult, error)
	ReorderPinnedForumTopics(ctx context.Context, userID int64, req domain.ReorderChannelPinnedForumTopicsRequest) (domain.ReorderChannelPinnedForumTopicsResult, error)
	DeleteForumTopicHistory(ctx context.Context, userID int64, req domain.DeleteChannelForumTopicHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	GetForumTopics(ctx context.Context, userID int64, filter domain.ChannelForumTopicFilter) (domain.ChannelForumTopicList, error)
	GetForumTopicsByID(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelForumTopicList, error)
	SendMessage(ctx context.Context, userID int64, req domain.SendChannelMessageRequest) (domain.SendChannelMessageResult, error)
	EditMessage(ctx context.Context, userID int64, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error)
	GetInlineBotMessage(ctx context.Context, botID, channelID int64, id int) (domain.Channel, domain.ChannelMessage, bool, error)
	ListStoryMessageForwards(ctx context.Context, userID int64, req domain.StoryMessageForwardListRequest) (domain.StoryMessageForwardList, error)
	EditInlineBotMessage(ctx context.Context, botID int64, req domain.EditChannelMessageRequest) (domain.EditChannelMessageResult, error)
	DeleteMessages(ctx context.Context, userID int64, req domain.DeleteChannelMessagesRequest) (domain.DeleteChannelMessagesResult, error)
	DeleteHistory(ctx context.Context, userID int64, req domain.DeleteChannelHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	DeleteParticipantHistory(ctx context.Context, userID int64, req domain.DeleteChannelParticipantHistoryRequest) (domain.DeleteChannelHistoryResult, error)
	UpdatePinnedMessage(ctx context.Context, userID int64, req domain.UpdateChannelPinnedMessageRequest) (domain.UpdateChannelPinnedMessageResult, error)
	UnpinAllMessages(ctx context.Context, userID int64, req domain.UnpinAllChannelMessagesRequest) (domain.UpdateChannelPinnedMessageResult, error)
	ClearDanglingPinnedMessage(ctx context.Context, channelID int64, messageID int) error
	ExportInvite(ctx context.Context, userID int64, req domain.ExportChannelInviteRequest) (domain.ExportChannelInviteResult, error)
	CheckInvite(ctx context.Context, userID int64, hash string, date int) (domain.CheckChannelInviteResult, error)
	ImportInvite(ctx context.Context, userID int64, req domain.ImportChannelInviteRequest) (domain.CreateChannelResult, error)
	ListExportedInvites(ctx context.Context, userID int64, req domain.ChannelInviteListRequest) (domain.ChannelInviteList, error)
	GetExportedInvite(ctx context.Context, userID int64, req domain.GetChannelInviteRequest) (domain.ChannelInvite, error)
	EditExportedInvite(ctx context.Context, userID int64, req domain.EditChannelInviteRequest) (domain.EditChannelInviteResult, error)
	DeleteExportedInvite(ctx context.Context, userID int64, req domain.DeleteChannelInviteRequest) error
	DeleteRevokedExportedInvites(ctx context.Context, userID int64, req domain.DeleteRevokedChannelInvitesRequest) error
	ListAdminsWithInvites(ctx context.Context, userID, channelID int64) ([]domain.ChannelAdminInviteCount, error)
	ListInviteImporters(ctx context.Context, userID int64, req domain.ChannelInviteImportersRequest) (domain.ChannelInviteImporterList, error)
	PendingJoinRequests(ctx context.Context, channelID int64, limit int) (domain.ChannelPendingJoinRequests, error)
	HideChatJoinRequest(ctx context.Context, userID int64, req domain.HideChannelJoinRequestRequest) (domain.CreateChannelResult, error)
	HideAllChatJoinRequests(ctx context.Context, userID int64, req domain.HideChannelJoinRequestsRequest) (domain.CreateChannelResult, error)
	CommonChannels(ctx context.Context, userID int64, req domain.CommonChannelsRequest) (domain.CommonChannelsResult, error)
	LeftChannels(ctx context.Context, userID int64, offset, limit int) (domain.LeftChannelsResult, error)
	InactiveChannels(ctx context.Context, userID int64, limit int) (domain.ChannelDialogList, error)
	ChannelRecommendations(ctx context.Context, userID int64, req domain.ChannelRecommendationsRequest) (domain.ChannelRecommendationsResult, error)
	DiscussionGroups(ctx context.Context, userID int64, limit int) ([]domain.Channel, error)
	SetDiscussionGroup(ctx context.Context, userID, broadcastID, groupID int64) (domain.DiscussionGroupUpdateResult, error)
	SetViewForumAsMessages(ctx context.Context, userID, channelID int64, enabled bool) (bool, error)
	GetHistory(ctx context.Context, userID int64, filter domain.ChannelHistoryFilter) (domain.ChannelHistory, error)
	SearchChannelMedia(ctx context.Context, userID, channelID int64, req domain.MediaSearchRequest) (domain.ChannelHistory, error)
	CountChannelMediaCategories(ctx context.Context, userID, channelID int64) (domain.MediaCategoryCounts, error)
	GetStats(ctx context.Context, userID int64, req domain.ChannelStatsRequest) (domain.ChannelStats, error)
	GetMessageStats(ctx context.Context, userID int64, req domain.ChannelMessageStatsRequest) (domain.ChannelMessageStats, error)
	ListMessagePublicForwards(ctx context.Context, userID int64, req domain.ChannelMessagePublicForwardListRequest) (domain.ChannelMessagePublicForwardList, error)
	SearchPosts(ctx context.Context, userID int64, req domain.ChannelSearchPostsRequest) (domain.ChannelHistory, error)
	SearchJoinedMessages(ctx context.Context, userID int64, req domain.ChannelGlobalSearchRequest) (domain.ChannelHistory, error)
	GetMessages(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelHistory, error)
	ChannelPollFanoutViews(ctx context.Context, channelID int64, msgID int, viewers []int64, now int) (map[int64]*domain.MessagePoll, error)
	GetReplies(ctx context.Context, userID int64, filter domain.ChannelRepliesFilter) (domain.ChannelHistory, error)
	GetUnreadMentions(ctx context.Context, userID int64, filter domain.ChannelUnreadMentionsFilter) (domain.ChannelHistory, error)
	ReadMentions(ctx context.Context, userID int64, req domain.ReadChannelMentionsRequest) (domain.ReadChannelMentionsResult, error)
	GetUnreadReactions(ctx context.Context, userID int64, filter domain.ChannelUnreadReactionsFilter) (domain.ChannelHistory, error)
	ReadReactions(ctx context.Context, userID int64, req domain.ReadChannelReactionsRequest) (domain.ReadChannelReactionsResult, error)
	GetDiscussionMessage(ctx context.Context, userID, channelID int64, msgID int) (domain.ChannelDiscussionMessage, error)
	SendMonoforumMessage(ctx context.Context, req domain.SendMonoforumMessageRequest) (domain.SendChannelMessageResult, error)
	ListMonoforumHistory(ctx context.Context, filter domain.MonoforumHistoryFilter) (domain.ChannelHistory, error)
	ListMonoforumDialogs(ctx context.Context, filter domain.MonoforumDialogsFilter) (domain.MonoforumDialogList, error)
	ResolveMonoforumSend(ctx context.Context, viewerUserID, monoforumID int64) (domain.Channel, bool, error)
	ReadHistory(ctx context.Context, userID int64, req domain.ReadChannelHistoryRequest) (domain.ReadChannelHistoryResult, error)
	ReadTopicHistory(ctx context.Context, userID int64, req domain.ReadChannelTopicHistoryRequest) (domain.ReadChannelTopicHistoryResult, error)
	GeneralForumTopic(ctx context.Context, userID, channelID int64) (domain.ChannelForumTopic, error)
	GetMessageReadParticipants(ctx context.Context, userID int64, req domain.ChannelReadParticipantsRequest) (domain.ChannelReadParticipantsResult, error)
	GetDifference(ctx context.Context, userID int64, req domain.ChannelDifferenceRequest) (domain.ChannelDifference, error)
	ActiveChannelIDsForUser(ctx context.Context, userID, afterChannelID int64, limit int) ([]int64, error)
	DirtyActiveChannelsForUser(ctx context.Context, userID int64, sinceDate int, afterChannelID int64, limit int) ([]domain.DirtyChannel, error)
	ActiveMemberIDs(ctx context.Context, userID, channelID int64, limit int) ([]int64, error)
	// SetActiveCall / AppendCallServiceMessage are the channel-side attachment points of the group call module.
	SetActiveCall(ctx context.Context, channelID, callID, callAccessHash int64, notEmpty bool) (domain.Channel, error)
	AppendCallServiceMessage(ctx context.Context, channelID, senderUserID int64, date int, action domain.ChannelMessageAction) (domain.SendChannelMessageResult, error)
	AppendStarGiftAdminLog(ctx context.Context, channelID, senderUserID int64, savedID int64, date int, action domain.ChannelMessageAction) error
	InviteAdminMemberIDs(ctx context.Context, channelID int64, limit int) ([]int64, error)
	FilterActiveMemberIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error)
}

// ChannelMessageAudienceService is the optional production authorization
// boundary for public short-poll subscribers. Lightweight test/domain adapters
// that only model joined members may omit it and retain member-only behavior.
type ChannelMessageAudienceService interface {
	FilterMessageAudienceIDs(ctx context.Context, channelID int64, userIDs []int64) ([]int64, error)
}

// ChannelAuthoritativeProjectionService bypasses long-lived channel read
// models for a durable channel_state refresh emitted by an admin mutation.
type ChannelAuthoritativeProjectionService interface {
	GetChannelsAuthoritative(ctx context.Context, userID int64, channelIDs []int64) ([]domain.ChannelView, error)
}

// CommunitiesService abstracts the Layer 228 Community aggregation domain.
// Community containers never expose tg types and never own message/read/pts state.
type CommunitiesService interface {
	Create(ctx context.Context, userID int64, req domain.CreateCommunityRequest) (domain.CommunityView, error)
	Get(ctx context.Context, userID, communityID int64) (domain.CommunityView, error)
	GetMany(ctx context.Context, userID int64, ids []int64) ([]domain.CommunityView, error)
	ListJoined(ctx context.Context, userID int64) ([]domain.CommunityView, error)
	TogglePeerLink(ctx context.Context, userID int64, req domain.CommunityTogglePeerLinkRequest) (domain.CommunityTogglePeerLinkResult, error)
	SetCollapsed(ctx context.Context, userID, communityID int64, collapsed bool) (domain.CommunityView, bool, error)
	ListPeerLinkRequests(ctx context.Context, userID, communityID int64, offset string, limit int) (domain.CommunityPeerLinkRequestPage, error)
	DecidePeerLinkRequest(ctx context.Context, userID, communityID int64, peer domain.Peer, reject bool, date int) (domain.CommunityTogglePeerLinkResult, error)
	DecideAllPeerLinkRequests(ctx context.Context, userID, communityID int64, reject bool, date int) ([]domain.CommunityTogglePeerLinkResult, error)
	ToggleParticipantBanned(ctx context.Context, userID, communityID, participantUserID int64, unban bool, date int) (domain.CommunityParticipantBanResult, error)
	ParticipantJoinedChats(ctx context.Context, userID, communityID, participantUserID int64) (domain.CommunityParticipantJoinedChats, error)
	Participants(ctx context.Context, userID, communityID int64, filter domain.ChannelParticipantsFilter, offset, limit int) (domain.CommunityParticipantList, error)
	EditTitle(ctx context.Context, userID, communityID int64, title string) (domain.CommunityView, bool, error)
	EditAbout(ctx context.Context, userID, communityID int64, about string) (domain.CommunityView, bool, error)
	EditAdmin(ctx context.Context, userID int64, req domain.CommunityEditAdminRequest) (domain.CommunityView, bool, error)
	EditDefaultBannedRights(ctx context.Context, userID, communityID int64, rights domain.ChannelBannedRights) (domain.CommunityView, bool, error)
	SetPhoto(ctx context.Context, userID, communityID int64, photo *domain.Photo, date int) (domain.CommunityView, bool, error)
	Delete(ctx context.Context, userID, communityID int64, date int) (domain.CommunityView, []domain.Peer, error)
	SetPinned(ctx context.Context, userID, communityID int64, pinned bool) (bool, error)
	ReorderPinned(ctx context.Context, userID int64, order []domain.Peer, force bool) (bool, error)
	SearchScope(ctx context.Context, userID, communityID int64) (domain.CommunitySearchScope, error)
}

// FilesService abstracts file upload parts, download and media (document/photo)
// assembly. Methods use only domain types; the rpc layer converts
// tg.InputFileLocation / InputMedia <-> domain.
type FilesService interface {
	SaveFilePart(ctx context.Context, ownerUserID, fileID int64, part int, bytes []byte) (bool, error)
	SaveBigFilePart(ctx context.Context, ownerUserID, fileID int64, part, totalParts int, bytes []byte) (bool, error)
	GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error)
	// CreateEncryptedFileFromUpload assembles secret chat upload parts into a blind blob and mints an EncryptedFile snapshot (P2).
	CreateEncryptedFileFromUpload(ctx context.Context, file domain.UploadedFileRef, keyFingerprint int) (domain.EncryptedFileRef, error)
	// GeoMapTile renders the geo message map thumbnail placeholder (upload.getWebFile), deterministic and with no external dependency.
	GeoMapTile(lat, long float64, w, h, zoom, scale int) ([]byte, string)
	// Resource reads (reaction / sticker / document).
	ListAvailableReactions(ctx context.Context) ([]domain.AvailableReaction, error)
	AvailableEffects(ctx context.Context) ([]domain.AvailableEffect, int, error)
	GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error)
	ResolveStickerSet(ctx context.Context, ref domain.StickerSetRef) (set domain.StickerSet, documents []domain.Document, found bool, err error)
	ListStickerSets(ctx context.Context, kind domain.StickerSetKind) ([]domain.StickerSet, error)
	CheckStickerSetShortName(ctx context.Context, shortName string) (bool, error)
	SuggestStickerSetShortName(ctx context.Context, title string, userID int64) (string, error)
	CreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error)
	ListCreatedStickerSets(ctx context.Context, userID int64, offsetID int64, limit int) ([]domain.StickerSet, int, error)
	AddStickerToSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error)
	RemoveStickerFromSet(ctx context.Context, actorUserID int64, documentID int64, accessHash int64) (domain.StickerSet, []domain.Document, error)
	ChangeStickerPosition(ctx context.Context, actorUserID int64, documentID int64, accessHash int64, position int) (domain.StickerSet, []domain.Document, error)
	RenameStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef, title string) (domain.StickerSet, []domain.Document, error)
	DeleteStickerSet(ctx context.Context, actorUserID int64, ref domain.StickerSetRef) (domain.StickerSetKind, error)
	// Avatar (profile photo) and message media assembly.
	CreatePhotoFromUpload(ctx context.Context, file domain.UploadedFileRef) (domain.Photo, error)
	CreatePhotoFromBytes(ctx context.Context, data []byte) (domain.Photo, error)
	// CreatePhotoFromURL / CreateDocumentFromURL fetch external-link media
	// (inputMediaPhoto/DocumentExternal), SSRF-safe; returns ErrExternalMediaDisabled
	// when not enabled.
	CreatePhotoFromURL(ctx context.Context, rawURL string) (domain.Photo, error)
	CreateDocumentFromURL(ctx context.Context, rawURL string) (domain.Document, error)
	// ResolveWebPage resolves a link preview (messages.getWebPagePreview / sending
	// with an attached card); SSRF-safe, deduped through the L1+L3 cache. Returns
	// ErrWebPagePreviewDisabled when not enabled; a transient failure returns an error
	// (the caller degrades).
	ResolveWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, error)
	// WebPagePreviewEnabled reports whether link preview is enabled; when not enabled, sending attaches no pending placeholder (otherwise it would stay pending forever).
	WebPagePreviewEnabled() bool
	// LookupWebPage only checks the cache (no fetch) and returns the resolved link
	// preview. Used when sending: if getWebPagePreview already resolved it when the
	// client typed, the echo carries a done card directly (matching official), not
	// relying on an async card swap.
	LookupWebPage(ctx context.Context, rawURL string) (domain.MessageWebPage, bool)
	CreateAvatarFromUpload(ctx context.Context, file domain.UploadedFileRef) (domain.Photo, error)
	CreateAvatarVideoFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64) (domain.Photo, error)
	CreateAvatarVideoMarkupFromUpload(ctx context.Context, file domain.UploadedFileRef, videoStartTs float64, markup domain.PhotoSize) (domain.Photo, error)
	CreateAvatarMarkup(ctx context.Context, size domain.PhotoSize) (domain.Photo, error)
	CreateDocumentFromUpload(ctx context.Context, file domain.UploadedFileRef, spec domain.DocumentSpec) (domain.Document, error)
	CreateDocumentFromBytes(ctx context.Context, data []byte, spec domain.DocumentSpec) (domain.Document, error)
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)
	GetDocument(ctx context.Context, id int64) (domain.Document, bool, error)
	UploadProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64, file domain.UploadedFileRef, date int) (domain.Photo, error)
	UploadProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, file domain.UploadedFileRef, date int) (domain.Photo, error)
	SetCurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID, photoID int64, date int) (domain.Photo, bool, error)
	SetCurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) (domain.Photo, bool, error)
	CurrentProfilePhoto(ctx context.Context, ownerType domain.PeerType, ownerID int64) (domain.Photo, bool, error)
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error)
	GetProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, offset, limit int, maxID int64) (photos []domain.Photo, total int, err error)
	GetProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, offset, limit int, maxID int64) (photos []domain.Photo, total int, err error)
	DeleteProfilePhotos(ctx context.Context, ownerType domain.PeerType, ownerID int64, photoIDs []int64) (int, error)
	DeleteProfilePhotosKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoIDs []int64) (int, error)
}

// LangPackService abstracts client language pack queries.
type LangPackService interface {
	GetLangPack(ctx context.Context, langPack, langCode string) (domain.LangPack, error)
	GetDifference(ctx context.Context, langPack, langCode string, fromVersion int) (domain.LangPack, error)
	GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error)
	ListLanguages(ctx context.Context, langPack string) ([]domain.LangPackLanguage, error)
}

// AIComposeService abstracts the client input-box AI rewrite/polish and the
// aicompose tones catalog. Only domain DTOs are used here; the rpc layer converts
// tg.TextWithEntities/InputAiComposeTone <-> domain.
type AIComposeService interface {
	ListTones(ctx context.Context, userID, hash int64) (domain.AIComposeTones, bool, error)
	GetTone(ctx context.Context, userID int64, ref domain.AIComposeToneRef) (domain.AIComposeTones, error)
	CreateTone(ctx context.Context, input domain.AIComposeToneInput) (domain.AIComposeTone, error)
	UpdateTone(ctx context.Context, update domain.AIComposeToneUpdate) (domain.AIComposeTone, error)
	SaveTone(ctx context.Context, userID int64, ref domain.AIComposeToneRef, unsave bool) error
	DeleteTone(ctx context.Context, userID int64, ref domain.AIComposeToneRef) error
	GetToneExample(ctx context.Context, userID int64, ref domain.AIComposeToneRef, num int) (domain.AIComposeToneExample, error)
	Compose(ctx context.Context, req domain.AIComposeRequest) (domain.AIComposeResult, error)
}

// EphemeralService owns Layer 228 short-lived bot/member state. It must never
// write ordinary messages, dialogs, pts/qts/seq logs or durable update outbox.
type EphemeralService interface {
	SendFromClient(ctx context.Context, request domain.SendClientEphemeralRequest) (domain.EphemeralMessage, bool, error)
	SendFromBot(ctx context.Context, request domain.SendBotEphemeralRequest) (domain.EphemeralMessage, bool, error)
	SendFromBotLazy(ctx context.Context, request domain.SendBotEphemeralRequest, build func(context.Context) (domain.EphemeralContent, error)) (domain.EphemeralMessage, bool, error)
	EditFromBot(ctx context.Context, botUserID int64, peer domain.Peer, id int, content domain.EphemeralContent) (domain.EphemeralMessage, error)
	EditFieldsFromBot(ctx context.Context, botUserID, receiverUserID int64, peer domain.Peer, id int, mode domain.EphemeralEditMode, fields domain.EditEphemeralFields) (domain.EphemeralMessage, error)
	EditFieldsFromBotLazy(ctx context.Context, botUserID, receiverUserID int64, peer domain.Peer, id int, mode domain.EphemeralEditMode, build func(context.Context) (domain.EditEphemeralFields, error)) (domain.EphemeralMessage, error)
	Delete(ctx context.Context, actorUserID, receiverUserID int64, peer domain.Peer, id int) (domain.EphemeralMessage, bool, error)
	DeleteFromDevice(ctx context.Context, actorUserID, receiverUserID int64, device domain.EphemeralDevice, peer domain.Peer, id int) (domain.EphemeralMessage, bool, error)
	Callback(ctx context.Context, userID int64, device domain.EphemeralDevice, peer domain.Peer, id int, data []byte) (domain.EphemeralCallback, error)
	PutCallbackAction(ctx context.Context, action domain.EphemeralCallbackAction) (bool, error)
	ReportTarget(ctx context.Context, userID int64, device domain.EphemeralDevice, peer domain.Peer, id int) (domain.EphemeralMessage, error)
}

// TranscriptionService hands a voice document to the local ASR backend for
// transcription (app/transcription): a cache hit returns text directly, otherwise a
// pending task is registered; on completion the callback injected by
// SetCompletionHandler pushes updateTranscribedAudio. When nil or the backend is not
// configured, the caller returns TRANSCRIPTION_FAILED.
type TranscriptionService interface {
	Transcribe(ctx context.Context, userID int64, peer domain.Peer, msgID int, document domain.Document) (domain.TranscriptionResult, error)
	SetCompletionHandler(handler func(ctx context.Context, userID int64, peer domain.Peer, msgID int, transcriptionID int64, text string))
}

// WelcomeMessageService owns the independent durable Layer 229 peer templates.
// It has no transient device, PTS, difference, push or outbox responsibility.
type WelcomeMessageService interface {
	Authorize(ctx context.Context, userID int64, peer domain.Peer) error
	Create(ctx context.Context, userID int64, peer domain.Peer, randomID int64, content domain.WelcomeMessageContent) (domain.WelcomeMessage, bool, error)
	Edit(ctx context.Context, userID int64, peer domain.Peer, id int, fields domain.WelcomeMessageEditFields) (domain.WelcomeMessage, error)
	List(ctx context.Context, userID int64, peer domain.Peer, hash int64) (domain.WelcomeMessageList, error)
	Delete(ctx context.Context, userID int64, peer domain.Peer, id int) (bool, error)
	DeleteAll(ctx context.Context, userID int64, peer domain.Peer) (bool, error)
	HasAny(ctx context.Context, peer domain.Peer) (bool, error)
}

// ModerationService accepts only final report choices. Implementations must
// validate and snapshot referenced evidence, then durably commit the immutable
// submission before returning success.
type ModerationService interface {
	ReportPeer(ctx context.Context, reporterUserID int64, source domain.ModerationReportSource, target domain.Peer, reason domain.ModerationReason, option, comment string, createdAt time.Time) (domain.ModerationReport, bool, error)
	ReportMessages(ctx context.Context, req domain.ModerationMessageReportRequest) (domain.ModerationReport, bool, error)
	ReportProfilePhoto(ctx context.Context, req domain.ModerationProfilePhotoReportRequest) (domain.ModerationReport, bool, error)
	ReportChannelSpam(ctx context.Context, req domain.ModerationChannelSpamReportRequest) (domain.ModerationReport, bool, error)
	ReportReaction(ctx context.Context, req domain.ModerationReactionReportRequest) (domain.ModerationReport, bool, error)
	ReportEncryptedSpam(ctx context.Context, reporterUserID int64, chat domain.SecretChat, createdAt time.Time) (domain.ModerationReport, bool, error)
	ReportStories(ctx context.Context, req domain.ModerationStoryReportRequest) (domain.ModerationReport, bool, error)
	ReportEphemeral(ctx context.Context, reporterUserID int64, target domain.EphemeralMessage, reason domain.ModerationReason, option, comment string, createdAt time.Time) (domain.ModerationReport, bool, error)
	SponsoredImpression(ctx context.Context, userID int64, randomID []byte, now time.Time) (domain.SponsoredMessageImpression, error)
	ReportSponsored(ctx context.Context, userID int64, randomID []byte, reason domain.ModerationReason, option string, now time.Time) (domain.ModerationReport, bool, error)
	ReportAntiSpamFalsePositive(ctx context.Context, reporterUserID, channelID int64, messageID int, now time.Time) (domain.ModerationReport, bool, error)
}

// PremiumPromoService exposes the immutable promo media catalog through a
// domain-only boundary. File bytes remain served by upload.getFile through the
// ordinary Files service.
type PremiumPromoService interface {
	PremiumPromo(ctx context.Context) (domain.PremiumPromoCatalog, bool, error)
}

// UsernameRegistryService is the collectible (Fragment-style) username registry
// boundary. It owns the full per-peer username list -- the editable slot the
// client owns through account/channels.updateUsername plus every collectible
// asset attached to the peer -- and the purchase record behind a collectible.
//
// The registry is deliberately optional. Every RPC surface that consults it must
// degrade to the legacy single-editable-username behaviour when the field is nil
// or a call fails, because the scalar users.username / channels.username column
// remains the editable-name persistence slot.
type UsernameRegistryService interface {
	// PeerUsernames returns one peer's full username list. Order is irrelevant:
	// callers project through domain.SortUsernames.
	PeerUsernames(ctx context.Context, peer domain.Peer) ([]domain.Username, error)
	// UsernamesBatch is the N+1-free variant used by list projections. Peers with
	// no registry row may be omitted from the result map.
	UsernamesBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer][]domain.Username, error)
	// ToggleUsername activates/deactivates one collectible username. The bool
	// reports whether anything changed; false maps to USERNAME_NOT_MODIFIED.
	ToggleUsername(ctx context.Context, peer domain.Peer, username string, active bool) (bool, error)
	// ReorderUsernames rewrites the active username display order, including the
	// editable slot when present (domain.ValidateUsernameReorder).
	ReorderUsernames(ctx context.Context, peer domain.Peer, order []string) (bool, error)
	// DeactivateAllUsernames deactivates every collectible username of the peer.
	DeactivateAllUsernames(ctx context.Context, peer domain.Peer) (bool, error)
	// Collectible returns the asset and owner needed by the RPC edge to enforce
	// fragment visibility before projecting fragment.collectibleInfo.
	Collectible(ctx context.Context, username string) (domain.CollectibleUsername, error)
}

// AccountRatingService exposes the stored gramsrv composite rating used by the
// userFull rating projection.
//
// It is deliberately read-only at the RPC boundary: ratings are computed by the
// bounded background worker, while profile reads only fetch the latest stored
// projection. A nil service or a read failure leaves every rating flag unset.
type AccountRatingService interface {
	Rating(ctx context.Context, userID int64) (domain.AccountRating, error)
}

// BotVerificationService is the third-party bot verification boundary
// (core.telegram.org/api/bots/verification): a verifier bot marking peers with its
// own icon and description, which official clients render as a badge distinct from
// the operator-granted checkmark.
//
// It is the single source for every surface that projects the feature --
// user.bot_verification_icon, channel.bot_verification_icon,
// userFull.bot_verification, channelFull.bot_verification,
// chatInvite.bot_verification and botInfo.verifier_settings -- so no two responses
// can disagree about which mark a peer carries.
//
// Optional like UsernameRegistryService: a nil field, or
// any read error, must leave every flag unset and bots.setCustomVerification
// answering BOT_VERIFIER_FORBIDDEN, which is exactly the pre-feature wire shape.
type BotVerificationService interface {
	// PeerVerification returns the peer's single mark, or
	// domain.ErrCustomVerificationNotFound.
	PeerVerification(ctx context.Context, peer domain.Peer) (domain.CustomVerification, error)
	// PeerVerificationBatch is the N+1-free variant used by the response-boundary
	// overlay. Peers without a mark may be omitted from the result map.
	PeerVerificationBatch(ctx context.Context, peers []domain.Peer) (map[domain.Peer]domain.CustomVerification, error)
	// VerifierSettings reads one bot's verifier status, or
	// domain.ErrVerifierNotFound.
	VerifierSettings(ctx context.Context, botID int64) (domain.BotVerifierSettings, error)
	// VerifierSettingsBatch resolves several bots at once for the botInfo
	// projection; bots without verifier status may be omitted.
	VerifierSettingsBatch(ctx context.Context, botIDs []int64) (map[int64]domain.BotVerifierSettings, error)
	// SetCustomVerification applies bots.setCustomVerification. changed controls
	// update fan-out only; the RPC returns Bool true for an idempotent success.
	SetCustomVerification(ctx context.Context, req domain.SetCustomVerificationRequest) (changed bool, err error)
}

// Deps injects service interfaces by business domain. See the corresponding file (auth.go / users.go / updates.go) for each domain's handler registration.
type Deps struct {
	Auth                AuthService
	AuthDeliveryReports AuthDeliveryReportService
	ClientTelemetry     ClientTelemetryService
	// AuthKeySessionLayers is the protocol-only durable ordering boundary for
	// explicit invokeWithLayer evidence. Production must wire the same auth-key
	// store used by the MTProto edge; nil is reserved for isolated router tests.
	AuthKeySessionLayers         store.AuthKeySessionLayerStore
	ReadModelVersions            store.ReadModelVersionStore
	UserProjectionFacts          UserProjectionFactInvalidator
	Account                      AccountService
	Privacy                      PrivacyService
	Help                         HelpService
	AppUpdates                   updatecdn.Resolver
	AccountFreeze                AccountFreezeService
	AccountFreezeNotifications   AccountFreezeNotificationService
	AccountDeletionNotifications AccountDeletionNotificationService
	AICompose                    AIComposeService
	Ephemeral                    EphemeralService
	EphemeralPush                store.EphemeralPushBroker
	WelcomeMessages              WelcomeMessageService
	Moderation                   ModerationService
	Users                        UsersService
	Usernames                    UsernameRegistryService
	CollectiblePhones            CollectiblePhoneService
	AccountRatings               AccountRatingService
	BotVerifications             BotVerificationService
	TelegramLogin                TelegramLoginService
	Updates                      UpdatesService
	BootstrapUpdates             store.BootstrapUpdateJobStore
	BotAPIUpdates                store.BotAPIUpdateStore
	BotCallbacks                 store.BotCallbackRegistryStore
	Contacts                     ContactsService
	Dialogs                      DialogsService
	Chatlists                    ChatlistsService
	Messages                     MessagesService
	Translation                  TranslationService
	Transcriptions               TranscriptionService
	Stories                      StoriesService
	Channels                     ChannelsService
	Communities                  CommunitiesService
	Files                        FilesService
	PremiumPromo                 PremiumPromoService
	Premium                      PremiumService
	Bots                         BotsService
	ServiceBotCallbacks          ServiceBotCallbacks
	ServiceBotInlineResults      ServiceBotInlineResults
	Polls                        PollsService
	Phone                        PhoneService
	GroupCalls                   GroupCallsService
	LiveStreams                  LiveStreamsService
	SFU                          sfu.Service
	TURN                         turnsrv.Service
	LangPack                     LangPackService
	Sessions                     SessionBinder
	Inline                       store.InlineRegistryStore
	Limiter                      RateLimiter
	Metrics                      Metrics
	SecretChats                  SecretChatService
	Stars                        StarsService
	Gifts                        GiftsService
	// Wallet is the Layer 230 in-app TON wallet; when nil, wallet.* returns WALLET_UNAVAILABLE.
	Wallet WalletService
	// MainTabs stores account/channel setMainProfileTab; nil keeps the default tab.
	MainTabs MainTabStore
	Passkey  PasskeyService
	Themes   ThemeService
	// GeoIP resolves the IP recorded for a session into the country/region text shown
	// by account.getAuthorizations. When nil (no geo backend configured) the list
	// keeps returning the placeholder text, as before it was enabled.
	GeoIP geoip.Resolver
}

// ThemeService abstracts custom cloud themes (app/themes): create/update/query
// themes + maintain the per-user installed list. File upload (uploadTheme) is done
// directly by the rpc layer through Files, not through this interface. Uses only
// domain + basic types.
type ThemeService interface {
	Create(ctx context.Context, spec domain.ThemeSpec) (domain.Theme, error)
	Update(ctx context.Context, userID int64, ref domain.ThemeRef, upd domain.ThemeUpdate) (domain.Theme, error)
	Get(ctx context.Context, ref domain.ThemeRef) (domain.Theme, bool, error)
	Save(ctx context.Context, userID int64, ref domain.ThemeRef) error
	Unsave(ctx context.Context, userID int64, ref domain.ThemeRef) error
	Install(ctx context.Context, userID int64, ref domain.ThemeRef, dark bool) error
	ListInstalled(ctx context.Context, userID int64) ([]domain.Theme, error)
	ListForUser(ctx context.Context, userID int64) ([]domain.Theme, error)
}

// PasskeyService abstracts passkey (WebAuthn) login and management (app/passkey).
// Challenge options are returned as DataJSON bytes; registration/login verification
// takes raw bytes (credentialID already base64url-decoded). FinishLogin returns the
// verified user id; auth_key binding is done by rpc via Auth.BindVerifiedLogin.
type PasskeyService interface {
	InitLogin(ctx context.Context) ([]byte, error)
	FinishLogin(ctx context.Context, credentialID, clientDataJSON, authenticatorData, signature []byte, userHandle string) (int64, error)
	InitRegistration(ctx context.Context, userID int64, displayName string) ([]byte, error)
	Register(ctx context.Context, userID int64, credentialID, clientDataJSON, attestationObject []byte, name string) (domain.PasskeyCredential, error)
	List(ctx context.Context, userID int64) ([]domain.PasskeyCredential, error)
	Delete(ctx context.Context, userID int64, credentialID []byte) (bool, error)
}

// GiftsService abstracts Star gifts (app/stargifts): catalog + CRUD of gift
// instances received by a peer. Charging/refunds/service message delivery is
// orchestrated by the rpc layer via the Stars ledger + Messages.SendPrivateText.
type GiftsService interface {
	SettledStarGiftPurchase(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, bool, error)
	Catalog(ctx context.Context) ([]domain.StarGift, error)
	CatalogHash(ctx context.Context) (int, error)
	GiftByID(ctx context.Context, id int64) (domain.StarGift, bool, error)
	GiftRevisionByID(ctx context.Context, revisionID int64) (domain.StarGift, bool, error)
	CollectiblePreview(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error)
	CollectiblePreviewSample(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error)
	CollectibleAvailability(ctx context.Context, giftIDs []int64) (map[int64]domain.StarGiftCollectibleAvailability, error)
	UniqueBySlug(ctx context.Context, slug string) (domain.UniqueStarGift, bool, error)
	UniqueByID(ctx context.Context, uniqueGiftID int64) (domain.UniqueStarGift, bool, error)
	UniqueByIDs(ctx context.Context, uniqueGiftIDs []int64) (map[int64]domain.UniqueStarGift, error)
	ListUniqueByOwner(ctx context.Context, owner domain.Peer, limit int) ([]domain.UniqueStarGift, error)
	Upgrade(ctx context.Context, req domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error)
	UpgradeReceipt(ctx context.Context, userID int64, commandKey string) (domain.StarGiftUpgradeReceipt, bool, error)
	RecordSavedGift(ctx context.Context, gift domain.SavedStarGift) (int64, error)
	ListSaved(ctx context.Context, owner domain.Peer, excludeUnsaved bool, offset string, limit int) (domain.SavedStarGiftPage, error)
	ListSavedFiltered(ctx context.Context, filter domain.SavedStarGiftFilter) (domain.SavedStarGiftPage, error)
	GetSaved(ctx context.Context, ref domain.SavedStarGiftRef) (domain.SavedStarGift, bool, error)
	ResolveSavedIDs(ctx context.Context, owner domain.Peer, refs []domain.SavedStarGiftRef) ([]int64, error)
	CountSaved(ctx context.Context, owner domain.Peer) (int, error)
	ToggleSaved(ctx context.Context, ref domain.SavedStarGiftRef, unsaved bool) (bool, error)
	ConvertAggregate(ctx context.Context, req domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error)
	ListCollections(ctx context.Context, owner domain.Peer) ([]domain.StarGiftCollection, error)
	CreateCollection(ctx context.Context, owner domain.Peer, title string, savedGiftIDs []int64) (domain.StarGiftCollection, error)
	UpdateCollection(ctx context.Context, owner domain.Peer, collectionID int, patch domain.StarGiftCollectionPatch) (domain.StarGiftCollection, error)
	DeleteCollection(ctx context.Context, owner domain.Peer, collectionID int) (bool, error)
	ReorderCollections(ctx context.Context, owner domain.Peer, collectionIDs []int) error
	SetPinned(ctx context.Context, owner domain.Peer, savedGiftIDs []int64) error
	ListResale(ctx context.Context, filter domain.StarGiftResaleFilter) (domain.StarGiftResalePage, error)
	ValueInfo(ctx context.Context, uniqueGiftID int64) (domain.StarGiftValueInfo, error)
	SetListing(ctx context.Context, req domain.StarGiftListingRequest) (domain.UniqueStarGift, error)
	Transfer(ctx context.Context, req domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error)
	PurchaseResale(ctx context.Context, req domain.StarGiftResalePurchaseRequest) (domain.StarGiftTransferResult, error)
	SendOffer(ctx context.Context, req domain.StarGiftOfferRequest) (domain.StarGiftOfferResult, error)
	ResolveOffer(ctx context.Context, req domain.StarGiftResolveOfferRequest) (domain.StarGiftOfferResult, error)
	ListCraft(ctx context.Context, userID, giftID int64, offset string, limit int) (domain.SavedStarGiftPage, error)
	Craft(ctx context.Context, req domain.StarGiftCraftRequest) (domain.StarGiftCraftResult, error)
	AuctionState(ctx context.Context, userID, giftID int64, slug string, now int) (domain.StarGiftAuction, error)
	ActiveAuctions(ctx context.Context, userID int64, now int) ([]domain.StarGiftAuction, error)
	AuctionAcquired(ctx context.Context, userID, giftID int64) ([]domain.StarGiftAuctionAcquired, error)
	BidAuction(ctx context.Context, req domain.StarGiftAuctionBidRequest) (domain.StarGiftAuction, domain.StarsBalance, error)
	PrepaidUpgradeTarget(ctx context.Context, owner domain.Peer, hash string) (domain.SavedStarGift, int64, error)
	PrepayUpgrade(ctx context.Context, req domain.StarGiftPrepaidUpgradeRequest) (domain.StarGiftPrepaidUpgradeResult, error)
	DropOriginalDetails(ctx context.Context, req domain.StarGiftDropOriginalDetailsRequest) (domain.StarGiftDropOriginalDetailsResult, error)
	SetNotifications(ctx context.Context, userID, channelID int64, enabled bool) error
	Withdraw(ctx context.Context, req domain.StarGiftWithdrawalRequest) (domain.StarGiftWithdrawal, error)
	// DeleteStarGift removes a received gift from the owner's profile
	// (payments.deleteStarGift, Layer 232).
	DeleteStarGift(ctx context.Context, req domain.StarGiftDeleteRequest) (domain.StarGiftDeleteResult, error)
	TonBalance(ctx context.Context, userID int64) (int64, error)
	TonTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
	IssuePurchaseForm(ctx context.Context, form domain.StarGiftPurchaseForm) (domain.StarGiftPurchaseForm, error)
	ValidatePurchaseForm(ctx context.Context, req domain.StarGiftPurchaseRequest) error
	Purchase(ctx context.Context, req domain.StarGiftPurchaseRequest) (domain.StarGiftPurchaseResult, error)
}

// StarsService abstracts the local Stars ledger (app/stars): balance query,
// credit/debit, transaction pagination. Debit is atomic and never negative; an
// insufficient balance returns domain.ErrStarsInsufficient (rpc maps it to
// BALANCE_TOO_LOW via starsErr). getStarsStatus lazily grants the starting balance
// on first read.
type StarsService interface {
	GetBalance(ctx context.Context, userID int64) (domain.StarsBalance, error)
	Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
	Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
	ListTransactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.StarsTransactionPage, error)
}

// WalletService abstracts the Layer 230 in-app TON wallet (app/wallet): ownership
// challenge, the binding between an account and a TON address, and (optionally)
// forwarding to a third-party provider. When nil (wallet catalog not configured),
// wallet.* uniformly returns WALLET_UNAVAILABLE rather than dropping the request as
// an unknown method.
type WalletTransferService interface {
	TransferWalletGram(ctx context.Context, req domain.WalletGramTransferRequest) (domain.WalletGramTransferResult, error)
}

type WalletService interface {
	// GetProofChallenge generates an unpredictable challenge bound to the account + MTProto authorization.
	GetProofChallenge(ctx context.Context, userID int64, authKeyID [8]byte) (domain.WalletChallenge, error)
	// Links returns the given accounts' verified TON address bindings (read-only, does not create).
	Links(ctx context.Context, userIDs []int64) ([]domain.WalletLink, error)
	// State returns the account's current wallet binding and local ledger balance;
	// ok=false when no wallet is bound, and the caller should return walletStateEmpty
	// rather than fabricating an address.
	State(ctx context.Context, userID int64) (link domain.WalletLink, balance int64, ok bool, err error)
	// ReplaceImported binds the address after validating the Ed25519 ownership proof;
	// when allowReplacement is true it allows rebinding over an existing binding (the
	// caller must first complete the account password check).
	ReplaceImported(ctx context.Context, userID int64, authKeyID [8]byte, publicKey []byte, timestamp int, signature []byte, allowReplacement bool) (domain.WalletLink, int64, error)
	// Create generates a TON wallet from telesrv: the private key stays server-side
	// only as ciphertext, and the client only gets the address, public key and local
	// ledger balance. Returns capability-unavailable when the server key is not
	// configured.
	Create(ctx context.Context, userID int64, authKeyID [8]byte, allowReplacement bool) (domain.WalletLink, int64, error)
	// Perform forwards one provider JSON request. When the provider is not configured
	// it returns app/wallet.ErrProviderUnavailable, mapped by rpc to 503, never
	// falling back to an unknown method.
	Perform(ctx context.Context, endpoint string, query, payload *string) (string, error)
	// Transactions returns the account's local ledger transactions. Fund transactions
	// live in telesrv's own DB, not in the wallet state catalog, so they are provided
	// by dependency injection rather than FileStore.
	Transactions(ctx context.Context, userID int64, query domain.StarsTransactionQuery) (domain.TonTransactionPage, error)
}

// PremiumService is the domain-only boundary for catalog reads, payment form
// issuance and atomic Stars-backed settlement.
type PremiumService interface {
	BotUserID() int64
	BotUsername() string
	Plans(ctx context.Context) ([]domain.PremiumPlan, error)
	Plan(ctx context.Context, months int) (domain.PremiumPlan, error)
	IssuePaymentForm(ctx context.Context, form domain.PremiumPaymentForm) (domain.PremiumPaymentForm, error)
	Purchase(ctx context.Context, req domain.PremiumPurchaseRequest) (domain.PremiumPurchaseResult, error)
	ActiveEntitlements(ctx context.Context, userID int64, now int) ([]domain.PremiumEntitlement, error)
	PurchaseHistory(ctx context.Context, userID int64, limit int) ([]domain.PremiumEntitlement, error)
	SweepExpired(ctx context.Context, now, limit int) ([]domain.User, error)
	Grant(ctx context.Context, req domain.PremiumAdminGrantRequest) (domain.PremiumEntitlement, domain.User, error)
	Revoke(ctx context.Context, req domain.PremiumAdminRevokeRequest) (domain.User, error)
	Refund(ctx context.Context, req domain.PremiumRefundRequest) (domain.PremiumPurchaseResult, error)
}

// SecretChatService abstracts the private end-to-end encryption (Secret Chat)
// handshake state machine (app/secretchat). The server is a blind relay; the error
// set is in domain.ErrSecretChat* and app/secretchat.ErrGAInvalid (the rpc layer
// maps them to ENCRYPTION_* / CHAT_ID_INVALID / DH_G_A_INVALID via secretChatErr).
type SecretChatService interface {
	RequestEncryption(ctx context.Context, req domain.SecretChatRequest) (domain.SecretChat, error)
	AcceptEncryption(ctx context.Context, chatID int, viewerUserID, participantAuthKeyID, accessHash int64, gb []byte, keyFingerprint int64) (domain.SecretChat, error)
	DiscardEncryption(ctx context.Context, chatID int, viewerUserID, viewerAuthKeyID int64, deleteHistory bool) (domain.SecretChat, bool, error)
	// DiscardForAuthKey cascade-discards all active secret chats bound to this perm
	// auth_key (device logout / authorization revocation), returning the chats
	// actually migrated to discarded for notifying the peer.
	DiscardForAuthKey(ctx context.Context, authKeyID int64) ([]domain.SecretChat, error)
	GetSecretChat(ctx context.Context, chatID int) (domain.SecretChat, bool, error)
	SendEncrypted(ctx context.Context, chatID int, viewerUserID, viewerAuthKeyID, accessHash int64, delivery domain.SecretMessageDelivery) (domain.SecretChat, domain.SecretChatMessage, error)
	ListNewMessages(ctx context.Context, deviceAuthKeyID int64, sinceQts, limit int) ([]domain.SecretChatMessage, error)
	DeviceReservedQts(ctx context.Context, deviceAuthKeyID int64) (int, error)
	AckQueue(ctx context.Context, deviceAuthKeyID int64, maxQts int) error
	RecordEncryptionEvent(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, date int) error
	RecordReadEvent(ctx context.Context, chatID int, targetUserID, targetAuthKeyID int64, maxDate, date int) error
	ListStateEvents(ctx context.Context, userID, deviceAuthKeyID int64, limit int) ([]domain.EncryptedStateEvent, error)
	MarkStateEventsDelivered(ctx context.Context, deviceAuthKeyID int64, eventIDs []int64) error
	PutEncryptedFile(ctx context.Context, ownerUserID int64, ref domain.EncryptedFileRef) error
	GetEncryptedFile(ctx context.Context, id, accessHash int64) (domain.EncryptedFileRef, bool, error)
}

// PhoneService abstracts the private 1:1 call signaling state machine (app/phone).
// All return values are state snapshots; the error set is app/phone's Err* (the rpc
// layer maps them to CALL_* RPC_ERROR via phoneCallErr).
type PhoneService interface {
	RequestCall(ctx context.Context, callerID int64, in domain.PhoneCallRequest) (domain.PhoneCall, error)
	ReceivedCall(ctx context.Context, userID, callID, accessHash int64) (domain.PhoneCall, bool, error)
	AcceptCall(ctx context.Context, userID, callID, accessHash int64, gb []byte, proto domain.PhoneCallProtocol, device domain.SessionRef) (domain.PhoneCall, error)
	ConfirmCall(ctx context.Context, userID, callID, accessHash int64, ga []byte, keyFingerprint int64, proto domain.PhoneCallProtocol) (call domain.PhoneCall, forcedDiscard bool, err error)
	DiscardCall(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, duration int) (call domain.PhoneCall, already bool, err error)
	DiscardCallWithSlug(ctx context.Context, userID, callID, accessHash int64, reason domain.PhoneCallDiscardReason, reasonSlug string, duration int) (call domain.PhoneCall, already bool, err error)
	// Signal executes forward within the call's signaling-order lock; drop=true means
	// silently swallowed per contract. peerDevice is the peer's accepting-device
	// anchor (may be zero/stale); a failed targeted push must fall back to user
	// fan-out.
	Signal(ctx context.Context, userID, callID, accessHash int64, forward func(peerUserID int64, peerDevice domain.SessionRef)) (drop bool, err error)
	Lookup(ctx context.Context, callID, accessHash int64) (domain.PhoneCall, bool)
	// ExpireDue is called periodically by PhoneExpiryDispatcher: migrate timed-out calls and return snapshots.
	ExpireDue(ctx context.Context, now time.Time) []domain.PhoneCall
}

// GroupCallsService abstracts supergroup voice chat signaling (app/groupcalls).
// The error set is domain.ErrGroupCall* (the rpc layer maps them to GROUPCALL_*
// RPC_ERROR).
type GroupCallsService interface {
	Create(ctx context.Context, channelID, creatorUserID int64, title string, rtmpStream, joinMuted bool, scheduleDate, now int) (domain.GroupCall, error)
	// RtmpStreamKey fetches/rotates a channel's persistent RTMP stream key (rotate=true overwrites the old key).
	RtmpStreamKey(ctx context.Context, channelID int64, rotate bool, now int) (string, error)
	// StartScheduled / SetScheduleSubscription / ScheduleSubscriberIDs are the scheduled-call flow.
	StartScheduled(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	SetScheduleSubscription(ctx context.Context, callID, userID int64, subscribed bool) error
	ScheduleSubscriberIDs(ctx context.Context, callID int64) ([]int64, error)
	CreateConference(ctx context.Context, creatorUserID, randomID, migratedFromPhoneCallID int64, now int) (domain.GroupCall, error)
	Get(ctx context.Context, callID int64) (domain.GroupCall, bool, error)
	GetBySlug(ctx context.Context, slug string) (domain.GroupCall, bool, error)
	GetByInviteMessage(ctx context.Context, userID int64, msgID int) (domain.GroupCall, domain.GroupCallInvite, bool, error)
	Join(ctx context.Context, req domain.JoinGroupCallRequest) (domain.GroupCallMutation, error)
	Leave(ctx context.Context, callID, userID int64, now int) (domain.GroupCallMutation, error)
	RemoveConferenceParticipants(ctx context.Context, req domain.RemoveConferenceCallParticipantsRequest) (domain.RemoveConferenceCallParticipantsResult, error)
	Discard(ctx context.Context, callID int64, now int) (domain.GroupCall, []domain.GroupCallParticipant, error)
	Touch(ctx context.Context, callID, userID int64, now int) (activeSSRCs []int64, joined bool, err error)
	Participant(ctx context.Context, callID, userID int64) (domain.GroupCallParticipant, bool, error)
	Participants(ctx context.Context, callID int64, offset string, limit int) (domain.GroupCallParticipantPage, error)
	UpdateParticipant(ctx context.Context, callID, userID int64, update domain.GroupCallParticipantUpdate) (domain.GroupCallMutation, bool, error)
	SetTitle(ctx context.Context, callID int64, title string) (domain.GroupCall, bool, error)
	SetJoinMuted(ctx context.Context, callID int64, joinMuted bool) (domain.GroupCall, bool, error)
	SetStartedMessageID(ctx context.Context, callID int64, msgID int) error
	SweepStale(ctx context.Context, checkOlderThan, now, limit int) ([]domain.GroupCallMutation, error)
	ResetAllParticipants(ctx context.Context, now int) ([]domain.GroupCall, error)
	NextRaiseHandRating(ctx context.Context, callID int64) (int64, error)
	SetParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64, override domain.GroupCallParticipantOverride, clear bool) error
	ParticipantOverride(ctx context.Context, callID, setterUserID, targetUserID int64) (domain.GroupCallParticipantOverride, bool, error)
	CreateConferenceInvite(ctx context.Context, invite domain.GroupCallInvite) (domain.GroupCallInvite, error)
	SetConferenceInviteStatus(ctx context.Context, callID, inviteeUserID int64, msgID int, status domain.GroupCallInviteStatus, now int) (domain.GroupCallInvite, bool, error)
	ConferenceRecipients(ctx context.Context, callID int64) ([]int64, error)
	AppendChainBlock(ctx context.Context, block domain.GroupCallChainBlock) (domain.GroupCallChainBlock, error)
	ChainBlocks(ctx context.Context, callID int64, subChainID, offset, limit int) (domain.GroupCallChainBlockPage, error)
}

// LiveStreamsService abstracts the live media plane (app/livestream: RTMP ingest +
// segment ring). nil = live media plane not enabled (signaling still works, viewers
// stay on the "waiting for stream" placeholder).
type LiveStreamsService interface {
	// StreamChannels returns the channel's current live timeline; empty when there is no active stream.
	StreamChannels(channelID int64) []domain.LiveStreamChannel
	// StreamPart fetches a packaged tgcalls broadcast part by time_ms/scale.
	StreamPart(channelID int64, timeMs int64, scale int) ([]byte, error)
	// DropChannel disconnects the channel's stream session and clears the buffer (discard/revoke).
	DropChannel(channelID int64)
}

// PollsService abstracts send-time creation of poll authoritative state and the voter list (messages.getPollVotes).
type PollsService interface {
	CreatePoll(ctx context.Context, def domain.PollDefinition) error
	GetPollDefinition(ctx context.Context, pollID int64) (domain.PollDefinition, bool, error)
	ListPollVotes(ctx context.Context, req domain.PollVotesListRequest) (domain.PollVotesList, error)
}
