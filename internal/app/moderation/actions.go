package moderation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
	"telesrv/internal/store"
)

type moderationAdminActions interface {
	SetAccountFrozen(ctx context.Context, req admin.SetAccountFrozenRequest) (admin.CommandResult, error)
	SetUserFlags(ctx context.Context, req admin.SetUserFlagsRequest) (admin.CommandResult, error)
	SetChannelFlags(ctx context.Context, req admin.SetChannelFlagsRequest) (admin.CommandResult, error)
	DeletePrivateMessages(ctx context.Context, req admin.DeletePrivateMessagesRequest) (admin.CommandResult, error)
}

type moderationChannelDeleter interface {
	ModerationDeleteMessages(ctx context.Context, channelID int64, ids []int, date int) (domain.DeleteChannelMessagesResult, error)
}

type moderationChannelDeleteNotifier interface {
	NotifyModerationChannelDeletion(ctx context.Context, result domain.DeleteChannelMessagesResult)
}

type moderationAccountDeleter interface {
	ExecuteAccountDeletion(ctx context.Context, userID int64, source domain.AccountDeletionSource, reason string, now time.Time) (domain.AccountDeletionResult, error)
}

type moderationAppealLinkIssuer interface {
	IssueAppealLink(ctx context.Context, caseID, appellantUserID int64, expiresAt, now time.Time) (string, error)
}

type ActionExecutor struct {
	admin           moderationAdminActions
	channels        moderationChannelDeleter
	channelNotifier moderationChannelDeleteNotifier
	accounts        moderationAccountDeleter
	appealLinks     moderationAppealLinkIssuer
	publicBaseURL   string
	now             func() time.Time
}

type ActionExecutorOption func(*ActionExecutor)

func WithAppealLinks(issuer moderationAppealLinkIssuer, publicBaseURL string) ActionExecutorOption {
	return func(executor *ActionExecutor) {
		executor.appealLinks = issuer
		executor.publicBaseURL = strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	}
}

func WithActionClock(now func() time.Time) ActionExecutorOption {
	return func(executor *ActionExecutor) {
		if now != nil {
			executor.now = now
		}
	}
}

