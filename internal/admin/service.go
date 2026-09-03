package admin

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/officialgifts"
)

const (
	ActionSetAccountFrozen        = "account.set_frozen"
	ActionGrantPremium            = "account.grant_premium"
	ActionRefundPremium           = "account.refund_premium"
	ActionUpsertPremiumPlan       = "premium.plan.upsert"
	ActionGrantStars              = "account.grant_stars"
	ActionDebitStars              = "account.debit_stars"
	ActionSetVerified             = "account.set_verified"
	ActionSetUserFlags            = "account.set_flags"
	ActionSetSupport              = "account.set_support"
	ActionSetUsername             = "account.set_username"
	ActionSetProfile              = "account.set_profile"
	ActionSetPhone                = "account.set_phone"
	ActionSetLoginEmail           = "account.set_login_email"
	ActionSetAccountAvatar        = "account.set_avatar"
	ActionSetUserColor            = "account.set_color"
	ActionSetUserEmojiStatus      = "account.set_emoji_status"
	ActionSetChannelUsername      = "channel.set_username"
	ActionSetChannelSettings      = "channel.set_settings"
	ActionSetChannelColor         = "channel.set_color"
	ActionSetChannelEmojiStatus   = "channel.set_emoji_status"
	ActionSetChannelAvatar        = "channel.set_avatar"
	ActionSetChannelVerified      = "channel.set_verified"
	ActionSetChannelFlags         = "channel.set_flags"
	ActionRevokeSessions          = "account.revoke_sessions"
	ActionDeletePrivateMessages   = "messages.delete_private_messages"
	ActionDeletePrivateHistory    = "messages.delete_private_history"
	ActionImportStarGift          = "gifts.import"
	ActionImportOfficialStarGift  = "gifts.official.import"
	ActionPublishGiftCollectibles = "gifts.collectibles.publish"
	ActionSetStarGiftEnabled      = "gifts.set_enabled"
	ActionSetStarGiftSortOrder    = "gifts.set_sort_order"
	ActionGiveGift                = "gifts.give"
	ActionDeleteStarGift          = "gifts.delete"
	ActionCreateBot               = "bot.create"
	ActionCreateBroadcast         = "broadcast.create"
	ActionSetStickerSetArchived   = "stickers.set_archived"
	ActionSetStickerSetSortOrder  = "stickers.set_sort_order"
	ActionRenameStickerSet        = "stickers.rename"
	ActionDeleteStickerSet        = "stickers.delete"
	ActionCreateStickerSet        = "stickers.create"
	ActionAddStickerToSet         = "stickers.add_sticker"
	ActionRemoveStickerFromSet    = "stickers.remove_sticker"
	ActionCreateGifCatalogEntry   = "gif_catalog.create"
	ActionSetGifCatalogEnabled    = "gif_catalog.set_enabled"
	ActionSetGifCatalogSortOrder  = "gif_catalog.set_sort_order"
	ActionDeleteGifCatalogEntry   = "gif_catalog.delete"
	ActionDeleteBot               = "bot.delete"
	ActionExportBotToken          = "bot.export_token"
	// Collectible (Fragment-style) username lifecycle.
	ActionMintCollectibleUsername     = "usernames.collectible.mint"
	ActionTransferCollectibleUsername = "usernames.collectible.transfer"
	ActionRevokeCollectibleUsername   = "usernames.collectible.revoke"
	ActionDeleteCollectibleUsername   = "usernames.collectible.delete"
	ActionMintCollectiblePhone        = "phones.collectible.mint"
	ActionUpdateCollectiblePhonePrice = "phones.collectible.update_price"
	ActionTransferCollectiblePhone    = "phones.collectible.transfer"
	ActionRevokeCollectiblePhone      = "phones.collectible.revoke"
	ActionDeleteCollectiblePhone      = "phones.collectible.delete"
	// Composite account rating.
	ActionRecomputeAccountRating = "rating.recompute"
	ActionAdjustAccountRating    = "rating.adjust"
	// Admin panel auto-subscribe channel list.
	ActionAddAutoSubscribeChannel    = "channels.auto_subscribe.add"
	ActionRemoveAutoSubscribeChannel = "channels.auto_subscribe.remove"
	// Official platform verification review. Claim/approve/reject act on one
	// application; revoke acts on a target, because clearing a badge is not a
	// decision on the application that granted it.
	ActionClaimVerification   = "verification.claim"
	ActionApproveVerification = "verification.approve"
	ActionRejectVerification  = "verification.reject"
	ActionRevokeVerification  = "verification.revoke"
	// Third-party bot verification (see botverification.go). A namespace of its own
	// on purpose: these actions write the verifier catalogue and the attributed
	// marks, never the platform checkmark, and the audit trail has to keep the two
	// mechanisms apart at a glance.
	ActionGrantBotVerifier          = "botverification.grant_verifier"
	ActionSetBotVerifierEnabled     = "botverification.set_verifier_enabled"
	ActionRevokeBotVerifier         = "botverification.revoke_verifier"
	ActionUpsertVerificationIcon    = "botverification.upsert_icon"
	ActionSetVerificationIconActive = "botverification.set_icon_active"
	ActionRevokeCustomVerification  = "botverification.revoke_mark"
	ActionApproveBotVerification    = "botverification.approve"
	ActionRejectBotVerification     = "botverification.reject"
	ActionRevokeBotVerification     = "botverification.revoke_request"

	maxCommandIDLength       = 128
	maxActorLength           = 128
	maxReasonLength          = 1000
	maxHistoryBatches        = 100
	maxPremiumMonths         = 120
	maxStarsGrant            = 1_000_000_000
	maxFreezeAppealURLLength = 2048
	// maxAccountRatingAdjustment bounds one manual rating adjustment in either
	// direction. The domain only rejects a zero delta, so the operator-facing
	// bound lives here next to the other grant limits.
	maxAccountRatingAdjustment = 1_000_000_000
)

// Stable admin error codes for the collectible-username and account-rating
// subsystems. The panel switches on the code, so a command failure and a read
// failure describe the same condition with the same token instead of leaking a
// Go error string the UI would have to pattern-match.
const (
	CodeUsernameOccupied           = "USERNAME_OCCUPIED"
	CodeUsernameInvalid            = "USERNAME_INVALID"
	CodeUsernameNotCollectible     = "USERNAME_NOT_COLLECTIBLE"
	CodeCollectibleNotFound        = "COLLECTIBLE_NOT_FOUND"
	CodeCollectibleNotOwned        = "COLLECTIBLE_NOT_OWNED"
	CodeCollectibleBurned          = "COLLECTIBLE_BURNED"
	CodeCollectiblePeerLimit       = "COLLECTIBLE_PEER_LIMIT"
	CodeCollectibleCurrencyInvalid = "COLLECTIBLE_CURRENCY_INVALID"
	CodeCollectibleStateInvalid    = "COLLECTIBLE_STATE_INVALID"
	CodeRatingNotFound             = "RATING_NOT_FOUND"
	CodeRatingAdjustmentInvalid    = "RATING_ADJUSTMENT_INVALID"
	CodeRatingWeightsInvalid       = "RATING_WEIGHTS_INVALID"
	// Official platform verification review. CodeVerificationConflict is the lost
	// optimistic-locking race -- two reviewers deciding at once -- and is the one
	// the panel must render as "reload and look again" rather than as a bad
	// request.
	CodeVerificationNotFound            = "VERIFICATION_NOT_FOUND"
	CodeVerificationConflict            = "VERIFICATION_CONFLICT"
	CodeVerificationStatusInvalid       = "VERIFICATION_STATUS_INVALID"
	CodeVerificationReasonRequired      = "VERIFICATION_REASON_REQUIRED"
	CodeVerificationTargetInvalid       = "VERIFICATION_TARGET_INVALID"
	CodeVerificationTargetOccupied      = "VERIFICATION_TARGET_OCCUPIED"
	CodeVerificationTargetVerified      = "VERIFICATION_TARGET_ALREADY_VERIFIED"
	CodeVerificationTargetNotPublic     = "VERIFICATION_TARGET_NOT_PUBLIC"
	CodeVerificationTargetRestricted    = "VERIFICATION_TARGET_RESTRICTED"
	CodeVerificationTargetSystem        = "VERIFICATION_TARGET_SYSTEM"
	CodeVerificationNotOwner            = "VERIFICATION_NOT_OWNER"
	CodeVerificationUserTargetsDisabled = "VERIFICATION_USER_TARGETS_DISABLED"
	CodeVerificationInvalid             = "VERIFICATION_INVALID"
)

// Stable admin error codes for third-party bot verification (see
// botverification.go). They are a separate set from the official verification
// codes above: the two mechanisms own separate tables and fail for separate
// reasons, and the panel renders them in separate sections, so one shared token
// would land a message in the wrong place.
//
// CodeCustomVerificationConflict is the lost optimistic-locking race -- two
// operators deciding at once -- and is the one the panel must render as "reload
// and look again" rather than as a bad request.
const (
	CodeBotVerifierNotFound             = "BOTVERIFIER_NOT_FOUND"
	CodeBotVerifierForbidden            = "BOTVERIFIER_FORBIDDEN"
	CodeBotVerifierInvalid              = "BOTVERIFIER_INVALID"
	CodeBotVerifierBotNotFound          = "BOTVERIFIER_BOT_NOT_FOUND"
	CodeBotVerifierDescriptionForbidden = "BOTVERIFIER_DESCRIPTION_FORBIDDEN"

	CodeVerificationIconNotFound = "VERIFICATION_ICON_NOT_FOUND"
	CodeVerificationIconInactive = "VERIFICATION_ICON_INACTIVE"
	CodeVerificationIconInvalid  = "VERIFICATION_ICON_INVALID"

	CodeCustomVerificationNotFound        = "CUSTOM_VERIFICATION_NOT_FOUND"
	CodeCustomVerificationRequestNotFound = "CUSTOM_VERIFICATION_REQUEST_NOT_FOUND"
	CodeCustomVerificationRequestExists   = "CUSTOM_VERIFICATION_REQUEST_EXISTS"
	CodeCustomVerificationConflict        = "CUSTOM_VERIFICATION_CONFLICT"
	CodeCustomVerificationLimit           = "CUSTOM_VERIFICATION_LIMIT"
	CodeCustomVerificationStatusInvalid   = "CUSTOM_VERIFICATION_STATUS_INVALID"
	CodeCustomVerificationReasonRequired  = "CUSTOM_VERIFICATION_REASON_REQUIRED"
	CodeCustomVerificationTargetInvalid   = "CUSTOM_VERIFICATION_TARGET_INVALID"
	CodeCustomVerificationTargetSystem    = "CUSTOM_VERIFICATION_TARGET_SYSTEM"
	CodeCustomVerificationRateLimited     = "CUSTOM_VERIFICATION_RATE_LIMITED"
	CodeCustomVerificationInvalid         = "CUSTOM_VERIFICATION_INVALID"
)

type CommandRepository interface {
	BeginCommand(ctx context.Context, cmd domain.AdminCommand) (domain.AdminCommand, bool, error)
	FinishCommand(ctx context.Context, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error)
}

type RestrictionStore interface {
	GetAccountFreeze(ctx context.Context, userID int64) (domain.AccountFreeze, bool, error)
	SetAccountFreeze(ctx context.Context, freeze domain.AccountFreeze) (domain.AccountFreeze, error)
}

type accountFreezeBatchStore interface {
	GetAccountFreezes(ctx context.Context, userIDs []int64) (map[int64]domain.AccountFreeze, error)
}

type accountFreezeNotificationStore interface {
	ClaimAccountFreezeNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountFreezeNotification, error)
	CompleteAccountFreezeNotification(ctx context.Context, id, version int64, now time.Time) error
}

type AuthService interface {
	ListAuthorizations(ctx context.Context, userID int64) ([]domain.Authorization, error)
	ResetAuthorization(ctx context.Context, userID, hash int64) (domain.Authorization, bool, error)
	ResetAuthorizations(ctx context.Context, userID int64, keepAuthKeyID [8]byte) ([]domain.Authorization, error)
}

type AuthKeyRevoker interface {
	RevokeAuthorizationAuthKey(ctx context.Context, authKeyID [8]byte, userID int64) error
}

type UsersService interface {
	AdminUser(ctx context.Context, userID int64) (domain.User, bool, error)
	GrantPremium(ctx context.Context, userID int64, months int) (domain.User, error)
	SetVerified(ctx context.Context, userID int64, verified bool) (domain.User, error)
	SetScamFake(ctx context.Context, userID int64, scam, fake bool) (domain.User, error)
	SetSupport(ctx context.Context, userID int64, support bool) (domain.User, error)
	UpdateUsername(ctx context.Context, userID int64, username string) (domain.User, error)
	UpdateColor(ctx context.Context, userID int64, forProfile bool, color domain.PeerColor) (domain.User, error)
	UpdateEmojiStatus(ctx context.Context, userID int64, status domain.UserEmojiStatus) (domain.User, error)
	UpdateProfile(ctx context.Context, userID int64, update domain.UserProfileUpdate) (domain.User, error)
	SetPhone(ctx context.Context, userID int64, phone string) (domain.User, error)
}

type AccountService interface {
	ValidLoginEmail(email string) bool
	SetLoginEmail(ctx context.Context, userID int64, email string) error
	ClearLoginEmail(ctx context.Context, userID int64) error
	LoginEmail(ctx context.Context, userID int64) (string, bool, error)
}

type StarsService interface {
	Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
	Debit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
}

type BroadcastService interface {
	Preview(ctx context.Context, message string, mode domain.BroadcastTargetMode, selectedUserIDs []int64) (int64, error)
	Create(ctx context.Context, message string, mode domain.BroadcastTargetMode, selectedUserIDs []int64, createdBy string) (domain.Broadcast, error)
}

type PremiumService interface {
	Plan(ctx context.Context, months int) (domain.PremiumPlan, error)
	Catalog(ctx context.Context) ([]domain.PremiumPlan, error)
	UpsertPlan(ctx context.Context, req domain.PremiumPlanUpsertRequest) (domain.PremiumPlan, error)
	Entitlements(ctx context.Context, userID int64, limit int) ([]domain.PremiumEntitlement, error)
	Payment(ctx context.Context, paymentIntentID int64) (domain.PremiumPaymentDetails, bool, error)
	Grant(ctx context.Context, req domain.PremiumAdminGrantRequest) (domain.PremiumEntitlement, domain.User, error)
	Revoke(ctx context.Context, req domain.PremiumAdminRevokeRequest) (domain.User, error)
	Refund(ctx context.Context, req domain.PremiumRefundRequest) (domain.PremiumPurchaseResult, error)
}

type StarsNotifier interface {
	NotifyStarsBalanceChanged(ctx context.Context, balance domain.StarsBalance) error
}

type UserNotifier interface {
	NotifyUserChanged(ctx context.Context, u domain.User) error
}

type UserModerationNotifier interface {
	NotifyUserModerationFlagsChanged(ctx context.Context, u domain.User) error
}

type AccountFreezeNotifier interface {
	NotifyAccountFreezeChanged(ctx context.Context, freeze domain.AccountFreeze) error
}

type ChannelsService interface {
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
	SetVerified(ctx context.Context, channelID int64, verified bool) (domain.Channel, error)
	SetScamFake(ctx context.Context, channelID int64, scam, fake bool) (domain.Channel, error)
	AdminSetSettings(ctx context.Context, channelID int64, patch domain.ChannelAdminSettings) (domain.Channel, error)
	AdminSetUsername(ctx context.Context, channelID int64, username string) (domain.Channel, error)
	AdminSetColor(ctx context.Context, channelID int64, forProfile bool, color domain.ChannelPeerColor) (domain.Channel, error)
	AdminSetEmojiStatus(ctx context.Context, channelID int64, status domain.ChannelEmojiStatus) (domain.Channel, error)
	AdminSetPhoto(ctx context.Context, channelID int64, photo domain.Photo) (domain.Channel, error)
}

type ChannelNotifier interface {
	NotifyChannelChanged(ctx context.Context, ch domain.Channel) error
}

type MessagesService interface {
	GetMessages(ctx context.Context, userID int64, ids []int) (domain.MessageList, error)
	GetHistory(ctx context.Context, userID int64, filter domain.MessageFilter) (domain.MessageList, error)
	DeleteMessages(ctx context.Context, userID int64, req domain.DeleteMessagesRequest) (domain.DeleteMessagesResult, error)
	DeleteHistory(ctx context.Context, userID int64, req domain.DeleteHistoryRequest) (domain.DeleteMessagesResult, error)
}

type GiftsService interface {
	GiftByID(ctx context.Context, id int64) (domain.StarGift, bool, error)
	PrepareAnimation(fileName string, data []byte) (domain.StarGiftAnimation, error)
	PrepareOfficialAnimation(fileName string, data []byte) (domain.StarGiftAnimation, error)
	CreateCatalogRevision(ctx context.Context, write domain.StarGiftCatalogWrite) (domain.StarGiftCatalogEntry, error)
	CreateCatalogBundle(ctx context.Context, write domain.StarGiftCatalogBundleWrite) (domain.StarGiftCatalogBundleResult, error)
	SetCatalogEnabled(ctx context.Context, giftID int64, enabled bool) (bool, error)
	SetCatalogSortOrder(ctx context.Context, giftID int64, sortOrder int) (bool, error)
	AnimationJSON(ctx context.Context, giftID int64) ([]byte, bool, error)
	CreateCollectibleRevision(ctx context.Context, write domain.StarGiftCollectibleWrite) (domain.StarGiftCollectibleRevision, error)
	CollectiblePreview(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error)
	CollectibleAnimationJSON(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error)
	// DeleteEverywhere backs the admin panel's "delete gift" action -- see
	// store.StarGiftLifecycleStore.DeleteStarGiftEverywhere's own doc.
	DeleteEverywhere(ctx context.Context, giftID int64, refund, dryRun bool, date int) (domain.StarGiftDeletionResult, error)
}

type OfficialGiftsSource interface {
	List(ctx context.Context) ([]officialgifts.GiftSummary, error)
	Bundle(ctx context.Context, giftID int64, includeCollectible bool) (officialgifts.Bundle, error)
}

type AvatarResolver interface {
	CurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind) (domain.Photo, bool, error)
	GetPhoto(ctx context.Context, id int64) (domain.Photo, bool, error)
	GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error)
	ValidateAvatarUpload(data []byte) bool
	CreateAvatarFromBytes(ctx context.Context, data []byte) (domain.Photo, error)
	SetCurrentProfilePhotoKind(ctx context.Context, ownerType domain.PeerType, ownerID int64, kind domain.ProfilePhotoKind, photoID int64, date int) (domain.Photo, bool, error)
}

// BotService creates bot accounts on behalf of the admin. It mirrors the
// owner-scoped /newbot flow: a bot is a users row (is_bot=true) plus a bots row
// owned by ownerUserID, and the returned token is shown once to the operator.
type BotService interface {
	CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error)
	DeleteBot(ctx context.Context, botUserID int64) (domain.User, error)
	AdminExportBotToken(ctx context.Context, botUserID int64) (string, error)
}

// EmojiService renders custom-emoji document animations for the admin emoji
// browser (Lottie JSON, TGS transparently decompressed).
type EmojiService interface {
	DocumentAnimationJSON(ctx context.Context, documentID int64) ([]byte, bool, error)
}

// StickerSetsService is the admin-console management surface over sticker and
// custom-emoji packs. These methods deliberately do not require creator
// ownership, so seed-imported and operator-created packs can both be managed.
type StickerSetsService interface {
	AdminSetStickerSetArchived(ctx context.Context, setID int64, archived bool) (bool, error)
	AdminSetStickerSetSortOrder(ctx context.Context, setID int64, order int) (bool, error)
	AdminRenameStickerSet(ctx context.Context, setID int64, title string) (domain.StickerSet, error)
	AdminDeleteStickerSet(ctx context.Context, setID int64) (domain.StickerSetKind, error)
	ValidateStickerMaterialUpload(fileName string, data []byte) (mimeType string, ok bool)
	ValidateAdminCreateStickerSet(ctx context.Context, title, shortName, emoji string, kind domain.StickerSetKind) error
	ValidateAdminAddStickerToSet(ctx context.Context, setID int64, emoji string) error
	AdminUploadStickerMaterial(ctx context.Context, fileName string, data []byte) (domain.Document, error)
	AdminCreateStickerSet(ctx context.Context, req domain.CreateStickerSetRequest) (domain.StickerSet, []domain.Document, error)
	AdminAddStickerToSet(ctx context.Context, setID int64, item domain.StickerSetItemInput) (domain.StickerSet, []domain.Document, error)
	AdminRemoveStickerFromSet(ctx context.Context, setID int64, documentID int64) (domain.StickerSet, []domain.Document, error)
}

type GifCatalogService interface {
	ValidateGifUpload(fileName string, data []byte) (string, bool)
	AdminUploadGifMaterial(ctx context.Context, fileName string, data []byte) (domain.Document, error)
	AdminCreateGifCatalogEntry(ctx context.Context, title string, documentID int64) (domain.GifCatalogEntry, error)
	AdminListGifCatalog(ctx context.Context) ([]domain.GifCatalogEntry, error)
	AdminSetGifCatalogEnabled(ctx context.Context, id int64, enabled bool) (bool, error)
	AdminSetGifCatalogSortOrder(ctx context.Context, id int64, order int) (bool, error)
	AdminDeleteGifCatalogEntry(ctx context.Context, id int64) (bool, error)
	GetFile(ctx context.Context, req domain.FileDownloadRequest) (domain.FileChunk, bool, error)
}

type ModerationService interface {
	ListCases(ctx context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error)
	Case(ctx context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error)
	Report(ctx context.Context, reportID int64) (domain.ModerationReport, bool, error)
	ClaimCase(ctx context.Context, caseID, expectedVersion int64, actor string, now time.Time) (domain.ModerationCase, error)
	DecideCase(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error)
	SubmitAppeal(ctx context.Context, caseID, appellantUserID int64, text string, now time.Time) (domain.ModerationAppeal, bool, error)
	ReviewAppeal(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error)
}

// CollectibleUsernamesService is the operator-facing slice of the collectible
// username use cases: the mint/transfer/revoke lifecycle plus the reads the
// admin panel explains an asset with. It is deliberately narrow -- the client
// facing toggle/reorder entry points stay out of the admin surface, because the
// editable slot and the row order belong to the peer, not to the operator.
type CollectibleUsernamesService interface {
	Mint(ctx context.Context, req domain.MintCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error)
	Transfer(ctx context.Context, req domain.TransferCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error)
	Revoke(ctx context.Context, req domain.RevokeCollectibleUsernameRequest) (domain.CollectibleUsername, bool, error)
	Delete(ctx context.Context, req domain.DeleteCollectibleUsernameRequest) (bool, error)
	Collectible(ctx context.Context, username string) (domain.CollectibleUsername, error)
	List(ctx context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error)
	Transfers(ctx context.Context, collectibleID int64, limit int) ([]domain.CollectibleUsernameTransfer, error)
}

type CollectiblePhonesService interface {
	MintCollectiblePhone(context.Context, domain.MintCollectiblePhoneRequest) (domain.CollectiblePhone, bool, error)
	UpdateCollectiblePhonePrice(context.Context, domain.UpdateCollectiblePhonePriceRequest) (domain.CollectiblePhone, bool, error)
	TransferCollectiblePhone(context.Context, domain.TransferCollectiblePhoneRequest) (domain.CollectiblePhone, bool, error)
	RevokeCollectiblePhone(context.Context, domain.RevokeCollectiblePhoneRequest) (domain.CollectiblePhone, bool, error)
	DeleteCollectiblePhone(context.Context, domain.DeleteCollectiblePhoneRequest) (bool, error)
	CollectiblePhone(context.Context, string) (domain.CollectiblePhone, error)
	CollectiblePhoneByID(context.Context, int64) (domain.CollectiblePhone, error)
	ListCollectiblePhones(context.Context, domain.CollectiblePhoneFilter) ([]domain.CollectiblePhone, error)
	CollectiblePhoneTransfers(context.Context, int64, int) ([]domain.CollectiblePhoneTransfer, error)
}

