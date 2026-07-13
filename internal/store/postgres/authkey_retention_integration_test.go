package postgres

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAuthKeyStoreDeleteOrphanedIsBoundedAndProtectsReferencesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	userID := createRevokeTestUser(t, ctx, pool, "orphan-auth-key")

	newKey := func(expiresAt int) [8]byte {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			t.Fatalf("random auth key id: %v", err)
		}
		if err := keys.Save(ctx, store.AuthKeyData{ID: id, ExpiresAt: expiresAt}); err != nil {
			t.Fatalf("save auth key %x: %v", id, err)
		}
		t.Cleanup(func() { _ = keys.Delete(ctx, id) })
		return id
	}
	tempExpiry := int(time.Now().Add(time.Hour).Unix())
	orphanOne, orphanTwo := newKey(0), newKey(0)
	recent := newKey(0)
	authorized := newKey(0)
	temp, perm := newKey(tempExpiry), newKey(0)
	active := newKey(0)
	if _, err := pool.Exec(ctx, `
INSERT INTO update_states (auth_key_id, user_id, pts, observed_pts)
VALUES ($1, $3, 0, 0), ($2, $3, 0, 0)`,
		authKeyIDToInt64(orphanOne), authKeyIDToInt64(orphanTwo), userID); err != nil {
		t.Fatalf("insert stale orphan update states: %v", err)
	}

	if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: authorized, UserID: userID}); err != nil {
		t.Fatalf("bind authorization: %v", err)
	}
	if err := NewTempAuthKeyBindingStore(pool).Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: temp, PermAuthKeyID: authKeyIDToInt64(perm), Nonce: 1,
		TempSessionID: 2, ExpiresAt: tempExpiry, EncryptedMessage: []byte{1},
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}

	// Use a test-only historical window so a shared developer database's unrelated 24h-old
	// handshake keys cannot win the bounded candidate slot or be mutated by this test.
	const retention = 150 * 365 * 24 * time.Hour
	old := time.Now().Add(-200 * 365 * 24 * time.Hour)
	oldIDs := [][8]byte{orphanOne, orphanTwo, authorized, temp, perm, active}
	for _, id := range oldIDs {
		if _, err := pool.Exec(ctx, "UPDATE auth_keys SET created_at = $2, last_used_at = $2 WHERE auth_key_id = $1", authKeyIDToInt64(id), old); err != nil {
			t.Fatalf("age auth key %x: %v", id, err)
		}
	}

	deleted, err := keys.DeleteOrphaned(ctx, retention, 1, [][8]byte{active})
	if err != nil || deleted != 1 {
		t.Fatalf("first bounded orphan delete = %d/%v, want 1/nil", deleted, err)
	}
	var remainingOrphans int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM auth_keys WHERE auth_key_id = ANY($1::bigint[])
`, []int64{authKeyIDToInt64(orphanOne), authKeyIDToInt64(orphanTwo)}).Scan(&remainingOrphans); err != nil {
		t.Fatalf("count remaining orphans: %v", err)
	}
	if remainingOrphans != 1 {
		t.Fatalf("remaining old unreferenced orphans = %d, want 1 after batch=1", remainingOrphans)
	}

	deleted, err = keys.DeleteOrphaned(ctx, retention, 20, [][8]byte{active})
	if err != nil || deleted != 1 {
		t.Fatalf("second orphan delete = %d/%v, want remaining 1/nil", deleted, err)
	}
	var orphanStates int
	if err := pool.QueryRow(ctx, `
SELECT count(*)::int
FROM update_states
WHERE auth_key_id = ANY($1::bigint[])`, []int64{
		authKeyIDToInt64(orphanOne), authKeyIDToInt64(orphanTwo),
	}).Scan(&orphanStates); err != nil {
		t.Fatalf("count orphan update states: %v", err)
	}
	if orphanStates != 0 {
		t.Fatalf("orphan update states = %d, want 0 after atomic key GC", orphanStates)
	}
	for name, id := range map[string][8]byte{
		"recent": recent, "authorized": authorized, "temp": temp, "perm": perm, "active": active,
	} {
		if _, found, err := keys.Get(ctx, id); err != nil || !found {
			t.Fatalf("protected %s key %x found=%v err=%v, want retained", name, id, found, err)
		}
	}
}

func TestAuthKeyStoreDeleteCleansPermanentAndTempUpdateStatesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	userID := createRevokeTestUser(t, ctx, pool, "auth-key-delete-state")
	perm := randomUpdateRetentionAuthKey(t)
	temp := randomUpdateRetentionAuthKey(t)
	tempExpiry := int(time.Now().Add(time.Hour).Unix())
	for id, expiresAt := range map[[8]byte]int{perm: 0, temp: tempExpiry} {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id, ExpiresAt: expiresAt}); err != nil {
			t.Fatalf("save auth key %x: %v", id, err)
		}
		id := id
		t.Cleanup(func() { _ = keys.Delete(ctx, id) })
	}
	if err := NewTempAuthKeyBindingStore(pool).Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID:    temp,
		PermAuthKeyID:    authKeyIDToInt64(perm),
		Nonce:            31,
		TempSessionID:    32,
		ExpiresAt:        tempExpiry,
		EncryptedMessage: []byte{1},
	}); err != nil {
		t.Fatalf("save temp binding: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO update_states (auth_key_id, user_id, pts, observed_pts)
VALUES ($1, $3, 0, 0), ($2, $3, 0, 0)`,
		authKeyIDToInt64(perm), authKeyIDToInt64(temp), userID); err != nil {
		t.Fatalf("insert permanent/temp update states: %v", err)
	}

	if err := keys.Delete(ctx, perm); err != nil {
		t.Fatalf("delete permanent auth key: %v", err)
	}
	ids := []int64{authKeyIDToInt64(perm), authKeyIDToInt64(temp)}
	var keyRows, stateRows int
	if err := pool.QueryRow(ctx, `
SELECT
  (SELECT count(*) FROM auth_keys WHERE auth_key_id = ANY($1::bigint[]))::int,
  (SELECT count(*) FROM update_states WHERE auth_key_id = ANY($1::bigint[]))::int`, ids).Scan(&keyRows, &stateRows); err != nil {
		t.Fatalf("count deleted auth key state: %v", err)
	}
	if keyRows != 0 || stateRows != 0 {
		t.Fatalf("remaining key/state rows = %d/%d, want 0/0", keyRows, stateRows)
	}
}

