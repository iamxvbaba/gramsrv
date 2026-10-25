package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// The bot wallet is a separate bucket from the bot user identity's personal
// Stars balance, credits are idempotent per invoice key, and lifetime revenue
// must stay monotonic across refunds.

func TestBotStarsWalletCreditIsIdempotentPerInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	payer := createTestUser(t, ctx, users, "+1892"+suffix+"01", "InvoicePayer", "")
	bot := createTestUser(t, ctx, users, "+1892"+suffix+"02", "InvoiceBot", "")

	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)

	// A bot that was never paid reads zero instead of erroring.
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 0 {
		t.Fatalf("fresh bot balance = %d, err = %v, want 0, nil", balance, err)
	}

	credit := domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: payer.ID, Amount: 250,
		Reason: domain.StarsReasonBotInvoice, InvoiceKey: "inv-" + suffix, Date: now,
	}
	balance, credited, err := lifecycle.CreditBotStarsWallet(ctx, credit)
	if err != nil || !credited || balance != 250 {
		t.Fatalf("credit = %d, %v, %v, want 250, true, nil", balance, credited, err)
	}

	// A retried payments.sendPaymentForm must not mint a second credit.
	balance, credited, err = lifecycle.CreditBotStarsWallet(ctx, credit)
	if err != nil || credited || balance != 250 {
		t.Fatalf("replayed credit = %d, %v, %v, want 250, false, nil", balance, credited, err)
	}
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 250 {
		t.Fatalf("balance after replay = %d, err = %v, want 250, nil", balance, err)
	}
	if revenue, err := lifecycle.BotStarsOverallRevenue(ctx, bot.ID); err != nil || revenue != 250 {
		t.Fatalf("revenue after replay = %d, err = %v, want 250, nil", revenue, err)
	}

	// A second, distinct invoice accumulates.
	second := credit
	second.Amount = 100
	second.InvoiceKey = "inv2-" + suffix
	if balance, credited, err := lifecycle.CreditBotStarsWallet(ctx, second); err != nil || !credited || balance != 350 {
		t.Fatalf("second credit = %d, %v, %v, want 350, true, nil", balance, credited, err)
	}

	// The wallet is separate from the payer's and the bot's personal balances:
	// crediting revenue must not move any personal Stars row.
	if balance, err := NewStarsStore(pool).GetBalance(ctx, payer.ID); err == nil && balance.Balance > 0 {
		t.Fatalf("payer personal balance = %d, want 0: bot revenue must stay separate", balance.Balance)
	}
}

func TestBotStarsWalletRefundKeepsRevenueMonotonicAndRejectsOverdraft(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	payer := createTestUser(t, ctx, users, "+1893"+suffix+"01", "RefundPayer", "")
	bot := createTestUser(t, ctx, users, "+1893"+suffix+"02", "RefundBot", "")

	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: payer.ID, Amount: 400,
		Reason: domain.StarsReasonBotInvoice, InvoiceKey: "refund-" + suffix, Date: now,
	}); err != nil {
		t.Fatalf("credit: %v", err)
	}

	refund := domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: payer.ID, Amount: -150,
		Reason: domain.StarsReasonBotRefund, InvoiceKey: "refund2-" + suffix, Date: now,
	}
	balance, credited, err := lifecycle.CreditBotStarsWallet(ctx, refund)
	if err != nil || !credited || balance != 250 {
		t.Fatalf("refund = %d, %v, %v, want 250, true, nil", balance, credited, err)
	}
	// Overall revenue is lifetime positive income: a refund lowers the balance
	// but must never lower the reported revenue.
	if revenue, err := lifecycle.BotStarsOverallRevenue(ctx, bot.ID); err != nil || revenue != 400 {
		t.Fatalf("revenue after refund = %d, err = %v, want 400, nil", revenue, err)
	}
	// A refund cannot overdraw the wallet.
	overdraw := refund
	overdraw.Amount = -1000
	overdraw.InvoiceKey = "refund3-" + suffix
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, overdraw); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("overdraw err = %v, want ErrStarsInsufficient", err)
	}
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 250 {
		t.Fatalf("balance after rejected overdraft = %d, err = %v, want 250, nil", balance, err)
	}
}

