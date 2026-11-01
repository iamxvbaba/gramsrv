package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestEncryptedQueueStorePostgres verifies the PG implementation of the secret-chat qts delivery
// queue: monotonic qts allocation (first value 1, no gaps), idempotent dedup (same random_id
// returns the existing qts/date), difference catch-up by qts seek, reserved/confirmed watermarks,
// and the ack flag. Gated on TELESRV_TEST_POSTGRES_DSN.
func TestEncryptedQueueStorePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewEncryptedQueueStore(pool)

	const device = int64(0x7700AA01)
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM encrypted_message_queue WHERE receiver_auth_key_id = $1`, device)
		_, _ = pool.Exec(ctx, `DELETE FROM secret_qts_watermarks WHERE auth_key_id = $1`, device)
	}
	cleanup()
	t.Cleanup(cleanup)

	mk := func(randomID int64, b byte) domain.SecretChatMessage {
		return domain.SecretChatMessage{
			ReceiverAuthKeyID: device,
			ReceiverUserID:    99,
			ChatID:            7,
			RandomID:          randomID,
			Date:              int(1000 + randomID),
			Bytes:             []byte{b, b, b},
		}
	}

	// qts is allocated monotonically starting at 1.
	m1, existing, err := store.AppendEncryptedMessage(ctx, mk(101, 0x11))
	if err != nil || existing || m1.Qts != 1 {
		t.Fatalf("append 1 = qts %d existing %v err %v, want qts=1 existing=false", m1.Qts, existing, err)
	}
	m2, _, err := store.AppendEncryptedMessage(ctx, mk(102, 0x22))
	if err != nil || m2.Qts != 2 {
		t.Fatalf("append 2 = qts %d err %v, want 2", m2.Qts, err)
	}

	// Idempotent: the same random_id returns the existing qts/date without reallocating.
	dup, existing, err := store.AppendEncryptedMessage(ctx, mk(101, 0x99))
	if err != nil || !existing || dup.Qts != 1 || dup.Date != m1.Date {
		t.Fatalf("dedup = qts %d date %d existing %v, want qts=1 date=%d existing=true", dup.Qts, dup.Date, existing, m1.Date)
	}
	if string(dup.Bytes) != string([]byte{0x11, 0x11, 0x11}) {
		t.Fatalf("dedup must return first-stored bytes, got %x", dup.Bytes)
	}

	// reserved qts = 2.
	if q, err := store.ReservedQts(ctx, device); err != nil || q != 2 {
		t.Fatalf("reserved qts = %d err %v, want 2", q, err)
	}

	// Difference catch-up: since 0 → 2 rows; since 1 → 1 row (qts=2).
	msgs, err := store.ListEncryptedMessagesSince(ctx, device, 0, 0)
	if err != nil || len(msgs) != 2 || msgs[0].Qts != 1 || msgs[1].Qts != 2 {
		t.Fatalf("list since 0 = %+v err %v, want qts 1,2", msgs, err)
	}
	msgs, _ = store.ListEncryptedMessagesSince(ctx, device, 1, 0)
	if len(msgs) != 1 || msgs[0].Qts != 2 {
		t.Fatalf("list since 1 = %+v, want qts 2", msgs)
	}

	// ack up to 1: confirmed advances; an ack retreat (0) is idempotently ignored.
	if err := store.AckEncryptedMessages(ctx, device, 1); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := store.AckEncryptedMessages(ctx, device, 0); err != nil {
		t.Fatalf("ack rollback should be no-op: %v", err)
	}
	var confirmed int
	if err := pool.QueryRow(ctx, `SELECT confirmed_qts FROM secret_qts_watermarks WHERE auth_key_id = $1`, device).Scan(&confirmed); err != nil {
		t.Fatalf("read confirmed: %v", err)
	}
	if confirmed != 1 {
		t.Fatalf("confirmed qts = %d, want 1", confirmed)
	}

	// A device not involved has reserved = 0.
	if q, err := store.ReservedQts(ctx, int64(0xDEADBEEF)); err != nil || q != 0 {
		t.Fatalf("unrelated device reserved = %d err %v, want 0", q, err)
	}
}

// TestEncryptedStateEventsPostgres verifies the PG implementation of secret-chat state events:
// account-level (target_auth_key_id=0) is visible to all of the user's devices, device-level is
// visible only to the bound device, and the undelivered mark is delivered exactly once.
func TestEncryptedStateEventsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewEncryptedQueueStore(pool)

	const (
		user    = int64(0x7700BB01)
		devA    = int64(0x7700BB0A)
		devB    = int64(0x7700BB0B)
		otherKy = int64(0x7700BB0C)
	)
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM encrypted_state_events WHERE target_user_id = $1`, user)
	}
	cleanup()
	t.Cleanup(cleanup)

	// Account-level event (target_auth_key_id=0) + device-level event (bound to devA).
	accountEvID, err := store.AppendStateEvent(ctx, domain.EncryptedStateEvent{
		TargetUserID: user, TargetAuthKeyID: 0, ChatID: 11, Type: domain.EncryptedStateEventEncryption, Date: 1000,
	})
	if err != nil {
		t.Fatalf("append account event: %v", err)
	}
	devAEvID, err := store.AppendStateEvent(ctx, domain.EncryptedStateEvent{
		TargetUserID: user, TargetAuthKeyID: devA, ChatID: 12, Type: domain.EncryptedStateEventRead, MaxDate: 1500, Date: 1001,
	})
	if err != nil {
		t.Fatalf("append device event: %v", err)
	}

	// devA undelivered: account-level + device-level = 2 rows.
	evs, err := store.ListUndeliveredStateEvents(ctx, user, devA, 0)
	if err != nil || len(evs) != 2 {
		t.Fatalf("devA undelivered = %d err %v, want 2", len(evs), err)
	}
	// devB undelivered: account-level only = 1 row (cannot see devA's device-level event).
	evs, _ = store.ListUndeliveredStateEvents(ctx, user, devB, 0)
	if len(evs) != 1 || evs[0].ID != accountEvID {
		t.Fatalf("devB undelivered = %+v, want only account event %d", evs, accountEvID)
	}

	// Mark devA's two rows delivered → listing again is empty; devB can still see the account-level row (independent delivery mark).
	if err := store.MarkStateEventsDelivered(ctx, devA, []int64{accountEvID, devAEvID}); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	if evs, _ := store.ListUndeliveredStateEvents(ctx, user, devA, 0); len(evs) != 0 {
		t.Fatalf("devA after deliver = %d, want 0", len(evs))
	}
	if evs, _ := store.ListUndeliveredStateEvents(ctx, user, devB, 0); len(evs) != 1 {
		t.Fatalf("devB after devA deliver = %d, want 1 (independent mark)", len(evs))
	}
	// Idempotent repeated mark does not error.
	if err := store.MarkStateEventsDelivered(ctx, devA, []int64{accountEvID}); err != nil {
		t.Fatalf("idempotent mark: %v", err)
	}
	// Unrelated device (different user dimension): a brand-new key for the same user still sees the account-level row.
	if evs, _ := store.ListUndeliveredStateEvents(ctx, user, otherKy, 0); len(evs) != 1 {
		t.Fatalf("fresh device undelivered = %d, want 1 account-level", len(evs))
	}
}

