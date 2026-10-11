package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// The panel's "put this gift on a TON wallet" command reaches this store with a
// slug instead of an id, so the test binds by ref, checks the ledger row keeps
// the previous peer owner for a later release, and then releases.
func TestStarGiftWalletBindByRefPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1781"+suffix+"71", "WalletSender", "")
	owner := createTestUser(t, ctx, users, "+1781"+suffix+"72", "WalletOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Wallet " + suffix, Stars: 50, ConvertStars: 25, Enabled: true,
		Document:  collectibleTestDocument(baseDocumentID, "gift.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "gift"),
		Animation: collectibleTestAnimation("gift.tgs"),
		Actor:     "integration", CommandID: "catalog-wallet-" + suffix,
	})
	if err != nil {
		t.Fatalf("create wallet catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "wal-" + suffix,
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
		Actor: "integration", CommandID: "collectibles-wallet-" + suffix,
		OfficialGiftID: 5170145012310081617, SourceManifestSHA256: make([]byte, 32),
	}); err != nil {
		t.Fatalf("publish wallet collectible pool: %v", err)
	}

	messages := NewMessageStore(pool)
	uniqueSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001100, ConvertStars: 25,
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700001101); err != nil {
		t.Fatalf("grant wallet stars: %v", err)
	}
	upgraded, err := NewStarGiftUpgradeStore(pool, messages).UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: uniqueSaved.MsgID},
		KeepOriginalDetails: true, ChargeStars: 100, FormID: 995,
		CommandKey: "wallet-" + suffix, Date: 1700001102,
	})
	if err != nil || upgraded.Saved.UniqueGiftID == 0 {
		t.Fatalf("upgrade for wallet = %+v err %v", upgraded, err)
	}
	uniqueGift, found, err := gifts.UniqueByID(ctx, upgraded.Saved.UniqueGiftID)
	if err != nil || !found {
		t.Fatalf("unique gift = found %v err %v", found, err)
	}
	if uniqueGift.Slug == "" {
		t.Fatalf("unique gift has no slug: %+v", uniqueGift)
	}

	claims := NewStarGiftClaimStore(pool)
	wallet := "0:" + strings.Repeat("cd", 32)
	bound, err := claims.WalletBindGift(ctx, domain.StarGiftWalletBind{
		Ref: uniqueGift.Slug, WalletName: "Alice", WalletAddress: wallet,
		ActorUserID: sender.ID, HostPeerType: "user", HostPeerID: owner.ID,
	})
	if err != nil {
		t.Fatalf("bind by ref: %v", err)
	}
	if bound.OwnerAddress != wallet || bound.OwnerName != "Alice" {
		t.Fatalf("bound = %+v, want wallet %q", bound, wallet)
	}
	if bound.Owner.ID != 0 {
		t.Fatalf("bound owner peer = %+v, want none once the wallet owns it", bound.Owner)
	}
	if bound.Host.ID != owner.ID {
		t.Fatalf("bound host = %+v, want user %d", bound.Host, owner.ID)
	}

	var previousOwnerType, previousHostType string
	var previousOwnerID, previousHostID, actorID int64
	var action, ledgerWallet string
	if err := pool.QueryRow(ctx, `SELECT action, wallet_address,
       previous_owner_peer_type, previous_owner_peer_id,
       previous_host_peer_type, previous_host_peer_id, actor_user_id
FROM wallet_gift_transfers WHERE unique_gift_id=$1 ORDER BY id DESC LIMIT 1`,
		upgraded.Saved.UniqueGiftID).Scan(&action, &ledgerWallet,
		&previousOwnerType, &previousOwnerID, &previousHostType, &previousHostID,
		&actorID); err != nil {
		t.Fatalf("read bind ledger row: %v", err)
	}
	if action != "bind" || ledgerWallet != wallet || previousOwnerType != "user" ||
		previousOwnerID != owner.ID || actorID != sender.ID {
		t.Fatalf("ledger row = action %q wallet %q owner %s/%d actor %d",
			action, ledgerWallet, previousOwnerType, previousOwnerID, actorID)
	}
	_ = previousHostType
	_ = previousHostID

	if _, err := claims.WalletBindGift(ctx, domain.StarGiftWalletBind{
		Ref: "no-such-gift-" + suffix, WalletName: "Alice", WalletAddress: wallet, ActorUserID: sender.ID,
	}); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("unknown ref err = %v, want not found", err)
	}

	released, err := claims.WalletReleaseGift(ctx, upgraded.Saved.UniqueGiftID, sender.ID)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.OwnerAddress != "" || released.OwnerName != "" {
		t.Fatalf("released = %+v, want the wallet cleared", released)
	}
	if released.Owner != ownerPeer {
		t.Fatalf("released owner = %+v, want %v", released.Owner, ownerPeer)
	}

	defaultHost, err := claims.WalletBindGift(ctx, domain.StarGiftWalletBind{
		Ref: uniqueGift.Slug, WalletName: "Bob", WalletAddress: wallet, ActorUserID: sender.ID,
	})
	if err != nil {
		t.Fatalf("re-bind without host: %v", err)
	}
	if defaultHost.Host.ID != domain.GiftRelayerUserID {
		t.Fatalf("default host = %+v, want the relayer profile", defaultHost.Host)
	}
}
