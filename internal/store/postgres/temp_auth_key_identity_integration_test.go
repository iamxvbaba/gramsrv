package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestTempAuthKeyBindingStoreRejectsIntegerWraparoundPostgres(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("64-bit int required to construct an out-of-int32 expiry")
	}
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)
	handshakeExpiry := int(time.Now().Add(time.Hour).Unix())
	temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, handshakeExpiry)
	perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)

	overflow := domain.TempAuthKeyBinding{
		TempAuthKeyID:    temp,
		PermAuthKeyID:    authKeyIDToInt64(perm),
		Nonce:            901,
		TempSessionID:    902,
		ExpiresAt:        int(int64(handshakeExpiry) + (int64(1) << 32)),
		EncryptedMessage: []byte("wraparound"),
	}
	if err := bindings.Save(ctx, overflow); !errors.Is(err, store.ErrAuthKeyBindingInvalid) {
		t.Fatalf("overflow binding expiry error = %v, want %v", err, store.ErrAuthKeyBindingInvalid)
	}
	if _, found, err := bindings.GetByTemp(ctx, temp); err != nil || found {
		t.Fatalf("binding after overflow found=%v err=%v, want absent", found, err)
	}
	if _, err := bindings.DeleteExpired(ctx, int64(math.MaxInt32)+1, 1); err == nil {
		t.Fatal("overflow retention cutoff succeeded, want explicit rejection")
	}
}

func TestAuthorizationStoreRejectsTemporaryProtocolKeyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, int(time.Now().Add(time.Hour).Unix()))
	phone := fmt.Sprintf("15558%015d", time.Now().UnixNano())
	user, err := NewUserStore(pool).Create(ctx, domain.User{Phone: phone, FirstName: "TempKeyGuard"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
	})

	err = NewAuthorizationStore(pool).Bind(ctx, domain.Authorization{AuthKeyID: temp, UserID: user.ID})
	if !errors.Is(err, store.ErrAuthKeyNotPermanent) {
		t.Fatalf("bind authorization to temp key error = %v, want %v", err, store.ErrAuthKeyNotPermanent)
	}
	if _, found, getErr := NewAuthorizationStore(pool).ByAuthKey(ctx, temp); getErr != nil || found {
		t.Fatalf("temporary authorization found=%v err=%v, want absent", found, getErr)
	}
}

func TestTempAuthKeyBindingStorePreservesHandshakeExpiryAndRejectsRebindPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)

	handshakeExpiry := int(time.Now().Add(time.Hour).Unix())
	temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, handshakeExpiry)
	permA := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
	permB := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)

	first := domain.TempAuthKeyBinding{
		TempAuthKeyID:    temp,
		PermAuthKeyID:    authKeyIDToInt64(permA),
		Nonce:            101,
		TempSessionID:    201,
		ExpiresAt:        handshakeExpiry,
		EncryptedMessage: []byte("first binding"),
	}
	if err := bindings.Save(ctx, first); err != nil {
		t.Fatalf("save first binding: %v", err)
	}
	assertTempIdentityBinding(t, ctx, bindings, first)
	assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, handshakeExpiry)

	// The app service accepts client-specific proof expiry (TDesktop adds 30s)
	// but must normalize what it passes to the store. A direct caller cannot
	// persist proof metadata whose lifetime differs from the handshake key.
	mismatched := first
	mismatched.Nonce = 102
	mismatched.TempSessionID = 202
	mismatched.ExpiresAt = handshakeExpiry + 60
	mismatched.EncryptedMessage = []byte("mismatched replay")
	if err := bindings.Save(ctx, mismatched); !errors.Is(err, store.ErrAuthKeyBindingInvalid) {
		t.Fatalf("mismatched replay error = %v, want %v", err, store.ErrAuthKeyBindingInvalid)
	}
	assertTempIdentityBinding(t, ctx, bindings, first)

	replayed := first
	replayed.Nonce = 103
	replayed.TempSessionID = 203
	replayed.EncryptedMessage = []byte("normalized replay")
	if err := bindings.Save(ctx, replayed); err != nil {
		t.Fatalf("replay normalized binding: %v", err)
	}
	assertTempIdentityBinding(t, ctx, bindings, replayed)
	assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, handshakeExpiry)

	forbidden := replayed
	forbidden.PermAuthKeyID = authKeyIDToInt64(permB)
	forbidden.Nonce = 999
	forbidden.ExpiresAt = handshakeExpiry
	forbidden.EncryptedMessage = []byte("must not persist")
	if err := bindings.Save(ctx, forbidden); !errors.Is(err, store.ErrTempAuthKeyAlreadyBound) {
		t.Fatalf("cross-permanent rebind error = %v, want %v", err, store.ErrTempAuthKeyAlreadyBound)
	}
	assertTempIdentityBinding(t, ctx, bindings, replayed)
	assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, handshakeExpiry)
}

