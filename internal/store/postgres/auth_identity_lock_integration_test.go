package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestAuthIdentitySelectorRetriesUncommittedFirstBindSnapshotPostgres(t *testing.T) {
	pool := testPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	keys := NewAuthKeyStore(pool)
	bindings := NewTempAuthKeyBindingStore(pool)
	expiresAt := int(time.Now().Add(time.Hour).Unix())
	temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, expiresAt)
	perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
	binding := domain.TempAuthKeyBinding{
		TempAuthKeyID: temp, PermAuthKeyID: authKeyIDToInt64(perm),
		Nonce: 8701, TempSessionID: 8702, ExpiresAt: expiresAt,
		EncryptedMessage: []byte("first bind snapshot"),
	}

	advanceConn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer advanceConn.Release()
	var advancePID int
	if err := advanceConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&advancePID); err != nil {
		t.Fatal(err)
	}
	barrier := newAuthStoreQueryBarrier(advanceConn, "auth_identity_hint", "")
	msgID := authKeySessionLayerTestMsgID(time.Now().UTC(), 1)
	type advanceResult struct {
		value   store.AuthKeySessionLayer
		applied bool
		err     error
	}
	result := make(chan advanceResult, 1)
	go func() {
		value, applied, err := NewAuthKeyStore(barrier).AdvanceSessionLayer(ctx, temp, 8703, 227, msgID)
		result <- advanceResult{value: value, applied: applied, err: err}
	}()
	<-barrier.observed

	// The selector already read "unbound". Stage a committed binding behind
	// its statement snapshot while retaining P/raw row locks in the outer tx.
	bindTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bindTx.Rollback(context.Background()) }()
	if err := NewTempAuthKeyBindingStore(bindTx).Save(ctx, binding); err != nil {
		t.Fatalf("stage first bind: %v", err)
	}
	close(barrier.release)
	waitForPostgresBackendLockWait(t, ctx, pool, advancePID)
	if err := bindTx.Commit(ctx); err != nil {
		t.Fatalf("commit first bind: %v", err)
	}

	got := <-result
	if got.err != nil || !got.applied || got.value.Layer != 227 || !got.value.SharedDefault {
		t.Fatalf("advance after identity retry = (%+v,%v,%v)", got.value, got.applied, got.err)
	}
	assertTempIdentityBinding(t, ctx, bindings, binding)
	for _, id := range [][8]byte{temp, perm} {
		stored, found, err := keys.Get(ctx, id)
		if err != nil || !found || stored.Layer != 227 || stored.LayerObservationID != got.value.ObservationID {
			t.Fatalf("shared tuple %x = (%+v,%v,%v)", id, stored, found, err)
		}
	}
}

func TestAuthIdentitySelectorSerializesWithPermanentRevocationAndDeletePostgres(t *testing.T) {
	for _, op := range []string{"revoke", "delete"} {
		t.Run(op, func(t *testing.T) {
			pool := testPool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			t.Cleanup(cancel)
			keys := NewAuthKeyStore(pool)
			auths := NewAuthorizationStore(pool)
			bindings := NewTempAuthKeyBindingStore(pool)
			expiresAt := int(time.Now().Add(time.Hour).Unix())
			temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, expiresAt)
			perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
			userID := createRevokeTestUser(t, ctx, pool, "selector-"+op)
			hash := int64(8800)
			if err := auths.Bind(ctx, domain.Authorization{AuthKeyID: perm, UserID: userID, Hash: hash}); err != nil {
				t.Fatal(err)
			}
			if err := bindings.Save(ctx, domain.TempAuthKeyBinding{
				TempAuthKeyID: temp, PermAuthKeyID: authKeyIDToInt64(perm), ExpiresAt: expiresAt,
				EncryptedMessage: []byte("identity serialization"),
			}); err != nil {
				t.Fatal(err)
			}

			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = blocker.Rollback(context.Background()) }()
			if err := lockPermanentAuthIdentities(ctx, blocker, []int64{authKeyIDToInt64(perm)}); err != nil {
				t.Fatal(err)
			}

			selectorConn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer selectorConn.Release()
			opConn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer opConn.Release()
			var selectorPID, opPID int
			if err := selectorConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&selectorPID); err != nil {
				t.Fatal(err)
			}
			if err := opConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&opPID); err != nil {
				t.Fatal(err)
			}

			selectorResult := make(chan error, 1)
			go func() {
				_, _, err := NewAuthKeyStore(selectorConn).AdvanceSessionLayer(
					ctx, temp, 8801, 227, authKeySessionLayerTestMsgID(time.Now().UTC(), 1),
				)
				selectorResult <- err
			}()
			waitForPostgresBackendLockWait(t, ctx, pool, selectorPID)
			opResult := make(chan error, 1)
			go func() {
				if op == "revoke" {
					_, found, err := NewAuthorizationStore(opConn).RevokeByHash(ctx, userID, hash)
					if err == nil && !found {
						err = errors.New("revoke target disappeared")
					}
					opResult <- err
					return
				}
				opResult <- NewAuthKeyStore(opConn).Delete(ctx, perm)
			}()
			waitForPostgresBackendLockWait(t, ctx, pool, opPID)
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			selectorErr := <-selectorResult
			if selectorErr != nil &&
				!errors.Is(selectorErr, store.ErrAuthKeyNotFound) &&
				!errors.Is(selectorErr, store.ErrAuthKeyBindingInvalid) {
				t.Fatalf("selector error = %v", selectorErr)
			}
			if err := <-opResult; err != nil {
				t.Fatalf("%s error = %v", op, err)
			}
			assertTempIdentityAuthKeyMissing(t, ctx, keys, temp)
			assertTempIdentityAuthKeyMissing(t, ctx, keys, perm)
			assertRevokeTestNoAuthorization(t, ctx, auths, perm)
			if _, found, err := bindings.GetByTemp(ctx, temp); err != nil || found {
				t.Fatalf("binding after %s found=%v err=%v", op, found, err)
			}
		})
	}
}

