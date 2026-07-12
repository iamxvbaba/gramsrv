package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAuthorizationStoreRevokeByHashDeletesProtocolKeyCascadePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "hash")
	perm := revokeTestAuthKeyID(0x91)
	temp := revokeTestAuthKeyID(0x92)
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)

	saveRevokeTestAuthKey(t, ctx, keys, perm)
	saveRevokeTestAuthKey(t, ctx, keys, temp)
	if err := auths.Bind(ctx, domain.Authorization{
		AuthKeyID:       perm,
		UserID:          userID,
		Hash:            9001,
		Layer:           225,
		DeviceModel:     "WebA",
		Platform:        "web",
		SystemVersion:   "test",
		APIID:           100,
		AppVersion:      "test",
		IP:              "127.0.0.1",
		PasswordPending: true,
	}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}
	if err := NewUpdateStateStore(pool).Save(ctx, perm, userID, domain.UpdateState{Pts: 11, Date: int(time.Now().Unix())}); err != nil {
		t.Fatalf("save update state: %v", err)
	}
	if err := NewTempAuthKeyBindingStore(pool).Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID:    temp,
		PermAuthKeyID:    authKeyIDToInt64(perm),
		Nonce:            1,
		TempSessionID:    2,
		ExpiresAt:        int(time.Now().Add(time.Hour).Unix()),
		EncryptedMessage: []byte{1, 2, 3, 4},
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}

	deleted, found, err := auths.RevokeByHash(ctx, userID, 9001)
	if err != nil || !found {
		t.Fatalf("RevokeByHash found=%v err=%v, want found", found, err)
	}
	if deleted.AuthKeyID != perm || !deleted.PasswordPending {
		t.Fatalf("deleted authorization = %+v, want perm key and password_pending", deleted)
	}
	assertRevokeTestMissingAuthKey(t, ctx, keys, perm)
	assertRevokeTestMissingAuthKey(t, ctx, keys, temp)
	assertRevokeTestNoAuthorization(t, ctx, auths, perm)
	assertRevokeTestTableCount(t, ctx, pool, "update_states", "auth_key_id", authKeyIDToInt64(perm), 0)
	assertRevokeTestTableCount(t, ctx, pool, "temp_auth_key_bindings", "temp_auth_key_id", authKeyIDToInt64(temp), 0)
}

func TestAuthorizationStoreRevokeByUserExceptDeletesOnlyRevokedKeysPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "bulk")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	keep := revokeTestAuthKeyID(0xa1)
	revokedOne := revokeTestAuthKeyID(0xa2)
	revokedTwo := revokeTestAuthKeyID(0xa3)
	tempForTwo := revokeTestAuthKeyID(0xa4)

	for i, key := range [][8]byte{keep, revokedOne, revokedTwo, tempForTwo} {
		saveRevokeTestAuthKey(t, ctx, keys, key)
		if i < 3 {
			if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: key, UserID: userID, Hash: int64(9100 + i)}); err != nil {
				t.Fatalf("bind auth %x: %v", key, err)
			}
		}
	}
	if err := NewTempAuthKeyBindingStore(pool).Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempForTwo,
		PermAuthKeyID:    authKeyIDToInt64(revokedTwo),
		Nonce:            3,
		TempSessionID:    4,
		ExpiresAt:        int(time.Now().Add(time.Hour).Unix()),
		EncryptedMessage: []byte{5, 6, 7, 8},
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}

	deleted, err := auths.RevokeByUserExcept(ctx, userID, keep)
	if err != nil {
		t.Fatalf("RevokeByUserExcept: %v", err)
	}
	if len(deleted) != 2 {
		t.Fatalf("deleted len = %d, want 2 (%+v)", len(deleted), deleted)
	}
	assertRevokeTestPresentAuthKey(t, ctx, keys, keep)
	assertRevokeTestPresentAuthorization(t, ctx, auths, keep)
	assertRevokeTestMissingAuthKey(t, ctx, keys, revokedOne)
	assertRevokeTestMissingAuthKey(t, ctx, keys, revokedTwo)
	assertRevokeTestMissingAuthKey(t, ctx, keys, tempForTwo)
}