func TestTempAuthKeyBindingStoreAtomicallyMergesLayerObservationsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tests := []struct {
		name      string
		tempLayer int
		tempObs   int64
		permLayer int
		permObs   int64
		wantLayer int
		wantObs   int64
		wantErr   error
	}{
		{name: "temporary newer", tempLayer: 227, tempObs: 20, permLayer: 220, permObs: 10, wantLayer: 227, wantObs: 20},
		{name: "permanent newer", tempLayer: 220, tempObs: 10, permLayer: 227, permObs: 20, wantLayer: 227, wantObs: 20},
		{name: "equal ordered same layer", tempLayer: 225, tempObs: 30, permLayer: 225, permObs: 30, wantLayer: 225, wantObs: 30},
		{name: "equal ordered conflict", tempLayer: 220, tempObs: 30, permLayer: 227, permObs: 30, wantErr: store.ErrAuthKeySessionLayerConflict},
		{name: "legacy permanent wins", tempLayer: 220, permLayer: 227, wantLayer: 227},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID := createRevokeTestUser(t, ctx, pool, fmt.Sprintf("layer-merge-%d", i))
			keys := NewAuthKeyStore(pool)
			bindings := NewTempAuthKeyBindingStore(pool)
			auths := NewAuthorizationStore(pool)
			handshakeExpiry := int(time.Now().Add(time.Hour).Unix()) + i
			tempID := saveTempIdentityTestAuthKey(t, ctx, pool, keys, handshakeExpiry)
			permID := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
			if err := auths.Bind(ctx, domain.Authorization{
				AuthKeyID: permID, UserID: userID, Layer: tt.permLayer,
			}); err != nil {
				t.Fatalf("bind permanent authorization: %v", err)
			}
			if _, err := pool.Exec(ctx, `
UPDATE auth_keys SET layer = $2, layer_observation_id = $3
WHERE auth_key_id = $1`, authKeyIDToInt64(tempID), tt.tempLayer, tt.tempObs); err != nil {
				t.Fatalf("seed temporary layer observation: %v", err)
			}
			if _, err := pool.Exec(ctx, `
UPDATE auth_keys SET layer = $2, layer_observation_id = $3
WHERE auth_key_id = $1`, authKeyIDToInt64(permID), tt.permLayer, tt.permObs); err != nil {
				t.Fatalf("seed permanent layer observation: %v", err)
			}
			if _, err := pool.Exec(ctx, `
UPDATE authorizations SET layer = $2 WHERE auth_key_id = $1`, authKeyIDToInt64(permID), tt.permLayer); err != nil {
				t.Fatalf("seed authorization layer mirror: %v", err)
			}

			binding := domain.TempAuthKeyBinding{
				TempAuthKeyID:    tempID,
				PermAuthKeyID:    authKeyIDToInt64(permID),
				Nonce:            int64(1_000 + i),
				TempSessionID:    int64(2_000 + i),
				ExpiresAt:        handshakeExpiry,
				EncryptedMessage: []byte("layer merge proof"),
			}
			err := bindings.Save(ctx, binding)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("bind error = %v, want %v", err, tt.wantErr)
				}
				if _, found, getErr := bindings.GetByTemp(ctx, tempID); getErr != nil || found {
					t.Fatalf("conflicting binding found=%v err=%v, want absent", found, getErr)
				}
				assertTempIdentityLayerTuple(t, ctx, pool, tempID, tt.tempLayer, tt.tempObs)
				assertTempIdentityLayerTuple(t, ctx, pool, permID, tt.permLayer, tt.permObs)
				assertTempIdentityAuthorizationLayer(t, ctx, pool, permID, tt.permLayer)
				return
			}
			if err != nil {
				t.Fatalf("bind: %v", err)
			}
			// A normalized proof replay is idempotent and repeats the same merge.
			binding.Nonce++
			if err := bindings.Save(ctx, binding); err != nil {
				t.Fatalf("replay merged binding: %v", err)
			}
			assertTempIdentityLayerTuple(t, ctx, pool, tempID, tt.wantLayer, tt.wantObs)
			assertTempIdentityLayerTuple(t, ctx, pool, permID, tt.wantLayer, tt.wantObs)
			assertTempIdentityAuthorizationLayer(t, ctx, pool, permID, tt.wantLayer)
		})
	}
}

