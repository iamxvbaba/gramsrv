package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// A bot's spendable Stars are the revenue it earned from settled invoices, which
// lands in bot_stars_balances. The bot user identity has no stars_balances row at
// all, so charging a gift purchase against the personal ledger refused every bot
// with ErrStarsInsufficient — a BALANCE_TOO_LOW no matter how full the wallet was.
// These tests pin the wallet routing so that regression cannot come back.

// TestBotGiftPurchaseDebitsBotWalletPostgres proves a bot-initiated sendGift is
// charged to bot_stars_balances and leaves the personal ledger untouched.
func TestBotGiftPurchaseDebitsBotWalletPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	payer := createTestUser(t, ctx, users, "+1896"+suffix+"01", "BotGiftPayer", "")
	bot := createTestUser(t, ctx, users, "+1896"+suffix+"02", "BotGiftBot", "")
	recipient := createTestUser(t, ctx, users, "+1896"+suffix+"03", "BotGiftRecipient", "")

	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1_000_000)
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: payer.ID, Amount: 300,
		Reason: domain.StarsReasonBotInvoice, InvoiceKey: "botgift-" + suffix, Date: now,
	}); err != nil {
		t.Fatalf("credit bot wallet: %v", err)
	}

	stars := NewStarsStore(pool)
	// A personal balance on the bot identity must not exist, otherwise the test
	// would pass for the wrong reason.
	if balance, err := stars.GetBalance(ctx, bot.ID); err == nil && balance.Balance != 0 {
		t.Fatalf("bot personal balance = %d, want 0", balance.Balance)
	}

	// The origin pair must be a zero pair. Bot API has no MTProto session, so a
	// lone OriginAuthKeyID made enqueueDispatch reject the whole transaction with
	// errInvalidDispatchOutboxExclusionPair — a 500 no matter what the wallet held.
	// This exercises the real message-send path, which is the only place the guard
	// fires, so a regression cannot hide behind the wallet assertions below.

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Bot Wallet Gift " + suffix, Stars: 150, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "bot-wallet.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "bot-wallet"), Animation: collectibleTestAnimation("bot-wallet.tgs"),
		Actor: "integration", CommandID: "bot-wallet-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create bot wallet catalog: %v", err)
	}
	// Priced above what is left in the wallet after the first gift, so spending it
	// is a real overdraft rather than an exact-balance spend.
	pricey, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Bot Wallet Pricey " + suffix, Stars: 400, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID+8, "bot-wallet-pricey.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID+8, "bot-wallet-pricey"), Animation: collectibleTestAnimation("bot-wallet-pricey.tgs"),
		Actor: "integration", CommandID: "bot-wallet-pricey-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create pricey catalog: %v", err)
	}

	base := domain.StarGiftPurchaseRequest{
		BuyerUserID: bot.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
		GiftID: entry.Gift.ID, CommandKey: "botapi-stargift:" + suffix + ":send", Date: now,
		BuyerIsBot: true, Message: "from the bot",
	}

	// A half origin pair must be refused outright, and must not touch the wallet:
	// this is the guard that made sendGift fail for every gift regardless of funds.
	halfPair := base
	halfPair.OriginAuthKeyID = [8]byte{'B', 'O', 'T', 'A', 'P', 'I', 0, 1}
	halfPair.CommandKey = "botapi-stargift:" + suffix + ":halfpair"
	if _, err := lifecycle.PurchaseStarGift(ctx, issueLifecyclePurchaseForm(t, ctx, lifecycle, halfPair)); !errors.Is(err, errInvalidDispatchOutboxExclusionPair) {
		t.Fatalf("half origin pair err = %v, want errInvalidDispatchOutboxExclusionPair", err)
	}
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 300 {
		t.Fatalf("wallet after rejected half pair = %d err %v, want 300", balance, err)
	}

	purchased, err := lifecycle.PurchaseStarGift(ctx, issueLifecyclePurchaseForm(t, ctx, lifecycle, base))
	if err != nil {
		t.Fatalf("bot purchase: %v", err)
	}
	if purchased.Saved.ID <= 0 || purchased.Saved.FromUserID != bot.ID {
		t.Fatalf("purchased = %+v", purchased.Saved)
	}

	// The wallet paid for it, and the reported balance is the wallet balance.
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 150 {
		t.Fatalf("wallet after purchase = %d err %v, want 150", balance, err)
	}
	if purchased.Balance.UserID != bot.ID || purchased.Balance.Balance != 150 {
		t.Fatalf("purchase balance = %+v, want user %d balance 150", purchased.Balance, bot.ID)
	}
	// The personal ledger must not have been created or touched.
	if balance, err := stars.GetBalance(ctx, bot.ID); err == nil && balance.Balance != 0 {
		t.Fatalf("bot personal balance after purchase = %d, want 0", balance.Balance)
	}

	// The spend is journaled in the wallet so the operator's revenue screen shows
	// it, and no personal stars_transactions row was invented for the bot.
	var reason string
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT reason,amount FROM bot_stars_transactions
WHERE bot_user_id=$1 AND amount<0 ORDER BY id DESC LIMIT 1`, bot.ID).Scan(&reason, &amount); err != nil {
		t.Fatalf("read wallet spend: %v", err)
	}
	if reason != string(domain.StarsReasonBotSpend) || amount != -150 {
		t.Fatalf("wallet spend = %s/%d, want bot_spend/-150", reason, amount)
	}
	var personalRows int64
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM stars_transactions WHERE user_id=$1`, bot.ID).
		Scan(&personalRows); err != nil || personalRows != 0 {
		t.Fatalf("personal transactions = %d err %v, want 0", personalRows, err)
	}

	// A replay under the same command key must not spend the wallet twice.
	retry := issueLifecyclePurchaseForm(t, ctx, lifecycle, base)
	if _, found, err := lifecycle.SettledStarGiftPurchase(ctx, retry); err != nil || !found {
		t.Fatalf("settled replay = found %v err %v", found, err)
	}
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 150 {
		t.Fatalf("wallet after replay = %d err %v, want 150", balance, err)
	}

	// Spending past the wallet balance is still refused, and leaves it untouched.
	over := base
	over.GiftID = pricey.Gift.ID
	over.CommandKey = "botapi-stargift:" + suffix + ":over"
	over.Date = now + 1
	if _, err := lifecycle.PurchaseStarGift(ctx, issueLifecyclePurchaseForm(t, ctx, lifecycle, over)); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("overdraft err = %v, want ErrStarsInsufficient", err)
	}
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 150 {
		t.Fatalf("wallet after rejected overdraft = %d err %v, want 150", balance, err)
	}
}

