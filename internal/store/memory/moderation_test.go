package memory

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestModerationReportStoreIdempotencyAndCopyIsolation(t *testing.T) {
	report, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: 11, Source: domain.ModerationSourceMessages,
		Target: domain.Peer{Type: domain.PeerTypeUser, ID: 22},
		Reason: domain.ModerationReasonSpam, Option: "v1/spam",
		Items: []domain.ModerationReportItem{{
			Kind:   domain.ModerationItemMessage,
			Peer:   domain.Peer{Type: domain.PeerTypeUser, ID: 22},
			ItemID: 5, AuthorUserID: 22, EvidenceSchemaVersion: 1,
			Evidence: []byte(`{"message":"spam"}`),
		}},
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := NewModerationReportStore()
	first, created, err := store.CreateModerationReport(context.Background(), report)
	if err != nil || !created || first.ID <= 0 {
		t.Fatalf("first = %+v created=%v err=%v", first, created, err)
	}
	first.Items[0].Evidence[0] = '['
	retry, created, err := store.CreateModerationReport(context.Background(), report)
	if err != nil || created || retry.ID != first.ID {
		t.Fatalf("retry = %+v created=%v err=%v", retry, created, err)
	}
	if retry.Items[0].Evidence[0] != '{' {
		t.Fatalf("caller mutation changed stored evidence: %s", retry.Items[0].Evidence)
	}
}

func TestModerationReportStoreRateLimitDoesNotChargeIdempotentRetry(t *testing.T) {
	ctx := context.Background()
	store := NewModerationReportStore()
	now := time.Unix(1_750_000_000, 0).UTC()
	var first domain.ModerationReport
	for i := 0; i < domain.MaxModerationReportsPerHour; i++ {
		report, err := domain.NewModerationReport(domain.ModerationReportDraft{
			ReporterUserID: 71, Source: domain.ModerationSourceMessages,
			Target: domain.Peer{Type: domain.PeerTypeUser, ID: 72},
			Reason: domain.ModerationReasonSpam, Option: "spam",
			Items: []domain.ModerationReportItem{{
				Kind:   domain.ModerationItemMessage,
				Peer:   domain.Peer{Type: domain.PeerTypeUser, ID: 72},
				ItemID: int64(i + 1), AuthorUserID: 72,
				EvidenceSchemaVersion: 1,
				Evidence:              []byte(`{"message":"spam"}`),
			}},
			CreatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, created, err := store.CreateModerationReport(ctx, report); err != nil || !created {
			t.Fatalf("create %d: created=%v err=%v", i, created, err)
		}
		if i == 0 {
			first = report
		}
	}
	if got, created, err := store.CreateModerationReport(ctx, first); err != nil || created || got.ID == 0 {
		t.Fatalf("retry after limit: got=%+v created=%v err=%v", got, created, err)
	}
	overflow, err := domain.NewModerationReport(domain.ModerationReportDraft{
		ReporterUserID: 71, Source: domain.ModerationSourceMessages,
		Target: domain.Peer{Type: domain.PeerTypeUser, ID: 72},
		Reason: domain.ModerationReasonSpam, Option: "spam",
		Items: []domain.ModerationReportItem{{
			Kind:   domain.ModerationItemMessage,
			Peer:   domain.Peer{Type: domain.PeerTypeUser, ID: 72},
			ItemID: 999, AuthorUserID: 72, EvidenceSchemaVersion: 1,
			Evidence: []byte(`{"message":"overflow"}`),
		}},
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateModerationReport(ctx, overflow); err != domain.ErrModerationRateLimited {
		t.Fatalf("overflow err=%v, want ErrModerationRateLimited", err)
	}
}