func TestAuthIdentityAuthorizationMirrorUsesLockedPrimaryLayerPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	keys := NewAuthKeyStore(pool)
	auths := NewAuthorizationStore(pool)

	t.Run("advance before stale bind", func(t *testing.T) {
		perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		userID := createRevokeTestUser(t, ctx, pool, "layer-advance-before-bind")
		if _, _, err := keys.AdvanceSessionLayer(
			ctx, perm, 8901, 227, authKeySessionLayerTestMsgID(time.Now().UTC(), 1),
		); err != nil {
			t.Fatal(err)
		}
		if err := auths.Bind(ctx, domain.Authorization{
			AuthKeyID: perm, UserID: userID, Hash: 8902, Layer: 220,
		}); err != nil {
			t.Fatal(err)
		}
		got, found, err := auths.ByAuthKey(ctx, perm)
		if err != nil || !found || got.Layer != 227 {
			t.Fatalf("stale bind mirror = (%+v,%v,%v)", got, found, err)
		}
	})

	t.Run("bind before advance", func(t *testing.T) {
		perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		userID := createRevokeTestUser(t, ctx, pool, "layer-bind-before-advance")
		if err := auths.Bind(ctx, domain.Authorization{
			AuthKeyID: perm, UserID: userID, Hash: 8903, Layer: 220,
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := keys.AdvanceSessionLayer(
			ctx, perm, 8904, 227, authKeySessionLayerTestMsgID(time.Now().UTC(), 2),
		); err != nil {
			t.Fatal(err)
		}
		got, found, err := auths.ByAuthKey(ctx, perm)
		if err != nil || !found || got.Layer != 227 {
			t.Fatalf("advanced mirror = (%+v,%v,%v)", got, found, err)
		}
	})
}

func TestDeleteOrphanedRevalidatesUncommittedAuthorizationAndTempBindPostgres(t *testing.T) {
	t.Run("authorization bind", func(t *testing.T) {
		pool := testPool(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)
		keys := NewAuthKeyStore(pool)
		auths := NewAuthorizationStore(pool)
		perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		if _, err := pool.Exec(ctx, `UPDATE auth_keys SET last_used_at = now() - interval '72 hours' WHERE auth_key_id = $1`, authKeyIDToInt64(perm)); err != nil {
			t.Fatal(err)
		}
		userID := createRevokeTestUser(t, ctx, pool, "orphan-auth-bind")

		gcConn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer gcConn.Release()
		var gcPID int
		if err := gcConn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&gcPID); err != nil {
			t.Fatal(err)
		}
		barrier := newAuthStoreQueryBarrier(gcConn, "", "orphan_identity_candidates")
		gcResult := make(chan struct {
			deleted int
			err     error
		}, 1)
		go func() {
			deleted, err := NewAuthKeyStore(barrier).DeleteOrphaned(ctx, 24*time.Hour, 1, nil)
			gcResult <- struct {
				deleted int
				err     error
			}{deleted: deleted, err: err}
		}()
		<-barrier.observed

		bindTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = bindTx.Rollback(context.Background()) }()
		if err := NewAuthorizationStore(bindTx).Bind(ctx, domain.Authorization{
			AuthKeyID: perm, UserID: userID, Hash: 9001,
		}); err != nil {
			t.Fatal(err)
		}
		close(barrier.release)
		waitForPostgresBackendLockWait(t, ctx, pool, gcPID)
		if err := bindTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		got := <-gcResult
		if got.err != nil || got.deleted != 0 {
			t.Fatalf("orphan GC after authorization bind = (%d,%v)", got.deleted, got.err)
		}
		assertRevokeTestPresentAuthKey(t, ctx, keys, perm)
		assertRevokeTestPresentAuthorization(t, ctx, auths, perm)
	})

	t.Run("temporary bind", func(t *testing.T) {
		pool := testPool(t)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		t.Cleanup(cancel)
		keys := NewAuthKeyStore(pool)
		bindings := NewTempAuthKeyBindingStore(pool)
		expiresAt := int(time.Now().Add(time.Hour).Unix())
		temp := saveTempIdentityTestAuthKey(t, ctx, pool, keys, expiresAt)
		perm := saveTempIdentityTestAuthKey(t, ctx, pool, keys, 0)
		if _, err := pool.Exec(ctx, `UPDATE auth_keys SET last_used_at = now() - interval '72 hours' WHERE auth_key_id = $1`, authKeyIDToInt64(temp)); err != nil {
			t.Fatal(err)
		}
		binding := domain.TempAuthKeyBinding{
			TempAuthKeyID: temp, PermAuthKeyID: authKeyIDToInt64(perm),
			Nonce: 9002, TempSessionID: 9003, ExpiresAt: expiresAt,
			EncryptedMessage: []byte("orphan bind revalidation"),
		}

		gcConn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer gcConn.Release()
		barrier := newAuthStoreQueryBarrier(gcConn, "", "orphan_identity_candidates")
		gcResult := make(chan struct {
			deleted int
			err     error
		}, 1)
		go func() {
			deleted, err := NewAuthKeyStore(barrier).DeleteOrphaned(ctx, 24*time.Hour, 1, nil)
			gcResult <- struct {
				deleted int
				err     error
			}{deleted: deleted, err: err}
		}()
		<-barrier.observed

		bindTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = bindTx.Rollback(context.Background()) }()
		if err := NewTempAuthKeyBindingStore(bindTx).Save(ctx, binding); err != nil {
			t.Fatal(err)
		}
		close(barrier.release)
		var gc struct {
			deleted int
			err     error
		}
		select {
		case gc = <-gcResult:
		case <-time.After(2 * time.Second):
			_ = bindTx.Commit(context.Background())
			t.Fatal("orphan GC blocked on an uncommitted temp bind despite SKIP LOCKED")
		}
		if gc.err != nil || gc.deleted != 0 {
			t.Fatalf("orphan GC during temp bind = (%d,%v)", gc.deleted, gc.err)
		}
		if err := bindTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertTempIdentityBinding(t, ctx, bindings, binding)
		assertRevokeTestPresentAuthKey(t, ctx, keys, temp)
		assertRevokeTestPresentAuthKey(t, ctx, keys, perm)
	})
}