// collectibleUsernameByIDLookup is the optional by-identity read. Stores that
// expose it answer a detail request in one round trip; the keyset fallback in
// CollectibleUsernameByID keeps a service without it correct.
type collectibleUsernameByIDLookup interface {
	CollectibleUsernameByID(ctx context.Context, id int64) (domain.CollectibleUsername, error)
}

// AccountRatingService is the operator-facing slice of the composite account
// rating use cases: read the stored projection, force a recompute, adjust the
// manual component and page the ledger that explains a level.
type AccountRatingService interface {
	Rating(ctx context.Context, userID int64) (domain.AccountRating, error)
	Recompute(ctx context.Context, userID int64) (domain.AccountRating, error)
	Adjust(ctx context.Context, req domain.AdjustAccountRatingRequest) (domain.AccountRating, bool, error)
	List(ctx context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error)
	Events(ctx context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error)
}

// AutoSubscribeService backs the admin panel's auto-subscribe channel list --
// see internal/app/autosubscribe's own package doc for the full semantics.
type AutoSubscribeService interface {
	List(ctx context.Context) ([]domain.AutoSubscribeChannel, error)
	Add(ctx context.Context, channelID int64, addedBy string) (added bool, joined int, err error)
	Remove(ctx context.Context, channelID int64) (removed bool, err error)
}

// GiftGranter delivers a catalog gift to a recipient peer on behalf of a sender
// without charging Stars. Implemented by the RPC router, it reuses the standard
// gift-delivery path (service message for users, saved-gift + admin log for
// channels) so granted gifts are indistinguishable from paid ones.
type GiftGranter interface {
	AdminGrantStarGift(ctx context.Context, grant domain.AdminStarGiftGrant) error
}

type Dependencies struct {
	Commands               CommandRepository
	Restrictions           RestrictionStore
	Auth                   AuthService
	Revoker                AuthKeyRevoker
	Users                  UsersService
	Account                AccountService
	Photos                 AvatarResolver
	Stars                  StarsService
	Premium                PremiumService
	StarsNotifier          StarsNotifier
	UserNotifier           UserNotifier
	UserModerationNotifier UserModerationNotifier
	FreezeNotifier         AccountFreezeNotifier
	Channels               ChannelsService
	ChannelNotifier        ChannelNotifier
	Messages               MessagesService
	Gifts                  GiftsService
	GiftGranter            GiftGranter
	OfficialGifts          OfficialGiftsSource
	Bots                   BotService
	Broadcast              BroadcastService
	Emoji                  EmojiService
	StickerSets            StickerSetsService
	GifCatalog             GifCatalogService
	Moderation             ModerationService
	Usernames              CollectibleUsernamesService
	CollectiblePhones      CollectiblePhonesService
	Rating                 AccountRatingService
	Verification           VerificationService
	// BotVerification is the third-party mechanism, wired separately from
	// Verification: the two never read each other's state.
	BotVerification BotVerificationService
	AutoSubscribe   AutoSubscribeService
	Now             func() time.Time
}

type Service struct {
	commands               CommandRepository
	restrictions           RestrictionStore
	auth                   AuthService
	revoker                AuthKeyRevoker
	users                  UsersService
	account                AccountService
	photos                 AvatarResolver
	stars                  StarsService
	premium                PremiumService
	starsNotifier          StarsNotifier
	userNotifier           UserNotifier
	userModerationNotifier UserModerationNotifier
	freezeNotifier         AccountFreezeNotifier
	channels               ChannelsService
	channelNotifier        ChannelNotifier
	messages               MessagesService
	gifts                  GiftsService
	giftGranter            GiftGranter
	officialGifts          OfficialGiftsSource
	bots                   BotService
	broadcast              BroadcastService
	emoji                  EmojiService
	stickerSets            StickerSetsService
	gifCatalog             GifCatalogService
	moderation             ModerationService
	usernames              CollectibleUsernamesService
	collectiblePhones      CollectiblePhonesService
	rating                 AccountRatingService
	verification           VerificationService
	botVerification        BotVerificationService
	autoSubscribe          AutoSubscribeService
	now                    func() time.Time
}

func NewService(deps Dependencies) *Service {
	s := &Service{now: time.Now}
	return s.Configure(deps)
}

func (s *Service) Configure(deps Dependencies) *Service {
	if deps.Commands != nil {
		s.commands = deps.Commands
	}
	if deps.Restrictions != nil {
		s.restrictions = deps.Restrictions
	}
	if deps.Auth != nil {
		s.auth = deps.Auth
	}
	if deps.Revoker != nil {
		s.revoker = deps.Revoker
	}
	if deps.Users != nil {
		s.users = deps.Users
	}
	if deps.Account != nil {
		s.account = deps.Account
	}
	if deps.Photos != nil {
		s.photos = deps.Photos
	}
	if deps.Stars != nil {
		s.stars = deps.Stars
	}
	if deps.Premium != nil {
		s.premium = deps.Premium
	}
	if deps.StarsNotifier != nil {
		s.starsNotifier = deps.StarsNotifier
	}
	if deps.UserNotifier != nil {
		s.userNotifier = deps.UserNotifier
	}
	if deps.UserModerationNotifier != nil {
		s.userModerationNotifier = deps.UserModerationNotifier
	}
	if deps.FreezeNotifier != nil {
		s.freezeNotifier = deps.FreezeNotifier
	}
	if deps.Channels != nil {
		s.channels = deps.Channels
	}
	if deps.ChannelNotifier != nil {
		s.channelNotifier = deps.ChannelNotifier
	}
	if deps.Messages != nil {
		s.messages = deps.Messages
	}
	if deps.Gifts != nil {
		s.gifts = deps.Gifts
	}
	if deps.GiftGranter != nil {
		s.giftGranter = deps.GiftGranter
	}
	if deps.OfficialGifts != nil {
		s.officialGifts = deps.OfficialGifts
	}
	if deps.Bots != nil {
		s.bots = deps.Bots
	}
	if deps.Broadcast != nil {
		s.broadcast = deps.Broadcast
	}
	if deps.Emoji != nil {
		s.emoji = deps.Emoji
	}
	if deps.StickerSets != nil {
		s.stickerSets = deps.StickerSets
	}
	if deps.GifCatalog != nil {
		s.gifCatalog = deps.GifCatalog
	}
	if deps.Moderation != nil {
		s.moderation = deps.Moderation
	}
	if deps.Usernames != nil {
		s.usernames = deps.Usernames
	}
	if deps.CollectiblePhones != nil {
		s.collectiblePhones = deps.CollectiblePhones
	}
	if deps.Rating != nil {
		s.rating = deps.Rating
	}
	if deps.Verification != nil {
		s.verification = deps.Verification
	}
	if deps.BotVerification != nil {
		s.botVerification = deps.BotVerification
	}
	if deps.AutoSubscribe != nil {
		s.autoSubscribe = deps.AutoSubscribe
	}
	if deps.Now != nil {
		s.now = deps.Now
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

func (s *Service) ModerationCases(ctx context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error) {
	if s == nil || s.moderation == nil {
		return nil, fmt.Errorf("moderation dependency is not configured")
	}
	return s.moderation.ListCases(ctx, filter)
}

func (s *Service) ModerationCase(ctx context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.moderation == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation dependency is not configured")
	}
	return s.moderation.Case(ctx, caseID)
}

func (s *Service) ModerationReport(ctx context.Context, reportID int64) (domain.ModerationReport, bool, error) {
	if s == nil || s.moderation == nil {
		return domain.ModerationReport{}, false, fmt.Errorf("moderation dependency is not configured")
	}
	return s.moderation.Report(ctx, reportID)
}

func (s *Service) ClaimModerationCase(ctx context.Context, caseID, expectedVersion int64, actor string) (domain.ModerationCase, error) {
	if s == nil || s.moderation == nil {
		return domain.ModerationCase{}, fmt.Errorf("moderation dependency is not configured")
	}
	return s.moderation.ClaimCase(ctx, caseID, expectedVersion, actor, s.now().UTC())
}

func (s *Service) DecideModerationCase(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.moderation == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation dependency is not configured")
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = s.now().UTC()
	}
	return s.moderation.DecideCase(ctx, request)
}

func (s *Service) SubmitModerationAppeal(ctx context.Context, caseID, appellantUserID int64, text string) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.moderation == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation dependency is not configured")
	}
	return s.moderation.SubmitAppeal(ctx, caseID, appellantUserID, text, s.now().UTC())
}

func (s *Service) ReviewModerationAppeal(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.moderation == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation dependency is not configured")
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = s.now().UTC()
	}
	return s.moderation.ReviewAppeal(ctx, request)
}

// CollectibleUsernames is the admin listing read. The filter is passed through
// unchanged: the use-case layer owns normalisation and the page bound, so the
// admin API and the RPC edge page the registry identically.
func (s *Service) CollectibleUsernames(ctx context.Context, filter domain.CollectibleUsernameFilter) ([]domain.CollectibleUsername, error) {
	if s == nil || s.usernames == nil {
		return nil, fmt.Errorf("collectible username dependency is not configured")
	}
	return s.usernames.List(ctx, filter)
}

// CollectibleUsername resolves one asset by name.
func (s *Service) CollectibleUsername(ctx context.Context, username string) (domain.CollectibleUsername, error) {
	if s == nil || s.usernames == nil {
		return domain.CollectibleUsername{}, fmt.Errorf("collectible username dependency is not configured")
	}
	return s.usernames.Collectible(ctx, username)
}

// CollectibleUsernameByID resolves one asset by identity, which is how the admin
// panel links a row to its detail view.
//
// A service exposing the direct by-identity read is used as-is. Otherwise the
// bounded keyset listing answers it: the listing is ordered by descending id, so
// the single row taken before id+1 is the asset itself whenever it exists, and
// any other id proves the asset is gone.
func (s *Service) CollectibleUsernameByID(ctx context.Context, id int64) (domain.CollectibleUsername, error) {
	if s == nil || s.usernames == nil {
		return domain.CollectibleUsername{}, fmt.Errorf("collectible username dependency is not configured")
	}
	if id <= 0 {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	if lookup, ok := s.usernames.(collectibleUsernameByIDLookup); ok {
		return lookup.CollectibleUsernameByID(ctx, id)
	}
	before := int64(0)
	if id < math.MaxInt64 {
		before = id + 1
	}
	items, err := s.usernames.List(ctx, domain.CollectibleUsernameFilter{BeforeID: before, Limit: 1})
	if err != nil {
		return domain.CollectibleUsername{}, err
	}
	if len(items) == 0 || items[0].ID != id {
		return domain.CollectibleUsername{}, domain.ErrCollectibleUsernameNotFound
	}
	return items[0], nil
}

// CollectibleUsernameTransfers returns one asset's provenance log, newest first.
func (s *Service) CollectibleUsernameTransfers(ctx context.Context, collectibleID int64, limit int) ([]domain.CollectibleUsernameTransfer, error) {
	if s == nil || s.usernames == nil {
		return nil, fmt.Errorf("collectible username dependency is not configured")
	}
	return s.usernames.Transfers(ctx, collectibleID, limit)
}

// AccountRating returns one user's stored composite rating projection.
func (s *Service) AccountRating(ctx context.Context, userID int64) (domain.AccountRating, error) {
	if s == nil || s.rating == nil {
		return domain.AccountRating{}, fmt.Errorf("account rating dependency is not configured")
	}
	return s.rating.Rating(ctx, userID)
}

// AccountRatings is the admin leaderboard read.
func (s *Service) AccountRatings(ctx context.Context, filter domain.AccountRatingFilter) ([]domain.AccountRating, error) {
	if s == nil || s.rating == nil {
		return nil, fmt.Errorf("account rating dependency is not configured")
	}
	return s.rating.List(ctx, filter)
}

// AccountRatingEvents returns the contribution ledger that explains a level.
func (s *Service) AccountRatingEvents(ctx context.Context, userID int64, limit int) ([]domain.AccountRatingEvent, error) {
	if s == nil || s.rating == nil {
		return nil, fmt.Errorf("account rating dependency is not configured")
	}
	return s.rating.Events(ctx, userID, limit)
}

type CommandMeta struct {
	CommandID string `json:"command_id"`
	Actor     string `json:"actor"`
	Reason    string `json:"reason"`
	DryRun    bool   `json:"dry_run"`
}

type CommandResult struct {
	CommandID       string         `json:"command_id"`
	Action          string         `json:"action"`
	Status          string         `json:"status"`
	AlreadyExecuted bool           `json:"already_executed"`
	DryRun          bool           `json:"dry_run"`
	TargetUserID    int64          `json:"target_user_id,omitempty"`
	TargetPeer      domain.Peer    `json:"target_peer,omitempty"`
	Message         string         `json:"message"`
	Details         map[string]any `json:"details,omitempty"`
	Error           string         `json:"error,omitempty"`
	// transientDetails are returned to the initiating caller only. They are
	// deliberately excluded from JSON so credentials can never enter command
	// replay or audit storage.
	transientDetails map[string]any
}

type ImportStarGiftRequest struct {
	CommandMeta
	GiftID       int64  `json:"gift_id,omitempty"`
	Title        string `json:"title"`
	Stars        int64  `json:"stars"`
	ConvertStars int64  `json:"convert_stars"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sort_order"`
	FileName     string `json:"file_name"`
	ContentSHA   string `json:"content_sha256"`
	Data         []byte `json:"-"`

	// Optional lifecycle authoring for the auction panel and scheduled-release
	// ("отложенный дроп") surfaces. Zero values describe an ordinary gift.
	// Validated by domain.StarGiftCatalogWrite.ValidateLifecycleAuthoring.
	// For auctions Stars is the starting/minimum bid and AvailabilityTotal is
	// the supply; AuctionRoundDuration is seconds (0 => engine default).
	Auction              bool   `json:"auction,omitempty"`
	AuctionSlug          string `json:"auction_slug,omitempty"`
	GiftsPerRound        int    `json:"gifts_per_round,omitempty"`
	AuctionStartDate     int    `json:"auction_start_date,omitempty"`
	AuctionRoundDuration int    `json:"auction_round_duration,omitempty"`
	AvailabilityTotal    int    `json:"availability_total,omitempty"`
	LockedUntilDate      int    `json:"locked_until_date,omitempty"`
}

type ImportOfficialStarGiftRequest struct {
	CommandMeta
	SourceGiftID       string `json:"source_gift_id"`
	GiftID             int64  `json:"gift_id,omitempty"`
	Title              string `json:"title"`
	Stars              int64  `json:"stars"`
	ConvertStars       int64  `json:"convert_stars"`
	Enabled            bool   `json:"enabled"`
	SortOrder          int    `json:"sort_order"`
	IncludeCollectible bool   `json:"include_collectible"`
	UpgradeStars       int64  `json:"upgrade_stars,omitempty"`
	SupplyTotal        int    `json:"supply_total,omitempty"`
	SlugPrefix         string `json:"slug_prefix,omitempty"`
	// AvailabilityTotal caps how many REGULAR (not-yet-upgraded) copies of
	// this gift are purchasable -- distinct from SupplyTotal above, which
	// only bounds the collectible/upgrade pool. Previously only auctions
	// could ever get a real regular-gift cap here (see
	// StarGiftCatalogWrite.NormalizeLifecycleAuthoring's own fix); this is
	// the same "everywhere gifts exist" cap applied to an ordinary
	// (non-auction) official import. Ignored for an auction import, which
	// already derives its own supply from the snapshot (see auctionTotal
	// below).
	AvailabilityTotal int `json:"availability_total,omitempty"`
	// LockedUntilDate schedules the local release of an imported official gift.
	// Zero keeps whatever release time the snapshot carries. Validated in
	// ImportOfficialStarGift, which requires a future timestamp.
	LockedUntilDate int      `json:"locked_until_date,omitempty"`
	ManifestSHA256  string   `json:"manifest_sha256,omitempty"`
	AssetSHA256     []string `json:"asset_sha256,omitempty"`
}

type SetStarGiftEnabledRequest struct {
	CommandMeta
	GiftID  int64 `json:"gift_id"`
	Enabled bool  `json:"enabled"`
}

type SetStarGiftSortOrderRequest struct {
	CommandMeta
	GiftID    int64 `json:"gift_id"`
	SortOrder int   `json:"sort_order"`
}

// DeleteStarGiftRequest removes a catalog gift everywhere it's visible to
// users -- the catalog entry, every current owner's copy, every live
// listing/pending offer -- while keeping the official gift snapshot (so it
// can be re-imported later) and every audit/ledger table untouched. See
// domain.StarGiftDeletionResult and the postgres store's own doc for the
// exact mechanics. Refund, when true, credits Stars back through the same
// dry-run preview this request always computes (see Details in the
// CommandResult): purchase + transfer + upgrade, one sum per user, read
// from the actual command tables so it's scoped to only this gift.
type DeleteStarGiftRequest struct {
	CommandMeta
	GiftID int64 `json:"gift_id,string"`
	Refund bool  `json:"refund"`
}

// GiveGiftRequest grants a catalog gift to a recipient (user or channel) from
// the official system account 777000 at no charge.
// Exactly one of UserID / ChannelID identifies the recipient.
type GiveGiftRequest struct {
	CommandMeta
	SenderUserID        int64  `json:"sender_user_id"`
	UserID              int64  `json:"user_id"`
	ChannelID           int64  `json:"channel_id"`
	GiftID              int64  `json:"gift_id"`
	HideName            bool   `json:"hide_name"`
	Message             string `json:"message"`
	Upgrade             bool   `json:"upgrade"`
	ModelAttributeID    int64  `json:"model_attribute_id"`
	PatternAttributeID  int64  `json:"pattern_attribute_id"`
	BackdropAttributeID int64  `json:"backdrop_attribute_id"`
}

type StarGiftCollectibleAnimationUpload struct {
	Name           string `json:"name"`
	RarityPermille int    `json:"rarity_permille"`
	SortOrder      int    `json:"sort_order"`
	FileKey        string `json:"file_key"`
	FileName       string `json:"file_name,omitempty"`
	ContentSHA     string `json:"content_sha256,omitempty"`
	Data           []byte `json:"-"`
}

type StarGiftCollectibleBackdropInput struct {
	Name           string `json:"name"`
	BackdropID     int    `json:"backdrop_id"`
	CenterColor    int    `json:"center_color"`
	EdgeColor      int    `json:"edge_color"`
	PatternColor   int    `json:"pattern_color"`
	TextColor      int    `json:"text_color"`
	RarityPermille int    `json:"rarity_permille"`
	SortOrder      int    `json:"sort_order"`
}

type PublishStarGiftCollectiblesRequest struct {
	CommandMeta
	GiftID       int64                                `json:"gift_id"`
	UpgradeStars int64                                `json:"upgrade_stars"`
	SupplyTotal  int                                  `json:"supply_total"`
	SlugPrefix   string                               `json:"slug_prefix"`
	Models       []StarGiftCollectibleAnimationUpload `json:"models"`
	Patterns     []StarGiftCollectibleAnimationUpload `json:"patterns"`
	Backdrops    []StarGiftCollectibleBackdropInput   `json:"backdrops"`
}

type SetAccountFrozenRequest struct {
	CommandMeta
	UserID    int64     `json:"user_id"`
	Frozen    bool      `json:"frozen"`
	Until     time.Time `json:"freeze_until,omitempty"`
	AppealURL string    `json:"freeze_appeal_url,omitempty"`
}

type GrantPremiumRequest struct {
	CommandMeta
	UserID        int64 `json:"user_id"`
	Months        int   `json:"months"`
	EntitlementID int64 `json:"entitlement_id,omitempty"`
}

type RefundPremiumRequest struct {
	CommandMeta
	PaymentIntentID int64 `json:"payment_intent_id"`
}

type UpsertPremiumPlanRequest struct {
	CommandMeta
	Months          int    `json:"months"`
	DurationDays    int    `json:"duration_days"`
	AmountStars     int64  `json:"amount_stars"`
	FiatCurrency    string `json:"fiat_currency"`
	FiatAmount      int64  `json:"fiat_amount"`
	StoreProduct    string `json:"store_product"`
	StoreQuantity   int    `json:"store_quantity"`
	Enabled         bool   `json:"enabled"`
	SortOrder       int    `json:"sort_order"`
	Label           string `json:"label"`
	ExpectedVersion int64  `json:"expected_version"`
}

type GrantStarsRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	Amount int64 `json:"amount"`
}

type DebitStarsRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	Amount int64 `json:"amount"`
}

type SetVerifiedRequest struct {
	CommandMeta
	UserID   int64 `json:"user_id"`
	Verified bool  `json:"verified"`
}

type SetChannelVerifiedRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id"`
	Verified  bool  `json:"verified"`
}

type SetUserFlagsRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	Scam   bool  `json:"scam"`
	Fake   bool  `json:"fake"`
}

type SetChannelFlagsRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id"`
	Scam      bool  `json:"scam"`
	Fake      bool  `json:"fake"`
}

type SetSupportRequest struct {
	CommandMeta
	UserID  int64 `json:"user_id"`
	Support bool  `json:"support"`
}

type SetUsernameRequest struct {
	CommandMeta
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
}

type SetProfileRequest struct {
	CommandMeta
	UserID    int64  `json:"user_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type SetPhoneRequest struct {
	CommandMeta
	UserID int64  `json:"user_id"`
	Phone  string `json:"phone"`
}

type SetLoginEmailRequest struct {
	CommandMeta
	UserID int64  `json:"user_id"`
	Email  string `json:"email"`
}

type SetAccountAvatarRequest struct {
	CommandMeta
	UserID        int64  `json:"user_id"`
	FileName      string `json:"file_name"`
	ContentSHA256 string `json:"content_sha256"`
	Data          []byte `json:"-"`
}

type SetChannelAvatarRequest struct {
	CommandMeta
	ChannelID     int64  `json:"channel_id"`
	FileName      string `json:"file_name"`
	ContentSHA256 string `json:"content_sha256"`
	Data          []byte `json:"-"`
}

type SetChannelUsernameRequest struct {
	CommandMeta
	ChannelID int64  `json:"channel_id"`
	Username  string `json:"username"`
}

type PeerColorInput struct {
	ForProfile        bool  `json:"for_profile"`
	HasColor          bool  `json:"has_color"`
	Color             int   `json:"color"`
	BackgroundEmojiID int64 `json:"background_emoji_id,string"`
}

type SetUserColorRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	PeerColorInput
}

type SetChannelColorRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id"`
	PeerColorInput
}

type EmojiStatusInput struct {
	DocumentID int64 `json:"document_id,string"`
	Until      int   `json:"until"`
}

type SetUserEmojiStatusRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	EmojiStatusInput
}

type SetChannelEmojiStatusRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id"`
	EmojiStatusInput
}

type SetChannelSettingsRequest struct {
	CommandMeta
	ChannelID          int64 `json:"channel_id"`
	Gigagroup          *bool `json:"gigagroup,omitempty"`
	AntiSpam           *bool `json:"antispam,omitempty"`
	ParticipantsHidden *bool `json:"participants_hidden,omitempty"`
	NoForwards         *bool `json:"noforwards,omitempty"`
	JoinToSend         *bool `json:"join_to_send,omitempty"`
	JoinRequest        *bool `json:"join_request,omitempty"`
	SlowmodeSeconds    *int  `json:"slowmode_seconds,omitempty"`
}

