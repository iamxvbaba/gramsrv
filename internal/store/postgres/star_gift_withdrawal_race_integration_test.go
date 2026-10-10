package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestStarGiftWithdrawalConcurrentRecordCollapsesPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1779"+suffix+"61", "WithdrawalSender", "")
	owner := createTestUser(t, ctx, users, "+1779"+suffix+"62", "WithdrawalOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Withdrawal " + suffix, Stars: 50, ConvertStars: 25, Enabled: true,
		Document:  collectibleTestDocument(baseDocumentID, "gift.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "gift"),
		Animation: collectibleTestAnimation("gift.tgs"),
		Actor:     "integration", CommandID: "catalog-withdrawal-" + suffix,
	})
	if err != nil {
		t.Fatalf("create withdrawal catalog: %v", err)
	}
	if _, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "wdraw-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Base",
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
			Document:           collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"),
			Blob:               collectibleTestBlobPtr(baseDocumentID+1, "model"),
			Animation:          collectibleTestAnimationPtr("model.tgs"),
			OfficialDocumentID: 5100000000000000011,
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
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Night", BackdropID: 91,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1000,
		}, {
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Day", BackdropID: 92,
			CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111,
			RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1,
		}},
		Actor: "integration", CommandID: "collectibles-withdrawal-" + suffix,
		OfficialGiftID: 5170145012310081616, SourceManifestSHA256: make([]byte, 32),
	}); err != nil {
		t.Fatalf("publish withdrawal collectible pool: %v", err)
	}

	messages := NewMessageStore(pool)
	saved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001000, ConvertStars: 25,
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700001001); err != nil {
		t.Fatalf("grant withdrawal stars: %v", err)
	}
	upgraded, err := NewStarGiftUpgradeStore(pool, messages).UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID},
		KeepOriginalDetails: true, ChargeStars: 100, FormID: 993,
		CommandKey: "withdrawal-" + suffix, Date: 1700001002,
	})
	if err != nil || upgraded.Saved.UniqueGiftID == 0 {
		t.Fatalf("upgrade for withdrawal = %+v err %v", upgraded, err)
	}

	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
	req := domain.StarGiftWithdrawalRequest{
		UserID: owner.ID,
		Ref:    domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID},
		Date:   1700001100,
	}
	recorded, err := lifecycle.RecordStarGiftWithdrawal(ctx, req, "local", "withdraw-"+suffix,
		"https://telesrv.invalid/gift-withdrawal/"+suffix, 1700001700)
	if err != nil || recorded.Status != "pending" || recorded.URL == "" {
		t.Fatalf("record withdrawal = %+v err %v", recorded, err)
	}

	type raceOutcome struct {
		out domain.StarGiftWithdrawal
		err error
	}
	outcomes := make([]raceOutcome, 10)
	var wg sync.WaitGroup
	for i := range outcomes {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			out, err := lifecycle.RecordStarGiftWithdrawal(ctx, req, "local",
				fmt.Sprintf("withdraw-race-%d-%s", index, suffix),
				"https://telesrv.invalid/gift-withdrawal/race/"+fmt.Sprint(index), 1700001700)
			outcomes[index] = raceOutcome{out: out, err: err}
		}(i)
	}
	wg.Wait()
	for index, outcome := range outcomes {
		if outcome.err != nil || outcome.out.ProviderRequestID != recorded.ProviderRequestID ||
			outcome.out.URL != recorded.URL || outcome.out.Status != "pending" {
			t.Fatalf("concurrent withdrawal %d = %+v err %v, want replay of %s",
				index, outcome.out, outcome.err, recorded.ProviderRequestID)
		}
	}

	var rows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_withdrawal_requests WHERE unique_gift_id=$1`,
		upgraded.Saved.UniqueGiftID).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("withdrawal rows = %d err %v, want a single row", rows, err)
	}
	resolved, found, err := lifecycle.ResolveStarGiftWithdrawal(ctx, recorded.ProviderRequestID)
	if err != nil || !found || resolved.URL != recorded.URL {
		t.Fatalf("resolve withdrawal = %+v found %v err %v", resolved, found, err)
	}
	completed, err := lifecycle.CompleteStarGiftWithdrawal(ctx, recorded.ProviderRequestID, 1700001150)
	if err != nil || completed.Status != "completed" {
		t.Fatalf("complete withdrawal = %+v err %v", completed, err)
	}
}
