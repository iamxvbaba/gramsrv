package postgres

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

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
// created_at 交由 DB 默认值（now()），故传入的 CreatedAt 不落库。
func (s *AuthKeyStore) Save(ctx context.Context, k store.AuthKeyData) error {
	if _, err := s.db.Exec(ctx, `
INSERT INTO auth_keys (auth_key_id, body, server_salt)
VALUES ($1, $2, $3)
ON CONFLICT (auth_key_id) DO UPDATE
SET body = EXCLUDED.body, server_salt = EXCLUDED.server_salt
`, authKeyIDToInt64(k.ID), k.Value[:], k.ServerSalt); err != nil {
		return fmt.Errorf("upsert auth key: %w", err)
	}
	return nil
}

// Get 实现 store.AuthKeyStore。不存在时 found=false。
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
SELECT auth_key_id, body, server_salt, created_at,
       layer, device_model, platform, system_version, api_id, app_version
FROM auth_keys
WHERE auth_key_id = $1
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
	if _, err := s.db.Exec(ctx, `
WITH doomed_temp AS (
	SELECT temp_auth_key_id
	FROM temp_auth_key_bindings
	WHERE perm_auth_key_id = $1
), deleted_temp AS (
	DELETE FROM auth_keys
	WHERE auth_key_id IN (SELECT temp_auth_key_id FROM doomed_temp)
)
DELETE FROM auth_keys
WHERE auth_key_id = $1
`, keyID); err != nil {
		return fmt.Errorf("delete auth key and temp bindings: %w", err)
	}
	return nil
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