// TestBotGiftPurchaseIsNotConvertibleToStarsPostgres pins the invariant behind
// Bot API sendGift: a gift the bot paid for must reach the recipient with
// convert_stars = 0. The catalog's convert price is a refund of the *buyer's*
// Stars, and a bot's Stars are invoice revenue sitting in bot_stars_balances,
// so copying it over would let whoever receives the gift cash the bot's spend
// out into their own balance. The same purchase path for a human buyer must
// keep the catalog price, otherwise the fix above would just disable
// conversion for everyone.
func TestBotGiftPurchaseIsNotConvertibleToStarsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1898"+suffix+"01", "BotNoConvertOwner", "")
	// The buyer must be an actual bot account (users.is_bot = true): bad-faith
	// RPC callers could replay a convertStarGift against a stale row, so the
	// sender-identity guard in ConvertStarGift is the final backstop and has to
	// see the flag the same way production does.
	bot, _, err := NewBotStore(pool).CreateBotAccount(ctx, domain.User{
		AccessHash: 1898_0001, FirstName: "NoConvertBot", Username: "botnoconvert" + suffix + "_bot",
	}, domain.BotProfile{OwnerUserID: owner.ID, TokenSecret: "secret_" + suffix})
	if err != nil {
		t.Fatalf("create bot account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", bot.ID) // the bots row follows via FK CASCADE
	})
	human := createTestUser(t, ctx, users, "+1898"+suffix+"02", "BotNoConvertHuman", "")
	botRecipient := createTestUser(t, ctx, users, "+1898"+suffix+"03", "BotNoConvertTo", "")
	humanRecipient := createTestUser(t, ctx, users, "+1898"+suffix+"04", "HumanNoConvertTo", "")

	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1_000_000)
	if _, _, err := lifecycle.CreditBotStarsWallet(ctx, domain.BotStarsCredit{
		BotUserID: bot.ID, PayerUserID: human.ID, Amount: 300,
		Reason: domain.StarsReasonBotInvoice, InvoiceKey: "bot-noconvert-" + suffix, Date: now,
	}); err != nil {
		t.Fatalf("credit bot wallet: %v", err)
	}
	stars := NewStarsStore(pool)
	if _, err := stars.Credit(ctx, human.ID, 300, domain.StarsReasonGrant,
		domain.Peer{Type: domain.PeerTypeUser, ID: human.ID}, now, "seed", ""); err != nil {
		t.Fatalf("credit human buyer: %v", err)
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "No Convert Gift " + suffix, Stars: 150, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "no-convert.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "no-convert"), Animation: collectibleTestAnimation("no-convert.tgs"),
		Actor: "integration", CommandID: "no-convert-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog: %v", err)
	}

	botPurchase, err := lifecycle.PurchaseStarGift(ctx, issueLifecyclePurchaseForm(t, ctx, lifecycle,
		domain.StarGiftPurchaseRequest{
			BuyerUserID: bot.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: botRecipient.ID},
			GiftID: entry.Gift.ID, CommandKey: "botapi-stargift:" + suffix + ":noconvert", Date: now,
			BuyerIsBot: true, Message: "from the bot",
		}))
	if err != nil {
		t.Fatalf("bot purchase: %v", err)
	}
	if botPurchase.Saved.ConvertStars != 0 {
		t.Fatalf("bot gift convert_stars = %d, want 0", botPurchase.Saved.ConvertStars)
	}
	// The stored row is what the profile projection and the service message read,
	// so the client only ever hides the convert button off that column.
	var stored int64
	if err := pool.QueryRow(ctx, `SELECT convert_stars FROM peer_star_gifts WHERE id=$1`,
		botPurchase.Saved.ID).Scan(&stored); err != nil {
		t.Fatalf("read stored gift: %v", err)
	}
	if stored != 0 {
		t.Fatalf("stored convert_stars = %d, want 0", stored)
	}

	// Converting it must be refused outright: the gift stays untouched, no Stars
	// credited, nothing archived. A stale client still showing the button gets a
	// clean error instead of silently losing the gift.
	if _, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{
		ActorUserID: botRecipient.ID, Date: now + 1,
		Ref: domain.SavedStarGiftRef{
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: botRecipient.ID},
			MsgID: botPurchase.Saved.MsgID,
		},
	}); !errors.Is(err, domain.ErrStarGiftNotConvertible) {
		t.Fatalf("convert bot gift err = %v, want ErrStarGiftNotConvertible", err)
	}
	// The failed conversion must not have touched the gift or the recipient's
	// balance.
	afterGift, found, err := NewStarGiftStore(pool).GetByRef(ctx, domain.SavedStarGiftRef{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: botRecipient.ID},
		MsgID: botPurchase.Saved.MsgID,
	})
	if err != nil || !found || afterGift.Converted {
		t.Fatalf("gift after refused conversion = found %v err %v, want unconverted", found, err)
	}
	if balance, err := stars.GetBalance(ctx, botRecipient.ID); err != nil || balance.Balance != 0 {
		t.Fatalf("recipient balance after refused conversion = %+v err %v, want 0", balance, err)
	}

	// A pre-0217 row is indistinguishable from this one except by the stored
	// column: simulate the old state (convert_stars still carrying the catalog
	// price) and prove the sender-identity guard refuses it regardless, so the
	// invariant does not depend on the migration having run first.
	if _, err := pool.Exec(ctx, `UPDATE peer_star_gifts SET convert_stars=20 WHERE id=$1`, botPurchase.Saved.ID); err != nil {
		t.Fatalf("simulate pre-fix convert_stars: %v", err)
	}
	if _, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{
		ActorUserID: botRecipient.ID, Date: now + 2,
		Ref: domain.SavedStarGiftRef{
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: botRecipient.ID},
			MsgID: botPurchase.Saved.MsgID,
		},
	}); !errors.Is(err, domain.ErrStarGiftNotConvertible) {
		t.Fatalf("pre-fix bot gift conversion err = %v, want ErrStarGiftNotConvertible", err)
	}

	humanPurchase, err := lifecycle.PurchaseStarGift(ctx, issueLifecyclePurchaseForm(t, ctx, lifecycle,
		domain.StarGiftPurchaseRequest{
			BuyerUserID: human.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: humanRecipient.ID},
			GiftID: entry.Gift.ID, CommandKey: "botapi-stargift:" + suffix + ":human", Date: now,
			Message: "from a person",
		}))
	if err != nil {
		t.Fatalf("human purchase: %v", err)
	}
	if humanPurchase.Saved.ConvertStars != 20 {
		t.Fatalf("human gift convert_stars = %d, want the catalog price 20", humanPurchase.Saved.ConvertStars)
	}
	// The same path must keep working for a human-sent gift (control): the guard
	// above targets bot senders only.
	humanConverted, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{
		ActorUserID: humanRecipient.ID, Date: now + 3,
		Ref: domain.SavedStarGiftRef{
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: humanRecipient.ID},
			MsgID: humanPurchase.Saved.MsgID,
		},
	})
	if err != nil {
		t.Fatalf("human gift conversion: %v", err)
	}
	if !humanConverted.Saved.Converted || humanConverted.OwnerBalance != 20 {
		t.Fatalf("human converted = %+v balance %d, want converted with 20", humanConverted.Saved, humanConverted.OwnerBalance)
	}
}