type CreateBotRequest struct {
	CommandMeta
	OwnerUserID int64  `json:"owner_user_id"`
	Name        string `json:"name"`
	Username    string `json:"username"`
}

type CreateBroadcastRequest struct {
	CommandMeta
	Message    string  `json:"message"`
	TargetMode string  `json:"target_mode"`
	UserIDs    []int64 `json:"user_ids,omitempty"`
}

type SetStickerSetArchivedRequest struct {
	CommandMeta
	SetID    int64 `json:"set_id,string"`
	Archived bool  `json:"archived"`
}

type SetStickerSetSortOrderRequest struct {
	CommandMeta
	SetID     int64 `json:"set_id,string"`
	SortOrder int   `json:"sort_order"`
}

type RenameStickerSetRequest struct {
	CommandMeta
	SetID int64  `json:"set_id,string"`
	Title string `json:"title"`
}

type DeleteStickerSetRequest struct {
	CommandMeta
	SetID int64 `json:"set_id,string"`
}

type CreateStickerSetRequest struct {
	CommandMeta
	Title         string `json:"title"`
	ShortName     string `json:"short_name"`
	Kind          string `json:"kind"`
	Emoji         string `json:"emoji"`
	Keywords      string `json:"keywords,omitempty"`
	FileName      string `json:"file_name"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	Data          []byte `json:"-"`
}

type AddStickerToSetRequest struct {
	CommandMeta
	SetID         int64  `json:"set_id,string"`
	Emoji         string `json:"emoji"`
	Keywords      string `json:"keywords,omitempty"`
	FileName      string `json:"file_name"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	Data          []byte `json:"-"`
}

type RemoveStickerFromSetRequest struct {
	CommandMeta
	SetID      int64 `json:"set_id,string"`
	DocumentID int64 `json:"document_id,string"`
}

