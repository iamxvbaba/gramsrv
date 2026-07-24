package moderation

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

type legacyEphemeralReader struct {
	rows []store.LegacyEphemeralReport
}

func (r *legacyEphemeralReader) ListUnmigratedEphemeralReports(_ context.Context, limit int) ([]store.LegacyEphemeralReport, error) {
	if len(r.rows) == 0 {
		return nil, nil
	}
	if limit > len(r.rows) {
		limit = len(r.rows)
	}
	out := append([]store.LegacyEphemeralReport(nil), r.rows[:limit]...)
	r.rows = r.rows[limit:]
	return out, nil
}

type legacyModerationImporter struct {
	*memory.ModerationReportStore
	mappings map[int64]int64
}

func (s *legacyModerationImporter) ImportLegacyEphemeralReport(ctx context.Context, legacyID int64, report domain.ModerationReport) (domain.ModerationReport, bool, error) {
	if reportID, ok := s.mappings[legacyID]; ok {
		existing, _, err := s.GetModerationReport(ctx, reportID)
		return existing, false, err
	}
	stored, created, err := s.CreateModerationReport(ctx, report)
	if err == nil {
		s.mappings[legacyID] = stored.ID
	}
	return stored, created, err
}

func TestMigrateLegacyEphemeralReportsPreservesEvidenceAndMediaHolds(t *testing.T) {
	now := time.Now().UTC()
	reporter := int64(101)
	message := domain.EphemeralMessage{
		ID: 44, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: 303},
		SenderUserID: 202, ReceiverUserID: reporter, Date: int(now.Unix()),
		Content: domain.EphemeralContent{
			Message: "evidence",
			Media: &domain.MessageMedia{
				Kind: domain.MessageMediaKindDocument,
				Document: &domain.Document{
					ID: 909, AccessHash: 1, MimeType: "text/plain", Size: 8,
				},
			},
		},
		Version: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	legacy := domain.NewEphemeralAbuseReport(reporter, "spam", "review", message, now)
	source := &legacyEphemeralReader{rows: []store.LegacyEphemeralReport{{ID: 7, Report: legacy}}}
	target := &legacyModerationImporter{
		ModerationReportStore: memory.NewModerationReportStore(),
		mappings:              make(map[int64]int64),
	}
	service := NewService(target)
	count, err := service.MigrateLegacyEphemeralReports(context.Background(), source, 10)
	if err != nil || count != 1 {
		t.Fatalf("migrate count=%d err=%v", count, err)
	}
	reports := target.Reports()
	if len(reports) != 1 {
		t.Fatalf("reports=%d, want 1", len(reports))
	}
	got := reports[0]
	if got.Source != domain.ModerationSourceEphemeral ||
		got.Target != message.Peer || len(got.Items) != 1 ||
		got.Items[0].AuthorUserID != message.SenderUserID {
		t.Fatalf("migrated report=%+v", got)
	}
	if len(got.MediaHolds) != 1 ||
		got.MediaHolds[0].StorageKey != "doc:909" {
		t.Fatalf("media holds=%+v", got.MediaHolds)
	}
}
