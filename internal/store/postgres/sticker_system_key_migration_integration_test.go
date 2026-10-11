package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/deploy"
)

const (
	stickerSystemKeyMigrationUp   = "migrations/0183_sticker_set_system_key_unique.up.sql"
	stickerSystemKeyMigrationDown = "migrations/0183_sticker_set_system_key_unique.down.sql"
	synthesizedDefaultStatusSetID = int64(7_777_000_000_000_001)
)

func TestStickerSetSystemKeyMigrationCanonicalizesAndRejectsDuplicatesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	upSQL, err := deploy.Migrations.ReadFile(stickerSystemKeyMigrationUp)
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	downSQL, err := deploy.Migrations.ReadFile(stickerSystemKeyMigrationDown)
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin sticker system-key migration test: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("return system-key index to non-unique form: %v", err)
	}
	const (
		realSetID       = int64(9_100_000_000_018_101)
		thirdSetID      = int64(9_100_000_000_018_102)
		defaultStatuses = "emoji_default_statuses"
	)
	if _, err := tx.Exec(ctx, `UPDATE public.sticker_sets SET system_key='' WHERE system_key=$1`, defaultStatuses); err != nil {
		t.Fatalf("isolate existing default-status fixture keys: %v", err)
	}
	if _, err := tx.Exec(ctx, `
DELETE FROM public.sticker_sets
WHERE id IN ($1,$2,$3)`, synthesizedDefaultStatusSetID, realSetID, thirdSetID); err != nil {
		t.Fatalf("clean migration fixtures: %v", err)
	}
	for _, fixture := range []struct {
		id        int64
		shortName string
	}{
		{id: synthesizedDefaultStatusSetID, shortName: "TelesrvDefaultStatusesMigrationTest"},
		{id: realSetID, shortName: "StatusPackMigrationTest"},
	} {
		if _, err := tx.Exec(ctx, `
INSERT INTO public.sticker_sets(id,access_hash,short_name,title,count,hash,set_kind,system_key)
VALUES ($1,$1,$2,$2,0,1,'system',$3)`, fixture.id, fixture.shortName, defaultStatuses); err != nil {
			t.Fatalf("insert duplicate system-key fixture %d: %v", fixture.id, err)
		}
	}

	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("apply sticker system-key uniqueness migration: %v", err)
	}
	var synthesizedKey, realKey string
	if err := tx.QueryRow(ctx, `SELECT system_key FROM public.sticker_sets WHERE id=$1`, synthesizedDefaultStatusSetID).Scan(&synthesizedKey); err != nil {
		t.Fatalf("read synthesized set after migration: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT system_key FROM public.sticker_sets WHERE id=$1`, realSetID).Scan(&realKey); err != nil {
		t.Fatalf("read real set after migration: %v", err)
	}
	if synthesizedKey != "" || realKey != defaultStatuses {
		t.Fatalf("canonical system keys = synthesized %q real %q", synthesizedKey, realKey)
	}

	_, err = tx.Exec(ctx, `
INSERT INTO public.sticker_sets(id,access_hash,short_name,title,count,hash,set_kind,system_key)
VALUES ($1,$1,'ThirdStatusPackMigrationTest','ThirdStatusPackMigrationTest',0,1,'system',$2)`, thirdSetID, defaultStatuses)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgerrcode.UniqueViolation || pgErr.ConstraintName != "sticker_sets_system_key_idx" {
		t.Fatalf("duplicate non-empty system key error = %v, want sticker_sets_system_key_idx unique violation", err)
	}
}
