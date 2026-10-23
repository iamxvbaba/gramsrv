package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"telesrv/internal/admin"
	stargiftapp "telesrv/internal/app/stargifts"
	"telesrv/internal/domain"
	"telesrv/internal/officialgifts"
)

type inventoryOfficialSource struct{ bundle officialgifts.Bundle }

func (s inventoryOfficialSource) List(context.Context) ([]officialgifts.GiftSummary, error) {
	return nil, nil
}
func (s inventoryOfficialSource) Bundle(_ context.Context, _ int64, include bool) (officialgifts.Bundle, error) {
	bundle := s.bundle
	if !include {
		bundle.Collectible = nil
	}
	return bundle, nil
}

type inventoryBlob map[string][]byte

func (b inventoryBlob) Name() string { return "localfs" }
func (b inventoryBlob) Put(_ context.Context, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	key := hex.EncodeToString(sum[:])
	b[key] = append([]byte(nil), data...)
	return key, nil
}
func (b inventoryBlob) Get(_ context.Context, key string) ([]byte, error) { return b[key], nil }

func TestOfficialStarGiftInventoryPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	now := int(time.Now().Unix())
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	buyer := createTestUser(t, ctx, users, "+1872"+suffix+"01", "ReviewBuyer", "")
	owner := createTestUser(t, ctx, users, "+1872"+suffix+"02", "ReviewOwner", "")
	if _, _, err := NewStarsStore(pool).EnsureGrant(ctx, buyer.ID, 10000, now); err != nil {
		t.Fatal(err)
	}
	animation := []byte(`{"v":"5.7.4","fr":30,"ip":0,"op":60,"w":512,"h":512,"layers":[{"ty":4}],"assets":[]}`)
	sum := sha256.Sum256(animation)
	document := func(id int64) officialgifts.Document {
		return officialgifts.Document{ID: id, FileName: fmt.Sprintf("attribute-%d.json", id), Data: animation, SHA256: hex.EncodeToString(sum[:])}
	}
	permille := 500
	rarity := officialgifts.Rarity{Kind: "permille", Permille: &permille}
	source := inventoryOfficialSource{officialgifts.Bundle{
		Gift:       officialgifts.Gift{ID: 72, Title: "Basic unlimited", Stars: 50, ConvertStars: 25, DocumentID: 1},
		SourceJSON: []byte(`{"id":72,"limited":false}`), ManifestSHA256: sum[:],
		BaseDocument: officialgifts.Document{ID: 1, FileName: "gift.json", Data: animation, SHA256: hex.EncodeToString(sum[:])},
		Collectible: &officialgifts.CollectibleSet{
			Models: []officialgifts.Model{
				{Name: "One", DocumentID: 2, Rarity: rarity, Document: document(2)},
				{Name: "Two", DocumentID: 3, Rarity: rarity, Document: document(3)},
			},
			Patterns: []officialgifts.Pattern{
				{Name: "One", DocumentID: 4, Rarity: rarity, Document: document(4)},
				{Name: "Two", DocumentID: 5, Rarity: rarity, Document: document(5)},
			},
			Backdrops: []officialgifts.Backdrop{
				{Name: "One", BackdropID: 0, Rarity: rarity},
				{Name: "Two", BackdropID: 1, Rarity: rarity},
			},
		},
	}}
	giftService := stargiftapp.NewService(NewStarGiftStore(pool), inventoryBlob{}, 2)
	svc := admin.NewService(admin.Dependencies{Commands: NewAdminStore(pool), Gifts: giftService, OfficialGifts: source})
	lifecycle := NewStarGiftLifecycleStore(pool, NewMessageStore(pool), 10000)
	// Each local gift needs its own slug prefix: NFT slugs are
	// "<prefix>-<number>" with numbers counted per gift.
	slugPrefixes := map[int64]string{}
	slugPrefixFor := func(command string, giftID int64) string {
		if prefix, ok := slugPrefixes[giftID]; ok && giftID != 0 {
			return prefix
		}
		return "probe-" + command + "-" + suffix
	}
	importGift := func(command string, giftID int64, supply int, include bool, limited ...bool) int64 {
		t.Helper()
		prefix := slugPrefixFor(command, giftID)
		req := admin.ImportOfficialStarGiftRequest{
			CommandMeta:  admin.CommandMeta{CommandID: command + "-" + suffix, Actor: "review", Reason: "inventory regression"},
			SourceGiftID: "72", GiftID: giftID, Enabled: true, IncludeCollectible: include, SupplyTotal: supply,
			UpgradeStars: 100, SlugPrefix: prefix,
		}
		if len(limited) > 0 {
			req.Limited = limited[0]
		}
		result, err := svc.ImportOfficialStarGift(ctx, req)
		if err != nil {
			t.Fatalf("import: %v", err)
		}
		id, err := strconv.ParseInt(result.Details["gift_id"].(string), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := slugPrefixes[id]; !ok {
			slugPrefixes[id] = prefix
		}
		return id
	}
	purchase := func(giftID int64, command string) error {
		entry, err := catalogEntryByID(ctx, pool, giftID)
		if err != nil {
			return err
		}
		form, err := lifecycle.IssueStarGiftPurchaseForm(ctx, domain.StarGiftPurchaseForm{
			BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID},
			GiftID: giftID, RevisionID: entry.Gift.RevisionID, ChargeStars: 50, IssuedAt: now, ExpiresAt: now + 600,
		})
		if err != nil {
			return err
		}
		_, err = lifecycle.PurchaseStarGift(ctx, domain.StarGiftPurchaseRequest{
			BuyerUserID: buyer.ID, To: domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}, GiftID: giftID,
			RevisionID: entry.Gift.RevisionID, ChargeStars: 50, FormID: form.FormID, Date: now, CommandKey: command + "-" + suffix,
		})
		return err
	}
	t.Run("inactive collectible defaults do not cap purchases", func(t *testing.T) {
		// chooseOfficial uses max(upgrade_variants, 1) when availability_total is 0,
		// including when can_upgrade is false and the supply field is hidden.
		id := importGift("basic", 0, 1, false, false)
		if err := purchase(id, "basic-first"); err != nil {
			t.Fatal(err)
		}
		if err := purchase(id, "basic-second"); err != nil {
			t.Errorf("second purchase of unlimited non-collectible gift rejected: %v", err)
		}
	})
	t.Run("partial and exhausted stock survives repeated imports", func(t *testing.T) {
		id := importGift("limited", 0, 2, true, true)
		for i := 0; i < 2; i++ {
			if err := purchase(id, fmt.Sprintf("limited-%d", i)); err != nil {
				t.Fatal(err)
			}
			importGift(fmt.Sprintf("partial-revision-%d", i), id, 2, true, true)
			entry, err := catalogEntryByID(ctx, pool, id)
			if err != nil {
				t.Fatal(err)
			}
			if !entry.Gift.Limited || entry.Gift.AvailabilityRemains != 1-i || entry.Gift.FirstSaleDate != now || entry.Gift.LastSaleDate != now {
				t.Fatalf("revision changed sold stock or sale dates: %+v", entry.Gift)
			}
		}
		before, err := catalogEntryByID(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if before.Gift.AvailabilityRemains != 0 || !before.Gift.SoldOut {
			t.Fatalf("exhausted limited edition did not flip sold_out: limited=%v sold_out=%v remains=%d", before.Gift.Limited, before.Gift.SoldOut, before.Gift.AvailabilityRemains)
		}
		importGift("limited-revision", id, 2, true, true)
		after, err := catalogEntryByID(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		thirdErr := purchase(id, "limited-third")
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM peer_star_gifts WHERE gift_id=$1`, id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		t.Logf("before limited=%v total=%d remains=%d; after total=%d remains=%d; third purchase err=%v; total sold=%d", before.Gift.Limited, before.Gift.AvailabilityTotal, before.Gift.AvailabilityRemains, after.Gift.AvailabilityTotal, after.Gift.AvailabilityRemains, thirdErr, count)
		if !before.Gift.Limited || after.Gift.AvailabilityRemains != 0 || thirdErr == nil || count != 2 {
			t.Errorf("reimport restocked an exhausted edition and permitted total sold %d > supply %d", count, after.Gift.AvailabilityTotal)
		}
		// Omitting the pool and all supply fields must not remove an existing cap.
		importGift("limited-no-pool", id, 0, false, true)
		entry, err := catalogEntryByID(ctx, pool, id)
		if err != nil {
			t.Fatal(err)
		}
		if !entry.Gift.Limited || entry.Gift.AvailabilityTotal != 2 || entry.Gift.AvailabilityRemains != 0 {
			t.Fatalf("omitted supply removed cap: %+v", entry.Gift)
		}
		// A different cap is an explicit error, with neither active pointer moved.
		for _, supply := range []int{1, 3} {
			var beforeRevision, beforePool int64
			if err := pool.QueryRow(ctx, `SELECT active_revision_id,collectible_revision_id FROM star_gift_catalog WHERE gift_id=$1`, id).Scan(&beforeRevision, &beforePool); err != nil {
				t.Fatal(err)
			}
			_, err := svc.ImportOfficialStarGift(ctx, admin.ImportOfficialStarGiftRequest{
				CommandMeta:  admin.CommandMeta{CommandID: fmt.Sprintf("change-%d-%s", supply, suffix), Actor: "review", Reason: "supply bounds"},
				SourceGiftID: "72", GiftID: id, Enabled: true, IncludeCollectible: true, SupplyTotal: supply, UpgradeStars: 100, SlugPrefix: slugPrefixFor("", id), Limited: true,
			})
			if !errors.Is(err, domain.ErrStarGiftLifecycleInvalid) {
				t.Fatalf("changed supply=%d err=%v", supply, err)
			}
			var afterRevision, afterPool int64
			if err := pool.QueryRow(ctx, `SELECT active_revision_id,collectible_revision_id FROM star_gift_catalog WHERE gift_id=$1`, id).Scan(&afterRevision, &afterPool); err != nil {
				t.Fatal(err)
			}
			if beforeRevision != afterRevision || beforePool != afterPool {
				t.Fatal("rejected supply change moved an active pointer")
			}
		}
	})
	t.Run("purchase racing revision never restores stock", func(t *testing.T) {
		id := importGift("racing", 0, 20, true, true)
		for i := 0; i < 10; i++ {
			start := make(chan struct{})
			imported := make(chan error, 1)
			purchased := make(chan error, 1)
			go func() {
				<-start
				_, err := svc.ImportOfficialStarGift(ctx, admin.ImportOfficialStarGiftRequest{
					CommandMeta:  admin.CommandMeta{CommandID: fmt.Sprintf("race-import-%d-%s", i, suffix), Actor: "review", Reason: "concurrent inventory"},
					SourceGiftID: "72", GiftID: id, Enabled: true, IncludeCollectible: true, SupplyTotal: 20, UpgradeStars: 100, SlugPrefix: "race-" + suffix, Limited: true,
				})
				imported <- err
			}()
			go func() { <-start; purchased <- purchase(id, fmt.Sprintf("race-buy-%d", i)) }()
			close(start)
			if err := <-imported; err != nil {
				t.Fatal(err)
			}
			if err := <-purchased; err != nil && !errors.Is(err, domain.ErrStarGiftFormAmountMismatch) {
				t.Fatal(err)
			}
			entry, err := catalogEntryByID(ctx, pool, id)
			if err != nil {
				t.Fatal(err)
			}
			var sold int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM peer_star_gifts WHERE gift_id=$1`, id).Scan(&sold); err != nil {
				t.Fatal(err)
			}
			if entry.Gift.AvailabilityRemains+sold != 20 {
				t.Fatalf("remains=%d sold=%d total=20", entry.Gift.AvailabilityRemains, sold)
			}
		}
	})
}
