package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"telesrv/internal/domain"
	"telesrv/internal/store"
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

func (s *EphemeralReportStore) ListUnmigratedEphemeralReports(ctx context.Context, limit int) ([]store.LegacyEphemeralReport, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("ephemeral report store is not configured")
	}
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("legacy ephemeral report batch limit out of range")
	}
	rows, err := s.db.Query(ctx, `
SELECT r.id, r.reporter_user_id, r.channel_id, r.ephemeral_message_id,
       r.sender_user_id, r.receiver_user_id, r.report_option,
       r.report_comment, r.comment_hash, r.payload_hash, r.evidence,
       r.created_at
FROM ephemeral_abuse_reports r
LEFT JOIN moderation_legacy_ephemeral_migrations m
  ON m.legacy_report_id = r.id
WHERE m.legacy_report_id IS NULL
ORDER BY r.id
LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list unmigrated ephemeral reports: %w", err)
	}
	defer rows.Close()

	out := make([]store.LegacyEphemeralReport, 0, limit)
	for rows.Next() {
		var (
			legacy                                  store.LegacyEphemeralReport
			channelID, senderUserID, receiverUserID int64
			messageID                               int
			commentHash, payloadHash, evidenceRaw   []byte
		)
		if err := rows.Scan(
			&legacy.ID, &legacy.Report.ReporterUserID, &channelID,
			&messageID, &senderUserID, &receiverUserID,
			&legacy.Report.Option, &legacy.Report.Comment, &commentHash,
			&payloadHash, &evidenceRaw, &legacy.Report.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan legacy ephemeral report: %w", err)
		}
		if legacy.ID <= 0 || len(commentHash) != len(legacy.Report.CommentHash) ||
			len(payloadHash) != len(legacy.Report.Evidence.PayloadHash) {
			return nil, fmt.Errorf("legacy ephemeral report %d has invalid persisted identity", legacy.ID)
		}
		copy(legacy.Report.CommentHash[:], commentHash)
		if err := json.Unmarshal(evidenceRaw, &legacy.Report.Evidence); err != nil {
			return nil, fmt.Errorf("decode legacy ephemeral report %d evidence: %w", legacy.ID, err)
		}
		if legacy.Report.Evidence.Peer.Type != domain.PeerTypeChannel ||
			legacy.Report.Evidence.Peer.ID != channelID ||
			legacy.Report.Evidence.MessageID != messageID ||
			legacy.Report.Evidence.SenderUserID != senderUserID ||
			legacy.Report.Evidence.ReceiverUserID != receiverUserID ||
			!bytes.Equal(legacy.Report.Evidence.PayloadHash[:], payloadHash) {
			return nil, fmt.Errorf("legacy ephemeral report %d evidence disagrees with indexed columns", legacy.ID)
		}
		if err := legacy.Report.Validate(); err != nil {
			return nil, fmt.Errorf("validate legacy ephemeral report %d: %w", legacy.ID, err)
		}
		out = append(out, legacy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate legacy ephemeral reports: %w", err)
	}
	return out, nil
}