func TestTempAuthKeyBindingStoreConcurrentFirstBindKeepsHandshakeExpiryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)

	// A temp key has exactly one permanent identity even when two valid bind
	// proofs race. Repeat with fresh rows so the test covers the contended first
	// bind path instead of only the already-bound fast path.
	for attempt := 0; attempt < 16; attempt++ {
		handshakeExpiry := int(time.Now().Add(30*time.Minute).Unix()) + attempt
		temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, handshakeExpiry)
		permA := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		permB := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		candidates := []domain.TempAuthKeyBinding{
			{
				TempAuthKeyID: temp, PermAuthKeyID: authKeyIDToInt64(permA), Nonce: 301,
				ExpiresAt: handshakeExpiry, EncryptedMessage: []byte("candidate-a"),
			},
			{
				TempAuthKeyID: temp, PermAuthKeyID: authKeyIDToInt64(permB), Nonce: 302,
				ExpiresAt: handshakeExpiry, EncryptedMessage: []byte("candidate-b"),
			},
		}

		start := make(chan struct{})
		results := make(chan error, len(candidates))
		for _, candidate := range candidates {
			candidate := candidate
			go func() {
				<-start
				results <- bindings.Save(ctx, candidate)
			}()
		}
		close(start)

		var success, rejected int
		for range candidates {
			err := <-results
			switch {
			case err == nil:
				success++
			case errors.Is(err, store.ErrTempAuthKeyAlreadyBound):
				rejected++
			default:
				t.Fatalf("attempt %d concurrent bind: unexpected error %v", attempt, err)
			}
		}
		if success != 1 || rejected != 1 {
			t.Fatalf("attempt %d concurrent bind outcomes: success=%d rejected=%d, want 1/1", attempt, success, rejected)
		}

		got, found, err := bindings.GetByTemp(ctx, temp)
		if err != nil || !found {
			t.Fatalf("attempt %d get winner: found=%v err=%v", attempt, found, err)
		}
		var winner domain.TempAuthKeyBinding
		switch got.PermAuthKeyID {
		case candidates[0].PermAuthKeyID:
			winner = candidates[0]
		case candidates[1].PermAuthKeyID:
			winner = candidates[1]
		default:
			t.Fatalf("attempt %d winner perm auth key = %d, want one of the candidates", attempt, got.PermAuthKeyID)
		}
		assertTempIdentityBinding(t, ctx, bindings, winner)
		assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, handshakeExpiry)
	}
}

