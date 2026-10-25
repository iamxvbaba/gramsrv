package rpc

import (
	"context"
	"fmt"
	"github.com/iamxvbaba/td/tg"
	"sync"
	"telesrv/internal/store"
	"testing"
	"time"
)

func TestBotInvoiceAtomicFailureAndConcurrentReplay(t *testing.T) {
	c := newInvoiceChain(t)
	ctx := context.Background()
	_, invoice := c.sellInvoice(t, "atomic-checkout", 100)
	balance := c.payerBalance(t)
	payments := c.router.deps.Bots.(store.BotInvoicePayments)
	now := int(time.Now().Unix())
	// Failure at the last write must roll back both ledgers, invoice state, message
	// boxes and their PTS, not merely return an error to the RPC caller.
	name := fmt.Sprintf("invoice_failure_%d", c.botID)
	sql := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.bot_user_id=%d THEN RAISE EXCEPTION 'injected delivery write failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER %s BEFORE INSERT ON bot_api_updates FOR EACH ROW EXECUTE FUNCTION %s()`, name, c.botID, name, name)
	if _, err := c.pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		if _, err := c.pool.Exec(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s ON bot_api_updates; DROP FUNCTION IF EXISTS %s()", name, name)); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(cleanup)
	var ptsBefore int
	if err := c.pool.QueryRow(ctx, `SELECT contiguous_pts FROM user_update_watermarks WHERE user_id=$1`, c.buyerID).Scan(&ptsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := payments.PayBotInvoice(ctx, invoice.ID, c.buyerID, now); err == nil {
		t.Fatal("injected write failure succeeded")
	}
	reloaded, _, err := c.router.deps.Bots.BotInvoiceByMessage(ctx, c.botID, c.buyerID, invoice.MessageID)
	if err != nil || reloaded.Paid || c.walletBalance(t) != 0 || c.payerBalance(t) != balance {
		t.Fatalf("partial payment: %+v %v", reloaded, err)
	}
	var ptsAfter int
	if err := c.pool.QueryRow(ctx, `SELECT contiguous_pts FROM user_update_watermarks WHERE user_id=$1`, c.buyerID).Scan(&ptsAfter); err != nil {
		t.Fatal(err)
	}
	if ptsAfter != ptsBefore {
		t.Fatalf("failed transaction consumed PTS: %d -> %d", ptsBefore, ptsAfter)
	}
	cleanup()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := payments.PayBotInvoice(ctx, invoice.ID, c.buyerID, now)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.walletBalance(t) != 100 || c.payerBalance(t) != balance-100 {
		t.Fatal("concurrent replay changed money more than once")
	}
	if countPaymentMessages(t, c) != 1 {
		t.Fatal("duplicate payment notification")
	}
	if _, err := payments.PayBotInvoice(ctx, invoice.ID, c.otherBotID, now); err == nil {
		t.Fatal("another principal paid the invoice")
	}
}

func TestBotInvoiceInsufficientFundsAndWrongFormLeaveUnpaid(t *testing.T) {
	c := newInvoiceChain(t)
	ctx := context.Background()
	input, invoice := c.sellInvoice(t, "unfunded-checkout", 6000)
	c.payerBalance(t)
	payments := c.router.deps.Bots.(store.BotInvoicePayments)
	if _, err := payments.PayBotInvoice(ctx, invoice.ID, c.buyerID, int(time.Now().Unix())); err == nil {
		t.Fatal("unfunded payment succeeded")
	}
	if _, _, err := c.router.botInvoiceSettle(WithUserID(ctx, c.buyerID), c.buyerID, &tg.PaymentsSendPaymentFormRequest{FormID: botInvoiceFormID(invoice) + 1}, input); err == nil {
		t.Fatal("foreign form accepted")
	}
	got, _, err := c.router.deps.Bots.BotInvoiceByMessage(ctx, c.botID, c.buyerID, invoice.MessageID)
	if err != nil || got.Paid || c.walletBalance(t) != 0 || c.payerBalance(t) != 5000 {
		t.Fatalf("rejected checkout changed state: %+v %v", got, err)
	}
}
