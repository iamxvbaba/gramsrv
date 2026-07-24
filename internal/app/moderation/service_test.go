package moderation

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestAcceptReportReturnsDurableRetry(t *testing.T) {
	reports := memory.NewModerationReportStore()
	service := NewService(reports)
	draft := domain.ModerationReportDraft{
		ReporterUserID: 100, Source: domain.ModerationSourceMessagesSpam,
		Target: domain.Peer{Type: domain.PeerTypeUser, ID: 200},
		Reason: domain.ModerationReasonSpam, Option: "v1/spam",
		Items: []domain.ModerationReportItem{{
			Kind:   domain.ModerationItemPeer,
			Peer:   domain.Peer{Type: domain.PeerTypeUser, ID: 200},
			ItemID: 200, AuthorUserID: 200, EvidenceSchemaVersion: 1,
			Evidence: []byte(`{"snapshot":"peer"}`),
		}},
		CreatedAt: time.Now().UTC(),
	}
	first, created, err := service.AcceptReport(context.Background(), draft)
	if err != nil || !created {
		t.Fatalf("first created=%v err=%v", created, err)
	}
	draft.CreatedAt = draft.CreatedAt.Add(time.Minute)
	retry, created, err := service.AcceptReport(context.Background(), draft)
	if err != nil || created || retry.ID != first.ID {
		t.Fatalf("retry=%+v created=%v err=%v", retry, created, err)
	}
}
