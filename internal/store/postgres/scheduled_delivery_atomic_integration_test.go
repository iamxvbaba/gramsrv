package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func TestScheduledMutationAndAbsoluteDeliveryCommitAtomicallyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 101, Phone: "+1896" + suffix + "01", FirstName: "Scheduled Atomic"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	peerUser, err := users.Create(ctx, domain.User{AccessHash: 102, Phone: "+1896" + suffix + "02", FirstName: "Scheduled Peer"})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	userIDs := []int64{owner.ID, peerUser.ID}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM edge_delivery_outbox WHERE target_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM scheduled_messages WHERE owner_user_id = $1", owner.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM dialogs WHERE user_id = ANY($1::bigint[])", userIDs)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", userIDs)
	})

	messages := newTestMessageStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: peerUser.ID}
	var beforePTS int
	if err := pool.QueryRow(ctx, "SELECT COALESCE((SELECT contiguous_pts FROM user_update_watermarks WHERE user_id = $1), 0)", owner.ID).Scan(&beforePTS); err != nil {
		t.Fatalf("read owner pts: %v", err)
	}
	scheduled, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID: owner.ID, Peer: peer, RandomID: 8123001, Message: "before",
		ScheduleDate: 1_900_000_000, Date: 1_700_010_000,
	}, testScheduledMessageEffects)
	if err != nil {
		t.Fatalf("create scheduled: %v", err)
	}
	if scheduled.ID <= 0 {
		t.Fatalf("scheduled id = %d", scheduled.ID)
	}
	var deliveryBefore int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id = $1", owner.ID).Scan(&deliveryBefore); err != nil {
		t.Fatalf("count create delivery: %v", err)
	}
	if deliveryBefore != 1 {
		t.Fatalf("create scheduled delivery rows = %d, want 1", deliveryBefore)
	}

	projectionErr := errors.New("scheduled projection failed")
	failedOne := func(domain.ScheduledMessage) ([]store.DeliveryEffect, error) { return nil, projectionErr }
	if _, err := messages.EditScheduledMessage(ctx, domain.EditScheduledMessageRequest{
		OwnerUserID: owner.ID, Peer: peer, ID: scheduled.ID, SetMessage: true,
		Message: "must roll back", ScheduleDate: 1_900_000_100, Date: 1_700_010_001,
	}, failedOne); !errors.Is(err, projectionErr) {
		t.Fatalf("failed edit err = %v, want projection error", err)
	}
	loaded, err := messages.GetScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner.ID, Peer: peer, IDs: []int{scheduled.ID}, Limit: 1})
	if err != nil || len(loaded.Messages) != 1 {
		t.Fatalf("load scheduled after failed edit = %+v err %v", loaded, err)
	}
	if loaded.Messages[0].Message != "before" || loaded.Messages[0].ScheduleDate != 1_900_000_000 {
		t.Fatalf("failed edit survived rollback: %+v", loaded.Messages[0])
	}

	if _, err := messages.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{
		OwnerUserID: owner.ID, Peer: peer, IDs: []int{scheduled.ID},
	}, 1_700_010_002, func([]domain.ScheduledMessage) ([]store.DeliveryEffect, error) {
		return nil, projectionErr
	}); !errors.Is(err, projectionErr) {
		t.Fatalf("failed delete err = %v, want projection error", err)
	}
	loaded, err = messages.GetScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: owner.ID, Peer: peer, IDs: []int{scheduled.ID}, Limit: 1})
	if err != nil || len(loaded.Messages) != 1 {
		t.Fatalf("scheduled disappeared after failed delete = %+v err %v", loaded, err)
	}

	// D6: the sent transition and its owner delivery commit inside the send
	// transaction, so a failing completion payload rolls back the message too.
	claimed, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{
		OwnerUserID: owner.ID, Peer: peer, IDs: []int{scheduled.ID}, Now: 1_700_010_003, LeaseUntil: 1_700_010_063,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim scheduled = %+v err %v", claimed, err)
	}
	if _, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: owner.ID, RecipientUserID: peerUser.ID, RandomID: claimed[0].RandomID, Message: claimed[0].Message, Date: 1_700_010_003,
		ScheduledDelivery: &domain.ScheduledDelivery{
			OwnerUserID: owner.ID, ID: claimed[0].ID, Generation: claimed[0].Generation,
			Payload: func(domain.ScheduledCompletion) ([]byte, error) { return nil, projectionErr },
		},
	}); !errors.Is(err, projectionErr) {
		t.Fatalf("failed scheduled completion err = %v, want projection error", err)
	}
	var state string
	var sentMessageID, sentRows int
	if err := pool.QueryRow(ctx, `SELECT state, sent_message_id, (SELECT count(*) FROM private_messages WHERE sender_user_id = $1)
FROM scheduled_messages WHERE owner_user_id = $1 AND scheduled_id = $2`, owner.ID, scheduled.ID).Scan(&state, &sentMessageID, &sentRows); err != nil {
		t.Fatalf("read scheduled state after failed completion: %v", err)
	}
	if state != "dispatching" || sentMessageID != 0 || sentRows != 0 {
		t.Fatalf("failed completion survived rollback: state=%s sent=%d messages=%d", state, sentMessageID, sentRows)
	}
	var deliveryAfter, afterPTS int
	if err := pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id = $1),
  COALESCE((SELECT contiguous_pts FROM user_update_watermarks WHERE user_id = $1), 0)`, owner.ID).Scan(&deliveryAfter, &afterPTS); err != nil {
		t.Fatalf("read scheduled atomic facts: %v", err)
	}
	if deliveryAfter != deliveryBefore {
		t.Fatalf("failed scheduled mutations appended delivery: %d -> %d", deliveryBefore, deliveryAfter)
	}
	if afterPTS != beforePTS {
		t.Fatalf("scheduled mutations changed account pts %d -> %d", beforePTS, afterPTS)
	}
}
