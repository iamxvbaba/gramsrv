package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

// SecretChatStore is the PostgreSQL implementation of store.SecretChatStore.
// Blind relay: g_a/g_b/key_fingerprint are stored verbatim as BYTEA/BIGINT; handshake state
// transitions use a conditional UPDATE as an atomic CAS (accept binds the accepting device,
// discard is idempotent). The behavior contract with the memory implementation is pinned by storetest.
type SecretChatStore struct {
	db sqlcgen.DBTX
}

// NewSecretChatStore creates a SecretChatStore on top of a pgx pool.
func NewSecretChatStore(db sqlcgen.DBTX) *SecretChatStore {
	return &SecretChatStore{db: db}
}

const secretChatColumns = `chat_id, admin_access_hash, participant_access_hash, admin_user_id,
admin_auth_key_id, participant_user_id, participant_auth_key_id, state, g_a, g_b,
key_fingerprint, layer, folder_id, history_deleted, random_id, date`

func scanSecretChat(row rowScanner) (domain.SecretChat, error) {
	var c domain.SecretChat
	var state string
	if err := row.Scan(
		&c.ID, &c.AdminAccessHash, &c.ParticipantAccessHash, &c.AdminUserID,
		&c.AdminAuthKeyID, &c.ParticipantUserID, &c.ParticipantAuthKeyID, &state, &c.GA, &c.GB,
		&c.KeyFingerprint, &c.Layer, &c.FolderID, &c.HistoryDeleted, &c.RandomID, &c.Date,
	); err != nil {
		return domain.SecretChat{}, err
	}
	c.State = domain.SecretChatState(state)
	return c, nil
}

func (s *SecretChatStore) CreateSecretChat(ctx context.Context, chat domain.SecretChat) error {
	if chat.ID == 0 || chat.ID != int(chat.RandomID) {
		return domain.ErrSecretChatRandomIDDuplicate
	}
	if chat.State == "" {
		chat.State = domain.SecretChatStateRequested
	}
	_, err := s.db.Exec(ctx, `
INSERT INTO secret_chats (chat_id, admin_access_hash, participant_access_hash, admin_user_id,
    admin_auth_key_id, participant_user_id, participant_auth_key_id, state, g_a, g_b,
    key_fingerprint, layer, folder_id, history_deleted, random_id, date)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		chat.ID, chat.AdminAccessHash, chat.ParticipantAccessHash, chat.AdminUserID,
		chat.AdminAuthKeyID, chat.ParticipantUserID, chat.ParticipantAuthKeyID, string(chat.State),
		nullableBytes(chat.GA), nullableBytes(chat.GB), chat.KeyFingerprint, chat.Layer,
		chat.FolderID, chat.HistoryDeleted, chat.RandomID, chat.Date)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return domain.ErrSecretChatRandomIDDuplicate
		}
		return fmt.Errorf("insert secret chat: %w", err)
	}
	return nil
}

func (s *SecretChatStore) GetSecretChat(ctx context.Context, chatID int) (domain.SecretChat, bool, error) {
	chat, err := scanSecretChat(s.db.QueryRow(ctx,
		`SELECT `+secretChatColumns+` FROM secret_chats WHERE chat_id = $1`, chatID))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SecretChat{}, false, nil
	}
	if err != nil {
		return domain.SecretChat{}, false, fmt.Errorf("get secret chat: %w", err)
	}
	return chat, true, nil
}

func (s *SecretChatStore) AcceptSecretChat(ctx context.Context, chatID int, participantAuthKeyID int64, gb []byte, keyFingerprint int64) (domain.SecretChat, error) {
	// Atomic CAS: transition to normal only when requested and no accepting device is bound.
	chat, err := scanSecretChat(s.db.QueryRow(ctx, `
UPDATE secret_chats
SET state = 'normal', participant_auth_key_id = $2, g_b = $3, key_fingerprint = $4
WHERE chat_id = $1 AND state = 'requested' AND participant_auth_key_id = 0
RETURNING `+secretChatColumns, chatID, participantAuthKeyID, nullableBytes(gb), keyFingerprint))
	if err == nil {
		return chat, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SecretChat{}, fmt.Errorf("accept secret chat: %w", err)
	}
	// CAS missed: decide the error code from the current state.
	current, ok, gerr := s.GetSecretChat(ctx, chatID)
	if gerr != nil {
		return domain.SecretChat{}, gerr
	}
	if !ok {
		return domain.SecretChat{}, domain.ErrSecretChatNotFound
	}
	switch current.State {
	case domain.SecretChatStateDiscarded:
		return domain.SecretChat{}, domain.ErrSecretChatAlreadyDeclined
	default:
		return domain.SecretChat{}, domain.ErrSecretChatAlreadyAccepted
	}
}

func (s *SecretChatStore) DiscardSecretChat(ctx context.Context, chatID int, historyDeleted bool) (domain.SecretChat, bool, error) {
	chat, err := scanSecretChat(s.db.QueryRow(ctx, `
UPDATE secret_chats
SET state = 'discarded', history_deleted = $2
WHERE chat_id = $1 AND state <> 'discarded'
RETURNING `+secretChatColumns, chatID, historyDeleted))
	if err == nil {
		return chat, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.SecretChat{}, false, fmt.Errorf("discard secret chat: %w", err)
	}
	// Not updated: either already in a terminal state (idempotent success) or non-existent.
	current, ok, gerr := s.GetSecretChat(ctx, chatID)
	if gerr != nil {
		return domain.SecretChat{}, false, gerr
	}
	if !ok {
		return domain.SecretChat{}, false, domain.ErrSecretChatNotFound
	}
	return current, true, nil
}

func (s *SecretChatStore) ListActiveSecretChatsByAuthKey(ctx context.Context, authKeyID int64) ([]domain.SecretChat, error) {
	if authKeyID == 0 {
		return nil, nil
	}
	rows, err := s.db.Query(ctx, `SELECT `+secretChatColumns+` FROM secret_chats
WHERE state <> 'discarded' AND (admin_auth_key_id = $1 OR participant_auth_key_id = $1)
ORDER BY chat_id`, authKeyID)
	if err != nil {
		return nil, fmt.Errorf("list secret chats by auth key: %w", err)
	}
	defer rows.Close()
	var out []domain.SecretChat
	for rows.Next() {
		chat, serr := scanSecretChat(rows)
		if serr != nil {
			return nil, fmt.Errorf("scan secret chat by auth key: %w", serr)
		}
		out = append(out, chat)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list secret chats by auth key rows: %w", err)
	}
	return out, nil
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
