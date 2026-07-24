package moderation

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAppealLinkSubmissionIsHashOnlyIdempotentAndExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	store := memory.NewModerationReportStore()
	service := NewService(store)
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
	claimed, err := service.ClaimCase(
		ctx, cases[0].ID, cases[0].Version, "reviewer", now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	detail, _, err := service.DecideCase(ctx, domain.ModerationDecisionRequest{
		CaseID: claimed.ID, ExpectedVersion: claimed.Version,
		Actor: "reviewer", Reason: "confirmed", CommandID: "appeal-link-decision",
		Kind: domain.ModerationDecisionViolation,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionMarkFake, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := service.IssueAppealLink(
		ctx, claimed.ID, target.ID, now.Add(24*time.Hour), now.Add(3*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatalf("token length=%d", len(token))
	}
	link, found, err := service.ResolveAppealLink(ctx, token, now.Add(4*time.Second))
	if err != nil || !found || link.TokenHash == ([32]byte{}) {
		t.Fatalf("link=%+v found=%v err=%v", link, found, err)
	}
	expiredToken, err := service.IssueAppealLink(
		ctx, claimed.ID, target.ID, now.Add(10*time.Second), now.Add(4*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < domain.MaxModerationAppealLinksPerCase-2; i++ {
		issuedAt := now.Add(time.Duration(20+i) * time.Second)
		if _, err := service.IssueAppealLink(
			ctx, claimed.ID, target.ID, now.Add(time.Hour), issuedAt,
		); err != nil {
			t.Fatalf("issue bounded link %d: %v", i, err)
		}
	}
	if _, err := service.IssueAppealLink(
		ctx, claimed.ID, target.ID, now.Add(time.Hour), now.Add(time.Minute),
	); !errors.Is(err, domain.ErrModerationActionConflict) {
		t.Fatalf("appeal link overflow err=%v", err)
	}
	if actions, err := store.ClaimModerationActions(
		ctx, now.Add(5*time.Second), 10, time.Minute,
	); err != nil || len(actions) != 1 {
		t.Fatalf("actions=%+v err=%v", actions, err)
	} else if err := store.CompleteModerationAction(
		ctx, actions[0].ID, actions[0].Attempts, true, "",
		time.Time{}, now.Add(6*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	appeal, created, err := service.SubmitAppealLink(
		ctx, token, "The account was impersonated.", now.Add(7*time.Second),
	)
	if err != nil || !created || appeal.CaseID != claimed.ID ||
		appeal.AppellantUserID != target.ID {
		t.Fatalf("appeal=%+v created=%v err=%v", appeal, created, err)
	}
	retry, created, err := service.SubmitAppealLink(
		ctx, token, "A different retry body must not create another appeal.",
		now.Add(8*time.Second),
	)
	if err != nil || created || retry.ID != appeal.ID || retry.Text != appeal.Text {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	appealed, found, err := service.Case(ctx, detail.Case.ID)
	if err != nil || !found ||
		appealed.Case.Status != domain.ModerationCaseAppealReview ||
		len(appealed.Appeals) != 1 {
		t.Fatalf("appealed=%+v found=%v err=%v", appealed, found, err)
	}
	appealClaim, err := service.ClaimCase(
		ctx, appealed.Case.ID, appealed.Case.Version,
		"appeal-reviewer", now.Add(8*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	grant := domain.ModerationDecisionRequest{
		CaseID: appealClaim.ID, AppealID: appeal.ID,
		ExpectedVersion: appealClaim.Version, Actor: "appeal-reviewer",
		Reason:    "original evidence was insufficient",
		CommandID: "appeal-grant-without-remedy",
		Kind:      domain.ModerationDecisionAppealGrant,
		CreatedAt: now.Add(9 * time.Second),
	}
	if _, _, err := service.ReviewAppeal(ctx, grant); !errors.Is(
		err, domain.ErrModerationActionInvalid,
	) {
		t.Fatalf("grant without required flag remedy err=%v", err)
	}
	grant.CommandID = "appeal-grant-with-remedy"
	grant.Actions = []domain.ModerationActionDraft{{
		Kind: domain.ModerationActionClearPeerFlags, Payload: []byte(`{}`),
	}}
	granted, created, err := service.ReviewAppeal(ctx, grant)
	if err != nil || !created ||
		granted.Case.Status != domain.ModerationCaseActionPending {
		t.Fatalf("granted=%+v created=%v err=%v", granted, created, err)
	}
	remedies, err := store.ClaimModerationActions(
		ctx, now.Add(10*time.Second), 10, time.Minute,
	)
	if err != nil || len(remedies) != 1 ||
		remedies[0].Kind != domain.ModerationActionClearPeerFlags {
		t.Fatalf("remedies=%+v err=%v", remedies, err)
	}
	if err := store.CompleteModerationAction(
		ctx, remedies[0].ID, remedies[0].Attempts, true, "",
		time.Time{}, now.Add(11*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	dismissed, found, err := service.Case(ctx, appealed.Case.ID)
	if err != nil || !found ||
		dismissed.Case.Status != domain.ModerationCaseDismissed {
		t.Fatalf("dismissed=%+v found=%v err=%v", dismissed, found, err)
	}

	if _, found, err := service.ResolveAppealLink(
		ctx, expiredToken, now.Add(10*time.Second),
	); err != nil || found {
		t.Fatalf("expired resolve found=%v err=%v", found, err)
	}
	if _, _, err := service.SubmitAppealLink(
		ctx, expiredToken, "too late", now.Add(10*time.Second),
	); !errors.Is(err, domain.ErrModerationAppealLinkInvalid) {
		t.Fatalf("expired submit err=%v", err)
	}
	if _, _, err := service.ResolveAppealLink(ctx, "not-a-token", now); !errors.Is(
		err, domain.ErrModerationAppealLinkInvalid,
	) {
		t.Fatalf("invalid token err=%v", err)
	}
}
