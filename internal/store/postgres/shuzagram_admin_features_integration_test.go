package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// adminFeatureGift creates an enabled catalog gift for the admin feature tests.
func adminFeatureGift(t *testing.T, ctx context.Context, gifts *StarGiftStore, base int64, suffix, name string, stars int64) domain.StarGiftCatalogEntry {
	t.Helper()
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: name + " " + suffix, Stars: stars, ConvertStars: stars / 2, Enabled: true,
		Document: collectibleTestDocument(base, name+".tgs"),
		Blob:     collectibleTestBlob(base, name), Animation: collectibleTestAnimation(name + ".tgs"),
		Actor: "integration", CommandID: name + "-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create %s catalog gift: %v", name, err)
	}
	return entry
}

func adminFeaturePool(giftID, base int64, slug, commandID string, publishAt int64) domain.StarGiftCollectibleWrite {
	return domain.StarGiftCollectibleWrite{
		GiftID: giftID, UpgradeStars: 100, SupplyTotal: 20, SlugPrefix: slug, PublishAt: publishAt,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Base", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+1, slug+"-model.tgs"), Blob: collectibleTestBlobPtr(base+1, slug+"-model"), Animation: collectibleTestAnimationPtr(slug + "-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Base Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestDocumentPtr(base+2, slug+"-model-two.tgs"), Blob: collectibleTestBlobPtr(base+2, slug+"-model-two"), Animation: collectibleTestAnimationPtr(slug + "-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+3, slug+"-pattern.tgs"), Blob: collectibleTestBlobPtr(base+3, slug+"-pattern"), Animation: collectibleTestAnimationPtr(slug + "-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
				Document: collectibleTestPatternDocumentPtr(base+4, slug+"-pattern-two.tgs"), Blob: collectibleTestBlobPtr(base+4, slug+"-pattern-two"), Animation: collectibleTestAnimationPtr(slug + "-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 77, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 78, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: commandID,
	}
}

func TestScheduledCollectiblePoolPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7fffffffffff0000
	entry := adminFeatureGift(t, ctx, gifts, base, suffix, "scheduled", 500)
	giftID := entry.Gift.ID

	future := time.Now().Add(time.Hour).Unix()
	draft, err := gifts.PublishCollectibleRevision(ctx, adminFeaturePool(giftID, base, "sched-a-"+suffix, "sched-a-"+suffix, future))
	if err != nil {
		t.Fatalf("schedule pool: %v", err)
	}
	if draft.Published || draft.ScheduledPublishAt != future {
		t.Fatalf("scheduled draft = published:%v at:%d, want draft at %d", draft.Published, draft.ScheduledPublishAt, future)
	}
	if _, found, err := gifts.ActiveCollectibleProjection(ctx, giftID, 0); err != nil || found {
		t.Fatalf("scheduled pool is already live: found=%v err=%v", found, err)
	}
	pending, found, err := gifts.PendingCollectibleRevision(ctx, giftID)
	if err != nil || !found || pending.ID != draft.ID || len(pending.Models) != 2 {
		t.Fatalf("pending = %+v found=%v err=%v", pending, found, err)
	}
	availability, err := gifts.PendingCollectibleAvailability(ctx, []int64{giftID})
	if err != nil || availability[giftID].SupplyTotal != 20 {
		t.Fatalf("pending availability = %+v err=%v", availability, err)
	}
	// Not due yet: the dispatcher must leave it alone.
	due, err := gifts.ActivateDueCollectibleRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range due {
		if id == giftID {
			t.Fatal("future pool activated early")
		}
	}

	released, err := gifts.ReleaseCollectiblePoolNow(ctx, giftID)
	if err != nil || released.ID != draft.ID || !released.Published {
		t.Fatalf("release now = %+v err=%v", released, err)
	}
	if _, found, _ := gifts.PendingCollectibleRevision(ctx, giftID); found {
		t.Fatal("released pool still pending")
	}
	if active, found, err := gifts.ActiveCollectibleProjection(ctx, giftID, 0); err != nil || !found || active.ID != draft.ID {
		t.Fatalf("active after release = %+v found=%v err=%v", active, found, err)
	}
	if _, err := gifts.ReleaseCollectiblePoolNow(ctx, giftID); !errors.Is(err, domain.ErrStarGiftCollectibleUnavailable) {
		t.Fatalf("second release err = %v", err)
	}

	// A second scheduled pool whose time has come is opened by the dispatcher.
	next, err := gifts.PublishCollectibleRevision(ctx, adminFeaturePool(giftID, base+100, "sched-b-"+suffix, "sched-b-"+suffix, future))
	if err != nil || next.Published {
		t.Fatalf("schedule second pool = %+v err=%v", next, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_collectible_revisions SET scheduled_publish_at=now()-interval '1 minute' WHERE id=$1`, next.ID); err != nil {
		t.Fatal(err)
	}
	due, err = gifts.ActivateDueCollectibleRevisions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activated := false
	for _, id := range due {
		activated = activated || id == giftID
	}
	if !activated {
		t.Fatalf("due pool not activated: %v", due)
	}
	if active, found, err := gifts.ActiveCollectibleProjection(ctx, giftID, 0); err != nil || !found || active.ID != next.ID {
		t.Fatalf("active after dispatch = %+v found=%v err=%v", active, found, err)
	}
	// A past PublishAt opens the pool immediately.
	immediate, err := gifts.PublishCollectibleRevision(ctx, adminFeaturePool(giftID, base+200, "sched-c-"+suffix, "sched-c-"+suffix, time.Now().Add(-time.Hour).Unix()))
	if err != nil || !immediate.Published {
		t.Fatalf("past publish_at pool = %+v err=%v", immediate, err)
	}
}

func TestStarGiftQuickSettingsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	buyer := createTestUser(t, ctx, users, "+1997"+suffix+"01", "QuickBuyer", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM stars_transactions WHERE user_id=$1`, buyer.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM stars_balances WHERE user_id=$1`, buyer.ID)
	})
	if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, buyer.ID, 100000, now); err != nil {
		t.Fatalf("grant: %v", err)
	}
	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7fffffffffff0000
	entry := adminFeatureGift(t, ctx, gifts, base, suffix, "quick", 10)
	giftID := entry.Gift.ID

	// Supply.
	if changed, err := gifts.SetCatalogSupply(ctx, giftID, true, 100, 40); err != nil || !changed {
		t.Fatalf("set supply = %v, %v", changed, err)
	}
	var limited, soldOut bool
	var total, remains int
	if err := pool.QueryRow(ctx, `SELECT r.limited, r.availability_total, r.sold_out, c.availability_remains
FROM star_gift_catalog c JOIN star_gift_catalog_revisions r ON r.id=c.active_revision_id WHERE c.gift_id=$1`, giftID).
		Scan(&limited, &total, &soldOut, &remains); err != nil {
		t.Fatal(err)
	}
	if !limited || total != 100 || remains != 60 || soldOut {
		t.Fatalf("supply = limited:%v total:%d remains:%d soldOut:%v", limited, total, remains, soldOut)
	}
	if _, err := gifts.SetCatalogSupply(ctx, giftID, true, 10, 11); !errors.Is(err, domain.ErrStarGiftInvalid) {
		t.Fatalf("issued > total err = %v", err)
	}
	if _, err := gifts.SetCatalogSupply(ctx, giftID, false, 0, 0); err != nil {
		t.Fatalf("clear supply: %v", err)
	}

	// Drop protection: 2 purchases per hour, then the third is refused.
	if _, err := gifts.SetDropProtection(ctx, giftID, false, 0, 2, 0); !errors.Is(err, domain.ErrStarGiftInvalid) {
		t.Fatalf("half rate limit err = %v", err)
	}
	if changed, err := gifts.SetDropProtection(ctx, giftID, false, 0, 2, 3600); err != nil || !changed {
		t.Fatalf("set protection = %v, %v", changed, err)
	}
	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 1_000_000)
	buy := func(n int) error {
		form := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
			BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
			GiftID: giftID, CommandKey: "quick-buy-" + suffix + "-" + string(rune('a'+n)), Date: now + n,
		})
		_, err := lifecycle.PurchaseStarGift(ctx, form)
		return err
	}
	for i := 0; i < 2; i++ {
		if err := buy(i); err != nil {
			t.Fatalf("purchase %d: %v", i, err)
		}
	}
	if err := buy(2); !errors.Is(err, domain.ErrStarGiftUnavailable) {
		t.Fatalf("rate-limited purchase err = %v", err)
	}
	// Turning the limit off lets the buyer through again.
	if _, err := gifts.SetDropProtection(ctx, giftID, false, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := buy(3); err != nil {
		t.Fatalf("purchase after limit off: %v", err)
	}

	// A pinned copy of another gift, to check the owner's pins are compacted.
	other := adminFeatureGift(t, ctx, gifts, base+50, suffix, "quick-other", 10)
	form := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		GiftID: other.Gift.ID, CommandKey: "quick-other-" + suffix, Date: now + 10,
	})
	otherBought, err := lifecycle.PurchaseStarGift(ctx, form)
	if err != nil {
		t.Fatal(err)
	}
	var deletedPinID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM peer_star_gifts WHERE gift_id=$1 AND owner_peer_id=$2 ORDER BY id LIMIT 1`, giftID, buyer.ID).Scan(&deletedPinID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=1 WHERE id=$1`, deletedPinID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE peer_star_gifts SET pinned_order=2 WHERE id=$1`, otherBought.Saved.ID); err != nil {
		t.Fatal(err)
	}

	// Delete retires every ordinary copy. A purchase form issued before the
	// deletion can no longer be paid afterwards.
	pendingForm := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		GiftID: giftID, CommandKey: "quick-pending-" + suffix, Date: now + 20,
	})
	preview, err := gifts.PreviewStarGiftDelete(ctx, giftID)
	if err != nil || len(preview.Blockers) != 0 || preview.Owners != 3 {
		t.Fatalf("preview = %+v err=%v", preview, err)
	}
	result, err := gifts.DeleteStarGift(ctx, giftID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if result.Owners != 3 || len(result.Refunds) != 1 || result.Refunds[0].UserID != buyer.ID || result.Refunds[0].Stars != 3*5 {
		t.Fatalf("delete result = %+v", result)
	}
	var active, enabled int
	var deletedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE lifecycle_status='active') FROM peer_star_gifts WHERE gift_id=$1`, giftID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active copies after delete = %d, %v", active, err)
	}
	if err := pool.QueryRow(ctx, `SELECT enabled::int, deleted_at FROM star_gift_catalog WHERE gift_id=$1`, giftID).Scan(&enabled, &deletedAt); err != nil || enabled != 0 || deletedAt == nil {
		t.Fatalf("catalog after delete = enabled:%d deleted:%v err:%v", enabled, deletedAt, err)
	}
	var otherPin int
	if err := pool.QueryRow(ctx, `SELECT pinned_order FROM peer_star_gifts WHERE id=$1`, otherBought.Saved.ID).Scan(&otherPin); err != nil || otherPin != 1 {
		t.Fatalf("remaining pin = %d, %v; want 1", otherPin, err)
	}
	if _, err := gifts.DeleteStarGift(ctx, giftID); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("second delete err = %v", err)
	}
	if _, err := lifecycle.PurchaseStarGift(ctx, pendingForm); err == nil {
		t.Fatal("purchase of a deleted gift succeeded")
	}
	if _, err := gifts.SetCatalogEnabled(ctx, giftID, true); err == nil {
		t.Fatal("deleted gift was re-enabled")
	}
}

func TestAutoSubscribeStorePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	owner := createTestUser(t, ctx, NewUserStore(pool), "+1998"+suffix+"01", "AutoSubOwner", "")
	created, err := NewChannelStore(pool).CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID, Title: "Auto " + suffix, Broadcast: true, Date: 1_700_000_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	store := NewAutoSubscribeStore(pool)
	t.Cleanup(func() { _, _ = store.RemoveAutoSubscribeChannel(context.Background(), channelID) })

	if added, err := store.AddAutoSubscribeChannel(ctx, channelID, "tester"); err != nil || !added {
		t.Fatalf("add = %v, %v", added, err)
	}
	if added, err := store.AddAutoSubscribeChannel(ctx, channelID, "tester"); err != nil || added {
		t.Fatalf("second add = %v, %v", added, err)
	}
	list, err := store.ListAutoSubscribeChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range list {
		found = found || (row.ChannelID == channelID && row.Title == "Auto "+suffix && row.AddedBy == "tester")
	}
	if !found {
		t.Fatalf("channel missing from list: %+v", list)
	}
	ids, err := store.EligibleAutoSubscribeUserIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		for _, system := range domain.SystemUserIDs() {
			if id == system {
				t.Fatalf("system user %d is eligible", id)
			}
		}
	}
	if removed, err := store.RemoveAutoSubscribeChannel(ctx, channelID); err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if removed, err := store.RemoveAutoSubscribeChannel(ctx, channelID); err != nil || removed {
		t.Fatalf("second remove = %v, %v", removed, err)
	}
}

// A limited gift sells out; once its first NFT copy is listed, the catalog
// carries availability_resale, which is what turns the client's "sold out"
// tile into a marketplace tile. NFT slugs keep the title's case.
func TestSoldOutGiftResaleAndTitleSlugPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	buyer := createTestUser(t, ctx, NewUserStore(pool), "+1999"+suffix+"01", "ResaleBuyer", "")
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM stars_transactions WHERE user_id=$1`, buyer.ID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM stars_balances WHERE user_id=$1`, buyer.ID)
	})
	if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, buyer.ID, 100000, now); err != nil {
		t.Fatal(err)
	}
	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7fffffffffff0000
	entry := adminFeatureGift(t, ctx, gifts, base, suffix, "resale", 100)
	giftID := entry.Gift.ID
	if _, err := gifts.SetCatalogSupply(ctx, giftID, true, 1, 0); err != nil {
		t.Fatal(err)
	}
	prefix := "PlushPepe" + suffix
	if _, err := gifts.PublishCollectibleRevision(ctx, adminFeaturePool(giftID, base, prefix, "resale-pool-"+suffix, 0)); err != nil {
		t.Fatalf("publish mixed-case pool: %v", err)
	}
	// Another gift may not reuse the prefix, in any case.
	other := adminFeatureGift(t, ctx, gifts, base+50, suffix, "resale-other", 100)
	if _, err := gifts.PublishCollectibleRevision(ctx, adminFeaturePool(other.Gift.ID, base+60, strings.ToLower(prefix), "resale-other-"+suffix, 0)); !errors.Is(err, domain.ErrStarGiftCollectibleInvalid) {
		t.Fatalf("shared prefix err = %v", err)
	}

	messages := NewMessageStore(pool)
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))
	form := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID},
		GiftID: giftID, IncludeUpgrade: true, CommandKey: "resale-buy-" + suffix, Date: now,
	})
	bought, err := lifecycle.PurchaseStarGift(ctx, form)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	gift, found, err := gifts.CatalogGift(ctx, giftID)
	if err != nil || !found || !gift.SoldOut || gift.AvailabilityResale != 0 {
		t.Fatalf("after sell-out = %+v found=%v err=%v", gift, found, err)
	}
	owner := domain.Peer{Type: domain.PeerTypeUser, ID: buyer.ID}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: buyer.ID, Ref: domain.SavedStarGiftRef{Owner: owner, MsgID: bought.Saved.MsgID},
		RequirePrepaid: true, KeepOriginalDetails: true, CommandKey: "resale-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	unique, found, err := gifts.UniqueBySlug(ctx, strings.ToLower(prefix)+"-1")
	if err != nil || !found || unique.Slug != prefix+"-1" {
		t.Fatalf("slug lookup = %q found=%v err=%v, want %q", unique.Slug, found, err, prefix+"-1")
	}
	if _, err := lifecycle.SetStarGiftListing(ctx, domain.StarGiftListingRequest{
		ActorUserID: buyer.ID, Ref: domain.SavedStarGiftRef{Owner: owner, MsgID: upgraded.Saved.MsgID},
		Amount: &domain.StarGiftAmount{Currency: domain.StarGiftCurrencyStars, Amount: 500}, Date: now + 2,
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	gift, found, err = gifts.CatalogGift(ctx, giftID)
	if err != nil || !found || !gift.SoldOut || gift.AvailabilityResale != 1 || gift.ResellMinStars != 500 {
		t.Fatalf("after first listing = soldOut:%v resale:%d min:%d found=%v err=%v", gift.SoldOut, gift.AvailabilityResale, gift.ResellMinStars, found, err)
	}
}

// Sort order 0 means "not chosen": a new gift goes to the end of the list and
// a replaced revision keeps its place.
func TestStarGiftAutomaticSortOrderPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	gifts := NewStarGiftStore(pool)
	base := time.Now().UnixNano() & 0x7fffffffffff0000
	var maxBefore int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(MAX(sort_order),0) FROM star_gift_catalog WHERE deleted_at IS NULL`).Scan(&maxBefore); err != nil {
		t.Fatal(err)
	}
	first := adminFeatureGift(t, ctx, gifts, base, suffix, "order-a", 10)
	second := adminFeatureGift(t, ctx, gifts, base+10, suffix, "order-b", 10)
	sortOf := func(id int64) int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT sort_order FROM star_gift_catalog WHERE gift_id=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if a, b := sortOf(first.Gift.ID), sortOf(second.Gift.ID); a != maxBefore+1 || b != maxBefore+2 {
		t.Fatalf("auto sort orders = %d, %d; want %d, %d", a, b, maxBefore+1, maxBefore+2)
	}
	if _, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		GiftID: first.Gift.ID, Title: "order-a replaced " + suffix, Stars: 10, ConvertStars: 5, Enabled: true,
		Document: collectibleTestDocument(base+20, "order-a2.tgs"),
		Blob:     collectibleTestBlob(base+20, "order-a2"), Animation: collectibleTestAnimation("order-a2.tgs"),
		Actor: "integration", CommandID: "order-a2-" + suffix,
	}); err != nil {
		t.Fatalf("replace revision: %v", err)
	}
	if got := sortOf(first.Gift.ID); got != maxBefore+1 {
		t.Fatalf("replaced gift moved to %d, want %d", got, maxBefore+1)
	}
}
