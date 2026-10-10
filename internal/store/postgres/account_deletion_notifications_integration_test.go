package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// A deleted account has to reach every viewer who ever shared a contact or a
// dialog with it, including the ones that are offline at that moment. The
// online-only fan-out cannot do that, so the audience is committed with the
// tombstone and served from the durable queue.
func TestAccountDeletionEnqueuesTombstoneNotificationsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	nonce := time.Now().UnixNano()
	users := NewUserStore(pool)
	deleted := createTestUser(t, ctx, users, fmt.Sprintf("15591%d", nonce), "Ghost", "Owner")
	contact := createTestUser(t, ctx, users, fmt.Sprintf("15592%d", nonce), "Contact", "Peer")
	dialogPeer := createTestUser(t, ctx, users, fmt.Sprintf("15593%d", nonce), "Dialog", "Peer")
	stranger := createTestUser(t, ctx, users, fmt.Sprintf("15594%d", nonce), "Nobody", "Saw")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_notifications WHERE deleted_user_id = $1`, deleted.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_requests WHERE user_id = ANY($1)`, []int64{deleted.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM contacts WHERE user_id = ANY($1) OR contact_user_id = ANY($1)`, []int64{deleted.ID, contact.ID, dialogPeer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM private_messages WHERE sender_user_id = ANY($1) OR recipient_user_id = ANY($1)`, []int64{deleted.ID, contact.ID, dialogPeer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM message_boxes WHERE owner_user_id = ANY($1)`, []int64{deleted.ID, contact.ID, dialogPeer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int64{deleted.ID, contact.ID, dialogPeer.ID, stranger.ID})
	})

	if _, err := pool.Exec(ctx, `INSERT INTO contacts
(user_id, contact_user_id, contact_phone, contact_first_name, contact_last_name)
VALUES ($1, $2, '', 'Contact', 'Peer')`, contact.ID, deleted.ID); err != nil {
		t.Fatalf("insert contact: %v", err)
	}
	if _, err := NewMessageStore(pool).SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: deleted.ID, RecipientUserID: dialogPeer.ID, RandomID: nonce, Message: "keep a dialog row",
	}); err != nil {
		t.Fatalf("send dialog seed message: %v", err)
	}

	lifecycle := NewAccountLifecycleStore(pool)
	if _, err := lifecycle.ExecuteAccountDeletion(ctx, deleted.ID, domain.AccountDeletionManual, "manual", time.Now().UTC()); err != nil {
		t.Fatalf("execute account deletion: %v", err)
	}

	var queued int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM account_deletion_notifications
WHERE deleted_user_id = $1 AND status = 'pending'`, deleted.ID).Scan(&queued); err != nil {
		t.Fatalf("count queued notifications: %v", err)
	}
	if queued != 2 {
		t.Fatalf("queued tombstone notifications = %d, want the contact and the dialog peer", queued)
	}

	claimed, err := lifecycle.ClaimAccountDeletionNotifications(ctx, time.Now().UTC().Add(time.Second), 50, time.Minute)
	if err != nil {
		t.Fatalf("claim notifications: %v", err)
	}
	// The queue is global: another test may have rows pending. Complete them all
	// (the claim leased them) but only assert on this deletion's audience.
	viewers := map[int64]bool{}
	for _, n := range claimed {
		if err := lifecycle.CompleteAccountDeletionNotification(ctx, n.ID, time.Now().UTC()); err != nil {
			t.Fatalf("complete notification %d: %v", n.ID, err)
		}
		if n.DeletedUserID != deleted.ID {
			continue
		}
		viewers[n.TargetUserID] = true
	}
	if !viewers[contact.ID] || !viewers[dialogPeer.ID] {
		t.Fatalf("claim audience = %v, want contact %d and dialog peer %d", viewers, contact.ID, dialogPeer.ID)
	}
	if viewers[stranger.ID] {
		t.Fatal("a peer without a contact or dialog relation must not be notified")
	}

	// A second claim must not replay this deletion's delivered tombstones.
	again, err := lifecycle.ClaimAccountDeletionNotifications(ctx, time.Now().UTC().Add(time.Second), 50, time.Minute)
	if err != nil {
		t.Fatalf("re-claim notifications: %v", err)
	}
	for _, n := range again {
		if n.DeletedUserID == deleted.ID {
			t.Fatalf("re-claimed a delivered tombstone for this deletion: %+v", n)
		}
	}
}
