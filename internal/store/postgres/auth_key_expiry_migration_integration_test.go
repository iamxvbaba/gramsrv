package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/deploy"
)

const (
	authKeyExpiryMigrationUp   = "migrations/0086_auth_key_protocol_expiry.up.sql"
	authKeyExpiryMigrationDown = "migrations/0086_auth_key_protocol_expiry.down.sql"
)

func TestAuthKeyProtocolExpiryMigrationBackfillAndRollbackPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	upSQL := readAuthKeyExpiryMigration(t, authKeyExpiryMigrationUp)
	downSQL := readAuthKeyExpiryMigration(t, authKeyExpiryMigrationDown)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin auth-key expiry migration test: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, downSQL); err != nil {
		t.Fatalf("return schema to 0085: %v", err)
	}

	base := authKeyExpiryMigrationBaseID()
	tempKeyID := base
	permKeyID := base - 1
	authorizedPermKeyID := base - 2
	unknownKeyID := base - 3
	userID := base - 4
	const tempExpiresAt = 1_800_086_000

	for _, authKeyID := range []int64{tempKeyID, permKeyID, authorizedPermKeyID, unknownKeyID} {
		insertAuthKeyExpiryMigrationKey(t, ctx, tx, authKeyID)
	}
	insertAuthKeyExpiryMigrationBinding(t, ctx, tx, tempKeyID, permKeyID, tempExpiresAt)
	insertAuthKeyExpiryMigrationUser(t, ctx, tx, userID)
	if _, err := tx.Exec(ctx, `
INSERT INTO public.authorizations (auth_key_id, user_id, hash)
VALUES ($1, $2, $3)`, authorizedPermKeyID, userID, base-5); err != nil {
		t.Fatalf("insert authorized permanent key fixture: %v", err)
	}

	if _, err := tx.Exec(ctx, upSQL); err != nil {
		t.Fatalf("apply auth-key expiry migration: %v", err)
	}

	for _, test := range []struct {
		name      string
		authKeyID int64
		want      int
	}{
		{name: "bound temporary", authKeyID: tempKeyID, want: tempExpiresAt},
		{name: "binding permanent", authKeyID: permKeyID, want: 0},
		{name: "authorized permanent", authKeyID: authorizedPermKeyID, want: 0},
		{name: "unclassified legacy", authKeyID: unknownKeyID, want: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got int
			if err := tx.QueryRow(ctx, `SELECT expires_at FROM public.auth_keys WHERE auth_key_id = $1`, test.authKeyID).Scan(&got); err != nil {
				t.Fatalf("read expires_at: %v", err)
			}
			if got != test.want {
				t.Fatalf("expires_at = %d, want %d", got, test.want)
			}
		})
	}

	var (
		indexPredicate string
		indexValid     bool
	)
	if err := tx.QueryRow(ctx, `
SELECT pg_get_expr(i.indpred, i.indrelid), i.indisvalid
FROM pg_catalog.pg_index AS i
JOIN pg_catalog.pg_class AS c ON c.oid = i.indexrelid
JOIN pg_catalog.pg_namespace AS n ON n.oid = c.relnamespace
WHERE n.nspname = 'public'
  AND c.relname = 'auth_keys_temporary_expiry_seek_idx'`).Scan(&indexPredicate, &indexValid); err != nil {
		t.Fatalf("inspect temporary expiry partial index: %v", err)
	}
	if !indexValid || !strings.Contains(indexPredicate, "expires_at > 0") {
		t.Fatalf("temporary expiry index valid=%v predicate=%q, want valid partial expires_at > 0", indexValid, indexPredicate)
	}

	var (
		deleteAction string
		fkValidated  bool
	)
	if err := tx.QueryRow(ctx, `
SELECT c.confdeltype::text, c.convalidated
FROM pg_catalog.pg_constraint AS c
WHERE c.conrelid = 'public.temp_auth_key_bindings'::regclass
  AND c.conname = 'temp_auth_key_bindings_perm_auth_key_id_fkey'`).Scan(&deleteAction, &fkValidated); err != nil {
		t.Fatalf("inspect permanent auth-key FK: %v", err)
	}
	if deleteAction != "r" || !fkValidated {
		t.Fatalf("permanent auth-key FK delete action=%q validated=%v, want RESTRICT/true", deleteAction, fkValidated)
	}

	assertAuthKeyExpiryMigrationForeignKeyViolation(t, ctx, tx, func(nested pgx.Tx) error {
		_, err := nested.Exec(ctx, `DELETE FROM public.auth_keys WHERE auth_key_id = $1`, permKeyID)
		return err
	})
	assertAuthKeyExpiryMigrationForeignKeyViolation(t, ctx, tx, func(nested pgx.Tx) error {
		_, err := nested.Exec(ctx, `
INSERT INTO public.temp_auth_key_bindings (
  temp_auth_key_id, perm_auth_key_id, nonce, expires_at, encrypted_message, temp_session_id
) VALUES ($1, $2, 86, $3, decode('86', 'hex'), 86)`, unknownKeyID, base-86, tempExpiresAt)
		return err
	})

	if _, err := tx.Exec(ctx, downSQL); err != nil {
		t.Fatalf("roll back auth-key expiry migration: %v", err)
	}

	var (
		expiresColumnExists bool
		expiryIndexExists   bool
		permFKExists        bool
		fixtureKeyCount     int
	)
	if err := tx.QueryRow(ctx, `
SELECT
  EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_schema = 'public' AND table_name = 'auth_keys' AND column_name = 'expires_at'
  ),
  to_regclass('public.auth_keys_temporary_expiry_seek_idx') IS NOT NULL,
  EXISTS (
    SELECT 1 FROM pg_catalog.pg_constraint
    WHERE conrelid = 'public.temp_auth_key_bindings'::regclass
      AND conname = 'temp_auth_key_bindings_perm_auth_key_id_fkey'
  ),
  (SELECT count(*) FROM public.auth_keys WHERE auth_key_id = ANY($1::bigint[]))
`, []int64{tempKeyID, permKeyID, authorizedPermKeyID, unknownKeyID}).Scan(
		&expiresColumnExists,
		&expiryIndexExists,
		&permFKExists,
		&fixtureKeyCount,
	); err != nil {
		t.Fatalf("inspect 0086 down result: %v", err)
	}
	if expiresColumnExists || expiryIndexExists || permFKExists || fixtureKeyCount != 4 {
		t.Fatalf("0086 down result column=%v index=%v fk=%v keys=%d, want false/false/false/4", expiresColumnExists, expiryIndexExists, permFKExists, fixtureKeyCount)
	}
}

