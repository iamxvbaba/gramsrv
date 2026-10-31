package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

// AuthKeyStore implements store.AuthKeyStore with PostgreSQL.
type AuthKeyStore struct {
	q  *sqlcgen.Queries
	db sqlcgen.DBTX
}

// NewAuthKeyStore creates an AuthKeyStore backed by a pgx pool (or transaction).
func NewAuthKeyStore(db sqlcgen.DBTX) *AuthKeyStore {
	return &AuthKeyStore{q: sqlcgen.New(db), db: db}
}

// Save implements store.AuthKeyStore. auth_key_id is interpreted little-endian as int64 and stored in BIGINT;
// created_at/last_used_at are left to the DB defaults (now()), so the passed CreatedAt is not persisted.
func (s *AuthKeyStore) Save(ctx context.Context, k store.AuthKeyData) error {
	if !store.ValidNewAuthKeyProtocolExpiry(k.ExpiresAt) {
		return store.ErrInvalidAuthKeyProtocolExpiry
	}
	tag, err := s.db.Exec(ctx, `
INSERT INTO auth_keys (auth_key_id, body, server_salt, expires_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (auth_key_id) DO UPDATE
SET server_salt = EXCLUDED.server_salt,
    last_used_at = now()
WHERE auth_keys.body = EXCLUDED.body
  AND auth_keys.expires_at = EXCLUDED.expires_at
`, authKeyIDToInt64(k.ID), k.Value[:], k.ServerSalt, k.ExpiresAt)
	if err != nil {
		return fmt.Errorf("upsert auth key: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return store.ErrAuthKeyProtocolMetadataConflict
	}
	return nil
}

// Get implements store.AuthKeyStore. found=false when absent. The read and the last_used_at touch are the same
// UPDATE ... RETURNING: if orphan GC has already locked and deleted the row, Get waits and gets no rows; if Get
// finishes first, the GC's cutoff/final predicate sees the new watermark and skips. This way a connection is not
// cleaned up in the background during the window of "read an old key but not yet registered into SessionManager".
func (s *AuthKeyStore) Get(ctx context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	data, err := scanAuthKeyData(s.db.QueryRow(ctx, `
UPDATE auth_keys
SET last_used_at = now()
WHERE auth_key_id = $1
RETURNING auth_key_id, body, server_salt, created_at,
       expires_at, layer, layer_observation_id,
       device_model, platform, system_version, api_id, app_version
`, authKeyIDToInt64(id)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.AuthKeyData{}, false, nil
		}
		return store.AuthKeyData{}, false, fmt.Errorf("get auth key: %w", err)
	}
	if data.ID != id {
		return store.AuthKeyData{}, false, fmt.Errorf("get auth key returned id %x, want %x", data.ID, id)
	}
	return data, true, nil
}

// Revalidate reads the immutable key/protocol tuple after an activation claim
// is visible. It deliberately does not touch last_used_at: the physical
// connection's initial Get already established the orphan lease and the claim
// is now the local delete/revoke serialization boundary.
func (s *AuthKeyStore) Revalidate(ctx context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	data, err := scanAuthKeyData(s.db.QueryRow(ctx, `
SELECT auth_key_id, body, server_salt, created_at,
       expires_at, layer, layer_observation_id,
       device_model, platform, system_version, api_id, app_version
FROM auth_keys
WHERE auth_key_id = $1
`, authKeyIDToInt64(id)))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.AuthKeyData{}, false, nil
		}
		return store.AuthKeyData{}, false, fmt.Errorf("revalidate auth key: %w", err)
	}
	if data.ID != id {
		return store.AuthKeyData{}, false, fmt.Errorf("revalidate auth key returned id %x, want %x", data.ID, id)
	}
	return data, true, nil
}

