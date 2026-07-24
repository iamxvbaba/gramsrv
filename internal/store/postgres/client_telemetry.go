package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"telesrv/internal/domain"
	"telesrv/internal/store/postgres/sqlcgen"
)

type ClientTelemetryStore struct {
	db sqlcgen.DBTX
}

func NewClientTelemetryStore(db sqlcgen.DBTX) *ClientTelemetryStore {
	return &ClientTelemetryStore{db: db}
}

func (s *ClientTelemetryStore) CreateClientTelemetry(ctx context.Context, event domain.ClientTelemetryEvent) (domain.ClientTelemetryEvent, bool, error) {
	if s == nil || s.db == nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("client telemetry store is not configured")
	}
	if err := event.Validate(); err != nil || event.ID != 0 {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryInvalid
	}
	beginner, ok := s.db.(txBeginner)
	if !ok {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("client telemetry store requires transaction-capable postgres handle")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("begin client telemetry: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
SELECT pg_advisory_xact_lock(
  hashtextextended('client-telemetry:' || $1::bigint::text, 0)
)`, event.UserID); err != nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("lock client telemetry user: %w", err)
	}
	existing, found, err := getClientTelemetryByFingerprint(
		ctx, tx, event.UserID, event.Fingerprint,
	)
	if err != nil {
		return domain.ClientTelemetryEvent{}, false, err
	}
	if found {
		return existing, false, nil
	}
	var hourly, daily int
	if err := tx.QueryRow(ctx, `
SELECT
  count(*) FILTER (WHERE created_at >= $2::timestamptz - interval '1 hour'),
  count(*) FILTER (WHERE created_at >= $2::timestamptz - interval '24 hours')
FROM client_telemetry_events
WHERE user_id = $1 AND created_at <= $2::timestamptz`,
		event.UserID, event.CreatedAt,
	).Scan(&hourly, &daily); err != nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("count client telemetry: %w", err)
	}
	if hourly >= domain.MaxClientTelemetryEventsPerHour ||
		daily >= domain.MaxClientTelemetryEventsPerDay {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryRateLimited
	}
	if err := tx.QueryRow(ctx, `
INSERT INTO client_telemetry_events (
  user_id, kind, peer_type, peer_id, subject_ids, payload,
  fingerprint, created_at
) VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,$8)
RETURNING id`,
		event.UserID, string(event.Kind), string(event.Peer.Type),
		event.Peer.ID, event.SubjectIDs, []byte(event.Payload),
		event.Fingerprint[:], event.CreatedAt,
	).Scan(&event.ID); err != nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("insert client telemetry: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("commit client telemetry: %w", err)
	}
	return event, true, nil
}

func (s *ClientTelemetryStore) DeleteExpiredClientTelemetry(ctx context.Context, olderThan time.Time, limit int) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("client telemetry store is not configured")
	}
	if olderThan.IsZero() || limit <= 0 || limit > 10000 {
		return 0, domain.ErrClientTelemetryInvalid
	}
	tag, err := s.db.Exec(ctx, `
WITH doomed AS (
  SELECT id
  FROM client_telemetry_events
  WHERE created_at < $1
  ORDER BY created_at, id
  LIMIT $2
)
DELETE FROM client_telemetry_events e
USING doomed d
WHERE e.id = d.id`, olderThan, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired client telemetry: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func getClientTelemetryByFingerprint(ctx context.Context, db sqlcgen.DBTX, userID int64, fingerprint [32]byte) (domain.ClientTelemetryEvent, bool, error) {
	var event domain.ClientTelemetryEvent
	var kind, peerType string
	var payload, storedFingerprint []byte
	if err := db.QueryRow(ctx, `
SELECT id, user_id, kind, peer_type, peer_id, subject_ids, payload,
       fingerprint, created_at
FROM client_telemetry_events
WHERE user_id = $1 AND fingerprint = $2`,
		userID, fingerprint[:],
	).Scan(
		&event.ID, &event.UserID, &kind, &peerType, &event.Peer.ID,
		&event.SubjectIDs, &payload, &storedFingerprint, &event.CreatedAt,
	); errors.Is(err, pgx.ErrNoRows) {
		return domain.ClientTelemetryEvent{}, false, nil
	} else if err != nil {
		return domain.ClientTelemetryEvent{}, false, fmt.Errorf("get client telemetry: %w", err)
	}
	event.Kind = domain.ClientTelemetryKind(kind)
	event.Peer.Type = domain.PeerType(peerType)
	var canonicalPayload map[string]any
	if err := json.Unmarshal(payload, &canonicalPayload); err != nil || canonicalPayload == nil {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryInvalid
	}
	canonicalRaw, marshalErr := json.Marshal(canonicalPayload)
	if marshalErr != nil {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryInvalid
	}
	event.Payload = canonicalRaw
	if len(storedFingerprint) != len(event.Fingerprint) {
		return domain.ClientTelemetryEvent{}, false, domain.ErrClientTelemetryInvalid
	}
	copy(event.Fingerprint[:], storedFingerprint)
	if err := event.Validate(); err != nil {
		return domain.ClientTelemetryEvent{}, false, err
	}
	return event, true, nil
}
