package postgres

import (
	"context"
	"testing"

	"telesrv/deploy"
)

func TestStickerDocumentReferenceMigrationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Shadow real tables so migration fixtures cannot touch account or media data.
	_, err = tx.Exec(ctx, `
CREATE TEMP TABLE sticker_sets(id bigint PRIMARY KEY, access_hash bigint) ON COMMIT DROP;
CREATE TEMP TABLE documents(id bigint PRIMARY KEY, attributes jsonb) ON COMMIT DROP;
INSERT INTO sticker_sets VALUES (2562980648230846492,-4380212461388408925);
INSERT INTO documents VALUES (1,'[
 {"kind":"filename","filename":"sample.tgs"},
 {"kind":"sticker","sticker_set_id":2562980648230846492,"sticker_set_access_hash":1318408154723431293,"alt":"hello"},
 {"kind":"custom_emoji","sticker_set_id":2562980648230846492,"sticker_set_access_hash":1},
 {"kind":"sticker","sticker_set_id":99,"sticker_set_access_hash":123}
]'),(2,'[]');`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := deploy.Migrations.ReadFile("migrations/0259_sticker_document_set_references.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		result, err := tx.Exec(ctx, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if result.RowsAffected() != int64(1-i) {
			t.Fatalf("pass %d: affected=%d", i, result.RowsAffected())
		}
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT attributes = '[
 {"kind":"filename","filename":"sample.tgs"},
 {"kind":"sticker","sticker_set_id":2562980648230846492,"sticker_set_access_hash":-4380212461388408925,"alt":"hello"},
 {"kind":"custom_emoji","sticker_set_id":2562980648230846492,"sticker_set_access_hash":-4380212461388408925},
 {"kind":"sticker","sticker_set_id":99,"sticker_set_access_hash":123}
]'::jsonb FROM documents WHERE id=1`).Scan(&valid)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("migration changed attribute order, unrelated fields or unknown reference")
	}
}
