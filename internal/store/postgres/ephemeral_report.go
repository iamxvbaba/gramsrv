package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// EphemeralReportStore persists the low-volume abuse-review evidence path.
// The hot ephemeral send/edit/delete path remains entirely in Redis.
type EphemeralReportStore struct {
	db sqlcgen.DBTX
}

func NewEphemeralReportStore(db sqlcgen.DBTX) *EphemeralReportStore {
	return &EphemeralReportStore{db: db}
}

func (s *EphemeralReportStore) CreateEphemeralReport(ctx context.Context, report domain.EphemeralAbuseReport) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("ephemeral report store is not configured")
	}
	if err := report.Validate(); err != nil {
		return false, err
	}
	evidence, err := json.Marshal(report.Evidence)
	if err != nil {
		return false, fmt.Errorf("marshal ephemeral report evidence: %w", err)
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO ephemeral_abuse_reports (
  reporter_user_id, channel_id, ephemeral_message_id, sender_user_id,
  receiver_user_id, report_option, report_comment, comment_hash,
  payload_hash, evidence, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11)
ON CONFLICT (
  reporter_user_id, channel_id, ephemeral_message_id, report_option, comment_hash
) DO NOTHING
`, report.ReporterUserID, report.Evidence.Peer.ID, report.Evidence.MessageID,
		report.Evidence.SenderUserID, report.Evidence.ReceiverUserID,
		report.Option, report.Comment, report.CommentHash[:], report.Evidence.PayloadHash[:], evidence, report.CreatedAt)
	if err != nil {
		return false, fmt.Errorf("insert ephemeral abuse report: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
