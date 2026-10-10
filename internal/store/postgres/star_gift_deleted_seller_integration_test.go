package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// A deleted seller must disappear from the marketplace and must not be able to
// take payment. The listing rows stay for history -- exactly like the freeze path
// already handled -- but they stop being visible and buyable.
func TestDeletedSellerIsPulledOffResaleMarketPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	seller := createTestUser(t, ctx, users, "+1885"+suffix+"01", "GoneSeller", "")
	buyer := createTestUser(t, ctx, users, "+1885"+suffix+"02", "Buyer", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM account_deletion_requests WHERE user_id = ANY($1)`, []int64{seller.ID, buyer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM star_gift_listings WHERE seller_peer_id = ANY($1)`, []int64{seller.ID, buyer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM stars_transactions WHERE user_id = ANY($1)`, []int64{seller.ID, buyer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM stars_balances WHERE user_id = ANY($1)`, []int64{seller.ID, buyer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM peer_star_gifts WHERE owner_peer_id = ANY($1)`, []int64{seller.ID, buyer.ID})
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1)`, []int64{seller.ID, buyer.ID})
	})

	stars := NewStarsStore(pool)
	for _, u := range []domain.User{seller, buyer} {
		if _, _, err := stars.EnsureGrant(ctx, u.ID, 20000, now); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7fffffffffff0000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Deleted Seller " + suffix, Stars: 600, ConvertStars: 200, Enabled: true,
		Document: collectibleTestDocument(base, "deleted-seller.tgs"),
		Blob:     collectibleTestBlob(base, "deleted-seller"), Animation: collectibleTestAnimation("deleted-seller.tgs"),
		Actor: "integration", CommandID: "deleted-seller-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: "ds-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+1, "ds-model.tgs"), Blob: collectibleTestBlobPtr(base+1, "ds-model"), Animation: collectibleTestAnimationPtr("ds-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+4, "ds-model-two.tgs"), Blob: collectibleTestBlobPtr(base+4, "ds-model-two"), Animation: collectibleTestAnimationPtr("ds-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+2, "ds-pattern.tgs"), Blob: collectibleTestBlobPtr(base+2, "ds-pattern"), Animation: collectibleTestAnimationPtr("ds-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+3, "ds-pattern-two.tgs"), Blob: collectibleTestBlobPtr(base+3, "ds-pattern-two"), Animation: collectibleTestAnimationPtr("ds-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 78, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "deleted-seller-pool-" + suffix,
	}); err != nil {
		t.Fatalf("pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))

	purchase := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: seller.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: seller.ID},
		GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "ds-purchase-" + suffix, Date: now,
	})
	bought, err := lifecycle.PurchaseStarGift(ctx, purchase)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: seller.ID}, MsgID: bought.Saved.MsgID},
		RequirePrepaid: true, KeepOriginalDetails: true, CommandKey: "ds-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	sellerRef := domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: seller.ID}, MsgID: upgraded.Saved.MsgID}

	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: seller.ID, Ref: sellerRef,
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 1000}, Date: now + 2,
	}); err != nil {
		t.Fatalf("seller listing: %v", err)
	}

	visible := func() bool {
		t.Helper()
		page, err := lifecycle.ListResaleStarGifts(ctx, domain.StarGiftResaleFilter{GiftID: entry.Gift.ID, Limit: 50})
		if err != nil {
			t.Fatalf("list resale: %v", err)
		}
		for _, item := range page.Gifts {
			if item.ID == upgraded.Unique.ID {
				return true
			}
		}
		return false
	}
	if !visible() {
		t.Fatal("listed gift must be visible on the market while the seller is alive")
	}

	if _, err := NewAccountLifecycleStore(pool).ExecuteAccountDeletion(
		ctx, seller.ID, domain.AccountDeletionManual, "manual", time.Now().UTC()); err != nil {
		t.Fatalf("delete seller: %v", err)
	}

	if visible() {
		t.Fatal("a deleted seller is still listed on the marketplace")
	}

	// The listing row is still on disk for history, but it must not be buyable.
	form := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "ds-resale-" + suffix, Date: now + 3,
	})
	resale := domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: upgraded.Unique.Slug,
		To:     domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 1000},
		FormID: form.FormID, CommandKey: "ds-resale-buy-" + suffix, Date: now + 3,
	}
	if _, err := lifecycle.PurchaseResaleStarGift(ctx, resale); err == nil {
		t.Fatal("a deleted seller's listing is still buyable")
	}
}