func TestAuthorizationStoreUpdateClientInfoMergesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "client-info")
	id := revokeTestAuthKeyID(0xb1)
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	saveRevokeTestAuthKey(t, ctx, keys, id)
	if err := auths.Bind(ctx, domain.Authorization{
		AuthKeyID:   id,
		UserID:      userID,
		Hash:        9201,
		Platform:    "unknown",
		DeviceModel: "legacy",
		IP:          "127.0.0.1",
	}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}
	if err := auths.UpdateClientInfo(ctx, id, domain.AuthKeyClientInfo{
		Layer:         227,
		DeviceModel:   "iPhone Simulator",
		Platform:      "ios",
		SystemVersion: "26.5",
		APIID:         1,
		AppVersion:    "12.8 (10000)",
	}); err != nil {
		t.Fatalf("update client info: %v", err)
	}
	// Empty/zero values are a partial update and must not erase strong metadata.
	if err := auths.UpdateClientInfo(ctx, id, domain.AuthKeyClientInfo{AppVersion: "12.8.1"}); err != nil {
		t.Fatalf("partial update client info: %v", err)
	}

	got, found, err := auths.ByAuthKey(ctx, id)
	if err != nil || !found {
		t.Fatalf("get authorization: found=%v err=%v", found, err)
	}
	if got.Layer != 227 || got.DeviceModel != "iPhone Simulator" || got.Platform != "ios" ||
		got.SystemVersion != "26.5" || got.APIID != 1 || got.AppVersion != "12.8.1" {
		t.Fatalf("merged client info = %+v", got)
	}
}

func createRevokeTestUser(t *testing.T, ctx context.Context, db *pgxpool.Pool, suffix string) int64 {
	t.Helper()
	phone := fmt.Sprintf("+1555%09d", time.Now().UnixNano()%1_000_000_000)
	var userID int64
	if err := db.QueryRow(ctx, `
INSERT INTO users (access_hash, phone, first_name)
VALUES ($1, $2, $3)
RETURNING id`, time.Now().UnixNano(), phone, "revoke-"+suffix).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})
	return userID
}

func revokeTestAuthKeyID(seed byte) [8]byte {
	return [8]byte{seed, seed, seed, seed, seed, seed, seed, seed}
}

func saveRevokeTestAuthKey(t *testing.T, ctx context.Context, keys store.AuthKeyStore, id [8]byte) {
	t.Helper()
	if err := keys.Save(ctx, store.AuthKeyData{ID: id, ServerSalt: int64(id[0])}); err != nil {
		t.Fatalf("save auth key %x: %v", id, err)
	}
	t.Cleanup(func() {
		_ = keys.Delete(ctx, id)
	})
}

func assertRevokeTestMissingAuthKey(t *testing.T, ctx context.Context, keys store.AuthKeyStore, id [8]byte) {
	t.Helper()
	if _, found, err := keys.Get(ctx, id); err != nil || found {
		t.Fatalf("auth key %x found=%v err=%v, want missing", id, found, err)
	}
}

func assertRevokeTestPresentAuthKey(t *testing.T, ctx context.Context, keys store.AuthKeyStore, id [8]byte) {
	t.Helper()
	if _, found, err := keys.Get(ctx, id); err != nil || !found {
		t.Fatalf("auth key %x found=%v err=%v, want present", id, found, err)
	}
}

func assertRevokeTestNoAuthorization(t *testing.T, ctx context.Context, auths *AuthorizationStore, id [8]byte) {
	t.Helper()
	if _, found, err := auths.ByAuthKey(ctx, id); err != nil || found {
		t.Fatalf("authorization %x found=%v err=%v, want missing", id, found, err)
	}
}

func assertRevokeTestPresentAuthorization(t *testing.T, ctx context.Context, auths *AuthorizationStore, id [8]byte) {
	t.Helper()
	if _, found, err := auths.ByAuthKey(ctx, id); err != nil || !found {
		t.Fatalf("authorization %x found=%v err=%v, want present", id, found, err)
	}
}

func assertRevokeTestTableCount(t *testing.T, ctx context.Context, db *pgxpool.Pool, table, column string, value int64, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE %s = $1", table, column), value).Scan(&got); err != nil {
		t.Fatalf("count %s.%s: %v", table, column, err)
	}
	if got != want {
		t.Fatalf("count %s.%s = %d, want %d", table, column, got, want)
	}
}
