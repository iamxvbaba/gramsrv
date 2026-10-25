package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftDeleteRemovesGiftFromProfilePostgres pins payments.deleteStarGift:
// the gift leaves the profile through the deleted lifecycle state, the row
// survives for the audit trail and a second delete is refused.
func TestStarGiftDeleteRemovesGiftFromProfilePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1882"+suffix+"01", "DeleteOwner", "")
	stranger := createTestUser(t, ctx, users, "+1882"+suffix+"02", "DeleteStranger", "")
	revisionID, giftID := ratingTestCatalogRevision(t, pool)

	var savedID int64
	if err := pool.QueryRow(ctx, `
INSERT INTO peer_star_gifts (owner_peer_type, owner_peer_id, msg_id, gift_id, gift_date, catalog_revision_id, lifecycle_status, converted)
VALUES ('user', $1, 1, $2, 0, $3, 'active', false) RETURNING id`,
		owner.ID, giftID, revisionID).Scan(&savedID); err != nil {
		t.Fatalf("seed saved gift: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_collection_items WHERE saved_gift_id = $1`, savedID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM peer_star_gifts WHERE id = $1`, savedID)
	})

	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	ref := domain.SavedStarGiftRef{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, MsgID: 1}

	// A stranger may not delete somebody else's gift.
	if _, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{
		UserID: stranger.ID, Ref: ref, Date: now,
	}); !errors.Is(err, domain.ErrStarGiftOwnerInvalid) {
		t.Fatalf("stranger delete err = %v, want ErrStarGiftOwnerInvalid", err)
	}

	result, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{UserID: owner.ID, Ref: ref, Date: now})
	if err != nil {
		t.Fatalf("delete star gift: %v", err)
	}
	if !result.Deleted || result.SavedGiftID != savedID || result.UniqueGiftID != 0 {
		t.Fatalf("delete result = %+v", result)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT lifecycle_status FROM peer_star_gifts WHERE id = $1`, savedID).Scan(&status); err != nil {
		t.Fatalf("read lifecycle status: %v", err)
	}
	if status != string(domain.StarGiftLifecycleDeleted) {
		t.Fatalf("lifecycle status = %q, want %q", status, domain.StarGiftLifecycleDeleted)
	}
	// The deleted gift must not be projected as a saved gift any more.
	saved, found, err := NewStarGiftStore(pool).GetByRef(ctx, ref)
	if err != nil {
		t.Fatalf("read saved gift: %v", err)
	}
	if found && saved.LifecycleStatus.Live() {
		t.Fatalf("deleted gift is still live: %+v", saved)
	}
	// A second delete is refused instead of silently succeeding.
	if _, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{UserID: owner.ID, Ref: ref, Date: now}); !errors.Is(err, domain.ErrStarGiftDeleteUnavailable) {
		t.Fatalf("second delete err = %v, want ErrStarGiftDeleteUnavailable", err)
	}
}

// TestStarGiftDeleteRefusesUpgradedGiftPostgres pins that a gift which was
// upgraded into a collectible (NFT) is not deletable: destroying a collectible
// is a different operation with its own financial rules.
func TestStarGiftDeleteRefusesUpgradedGiftPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1883"+suffix+"01", "UpgradedOwner", "")
	sender := createTestUser(t, ctx, users, "+1883"+suffix+"02", "UpgradedSender", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Delete " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "delete.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "delete"), Animation: collectibleTestAnimation("delete.tgs"),
		Actor: "integration", CommandID: "delete-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create delete catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "del-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs"),
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs"),
		}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 91, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
		}, {
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 92, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
			CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111,
		}},
		Actor: "integration", CommandID: "delete-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish delete pool: %v", err)
	}

	messages := newTestMessageStore(pool)
	saved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now, ConvertStars: 20, Message: "upgraded gift",
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, now); err != nil {
		t.Fatalf("grant upgrade stars: %v", err)
	}
	upgrades := NewStarGiftUpgradeStore(pool, messages)
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID},
		ChargeStars: 100, FormID: 8801, CommandKey: "delete-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade gift: %v", err)
	}
	if upgraded.Unique.ID <= 0 {
		t.Fatalf("upgrade result = %+v", upgraded)
	}

	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
	if _, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID}, Date: now + 2,
	}); !errors.Is(err, domain.ErrStarGiftDeleteUnavailable) {
		t.Fatalf("delete upgraded gift err = %v, want ErrStarGiftDeleteUnavailable", err)
	}
	var status string
	if err := pool.QueryRow(ctx, `SELECT lifecycle_status FROM peer_star_gifts WHERE id = $1`, saved.ID).Scan(&status); err != nil {
		t.Fatalf("read lifecycle status: %v", err)
	}
	if status != string(domain.StarGiftLifecycleActive) {
		t.Fatalf("upgraded gift lifecycle = %q, want %q", status, domain.StarGiftLifecycleActive)
	}
}

// TestStarGiftDeleteBlocksLaterUpgradePostgres pins the other direction: a
// deleted gift keeps its row, but a paid upgrade must refuse it under the row
// lock instead of charging Stars against a gift that left the profile.
func TestStarGiftDeleteBlocksLaterUpgradePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1887"+suffix+"01", "DeletedOwner", "")
	sender := createTestUser(t, ctx, users, "+1887"+suffix+"02", "DeletedSender", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Delete upgrade " + suffix, Stars: 50, ConvertStars: 0, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "delete-upgrade.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "delete-upgrade"), Animation: collectibleTestAnimation("delete-upgrade.tgs"),
		Actor: "integration", CommandID: "delete-upgrade-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create delete catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "delup-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs"),
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs"),
		}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 91, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
		}, {
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 92, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
			CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111,
		}},
		Actor: "integration", CommandID: "delete-upgrade-pool-" + suffix,
	}); err != nil {
		t.Fatalf("publish delete pool: %v", err)
	}

	messages := newTestMessageStore(pool)
	saved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now, ConvertStars: 0, Message: "deleted gift",
	})
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
	ref := domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID}
	if _, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{UserID: owner.ID, Ref: ref, Date: now}); err != nil {
		t.Fatalf("delete plain gift: %v", err)
	}
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, now); err != nil {
		t.Fatalf("grant upgrade stars: %v", err)
	}
	before, err := stars.GetBalance(ctx, owner.ID)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	upgrades := NewStarGiftUpgradeStore(pool, messages)
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: ref, ChargeStars: 100, FormID: 8802,
		CommandKey: "delete-then-upgrade-" + suffix, Date: now + 1,
	}); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("upgrade deleted gift err = %v, want ErrStarGiftNotFound", err)
	}
	after, err := stars.GetBalance(ctx, owner.ID)
	if err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if after.Balance != before.Balance {
		t.Fatalf("balance %d -> %d, want unchanged", before.Balance, after.Balance)
	}
	var status string
	var uniqueGiftID *int64
	if err := pool.QueryRow(ctx, `SELECT lifecycle_status, unique_gift_id FROM peer_star_gifts WHERE id = $1`, saved.ID).Scan(&status, &uniqueGiftID); err != nil {
		t.Fatalf("read deleted gift: %v", err)
	}
	if status != string(domain.StarGiftLifecycleDeleted) || uniqueGiftID != nil {
		t.Fatalf("deleted gift = %q unique=%v, want deleted without a collectible", status, uniqueGiftID)
	}
	var minted, commands int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM unique_star_gifts WHERE gift_id = $1`, entry.Gift.ID).Scan(&minted); err != nil {
		t.Fatalf("count collectibles: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM star_gift_upgrade_commands WHERE source_saved_gift_id = $1`, saved.ID).Scan(&commands); err != nil {
		t.Fatalf("count upgrade commands: %v", err)
	}
	if minted != 0 || commands != 0 {
		t.Fatalf("collectibles=%d upgrade commands=%d after a refused upgrade, want 0/0", minted, commands)
	}
}