func NewActionExecutor(adminActions moderationAdminActions, channels moderationChannelDeleter, channelNotifier moderationChannelDeleteNotifier, accounts moderationAccountDeleter, opts ...ActionExecutorOption) *ActionExecutor {
	executor := &ActionExecutor{
		admin: adminActions, channels: channels,
		channelNotifier: channelNotifier, accounts: accounts,
		now: func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range opts {
		if opt != nil {
			opt(executor)
		}
	}
	return executor
}

type freezeAccountActionPayload struct {
	Until     time.Time `json:"until,omitempty"`
	AppealURL string    `json:"appeal_url,omitempty"`
}

type deletePrivateMessageActionPayload struct {
	OwnerUserID int64 `json:"owner_user_id"`
	IDs         []int `json:"ids"`
	Revoke      bool  `json:"revoke"`
}

type deleteChannelMessageActionPayload struct {
	IDs []int `json:"ids"`
}

func (e *ActionExecutor) Execute(ctx context.Context, detail domain.ModerationCaseDetail, action domain.ModerationAction) error {
	if e == nil || action.CaseID != detail.Case.ID {
		return domain.ErrModerationActionInvalid
	}
	actor, _, ok := decisionAuditContext(detail.Decisions, action.DecisionID)
	if !ok {
		return domain.ErrModerationActionInvalid
	}
	meta := admin.CommandMeta{
		CommandID: action.CommandID, Actor: actor,
		Reason: fmt.Sprintf("moderation case %d decision %d", detail.Case.ID, action.DecisionID),
	}
	switch action.Kind {
	case domain.ModerationActionMarkScam:
		if err := decodeStrictActionPayload(action.Payload, &struct{}{}); err != nil {
			return err
		}
		return e.setPeerFlags(ctx, detail.Case.Target, true, false, meta)
	case domain.ModerationActionMarkFake:
		if err := decodeStrictActionPayload(action.Payload, &struct{}{}); err != nil {
			return err
		}
		return e.setPeerFlags(ctx, detail.Case.Target, false, true, meta)
	case domain.ModerationActionClearPeerFlags:
		if err := decodeStrictActionPayload(action.Payload, &struct{}{}); err != nil {
			return err
		}
		return e.setPeerFlags(ctx, detail.Case.Target, false, false, meta)
	case domain.ModerationActionFreezeAccount, domain.ModerationActionUnfreezeAccount:
		if e.admin == nil || detail.Case.Target.Type != domain.PeerTypeUser {
			return domain.ErrModerationActionInvalid
		}
		var payload freezeAccountActionPayload
		if err := decodeStrictActionPayload(action.Payload, &payload); err != nil {
			return err
		}
		frozen := action.Kind == domain.ModerationActionFreezeAccount
		if !frozen && (!payload.Until.IsZero() || payload.AppealURL != "") {
			return domain.ErrModerationActionInvalid
		}
		if frozen {
			now := e.now().UTC()
			if payload.Until.IsZero() {
				payload.Until = now.Add(30 * 24 * time.Hour)
			}
			if !payload.Until.After(now) {
				return domain.ErrModerationActionInvalid
			}
			if payload.AppealURL == "" {
				if e.appealLinks == nil || e.publicBaseURL == "" {
					return fmt.Errorf("moderation appeal link issuer is not configured")
				}
				linkExpiresAt := payload.Until
				maxLinkExpiry := now.Add(domain.MaxModerationAppealLinkLifetime)
				if linkExpiresAt.After(maxLinkExpiry) {
					linkExpiresAt = maxLinkExpiry
				}
				token, err := e.appealLinks.IssueAppealLink(
					ctx, detail.Case.ID, detail.Case.Target.ID,
					linkExpiresAt, now,
				)
				if err != nil {
					return err
				}
				payload.AppealURL = e.publicBaseURL + "/appeal/" + token
			}
		}
		_, err := e.admin.SetAccountFrozen(ctx, admin.SetAccountFrozenRequest{
			CommandMeta: meta, UserID: detail.Case.Target.ID, Frozen: frozen,
			Until: payload.Until, AppealURL: payload.AppealURL,
		})
		return err
	case domain.ModerationActionDeletePrivateMessage:
		if e.admin == nil || detail.Case.Target.Type != domain.PeerTypeUser {
			return domain.ErrModerationActionInvalid
		}
		var payload deletePrivateMessageActionPayload
		if err := decodeStrictActionPayload(action.Payload, &payload); err != nil {
			return err
		}
		if payload.OwnerUserID <= 0 || len(payload.IDs) == 0 ||
			len(payload.IDs) > domain.MaxDeleteMessageIDs {
			return domain.ErrModerationActionInvalid
		}
		_, err := e.admin.DeletePrivateMessages(ctx, admin.DeletePrivateMessagesRequest{
			CommandMeta: meta, OwnerUserID: payload.OwnerUserID,
			Peer: detail.Case.Target, IDs: payload.IDs, Revoke: payload.Revoke,
		})
		return err
	case domain.ModerationActionDeleteChannelMessage:
		if e.channels == nil || detail.Case.Target.Type != domain.PeerTypeChannel {
			return domain.ErrModerationActionInvalid
		}
		var payload deleteChannelMessageActionPayload
		if err := decodeStrictActionPayload(action.Payload, &payload); err != nil {
			return err
		}
		if len(payload.IDs) == 0 || len(payload.IDs) > domain.MaxDeleteMessageIDs {
			return domain.ErrModerationActionInvalid
		}
		result, err := e.channels.ModerationDeleteMessages(
			ctx, detail.Case.Target.ID, payload.IDs, int(e.now().Unix()),
		)
		if err != nil {
			return err
		}
		if e.channelNotifier != nil {
			e.channelNotifier.NotifyModerationChannelDeletion(ctx, result)
		}
		return nil
	case domain.ModerationActionDeleteAccount:
		if e.accounts == nil || detail.Case.Target.Type != domain.PeerTypeUser {
			return domain.ErrModerationActionInvalid
		}
		if err := decodeStrictActionPayload(action.Payload, &struct{}{}); err != nil {
			return err
		}
		_, err := e.accounts.ExecuteAccountDeletion(
			ctx, detail.Case.Target.ID, domain.AccountDeletionManual,
			fmt.Sprintf("moderation case %d", detail.Case.ID), e.now().UTC(),
		)
		return err
	default:
		return domain.ErrModerationActionInvalid
	}
}

func (s *Service) validateDecisionActions(ctx context.Context, detail domain.ModerationCaseDetail, actions []domain.ModerationActionDraft) error {
	if len(actions) == 0 {
		return nil
	}
	seen := make(map[domain.ModerationActionKind]struct{}, len(actions))
	flagActions := 0
	freezeActions := 0
	hasDeleteAccount := false
	for _, action := range actions {
		if _, duplicate := seen[action.Kind]; duplicate {
			return domain.ErrModerationActionInvalid
		}
		seen[action.Kind] = struct{}{}
		switch action.Kind {
		case domain.ModerationActionMarkScam, domain.ModerationActionMarkFake,
			domain.ModerationActionClearPeerFlags:
			flagActions++
			if err := decodeStrictActionPayload(action.Payload, &struct{}{}); err != nil {
				return err
			}
		case domain.ModerationActionFreezeAccount, domain.ModerationActionUnfreezeAccount:
			freezeActions++
			if detail.Case.Target.Type != domain.PeerTypeUser {
				return domain.ErrModerationActionInvalid
			}
			var payload freezeAccountActionPayload
			if err := decodeStrictActionPayload(action.Payload, &payload); err != nil {
				return err
			}
			if action.Kind == domain.ModerationActionUnfreezeAccount &&
				(!payload.Until.IsZero() || payload.AppealURL != "") {
				return domain.ErrModerationActionInvalid
			}
		case domain.ModerationActionDeletePrivateMessage:
			var payload deletePrivateMessageActionPayload
			if err := decodeStrictActionPayload(action.Payload, &payload); err != nil {
				return err
			}
			if detail.Case.Target.Type != domain.PeerTypeUser ||
				payload.OwnerUserID <= 0 ||
				!validModerationMessageIDs(payload.IDs) ||
				!s.privateDeletionCoveredByEvidence(ctx, detail, payload) {
				return domain.ErrModerationEvidenceNotFound
			}
		case domain.ModerationActionDeleteChannelMessage:
			var payload deleteChannelMessageActionPayload
			if err := decodeStrictActionPayload(action.Payload, &payload); err != nil {
				return err
			}
			if detail.Case.Target.Type != domain.PeerTypeChannel ||
				!validModerationMessageIDs(payload.IDs) ||
				!s.channelDeletionCoveredByEvidence(ctx, detail, payload.IDs) {
				return domain.ErrModerationEvidenceNotFound
			}
		case domain.ModerationActionDeleteAccount:
			hasDeleteAccount = true
			if detail.Case.Target.Type != domain.PeerTypeUser {
				return domain.ErrModerationActionInvalid
			}
			if err := decodeStrictActionPayload(action.Payload, &struct{}{}); err != nil {
				return err
			}
		default:
			return domain.ErrModerationActionInvalid
		}
	}
	if flagActions > 1 || freezeActions > 1 ||
		(hasDeleteAccount && len(actions) != 1) {
		return domain.ErrModerationActionInvalid
	}
	return nil
}

func (s *Service) privateDeletionCoveredByEvidence(ctx context.Context, detail domain.ModerationCaseDetail, payload deletePrivateMessageActionPayload) bool {
	needed := make(map[int]struct{}, len(payload.IDs))
	for _, id := range payload.IDs {
		needed[id] = struct{}{}
	}
	for _, reportID := range detail.ReportIDs {
		report, found, err := s.Report(ctx, reportID)
		if err != nil || !found || report.ReporterUserID != payload.OwnerUserID {
			continue
		}
		for _, item := range report.Items {
			if item.Kind == domain.ModerationItemMessage &&
				item.Peer == detail.Case.Target {
				delete(needed, int(item.ItemID))
			}
		}
	}
	return len(needed) == 0
}

func (s *Service) channelDeletionCoveredByEvidence(ctx context.Context, detail domain.ModerationCaseDetail, ids []int) bool {
	needed := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		needed[id] = struct{}{}
	}
	for _, reportID := range detail.ReportIDs {
		report, found, err := s.Report(ctx, reportID)
		if err != nil || !found {
			continue
		}
		for _, item := range report.Items {
			if item.Kind == domain.ModerationItemMessage &&
				item.Peer == detail.Case.Target {
				delete(needed, int(item.ItemID))
			}
		}
	}
	return len(needed) == 0
}

