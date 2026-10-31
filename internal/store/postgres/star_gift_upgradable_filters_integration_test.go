package postgres

import (
	"context"
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftOwnGiftsFiltersPostgres pins the filter combination Telegram
// Desktop sends for the gift box "my collectibles" tab.
//
// The client loads that tab with exclude_unlimited + exclude_upgradable +
// exclude_unupgradable together (data_star_gift.cpp, MyUniqueGiftsSlice). The
// two upgrade flags are documented as mutually exclusive, so a naive
// implementation returns an empty list and the tab never appears — the "gift box
// only has All / Collectibles" report. Relaxing either flag on its own is not an
// option, because the profile gift filter expresses its Upgradeable / Limited
// categories with exactly these two flags, so the pair is only neutralised when
// both are present.
func TestStarGiftOwnGiftsFiltersPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1884"+suffix+"01", "OwnGiftsOwner", "")
	sender := createTestUser(t, ctx, users, "+1884"+suffix+"02", "OwnGiftsSender", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	messages := NewMessageStore(pool)
	base := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "OwnGifts " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(base, "own-gifts.tgs"),
		Blob:     collectibleTestBlob(base, "own-gifts"), Animation: collectibleTestAnimation("own-gifts.tgs"),
		Actor: "integration", CommandID: "own-gifts-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	// Upgradable gift: the upgrade tier has stock.
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "own-" + suffix,
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
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 81, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 82, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "own-gifts-pool-" + suffix,
	}); err != nil {
		t.Fatalf("pool: %v", err)
	}

	upgradable := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now - 300, ConvertStars: 20,
	})
	collectible := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now - 200, ConvertStars: 20,
	})

	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, now); err != nil {
		t.Fatalf("grant: %v", err)
	}
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: collectible.MsgID},
		ChargeStars: 100, FormID: 41101, CommandKey: "own-gifts-upgrade-" + suffix, Date: now - 100,
	}); err != nil {
		t.Fatalf("upgrade own collectible: %v", err)
	}

	ids := func(filter domain.SavedStarGiftFilter) []int64 {
		t.Helper()
		filter.Owner, filter.Limit = ownerPeer, 10
		page, err := gifts.ListByOwnerFiltered(ctx, filter)
		if err != nil {
			t.Fatalf("list %+v: %v", filter, err)
		}
		out := make([]int64, 0, len(page.Gifts))
		for _, gift := range page.Gifts {
			out = append(out, gift.ID)
		}
		return out
	}

	// The desktop "My Collectibles" tab's exact request: the result must be that upgraded gift.
	own := ids(domain.SavedStarGiftFilter{ExcludeUnlimited: true, ExcludeUpgradable: true, ExcludeUnupgradable: true})
	if !slices.Equal(own, []int64{collectible.ID}) {
		t.Fatalf("own gifts = %v, want only the collectible %d", own, collectible.ID)
	}
	// An upgraded gift must really be unique in the result, otherwise the tab's card is an empty shell.
	page, err := gifts.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: ownerPeer, ExcludeUnlimited: true, ExcludeUpgradable: true, ExcludeUnupgradable: true, Limit: 10,
	})
	if err != nil || len(page.Gifts) != 1 || page.Gifts[0].UniqueGiftID == 0 {
		t.Fatalf("own gift unique id = %+v err %v, want a collectible instance", page.Gifts, err)
	}

	// Each flag must keep its literal meaning: exclude_unupgradable leaves only "upgradable". The
	// profile page's Upgradeable / Limited category filters are expressed by these two flags
	// respectively (info_peer_gifts_widget.cpp), and loosening either would scramble the profile filtering.
	if got := ids(domain.SavedStarGiftFilter{ExcludeUnupgradable: true}); !slices.Equal(got, []int64{upgradable.ID}) {
		t.Fatalf("exclude_unupgradable = %v, want only [%d]", got, upgradable.ID)
	}
	if got := ids(domain.SavedStarGiftFilter{ExcludeUpgradable: true}); !slices.Equal(got, []int64{collectible.ID}) {
		t.Fatalf("exclude_upgradable = %v, want [%d]", got, collectible.ID)
	}
	if got := ids(domain.SavedStarGiftFilter{ExcludeUnique: true}); !slices.Equal(got, []int64{upgradable.ID}) {
		t.Fatalf("exclude_unique = %v, want [%d]", got, upgradable.ID)
	}
}
