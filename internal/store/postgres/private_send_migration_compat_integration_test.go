package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"telesrv/deploy"
	"telesrv/internal/domain"
	storepkg "telesrv/internal/store"
)

func TestPrivateSendFreshMigrationsKeepLegacyWriterDefaults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		migration string
		adds      []string
		drops     []string
	}{
		{
			migration: "migrations/0062_private_message_idempotency.up.sql",
			adds: []string{
				"ADD COLUMN request_fingerprint bytea NOT NULL DEFAULT '\\x'",
				"ADD COLUMN recipient_delivered boolean NOT NULL DEFAULT false",
			},
			drops: []string{
				"ALTER COLUMN request_fingerprint DROP DEFAULT",
				"ALTER COLUMN recipient_delivered DROP DEFAULT",
			},
		},
		{
			migration: "migrations/0068_private_message_send_receipt.up.sql",
			adds: []string{
				"ADD COLUMN sender_box_id integer NOT NULL DEFAULT 0",
				"ADD COLUMN sender_pts integer NOT NULL DEFAULT 0",
				"ADD COLUMN recipient_box_id integer NOT NULL DEFAULT 0",
				"ADD COLUMN recipient_pts integer NOT NULL DEFAULT 0",
			},
			drops: []string{
				"ALTER COLUMN sender_box_id DROP DEFAULT",
				"ALTER COLUMN sender_pts DROP DEFAULT",
				"ALTER COLUMN recipient_box_id DROP DEFAULT",
				"ALTER COLUMN recipient_pts DROP DEFAULT",
			},
		},
	} {
		t.Run(test.migration, func(t *testing.T) {
			sql, err := deploy.Migrations.ReadFile(test.migration)
			if err != nil {
				t.Fatalf("read %s: %v", test.migration, err)
			}
			body := string(sql)
			for _, add := range test.adds {
				if !strings.Contains(body, add) {
					t.Errorf("%s does not install legacy-writer default %q", test.migration, add)
				}
			}
			for _, drop := range test.drops {
				if strings.Contains(body, drop) {
					t.Errorf("%s removes legacy-writer default %q", test.migration, drop)
				}
			}
		})
	}
}

func TestPrivateSendMigration75To77PreservesOldWritersAndRejectsUnknownReceiptsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1888"+suffix+"01", "MigrationSender", "")
	recipient := createTestUser(t, ctx, users, "+1888"+suffix+"02", "MigrationRecipient", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration compatibility tx: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Recreate the schema shape left by the original 0062/0068 migrations at
	// version 75, then apply the corrective expand migration in isolation.
	if _, err := tx.Exec(ctx, `
ALTER TABLE public.private_messages
    ALTER COLUMN request_fingerprint DROP DEFAULT,
    ALTER COLUMN recipient_delivered DROP DEFAULT,
    ALTER COLUMN sender_box_id DROP DEFAULT,
    ALTER COLUMN sender_pts DROP DEFAULT,
    ALTER COLUMN recipient_box_id DROP DEFAULT,
    ALTER COLUMN recipient_pts DROP DEFAULT`); err != nil {
		t.Fatalf("simulate version 75 defaults: %v", err)
	}
	upSQL, err := deploy.Migrations.ReadFile("migrations/0077_correct_private_send_defaults.up.sql")
	if err != nil {
		t.Fatalf("read 0077 up: %v", err)
	}
	if _, err := tx.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("apply 0077 up: %v", err)
	}

	var defaultCount int
	if err := tx.QueryRow(ctx, `
SELECT count(*)
FROM information_schema.columns
WHERE table_schema = 'public'
  AND table_name = 'private_messages'
  AND column_name = ANY($1::text[])
  AND column_default IS NOT NULL`, []string{
		"request_fingerprint", "recipient_delivered", "sender_box_id",
		"sender_pts", "recipient_box_id", "recipient_pts",
	}).Scan(&defaultCount); err != nil {
		t.Fatalf("inspect restored defaults: %v", err)
	}
	if defaultCount != 6 {
		t.Fatalf("restored private-send defaults = %d, want 6", defaultCount)
	}

	insertLegacyBoxes := func(privateMessageID int64, boxID, pts, date int, body string) {
		t.Helper()
		if _, err := tx.Exec(ctx, `
INSERT INTO message_boxes (
  owner_user_id, box_id, private_message_id, message_sender_id,
  peer_type, peer_id, from_user_id, message_date, outgoing, body, entities, pts
) VALUES
  ($1, $3, $5, $1, 'user', $2, $1, $6, true,  $7, '[]'::jsonb, $4),
  ($2, $3, $5, $1, 'user', $1, $1, $6, false, $7, '[]'::jsonb, $4)`,
			sender.ID, recipient.ID, boxID, pts, privateMessageID, date, body); err != nil {
			t.Fatalf("insert legacy message boxes: %v", err)
		}
	}

	pre0062 := domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID,
		RandomID: 887_001, Message: "pre-0062 writer", Date: 1_700_040_001,
	}
	var pre0062ID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO private_messages (
  sender_user_id, recipient_user_id, random_id, message_date, body, entities
) VALUES ($1, $2, $3, $4, $5, '[]'::jsonb)
RETURNING id`, pre0062.SenderUserID, pre0062.RecipientUserID, pre0062.RandomID, pre0062.Date, pre0062.Message).Scan(&pre0062ID); err != nil {
		t.Fatalf("pre-0062 INSERT after migration: %v", err)
	}
	insertLegacyBoxes(pre0062ID, 1, 1, pre0062.Date, pre0062.Message)
	assertLegacyPrivateSendSentinels(t, ctx, tx, pre0062ID, false)
	if _, err := NewMessageStore(tx).SendPrivateText(ctx, pre0062); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
		t.Fatalf("pre-0062 unknown replay err = %v, want ErrMessageRandomIDDuplicate", err)
	}

	era0062 := domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID,
		RandomID: 887_002, Message: "0062-era writer", Date: 1_700_040_002,
	}
	fingerprint, err := storepkg.PrivateSendFingerprint(era0062)
	if err != nil {
		t.Fatalf("fingerprint 0062-era request: %v", err)
	}
	var era0062ID int64
	if err := tx.QueryRow(ctx, `