func TestTempAuthKeyRetentionUsesAuthKeyExpiryForBoundAndUnboundKeysPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)
	cutoff := int64(time.Now().Add(-time.Hour).Unix())
	unbound := randomUpdateRetentionAuthKey(t)
	bound := randomUpdateRetentionAuthKey(t)
	live := randomUpdateRetentionAuthKey(t)
	perm := randomUpdateRetentionAuthKey(t)
	expiries := map[[8]byte]int{
		unbound: int(cutoff - 2),
		bound:   int(cutoff - 1),
		live:    int(cutoff + 1),
		perm:    0,
	}
	for id, expiresAt := range expiries {
		if err := keys.Save(ctx, store.AuthKeyData{ID: id, ExpiresAt: expiresAt}); err != nil {
			t.Fatalf("save key %x: %v", id, err)
		}
		id := id
		t.Cleanup(func() { _ = keys.Delete(ctx, id) })
	}
	if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID: bound, PermAuthKeyID: authKeyIDToInt64(perm), Nonce: 41,
		TempSessionID: 42, ExpiresAt: expiries[bound], EncryptedMessage: []byte{1},
	}); err != nil {
		t.Fatalf("save expired bound key: %v", err)
	}

	deleted, err := bindings.DeleteExpired(ctx, cutoff, 1)
	if err != nil || deleted != 1 {
		t.Fatalf("first bounded expiry delete = %d/%v, want 1/nil", deleted, err)
	}
	if _, found, err := keys.Get(ctx, unbound); err != nil || found {
		t.Fatalf("earliest unbound temp found=%v err=%v, want deleted", found, err)
	}
	if _, found, err := keys.Get(ctx, bound); err != nil || !found {
		t.Fatalf("second expired bound temp found=%v err=%v, want retained after limit=1", found, err)
	}

	deleted, err = bindings.DeleteExpired(ctx, cutoff, 10)
	if err != nil || deleted != 1 {
		t.Fatalf("second expiry delete = %d/%v, want 1/nil", deleted, err)
	}
	if _, found, err := bindings.GetByTemp(ctx, bound); err != nil || found {
		t.Fatalf("binding after temp key cascade found=%v err=%v, want absent", found, err)
	}
	for name, id := range map[string][8]byte{"live temp": live, "permanent": perm} {
		if _, found, err := keys.Get(ctx, id); err != nil || !found {
			t.Fatalf("%s found=%v err=%v, want retained", name, found, err)
		}
	}
}

func TestAuthKeyGetTouchPreventsOrphanCollectionPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("random auth key id: %v", err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() { _ = keys.Delete(ctx, id) })

	const retention = 150 * 365 * 24 * time.Hour
	old := time.Now().Add(-200 * 365 * 24 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE auth_keys SET created_at = $2, last_used_at = $2 WHERE auth_key_id = $1", authKeyIDToInt64(id), old); err != nil {
		t.Fatalf("age auth key: %v", err)
	}
	if _, found, err := keys.Get(ctx, id); err != nil || !found {
		t.Fatalf("touch auth key found=%v err=%v", found, err)
	}
	deleted, err := keys.DeleteOrphaned(ctx, retention, 10, nil)
	if err != nil {
		t.Fatalf("delete orphaned: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want 0 after atomic Get touch", deleted)
	}
	if _, found, err := keys.Get(ctx, id); err != nil || !found {
		t.Fatalf("touched key retained found=%v err=%v", found, err)
	}
}

func TestActiveRawAuthKeyHeartbeatProtectsOtherInstanceKeyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("random auth key id: %v", err)
	}
	t.Cleanup(func() { _ = keys.Delete(ctx, id) })
	if err := keys.Save(ctx, store.AuthKeyData{ID: id}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE auth_keys SET created_at = $2, last_used_at = $2 WHERE auth_key_id = $1", authKeyIDToInt64(id), old); err != nil {
		t.Fatalf("age active auth key: %v", err)
	}

	// Model another process heartbeating its local SessionManager snapshot. The collector on this
	// process has no protected-list entry for the key and must still respect durable last_used_at.
	if err := keys.TouchActiveRawAuthKeys(ctx, [][8]byte{id, id}); err != nil {
		t.Fatalf("heartbeat active raw auth key: %v", err)
	}
	deleted, err := keys.DeleteOrphaned(ctx, 24*time.Hour, 10, nil)
	if err != nil {
		t.Fatalf("delete orphaned after heartbeat: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("deleted = %d, want active key protected by durable heartbeat", deleted)
	}
	if _, found, err := keys.Get(ctx, id); err != nil || !found {
		t.Fatalf("heartbeat key found=%v err=%v, want present", found, err)
	}
}
