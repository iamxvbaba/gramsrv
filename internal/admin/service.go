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
	ActionSetChannelVerified      = "channel.set_verified"
	ActionRevokeSessions          = "account.revoke_sessions"
	ActionDeletePrivateMessages   = "messages.delete_private_messages"
	ActionDeletePrivateHistory    = "messages.delete_private_history"
	ActionImportStarGift          = "gifts.import"
	ActionImportOfficialStarGift  = "gifts.official.import"
	ActionPublishGiftCollectibles = "gifts.collectibles.publish"
	ActionSetStarGiftEnabled      = "gifts.set_enabled"
	ActionSetStarGiftSortOrder    = "gifts.set_sort_order"

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
	OfficialGifts   OfficialGiftsSource
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
	officialGifts   OfficialGiftsSource
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
	if deps.OfficialGifts != nil {
		s.officialGifts = deps.OfficialGifts
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
	errorText := ""
	if opErr != nil {
		errorText = opErr.Error()
	}
	if _, err := s.commands.FinishCommand(ctx, meta.CommandID, status, resultJSON, errorText); err != nil {
		return result, err
	}
	return result, opErr
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