// TestGiftPurchaseForUnfundedBotRefusesPostgres pins the honest failure: a bot that
// never earned anything has an absent wallet row, which must read as an empty
// wallet rather than as an internal failure.
func TestGiftPurchaseForUnfundedBotRefusesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	bot := createTestUser(t, ctx, users, "+1897"+suffix+"01", "UnfundedBotGift", "")
	recipient := createTestUser(t, ctx, users, "+1897"+suffix+"02", "UnfundedRecipient", "")

	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1_000_000)
	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Unfunded Gift " + suffix, Stars: 10, ConvertStars: 5, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "unfunded.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "unfunded"), Animation: collectibleTestAnimation("unfunded.tgs"),
		Actor: "integration", CommandID: "unfunded-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create unfunded catalog: %v", err)
	}
	if _, err := lifecycle.PurchaseStarGift(ctx, issueLifecyclePurchaseForm(t, ctx, lifecycle,
		domain.StarGiftPurchaseRequest{
			BuyerUserID: bot.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID},
			GiftID: entry.Gift.ID, CommandKey: "botapi-stargift:" + suffix + ":unfunded", Date: now,
			BuyerIsBot: true,
		})); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("err = %v, want ErrStarsInsufficient", err)
	}
	if balance, err := lifecycle.BotStarsBalance(ctx, bot.ID); err != nil || balance != 0 {
		t.Fatalf("unfunded wallet = %d err %v, want 0", balance, err)
	}
}
