package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// A winner must receive the issue number they won. The old implementation issued numbers by "who
// clicked upgrade first" (issued+1), so the person who paid a high price for #1 became #16 after
// upgrading, while the winner of #109 got #1. This pins three adjacent invariants at once: the winning
// number is reserved (both normal purchases and admin grants must skip it), a normal upgrade against a
// supply with only a reserved number left gets a clean sold out, and the winner ultimately receives
// their own number written into the service message snapshot.
func TestStarGiftAuctionWinnerKeepsWonCollectibleNumberPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1781"+suffix+"71", "AuctionNumSender", "")
	winner := createTestUser(t, ctx, users, "+1781"+suffix+"72", "AuctionNumWinner", "")
	buyer := createTestUser(t, ctx, users, "+1781"+suffix+"73", "AuctionNumBuyer", "")
	latecomer := createTestUser(t, ctx, users, "+1781"+suffix+"74", "AuctionNumLate", "")
	grantee := createTestUser(t, ctx, users, "+1781"+suffix+"75", "AuctionNumGrantee", "")
	winnerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: winner.ID}
	buyerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID}
	latecomerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: latecomer.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Auction Number " + suffix, Stars: 50, ConvertStars: 25, Enabled: true,
		Document:  collectibleTestDocument(baseDocumentID, "auction-num-gift.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "auction-num-gift"),
		Animation: collectibleTestAnimation("auction-num-gift.tgs"),
		Actor:     "integration", CommandID: "auction-num-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create auction catalog gift: %v", err)
	}
	slugPrefix := "auction-num-" + suffix
	revision, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 3, SlugPrefix: slugPrefix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Model", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
			Document:  collectibleTestDocumentPtr(baseDocumentID+1, "auction-num-model.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+1, "auction-num-model"),
			Animation: collectibleTestAnimationPtr("auction-num-model.tgs"),
		}, {
			Kind: domain.StarGiftCollectibleModel, Name: "Model Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
			Document:  collectibleTestDocumentPtr(baseDocumentID+3, "auction-num-model-two.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+3, "auction-num-model-two"),
			Animation: collectibleTestAnimationPtr("auction-num-model-two.tgs"),
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectiblePattern, Name: "Pattern", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
			Document:  collectibleTestPatternDocumentPtr(baseDocumentID+2, "auction-num-pattern.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+2, "auction-num-pattern"),
			Animation: collectibleTestAnimationPtr("auction-num-pattern.tgs"),
		}, {
			Kind: domain.StarGiftCollectiblePattern, Name: "Pattern Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
			Document:  collectibleTestPatternDocumentPtr(baseDocumentID+4, "auction-num-pattern-two.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+4, "auction-num-pattern-two"),
			Animation: collectibleTestAnimationPtr("auction-num-pattern-two.tgs"),
		}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Backdrop", BackdropID: 1,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
		}, {
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Backdrop Two", BackdropID: 2,
			CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
		}},
		Actor: "integration", CommandID: "auction-num-pool-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish auction collectible pool: %v", err)
	}

	messages := NewMessageStore(pool)
	upgrades := NewStarGiftUpgradeStore(pool, messages)
	stars := NewStarsStore(pool)
	for _, id := range []int64{winner.ID, buyer.ID, latecomer.ID} {
		if _, _, err := stars.EnsureGrant(ctx, id, 1000, now); err != nil {
			t.Fatalf("grant upgrade stars to %d: %v", id, err)
		}
	}

	// Auction settlement stamps the won number onto the gift (peer_star_gifts.gift_num). It is not
	// minted into a collectible yet, so it must visibly "reserve" the number for the other minting paths.
	wonSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: winnerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now, ConvertStars: 25, GiftNum: 1, Message: "auction award",
	})
	if wonSaved.GiftNum != 1 {
		t.Fatalf("awarded saved gift lost its won number: %+v", wonSaved)
	}
	boughtSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: buyerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now + 1, ConvertStars: 25, Message: "regular purchase",
	})
	lateSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: latecomerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now + 2, ConvertStars: 25, Message: "late purchase",
	})

	// The admin grant takes a different minting path and must likewise not take the winning number.
	granted, err := upgrades.GrantUniqueStarGift(ctx, domain.AdminStarGiftGrant{
		SenderID: domain.OfficialSystemUserID, Recipient: domain.Peer{Type: domain.PeerTypeUser, ID: grantee.ID},
		GiftID: entry.Gift.ID, Upgrade: true, CommandKey: "auction-num-grant-" + suffix, Date: now,
		ModelAttributeID: revision.Models[0].ID, PatternAttributeID: revision.Patterns[0].ID,
		BackdropAttributeID: revision.Backdrops[0].ID,
	})
	if err != nil {
		t.Fatalf("admin grant beside reserved auction number: %v", err)
	}
	if granted.Unique.Num != 2 || granted.Unique.Slug != slugPrefix+"-2" {
		t.Fatalf("admin grant took a reserved auction number: num=%d slug=%s", granted.Unique.Num, granted.Unique.Slug)
	}

	bought, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: buyer.ID, Ref: domain.SavedStarGiftRef{Owner: buyerPeer, MsgID: boughtSaved.MsgID},
		ChargeStars: 100, FormID: 9971, CommandKey: "auction-num-buyer-" + suffix, Date: now + 3,
	})
	if err != nil {
		t.Fatalf("regular upgrade beside reserved auction number: %v", err)
	}
	if bought.Unique.Num != 3 || bought.Unique.Slug != slugPrefix+"-3" {
		t.Fatalf("regular upgrade took a reserved auction number: num=%d slug=%s", bought.Unique.Num, bought.Unique.Slug)
	}

	// Only the winner-reserved #1 is left: a normal upgrade must get a clean sold out, neither
	// grabbing the number nor hitting the unique index and returning an internal error.
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: latecomer.ID, Ref: domain.SavedStarGiftRef{Owner: latecomerPeer, MsgID: lateSaved.MsgID},
		ChargeStars: 100, FormID: 9972, CommandKey: "auction-num-late-" + suffix, Date: now + 4,
	}); !errors.Is(err, domain.ErrStarGiftCollectibleSoldOut) {
		t.Fatalf("upgrade against a reserved-only supply error=%v, want sold out", err)
	}
	var lateBalance int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM stars_balances WHERE user_id=$1`, latecomer.ID).Scan(&lateBalance); err != nil {
		t.Fatal(err)
	}
	if lateBalance != 1000 {
		t.Fatalf("sold-out upgrade charged the user: balance=%d, want 1000", lateBalance)
	}

	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: winner.ID, Ref: domain.SavedStarGiftRef{Owner: winnerPeer, MsgID: wonSaved.MsgID},
		ChargeStars: 100, FormID: 9973, CommandKey: "auction-num-winner-" + suffix, Date: now + 5,
	})
	if err != nil {
		t.Fatalf("winner upgrade: %v", err)
	}
	if upgraded.Unique.Num != 1 || upgraded.Unique.Slug != slugPrefix+"-1" {
		t.Fatalf("winner lost the won number: num=%d slug=%s", upgraded.Unique.Num, upgraded.Unique.Slug)
	}
	action := upgraded.Send.RecipientMessage.Media.ServiceAction.StarGiftUnique
	if action == nil || action.Gift.Num != 1 || action.Gift.Slug != slugPrefix+"-1" {
		t.Fatalf("winner upgrade service snapshot = %+v", action)
	}

	var issued, distinctNums, totalNums int
	if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, revision.ID).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(DISTINCT num), COUNT(*) FROM unique_star_gifts WHERE gift_id=$1`,
		entry.Gift.ID).Scan(&distinctNums, &totalNums); err != nil {
		t.Fatal(err)
	}
	if issued != 3 || totalNums != 3 || distinctNums != 3 {
		t.Fatalf("issuance after full mint: issued=%d minted=%d distinct=%d, want 3/3/3", issued, totalNums, distinctNums)
	}
}
