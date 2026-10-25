package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// sellInvoice publishes one invoice and returns it the way a payer would address it.
func (c *invoiceChain) sellInvoice(t *testing.T, payload string, amount int64) (*tg.InputInvoiceMessage, domain.BotInvoice) {
	t.Helper()
	if _, err := c.router.BotAPISendInvoice(context.Background(),
		c.botID, c.buyerID, "Товар", "Оплата звёздами", payload, amount); err != nil {
		t.Fatalf("sendInvoice: %v", err)
	}
	payerID := c.newestInvoiceID(t)
	stored, found, err := c.router.deps.Bots.BotInvoiceByMessage(context.Background(), c.botID, c.buyerID, payerID)
	if err != nil || !found {
		t.Fatalf("invoice lookup: %v found=%v", err, found)
	}
	return &tg.InputInvoiceMessage{Peer: &tg.InputPeerUser{UserID: c.botID, AccessHash: 1}, MsgID: payerID}, stored
}

// payStarsForm drives the payment the way TDesktop does and returns the raw error,
// so a gate failure can be inspected instead of aborting the test.
func (c *invoiceChain) payStarsForm(t *testing.T, invoice *tg.InputInvoiceMessage, invoiceID int64) error {
	t.Helper()
	var buf bin.Buffer
	req := &tg.PaymentsSendStarsFormRequest{FormID: invoiceID + botInvoiceFormIDBase, Invoice: invoice}
	if err := req.Encode(&buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	_, err := c.router.Dispatch(WithUserID(context.Background(), c.buyerID), [8]byte{}, 0, &buf)
	return err
}

// answerNext waits for the gate to ask, then answers it as the given bot would
// through messages.setBotPrecheckoutResults. It reports whether a question was
// there to answer.
//
// It never touches *testing.T because it runs on its own goroutine alongside the
// payment: a late caller outlives its test, and logging after completion panics.
func (c *invoiceChain) answerNext(botID int64, ans domain.BotPreCheckoutAnswer) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pending := c.router.preCheckouts.pendingSnapshot()
		if len(pending) == 0 {
			time.Sleep(2 * time.Millisecond)
			continue
		}
		req := &tg.MessagesSetBotPrecheckoutResultsRequest{QueryID: pending[0]}
		if ans.OK {
			req.SetSuccess(true)
		} else {
			req.SetError(ans.Error)
		}
		// A rejected answer is the point of the ownership test, so the result here
		// is deliberately not asserted.
		_, _ = c.router.onMessagesSetBotPrecheckoutResults(WithUserID(context.Background(), botID), req)
		return true
	}
	return false
}

// The gate has to actually block: only the owning bot's answer opens it.
func TestBotInvoicePreCheckoutGate(t *testing.T) {
	t.Run("approved lets the payment settle", func(t *testing.T) {
		chain := newInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "gate-ok", 100)
		go chain.answerNext(chain.botID, domain.BotPreCheckoutAnswer{OK: true})
		if err := chain.payStarsForm(t, invoice, stored.ID); err != nil {
			t.Fatalf("approved pre-checkout failed the payment: %v", err)
		}
		if chain.walletBalance(t) != 100 {
			t.Fatalf("bot wallet = %d, want 100", chain.walletBalance(t))
		}
	})

	t.Run("declined stops the payment", func(t *testing.T) {
		chain := newInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "gate-no", 100)
		go chain.answerNext(chain.botID, domain.BotPreCheckoutAnswer{OK: false, Error: "товара нет"})
		err := chain.payStarsForm(t, invoice, stored.ID)
		if err == nil {
			t.Fatal("a declined pre-checkout still let the payment through")
		}
		// Assert on what reaches the wire: ErrorMessage carries rpcErr.Message,
		// while err.Error() prints the error type instead.
		code, ok := tgerr.As(err)
		if !ok {
			t.Fatalf("decline error = %v, want an rpc_error", err)
		}
		if code.Message != "товара нет" {
			t.Fatalf("decline reason = %q, want %q", code.Message, "товара нет")
		}
		if code.Type != "PAYMENT_FAILED" {
			t.Fatalf("decline type = %q, want PAYMENT_FAILED", code.Type)
		}
		if chain.walletBalance(t) != 0 {
			t.Fatalf("bot wallet = %d after a decline, want 0", chain.walletBalance(t))
		}
	})

	t.Run("silence fails the payment after ten seconds", func(t *testing.T) {
		chain := newInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "gate-silent", 100)
		// A late answer must not rescue a payment that already failed.
		go func() {
			time.Sleep(preCheckoutTimeout + 500*time.Millisecond)
			chain.answerNext(chain.botID, domain.BotPreCheckoutAnswer{OK: true})
		}()
		start := time.Now()
		err := chain.payStarsForm(t, invoice, stored.ID)
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("an unanswered pre-checkout still let the payment through")
		}
		if code, ok := tgerr.As(err); !ok || code.Code != 400 {
			t.Fatalf("timeout error = %v, want a 400 rpc_error", err)
		}
		if elapsed < preCheckoutTimeout {
			t.Fatalf("gave up after %v, want it to wait the full %v", elapsed, preCheckoutTimeout)
		}
		if chain.walletBalance(t) != 0 {
			t.Fatalf("bot wallet = %d after a timeout, want 0", chain.walletBalance(t))
		}
	})

	t.Run("a foreign bot cannot open the gate", func(t *testing.T) {
		chain := newInvoiceChain(t)
		invoice, stored := chain.sellInvoice(t, "gate-foreign", 100)
		go chain.answerNext(chain.otherBotID, domain.BotPreCheckoutAnswer{OK: true})
		err := chain.payStarsForm(t, invoice, stored.ID)
		if err == nil {
			t.Fatal("a bot that does not own the invoice approved the payment")
		}
		if chain.walletBalance(t) != 0 {
			t.Fatalf("bot wallet = %d, want 0", chain.walletBalance(t))
		}
	})
}

