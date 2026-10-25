package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftRoundTripTransferNotReplayedPostgres pins the round-trip
// transfer: A sends a collectible to B, B sends it back, A sends it to B again.
//
// The third leg used to be swallowed. The transfer idempotency key is derived
// from (actor, gift, recipient) plus the payment form id, and the form id is
// derived from (user, gift, owner, recipient, stars, can_transfer_at) — but
// can_transfer_at is never re-armed after a transfer, so every leg produces the
// same key. The private message random id comes from that key, so the send was
// classified as a duplicate of the first card: the RPC returned success
// ("gift transferred") and no message and no ownership change happened.
//
// Retries inside one ownership epoch must stay idempotent, so the key now
// carries the epoch (peer_star_gifts.gift_date, rewritten on every move).
func TestStarGiftRoundTripTransferNotReplayedPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	alice := createTestUser(t, ctx, users, "+1885"+suffix+"01", "RoundTripAlice", "")
	bob := createTestUser(t, ctx, users, "+1885"+suffix+"02", "RoundTripBob", "")
	alicePeer := domain.Peer{Type: domain.PeerTypeUser, ID: alice.ID}
	bobPeer := domain.Peer{Type: domain.PeerTypeUser, ID: bob.ID}

	gifts := NewStarGiftStore(pool)
	messages := newTestMessageStore(pool)
	base := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "RoundTrip " + suffix, Stars: 50, ConvertStars: 20, Enabled: true,
		Document: collectibleTestDocument(base, "round-trip.tgs"),
		Blob:     collectibleTestBlob(base, "round-trip"), Animation: collectibleTestAnimation("round-trip.tgs"),
		Actor: "integration", CommandID: "round-trip-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 5, SlugPrefix: "rt-" + suffix,
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
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 71, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 72, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000},
		},
		Actor: "integration", CommandID: "round-trip-pool-" + suffix,
	}); err != nil {
		t.Fatalf("pool: %v", err)
	}
	stars := NewStarsStore(pool)
	for _, user := range []domain.User{alice, bob} {
		if _, _, err := stars.EnsureGrant(ctx, user.ID, 100000, now); err != nil {
			t.Fatalf("grant: %v", err)
		}
	}
	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000, WithStarGiftMarketPolicy(domain.StarGiftMarketPolicy{
		StarsProceedsPermille: 900, TONProceedsPermille: 900,
	}))
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 500,
	}))
	purchase := issueLifecyclePurchaseForm(t, ctx, lifecycle, domain.StarGiftPurchaseRequest{
		BuyerUserID: alice.ID, To: alicePeer, GiftID: entry.Gift.ID, IncludeUpgrade: true,
		CommandKey: "round-trip-purchase-" + suffix, Date: now,
	})
	bought, err := purchaseStarGiftForTest(ctx, lifecycle, purchase)
	if err != nil {
		t.Fatalf("purchase: %v", err)
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: alice.ID, Ref: domain.SavedStarGiftRef{Owner: alicePeer, MsgID: bought.Saved.MsgID},
		RequirePrepaid: true, CommandKey: "round-trip-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	giftID := upgraded.Unique.ID
	if upgraded.Unique.Owner != alicePeer || upgraded.Saved.TransferStars != 25 {
		t.Fatalf("upgraded collectible = %+v / %+v, want owned by %+v with 25 XTR",
			upgraded.Unique.Owner, upgraded.Saved.TransferStars, alicePeer)
	}
	aliceBefore, err := stars.GetBalance(ctx, alice.ID)
	if err != nil {
		t.Fatalf("alice balance before: %v", err)
	}

	// The command key and form id the RPC builds are the same for every leg of
	// the round trip: the form id embeds can_transfer_at, which is zeroed by each
	// move and never re-armed, and the key embeds only gift id and form id.
	const formID = 987654
	commandKey := "paid-transfer:" + itoa(giftID) + ":" + itoa(formID)

	toBob, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{
		ActorUserID: alice.ID, Ref: domain.SavedStarGiftRef{Owner: alicePeer, MsgID: upgraded.Saved.MsgID},
		To: bobPeer, ChargeStars: 25, FormID: formID, CommandKey: commandKey, Date: now + 2,
	})
	if err != nil || toBob.Duplicate || toBob.Unique.Owner != bobPeer {
		t.Fatalf("alice -> bob = %+v err %v, want a fresh transfer to %+v", toBob.Unique.Owner, err, bobPeer)
	}

	// A retry of that very same leg must replay instead of paying twice: the gift
	// has already left Alice, so the key resolves to the recorded command and no
	// second transfer happens. The balance assertion at the end proves no double
	// charge.
	retry, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{
		ActorUserID: alice.ID, Ref: domain.SavedStarGiftRef{Owner: alicePeer, MsgID: upgraded.Saved.MsgID},
		To: bobPeer, ChargeStars: 25, FormID: formID, CommandKey: commandKey, Date: now + 2,
	})
	if err != nil {
		t.Fatalf("alice -> bob retry: %v", err)
	}
	if !retry.Duplicate || retry.Unique.Owner != bobPeer {
		t.Fatalf("retry of a finished leg = %+v duplicate %v, want a replay of the recorded transfer",
			retry.Unique.Owner, retry.Duplicate)
	}

	back, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{
		ActorUserID: bob.ID, Ref: domain.SavedStarGiftRef{Owner: bobPeer, MsgID: toBob.Saved.MsgID},
		To: alicePeer, ChargeStars: 25, FormID: formID, CommandKey: commandKey, Date: now + 2,
	})
	if err != nil || back.Duplicate || back.Unique.Owner != alicePeer {
		t.Fatalf("bob -> alice = %+v err %v, want a fresh transfer to %+v", back.Unique.Owner, err, alicePeer)
	}

	again, err := lifecycle.TransferStarGift(ctx, domain.StarGiftTransferRequest{
		ActorUserID: alice.ID, Ref: domain.SavedStarGiftRef{Owner: alicePeer, MsgID: back.Saved.MsgID},
		To: bobPeer, ChargeStars: 25, FormID: formID, CommandKey: commandKey, Date: now + 2,
	})
	if err != nil {
		t.Fatalf("alice -> bob again: %v", err)
	}
	if again.Duplicate || again.Unique.Owner != bobPeer {
		t.Fatalf("second alice -> bob = %+v duplicate %v, want a fresh transfer to %+v",
			again.Unique.Owner, again.Duplicate, bobPeer)
	}

	// Ownership really moved, and the recipient has a service message for it.
	owner, found, err := gifts.UniqueByID(ctx, giftID)
	if err != nil || !found || owner.Owner != bobPeer {
		t.Fatalf("collectible owner = %+v found %v err %v, want %+v", owner.Owner, found, err, bobPeer)
	}
	if again.Saved.MsgID <= 0 {
		t.Fatalf("recipient message id = %d, want a delivered card", again.Saved.MsgID)
	}
	var cards int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM message_boxes
WHERE owner_user_id=$1 AND box_id=$2 AND NOT deleted`, bob.ID, again.Saved.MsgID).Scan(&cards); err != nil || cards != 1 {
		t.Fatalf("recipient cards = %d err %v, want exactly one", cards, err)
	}
	// Alice's balance must be charged twice: the first leg and the round trip.
	// The retry in between must not add a third charge.
	if balance, err := stars.GetBalance(ctx, alice.ID); err != nil ||
		balance.Balance != aliceBefore.Balance-2*25 {
		t.Fatalf("alice balance = %+v err %v, want %d", balance, err, aliceBefore.Balance-2*25)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	var buf [24]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
