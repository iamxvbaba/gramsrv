package postgres

import (
	"context"
	"testing"

	"telesrv/deploy"
)

func TestSendReplaySnapshotMigrationRoundTripPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	downSQL, err := deploy.Migrations.ReadFile("migrations/0073_send_replay_snapshots.down.sql")
	if err != nil {
		t.Fatalf("read 0073 down: %v", err)
	}
	upSQL, err := deploy.Migrations.ReadFile("migrations/0073_send_replay_snapshots.up.sql")
	if err != nil {
		t.Fatalf("read 0073 up: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration round trip: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, string(downSQL)); err != nil {
		t.Fatalf("0073 down: %v", err)
	}
	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("0073 up: %v", err)
	}
	var privateSnapshot, channelSnapshot, privateDeleteIDs, channelDeleteIDs bool
	if err := tx.QueryRow(ctx, `
SELECT
  EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='private_messages' AND column_name='sender_snapshot'),
  EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_messages' AND column_name='send_snapshot'),
  EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='private_messages' AND column_name='sender_delete_message_ids'),
  EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='channel_messages' AND column_name='delete_message_ids')
`).Scan(&privateSnapshot, &channelSnapshot, &privateDeleteIDs, &channelDeleteIDs); err != nil {
		t.Fatalf("inspect 0073 columns: %v", err)
	}
	if !privateSnapshot || !channelSnapshot || !privateDeleteIDs || !channelDeleteIDs {
		t.Fatalf("0073 columns private/channel snapshot=%v/%v delete_ids=%v/%v, want all true", privateSnapshot, channelSnapshot, privateDeleteIDs, channelDeleteIDs)
	}
}
