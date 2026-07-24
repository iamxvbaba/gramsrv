package memory

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestModerationCaseLifecycleAndNewReportsDuringAction(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	target := domain.Peer{Type: domain.PeerTypeUser, ID: 900}
	store := NewModerationReportStore()
	create := func(reporter int64, option string, at time.Time) domain.ModerationReport {
		report, err := domain.NewModerationReport(domain.ModerationReportDraft{
			ReporterUserID: reporter, Source: domain.ModerationSourceAccountPeer,
			Target: target, Reason: domain.ModerationReasonFake,
			Option: option,
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
		stored, created, err := store.CreateModerationReport(ctx, report)
		if err != nil || !created {
			t.Fatalf("create report created=%v err=%v", created, err)
		}
		return stored
	}
	create(101, "fake", now)
	create(102, "fake:impersonation", now.Add(time.Second))
	cases, err := store.ListModerationCases(ctx, domain.ModerationCaseFilter{Limit: 10})
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	item := cases[0]
	if item.ReportCount != 2 || item.DistinctReporterCount != 2 ||
		item.Version != 2 || item.Severity != domain.ModerationSeverityMedium {
		t.Fatalf("case aggregate=%+v", item)
	}
	claimed, err := store.ClaimModerationCase(ctx, item.ID, item.Version, "reviewer", now.Add(2*time.Second))
	if err != nil || claimed.Status != domain.ModerationCaseInReview {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	decision, err := domain.NewModerationDecisionRequest(domain.ModerationDecisionRequest{
		CaseID: item.ID, ExpectedVersion: claimed.Version, Actor: "reviewer",
		Reason: "confirmed impersonation", CommandID: "decision-1",
		Kind: domain.ModerationDecisionViolation,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionMarkFake, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, created, err := store.DecideModerationCase(ctx, decision)
	if err != nil || !created ||
		detail.Case.Status != domain.ModerationCaseActionPending ||
		len(detail.Actions) != 1 {
		t.Fatalf("decision detail=%+v created=%v err=%v", detail, created, err)
	}
	if _, created, err := store.DecideModerationCase(ctx, decision); err != nil || created {
		t.Fatalf("decision retry created=%v err=%v", created, err)
	}

	// Once a decision is durable, later reports open a new case instead of
	// mutating the evidence set under the pending action.
	create(103, "fake:new-evidence", now.Add(4*time.Second))
	cases, err = store.ListModerationCases(ctx, domain.ModerationCaseFilter{Limit: 10})
	if err != nil || len(cases) != 2 {
		t.Fatalf("cases after new evidence=%+v err=%v", cases, err)
	}
	actions, err := store.ClaimModerationActions(ctx, now.Add(5*time.Second), 10, time.Minute)
	if err != nil || len(actions) != 1 {
		t.Fatalf("claimed actions=%+v err=%v", actions, err)
	}
	if err := store.CompleteModerationAction(
		ctx, actions[0].ID, actions[0].Attempts, true, "",
		time.Time{}, now.Add(6*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	resolved, found, err := store.GetModerationCase(ctx, item.ID)
	if err != nil || !found || resolved.Case.Status != domain.ModerationCaseResolved {
		t.Fatalf("resolved=%+v found=%v err=%v", resolved, found, err)
	}
	appeal, err := domain.NewModerationAppeal(
		item.ID, target.ID, domain.ModerationCaseResolved,
		"This is a mistake.", now.Add(7*time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.CreateModerationAppeal(ctx, appeal); err != nil || !created {
		t.Fatalf("appeal created=%v err=%v", created, err)
	}
	appealed, _, _ := store.GetModerationCase(ctx, item.ID)
	if appealed.Case.Status != domain.ModerationCaseAppealReview ||
		len(appealed.Appeals) != 1 {
		t.Fatalf("appealed detail=%+v", appealed)
	}
}

func TestModerationActionFailedCanBeRedrivenByNewDecision(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	target := domain.Peer{Type: domain.PeerTypeUser, ID: 902}
	store := NewModerationReportStore()
	report, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: 901, Source: domain.ModerationSourceAccountPeer,
		Target: target, Reason: domain.ModerationReasonSpam, Option: "spam",
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
	if _, created, err := store.CreateModerationReport(ctx, report); err != nil || !created {
		t.Fatalf("create report created=%v err=%v", created, err)
	}
	cases, err := store.ListModerationCases(ctx, domain.ModerationCaseFilter{Limit: 10})
	if err != nil || len(cases) != 1 {
		t.Fatalf("cases=%+v err=%v", cases, err)
	}
	claimed, err := store.ClaimModerationCase(
		ctx, cases[0].ID, cases[0].Version, "reviewer", now.Add(time.Second),
	)
	if err != nil {
		t.Fatal(err)
	}
	firstDecision, err := domain.NewModerationDecisionRequest(domain.ModerationDecisionRequest{
		CaseID: claimed.ID, ExpectedVersion: claimed.Version,
		Actor: "reviewer", Reason: "first command kept failing",
		CommandID: "redrive-first", Kind: domain.ModerationDecisionViolation,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionMarkScam, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, err := store.DecideModerationCase(ctx, firstDecision); err != nil || !created {
		t.Fatalf("first decision created=%v err=%v", created, err)
	}
	for attempt := 1; attempt <= domain.MaxModerationActionAttempts; attempt++ {
		at := now.Add(time.Duration(attempt+2) * time.Second)
		actions, err := store.ClaimModerationActions(ctx, at, 10, time.Second)
		if err != nil || len(actions) != 1 {
			t.Fatalf("attempt %d actions=%+v err=%v", attempt, actions, err)
		}
		if err := store.CompleteModerationAction(
			ctx, actions[0].ID, actions[0].Attempts, false, "downstream unavailable",
			at.Add(time.Millisecond), at,
		); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}
	failed, found, err := store.GetModerationCase(ctx, claimed.ID)
	if err != nil || !found || failed.Case.Status != domain.ModerationCaseActionFailed ||
		len(failed.Actions) != 1 ||
		failed.Actions[0].Status != domain.ModerationActionFailed {
		t.Fatalf("failed=%+v found=%v err=%v", failed, found, err)
	}
	redrive, err := domain.NewModerationDecisionRequest(domain.ModerationDecisionRequest{
		CaseID: claimed.ID, ExpectedVersion: failed.Case.Version,
		Actor: "reviewer", Reason: "redrive after dependency recovery",
		CommandID: "redrive-second", Kind: domain.ModerationDecisionViolation,
		Actions: []domain.ModerationActionDraft{{
			Kind: domain.ModerationActionMarkScam, Payload: []byte(`{}`),
		}},
		CreatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, created, err := store.DecideModerationCase(ctx, redrive)
	if err != nil || !created ||
		pending.Case.Status != domain.ModerationCaseActionPending ||
		len(pending.Actions) != 2 {
		t.Fatalf("pending=%+v created=%v err=%v", pending, created, err)
	}
	actions, err := store.ClaimModerationActions(ctx, now.Add(2*time.Minute), 10, time.Second)
	if err != nil || len(actions) != 1 || actions[0].DecisionID == failed.Actions[0].DecisionID {
		t.Fatalf("redrive actions=%+v err=%v", actions, err)
	}
	if err := store.CompleteModerationAction(
		ctx, actions[0].ID, actions[0].Attempts, true, "",
		time.Time{}, now.Add(2*time.Minute+time.Second),
	); err != nil {
		t.Fatal(err)
	}
	resolved, found, err := store.GetModerationCase(ctx, claimed.ID)
	if err != nil || !found || resolved.Case.Status != domain.ModerationCaseResolved {
		t.Fatalf("resolved=%+v found=%v err=%v", resolved, found, err)
	}
	var failedCount, succeededCount int
	for _, action := range resolved.Actions {
		switch action.Status {
		case domain.ModerationActionFailed:
			failedCount++
		case domain.ModerationActionSucceeded:
			succeededCount++
		}
	}
	if failedCount != 1 || succeededCount != 1 {
		t.Fatalf("action history failed=%d succeeded=%d", failedCount, succeededCount)
	}
}
