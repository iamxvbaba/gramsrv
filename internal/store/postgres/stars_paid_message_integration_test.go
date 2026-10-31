package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

func TestPaidMessageStarsChargedInSendTx(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "1555009301"+suffix, "Pay", "Sender")
	recipient := createTestUser(t, ctx, users, "1555009302"+suffix, "Pay", "Recipient")
	cleanup := func() {
		for _, id := range []int64{sender.ID, recipient.ID} {
			_, _ = pool.Exec(ctx, "DELETE FROM stars_transactions WHERE user_id=$1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM stars_balances WHERE user_id=$1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM private_messages WHERE sender_user_id=$1 OR recipient_user_id=$1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM message_boxes WHERE owner_user_id=$1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", id)
		}
	}
	t.Cleanup(cleanup)

	const price int64 = 25
	messages := newTestMessageStore(pool)
	send := func(randomID int64, paid int64) (domain.SendPrivateTextResult, error) {
		return messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID:           sender.ID,
			RecipientUserID:        recipient.ID,
			RandomID:               randomID,
			Message:                "paid hello",
			Date:                   1735689600,
			PaidStars:              paid,
			IdempotencyPreflighted: randomID != 0,
		})
	}

	// No balance and a fee is required: the whole send must fail (ErrStarsInsufficient), producing no message/ledger.
	first, err := send(93001, price)
	if first.SenderMessage.ID != 0 {
		t.Fatalf("insufficient send must not persist a message, got %+v", first.SenderMessage)
	}
	if !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient send err = %v, want ErrStarsInsufficient", err)
	}
	for _, id := range []int64{sender.ID, recipient.ID} {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM stars_transactions WHERE user_id=$1", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("user %d has %d ledger rows after insufficient send, want 0", id, n)
		}
	}

	// Resend after topping up: atomically debit the sender and credit the recipient in the transaction, the difference must be exactly price, and both ledger sides are written.
	if err := mustFundStars(ctx, pool, sender.ID, 1000); err != nil {
		t.Fatal(err)
	}
	sent, err := send(93002, price)
	if err != nil {
		t.Fatalf("paid send err = %v, want nil", err)
	}
	if sent.Duplicate {
		t.Fatal("paid send must not be a duplicate")
	}
	var sBal, rBal int64
	if err := pool.QueryRow(ctx, "SELECT balance FROM stars_balances WHERE user_id=$1", sender.ID).Scan(&sBal); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT balance FROM stars_balances WHERE user_id=$1", recipient.ID).Scan(&rBal); err != nil {
		t.Fatal(err)
	}
	credit := price * paidMessageChannelCommissionPermille / 1000
	if sBal != 1000-price || rBal != credit {
		t.Fatalf("balances sender=%d recipient=%d, want %d/%d", sBal, rBal, 1000-price, credit)
	}
	ledger := map[int64]int64{}
	for _, id := range []int64{sender.ID, recipient.ID} {
		amt := int64(0)
		if err := pool.QueryRow(ctx, "SELECT amount FROM stars_transactions WHERE user_id=$1 AND reason='paid_message'", id).Scan(&amt); err != nil {
			t.Fatalf("ledger for %d: %v", id, err)
		}
		ledger[id] = amt
	}
	if ledger[sender.ID] != -price || ledger[recipient.ID] != credit {
		t.Fatalf("ledger sender=%d recipient=%d, want %d/%d", ledger[sender.ID], ledger[recipient.ID], -price, credit)
	}
	if sent.PaidStars == nil {
		t.Fatalf("paid send result must carry the charge, got nil")
	}
	if sent.PaidStars.Amount != price || sent.PaidStars.SenderBalance != sBal || sent.PaidStars.RecipientBalance != credit {
		t.Fatalf("charge = %+v, want amount=%d senderBalance=%d recipientBalance=%d", sent.PaidStars, price, sBal, credit)
	}
	if sent.SenderMessage.PaidStars != price || sent.RecipientMessage.PaidStars != price {
		t.Fatalf("message paid stars sender=%d recipient=%d, want %d", sent.SenderMessage.PaidStars, sent.RecipientMessage.PaidStars, price)
	}
	list, err := messages.GetByIDs(ctx, sender.ID, []int{sent.SenderMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Messages) != 1 || list.Messages[0].PaidStars != price {
		t.Fatalf("reloaded message paid stars = %+v (len %d), want %d", list.Messages, len(list.Messages), price)
	}

	// Idempotent replay: resending the same random_id must not charge twice.
	replay, err := send(93002, price)
	if err != nil {
		t.Fatalf("replay err = %v, want nil", err)
	}
	if !replay.Duplicate {
		t.Fatal("same random_id must return duplicate replay")
	}
	var sBal2 int64
	if err := pool.QueryRow(ctx, "SELECT balance FROM stars_balances WHERE user_id=$1", sender.ID).Scan(&sBal2); err != nil {
		t.Fatal(err)
	}
	if sBal2 != sBal {
		t.Fatalf("replay charged again: sender balance %d, want %d", sBal2, sBal)
	}
}

