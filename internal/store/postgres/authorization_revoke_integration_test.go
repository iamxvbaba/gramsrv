package postgres

import (
	"context"
	"errors"
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
	tempExpiry := int(time.Now().Add(time.Hour).Unix())

	saveRevokeTestAuthKey(t, ctx, keys, perm, 0)
	saveRevokeTestAuthKey(t, ctx, keys, temp, tempExpiry)
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
		ExpiresAt:        tempExpiry,
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

	for i, key := range [][8]byte{keep, revokedOne, revokedTwo} {
		saveRevokeTestAuthKey(t, ctx, keys, key, 0)
		if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: key, UserID: userID, Hash: int64(9100 + i)}); err != nil {
			t.Fatalf("bind auth %x: %v", key, err)
		}
	}
	tempExpiry := int(time.Now().Add(time.Hour).Unix())
	saveRevokeTestAuthKey(t, ctx, keys, tempForTwo, tempExpiry)
	if err := NewTempAuthKeyBindingStore(pool).Save(ctx, domain.TempAuthKeyBinding{
		TempAuthKeyID:    tempForTwo,
		PermAuthKeyID:    authKeyIDToInt64(revokedTwo),
		Nonce:            3,
		TempSessionID:    4,
		ExpiresAt:        tempExpiry,
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
	saveRevokeTestAuthKey(t, ctx, keys, id, 0)
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

func TestAuthorizationStoreRevokeByHashConcurrentTempBindLeavesNoDanglingStatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "bind-revoke-race")
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)

	for attempt := 0; attempt < 24; attempt++ {
		tempExpiry := int(time.Now().Add(time.Hour).Unix()) + attempt
		perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, tempExpiry)
		hash := int64(9300 + attempt)
		if err := auths.Bind(ctx, domain.Authorization{
			AuthKeyID: perm,
			UserID:    userID,
			Hash:      hash,
		}); err != nil {
			t.Fatalf("attempt %d bind authorization: %v", attempt, err)
		}
		candidate := domain.TempAuthKeyBinding{
			TempAuthKeyID:    temp,
			PermAuthKeyID:    authKeyIDToInt64(perm),
			Nonce:            int64(700 + attempt),
			TempSessionID:    int64(800 + attempt),
			ExpiresAt:        tempExpiry,
			EncryptedMessage: []byte("bind-revoke race"),
		}

		start := make(chan struct{})
		bindResult := make(chan error, 1)
		type revokeResult struct {
			found bool
			err   error
		}
		revokeResults := make(chan revokeResult, 1)
		go func() {
			<-start
			bindResult <- bindings.Save(ctx, candidate)
		}()
		go func() {
			<-start
			_, found, err := auths.RevokeByHash(ctx, userID, hash)
			revokeResults <- revokeResult{found: found, err: err}
		}()
		close(start)

		bindErr := <-bindResult
		if bindErr != nil && !errors.Is(bindErr, store.ErrAuthKeyBindingInvalid) {
			t.Fatalf("attempt %d bind/revoke race bind error = %v", attempt, bindErr)
		}
		revoked := <-revokeResults
		if revoked.err != nil || !revoked.found {
			t.Fatalf("attempt %d bind/revoke race found=%v err=%v", attempt, revoked.found, revoked.err)
		}
		if _, found, err := bindings.GetByTemp(ctx, temp); err != nil || found {
			t.Fatalf("attempt %d dangling binding found=%v err=%v", attempt, found, err)
		}
		assertRevokeTestMissingAuthKey(t, ctx, keys, perm)
		assertRevokeTestNoAuthorization(t, ctx, auths, perm)
		if bindErr == nil {
			assertRevokeTestMissingAuthKey(t, ctx, keys, temp)
		} else {
			assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, tempExpiry)
			assertRevokeTestNoAuthorization(t, ctx, auths, temp)
			if err := keys.Delete(ctx, temp); err != nil {
				t.Fatalf("attempt %d clean unbound loser temp: %v", attempt, err)
			}
			assertRevokeTestMissingAuthKey(t, ctx, keys, temp)
		}
	}
}

