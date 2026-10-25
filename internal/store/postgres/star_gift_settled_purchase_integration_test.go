package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestSettledStarGiftPurchaseReplaysBotCommandKeyPostgres covers the read half of
// bot-initiated gift idempotency. PurchaseStarGift replays only when the retry
// carries the same form_id, while a Bot API retry mints a fresh form, so this read
// is what makes a retried sendGift answer success instead of a bogus failure — and
// what keeps a reused key from answering with another request's result.
func TestSettledStarGiftPurchaseReplaysBotCommandKeyPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	buyer := createTestUser(t, ctx, users, "+1883"+suffix+"01", "SettledBuyer", "")
	recipient := createTestUser(t, ctx, users, "+1883"+suffix+"02", "SettledRecipient", "")
	other := createTestUser(t, ctx, users, "+1883"+suffix+"03", "SettledOther", "")
	recipientPeer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	otherPeer := domain.Peer{Type: domain.PeerTypeUser, ID: other.ID}

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, buyer.ID, 10000, now); err != nil {
		t.Fatalf("grant buyer stars: %v", err)
	}
	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Settled Gift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "settled.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "settled"), Animation: collectibleTestAnimation("settled.tgs"),
		Actor: "integration", CommandID: "settled-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create settled catalog: %v", err)
	}
	otherEntry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Settled Gift Other " + suffix, Stars: 60, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID+8, "settled-other.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID+8, "settled-other"), Animation: collectibleTestAnimation("settled-other.tgs"),
		Actor: "integration", CommandID: "settled-catalog-other-" + suffix,
	})
	if err != nil {
		t.Fatalf("create other settled catalog: %v", err)
	}
	messages := newTestMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))

	commandKey := "botapi-stargift:" + suffix + ":send"
	base := domain.StarGiftPurchaseRequest{
		BuyerUserID: buyer.ID, To: recipientPeer, GiftID: entry.Gift.ID, CommandKey: commandKey,
		Date: now, Message: "hello", MessageEntities: []domain.MessageEntity{
			{Type: domain.MessageEntityBold, Offset: 0, Length: 5},
		},
	}
	if _, found, err := lifecycle.SettledStarGiftPurchase(ctx, base); err != nil || found {
		t.Fatalf("unused command key = found %v err %v, want false/nil", found, err)
	}
	purchased, err := purchaseStarGiftForTest(ctx, lifecycle, issueLifecyclePurchaseForm(t, ctx, lifecycle, base))
	if err != nil || purchased.Saved.ID <= 0 {
		t.Fatalf("purchase = %+v err %v", purchased, err)
	}
	if balance, err := stars.GetBalance(ctx, buyer.ID); err != nil || balance.Balance != 9950 {
		t.Fatalf("balance after purchase = %+v err %v", balance, err)
	}

	// The Bot API retry shape: same command key, brand new payment form.
	retry := issueLifecyclePurchaseForm(t, ctx, lifecycle, base)
	if retry.FormID == 0 {
		t.Fatal("retry must carry a fresh form id")
	}
	settled, found, err := lifecycle.SettledStarGiftPurchase(ctx, retry)
	if err != nil || !found {
		t.Fatalf("settled replay = found %v err %v", found, err)
	}
	if !settled.Duplicate || settled.Saved.ID != purchased.Saved.ID || settled.Saved.MsgID != purchased.Saved.MsgID ||
		settled.Gift.ID != entry.Gift.ID || settled.Balance.Balance != 9950 {
		t.Fatalf("settled replay = %+v", settled)
	}
	if balance, err := stars.GetBalance(ctx, buyer.ID); err != nil || balance.Balance != 9950 {
		t.Fatalf("replay must not debit again = %+v err %v", balance, err)
	}

	// The same key carrying a different request must not be answered with the
	// committed gift: the caller would believe its own send succeeded.
	conflicts := map[string]domain.StarGiftPurchaseRequest{
		"recipient": {BuyerUserID: buyer.ID, To: otherPeer, GiftID: entry.Gift.ID, CommandKey: commandKey, Date: now + 1},
		"gift":      {BuyerUserID: buyer.ID, To: recipientPeer, GiftID: otherEntry.Gift.ID, CommandKey: commandKey, Date: now + 1},
		"text":      {BuyerUserID: buyer.ID, To: recipientPeer, GiftID: entry.Gift.ID, CommandKey: commandKey, Date: now + 1, Message: "other"},
		"entities":  {BuyerUserID: buyer.ID, To: recipientPeer, GiftID: entry.Gift.ID, CommandKey: commandKey, Date: now + 1, Message: "hello"},
		"hidden":    {BuyerUserID: buyer.ID, To: recipientPeer, GiftID: entry.Gift.ID, CommandKey: commandKey, Date: now + 1, Message: "hello", HideName: true, MessageEntities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 5}}},
	}
	for name, req := range conflicts {
		t.Run(name, func(t *testing.T) {
			if _, _, err := lifecycle.SettledStarGiftPurchase(ctx, req); !errors.Is(err, domain.ErrStarGiftIdempotencyConflict) {
				t.Fatalf("err = %v, want ErrStarGiftIdempotencyConflict", err)
			}
		})
	}
	// A command key is scoped to its buyer, so the same string from another
	// account is an unused key rather than a conflict.
	if _, found, err := lifecycle.SettledStarGiftPurchase(ctx, domain.StarGiftPurchaseRequest{
		BuyerUserID: other.ID, To: recipientPeer, GiftID: entry.Gift.ID, CommandKey: commandKey, Date: now + 1,
	}); err != nil || found {
		t.Fatalf("other buyer key = found %v err %v, want false/nil", found, err)
	}
	for name, req := range map[string]domain.StarGiftPurchaseRequest{
		"no buyer": {To: recipientPeer, GiftID: entry.Gift.ID, CommandKey: commandKey},
		"no peer":  {BuyerUserID: buyer.ID, GiftID: entry.Gift.ID, CommandKey: commandKey},
		"no gift":  {BuyerUserID: buyer.ID, To: recipientPeer, CommandKey: commandKey},
		"no key":   {BuyerUserID: buyer.ID, To: recipientPeer, GiftID: entry.Gift.ID},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := lifecycle.SettledStarGiftPurchase(ctx, req); !errors.Is(err, domain.ErrStarGiftInvalid) {
				t.Fatalf("err = %v, want ErrStarGiftInvalid", err)
			}
		})
	}
}
