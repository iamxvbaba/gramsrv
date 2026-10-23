package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestSupergroupPaidMessagesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "7902"+suffix, "Owner", "Group")
	member := createTestUser(t, ctx, users, "7903"+suffix, "Member", "Group")
	if _, err := pool.Exec(ctx, `INSERT INTO stars_balances (user_id, balance, granted) VALUES ($1, 0, true), ($2, 12, true)`, owner.ID, member.ID); err != nil {
		t.Fatal(err)
	}
	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Paid Group " + suffix, Megagroup: true, Date: int(time.Now().Unix()),
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	groupID := created.Channel.ID
	if _, err := channels.JoinChannel(ctx, groupID, member.ID, int(time.Now().Unix())); err != nil {
		t.Fatalf("join: %v", err)
	}
	priced, err := channels.SetPaidMessagesPrice(ctx, owner.ID, groupID, 5, false)
	if err != nil {
		t.Fatalf("set price: %v", err)
	}
	if priced.ServiceMessage == nil || priced.ServiceMessage.Message.Action == nil || priced.ServiceMessage.Message.Action.Type != domain.ChannelActionPaidMessagesPrice {
		t.Fatalf("no price service message: %+v", priced.ServiceMessage)
	}
	balance := func(id int64) int64 {
		var b int64
		if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id = $1`, id).Scan(&b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	groupBalance := func() int64 {
		var b int64
		err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id = $1`, groupID).Scan(&b)
		if err != nil {
			return 0
		}
		return b
	}
	send := func(userID, randomID, allow int64) (domain.SendChannelMessageResult, error) {
		return channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID: userID, ChannelID: groupID, RandomID: randomID, Message: "hi", AllowPaidStars: allow,
		})
	}

	var required *domain.StarsPaymentRequiredError
	if _, err := send(member.ID, 9201, 0); !errors.As(err, &required) || required.Stars != 5 {
		t.Fatalf("unpaid member send err = %v, want payment required 5", err)
	}
	res, err := send(member.ID, 9202, 5)
	if err != nil {
		t.Fatalf("paid member send: %v", err)
	}
	if res.Message.PaidMessageStars != 5 || res.SenderStarsBalance == nil || res.SenderStarsBalance.Balance != 7 {
		t.Fatalf("paid result stars=%d balance=%+v", res.Message.PaidMessageStars, res.SenderStarsBalance)
	}
	if balance(member.ID) != 7 || groupBalance() != 4 {
		t.Fatalf("balances member=%d group=%d, want 7/4", balance(member.ID), groupBalance())
	}
	// Overpaying still charges only the price.
	if _, err := send(member.ID, 9203, 100); err != nil || balance(member.ID) != 2 {
		t.Fatalf("overpay err=%v balance=%d", err, balance(member.ID))
	}
	if _, err := send(member.ID, 9204, 5); !errors.Is(err, domain.ErrStarsInsufficient) || balance(member.ID) != 2 {
		t.Fatalf("insufficient err=%v balance=%d", err, balance(member.ID))
	}
	// The owner writes for free.
	if res, err := send(owner.ID, 9205, 0); err != nil || res.Message.PaidMessageStars != 0 {
		t.Fatalf("owner send err=%v paid=%d", err, res.Message.PaidMessageStars)
	}
	// Price off: free for everyone again.
	if _, err := channels.SetPaidMessagesPrice(ctx, owner.ID, groupID, 0, false); err != nil {
		t.Fatal(err)
	}
	if _, err := send(member.ID, 9206, 0); err != nil || balance(member.ID) != 2 {
		t.Fatalf("free send err=%v balance=%d", err, balance(member.ID))
	}
}