func mustFundStars(ctx context.Context, pool *pgxpool.Pool, userID, amount int64) error {
	_, err := pool.Exec(ctx, `
INSERT INTO stars_balances (user_id, balance, granted, updated_at)
VALUES ($1, $2, true, now())
ON CONFLICT (user_id) DO UPDATE SET balance = $2, granted = true, updated_at = now()`, userID, amount)
	return err
}

func TestScheduledMessagePaidStarsPersisted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "1555009401"+suffix, "Sched", "Owner")
	peer := createTestUser(t, ctx, users, "1555009402"+suffix, "Sched", "Peer")
	cleanup := func() {
		for _, id := range []int64{owner.ID, peer.ID} {
			_, _ = pool.Exec(ctx, "DELETE FROM scheduled_messages WHERE owner_user_id=$1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", id)
		}
	}
	t.Cleanup(cleanup)
	messages := newTestMessageStore(pool)
	date := 1735689600 + 600
	msg, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID:  owner.ID,
		Peer:         domain.Peer{Type: domain.PeerTypeUser, ID: peer.ID},
		RandomID:     940011,
		Message:      "paid later",
		ScheduleDate: date,
		PaidStars:    7,
		Date:         date - 10,
	})
	if err != nil {
		t.Fatalf("create scheduled message: %v", err)
	}
	if msg.ID == 0 {
		t.Fatal("scheduled message id not allocated")
	}
	if msg.PaidStars != 7 {
		t.Fatalf("scheduled paid stars = %d, want 7", msg.PaidStars)
	}
	list, err := messages.ListScheduledMessages(ctx, domain.ScheduledMessageFilter{
		OwnerUserID: owner.ID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: peer.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Messages) != 1 {
		t.Fatalf("scheduled list count=%d len=%d, want 1/1", list.Count, list.Count)
	}
	if list.Messages[0].PaidStars != 7 {
		t.Fatalf("stored scheduled paid stars = %d, want 7", list.Messages[0].PaidStars)
	}
}

func TestScheduledRepeatingPaidStarsPersistedAcrossClaims(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "1555009403"+suffix, "Sched", "Repeat")
	peer := createTestUser(t, ctx, users, "1555009404"+suffix, "Sched", "RepeatPeer")
	cleanup := func() {
		for _, id := range []int64{owner.ID, peer.ID} {
			_, _ = pool.Exec(ctx, "DELETE FROM scheduled_messages WHERE owner_user_id=$1", id)
			_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id=$1", id)
		}
	}
	t.Cleanup(cleanup)
	messages := newTestMessageStore(pool)
	date := 1735689600 + 1200
	msg, err := messages.CreateScheduledMessage(ctx, domain.ScheduleMessageRequest{
		OwnerUserID:          owner.ID,
		Peer:                 domain.Peer{Type: domain.PeerTypeUser, ID: peer.ID},
		RandomID:             940031,
		Message:              "paid repeat",
		ScheduleDate:         date,
		ScheduleRepeatPeriod: 86400,
		PaidStars:            9,
		Date:                 date - 10,
	})
	if err != nil {
		t.Fatalf("create repeating scheduled message: %v", err)
	}
	peerFilter := domain.Peer{Type: domain.PeerTypeUser, ID: peer.ID}
	claimOnce := func(stage string) {
		t.Helper()
		claim, err := messages.ClaimScheduledMessages(ctx, domain.ScheduledMessageClaim{
			OwnerUserID: owner.ID,
			Peer:        peerFilter,
			IDs:         []int{msg.ID},
			Now:         date - 1,
			LeaseUntil:  date + 30,
		})
		if err != nil {
			t.Fatalf("claim %s: %v", stage, err)
		}
		if len(claim) != 1 {
			t.Fatalf("claim %s returned %d rows, want 1", stage, len(claim))
		}
		if claim[0].PaidStars != 9 {
			t.Fatalf("claim %s paid stars = %d, want stored 9", stage, claim[0].PaidStars)
		}
	}
	claimOnce("first repetition")
	if err := messages.ReleaseScheduledMessage(ctx, owner.ID, msg.ID, "lease expired"); err != nil {
		t.Fatalf("release: %v", err)
	}
	claimOnce("second repetition")
}