// LoadBindingKeys touches and returns both cryptographic proof keys in one
// statement. Missing rows remain explicit in the result so the application can
// preserve its temp-rotation versus invalid-encrypted-proof error split.
func (s *AuthKeyStore) LoadBindingKeys(ctx context.Context, tempID, permID [8]byte) (store.AuthKeyBindingKeys, error) {
	rows, err := s.db.Query(ctx, `
UPDATE auth_keys
SET last_used_at = now()
WHERE auth_key_id = ANY($1::bigint[])
RETURNING auth_key_id, body, server_salt, created_at,
       expires_at, layer, layer_observation_id,
       device_model, platform, system_version, api_id, app_version
`, []int64{authKeyIDToInt64(tempID), authKeyIDToInt64(permID)})
	if err != nil {
		return store.AuthKeyBindingKeys{}, fmt.Errorf("load auth key binding pair: %w", err)
	}
	defer rows.Close()
	var result store.AuthKeyBindingKeys
	for rows.Next() {
		data, scanErr := scanAuthKeyData(rows)
		if scanErr != nil {
			return store.AuthKeyBindingKeys{}, fmt.Errorf("scan auth key binding pair: %w", scanErr)
		}
		switch data.ID {
		case tempID:
			result.Temporary = data
			result.TemporaryFound = true
		case permID:
			result.Permanent = data
			result.PermanentFound = true
		default:
			return store.AuthKeyBindingKeys{}, fmt.Errorf("load auth key binding pair returned unexpected id %x", data.ID)
		}
	}
	if err := rows.Err(); err != nil {
		return store.AuthKeyBindingKeys{}, fmt.Errorf("iterate auth key binding pair: %w", err)
	}
	if tempID == permID && result.TemporaryFound {
		result.Permanent = result.Temporary
		result.PermanentFound = true
	}
	return result, nil
}

type authKeyDataScanner interface {
	Scan(dest ...any) error
}

func scanAuthKeyData(row authKeyDataScanner) (store.AuthKeyData, error) {
	var (
		storedID           int64
		body               []byte
		serverSalt         int64
		createdAt          pgtype.Timestamptz
		expiresAt          int
		layer              int
		layerObservationID int64
		deviceModel        string
		platform           string
		systemVersion      string
		apiID              int
		appVersion         string
	)
	if err := row.Scan(
		&storedID, &body, &serverSalt, &createdAt,
		&expiresAt, &layer, &layerObservationID,
		&deviceModel, &platform, &systemVersion, &apiID, &appVersion,
	); err != nil {
		return store.AuthKeyData{}, err
	}
	if len(body) != len(store.AuthKeyData{}.Value) {
		return store.AuthKeyData{}, fmt.Errorf("auth key body length = %d, want 256", len(body))
	}
	data := store.AuthKeyData{
		ID:                 authKeyIDFromInt64(storedID),
		ServerSalt:         serverSalt,
		ExpiresAt:          expiresAt,
		Layer:              layer,
		LayerObservationID: layerObservationID,
		DeviceModel:        deviceModel,
		Platform:           platform,
		SystemVersion:      systemVersion,
		APIID:              apiID,
		AppVersion:         appVersion,
	}
	copy(data.Value[:], body)
	if createdAt.Valid {
		data.CreatedAt = createdAt.Time.Unix()
	}
	return data, nil
}

const activeAuthKeyHeartbeatBatch = 4096

// TouchActiveRawAuthKeys refreshes the durable activity lease for raw auth keys currently held by
// this server instance. Orphan collection is database-global while SessionManager is process-local;
// without this heartbeat, instance A can collect a long-lived unauthorised key that is active on
// instance B after its one-time Get touch ages past the retention cutoff.
//
// The caller runs this well inside the orphan-retention window and skips collection if a heartbeat
// fails. Batching keeps the ANY array and one UPDATE bounded at large connection counts.
func (s *AuthKeyStore) TouchActiveRawAuthKeys(ctx context.Context, ids [][8]byte) error {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	keyIDs := make([]int64, 0, len(ids))
	for _, id := range ids {
		keyID := authKeyIDToInt64(id)
		if _, duplicate := seen[keyID]; duplicate {
			continue
		}
		seen[keyID] = struct{}{}
		keyIDs = append(keyIDs, keyID)
	}
	for start := 0; start < len(keyIDs); start += activeAuthKeyHeartbeatBatch {
		end := start + activeAuthKeyHeartbeatBatch
		if end > len(keyIDs) {
			end = len(keyIDs)
		}
		batch := keyIDs[start:end]
		tag, err := s.db.Exec(ctx, `
UPDATE auth_keys
SET last_used_at = now()
WHERE auth_key_id = ANY($1::bigint[])`, batch)
		if err != nil {
			return fmt.Errorf("touch active raw auth keys: %w", err)
		}
		if tag.RowsAffected() != int64(len(batch)) {
			return fmt.Errorf(
				"touch active raw auth keys: refreshed %d of %d keys",
				tag.RowsAffected(), len(batch),
			)
		}
	}
	return nil
}

