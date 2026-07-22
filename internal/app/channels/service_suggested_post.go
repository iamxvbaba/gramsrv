package channels

import (
	"context"

	"telesrv/internal/domain"
)

type suggestedPostStore interface {
	ToggleSuggestedPostApproval(context.Context, domain.ToggleSuggestedPostApprovalRequest) (domain.ToggleSuggestedPostApprovalResult, error)
	ProcessSuggestedPostLifecycle(context.Context, domain.SuggestedPostLifecycleRequest) ([]domain.ToggleSuggestedPostApprovalResult, error)
}

func (s *Service) ToggleSuggestedPostApproval(ctx context.Context, req domain.ToggleSuggestedPostApprovalRequest) (domain.ToggleSuggestedPostApprovalResult, error) {
	if s == nil || s.channels == nil || req.UserID == 0 || req.MonoforumID == 0 || req.MessageID <= 0 {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	store, ok := s.channels.(suggestedPostStore)
	if !ok {
		return domain.ToggleSuggestedPostApprovalResult{}, domain.ErrSuggestedPostInvalid
	}
	return store.ToggleSuggestedPostApproval(ctx, req)
}

func (s *Service) ProcessSuggestedPostLifecycle(ctx context.Context, req domain.SuggestedPostLifecycleRequest) ([]domain.ToggleSuggestedPostApprovalResult, error) {
	if s == nil || s.channels == nil {
		return nil, domain.ErrSuggestedPostInvalid
	}
	store, ok := s.channels.(suggestedPostStore)
	if !ok {
		return nil, domain.ErrSuggestedPostInvalid
	}
	return store.ProcessSuggestedPostLifecycle(ctx, req)
}
