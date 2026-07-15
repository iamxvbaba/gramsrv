package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/internal/store"
	"telesrv/internal/store/postgres/sqlcgen"
)

const (
	// authIdentityAdvisoryNamespace is deliberately a two-int advisory-lock
	// namespace. PostgreSQL keeps it disjoint from the one-bigint advisory locks
	// used elsewhere in the store. AUTH in ASCII is stable and recognizable in
	// pg_locks diagnostics.
	authIdentityAdvisoryNamespace int32 = 0x41555448
	authIdentityTxMaxAttempts           = 3
)

var errAuthIdentityChanged = errors.New("auth key permanent identity changed while acquiring locks")

type authKeyIdentityHint struct {
	found       bool
	expiresAt   int
	bound       bool
	permID      int64
	identityID  int64
	hasIdentity bool
}

// withAuthIdentityTx gives identity-sensitive stores an explicit transaction
// boundary. Application-level identity visibility changes are retried after a
// savepoint/transaction rollback. PostgreSQL deadlock/serialization retries are
// only safe when this store owns the top-level transaction; an injected pgx.Tx
// is never silently replayed after 40P01/40001.
func withAuthIdentityTx(
	ctx context.Context,
	db sqlcgen.DBTX,
	op string,
	fn func(pgx.Tx) error,
) error {
	_, embedded := db.(pgx.Tx)
	var lastErr error
	for attempt := 0; attempt < authIdentityTxMaxAttempts; attempt++ {
		err := withTx(ctx, db, op, fn)
		if err == nil {
			return nil
		}
		lastErr = err
		switch {
		case errors.Is(err, errAuthIdentityChanged):
			// The attempt owns a nested savepoint even for an injected pgx.Tx,
			// so all row/advisory locks from the stale hint have been released
			// before the next READ COMMITTED statement snapshot is taken.
			continue
		case !embedded && isAuthIdentityRetryableDatabaseError(err):
			// Defensive retry only. The identity gate is the deadlock fix; this
			// does not substitute for the global lock order.
			continue
		default:
			return err
		}
	}
	return fmt.Errorf("%s did not stabilize after %d attempts: %w", op, authIdentityTxMaxAttempts, lastErr)
}

func isAuthIdentityRetryableDatabaseError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40P01" || pgErr.Code == "40001")
}

// lockPermanentAuthIdentities acquires the complete batch before any auth-key,
// binding, authorization, or update-state row lock. Ordering is by the final
// int32 hashint8 key, not by the source bigint identity: hash collisions are
// intentionally one lock and cannot create an opposite acquisition order.
func lockPermanentAuthIdentities(ctx context.Context, tx pgx.Tx, permIDs []int64) error {
	if len(permIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
SELECT DISTINCT hashint8(identity_id)::integer AS lock_key
FROM unnest($1::bigint[]) AS identities(identity_id)
ORDER BY lock_key`, permIDs)
	if err != nil {
		return fmt.Errorf("derive permanent auth identity lock keys: %w", err)
	}
	lockKeys := make([]int32, 0, len(permIDs))
	for rows.Next() {
		var key int32
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return fmt.Errorf("scan permanent auth identity lock key: %w", err)
		}
		lockKeys = append(lockKeys, key)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate permanent auth identity lock keys: %w", err)
	}
	rows.Close()
	for _, key := range lockKeys {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1::integer, $2::integer)`, authIdentityAdvisoryNamespace, key); err != nil {
			return fmt.Errorf("lock permanent auth identity %d: %w", key, err)
		}
	}
	return nil
}

