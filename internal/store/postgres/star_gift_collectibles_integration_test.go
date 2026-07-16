package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

func TestStarGiftCollectibleUpgradeAggregatePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1778"+suffix+"41", "CollectibleSender", "")
	owner := createTestUser(t, ctx, users, "+1778"+suffix+"42", "CollectibleOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Comet", Stars: 50, ConvertStars: 25, Enabled: true,
		Document:  collectibleTestDocument(baseDocumentID, "gift.tgs"),
		Blob:      collectibleTestBlob(baseDocumentID, "gift"),
		Animation: collectibleTestAnimation("gift.tgs"),
		Actor:     "integration", CommandID: "catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create collectible catalog gift: %v", err)
	}
	poolRevision, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "comet-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Aurora", RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs"),
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+2, "pattern.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+2, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs"),
		}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Midnight", BackdropID: 1,
			CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff,
			RarityPermille: 1000,
		}},
		Actor: "integration", CommandID: "collectibles-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}
	if !poolRevision.Published || poolRevision.Issued != 0 || len(poolRevision.Models) != 1 || len(poolRevision.Patterns) != 1 || len(poolRevision.Backdrops) != 1 {
		t.Fatalf("published pool = %+v", poolRevision)
	}
	availability, err := gifts.CollectibleAvailability(ctx, []int64{entry.Gift.ID, entry.Gift.ID + 1})
	if err != nil {
		t.Fatalf("collectible availability: %v", err)
	}
	if got, ok := availability[entry.Gift.ID]; !ok || got.UpgradeStars != 100 || got.SupplyTotal != 10 || got.Issued != 0 {
		t.Fatalf("collectible availability = %+v, want active published pool", availability)
	}
	if _, ok := availability[entry.Gift.ID+1]; ok {
		t.Fatalf("unknown gift must not have collectible availability: %+v", availability)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_collectible_revisions SET issued=issued WHERE id=$1`, poolRevision.ID); err == nil {
		t.Fatal("published collectible revision accepted a non-advancing issuance update")
	}
	var guardedIssued int
	if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&guardedIssued); err != nil || guardedIssued != 0 {
		t.Fatalf("issued after rejected manual update = %d err %v, want 0", guardedIssued, err)
	}

	savedID, err := gifts.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		MsgID: 700001, Date: 1700001000, ConvertStars: 25, Message: "original",
	})
	if err != nil {
		t.Fatalf("create saved gift: %v", err)
	}
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700001001); err != nil {
		t.Fatalf("grant upgrade stars: %v", err)
	}
	messages := NewMessageStore(pool)
	upgrades := NewStarGiftUpgradeStore(pool, messages)
	req := domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 700001},
		KeepOriginalDetails: true, ChargeStars: 100, FormID: 991,
		CommandKey: "paid-" + suffix, Date: 1700001002,
	}
	upgraded, err := upgrades.UpgradeStarGift(ctx, req)
	if err != nil {
		t.Fatalf("upgrade star gift: %v", err)
	}
	if upgraded.Duplicate || upgraded.Unique.Num != 1 || upgraded.Unique.Slug != "comet-"+suffix+"-1" ||
		upgraded.Unique.Model.Name != "Aurora" || upgraded.Unique.Pattern.Name != "Orbit" ||
		upgraded.Unique.Backdrop.Name != "Midnight" || upgraded.Balance.Balance != 900 ||
		upgraded.Saved.ID != savedID || upgraded.Saved.UniqueGiftID != upgraded.Unique.ID || upgraded.Saved.UpgradeMsgID <= 0 {
		t.Fatalf("upgrade result = %+v", upgraded)
	}
	ownerMessage := upgraded.Send.RecipientMessage
	if ownerMessage.OwnerUserID != owner.ID || ownerMessage.Pts <= 0 || ownerMessage.Media == nil ||
		ownerMessage.Media.ServiceAction == nil || ownerMessage.Media.ServiceAction.Kind != domain.MessageServiceActionStarGiftUnique ||
		ownerMessage.Media.ServiceAction.StarGiftUnique == nil || ownerMessage.Media.ServiceAction.StarGiftUnique.Gift.ID != upgraded.Unique.ID {
		t.Fatalf("owner upgrade service message = %+v", ownerMessage)
	}

	var (
		issued, uniqueCount, commandCount int
		reason                            string
	)
	if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM unique_star_gifts WHERE source_saved_gift_id=$1`, savedID).Scan(&uniqueCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_upgrade_commands WHERE source_saved_gift_id=$1`, savedID).Scan(&commandCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT reason FROM stars_transactions WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, owner.ID).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if issued != 1 || uniqueCount != 1 || commandCount != 1 || reason != string(domain.StarsReasonGiftUpgrade) {
		t.Fatalf("durable aggregate issued=%d unique=%d command=%d reason=%q", issued, uniqueCount, commandCount, reason)
	}

	replayed, err := upgrades.UpgradeStarGift(ctx, req)
	if err != nil {
		t.Fatalf("replay upgrade: %v", err)
	}
	if !replayed.Duplicate || replayed.Unique.ID != upgraded.Unique.ID || replayed.Balance.Balance != 900 {
		t.Fatalf("replayed upgrade = %+v", replayed)
	}
	conflictingReplay := req
	conflictingReplay.KeepOriginalDetails = false
	if _, err := upgrades.UpgradeStarGift(ctx, conflictingReplay); err == nil {
		t.Fatal("same command key with a changed semantic payload must not replay")
	}
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: req.Ref, ChargeStars: 100, FormID: 992,
		CommandKey: "different-" + suffix, Date: 1700001003,
	}); !errors.Is(err, domain.ErrStarGiftAlreadyUpgraded) {
		t.Fatalf("second logical upgrade err = %v", err)
	}
	bal, err := stars.GetBalance(ctx, owner.ID)
	if err != nil || bal.Balance != 900 {
		t.Fatalf("balance after retries = %+v err %v", bal, err)
	}

	prepaidSavedID, err := gifts.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		// A later pool revision may raise the current price; the historical paid
		// amount remains an entitlement instead of being compared to that price.
		MsgID: 700002, Date: 1700001004, ConvertStars: 25, PrepaidUpgradeStars: 50,
	})
	if err != nil {
		t.Fatalf("create prepaid saved gift: %v", err)
	}
	prepaid, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 700002},
		RequirePrepaid: true, CommandKey: "prepaid-" + suffix, Date: 1700001005,
	})
	if err != nil {
		t.Fatalf("free prepaid upgrade: %v", err)
	}
	if prepaid.Saved.ID != prepaidSavedID || prepaid.Unique.Num != 2 || prepaid.Balance.Balance != 900 ||
		prepaid.Send.RecipientMessage.Media.ServiceAction.StarGiftUnique == nil ||
		!prepaid.Send.RecipientMessage.Media.ServiceAction.StarGiftUnique.PrepaidUpgrade {
		t.Fatalf("prepaid upgrade = %+v", prepaid)
	}

	insufficientSavedID, err := gifts.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		MsgID: 700003, Date: 1700001006, ConvertStars: 25,
	})
	if err != nil {
		t.Fatalf("create insufficient saved gift: %v", err)
	}
	if _, err := stars.Debit(ctx, owner.ID, 850, domain.StarsReasonReaction,
		domain.Peer{Type: domain.PeerTypeChannel, ID: 777001}, 1700001007, "paid reaction", ""); err != nil {
		t.Fatalf("seed isolated paid reaction debit: %v", err)
	}
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 700003},
		ChargeStars: 100, CommandKey: "insufficient-" + suffix, Date: 1700001008,
	}); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient upgrade err = %v", err)
	}
	insufficientSaved, found, err := gifts.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 700003})
	if err != nil || !found || insufficientSaved.ID != insufficientSavedID || insufficientSaved.UniqueGiftID != 0 {
		t.Fatalf("saved gift after rejected upgrade = %+v found %v err %v", insufficientSaved, found, err)
	}
	if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, poolRevision.ID).Scan(&issued); err != nil || issued != 2 {
		t.Fatalf("issued after rejected upgrade = %d err %v, want 2", issued, err)
	}
	if err := pool.QueryRow(ctx, `SELECT reason FROM stars_transactions WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, owner.ID).Scan(&reason); err != nil || reason != string(domain.StarsReasonReaction) {
		t.Fatalf("paid reaction ledger reason after rejected upgrade = %q err %v", reason, err)
	}

	collection, err := gifts.CreateCollection(ctx, ownerPeer, "Favorites", []int64{savedID})
	if err != nil {
		t.Fatalf("create unique collection: %v", err)
	}
	filtered, err := gifts.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{Owner: ownerPeer, CollectionID: collection.CollectionID, Limit: 10})
	if err != nil || filtered.Count != 1 || len(filtered.Gifts) != 1 || filtered.Gifts[0].UniqueGiftID != upgraded.Unique.ID {
		t.Fatalf("collection filter = %+v err %v", filtered, err)
	}
	if err := gifts.SetPinned(ctx, ownerPeer, []int64{savedID}); err != nil {
		t.Fatalf("pin unique gift: %v", err)
	}
	pinned, found, err := gifts.GetByRef(ctx, req.Ref)
	if err != nil || !found || pinned.PinnedOrder != 1 || len(pinned.CollectionIDs) != 1 || pinned.CollectionIDs[0] != collection.CollectionID {
		t.Fatalf("pinned saved gift = %+v found %v err %v", pinned, found, err)
	}

	concurrentOwner := createTestUser(t, ctx, users, "+1778"+suffix+"43", "ConcurrentOwner", "")
	concurrentPeer := domain.Peer{Type: domain.PeerTypeUser, ID: concurrentOwner.ID}
	if _, err := gifts.Create(ctx, domain.SavedStarGift{
		Owner: concurrentPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		MsgID: 700004, Date: 1700001010, ConvertStars: 25,
	}); err != nil {
		t.Fatalf("create concurrent upgrade target: %v", err)
	}
	if _, _, err := stars.EnsureGrant(ctx, concurrentOwner.ID, 150, 1700001011); err != nil {
		t.Fatalf("grant concurrent balance: %v", err)
	}
	type concurrentDebitResult struct {
		kind string
		err  error
	}
	start := make(chan struct{})
	results := make(chan concurrentDebitResult, 2)
	go func() {
		<-start
		_, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
			UserID: concurrentOwner.ID, Ref: domain.SavedStarGiftRef{Owner: concurrentPeer, MsgID: 700004},
			ChargeStars: 100, FormID: 993, CommandKey: "concurrent-upgrade-" + suffix, Date: 1700001012,
		})
		results <- concurrentDebitResult{kind: "gift_upgrade", err: err}
	}()
	go func() {
		<-start
		_, err := stars.Debit(ctx, concurrentOwner.ID, 100, domain.StarsReasonReaction,
			domain.Peer{Type: domain.PeerTypeChannel, ID: 777002}, 1700001012, "paid reaction", "")
		results <- concurrentDebitResult{kind: "paid_reaction", err: err}
	}()
	close(start)
	firstResult, secondResult := <-results, <-results
	successes := 0
	for _, result := range []concurrentDebitResult{firstResult, secondResult} {
		if result.err == nil {
			successes++
			continue
		}
		if !errors.Is(result.err, domain.ErrStarsInsufficient) {
			t.Fatalf("concurrent %s err = %v, want Stars insufficient for loser", result.kind, result.err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent debit results = %+v / %+v, want exactly one success", firstResult, secondResult)
	}
	concurrentBalance, err := stars.GetBalance(ctx, concurrentOwner.ID)
	if err != nil || concurrentBalance.Balance != 50 {
		t.Fatalf("concurrent balance = %+v err %v, want 50", concurrentBalance, err)
	}
	reasonRows, err := pool.Query(ctx, `SELECT reason FROM stars_transactions WHERE user_id=$1 AND amount<0 ORDER BY id`, concurrentOwner.ID)
	if err != nil {
		t.Fatalf("list concurrent debit reasons: %v", err)
	}
	var debitReasons []string
	for reasonRows.Next() {
		var got string
		if err := reasonRows.Scan(&got); err != nil {
			reasonRows.Close()
			t.Fatal(err)
		}
		debitReasons = append(debitReasons, got)
	}
	if err := reasonRows.Err(); err != nil {
		reasonRows.Close()
		t.Fatal(err)
	}
	reasonRows.Close()
	if len(debitReasons) != 1 || (debitReasons[0] != string(domain.StarsReasonGiftUpgrade) && debitReasons[0] != string(domain.StarsReasonReaction)) {
		t.Fatalf("concurrent debit reasons = %+v, want exactly one isolated business reason", debitReasons)
	}

	soldOutEntry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Nova", Stars: 25, ConvertStars: 10, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID+100, "nova.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID+100, "nova"), Animation: collectibleTestAnimation("nova.tgs"),
		Actor: "integration", CommandID: "soldout-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create sold-out catalog: %v", err)
	}
	soldOutRevision, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: soldOutEntry.Gift.ID, UpgradeStars: 10, SupplyTotal: 1, SlugPrefix: "nova-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleModel, Name: "Nova", RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+101, "nova-model.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+101, "nova-model"), Animation: collectibleTestAnimationPtr("nova-model.tgs"),
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectiblePattern, Name: "Ray", RarityPermille: 1000,
			Document: collectibleTestDocumentPtr(baseDocumentID+102, "nova-pattern.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+102, "nova-pattern"), Animation: collectibleTestAnimationPtr("nova-pattern.tgs"),
		}},
		Backdrops: []domain.StarGiftCollectibleAttribute{{
			Kind: domain.StarGiftCollectibleBackdrop, Name: "Void", BackdropID: 2,
			CenterColor: 0x101010, EdgeColor: 0x202020, PatternColor: 0x303030, TextColor: 0xffffff,
			RarityPermille: 1000,
		}},
		Actor: "integration", CommandID: "soldout-pool-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish sold-out pool: %v", err)
	}
	soldOutOwner := createTestUser(t, ctx, users, "+1778"+suffix+"44", "SoldOutOwner", "")
	soldOutPeer := domain.Peer{Type: domain.PeerTypeUser, ID: soldOutOwner.ID}
	for index, msgID := range []int{700010, 700011} {
		if _, err := gifts.Create(ctx, domain.SavedStarGift{
			Owner: soldOutPeer, FromUserID: sender.ID, GiftID: soldOutEntry.Gift.ID, RevisionID: soldOutEntry.Gift.RevisionID,
			MsgID: msgID, Date: 1700001020 + index, ConvertStars: 10,
		}); err != nil {
			t.Fatalf("create sold-out target %d: %v", msgID, err)
		}
	}
	if _, _, err := stars.EnsureGrant(ctx, soldOutOwner.ID, 100, 1700001022); err != nil {
		t.Fatalf("grant sold-out owner balance: %v", err)
	}
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: soldOutOwner.ID, Ref: domain.SavedStarGiftRef{Owner: soldOutPeer, MsgID: 700010},
		ChargeStars: 10, CommandKey: "soldout-first-" + suffix, Date: 1700001023,
	}); err != nil {
		t.Fatalf("fill collectible supply: %v", err)
	}
	balanceBeforeSoldOut, _ := stars.GetBalance(ctx, soldOutOwner.ID)
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: soldOutOwner.ID, Ref: domain.SavedStarGiftRef{Owner: soldOutPeer, MsgID: 700011},
		ChargeStars: 10, CommandKey: "soldout-second-" + suffix, Date: 1700001024,
	}); !errors.Is(err, domain.ErrStarGiftCollectibleSoldOut) {
		t.Fatalf("sold-out upgrade err = %v", err)
	}
	balanceAfterSoldOut, _ := stars.GetBalance(ctx, soldOutOwner.ID)
	var soldOutIssued int
	if err := pool.QueryRow(ctx, `SELECT issued FROM star_gift_collectible_revisions WHERE id=$1`, soldOutRevision.ID).Scan(&soldOutIssued); err != nil || soldOutIssued != 1 || balanceAfterSoldOut.Balance != balanceBeforeSoldOut.Balance {
		t.Fatalf("sold-out state issued=%d balance=%d->%d err=%v", soldOutIssued, balanceBeforeSoldOut.Balance, balanceAfterSoldOut.Balance, err)
	}

	ordinaryCollection, err := gifts.CreateCollection(ctx, ownerPeer, "Ordinary", []int64{insufficientSavedID})
	if err != nil {
		t.Fatalf("create ordinary collection: %v", err)
	}
	converted, err := gifts.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 700003})
	if err != nil || !converted.Converted || converted.PinnedOrder != 0 || len(converted.CollectionIDs) != 0 {
		t.Fatalf("convert collection member = %+v err %v", converted, err)
	}
	collections, err := gifts.ListCollections(ctx, ownerPeer)
	if err != nil {
		t.Fatalf("list collections after conversion: %v", err)
	}
	foundOrdinary := false
	for _, got := range collections {
		if got.CollectionID != ordinaryCollection.CollectionID {
			continue
		}
		foundOrdinary = true
		if len(got.GiftIDs) != 0 || got.Hash != domain.StarGiftCollectionHash(got.Title, nil) || got.Hash == ordinaryCollection.Hash {
			t.Fatalf("ordinary collection after conversion = %+v", got)
		}
	}
	if !foundOrdinary {
		t.Fatal("ordinary collection disappeared after member conversion")
	}
	filteredAfterConvert, err := gifts.ListByOwnerFiltered(ctx, domain.SavedStarGiftFilter{
		Owner: ownerPeer, CollectionID: ordinaryCollection.CollectionID, Limit: 10,
	})
	if err != nil || filteredAfterConvert.Count != 0 || len(filteredAfterConvert.Gifts) != 0 {
		t.Fatalf("converted collection filter = %+v err %v, want empty", filteredAfterConvert, err)
	}
}

