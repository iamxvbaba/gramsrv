package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"testing"
)

func TestBotInvoiceRefundFailureRollsBackAndCanRetry(t *testing.T) {
	c := newInvoiceChain(t)
	ctx := context.Background()
	if _, err := c.router.BotAPISendInvoice(ctx, c.botID, c.buyerID, "Item", "Description", "refund-fail", 100); err != nil {
		t.Fatal(err)
	}
	id := c.newestInvoiceID(t)
	stored, found, err := c.router.deps.Bots.BotInvoiceByMessage(ctx, c.botID, c.buyerID, id)
	if err != nil || !found {
		t.Fatalf("invoice: %v %v", found, err)
	}
	c.pay(t, stored.ID, &tg.InputInvoiceMessage{Peer: &tg.InputPeerUser{UserID: c.botID, AccessHash: 1}, MsgID: id})
	charge := botInvoiceChargeID(stored)
	credit := domain.BotStarsCredit{BotUserID: c.botID, PayerUserID: c.buyerID, Amount: -100, Reason: domain.StarsReasonBotRefund, InvoiceKey: charge + "-spend", Date: 1700000001}
	if _, _, err := c.gifts.CreditBotStarsWallet(ctx, credit); err != nil {
		t.Fatal(err)
	}
	balance := c.payerBalance(t)
	if _, err := c.router.BotAPIRefundStarPayment(ctx, c.botID, c.buyerID, charge); err == nil {
		t.Fatal("refund without funds succeeded")
	}
	got, _, err := c.router.deps.Bots.BotInvoiceByCharge(ctx, charge)
	if err != nil || got.Refunded || c.payerBalance(t) != balance {
		t.Fatalf("failed refund changed state: %+v %v", got, err)
	}
	credit.Amount = 100
	credit.InvoiceKey = charge + "-replenish"
	if _, _, err := c.gifts.CreditBotStarsWallet(ctx, credit); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := c.router.BotAPIRefundStarPayment(ctx, c.botID, c.buyerID, charge); err != nil {
			t.Fatal(err)
		}
	}
	if c.walletBalance(t) != 0 || c.payerBalance(t) != balance+100 {
		t.Fatal("retry moved funds more than once")
	}
}