// TestEncryptedFilesPostgres verifies the PG implementation of secret-chat file metadata:
// put/get round-trip, access_hash validation, and found=false when absent. Gated on
// TELESRV_TEST_POSTGRES_DSN.
func TestEncryptedFilesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	store := NewEncryptedQueueStore(pool)

	const fileID = int64(0x7700CC01)
	cleanup := func() { _, _ = pool.Exec(ctx, `DELETE FROM encrypted_files WHERE id = $1`, fileID) }
	cleanup()
	t.Cleanup(cleanup)

	ref := domain.EncryptedFileRef{ID: fileID, AccessHash: 0xABCD, Size: 4096, DCID: 2, KeyFingerprint: 12345}
	if err := store.PutEncryptedFile(ctx, 77, ref); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, found, err := store.GetEncryptedFile(ctx, fileID, 0xABCD)
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got != ref {
		t.Fatalf("round-trip mismatch: %+v vs %+v", got, ref)
	}
	// Wrong access_hash → no match.
	if _, found, _ := store.GetEncryptedFile(ctx, fileID, 0x9999); found {
		t.Fatal("wrong access_hash must not match")
	}
	// Idempotent overwrite.
	if err := store.PutEncryptedFile(ctx, 77, ref); err != nil {
		t.Fatalf("idempotent put: %v", err)
	}
}
