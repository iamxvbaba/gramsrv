package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// EncryptedQueueStore is the PostgreSQL implementation of store.EncryptedQueueStore.
// Blind-relay qts delivery queue: qts allocation (incrementing secret_qts_watermarks.reserved_qts)
// plus writing the queue row complete in a single transaction, guaranteeing no gaps in a device's
// qts. bytes is stored as-is as BYTEA and never decrypted.
type EncryptedQueueStore struct {
	db sqlcgen.DBTX
}

// NewEncryptedQueueStore creates an EncryptedQueueStore backed by a pgx pool.
func NewEncryptedQueueStore(db sqlcgen.DBTX) *EncryptedQueueStore {
	return &EncryptedQueueStore{db: db}
}

const encryptedMessageColumns = `receiver_auth_key_id, qts, receiver_user_id, chat_id, random_id,
date, is_service, bytes, file_id, file_access_hash, file_size, file_dc_id, file_key_fingerprint`

func scanEncryptedMessage(row rowScanner) (domain.SecretChatMessage, error) {
	var m domain.SecretChatMessage
	var (
		fileID, fileAccessHash, fileSize *int64
		fileDC, fileKeyFP                *int32
	)
	if err := row.Scan(
		&m.ReceiverAuthKeyID, &m.Qts, &m.ReceiverUserID, &m.ChatID, &m.RandomID,
		&m.Date, &m.IsService, &m.Bytes, &fileID, &fileAccessHash, &fileSize, &fileDC, &fileKeyFP,
	); err != nil {
		return domain.SecretChatMessage{}, err
	}
	if fileID != nil {
		ref := domain.EncryptedFileRef{ID: *fileID}
		if fileAccessHash != nil {
			ref.AccessHash = *fileAccessHash
		}
		if fileSize != nil {
			ref.Size = *fileSize
		}
		if fileDC != nil {
			ref.DCID = int(*fileDC)
		}
		if fileKeyFP != nil {
			ref.KeyFingerprint = int(*fileKeyFP)
		}
		m.File = &ref
	}
	return m, nil
}

func (s *EncryptedQueueStore) begin(ctx context.Context, op string) (pgx.Tx, error) {
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return nil, fmt.Errorf("%s: db does not support transactions", op)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin %s: %w", op, err)
	}
	return tx, nil
}