// lookupAuthKeyIdentityHint is intentionally lock-free. A positive-expiry raw
// key has no permanent identity until a binding is committed; a permanent raw
// key is its own identity. Callers must re-read after the raw row is locked.
func lookupAuthKeyIdentityHint(ctx context.Context, tx pgx.Tx, rawID int64) (authKeyIdentityHint, error) {
	var hint authKeyIdentityHint
	err := tx.QueryRow(ctx, `
/* auth_identity_hint */
SELECT key.expires_at,
       binding.temp_auth_key_id IS NOT NULL,
       COALESCE(binding.perm_auth_key_id, 0)
FROM auth_keys AS key
LEFT JOIN temp_auth_key_bindings AS binding
  ON binding.temp_auth_key_id = key.auth_key_id
WHERE key.auth_key_id = $1`, rawID).Scan(&hint.expiresAt, &hint.bound, &hint.permID)
	if errors.Is(err, pgx.ErrNoRows) {
		return authKeyIdentityHint{}, nil
	}
	if err != nil {
		return authKeyIdentityHint{}, fmt.Errorf("resolve auth key identity hint: %w", err)
	}
	hint.found = true
	switch {
	case hint.bound:
		hint.identityID = hint.permID
		hint.hasIdentity = true
	case hint.expiresAt == 0:
		hint.identityID = rawID
		hint.hasIdentity = true
	}
	return hint, nil
}

// lockRawAuthKeyInIdentityOrder establishes the only cross-identity row-lock
// order used by bind, selector advance and direct key deletion:
//
//	permanent identity advisory gate -> raw auth-key row -> permanent row
//
// If an initially-unbound temp key becomes bound before the raw lock is
// acquired, taking its newly discovered identity advisory lock at that point
// would recreate raw->identity inversion. The caller must roll back and retry.
func lockRawAuthKeyInIdentityOrder(
	ctx context.Context,
	tx pgx.Tx,
	rawID int64,
) (rawExpiry int, permID int64, bound bool, err error) {
	hint, err := lookupAuthKeyIdentityHint(ctx, tx, rawID)
	if err != nil || !hint.found {
		if err != nil {
			return 0, 0, false, err
		}
		return 0, 0, false, store.ErrAuthKeyNotFound
	}
	if hint.hasIdentity {
		if err := lockPermanentAuthIdentities(ctx, tx, []int64{hint.identityID}); err != nil {
			return 0, 0, false, err
		}
	}
	if err := tx.QueryRow(ctx, `
SELECT expires_at
FROM auth_keys
WHERE auth_key_id = $1
FOR UPDATE`, rawID).Scan(&rawExpiry); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, false, store.ErrAuthKeyNotFound
		}
		return 0, 0, false, fmt.Errorf("lock raw auth key: %w", err)
	}

	var actualPermID int64
	err = tx.QueryRow(ctx, `
SELECT perm_auth_key_id
FROM temp_auth_key_bindings
WHERE temp_auth_key_id = $1`, rawID).Scan(&actualPermID)
	switch {
	case err == nil:
		bound = true
		permID = actualPermID
	case errors.Is(err, pgx.ErrNoRows):
		permID = rawID
	default:
		return 0, 0, false, fmt.Errorf("revalidate auth key permanent identity: %w", err)
	}

	actualHasIdentity := bound || rawExpiry == 0
	actualIdentityID := permID
	if actualHasIdentity != hint.hasIdentity ||
		(actualHasIdentity && actualIdentityID != hint.identityID) ||
		bound != hint.bound || rawExpiry != hint.expiresAt {
		return 0, 0, false, errAuthIdentityChanged
	}
	if !bound {
		return rawExpiry, permID, false, nil
	}

	var permExpiry int
	if err := tx.QueryRow(ctx, `
SELECT expires_at
FROM auth_keys
WHERE auth_key_id = $1
FOR UPDATE`, permID).Scan(&permExpiry); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, false, store.ErrAuthKeyBindingInvalid
		}
		return 0, 0, false, fmt.Errorf("lock permanent auth key: %w", err)
	}
	if rawExpiry <= 0 || permExpiry != 0 {
		return 0, 0, false, store.ErrAuthKeyBindingInvalid
	}
	return rawExpiry, permID, true, nil
}
