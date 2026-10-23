package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

func TestDeleteStarGiftRemovesOrdinaryGift(t *testing.T) {
	r, sender, recipient, gift := starGiftTestRouter(t)
	ctx := context.Background()
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}
	record := func(msgID int, uniqueID int64) {
		t.Helper()
		if _, err := r.deps.Gifts.RecordSavedGift(ctx, domain.SavedStarGift{
			Owner: owner, FromUserID: sender.ID, GiftID: gift.ID, RevisionID: gift.RevisionID,
			MsgID: msgID, Date: 1, ConvertStars: gift.ConvertStars, UniqueGiftID: uniqueID,
			LifecycleStatus: domain.StarGiftLifecycleActive,
		}); err != nil {
			t.Fatal(err)
		}
	}
	record(31, 0)
	before, err := r.deps.Gifts.CountSaved(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}

	// Somebody else cannot delete Recipient's gift: the reference resolves
	// against the caller's own messages.
	if ok, err := r.onPaymentsDeleteStarGift(WithUserID(ctx, sender.ID), &tg.PaymentsDeleteStarGiftRequest{
		Stargift: &tg.InputSavedStarGiftUser{MsgID: 31},
	}); ok || !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("foreign delete = %v, %v", ok, err)
	}

	recipientCtx := WithUserID(ctx, recipient.ID)
	ok, err := r.onPaymentsDeleteStarGift(recipientCtx, &tg.PaymentsDeleteStarGiftRequest{Stargift: &tg.InputSavedStarGiftUser{MsgID: 31}})
	if err != nil || !ok {
		t.Fatalf("delete = %v, %v", ok, err)
	}
	after, err := r.deps.Gifts.CountSaved(ctx, owner)
	if err != nil || after != before-1 {
		t.Fatalf("saved count %d -> %d (%v)", before, after, err)
	}
	if _, found, _ := r.deps.Gifts.GetSaved(ctx, domain.SavedStarGiftRef{Owner: owner, MsgID: 31}); found {
		t.Fatal("deleted gift still resolves")
	}
	if ok, err := r.onPaymentsDeleteStarGift(recipientCtx, &tg.PaymentsDeleteStarGiftRequest{Stargift: &tg.InputSavedStarGiftUser{MsgID: 31}}); ok || !tgerr.Is(err, "STARGIFT_INVALID") {
		t.Fatalf("second delete = %v, %v", ok, err)
	}
}

func TestLayer232ChainGiftMethodsRefuseWithoutTON(t *testing.T) {
	r, _, recipient, _ := starGiftTestRouter(t)
	ctx := WithAuthKeyID(WithUserID(context.Background(), recipient.ID), [8]byte{1, 2, 3})
	for name, err := range map[string]error{
		"withdraw":      r.walletCapabilityUnsupported(ctx, "gift-withdraw", 20),
		"deposit":       r.walletCapabilityUnsupported(ctx, "gift-deposit", 20),
		"display":       r.walletCapabilityUnsupported(ctx, "gift-display", 60),
		"card stickers": r.walletCapabilityUnsupported(ctx, "card-stickers", 60),
	} {
		if !tgerr.Is(err, "WALLET_UNAVAILABLE") {
			t.Fatalf("%s err = %v, want WALLET_UNAVAILABLE", name, err)
		}
	}
}

func TestFlattenRichQuoteKeepsLayer232QuoteText(t *testing.T) {
	got := flattenRichQuote([]tg.RichTextClass{
		&tg.TextBold{Text: &tg.TextPlain{Text: "Hello, "}},
		&tg.TextConcat{Texts: []tg.RichTextClass{&tg.TextPlain{Text: "rich "}, &tg.TextCustomEmoji{Alt: "🙂"}}},
	})
	if got != "Hello, rich 🙂" {
		t.Fatalf("flattened quote = %q", got)
	}
}
