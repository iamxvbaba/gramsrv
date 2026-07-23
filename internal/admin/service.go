package admin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	ActionGrantStars              = "account.grant_stars"
	ActionSetVerified             = "account.set_verified"
	ActionSetUserFlags            = "account.set_flags"
	ActionSetSupport              = "account.set_support"
	ActionSetUsername             = "account.set_username"
	ActionSetUserColor            = "account.set_color"
	ActionSetUserEmojiStatus      = "account.set_emoji_status"
	ActionSetChannelUsername      = "channel.set_username"
	ActionSetChannelSettings      = "channel.set_settings"
	ActionSetChannelColor         = "channel.set_color"
	ActionSetChannelEmojiStatus   = "channel.set_emoji_status"
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
	ActionCreateBot               = "bot.create"
	ActionDeleteBot               = "bot.delete"

	maxCommandIDLength       = 128
	maxActorLength           = 128
	maxReasonLength          = 1000
	maxHistoryBatches        = 100
	maxPremiumMonths         = 120
	maxStarsGrant            = 1_000_000_000
	maxFreezeAppealURLLength = 2048
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
}

type StarsService interface {
	Credit(ctx context.Context, userID, amount int64, reason domain.StarsTransactionReason, peer domain.Peer, title, desc string) (domain.StarsBalance, error)
}

type StarsNotifier interface {
	NotifyStarsBalanceChanged(ctx context.Context, balance domain.StarsBalance) error
}

type UserNotifier interface {
	NotifyUserChanged(ctx context.Context, u domain.User) error
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
}

type OfficialGiftsSource interface {
	List(ctx context.Context) ([]officialgifts.GiftSummary, error)
	Bundle(ctx context.Context, giftID int64, includeCollectible bool) (officialgifts.Bundle, error)
}

// BotService creates bot accounts on behalf of the admin. It mirrors the
// owner-scoped /newbot flow: a bot is a users row (is_bot=true) plus a bots row
// owned by ownerUserID, and the returned token is shown once to the operator.
type BotService interface {
	CreateBot(ctx context.Context, ownerUserID int64, name, username string) (domain.User, string, error)
	DeleteBot(ctx context.Context, botUserID int64) (domain.User, error)
}

// EmojiService renders custom-emoji document animations for the admin emoji
// browser (Lottie JSON, TGS transparently decompressed).
type EmojiService interface {
	DocumentAnimationJSON(ctx context.Context, documentID int64) ([]byte, bool, error)
}

// GiftGranter delivers a catalog gift to a recipient peer on behalf of a sender
// without charging Stars. Implemented by the RPC router, it reuses the standard
// gift-delivery path (service message for users, saved-gift + admin log for
// channels) so granted gifts are indistinguishable from paid ones.
type GiftGranter interface {
	AdminGrantStarGift(ctx context.Context, grant domain.AdminStarGiftGrant) error
}

type Dependencies struct {
	Commands        CommandRepository
	Restrictions    RestrictionStore
	Auth            AuthService
	Revoker         AuthKeyRevoker
	Users           UsersService
	Stars           StarsService
	StarsNotifier   StarsNotifier
	UserNotifier    UserNotifier
	FreezeNotifier  AccountFreezeNotifier
	Channels        ChannelsService
	ChannelNotifier ChannelNotifier
	Messages        MessagesService
	Gifts           GiftsService
	GiftGranter     GiftGranter
	OfficialGifts   OfficialGiftsSource
	Bots            BotService
	Emoji           EmojiService
	Now             func() time.Time
}

