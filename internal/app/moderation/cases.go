package moderation

import (
	"context"
	"fmt"
	"sort"
	"time"

	"telesrv/internal/domain"
)

func (s *Service) ListCases(ctx context.Context, filter domain.ModerationCaseFilter) ([]domain.ModerationCase, error) {
	if s == nil || s.cases == nil {
		return nil, fmt.Errorf("moderation case store is not configured")
	}
	return s.cases.ListModerationCases(ctx, filter)
}

func (s *Service) Case(ctx context.Context, caseID int64) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store is not configured")
	}
	return s.cases.GetModerationCase(ctx, caseID)
}

func (s *Service) ClaimCase(ctx context.Context, caseID, expectedVersion int64, actor string, now time.Time) (domain.ModerationCase, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationCase{}, fmt.Errorf("moderation case store is not configured")
	}
	return s.cases.ClaimModerationCase(ctx, caseID, expectedVersion, actor, now)
}

func (s *Service) DecideCase(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store is not configured")
	}
	prepared, err := domain.NewModerationDecisionRequest(request)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	detail, found, err := s.cases.GetModerationCase(ctx, prepared.CaseID)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if !found {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	if err := s.validateDecisionActions(ctx, detail, prepared.Actions); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	return s.cases.DecideModerationCase(ctx, prepared)
}

func (s *Service) SubmitAppeal(ctx context.Context, caseID, appellantUserID int64, text string, now time.Time) (domain.ModerationAppeal, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationAppeal{}, false, fmt.Errorf("moderation case store is not configured")
	}
	detail, found, err := s.cases.GetModerationCase(ctx, caseID)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	if !found {
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseNotFound
	}
	switch detail.Case.Target.Type {
	case domain.PeerTypeUser:
		if detail.Case.Target.ID != appellantUserID {
			return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
		}
	case domain.PeerTypeChannel:
		if s.channels == nil {
			return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
		}
		view, err := s.channels.ResolveChannel(ctx, appellantUserID, detail.Case.Target.ID)
		if err != nil || view.Forbidden ||
			(view.Self.Role != domain.ChannelRoleCreator &&
				view.Self.Role != domain.ChannelRoleAdmin) {
			return domain.ModerationAppeal{}, false, domain.ErrModerationPermissionDenied
		}
	default:
		return domain.ModerationAppeal{}, false, domain.ErrModerationCaseInvalid
	}
	appeal, err := domain.NewModerationAppeal(
		caseID, appellantUserID, detail.Case.Status, text, now,
	)
	if err != nil {
		return domain.ModerationAppeal{}, false, err
	}
	return s.cases.CreateModerationAppeal(ctx, appeal)
}

func (s *Service) ReviewAppeal(ctx context.Context, request domain.ModerationDecisionRequest) (domain.ModerationCaseDetail, bool, error) {
	if s == nil || s.cases == nil {
		return domain.ModerationCaseDetail{}, false, fmt.Errorf("moderation case store is not configured")
	}
	prepared, err := domain.NewModerationDecisionRequest(request)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if prepared.AppealID <= 0 ||
		(prepared.Kind != domain.ModerationDecisionAppealGrant &&
			prepared.Kind != domain.ModerationDecisionAppealDeny) {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseInvalid
	}
	detail, found, err := s.cases.GetModerationCase(ctx, prepared.CaseID)
	if err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if !found {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	appealFound := false
	for _, appeal := range detail.Appeals {
		if appeal.ID == prepared.AppealID &&
			appeal.Status == domain.ModerationAppealPending {
			appealFound = true
			break
		}
	}
	if !appealFound {
		return domain.ModerationCaseDetail{}, false, domain.ErrModerationCaseNotFound
	}
	if err := s.validateDecisionActions(ctx, detail, prepared.Actions); err != nil {
		return domain.ModerationCaseDetail{}, false, err
	}
	if prepared.Kind == domain.ModerationDecisionAppealGrant {
		if err := validateAppealRemedyActions(detail, prepared.Actions); err != nil {
			return domain.ModerationCaseDetail{}, false, err
		}
	}
	return s.cases.ReviewModerationAppeal(ctx, prepared)
}

func validateAppealRemedyActions(detail domain.ModerationCaseDetail, actions []domain.ModerationActionDraft) error {
	history := append([]domain.ModerationAction(nil), detail.Actions...)
	sort.Slice(history, func(i, j int) bool { return history[i].ID < history[j].ID })
	var flagsActive, freezeActive, irreversible bool
	for _, action := range history {
		if action.Status != domain.ModerationActionSucceeded {
			continue
		}
		switch action.Kind {
		case domain.ModerationActionMarkScam, domain.ModerationActionMarkFake:
			flagsActive = true
		case domain.ModerationActionClearPeerFlags:
			flagsActive = false
		case domain.ModerationActionFreezeAccount:
			freezeActive = true
		case domain.ModerationActionUnfreezeAccount:
			freezeActive = false
		case domain.ModerationActionDeletePrivateMessage,
			domain.ModerationActionDeleteChannelMessage,
			domain.ModerationActionDeleteAccount:
			irreversible = true
		}
	}
	if irreversible {
		return domain.ErrModerationActionInvalid
	}
	expected := make(map[domain.ModerationActionKind]bool, 2)
	if flagsActive {
		expected[domain.ModerationActionClearPeerFlags] = true
	}
	if freezeActive {
		expected[domain.ModerationActionUnfreezeAccount] = true
	}
	if len(actions) != len(expected) {
		return domain.ErrModerationActionInvalid
	}
	for _, action := range actions {
		if !expected[action.Kind] {
			return domain.ErrModerationActionInvalid
		}
		delete(expected, action.Kind)
	}
	if len(expected) != 0 {
		return domain.ErrModerationActionInvalid
	}
	return nil
}
