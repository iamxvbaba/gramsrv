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
			Kind: domain.StarGiftCollectibleModel, Name: "Aurora", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 922,
			Document: collectibleTestDocumentPtr(baseDocumentID+1, "model.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+1, "model"), Animation: collectibleTestAnimationPtr("model.tgs"),
			OfficialDocumentID: 5100000000000000001,
		}, {
			Kind: domain.StarGiftCollectibleModel, Name: "Crafted Aurora", RarityKind: domain.StarGiftRarityLegendary, Crafted: true,
			Document: collectibleTestDocumentPtr(baseDocumentID+3, "crafted-model.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+3, "crafted-model"), Animation: collectibleTestAnimationPtr("crafted-model.tgs"),
			OfficialDocumentID: 5100000000000000003,
		}, {
			Kind: domain.StarGiftCollectibleModel, Name: "Solar", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 78,
			Document: collectibleTestDocumentPtr(baseDocumentID+4, "model-two.tgs"),
			Blob:     collectibleTestBlobPtr(baseDocumentID+4, "model-two"), Animation: collectibleTestAnimationPtr("model-two.tgs"),
			OfficialDocumentID: 5100000000000000004,
		}},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Orbit", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 989,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "pattern"), Animation: collectibleTestAnimationPtr("pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Rings", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 11,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+5, "pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+5, "pattern-two"), Animation: collectibleTestAnimationPtr("pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Midnight", BackdropID: 1, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 999},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Daylight", BackdropID: 2, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 1},
		},
		Actor: "integration", CommandID: "collectibles-" + suffix,
		OfficialGiftID: 5170145012310081615, SourceManifestSHA256: make([]byte, 32),
	})
	if err != nil {
		t.Fatalf("publish collectible pool: %v", err)
	}
	if !poolRevision.Published || poolRevision.Issued != 0 || len(poolRevision.Models) != 3 || len(poolRevision.Patterns) != 2 || len(poolRevision.Backdrops) != 2 ||
		!poolRevision.Models[1].Crafted || poolRevision.Models[1].RarityKind != domain.StarGiftRarityLegendary ||
		poolRevision.Models[1].RarityPermille != 0 || poolRevision.Models[0].OfficialDocumentID != 5100000000000000001 {
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

	messages := NewMessageStore(pool)
	saved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001000, ConvertStars: 25, Message: "original",
	})
	savedID := saved.ID
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, 1700001001); err != nil {
		t.Fatalf("grant upgrade stars: %v", err)
	}
	upgrades := NewStarGiftUpgradeStore(pool, messages)
	req := domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID},
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
	uniqueAction := ownerMessage.Media.ServiceAction.StarGiftUnique
	if uniqueAction.SavedID != 0 || uniqueAction.Peer.Type != "" || uniqueAction.Peer.ID != 0 {
		t.Fatalf("user unique action leaked channel peer/saved_id: %+v", uniqueAction)
	}
	senderUniqueAction := upgraded.Send.SenderMessage.Media.ServiceAction.StarGiftUnique
	if senderUniqueAction == nil || senderUniqueAction.SavedID != 0 {
		t.Fatalf("sender unique action leaked owner-only saved_id: %+v", senderUniqueAction)
	}
	if byOutput, found, err := gifts.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: ownerMessage.ID}); err != nil || !found || byOutput.ID != savedID {
		t.Fatalf("owner upgrade output ref = %+v found %v err %v", byOutput, found, err)
	}
	var ownerAliasCount, senderAliasCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND msg_id=$2 AND saved_gift_id=$3`, owner.ID, ownerMessage.ID, savedID).Scan(&ownerAliasCount); err != nil {
		t.Fatalf("load owner upgrade output alias: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_user_message_refs
WHERE owner_user_id=$1 AND msg_id=$2`, sender.ID, ownerMessage.ID).Scan(&senderAliasCount); err != nil {
		t.Fatalf("load sender upgrade output alias: %v", err)
	}
	if ownerAliasCount != 1 || senderAliasCount != 0 {
		t.Fatalf("upgrade output aliases owner=%d sender=%d, want owner-only", ownerAliasCount, senderAliasCount)
	}
	ownerSourceEdit := upgradedSourceEditForUser(upgraded, owner.ID)
	if ownerSourceEdit.Event.Pts <= ownerMessage.Pts || ownerSourceEdit.Message.Media == nil ||
		ownerSourceEdit.Message.Media.ServiceAction == nil || ownerSourceEdit.Message.Media.ServiceAction.StarGift == nil ||
		ownerSourceEdit.Message.Media.ServiceAction.StarGift.UpgradeMsgID != ownerMessage.ID ||
		ownerSourceEdit.Message.Media.ServiceAction.StarGift.CanUpgrade {
		t.Fatalf("owner source gift was not durably marked upgraded: %+v", ownerSourceEdit)
	}
	senderSourceEdit := upgradedSourceEditForUser(upgraded, sender.ID)
	if senderSourceEdit.Message.Media == nil || senderSourceEdit.Message.Media.ServiceAction == nil ||
		senderSourceEdit.Message.Media.ServiceAction.StarGift == nil ||
		senderSourceEdit.Message.Media.ServiceAction.StarGift.UpgradeMsgID != upgraded.Send.SenderMessage.ID {
		t.Fatalf("sender source gift has wrong box-local upgrade link: %+v", senderSourceEdit)
	}
	difference, err := NewUpdateEventStore(pool).ListAfter(ctx, owner.ID, ownerMessage.Pts-1, 4)
	if err != nil || len(difference) < 2 || difference[0].Type != domain.UpdateEventNewMessage ||
		difference[0].Message.ID != ownerMessage.ID || difference[1].Type != domain.UpdateEventEditMessage ||
		difference[1].Message.ID != saved.MsgID || difference[1].Message.Media == nil ||
		difference[1].Message.Media.ServiceAction == nil || difference[1].Message.Media.ServiceAction.StarGift == nil ||
		difference[1].Message.Media.ServiceAction.StarGift.UpgradeMsgID != ownerMessage.ID {
		t.Fatalf("owner upgrade difference = %+v err %v", difference, err)
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
	receipt, found, err := upgrades.StarGiftUpgradeReceipt(ctx, owner.ID, req.CommandKey)
	if err != nil || !found || receipt.SourceSavedGiftID != savedID || receipt.UniqueGiftID != upgraded.Unique.ID ||
		receipt.FormID != req.FormID || receipt.ChargeStars != req.ChargeStars || receipt.RequirePrepaid ||
		!receipt.KeepOriginalDetails || receipt.BalanceAfter != 900 || receipt.SourceEditPts != ownerSourceEdit.Event.Pts {
		t.Fatalf("upgrade receipt = %+v found=%v err=%v", receipt, found, err)
	}

	replayed, err := upgrades.UpgradeStarGift(ctx, req)
	if err != nil {
		t.Fatalf("replay upgrade: %v", err)
	}
	if !replayed.Duplicate || replayed.Unique.ID != upgraded.Unique.ID || replayed.Balance.Balance != 900 ||
		upgradedSourceEditForUser(replayed, owner.ID).Event.Pts != ownerSourceEdit.Event.Pts {
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

	prepaidSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		// A later pool revision may raise the current price; the historical paid
		// amount remains an entitlement instead of being compared to that price.
		Date: 1700001004, ConvertStars: 25, PrepaidUpgradeStars: 50,
	})
	prepaidSavedID := prepaidSaved.ID
	prepaid, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: prepaidSaved.MsgID},
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

	insufficientSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001006, ConvertStars: 25,
	})
	insufficientSavedID := insufficientSaved.ID
	if _, err := stars.Debit(ctx, owner.ID, 850, domain.StarsReasonReaction,
		domain.Peer{Type: domain.PeerTypeChannel, ID: 777001}, 1700001007, "paid reaction", ""); err != nil {
		t.Fatalf("seed isolated paid reaction debit: %v", err)
	}
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: insufficientSaved.MsgID},
		ChargeStars: 100, FormID: 994, CommandKey: "insufficient-" + suffix, Date: 1700001008,
	}); !errors.Is(err, domain.ErrStarsInsufficient) {
		t.Fatalf("insufficient upgrade err = %v", err)
	}
	insufficientAfter, found, err := gifts.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: insufficientSaved.MsgID})
	if err != nil || !found || insufficientAfter.ID != insufficientSavedID || insufficientAfter.UniqueGiftID != 0 {
		t.Fatalf("saved gift after rejected upgrade = %+v found %v err %v", insufficientAfter, found, err)
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
	concurrentSaved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: concurrentPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: 1700001010, ConvertStars: 25,
	})
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
			UserID: concurrentOwner.ID, Ref: domain.SavedStarGiftRef{Owner: concurrentPeer, MsgID: concurrentSaved.MsgID},
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
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Nova", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestDocumentPtr(baseDocumentID+101, "nova-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+101, "nova-model"), Animation: collectibleTestAnimationPtr("nova-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Nova Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestDocumentPtr(baseDocumentID+103, "nova-model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+103, "nova-model-two"), Animation: collectibleTestAnimationPtr("nova-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Ray", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+102, "nova-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+102, "nova-pattern"), Animation: collectibleTestAnimationPtr("nova-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Ray Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+104, "nova-pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+104, "nova-pattern-two"), Animation: collectibleTestAnimationPtr("nova-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Void", BackdropID: 2, CenterColor: 0x101010, EdgeColor: 0x202020, PatternColor: 0x303030, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Light", BackdropID: 3, CenterColor: 0xeeeeee, EdgeColor: 0xcccccc, PatternColor: 0xaaaaaa, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500},
		},
		Actor: "integration", CommandID: "soldout-pool-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish sold-out pool: %v", err)
	}
	soldOutOwner := createTestUser(t, ctx, users, "+1778"+suffix+"44", "SoldOutOwner", "")
	soldOutPeer := domain.Peer{Type: domain.PeerTypeUser, ID: soldOutOwner.ID}
	soldOutSaved := make([]domain.SavedStarGift, 0, 2)
	for index := range 2 {
		soldOutSaved = append(soldOutSaved, createCollectibleSavedGift(t, ctx, messages, gifts, soldOutEntry.Gift, domain.SavedStarGift{
			Owner: soldOutPeer, FromUserID: sender.ID, GiftID: soldOutEntry.Gift.ID, RevisionID: soldOutEntry.Gift.RevisionID,
			Date: 1700001020 + index, ConvertStars: 10,
		}))
	}
	if _, _, err := stars.EnsureGrant(ctx, soldOutOwner.ID, 100, 1700001022); err != nil {
		t.Fatalf("grant sold-out owner balance: %v", err)
	}
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: soldOutOwner.ID, Ref: domain.SavedStarGiftRef{Owner: soldOutPeer, MsgID: soldOutSaved[0].MsgID},
		ChargeStars: 10, FormID: 995, CommandKey: "soldout-first-" + suffix, Date: 1700001023,
	}); err != nil {
		t.Fatalf("fill collectible supply: %v", err)
	}
	balanceBeforeSoldOut, _ := stars.GetBalance(ctx, soldOutOwner.ID)
	if _, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: soldOutOwner.ID, Ref: domain.SavedStarGiftRef{Owner: soldOutPeer, MsgID: soldOutSaved[1].MsgID},
		ChargeStars: 10, FormID: 996, CommandKey: "soldout-second-" + suffix, Date: 1700001024,
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
	converted, err := gifts.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: insufficientSaved.MsgID})
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