type CreateGifCatalogEntryRequest struct {
	CommandMeta
	Title         string `json:"title"`
	FileName      string `json:"file_name"`
	Data          []byte `json:"-"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
}

type SetGifCatalogEnabledRequest struct {
	CommandMeta
	ID      int64 `json:"id,string"`
	Enabled bool  `json:"enabled"`
}

type SetGifCatalogSortOrderRequest struct {
	CommandMeta
	ID        int64 `json:"id,string"`
	SortOrder int   `json:"sort_order"`
}

type DeleteGifCatalogEntryRequest struct {
	CommandMeta
	ID int64 `json:"id,string"`
}

type DeleteBotRequest struct {
	CommandMeta
	BotUserID int64 `json:"bot_user_id"`
}

type ExportBotTokenRequest struct {
	CommandMeta
	BotUserID int64 `json:"bot_user_id"`
}

// MintCollectibleUsernameRequest mints a collectible username asset. At most one
// of OwnerUserID / OwnerChannelID may be set: neither mints into the operator
// vault, one assigns the asset to that holder in the same command.
//
// Amount and CryptoAmount are minor units (nanotons for TON), so they cross the
// JSON boundary as decimal strings and stay exact. PurchaseDate is an optional
// Unix timestamp; zero is stamped with the command clock.
type MintCollectibleUsernameRequest struct {
	CommandMeta
	Username       string `json:"username"`
	OwnerUserID    int64  `json:"owner_user_id,string,omitempty"`
	OwnerChannelID int64  `json:"owner_channel_id,string,omitempty"`
	Currency       string `json:"currency"`
	Amount         int64  `json:"amount,string"`
	CryptoCurrency string `json:"crypto_currency,omitempty"`
	CryptoAmount   int64  `json:"crypto_amount,string,omitempty"`
	URL            string `json:"url,omitempty"`
	PurchaseDate   int64  `json:"purchase_date,omitempty"`
}

// TransferCollectibleUsernameRequest moves an asset out of the vault or between
// holders. Exactly one of ToUserID / ToChannelID identifies the new holder.
type TransferCollectibleUsernameRequest struct {
	CommandMeta
	Username    string `json:"username"`
	ToUserID    int64  `json:"to_user_id,string,omitempty"`
	ToChannelID int64  `json:"to_channel_id,string,omitempty"`
}

// RevokeCollectibleUsernameRequest returns an asset to the vault, or retires it
// permanently when Burn is set.
type RevokeCollectibleUsernameRequest struct {
	CommandMeta
	Username            string `json:"username"`
	ExpectedOwnerUserID int64  `json:"expected_owner_user_id,string,omitempty"`
	Burn                bool   `json:"burn"`
}

// DeleteCollectibleUsernameRequest erases a collectible asset entirely. Unlike a
// burn, which retires the asset and keeps its provenance, this drops the record
// and frees the name for a fresh issue -- the escape hatch for a mistaken mint.
type DeleteCollectibleUsernameRequest struct {
	CommandMeta
	Username string `json:"username"`
}

type MintCollectiblePhoneRequest struct {
	CommandMeta
	Phone          string                      `json:"phone"`
	Tier           domain.CollectiblePhoneTier `json:"tier"`
	OwnerUserID    int64                       `json:"owner_user_id,string,omitempty"`
	Currency       string                      `json:"currency"`
	Amount         int64                       `json:"amount,string"`
	CryptoCurrency string                      `json:"crypto_currency,omitempty"`
	CryptoAmount   int64                       `json:"crypto_amount,string,omitempty"`
	URL            string                      `json:"url,omitempty"`
	PurchaseDate   int64                       `json:"purchase_date,omitempty"`
}

type UpdateCollectiblePhonePriceRequest struct {
	CommandMeta
	Phone          string `json:"phone"`
	Currency       string `json:"currency"`
	Amount         int64  `json:"amount,string"`
	CryptoCurrency string `json:"crypto_currency"`
	CryptoAmount   int64  `json:"crypto_amount,string"`
}

type TransferCollectiblePhoneRequest struct {
	CommandMeta
	Phone    string `json:"phone"`
	ToUserID int64  `json:"to_user_id,string"`
}
type RevokeCollectiblePhoneRequest struct {
	CommandMeta
	Phone string `json:"phone"`
	Burn  bool   `json:"burn"`
}
type DeleteCollectiblePhoneRequest struct {
	CommandMeta
	Phone string `json:"phone"`
}

// RecomputeAccountRatingRequest forces one user's composite rating to be
// recomputed from the current contribution signals.
type RecomputeAccountRatingRequest struct {
	CommandMeta
	UserID int64 `json:"user_id,string"`
}

// AdjustAccountRatingRequest moves one user's manual rating component by a
// signed delta. The delta survives recomputes, so it is the operator's durable
// override rather than a one-off nudge.
type AdjustAccountRatingRequest struct {
	CommandMeta
	UserID int64 `json:"user_id,string"`
	Amount int64 `json:"amount,string"`
}

type RevokeSessionsRequest struct {
	CommandMeta
	UserID    int64 `json:"user_id"`
	Hash      int64 `json:"hash,omitempty"`
	KeepHash  int64 `json:"keep_hash,omitempty"`
	RevokeAll bool  `json:"revoke_all,omitempty"`
}

type DeletePrivateMessagesRequest struct {
	CommandMeta
	OwnerUserID int64       `json:"owner_user_id"`
	Peer        domain.Peer `json:"peer"`
	IDs         []int       `json:"ids"`
	Revoke      bool        `json:"revoke"`
}

type DeletePrivateHistoryRequest struct {
	CommandMeta
	OwnerUserID int64       `json:"owner_user_id"`
	Peer        domain.Peer `json:"peer"`
	MaxID       int         `json:"max_id,omitempty"`
	MinDate     int         `json:"min_date,omitempty"`
	MaxDate     int         `json:"max_date,omitempty"`
	JustClear   bool        `json:"just_clear,omitempty"`
	Revoke      bool        `json:"revoke"`
	MaxBatches  int         `json:"max_batches,omitempty"`
}

// AccountFreeze returns the durable account-level freeze state. A missing row
// is the only non-frozen default; invalid active rows are rejected by the
// store/schema instead of normalized on read.
func (s *Service) AccountFreeze(ctx context.Context, userID int64) (domain.AccountFreeze, bool, error) {
	if s == nil || s.restrictions == nil || userID == 0 {
		return domain.AccountFreeze{}, false, nil
	}
	freeze, found, err := s.restrictions.GetAccountFreeze(ctx, userID)
	if err != nil || !found {
		return freeze, found, err
	}
	if err := validateAccountFreeze(freeze); err != nil {
		return domain.AccountFreeze{}, false, fmt.Errorf("invalid durable account freeze for user %d: %w", userID, err)
	}
	return freeze, true, nil
}

// AccountFreezes is the bounded-query projection API used by user hydration.
// Production stores use array batches; lightweight test stores keep the exact
// same semantics through the single-row fallback.
func (s *Service) AccountFreezes(ctx context.Context, userIDs []int64) (map[int64]domain.AccountFreeze, error) {
	out := make(map[int64]domain.AccountFreeze)
	if s == nil || s.restrictions == nil || len(userIDs) == 0 {
		return out, nil
	}
	ids := uniqueFreezeUserIDs(userIDs)
	if batch, ok := s.restrictions.(accountFreezeBatchStore); ok {
		const batchSize = 1000
		for start := 0; start < len(ids); start += batchSize {
			end := min(start+batchSize, len(ids))
			items, err := batch.GetAccountFreezes(ctx, ids[start:end])
			if err != nil {
				return nil, err
			}
			for id, freeze := range items {
				if err := validateAccountFreeze(freeze); err != nil {
					return nil, fmt.Errorf("invalid durable account freeze for user %d: %w", id, err)
				}
				if freeze.Frozen {
					out[id] = freeze
				}
			}
		}
		return out, nil
	}
	for _, id := range ids {
		freeze, found, err := s.AccountFreeze(ctx, id)
		if err != nil {
			return nil, err
		}
		if found && freeze.Frozen {
			out[id] = freeze
		}
	}
	return out, nil
}

func uniqueFreezeUserIDs(userIDs []int64) []int64 {
	out := make([]int64, 0, len(userIDs))
	seen := make(map[int64]struct{}, len(userIDs))
	for _, id := range userIDs {
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

func (s *Service) ClaimAccountFreezeNotifications(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]domain.AccountFreezeNotification, error) {
	store, ok := s.restrictions.(accountFreezeNotificationStore)
	if !ok {
		return nil, nil
	}
	return store.ClaimAccountFreezeNotifications(ctx, now, limit, lease)
}

func (s *Service) CompleteAccountFreezeNotification(ctx context.Context, id, version int64, now time.Time) error {
	store, ok := s.restrictions.(accountFreezeNotificationStore)
	if !ok {
		return nil
	}
	return store.CompleteAccountFreezeNotification(ctx, id, version, now)
}

func validateAccountFreeze(freeze domain.AccountFreeze) error {
	if !freeze.Frozen {
		if !freeze.Since.IsZero() || !freeze.Until.IsZero() || freeze.AppealURL != "" {
			return fmt.Errorf("inactive freeze retains client-visible state")
		}
		return nil
	}
	if freeze.Since.IsZero() || freeze.Until.IsZero() || !freeze.Until.After(freeze.Since) ||
		freeze.Since.Unix() <= 0 || freeze.Until.Unix() > math.MaxInt32 {
		return fmt.Errorf("active freeze has invalid since/until")
	}
	if len(freeze.AppealURL) > maxFreezeAppealURLLength {
		return fmt.Errorf("active freeze appeal URL is too long")
	}
	parsed, err := url.ParseRequestURI(freeze.AppealURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("active freeze has invalid appeal URL")
	}
	return nil
}

func (s *Service) CanSendMessages(ctx context.Context, userID int64) error {
	freeze, found, err := s.AccountFreeze(ctx, userID)
	if err != nil {
		return err
	}
	if found && freeze.Frozen {
		return domain.ErrUserFrozen
	}
	return nil
}

func (s *Service) SetAccountFrozen(ctx context.Context, req SetAccountFrozenRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.restrictions == nil {
		return CommandResult{}, fmt.Errorf("admin restriction store is not configured")
	}
	now := s.now().UTC()
	appealURL := strings.TrimSpace(req.AppealURL)
	if req.Frozen {
		if req.Until.IsZero() || req.Until.Unix() > math.MaxInt32 {
			return CommandResult{}, fmt.Errorf("freeze_until must be a non-zero int32 Unix timestamp")
		}
		if len(appealURL) > maxFreezeAppealURLLength {
			return CommandResult{}, fmt.Errorf("freeze_appeal_url must be <= %d bytes", maxFreezeAppealURLLength)
		}
		parsed, err := url.ParseRequestURI(appealURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return CommandResult{}, fmt.Errorf("freeze_appeal_url must be an absolute HTTP(S) URL")
		}
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetAccountFrozen, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		// Keep this time-relative check inside runCommand: a completed command ID
		// must remain replayable after its deadline, while a new stale request is
		// recorded as failed and cannot mutate the restriction row.
		if req.Frozen && !req.Until.After(now) {
			return CommandResult{}, fmt.Errorf("freeze_until must be in the future")
		}
		prev, found, err := s.restrictions.GetAccountFreeze(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		next := domain.AccountFreeze{
			UserID:    req.UserID,
			Frozen:    req.Frozen,
			Reason:    req.Reason,
			Actor:     req.Actor,
			CommandID: req.CommandID,
		}
		if req.Frozen {
			next.Since = now
			if found && prev.Frozen {
				next.Since = prev.Since
			}
			next.Until = req.Until.UTC()
			next.AppealURL = appealURL
			if !next.Until.After(next.Since) {
				return CommandResult{}, fmt.Errorf("freeze_until must be after freeze_since")
			}
		}
		wouldChange := !found || prev.Frozen != next.Frozen ||
			!prev.Since.Equal(next.Since) || !prev.Until.Equal(next.Until) ||
			prev.AppealURL != next.AppealURL
		details := map[string]any{
			"previous_frozen": found && prev.Frozen,
			"new_frozen":      req.Frozen,
			"would_change":    wouldChange,
		}
		if req.Frozen {
			details["freeze_since"] = next.Since.Format(time.RFC3339)
			details["freeze_until"] = next.Until.Format(time.RFC3339)
			details["freeze_appeal_url"] = next.AppealURL
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.restrictions.SetAccountFreeze(ctx, next)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_at"] = updated.UpdatedAt.UTC().Format(time.RFC3339)
		details["version"] = updated.Version
		if err := s.notifyAccountFreezeChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "account freeze updated", Details: details}, nil
	})
}

func (s *Service) GrantPremium(ctx context.Context, req GrantPremiumRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.Months < 0 || req.Months > maxPremiumMonths {
		return CommandResult{}, fmt.Errorf("months must be between 0 and %d", maxPremiumMonths)
	}
	if req.EntitlementID < 0 || (req.Months > 0 && req.EntitlementID != 0) {
		return CommandResult{}, fmt.Errorf("entitlement_id is only valid when months is 0")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	if req.EntitlementID > 0 && s.premium == nil {
		return CommandResult{}, fmt.Errorf("exact premium entitlement revoke is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionGrantPremium, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		if u.Bot {
			return CommandResult{}, domain.ErrPremiumBotUnsupported
		}
		details := premiumCommandDetails(u, req.Months, s.now())
		if req.EntitlementID > 0 {
			details["entitlement_id"] = req.EntitlementID
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		var updated domain.User
		if s.premium != nil {
			actorID := premiumAdminActorID(req.Actor)
			if req.Months == 0 {
				updated, err = s.premium.Revoke(ctx, domain.PremiumAdminRevokeRequest{
					UserID: req.UserID, ActorUserID: actorID, Date: int(s.now().Unix()),
					Reason: req.Reason, CommandKey: strings.TrimSpace(req.CommandID), EntitlementID: req.EntitlementID,
				})
			} else {
				durationDays := req.Months * 30
				if plan, planErr := s.premium.Plan(ctx, req.Months); planErr == nil {
					durationDays = plan.DurationDays
				} else if !errors.Is(planErr, domain.ErrPremiumPlanUnavailable) {
					return CommandResult{}, planErr
				}
				var entitlement domain.PremiumEntitlement
				entitlement, updated, err = s.premium.Grant(ctx, domain.PremiumAdminGrantRequest{
					UserID: req.UserID, ActorUserID: actorID, Months: req.Months,
					DurationDays: durationDays, Date: int(s.now().Unix()),
					Reason: req.Reason, CommandKey: strings.TrimSpace(req.CommandID),
				})
				if err == nil {
					details["entitlement_id"] = entitlement.ID
				}
			}
		} else {
			updated, err = s.users.GrantPremium(ctx, req.UserID, req.Months)
		}
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_premium_until"] = updated.PremiumUntil
		details["updated_premium_active"] = updated.PremiumActiveAt(s.now().Unix())
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		msg := "premium updated"
		if req.Months == 0 {
			msg = "premium cleared"
		}
		return CommandResult{Message: msg, Details: details}, nil
	})
}

func (s *Service) RefundPremium(ctx context.Context, req RefundPremiumRequest) (CommandResult, error) {
	if req.PaymentIntentID <= 0 {
		return CommandResult{}, fmt.Errorf("payment_intent_id is required")
	}
	if s == nil || s.premium == nil {
		return CommandResult{}, fmt.Errorf("admin premium dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRefundPremium, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"payment_intent_id": req.PaymentIntentID}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		result, err := s.premium.Refund(ctx, domain.PremiumRefundRequest{
			PaymentIntentID: req.PaymentIntentID,
			ActorUserID:     premiumAdminActorID(req.Actor),
			Date:            int(s.now().Unix()),
			Reason:          req.Reason,
			CommandKey:      strings.TrimSpace(req.CommandID),
		})
		if err != nil {
			return CommandResult{}, err
		}
		details["buyer_user_id"] = result.Form.BuyerUserID
		details["recipient_user_id"] = result.Form.RecipientUserID
		details["amount_stars"] = result.Form.AmountStars
		details["months"] = result.Form.Months
		details["updated_premium_until"] = result.User.PremiumUntil
		details["updated_stars_balance"] = result.Balance.Balance
		if err := s.notifyUserChanged(ctx, result.User); err != nil {
			details["user_notify_error"] = err.Error()
		}
		if err := s.notifyStarsBalanceChanged(ctx, result.Balance); err != nil {
			details["stars_notify_error"] = err.Error()
		}
		return CommandResult{Message: "premium payment refunded", Details: details}, nil
	})
}

func (s *Service) PremiumEntitlements(
	ctx context.Context,
	userID int64,
	limit int,
) ([]domain.PremiumEntitlement, error) {
	if userID <= 0 {
		return nil, fmt.Errorf("user_id is required")
	}
	if s == nil || s.premium == nil {
		return nil, fmt.Errorf("admin premium dependency is not configured")
	}
	return s.premium.Entitlements(ctx, userID, limit)
}

func (s *Service) PremiumPayment(
	ctx context.Context,
	paymentIntentID int64,
) (domain.PremiumPaymentDetails, bool, error) {
	if paymentIntentID <= 0 {
		return domain.PremiumPaymentDetails{}, false, fmt.Errorf("payment_intent_id is required")
	}
	if s == nil || s.premium == nil {
		return domain.PremiumPaymentDetails{}, false, fmt.Errorf("admin premium dependency is not configured")
	}
	return s.premium.Payment(ctx, paymentIntentID)
}

func (s *Service) PremiumPlans(ctx context.Context) ([]domain.PremiumPlan, error) {
	if s == nil || s.premium == nil {
		return nil, fmt.Errorf("admin premium dependency is not configured")
	}
	return s.premium.Catalog(ctx)
}

func (s *Service) UpsertPremiumPlan(
	ctx context.Context,
	req UpsertPremiumPlanRequest,
) (CommandResult, error) {
	req.Label = strings.TrimSpace(req.Label)
	if s == nil || s.premium == nil {
		return CommandResult{}, fmt.Errorf("admin premium dependency is not configured")
	}
	validation := domain.PremiumPlanUpsertRequest{
		Months: req.Months, DurationDays: req.DurationDays, AmountStars: req.AmountStars,
		FiatCurrency: req.FiatCurrency, FiatAmount: req.FiatAmount,
		StoreProduct: req.StoreProduct, StoreQuantity: req.StoreQuantity,
		Enabled: req.Enabled, SortOrder: req.SortOrder, Label: req.Label,
		ExpectedVersion: req.ExpectedVersion, ActorUserID: 1, Date: 1,
		Reason: strings.TrimSpace(req.Reason), CommandKey: strings.TrimSpace(req.CommandID),
	}
	if !validation.Valid() {
		return CommandResult{}, domain.ErrPremiumPlanInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionUpsertPremiumPlan, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			details := map[string]any{
				"months": req.Months, "duration_days": req.DurationDays,
				"amount_stars": req.AmountStars, "enabled": req.Enabled,
				"fiat_currency": req.FiatCurrency, "fiat_amount": req.FiatAmount,
				"store_product": req.StoreProduct, "store_quantity": req.StoreQuantity,
				"sort_order": req.SortOrder, "label": req.Label,
				"expected_version": req.ExpectedVersion,
			}
			if req.DryRun {
				return CommandResult{Message: "premium plan validated", Details: details}, nil
			}
			updated, err := s.premium.UpsertPlan(ctx, domain.PremiumPlanUpsertRequest{
				Months: req.Months, DurationDays: req.DurationDays, AmountStars: req.AmountStars,
				FiatCurrency: req.FiatCurrency, FiatAmount: req.FiatAmount,
				StoreProduct: req.StoreProduct, StoreQuantity: req.StoreQuantity,
				Enabled: req.Enabled, SortOrder: req.SortOrder, Label: req.Label,
				ExpectedVersion: req.ExpectedVersion,
				ActorUserID:     premiumAdminActorID(req.Actor), Date: int(s.now().Unix()),
				Reason: req.Reason, CommandKey: req.CommandID,
			})
			if err != nil {
				return CommandResult{Details: details}, err
			}
			details["plan"] = updated
			return CommandResult{Message: "premium plan saved", Details: details}, nil
		})
}

func (s *Service) GrantStars(ctx context.Context, req GrantStarsRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.Amount <= 0 || req.Amount > maxStarsGrant {
		return CommandResult{}, fmt.Errorf("amount must be between 1 and %d", maxStarsGrant)
	}
	if s == nil || s.users == nil || s.stars == nil {
		return CommandResult{}, fmt.Errorf("admin stars dependencies are not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionGrantStars, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{
			"amount":       req.Amount,
			"username":     u.Username,
			"phone":        u.Phone,
			"would_credit": true,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		balance, err := s.stars.Credit(ctx, req.UserID, req.Amount, domain.StarsReasonAdjust, domain.Peer{}, "Admin Stars grant", req.Reason)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_balance"] = balance.Balance
		details["starting_grant_applied"] = balance.Granted
		if err := s.notifyStarsBalanceChanged(ctx, balance); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "stars granted", Details: details}, nil
	})
}

func (s *Service) DebitStars(ctx context.Context, req DebitStarsRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.Amount <= 0 || req.Amount > maxStarsGrant {
		return CommandResult{}, fmt.Errorf("amount must be between 1 and %d", maxStarsGrant)
	}
	if s == nil || s.users == nil || s.stars == nil {
		return CommandResult{}, fmt.Errorf("admin stars dependencies are not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDebitStars, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{"amount": req.Amount, "username": u.Username, "phone": u.Phone, "would_debit": true}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		balance, err := s.stars.Debit(ctx, req.UserID, req.Amount, domain.StarsReasonAdjust, domain.Peer{}, "Admin Stars debit", req.Reason)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_balance"] = balance.Balance
		if err := s.notifyStarsBalanceChanged(ctx, balance); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "stars debited", Details: details}, nil
	})
}

func (s *Service) SetVerified(ctx context.Context, req SetVerifiedRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if domain.IsSystemUserID(req.UserID) && !req.Verified {
		return CommandResult{}, fmt.Errorf("system user verification cannot be removed")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetVerified, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{
			"previous_verified": u.Verified,
			"new_verified":      req.Verified,
			"would_change":      u.Verified != req.Verified,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.SetVerified(ctx, req.UserID, req.Verified)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_verified"] = updated.Verified
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "verified updated", Details: details}, nil
	})
}

// SetUserFlags sets or clears the scam/fake moderation flags on a user (bots
// reuse the same path). Both flags are applied together from the desired state.
func (s *Service) SetUserFlags(ctx context.Context, req SetUserFlagsRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	if req.Scam && req.Fake {
		return CommandResult{}, domain.ErrPeerModerationFlagsInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetUserFlags, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{
			"previous_scam": u.Scam, "previous_fake": u.Fake,
			"new_scam": req.Scam, "new_fake": req.Fake,
			"would_change": u.Scam != req.Scam || u.Fake != req.Fake,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.SetScamFake(ctx, req.UserID, req.Scam, req.Fake)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_scam"] = updated.Scam
		details["updated_fake"] = updated.Fake
		if err := s.notifyUserModerationFlagsChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "user flags updated", Details: details}, nil
	})
}

// SetSupport sets or clears the official-support flag on a user.
func (s *Service) SetSupport(ctx context.Context, req SetSupportRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetSupport, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{"previous_support": u.Support, "new_support": req.Support, "would_change": u.Support != req.Support}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.SetSupport(ctx, req.UserID, req.Support)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_support"] = updated.Support
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "support updated", Details: details}, nil
	})
}

func collectibleAttrPresent(attrs []domain.StarGiftCollectibleAttribute, id int64) bool {
	for _, attr := range attrs {
		if attr.ID == id {
			return true
		}
	}
	return false
}

// GiveGift grants a catalog gift to a recipient (user or channel) from the
// official system account 777000 without charging any Stars. Delivery reuses
// the standard gift path via the GiftGranter dependency.
func (s *Service) GiveGift(ctx context.Context, req GiveGiftRequest) (CommandResult, error) {
	if req.GiftID <= 0 {
		return CommandResult{}, fmt.Errorf("gift_id is required")
	}
	if (req.UserID > 0) == (req.ChannelID > 0) {
		return CommandResult{}, fmt.Errorf("exactly one of user_id or channel_id is required")
	}
	if s == nil || s.giftGranter == nil {
		return CommandResult{}, fmt.Errorf("gift granter dependency is not configured")
	}
	sender := req.SenderUserID
	if sender <= 0 {
		sender = domain.OfficialSystemUserID
	}
	if sender != domain.OfficialSystemUserID {
		return CommandResult{}, fmt.Errorf("gift sender must be the official system account")
	}
	req.Message = strings.TrimSpace(req.Message)
	if len([]rune(req.Message)) > 128 {
		return CommandResult{}, fmt.Errorf("gift message must be <= 128 characters")
	}
	var recipient domain.Peer
	if req.ChannelID > 0 {
		recipient = domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	} else {
		recipient = domain.Peer{Type: domain.PeerTypeUser, ID: req.UserID}
	}
	if req.Upgrade && recipient.Type != domain.PeerTypeUser {
		return CommandResult{}, fmt.Errorf("upgraded gift delivery is supported for user recipients only")
	}
	if !req.Upgrade && (req.ModelAttributeID > 0 || req.PatternAttributeID > 0 || req.BackdropAttributeID > 0) {
		return CommandResult{}, fmt.Errorf("collectible attributes require upgrade")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionGiveGift, req.UserID, recipient, req, func() (CommandResult, error) {
		details := map[string]any{
			"sender_user_id": sender,
			"gift_id":        req.GiftID,
			"recipient_type": string(recipient.Type),
			"recipient_id":   recipient.ID,
			"hide_name":      req.HideName,
			"upgrade":        req.Upgrade,
		}
		if req.Message != "" {
			details["message"] = req.Message
		}
		if s.gifts != nil {
			gift, found, err := s.gifts.GiftByID(ctx, req.GiftID)
			if err != nil {
				return CommandResult{}, err
			}
			if !found {
				return CommandResult{}, fmt.Errorf("gift %d not found", req.GiftID)
			}
			details["gift_title"] = gift.Title
			details["gift_stars"] = gift.Stars
			if req.Upgrade {
				preview, ok, err := s.gifts.CollectiblePreview(ctx, req.GiftID)
				if err != nil {
					return CommandResult{}, err
				}
				if !ok || preview.UpgradeStars <= 0 {
					return CommandResult{}, fmt.Errorf("gift %d has no published collectible upgrade", req.GiftID)
				}
				if preview.Issued >= preview.SupplyTotal {
					return CommandResult{}, fmt.Errorf("gift %d collectible supply is exhausted", req.GiftID)
				}
				if req.ModelAttributeID > 0 && !collectibleAttrPresent(preview.Models, req.ModelAttributeID) {
					return CommandResult{}, fmt.Errorf("model attribute %d is not part of gift %d", req.ModelAttributeID, req.GiftID)
				}
				if req.PatternAttributeID > 0 && !collectibleAttrPresent(preview.Patterns, req.PatternAttributeID) {
					return CommandResult{}, fmt.Errorf("pattern attribute %d is not part of gift %d", req.PatternAttributeID, req.GiftID)
				}
				if req.BackdropAttributeID > 0 && !collectibleAttrPresent(preview.Backdrops, req.BackdropAttributeID) {
					return CommandResult{}, fmt.Errorf("backdrop attribute %d is not part of gift %d", req.BackdropAttributeID, req.GiftID)
				}
				details["collectible_supply_total"] = preview.SupplyTotal
				details["collectible_issued"] = preview.Issued
				if req.ModelAttributeID > 0 {
					details["model_attribute_id"] = req.ModelAttributeID
				}
				if req.PatternAttributeID > 0 {
					details["pattern_attribute_id"] = req.PatternAttributeID
				}
				if req.BackdropAttributeID > 0 {
					details["backdrop_attribute_id"] = req.BackdropAttributeID
				}
			}
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		if err := s.giftGranter.AdminGrantStarGift(ctx, domain.AdminStarGiftGrant{
			SenderID:            sender,
			Recipient:           recipient,
			GiftID:              req.GiftID,
			HideName:            req.HideName,
			Message:             req.Message,
			Upgrade:             req.Upgrade,
			CommandKey:          "admin-gift:" + req.CommandID,
			ModelAttributeID:    req.ModelAttributeID,
			PatternAttributeID:  req.PatternAttributeID,
			BackdropAttributeID: req.BackdropAttributeID,
		}); err != nil {
			return CommandResult{}, err
		}
		msg := "gift granted"
		if req.Upgrade {
			msg = "collectible gift granted"
		}
		return CommandResult{Message: msg, Details: details}, nil
	})
}

// SetUsername force-sets or clears (empty) a user/bot username. Format and
// availability are validated by the users service.
func (s *Service) SetUsername(ctx context.Context, req SetUsernameRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	username := strings.TrimSpace(strings.TrimPrefix(req.Username, "@"))
	req.Username = username
	return s.runCommand(ctx, req.CommandMeta, ActionSetUsername, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{"previous_username": u.Username, "new_username": username}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.UpdateUsername(ctx, req.UserID, username)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_username"] = updated.Username
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "username updated", Details: details}, nil
	})
}

func (s *Service) SetProfile(ctx context.Context, req SetProfileRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if domain.IsSystemUserID(req.UserID) {
		return CommandResult{}, fmt.Errorf("system user profile cannot be changed")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	return s.runCommand(ctx, req.CommandMeta, ActionSetProfile, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{
			"previous_first_name": u.FirstName,
			"previous_last_name":  u.LastName,
			"new_first_name":      req.FirstName,
			"new_last_name":       req.LastName,
			"would_change":        u.FirstName != req.FirstName || u.LastName != req.LastName,
		}
		if req.DryRun {
			return CommandResult{Message: "profile update validated", Details: details}, nil
		}
		updated, err := s.users.UpdateProfile(ctx, req.UserID, domain.UserProfileUpdate{
			FirstName: req.FirstName, HasFirstName: true,
			LastName: req.LastName, HasLastName: true,
		})
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "profile updated", Details: details}, nil
	})
}

func (s *Service) SetPhone(ctx context.Context, req SetPhoneRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	req.Phone = domain.NormalizePhone(req.Phone)
	if !domain.ValidPhone(req.Phone) {
		return CommandResult{}, domain.ErrPhoneNumberInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetPhone, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		if u.Bot || domain.IsSystemUserID(u.ID) {
			return CommandResult{}, domain.ErrPhoneChangeForbidden
		}
		details := map[string]any{
			"previous_phone": u.Phone,
			"new_phone":      req.Phone,
			"would_change":   u.Phone != req.Phone,
		}
		if req.DryRun {
			return CommandResult{Message: "phone update validated", Details: details}, nil
		}
		updated, err := s.users.SetPhone(ctx, req.UserID, req.Phone)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["changed"] = u.Phone != updated.Phone
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "phone updated", Details: details}, nil
	})
}

func (s *Service) SetLoginEmail(ctx context.Context, req SetLoginEmailRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil || s.account == nil {
		return CommandResult{}, fmt.Errorf("admin account dependencies are not configured")
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email != "" && !s.account.ValidLoginEmail(req.Email) {
		return CommandResult{}, domain.ErrEmailInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetLoginEmail, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		if u.Bot || domain.IsSystemUserID(u.ID) {
			return CommandResult{}, domain.ErrEmailInvalid
		}
		previous, _, err := s.account.LoginEmail(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"previous_login_email": previous,
			"new_login_email":      req.Email,
			"would_change":         !strings.EqualFold(previous, req.Email),
		}
		if req.DryRun {
			return CommandResult{Message: "login email update validated", Details: details}, nil
		}
		if req.Email == "" {
			err = s.account.ClearLoginEmail(ctx, req.UserID)
		} else {
			err = s.account.SetLoginEmail(ctx, req.UserID, req.Email)
		}
		if err != nil {
			return CommandResult{Details: details}, err
		}
		message := "login email updated"
		if req.Email == "" {
			message = "login email cleared"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

const MaxAccountAvatarBytes = 4 << 20

func (s *Service) SetAccountAvatar(ctx context.Context, req SetAccountAvatarRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if domain.IsSystemUserID(req.UserID) {
		return CommandResult{}, fmt.Errorf("system user avatar cannot be changed")
	}
	if s == nil || s.users == nil || s.photos == nil {
		return CommandResult{}, fmt.Errorf("admin avatar dependencies are not configured")
	}
	if len(req.Data) == 0 || len(req.Data) > MaxAccountAvatarBytes || !s.photos.ValidateAvatarUpload(req.Data) {
		return CommandResult{}, domain.ErrPhotoInvalid
	}
	digest := sha256.Sum256(req.Data)
	req.ContentSHA256 = hex.EncodeToString(digest[:])
	return s.runCommand(ctx, req.CommandMeta, ActionSetAccountAvatar, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		u, found, err := s.users.AdminUser(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, domain.ErrUserNotFound
		}
		details := map[string]any{
			"file_name":      req.FileName,
			"bytes":          len(req.Data),
			"content_sha256": req.ContentSHA256,
			"bot":            u.Bot,
		}
		if req.DryRun {
			return CommandResult{Message: "avatar update validated", Details: details}, nil
		}
		photo, err := s.photos.CreateAvatarFromBytes(ctx, req.Data)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if _, found, err := s.photos.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, req.UserID, domain.ProfilePhotoKindProfile, photo.ID, int(s.now().Unix())); err != nil {
			return CommandResult{Details: details}, err
		} else if !found {
			return CommandResult{Details: details}, domain.ErrPhotoInvalid
		}
		details["photo_id"] = strconv.FormatInt(photo.ID, 10)
		if err := s.notifyUserChanged(ctx, u); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "avatar updated", Details: details}, nil
	})
}

// SetUserColor force-sets or clears a user's name/profile color.
func (s *Service) SetUserColor(ctx context.Context, req SetUserColorRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	color := domain.PeerColor{HasColor: req.HasColor, Color: req.Color, BackgroundEmojiID: req.BackgroundEmojiID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetUserColor, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"for_profile": req.ForProfile, "has_color": req.HasColor, "color": req.Color}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.UpdateColor(ctx, req.UserID, req.ForProfile, color)
		if err != nil {
			return CommandResult{}, err
		}
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "user color updated", Details: details}, nil
	})
}

// SetUserEmojiStatus force-sets or clears (document_id=0) a user's emoji status.
func (s *Service) SetUserEmojiStatus(ctx context.Context, req SetUserEmojiStatusRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
	}
	status := domain.UserEmojiStatus{DocumentID: req.DocumentID, Until: req.Until}
	return s.runCommand(ctx, req.CommandMeta, ActionSetUserEmojiStatus, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"document_id": strconv.FormatInt(req.DocumentID, 10), "until": req.Until}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.UpdateEmojiStatus(ctx, req.UserID, status)
		if err != nil {
			return CommandResult{}, err
		}
		if err := s.notifyUserChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "user emoji status updated", Details: details}, nil
	})
}

// CreateBot provisions a new bot account owned by ownerUserID. The dry-run stage
// only validates the display name and username; the confirm stage creates the
// users+bots rows and returns the freshly minted token in the result details so
// the operator can copy it once.
func (s *Service) CreateBot(ctx context.Context, req CreateBotRequest) (CommandResult, error) {
	if s == nil || s.bots == nil {
		return CommandResult{}, fmt.Errorf("admin bot dependency is not configured")
	}
	if req.OwnerUserID <= 0 {
		return CommandResult{}, fmt.Errorf("owner_user_id is required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > domain.MaxBotNameLength {
		return CommandResult{}, domain.ErrBotNameInvalid
	}
	username := strings.TrimSpace(strings.TrimPrefix(req.Username, "@"))
	if !domain.ValidBotUsername(username) {
		return CommandResult{}, domain.ErrBotUsernameInvalid
	}
	req.Name = name
	req.Username = username
	return s.runCommand(ctx, req.CommandMeta, ActionCreateBot, req.OwnerUserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"owner_user_id": req.OwnerUserID,
			"name":          name,
			"username":      username,
		}
		if req.DryRun {
			return CommandResult{Message: "bot creation validated", Details: details}, nil
		}
		bot, token, err := s.bots.CreateBot(ctx, req.OwnerUserID, name, username)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["bot_user_id"] = bot.ID
		if err := s.notifyUserChanged(ctx, bot); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{
			Message:          "bot created",
			Details:          details,
			transientDetails: map[string]any{"token": token},
		}, nil
	})
}

// CreateBroadcast records only the durable campaign. Recipient enumeration and
// delivery are handled by the bounded background worker.
func (s *Service) CreateBroadcast(ctx context.Context, req CreateBroadcastRequest) (CommandResult, error) {
	if s == nil || s.broadcast == nil {
		return CommandResult{}, fmt.Errorf("admin broadcast dependency is not configured")
	}
	mode := domain.BroadcastTargetMode(strings.TrimSpace(req.TargetMode))
	return s.runCommand(ctx, req.CommandMeta, ActionCreateBroadcast, 0, domain.Peer{}, req, func() (CommandResult, error) {
		count, err := s.broadcast.Preview(ctx, req.Message, mode, req.UserIDs)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"target_mode":     string(mode),
			"recipient_count": count,
			"message_preview": truncateBroadcastPreview(req.Message),
		}
		if req.DryRun {
			return CommandResult{Message: "broadcast validated", Details: details}, nil
		}
		created, err := s.broadcast.Create(ctx, req.Message, mode, req.UserIDs, req.Actor)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["broadcast_id"] = created.ID
		details["recipient_count"] = created.TargetCount
		return CommandResult{Message: "broadcast created", Details: details}, nil
	})
}

func truncateBroadcastPreview(message string) string {
	runes := []rune(strings.TrimSpace(message))
	if len(runes) <= 120 {
		return string(runes)
	}
	return string(runes[:120]) + "…"
}

// DeleteBot permanently removes a user-created bot. The dry-run stage verifies
// the target is a non-system bot; the confirm stage tombstones the account and
// invalidates its token. System bots are rejected outright.
func (s *Service) DeleteBot(ctx context.Context, req DeleteBotRequest) (CommandResult, error) {
	if s == nil || s.bots == nil {
		return CommandResult{}, fmt.Errorf("admin bot dependency is not configured")
	}
	if req.BotUserID <= 0 {
		return CommandResult{}, fmt.Errorf("bot_user_id is required")
	}
	if domain.IsSystemUserID(req.BotUserID) {
		return CommandResult{}, fmt.Errorf("system bots cannot be deleted")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteBot, req.BotUserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"bot_user_id": req.BotUserID}
		if s.users != nil {
			u, found, err := s.users.AdminUser(ctx, req.BotUserID)
			if err != nil {
				return CommandResult{}, err
			}
			if !found || !u.Bot {
				return CommandResult{}, domain.ErrBotNotFound
			}
			details["username"] = u.Username
			details["name"] = u.FirstName
		}
		if req.DryRun {
			return CommandResult{Message: "bot deletion validated", Details: details}, nil
		}
		deleted, err := s.bots.DeleteBot(ctx, req.BotUserID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["deleted"] = true
		if err := s.notifyUserChanged(ctx, deleted); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "bot deleted", Details: details}, nil
	})
}

// ExportBotToken returns the current token only to this request. The secret is
// merged into the HTTP response after the persisted command result is encoded,
// so audit rows and completed-command replay never contain it.
func (s *Service) ExportBotToken(ctx context.Context, req ExportBotTokenRequest) (CommandResult, error) {
	if s == nil || s.bots == nil {
		return CommandResult{}, fmt.Errorf("admin bot dependency is not configured")
	}
	if req.BotUserID <= 0 {
		return CommandResult{}, fmt.Errorf("bot_user_id is required")
	}
	if domain.IsSystemUserID(req.BotUserID) {
		return CommandResult{}, fmt.Errorf("system bots have no exportable token")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionExportBotToken, req.BotUserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"bot_user_id": req.BotUserID}
		if req.DryRun {
			return CommandResult{Message: "token export validated", Details: details}, nil
		}
		token, err := s.bots.AdminExportBotToken(ctx, req.BotUserID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		return CommandResult{
			Message:          "token exported",
			Details:          details,
			transientDetails: map[string]any{"token": token},
		}, nil
	})
}

// MintCollectibleUsername creates a collectible username asset and optionally
// assigns it in the same command. Shape validation runs before the command is
// journalled; occupancy is checked inside it, so a dry-run reports a taken name
// without minting and a replay of a completed command stays idempotent.
func (s *Service) MintCollectibleUsername(ctx context.Context, req MintCollectibleUsernameRequest) (CommandResult, error) {
	if s == nil || s.usernames == nil {
		return CommandResult{}, fmt.Errorf("admin collectible username dependency is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	if !domain.ValidCollectibleUsername(req.Username) {
		return CommandResult{}, codedError(CodeUsernameInvalid, domain.ErrUsernameInvalid)
	}
	owner, err := collectibleOwnerPeer(req.OwnerUserID, req.OwnerChannelID)
	if err != nil {
		return CommandResult{}, err
	}
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.CryptoCurrency = strings.ToUpper(strings.TrimSpace(req.CryptoCurrency))
	if err := domain.ValidateCollectibleAmounts(req.Currency, req.Amount, req.CryptoCurrency, req.CryptoAmount); err != nil {
		return CommandResult{}, codedError(CodeCollectibleCurrencyInvalid, err)
	}
	req.URL = strings.TrimSpace(req.URL)
	if len(req.URL) > domain.MaxCollectibleUsernameURLLength {
		return CommandResult{}, fmt.Errorf("url must be <= %d bytes", domain.MaxCollectibleUsernameURLLength)
	}
	purchase, err := collectiblePurchaseDate(req.PurchaseDate, s.now)
	if err != nil {
		return CommandResult{}, err
	}
	req.PurchaseDate = purchase.Unix()
	return s.runCommand(ctx, req.CommandMeta, ActionMintCollectibleUsername, req.OwnerUserID, owner, req, func() (CommandResult, error) {
		details := map[string]any{
			"username":        req.Username,
			"owner_type":      string(owner.Type),
			"owner_id":        strconv.FormatInt(owner.ID, 10),
			"currency":        req.Currency,
			"amount":          strconv.FormatInt(req.Amount, 10),
			"crypto_currency": req.CryptoCurrency,
			"crypto_amount":   strconv.FormatInt(req.CryptoAmount, 10),
			"purchase_date":   purchase.Format(time.RFC3339),
			"url":             req.URL,
		}
		existing, err := s.usernames.Collectible(ctx, req.Username)
		switch {
		case err == nil:
			details["existing_collectible_id"] = strconv.FormatInt(existing.ID, 10)
			details["existing_status"] = string(existing.Status)
			return CommandResult{Details: details}, codedError(CodeUsernameOccupied, domain.ErrUsernameOccupied)
		case !errors.Is(err, domain.ErrCollectibleUsernameNotFound):
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		if req.DryRun {
			return CommandResult{Message: "collectible username mint validated", Details: details}, nil
		}
		asset, created, err := s.usernames.Mint(ctx, domain.MintCollectibleUsernameRequest{
			Username:       req.Username,
			Owner:          owner,
			PurchaseDate:   purchase,
			Currency:       req.Currency,
			Amount:         req.Amount,
			CryptoCurrency: req.CryptoCurrency,
			CryptoAmount:   req.CryptoAmount,
			URL:            req.URL,
			Actor:          req.Actor,
			Reason:         req.Reason,
			CommandKey:     "admin-collectible-mint:" + req.CommandID,
		})
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["collectible_id"] = strconv.FormatInt(asset.ID, 10)
		details["status"] = string(asset.Status)
		details["url"] = asset.URL
		details["created"] = created
		message := "collectible username minted"
		if !created {
			message = "collectible username mint replayed"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// TransferCollectibleUsername moves an asset to a new holder. The asset must
// exist and must not be burned; the store keeps the move atomic with the
// receiving peer's username registry row.
func (s *Service) TransferCollectibleUsername(ctx context.Context, req TransferCollectibleUsernameRequest) (CommandResult, error) {
	if s == nil || s.usernames == nil {
		return CommandResult{}, fmt.Errorf("admin collectible username dependency is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	if !domain.ValidCollectibleUsername(req.Username) {
		return CommandResult{}, codedError(CodeUsernameInvalid, domain.ErrUsernameInvalid)
	}
	to, err := collectibleOwnerPeer(req.ToUserID, req.ToChannelID)
	if err != nil {
		return CommandResult{}, err
	}
	if to.Type == "" {
		return CommandResult{}, fmt.Errorf("exactly one of to_user_id or to_channel_id is required")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionTransferCollectibleUsername, req.ToUserID, to, req, func() (CommandResult, error) {
		details := map[string]any{
			"username": req.Username,
			"to_type":  string(to.Type),
			"to_id":    strconv.FormatInt(to.ID, 10),
		}
		asset, err := s.usernames.Collectible(ctx, req.Username)
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["collectible_id"] = strconv.FormatInt(asset.ID, 10)
		details["previous_status"] = string(asset.Status)
		details["previous_owner_type"] = string(asset.Owner.Type)
		details["previous_owner_id"] = strconv.FormatInt(asset.Owner.ID, 10)
		details["transfer_count"] = asset.TransferCount
		if asset.Status == domain.CollectibleUsernameStatusBurned {
			return CommandResult{Details: details}, codedError(CodeCollectibleBurned, domain.ErrCollectibleUsernameBurned)
		}
		details["would_change"] = !asset.Owned() || asset.Owner != to
		if req.DryRun {
			return CommandResult{Message: "collectible username transfer validated", Details: details}, nil
		}
		updated, changed, err := s.usernames.Transfer(ctx, domain.TransferCollectibleUsernameRequest{
			Username:   req.Username,
			To:         to,
			Actor:      req.Actor,
			Reason:     req.Reason,
			CommandKey: "admin-collectible-transfer:" + req.CommandID,
		})
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["status"] = string(updated.Status)
		details["owner_type"] = string(updated.Owner.Type)
		details["owner_id"] = strconv.FormatInt(updated.Owner.ID, 10)
		details["changed"] = changed
		message := "collectible username transferred"
		if !changed {
			message = "collectible username transfer was a no-op"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// RevokeCollectibleUsername returns an asset to the operator vault, or burns it
// permanently when Burn is set. Revoking an asset nobody holds is rejected:
// there is nothing to take back, and a silent no-op would read as success.
func (s *Service) RevokeCollectibleUsername(ctx context.Context, req RevokeCollectibleUsernameRequest) (CommandResult, error) {
	if s == nil || s.usernames == nil {
		return CommandResult{}, fmt.Errorf("admin collectible username dependency is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	if !domain.ValidCollectibleUsername(req.Username) {
		return CommandResult{}, codedError(CodeUsernameInvalid, domain.ErrUsernameInvalid)
	}
	if req.ExpectedOwnerUserID < 0 {
		return CommandResult{}, fmt.Errorf("expected_owner_user_id must not be negative")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeCollectibleUsername, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"username": req.Username, "burn": req.Burn}
		asset, err := s.usernames.Collectible(ctx, req.Username)
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["collectible_id"] = strconv.FormatInt(asset.ID, 10)
		details["previous_status"] = string(asset.Status)
		details["previous_owner_type"] = string(asset.Owner.Type)
		details["previous_owner_id"] = strconv.FormatInt(asset.Owner.ID, 10)
		if asset.Status == domain.CollectibleUsernameStatusBurned {
			return CommandResult{Details: details}, codedError(CodeCollectibleBurned, domain.ErrCollectibleUsernameBurned)
		}
		if !req.Burn && !asset.Owned() {
			return CommandResult{Details: details}, codedError(CodeCollectibleNotOwned, domain.ErrCollectibleUsernameNotOwned)
		}
		if req.ExpectedOwnerUserID > 0 && asset.Owner != (domain.Peer{Type: domain.PeerTypeUser, ID: req.ExpectedOwnerUserID}) {
			return CommandResult{Details: details}, codedError(CodeCollectibleNotOwned, domain.ErrCollectibleUsernameNotOwned)
		}
		if req.DryRun {
			return CommandResult{Message: "collectible username revoke validated", Details: details}, nil
		}
		updated, changed, err := s.usernames.Revoke(ctx, domain.RevokeCollectibleUsernameRequest{
			Username:   req.Username,
			Burn:       req.Burn,
			Actor:      req.Actor,
			Reason:     req.Reason,
			CommandKey: "admin-collectible-revoke:" + req.CommandID,
		})
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["status"] = string(updated.Status)
		details["changed"] = changed
		message := "collectible username returned to vault"
		if req.Burn {
			message = "collectible username burned"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// DeleteCollectibleUsername erases the asset and its provenance, releasing the
// name. Because the history disappears with the record, the command journal is
// the only remaining trace: the details below are captured before the delete so
// the entry still says what was removed and from whom.
func (s *Service) DeleteCollectibleUsername(ctx context.Context, req DeleteCollectibleUsernameRequest) (CommandResult, error) {
	if s == nil || s.usernames == nil {
		return CommandResult{}, fmt.Errorf("admin collectible username dependency is not configured")
	}
	req.Username = domain.NormalizeUsername(req.Username)
	if !domain.ValidCollectibleUsername(req.Username) {
		return CommandResult{}, codedError(CodeUsernameInvalid, domain.ErrUsernameInvalid)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteCollectibleUsername, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"username": req.Username}
		asset, err := s.usernames.Collectible(ctx, req.Username)
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["collectible_id"] = strconv.FormatInt(asset.ID, 10)
		details["previous_status"] = string(asset.Status)
		details["previous_owner_type"] = string(asset.Owner.Type)
		details["previous_owner_id"] = strconv.FormatInt(asset.Owner.ID, 10)
		details["transfer_count"] = asset.TransferCount
		details["currency"] = asset.Currency
		details["amount"] = strconv.FormatInt(asset.Amount, 10)
		if asset.Status == domain.CollectibleUsernameStatusBurned {
			// Only live assets can be deleted; burned rows are history and are
			// released by re-issuing the name instead.
			return CommandResult{Details: details}, codedError(CodeCollectibleBurned, domain.ErrCollectibleUsernameBurned)
		}
		if req.DryRun {
			return CommandResult{Message: "collectible username delete validated", Details: details}, nil
		}
		deleted, err := s.usernames.Delete(ctx, domain.DeleteCollectibleUsernameRequest{
			Username:   req.Username,
			Actor:      req.Actor,
			Reason:     req.Reason,
			CommandKey: "admin-collectible-delete:" + req.CommandID,
		})
		if err != nil {
			return CommandResult{Details: details}, collectibleUsernameError(err)
		}
		details["deleted"] = deleted
		if !deleted {
			return CommandResult{Message: "collectible username already absent", Details: details}, nil
		}
		return CommandResult{Message: "collectible username deleted", Details: details}, nil
	})
}

func (s *Service) CollectiblePhones(ctx context.Context, filter domain.CollectiblePhoneFilter) ([]domain.CollectiblePhone, error) {
	if s == nil || s.collectiblePhones == nil {
		return nil, fmt.Errorf("collectible phone dependency is not configured")
	}
	return s.collectiblePhones.ListCollectiblePhones(ctx, filter)
}

func (s *Service) CollectiblePhoneByID(ctx context.Context, id int64) (domain.CollectiblePhone, error) {
	if s == nil || s.collectiblePhones == nil {
		return domain.CollectiblePhone{}, fmt.Errorf("collectible phone dependency is not configured")
	}
	return s.collectiblePhones.CollectiblePhoneByID(ctx, id)
}

func (s *Service) CollectiblePhoneTransfers(ctx context.Context, id int64, limit int) ([]domain.CollectiblePhoneTransfer, error) {
	if s == nil || s.collectiblePhones == nil {
		return nil, fmt.Errorf("collectible phone dependency is not configured")
	}
	return s.collectiblePhones.CollectiblePhoneTransfers(ctx, id, limit)
}

func (s *Service) MintCollectiblePhone(ctx context.Context, req MintCollectiblePhoneRequest) (CommandResult, error) {
	if s == nil || s.collectiblePhones == nil {
		return CommandResult{}, fmt.Errorf("collectible phone dependency is not configured")
	}
	req.Phone = domain.NormalizeCollectiblePhone(req.Phone)
	if req.Tier == "" {
		req.Tier = domain.CollectiblePhoneTierStandard
	}
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	if req.Currency == "" {
		req.Currency = domain.CollectibleCurrencyUSD
	}
	req.CryptoCurrency = strings.ToUpper(strings.TrimSpace(req.CryptoCurrency))
	purchase, err := collectiblePurchaseDate(req.PurchaseDate, s.now)
	if err != nil {
		return CommandResult{}, err
	}
	domainReq := domain.MintCollectiblePhoneRequest{Phone: req.Phone, Tier: req.Tier, OwnerUserID: req.OwnerUserID, PurchaseDate: purchase,
		Currency: req.Currency, Amount: req.Amount, CryptoCurrency: req.CryptoCurrency, CryptoAmount: req.CryptoAmount, URL: strings.TrimSpace(req.URL), Actor: req.Actor, Reason: req.Reason}
	if err := domainReq.Validate(); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionMintCollectiblePhone, req.OwnerUserID, domain.Peer{Type: domain.PeerTypeUser, ID: req.OwnerUserID}, req, func() (CommandResult, error) {
		details := map[string]any{"phone": req.Phone, "tier": string(req.Tier), "owner_user_id": strconv.FormatInt(req.OwnerUserID, 10), "currency": req.Currency, "amount": strconv.FormatInt(req.Amount, 10)}
		if _, err := s.collectiblePhones.CollectiblePhone(ctx, req.Phone); err == nil {
			return CommandResult{Details: details}, domain.ErrCollectiblePhoneInvalid
		} else if !errors.Is(err, domain.ErrCollectiblePhoneNotFound) {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "collectible phone mint validated", Details: details}, nil
		}
		domainReq.CommandKey = "admin-collectible-phone-mint:" + req.CommandID
		a, created, err := s.collectiblePhones.MintCollectiblePhone(ctx, domainReq)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["collectible_id"] = strconv.FormatInt(a.ID, 10)
		details["created"] = created
		details["status"] = string(a.Status)
		s.notifyCollectiblePhoneOwners(ctx, 0, a.OwnerUserID)
		return CommandResult{Message: "collectible phone minted", Details: details}, nil
	})
}

func (s *Service) UpdateCollectiblePhonePrice(ctx context.Context, req UpdateCollectiblePhonePriceRequest) (CommandResult, error) {
	if s == nil || s.collectiblePhones == nil {
		return CommandResult{}, fmt.Errorf("collectible phone dependency is not configured")
	}
	req.Phone = domain.NormalizeCollectiblePhone(req.Phone)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.CryptoCurrency = strings.ToUpper(strings.TrimSpace(req.CryptoCurrency))
	d := domain.UpdateCollectiblePhonePriceRequest{Phone: req.Phone, Currency: req.Currency, Amount: req.Amount,
		CryptoCurrency: req.CryptoCurrency, CryptoAmount: req.CryptoAmount, Actor: req.Actor, Reason: req.Reason}
	if err := d.Validate(); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionUpdateCollectiblePhonePrice, 0, domain.Peer{}, req, func() (CommandResult, error) {
		a, err := s.collectiblePhones.CollectiblePhone(ctx, req.Phone)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{"phone": req.Phone, "owner_user_id": strconv.FormatInt(a.OwnerUserID, 10),
			"currency": req.Currency, "amount": strconv.FormatInt(req.Amount, 10), "crypto_currency": req.CryptoCurrency,
			"crypto_amount": strconv.FormatInt(req.CryptoAmount, 10)}
		if req.DryRun {
			return CommandResult{Message: "collectible phone price update validated", Details: details}, nil
		}
		u, changed, err := s.collectiblePhones.UpdateCollectiblePhonePrice(ctx, d)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["changed"] = changed
		s.notifyCollectiblePhoneOwners(ctx, u.OwnerUserID)
		return CommandResult{Message: "collectible phone price updated", Details: details}, nil
	})
}

func (s *Service) TransferCollectiblePhone(ctx context.Context, req TransferCollectiblePhoneRequest) (CommandResult, error) {
	if s == nil || s.collectiblePhones == nil {
		return CommandResult{}, fmt.Errorf("collectible phone dependency is not configured")
	}
	req.Phone = domain.NormalizeCollectiblePhone(req.Phone)
	d := domain.TransferCollectiblePhoneRequest{Phone: req.Phone, ToUserID: req.ToUserID, Actor: req.Actor, Reason: req.Reason}
	if err := d.Validate(); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionTransferCollectiblePhone, req.ToUserID, domain.Peer{Type: domain.PeerTypeUser, ID: req.ToUserID}, req, func() (CommandResult, error) {
		a, err := s.collectiblePhones.CollectiblePhone(ctx, req.Phone)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{"phone": req.Phone, "from_user_id": strconv.FormatInt(a.OwnerUserID, 10), "to_user_id": strconv.FormatInt(req.ToUserID, 10)}
		if req.DryRun {
			return CommandResult{Message: "collectible phone transfer validated", Details: details}, nil
		}
		d.CommandKey = "admin-collectible-phone-transfer:" + req.CommandID
		u, changed, err := s.collectiblePhones.TransferCollectiblePhone(ctx, d)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["changed"] = changed
		s.notifyCollectiblePhoneOwners(ctx, a.OwnerUserID, u.OwnerUserID)
		return CommandResult{Message: "collectible phone transferred", Details: details}, nil
	})
}

func (s *Service) RevokeCollectiblePhone(ctx context.Context, req RevokeCollectiblePhoneRequest) (CommandResult, error) {
	if s == nil || s.collectiblePhones == nil {
		return CommandResult{}, fmt.Errorf("collectible phone dependency is not configured")
	}
	req.Phone = domain.NormalizeCollectiblePhone(req.Phone)
	d := domain.RevokeCollectiblePhoneRequest{Phone: req.Phone, Burn: req.Burn, Actor: req.Actor, Reason: req.Reason}
	if err := d.Validate(); err != nil {
		return CommandResult{}, err
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeCollectiblePhone, 0, domain.Peer{}, req, func() (CommandResult, error) {
		a, err := s.collectiblePhones.CollectiblePhone(ctx, req.Phone)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{"phone": req.Phone, "previous_owner_user_id": strconv.FormatInt(a.OwnerUserID, 10), "burn": req.Burn}
		if req.DryRun {
			return CommandResult{Message: "collectible phone revoke validated", Details: details}, nil
		}
		d.CommandKey = "admin-collectible-phone-revoke:" + req.CommandID
		u, changed, err := s.collectiblePhones.RevokeCollectiblePhone(ctx, d)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["changed"] = changed
		details["status"] = string(u.Status)
		s.notifyCollectiblePhoneOwners(ctx, a.OwnerUserID, 0)
		return CommandResult{Message: "collectible phone revoked", Details: details}, nil
	})
}

func (s *Service) DeleteCollectiblePhone(ctx context.Context, req DeleteCollectiblePhoneRequest) (CommandResult, error) {
	if s == nil || s.collectiblePhones == nil {
		return CommandResult{}, fmt.Errorf("collectible phone dependency is not configured")
	}
	req.Phone = domain.NormalizeCollectiblePhone(req.Phone)
	if !domain.ValidCollectiblePhone(req.Phone) {
		return CommandResult{}, domain.ErrCollectiblePhoneInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteCollectiblePhone, 0, domain.Peer{}, req, func() (CommandResult, error) {
		a, err := s.collectiblePhones.CollectiblePhone(ctx, req.Phone)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{"phone": req.Phone, "previous_owner_user_id": strconv.FormatInt(a.OwnerUserID, 10)}
		if req.DryRun {
			return CommandResult{Message: "collectible phone delete validated", Details: details}, nil
		}
		deleted, err := s.collectiblePhones.DeleteCollectiblePhone(ctx, domain.DeleteCollectiblePhoneRequest{Phone: req.Phone, Actor: req.Actor, Reason: req.Reason, CommandKey: "admin-collectible-phone-delete:" + req.CommandID})
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["deleted"] = deleted
		s.notifyCollectiblePhoneOwners(ctx, a.OwnerUserID, 0)
		return CommandResult{Message: "collectible phone deleted", Details: details}, nil
	})
}

func (s *Service) notifyCollectiblePhoneOwners(ctx context.Context, ids ...int64) {
	seen := map[int64]struct{}{}
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		if u, found, err := s.users.AdminUser(ctx, id); err == nil && found {
			_ = s.notifyUserChanged(ctx, u)
		}
	}
}

// RecomputeAccountRating rebuilds one user's composite rating from the current
// contribution signals. A dry-run only reports the stored projection, so the
// operator can see what a recompute would start from without writing.
func (s *Service) RecomputeAccountRating(ctx context.Context, req RecomputeAccountRatingRequest) (CommandResult, error) {
	if s == nil || s.rating == nil {
		return CommandResult{}, fmt.Errorf("admin account rating dependency is not configured")
	}
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRecomputeAccountRating, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"user_id": strconv.FormatInt(req.UserID, 10)}
		previous, err := s.rating.Rating(ctx, req.UserID)
		switch {
		case err == nil:
			details["previous_found"] = true
			details["previous_level"] = previous.Level
			details["previous_stars"] = strconv.FormatInt(previous.Stars, 10)
		case errors.Is(err, domain.ErrAccountRatingNotFound):
			details["previous_found"] = false
		default:
			return CommandResult{Details: details}, accountRatingError(err)
		}
		if req.DryRun {
			return CommandResult{Message: "account rating recompute validated", Details: details}, nil
		}
		rating, err := s.rating.Recompute(ctx, req.UserID)
		if err != nil {
			return CommandResult{Details: details}, accountRatingError(err)
		}
		mergeAccountRatingDetails(details, rating)
		return CommandResult{Message: "account rating recomputed", Details: details}, nil
	})
}

// AdjustAccountRating moves the manual component of one user's rating by a
// signed delta and recomputes the projection so the change is visible at once.
// The command id doubles as the ledger key, so a retried command records the
// adjustment exactly once.
func (s *Service) AdjustAccountRating(ctx context.Context, req AdjustAccountRatingRequest) (CommandResult, error) {
	if s == nil || s.rating == nil {
		return CommandResult{}, fmt.Errorf("admin account rating dependency is not configured")
	}
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if req.Amount == 0 || req.Amount < -maxAccountRatingAdjustment || req.Amount > maxAccountRatingAdjustment {
		return CommandResult{}, codedError(CodeRatingAdjustmentInvalid, domain.ErrAccountRatingAdjustmentInvalid)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionAdjustAccountRating, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"user_id": strconv.FormatInt(req.UserID, 10),
			"amount":  strconv.FormatInt(req.Amount, 10),
		}
		previous, err := s.rating.Rating(ctx, req.UserID)
		switch {
		case err == nil:
			details["previous_found"] = true
			details["previous_level"] = previous.Level
			details["previous_stars"] = strconv.FormatInt(previous.Stars, 10)
			details["previous_manual_component"] = strconv.FormatInt(previous.ManualComponent, 10)
		case errors.Is(err, domain.ErrAccountRatingNotFound):
			details["previous_found"] = false
		default:
			return CommandResult{Details: details}, accountRatingError(err)
		}
		if req.DryRun {
			return CommandResult{Message: "account rating adjustment validated", Details: details}, nil
		}
		rating, applied, err := s.rating.Adjust(ctx, domain.AdjustAccountRatingRequest{
			UserID:     req.UserID,
			Amount:     req.Amount,
			Reason:     req.Reason,
			Actor:      req.Actor,
			CommandKey: "admin-rating-adjust:" + req.CommandID,
		})
		if err != nil {
			return CommandResult{Details: details}, accountRatingError(err)
		}
		details["applied"] = applied
		mergeAccountRatingDetails(details, rating)
		message := "account rating adjusted"
		if !applied {
			message = "account rating adjustment replayed"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

// AddAutoSubscribeChannelRequest adds a channel/supergroup to the admin
// panel's auto-subscribe list -- see autosubscribe.Service.Add for the full
// "join everyone right now, join every future signup" semantics.
type AddAutoSubscribeChannelRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id,string"`
}