func TestAuthorizationStoreRevokeByHashSkipsKeyTransferredAfterCandidateReadPostgres(t *testing.T) {
	pool := testPool(t)
	testCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)
	states := NewUpdateStateStore(pool)
	userA := createRevokeTestUser(t, testCtx, pool, "hash-owner-a")
	userB := createRevokeTestUser(t, testCtx, pool, "hash-owner-b")

	tempExpiry := int(time.Now().Add(time.Hour).Unix())
	perm := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, 0)
	temp := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, tempExpiry)
	const (
		hashA = int64(9501)
		hashB = int64(9502)
	)
	if err := auths.Bind(testCtx, domain.Authorization{
		AuthKeyID: perm,
		UserID:    userA,
		Hash:      hashA,
	}); err != nil {
		t.Fatalf("bind original A authorization: %v", err)
	}
	binding := domain.TempAuthKeyBinding{
		TempAuthKeyID:    temp,
		PermAuthKeyID:    authKeyIDToInt64(perm),
		Nonce:            951,
		TempSessionID:    952,
		ExpiresAt:        tempExpiry,
		EncryptedMessage: []byte("owner-transfer binding"),
	}
	if err := bindings.Save(testCtx, binding); err != nil {
		t.Fatalf("save temp binding before owner transfer: %v", err)
	}

	// Bind B performs the auth_keys-first ownership change inside an open
	// transaction. Its uncommitted row is invisible to A's candidate lookup, but
	// the parent FOR UPDATE lock is the deterministic barrier for revocation.
	bindB, err := pool.Begin(testCtx)
	if err != nil {
		t.Fatalf("begin B bind transaction: %v", err)
	}
	defer func() { _ = bindB.Rollback(context.Background()) }()
	wantB := domain.Authorization{
		AuthKeyID:       perm,
		UserID:          userB,
		Hash:            hashB,
		Layer:           227,
		DeviceModel:     "owner-b-device",
		Platform:        "android",
		SystemVersion:   "test",
		APIID:           100,
		AppVersion:      "owner-transfer",
		IP:              "127.0.0.2",
		PasswordPending: true,
	}
	if err := bindAuthorization(testCtx, bindB, wantB); err != nil {
		t.Fatalf("stage B ownership transfer: %v", err)
	}
	wantBStored, found, err := NewAuthorizationStore(bindB).ByAuthKey(testCtx, perm)
	if err != nil || !found {
		t.Fatalf("read staged B authorization found=%v err=%v", found, err)
	}
	wantBState, found, err := NewUpdateStateStore(bindB).Get(testCtx, perm, userB)
	if err != nil || !found {
		t.Fatalf("read staged B update state found=%v err=%v", found, err)
	}

	revokeConn, err := pool.Acquire(testCtx)
	if err != nil {
		t.Fatalf("acquire dedicated revoke connection: %v", err)
	}
	t.Cleanup(revokeConn.Release)
	var revokePID int
	if err := revokeConn.QueryRow(testCtx, "SELECT pg_backend_pid()").Scan(&revokePID); err != nil {
		t.Fatalf("get revoke backend pid: %v", err)
	}

	type revokeResult struct {
		a     domain.Authorization
		found bool
		err   error
	}
	revokeResults := make(chan revokeResult, 1)
	go func() {
		a, found, err := NewAuthorizationStore(revokeConn).RevokeByHash(testCtx, userA, hashA)
		revokeResults <- revokeResult{a: a, found: found, err: err}
	}()
	waitForPostgresBackendLockWait(t, testCtx, pool, revokePID)

	// Lock wait proves A's revoke already read its candidate and is now serialized
	// behind Bind B. After B commits, the fresh owner/hash revalidation must omit
	// the key instead of deleting B through A's stale candidate.
	if err := bindB.Commit(testCtx); err != nil {
		t.Fatalf("commit B ownership transfer: %v", err)
	}

	var revoked revokeResult
	select {
	case revoked = <-revokeResults:
	case <-testCtx.Done():
		t.Fatalf("revoke did not finish after releasing FK barrier: %v", testCtx.Err())
	}
	if revoked.err != nil || revoked.found {
		t.Fatalf("stale A revoke after B transfer found=%v err=%v, want not found", revoked.found, revoked.err)
	}
	if revoked.a != (domain.Authorization{}) {
		t.Fatalf("stale A revoke returned authorization %+v, want zero", revoked.a)
	}

	assertRevokeTestPresentAuthKey(t, testCtx, keys, perm)
	assertRevokeTestPresentAuthKey(t, testCtx, keys, temp)
	assertTempIdentityBinding(t, testCtx, bindings, binding)
	gotB, found, err := auths.ByAuthKey(testCtx, perm)
	if err != nil || !found || gotB != wantBStored {
		t.Fatalf("B authorization after stale A revoke = %+v found=%v err=%v, want %+v", gotB, found, err, wantBStored)
	}
	gotBState, found, err := states.Get(testCtx, perm, userB)
	if err != nil || !found || gotBState != wantBState {
		t.Fatalf("B update state after stale A revoke = %+v found=%v err=%v, want %+v", gotBState, found, err, wantBState)
	}
	if _, found, err := states.Get(testCtx, perm, userA); err != nil || found {
		t.Fatalf("stale A update state found=%v err=%v, want absent after B bind", found, err)
	}
}