func TestAuthKeyProtocolExpiryMigrationRejectsInvalidIdentityStatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	upSQL := readAuthKeyExpiryMigration(t, authKeyExpiryMigrationUp)
	downSQL := readAuthKeyExpiryMigration(t, authKeyExpiryMigrationDown)

	tests := []struct {
		name        string
		wantMessage string
		setup       func(*testing.T, context.Context, pgx.Tx, int64)
	}{
		{
			name:        "nonpositive binding expiry",
			wantMessage: "invalid non-positive temporary auth key expiry",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base)
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base-1)
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base, base-1, 0)
			},
		},
		{
			name:        "dangling permanent key",
			wantMessage: "temporary auth key binding references missing permanent key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base)
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base, base-1, 1_800_086_001)
			},
		},
		{
			name:        "self binding",
			wantMessage: "temporary auth key self-binding",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base)
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base, base, 1_800_086_002)
			},
		},
		{
			name:        "temporary and permanent role overlap",
			wantMessage: "auth key appears in both temporary and permanent roles",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				for _, authKeyID := range []int64{base, base - 1, base - 2} {
					insertAuthKeyExpiryMigrationKey(t, ctx, tx, authKeyID)
				}
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base, base-1, 1_800_086_003)
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base-1, base-2, 1_800_086_004)
			},
		},
		{
			name:        "authorization on bound temporary key",
			wantMessage: "invalid authorization on temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base)
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base-1)
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base, base-1, 1_800_086_005)
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, base-2)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.authorizations (auth_key_id, user_id, hash)
VALUES ($1, $2, $3)`, base, base-2, base-3); err != nil {
					t.Fatalf("insert temporary-key authorization fixture: %v", err)
				}
			},
		},
		{
			name:        "update state on bound temporary key",
			wantMessage: "update state references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base)
				insertAuthKeyExpiryMigrationKey(t, ctx, tx, base-1)
				insertAuthKeyExpiryMigrationBinding(t, ctx, tx, base, base-1, 1_800_086_006)
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, base-2)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.update_states (auth_key_id, user_id, pts, date)
VALUES ($1, $2, 1, 1)`, base, base-2); err != nil {
					t.Fatalf("insert temporary-key update state fixture: %v", err)
				}
			},
		},
		{
			name:        "bootstrap update job on bound temporary key",
			wantMessage: "bootstrap update job references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_007)
				userID := base - 2
				messageID := base - 3
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, userID)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.private_messages (
  id, sender_user_id, recipient_user_id, message_date, body
) VALUES ($1, $2, $2, 1, 'migration-0086')`, messageID, userID); err != nil {
					t.Fatalf("insert bootstrap private message fixture: %v", err)
				}
				if _, err := tx.Exec(ctx, `