func (s *AuthKeyStore) UpdateClientInfo(ctx context.Context, id [8]byte, info store.AuthKeyClientInfo) error {
	var updated int
	err := s.db.QueryRow(ctx, `
UPDATE auth_keys
SET layer = CASE WHEN $2::integer > 0 THEN $2 ELSE layer END,
    device_model = CASE WHEN $3::text <> '' THEN $3 ELSE device_model END,
    platform = CASE WHEN $4::text <> '' THEN $4 ELSE platform END,
    system_version = CASE WHEN $5::text <> '' THEN $5 ELSE system_version END,
    api_id = CASE WHEN $6::integer <> 0 THEN $6 ELSE api_id END,
    app_version = CASE WHEN $7::text <> '' THEN $7 ELSE app_version END
WHERE auth_key_id = $1
  AND ($2::integer <= 0 OR layer_observation_id = 0 OR layer = $2::integer)
RETURNING 1
`, authKeyIDToInt64(id), info.Layer, info.DeviceModel, info.Platform, info.SystemVersion, info.APIID, info.AppVersion).Scan(&updated)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var (
				currentLayer int
				observation  int64
			)
			lookupErr := s.db.QueryRow(ctx, `
SELECT layer, layer_observation_id
FROM auth_keys
WHERE auth_key_id = $1
`, authKeyIDToInt64(id)).Scan(&currentLayer, &observation)
			switch {
			case errors.Is(lookupErr, pgx.ErrNoRows):
				return store.ErrAuthKeyNotFound
			case lookupErr != nil:
				return fmt.Errorf("classify auth key client info update: %w", lookupErr)
			case info.Layer > 0 && observation > 0 && currentLayer != info.Layer:
				return store.ErrAuthKeySessionLayerConflict
			default:
				return fmt.Errorf("update auth key client info: guarded update affected no row")
			}
		}
		return fmt.Errorf("update auth key client info: %w", err)
	}
	return nil
}

// Delete implements store.AuthKeyStore. Succeeds silently when absent.
// Hand-written SQL rather than sqlc-generated: avoids touching the sqlcgen regeneration chain.
//
// It also cleans up temp auth key rows that use this key as their perm key: the
// temp_auth_key_bindings.temp_auth_key_id side has an ON DELETE CASCADE foreign key, so
// deleting the temp key clears the binding automatically; the perm_auth_key_id side is
// protected from dangling by a RESTRICT FK, so explicitly destroying a perm key must delete
// the associated temp keys first. Remote authorization revocation must not call this method:
// a kicked client must keep its protocol key and, after reconnecting into the RPC layer,
// receive AUTH_KEY_UNREGISTERED rather than only a transport-level -404.
func (s *AuthKeyStore) Delete(ctx context.Context, id [8]byte) error {
	return withAuthIdentityTx(ctx, s.db, "delete auth key", func(tx pgx.Tx) error {
		return deleteAuthKeyTx(ctx, tx, authKeyIDToInt64(id))
	})
}