func collectibleTestAnimation(name string) domain.StarGiftAnimation {
	return domain.StarGiftAnimation{
		SourceName: name, SourceFormat: domain.StarGiftAnimationTGS,
		JSON: []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`),
		TGS:  []byte("test"), SHA256: make([]byte, 32), Width: 512, Height: 512, FrameRate: 30, OutPoint: 30,
	}
}

func collectibleTestAnimationPtr(name string) *domain.StarGiftAnimation {
	animation := collectibleTestAnimation(name)
	return &animation
}

func collectibleTestDocument(id int64, name string) domain.Document {
	return domain.Document{
		ID: id, AccessHash: id + 100, FileReference: []byte("collectible-test"), Date: 1700001000,
		MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrImageSize, W: 512, H: 512},
			{Kind: domain.DocAttrSticker, Alt: "🎁"},
			{Kind: domain.DocAttrFilename, FileName: name},
		},
	}
}

func collectibleTestDocumentPtr(id int64, name string) *domain.Document {
	document := collectibleTestDocument(id, name)
	return &document
}

func collectibleTestBlob(id int64, suffix string) domain.FileBlob {
	return domain.FileBlob{
		LocationKey: fmt.Sprintf("doc:%d", id), Backend: domain.MediaBackendLocalFS,
		ObjectKey: "collectible-integration-" + suffix, Size: 4, SHA256: make([]byte, 32), MimeType: "application/x-tgsticker",
	}
}

func collectibleTestBlobPtr(id int64, suffix string) *domain.FileBlob {
	blob := collectibleTestBlob(id, suffix)
	return &blob
}