func TestAuthorizationStoreRevokeByUserExceptPartiallySkipsTransferredCandidatePostgres(t *testing.T) {
	pool := testPool(t)
	testCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)
	states := NewUpdateStateStore(pool)
	userA := createRevokeTestUser(t, testCtx, pool, "bulk-owner-a")
	userB := createRevokeTestUser(t, testCtx, pool, "bulk-owner-b")

	keep := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, 0)
	transferred := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, 0)
	revoked := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, 0)
	transferredExpiry := int(time.Now().Add(time.Hour).Unix())
	revokedExpiry := transferredExpiry + 1
	transferredTemp := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, transferredExpiry)
	revokedTemp := saveTempIdentityTestAuthKey(t, testCtx, pool, keys, revokedExpiry)

	authorizationsA := []domain.Authorization{
		{AuthKeyID: keep, UserID: userA, Hash: 9601, DeviceModel: "keep-a"},
		{AuthKeyID: transferred, UserID: userA, Hash: 9602, DeviceModel: "transfer-from-a"},
		{AuthKeyID: revoked, UserID: userA, Hash: 9603, DeviceModel: "revoke-a", PasswordPending: true},
	}
	for _, authorization := range authorizationsA {
		if err := auths.Bind(testCtx, authorization); err != nil {
			t.Fatalf("bind A authorization %x: %v", authorization.AuthKeyID, err)
		}
	}
	transferredBinding := domain.TempAuthKeyBinding{
		TempAuthKeyID:    transferredTemp,
		PermAuthKeyID:    authKeyIDToInt64(transferred),
		Nonce:            961,
		TempSessionID:    962,
		ExpiresAt:        transferredExpiry,
		EncryptedMessage: []byte("transferred candidate binding"),
	}
	revokedBinding := domain.TempAuthKeyBinding{
		TempAuthKeyID:    revokedTemp,
		PermAuthKeyID:    authKeyIDToInt64(revoked),
		Nonce:            963,
		TempSessionID:    964,
		ExpiresAt:        revokedExpiry,
		EncryptedMessage: []byte("revoked candidate binding"),
	}
	for _, binding := range []domain.TempAuthKeyBinding{transferredBinding, revokedBinding} {
		if err := bindings.Save(testCtx, binding); err != nil {
			t.Fatalf("save candidate binding for perm %d: %v", binding.PermAuthKeyID, err)
		}
	}

	wantKeepKey, found, err := keys.Get(testCtx, keep)
	if err != nil || !found {
		t.Fatalf("read keep key before revoke found=%v err=%v", found, err)
	}
	wantKeepAuth, found, err := auths.ByAuthKey(testCtx, keep)
	if err != nil || !found {
		t.Fatalf("read keep authorization before revoke found=%v err=%v", found, err)
	}
	wantKeepState, found, err := states.Get(testCtx, keep, userA)
	if err != nil || !found {
		t.Fatalf("read keep state before revoke found=%v err=%v", found, err)
	}
	wantRevokedAuth, found, err := auths.ByAuthKey(testCtx, revoked)
	if err != nil || !found {
		t.Fatalf("read revocable authorization before revoke found=%v err=%v", found, err)
	}

	bindB, err := pool.Begin(testCtx)
	if err != nil {
		t.Fatalf("begin partial owner-transfer transaction: %v", err)
	}
	defer func() { _ = bindB.Rollback(context.Background()) }()
	wantB := domain.Authorization{
		AuthKeyID:     transferred,
		UserID:        userB,
		Hash:          9604,
		Layer:         227,
		DeviceModel:   "bulk-owner-b",
		Platform:      "android",
		SystemVersion: "test",
		APIID:         100,
		AppVersion:    "partial-owner-transfer",
		IP:            "127.0.0.3",
	}
	if err := bindAuthorization(testCtx, bindB, wantB); err != nil {
		t.Fatalf("stage partial B ownership transfer: %v", err)
	}
	wantBStored, found, err := NewAuthorizationStore(bindB).ByAuthKey(testCtx, transferred)
	if err != nil || !found {
		t.Fatalf("read staged partial B authorization found=%v err=%v", found, err)
	}
	wantBState, found, err := NewUpdateStateStore(bindB).Get(testCtx, transferred, userB)
	if err != nil || !found {
		t.Fatalf("read staged partial B state found=%v err=%v", found, err)
	}

	revokeConn, err := pool.Acquire(testCtx)
	if err != nil {
		t.Fatalf("acquire dedicated bulk revoke connection: %v", err)
	}
	t.Cleanup(revokeConn.Release)
	var revokePID int
	if err := revokeConn.QueryRow(testCtx, "SELECT pg_backend_pid()").Scan(&revokePID); err != nil {
		t.Fatalf("get bulk revoke backend pid: %v", err)
	}
	type bulkRevokeResult struct {
		deleted []domain.Authorization
		err     error
	}
	revokeResults := make(chan bulkRevokeResult, 1)
	go func() {
		deleted, err := NewAuthorizationStore(revokeConn).RevokeByUserExcept(testCtx, userA, keep)
		revokeResults <- bulkRevokeResult{deleted: deleted, err: err}
	}()
	waitForPostgresBackendLockWait(t, testCtx, pool, revokePID)

	// The bulk candidate list now contains both old A keys. Releasing B's parent
	// lock forces a fresh owner revalidation: transferred must be omitted while
	// the unrelated candidate that still belongs to A remains revocable.
	if err := bindB.Commit(testCtx); err != nil {
		t.Fatalf("commit partial B ownership transfer: %v", err)
	}
	var result bulkRevokeResult
	select {
	case result = <-revokeResults:
	case <-testCtx.Done():
		t.Fatalf("bulk revoke did not finish after owner transfer: %v", testCtx.Err())
	}
	if result.err != nil {
		t.Fatalf("bulk revoke after partial owner transfer: %v", result.err)
	}
	if len(result.deleted) != 1 || result.deleted[0] != wantRevokedAuth {
		t.Fatalf("bulk revoked authorizations = %+v, want only %+v", result.deleted, wantRevokedAuth)
	}

	gotKeepKey, found, err := keys.Get(testCtx, keep)
	if err != nil || !found || gotKeepKey != wantKeepKey {
		t.Fatalf("keep key after bulk revoke = %+v found=%v err=%v, want unchanged", gotKeepKey, found, err)
	}
	gotKeepAuth, found, err := auths.ByAuthKey(testCtx, keep)
	if err != nil || !found || gotKeepAuth != wantKeepAuth {
		t.Fatalf("keep authorization after bulk revoke = %+v found=%v err=%v, want %+v", gotKeepAuth, found, err, wantKeepAuth)
	}
	gotKeepState, found, err := states.Get(testCtx, keep, userA)
	if err != nil || !found || gotKeepState != wantKeepState {
		t.Fatalf("keep state after bulk revoke = %+v found=%v err=%v, want %+v", gotKeepState, found, err, wantKeepState)
	}

	assertRevokeTestPresentAuthKey(t, testCtx, keys, transferred)
	assertRevokeTestPresentAuthKey(t, testCtx, keys, transferredTemp)
	assertTempIdentityBinding(t, testCtx, bindings, transferredBinding)
	gotB, found, err := auths.ByAuthKey(testCtx, transferred)
	if err != nil || !found || gotB != wantBStored {
		t.Fatalf("transferred B authorization = %+v found=%v err=%v, want %+v", gotB, found, err, wantBStored)
	}
	gotBState, found, err := states.Get(testCtx, transferred, userB)
	if err != nil || !found || gotBState != wantBState {
		t.Fatalf("transferred B state = %+v found=%v err=%v, want %+v", gotBState, found, err, wantBState)
	}
	if _, found, err := states.Get(testCtx, transferred, userA); err != nil || found {
		t.Fatalf("old A state for transferred key found=%v err=%v, want absent", found, err)
	}

	assertRevokeTestMissingAuthKey(t, testCtx, keys, revoked)
	assertRevokeTestMissingAuthKey(t, testCtx, keys, revokedTemp)
	assertRevokeTestNoAuthorization(t, testCtx, auths, revoked)
	if _, found, err := bindings.GetByTemp(testCtx, revokedTemp); err != nil || found {
		t.Fatalf("revoked temp binding found=%v err=%v, want absent", found, err)
	}
	if _, found, err := states.Get(testCtx, revoked, userA); err != nil || found {
		t.Fatalf("revoked A state found=%v err=%v, want absent", found, err)
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

func saveRevokeTestAuthKey(t *testing.T, ctx context.Context, keys store.AuthKeyStore, id [8]byte, expiresAt int) {
	t.Helper()
	if err := keys.Save(ctx, store.AuthKeyData{ID: id, ServerSalt: int64(id[0]), ExpiresAt: expiresAt}); err != nil {
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