func deleteAuthKeyTx(ctx context.Context, tx pgx.Tx, keyID int64) error {
	if _, _, _, err := lockRawAuthKeyInIdentityOrder(ctx, tx, keyID); err != nil {
		if errors.Is(err, store.ErrAuthKeyNotFound) {
			return nil
		}
		return err
	}
	var touched int
	if err := tx.QueryRow(ctx, `
WITH doomed_temp AS MATERIALIZED (
	SELECT temp_auth_key_id
	FROM temp_auth_key_bindings
	WHERE perm_auth_key_id = $1
), doomed_keys AS MATERIALIZED (
	SELECT $1::bigint AS auth_key_id
	UNION
	SELECT temp_auth_key_id FROM doomed_temp
), deleted_update_states AS (
	-- update_states intentionally has no auth_keys FK: remove device cursors in the
	-- same statement/transaction as both the permanent and derived temp keys.
	DELETE FROM update_states
	WHERE auth_key_id IN (SELECT auth_key_id FROM doomed_keys)
	RETURNING auth_key_id
), deleted_temp AS (
	DELETE FROM auth_keys
	WHERE auth_key_id IN (SELECT temp_auth_key_id FROM doomed_temp)
	RETURNING auth_key_id
), deleted_key AS (
	DELETE FROM auth_keys
	WHERE auth_key_id = $1
	  AND (SELECT count(*) FROM deleted_temp) >= 0
	RETURNING auth_key_id
)
SELECT
	(SELECT count(*) FROM deleted_update_states)::int +
	(SELECT count(*) FROM deleted_temp)::int +
	(SELECT count(*) FROM deleted_key)::int`, keyID).Scan(&touched); err != nil {
		return fmt.Errorf("delete auth key and temp bindings: %w", err)
	}
	return nil
}

const tempAuthKeyPermFKConstraint = "temp_auth_key_bindings_perm_auth_key_id_fkey"

