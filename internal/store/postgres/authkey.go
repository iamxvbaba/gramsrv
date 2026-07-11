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

// AuthKeyStore 用 PostgreSQL 实现 store.AuthKeyStore。
type AuthKeyStore struct {
	q  *sqlcgen.Queries
	db sqlcgen.DBTX
}

// NewAuthKeyStore 基于 pgx 连接池（或事务）创建 AuthKeyStore。
func NewAuthKeyStore(db sqlcgen.DBTX) *AuthKeyStore {
	return &AuthKeyStore{q: sqlcgen.New(db), db: db}
}

// Save 实现 store.AuthKeyStore。auth_key_id 以小端解释为 int64 存入 BIGINT；
// created_at/last_used_at 交由 DB 默认值（now()），故传入的 CreatedAt 不落库。
func (s *AuthKeyStore) Save(ctx context.Context, k store.AuthKeyData) error {
	if _, err := s.db.Exec(ctx, `
INSERT INTO auth_keys (auth_key_id, body, server_salt)
VALUES ($1, $2, $3)
ON CONFLICT (auth_key_id) DO UPDATE
SET body = EXCLUDED.body, server_salt = EXCLUDED.server_salt, last_used_at = now()
`, authKeyIDToInt64(k.ID), k.Value[:], k.ServerSalt); err != nil {
		return fmt.Errorf("upsert auth key: %w", err)
	}
	return nil
}

// Get 实现 store.AuthKeyStore。不存在时 found=false。读取与 last_used_at touch 是同一条
// UPDATE ... RETURNING：若 orphan GC 已锁定并删除该行，Get 等待后得到 no rows；若 Get 先
// 完成，GC 的 cutoff/final predicate 会看到新水位并跳过。这样连接不会在“读到旧 key、尚未
// 注册进 SessionManager”的窗口被后台清理。
func (s *AuthKeyStore) Get(ctx context.Context, id [8]byte) (store.AuthKeyData, bool, error) {
	var (
		body          []byte
		serverSalt    int64
		createdAt     pgtype.Timestamptz
		layer         int
		deviceModel   string
		platform      string
		systemVersion string
		apiID         int
		appVersion    string
	)
	err := s.db.QueryRow(ctx, `
UPDATE auth_keys
SET last_used_at = now()
WHERE auth_key_id = $1
RETURNING auth_key_id, body, server_salt, created_at,
       layer, device_model, platform, system_version, api_id, app_version
`, authKeyIDToInt64(id)).Scan(new(int64), &body, &serverSalt, &createdAt, &layer, &deviceModel, &platform, &systemVersion, &apiID, &appVersion)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.AuthKeyData{}, false, nil
		}
		return store.AuthKeyData{}, false, fmt.Errorf("get auth key: %w", err)
	}
	if len(body) != len(store.AuthKeyData{}.Value) {
		return store.AuthKeyData{}, false, fmt.Errorf("auth key body length = %d, want 256", len(body))
	}
	data := store.AuthKeyData{
		ID:            id,
		ServerSalt:    serverSalt,
		Layer:         layer,
		DeviceModel:   deviceModel,
		Platform:      platform,
		SystemVersion: systemVersion,
		APIID:         apiID,
		AppVersion:    appVersion,
	}
	copy(data.Value[:], body)
	if createdAt.Valid {
		data.CreatedAt = createdAt.Time.Unix()
	}
	return data, true, nil
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
	if _, err := s.db.Exec(ctx, `
UPDATE auth_keys
SET layer = CASE WHEN $2::integer > 0 THEN $2 ELSE layer END,
    device_model = CASE WHEN $3::text <> '' THEN $3 ELSE device_model END,
    platform = CASE WHEN $4::text <> '' THEN $4 ELSE platform END,
    system_version = CASE WHEN $5::text <> '' THEN $5 ELSE system_version END,
    api_id = CASE WHEN $6::integer <> 0 THEN $6 ELSE api_id END,
    app_version = CASE WHEN $7::text <> '' THEN $7 ELSE app_version END
WHERE auth_key_id = $1
`, authKeyIDToInt64(id), info.Layer, info.DeviceModel, info.Platform, info.SystemVersion, info.APIID, info.AppVersion); err != nil {
		return fmt.Errorf("update auth key client info: %w", err)
	}
	return nil
}

