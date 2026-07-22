package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestSuggestedPostLifecyclePostgres verifies that message state, channel pts,
// escrow and refund are committed through the real PostgreSQL transaction.
// It is gated by TELESRV_TEST_POSTGRES_DSN and testPool migrates through 0134.
func TestSuggestedPostLifecyclePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 201, Phone: "+1888" + suffix + "01", FirstName: "SuggestOwner"})
	if err != nil {
		t.Fatal(err)
	}
	subscriber, err := users.Create(ctx, domain.User{AccessHash: 202, Phone: "+1888" + suffix + "02", FirstName: "SuggestSubscriber"})
	if err != nil {
		t.Fatal(err)
	}
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Suggested " + suffix, Broadcast: true, Date: 1_700_000_000})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := channels.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM suggested_post_approvals WHERE monoforum_id=$1`, monoID)
		_, _ = pool.Exec(ctx, `DELETE FROM channel_stars_balances WHERE channel_id=$1`, created.Channel.ID)
		_, _ = pool.Exec(ctx, `DELETE FROM channels WHERE id=ANY($1::bigint[])`, []int64{monoID, created.Channel.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id=ANY($1::bigint[])`, []int64{owner.ID, subscriber.ID})
	})
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,100,true) ON CONFLICT(user_id) DO UPDATE SET balance=100,granted=true`, subscriber.ID); err != nil {
		t.Fatal(err)
	}
	saved := domain.Peer{Type: domain.PeerTypeUser, ID: subscriber.ID}
	suggestion, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: subscriber.ID, SavedPeer: saved, RandomID: 71, Message: "postgres suggestion", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}, Date: 1_700_000_100})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := channels.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: suggestion.Message.ID, Date: 1_700_000_200})
	if err != nil {
		t.Fatal(err)
	}
	if approved.State != domain.SuggestedPostStatePublished || approved.Published == nil || approved.PayerStarsBalance == nil || approved.PayerStarsBalance.Balance != 90 {
		t.Fatalf("approved=%+v", approved)
	}
	if approved.OriginalMessage.SuggestedPost.ScheduleDate != 1_700_000_200 || approved.ServiceMessage.Action.SuggestedPostScheduleDate != 1_700_000_200 {
		t.Fatalf("immediate approval dates original/action=%d/%d, want commit date", approved.OriginalMessage.SuggestedPost.ScheduleDate, approved.ServiceMessage.Action.SuggestedPostScheduleDate)
	}
	history, err := channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: monoID, SavedPeer: saved, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var persistedApprovalDate int
	for _, message := range history.Messages {
		if message.ID == approved.ServiceMessage.ID && message.Action != nil {
			persistedApprovalDate = message.Action.SuggestedPostScheduleDate
			break
		}
	}
	if persistedApprovalDate != 1_700_000_200 {
		t.Fatalf("persisted approval history date=%d, want commit date", persistedApprovalDate)
	}
	replay, err := channels.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: suggestion.Message.ID, Date: 1_700_000_201})
	if err != nil || !replay.Duplicate || replay.OriginalEvent.Type != domain.ChannelUpdateEditMessage || replay.ServiceEvent.Type != domain.ChannelUpdateNewMessage || replay.Published == nil {
		t.Fatalf("approval replay=%+v err=%v", replay, err)
	}
	var state string
	var scheduleDate int
	var debit, channelBalance int64
	if err := pool.QueryRow(ctx, `SELECT state,schedule_date FROM suggested_post_approvals WHERE monoforum_id=$1 AND suggestion_message_id=$2`, monoID, suggestion.Message.ID).Scan(&state, &scheduleDate); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, subscriber.ID).Scan(&debit); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id=$1),0)`, created.Channel.ID).Scan(&channelBalance)
	if state != string(domain.SuggestedPostStatePublished) || scheduleDate != 1_700_000_200 || debit != 90 || channelBalance != 0 {
		t.Fatalf("state/schedule/debit/channel=%s/%d/%d/%d", state, scheduleDate, debit, channelBalance)
	}
	if _, err := pool.Exec(ctx, `UPDATE channel_messages SET deleted=true WHERE channel_id=$1 AND id=$2`, created.Channel.ID, approved.Published.Message.ID); err != nil {
		t.Fatal(err)
	}
	resolved, err := channels.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_000_300, Limit: 10})
	if err != nil || len(resolved) != 1 || resolved[0].State != domain.SuggestedPostStateRefunded || resolved[0].ServiceMessage.Action == nil || resolved[0].ServiceMessage.Action.Type != domain.ChannelActionSuggestedPostRefund {
		t.Fatalf("refund=%+v err=%v", resolved, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, subscriber.ID).Scan(&debit); err != nil {
		t.Fatal(err)
	}
	var txnNet int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(sum(amount),0) FROM stars_transactions WHERE user_id=$1 AND reason=$2`, subscriber.ID, string(domain.StarsReasonSuggestedPost)).Scan(&txnNet); err != nil {
		t.Fatal(err)
	}
	if debit != 100 || txnNet != 0 {
		t.Fatalf("refund balance/net=%d/%d, want 100/0", debit, txnNet)
	}

	late, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: subscriber.ID, SavedPeer: saved, RandomID: 72, Message: "late deletion", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}, Date: 1_700_000_400})
	if err != nil {
		t.Fatal(err)
	}
	approvedAt := 1_700_000_500
	lateApproved, err := channels.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: late.Message.ID, Date: approvedAt})
	if err != nil || lateApproved.Published == nil {
		t.Fatalf("late approval=%+v err=%v", lateApproved, err)
	}
	due := approvedAt + suggestedPostSettlementAge
	if _, err := channels.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{UserID: owner.ID, ChannelID: created.Channel.ID, IDs: []int{lateApproved.Published.Message.ID}, Date: due + 1}); err != nil {
		t.Fatal(err)
	}
	resolved, err = channels.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: due + 2, Limit: 10})
	if err != nil || len(resolved) != 1 || resolved[0].State != domain.SuggestedPostStateCompleted || resolved[0].ServiceMessage.Action == nil || resolved[0].ServiceMessage.Action.Type != domain.ChannelActionSuggestedPostSuccess {
		t.Fatalf("late settlement=%+v err=%v", resolved, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, subscriber.ID).Scan(&debit); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id=$1),0)`, created.Channel.ID).Scan(&channelBalance); err != nil {
		t.Fatal(err)
	}
	if debit != 90 || channelBalance != 8 {
		t.Fatalf("late settlement balance/channel=%d/%d, want 90/8", debit, channelBalance)
	}
}
