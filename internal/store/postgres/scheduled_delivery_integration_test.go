package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

func scheduledOwnerEffects(ownerID int64) store.DeliveryEffectsBuilder[domain.ScheduledMessage] {
	return func(domain.ScheduledMessage) ([]store.DeliveryEffect, error) {
		return postgresAbsoluteOwnerEffect(ownerID), nil
	}
}

func TestScheduledDeliveryFencesClaimsAndCommitsNextOccurrence(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	a := createTestUser(t, ctx, users, "+1612"+suffix+"01", "Schedule owner", "")
	b := createTestUser(t, ctx, users, "+1612"+suffix+"02", "Schedule peer", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{a.ID, b.ID}) })
	const now = 1800001000
	if _, err := pool.Exec(ctx, "UPDATE users SET premium_expires_at=to_timestamp($2) WHERE id=$1", a.ID, now+864000); err != nil {
		t.Fatal(err)
	}
	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: b.ID}
	makeSchedule := func(random int64, repeat int) domain.ScheduledMessage {
		t.Helper()
		msg, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{OwnerUserID: a.ID, Peer: peer, RandomID: random, Message: "repeat", ScheduleDate: now, ScheduleRepeatPeriod: repeat, Date: now - 60}, scheduledOwnerEffects(a.ID))
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	claim := func(id, date int) domain.ScheduledMessage {
		t.Helper()
		items, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: a.ID, Peer: peer, IDs: []int{id}, Now: date, LeaseUntil: date + 60})
		if err != nil || len(items) != 1 {
			t.Fatalf("claim %v %v", items, err)
		}
		return items[0]
	}
	var completions []domain.ScheduledCompletion
	send := func(msg domain.ScheduledMessage) (domain.SendPrivateTextResult, error) {
		return messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: a.ID, RecipientUserID: b.ID, RandomID: msg.RandomID, Message: msg.Message, Date: now + 62, ScheduledDelivery: &domain.ScheduledDelivery{
			OwnerUserID: a.ID, ID: msg.ID, Generation: msg.Generation,
			Payload: func(completion domain.ScheduledCompletion) ([]byte, error) {
				completions = append(completions, completion)
				return []byte{byte(len(completions))}, nil
			},
		}})
	}
	outboxRows := func() int {
		t.Helper()
		var rows int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1", a.ID).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	recurring := makeSchedule(1001, 86400)
	old := claim(recurring.ID, now)
	active := claim(recurring.ID, now+61)
	if _, err := send(old); !errors.Is(err, domain.ErrScheduledClaimLost) {
		t.Fatalf("expired claim: %v", err)
	}
	if err := messages.ReleaseScheduledMessage(ctx, a.ID, old.ID, old.Generation, "stale worker"); err != nil {
		t.Fatal(err)
	}
	beforeSend := outboxRows()
	result, err := send(active)
	if err != nil {
		t.Fatal(err)
	}
	// v2 (D6): the sent transition and the next occurrence reach the owner as one
	// durable delivery committed by the send transaction itself.
	if got := outboxRows(); got != beforeSend+1 || len(completions) != 1 ||
		completions[0].Sent.ID != active.ID || completions[0].Sent.SentMessageID != result.SenderMessage.ID ||
		completions[0].Next == nil || completions[0].Next.ID != result.NextScheduled.ID {
		t.Fatalf("completion delivery rows %d->%d completions=%+v", beforeSend, got, completions)
	}
	if result.NextScheduled == nil || result.NextScheduled.ID == active.ID || result.NextScheduled.RandomID == active.RandomID || result.NextScheduled.ScheduleDate != now+62+86400 {
		t.Fatalf("next occurrence: %+v", result.NextScheduled)
	}
	replay, err := send(active)
	if err != nil || !replay.Duplicate || replay.SenderMessage.ID != result.SenderMessage.ID {
		t.Fatalf("committed replay: %+v %v", replay, err)
	}
	pending, err := messages.ListScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: a.ID, Peer: peer})
	if err != nil || len(pending.Messages) != 1 {
		t.Fatalf("pending %v %v", pending, err)
	}
	if pending.Messages[0].ID != result.NextScheduled.ID {
		t.Fatal("next occurrence duplicated")
	}
	edited := makeSchedule(1002, 0)
	beforeEdit := claim(edited.ID, now)
	if _, err := messages.EditScheduledMessage(ctx, domain.EditScheduledMessageRequest{OwnerUserID: a.ID, Peer: peer, ID: edited.ID, SetMessage: true, Message: "edited", ScheduleDate: now + 100, Date: now + 1}, scheduledOwnerEffects(a.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err := send(beforeEdit); !errors.Is(err, domain.ErrScheduledClaimLost) {
		t.Fatalf("edited claim: %v", err)
	}
	canceled := makeSchedule(1003, 0)
	beforeCancel := claim(canceled.ID, now)
	if _, err := messages.DeleteScheduledMessages(ctx, domain.ScheduledMessageFilter{OwnerUserID: a.ID, Peer: peer, IDs: []int{canceled.ID}}, now+1, func([]domain.ScheduledMessage) ([]store.DeliveryEffect, error) {
		return postgresAbsoluteOwnerEffect(a.ID), nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := send(beforeCancel); !errors.Is(err, domain.ErrScheduledClaimLost) {
		t.Fatalf("canceled claim: %v", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM private_messages WHERE sender_user_id=$1", a.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("message count=%d", count)
	}
}

func TestScheduledDeliveryNextOccurrenceKeepsPaidStars(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	a := createTestUser(t, ctx, users, "+1613"+suffix+"01", "Paid repeat owner", "")
	b := createTestUser(t, ctx, users, "+1613"+suffix+"02", "Paid repeat peer", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{a.ID, b.ID}) })
	const now = 1800002000
	if _, err := pool.Exec(ctx, "UPDATE users SET premium_expires_at=to_timestamp($2) WHERE id=$1", a.ID, now+864000); err != nil {
		t.Fatal(err)
	}
	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: b.ID}
	msg, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{OwnerUserID: a.ID, Peer: peer, RandomID: 2001, Message: "paid repeat", ScheduleDate: now, ScheduleRepeatPeriod: 86400, PaidStars: 9, Date: now - 60}, scheduledOwnerEffects(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	items, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: a.ID, Peer: peer, IDs: []int{msg.ID}, Now: now, LeaseUntil: now + 60})
	if err != nil || len(items) != 1 {
		t.Fatalf("claim %v %v", items, err)
	}
	claimed := items[0]
	result, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{SenderUserID: a.ID, RecipientUserID: b.ID, RandomID: claimed.RandomID, Message: claimed.Message, Date: now + 1, ScheduledDelivery: &domain.ScheduledDelivery{
		OwnerUserID: a.ID, ID: claimed.ID, Generation: claimed.Generation,
		Payload: func(domain.ScheduledCompletion) ([]byte, error) { return []byte{1}, nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.NextScheduled == nil || result.NextScheduled.PaidStars != 9 {
		t.Fatalf("next occurrence must keep the scheduled paid stars: %+v", result.NextScheduled)
	}
}

func TestScheduledOnlineClaimsOnlyEligibleTargets(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	a := createTestUser(t, ctx, users, "+1613"+suffix+"01", "Owner", "")
	b := createTestUser(t, ctx, users, "+1613"+suffix+"02", "Target", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=ANY($1::bigint[])", []int64{a.ID, b.ID}) })
	messages := NewMessageStore(pool, WithMessageAllocators(&perUserCounterAllocator{}))
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: b.ID}
	msg, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{OwnerUserID: a.ID, Peer: peer, RandomID: 987, Message: "online", ScheduleDate: domain.ScheduleWhenOnline, Date: 1800001000}, scheduledOwnerEffects(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	none, err := messages.ClaimDueScheduledMessages(ctx, 1800001001, 100, 60, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range none {
		if v.OwnerUserID == a.ID {
			t.Fatal("offline message claimed")
		}
	}
	items, err := messages.ClaimDueScheduledMessages(ctx, 1800001002, 100, 60, []int64{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, v := range items {
		if v.OwnerUserID == a.ID && v.ID == msg.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("online message not claimed")
	}
}

// D6 on the channel send transaction: the scheduled row is fenced and
// completed, and the owner's completion delivery commits with the message.
func TestScheduledChannelDeliveryCommitsCompletionDelivery(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1617"+randomSuffix(t)+"01", "Channel schedule", "")
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", owner.ID) })
	channels := newTestChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Scheduled", Megagroup: true, Date: 1800002000})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id=$1", created.Channel.ID) })
	messages := newTestMessageStore(pool)
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	scheduled, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{OwnerUserID: owner.ID, Peer: peer, RandomID: 4401, Message: "channel later", ScheduleDate: 1800002100, Date: 1800002000}, scheduledOwnerEffects(owner.ID))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{OwnerUserID: owner.ID, Peer: peer, IDs: []int{scheduled.ID}, Now: 1800002010, LeaseUntil: 1800002070})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v err=%v", claimed, err)
	}
	var beforeRows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1", owner.ID).Scan(&beforeRows); err != nil {
		t.Fatal(err)
	}
	var completion domain.ScheduledCompletion
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: peer.ID, RandomID: claimed[0].RandomID, Message: claimed[0].Message, Date: 1800002010,
		ScheduledDelivery: &domain.ScheduledDelivery{
			OwnerUserID: owner.ID, ID: claimed[0].ID, Generation: claimed[0].Generation,
			Payload: func(c domain.ScheduledCompletion) ([]byte, error) { completion = c; return []byte{1}, nil },
		},
	})
	if err != nil {
		t.Fatalf("send scheduled channel message: %v", err)
	}
	var state string
	var sentID, afterRows int
	if err := pool.QueryRow(ctx, `SELECT state, sent_message_id, (SELECT count(*) FROM edge_delivery_outbox WHERE target_user_id=$1)
FROM scheduled_messages WHERE owner_user_id=$1 AND scheduled_id=$2`, owner.ID, scheduled.ID).Scan(&state, &sentID, &afterRows); err != nil {
		t.Fatal(err)
	}
	if state != "sent" || sentID != sent.Message.ID || afterRows != beforeRows+1 || completion.Sent.SentMessageID != sent.Message.ID || completion.Next != nil {
		t.Fatalf("channel completion state=%s sent=%d/%d rows %d->%d completion=%+v", state, sentID, sent.Message.ID, beforeRows, afterRows, completion)
	}
	if _, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: peer.ID, RandomID: claimed[0].RandomID + 1, Message: "stale", Date: 1800002011,
		ScheduledDelivery: &domain.ScheduledDelivery{OwnerUserID: owner.ID, ID: claimed[0].ID, Generation: claimed[0].Generation,
			Payload: func(domain.ScheduledCompletion) ([]byte, error) { return []byte{1}, nil }},
	}); !errors.Is(err, domain.ErrScheduledClaimLost) {
		t.Fatalf("completed claim reused: %v", err)
	}
}