func TestBotStarsRevenueSeriesGroupsByUTCDay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	payer := createTestUser(t, ctx, users, "+1895"+suffix+"01", "SeriesPayer", "")
	bot := createTestUser(t, ctx, users, "+1895"+suffix+"02", "SeriesBot", "")

	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	// 2023-11-14T22:00:00Z and the two following days, so the UTC bucketing is
	// observable rather than accidentally aligned to the insert time.
	day := (1700006400 / 86400) * 86400
	for i, step := range []struct {
		dayOffset int
		amount    int64
		reason    domain.StarsTransactionReason
	}{
		{0, 100, domain.StarsReasonBotInvoice},
		{0, 50, domain.StarsReasonBotInvoice},
		{86400, 200, domain.StarsReasonBotInvoice},
		{86400, -70, domain.StarsReasonBotRefund},
	} {
		if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
			BotUserID: bot.ID, PayerUserID: payer.ID, Amount: step.amount,
			Reason: step.reason, InvoiceKey: suffix + "-series-" + string(rune('a'+i)), Date: day + step.dayOffset,
		}); err != nil {
			t.Fatalf("credit %d: %v", i, err)
		}
	}

	// A credit outside the requested window must not appear.
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: payer.ID, Amount: 999,
		Reason: domain.StarsReasonBotInvoice, InvoiceKey: suffix + "-outside", Date: now,
	}); err != nil {
		t.Fatalf("outside credit: %v", err)
	}

	points, err := lifecycle.BotStarsRevenueSeries(ctx, bot.ID, day, day+86400)
	if err != nil {
		t.Fatalf("revenue series: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("series len = %d, want 2 days", len(points))
	}
	if points[0].DayStart != day || points[0].Amount != 150 || points[0].Spent != 0 {
		t.Fatalf("first day = %+v, want day=%d amount=150 withdrawn=0", points[0], day)
	}
	if points[1].DayStart != day+86400 || points[1].Amount != 200 || points[1].Spent != 70 {
		t.Fatalf("second day = %+v, want day=%d amount=200 withdrawn=70", points[1], day+86400)
	}
	// The out-of-window credit is excluded, proving the range filter works.
	for _, point := range points {
		if point.Amount == 999 {
			t.Fatal("out-of-window credit leaked into the series")
		}
	}

	// An inverted window must fail rather than scan the whole journal.
	if _, err := lifecycle.BotStarsRevenueSeries(ctx, bot.ID, day+86400, day); !errors.Is(err, domain.ErrStarGiftLifecycleInvalid) {
		t.Fatalf("inverted range err = %v, want ErrStarGiftLifecycleInvalid", err)
	}
	if _, err := lifecycle.BotStarsRevenueSeries(ctx, 0, day, day+86400); !errors.Is(err, domain.ErrStarGiftOwnerInvalid) {
		t.Fatalf("zero bot err = %v, want ErrStarGiftOwnerInvalid", err)
	}
}

func TestBotStarsWalletTransactionsPageAndRejectsInvalidCredit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	payer := createTestUser(t, ctx, users, "+1894"+suffix+"01", "HistoryPayer", "")
	bot := createTestUser(t, ctx, users, "+1894"+suffix+"02", "HistoryBot", "")

	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	for i, amount := range []int64{30, 20} {
		if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
			BotUserID: bot.ID, PayerUserID: payer.ID, Amount: amount,
			Reason: domain.StarsReasonBotInvoice, InvoiceKey: suffix + "-" + string(rune('a'+i)), Date: now,
		}); err != nil {
			t.Fatalf("credit %d: %v", i, err)
		}
	}

	page, err := lifecycle.BotStarsTransactions(ctx, bot.ID, domain.StarsTransactionQuery{
		Limit: 10, Direction: domain.StarsTransactionDirectionIncoming,
	})
	if err != nil {
		t.Fatalf("transactions: %v", err)
	}
	if len(page.Transactions) != 2 {
		t.Fatalf("transactions len = %d, want 2", len(page.Transactions))
	}
	if page.Balance != 50 {
		t.Fatalf("page balance = %d, want 50", page.Balance)
	}
	for _, txn := range page.Transactions {
		if txn.UserID != bot.ID || txn.Reason != domain.StarsReasonBotInvoice {
			t.Fatalf("transaction owner/reason = %d/%s, want %d/%s",
				txn.UserID, txn.Reason, bot.ID, domain.StarsReasonBotInvoice)
		}
	}

	// A malformed credit must never reach SQL.
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: payer.ID, Amount: 10,
		Reason: domain.StarsReasonAdjust, InvoiceKey: "bad-" + suffix, Date: now,
	}); !errors.Is(err, domain.ErrStarGiftLifecycleInvalid) {
		t.Fatalf("unsupported reason err = %v, want ErrStarGiftLifecycleInvalid", err)
	}
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
		BotUserID: 0, PayerUserID: payer.ID, Amount: 10,
		Reason: domain.StarsReasonBotInvoice, InvoiceKey: "bad2-" + suffix, Date: now,
	}); !errors.Is(err, domain.ErrStarGiftLifecycleInvalid) {
		t.Fatalf("zero bot err = %v, want ErrStarGiftLifecycleInvalid", err)
	}
}