// RemoveAutoSubscribeChannelRequest takes a channel off the list. It never
// removes anyone already joined -- see autosubscribe.Service.Remove.
type RemoveAutoSubscribeChannelRequest struct {
	CommandMeta
	ChannelID int64 `json:"channel_id,string"`
}

func (s *Service) ListAutoSubscribeChannels(ctx context.Context) ([]domain.AutoSubscribeChannel, error) {
	if s == nil || s.autoSubscribe == nil {
		return nil, fmt.Errorf("auto-subscribe dependency is not configured")
	}
	return s.autoSubscribe.List(ctx)
}

func (s *Service) AddAutoSubscribeChannel(ctx context.Context, req AddAutoSubscribeChannelRequest) (CommandResult, error) {
	if s == nil || s.autoSubscribe == nil {
		return CommandResult{}, fmt.Errorf("auto-subscribe dependency is not configured")
	}
	if req.ChannelID <= 0 {
		return CommandResult{}, domain.ErrAutoSubscribeChannelInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionAddAutoSubscribeChannel, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			details := map[string]any{"channel_id": strconv.FormatInt(req.ChannelID, 10)}
			if req.DryRun {
				return CommandResult{Message: "dry-run completed", Details: details}, nil
			}
			added, joined, err := s.autoSubscribe.Add(ctx, req.ChannelID, req.Actor)
			if err != nil {
				return CommandResult{Details: details}, err
			}
			details["added"] = added
			details["joined_existing_users"] = joined
			message := "channel added to auto-subscribe list"
			if !added {
				message = "channel was already on the auto-subscribe list"
			}
			return CommandResult{Message: message, Details: details}, nil
		})
}

func (s *Service) RemoveAutoSubscribeChannel(ctx context.Context, req RemoveAutoSubscribeChannelRequest) (CommandResult, error) {
	if s == nil || s.autoSubscribe == nil {
		return CommandResult{}, fmt.Errorf("auto-subscribe dependency is not configured")
	}
	if req.ChannelID <= 0 {
		return CommandResult{}, domain.ErrAutoSubscribeChannelInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRemoveAutoSubscribeChannel, 0, domain.Peer{}, req,
		func() (CommandResult, error) {
			details := map[string]any{"channel_id": strconv.FormatInt(req.ChannelID, 10)}
			if req.DryRun {
				return CommandResult{Message: "dry-run completed", Details: details}, nil
			}
			removed, err := s.autoSubscribe.Remove(ctx, req.ChannelID)
			if err != nil {
				return CommandResult{Details: details}, err
			}
			details["removed"] = removed
			message := "channel removed from auto-subscribe list"
			if !removed {
				message = "channel was not on the auto-subscribe list"
			}
			return CommandResult{Message: message, Details: details}, nil
		})
}

// collectibleOwnerPeer resolves the optional owner of a collectible asset. At
// most one identifier may be set; neither yields the zero peer, which the
// lifecycle reads as "the operator vault".
func collectibleOwnerPeer(userID, channelID int64) (domain.Peer, error) {
	if userID < 0 || channelID < 0 {
		return domain.Peer{}, fmt.Errorf("owner id must be positive")
	}
	if userID > 0 && channelID > 0 {
		return domain.Peer{}, fmt.Errorf("at most one of user id or channel id is allowed")
	}
	switch {
	case userID > 0:
		return domain.Peer{Type: domain.PeerTypeUser, ID: userID}, nil
	case channelID > 0:
		return domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}, nil
	default:
		return domain.Peer{}, nil
	}
}

// collectiblePurchaseDate resolves the optional Unix purchase timestamp. Zero
// means "now", so a mint always records a complete, reproducible provenance
// entry; the int32 bound matches the TL date field clients render.
func collectiblePurchaseDate(unix int64, now func() time.Time) (time.Time, error) {
	if unix == 0 {
		return now().UTC(), nil
	}
	if unix < 0 || unix > math.MaxInt32 {
		return time.Time{}, fmt.Errorf("purchase_date must be a non-negative int32 Unix timestamp")
	}
	return time.Unix(unix, 0).UTC(), nil
}

// mergeAccountRatingDetails records the computed projection in command details.
// Every score component crosses the JSON boundary as a decimal string so an
// audit entry reproduces the exact int64 the store holds.
func mergeAccountRatingDetails(details map[string]any, rating domain.AccountRating) {
	details["level"] = rating.Level
	details["stars"] = strconv.FormatInt(rating.Stars, 10)
	details["current_level_stars"] = strconv.FormatInt(rating.CurrentLevelStars, 10)
	details["has_next_level"] = rating.HasNextLevel
	if rating.HasNextLevel {
		details["next_level_stars"] = strconv.FormatInt(rating.NextLevelStars, 10)
	}
	details["stars_component"] = strconv.FormatInt(rating.StarsComponent, 10)
	details["activity_component"] = strconv.FormatInt(rating.ActivityComponent, 10)
	details["penalty_component"] = strconv.FormatInt(rating.PenaltyComponent, 10)
	details["manual_component"] = strconv.FormatInt(rating.ManualComponent, 10)
	details["pending_stars"] = strconv.FormatInt(rating.PendingStars, 10)
	if !rating.PendingDate.IsZero() {
		details["pending_date"] = rating.PendingDate.UTC().Format(time.RFC3339)
	}
	details["version"] = strconv.FormatInt(rating.Version, 10)
}