func TestStarGiftCollectiblePreviewActivationGuardPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	gifts := NewStarGiftStore(pool)
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "Unsafe Preview " + suffix, Stars: 10, ConvertStars: 5, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "unsafe-preview.tgs"), Blob: collectibleTestBlob(baseDocumentID, "unsafe-preview"),
		Animation: collectibleTestAnimation("unsafe-preview.tgs"), Actor: "integration", CommandID: "unsafe-preview-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create unsafe-preview catalog: %v", err)
	}
	var revisionID int64
	if err := pool.QueryRow(ctx, `
INSERT INTO star_gift_collectible_revisions
    (gift_id, revision, upgrade_stars, supply_total, slug_prefix, status, created_by, command_id)
VALUES ($1, 1, 10, 10, $2, 'draft', 'integration', $3)
RETURNING id`, entry.Gift.ID, "unsafe-"+suffix, "unsafe-preview-pool-"+suffix).Scan(&revisionID); err != nil {
		t.Fatalf("insert unsafe-preview revision: %v", err)
	}
	animation := collectibleTestAnimation("unsafe-preview-attribute.tgs")
	if _, err := pool.Exec(ctx, `
INSERT INTO star_gift_collectible_models
    (collectible_revision_id, name, document_id, animation_json, animation_sha256, source_name, source_format,
     width, height, frame_rate, in_point, out_point, rarity_kind, rarity_permille, crafted, sort_order)
VALUES ($1, 'Only Model', $2, $3::jsonb, $4, 'model.tgs', 'tgs', 512, 512, 30, 0, 60, 'permille', 1000, false, 0)`,
		revisionID, baseDocumentID, string(animation.JSON), animation.SHA256); err != nil {
		t.Fatalf("insert unsafe-preview model: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO star_gift_collectible_patterns
    (collectible_revision_id, name, document_id, animation_json, animation_sha256, source_name, source_format,
     width, height, frame_rate, in_point, out_point, rarity_kind, rarity_permille, sort_order)
VALUES ($1, 'Only Pattern', $2, $3::jsonb, $4, 'pattern.tgs', 'tgs', 512, 512, 30, 0, 60, 'permille', 1000, 0)`,
		revisionID, baseDocumentID, string(animation.JSON), animation.SHA256); err != nil {
		t.Fatalf("insert unsafe-preview pattern: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO star_gift_collectible_backdrops
    (collectible_revision_id, name, backdrop_id, center_color, edge_color, pattern_color, text_color,
     rarity_kind, rarity_permille, sort_order)
VALUES ($1, 'Only Backdrop', 1, 1, 2, 3, 4, 'permille', 1000, 0)`, revisionID); err != nil {
		t.Fatalf("insert unsafe-preview backdrop: %v", err)
	}
	if _, err := pool.Exec(ctx, `
UPDATE star_gift_collectible_revisions SET status='published', published_at=now() WHERE id=$1`, revisionID); err != nil {
		t.Fatalf("publish unsafe-preview revision directly: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE star_gift_catalog SET collectible_revision_id=$2 WHERE gift_id=$1`, entry.Gift.ID, revisionID); err == nil {
		t.Fatal("database activated a collectible preview pool with one client-distinct item per category")
	}
	var activeRevisionID *int64
	if err := pool.QueryRow(ctx, `SELECT collectible_revision_id FROM star_gift_catalog WHERE gift_id=$1`, entry.Gift.ID).Scan(&activeRevisionID); err != nil || activeRevisionID != nil {
		t.Fatalf("unsafe preview activation pointer=%v err=%v, want null", activeRevisionID, err)
	}
}

func TestStarGiftUpgradeWithoutCraftedModelDoesNotAdvertiseCraft(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	now := int(time.Now().Unix())
	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1779"+suffix+"51", "NoCraftSender", "")
	owner := createTestUser(t, ctx, users, "+1779"+suffix+"52", "NoCraftOwner", "")
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}

	gifts := NewStarGiftStore(pool)
	baseDocumentID := time.Now().UnixNano() & 0x7ffffffffffff000
	entry, err := gifts.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Title: "No Craft " + suffix, Stars: 50, ConvertStars: 25, Enabled: true,
		Document: collectibleTestDocument(baseDocumentID, "no-craft-gift.tgs"),
		Blob:     collectibleTestBlob(baseDocumentID, "no-craft-gift"), Animation: collectibleTestAnimation("no-craft-gift.tgs"),
		Actor: "integration", CommandID: "no-craft-catalog-" + suffix,
	})
	if err != nil {
		t.Fatalf("create no-craft catalog gift: %v", err)
	}
	revision, err := gifts.PublishCollectibleRevision(ctx, domain.StarGiftCollectibleWrite{
		GiftID: entry.Gift.ID, UpgradeStars: 100, SupplyTotal: 10, SlugPrefix: "no-craft-" + suffix,
		Models: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleModel, Name: "Ordinary", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestDocumentPtr(baseDocumentID+1, "no-craft-model.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+1, "no-craft-model"), Animation: collectibleTestAnimationPtr("no-craft-model.tgs")},
			{Kind: domain.StarGiftCollectibleModel, Name: "Ordinary Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestDocumentPtr(baseDocumentID+3, "no-craft-model-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+3, "no-craft-model-two"), Animation: collectibleTestAnimationPtr("no-craft-model-two.tgs")},
		},
		Patterns: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectiblePattern, Name: "Pattern", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+2, "no-craft-pattern.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+2, "no-craft-pattern"), Animation: collectibleTestAnimationPtr("no-craft-pattern.tgs")},
			{Kind: domain.StarGiftCollectiblePattern, Name: "Pattern Two", RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500,
				Document: collectibleTestPatternDocumentPtr(baseDocumentID+4, "no-craft-pattern-two.tgs"), Blob: collectibleTestBlobPtr(baseDocumentID+4, "no-craft-pattern-two"), Animation: collectibleTestAnimationPtr("no-craft-pattern-two.tgs")},
		},
		Backdrops: []domain.StarGiftCollectibleAttribute{
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Backdrop", BackdropID: 1, CenterColor: 0x112233, EdgeColor: 0x223344, PatternColor: 0x334455, TextColor: 0xffffff, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500},
			{Kind: domain.StarGiftCollectibleBackdrop, Name: "Backdrop Two", BackdropID: 2, CenterColor: 0xaabbcc, EdgeColor: 0x778899, PatternColor: 0xddeeff, TextColor: 0x111111, RarityKind: domain.StarGiftRarityPermille, RarityPermille: 500},
		},
		Actor: "integration", CommandID: "no-craft-pool-" + suffix,
	})
	if err != nil {
		t.Fatalf("publish no-craft pool: %v", err)
	}
	if len(revision.Models) != 2 || revision.Models[0].Crafted || revision.Models[1].Crafted {
		t.Fatalf("no-craft pool models = %+v", revision.Models)
	}

	messages := NewMessageStore(pool)
	saved := createCollectibleSavedGift(t, ctx, messages, gifts, entry.Gift, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: sender.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		Date: now, ConvertStars: 25,
	})
	stars := NewStarsStore(pool)
	if _, _, err := stars.EnsureGrant(ctx, owner.ID, 1000, now); err != nil {
		t.Fatalf("grant no-craft upgrade stars: %v", err)
	}
	upgrades := NewStarGiftUpgradeStore(pool, messages, WithStarGiftLifecyclePolicy(domain.StarGiftLifecyclePolicy{
		TransferStars: 25, DropOriginalDetailsStars: 25, OfferMinStars: 1, CraftChancePermille: 750,
	}))
	upgraded, err := upgrades.UpgradeStarGift(ctx, domain.StarGiftUpgradeRequest{
		UserID: owner.ID, Ref: domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: saved.MsgID},
		ChargeStars: 100, FormID: 551, CommandKey: "no-craft-upgrade-" + suffix, Date: now + 1,
	})
	if err != nil {
		t.Fatalf("upgrade no-craft gift: %v", err)
	}
	uniqueAction := upgraded.Send.RecipientMessage.Media.ServiceAction.StarGiftUnique
	if upgraded.Unique.CraftChancePermille != 0 || upgraded.Saved.CanCraftAt != 0 ||
		uniqueAction == nil || uniqueAction.Gift.CraftChancePermille != 0 || uniqueAction.CanCraftAt != 0 {
		t.Fatalf("no-craft capability leaked: saved=%+v unique=%+v action=%+v", upgraded.Saved, upgraded.Unique, uniqueAction)
	}

	lifecycle := NewStarGiftLifecycleStore(pool, messages, 1_000_000)
	page, err := lifecycle.ListCraftStarGifts(ctx, owner.ID, entry.Gift.ID, "", 10)
	if err != nil || page.Count != 0 || len(page.Gifts) != 0 {
		t.Fatalf("no-craft candidate page = %+v err %v", page, err)
	}
	if _, err := lifecycle.CraftStarGift(ctx, domain.StarGiftCraftRequest{
		UserID: owner.ID, Refs: []domain.SavedStarGiftRef{{Owner: ownerPeer, MsgID: saved.MsgID}},
		CommandKey: "no-craft-attempt-" + suffix, Date: now + 2,
	}); !errors.Is(err, domain.ErrStarGiftCraftUnavailable) {
		t.Fatalf("no-craft attempt err = %v", err)
	}
	var lifecycleStatus string
	var burned bool
	var commandCount int
	if err := pool.QueryRow(ctx, `SELECT p.lifecycle_status,u.burned
FROM peer_star_gifts p JOIN unique_star_gifts u ON u.id=p.unique_gift_id WHERE p.id=$1`, upgraded.Saved.ID).
		Scan(&lifecycleStatus, &burned); err != nil {
		t.Fatalf("load no-craft aggregate: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM star_gift_craft_commands WHERE user_id=$1 AND command_key=$2`,
		owner.ID, "no-craft-attempt-"+suffix).Scan(&commandCount); err != nil {
		t.Fatalf("count no-craft commands: %v", err)
	}
	if lifecycleStatus != "active" || burned || commandCount != 0 {
		t.Fatalf("no-craft attempt mutated aggregate: status=%q burned=%t commands=%d", lifecycleStatus, burned, commandCount)
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

func collectibleTestPatternDocumentPtr(id int64, name string) *domain.Document {
	document := collectibleTestDocument(id, name)
	document.Attributes[1] = domain.DocumentAttribute{Kind: domain.DocAttrCustomEmoji, Alt: "🎁", TextColor: true}
	document.Thumbs = []domain.PhotoSize{{Kind: domain.PhotoSizeKindPath, Type: "j", Bytes: []byte{1}}}
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

// createCollectibleSavedGift seeds the same valid source-message + saved-gift
// invariant as the purchase aggregate. Tests must not invent a peer_star_gifts
// msg_id that has no durable message box behind it.
func createCollectibleSavedGift(
	t *testing.T,
	ctx context.Context,
	messages *MessageStore,
	gifts *StarGiftStore,
	gift domain.StarGift,
	saved domain.SavedStarGift,
) domain.SavedStarGift {
	t.Helper()
	sticker := gift.Sticker
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID:    saved.FromUserID,
		RecipientUserID: saved.Owner.ID,
		RandomID:        (time.Now().UnixNano() & 0x7fffffffffffffff) ^ saved.Owner.ID ^ int64(saved.Date),
		Date:            saved.Date,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{
			Kind: domain.MessageServiceActionStarGift,
			StarGift: &domain.MessageStarGiftAction{
				GiftID: gift.ID, Stars: gift.Stars, ConvertStars: saved.ConvertStars,
				Title: gift.Title, Sticker: &sticker, Message: saved.Message,
				FromUserID: saved.FromUserID, PeerUserID: saved.Owner.ID, Saved: true,
				CanUpgrade: gift.UpgradeStars > 0, PrepaidUpgrade: saved.PrepaidUpgradeStars > 0,
				UpgradePriceStars: gift.UpgradeStars, UpgradeStars: saved.PrepaidUpgradeStars,
			},
		}},
	})
	if err != nil {
		t.Fatalf("create collectible source message: %v", err)
	}
	saved.MsgID = sent.RecipientMessage.ID
	id, err := gifts.Create(ctx, saved)
	if err != nil {
		t.Fatalf("create saved gift: %v", err)
	}
	saved.ID = id
	return saved
}

func upgradedSourceEditForUser(result domain.StarGiftUpgradeResult, userID int64) domain.EditedMessageForUser {
	for _, edit := range result.SourceEdits {
		if edit.UserID == userID {
			return edit
		}
	}
	return domain.EditedMessageForUser{UserID: userID}
}