func validModerationMessageIDs(ids []int) bool {
	if len(ids) == 0 || len(ids) > domain.MaxDeleteMessageIDs {
		return false
	}
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 || id > domain.MaxMessageBoxID {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func (e *ActionExecutor) setPeerFlags(ctx context.Context, target domain.Peer, scam, fake bool, meta admin.CommandMeta) error {
	if e.admin == nil {
		return domain.ErrModerationActionInvalid
	}
	switch target.Type {
	case domain.PeerTypeUser:
		_, err := e.admin.SetUserFlags(ctx, admin.SetUserFlagsRequest{
			CommandMeta: meta, UserID: target.ID, Scam: scam, Fake: fake,
		})
		return err
	case domain.PeerTypeChannel:
		_, err := e.admin.SetChannelFlags(ctx, admin.SetChannelFlagsRequest{
			CommandMeta: meta, ChannelID: target.ID, Scam: scam, Fake: fake,
		})
		return err
	default:
		return domain.ErrModerationActionInvalid
	}
}

func decisionAuditContext(decisions []domain.ModerationDecision, decisionID int64) (string, string, bool) {
	for _, decision := range decisions {
		if decision.ID == decisionID {
			return decision.Actor, decision.Reason, true
		}
	}
	return "", "", false
}

func decodeStrictActionPayload(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domain.ErrModerationActionInvalid
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrModerationActionInvalid
	}
	return nil
}

type ActionWorker struct {
	store    store.ModerationCaseStore
	executor *ActionExecutor
	interval time.Duration
	lease    time.Duration
	batch    int
	log      *zap.Logger
}

func NewActionWorker(caseStore store.ModerationCaseStore, executor *ActionExecutor, log *zap.Logger) *ActionWorker {
	if log == nil {
		log = zap.NewNop()
	}
	return &ActionWorker{
		store: caseStore, executor: executor,
		interval: time.Second, lease: 30 * time.Second, batch: 20, log: log,
	}
}

func (w *ActionWorker) Run(ctx context.Context) {
	if w == nil || w.store == nil || w.executor == nil {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		if err := w.runOnce(ctx); err != nil && ctx.Err() == nil {
			w.log.Warn("审核处置任务执行失败", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *ActionWorker) runOnce(ctx context.Context) error {
	now := time.Now().UTC()
	actions, err := w.store.ClaimModerationActions(ctx, now, w.batch, w.lease)
	if err != nil {
		return err
	}
	for _, action := range actions {
		current, currentErr := w.store.IsModerationActionCurrent(ctx, action)
		if currentErr == nil && !current {
			if err := w.store.SupersedeModerationAction(
				ctx, action.ID, action.Attempts, time.Now().UTC(),
			); err != nil {
				w.log.Warn("提交已被新案件取代的审核处置失败",
					zap.Int64("case_id", action.CaseID),
					zap.Int64("action_id", action.ID),
					zap.String("kind", string(action.Kind)),
					zap.Error(err))
			} else {
				w.log.Info("审核处置已被同目标的更新处置取代",
					zap.Int64("case_id", action.CaseID),
					zap.Int64("action_id", action.ID),
					zap.String("kind", string(action.Kind)))
			}
			continue
		}
		detail, found, getErr := w.store.GetModerationCase(ctx, action.CaseID)
		execErr := currentErr
		if execErr == nil {
			execErr = getErr
		}
		if execErr == nil && !found {
			execErr = domain.ErrModerationCaseNotFound
		}
		if execErr == nil {
			execErr = w.executor.Execute(ctx, detail, action)
		}
		finishedAt := time.Now().UTC()
		retryAt := finishedAt
		errorText := ""
		if execErr != nil {
			errorText = execErr.Error()
			retryAt = finishedAt.Add(moderationActionRetryDelay(action.Attempts))
		}
		if err := w.store.CompleteModerationAction(
			ctx, action.ID, action.Attempts, execErr == nil,
			errorText, retryAt, finishedAt,
		); err != nil {
			w.log.Warn("提交审核处置结果失败",
				zap.Int64("action_id", action.ID),
				zap.Int("attempts", action.Attempts),
				zap.Error(err))
			continue
		}
		if execErr != nil {
			w.log.Warn("审核处置等待重试",
				zap.Int64("case_id", action.CaseID),
				zap.Int64("action_id", action.ID),
				zap.String("kind", string(action.Kind)),
				zap.Int("attempts", action.Attempts),
				zap.Error(execErr))
		} else {
			w.log.Info("审核处置完成",
				zap.Int64("case_id", action.CaseID),
				zap.Int64("action_id", action.ID),
				zap.String("kind", string(action.Kind)))
		}
	}
	return nil
}

func moderationActionRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second << min(attempt-1, 10)
	if delay > time.Hour {
		return time.Hour
	}
	return delay
}