// CollectibleUsernameErrorCode maps a collectible-username domain error onto the
// stable code the admin panel switches on. An unmapped error returns "" so the
// caller can fall back to a generic failure instead of inventing a code.
func CollectibleUsernameErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrUsernameOccupied):
		return CodeUsernameOccupied
	case errors.Is(err, domain.ErrCollectibleUsernameNotFound), errors.Is(err, domain.ErrUsernameNotOccupied):
		return CodeCollectibleNotFound
	case errors.Is(err, domain.ErrCollectibleUsernameBurned):
		return CodeCollectibleBurned
	case errors.Is(err, domain.ErrCollectibleUsernameLimit):
		return CodeCollectiblePeerLimit
	case errors.Is(err, domain.ErrCollectibleUsernameNotOwned):
		return CodeCollectibleNotOwned
	case errors.Is(err, domain.ErrCollectibleCurrencyInvalid):
		return CodeCollectibleCurrencyInvalid
	case errors.Is(err, domain.ErrUsernameNotCollectible), errors.Is(err, domain.ErrUsernameNotEditable):
		return CodeUsernameNotCollectible
	case errors.Is(err, domain.ErrUsernameInvalid):
		return CodeUsernameInvalid
	case errors.Is(err, domain.ErrCollectibleUsernameStateInvalid), errors.Is(err, domain.ErrUsernameOrderInvalid):
		return CodeCollectibleStateInvalid
	default:
		return ""
	}
}

// AccountRatingErrorCode maps an account-rating domain error onto its stable
// admin code. An unmapped error returns "".
func AccountRatingErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrAccountRatingNotFound):
		return CodeRatingNotFound
	case errors.Is(err, domain.ErrAccountRatingAdjustmentInvalid):
		return CodeRatingAdjustmentInvalid
	case errors.Is(err, domain.ErrAccountRatingWeightsInvalid):
		return CodeRatingWeightsInvalid
	default:
		return ""
	}
}

// collectibleUsernameError / accountRatingError prefix a recognised domain error
// with its stable code, so the journalled command result and the operator both
// see "CODE: message" instead of a bare Go string. Unrecognised errors are
// returned untouched: inventing a code for an unknown failure would be worse
// than reporting it verbatim.
func collectibleUsernameError(err error) error {
	if code := CollectibleUsernameErrorCode(err); code != "" {
		return codedError(code, err)
	}
	return err
}

func accountRatingError(err error) error {
	if code := AccountRatingErrorCode(err); code != "" {
		return codedError(code, err)
	}
	return err
}

func codedError(code string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", code, err)
}

func (s *Service) SetChannelVerified(ctx context.Context, req SetChannelVerifiedRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil {
		return CommandResult{}, fmt.Errorf("admin channel dependency is not configured")
	}
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelVerified, 0, target, req, func() (CommandResult, error) {
		ch, err := s.channels.GetChannelByID(ctx, req.ChannelID)
		if err != nil {
			return CommandResult{}, err
		}
		if ch.Monoforum || (!ch.Broadcast && !ch.Megagroup) {
			return CommandResult{}, domain.ErrChannelInvalid
		}
		details := map[string]any{
			"title":             ch.Title,
			"username":          ch.Username,
			"broadcast":         ch.Broadcast,
			"megagroup":         ch.Megagroup,
			"previous_verified": ch.Verified,
			"new_verified":      req.Verified,
			"would_change":      ch.Verified != req.Verified,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.channels.SetVerified(ctx, req.ChannelID, req.Verified)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_verified"] = updated.Verified
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel verified updated", Details: details}, nil
	})
}

// SetChannelFlags sets or clears the scam/fake moderation flags on a channel or
// supergroup. Both flags are applied together from the desired state.
func (s *Service) SetChannelFlags(ctx context.Context, req SetChannelFlagsRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil {
		return CommandResult{}, fmt.Errorf("admin channel dependency is not configured")
	}
	if req.Scam && req.Fake {
		return CommandResult{}, domain.ErrPeerModerationFlagsInvalid
	}
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelFlags, 0, target, req, func() (CommandResult, error) {
		ch, err := s.channels.GetChannelByID(ctx, req.ChannelID)
		if err != nil {
			return CommandResult{}, err
		}
		if ch.Monoforum || (!ch.Broadcast && !ch.Megagroup) {
			return CommandResult{}, domain.ErrChannelInvalid
		}
		details := map[string]any{
			"title": ch.Title, "username": ch.Username,
			"previous_scam": ch.Scam, "previous_fake": ch.Fake,
			"new_scam": req.Scam, "new_fake": req.Fake,
			"would_change": ch.Scam != req.Scam || ch.Fake != req.Fake,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.channels.SetScamFake(ctx, req.ChannelID, req.Scam, req.Fake)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_scam"] = updated.Scam
		details["updated_fake"] = updated.Fake
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel flags updated", Details: details}, nil
	})
}

// SetChannelSettings applies an admin moderation-settings patch to a channel/supergroup.
func (s *Service) SetChannelSettings(ctx context.Context, req SetChannelSettingsRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil {
		return CommandResult{}, fmt.Errorf("admin channel dependency is not configured")
	}
	if req.SlowmodeSeconds != nil && (*req.SlowmodeSeconds < 0 || *req.SlowmodeSeconds > 86400) {
		return CommandResult{}, fmt.Errorf("slowmode_seconds must be between 0 and 86400")
	}
	patch := domain.ChannelAdminSettings{
		Gigagroup: req.Gigagroup, AntiSpam: req.AntiSpam, ParticipantsHidden: req.ParticipantsHidden,
		NoForwards: req.NoForwards, JoinToSend: req.JoinToSend, JoinRequest: req.JoinRequest, SlowmodeSeconds: req.SlowmodeSeconds,
	}
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelSettings, 0, target, req, func() (CommandResult, error) {
		if patch.Empty() {
			return CommandResult{}, fmt.Errorf("no settings provided")
		}
		details := boolIntPatchDetails(patch)
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.channels.AdminSetSettings(ctx, req.ChannelID, patch)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated"] = true
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel settings updated", Details: details}, nil
	})
}

func (s *Service) SetChannelAvatar(ctx context.Context, req SetChannelAvatarRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil || s.photos == nil {
		return CommandResult{}, fmt.Errorf("admin channel avatar dependencies are not configured")
	}
	if len(req.Data) == 0 || len(req.Data) > MaxAccountAvatarBytes || !s.photos.ValidateAvatarUpload(req.Data) {
		return CommandResult{}, domain.ErrPhotoInvalid
	}
	digest := sha256.Sum256(req.Data)
	req.ContentSHA256 = hex.EncodeToString(digest[:])
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelAvatar, 0, target, req, func() (CommandResult, error) {
		channel, err := s.channels.GetChannelByID(ctx, req.ChannelID)
		if err != nil {
			return CommandResult{}, err
		}
		if channel.Deleted || channel.Monoforum || (!channel.Broadcast && !channel.Megagroup) {
			return CommandResult{}, domain.ErrChannelInvalid
		}
		details := map[string]any{
			"file_name":         req.FileName,
			"bytes":             len(req.Data),
			"content_sha256":    req.ContentSHA256,
			"previous_photo_id": strconv.FormatInt(channel.PhotoID, 10),
		}
		if req.DryRun {
			return CommandResult{Message: "channel avatar update validated", Details: details}, nil
		}
		photo, err := s.photos.CreateAvatarFromBytes(ctx, req.Data)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		updated, err := s.channels.AdminSetPhoto(ctx, req.ChannelID, photo)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["photo_id"] = strconv.FormatInt(photo.ID, 10)
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel avatar updated", Details: details}, nil
	})
}

// SetChannelUsername force-sets or clears a channel username.
func (s *Service) SetChannelUsername(ctx context.Context, req SetChannelUsernameRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil {
		return CommandResult{}, fmt.Errorf("admin channel dependency is not configured")
	}
	username := strings.TrimSpace(strings.TrimPrefix(req.Username, "@"))
	req.Username = username
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelUsername, 0, target, req, func() (CommandResult, error) {
		details := map[string]any{"new_username": username}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.channels.AdminSetUsername(ctx, req.ChannelID, username)
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_username"] = updated.Username
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel username updated", Details: details}, nil
	})
}

// SetChannelColor force-sets or clears a channel name/profile color.
func (s *Service) SetChannelColor(ctx context.Context, req SetChannelColorRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil {
		return CommandResult{}, fmt.Errorf("admin channel dependency is not configured")
	}
	color := domain.ChannelPeerColor{HasColor: req.HasColor, Color: req.Color, BackgroundEmojiID: req.BackgroundEmojiID}
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelColor, 0, target, req, func() (CommandResult, error) {
		details := map[string]any{"for_profile": req.ForProfile, "has_color": req.HasColor, "color": req.Color}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.channels.AdminSetColor(ctx, req.ChannelID, req.ForProfile, color)
		if err != nil {
			return CommandResult{}, err
		}
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel color updated", Details: details}, nil
	})
}

// SetChannelEmojiStatus force-sets or clears (document_id=0) a channel emoji status.
func (s *Service) SetChannelEmojiStatus(ctx context.Context, req SetChannelEmojiStatusRequest) (CommandResult, error) {
	if req.ChannelID <= 0 {
		return CommandResult{}, fmt.Errorf("channel_id is required")
	}
	if s == nil || s.channels == nil {
		return CommandResult{}, fmt.Errorf("admin channel dependency is not configured")
	}
	status := domain.ChannelEmojiStatus{DocumentID: req.DocumentID, Until: req.Until}
	target := domain.Peer{Type: domain.PeerTypeChannel, ID: req.ChannelID}
	return s.runCommand(ctx, req.CommandMeta, ActionSetChannelEmojiStatus, 0, target, req, func() (CommandResult, error) {
		details := map[string]any{"document_id": strconv.FormatInt(req.DocumentID, 10), "until": req.Until}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.channels.AdminSetEmojiStatus(ctx, req.ChannelID, status)
		if err != nil {
			return CommandResult{}, err
		}
		if err := s.notifyChannelChanged(ctx, updated); err != nil {
			details["notify_error"] = err.Error()
		}
		return CommandResult{Message: "channel emoji status updated", Details: details}, nil
	})
}

func boolIntPatchDetails(p domain.ChannelAdminSettings) map[string]any {
	details := map[string]any{}
	if p.Gigagroup != nil {
		details["gigagroup"] = *p.Gigagroup
	}
	if p.AntiSpam != nil {
		details["antispam"] = *p.AntiSpam
	}
	if p.ParticipantsHidden != nil {
		details["participants_hidden"] = *p.ParticipantsHidden
	}
	if p.NoForwards != nil {
		details["noforwards"] = *p.NoForwards
	}
	if p.JoinToSend != nil {
		details["join_to_send"] = *p.JoinToSend
	}
	if p.JoinRequest != nil {
		details["join_request"] = *p.JoinRequest
	}
	if p.SlowmodeSeconds != nil {
		details["slowmode_seconds"] = *p.SlowmodeSeconds
	}
	return details
}

func (s *Service) RevokeSessions(ctx context.Context, req RevokeSessionsRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.auth == nil || s.revoker == nil {
		return CommandResult{}, fmt.Errorf("admin auth dependencies are not configured")
	}
	modeCount := 0
	if req.Hash != 0 {
		modeCount++
	}
	if req.KeepHash != 0 {
		modeCount++
	}
	if req.RevokeAll {
		modeCount++
	}
	if modeCount != 1 {
		return CommandResult{}, fmt.Errorf("choose one revoke mode")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRevokeSessions, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		items, err := s.auth.ListAuthorizations(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		targets, keep, err := revokeTargets(items, req)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"target_hashes": authorizationHashes(targets),
			"target_count":  len(targets),
			"keep_hash":     authorizationHashString(keep.Hash),
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		var revoked []domain.Authorization
		if req.Hash != 0 {
			deleted, found, err := s.auth.ResetAuthorization(ctx, req.UserID, req.Hash)
			if err != nil {
				return CommandResult{}, err
			}
			if !found {
				return CommandResult{}, fmt.Errorf("authorization hash not found")
			}
			revoked = append(revoked, deleted)
		} else {
			deleted, err := s.auth.ResetAuthorizations(ctx, req.UserID, keep.AuthKeyID)
			if err != nil {
				return CommandResult{}, err
			}
			revoked = append(revoked, deleted...)
		}
		for _, a := range revoked {
			if err := s.revoker.RevokeAuthorizationAuthKey(ctx, a.AuthKeyID, req.UserID); err != nil {
				return CommandResult{}, err
			}
		}
		details["revoked_hashes"] = authorizationHashes(revoked)
		details["revoked_count"] = len(revoked)
		return CommandResult{Message: "sessions revoked", Details: details}, nil
	})
}

func (s *Service) DeletePrivateMessages(ctx context.Context, req DeletePrivateMessagesRequest) (CommandResult, error) {
	ids, err := normalizeIDs(req.IDs)
	if err != nil {
		return CommandResult{}, err
	}
	req.IDs = ids
	if req.OwnerUserID <= 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID <= 0 {
		return CommandResult{}, fmt.Errorf("owner_user_id and user peer are required")
	}
	if s == nil || s.messages == nil {
		return CommandResult{}, fmt.Errorf("admin message dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeletePrivateMessages, req.OwnerUserID, req.Peer, req, func() (CommandResult, error) {
		list, err := s.messages.GetMessages(ctx, req.OwnerUserID, req.IDs)
		if err != nil {
			return CommandResult{}, err
		}
		found, missing, err := validatePrivateMessageSelection(req.OwnerUserID, req.Peer, req.IDs, list.Messages)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"requested_ids": req.IDs,
			"found_ids":     found,
			"missing_ids":   missing,
			"revoke":        req.Revoke,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		if len(missing) > 0 {
			return CommandResult{}, fmt.Errorf("messages not found for owner/peer: %v", missing)
		}
		res, err := s.messages.DeleteMessages(ctx, req.OwnerUserID, domain.DeleteMessagesRequest{
			OwnerUserID: req.OwnerUserID,
			IDs:         req.IDs,
			Revoke:      req.Revoke,
			Date:        int(s.now().Unix()),
		})
		if err != nil {
			return CommandResult{}, err
		}
		details["deleted"] = summarizeDeleteResult(res)
		details["changed"] = res.Changed()
		return CommandResult{Message: "messages deleted", Details: details}, nil
	})
}

func (s *Service) DeletePrivateHistory(ctx context.Context, req DeletePrivateHistoryRequest) (CommandResult, error) {
	if req.OwnerUserID <= 0 || req.Peer.Type != domain.PeerTypeUser || req.Peer.ID <= 0 {
		return CommandResult{}, fmt.Errorf("owner_user_id and user peer are required")
	}
	if req.MaxID < 0 || req.MaxID > domain.MaxMessageBoxID || req.MinDate < 0 || req.MaxDate < 0 {
		return CommandResult{}, domain.ErrMessageIDInvalid
	}
	if req.MaxBatches <= 0 {
		req.MaxBatches = 10
	}
	if req.MaxBatches > maxHistoryBatches {
		return CommandResult{}, fmt.Errorf("max_batches exceeds %d", maxHistoryBatches)
	}
	if s == nil || s.messages == nil {
		return CommandResult{}, fmt.Errorf("admin message dependency is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeletePrivateHistory, req.OwnerUserID, req.Peer, req, func() (CommandResult, error) {
		preview, err := s.messages.GetHistory(ctx, req.OwnerUserID, domain.MessageFilter{
			HasPeer: true,
			Peer:    req.Peer,
			MaxID:   req.MaxID,
			Limit:   50,
		})
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"preview_ids":       messageIDs(preview.Messages),
			"preview_count":     len(preview.Messages),
			"batch_limit":       domain.MaxDeleteHistoryBatch,
			"max_batches":       req.MaxBatches,
			"revoke":            req.Revoke,
			"just_clear":        req.JustClear,
			"date_range_filter": req.MinDate != 0 || req.MaxDate != 0,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		totalDeleted := 0
		ownerBatches := make([]any, 0, req.MaxBatches)
		offset := 0
		for batch := 0; batch < req.MaxBatches; batch++ {
			res, err := s.messages.DeleteHistory(ctx, req.OwnerUserID, domain.DeleteHistoryRequest{
				OwnerUserID: req.OwnerUserID,
				Peer:        req.Peer,
				MaxID:       req.MaxID,
				MinDate:     req.MinDate,
				MaxDate:     req.MaxDate,
				JustClear:   req.JustClear,
				Revoke:      req.Revoke,
				Date:        int(s.now().Unix()),
			})
			if err != nil {
				return CommandResult{}, err
			}
			self := res.Self()
			totalDeleted += len(self.MessageIDs)
			ownerBatches = append(ownerBatches, summarizeDeleteResult(res)...)
			offset = res.Offset
			if res.Offset == 0 {
				break
			}
		}
		details["deleted_count"] = totalDeleted
		details["deleted"] = ownerBatches
		details["has_more"] = offset != 0
		msg := "history deleted"
		if offset != 0 {
			msg = "history partially deleted; run another command to continue"
		}
		return CommandResult{Message: msg, Details: details}, nil
	})
}

func (s *Service) ImportStarGift(ctx context.Context, req ImportStarGiftRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil {
		return CommandResult{}, fmt.Errorf("star gift service is not configured")
	}
	if req.GiftID < 0 || req.Stars <= 0 || req.ConvertStars < 0 || req.ConvertStars > req.Stars ||
		req.SortOrder < math.MinInt32 || req.SortOrder > math.MaxInt32 ||
		len([]rune(strings.TrimSpace(req.Title))) > domain.MaxStarGiftTitleRunes {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	lifecycle := domain.StarGiftCatalogWrite{
		Auction: req.Auction, AuctionSlug: strings.TrimSpace(req.AuctionSlug), GiftsPerRound: req.GiftsPerRound,
		AuctionStartDate: req.AuctionStartDate, AuctionRoundDuration: req.AuctionRoundDuration,
		AvailabilityTotal: req.AvailabilityTotal, LockedUntilDate: req.LockedUntilDate,
		// The price is the auction's opening bid, so the validator needs it to bound
		// the bid ladder; see domain.MaxStarGiftAuctionBidStars.
		Stars: req.Stars,
	}
	now := int(s.now().Unix())
	if err := lifecycle.ValidateLifecycleAuthoring(now); err != nil {
		return CommandResult{}, err
	}
	// Fills in limited / availability_remains / a concrete auction_start_date,
	// which the revision's CHECK constraints require for an auction.
	lifecycle.NormalizeLifecycleAuthoring(now)
	animation, err := s.gifts.PrepareAnimation(req.FileName, req.Data)
	if err != nil {
		return CommandResult{}, err
	}
	req.ContentSHA = hex.EncodeToString(animation.SHA256)
	return s.runCommand(ctx, req.CommandMeta, ActionImportStarGift, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"gift_id": strconv.FormatInt(req.GiftID, 10), "title": strings.TrimSpace(req.Title),
			"stars": strconv.FormatInt(req.Stars, 10), "convert_stars": strconv.FormatInt(req.ConvertStars, 10),
			"enabled": req.Enabled, "sort_order": req.SortOrder,
			"source_format": animation.SourceFormat, "source_name": animation.SourceName,
			"sha256": req.ContentSHA, "width": animation.Width, "height": animation.Height,
			"frame_rate": animation.FrameRate, "compressed_bytes": len(animation.TGS), "json_bytes": len(animation.JSON),
		}
		if lifecycle.Auction {
			details["auction"] = true
			details["auction_slug"] = lifecycle.AuctionSlug
			details["gifts_per_round"] = lifecycle.GiftsPerRound
			details["auction_start_date"] = lifecycle.AuctionStartDate
			details["auction_round_duration"] = lifecycle.AuctionRoundDuration
			details["availability_total"] = lifecycle.AvailabilityTotal
			details["limited"] = lifecycle.Limited
			details["availability_remains"] = lifecycle.AvailabilityRemains
		}
		if lifecycle.LockedUntilDate > 0 {
			details["locked_until_date"] = lifecycle.LockedUntilDate
		}
		if req.DryRun {
			return CommandResult{Message: "star gift import validated", Details: details}, nil
		}
		entry, err := s.gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
			GiftID: req.GiftID, Title: req.Title, Stars: req.Stars, ConvertStars: req.ConvertStars,
			Enabled: req.Enabled, SortOrder: req.SortOrder, Animation: animation,
			Actor: req.Actor, CommandID: req.CommandID,
			Auction: lifecycle.Auction, AuctionSlug: lifecycle.AuctionSlug, GiftsPerRound: lifecycle.GiftsPerRound,
			AuctionStartDate: lifecycle.AuctionStartDate, AuctionRoundDuration: lifecycle.AuctionRoundDuration,
			AvailabilityTotal: lifecycle.AvailabilityTotal, LockedUntilDate: lifecycle.LockedUntilDate,
			Limited: lifecycle.Limited, AvailabilityRemains: lifecycle.AvailabilityRemains,
		})
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["gift_id"] = strconv.FormatInt(entry.Gift.ID, 10)
		details["revision_id"] = strconv.FormatInt(entry.Gift.RevisionID, 10)
		details["revision"] = entry.Revision
		return CommandResult{Message: "star gift imported", Details: details}, nil
	})
}

func (s *Service) OfficialStarGifts(ctx context.Context) ([]officialgifts.GiftSummary, error) {
	if s == nil || s.officialGifts == nil {
		return nil, officialgifts.ErrUnavailable
	}
	return s.officialGifts.List(ctx)
}

