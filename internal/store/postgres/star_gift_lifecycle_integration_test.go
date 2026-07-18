package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestStarGiftLifecycleAggregatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	buyer := createTestUser(t, ctx, users, "+1881"+suffix+"01", "GiftBuyer", "")
	owner := createTestUser(t, ctx, users, "+1881"+suffix+"02", "GiftOwner", "")
	offerBuyer := createTestUser(t, ctx, users, "+1881"+suffix+"03", "OfferBuyer", "")
	resaleBuyer := createTestUser(t, ctx, users, "+1881"+suffix+"04", "ResaleBuyer", "")
	loser := createTestUser(t, ctx, users, "+1881"+suffix+"05", "AuctionLoser", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	stars := NewStarsStore(pool)
	for _, user := range []domain.User{buyer, owner, offerBuyer, resaleBuyer, loser} {
		if _, _, err := stars.EnsureGrant(ctx, user.ID, 10000, now); err != nil {
			t.Fatalf("grant stars to %d: %v", user.ID, err)
		}
	}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Lifecycle " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "lifecycle.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "lifecycle"), Animation: collectibleTestAnimation("lifecycle.tgs"),
		Actor: "integration", CommandID: "lifecycle-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create lifecycle catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: "life-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Crafted", RarityKind: domain.StarGiftRarityLegendary, Crafted: true,
				Document: collectibleTestDocumentPtr(baseDocumentID+2, "crafted.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "crafted"), Animation: collectibleTestAnimationPtr("crafted.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+3, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000}},
		Actor: "integration", CommandID: "lifecycle-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish lifecycle pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))

	purchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: buyer.ID, To: ownerPeer,
		GiftID: entry.Gift.ID, CommandKey: "purchase-" + suffix, Date: now, Message: "hello"})
	purchased, err := lifecycle.PurchaseStarGift(ctx, purchaseReq)
	if err != nil {
		t.Fatalf("purchase gift: %v", err)
	}
	if purchased.Saved.ID <= 0 || purchased.Saved.MsgID <= 0 || purchased.Saved.PrepaidUpgradeHash == "" || purchased.Balance.Balance != 9950 {
		t.Fatalf("purchase result = %+v", purchased)
	}
	ordinaryAction := purchased.Send.RecipientMessage.Media.ServiceAction.StarGift
	if ordinaryAction == nil || !ordinaryAction.CanUpgrade || ordinaryAction.PrepaidUpgrade ||
		ordinaryAction.UpgradePriceStars != 100 || ordinaryAction.UpgradeStars != 0 {
		t.Fatalf("ordinary purchase action mixed paid price with prepaid amount: %+v", ordinaryAction)
	}
	replayedPurchase, err := lifecycle.PurchaseStarGift(ctx, purchaseReq)
	if err != nil || !replayedPurchase.Duplicate || replayedPurchase.Saved.ID != purchased.Saved.ID || replayedPurchase.Balance.Balance != 9950 ||
		replayedPurchase.Send.SenderMessage.ID != purchased.Send.SenderMessage.ID ||
		replayedPurchase.Send.RecipientMessage.ID != purchased.Send.RecipientMessage.ID {
		t.Fatalf("purchase replay = %+v err %v", replayedPurchase, err)
	}

	target, price, err := lifecycle.PrepaidUpgradeTarget(ctx, ownerPeer, purchased.Saved.PrepaidUpgradeHash)
	if err != nil || target.ID != purchased.Saved.ID || price != 100 {
		t.Fatalf("prepaid target = %+v price %d err %v", target, price, err)
	}
	prepaid, err := lifecycle.PrepayStarGiftUpgrade(ctx, domain.StarGiftPrepaidUpgradeRequest{
		PayerUserID: buyer.ID, Owner: ownerPeer, Hash: purchased.Saved.PrepaidUpgradeHash,
		ChargeStars: 100, FormID: 11002, CommandKey: "prepay-" + suffix, Date: now + 1,
	})
	if err != nil || prepaid.Saved.PrepaidUpgradeStars != 100 || prepaid.Saved.PrepaidUpgradeHash != "" || prepaid.Balance.Balance != 9850 {
		t.Fatalf("prepay upgrade = %+v err %v", prepaid, err)
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: purchased.Saved.MsgID},
		RequirePrepaid: true, KeepOriginalDetails: true, CommandKey: "upgrade-" + suffix, Date: now + 2,
	})
	if err != nil {
		t.Fatalf("upgrade prepaid gift: %v", err)
	}
	if upgraded.Saved.TransferStars != 25 || upgraded.Saved.DropOriginalDetailsStars != 25 ||
		upgraded.Unique.CraftChancePermille != 500 || !upgraded.Unique.KeepOriginalDetails {
		t.Fatalf("issued lifecycle snapshot = saved %+v unique %+v", upgraded.Saved, upgraded.Unique)
	}
	upgradeAction := upgraded.Send.RecipientMessage.Media.ServiceAction.StarGiftUnique
	ownerSourceEdit := upgradedSourceEditForUser(upgraded, owner.ID)
	if upgradeAction == nil || upgradeAction.SavedID != int64(purchased.Saved.MsgID) ||
		ownerSourceEdit.Message.Media == nil || ownerSourceEdit.Message.Media.ServiceAction == nil ||
		ownerSourceEdit.Message.Media.ServiceAction.StarGift == nil ||
		ownerSourceEdit.Message.Media.ServiceAction.StarGift.UpgradeMsgID != upgraded.Saved.UpgradeMsgID ||
		ownerSourceEdit.Message.Media.ServiceAction.StarGift.CanUpgrade {
		t.Fatalf("upgrade message linkage = action %+v source edit %+v", upgradeAction, ownerSourceEdit)
	}
	dropped, err := lifecycle.DropStarGiftOriginalDetails(ctx, domain.StarGiftDropOriginalDetailsRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: purchased.Saved.MsgID},
		ChargeStars: 25, FormID: 11003, CommandKey: "drop-" + suffix, Date: now + 3,
	})
	if err != nil || dropped.Unique.KeepOriginalDetails || dropped.Saved.DropOriginalDetailsStars != 0 || dropped.Balance.Balance != 9975 {
		t.Fatalf("drop original details = %+v err %v", dropped, err)
	}

	// Expiry is driven by the background sweep, refunds exactly once and emits a
	// durable declined/expired service message even when no user opens the offer.
	expiring, err := lifecycle.SendStarGiftOffer(ctx, domain.StarGiftOfferRequest{BuyerUserID: offerBuyer.ID,
		Owner: ownerPeer, Slug: upgraded.Unique.Slug, Price: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300},
		Duration: 120, RandomID: 22001, Date: now + 10,
	})
	if err != nil || expiring.Balance.Balance != 9700 {
		t.Fatalf("send expiring offer = %+v err %v", expiring, err)
	}
	if err := lifecycle.SweepStarGiftLifecycle(ctx, now+131, 1000); err != nil {
		t.Fatalf("sweep expired offer: %v", err)
	}
	var expiredStatus string
	var resolutionNotified bool
	if err := pool.QueryRow(ctx, `SELECT status,resolution_notified FROM star_gift_offers WHERE id=$1`, expiring.Offer.ID).
		Scan(&expiredStatus, &resolutionNotified); err != nil || expiredStatus != "expired" || !resolutionNotified {
		t.Fatalf("expired offer state = %q notified %v err %v", expiredStatus, resolutionNotified, err)
	}
	if balance, err := stars.GetBalance(ctx, offerBuyer.ID); err != nil || balance.Balance != 10000 {
		t.Fatalf("expired offer refund balance = %+v err %v", balance, err)
	}

	// TON offers use the same durable offer state machine, but only mutate the
	// internal telesrv TON ledger. Idempotent replay must report that ledger's
	// balance instead of accidentally projecting the buyer's Stars balance.
	tonOfferReq := domain.StarGiftOfferRequest{BuyerUserID: offerBuyer.ID,
		Owner: ownerPeer, Slug: upgraded.Unique.Slug, Price: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: 300},
		Duration: 120, RandomID: 22003, Date: now + 132}
	tonOffer, err := lifecycle.SendStarGiftOffer(ctx, tonOfferReq)
	if err != nil || tonOffer.Balance.Balance != 999700 {
		t.Fatalf("send TON offer = %+v err %v", tonOffer, err)
	}
	tonOfferReplay, err := lifecycle.SendStarGiftOffer(ctx, tonOfferReq)
	if err != nil || !tonOfferReplay.Duplicate || tonOfferReplay.Balance.Balance != 999700 {
		t.Fatalf("replay TON offer = %+v err %v", tonOfferReplay, err)
	}
	if _, err := lifecycle.ResolveStarGiftOffer(ctx, domain.StarGiftResolveOfferRequest{
		OwnerUserID: owner.ID, OfferMsgID: tonOffer.Offer.OfferMsgID, Decline: true, Date: now + 133,
	}); err != nil {
		t.Fatalf("decline TON offer: %v", err)
	}
	if balance, err := lifecycle.TonBalance(ctx, offerBuyer.ID); err != nil || balance != 1_000_000 {
		t.Fatalf("declined TON offer refund balance = %d err %v", balance, err)
	}

	acceptedOffer, err := lifecycle.SendStarGiftOffer(ctx, domain.StarGiftOfferRequest{BuyerUserID: offerBuyer.ID,
		Owner: ownerPeer, Slug: upgraded.Unique.Slug, Price: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 300},
		Duration: 120, RandomID: 22002, Date: now + 140,
	})
	if err != nil {
		t.Fatalf("send accepted offer: %v", err)
	}
	accepted, err := lifecycle.ResolveStarGiftOffer(ctx, domain.StarGiftResolveOfferRequest{
		OwnerUserID: owner.ID, OfferMsgID: acceptedOffer.Offer.OfferMsgID, Date: now + 141,
	})
	if err != nil || accepted.Offer.Status != "accepted" || accepted.Unique.Owner.ID != offerBuyer.ID || accepted.Saved.MsgID <= 0 {
		t.Fatalf("accept offer = %+v err %v", accepted, err)
	}
	if balance, err := stars.GetBalance(ctx, owner.ID); err != nil || balance.Balance != 10245 {
		t.Fatalf("offer seller balance = %+v err %v", balance, err)
	}
	var offerCommission int64
	if err := pool.QueryRow(ctx, `SELECT commission_amount FROM star_gift_sales WHERE command_key=$1`,
		fmt.Sprintf("offer:%d", acceptedOffer.Offer.ID)).Scan(&offerCommission); err != nil || offerCommission != 30 {
		t.Fatalf("accepted Stars offer commission = %d err %v", offerCommission, err)
	}

	listed, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: offerBuyer.ID,
		Ref:    domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: offerBuyer.ID}, MsgID: accepted.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: 1000}, Date: now + 142,
	})
	if err != nil || listed.ResellAmount == nil || listed.ResellAmount.Currency != domain.StarGiftCurrencyTON {
		t.Fatalf("TON listing = %+v err %v", listed, err)
	}
	tonBefore, err := lifecycle.TonBalance(ctx, resaleBuyer.ID)
	if err != nil || tonBefore != 1_000_000 {
		t.Fatalf("resale buyer TON grant = %d err %v", tonBefore, err)
	}
	resold, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: resaleBuyer.ID, Slug: listed.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: resaleBuyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: 1000}, FormID: 11004,
		CommandKey: "resale-" + suffix, Date: now + 143,
	})
	if err != nil || resold.Unique.Owner.ID != resaleBuyer.ID || resold.Balance.Balance != 999000 || resold.Saved.TransferStars != 25 {
		t.Fatalf("TON resale = %+v err %v", resold, err)
	}
	if sellerTON, err := lifecycle.TonBalance(ctx, offerBuyer.ID); err != nil || sellerTON != 1_000_900 {
		t.Fatalf("TON seller local balance = %d err %v", sellerTON, err)
	}
	var resaleCommission int64
	if err := pool.QueryRow(ctx, `SELECT commission_amount FROM star_gift_sales WHERE command_key=$1`, "resale-"+suffix).
		Scan(&resaleCommission); err != nil || resaleCommission != 100 {
		t.Fatalf("TON resale commission = %d err %v", resaleCommission, err)
	}
	tonPage, err := lifecycle.TonTransactions(ctx, resaleBuyer.ID, "", 20)
	if err != nil || tonPage.Balance != 999000 || len(tonPage.Transactions) < 2 {
		t.Fatalf("TON ledger page = %+v err %v", tonPage, err)
	}

	transferred, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{ActorUserID: resaleBuyer.ID,
		Ref: domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: resaleBuyer.ID}, MsgID: resold.Saved.MsgID},
		To:  ownerPeer, ChargeStars: 25, FormID: 11005, CommandKey: "transfer-back-" + suffix, Date: now + 144,
	})
	if err != nil || transferred.Unique.Owner != ownerPeer || transferred.Saved.TransferStars != 25 || transferred.Balance.Balance != 9975 {
		t.Fatalf("paid transfer = %+v err %v", transferred, err)
	}

	// A second prepaid collectible makes craft chance exactly 1000‰. Success
	// preserves the first aggregate as crafted and burns the other input. The
	// fresh payment intent must create another gift even though buyer, owner and
	// catalog gift are identical to the first purchase.
	secondPurchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: buyer.ID, To: ownerPeer,
		GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "purchase-second-" + suffix, Date: now + 145})
	secondPurchase, err := lifecycle.PurchaseStarGift(ctx, secondPurchaseReq)
	if err != nil {
		t.Fatalf("purchase second prepaid gift: %v", err)
	}
	prepaidAction := secondPurchase.Send.RecipientMessage.Media.ServiceAction.StarGift
	if prepaidAction == nil || !prepaidAction.PrepaidUpgrade || prepaidAction.UpgradePriceStars != 100 || prepaidAction.UpgradeStars != 100 {
		t.Fatalf("prepaid purchase action lost price/entitlement split: %+v", prepaidAction)
	}
	secondUpgrade, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{UserID: owner.ID,
		Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: secondPurchase.Saved.MsgID}, RequirePrepaid: true,
		CommandKey: "upgrade-second-" + suffix, Date: now + 146,
	})
	if err != nil {
		t.Fatalf("upgrade second prepaid gift: %v", err)
	}
	listedForCraft, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: owner.ID,
		Ref:    domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: transferred.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 125}, Date: now + 146,
	})
	if err != nil || listedForCraft.ResellAmount == nil || listedForCraft.ResellAmount.Amount != 125 {
		t.Fatalf("list craft input = %+v err %v", listedForCraft, err)
	}
	loserBalanceBeforeOffer, err := stars.GetBalance(ctx, loser.ID)
	if err != nil {
		t.Fatalf("craft offer buyer balance: %v", err)
	}
	pendingCraftOffer, err := lifecycle.SendStarGiftOffer(ctx, domain.StarGiftOfferRequest{BuyerUserID: loser.ID,
		Owner: ownerPeer, Slug: transferred.Unique.Slug,
		Price:    domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 125},
		Duration: 120, RandomID: 22003, Date: now + 146,
	})
	if err != nil || pendingCraftOffer.Offer.Status != "pending" {
		t.Fatalf("pending craft offer = %+v err %v", pendingCraftOffer, err)
	}
	resolvedCraftIDs, err := gifts.ResolveSavedIDs(ctx, ownerPeer, []domain.SavedStarGiftRef{
		{Owner: ownerPeer, MsgID: transferred.Saved.MsgID},
		{Owner: ownerPeer, Slug: secondUpgrade.Unique.Slug},
	})
	if err != nil || len(resolvedCraftIDs) != 2 || resolvedCraftIDs[0] != transferred.Saved.ID || resolvedCraftIDs[1] != secondUpgrade.Saved.ID {
		t.Fatalf("resolve mixed craft refs = %v err %v", resolvedCraftIDs, err)
	}
	if _, err := gifts.ResolveSavedIDs(ctx, ownerPeer, []domain.SavedStarGiftRef{
		{Owner: ownerPeer, MsgID: secondUpgrade.Saved.UpgradeMsgID},
		{Owner: ownerPeer, Slug: secondUpgrade.Unique.Slug},
	}); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("upgrade message id lookup err = %v, want ErrStarGiftNotFound", err)
	}
	if saved, found, err := gifts.GetByRef(ctx, domain.SavedStarGiftRef{
		Owner: ownerPeer, MsgID: secondUpgrade.Saved.UpgradeMsgID,
	}); err != nil || found {
		t.Fatalf("upgrade message id resolved a gift: saved=%+v found=%v err=%v", saved, found, err)
	}
	if _, err := gifts.ResolveSavedIDs(ctx, ownerPeer, []domain.SavedStarGiftRef{
		{Owner: ownerPeer, MsgID: secondUpgrade.Saved.MsgID},
		{Owner: ownerPeer, Slug: secondUpgrade.Unique.Slug},
	}); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("duplicate official identities err = %v", err)
	}
	crafted, err := lifecycle.CraftStarGift(ctx, domain.StarGiftCraftRequest{UserID: owner.ID,
		Refs: []domain.SavedStarGiftRef{
			{Owner: ownerPeer, MsgID: transferred.Saved.MsgID},
			// TDesktop sends collectibles without a manage id as the official
			// inputSavedStarGiftSlug alias.
			{Owner: ownerPeer, Slug: secondUpgrade.Unique.Slug},
		}, CommandKey: "craft-" + suffix, Date: now + 147,
	})
	if err != nil || !crafted.Success || crafted.Chance != 1000 || crafted.Gift == nil || !crafted.Gift.Crafted || crafted.Send.RecipientMessage.ID <= 0 {
		t.Fatalf("craft result = %+v err %v", crafted, err)
	}
	craftedInputEdit := craftedSourceEditForUserAndGift(crafted, owner.ID, transferred.Unique.ID)
	craftedInputAction := starGiftUniqueActionFromEdit(craftedInputEdit)
	burnedInputEdit := craftedSourceEditForUserAndGift(crafted, owner.ID, secondUpgrade.Unique.ID)
	burnedInputAction := starGiftUniqueActionFromEdit(burnedInputEdit)
	if craftedInputAction == nil || !craftedInputAction.Gift.Crafted || craftedInputAction.Gift.Burned ||
		craftedInputAction.Gift.CraftChancePermille != 0 || !craftedInputAction.Saved || craftedInputAction.CanCraftAt != 0 {
		t.Fatalf("crafted input message projection = %+v", craftedInputAction)
	}
	if burnedInputAction == nil || !burnedInputAction.Gift.Burned || burnedInputAction.Gift.CraftChancePermille != 0 ||
		burnedInputAction.Saved || burnedInputAction.CanCraftAt != 0 {
		t.Fatalf("burned input message projection = %+v", burnedInputAction)
	}
	craftReq := domain.StarGiftCraftRequest{UserID: owner.ID,
		Refs: []domain.SavedStarGiftRef{
			{Owner: ownerPeer, MsgID: transferred.Saved.MsgID},
			{Owner: ownerPeer, Slug: secondUpgrade.Unique.Slug},
		}, CommandKey: "craft-" + suffix, Date: now + 147,
	}
	craftedReplay, err := lifecycle.CraftStarGift(ctx, craftReq)
	if err != nil || !craftedReplay.Duplicate || !craftedReplay.Success || craftedReplay.Gift == nil ||
		craftedReplay.Send.RecipientMessage.ID != crafted.Send.RecipientMessage.ID ||
		craftedSourceEditForUserAndGift(craftedReplay, owner.ID, transferred.Unique.ID).Event.Pts != craftedInputEdit.Event.Pts ||
		craftedSourceEditForUserAndGift(craftedReplay, owner.ID, secondUpgrade.Unique.ID).Event.Pts != burnedInputEdit.Event.Pts {
		t.Fatalf("craft success replay = %+v err %v", craftedReplay, err)
	}
	var craftListings, resaleAvailability int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_listings WHERE unique_gift_id=ANY($1::bigint[])`,
		[]int64{transferred.Unique.ID, secondUpgrade.Unique.ID}).Scan(&craftListings); err != nil || craftListings != 0 {
		t.Fatalf("craft input listings = %d err %v", craftListings, err)
	}
	if err := pool.QueryRow(ctx, `SELECT availability_resale FROM star_gift_catalog WHERE gift_id=$1`, entry.Gift.ID).Scan(&resaleAvailability); err != nil || resaleAvailability != 0 {
		t.Fatalf("craft resale projection = %d err %v", resaleAvailability, err)
	}
	var craftOfferStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM star_gift_offers WHERE id=$1`, pendingCraftOffer.Offer.ID).Scan(&craftOfferStatus); err != nil || craftOfferStatus != "cancelled" {
		t.Fatalf("craft offer status = %q err %v", craftOfferStatus, err)
	}
	loserBalanceAfterCraft, err := stars.GetBalance(ctx, loser.ID)
	if err != nil || loserBalanceAfterCraft.Balance != loserBalanceBeforeOffer.Balance {
		t.Fatalf("craft offer refund balance = %+v err %v, want %d", loserBalanceAfterCraft, err, loserBalanceBeforeOffer.Balance)
	}
	var secondStatus string
	if err := pool.QueryRow(ctx, `SELECT lifecycle_status FROM peer_star_gifts WHERE id=$1`, secondUpgrade.Saved.ID).Scan(&secondStatus); err != nil || secondStatus != "burned" {
		t.Fatalf("second craft input status = %q err %v", secondStatus, err)
	}

	// A failed draw is just as terminal as success: the input aggregate and both
	// users' message snapshots are burned in the outcome transaction. An exact
	// retry replays the receipt, while a fresh command cannot consume it again.
	thirdPurchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: buyer.ID, To: ownerPeer,
		GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "purchase-third-" + suffix, Date: now + 148})
	thirdPurchase, err := lifecycle.PurchaseStarGift(ctx, thirdPurchaseReq)
	if err != nil {
		t.Fatalf("purchase third prepaid gift: %v", err)
	}
	thirdUpgrade, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{UserID: owner.ID,
		Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: thirdPurchase.Saved.MsgID}, RequirePrepaid: true,
		CommandKey: "upgrade-third-" + suffix, Date: now + 149,
	})
	if err != nil {
		t.Fatalf("upgrade third prepaid gift: %v", err)
	}
	failingLifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000,
		WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{StarsProceedsPermille: 900, TONProceedsPermille: 900}),
		WithStarGiftCraftDraw(func(upper int) (int, error) { return upper - 1, nil }))
	failureReq := domain.StarGiftCraftRequest{UserID: owner.ID,
		Refs:       []domain.SavedStarGiftRef{{Owner: ownerPeer, MsgID: thirdUpgrade.Saved.MsgID}},
		CommandKey: "craft-fail-" + suffix, Date: now + 150,
	}
	failedCraft, err := failingLifecycle.CraftStarGift(ctx, failureReq)
	if err != nil || failedCraft.Success || failedCraft.Chance != 500 || failedCraft.Gift != nil {
		t.Fatalf("craft failure result = %+v err %v", failedCraft, err)
	}
	failedInputEdit := craftedSourceEditForUserAndGift(failedCraft, owner.ID, thirdUpgrade.Unique.ID)
	failedInputAction := starGiftUniqueActionFromEdit(failedInputEdit)
	if failedInputAction == nil || !failedInputAction.Gift.Burned || failedInputAction.Gift.CraftChancePermille != 0 ||
		failedInputAction.Gift.OfferMinStars != 0 || failedInputAction.Saved || failedInputAction.CanCraftAt != 0 {
		t.Fatalf("failed craft message projection = %+v", failedInputAction)
	}
	var failedLifecycle string
	var failedUnsaved bool
	var failedTransferStars int64
	var failedCanExportAt, failedCanTransferAt, failedCanResellAt, failedCanCraftAt int
	var failedDropStars int64
	if err := pool.QueryRow(ctx, `SELECT lifecycle_status,unsaved,transfer_stars,can_export_at,can_transfer_at,
can_resell_at,drop_original_details_stars,can_craft_at FROM peer_star_gifts WHERE id=$1`, thirdUpgrade.Saved.ID).
		Scan(&failedLifecycle, &failedUnsaved, &failedTransferStars, &failedCanExportAt, &failedCanTransferAt,
			&failedCanResellAt, &failedDropStars, &failedCanCraftAt); err != nil || failedLifecycle != "burned" || !failedUnsaved ||
		failedTransferStars != 0 || failedCanExportAt != 0 || failedCanTransferAt != 0 || failedCanResellAt != 0 ||
		failedDropStars != 0 || failedCanCraftAt != 0 {
		t.Fatalf("failed craft saved aggregate = status %q unsaved %v transfer %d export %d transfer_at %d resale %d drop %d craft %d err %v",
			failedLifecycle, failedUnsaved, failedTransferStars, failedCanExportAt, failedCanTransferAt,
			failedCanResellAt, failedDropStars, failedCanCraftAt, err)
	}
	var failedBurned bool
	var failedChance, failedOfferMin int
	if err := pool.QueryRow(ctx, `SELECT burned,craft_chance_permille,offer_min_stars FROM unique_star_gifts WHERE id=$1`, thirdUpgrade.Unique.ID).
		Scan(&failedBurned, &failedChance, &failedOfferMin); err != nil || !failedBurned || failedChance != 0 || failedOfferMin != 0 {
		t.Fatalf("failed craft unique aggregate = burned %v chance %d offer %d err %v", failedBurned, failedChance, failedOfferMin, err)
	}
	failedReplay, err := failingLifecycle.CraftStarGift(ctx, failureReq)
	if err != nil || !failedReplay.Duplicate || failedReplay.Success || failedReplay.Chance != failedCraft.Chance ||
		craftedSourceEditForUserAndGift(failedReplay, owner.ID, thirdUpgrade.Unique.ID).Event.Pts != failedInputEdit.Event.Pts {
		t.Fatalf("craft failure replay = %+v err %v", failedReplay, err)
	}
	invalidRetry := failureReq
	invalidRetry.CommandKey = "craft-fail-new-command-" + suffix
	if _, err := failingLifecycle.CraftStarGift(ctx, invalidRetry); !errors.Is(err, domain.ErrStarGiftCraftUnavailable) {
		t.Fatalf("fresh command reused burned craft input: %v", err)
	}
	craftCandidates, err := lifecycle.ListCraftStarGifts(ctx, owner.ID, entry.Gift.ID, "", 20)
	if err != nil || craftCandidates.Count != 0 || len(craftCandidates.Gifts) != 0 {
		t.Fatalf("terminal craft inputs remained candidates: %+v err %v", craftCandidates, err)
	}

	withdrawalReq := domain.StarGiftWithdrawalRequest{UserID: owner.ID,
		Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: transferred.Saved.MsgID}, Date: now + 151}
	recorded, err := lifecycle.RecordStarGiftWithdrawal(ctx, withdrawalReq, "local", "withdraw-"+suffix,
		"https://telesrv.invalid/gift-withdrawal/"+suffix, now+748)
	if err != nil || recorded.Status != "pending" {
		t.Fatalf("record local withdrawal = %+v err %v", recorded, err)
	}
	completed, err := lifecycle.CompleteStarGiftWithdrawal(ctx, recorded.ProviderRequestID, now+152)
	if err != nil || completed.Status != "completed" || completed.Gift.OwnerAddress == "" || completed.Gift.GiftAddress == "" {
		t.Fatalf("complete local withdrawal = %+v err %v", completed, err)
	}

	// Auction winner reservation is consumed; the unreachable lower bid is
	// refunded atomically. Award delivery is durable and includes gift_num.
	auctionEntry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Auction " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 1, AvailabilityRemains: 1, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionSlug: "auction-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID+100, "auction.tgs"),
		Blob:        collectibleTestBlob(baseDocumentID+100, "auction"), Animation: collectibleTestAnimation("auction.tgs"),
		Actor: "integration", CommandID: "auction-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create auction catalog: %v", err)
	}
	winnerState, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: resaleBuyer.ID,
		GiftID: auctionEntry.Gift.ID, Peer: ownerPeer, BidAmount: 200, FormID: 12001, Date: now, Message: "winner"})
	if err != nil || winnerState.UserState.BidAmount != 200 {
		t.Fatalf("winner bid state = %+v err %v", winnerState, err)
	}
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: loser.ID,
		GiftID: auctionEntry.Gift.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: loser.ID},
		BidAmount: 150, FormID: 12002, Date: now + 1}); err != nil {
		t.Fatalf("loser bid: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET next_round_at=$2 WHERE gift_id=$1`, auctionEntry.Gift.ID, now+2); err != nil {
		t.Fatalf("make auction round due: %v", err)
	}
	if err := lifecycle.SweepStarGiftLifecycle(ctx, now+2, 1000); err != nil {
		t.Fatalf("settle auction sweep: %v", err)
	}
	acquired, err := lifecycle.StarGiftAuctionAcquired(ctx, resaleBuyer.ID, auctionEntry.Gift.ID)
	if err != nil || len(acquired) != 1 || acquired[0].GiftNum != 1 || acquired[0].BidAmount != 200 {
		t.Fatalf("auction acquired = %+v err %v", acquired, err)
	}
	if loserBalance, err := stars.GetBalance(ctx, loser.ID); err != nil || loserBalance.Balance != 10000 {
		t.Fatalf("auction loser refund = %+v err %v", loserBalance, err)
	}
	var auctionSavedCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM peer_star_gifts WHERE gift_id=$1 AND gift_num=1 AND convert_stars=0`, auctionEntry.Gift.ID).
		Scan(&auctionSavedCount); err != nil || auctionSavedCount != 1 {
		t.Fatalf("auction saved award count = %d err %v", auctionSavedCount, err)
	}

}

func TestStarGiftChannelLifecycleAtomicPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	actor := createTestUser(t, ctx, users, "+1882"+suffix+"01", "ChannelGiftActor", "")
	if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, actor.ID, 10000, now); err != nil {
		t.Fatalf("grant actor stars: %v", err)
	}
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: actor.ID, Title: "Gift Channel " + suffix, Megagroup: true, Date: now,
	})
	if err != nil {
		t.Fatalf("create gift channel: %v", err)
	}
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID}
	createdTarget, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: actor.ID, Title: "Gift Target Channel " + suffix, Megagroup: true, Date: now,
	})
	if err != nil {
		t.Fatalf("create target gift channel: %v", err)
	}
	targetChannelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: createdTarget.Channel.ID}
	gifts := NewStarGiftStore(pool)
	baseDocumentID := (time.Now().UnixNano() & 0x7ffffffffffff000) + 500
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Channel Gift " + suffix, Stars: 50, ConvertStars: 20, Enabled: true, Limited: true,
		AvailabilityTotal: 5, AvailabilityRemains: 5,
		Document: collectibleTestDocument(baseDocumentID, "channel-gift.tgs"), Blob: collectibleTestBlob(baseDocumentID, "channel-gift"),
		Animation: collectibleTestAnimation("channel-gift.tgs"), Actor: "integration", CommandID: "channel-gift-" + suffix,
	})
	if err != nil {
		t.Fatalf("create channel gift catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "channel-life-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectibleModel, Name: "Channel Model", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+1, "channel-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "channel-model"), Animation: collectibleTestAnimationPtr("channel-model.tgs")}},
		Patterns: []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectiblePattern, Name: "Channel Pattern", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+2, "channel-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "channel-pattern"), Animation: collectibleTestAnimationPtr("channel-pattern.tgs")}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{Kind: domain.StarGiftCollectibleBackdrop, Name: "Channel Backdrop", BackdropID: 88,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000}},
		Actor: "integration", CommandID: "channel-gift-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish channel gift pool: %v", err)
	}
	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))
	channelPurchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: actor.ID, To: channelPeer,
		GiftID: entry.Gift.ID, CommandKey: "channel-purchase-" + suffix, Date: now + 1})
	purchased, err := lifecycle.PurchaseStarGift(ctx, channelPurchaseReq)
	if err != nil || purchased.Saved.SavedID <= 0 || purchased.Balance.Balance != 9950 {
		t.Fatalf("atomic channel purchase = %+v err %v", purchased, err)
	}
	var regularLogs int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND event_type='send_message' AND message::text LIKE '%star_gift%'`, created.Channel.ID).Scan(&regularLogs); err != nil || regularLogs != 1 {
		t.Fatalf("channel purchase admin logs = %d err %v", regularLogs, err)
	}
	var channelPrice string
	var channelPrepaidAmount any
	if err := pool.QueryRow(ctx, `SELECT message #>> '{Action,StarGift,upgrade_price_stars}', message #> '{Action,StarGift,upgrade_stars}'
FROM channel_admin_log_events WHERE channel_id=$1 AND event_type='send_message' ORDER BY id DESC LIMIT 1`, created.Channel.ID).
		Scan(&channelPrice, &channelPrepaidAmount); err != nil || channelPrice != "100" || channelPrepaidAmount != nil {
		t.Fatalf("channel ordinary action price=%q prepaid=%v err=%v", channelPrice, channelPrepaidAmount, err)
	}
	if replay, err := lifecycle.PurchaseStarGift(ctx, channelPurchaseReq); err != nil || !replay.Duplicate {
		t.Fatalf("channel purchase replay = %+v err %v", replay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND event_type='send_message' AND message::text LIKE '%star_gift%'`, created.Channel.ID).Scan(&regularLogs); err != nil || regularLogs != 1 {
		t.Fatalf("channel replay duplicated admin log count=%d err %v", regularLogs, err)
	}

	converted, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{ActorUserID: actor.ID,
		Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased.Saved.SavedID}, Date: now + 2})
	if err != nil || !converted.Saved.Converted || converted.OwnerBalance != 20 {
		t.Fatalf("atomic channel conversion = %+v err %v", converted, err)
	}
	var channelBalance, conversionRows, conversionTxns int64
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, created.Channel.ID).Scan(&channelBalance); err != nil || channelBalance != 20 {
		t.Fatalf("channel conversion balance = %d err %v", channelBalance, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_conversions WHERE saved_gift_id=$1`, purchased.Saved.ID).Scan(&conversionRows); err != nil || conversionRows != 1 {
		t.Fatalf("channel conversion command rows = %d err %v", conversionRows, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_stars_transactions WHERE channel_id=$1 AND gift_id=$2`, created.Channel.ID, entry.Gift.ID).Scan(&conversionTxns); err != nil || conversionTxns != 1 {
		t.Fatalf("channel conversion transactions = %d err %v", conversionTxns, err)
	}
	if balance, err := lifecycle.ChannelStarsBalance(ctx, created.Channel.ID); err != nil || balance != 20 {
		t.Fatalf("channel stars balance projection = %d err %v", balance, err)
	}
	starsPage, err := lifecycle.ChannelStarsTransactions(ctx, created.Channel.ID, "", 20)
	if err != nil || starsPage.Balance != 20 || len(starsPage.Transactions) != 1 ||
		starsPage.Transactions[0].Amount != 20 || starsPage.Transactions[0].Reason != domain.StarsReasonGift {
		t.Fatalf("channel stars transaction projection = %+v err %v", starsPage, err)
	}
	if _, err := lifecycle.ConvertStarGift(ctx, domain.StarGiftConvertRequest{ActorUserID: actor.ID,
		Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: purchased.Saved.SavedID}, Date: now + 3}); !errors.Is(err, domain.ErrStarGiftAlreadyConverted) {
		t.Fatalf("repeated channel conversion err = %v, want already converted", err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance FROM channel_stars_balances WHERE channel_id=$1`, created.Channel.ID).Scan(&channelBalance); err != nil || channelBalance != 20 {
		t.Fatalf("channel balance after replay = %d err %v", channelBalance, err)
	}

	// A third party may prepay the upgrade entitlement of a channel-owned gift.
	// The payer's personal Stars and the channel saved-gift entitlement commit
	// together; the payment is also visible in channel Recent Actions.
	channelPrepayTargetReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: actor.ID, To: channelPeer,
		GiftID: entry.Gift.ID, CommandKey: "channel-prepay-target-" + suffix, Date: now + 4})
	channelPrepayTarget, err := lifecycle.PurchaseStarGift(ctx, channelPrepayTargetReq)
	if err != nil || channelPrepayTarget.Saved.PrepaidUpgradeHash == "" {
		t.Fatalf("channel prepay target purchase = %+v err %v", channelPrepayTarget, err)
	}
	prepayTarget, prepayPrice, err := lifecycle.PrepaidUpgradeTarget(ctx, channelPeer, channelPrepayTarget.Saved.PrepaidUpgradeHash)
	if err != nil || prepayTarget.ID != channelPrepayTarget.Saved.ID || prepayPrice != 100 {
		t.Fatalf("channel prepay target = %+v price=%d err=%v", prepayTarget, prepayPrice, err)
	}
	channelPrepayReq := domain.StarGiftPrepaidUpgradeRequest{
		PayerUserID: actor.ID, Owner: channelPeer, Hash: channelPrepayTarget.Saved.PrepaidUpgradeHash,
		ChargeStars: 100, FormID: 21006, CommandKey: "channel-prepay-" + suffix, Date: now + 4,
	}
	channelPrepay, err := lifecycle.PrepayStarGiftUpgrade(ctx, channelPrepayReq)
	if err != nil || channelPrepay.Saved.PrepaidUpgradeStars != 100 || channelPrepay.Saved.PrepaidUpgradeHash != "" ||
		channelPrepay.Send.RecipientMessage.OwnerUserID != actor.ID {
		t.Fatalf("channel prepaid entitlement = %+v err %v", channelPrepay, err)
	}
	var prepayLogs int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND message::text LIKE '%prepaid_upgrade%'`, created.Channel.ID).Scan(&prepayLogs); err != nil || prepayLogs != 2 {
		t.Fatalf("channel prepaid upgrade admin logs = %d err %v", prepayLogs, err)
	}
	channelPrepayReplay, err := lifecycle.PrepayStarGiftUpgrade(ctx, channelPrepayReq)
	if err != nil || !channelPrepayReplay.Duplicate || channelPrepayReplay.Saved.ID != channelPrepay.Saved.ID {
		t.Fatalf("channel prepaid entitlement replay = %+v err %v", channelPrepayReplay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND message::text LIKE '%prepaid_upgrade%'`, created.Channel.ID).Scan(&prepayLogs); err != nil || prepayLogs != 2 {
		t.Fatalf("channel prepaid upgrade replay logs = %d err %v", prepayLogs, err)
	}

	var ptsBeforeUpgrade int
	if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id=$1`, created.Channel.ID).Scan(&ptsBeforeUpgrade); err != nil {
		t.Fatal(err)
	}
	prepaidPurchaseReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: actor.ID, To: channelPeer,
		GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "channel-prepaid-purchase-" + suffix, Date: now + 4})
	prepaidPurchase, err := lifecycle.PurchaseStarGift(ctx, prepaidPurchaseReq)
	if err != nil || prepaidPurchase.Saved.PrepaidUpgradeStars != 100 || prepaidPurchase.Saved.SavedID <= 0 {
		t.Fatalf("channel prepaid gift purchase = %+v err %v", prepaidPurchase, err)
	}
	upgradeReq := domain.StarGiftUpgradeRequest{UserID: actor.ID,
		Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: prepaidPurchase.Saved.SavedID}, RequirePrepaid: true,
		KeepOriginalDetails: true, CommandKey: "channel-upgrade-" + suffix, Date: now + 5,
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, upgradeReq)
	if err != nil || upgraded.Saved.Owner != channelPeer || upgraded.Unique.Owner != channelPeer ||
		upgraded.Saved.SavedID != prepaidPurchase.Saved.SavedID || upgraded.Send.RecipientMessage.OwnerUserID != actor.ID {
		t.Fatalf("channel prepaid upgrade = %+v err %v", upgraded, err)
	}
	action := upgraded.Send.RecipientMessage.Media.ServiceAction.StarGiftUnique
	if action == nil || action.FromUserID != domain.OfficialSystemUserID || action.Peer != channelPeer ||
		action.SavedID != prepaidPurchase.Saved.SavedID || !action.Upgrade || !action.PrepaidUpgrade || action.TransferStars != 25 {
		t.Fatalf("channel upgrade service action = %+v", action)
	}
	var ptsAfterUpgrade int
	if err := pool.QueryRow(ctx, `SELECT pts FROM channels WHERE id=$1`, created.Channel.ID).Scan(&ptsAfterUpgrade); err != nil || ptsAfterUpgrade != ptsBeforeUpgrade {
		t.Fatalf("channel pts after profile gift upgrade = %d want %d err %v", ptsAfterUpgrade, ptsBeforeUpgrade, err)
	}
	var upgradeLogs int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND message::text LIKE '%star_gift_unique%'`, created.Channel.ID).Scan(&upgradeLogs); err != nil || upgradeLogs != 1 {
		t.Fatalf("channel upgrade admin logs = %d err %v", upgradeLogs, err)
	}
	replayedUpgrade, err := upgrades.UpgradeStarGift(ctx, upgradeReq)
	if err != nil || !replayedUpgrade.Duplicate || replayedUpgrade.Unique.ID != upgraded.Unique.ID {
		t.Fatalf("channel upgrade replay = %+v err %v", replayedUpgrade, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND message::text LIKE '%star_gift_unique%'`, created.Channel.ID).Scan(&upgradeLogs); err != nil || upgradeLogs != 1 {
		t.Fatalf("channel upgrade replay admin logs = %d err %v", upgradeLogs, err)
	}
	dropped, err := lifecycle.DropStarGiftOriginalDetails(ctx, domain.StarGiftDropOriginalDetailsRequest{
		UserID: actor.ID, Ref: domain.SavedStarGiftRef{Owner: channelPeer, SavedID: upgraded.Saved.SavedID},
		ChargeStars: 25, FormID: 21007, CommandKey: "channel-drop-details-" + suffix, Date: now + 6,
	})
	if err != nil || dropped.Saved.Owner != channelPeer || dropped.Unique.KeepOriginalDetails || dropped.Saved.DropOriginalDetailsStars != 0 {
		t.Fatalf("channel drop original details = %+v err %v", dropped, err)
	}

	listed, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: actor.ID,
		Ref:    domain.SavedStarGiftRef{Owner: channelPeer, SavedID: upgraded.Saved.SavedID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: 1000}, Date: now + 6,
	})
	if err != nil || listed.ResellAmount == nil || listed.Owner != channelPeer {
		t.Fatalf("list channel collectible = %+v err %v", listed, err)
	}
	if balance, err := lifecycle.TonBalance(ctx, actor.ID); err != nil || balance != 1_000_000 {
		t.Fatalf("channel resale buyer local TON grant = %d err %v", balance, err)
	}
	resaleReq := domain.StarGiftResalePurchaseRequest{BuyerUserID: actor.ID, Slug: listed.Slug, To: targetChannelPeer,
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyTON, Amount: 1000}, FormID: 21004,
		CommandKey: "channel-to-channel-resale-" + suffix, Date: now + 7,
	}
	resold, err := lifecycle.PurchaseResaleStarGift(ctx, resaleReq)
	if err != nil || resold.Unique.Owner != targetChannelPeer || resold.Saved.Owner != targetChannelPeer ||
		resold.Saved.SavedID != upgraded.Saved.ID || resold.Balance.Balance != 999000 {
		t.Fatalf("channel-to-channel local TON resale = %+v err %v", resold, err)
	}
	var channelTON, channelTONTxns, targetResaleLogs, commission int64
	if err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM channel_ton_balances WHERE channel_id=$1`, created.Channel.ID).Scan(&channelTON); err != nil || channelTON != 900 {
		t.Fatalf("channel local TON proceeds = %d err %v", channelTON, err)
	}
	if balance, err := lifecycle.ChannelTonBalance(ctx, created.Channel.ID); err != nil || balance != 900 {
		t.Fatalf("channel ton balance projection = %d err %v", balance, err)
	}
	tonPage, err := lifecycle.ChannelTonTransactions(ctx, created.Channel.ID, "", 20)
	if err != nil || tonPage.Balance != 900 || len(tonPage.Transactions) != 1 ||
		tonPage.Transactions[0].Amount != 900 || tonPage.Transactions[0].Reason != domain.StarsReasonGiftResale {
		t.Fatalf("channel ton transaction projection = %+v err %v", tonPage, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_ton_transactions WHERE channel_id=$1 AND gift_id=$2`, created.Channel.ID, listed.ID).Scan(&channelTONTxns); err != nil || channelTONTxns != 1 {
		t.Fatalf("channel local TON transactions = %d err %v", channelTONTxns, err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events WHERE channel_id=$1 AND message::text LIKE '%star_gift_unique%'`,
		createdTarget.Channel.ID).Scan(&targetResaleLogs); err != nil || targetResaleLogs != 1 {
		t.Fatalf("target channel resale admin logs = %d err %v", targetResaleLogs, err)
	}
	if err := pool.QueryRow(ctx, `SELECT commission_amount FROM star_gift_sales WHERE command_key=$1`, resaleReq.CommandKey).Scan(&commission); err != nil || commission != 100 {
		t.Fatalf("channel TON resale commission = %d err %v", commission, err)
	}
	resaleReplay, err := lifecycle.PurchaseResaleStarGift(ctx, resaleReq)
	if err != nil || !resaleReplay.Duplicate || resaleReplay.Unique.ID != resold.Unique.ID {
		t.Fatalf("channel resale replay = %+v err %v", resaleReplay, err)
	}
	if err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM channel_ton_balances WHERE channel_id=$1`, created.Channel.ID).Scan(&channelTON); err != nil || channelTON != 900 {
		t.Fatalf("channel TON proceeds after replay = %d err %v", channelTON, err)
	}

	var remainsBefore int
	if err := pool.QueryRow(ctx, `SELECT availability_remains FROM star_gift_catalog WHERE gift_id=$1`, entry.Gift.ID).Scan(&remainsBefore); err != nil {
		t.Fatal(err)
	}
	balanceBefore, _ := NewStarsStore(pool).GetBalance(ctx, actor.ID)
	invalidChannelReq := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{BuyerUserID: actor.ID,
		To: domain.Peer{Type: domain.PeerTypeChannel, ID: created.Channel.ID + 999999}, GiftID: entry.Gift.ID,
		CommandKey: "invalid-channel-purchase-" + suffix, Date: now + 2})
	_, err = lifecycle.PurchaseStarGift(ctx, invalidChannelReq)
	if err == nil {
		t.Fatal("purchase to missing channel unexpectedly succeeded")
	}
	var remainsAfter int
	if err := pool.QueryRow(ctx, `SELECT availability_remains FROM star_gift_catalog WHERE gift_id=$1`, entry.Gift.ID).Scan(&remainsAfter); err != nil || remainsAfter != remainsBefore {
		t.Fatalf("inventory after rolled-back channel purchase = %d want %d err %v", remainsAfter, remainsBefore, err)
	}
	if balanceAfter, err := NewStarsStore(pool).GetBalance(ctx, actor.ID); err != nil || balanceAfter.Balance != balanceBefore.Balance {
		t.Fatalf("balance after rolled-back channel purchase = %+v want %+v err %v", balanceAfter, balanceBefore, err)
	}

	auctionEntry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Channel Auction " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
		AvailabilityTotal: 1, AvailabilityRemains: 1, GiftsPerRound: 1, AuctionStartDate: now - 10,
		AuctionSlug: "channel-auction-" + suffix,
		Document:    collectibleTestDocument(baseDocumentID+100, "channel-auction.tgs"), Blob: collectibleTestBlob(baseDocumentID+100, "channel-auction"),
		Animation: collectibleTestAnimation("channel-auction.tgs"), Actor: "integration", CommandID: "channel-auction-" + suffix,
	})
	if err != nil {
		t.Fatalf("create channel auction: %v", err)
	}
	if _, _, err := lifecycle.BidStarGiftAuction(ctx, domain.StarGiftAuctionBidRequest{UserID: actor.ID,
		GiftID: auctionEntry.Gift.ID, Peer: channelPeer, BidAmount: 100, FormID: 22001, Date: now + 3,
	}); err != nil {
		t.Fatalf("bid channel auction: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_auctions SET next_round_at=$2 WHERE gift_id=$1`, auctionEntry.Gift.ID, now+4); err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.SweepStarGiftLifecycle(ctx, now+4, 1000); err != nil {
		t.Fatalf("settle channel auction: %v", err)
	}
	var awardSavedID int64
	if err := pool.QueryRow(ctx, `SELECT saved_gift_id FROM star_gift_auction_acquired WHERE gift_id=$1`, auctionEntry.Gift.ID).Scan(&awardSavedID); err != nil || awardSavedID <= 0 {
		t.Fatalf("channel auction saved id = %d err %v", awardSavedID, err)
	}
	var awardLogs int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM channel_admin_log_events
WHERE channel_id=$1 AND message::text LIKE '%auction_acquired%'`, created.Channel.ID).Scan(&awardLogs); err != nil || awardLogs != 1 {
		t.Fatalf("channel auction admin logs = %d err %v", awardLogs, err)
	}
}

func issueLifecyclePurchaseForm(t *testing.T, ctx context.Context, lifecycle *StarGiftLifecycleStore,
	req domain.StarGiftPurchaseRequest) domain.StarGiftPurchaseRequest {
	t.Helper()
	var revisionID int64
	if err := lifecycle.db.QueryRow(ctx, `SELECT active_revision_id FROM star_gift_catalog WHERE gift_id=$1`, req.GiftID).Scan(&revisionID); err != nil {
		t.Fatalf("load active gift revision: %v", err)
	}
	gift, found, err := NewStarGiftStore(lifecycle.db).CatalogRevision(ctx, revisionID)
	if err != nil || !found {
		t.Fatalf("load gift revision %d: found=%v err=%v", revisionID, found, err)
	}
	req.RevisionID = gift.RevisionID
	req.ChargeStars = gift.Stars
	if req.IncludeUpgrade {
		req.ChargeStars += gift.UpgradeStars
	}
	issued, err := lifecycle.IssueStarGiftPurchaseForm(ctx, domain.StarGiftPurchaseForm{
		BuyerUserID: req.BuyerUserID, To: req.To, GiftID: req.GiftID, RevisionID: req.RevisionID,
		IncludeUpgrade: req.IncludeUpgrade, HideName: req.HideName, Message: req.Message, ChargeStars: req.ChargeStars,
		IssuedAt: req.Date, ExpiresAt: req.Date + 600,
	})
	if err != nil {
		t.Fatalf("issue purchase form: %v", err)
	}
	req.FormID = issued.FormID
	return req
}

func craftedSourceEditForUserAndGift(result domain.StarGiftCraftResult, userID, uniqueGiftID int64) domain.EditedMessageForUser {
	for _, edit := range result.SourceEdits {
		if edit.UserID != userID {
			continue
		}
		action := starGiftUniqueActionFromEdit(edit)
		if action != nil && action.Gift.ID == uniqueGiftID {
			return edit
		}
	}
	return domain.EditedMessageForUser{UserID: userID}
}

func starGiftUniqueActionFromEdit(edit domain.EditedMessageForUser) *domain.MessageStarGiftUniqueAction {
	if edit.Message.Media == nil || edit.Message.Media.ServiceAction == nil {
		return nil
	}
	return edit.Message.Media.ServiceAction.StarGiftUnique
}