func TestTempAuthKeyBindingStoreRejectsMissingPermanentKeyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)

	handshakeExpiry := int(time.Now().Add(time.Hour).Unix())
	temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, handshakeExpiry)
	missingPerm := randomTempIdentityAuthKeyID(t)
	candidate := domain.TempAuthKeyBinding{
		TempAuthKeyID:    temp,
		PermAuthKeyID:    authKeyIDToInt64(missingPerm),
		Nonce:            401,
		TempSessionID:    402,
		ExpiresAt:        handshakeExpiry,
		EncryptedMessage: []byte("missing permanent key"),
	}
	if err := bindings.Save(ctx, candidate); !errors.Is(err, store.ErrAuthKeyBindingInvalid) {
		t.Fatalf("missing permanent key error = %v, want %v", err, store.ErrAuthKeyBindingInvalid)
	}
	_, rawInsertErr := pool.Exec(ctx, `
INSERT INTO temp_auth_key_bindings (
  temp_auth_key_id, perm_auth_key_id, nonce, temp_session_id, expires_at, encrypted_message
) VALUES ($1, $2, $3, $4, $5, $6)`,
		authKeyIDToInt64(temp), candidate.PermAuthKeyID, candidate.Nonce,
		candidate.TempSessionID, candidate.ExpiresAt, candidate.EncryptedMessage,
	)
	var pgErr *pgconn.PgError
	if !errors.As(rawInsertErr, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != tempAuthKeyPermFKConstraint {
		t.Fatalf("raw missing-perm FK error = %v, want 23503/%s", rawInsertErr, tempAuthKeyPermFKConstraint)
	}
	if _, found, err := bindings.GetByTemp(ctx, temp); err != nil || found {
		t.Fatalf("binding with missing permanent key found=%v err=%v, want absent", found, err)
	}
	assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, handshakeExpiry)
}

func TestTempAuthKeyBindingConcurrentWithPermanentDeleteLeavesNoDanglingStatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)

	for attempt := 0; attempt < 32; attempt++ {
		handshakeExpiry := int(time.Now().Add(time.Hour).Unix()) + attempt
		temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, handshakeExpiry)
		perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		candidate := domain.TempAuthKeyBinding{
			TempAuthKeyID:    temp,
			PermAuthKeyID:    authKeyIDToInt64(perm),
			Nonce:            int64(500 + attempt),
			TempSessionID:    int64(600 + attempt),
			ExpiresAt:        handshakeExpiry,
			EncryptedMessage: []byte("bind-delete race"),
		}

		start := make(chan struct{})
		bindResult := make(chan error, 1)
		deleteResult := make(chan error, 1)
		go func() {
			<-start
			bindResult <- bindings.Save(ctx, candidate)
		}()
		go func() {
			<-start
			deleteResult <- keys.Delete(ctx, perm)
		}()
		close(start)

		bindErr := <-bindResult
		if bindErr != nil && !errors.Is(bindErr, store.ErrAuthKeyBindingInvalid) {
			t.Fatalf("attempt %d bind/delete race bind error = %v", attempt, bindErr)
		}
		if err := <-deleteResult; err != nil {
			t.Fatalf("attempt %d bind/delete race delete: %v", attempt, err)
		}
		if _, found, err := bindings.GetByTemp(ctx, temp); err != nil || found {
			t.Fatalf("attempt %d dangling binding found=%v err=%v", attempt, found, err)
		}
		assertTempIdentityAuthKeyMissing(t, ctx, keys, perm)
		if bindErr == nil {
			// The binding committed first, so permanent-key deletion must have
			// observed it (or retried after the FK race) and deleted the temp key.
			assertTempIdentityAuthKeyMissing(t, ctx, keys, temp)
		} else {
			// Deletion won before the binding existed. The loser remains a valid,
			// unbound protocol temp key until its own expiry collector runs; it is
			// not allowed to acquire a binding or authorization to the deleted perm.
			assertTempIdentityAuthKeyExpiry(t, ctx, keys, temp, handshakeExpiry)
			if _, found, err := NewAuthorizationStore(pool).ByAuthKey(ctx, temp); err != nil || found {
				t.Fatalf("attempt %d loser temp authorization found=%v err=%v", attempt, found, err)
			}
			if err := keys.Delete(ctx, temp); err != nil {
				t.Fatalf("attempt %d clean unbound loser temp: %v", attempt, err)
			}
			assertTempIdentityAuthKeyMissing(t, ctx, keys, temp)
		}
	}
}