func (s *Service) OfficialStarGiftAnimation(ctx context.Context, sourceGiftID string) ([]byte, bool, error) {
	if s == nil || s.officialGifts == nil || s.gifts == nil {
		return nil, false, officialgifts.ErrUnavailable
	}
	id, err := strconv.ParseInt(strings.TrimSpace(sourceGiftID), 10, 64)
	if err != nil || id <= 0 {
		return nil, false, officialgifts.ErrNotFound
	}
	bundle, err := s.officialGifts.Bundle(ctx, id, false)
	if errors.Is(err, officialgifts.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	animation, err := s.gifts.PrepareOfficialAnimation(bundle.BaseDocument.FileName, bundle.BaseDocument.Data)
	if err != nil {
		return nil, false, err
	}
	return animation.JSON, true, nil
}

func (s *Service) ImportOfficialStarGift(ctx context.Context, req ImportOfficialStarGiftRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil || s.officialGifts == nil {
		return CommandResult{}, fmt.Errorf("official star gift importer is not configured")
	}
	sourceID, err := strconv.ParseInt(strings.TrimSpace(req.SourceGiftID), 10, 64)
	if err != nil || sourceID <= 0 || req.GiftID < 0 || req.SortOrder < math.MinInt32 || req.SortOrder > math.MaxInt32 {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	bundle, err := s.officialGifts.Bundle(ctx, sourceID, req.IncludeCollectible)
	if err != nil {
		return CommandResult{}, err
	}
	if req.Title = strings.TrimSpace(req.Title); req.Title == "" {
		req.Title = strings.TrimSpace(bundle.Gift.Title)
		if req.Title == "" {
			req.Title = "Official gift " + req.SourceGiftID
		}
	}
	if req.Stars <= 0 {
		req.Stars = bundle.Gift.Stars
	}
	if req.ConvertStars < 0 || req.ConvertStars > req.Stars || len([]rune(req.Title)) > domain.MaxStarGiftTitleRunes {
		return CommandResult{}, domain.ErrStarGiftInvalid
	}
	if req.UpgradeStars <= 0 {
		req.UpgradeStars = bundle.Gift.UpgradeStars
	}
	if req.SupplyTotal <= 0 {
		req.SupplyTotal = bundle.Gift.AvailabilityTotal
		if req.SupplyTotal <= 0 {
			// Official unlimited gifts do not carry availability_total. Their
			// upgrade_variants is the complete model/pattern/backdrop combination
			// count and is the only finite capacity supplied by the snapshot.
			// Falling back to one made the first collectible exhaust the entire
			// locally imported pool.
			req.SupplyTotal = bundle.Gift.UpgradeVariants
		}
	}
	if req.SlugPrefix = strings.ToLower(strings.TrimSpace(req.SlugPrefix)); req.SlugPrefix == "" {
		req.SlugPrefix = "official-" + req.SourceGiftID
	}

	// Scheduled release ("отложенный дроп") for a snapshot gift. The snapshot's own
	// locked_until_date records Telegram's release window, which is normally long
	// past by the time the gift is mirrored here, so it cannot serve as a local
	// schedule. An operator-supplied time therefore wins, and must be in the future
	// — a past one would publish the gift immediately while reading as scheduled.
	lockedUntilDate := bundle.Gift.LockedUntilDate
	if req.LockedUntilDate != 0 {
		if req.LockedUntilDate < 0 || req.LockedUntilDate <= int(s.now().Unix()) {
			return CommandResult{}, fmt.Errorf("%w: scheduled-release time must be in the future",
				domain.ErrStarGiftLifecycleInvalid)
		}
		lockedUntilDate = req.LockedUntilDate
	}

	// Supply for an auction import. The rule below — publish a snapshot as a fresh
	// local entry with no inventory — cannot hold for an auction, because
	// star_gift_catalog_revision_auction_check requires an auction to be limited and
	// the supply check then requires availability_total > 0. Leaving both zero made
	// every official auction import fail on the INSERT. Carry the snapshot's supply
	// for that case only, and let the shared normalizer derive the rest.
	//
	// A plain (non-auction) import can carry the same real cap now too --
	// req.AvailabilityTotal (see that field's own doc comment on why it's
	// separate from SupplyTotal, the collectible pool size). Auction imports
	// ignore it; their supply comes from the snapshot/SupplyTotal fallback
	// above, same as before this fix.
	auctionLimited, auctionTotal := false, 0
	if bundle.Gift.Auction {
		auctionLimited, auctionTotal = true, bundle.Gift.AvailabilityTotal
		if auctionTotal <= 0 {
			auctionTotal = req.SupplyTotal
		}
	} else if req.AvailabilityTotal > 0 {
		auctionLimited, auctionTotal = true, req.AvailabilityTotal
	}

	baseAnimation, err := s.gifts.PrepareOfficialAnimation(bundle.BaseDocument.FileName, bundle.BaseDocument.Data)
	if err != nil {
		return CommandResult{}, fmt.Errorf("prepare official gift animation: %w", err)
	}
	assetHashes := []string{bundle.BaseDocument.SHA256}
	rarityCounts := map[string]int{}
	var background *domain.StarGiftBackground
	if bundle.Gift.Background != nil {
		background = &domain.StarGiftBackground{
			CenterColor: bundle.Gift.Background.CenterColor,
			EdgeColor:   bundle.Gift.Background.EdgeColor,
			TextColor:   bundle.Gift.Background.TextColor,
		}
	}
	var collectible *domain.StarGiftCollectibleWrite
	if req.IncludeCollectible {
		if bundle.Collectible == nil {
			return CommandResult{}, domain.ErrStarGiftCollectibleInvalid
		}
		models := make([]domain.StarGiftCollectibleAttribute, 0, len(bundle.Collectible.Models))
		for index, value := range bundle.Collectible.Models {
			animation, err := s.gifts.PrepareOfficialAnimation(value.Document.FileName, value.Document.Data)
			if err != nil {
				return CommandResult{}, fmt.Errorf("prepare official model %q: %w", value.Name, err)
			}
			rarityKind, permille, err := officialRarity(value.Rarity)
			if err != nil {
				return CommandResult{}, err
			}
			models = append(models, domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectibleModel,
				Name: strings.TrimSpace(value.Name), RarityKind: rarityKind, RarityPermille: permille,
				Crafted: value.Crafted, OfficialDocumentID: value.DocumentID, SortOrder: index, Animation: &animation})
			assetHashes = append(assetHashes, value.Document.SHA256)
			rarityCounts[string(rarityKind)]++
		}
		patterns := make([]domain.StarGiftCollectibleAttribute, 0, len(bundle.Collectible.Patterns))
		for index, value := range bundle.Collectible.Patterns {
			animation, err := s.gifts.PrepareOfficialAnimation(value.Document.FileName, value.Document.Data)
			if err != nil {
				return CommandResult{}, fmt.Errorf("prepare official pattern %q: %w", value.Name, err)
			}
			rarityKind, permille, err := officialRarity(value.Rarity)
			if err != nil {
				return CommandResult{}, err
			}
			patterns = append(patterns, domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectiblePattern,
				Name: strings.TrimSpace(value.Name), RarityKind: rarityKind, RarityPermille: permille,
				OfficialDocumentID: value.DocumentID, SortOrder: index, Animation: &animation})
			assetHashes = append(assetHashes, value.Document.SHA256)
			rarityCounts[string(rarityKind)]++
		}
		backdrops := make([]domain.StarGiftCollectibleAttribute, 0, len(bundle.Collectible.Backdrops))
		for index, value := range bundle.Collectible.Backdrops {
			rarityKind, permille, err := officialRarity(value.Rarity)
			if err != nil {
				return CommandResult{}, err
			}
			backdrops = append(backdrops, domain.StarGiftCollectibleAttribute{Kind: domain.StarGiftCollectibleBackdrop,
				Name: strings.TrimSpace(value.Name), BackdropID: value.BackdropID, CenterColor: value.CenterColor,
				EdgeColor: value.EdgeColor, PatternColor: value.PatternColor, TextColor: value.TextColor,
				RarityKind: rarityKind, RarityPermille: permille, SortOrder: index})
			rarityCounts[string(rarityKind)]++
		}
		collectible = &domain.StarGiftCollectibleWrite{GiftID: req.GiftID, UpgradeStars: req.UpgradeStars,
			SupplyTotal: req.SupplyTotal, SlugPrefix: req.SlugPrefix, Models: models, Patterns: patterns, Backdrops: backdrops,
			Actor: req.Actor, CommandID: req.CommandID, OfficialGiftID: sourceID,
			SourceManifestSHA256: append([]byte(nil), bundle.ManifestSHA256...)}
		validation := *collectible
		if validation.GiftID == 0 {
			validation.GiftID = 1
		}
		if err := domain.ValidateStarGiftCollectibleDraft(validation); err != nil {
			return CommandResult{}, err
		}
	}
	req.ManifestSHA256 = hex.EncodeToString(bundle.ManifestSHA256)
	sort.Strings(assetHashes)
	req.AssetSHA256 = assetHashes
	write := domain.StarGiftCatalogBundleWrite{Catalog: domain.StarGiftCatalogWrite{
		GiftID: req.GiftID, Title: req.Title, Stars: req.Stars, ConvertStars: req.ConvertStars,
		Enabled: req.Enabled, SortOrder: req.SortOrder, Animation: baseAnimation, Actor: req.Actor, CommandID: req.CommandID,
		OfficialGiftID: sourceID, SourceManifestSHA256: append([]byte(nil), bundle.ManifestSHA256...),
		OfficialSourceJSON: append([]byte(nil), bundle.SourceJSON...),
		// The snapshot describes Telegram's global market, not this deployment's
		// inventory. Keep the complete source JSON as provenance, while publishing
		// regular official imports as a fresh, locally purchasable catalog entry.
		// Local resale counters and sale dates are derived by lifecycle writes.
		// Auctions are the one exception; see auctionLimited above.
		Limited: auctionLimited, SoldOut: false, Birthday: bundle.Gift.Birthday,
		RequirePremium: bundle.Gift.RequirePremium, LimitedPerUser: bundle.Gift.LimitedPerUser,
		PeerColorAvailable: bundle.Gift.PeerColorAvailable, Auction: bundle.Gift.Auction,
		AvailabilityRemains: 0, AvailabilityTotal: auctionTotal,
		AvailabilityResale: 0, FirstSaleDate: 0,
		LastSaleDate: 0, ResellMinStars: 0,
		PerUserTotal: bundle.Gift.PerUserTotal, LockedUntilDate: lockedUntilDate,
		AuctionSlug: bundle.Gift.AuctionSlug, GiftsPerRound: bundle.Gift.GiftsPerRound,
		AuctionStartDate: bundle.Gift.AuctionStartDate, UpgradeVariants: bundle.Gift.UpgradeVariants,
		Background: background,
	}, Collectible: collectible}
	// Only auctions are touched: a snapshot auction_start_date may be zero or in the
	// past, and the revision's CHECK requires it to be positive. The full
	// ValidateLifecycleAuthoring is deliberately not run here — it is the authoring
	// contract for operator input, and a snapshot legitimately carries a
	// locked_until_date that has already elapsed.
	write.Catalog.NormalizeLifecycleAuthoring(int(s.now().Unix()))
	return s.runCommand(ctx, req.CommandMeta, ActionImportOfficialStarGift, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"source_gift_id": req.SourceGiftID, "gift_id": strconv.FormatInt(req.GiftID, 10),
			"manifest_sha256": req.ManifestSHA256, "title": req.Title, "stars": strconv.FormatInt(req.Stars, 10),
			"convert_stars": strconv.FormatInt(req.ConvertStars, 10), "include_collectible": req.IncludeCollectible,
			"verified_asset_count": len(assetHashes), "rarity_counts": rarityCounts,
			"official_limited": bundle.Gift.Limited, "official_sold_out": bundle.Gift.SoldOut,
			"official_auction": bundle.Gift.Auction, "official_birthday": bundle.Gift.Birthday,
			"official_require_premium":      bundle.Gift.RequirePremium,
			"official_availability_remains": bundle.Gift.AvailabilityRemains,
			"official_availability_total":   bundle.Gift.AvailabilityTotal,
			"official_availability_resale":  bundle.Gift.AvailabilityResale,
		}
		if lockedUntilDate > 0 {
			details["locked_until_date"] = lockedUntilDate
			details["locked_until_scheduled_by_operator"] = req.LockedUntilDate > 0
		}
		if write.Catalog.Auction {
			details["auction_availability_total"] = write.Catalog.AvailabilityTotal
			details["auction_start_date"] = write.Catalog.AuctionStartDate
		}
		if bundle.Collectible != nil {
			details["models"] = len(bundle.Collectible.Models)
			details["patterns"] = len(bundle.Collectible.Patterns)
			details["backdrops"] = len(bundle.Collectible.Backdrops)
			crafted := 0
			for _, model := range bundle.Collectible.Models {
				if model.Crafted {
					crafted++
				}
			}
			details["crafted_models"] = crafted
		}
		if req.DryRun {
			return CommandResult{Message: "official star gift bundle validated", Details: details}, nil
		}
		result, err := s.gifts.CreateCatalogBundle(ctx, write)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["gift_id"] = strconv.FormatInt(result.Catalog.Gift.ID, 10)
		details["catalog_revision_id"] = strconv.FormatInt(result.Catalog.Gift.RevisionID, 10)
		if result.Collectible != nil {
			details["collectible_revision_id"] = strconv.FormatInt(result.Collectible.ID, 10)
			details["collectible_revision"] = result.Collectible.Revision
		}
		return CommandResult{Message: "official star gift bundle imported", Details: details}, nil
	})
}

func officialRarity(value officialgifts.Rarity) (domain.StarGiftAttributeRarityKind, int, error) {
	kind := domain.StarGiftAttributeRarityKind(strings.ToLower(strings.TrimSpace(value.Kind)))
	if !kind.Valid() {
		return "", 0, domain.ErrStarGiftCollectibleInvalid
	}
	if kind == domain.StarGiftRarityPermille {
		if value.Permille == nil || *value.Permille <= 0 || *value.Permille > 1000 {
			return "", 0, domain.ErrStarGiftCollectibleInvalid
		}
		return kind, *value.Permille, nil
	}
	if value.Permille != nil {
		return "", 0, domain.ErrStarGiftCollectibleInvalid
	}
	return kind, 0, nil
}

func (s *Service) PublishStarGiftCollectibles(ctx context.Context, req PublishStarGiftCollectiblesRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil {
		return CommandResult{}, fmt.Errorf("star gift service is not configured")
	}
	toAttributes := func(kind domain.StarGiftCollectibleAttributeKind, uploads []StarGiftCollectibleAnimationUpload) ([]domain.StarGiftCollectibleAttribute, error) {
		attributes := make([]domain.StarGiftCollectibleAttribute, len(uploads))
		for i := range uploads {
			animation, err := s.gifts.PrepareAnimation(uploads[i].FileName, uploads[i].Data)
			if err != nil {
				return nil, fmt.Errorf("prepare %s %q: %w", kind, uploads[i].Name, err)
			}
			uploads[i].ContentSHA = hex.EncodeToString(animation.SHA256)
			attributes[i] = domain.StarGiftCollectibleAttribute{
				Kind: kind, Name: strings.TrimSpace(uploads[i].Name), RarityKind: domain.StarGiftRarityPermille,
				RarityPermille: uploads[i].RarityPermille,
				SortOrder:      uploads[i].SortOrder, Animation: &animation,
			}
		}
		return attributes, nil
	}
	models, err := toAttributes(domain.StarGiftCollectibleModel, req.Models)
	if err != nil {
		return CommandResult{}, err
	}
	patterns, err := toAttributes(domain.StarGiftCollectiblePattern, req.Patterns)
	if err != nil {
		return CommandResult{}, err
	}
	backdrops := make([]domain.StarGiftCollectibleAttribute, len(req.Backdrops))
	for i, backdrop := range req.Backdrops {
		backdrops[i] = domain.StarGiftCollectibleAttribute{
			Kind: domain.StarGiftCollectibleBackdrop, Name: strings.TrimSpace(backdrop.Name), BackdropID: backdrop.BackdropID,
			CenterColor: backdrop.CenterColor, EdgeColor: backdrop.EdgeColor, PatternColor: backdrop.PatternColor,
			TextColor: backdrop.TextColor, RarityKind: domain.StarGiftRarityPermille,
			RarityPermille: backdrop.RarityPermille, SortOrder: backdrop.SortOrder,
		}
	}
	write := domain.StarGiftCollectibleWrite{
		GiftID: req.GiftID, UpgradeStars: req.UpgradeStars, SupplyTotal: req.SupplyTotal,
		SlugPrefix: strings.ToLower(strings.TrimSpace(req.SlugPrefix)), Models: models, Patterns: patterns, Backdrops: backdrops,
		Actor: req.Actor, CommandID: req.CommandID,
	}
	if err := domain.ValidateStarGiftCollectibleDraft(write); err != nil {
		return CommandResult{}, err
	}
	// Persist normalized content hashes in the command payload so retries with changed files are
	// rejected by the shared idempotency boundary even though raw file bytes are not audit-logged.
	for i := range req.Models {
		req.Models[i].ContentSHA = hex.EncodeToString(models[i].Animation.SHA256)
	}
	for i := range req.Patterns {
		req.Patterns[i].ContentSHA = hex.EncodeToString(patterns[i].Animation.SHA256)
	}
	return s.runCommand(ctx, req.CommandMeta, ActionPublishGiftCollectibles, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"gift_id": strconv.FormatInt(req.GiftID, 10), "upgrade_stars": strconv.FormatInt(req.UpgradeStars, 10),
			"supply_total": req.SupplyTotal,
			"slug_prefix":  write.SlugPrefix, "models": collectibleUploadDetails(req.Models),
			"patterns": collectibleUploadDetails(req.Patterns), "backdrops": len(req.Backdrops),
		}
		if req.DryRun {
			return CommandResult{Message: "star gift collectible pool validated", Details: details}, nil
		}
		revision, err := s.gifts.CreateCollectibleRevision(ctx, write)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["revision_id"] = strconv.FormatInt(revision.ID, 10)
		details["revision"] = revision.Revision
		details["published"] = revision.Published
		return CommandResult{Message: "star gift collectible pool published", Details: details}, nil
	})
}

func collectibleUploadDetails(uploads []StarGiftCollectibleAnimationUpload) []map[string]any {
	details := make([]map[string]any, 0, len(uploads))
	for _, upload := range uploads {
		details = append(details, map[string]any{
			"name": strings.TrimSpace(upload.Name), "rarity_permille": upload.RarityPermille,
			"sort_order": upload.SortOrder, "source_name": upload.FileName, "sha256": upload.ContentSHA,
		})
	}
	return details
}

func (s *Service) SetStarGiftEnabled(ctx context.Context, req SetStarGiftEnabledRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil || req.GiftID <= 0 {
		return CommandResult{}, fmt.Errorf("valid star gift and service are required")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStarGiftEnabled, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"gift_id": strconv.FormatInt(req.GiftID, 10), "enabled": req.Enabled}
		if req.DryRun {
			return CommandResult{Message: "star gift state change validated", Details: details}, nil
		}
		changed, err := s.gifts.SetCatalogEnabled(ctx, req.GiftID, req.Enabled)
		details["changed"] = changed
		return CommandResult{Message: "star gift state updated", Details: details}, err
	})
}

func (s *Service) SetStarGiftSortOrder(ctx context.Context, req SetStarGiftSortOrderRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil || req.GiftID <= 0 || req.SortOrder < math.MinInt32 || req.SortOrder > math.MaxInt32 {
		return CommandResult{}, fmt.Errorf("valid star gift and service are required")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStarGiftSortOrder, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"gift_id": strconv.FormatInt(req.GiftID, 10), "sort_order": req.SortOrder}
		if req.DryRun {
			return CommandResult{Message: "star gift order change validated", Details: details}, nil
		}
		changed, err := s.gifts.SetCatalogSortOrder(ctx, req.GiftID, req.SortOrder)
		details["changed"] = changed
		return CommandResult{Message: "star gift order updated", Details: details}, err
	})
}

func (s *Service) DeleteStarGift(ctx context.Context, req DeleteStarGiftRequest) (CommandResult, error) {
	if s == nil || s.gifts == nil || req.GiftID <= 0 {
		return CommandResult{}, fmt.Errorf("valid star gift and service are required")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteStarGift, 0, domain.Peer{}, req, func() (CommandResult, error) {
		// DeleteEverywhere itself honors DryRun (it runs every real query
		// inside a transaction it then rolls back), so the preview below
		// always reflects the actual counts and compensation figures a real
		// run would produce -- never a canned "validated" message.
		result, err := s.gifts.DeleteEverywhere(ctx, req.GiftID, req.Refund, req.DryRun, int(s.now().Unix()))
		if err != nil {
			return CommandResult{}, err
		}
		compensation := make([]map[string]any, 0, len(result.Compensation))
		for _, c := range result.Compensation {
			if c.Total() <= 0 {
				continue
			}
			compensation = append(compensation, map[string]any{
				"user_id":  strconv.FormatInt(c.UserID, 10),
				"purchase": strconv.FormatInt(c.Purchase, 10),
				"transfer": strconv.FormatInt(c.Transfer, 10),
				"upgrade":  strconv.FormatInt(c.Upgrade, 10),
				"total":    strconv.FormatInt(c.Total(), 10),
			})
		}
		details := map[string]any{
			"gift_id":            strconv.FormatInt(req.GiftID, 10),
			"revoked_instances":  result.RevokedInstances,
			"cancelled_listings": result.CancelledListings,
			"cancelled_offers":   result.CancelledOffers,
			"refunded":           result.Refunded,
			"total_compensation": strconv.FormatInt(result.TotalCompensation, 10),
			"compensation":       compensation,
		}
		message := "star gift deleted everywhere"
		if req.DryRun {
			message = "star gift deletion validated"
		}
		return CommandResult{Message: message, Details: details}, nil
	})
}

func (s *Service) StarGiftAnimation(ctx context.Context, giftID int64) ([]byte, bool, error) {
	if s == nil || s.gifts == nil || giftID <= 0 {
		return nil, false, nil
	}
	return s.gifts.AnimationJSON(ctx, giftID)
}

// EmojiAnimation returns the Lottie JSON for a custom-emoji document (admin emoji browser preview).
func (s *Service) EmojiAnimation(ctx context.Context, documentID int64) ([]byte, bool, error) {
	if s == nil || s.emoji == nil || documentID <= 0 {
		return nil, false, nil
	}
	return s.emoji.DocumentAnimationJSON(ctx, documentID)
}

func (s *Service) SetStickerSetArchived(ctx context.Context, req SetStickerSetArchivedRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil || req.SetID <= 0 {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStickerSetArchived, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"set_id": strconv.FormatInt(req.SetID, 10), "archived": req.Archived}
		if req.DryRun {
			return CommandResult{Message: "sticker set state change validated", Details: details}, nil
		}
		changed, err := s.stickerSets.AdminSetStickerSetArchived(ctx, req.SetID, req.Archived)
		details["changed"] = changed
		return CommandResult{Message: "sticker set state updated", Details: details}, err
	})
}

func (s *Service) SetStickerSetSortOrder(ctx context.Context, req SetStickerSetSortOrderRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil || req.SetID <= 0 || req.SortOrder < math.MinInt32 || req.SortOrder > math.MaxInt32 {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetStickerSetSortOrder, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"set_id": strconv.FormatInt(req.SetID, 10), "sort_order": req.SortOrder}
		if req.DryRun {
			return CommandResult{Message: "sticker set order change validated", Details: details}, nil
		}
		changed, err := s.stickerSets.AdminSetStickerSetSortOrder(ctx, req.SetID, req.SortOrder)
		details["changed"] = changed
		return CommandResult{Message: "sticker set order updated", Details: details}, err
	})
}

func (s *Service) RenameStickerSet(ctx context.Context, req RenameStickerSetRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil || req.SetID <= 0 || strings.TrimSpace(req.Title) == "" {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRenameStickerSet, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"set_id": strconv.FormatInt(req.SetID, 10), "title": req.Title}
		if req.DryRun {
			return CommandResult{Message: "sticker set rename validated", Details: details}, nil
		}
		set, err := s.stickerSets.AdminRenameStickerSet(ctx, req.SetID, req.Title)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["title"] = set.Title
		return CommandResult{Message: "sticker set renamed", Details: details}, nil
	})
}

func (s *Service) DeleteStickerSet(ctx context.Context, req DeleteStickerSetRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil || req.SetID <= 0 {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteStickerSet, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"set_id": strconv.FormatInt(req.SetID, 10)}
		if req.DryRun {
			return CommandResult{Message: "sticker set deletion validated", Details: details}, nil
		}
		kind, err := s.stickerSets.AdminDeleteStickerSet(ctx, req.SetID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["kind"] = string(kind)
		return CommandResult{Message: "sticker set deleted", Details: details}, nil
	})
}

func (s *Service) CreateStickerSet(ctx context.Context, req CreateStickerSetRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.ShortName) == "" || strings.TrimSpace(req.Emoji) == "" {
		return CommandResult{}, domain.ErrStickerSetFileInvalid
	}
	mimeType, ok := s.stickerSets.ValidateStickerMaterialUpload(req.FileName, req.Data)
	if !ok {
		return CommandResult{}, domain.ErrStickerSetFileInvalid
	}
	kind := domain.StickerSetKindStickers
	if req.Kind == string(domain.StickerSetKindEmoji) {
		kind = domain.StickerSetKindEmoji
	}
	digest := sha256.Sum256(req.Data)
	req.ContentSHA256 = hex.EncodeToString(digest[:])
	return s.runCommand(ctx, req.CommandMeta, ActionCreateStickerSet, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"title": req.Title, "short_name": req.ShortName, "kind": string(kind),
			"file_name": req.FileName, "mime_type": mimeType, "bytes": len(req.Data),
		}
		if err := s.stickerSets.ValidateAdminCreateStickerSet(ctx, req.Title, req.ShortName, req.Emoji, kind); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "sticker pack validated", Details: details}, nil
		}
		doc, err := s.stickerSets.AdminUploadStickerMaterial(ctx, req.FileName, req.Data)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		set, _, err := s.stickerSets.AdminCreateStickerSet(ctx, domain.CreateStickerSetRequest{
			Title:     req.Title,
			ShortName: req.ShortName,
			Kind:      kind,
			Items: []domain.StickerSetItemInput{{
				DocumentID:         doc.ID,
				DocumentAccessHash: doc.AccessHash,
				Emoji:              req.Emoji,
				Keywords:           req.Keywords,
			}},
		})
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["set_id"] = strconv.FormatInt(set.ID, 10)
		details["short_name"] = set.ShortName
		return CommandResult{Message: "sticker pack created", Details: details}, nil
	})
}

