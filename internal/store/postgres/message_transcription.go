package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

type MessageTranscriptionStore struct {
	db sqlcgen.DBTX
}

func NewMessageTranscriptionStore(db sqlcgen.DBTX) *MessageTranscriptionStore {
	return &MessageTranscriptionStore{db: db}
}

var _ store.TranscriptionStore = (*MessageTranscriptionStore)(nil)

const messageTranscriptionColumns = `document_id, transcription_id, status, text,
       requested_by, peer_type, peer_id, msg_id, failure, created_at, updated_at`

func scanMessageTranscription(row pgx.Row) (domain.MessageTranscription, error) {
	var out domain.MessageTranscription
	var status, peerType, text string
	err := row.Scan(&out.DocumentID, &out.TranscriptionID, &status, &text,
		&out.RequestedBy, &peerType, &out.Peer.ID, &out.MsgID, &out.Failure,
		&out.CreatedAt, &out.UpdatedAt)
	if err != nil {
		return domain.MessageTranscription{}, err
	}
	out.Status = domain.TranscriptionStatus(status)
	out.Peer.Type = domain.PeerType(peerType)
	out.Text = text
	return out, nil
}

func (s *MessageTranscriptionStore) GetTranscription(ctx context.Context, documentID int64) (domain.MessageTranscription, bool, error) {
	if s == nil || s.db == nil {
		return domain.MessageTranscription{}, false, fmt.Errorf("message transcription store is not configured")
	}
	row, err := scanMessageTranscription(s.db.QueryRow(ctx, `
SELECT `+messageTranscriptionColumns+` FROM message_transcriptions WHERE document_id = $1`, documentID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MessageTranscription{}, false, nil
		}
		return domain.MessageTranscription{}, false, fmt.Errorf("get message transcription: %w", err)
	}
	return row, true, nil
}

func (s *MessageTranscriptionStore) CreateTranscriptionPending(ctx context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error) {
	if s == nil || s.db == nil {
		return domain.MessageTranscription{}, false, fmt.Errorf("message transcription store is not configured")
	}
	row, err := scanMessageTranscription(s.db.QueryRow(ctx, `
INSERT INTO message_transcriptions (document_id, transcription_id, status, requested_by, peer_type, peer_id, msg_id)
VALUES ($1, $2, 'pending', $3, $4, $5, $6)
ON CONFLICT (document_id) DO NOTHING
RETURNING `+messageTranscriptionColumns,
		req.DocumentID, req.TranscriptionID, req.RequestedBy,
		string(req.Peer.Type), req.Peer.ID, req.MsgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MessageTranscription{}, false, nil
		}
		return domain.MessageTranscription{}, false, fmt.Errorf("create message transcription: %w", err)
	}
	return row, true, nil
}

func (s *MessageTranscriptionStore) ResetTranscriptionPending(ctx context.Context, req domain.PendingTranscriptionRequest) (domain.MessageTranscription, bool, error) {
	if s == nil || s.db == nil {
		return domain.MessageTranscription{}, false, fmt.Errorf("message transcription store is not configured")
	}
	row, err := scanMessageTranscription(s.db.QueryRow(ctx, `
UPDATE message_transcriptions
SET status = 'pending', transcription_id = $2, requested_by = $3,
    peer_type = $4, peer_id = $5, msg_id = $6, failure = '', updated_at = now()
WHERE document_id = $1 AND status = 'failed'
RETURNING `+messageTranscriptionColumns,
		req.DocumentID, req.TranscriptionID, req.RequestedBy,
		string(req.Peer.Type), req.Peer.ID, req.MsgID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MessageTranscription{}, false, nil
		}
		return domain.MessageTranscription{}, false, fmt.Errorf("reset message transcription: %w", err)
	}
	return row, true, nil
}

func (s *MessageTranscriptionStore) MarkTranscriptionDone(ctx context.Context, documentID int64, text string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("message transcription store is not configured")
	}
	if _, err := s.db.Exec(ctx, `
UPDATE message_transcriptions
SET status = 'done', text = $2, failure = '', updated_at = now()
WHERE document_id = $1`, documentID, text); err != nil {
		return fmt.Errorf("mark message transcription done: %w", err)
	}
	return nil
}

func (s *MessageTranscriptionStore) MarkTranscriptionFailed(ctx context.Context, documentID int64, failure string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("message transcription store is not configured")
	}
	if _, err := s.db.Exec(ctx, `
UPDATE message_transcriptions
SET status = 'failed', failure = $2, updated_at = now()
WHERE document_id = $1 AND status <> 'done'`, documentID, failure); err != nil {
		return fmt.Errorf("mark message transcription failed: %w", err)
	}
	return nil
}