// TestStarGiftDeleteRefreshesCollectionHashPostgres pins the collection side of
// payments.deleteStarGift: removing the membership must refresh the stored
// collection hash under the owner advisory lock, otherwise
// payments.getStarGiftCollections(hash) answers NotModified for a collection
// that actually changed.
func TestStarGiftDeleteRefreshesCollectionHashPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1885"+suffix+"01", "HashOwner", "")
	revisionID, giftID := ratingTestCatalogRevision(t, pool)
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	var keepID, deleteID int64
	for i, dst := range []*int64{&keepID, &deleteID} {
		if err := pool.QueryRow(ctx, `
INSERT INTO peer_star_gifts (owner_peer_type, owner_peer_id, msg_id, gift_id, gift_date, catalog_revision_id, lifecycle_status, converted)
VALUES ('user', $1, $2, $3, 0, $4, 'active', false) RETURNING id`, owner.ID, i+1, giftID, revisionID).Scan(dst); err != nil {
			t.Fatalf("seed saved gift: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_collection_items WHERE saved_gift_id = ANY($1::bigint[])`, []int64{keepID, deleteID})
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_collections WHERE owner_peer_type='user' AND owner_peer_id=$1`, owner.ID)
		_, _ = pool.Exec(cleanupCtx, `DELETE FROM peer_star_gifts WHERE id = ANY($1::bigint[])`, []int64{keepID, deleteID})
	})

	gifts := NewStarGiftStore(pool)
	if _, err := gifts.CreateCollection(ctx, ownerPeer, "Hash", []int64{keepID, deleteID}); err != nil {
		t.Fatalf("create collection: %v", err)
	}
	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	if _, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 2}, Date: now,
	}); err != nil {
		t.Fatalf("delete star gift: %v", err)
	}
	collections, err := gifts.ListCollections(ctx, ownerPeer)
	if err != nil || len(collections) != 1 {
		t.Fatalf("collections = %+v, %v", collections, err)
	}
	got := collections[0]
	if len(got.GiftIDs) != 1 || got.GiftIDs[0] != keepID {
		t.Fatalf("collection membership = %v, want [%d]", got.GiftIDs, keepID)
	}
	if want := domain.StarGiftCollectionHash(got.Title, got.GiftIDs); got.Hash != want {
		t.Fatalf("stale collection hash after delete: stored %d, membership %d", got.Hash, want)
	}
}

// TestStarGiftDeleteRefusesIrreversibleGiftsPostgres pins the eligibility rules
// of payments.deleteStarGift: an irreversible delete is limited to a plain gift
// that carries no Stars. Limited, auction, convertible and prepaid gifts stay
// active and keep their value.
func TestStarGiftDeleteRefusesIrreversibleGiftsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	owner := createTestUser(t, ctx, users, "+1886"+suffix+"01", "RefuseOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	lifecycle := NewStarGiftLifecycleStore(pool, newTestMessageStore(pool), 1_000_000)
	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	created := 0

	createRevision := func(t *testing.T, write domain.StarGiftCatalogWrite) domain.StarGiftCatalogEntry {
		t.Helper()
		created++
		write.Document = collectibleTestDocument(baseDocumentID+int64(created), "delete-refuse.tgs")
		write.Blob = collectibleTestBlob(baseDocumentID+int64(created), "delete-refuse")
		write.Animation = collectibleTestAnimation("delete-refuse.tgs")
		write.Actor = "integration"
		write.CommandID = write.CommandID + "-" + suffix
		entry, err := gifts.CreateCatalogRevision(ctx, write)
		if err != nil {
			t.Fatalf("create catalog revision: %v", err)
		}
		t.Cleanup(func() {
			cleanupCtx := context.Background()
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_catalog WHERE gift_id = $1`, entry.Gift.ID)
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM star_gift_catalog_revisions WHERE gift_id = $1`, entry.Gift.ID)
		})
		return entry
	}
	seed := func(t *testing.T, entry domain.StarGiftCatalogEntry, convertStars, prepaidStars int64) (int64, int) {
		t.Helper()
		msgID := 100 + created
		var savedID int64
		if err := pool.QueryRow(ctx, `
INSERT INTO peer_star_gifts (owner_peer_type, owner_peer_id, msg_id, gift_id, gift_date, catalog_revision_id,
                             lifecycle_status, converted, convert_stars, prepaid_upgrade_stars)
VALUES ('user', $1, $2, $3, 0, $4, 'active', false, $5, $6) RETURNING id`,
			owner.ID, msgID, entry.Gift.ID, entry.Gift.RevisionID, convertStars, prepaidStars).Scan(&savedID); err != nil {
			t.Fatalf("seed saved gift: %v", err)
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `DELETE FROM peer_star_gifts WHERE id = $1`, savedID)
		})
		return savedID, msgID
	}
	assertRefused := func(t *testing.T, savedID int64, msgID int) {
		t.Helper()
		if _, err := lifecycle.DeleteStarGift(ctx, domain.StarGiftDeleteRequest{
			UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: msgID}, Date: now,
		}); !errors.Is(err, domain.ErrStarGiftDeleteUnavailable) {
			t.Fatalf("delete err = %v, want ErrStarGiftDeleteUnavailable", err)
		}
		var status string
		if err := pool.QueryRow(ctx, `SELECT lifecycle_status FROM peer_star_gifts WHERE id = $1`, savedID).Scan(&status); err != nil {
			t.Fatalf("read lifecycle status: %v", err)
		}
		if status != string(domain.StarGiftLifecycleActive) {
			t.Fatalf("lifecycle status = %q, want %q", status, domain.StarGiftLifecycleActive)
		}
	}

	t.Run("limited", func(t *testing.T) {
		entry := createRevision(t, domain.StarGiftCatalogWrite{
			Title: "Limited " + suffix, Stars: 50, Enabled: true, Limited: true,
			AvailabilityTotal: 5, AvailabilityRemains: 5,
		})
		savedID, msgID := seed(t, entry, 0, 0)
		assertRefused(t, savedID, msgID)
	})
	t.Run("auction", func(t *testing.T) {
		entry := createRevision(t, domain.StarGiftCatalogWrite{
			Title: "Auction " + suffix, Stars: 100, Enabled: true, Limited: true, Auction: true,
			AvailabilityTotal: 5, AvailabilityRemains: 5, GiftsPerRound: 1,
			AuctionStartDate: now - 10, AuctionRoundDuration: 60, AuctionSlug: "delete-auction-" + suffix,
		})
		savedID, msgID := seed(t, entry, 0, 0)
		assertRefused(t, savedID, msgID)
	})
	t.Run("convertible", func(t *testing.T) {
		entry := createRevision(t, domain.StarGiftCatalogWrite{
			Title: "Convertible " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		})
		savedID, msgID := seed(t, entry, 20, 0)
		assertRefused(t, savedID, msgID)
	})
	t.Run("prepaid", func(t *testing.T) {
		entry := createRevision(t, domain.StarGiftCatalogWrite{
			Title: "Prepaid " + suffix, Stars: 50, Enabled: true,
		})
		savedID, msgID := seed(t, entry, 0, 100)
		assertRefused(t, savedID, msgID)
	})
}