func waitForPostgresBackendLockWait(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	backendPID int,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
SELECT EXISTS (
  SELECT 1
  FROM pg_stat_activity AS activity
  WHERE activity.pid = $1
    AND activity.state = 'active'
    AND activity.wait_event_type = 'Lock'
    AND EXISTS (
      SELECT 1
      FROM pg_locks AS waiting_lock
      WHERE waiting_lock.pid = activity.pid
        AND NOT waiting_lock.granted
    )
)`, backendPID).Scan(&waiting); err != nil {
			t.Fatalf("observe delete backend lock wait: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backend %d never entered a PostgreSQL lock wait: %v", backendPID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func saveTempIdentityTestAuthKey(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	keys store.AuthKeyStore,
	expiresAt int,
) [8]byte {
	t.Helper()
	var id [8]byte
	var value [256]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatal(err)
	}
	if err := keys.Save(ctx, store.AuthKeyData{ID: id, Value: value, ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("save auth key: %v", err)
	}
	t.Cleanup(func() {
		_ = NewAuthKeyStore(pool).Delete(ctx, id)
	})
	return id
}

func randomTempIdentityAuthKeyID(t *testing.T) [8]byte {
	t.Helper()
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertTempIdentityBinding(
	t *testing.T,
	ctx context.Context,
	bindings store.TempAuthKeyBindingStore,
	want domain.TempAuthKeyBinding,
) {
	t.Helper()
	got, found, err := bindings.GetByTemp(ctx, want.TempAuthKeyID)
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if !found {
		t.Fatal("binding not found")
	}
	if got.TempAuthKeyID != want.TempAuthKeyID || got.PermAuthKeyID != want.PermAuthKeyID ||
		got.Nonce != want.Nonce || got.TempSessionID != want.TempSessionID || got.ExpiresAt != want.ExpiresAt ||
		!bytes.Equal(got.EncryptedMessage, want.EncryptedMessage) {
		t.Fatalf("binding mismatch: got %+v, want %+v", got, want)
	}
}

func assertTempIdentityAuthKeyExpiry(
	t *testing.T,
	ctx context.Context,
	keys store.AuthKeyStore,
	id [8]byte,
	want int,
) {
	t.Helper()
	got, found, err := keys.Get(ctx, id)
	if err != nil {
		t.Fatalf("get auth key: %v", err)
	}
	if !found {
		t.Fatal("auth key not found")
	}
	if got.ExpiresAt != want {
		t.Fatalf("auth key expires_at = %d, want %d", got.ExpiresAt, want)
	}
}

func assertTempIdentityAuthKeyMissing(
	t *testing.T,
	ctx context.Context,
	keys store.AuthKeyStore,
	id [8]byte,
) {
	t.Helper()
	if _, found, err := keys.Get(ctx, id); err != nil || found {
		t.Fatalf("auth key %x found=%v err=%v, want absent", id, found, err)
	}
}

func assertTempIdentityLayerTuple(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	authKeyID [8]byte,
	wantLayer int,
	wantObservationID int64,
) {
	t.Helper()
	var (
		layer         int
		observationID int64
	)
	if err := pool.QueryRow(ctx, `
SELECT layer, layer_observation_id
FROM auth_keys
WHERE auth_key_id = $1`, authKeyIDToInt64(authKeyID)).Scan(&layer, &observationID); err != nil {
		t.Fatalf("read auth-key layer tuple: %v", err)
	}
	if layer != wantLayer || observationID != wantObservationID {
		t.Fatalf("auth-key layer tuple = (%d,%d), want (%d,%d)", layer, observationID, wantLayer, wantObservationID)
	}
}

func assertTempIdentityAuthorizationLayer(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	authKeyID [8]byte,
	wantLayer int,
) {
	t.Helper()
	var layer int
	if err := pool.QueryRow(ctx, `
SELECT layer
FROM authorizations
WHERE auth_key_id = $1`, authKeyIDToInt64(authKeyID)).Scan(&layer); err != nil {
		t.Fatalf("read authorization layer mirror: %v", err)
	}
	if layer != wantLayer {
		t.Fatalf("authorization layer mirror = %d, want %d", layer, wantLayer)
	}
}
