package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestPrivatePaidMessagesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	alice := createTestUser(t, ctx, users, "7900"+suffix, "Alice", "Paid")
	bob := createTestUser(t, ctx, users, "7901"+suffix, "Bob", "Paid")
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances (user_id, balance, granted) VALUES ($1, 25, true), ($2, 0, true)`, alice.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	messages := NewMessageStore(pool)
	paid := NewPaidMessagesStore(pool)
	now := int(time.Now().Unix())
	send := func(randomID, stars int64) (domain.SendPrivateTextResult, error) {
		return messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
			SenderUserID: alice.ID, RecipientUserID: bob.ID, RandomID: randomID,
			Message: "paid hello", Date: now, PaidMessageStars: stars,
		})
	}
	balance := func(id int64) int64 {
		var b int64
		if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}

	res, err := send(9101, 10)
	if err != nil {
		t.Fatalf("paid send: %v", err)
	}
	if !res.PaidMessageCharged || res.PaidMessageSenderBalance != 15 || res.RecipientMessage.PaidMessageStars != 10 {
		t.Fatalf("paid result = charged %v balance %d stars %d", res.PaidMessageCharged, res.PaidMessageSenderBalance, res.RecipientMessage.PaidMessageStars)
	}
	if a, b := balance(alice.ID), balance(bob.ID); a != 15 || b != 8 {
		t.Fatalf("balances after one paid message = %d/%d, want 15/8", a, b)
	}
	// A replay of the same send is not charged again.
	if dup, err := send(9101, 10); err != nil || !dup.Duplicate || balance(alice.ID) != 15 {
		t.Fatalf("replay: dup=%v err=%v balance=%d", dup.Duplicate, err, balance(alice.ID))
	}
	// Reading the chat back shows the paid amount on both sides.
	list, err := messages.GetByIDs(ctx, bob.ID, []int{res.RecipientMessage.ID})
	if err != nil || len(list.Messages) != 1 || list.Messages[0].PaidMessageStars != 10 {
		t.Fatalf("recipient history = %+v err=%v", list.Messages, err)
	}
	list, err = messages.GetByIDs(ctx, alice.ID, []int{res.SenderMessage.ID})
	if err != nil || len(list.Messages) != 1 || list.Messages[0].PaidMessageStars != 10 {
		t.Fatalf("sender history = %+v err=%v", list.Messages, err)
	}
	// Not enough Stars: nothing is sent, nothing is charged.
	if _, err := send(9102, 20); !errors.Is(err, domain.ErrStarsInsufficient) || balance(alice.ID) != 15 {
		t.Fatalf("insufficient send err=%v balance=%d", err, balance(alice.ID))
	}
	if _, err := send(9103, 5); err != nil {
		t.Fatal(err)
	}
	if total, err := paid.PaidMessagesRevenue(ctx, bob.ID, alice.ID); err != nil || total != 15 {
		t.Fatalf("revenue = %d err=%v, want 15", total, err)
	}
	if first, err := paid.RecipientMessagedFirst(ctx, bob.ID, alice.ID); err != nil || first {
		t.Fatalf("bob never wrote first: %v %v", first, err)
	}
	if first, err := paid.RecipientMessagedFirst(ctx, alice.ID, bob.ID); err != nil || !first {
		t.Fatalf("alice wrote to bob: %v %v", first, err)
	}

	refund, err := paid.RefundPaidMessages(ctx, bob.ID, alice.ID, now)
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if refund.Count != 2 || refund.Stars != 15 || refund.PayerBalance != 25 || refund.RecipientBalance != 0 {
		t.Fatalf("refund = %+v", refund)
	}
	if a, b := balance(alice.ID), balance(bob.ID); a != 25 || b != 0 {
		t.Fatalf("balances after refund = %d/%d, want 25/0", a, b)
	}
	if total, _ := paid.PaidMessagesRevenue(ctx, bob.ID, alice.ID); total != 0 {
		t.Fatalf("revenue after refund = %d", total)
	}
	if again, err := paid.RefundPaidMessages(ctx, bob.ID, alice.ID, now); err != nil || again.Count != 0 {
		t.Fatalf("second refund = %+v err=%v", again, err)
	}
}