INSERT INTO public.message_boxes (
  owner_user_id, box_id, private_message_id, message_sender_id,
  peer_type, peer_id, from_user_id, message_date, body
) VALUES ($1, 860086, $2, $1, 'user', $1, $1, 1, 'migration-0086')`, userID, messageID); err != nil {
					t.Fatalf("insert bootstrap message box fixture: %v", err)
				}
				if _, err := tx.Exec(ctx, `
INSERT INTO public.bootstrap_update_jobs (
  kind, user_id, auth_key_id, session_id, message_box_id
) VALUES ('login_message', $1, $2, 86, 860086)`, userID, base); err != nil {
					t.Fatalf("insert temporary-key bootstrap job fixture: %v", err)
				}
			},
		},
		{
			name:        "secret qts watermark on bound temporary key",
			wantMessage: "secret qts watermark references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_008)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.secret_qts_watermarks (auth_key_id, reserved_qts, confirmed_qts)
VALUES ($1, 1, 1)`, base); err != nil {
					t.Fatalf("insert temporary-key secret qts watermark fixture: %v", err)
				}
			},
		},
		{
			name:        "encrypted message queue on bound temporary key",
			wantMessage: "encrypted message queue references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_009)
				userID := base - 2
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, userID)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.encrypted_message_queue (
  receiver_auth_key_id, qts, receiver_user_id, chat_id, random_id, date, bytes
) VALUES ($1, 1, $2, 860086, $3, 1, decode('86', 'hex'))`, base, userID, base-3); err != nil {
					t.Fatalf("insert temporary-key encrypted message queue fixture: %v", err)
				}
			},
		},
		{
			name:        "encrypted state delivery on bound temporary key",
			wantMessage: "encrypted state delivery references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_010)
				userID := base - 2
				eventID := base - 3
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, userID)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.encrypted_state_events (
  id, target_user_id, target_auth_key_id, chat_id, event_type, date
) VALUES ($1, $2, $3, 860086, 1, 1)`, eventID, userID, base-1); err != nil {
					t.Fatalf("insert permanent-key encrypted state event fixture: %v", err)
				}
				if _, err := tx.Exec(ctx, `
INSERT INTO public.encrypted_state_event_delivery (event_id, auth_key_id)
VALUES ($1, $2)`, eventID, base); err != nil {
					t.Fatalf("insert temporary-key encrypted state delivery fixture: %v", err)
				}
			},
		},
		{
			name:        "encrypted state event on bound temporary key",
			wantMessage: "encrypted state event targets temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_011)
				userID := base - 2
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, userID)
				if _, err := tx.Exec(ctx, `
INSERT INTO public.encrypted_state_events (
  id, target_user_id, target_auth_key_id, chat_id, event_type, date
) VALUES ($1, $2, $3, 860086, 1, 1)`, base-3, userID, base); err != nil {
					t.Fatalf("insert temporary-key encrypted state event fixture: %v", err)
				}
			},
		},
		{
			name:        "secret chat admin on bound temporary key",
			wantMessage: "secret chat references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_012)
				adminUserID := base - 2
				participantUserID := base - 3
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, adminUserID)
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, participantUserID)
				insertAuthKeyExpiryMigrationSecretChat(t, ctx, tx, base, base-1, adminUserID, participantUserID)
			},
		},
		{
			name:        "secret chat participant on bound temporary key",
			wantMessage: "secret chat references temporary auth key",
			setup: func(t *testing.T, ctx context.Context, tx pgx.Tx, base int64) {
				insertAuthKeyExpiryMigrationBoundPair(t, ctx, tx, base, base-1, 1_800_086_013)
				adminUserID := base - 2
				participantUserID := base - 3
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, adminUserID)
				insertAuthKeyExpiryMigrationUser(t, ctx, tx, participantUserID)
				insertAuthKeyExpiryMigrationSecretChat(t, ctx, tx, base-1, base, adminUserID, participantUserID)
			},
		},
	}

	for i, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin invalid-state migration test: %v", err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()

			if _, err := tx.Exec(ctx, downSQL); err != nil {
				t.Fatalf("return schema to 0085: %v", err)
			}
			test.setup(t, ctx, tx, authKeyExpiryMigrationBaseID()-int64(i*100))

			_, err = tx.Exec(ctx, upSQL)
			if err == nil {
				t.Fatal("0086 migration accepted invalid identity state")
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, test.wantMessage) {
				t.Fatalf("0086 migration error = %v, want SQLSTATE P0001 containing %q", err, test.wantMessage)
			}
		})
	}
}

