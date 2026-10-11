package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestStarGiftSavedListingFlagsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1780"+suffix+"71", "ListingSender", "")
	owner := createTestUser(t, ctx, users, "+1780"+suffix+"72", "ListingOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Listing " + suffix, Stars: 50, ConvertStars: 25, Enabled: true,
		Document:  collectibleTestDocument(baseDocumentID, "gift.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "gift"),
		Animation: collectibleTestAnimation("gift.tgs"),
		Actor:     "integration", CommandID: "catalog-listing-" + suffix,
	})
	if err != nil {
		t.Fatalf("create listing catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "lst-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Base",
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document:  collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+1, "model"),
			Animation: collectibleTestAnimationPtr("model.tgs"),
		}, {
			Kind: domain.StarGiftCollectibleModel, Name: "Rare",
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 100,
			Document:  collectibleTestDocumentPtr(baseDocumentID+3, "model-two.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+3, "model-two"),
			Animation: collectibleTestAnimationPtr("model-two.tgs"),
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectiblePattern, Name: "Orbit",
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document:  collectibleTestPatternDocumentPtr(baseDocumentID+2, "pattern.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+2, "pattern"),
			Animation: collectibleTestAnimationPtr("pattern.tgs"),
		}, {
			Kind: domain.StarGiftCollectiblePattern, Name: "Rings",
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 100,
			Document:  collectibleTestPatternDocumentPtr(baseDocumentID+4, "pattern-two.tgs"),
			Blob:      collectibleTestBlobPtr(baseDocumentID+4, "pattern-two"),
			Animation: collectibleTestAnimationPtr("pattern-two.tgs"),
		}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 93,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
		}, {
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 94,
			CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1,
		}},
		Actor: "integration", CommandID: "collectibles-listing-" + suffix,
		OfficialGiftID: 5170145012310081617, SourceManifestSHA256: make([]byte, 32),
	}); err != nil {
		t.Fatalf("publish listing collectible pool: %v", err)
	}

	messages := NewMessageStore(pool)
	uniqueSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001000, ConvertStars: 25,
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700001001); err != nil {
		t.Fatalf("grant listing stars: %v", err)
	}
	upgraded, err := NewStarGiftUpgradeStore(pool, messages).UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: uniqueSaved.MsgID},
		KeepOriginalDetails: true, ChargeStars: 100, FormID: 994,
		CommandKey: "listing-" + suffix, Date: 1700001002,
	})
	if err != nil || upgraded.Saved.UniqueGiftID == 0 {
		t.Fatalf("upgrade for listing = %+v err %v", upgraded, err)
	}
	plainSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001010, ConvertStars: 25,
	})
	if _, err := pool.Exec(ctx, `UPDATE unique_star_gifts SET value_currency='XTR', value_amount=777 WHERE id=$1`,
		upgraded.Saved.UniqueGiftID); err != nil {
		t.Fatalf("price the unique gift: %v", err)
	}

	ids := func(page domain.SavedStarGiftPage) []int64 {
		out := make([]int64, 0, len(page.Gifts))
		for _, gift := range page.Gifts {
			out = append(out, gift.ID)
		}
		return out
	}
	byValue := domain.SavedStarGiftFilter{Owner: ownerPeer, Limit: 10, SortByValue: true}
	valuePage, err := gifts.ListByOwnerFiltered(ctx, byValue)
	if err != nil {
		t.Fatalf("list by value: %v", err)
	}
	if valuePage.Count != 2 || len(valuePage.Gifts) != 2 ||
		valuePage.Gifts[0].ID != upgraded.Saved.ID || valuePage.Gifts[1].ID != plainSaved.ID {
		t.Fatalf("by value = count %d ids %v, want [%d %d]",
			valuePage.Count, ids(valuePage), upgraded.Saved.ID, plainSaved.ID)
	}

	var paged []int64
	offset := ""
	for page := 0; ; page++ {
		part := byValue
		part.Limit = 1
		part.Offset = offset
		chunk, err := gifts.ListByOwnerFiltered(ctx, part)
		if err != nil {
			t.Fatalf("paged list %d: %v", page, err)
		}
		if chunk.Count != 2 || len(chunk.Gifts) != 1 {
			t.Fatalf("paged chunk %d = count %d ids %v", page, chunk.Count, ids(chunk))
		}
		paged = append(paged, chunk.Gifts[0].ID)
		offset = chunk.NextOffset
		if offset == "" {
			break
		}
		if page > 4 {
			t.Fatalf("paging did not terminate, offset %q", offset)
		}
	}
	if len(paged) != 2 || paged[0] != upgraded.Saved.ID || paged[1] != plainSaved.ID {
		t.Fatalf("paged by value = %v, want [%d %d]", paged, upgraded.Saved.ID, plainSaved.ID)
	}

	colored := domain.SavedStarGiftFilter{Owner: ownerPeer, Limit: 10, PeerColorAvailable: true}
	if coloredPage, err := gifts.ListByOwnerFiltered(ctx, colored); err != nil ||
		coloredPage.Count != 0 || len(coloredPage.Gifts) != 0 {
		t.Fatalf("peer color before flag = count %d ids %v err %v",
			coloredPage.Count, ids(coloredPage), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_catalog_revisions SET peer_color_available=true
WHERE id=(SELECT active_revision_id FROM star_gift_catalog WHERE gift_id=$1)`, entry.Gift.ID); err != nil {
		t.Fatalf("enable peer color: %v", err)
	}
	if coloredPage, err := gifts.ListByOwnerFiltered(ctx, colored); err != nil ||
		coloredPage.Count != 1 || coloredPage.Gifts[0].ID != upgraded.Saved.ID {
		t.Fatalf("peer color after flag = count %d ids %v err %v",
			coloredPage.Count, ids(coloredPage), err)
	}

	hosted := "0:" + strings.Repeat("cd", 32)
	if _, err := pool.Exec(ctx, `UPDATE unique_star_gifts SET owner_address=$2, owner_peer_type=NULL, owner_peer_id=NULL,
host_peer_type='user', host_peer_id=$1 WHERE id=$3`, owner.ID, hosted, upgraded.Saved.UniqueGiftID); err != nil {
		t.Fatalf("host the unique gift: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE peer_star_gifts SET lifecycle_status='exported' WHERE id=$1`,
		upgraded.Saved.ID); err != nil {
		t.Fatalf("export the unique gift: %v", err)
	}
	plain := domain.SavedStarGiftFilter{Owner: ownerPeer, Limit: 10}
	hostedPage, err := gifts.ListByOwnerFiltered(ctx, plain)
	if err != nil || hostedPage.Count != 2 {
		t.Fatalf("hosted listing = count %d ids %v err %v", hostedPage.Count, ids(hostedPage), err)
	}
	plain.ExcludeHosted = true
	unhostedPage, err := gifts.ListByOwnerFiltered(ctx, plain)
	if err != nil || unhostedPage.Count != 1 || unhostedPage.Gifts[0].ID != plainSaved.ID {
		t.Fatalf("exclude_hosted = count %d ids %v, want [%d]",
			unhostedPage.Count, ids(unhostedPage), plainSaved.ID)
	}
}