type authStoreQueryBarrier struct {
	*pgxpool.Conn
	queryRowMarker string
	queryMarker    string
	observed       chan struct{}
	release        chan struct{}
	once           sync.Once
}

func newAuthStoreQueryBarrier(conn *pgxpool.Conn, queryRowMarker, queryMarker string) *authStoreQueryBarrier {
	return &authStoreQueryBarrier{
		Conn: conn, queryRowMarker: queryRowMarker, queryMarker: queryMarker,
		observed: make(chan struct{}), release: make(chan struct{}),
	}
}

func (db *authStoreQueryBarrier) Begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := db.Conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return &authStoreBarrierTx{Tx: tx, owner: db}, nil
}

type authStoreBarrierTx struct {
	pgx.Tx
	owner *authStoreQueryBarrier
}

func (tx *authStoreBarrierTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, sql, args...)
	if tx.owner.queryRowMarker != "" && strings.Contains(sql, tx.owner.queryRowMarker) {
		return &authStoreBarrierRow{Row: row, owner: tx.owner}
	}
	return row
}

func (tx *authStoreBarrierTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := tx.Tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	if tx.owner.queryMarker != "" && strings.Contains(sql, tx.owner.queryMarker) {
		return &authStoreBarrierRows{Rows: rows, owner: tx.owner}, nil
	}
	return rows, nil
}

type authStoreBarrierRow struct {
	pgx.Row
	owner *authStoreQueryBarrier
}

func (row *authStoreBarrierRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err == nil {
		row.owner.once.Do(func() {
			close(row.owner.observed)
			<-row.owner.release
		})
	}
	return err
}

type authStoreBarrierRows struct {
	pgx.Rows
	owner *authStoreQueryBarrier
}

func (rows *authStoreBarrierRows) Next() bool {
	next := rows.Rows.Next()
	if !next {
		rows.owner.once.Do(func() {
			close(rows.owner.observed)
			<-rows.owner.release
		})
	}
	return next
}