func readAuthKeyExpiryMigration(t *testing.T, name string) string {
	t.Helper()
	sql, err := deploy.Migrations.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(sql)
}

func authKeyExpiryMigrationBaseID() int64 {
	return -(time.Now().UnixNano() & 0x3fffffffffffffff)
}

func insertAuthKeyExpiryMigrationKey(t *testing.T, ctx context.Context, tx pgx.Tx, authKeyID int64) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
INSERT INTO public.auth_keys (auth_key_id, body, server_salt)
VALUES ($1, decode('86', 'hex'), 86)`, authKeyID); err != nil {
		t.Fatalf("insert auth key %d: %v", authKeyID, err)
	}
}

func insertAuthKeyExpiryMigrationBinding(t *testing.T, ctx context.Context, tx pgx.Tx, tempKeyID, permKeyID int64, expiresAt int) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
INSERT INTO public.temp_auth_key_bindings (
  temp_auth_key_id, perm_auth_key_id, nonce, expires_at, encrypted_message, temp_session_id
) VALUES ($1, $2, 86, $3, decode('86', 'hex'), 86)`, tempKeyID, permKeyID, expiresAt); err != nil {
		t.Fatalf("insert temp auth-key binding %d -> %d: %v", tempKeyID, permKeyID, err)
	}
}

func insertAuthKeyExpiryMigrationBoundPair(t *testing.T, ctx context.Context, tx pgx.Tx, tempKeyID, permKeyID int64, expiresAt int) {
	t.Helper()
	insertAuthKeyExpiryMigrationKey(t, ctx, tx, tempKeyID)
	insertAuthKeyExpiryMigrationKey(t, ctx, tx, permKeyID)
	insertAuthKeyExpiryMigrationBinding(t, ctx, tx, tempKeyID, permKeyID, expiresAt)
}

func insertAuthKeyExpiryMigrationUser(t *testing.T, ctx context.Context, tx pgx.Tx, userID int64) {
	t.Helper()
	phone := fmt.Sprintf("+860086%d", -userID)
	if _, err := tx.Exec(ctx, `
INSERT INTO public.users (id, access_hash, phone, first_name)
VALUES ($1, $2, $3, 'migration-0086')`, userID, userID-1, phone); err != nil {
		t.Fatalf("insert migration user %d: %v", userID, err)
	}
}

func insertAuthKeyExpiryMigrationSecretChat(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	adminAuthKeyID int64,
	participantAuthKeyID int64,
	adminUserID int64,
	participantUserID int64,
) {
	t.Helper()
	if _, err := tx.Exec(ctx, `
INSERT INTO public.secret_chats (
  chat_id, admin_access_hash, participant_access_hash,
  admin_user_id, admin_auth_key_id, participant_user_id, participant_auth_key_id,
  state, random_id, date
) VALUES (
  860086, 86, 87,
  $1, $2, $3, $4,
  'waiting', 86, 1
)`, adminUserID, adminAuthKeyID, participantUserID, participantAuthKeyID); err != nil {
		t.Fatalf("insert temporary-key secret chat fixture: %v", err)
	}
}

func assertAuthKeyExpiryMigrationForeignKeyViolation(t *testing.T, ctx context.Context, tx pgx.Tx, action func(pgx.Tx) error) {
	t.Helper()
	nested, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("begin FK assertion savepoint: %v", err)
	}
	defer func() { _ = nested.Rollback(context.Background()) }()

	err = action(nested)
	if err == nil {
		t.Fatal("operation bypassed permanent auth-key RESTRICT FK")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "temp_auth_key_bindings_perm_auth_key_id_fkey" {
		t.Fatalf("FK error = %v, want SQLSTATE 23503 from temp_auth_key_bindings_perm_auth_key_id_fkey", err)
	}
}
