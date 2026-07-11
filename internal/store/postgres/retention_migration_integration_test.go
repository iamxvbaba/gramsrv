package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/deploy"
	"telesrv/internal/domain"
)

func TestRetentionDownMigrationsRejectAdvancedFloorsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	userID := createRevokeTestUser(t, ctx, pool, "retention-down-guard")
	channel, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         "retention down guard",
		Megagroup:     true,
		Date:          1_700_030_000,
	})
	if err != nil {
		t.Fatalf("create guarded channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channel.Channel.ID)
	})

	for _, test := range []struct {
		name      string
		migration string
		advance   func(context.Context, interface {
			Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
		}) error
	}{
		{
			name:      "channel",
			migration: "migrations/0063_channel_update_retention.down.sql",
			advance: func(ctx context.Context, tx interface {
				Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
			}) error {
				_, err := tx.Exec(ctx, `
UPDATE channel_update_checkpoints
SET retained_through_pts = 1
WHERE channel_id = $1`, channel.Channel.ID)
				return err
			},
		},
		{
			name:      "user",
			migration: "migrations/0064_user_update_retention.down.sql",
			advance: func(ctx context.Context, tx interface {
				Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
			}) error {
				_, err := tx.Exec(ctx, `
INSERT INTO user_update_retention (user_id, retained_through_pts, retained_through_date)
VALUES ($1, 1, 1)
ON CONFLICT (user_id) DO UPDATE SET
  retained_through_pts = 1,
  retained_through_date = 1`, userID)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			downSQL, err := deploy.Migrations.ReadFile(test.migration)
			if err != nil {
				t.Fatalf("read %s: %v", test.migration, err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin guarded down migration: %v", err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if err := test.advance(ctx, tx); err != nil {
				t.Fatalf("advance retained floor: %v", err)
			}
			if _, err := tx.Exec(ctx, string(downSQL)); err == nil {
				t.Fatalf("%s succeeded with retained floor > 0", test.migration)
			} else {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
					t.Fatalf("%s error = %v, want SQLSTATE 55000", test.migration, err)
				}
			}
		})
	}
}

func TestPerformanceMigrationIndexesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// 0072 must reuse the base schema's unique key for its durable-head FK. A second identical
	// unique index would add outbox enqueue/delete write amplification without improving lookup.
	// Likewise 0067 supersedes the transitional created_at orphan-GC index with last_used_at.
	var (
		baseOutboxUnique, duplicateOutboxUnique bool
		lastUsedAuthIndex, obsoleteAuthIndex    bool
		pendingHeadIndex, staleHeadIndex        bool
		poisonHeadIndex                         bool
		tempExpiryIndex                         bool
	)
	if err := pool.QueryRow(ctx, `
SELECT
  to_regclass('public.dispatch_outbox_target_user_id_id_key') IS NOT NULL,
  to_regclass('public.dispatch_outbox_target_id_uidx') IS NOT NULL,
  to_regclass('public.auth_keys_orphan_last_used_idx') IS NOT NULL,
  to_regclass('public.auth_keys_orphan_retention_idx') IS NOT NULL,
  to_regclass('public.dispatch_outbox_user_heads_pending_shard_idx') IS NOT NULL,
  to_regclass('public.dispatch_outbox_user_heads_dispatching_shard_idx') IS NOT NULL,
  to_regclass('public.dispatch_outbox_user_heads_failed_cleanup_idx') IS NOT NULL,
  to_regclass('public.temp_auth_key_bindings_expiry_idx') IS NOT NULL
`).Scan(
		&baseOutboxUnique,
		&duplicateOutboxUnique,
		&lastUsedAuthIndex,
		&obsoleteAuthIndex,
		&pendingHeadIndex,
		&staleHeadIndex,
		&poisonHeadIndex,
		&tempExpiryIndex,
	); err != nil {
		t.Fatalf("inspect performance migration indexes: %v", err)
	}
	if !baseOutboxUnique || duplicateOutboxUnique {
		t.Fatalf("outbox target/id indexes base=%v duplicate=%v, want true/false", baseOutboxUnique, duplicateOutboxUnique)
	}
	if !lastUsedAuthIndex || obsoleteAuthIndex {
		t.Fatalf("auth orphan indexes last_used=%v created_at=%v, want true/false", lastUsedAuthIndex, obsoleteAuthIndex)
	}
	if !pendingHeadIndex || !staleHeadIndex || !poisonHeadIndex || !tempExpiryIndex {
		t.Fatalf("ready/expiry indexes pending=%v stale=%v poison=%v temp_expiry=%v, want all true", pendingHeadIndex, staleHeadIndex, poisonHeadIndex, tempExpiryIndex)
	}
}