// The 10 second window is a contract with Telegram rather than an implementation
// detail, so pin the number.
func TestPreCheckoutTimeoutIsTenSeconds(t *testing.T) {
	if preCheckoutTimeoutSeconds != 10 {
		t.Fatalf("pre-checkout window = %ds, want 10s", preCheckoutTimeoutSeconds)
	}
	if preCheckoutTimeout != 10*time.Second {
		t.Fatalf("pre-checkout timer = %v, want 10s", preCheckoutTimeout)
	}
}

// A declined payment must not leave the invoice flagged as paid, or the retry
// would hand back a receipt with no money behind it.
func TestDeclinedPreCheckoutLeavesInvoiceUnsettled(t *testing.T) {
	chain := newInvoiceChain(t)
	invoice, stored := chain.sellInvoice(t, "gate-unsettled", 100)
	go chain.answerNext(chain.botID, domain.BotPreCheckoutAnswer{OK: false, Error: "нет"})
	if err := chain.payStarsForm(t, invoice, stored.ID); err == nil {
		t.Fatal("decline was ignored")
	}
	// Invoices are keyed by the buyer's chat because private message ids are
	// scoped to the buyer's box.
	reloaded, found, err := chain.router.deps.Bots.BotInvoiceByMessage(context.Background(),
		chain.botID, chain.buyerID, invoice.MsgID)
	if err != nil || !found {
		t.Fatalf("lookup after decline: %v found=%v", err, found)
	}
	if reloaded.Settled() {
		t.Fatal("a declined invoice was left marked as paid")
	}
	_ = stored
}

// A replay of an already paid invoice must not ask the bot a second time: no
// responder is armed here, so a repeated question would time the replay out.
func TestSettledInvoiceSkipsPreCheckout(t *testing.T) {
	chain := newInvoiceChain(t)
	invoice, stored := chain.sellInvoice(t, "gate-replay", 100)
	go chain.answerNext(chain.botID, domain.BotPreCheckoutAnswer{OK: true})
	if err := chain.payStarsForm(t, invoice, stored.ID); err != nil {
		t.Fatalf("first payment: %v", err)
	}
	if err := chain.payStarsForm(t, invoice, stored.ID); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if chain.walletBalance(t) != 100 {
		t.Fatalf("bot wallet = %d after a replay, want 100", chain.walletBalance(t))
	}
}

// An answer for a question that no longer exists must be refused, not swallowed:
// a bot that is told "ok" while its answer landed nowhere would assume it
// approved a payment.
func TestAnswerUnknownPreCheckoutIsRejected(t *testing.T) {
	chain := newInvoiceChain(t)
	_, err := chain.router.onMessagesSetBotPrecheckoutResults(WithUserID(context.Background(), chain.botID),
		&tg.MessagesSetBotPrecheckoutResultsRequest{QueryID: 424242})
	if err == nil {
		t.Fatal("answering an unknown pre-checkout query succeeded")
	}
	if !tgerr.Is(err, "QUERY_ID_INVALID") {
		t.Fatalf("error = %v, want QUERY_ID_INVALID", err)
	}
}

// The pre-checkout question has to survive the whole delivery path: enqueued by
// the gate, read out of the queue by getUpdates, and projected to the Bot API
// shape the bot actually parses. It carries no peer and no message, which is
// exactly why the queue's message lookup used to discard it.
func TestPreCheckoutReachesBotAPIQueue(t *testing.T) {
	chain := newInvoiceChain(t)
	invoice, stored := chain.sellInvoice(t, "gate-delivery", 100)

	// The gate only exists while the payment is in flight, so the payment runs on
	// its own goroutine and the queue is read underneath it.
	payErr := make(chan error, 1)
	go func() { payErr <- chain.payStarsForm(t, invoice, stored.ID) }()
	go chain.answerNext(chain.botID, domain.BotPreCheckoutAnswer{OK: true})

	// Read it the way getUpdates does, not the way the store does. A row sitting
	// in the table proves nothing: BotAPIUpdates has to turn it into an event, and
	// that conversion is where a message-less update used to be rejected.
	var queued []domain.UpdateEvent
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		events, err := chain.router.BotAPIUpdates(context.Background(), chain.botID, 0)
		if err != nil {
			t.Fatalf("BotAPIUpdates: %v", err)
		}
		for _, event := range events {
			if event.Type == domain.UpdateEventBotPreCheckoutQuery {
				queued = events
			}
		}
		if queued != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if queued == nil {
		t.Fatal("no pre-checkout event reached getUpdates; the bot would time out and refuse the payment")
	}

	var found bool
	for _, event := range queued {
		if event.Type != domain.UpdateEventBotPreCheckoutQuery || event.BotPreCheckout == nil {
			continue
		}
		found = true
		query := event.BotPreCheckout
		if query.BotUserID != chain.botID || query.UserID != chain.buyerID {
			t.Fatalf("queued query = %+v, want the bot and the payer", query)
		}
		if query.TotalAmount != 100 || query.Currency != domain.PremiumCurrencyStars {
			t.Fatalf("queued query = %+v, want 100 XTR", query)
		}
		if query.Payload != "gate-delivery" {
			t.Fatalf("payload = %q, want the invoice payload", query.Payload)
		}
	}
	if !found {
		t.Fatalf("queue held a pre_checkout kind with no payload: %+v", queued)
	}

	if err := <-payErr; err != nil {
		t.Fatalf("payment: %v", err)
	}
}