INSERT INTO private_messages (
  sender_user_id, recipient_user_id, random_id, request_fingerprint,
  recipient_delivered, message_date, body, entities
) VALUES ($1, $2, $3, $4, true, $5, $6, '[]'::jsonb)
RETURNING id`, era0062.SenderUserID, era0062.RecipientUserID, era0062.RandomID,
		fingerprint, era0062.Date, era0062.Message).Scan(&era0062ID); err != nil {
		t.Fatalf("0062-era INSERT after migration: %v", err)
	}
	insertLegacyBoxes(era0062ID, 2, 2, era0062.Date, era0062.Message)
	assertLegacyPrivateSendSentinels(t, ctx, tx, era0062ID, true)
	if _, err := NewMessageStore(tx).SendPrivateText(ctx, era0062); err == nil ||
		errors.Is(err, domain.ErrMessageRandomIDDuplicate) ||
		!strings.Contains(err.Error(), "invalid immutable sender receipt") {
		t.Fatalf("0062-era unknown receipt err = %v, want explicit invalid receipt failure", err)
	}

	var privateRows, eventRows, outboxRows int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM private_messages WHERE sender_user_id = $1`, sender.ID).Scan(&privateRows); err != nil {
		t.Fatalf("count legacy private rows: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM user_update_events WHERE user_id = ANY($1::bigint[])`, []int64{sender.ID, recipient.ID}).Scan(&eventRows); err != nil {
		t.Fatalf("count legacy replay events: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM dispatch_outbox WHERE target_user_id = ANY($1::bigint[])`, []int64{sender.ID, recipient.ID}).Scan(&outboxRows); err != nil {
		t.Fatalf("count legacy replay outbox: %v", err)
	}
	if privateRows != 2 || eventRows != 0 || outboxRows != 0 {
		t.Fatalf("legacy replay facts private/events/outbox = %d/%d/%d, want 2/0/0", privateRows, eventRows, outboxRows)
	}
}

func assertLegacyPrivateSendSentinels(t *testing.T, ctx context.Context, q pgx.Tx, id int64, delivered bool) {
	t.Helper()
	var (
		fingerprint                                          []byte
		gotDelivered                                         bool
		senderBoxID, senderPts, recipientBoxID, recipientPts int
	)
	if err := q.QueryRow(ctx, `
SELECT request_fingerprint, recipient_delivered,
       sender_box_id, sender_pts, recipient_box_id, recipient_pts
FROM private_messages
WHERE id = $1`, id).Scan(
		&fingerprint, &gotDelivered,
		&senderBoxID, &senderPts, &recipientBoxID, &recipientPts,
	); err != nil {
		t.Fatalf("load legacy private-send sentinels: %v", err)
	}
	if (!delivered && (len(fingerprint) != 0 || gotDelivered)) ||
		senderBoxID != 0 || senderPts != 0 || recipientBoxID != 0 || recipientPts != 0 {
		t.Fatalf("legacy sentinels fingerprint=%x delivered=%v receipt=%d/%d/%d/%d",
			fingerprint, gotDelivered, senderBoxID, senderPts, recipientBoxID, recipientPts)
	}
	if delivered && (len(fingerprint) != 32 || !gotDelivered) {
		t.Fatalf("0062-era fingerprint/delivery = %x/%v, want 32 bytes/true", fingerprint, gotDelivered)
	}
}
