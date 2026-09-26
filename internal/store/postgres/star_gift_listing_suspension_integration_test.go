package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftResaleSuspendedByAccountFreezePostgres 钉住「заморозка аккаунта =
// витрина его подарков пустеет»: строки листингов не удаляются, а помечаются
// suspended, поэтому разморозка возвращает их с той же ценой. Проверяются все
// читатели маркета (список, value info, resell_amount уникального подарка,
// проекции каталога), невозможность купить и невозможность выставить/переоценить
// подарок замороженного продавца, а также изоляция: листинг второго продавца того
// же подарка остаётся в продаже.
func TestStarGiftResaleSuspendedByAccountFreezePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	seller := createTestUser(t, ctx, users, "+1886"+suffix+"01", "FrozenResaleSeller", "")
	peerSeller := createTestUser(t, ctx, users, "+1886"+suffix+"02", "PeerResaleSeller", "")
	buyer := createTestUser(t, ctx, users, "+1886"+suffix+"03", "FrozenResaleBuyer", "")
	gifter := createTestUser(t, ctx, users, "+1886"+suffix+"04", "FrozenResaleGifter", "")

	stars := NewStarsStore(pool)
	for _, u := range []domain.User{seller, peerSeller, buyer, gifter} {
		if _, _, err := stars.EnsureGrant(ctx, u.ID, 20000, now); err != nil {
			t.Fatalf("grant %d: %v", u.ID, err)
		}
	}

	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Freeze Suspension " + suffix, Stars: 600, ConvertStars: 200, Enabled: true,
		Document: collectibleTestDocument(base, "freeze-suspension.tgs"),
		Blob:     collectibleTestBlob(base, "freeze-suspension"), Animation: collectibleTestAnimation("freeze-suspension.tgs"),
		Actor: "integration", CommandID: "freeze-suspension-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: "fz-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+1, "fz-model.tgs"), Blob: collectibleTestBlobPtr(base+1, "fz-model"), Animation: collectibleTestAnimationPtr("fz-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+4, "fz-model-two.tgs"), Blob: collectibleTestBlobPtr(base+4, "fz-model-two"), Animation: collectibleTestAnimationPtr("fz-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+2, "fz-pattern.tgs"), Blob: collectibleTestBlobPtr(base+2, "fz-pattern"), Animation: collectibleTestAnimationPtr("fz-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+3, "fz-pattern-two.tgs"), Blob: collectibleTestBlobPtr(base+3, "fz-pattern-two"), Animation: collectibleTestAnimationPtr("fz-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 91, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 92, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "freeze-suspension-pool-" + suffix,
	}); err != nil {
		t.Fatalf("collectible pool: %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))

	upgradeUnique := func(from, to domain.User, ref string, date int) (domain.UniqueStarGift, domain.SavedStarGiftRef) {
		t.Helper()
		purchase := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: from.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: to.ID},
			GiftID: entry.Gift.ID, IncludeUpgrade: true, CommandKey: "fz-purchase-" + ref + "-" + suffix, Date: date,
		})
		bought, err := lifecycle.PurchaseStarGift(ctx, purchase)
		if err != nil {
			t.Fatalf("purchase %s: %v", ref, err)
		}
		upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
			UserID: to.ID, Ref: domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: to.ID}, MsgID: bought.Saved.MsgID},
			RequirePrepaid: true, KeepOriginalDetails: true, CommandKey: "fz-upgrade-" + ref + "-" + suffix, Date: date + 1,
		})
		if err != nil {
			t.Fatalf("upgrade %s: %v", ref, err)
		}
		return upgraded.Unique, domain.SavedStarGiftRef{
			Owner: domain.Peer{Type: domain.PeerTypeUser, ID: to.ID}, MsgID: upgraded.Saved.MsgID}
	}
	sellerUnique, sellerRef := upgradeUnique(gifter, seller, "seller", now)
	peerUnique, peerRef := upgradeUnique(gifter, peerSeller, "peer", now+10)

	// Второй продавец выставляет дороже, поэтому после заморозки первого пол,
	// витрина и проекции каталога обязаны считать только листинг второго: если
	// suspended попадёт в агрегаты, пол останется на заниженной цене первого.
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: peerSeller.ID,
		Ref:    peerRef,
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 900}, Date: now + 12,
	}); err != nil {
		t.Fatalf("peer listing: %v", err)
	}
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: seller.ID,
		Ref:    sellerRef,
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, Date: now + 2,
	}); err != nil {
		t.Fatalf("seller listing: %v", err)
	}

	listedIDs := func() map[int64]bool {
		t.Helper()
		page, err := lifecycle.ListResaleStarGifts(ctx, domain.StarGiftResaleFilter{GiftID: entry.Gift.ID, Limit: 10})
		if err != nil {
			t.Fatalf("list resale: %v", err)
		}
		ids := make(map[int64]bool, len(page.Gifts))
		for _, gift := range page.Gifts {
			ids[gift.ID] = true
		}
		if page.Count != len(page.Gifts) {
			t.Fatalf("resale page count = %d, want %d", page.Count, len(page.Gifts))
		}
		return ids
	}
	catalogFloor := func() (resale, floor int64) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT availability_resale,resell_min_stars FROM star_gift_catalog WHERE gift_id=$1`,
			entry.Gift.ID).Scan(&resale, &floor); err != nil {
			t.Fatalf("read catalog resale projection: %v", err)
		}
		return resale, floor
	}
	resellAmount := func(slug string) *domain.StarGiftAmount {
		t.Helper()
		gift, found, err := gifts.UniqueBySlug(ctx, slug)
		if err != nil || !found {
			t.Fatalf("unique by slug %q = %v found %v", slug, err, found)
		}
		return gift.ResellAmount
	}

	if ids := listedIDs(); !ids[sellerUnique.ID] || !ids[peerUnique.ID] {
		t.Fatalf("resale market before freeze = %v, want both listings", ids)
	}
	if amount := resellAmount(sellerUnique.Slug); amount == nil || amount.Amount != 500 {
		t.Fatalf("seller resell amount before freeze = %+v, want 500 XTR", amount)
	}
	if resale, floor := catalogFloor(); resale != 2 || floor != 500 {
		t.Fatalf("catalog projections before freeze = %d/%d, want 2/500", resale, floor)
	}

	admin := NewAdminStore(pool)
	frozen, err := admin.SetAccountFreeze(ctx, domain.AccountFreeze{UserID: seller.ID, Frozen: true,
		Since: time.Unix(int64(now), 0), Until: time.Unix(int64(now)+3600, 0),
		AppealURL: "https://example.test/" + suffix, Reason: "integration", Actor: "integration",
		CommandID: "freeze-resale-seller-" + suffix})
	if err != nil || !frozen.Frozen {
		t.Fatalf("freeze seller = %+v err %v", frozen, err)
	}

	// Витрина теряет только листинг замороженного продавца.
	if ids := listedIDs(); ids[sellerUnique.ID] || !ids[peerUnique.ID] {
		t.Fatalf("resale market while frozen = %v, want only the peer listing", ids)
	}
	if amount := resellAmount(sellerUnique.Slug); amount != nil {
		t.Fatalf("frozen seller resell amount = %+v, want nil", amount)
	}
	if amount := resellAmount(peerUnique.Slug); amount == nil || amount.Amount != 900 {
		t.Fatalf("peer resell amount while frozen = %+v, want 900 XTR", amount)
	}
	info, err := lifecycle.UniqueStarGiftValueInfo(ctx, sellerUnique.ID)
	if err != nil || info.ListedCount != 1 || info.FloorPrice != 900 {
		t.Fatalf("frozen seller value info = %+v err %v, want listed 1 floor 900 (peer listing only)", info, err)
	}
	if resale, floor := catalogFloor(); resale != 1 || floor != 900 {
		t.Fatalf("catalog projections while frozen = %d/%d, want 1/900", resale, floor)
	}
	// Строка листинга обязана сохранить цену и время выставления: разморозка
	// возвращает подарок на витрину, а не восстанавливает его с нуля.
	var amount, listedAt, updatedAt int64
	var suspended bool
	if err := pool.QueryRow(ctx, `SELECT amount,listed_at,updated_at,suspended FROM star_gift_listings WHERE unique_gift_id=$1`,
		sellerUnique.ID).Scan(&amount, &listedAt, &updatedAt, &suspended); err != nil {
		t.Fatalf("read frozen listing row: %v", err)
	}
	if amount != 500 || listedAt != int64(now+2) || updatedAt != int64(now+2) || !suspended {
		t.Fatalf("frozen listing row = %d/%d/%d suspended=%v, want 500/%d/%d suspended=true",
			amount, listedAt, updatedAt, suspended, now+2, now+2)
	}

	// Купить подарок замороженного продавца нельзя, и деньги покупателя не
	// списываются.
	buyerBalanceBefore, err := stars.GetBalance(ctx, buyer.ID)
	if err != nil {
		t.Fatalf("buyer balance before frozen purchase: %v", err)
	}
	if _, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: sellerUnique.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, FormID: 21101,
		CommandKey: "fz-frozen-resale-" + suffix, Date: now + 3,
	}); !errors.Is(err, domain.ErrStarGiftResaleUnavailable) {
		t.Fatalf("purchase from frozen seller = %v, want ErrStarGiftResaleUnavailable", err)
	}
	if buyerBalanceAfter, err := stars.GetBalance(ctx, buyer.ID); err != nil || buyerBalanceAfter.Balance != buyerBalanceBefore.Balance {
		t.Fatalf("buyer balance after rejected frozen purchase = %+v err %v, want %d",
			buyerBalanceAfter, err, buyerBalanceBefore.Balance)
	}
	// Выставить или переоценить подарок замороженного аккаунта тоже нельзя.
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{ActorUserID: seller.ID,
		Ref:    sellerRef,
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 1200}, Date: now + 4,
	}); !errors.Is(err, domain.ErrStarGiftResaleUnavailable) {
		t.Fatalf("re-list while frozen = %v, want ErrStarGiftResaleUnavailable", err)
	}
	// Тот же запрет держит guard-триггер, то есть даже прямой SQL не может вернуть
	// suspended-листинг в продажу, пока аккаунт заморожен.
	if _, err := pool.Exec(ctx, `UPDATE star_gift_listings SET suspended=false,amount=1200 WHERE unique_gift_id=$1`,
		sellerUnique.ID); err == nil {
		t.Fatal("guard trigger must reject re-activating a listing of a frozen seller")
	}

	// Разморозка возвращает ровно те листинги, которые были выставлены до
	// заморозки, с той же ценой, и покупка снова проходит.
	if _, err := admin.SetAccountFreeze(ctx, domain.AccountFreeze{UserID: seller.ID, Actor: "integration",
		CommandID: "unfreeze-resale-seller-" + suffix}); err != nil {
		t.Fatalf("unfreeze seller: %v", err)
	}
	if ids := listedIDs(); !ids[sellerUnique.ID] || !ids[peerUnique.ID] {
		t.Fatalf("resale market after unfreeze = %v, want both listings back", ids)
	}
	if restored := resellAmount(sellerUnique.Slug); restored == nil || restored.Amount != 500 {
		t.Fatalf("seller resell amount after unfreeze = %+v, want 500 XTR", restored)
	}
	info, err = lifecycle.UniqueStarGiftValueInfo(ctx, sellerUnique.ID)
	if err != nil || info.ListedCount != 2 || info.FloorPrice != 500 {
		t.Fatalf("seller value info after unfreeze = %+v err %v, want listed 2 floor 500", info, err)
	}
	if resale, floor := catalogFloor(); resale != 2 || floor != 500 {
		t.Fatalf("catalog projections after unfreeze = %d/%d, want 2/500", resale, floor)
	}
	resold, err := lifecycle.PurchaseResaleStarGift(ctx, domain.StarGiftResalePurchaseRequest{
		BuyerUserID: buyer.ID, Slug: sellerUnique.Slug, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		Amount: domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, FormID: 21102,
		CommandKey: "fz-restored-resale-" + suffix, Date: now + 5,
	})
	if err != nil || resold.Unique.Owner.ID != buyer.ID {
		t.Fatalf("purchase after unfreeze = %+v err %v", resold, err)
	}
	if buyerBalanceAfter, err := stars.GetBalance(ctx, buyer.ID); err != nil || buyerBalanceAfter.Balance != buyerBalanceBefore.Balance-500 {
		t.Fatalf("buyer balance after restored purchase = %+v err %v, want %d",
			buyerBalanceAfter, err, buyerBalanceBefore.Balance-500)
	}

	// Покупка подарка замороженного продавца не оставляет pending-предложений и
	// не восстанавливает листинг: продажа состоялась, витрина её продавца пуста.
	if ids := listedIDs(); ids[sellerUnique.ID] || !ids[peerUnique.ID] {
		t.Fatalf("resale market after sold = %v, want only the peer listing", ids)
	}
}

// TestStarGiftResaleFreezeSkipsNeverFrozenSellerPostgres 覆盖最便宜的路径： у
// продавца нет строки в account_restrictions вообще, поэтому FOR SHARE не находит
// ничего и выставление проходит.
func TestStarGiftResaleFreezeSkipsNeverFrozenSellerPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var frozen int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_restrictions`).Scan(&frozen); err != nil {
		t.Fatalf("count account restrictions: %v", err)
	}
	var misses int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM account_restrictions WHERE user_id=$1`,
		1<<40).Scan(&misses); err != nil || misses != 0 {
		t.Fatalf("restriction lookup for an unknown user = %d err %v", misses, err)
	}
	if err := rejectFrozenStarGiftSeller(ctx, pool, 1<<40); err != nil {
		t.Fatalf("never-frozen seller must pass the listing freeze gate: %v", err)
	}
}
