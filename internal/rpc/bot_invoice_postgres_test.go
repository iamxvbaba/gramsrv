package rpc

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appbots "telesrv/internal/app/bots"
	appmessages "telesrv/internal/app/messages"
	appstars "telesrv/internal/app/stars"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// pgInvoiceGifts credits the bot wallet like the production service does.
type pgInvoiceGifts struct {
	GiftsService
	balance int64
}

func (g *pgInvoiceGifts) CreditBotStarsWallet(_ context.Context, credit domain.BotStarsCredit) (int64, bool, error) {
	g.balance += credit.Amount
	return g.balance, true, nil
}

// The memory store hands out one message id per participant box per send, and
// that keeps the two boxes in lockstep. Postgres is the backend that runs in
// production, so the chain has to hold there rather than in a stand-in.
func TestBotInvoiceChainPostgres(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run")
	}
	ctx := context.Background()
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)

	users := postgres.NewUserStore(pool)
	dialogs := postgres.NewDialogStore(pool)
	msgStore := postgres.NewMessageStore(pool)
	bots := appbots.NewService(users, postgres.NewBotStore(pool), msgStore)

	// The phone is unique per user, so a rerun against the same database needs a
	// fresh one rather than colliding with the previous run.
	suffix := time.Now().UnixNano() % 100000000
	buyer, err2 := users.Create(ctx, domain.User{
		AccessHash: 77001 + suffix, Phone: fmt.Sprintf("1555%08d", suffix), FirstName: "Pg Buyer",
	})
	if err2 != nil {
		t.Fatalf("create buyer: %v", err2)
	}
	third, err3 := users.Create(ctx, domain.User{
		AccessHash: 78001 + suffix, Phone: fmt.Sprintf("1556%08d", suffix), FirstName: "Third Party",
	})
	if err3 != nil {
		t.Fatalf("create third: %v", err3)
	}
	bot, _, err := bots.CreateBot(ctx, buyer.ID, "Pg Chain Bot", fmt.Sprintf("chain%d_bot", suffix))
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	messages := appmessages.NewService(msgStore, dialogs)
	stars := appstars.NewService(postgres.NewStarsStore(pool), appstars.WithStartingGrant(5000))
	gifts := &pgInvoiceGifts{}
	r := New(Config{}, Deps{
		Users:         appusers.NewService(users),
		Bots:          bots,
		Messages:      messages,
		Stars:         stars,
		Gifts:         gifts,
		BotAPIUpdates: postgres.NewBotAPIUpdateStore(pool),
	}, zaptest.NewLogger(t), clock.System)

	// Box ids are per user, not per chat. Create allocates in the buyer's box
	// alone, which puts the two participants at different counters - the state
	// any live server is in once the bot and the buyer have both been busy.
	if _, err := msgStore.Create(ctx, domain.Message{
		OwnerUserID: buyer.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: third.ID},
		From: domain.Peer{Type: domain.PeerTypeUser, ID: third.ID},
		Out:  true, Body: "unrelated", Date: 1700000001,
	}); err != nil {
		t.Fatalf("skew buyer box: %v", err)
	}
	// The buyer writes next, so the skew survives into the invoice send.
	if _, err := messages.SendPrivateText(ctx, buyer.ID, domain.SendPrivateTextRequest{
		SenderUserID: buyer.ID, RecipientUserID: bot.ID, RandomID: 900001,
		Message: "привет", Date: 1700000000,
	}); err != nil {
		t.Fatalf("buyer message: %v", err)
	}

	msg, err := r.BotAPISendInvoice(ctx, bot.ID, buyer.ID, "Товар", "Оплата 100 звёздами", "pg-item-1", 100)
	if err != nil {
		t.Fatalf("sendInvoice: %v", err)
	}

	// The id the payer can reference is the one the buyer's own box holds.
	payerID := pgBuyerInvoiceID(t, msgStore, buyer.ID)
	t.Logf("bot reported message_id=%d, payer box id=%d", msg.ID, payerID)

	stored, found, err := r.deps.Bots.BotInvoiceByMessage(ctx, bot.ID, bot.ID, payerID)
	if err != nil || !found {
		t.Fatalf("invoice under payer id %d: err=%v found=%v, want found", payerID, err, found)
	}
	if stored.MessageID != payerID {
		t.Fatalf("stored message id = %d, want %d", stored.MessageID, payerID)
	}

	invoice := &tg.InputInvoiceMessage{Peer: &tg.InputPeerUser{UserID: bot.ID, AccessHash: 0}, MsgID: payerID}
	var buf bin.Buffer
	if err := (&tg.PaymentsGetPaymentFormRequest{Invoice: invoice}).Encode(&buf); err != nil {
		t.Fatalf("encode form: %v", err)
	}
	got, err := r.Dispatch(WithUserID(ctx, buyer.ID), [8]byte{}, 0, &buf)
	if err != nil {
		t.Fatalf("getPaymentForm: %v", err)
	}
	form, ok := got.(*tg.PaymentsPaymentFormStars)
	if !ok {
		t.Fatalf("form type = %T, want *tg.PaymentsPaymentFormStars", got)
	}
	if len(form.Invoice.Prices) != 1 || form.Invoice.Prices[0].Amount != 100 {
		t.Fatalf("form = %+v, want one price of 100", form.Invoice)
	}

	before, err := stars.GetBalance(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	// The payment gate is answered before the request goes out, mirroring a bot
	// that approves; the point of this test is the money, not the 10s window.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if pending := r.preCheckouts.pendingSnapshot(); len(pending) > 0 {
				req := &tg.MessagesSetBotPrecheckoutResultsRequest{QueryID: pending[0]}
				req.SetSuccess(true)
				_, _ = r.onMessagesSetBotPrecheckoutResults(WithUserID(ctx, bot.ID), req)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	var payBuf bin.Buffer
	if err := (&tg.PaymentsSendPaymentFormRequest{
		Credentials: &tg.InputPaymentCredentials{},
		FormID:      stored.ID + botInvoiceFormIDBase,
		Invoice:     invoice,
	}).Encode(&payBuf); err != nil {
		t.Fatalf("encode payment: %v", err)
	}
	if _, err := r.Dispatch(WithUserID(ctx, buyer.ID), [8]byte{}, 0, &payBuf); err != nil {
		t.Fatalf("sendPaymentForm: %v", err)
	}
	after, err := stars.GetBalance(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("balance after: %v", err)
	}
	if after.Balance != before.Balance-100 {
		t.Fatalf("payer balance = %d, want %d", after.Balance, before.Balance-100)
	}
	if gifts.balance != 100 {
		t.Fatalf("bot wallet = %d, want 100", gifts.balance)
	}
}

// pgBuyerInvoiceID scans the buyer's own box for the invoice the bot sent.
func pgBuyerInvoiceID(t *testing.T, store *postgres.MessageStore, buyerID int64) int {
	t.Helper()
	ids := []int{}
	for i := 1; i <= 40; i++ {
		ids = append(ids, i)
	}
	list, err := store.GetByIDs(context.Background(), buyerID, ids)
	if err != nil {
		t.Fatalf("scan buyer box: %v", err)
	}
	for _, m := range list.Messages {
		if m.Media != nil && m.Media.Kind == domain.MessageMediaKindInvoice {
			return m.ID
		}
	}
	t.Fatalf("no invoice in the buyer box (count=%d)", list.Count)
	return 0
}