// DeleteOrphaned reclaims old auth keys whose handshake was persisted but that never
// formed an authorization/temp binding and currently have no active physical connection.
// last_used_at pairs with the row lock of Get's UPDATE ... RETURNING, closing the race where
// a new connection starts using an old key after the active-key snapshot; all reference
// conditions are re-checked in the final DELETE. protected must be the SessionManager's raw key.
func (s *AuthKeyStore) DeleteOrphaned(ctx context.Context, olderThan time.Duration, limit int, protected [][8]byte) (int, error) {
	if olderThan <= 0 || limit <= 0 {
		return 0, nil
	}
	if limit > 100000 {
		limit = 100000
	}
	protectedIDs := make([]int64, 0, len(protected))
	for _, id := range protected {
		protectedIDs = append(protectedIDs, authKeyIDToInt64(id))
	}
	var deleted int
	err := withAuthIdentityTx(ctx, s.db, "delete orphaned auth keys", func(tx pgx.Tx) error {
		var err error
		deleted, err = deleteOrphanedAuthKeysTx(ctx, tx, olderThan, limit, protectedIDs)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete orphaned auth keys: %w", err)
	}
	return deleted, nil
}

func deleteOrphanedAuthKeysTx(
	ctx context.Context,
	tx pgx.Tx,
	olderThan time.Duration,
	limit int,
	protectedIDs []int64,
) (int, error) {
	// Phase 1 is only a bounded hint. It must not lock rows before the complete
	// permanent-identity advisory set has been derived and acquired.
	rows, err := tx.Query(ctx, `
/* orphan_identity_candidates */
SELECT k.auth_key_id, k.expires_at
FROM auth_keys AS k
WHERE k.last_used_at < now() - make_interval(secs => $1::double precision)
  AND NOT (k.auth_key_id = ANY($2::bigint[]))
  AND NOT EXISTS (
    SELECT 1 FROM authorizations AS a WHERE a.auth_key_id = k.auth_key_id
  )
  AND NOT EXISTS (
    SELECT 1
    FROM temp_auth_key_bindings AS b
    WHERE b.temp_auth_key_id = k.auth_key_id OR b.perm_auth_key_id = k.auth_key_id
  )
ORDER BY k.last_used_at, k.auth_key_id
LIMIT $3`, olderThan.Seconds(), protectedIDs, limit)
	if err != nil {
		return 0, fmt.Errorf("select orphan auth key candidates: %w", err)
	}
	candidates := make([]int64, 0, limit)
	permanentCandidates := make([]int64, 0, limit)
	for rows.Next() {
		var (
			id        int64
			expiresAt int
		)
		if err := rows.Scan(&id, &expiresAt); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan orphan auth key candidate: %w", err)
		}
		candidates = append(candidates, id)
		if expiresAt == 0 {
			permanentCandidates = append(permanentCandidates, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate orphan auth key candidates: %w", err)
	}
	rows.Close()
	if len(candidates) == 0 {
		return 0, nil
	}
	if err := lockPermanentAuthIdentities(ctx, tx, permanentCandidates); err != nil {
		return 0, err
	}

	// Phase 2 locks only the hinted raw rows, in real-ID order, after every P
	// gate is held. A temp bind already holding its raw row is skipped; if it
	// committed immediately before this lock, phase 3's new READ COMMITTED
	// statement sees the binding and excludes it.
	lockRows, err := tx.Query(ctx, `
SELECT auth_key_id
FROM auth_keys
WHERE auth_key_id = ANY($1::bigint[])
ORDER BY auth_key_id
FOR UPDATE SKIP LOCKED`, candidates)
	if err != nil {
		return 0, fmt.Errorf("lock orphan auth key candidates: %w", err)
	}
	lockedIDs := make([]int64, 0, len(candidates))
	for lockRows.Next() {
		var id int64
		if err := lockRows.Scan(&id); err != nil {
			lockRows.Close()
			return 0, fmt.Errorf("scan locked orphan auth key: %w", err)
		}
		lockedIDs = append(lockedIDs, id)
	}
	if err := lockRows.Err(); err != nil {
		lockRows.Close()
		return 0, fmt.Errorf("iterate locked orphan auth keys: %w", err)
	}
	lockRows.Close()
	if len(lockedIDs) == 0 {
		return 0, nil
	}

	// Phase 3 is a separate statement snapshot and repeats every ownership,
	// activity and protection predicate. Never delete from the phase-1 hint.
	var deleted int
	err = tx.QueryRow(ctx, `
WITH still_orphaned AS MATERIALIZED (
  SELECT k.auth_key_id
  FROM auth_keys AS k
  WHERE k.auth_key_id = ANY($3::bigint[])
    AND k.last_used_at < now() - make_interval(secs => $1::double precision)
    AND NOT (k.auth_key_id = ANY($2::bigint[]))
    AND NOT EXISTS (
      SELECT 1 FROM authorizations AS a WHERE a.auth_key_id = k.auth_key_id
    )
    AND NOT EXISTS (
      SELECT 1
      FROM temp_auth_key_bindings AS b
      WHERE b.temp_auth_key_id = k.auth_key_id OR b.perm_auth_key_id = k.auth_key_id
    )
), deleted_update_states AS (
  DELETE FROM update_states AS state
  USING still_orphaned AS orphan
  WHERE state.auth_key_id = orphan.auth_key_id
  RETURNING state.auth_key_id
), deleted_keys AS (
  DELETE FROM auth_keys AS key
  USING still_orphaned AS orphan
  WHERE key.auth_key_id = orphan.auth_key_id
  RETURNING key.auth_key_id
)
SELECT count(*)::integer
FROM deleted_keys
CROSS JOIN LATERAL (SELECT count(*) FROM deleted_update_states) AS touched`,
		olderThan.Seconds(), protectedIDs, lockedIDs,
	).Scan(&deleted)
	if err != nil {
		return 0, fmt.Errorf("delete revalidated orphan auth keys: %w", err)
	}
	return deleted, nil
}

// authKeyIDToInt64 interprets the [8]byte auth_key_id as a little-endian int64 (per the MTProto definition, the low 64 bits of SHA1).
func authKeyIDToInt64(id [8]byte) int64 {
	return int64(binary.LittleEndian.Uint64(id[:]))
}

func authKeyIDFromInt64(v int64) [8]byte {
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], uint64(v))
	return id
}