type Service struct {
	commands        CommandRepository
	restrictions    RestrictionStore
	auth            AuthService
	revoker         AuthKeyRevoker
	users           UsersService
	stars           StarsService
	starsNotifier   StarsNotifier
	userNotifier    UserNotifier
	freezeNotifier  AccountFreezeNotifier
	channels        ChannelsService
	channelNotifier ChannelNotifier
	messages        MessagesService
	gifts           GiftsService
	giftGranter     GiftGranter
	officialGifts   OfficialGiftsSource
	bots            BotService
	emoji           EmojiService
	now             func() time.Time
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
	if deps.Stars != nil {
		s.stars = deps.Stars
	}
	if deps.StarsNotifier != nil {
		s.starsNotifier = deps.StarsNotifier
	}
	if deps.UserNotifier != nil {
		s.userNotifier = deps.UserNotifier
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
	if deps.Emoji != nil {
		s.emoji = deps.Emoji
	}
	if deps.Now != nil {
		s.now = deps.Now
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
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
}

type ImportOfficialStarGiftRequest struct {
	CommandMeta
	SourceGiftID       string   `json:"source_gift_id"`
	GiftID             int64    `json:"gift_id,omitempty"`
	Title              string   `json:"title"`
	Stars              int64    `json:"stars"`
	ConvertStars       int64    `json:"convert_stars"`
	Enabled            bool     `json:"enabled"`
	SortOrder          int      `json:"sort_order"`
	IncludeCollectible bool     `json:"include_collectible"`
	UpgradeStars       int64    `json:"upgrade_stars,omitempty"`
	SupplyTotal        int      `json:"supply_total,omitempty"`
	SlugPrefix         string   `json:"slug_prefix,omitempty"`
	ManifestSHA256     string   `json:"manifest_sha256,omitempty"`
	AssetSHA256        []string `json:"asset_sha256,omitempty"`
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
	UserID int64 `json:"user_id"`
	Months int   `json:"months"`
}

type GrantStarsRequest struct {
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

type DeleteBotRequest struct {
	CommandMeta
	BotUserID int64 `json:"bot_user_id"`
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
	if s == nil || s.users == nil {
		return CommandResult{}, fmt.Errorf("admin user dependency is not configured")
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
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.users.GrantPremium(ctx, req.UserID, req.Months)
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

func (s *Service) SetVerified(ctx context.Context, req SetVerifiedRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
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
		if err := s.notifyUserChanged(ctx, updated); err != nil {
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
	if (req.Hash == 0 && req.KeepHash == 0 && !req.RevokeAll) || (req.Hash != 0 && (req.KeepHash != 0 || req.RevokeAll)) {
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
			"keep_hash":     keep.Hash,
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
			if found {
				revoked = append(revoked, deleted)
			}
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
		if req.DryRun {
			return CommandResult{Message: "star gift import validated", Details: details}, nil
		}
		entry, err := s.gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
			GiftID: req.GiftID, Title: req.Title, Stars: req.Stars, ConvertStars: req.ConvertStars,
			Enabled: req.Enabled, SortOrder: req.SortOrder, Animation: animation,
			Actor: req.Actor, CommandID: req.CommandID,
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
	}
	if req.SlugPrefix = strings.ToLower(strings.TrimSpace(req.SlugPrefix)); req.SlugPrefix == "" {
		req.SlugPrefix = "official-" + req.SourceGiftID
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
		Limited: false, SoldOut: false, Birthday: bundle.Gift.Birthday,
		RequirePremium: bundle.Gift.RequirePremium, LimitedPerUser: bundle.Gift.LimitedPerUser,
		PeerColorAvailable: bundle.Gift.PeerColorAvailable, Auction: bundle.Gift.Auction,
		AvailabilityRemains: 0, AvailabilityTotal: 0,
		AvailabilityResale: 0, FirstSaleDate: 0,
		LastSaleDate: 0, ResellMinStars: 0,
		PerUserTotal: bundle.Gift.PerUserTotal, LockedUntilDate: bundle.Gift.LockedUntilDate,
		AuctionSlug: bundle.Gift.AuctionSlug, GiftsPerRound: bundle.Gift.GiftsPerRound,
		AuctionStartDate: bundle.Gift.AuctionStartDate, UpgradeVariants: bundle.Gift.UpgradeVariants,
		Background: background,
	}, Collectible: collectible}
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

func revokeTargets(items []domain.Authorization, req RevokeSessionsRequest) ([]domain.Authorization, domain.Authorization, error) {
	if req.Hash != 0 {
		for _, a := range items {
			if a.Hash == req.Hash {
				return []domain.Authorization{a}, domain.Authorization{}, nil
			}
		}
		return nil, domain.Authorization{}, nil
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

func authorizationHashes(items []domain.Authorization) []int64 {
	out := make([]int64, 0, len(items))
	for _, a := range items {
		out = append(out, a.Hash)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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
		out = append(out, map[string]any{
			"user_id":     item.UserID,
			"message_ids": ids,
			"pts":         item.Event.Pts,
			"pts_count":   item.Event.PtsCount,
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
