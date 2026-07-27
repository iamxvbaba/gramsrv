package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftResaleClearsSellerProfileStatePostgres pins the full seller-side
// teardown of a resale: a collectible that was worn as an emoji status and
// pinned to the profile must, the moment somebody buys it, stop being worn, stop
// being pinned, leave the seller's saved-gift list, and produce the three
// durable seller-visible updates the clients need to converge on that
// (user_emoji_status for the cleared status, new_message for the sale card,
// edit_message for the retired older card).
//
// This is the "my sold gift is still on my profile" report: everything below is
// server state and server push, so a client still showing the gift after this
// test passes is showing its own cache, not our state.
func TestStarGiftResaleClearsSellerProfileStatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	seller := createTestUser(t, ctx, users, "+1882"+suffix+"01", "ResaleSeller", "")
	buyer := createTestUser(t, ctx, users, "+1882"+suffix+"02", "ResaleBuyer2", "")
	sellerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: seller.ID}

	stars := NewStarsStore(pool)
	for _, u := range []domain.User{seller, buyer} {
		if _, _, err := stars.EnsureGrant(ctx, u.ID, 10000, now); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}

	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "ResaleSync " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(base, "resale-sync.tgs"),
		Blob:     collectibleTestBlob(base, "resale-sync"), Animation: collectibleTestAnimation("resale-sync.tgs"),
		Actor: "integration", CommandID: "resale-sync-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: "rsy-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+1, "model.tgs"), Blob: collectibleTestBlobPtr(base+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+4, "model-two.tgs"), Blob: collectibleTestBlobPtr(base+4, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+3, "pattern.tgs"), Blob: collectibleTestBlobPtr(base+3, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+5, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(base+5, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 78, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "resale-sync-pool-" + suffix,
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
		BuyerUserID: seller.ID, To: sellerPeer, GiftID: entry.Gift.ID, IncludeUpgrade: true,
		CommandKey: "resale-sync-purchase-" + suffix, Date: now,
	})
	bought, err := lifecycle.PurchaseStarGift(ctx, purchase)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: bought.Saved.MsgID},
		RequirePrepaid: true, KeepOriginalDetails: true, CommandKey: "resale-sync-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}

	// Wear it.
	selected, valid := domain.CollectibleEmojiStatus(upgraded.Unique)
	if !valid {
		t.Fatalf("cannot wear: %+v", upgraded.Unique)
	}
	if _, err := users.UpdateEmojiStatus(ctx, seller.ID, domain.UserEmojiStatus{
		DocumentID: selected.DocumentID, Collectible: selected,
	}); err != nil {
		t.Fatalf("wear: %v", err)
	}
	// Pin it to the profile.
	if err := gifts.SetPinned(ctx, sellerPeer, []int64{upgraded.Saved.ID}); err != nil {
		t.Fatalf("pin: %v", err)
	}

	var pinnedOrder int
	if err := pool.QueryRow(ctx, `SELECT pinned_order FROM peer_star_gifts WHERE id=$1`, upgraded.Saved.ID).Scan(&pinnedOrder); err != nil {
		t.Fatalf("read pin: %v", err)
	}
	if pinnedOrder == 0 {
		t.Fatalf("gift was not pinned before the sale")
	}

	listed, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: seller.ID, Ref: domain.SavedStarGiftRef{Owner: sellerPeer, MsgID: upgraded.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, Date: now + 2,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	// The seller's profile-visible state right before the sale.
	beforePage, err := gifts.ListByOwner(ctx, sellerPeer, true, "", 20)
	if err != nil {
		t.Fatalf("list before: %v", err)
	}
	if len(beforePage.Gifts) != 1 {
		t.Fatalf("seller saved gifts before the sale = %d, want 1", len(beforePage.Gifts))
	}

	sellerPtsBefore, err := NewUpdateEventStore(pool).MaxContiguousPts(ctx, seller.ID)
	if err != nil {
		t.Fatalf("pts before: %v", err)
	}

	resold, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: listed.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, FormID: 31001,
		CommandKey: "resale-sync-resale-" + suffix, Date: now + 3,
	})
	if err != nil {
		t.Fatalf("resale: %v", err)
	}
	buyerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID}
	if resold.Unique.Owner != buyerPeer || resold.Saved.Owner != buyerPeer {
		t.Fatalf("resale ownership = unique %+v saved %+v, want %+v", resold.Unique.Owner, resold.Saved.Owner, buyerPeer)
	}

	// The gift is no longer pinned to the seller's profile.
	if err := pool.QueryRow(ctx, `SELECT pinned_order FROM peer_star_gifts WHERE id=$1`, upgraded.Saved.ID).Scan(&pinnedOrder); err != nil {
		t.Fatalf("read pin after: %v", err)
	}
	if pinnedOrder != 0 {
		t.Fatalf("sold gift is still pinned to the seller's profile: pinned_order = %d", pinnedOrder)
	}

	// The seller no longer wears it: the lifecycle trigger cleared the status.
	var docID int64
	var collectibleID *int64
	if err := pool.QueryRow(ctx, `SELECT emoji_status_document_id,emoji_status_collectible_id FROM users WHERE id=$1`,
		seller.ID).Scan(&docID, &collectibleID); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if docID != 0 || collectibleID != nil {
		t.Fatalf("seller still wears the sold collectible: document_id=%d collectible_id=%v", docID, collectibleID)
	}

	// And it has left the seller's saved-gift list entirely.
	afterPage, err := gifts.ListByOwner(ctx, sellerPeer, true, "", 20)
	if err != nil {
		t.Fatalf("list after: %v", err)
	}
	if len(afterPage.Gifts) != 0 {
		t.Fatalf("seller still owns %d saved gifts after the sale", len(afterPage.Gifts))
	}
	buyerPage, err := gifts.ListByOwner(ctx, buyerPeer, true, "", 20)
	if err != nil {
		t.Fatalf("buyer list after: %v", err)
	}
	if len(buyerPage.Gifts) != 1 || buyerPage.Gifts[0].ID != upgraded.Saved.ID || buyerPage.Gifts[0].PinnedOrder != 0 {
		t.Fatalf("buyer saved gifts = %+v, want exactly the unpinned sold gift", buyerPage.Gifts)
	}

	// Every seller-visible consequence is also a durable update the clients can
	// converge on, in contiguous pts order and with no gap.
	sellerPtsAfter, err := NewUpdateEventStore(pool).MaxContiguousPts(ctx, seller.ID)
	if err != nil {
		t.Fatalf("pts after: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT pts,event_type FROM user_update_events
	 WHERE user_id=$1 AND pts>$2 ORDER BY pts`, seller.ID, sellerPtsBefore)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	kinds := make([]string, 0, 3)
	pointers := make([]int, 0, 3)
	for rows.Next() {
		var pts int
		var kind string
		if err := rows.Scan(&pts, &kind); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		pointers = append(pointers, pts)
		kinds = append(kinds, kind)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		string(domain.UpdateEventUserEmojiStatus),
		string(domain.UpdateEventNewMessage),
		string(domain.UpdateEventEditMessage),
	}
	if len(kinds) != len(want) {
		t.Fatalf("seller update events = %v (pts %v), want %v", kinds, pointers, want)
	}
	for i, kind := range want {
		if kinds[i] != kind {
			t.Fatalf("seller update events = %v, want %v", kinds, want)
		}
		if pointers[i] != sellerPtsBefore+1+i {
			t.Fatalf("seller update pts = %v, want contiguous from %d", pointers, sellerPtsBefore+1)
		}
	}
	if sellerPtsAfter != sellerPtsBefore+len(want) {
		t.Fatalf("seller contiguous pts = %d, want %d", sellerPtsAfter, sellerPtsBefore+len(want))
	}
	// Each of them is also queued for online dispatch, and none of them is
	// suppressed by the buyer's origin session.
	var dispatched int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM dispatch_outbox
	 WHERE target_user_id=$1 AND pts>$2 AND exclude_auth_key_id=0 AND exclude_session_id=0`,
		seller.ID, sellerPtsBefore).Scan(&dispatched); err != nil {
		t.Fatalf("dispatch rows: %v", err)
	}
	if dispatched != len(want) {
		t.Fatalf("seller dispatch rows = %d, want %d", dispatched, len(want))
	}
	// The cleared status must serialise as an explicit empty status, not as a
	// dropped update: a client that never sees it keeps rendering the sold gift.
	events, err := NewUpdateEventStore(pool).ListAfter(ctx, seller.ID, sellerPtsBefore, 10)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type != domain.UpdateEventUserEmojiStatus {
			continue
		}
		found = true
		if event.UserID != seller.ID || !event.EmojiStatus.Empty() || !event.EmojiStatus.Valid() {
			t.Fatalf("cleared emoji status event = %+v, want a valid empty status for %d", event, seller.ID)
		}
	}
	if !found {
		t.Fatalf("no user_emoji_status event in %+v", events)
	}
}
