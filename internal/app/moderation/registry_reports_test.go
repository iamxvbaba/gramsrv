package moderation

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestSponsoredReportRequiresIssuedImpressionAndLinksAtomically(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	store := memory.NewModerationReportStore()
	service := NewService(store)
	randomID := []byte("server-issued-random-id")
	if _, _, err := service.ReportSponsored(
		ctx, 11, randomID, domain.ModerationReasonSpam, "spam", now,
	); !errors.Is(err, domain.ErrModerationImpressionExpired) {
		t.Fatalf("unseen impression err=%v", err)
	}
	impression, err := domain.NewSponsoredMessageImpression(
		11, randomID, domain.Peer{Type: domain.PeerTypeChannel, ID: 22},
		33, []byte(`{"author_id":33,"creative_id":"creative-1"}`),
		now, now.Add(time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	impression, created, err := store.CreateSponsoredMessageImpression(ctx, impression)
	if err != nil || !created {
		t.Fatalf("impression=%+v created=%v err=%v", impression, created, err)
	}
	report, created, err := service.ReportSponsored(
		ctx, 11, randomID, domain.ModerationReasonSpam, "spam", now.Add(time.Second),
	)
	if err != nil || !created || report.ID <= 0 {
		t.Fatalf("report=%+v created=%v err=%v", report, created, err)
	}
	retry, created, err := service.ReportSponsored(
		ctx, 11, randomID, domain.ModerationReasonFake, "fake", now.Add(2*time.Second),
	)
	if err != nil || created || retry.ID != report.ID {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	if reports := store.Reports(); len(reports) != 1 ||
		reports[0].Items[0].EvidenceHash != impression.EvidenceHash {
		t.Fatalf("reports=%+v", reports)
	}
	if _, _, err := service.ReportSponsored(
		ctx, 11, []byte("expired"),
		domain.ModerationReasonSpam, "spam", now.Add(2*time.Hour),
	); !errors.Is(err, domain.ErrModerationImpressionExpired) {
		t.Fatalf("expired/unseen err=%v", err)
	}
}

func TestAntiSpamFalsePositiveRequiresNativeDecisionAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_750_000_000, 0).UTC()
	store := memory.NewModerationReportStore()
	service := NewService(store)
	if _, _, err := service.ReportAntiSpamFalsePositive(
		ctx, 11, 22, 33, now,
	); !errors.Is(err, domain.ErrModerationEvidenceNotFound) {
		t.Fatalf("missing decision err=%v", err)
	}
	decision, err := domain.NewChannelAntiSpamDecision(
		22, 33, 44,
		[]byte(`{"engine":"native-v1","score":0.99}`), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	decision, created, err := store.CreateChannelAntiSpamDecision(ctx, decision)
	if err != nil || !created {
		t.Fatalf("decision=%+v created=%v err=%v", decision, created, err)
	}
	report, created, err := service.ReportAntiSpamFalsePositive(
		ctx, 11, 22, 33, now.Add(time.Second),
	)
	if err != nil || !created || report.ID <= 0 {
		t.Fatalf("report=%+v created=%v err=%v", report, created, err)
	}
	retry, created, err := service.ReportAntiSpamFalsePositive(
		ctx, 11, 22, 33, now.Add(2*time.Second),
	)
	if err != nil || created || retry.ID != report.ID {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
	if reports := store.Reports(); len(reports) != 1 ||
		reports[0].Items[0].EvidenceHash != decision.EvidenceHash ||
		reports[0].Items[0].SecondaryID != 33 {
		t.Fatalf("reports=%+v", reports)
	}
}