// Delete 实现 store.AuthKeyStore。不存在时静默成功。
// 手写 SQL 而非 sqlc 生成：避免触碰 sqlcgen 再生成链路。
//
// 同时清理把本 key 当作 perm key 的 temp auth key 行：temp_auth_key_bindings.temp_auth_key_id
// 侧有外键 ON DELETE CASCADE，删除 temp key 会自动清绑定；perm_auth_key_id 列无外键，
// 因此被踢/登出删除 perm key 时必须先把关联 temp key 一并删掉。否则 Web/上传连接用
// raw temp key 重连时仍能进入 RPC 层，只得到 AUTH_KEY_UNREGISTERED，而不是连接层 404。
func (s *AuthKeyStore) Delete(ctx context.Context, id [8]byte) error {
	keyID := authKeyIDToInt64(id)
	var touched int
	if err := s.db.QueryRow(ctx, `
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
	WHERE auth_key_id IN (SELECT auth_key_id FROM doomed_keys)
	RETURNING auth_key_id
)
SELECT
	(SELECT count(*) FROM deleted_update_states)::int +
	(SELECT count(*) FROM deleted_temp)::int`, keyID).Scan(&touched); err != nil {
		return fmt.Errorf("delete auth key and temp bindings: %w", err)
	}
	return nil
}

// DeleteOrphaned 回收握手已落库、但从未形成 authorization/temp binding 且当前没有
// 活跃物理连接的旧 auth key。last_used_at 与 Get 的 UPDATE ... RETURNING 行锁配对，封住
// active-key 快照之后新连接开始使用旧 key 的竞态；所有引用条件仍在最终 DELETE 中复核。
// protected 必须是 SessionManager 的 raw key。
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
	err := s.db.QueryRow(ctx, `
WITH candidates AS MATERIALIZED (
  SELECT k.auth_key_id
  FROM auth_keys k
  WHERE k.last_used_at < now() - make_interval(secs => $1::double precision)
    AND NOT (k.auth_key_id = ANY($2::bigint[]))
    AND NOT EXISTS (
      SELECT 1 FROM authorizations a WHERE a.auth_key_id = k.auth_key_id
    )
    AND NOT EXISTS (
      SELECT 1
      FROM temp_auth_key_bindings b
      WHERE b.temp_auth_key_id = k.auth_key_id OR b.perm_auth_key_id = k.auth_key_id
    )
  ORDER BY k.last_used_at ASC, k.auth_key_id ASC
  LIMIT $3
  FOR UPDATE OF k SKIP LOCKED
), deleted_update_states AS (
  -- Historical authorization-only deletion could leave a cursor without an
  -- auth_keys FK. GC owns that stale row once the raw key is proven orphaned.
  DELETE FROM update_states s
  USING candidates c
  WHERE s.auth_key_id = c.auth_key_id
  RETURNING s.auth_key_id
), deleted_keys AS (
  DELETE FROM auth_keys k
  USING candidates c
  WHERE k.auth_key_id = c.auth_key_id
    AND k.last_used_at < now() - make_interval(secs => $1::double precision)
    AND NOT (k.auth_key_id = ANY($2::bigint[]))
    AND NOT EXISTS (
      SELECT 1 FROM authorizations a WHERE a.auth_key_id = k.auth_key_id
    )
    AND NOT EXISTS (
      SELECT 1
      FROM temp_auth_key_bindings b
      WHERE b.temp_auth_key_id = k.auth_key_id OR b.perm_auth_key_id = k.auth_key_id
    )
  RETURNING k.auth_key_id
)
SELECT count(*)::int
FROM deleted_keys
CROSS JOIN LATERAL (SELECT count(*) FROM deleted_update_states) AS touched`, olderThan.Seconds(), protectedIDs, limit).Scan(&deleted)
	if err != nil {
		return 0, fmt.Errorf("delete orphaned auth keys: %w", err)
	}
	return deleted, nil
}

// authKeyIDToInt64 把 [8]byte 的 auth_key_id 按小端解释为 int64（MTProto 定义即 SHA1 低 64 位）。
func authKeyIDToInt64(id [8]byte) int64 {
	return int64(binary.LittleEndian.Uint64(id[:]))
}

func authKeyIDFromInt64(v int64) [8]byte {
	var id [8]byte
	binary.LittleEndian.PutUint64(id[:], uint64(v))
	return id
}