func (s *Service) AddStickerToSet(ctx context.Context, req AddStickerToSetRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil || req.SetID <= 0 {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	if strings.TrimSpace(req.Emoji) == "" {
		return CommandResult{}, domain.ErrStickerSetEmojiInvalid
	}
	mimeType, ok := s.stickerSets.ValidateStickerMaterialUpload(req.FileName, req.Data)
	if !ok {
		return CommandResult{}, domain.ErrStickerSetFileInvalid
	}
	digest := sha256.Sum256(req.Data)
	req.ContentSHA256 = hex.EncodeToString(digest[:])
	return s.runCommand(ctx, req.CommandMeta, ActionAddStickerToSet, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{
			"set_id": strconv.FormatInt(req.SetID, 10), "emoji": req.Emoji,
			"file_name": req.FileName, "mime_type": mimeType, "bytes": len(req.Data),
		}
		// Validate the target and item before materializing a loose
		// document/blob. Keeping this inside runCommand preserves replay of a
		// previously completed command even if the pack has since changed.
		if err := s.stickerSets.ValidateAdminAddStickerToSet(ctx, req.SetID, req.Emoji); err != nil {
			return CommandResult{Details: details}, err
		}
		if req.DryRun {
			return CommandResult{Message: "sticker upload validated", Details: details}, nil
		}
		doc, err := s.stickerSets.AdminUploadStickerMaterial(ctx, req.FileName, req.Data)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		set, _, err := s.stickerSets.AdminAddStickerToSet(ctx, req.SetID, domain.StickerSetItemInput{
			DocumentID:         doc.ID,
			DocumentAccessHash: doc.AccessHash,
			Emoji:              req.Emoji,
			Keywords:           req.Keywords,
		})
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["document_id"] = strconv.FormatInt(doc.ID, 10)
		details["count"] = set.Count
		return CommandResult{Message: "sticker added", Details: details}, nil
	})
}

func (s *Service) RemoveStickerFromSet(ctx context.Context, req RemoveStickerFromSetRequest) (CommandResult, error) {
	if s == nil || s.stickerSets == nil || req.SetID <= 0 || req.DocumentID <= 0 {
		return CommandResult{}, domain.ErrStickerSetInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionRemoveStickerFromSet, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"set_id": strconv.FormatInt(req.SetID, 10), "document_id": strconv.FormatInt(req.DocumentID, 10)}
		if req.DryRun {
			return CommandResult{Message: "sticker removal validated", Details: details}, nil
		}
		set, _, err := s.stickerSets.AdminRemoveStickerFromSet(ctx, req.SetID, req.DocumentID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["count"] = set.Count
		return CommandResult{Message: "sticker removed", Details: details}, nil
	})
}

const maxStickerDocumentBytes = 8 << 20

// StickerDocumentAnimation returns a sticker/custom-emoji document preview for
// the admin console. TGS is decompressed to Lottie JSON; static sticker images
// are returned as-is with a safe image content type.
func (s *Service) StickerDocumentAnimation(ctx context.Context, documentID int64) ([]byte, string, bool, error) {
	if s == nil || s.photos == nil || documentID <= 0 {
		return nil, "", false, nil
	}
	chunk, found, err := s.photos.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: fmt.Sprintf("doc:%d", documentID),
		Limit:       maxStickerDocumentBytes + 1,
	})
	if err != nil {
		return nil, "", false, err
	}
	if !found || chunk.Total <= 0 || chunk.Total > maxStickerDocumentBytes || int64(len(chunk.Bytes)) != chunk.Total {
		return nil, "", false, nil
	}
	data := chunk.Bytes
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "", false, nil
		}
		defer reader.Close()
		decompressed, err := io.ReadAll(io.LimitReader(reader, maxStickerDocumentBytes))
		if err != nil {
			return nil, "", false, nil
		}
		return decompressed, "application/json; charset=utf-8", true, nil
	}
	detected := http.DetectContentType(data)
	if !isSafeStickerPreviewImageType(detected) {
		return nil, "", false, nil
	}
	return data, detected, true, nil
}

func isSafeStickerPreviewImageType(value string) bool {
	switch value {
	case "image/webp", "image/png", "image/jpeg", "image/gif":
		return true
	default:
		return false
	}
}

func (s *Service) GifCatalog(ctx context.Context) ([]domain.GifCatalogEntry, error) {
	if s == nil || s.gifCatalog == nil {
		return nil, domain.ErrGifCatalogUnavailable
	}
	return s.gifCatalog.AdminListGifCatalog(ctx)
}

func (s *Service) CreateGifCatalogEntry(ctx context.Context, req CreateGifCatalogEntryRequest) (CommandResult, error) {
	if s == nil || s.gifCatalog == nil {
		return CommandResult{}, domain.ErrGifCatalogUnavailable
	}
	if strings.TrimSpace(req.Title) == "" {
		return CommandResult{}, domain.ErrGifCatalogEntryInvalid
	}
	mimeType, ok := s.gifCatalog.ValidateGifUpload(req.FileName, req.Data)
	if !ok {
		return CommandResult{}, domain.ErrGifCatalogFileInvalid
	}
	digest := sha256.Sum256(req.Data)
	req.ContentSHA256 = hex.EncodeToString(digest[:])
	return s.runCommand(ctx, req.CommandMeta, ActionCreateGifCatalogEntry, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"title": req.Title, "file_name": req.FileName, "mime_type": mimeType, "bytes": len(req.Data)}
		entries, err := s.gifCatalog.AdminListGifCatalog(ctx)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		if len(entries) >= domain.MaxGifCatalogEntries {
			return CommandResult{Details: details}, domain.ErrGifCatalogFull
		}
		if req.DryRun {
			return CommandResult{Message: "gif catalog entry validated", Details: details}, nil
		}
		doc, err := s.gifCatalog.AdminUploadGifMaterial(ctx, req.FileName, req.Data)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		entry, err := s.gifCatalog.AdminCreateGifCatalogEntry(ctx, req.Title, doc.ID)
		if err != nil {
			return CommandResult{Details: details}, err
		}
		details["id"], details["document_id"] = strconv.FormatInt(entry.ID, 10), strconv.FormatInt(doc.ID, 10)
		return CommandResult{Message: "gif catalog entry created", Details: details}, nil
	})
}

func (s *Service) SetGifCatalogEnabled(ctx context.Context, req SetGifCatalogEnabledRequest) (CommandResult, error) {
	if s == nil || s.gifCatalog == nil || req.ID <= 0 {
		return CommandResult{}, domain.ErrGifCatalogEntryInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetGifCatalogEnabled, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"id": strconv.FormatInt(req.ID, 10), "enabled": req.Enabled}
		if req.DryRun {
			return CommandResult{Message: "gif catalog state validated", Details: details}, nil
		}
		changed, err := s.gifCatalog.AdminSetGifCatalogEnabled(ctx, req.ID, req.Enabled)
		details["changed"] = changed
		return CommandResult{Message: "gif catalog state updated", Details: details}, err
	})
}

func (s *Service) SetGifCatalogSortOrder(ctx context.Context, req SetGifCatalogSortOrderRequest) (CommandResult, error) {
	if s == nil || s.gifCatalog == nil || req.ID <= 0 {
		return CommandResult{}, domain.ErrGifCatalogEntryInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetGifCatalogSortOrder, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"id": strconv.FormatInt(req.ID, 10), "sort_order": req.SortOrder}
		if req.DryRun {
			return CommandResult{Message: "gif catalog order validated", Details: details}, nil
		}
		changed, err := s.gifCatalog.AdminSetGifCatalogSortOrder(ctx, req.ID, req.SortOrder)
		details["changed"] = changed
		return CommandResult{Message: "gif catalog order updated", Details: details}, err
	})
}

func (s *Service) DeleteGifCatalogEntry(ctx context.Context, req DeleteGifCatalogEntryRequest) (CommandResult, error) {
	if s == nil || s.gifCatalog == nil || req.ID <= 0 {
		return CommandResult{}, domain.ErrGifCatalogEntryInvalid
	}
	return s.runCommand(ctx, req.CommandMeta, ActionDeleteGifCatalogEntry, 0, domain.Peer{}, req, func() (CommandResult, error) {
		details := map[string]any{"id": strconv.FormatInt(req.ID, 10)}
		if req.DryRun {
			return CommandResult{Message: "gif catalog deletion validated", Details: details}, nil
		}
		changed, err := s.gifCatalog.AdminDeleteGifCatalogEntry(ctx, req.ID)
		details["changed"] = changed
		return CommandResult{Message: "gif catalog entry deleted", Details: details}, err
	})
}

func (s *Service) GifCatalogDocumentPreview(ctx context.Context, documentID int64) ([]byte, string, bool, error) {
	if s == nil || s.gifCatalog == nil || documentID <= 0 {
		return nil, "", false, nil
	}
	chunk, found, err := s.gifCatalog.GetFile(ctx, domain.FileDownloadRequest{LocationKey: fmt.Sprintf("doc:%d", documentID), Limit: domain.MaxGifCatalogDocumentSize + 1})
	if err != nil || !found {
		return nil, "", found, err
	}
	if chunk.Total <= 0 || chunk.Total > domain.MaxGifCatalogDocumentSize || int64(len(chunk.Bytes)) != chunk.Total {
		return nil, "", false, nil
	}
	detected := http.DetectContentType(chunk.Bytes)
	if detected != "video/mp4" && !strings.HasPrefix(detected, "video/") {
		return nil, "", false, nil
	}
	return chunk.Bytes, detected, true, nil
}

func (s *Service) AccountAvatar(ctx context.Context, userID int64) ([]byte, string, bool, error) {
	if s == nil || s.photos == nil || userID <= 0 {
		return nil, "", false, nil
	}
	photo, found, err := s.photos.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, userID, domain.ProfilePhotoKindProfile)
	if err != nil || !found {
		return nil, "", found, err
	}
	return s.avatarBytes(ctx, photo)
}

func (s *Service) ChannelAvatar(ctx context.Context, channelID int64) ([]byte, string, bool, error) {
	if s == nil || s.photos == nil || s.channels == nil || channelID <= 0 {
		return nil, "", false, nil
	}
	channel, err := s.channels.GetChannelByID(ctx, channelID)
	if err != nil {
		return nil, "", false, err
	}
	if channel.PhotoID == 0 {
		return nil, "", false, nil
	}
	photo, found, err := s.photos.GetPhoto(ctx, channel.PhotoID)
	if err != nil || !found {
		return nil, "", found, err
	}
	return s.avatarBytes(ctx, photo)
}

func (s *Service) avatarBytes(ctx context.Context, photo domain.Photo) ([]byte, string, bool, error) {
	size, inline, ok := bestAccountPhotoSize(photo.Sizes)
	if !ok {
		return nil, "", false, nil
	}
	data := inline
	if len(data) == 0 {
		chunk, found, err := s.photos.GetFile(ctx, domain.FileDownloadRequest{
			LocationKey: fmt.Sprintf("photo:%d:%s", photo.ID, size.Type),
			Limit:       MaxAccountAvatarBytes + 1,
		})
		if err != nil || !found {
			return nil, "", found, err
		}
		if chunk.Total <= 0 || chunk.Total > MaxAccountAvatarBytes || int64(len(chunk.Bytes)) != chunk.Total {
			return nil, "", false, nil
		}
		data = chunk.Bytes
	}
	if len(data) == 0 || len(data) > MaxAccountAvatarBytes {
		return nil, "", false, nil
	}
	mimeType := http.DetectContentType(data)
	if !safeAccountImageType(mimeType) {
		return nil, "", false, nil
	}
	return data, mimeType, true, nil
}

func bestAccountPhotoSize(sizes []domain.PhotoSize) (domain.PhotoSize, []byte, bool) {
	var best domain.PhotoSize
	var bestBytes []byte
	bestScore := int64(-1)
	for _, size := range sizes {
		if !validAccountPhotoSizeType(size.Type) {
			continue
		}
		var inline []byte
		switch size.Kind {
		case domain.PhotoSizeKindCached:
			if len(size.Bytes) == 0 || len(size.Bytes) > MaxAccountAvatarBytes {
				continue
			}
			inline = size.Bytes
		case domain.PhotoSizeKindDefault, domain.PhotoSizeKindProgressive:
		default:
			continue
		}
		score := int64(size.W) * int64(size.H)
		if score <= 0 {
			score = int64(size.Size)
		}
		if score > bestScore {
			best, bestBytes, bestScore = size, inline, score
		}
	}
	return best, bestBytes, bestScore >= 0
}

func validAccountPhotoSizeType(value string) bool {
	if value == "" || len(value) > 8 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
}

func safeAccountImageType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func (s *Service) StarGiftCollectibles(ctx context.Context, giftID int64) (domain.StarGiftUpgradePreview, bool, error) {
	if s == nil || s.gifts == nil || giftID <= 0 {
		return domain.StarGiftUpgradePreview{}, false, nil
	}
	return s.gifts.CollectiblePreview(ctx, giftID)
}

func (s *Service) StarGiftCollectibleAnimation(ctx context.Context, giftID int64, kind domain.StarGiftCollectibleAttributeKind, attributeID int64) ([]byte, bool, error) {
	if s == nil || s.gifts == nil || giftID <= 0 || attributeID <= 0 {
		return nil, false, nil
	}
	if kind != domain.StarGiftCollectibleModel && kind != domain.StarGiftCollectiblePattern {
		return nil, false, domain.ErrStarGiftCollectibleInvalid
	}
	return s.gifts.CollectibleAnimationJSON(ctx, giftID, kind, attributeID)
}

func (s *Service) runCommand(ctx context.Context, meta CommandMeta, action string, targetUserID int64, targetPeer domain.Peer, request any, fn func() (CommandResult, error)) (CommandResult, error) {
	if s == nil || s.commands == nil {
		return CommandResult{}, fmt.Errorf("admin command store is not configured")
	}
	meta.CommandID = strings.TrimSpace(meta.CommandID)
	meta.Actor = strings.TrimSpace(meta.Actor)
	meta.Reason = strings.TrimSpace(meta.Reason)
	if meta.CommandID == "" || len(meta.CommandID) > maxCommandIDLength {
		return CommandResult{}, fmt.Errorf("command_id is required and must be <= %d bytes", maxCommandIDLength)
	}
	if meta.Actor == "" || len(meta.Actor) > maxActorLength {
		return CommandResult{}, fmt.Errorf("actor is required and must be <= %d bytes", maxActorLength)
	}
	if meta.Reason == "" || len(meta.Reason) > maxReasonLength {
		return CommandResult{}, fmt.Errorf("reason is required and must be <= %d bytes", maxReasonLength)
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return CommandResult{}, fmt.Errorf("marshal admin request: %w", err)
	}
	cmd, created, err := s.commands.BeginCommand(ctx, domain.AdminCommand{
		CommandID:    meta.CommandID,
		Actor:        meta.Actor,
		Action:       action,
		TargetUserID: targetUserID,
		TargetPeer:   targetPeer,
		DryRun:       meta.DryRun,
		Reason:       meta.Reason,
		RequestJSON:  requestJSON,
		Status:       domain.AdminCommandRunning,
		CreatedAt:    s.now(),
	})
	if err != nil {
		return CommandResult{}, err
	}
	if !created {
		if cmd.Action != action || cmd.DryRun != meta.DryRun || !sameJSON(cmd.RequestJSON, requestJSON) {
			return CommandResult{CommandID: meta.CommandID, Action: action, Status: string(domain.AdminCommandFailed), Error: "COMMAND_ID_CONFLICT", Message: "command_id is already bound to a different request"}, fmt.Errorf("COMMAND_ID_CONFLICT")
		}
		return resultFromCommand(cmd), nil
	}
	result, opErr := fn()
	result.CommandID = meta.CommandID
	result.Action = action
	result.DryRun = meta.DryRun
	result.TargetUserID = targetUserID
	result.TargetPeer = targetPeer
	status := domain.AdminCommandCompleted
	if opErr != nil {
		status = domain.AdminCommandFailed
		result.Status = string(status)
		result.Error = opErr.Error()
		if result.Message == "" {
			result.Message = "command failed"
		}
	} else {
		result.Status = string(status)
	}
	resultJSON, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		return result, fmt.Errorf("marshal admin result: %w", marshalErr)
	}
	response := result
	if len(result.transientDetails) > 0 {
		response.Details = make(map[string]any, len(result.Details)+len(result.transientDetails))
		for key, value := range result.Details {
			response.Details[key] = value
		}
		for key, value := range result.transientDetails {
			response.Details[key] = value
		}
	}
	errorText := ""
	if opErr != nil {
		errorText = opErr.Error()
	}
	if _, err := s.commands.FinishCommand(ctx, meta.CommandID, status, resultJSON, errorText); err != nil {
		return response, err
	}
	return response, opErr
}

func sameJSON(a, b []byte) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(left, right)
}

func resultFromCommand(cmd domain.AdminCommand) CommandResult {
	var result CommandResult
	if len(cmd.ResultJSON) > 0 {
		if err := json.Unmarshal(cmd.ResultJSON, &result); err == nil {
			result.AlreadyExecuted = true
			return result
		}
	}
	result = CommandResult{
		CommandID:       cmd.CommandID,
		Action:          cmd.Action,
		Status:          string(cmd.Status),
		AlreadyExecuted: true,
		DryRun:          cmd.DryRun,
		TargetUserID:    cmd.TargetUserID,
		TargetPeer:      cmd.TargetPeer,
		Message:         "command already exists",
		Error:           cmd.Error,
	}
	return result
}

func (s *Service) notifyUserChanged(ctx context.Context, u domain.User) error {
	if s == nil || s.userNotifier == nil {
		return nil
	}
	return s.userNotifier.NotifyUserChanged(ctx, u)
}

func (s *Service) notifyUserModerationFlagsChanged(ctx context.Context, u domain.User) error {
	if s == nil || s.userModerationNotifier == nil {
		return s.notifyUserChanged(ctx, u)
	}
	return s.userModerationNotifier.NotifyUserModerationFlagsChanged(ctx, u)
}

func (s *Service) notifyAccountFreezeChanged(ctx context.Context, freeze domain.AccountFreeze) error {
	if s == nil || s.freezeNotifier == nil {
		return nil
	}
	return s.freezeNotifier.NotifyAccountFreezeChanged(ctx, freeze)
}

func (s *Service) notifyStarsBalanceChanged(ctx context.Context, balance domain.StarsBalance) error {
	if s == nil || s.starsNotifier == nil {
		return nil
	}
	return s.starsNotifier.NotifyStarsBalanceChanged(ctx, balance)
}

func (s *Service) notifyChannelChanged(ctx context.Context, ch domain.Channel) error {
	if s == nil || s.channelNotifier == nil {
		return nil
	}
	return s.channelNotifier.NotifyChannelChanged(ctx, ch)
}

func premiumCommandDetails(u domain.User, months int, now time.Time) map[string]any {
	active := u.PremiumActiveAt(now.Unix())
	base := now
	if active {
		base = time.Unix(int64(u.PremiumUntil), 0)
	}
	projected := 0
	if months > 0 {
		projected = int(base.AddDate(0, months, 0).Unix())
	}
	return map[string]any{
		"previous_premium_until":  u.PremiumUntil,
		"previous_premium_active": active,
		"months":                  months,
		"new_premium_until":       projected,
		"would_change":            months > 0 || u.PremiumUntil != 0,
	}
}

// premiumAdminActorID gives the entitlement/audit tables a stable positive
// identity for an operator name without pretending that the operator is a
// Telegram user.
func premiumAdminActorID(actor string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strings.TrimSpace(actor)))
	id := int64(h.Sum64() & 0x7fffffffffffffff)
	if id == 0 {
		return 1
	}
	return id
}

func revokeTargets(items []domain.Authorization, req RevokeSessionsRequest) ([]domain.Authorization, domain.Authorization, error) {
	if req.Hash != 0 {
		for _, a := range items {
			if a.Hash == req.Hash {
				return []domain.Authorization{a}, domain.Authorization{}, nil
			}
		}
		return nil, domain.Authorization{}, fmt.Errorf("authorization hash not found")
	}
	var keep domain.Authorization
	if req.KeepHash != 0 {
		found := false
		for _, a := range items {
			if a.Hash == req.KeepHash {
				keep = a
				found = true
				break
			}
		}
		if !found {
			return nil, domain.Authorization{}, fmt.Errorf("keep_hash authorization not found")
		}
	}
	targets := make([]domain.Authorization, 0, len(items))
	for _, a := range items {
		if req.KeepHash != 0 && a.Hash == req.KeepHash {
			continue
		}
		targets = append(targets, a)
	}
	return targets, keep, nil
}

func authorizationHashes(items []domain.Authorization) []string {
	hashes := make([]int64, 0, len(items))
	for _, a := range items {
		hashes = append(hashes, a.Hash)
	}
	sort.Slice(hashes, func(i, j int) bool { return hashes[i] < hashes[j] })
	out := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		out = append(out, authorizationHashString(hash))
	}
	return out
}

func authorizationHashString(hash int64) string {
	if hash == 0 {
		return ""
	}
	return strconv.FormatInt(hash, 10)
}

func normalizeIDs(ids []int) ([]int, error) {
	if len(ids) == 0 {
		return nil, domain.ErrMessageIDInvalid
	}
	if len(ids) > domain.MaxDeleteMessageIDs {
		return nil, fmt.Errorf("too many ids: %d > %d", len(ids), domain.MaxDeleteMessageIDs)
	}
	seen := make(map[int]struct{}, len(ids))
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return nil, domain.ErrMessageIDInvalid
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Ints(out)
	return out, nil
}

func validatePrivateMessageSelection(ownerUserID int64, peer domain.Peer, ids []int, messages []domain.Message) ([]int, []int, error) {
	foundSet := make(map[int]domain.Message, len(messages))
	for _, msg := range messages {
		foundSet[msg.ID] = msg
		if msg.OwnerUserID != ownerUserID || msg.Peer.Type != domain.PeerTypeUser || msg.Peer.ID != peer.ID {
			return nil, nil, domain.ErrMessageIDInvalid
		}
	}
	found := make([]int, 0, len(messages))
	missing := make([]int, 0)
	for _, id := range ids {
		if _, ok := foundSet[id]; ok {
			found = append(found, id)
			continue
		}
		missing = append(missing, id)
	}
	return found, missing, nil
}

func summarizeDeleteResult(res domain.DeleteMessagesResult) []any {
	out := make([]any, 0, len(res.Deleted))
	for _, item := range res.Deleted {
		ids := append([]int(nil), item.MessageIDs...)
		sort.Ints(ids)
		pts, ptsCount := item.AffectedPts()
		out = append(out, map[string]any{
			"user_id":     item.UserID,
			"message_ids": ids,
			"pts":         pts,
			"pts_count":   ptsCount,
		})
	}
	return out
}

func messageIDs(messages []domain.Message) []int {
	out := make([]int, 0, len(messages))
	for _, msg := range messages {
		out = append(out, msg.ID)
	}
	return out
}
