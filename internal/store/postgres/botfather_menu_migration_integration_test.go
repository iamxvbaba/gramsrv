package postgres

import (
	"context"
	"testing"

	"telesrv/deploy"
)

func TestBotFatherMenuMigrationPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE bots SET commands='[
 {"command":"custom","description":"keep this","extra":true},
 {"command":"mybots","description":"list your bots"},
 {"command":"token","description":"old token"},
 {"command":"revoke","description":"old revoke"}
]'::jsonb WHERE bot_user_id=93372553;
UPDATE users SET bot_info_version=17 WHERE id=93372553;`)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := deploy.Migrations.ReadFile("migrations/0260_botfather_delete_command.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		result, err := tx.Exec(ctx, string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if result.RowsAffected() != int64(1-pass) {
			t.Fatalf("pass %d affected %d rows", pass, result.RowsAffected())
		}
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT bot_info_version=18 AND commands='[
 {"command":"custom","description":"keep this","extra":true},
 {"command":"mybots","description":"edit your bots"},
 {"command":"token","description":"get authorization token"},
 {"command":"revoke","description":"revoke bot access token"},
 {"command":"deletebot","description":"delete a bot"}
]'::jsonb FROM bots JOIN users ON users.id=bots.bot_user_id WHERE users.id=93372553`).Scan(&valid)
	if err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("menu migration lost unrelated commands or changed its version more than once")
	}
}
