package moderation

import (
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestValidateAppealRemedyActionsMatchesOnlyAppliedReversibleState(t *testing.T) {
	detail := domain.ModerationCaseDetail{Actions: []domain.ModerationAction{
		{ID: 2, Kind: domain.ModerationActionFreezeAccount, Status: domain.ModerationActionSucceeded},
		{ID: 1, Kind: domain.ModerationActionMarkScam, Status: domain.ModerationActionSucceeded},
		{ID: 3, Kind: domain.ModerationActionDeletePrivateMessage, Status: domain.ModerationActionFailed},
	}}
	remedies := []domain.ModerationActionDraft{
		{Kind: domain.ModerationActionClearPeerFlags},
		{Kind: domain.ModerationActionUnfreezeAccount},
	}
	if err := validateAppealRemedyActions(detail, remedies); err != nil {
		t.Fatalf("valid remedies err=%v", err)
	}
	if err := validateAppealRemedyActions(
		detail, remedies[:1],
	); !errors.Is(err, domain.ErrModerationActionInvalid) {
		t.Fatalf("missing unfreeze err=%v", err)
	}
	if err := validateAppealRemedyActions(detail, []domain.ModerationActionDraft{
		{Kind: domain.ModerationActionMarkFake},
		{Kind: domain.ModerationActionUnfreezeAccount},
	}); !errors.Is(err, domain.ErrModerationActionInvalid) {
		t.Fatalf("new punishment in appeal err=%v", err)
	}
	detail.Actions = append(detail.Actions, domain.ModerationAction{
		ID: 4, Kind: domain.ModerationActionDeletePrivateMessage,
		Status: domain.ModerationActionSucceeded,
	})
	if err := validateAppealRemedyActions(
		detail, remedies,
	); !errors.Is(err, domain.ErrModerationActionInvalid) {
		t.Fatalf("irreversible grant err=%v", err)
	}
}
