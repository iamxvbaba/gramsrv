package moderation

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"

	"telesrv/internal/admin"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type captureModerationAdmin struct {
	userFlags []admin.SetUserFlagsRequest
	frozen    []admin.SetAccountFrozenRequest
}

func (a *captureModerationAdmin) SetAccountFrozen(_ context.Context, req admin.SetAccountFrozenRequest) (admin.CommandResult, error) {
	a.frozen = append(a.frozen, req)
	return admin.CommandResult{}, nil
}

func (a *captureModerationAdmin) SetUserFlags(_ context.Context, req admin.SetUserFlagsRequest) (admin.CommandResult, error) {
	a.userFlags = append(a.userFlags, req)
	return admin.CommandResult{}, nil
}

func (*captureModerationAdmin) SetChannelFlags(context.Context, admin.SetChannelFlagsRequest) (admin.CommandResult, error) {
	return admin.CommandResult{}, nil
}

func (*captureModerationAdmin) DeletePrivateMessages(context.Context, admin.DeletePrivateMessagesRequest) (admin.CommandResult, error) {
	return admin.CommandResult{}, nil
}

func TestActionWorkerAppliesFakeFlagAndResolvesCase(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Add(-10 * time.Second)
	reports := memory.NewModerationReportStore()
	service := NewService(reports)
	target := domain.Peer{Type: domain.PeerTypeUser, ID: 202}
	if _, _, err := service.AcceptReport(ctx, domain.ModerationReportDraft{
		ReporterUserID: 101, Source: domain.ModerationSourceAccountPeer,
		Target: target, Reason: domain.ModerationReasonFake, Option: "fake",
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemPeer, Peer: target, ItemID: target.ID,
			AuthorUserID: target.ID, EvidenceSchemaVersion: 1,
			Evidence: []byte(`{"schema_version":1}`),
		}},
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	cases, err := service.ListCases(ctx, domain.ModerationCaseFilter{Limit: 10})
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	claimed, err := service.ClaimCase(ctx, cases[0].ID, cases[0].Version, "reviewer", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	decision, created, err := service.DecideCase(ctx, domain.ModerationDecisionRequest{
		CaseID: claimed.ID, ExpectedVersion: claimed.Version,
		Actor: "reviewer", Reason: "impersonation confirmed",
		CommandID: "mod-fake-1", Kind: domain.ModerationDecisionViolation,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionMarkFake, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(2 * time.Second),
	})
	if err != nil || !created || decision.Case.Status != domain.ModerationCaseActionPending {
		t.Fatalf("decision=%+v created=%v err=%v", decision, created, err)
	}
	adminActions := &captureModerationAdmin{}
	worker := NewActionWorker(
		reports,
		NewActionExecutor(adminActions, nil, nil, nil),
		zap.NewNop(),
	)
	if err := worker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(adminActions.userFlags) != 1 {
		t.Fatalf("flag actions=%d, want 1", len(adminActions.userFlags))
	}
	flag := adminActions.userFlags[0]
	if flag.UserID != target.ID || flag.Scam || !flag.Fake ||
		flag.CommandID != "mod-fake-1:000" || flag.Actor != "reviewer" {
		t.Fatalf("flag request=%+v", flag)
	}
	resolved, found, err := service.Case(ctx, claimed.ID)
	if err != nil || !found || resolved.Case.Status != domain.ModerationCaseResolved ||
		resolved.Actions[0].Status != domain.ModerationActionSucceeded {
		t.Fatalf("resolved=%+v found=%v err=%v", resolved, found, err)
	}
}

func TestActionWorkerSupersedesOlderTargetSanction(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	reports := memory.NewModerationReportStore()
	service := NewService(reports)
	target := domain.Peer{Type: domain.PeerTypeUser, ID: 202}
	createDecision := func(reporter int64, command string, kind domain.ModerationActionKind, at time.Time) int64 {
		t.Helper()
		if _, _, err := service.AcceptReport(ctx, domain.ModerationReportDraft{
			ReporterUserID: reporter, Source: domain.ModerationSourceAccountPeer,
			Target: target, Reason: domain.ModerationReasonFake, Option: command,
			Items: []domain.ModerationReportItem{{
				Kind: domain.ModerationItemPeer, Peer: target, ItemID: target.ID,
				AuthorUserID: target.ID, EvidenceSchemaVersion: 1,
				Evidence: []byte(`{"schema_version":1}`),
			}},
			CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		cases, err := service.ListCases(ctx, domain.ModerationCaseFilter{
			Statuses: []domain.ModerationCaseStatus{domain.ModerationCaseOpen},
			Target:   target, Limit: 10,
		})
		if err != nil || len(cases) != 1 {
			t.Fatalf("open cases=%+v err=%v", cases, err)
		}
		claimed, err := service.ClaimCase(ctx, cases[0].ID, cases[0].Version, "reviewer", at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.DecideCase(ctx, domain.ModerationDecisionRequest{
			CaseID: claimed.ID, ExpectedVersion: claimed.Version,
			Actor: "reviewer", Reason: "confirmed", CommandID: command,
			Kind:      domain.ModerationDecisionViolation,
			Actions:   []domain.ModerationActionDraft{{Kind: kind, Payload: []byte(`{}`)}},
			CreatedAt: at.Add(2 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		return claimed.ID
	}
	oldCaseID := createDecision(101, "old-scam", domain.ModerationActionMarkScam, now)
	newCaseID := createDecision(102, "new-fake", domain.ModerationActionMarkFake, now.Add(3*time.Second))

	adminActions := &captureModerationAdmin{}
	worker := NewActionWorker(
		reports,
		NewActionExecutor(adminActions, nil, nil, nil),
		zap.NewNop(),
	)
	if err := worker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(adminActions.userFlags) != 1 || adminActions.userFlags[0].Scam ||
		!adminActions.userFlags[0].Fake {
		t.Fatalf("flag actions=%+v", adminActions.userFlags)
	}
	oldDetail, _, err := service.Case(ctx, oldCaseID)
	if err != nil || oldDetail.Case.Status != domain.ModerationCaseResolved ||
		len(oldDetail.Actions) != 1 ||
		oldDetail.Actions[0].Status != domain.ModerationActionSuperseded {
		t.Fatalf("old detail=%+v err=%v", oldDetail, err)
	}
	newDetail, _, err := service.Case(ctx, newCaseID)
	if err != nil || newDetail.Case.Status != domain.ModerationCaseResolved ||
		len(newDetail.Actions) != 1 ||
		newDetail.Actions[0].Status != domain.ModerationActionSucceeded {
		t.Fatalf("new detail=%+v err=%v", newDetail, err)
	}
}

func TestAppealCannotClearSanctionOwnedByNewerCase(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	reports := memory.NewModerationReportStore()
	service := NewService(reports)
	target := domain.Peer{Type: domain.PeerTypeUser, ID: 202}
	createCase := func(reporter int64, option, command string, at time.Time) domain.ModerationCase {
		t.Helper()
		if _, _, err := service.AcceptReport(ctx, domain.ModerationReportDraft{
			ReporterUserID: reporter, Source: domain.ModerationSourceAccountPeer,
			Target: target, Reason: domain.ModerationReasonFake, Option: option,
			Items: []domain.ModerationReportItem{{
				Kind: domain.ModerationItemPeer, Peer: target, ItemID: target.ID,
				AuthorUserID: target.ID, EvidenceSchemaVersion: 1,
				Evidence: []byte(`{"schema_version":1}`),
			}},
			CreatedAt: at,
		}); err != nil {
			t.Fatal(err)
		}
		items, err := service.ListCases(ctx, domain.ModerationCaseFilter{
			Statuses: []domain.ModerationCaseStatus{domain.ModerationCaseOpen},
			Target:   target, Limit: 10,
		})
		if err != nil || len(items) != 1 {
			t.Fatalf("open cases=%+v err=%v", items, err)
		}
		claimed, err := service.ClaimCase(ctx, items[0].ID, items[0].Version, "reviewer", at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := service.DecideCase(ctx, domain.ModerationDecisionRequest{
			CaseID: claimed.ID, ExpectedVersion: claimed.Version,
			Actor: "reviewer", Reason: "confirmed", CommandID: command,
			Kind: domain.ModerationDecisionViolation,
			Actions: []domain.ModerationActionDraft{{
				Kind: domain.ModerationActionMarkFake, Payload: []byte(`{}`),
			}},
			CreatedAt: at.Add(2 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		detail, _, err := service.Case(ctx, claimed.ID)
		if err != nil {
			t.Fatal(err)
		}
		return detail.Case
	}
	oldCase := createCase(101, "old", "old", now)
	adminActions := &captureModerationAdmin{}
	worker := NewActionWorker(reports, NewActionExecutor(adminActions, nil, nil, nil), zap.NewNop())
	if err := worker.runOnce(ctx); err != nil {
		t.Fatal(err)
	}
	appeal, _, err := service.SubmitAppeal(ctx, oldCase.ID, target.ID, "mistake", now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	newCase := createCase(102, "new", "new", now.Add(4*time.Second))
	if newCase.ID == oldCase.ID {
		t.Fatal("new report reused decided case")
	}
	oldDetail, _, err := service.Case(ctx, oldCase.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := service.ClaimCase(ctx, oldCase.ID, oldDetail.Case.Version, "reviewer", now.Add(8*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.ReviewAppeal(ctx, domain.ModerationDecisionRequest{
		CaseID: oldCase.ID, AppealID: appeal.ID,
		ExpectedVersion: claimed.Version, Actor: "reviewer",
		Reason: "grant", CommandID: "stale-appeal",
		Kind: domain.ModerationDecisionAppealGrant,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionClearPeerFlags, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(9 * time.Second),
	})
	if !errors.Is(err, domain.ErrModerationActionConflict) {
		t.Fatalf("ReviewAppeal error=%v", err)
	}
}

type captureAppealLinkIssuer struct {
	caseID        int64
	appellantID   int64
	expiresAt     time.Time
	issuedAt      time.Time
	returnedToken string
}

func (i *captureAppealLinkIssuer) IssueAppealLink(_ context.Context, caseID, appellantUserID int64, expiresAt, now time.Time) (string, error) {
	i.caseID = caseID
	i.appellantID = appellantUserID
	i.expiresAt = expiresAt
	i.issuedAt = now
	return i.returnedToken, nil
}

func TestActionExecutorFreezeDefaultsAndBoundsAppealLink(t *testing.T) {
	now := time.Unix(1_750_000_000, 0).UTC()
	adminActions := &captureModerationAdmin{}
	issuer := &captureAppealLinkIssuer{returnedToken: "token"}
	executor := NewActionExecutor(
		adminActions, nil, nil, nil,
		WithActionClock(func() time.Time { return now }),
		WithAppealLinks(issuer, "https://example.test/"),
	)
	detail := domain.ModerationCaseDetail{
		Case: domain.ModerationCase{
			ID: 10, Target: domain.Peer{Type: domain.PeerTypeUser, ID: 20},
		},
		Decisions: []domain.ModerationDecision{{
			ID: 30, Actor: "reviewer",
		}},
	}
	action := domain.ModerationAction{
		CaseID: 10, DecisionID: 30,
		Kind:    domain.ModerationActionFreezeAccount,
		Payload: []byte(`{}`), CommandID: "freeze:000",
	}
	if err := executor.Execute(context.Background(), detail, action); err != nil {
		t.Fatal(err)
	}
	if len(adminActions.frozen) != 1 {
		t.Fatalf("freeze calls=%d", len(adminActions.frozen))
	}
	req := adminActions.frozen[0]
	wantUntil := now.Add(30 * 24 * time.Hour)
	if !req.Frozen || req.UserID != 20 || !req.Until.Equal(wantUntil) ||
		req.AppealURL != "https://example.test/appeal/token" {
		t.Fatalf("freeze request=%+v", req)
	}
	if issuer.caseID != 10 || issuer.appellantID != 20 ||
		!issuer.expiresAt.Equal(wantUntil) || !issuer.issuedAt.Equal(now) {
		t.Fatalf("appeal issue=%+v", issuer)
	}

	adminActions.frozen = nil
	longUntil := now.Add(365 * 24 * time.Hour)
	action.Payload = []byte(`{"until":"` + longUntil.Format(time.RFC3339Nano) + `"}`)
	if err := executor.Execute(context.Background(), detail, action); err != nil {
		t.Fatal(err)
	}
	if !adminActions.frozen[0].Until.Equal(longUntil) {
		t.Fatalf("long freeze until=%v", adminActions.frozen[0].Until)
	}
	if want := now.Add(domain.MaxModerationAppealLinkLifetime); !issuer.expiresAt.Equal(want) {
		t.Fatalf("link expiry=%v want=%v", issuer.expiresAt, want)
	}
}
