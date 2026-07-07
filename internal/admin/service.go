package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"telesrv/internal/domain"
)

const (
	ActionSetSendFrozen         = "account.set_send_frozen"
	ActionGrantPremium          = "account.grant_premium"
	ActionGrantStars            = "account.grant_stars"
	ActionSetVerified           = "account.set_verified"
	ActionSetChannelVerified    = "channel.set_verified"
	ActionRevokeSessions        = "account.revoke_sessions"
	ActionDeletePrivateMessages = "messages.delete_private_messages"
	ActionDeletePrivateHistory  = "messages.delete_private_history"

	maxCommandIDLength = 128
	maxActorLength     = 128
	maxReasonLength    = 1000
	maxHistoryBatches  = 100
	maxPremiumMonths   = 120
	maxStarsGrant      = 1_000_000_000
)

type CommandRepository interface {
	BeginCommand(ctx context.Context, cmd domain.AdminCommand) (domain.AdminCommand, bool, error)
	FinishCommand(ctx context.Context, commandID string, status domain.AdminCommandStatus, resultJSON []byte, errorText string) (domain.AdminCommand, error)
}

type RestrictionStore interface {
	GetSendRestriction(ctx context.Context, userID int64) (domain.AccountSendRestriction, bool, error)
	SetSendRestriction(ctx context.Context, restriction domain.AccountSendRestriction) (domain.AccountSendRestriction, error)
	IsSendFrozen(ctx context.Context, userID int64) (bool, error)
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

type Dependencies struct {
	Commands        CommandRepository
	Restrictions    RestrictionStore
	Auth            AuthService
	Revoker         AuthKeyRevoker
	Users           UsersService
	Stars           StarsService
	StarsNotifier   StarsNotifier
	UserNotifier    UserNotifier
	Channels        ChannelsService
	ChannelNotifier ChannelNotifier
	Messages        MessagesService
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
	channels        ChannelsService
	channelNotifier ChannelNotifier
	messages        MessagesService
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
	if deps.Channels != nil {
		s.channels = deps.Channels
	}
	if deps.ChannelNotifier != nil {
		s.channelNotifier = deps.ChannelNotifier
	}
	if deps.Messages != nil {
		s.messages = deps.Messages
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

type SetSendFrozenRequest struct {
	CommandMeta
	UserID int64 `json:"user_id"`
	Frozen bool  `json:"frozen"`
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

func (s *Service) CanSendMessages(ctx context.Context, userID int64) error {
	if s == nil || s.restrictions == nil || userID == 0 {
		return nil
	}
	frozen, err := s.restrictions.IsSendFrozen(ctx, userID)
	if err != nil {
		return err
	}
	if frozen {
		return domain.ErrUserSendRestricted
	}
	return nil
}

func (s *Service) SetSendFrozen(ctx context.Context, req SetSendFrozenRequest) (CommandResult, error) {
	if req.UserID <= 0 {
		return CommandResult{}, fmt.Errorf("user_id is required")
	}
	if s == nil || s.restrictions == nil {
		return CommandResult{}, fmt.Errorf("admin restriction store is not configured")
	}
	return s.runCommand(ctx, req.CommandMeta, ActionSetSendFrozen, req.UserID, domain.Peer{}, req, func() (CommandResult, error) {
		prev, found, err := s.restrictions.GetSendRestriction(ctx, req.UserID)
		if err != nil {
			return CommandResult{}, err
		}
		details := map[string]any{
			"previous_frozen": found && prev.Frozen,
			"new_frozen":      req.Frozen,
			"would_change":    !found || prev.Frozen != req.Frozen,
		}
		if req.DryRun {
			return CommandResult{Message: "dry-run completed", Details: details}, nil
		}
		updated, err := s.restrictions.SetSendRestriction(ctx, domain.AccountSendRestriction{
			UserID:    req.UserID,
			Frozen:    req.Frozen,
			Reason:    req.Reason,
			Actor:     req.Actor,
			CommandID: req.CommandID,
		})
		if err != nil {
			return CommandResult{}, err
		}
		details["updated_at"] = updated.UpdatedAt.UTC().Format(time.RFC3339)
		return CommandResult{Message: "send restriction updated", Details: details}, nil
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