func (s *EncryptedQueueStore) AppendEncryptedMessage(ctx context.Context, msg domain.SecretChatMessage) (domain.SecretChatMessage, bool, error) {
	tx, err := s.begin(ctx, "append encrypted message")
	if err != nil {
		return domain.SecretChatMessage{}, false, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	// Idempotent: if the same receiver device, same chat, same random_id already exists, return the existing row (without reallocating qts).
	existing, err := scanEncryptedMessage(tx.QueryRow(ctx,
		`SELECT `+encryptedMessageColumns+` FROM encrypted_message_queue
WHERE receiver_auth_key_id = $1 AND chat_id = $2 AND random_id = $3`,
		msg.ReceiverAuthKeyID, msg.ChatID, msg.RandomID))
	if err == nil {
		if cerr := tx.Commit(ctx); cerr != nil {
			return domain.SecretChatMessage{}, false, fmt.Errorf("commit append (dedup): %w", cerr)
		}
		committed = true
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SecretChatMessage{}, false, fmt.Errorf("dedup lookup: %w", err)
	}

	// Allocate the next qts (first value 1), in the same transaction as the queue write.
	var qts int
	if err := tx.QueryRow(ctx, `
INSERT INTO secret_qts_watermarks (auth_key_id, reserved_qts)
VALUES ($1, 1)
ON CONFLICT (auth_key_id) DO UPDATE SET reserved_qts = secret_qts_watermarks.reserved_qts + 1, updated_at = now()
RETURNING reserved_qts`, msg.ReceiverAuthKeyID).Scan(&qts); err != nil {
		return domain.SecretChatMessage{}, false, fmt.Errorf("reserve device qts: %w", err)
	}
	msg.Qts = qts

	var (
		fileID, fileAccessHash, fileSize any
		fileDC, fileKeyFP                any
	)
	if msg.File != nil {
		fileID = msg.File.ID
		fileAccessHash = msg.File.AccessHash
		fileSize = msg.File.Size
		fileDC = int32(msg.File.DCID)
		fileKeyFP = int32(msg.File.KeyFingerprint)
	}
	if _, err := tx.Exec(ctx, `
INSERT INTO encrypted_message_queue (receiver_auth_key_id, qts, receiver_user_id, chat_id, random_id,
    date, is_service, bytes, file_id, file_access_hash, file_size, file_dc_id, file_key_fingerprint)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		msg.ReceiverAuthKeyID, msg.Qts, msg.ReceiverUserID, msg.ChatID, msg.RandomID,
		msg.Date, msg.IsService, msg.Bytes, fileID, fileAccessHash, fileSize, fileDC, fileKeyFP); err != nil {
		return domain.SecretChatMessage{}, false, fmt.Errorf("insert encrypted message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SecretChatMessage{}, false, fmt.Errorf("commit append encrypted message: %w", err)
	}
	committed = true
	return msg, false, nil
}

func (s *EncryptedQueueStore) ListEncryptedMessagesSince(ctx context.Context, receiverAuthKeyID int64, sinceQts, limit int) ([]domain.SecretChatMessage, error) {
	// RPC difference pages 1000 at a time and reads 1 extra row to probe hasMore.
	if limit <= 0 || limit > 1001 {
		limit = 1000
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+encryptedMessageColumns+` FROM encrypted_message_queue
WHERE receiver_auth_key_id = $1 AND qts > $2 ORDER BY qts ASC LIMIT $3`,
		receiverAuthKeyID, sinceQts, limit)
	if err != nil {
		return nil, fmt.Errorf("list encrypted messages: %w", err)
	}
	defer rows.Close()
	var out []domain.SecretChatMessage
	for rows.Next() {
		m, err := scanEncryptedMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *EncryptedQueueStore) ReservedQts(ctx context.Context, receiverAuthKeyID int64) (int, error) {
	var qts int
	err := s.db.QueryRow(ctx,
		`SELECT reserved_qts FROM secret_qts_watermarks WHERE auth_key_id = $1`, receiverAuthKeyID).Scan(&qts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reserved qts: %w", err)
	}
	return qts, nil
}

func (s *EncryptedQueueStore) AckEncryptedMessages(ctx context.Context, receiverAuthKeyID int64, maxQts int) error {
	if maxQts <= 0 {
		return nil
	}
	// Advance confirmed_qts (idempotent via GREATEST; a retreat is ignored).
	if _, err := s.db.Exec(ctx, `
INSERT INTO secret_qts_watermarks (auth_key_id, confirmed_qts)
VALUES ($1, $2)
ON CONFLICT (auth_key_id) DO UPDATE SET confirmed_qts = GREATEST(secret_qts_watermarks.confirmed_qts, EXCLUDED.confirmed_qts), updated_at = now()`,
		receiverAuthKeyID, maxQts); err != nil {
		return fmt.Errorf("advance confirmed qts: %w", err)
	}
	if _, err := s.db.Exec(ctx, `
UPDATE encrypted_message_queue SET acked = true
WHERE receiver_auth_key_id = $1 AND qts <= $2 AND NOT acked`, receiverAuthKeyID, maxQts); err != nil {
		return fmt.Errorf("ack encrypted messages: %w", err)
	}
	return nil
}

func (s *EncryptedQueueStore) AppendStateEvent(ctx context.Context, ev domain.EncryptedStateEvent) (int64, error) {
	var id int64
	if err := s.db.QueryRow(ctx, `
INSERT INTO encrypted_state_events (target_user_id, target_auth_key_id, chat_id, event_type, max_date, date)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`,
		ev.TargetUserID, ev.TargetAuthKeyID, ev.ChatID, int16(ev.Type), ev.MaxDate, ev.Date).Scan(&id); err != nil {
		return 0, fmt.Errorf("append state event: %w", err)
	}
	return id, nil
}

func (s *EncryptedQueueStore) ListUndeliveredStateEvents(ctx context.Context, targetUserID, deviceAuthKeyID int64, limit int) ([]domain.EncryptedStateEvent, error) {
	// RPC difference pages 1000 at a time and reads 1 extra row to probe hasMore.
	if limit <= 0 || limit > 1001 {
		limit = 1000
	}
	rows, err := s.db.Query(ctx, `
SELECT e.id, e.target_user_id, e.target_auth_key_id, e.chat_id, e.event_type, e.max_date, e.date
FROM encrypted_state_events e
WHERE e.target_user_id = $1
  AND (e.target_auth_key_id = 0 OR e.target_auth_key_id = $2)
  AND NOT EXISTS (SELECT 1 FROM encrypted_state_event_delivery d WHERE d.event_id = e.id AND d.auth_key_id = $2)
ORDER BY e.id ASC LIMIT $3`, targetUserID, deviceAuthKeyID, limit)
	if err != nil {
		return nil, fmt.Errorf("list undelivered state events: %w", err)
	}
	defer rows.Close()
	var out []domain.EncryptedStateEvent
	for rows.Next() {
		var ev domain.EncryptedStateEvent
		var typ int16
		if err := rows.Scan(&ev.ID, &ev.TargetUserID, &ev.TargetAuthKeyID, &ev.ChatID, &typ, &ev.MaxDate, &ev.Date); err != nil {
			return nil, err
		}
		ev.Type = domain.EncryptedStateEventType(typ)
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *EncryptedQueueStore) MarkStateEventsDelivered(ctx context.Context, deviceAuthKeyID int64, eventIDs []int64) error {
	if len(eventIDs) == 0 {
		return nil
	}
	if _, err := s.db.Exec(ctx, `
INSERT INTO encrypted_state_event_delivery (event_id, auth_key_id)
SELECT unnest($1::bigint[]), $2
ON CONFLICT (event_id, auth_key_id) DO NOTHING`, eventIDs, deviceAuthKeyID); err != nil {
		return fmt.Errorf("mark state events delivered: %w", err)
	}
	return nil
}

func (s *EncryptedQueueStore) PutEncryptedFile(ctx context.Context, ownerUserID int64, ref domain.EncryptedFileRef) error {
	if _, err := s.db.Exec(ctx, `
INSERT INTO encrypted_files (id, access_hash, owner_user_id, size, dc_id, key_fingerprint)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (id) DO UPDATE SET access_hash = EXCLUDED.access_hash, size = EXCLUDED.size,
    dc_id = EXCLUDED.dc_id, key_fingerprint = EXCLUDED.key_fingerprint`,
		ref.ID, ref.AccessHash, ownerUserID, ref.Size, int32(ref.DCID), int32(ref.KeyFingerprint)); err != nil {
		return fmt.Errorf("put encrypted file: %w", err)
	}
	return nil
}

func (s *EncryptedQueueStore) GetEncryptedFile(ctx context.Context, id, accessHash int64) (domain.EncryptedFileRef, bool, error) {
	var (
		ref   domain.EncryptedFileRef
		dc    int32
		keyFP int32
	)
	err := s.db.QueryRow(ctx,
		`SELECT id, access_hash, size, dc_id, key_fingerprint FROM encrypted_files WHERE id = $1 AND access_hash = $2`,
		id, accessHash).Scan(&ref.ID, &ref.AccessHash, &ref.Size, &dc, &keyFP)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EncryptedFileRef{}, false, nil
	}
	if err != nil {
		return domain.EncryptedFileRef{}, false, fmt.Errorf("get encrypted file: %w", err)
	}
	ref.DCID = int(dc)
	ref.KeyFingerprint = int(keyFP)
	return ref, true, nil
}
