package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	moderationapp "telesrv/internal/app/moderation"
	"telesrv/internal/domain"
)

func TestModerationReportStoreAtomicEvidenceAndIdempotency(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reporter := now.UnixNano()&0x3fffffff + 5_000
	report, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: reporter, Source: domain.ModerationSourceProfilePhoto,
		Target: domain.Peer{Type: domain.PeerTypeUser, ID: reporter + 1},
		Reason: domain.ModerationReasonFake, Option: "v1/fake",
		Items: []domain.ModerationReportItem{{
			Kind:   domain.ModerationItemProfilePhoto,
			Peer:   domain.Peer{Type: domain.PeerTypeUser, ID: reporter + 1},
			ItemID: reporter + 2, AuthorUserID: reporter + 1,
			EvidenceSchemaVersion: 1,
			Evidence:              []byte(`{"photo_id":2,"owner_id":1}`),
		}},
		MediaHolds: []domain.ModerationMediaHold{{
			ItemIndex: 0, Kind: domain.ModerationMediaPhoto,
			StorageKey: "profile/photo/test",
		}},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewModerationReportStore(pool)
	stored, created, err := store.CreateModerationReport(ctx, report)
	if err != nil || !created {
		t.Fatalf("create=%v err=%v", created, err)
	}
	t.Cleanup(func() {
		cleanupModerationReport(t, pool, stored.ID)
	})
	retry, created, err := store.CreateModerationReport(ctx, report)
	if err != nil || created || retry.ID != stored.ID {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	got, found, err := store.GetModerationReport(ctx, stored.ID)
	if err != nil || !found {
		t.Fatalf("get found=%v err=%v", found, err)
	}
	if got.Fingerprint != report.Fingerprint || len(got.Items) != 1 ||
		len(got.MediaHolds) != 1 || got.MediaHolds[0].StorageKey != "profile/photo/test" {
		t.Fatalf("stored report = %+v", got)
	}
	var reports, items, holds int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM moderation_reports WHERE id = $1),
  (SELECT count(*) FROM moderation_report_items WHERE report_id = $1),
  (SELECT count(*) FROM moderation_media_holds WHERE report_id = $1)`,
		stored.ID).Scan(&reports, &items, &holds); err != nil {
		t.Fatal(err)
	}
	if reports != 1 || items != 1 || holds != 1 {
		t.Fatalf("rows reports=%d items=%d holds=%d", reports, items, holds)
	}
}

func TestModerationSponsoredReportIsAtomicUnderConcurrentFinalOptions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	userID := now.UnixNano()&0x3fffffff + 8_000
	randomID := []byte("postgres-sponsored-random-id")
	store := NewModerationReportStore(pool)
	impression, err := domain.NewSponsoredMessageImpression(
		userID, randomID,
		domain.Peer{Type: domain.PeerTypeChannel, ID: userID + 1},
		userID+2, []byte(`{"creative_id":"pg-creative","schema_version":1}`),
		now, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	impression, created, err := store.CreateSponsoredMessageImpression(ctx, impression)
	if err != nil || !created {
		t.Fatalf("impression=%+v created=%v err=%v", impression, created, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM sponsored_message_impressions WHERE id = $1", impression.ID)
	})
	service := moderationapp.NewService(store)
	type result struct {
		report  domain.ModerationReport
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, option := range []struct {
		reason domain.ModerationReason
		option string
	}{
		{domain.ModerationReasonSpam, "spam"},
		{domain.ModerationReasonFake, "fake"},
	} {
		wg.Add(1)
		go func(reason domain.ModerationReason, option string) {
			defer wg.Done()
			<-start
			report, created, err := service.ReportSponsored(
				ctx, userID, randomID, reason, option, now.Add(time.Second),
			)
			results <- result{report: report, created: created, err: err}
		}(option.reason, option.option)
	}
	close(start)
	wg.Wait()
	close(results)
	var reportID int64
	var createdCount int
	for got := range results {
		if got.err != nil || got.report.ID <= 0 {
			t.Fatalf("concurrent result=%+v", got)
		}
		if reportID == 0 {
			reportID = got.report.ID
		} else if got.report.ID != reportID {
			t.Fatalf("report ids differ: %d vs %d", reportID, got.report.ID)
		}
		if got.created {
			createdCount++
		}
	}
	if createdCount != 1 {
		t.Fatalf("created count=%d, want 1", createdCount)
	}
	t.Cleanup(func() { cleanupModerationReport(t, pool, reportID) })
	var reportCount int
	if err := pool.QueryRow(ctx, `
SELECT count(*)
FROM moderation_reports
WHERE reporter_user_id = $1 AND source = 'sponsored'`,
		userID,
	).Scan(&reportCount); err != nil {
		t.Fatal(err)
	}
	if reportCount != 1 {
		t.Fatalf("sponsored reports=%d, want 1", reportCount)
	}
}

func TestModerationCaseActionAppealLinkAndTelemetryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	reporter := now.UnixNano()&0x3fffffff + 12_000
	target := domain.Peer{Type: domain.PeerTypeUser, ID: reporter + 1}
	store := NewModerationReportStore(pool)
	service := moderationapp.NewService(store)
	report, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: reporter, Source: domain.ModerationSourceAccountPeer,
		Target: target, Reason: domain.ModerationReasonFake, Option: "fake",
		Items: []domain.ModerationReportItem{{
			Kind: domain.ModerationItemPeer, Peer: target, ItemID: target.ID,
			AuthorUserID: target.ID, EvidenceSchemaVersion: 1,
			Evidence: []byte(`{"schema_version":1}`),
		}},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, created, err := store.CreateModerationReport(ctx, report)
	if err != nil || !created {
		t.Fatalf("report=%+v created=%v err=%v", stored, created, err)
	}
	t.Cleanup(func() { cleanupModerationReport(t, pool, stored.ID) })
	cases, err := store.ListModerationCases(ctx, domain.ModerationCaseFilter{
		Target: target, Limit: 10,
	})
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	claimed, err := store.ClaimModerationCase(
		ctx, cases[0].ID, cases[0].Version, "pg-reviewer", now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := domain.NewModerationDecisionRequest(domain.ModerationDecisionRequest{
		CaseID: claimed.ID, ExpectedVersion: claimed.Version,
		Actor: "pg-reviewer", Reason: "confirmed fake",
		CommandID: "pg-moderation-decision-" + time.Unix(0, reporter).Format("150405.000000000"),
		Kind:      domain.ModerationDecisionViolation,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionMarkFake, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.DecideModerationCase(ctx, decision); err != nil || !created {
		t.Fatalf("decision created=%v err=%v", created, err)
	}
	actions, err := store.ClaimModerationActions(
		ctx, now.Add(3*time.Second), 10, time.Minute,
	)
	if err != nil || len(actions) != 1 {
		t.Fatalf("actions=%+v err=%v", actions, err)
	}
	if err := store.CompleteModerationAction(
		ctx, actions[0].ID, actions[0].Attempts, true, "",
		time.Time{}, now.Add(4*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	token, err := service.IssueAppealLink(
		ctx, cases[0].ID, target.ID, now.Add(time.Hour), now.Add(5*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	appeal, created, err := service.SubmitAppealLink(
		ctx, token, "Postgres appeal.", now.Add(6*time.Second),
	)
	if err != nil || !created || appeal.ID <= 0 {
		t.Fatalf("appeal=%+v created=%v err=%v", appeal, created, err)
	}
	retry, created, err := service.SubmitAppealLink(
		ctx, token, "retry body", now.Add(7*time.Second),
	)
	if err != nil || created || retry.ID != appeal.ID ||
		retry.Text != appeal.Text {
		t.Fatalf("appeal retry=%+v created=%v err=%v", retry, created, err)
	}

	telemetryStore := NewClientTelemetryStore(pool)
	telemetryAt := time.Unix(reporter%1_000_000+1, 0).UTC()
	event, err := domain.NewClientTelemetryEvent(
		reporter, domain.ClientTelemetryMessageDelivery, target,
		[]int64{3, 1, 2}, map[string]any{"push": true}, telemetryAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	telemetry, created, err := telemetryStore.CreateClientTelemetry(ctx, event)
	if err != nil || !created || telemetry.ID <= 0 {
		t.Fatalf("telemetry=%+v created=%v err=%v", telemetry, created, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM client_telemetry_events WHERE id = $1", telemetry.ID)
	})
	retryTelemetry, created, err := telemetryStore.CreateClientTelemetry(ctx, event)
	if err != nil || created || retryTelemetry.ID != telemetry.ID {
		t.Fatalf("telemetry retry=%+v created=%v err=%v", retryTelemetry, created, err)
	}
	deleted, err := telemetryStore.DeleteExpiredClientTelemetry(
		ctx, telemetryAt.Add(time.Second), 10,
	)
	if err != nil || deleted < 1 {
		t.Fatalf("telemetry retention deleted=%d err=%v", deleted, err)
	}
}

func TestModerationSanctionSupersessionAndAppealOwnershipPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := time.Now().UTC()
	store := NewModerationReportStore(pool)
	service := moderationapp.NewService(store)
	base := now.UnixNano()&0x3fffffff + 40_000

	createDecision := func(target domain.Peer, reporter int64, option, command string, at time.Time) (int64, int64) {
		t.Helper()
		report, _, err := service.AcceptReport(ctx, domain.ModerationReportDraft{
			ReporterUserID: reporter, Source: domain.ModerationSourceAccountPeer,
			Target: target, Reason: domain.ModerationReasonFake, Option: option,
			Items: []domain.ModerationReportItem{{
				Kind: domain.ModerationItemPeer, Peer: target, ItemID: target.ID,
				AuthorUserID: target.ID, EvidenceSchemaVersion: 1,
				Evidence: []byte(`{"schema_version":1}`),
			}},
			CreatedAt: at,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupModerationReport(t, pool, report.ID) })
		cases, err := service.ListCases(ctx, domain.ModerationCaseFilter{
			Statuses: []domain.ModerationCaseStatus{domain.ModerationCaseOpen},
			Target:   target, Limit: 10,
		})
		if err != nil || len(cases) != 1 {
			t.Fatalf("open cases=%+v err=%v", cases, err)
		}
		claimed, err := service.ClaimCase(
			ctx, cases[0].ID, cases[0].Version, "pg-owner", at.Add(time.Second),
		)
		if err != nil {
			t.Fatal(err)
		}
		detail, _, err := service.DecideCase(ctx, domain.ModerationDecisionRequest{
			CaseID: claimed.ID, ExpectedVersion: claimed.Version,
			Actor: "pg-owner", Reason: "confirmed", CommandID: command,
			Kind: domain.ModerationDecisionViolation,
			Actions: []domain.ModerationActionDraft{{
				Kind: domain.ModerationActionMarkFake, Payload: []byte(`{}`),
			}},
			CreatedAt: at.Add(2 * time.Second),
		})
		if err != nil || len(detail.Actions) != 1 {
			t.Fatalf("decision=%+v err=%v", detail, err)
		}
		return claimed.ID, detail.Actions[0].ID
	}

	target := domain.Peer{Type: domain.PeerTypeUser, ID: base + 1}
	oldCaseID, oldActionID := createDecision(target, base+2, "old", "pg-old", now)
	newCaseID, newActionID := createDecision(target, base+3, "new", "pg-new", now.Add(3*time.Second))
	claimedActions, err := store.ClaimModerationActions(ctx, now.Add(6*time.Second), 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claimedByID := make(map[int64]domain.ModerationAction, len(claimedActions))
	for _, action := range claimedActions {
		claimedByID[action.ID] = action
	}
	oldAction, oldFound := claimedByID[oldActionID]
	newAction, newFound := claimedByID[newActionID]
	if !oldFound || !newFound {
		t.Fatalf("claimed actions=%+v", claimedActions)
	}
	if current, err := store.IsModerationActionCurrent(ctx, oldAction); err != nil || current {
		t.Fatalf("old current=%v err=%v", current, err)
	}
	if current, err := store.IsModerationActionCurrent(ctx, newAction); err != nil || !current {
		t.Fatalf("new current=%v err=%v", current, err)
	}
	if err := store.SupersedeModerationAction(
		ctx, oldAction.ID, oldAction.Attempts, now.Add(7*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteModerationAction(
		ctx, newAction.ID, newAction.Attempts, true, "", time.Time{},
		now.Add(8*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	oldDetail, _, err := service.Case(ctx, oldCaseID)
	if err != nil || oldDetail.Case.Status != domain.ModerationCaseResolved ||
		oldDetail.Actions[0].Status != domain.ModerationActionSuperseded {
		t.Fatalf("old detail=%+v err=%v", oldDetail, err)
	}
	newDetail, _, err := service.Case(ctx, newCaseID)
	if err != nil || newDetail.Case.Status != domain.ModerationCaseResolved ||
		newDetail.Actions[0].Status != domain.ModerationActionSucceeded {
		t.Fatalf("new detail=%+v err=%v", newDetail, err)
	}

	appealTarget := domain.Peer{Type: domain.PeerTypeUser, ID: base + 10}
	appealedCaseID, appealedActionID := createDecision(
		appealTarget, base+11, "appealed", "pg-appealed", now.Add(10*time.Second),
	)
	actions, err := store.ClaimModerationActions(ctx, now.Add(13*time.Second), 10, time.Minute)
	if err != nil || len(actions) != 1 || actions[0].ID != appealedActionID {
		t.Fatalf("appealed action=%+v err=%v", actions, err)
	}
	if err := store.CompleteModerationAction(
		ctx, actions[0].ID, actions[0].Attempts, true, "", time.Time{},
		now.Add(14*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	appeal, _, err := service.SubmitAppeal(
		ctx, appealedCaseID, appealTarget.ID, "please review", now.Add(15*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = createDecision(
		appealTarget, base+12, "newer", "pg-newer-owner", now.Add(16*time.Second),
	)
	appealedDetail, _, err := service.Case(ctx, appealedCaseID)
	if err != nil {
		t.Fatal(err)
	}
	appealClaim, err := service.ClaimCase(
		ctx, appealedCaseID, appealedDetail.Case.Version, "pg-owner", now.Add(19*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = service.ReviewAppeal(ctx, domain.ModerationDecisionRequest{
		CaseID: appealedCaseID, AppealID: appeal.ID,
		ExpectedVersion: appealClaim.Version, Actor: "pg-owner",
		Reason: "grant", CommandID: "pg-stale-appeal",
		Kind: domain.ModerationDecisionAppealGrant,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionClearPeerFlags, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(20 * time.Second),
	})
	if !errors.Is(err, domain.ErrModerationActionConflict) {
		t.Fatalf("ReviewAppeal error=%v", err)
	}
}

func cleanupModerationReport(t *testing.T, pool *pgxpool.Pool, reportID int64) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Errorf("begin moderation cleanup: %v", err)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var caseID int64
	err = tx.QueryRow(ctx, `
SELECT case_id FROM moderation_case_reports WHERE report_id = $1`,
		reportID,
	).Scan(&caseID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("find moderation cleanup case: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, "DELETE FROM sponsored_message_impressions WHERE report_id = $1", reportID); err != nil {
		t.Errorf("cleanup sponsored impression: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, "DELETE FROM channel_antispam_decisions WHERE report_id = $1", reportID); err != nil {
		t.Errorf("cleanup anti-spam decision: %v", err)
		return
	}
	if caseID > 0 {
		for _, statement := range []string{
			"DELETE FROM moderation_actions WHERE case_id = $1",
			"DELETE FROM moderation_decisions WHERE case_id = $1",
			"DELETE FROM moderation_appeal_links WHERE case_id = $1",
			"DELETE FROM moderation_appeals WHERE case_id = $1",
			"DELETE FROM moderation_case_reports WHERE case_id = $1",
			"DELETE FROM moderation_cases WHERE id = $1",
		} {
			if _, err := tx.Exec(ctx, statement, caseID); err != nil {
				t.Errorf("moderation cleanup %q: %v", statement, err)
				return
			}
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM moderation_legacy_ephemeral_migrations WHERE moderation_report_id = $1", reportID); err != nil {
		t.Errorf("cleanup legacy moderation mapping: %v", err)
		return
	}
	if _, err := tx.Exec(ctx, "DELETE FROM moderation_reports WHERE id = $1", reportID); err != nil {
		t.Errorf("cleanup moderation report: %v", err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit moderation cleanup: %v", err)
	}
}
