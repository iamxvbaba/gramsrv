package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/store"
)

// testPool 连接 TELESRV_TEST_POSTGRES_DSN 指向的库（迁移到最新），未设则跳过。
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TELESRV_TEST_POSTGRES_DSN: %v", err)
	}
	if !strings.Contains(strings.ToLower(parsed.ConnConfig.Database), "test") {
		t.Fatalf("TELESRV_TEST_POSTGRES_DSN must name a dedicated test database, got %q", parsed.ConnConfig.Database)
	}
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestAuthKeyStoreRoundTrip 验证 auth_key 落 PG 后，用全新 store 实例（模拟进程重启、无内存缓存）能原样读回。
// 这是「server 重启保住 auth_key」的直接证明。
func TestAuthKeyStoreRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var id [8]byte
	var val [256]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(val[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(id))
	})

	want := store.AuthKeyData{
		ID:         id,
		Value:      val,
		ServerSalt: 0x0badf00d,
		ExpiresAt:  1_799_999_999,
	}
	if err := NewAuthKeyStore(pool).Save(ctx, want); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, found, err := NewAuthKeyStore(pool).Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("auth key not found after save (重启后丢失)")
	}
	if got.ID != want.ID || got.Value != want.Value || got.ServerSalt != want.ServerSalt || got.ExpiresAt != want.ExpiresAt {
		t.Fatalf("round trip mismatch: got salt=%#x expires_at=%d value[:4]=%x, want salt=%#x expires_at=%d value[:4]=%x",
			got.ServerSalt, got.ExpiresAt, got.Value[:4], want.ServerSalt, want.ExpiresAt, want.Value[:4])
	}
	conflicting := want
	conflicting.ExpiresAt++
	if err := NewAuthKeyStore(pool).Save(ctx, conflicting); !errors.Is(err, store.ErrAuthKeyProtocolMetadataConflict) {
		t.Fatalf("reclassify auth key error = %v, want %v", err, store.ErrAuthKeyProtocolMetadataConflict)
	}
	got, found, err = NewAuthKeyStore(pool).Get(ctx, id)
	if err != nil || !found || got.ExpiresAt != want.ExpiresAt {
		t.Fatalf("auth key expiry changed after rejected reclassification: got=%d found=%v err=%v", got.ExpiresAt, found, err)
	}

	var missing [8]byte
	missing[0] = id[0] ^ 0xff
	if _, found, err := NewAuthKeyStore(pool).Get(ctx, missing); err != nil || found {
		t.Fatalf("missing key: found=%v err=%v, want found=false err=nil", found, err)
	}
}

func TestAuthKeyStoreClientInfoRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var id [8]byte
	var val [256]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(val[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM auth_keys WHERE auth_key_id = $1", authKeyIDToInt64(id))
	})

	keys := NewAuthKeyStore(pool)
	if err := keys.Save(ctx, store.AuthKeyData{ID: id, Value: val, ServerSalt: 0x0badf00d}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer:         227,
		DeviceModel:   "GooglePixel 9a",
		Platform:      "android",
		SystemVersion: "SDK 36",
		APIID:         6,
		AppVersion:    "12.8.1 (69169) pbeta",
	}); err != nil {
		t.Fatalf("update client info: %v", err)
	}

	got, found, err := NewAuthKeyStore(pool).Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !found {
		t.Fatal("auth key not found after client info update")
	}
	if got.Layer != 227 || got.DeviceModel != "GooglePixel 9a" || got.Platform != "android" ||
		got.SystemVersion != "SDK 36" || got.APIID != 6 || got.AppVersion != "12.8.1 (69169) pbeta" {
		t.Fatalf("client info mismatch: %+v", got)
	}

	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{AppVersion: "12.8.2"}); err != nil {
		t.Fatalf("partial update client info: %v", err)
	}
	got, found, err = NewAuthKeyStore(pool).Get(ctx, id)
	if err != nil {
		t.Fatalf("get after partial update: %v", err)
	}
	if !found {
		t.Fatal("auth key not found after partial client info update")
	}
	if got.Layer != 227 || got.DeviceModel != "GooglePixel 9a" || got.Platform != "android" ||
		got.SystemVersion != "SDK 36" || got.APIID != 6 || got.AppVersion != "12.8.2" {
		t.Fatalf("partial client info merge mismatch: %+v", got)
	}
}

func TestAuthKeyStoreUpdateClientInfoProtectsObservedLayerPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)

	var id [8]byte
	var value [256]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keys.Delete(ctx, id) })
	if err := keys.Save(ctx, store.AuthKeyData{ID: id, Value: value}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE auth_keys
SET layer = 227, layer_observation_id = 91,
    device_model = 'before', platform = 'tdesktop'
WHERE auth_key_id = $1`, authKeyIDToInt64(id)); err != nil {
		t.Fatalf("seed ordered layer: %v", err)
	}

	err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer: 220, DeviceModel: "must-not-merge", AppVersion: "must-not-merge",
	})
	if !errors.Is(err, store.ErrAuthKeySessionLayerConflict) {
		t.Fatalf("conflicting layer update error = %v, want %v", err, store.ErrAuthKeySessionLayerConflict)
	}
	got, found, err := keys.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("get after conflict: found=%v err=%v", found, err)
	}
	if got.Layer != 227 || got.LayerObservationID != 91 || got.DeviceModel != "before" ||
		got.Platform != "tdesktop" || got.AppVersion != "" {
		t.Fatalf("conflicting update changed row: %+v", got)
	}

	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Layer: 227, DeviceModel: "same-layer", AppVersion: "1.0",
	}); err != nil {
		t.Fatalf("same observed layer metadata merge: %v", err)
	}
	if err := keys.UpdateClientInfo(ctx, id, store.AuthKeyClientInfo{
		Platform: "windows", SystemVersion: "11",
	}); err != nil {
		t.Fatalf("layerless metadata merge: %v", err)
	}
	got, found, err = keys.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("get guarded metadata merge: found=%v err=%v", found, err)
	}
	if got.Layer != 227 || got.LayerObservationID != 91 || got.DeviceModel != "same-layer" ||
		got.Platform != "windows" || got.SystemVersion != "11" || got.AppVersion != "1.0" {
		t.Fatalf("guarded metadata merge = %+v", got)
	}

	missing := id
	missing[0] ^= 0xff
	if err := keys.UpdateClientInfo(ctx, missing, store.AuthKeyClientInfo{Layer: 227}); !errors.Is(err, store.ErrAuthKeyNotFound) {
		t.Fatalf("missing primary update error = %v, want %v", err, store.ErrAuthKeyNotFound)
	}
}
