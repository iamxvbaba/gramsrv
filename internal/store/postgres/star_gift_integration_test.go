package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftStorePostgres regresses migration 0089: CRUD of immutable catalog revisions and
// received gift instances against a real PG (version pinning / create / keyset pagination / excludeUnsaved / hide toggle / idempotent conversion).
func TestStarGiftStorePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := NewStarGiftStore(pool)

	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 81, Phone: "+1779" + suffix + "51", FirstName: "GiftOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	from, err := users.Create(ctx, domain.User{AccessHash: 82, Phone: "+1779" + suffix + "52", FirstName: "GiftSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	docID := time.Now().UnixNano() & 0x7fffffffffffffff
	documentIDs := []int64{docID}
	locationKeys := []string{"doc:" + fmt.Sprint(docID)}
	entry, err := st.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Stars: 50, ConvertStars: 50, Enabled: true, Document: domain.Document{
			ID: docID, AccessHash: docID + 1, MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
		Blob:      postgresTestBlob("doc:"+fmt.Sprint(docID), "test-star-gift", 4, "application/x-tgsticker"),
		Animation: domain.StarGiftAnimation{JSON: []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`), SHA256: make([]byte, 32), SourceFormat: domain.StarGiftAnimationTGS, Width: 512, Height: 512},
		Actor:     "test", CommandID: "test-star-gift-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog gift: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM peer_star_gifts WHERE owner_peer_id IN ($1, $2)", owner.ID, int64(987654321))
		tx, _ := pool.Begin(ctx)
		if tx != nil {
			_, _ = tx.Exec(ctx, "DELETE FROM star_gift_catalog WHERE gift_id=$1", entry.Gift.ID)
			_, _ = tx.Exec(ctx, "DELETE FROM star_gift_catalog_revisions WHERE gift_id=$1", entry.Gift.ID)
			_, _ = tx.Exec(ctx, "DELETE FROM file_blobs WHERE location_key = ANY($1::text[])", locationKeys)
			_, _ = tx.Exec(ctx, "DELETE FROM documents WHERE id = ANY($1::bigint[])", documentIDs)
			_ = tx.Commit(ctx)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, from.ID})
	})

	// Create three gifts (msg_id increasing).
	savedIDs := make([]int64, 3)
	for i := 0; i < 3; i++ {
		savedID, err := st.Create(ctx, domain.SavedStarGift{
			Owner: ownerPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID, MsgID: 100 + i,
			Date: 1700000000 + i, ConvertStars: 50,
		})
		if err != nil {
			t.Fatalf("create gift #%d: %v", i, err)
		}
		savedIDs[i] = savedID
	}

	// After the active revision is updated, received gifts must stay pinned to the immutable revision as of the purchase moment.
	docID2 := docID + 1
	documentIDs = append(documentIDs, docID2)
	locationKeys = append(locationKeys, "doc:"+fmt.Sprint(docID2))
	updated, err := st.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		GiftID: entry.Gift.ID, Title: "Revision 2", Stars: 75, ConvertStars: 25, Enabled: true,
		Document: domain.Document{
			ID: docID2, AccessHash: docID2 + 1, MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
		Blob:      postgresTestBlob("doc:"+fmt.Sprint(docID2), "test-star-gift-v2", 4, "application/x-tgsticker"),
		Animation: domain.StarGiftAnimation{JSON: []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`), SHA256: make([]byte, 32), SourceFormat: domain.StarGiftAnimationTGS, Width: 512, Height: 512},
		Actor:     "test", CommandID: "test-star-gift-v2-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog revision 2: %v", err)
	}
	if updated.Revision != 2 || updated.Gift.RevisionID == entry.Gift.RevisionID {
		t.Fatalf("revision 2 = %+v, want a new immutable revision", updated)
	}
	if updated.ReceivedCount != 3 {
		t.Fatalf("revision 2 received count = %d, want all 3 historical instances", updated.ReceivedCount)
	}
	historical, found, err := st.CatalogRevision(ctx, entry.Gift.RevisionID)
	if err != nil || !found || historical.Stars != 50 || historical.Sticker.ID != docID {
		t.Fatalf("historical revision = %+v found %v err %v", historical, found, err)
	}

	// keyset pagination: 2 per page, the last page omits the cursor.
	page1, err := st.ListByOwner(ctx, ownerPeer, false, "", 2)
	if err != nil {
		t.Fatalf("list page1: %v", err)
	}
	if len(page1.Gifts) != 2 || page1.Count != 3 || page1.NextOffset == "" {
		t.Fatalf("page1 = %d count %d next %q, want 2/3/nonempty", len(page1.Gifts), page1.Count, page1.NextOffset)
	}
	if page1.Gifts[0].MsgID != 102 {
		t.Fatalf("page1[0] msg_id = %d, want 102 (newest first)", page1.Gifts[0].MsgID)
	}
	page2, err := st.ListByOwner(ctx, ownerPeer, false, page1.NextOffset, 2)
	if err != nil {
		t.Fatalf("list page2: %v", err)
	}
	if len(page2.Gifts) != 1 || page2.NextOffset != "" {
		t.Fatalf("page2 = %d next %q, want 1 + empty (terminal)", len(page2.Gifts), page2.NextOffset)
	}

	// Profile-page order: the full pin vector's order must become the list prefix; even when the cursor
	// splits inside the pinned block or at the pinned/unpinned boundary, nothing may be duplicated or skipped.
	if err := st.SetPinned(ctx, ownerPeer, []int64{savedIDs[0], savedIDs[2]}); err != nil {
		t.Fatalf("set pinned profile order: %v", err)
	}
	wantMsgIDs := []int{100, 102, 101}
	gotMsgIDs := make([]int, 0, len(wantMsgIDs))
	offset := ""
	for pageNumber := 0; ; pageNumber++ {
		page, err := st.ListByOwner(ctx, ownerPeer, false, offset, 1)
		if err != nil {
			t.Fatalf("list pinned page %d: %v", pageNumber, err)
		}
		if page.Count != 3 || len(page.Gifts) != 1 {
			t.Fatalf("pinned page %d = %+v, want count=3 and one gift", pageNumber, page)
		}
		gotMsgIDs = append(gotMsgIDs, page.Gifts[0].MsgID)
		if page.NextOffset == "" {
			break
		}
		offset = page.NextOffset
	}
	if !slices.Equal(gotMsgIDs, wantMsgIDs) {
		t.Fatalf("pinned paged msg ids = %v, want %v", gotMsgIDs, wantMsgIDs)
	}
	// Hiding a pinned gift atomically unpins it and compacts the remaining
	// vector. Pinning it again makes it visible in the same owner transaction.
	if ok, err := st.SetUnsaved(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100}, true); err != nil || !ok {
		t.Fatalf("hide pinned gift = %v err %v", ok, err)
	}
	hiddenPinned, found, err := st.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100})
	if err != nil || !found || !hiddenPinned.Unsaved || hiddenPinned.PinnedOrder != 0 {
		t.Fatalf("hidden pinned gift = %+v found %v err %v", hiddenPinned, found, err)
	}
	remainingPinned, found, err := st.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 102})
	if err != nil || !found || remainingPinned.PinnedOrder != 1 {
		t.Fatalf("remaining pin after compaction = %+v found %v err %v", remainingPinned, found, err)
	}
	if err := st.SetPinned(ctx, ownerPeer, []int64{savedIDs[0], savedIDs[2]}); err != nil {
		t.Fatalf("repin hidden gift: %v", err)
	}
	repinned, found, err := st.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100})
	if err != nil || !found || repinned.Unsaved || repinned.PinnedOrder != 1 {
		t.Fatalf("repinned hidden gift = %+v found %v err %v", repinned, found, err)
	}
	if err := st.SetPinned(ctx, ownerPeer, nil); err != nil {
		t.Fatalf("clear pinned profile order: %v", err)
	}

	// Hiding msg_id=101 -> the excludeUnsaved list has one fewer entry.
	if ok, err := st.SetUnsaved(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 101}, true); err != nil || !ok {
		t.Fatalf("set unsaved = %v err %v", ok, err)
	}
	shown, err := st.ListByOwner(ctx, ownerPeer, true, "", 100)
	if err != nil || len(shown.Gifts) != 2 || shown.Count != 2 {
		t.Fatalf("excludeUnsaved = %d count %d err %v, want 2/2", len(shown.Gifts), shown.Count, err)
	}

	// GetByRef(user msg_id).
	g, found, err := st.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100})
	if err != nil || !found || g.GiftID != entry.Gift.ID || g.RevisionID != entry.Gift.RevisionID || g.ConvertStars != 50 {
		t.Fatalf("get = %+v found %v err %v", g, found, err)
	}

	// Convert msg_id=100 -> converted, it disappears from the list; a duplicate conversion is rejected.
	conv, err := st.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100})
	if err != nil || conv.ConvertStars != 50 || !conv.Converted {
		t.Fatalf("convert = %+v err %v, want ConvertStars 50 converted", conv, err)
	}
	if _, err := st.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100}); !errors.Is(err, domain.ErrStarGiftAlreadyConverted) {
		t.Fatalf("double convert err = %v, want ErrStarGiftAlreadyConverted", err)
	}
	full, _ := st.ListByOwner(ctx, ownerPeer, false, "", 100)
	if full.Count != 2 {
		t.Fatalf("count after convert = %d, want 2", full.Count)
	}
	// Convert a non-existent gift.
	if _, err := st.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 999}); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("convert missing err = %v, want ErrStarGiftNotFound", err)
	}

	// CountByOwner (held total = active including hidden, excluding converted): 100 converted, 101 hidden, 102 still shown -> 2.
	n, err := st.CountByOwner(ctx, ownerPeer)
	if err != nil || n != 2 {
		t.Fatalf("CountByOwner = %d err %v, want 2 (100 converted, 101 hidden, 102 shown)", n, err)
	}

	// Channel gifts are located by saved_id and their identity key is isolated from the user msg_id.
	channelPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: 987654321}
	if _, err := st.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID, MsgID: 700,
		Date: 1700000100, ConvertStars: 50,
	}); err != nil {
		t.Fatalf("create user gift with same msg_id namespace: %v", err)
	}
	channelSavedID, err := st.Create(ctx, domain.SavedStarGift{
		Owner: channelPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID, MsgID: 0, SavedID: 0,
		Date: 1700000101, ConvertStars: 50,
	})
	if err != nil {
		t.Fatalf("create channel gift: %v", err)
	}
	cg, found, err := st.GetByRef(ctx, domain.SavedStarGiftRef{Owner: channelPeer, SavedID: channelSavedID})
	if err != nil || !found || cg.Owner != channelPeer || cg.MsgID != 0 || cg.SavedID != channelSavedID {
		t.Fatalf("get channel gift = %+v found %v err %v", cg, found, err)
	}
	cn, err := st.CountByOwner(ctx, channelPeer)
	if err != nil || cn != 1 {
		t.Fatalf("channel CountByOwner = %d err %v, want 1", cn, err)
	}

	// The support_only flag is persisted and restored when CatalogRevision is read.
	docID3 := docID + 2
	documentIDs = append(documentIDs, docID3)
	locationKeys = append(locationKeys, "doc:"+fmt.Sprint(docID3))
	supportOnlyRev, err := st.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		GiftID: entry.Gift.ID, Title: "Support Only", Stars: 120, ConvertStars: 60, Enabled: true, SupportOnly: true,
		Document: domain.Document{
			ID: docID3, AccessHash: docID3 + 1, MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
		Blob:      postgresTestBlob("doc:"+fmt.Sprint(docID3), "test-star-gift-v3", 4, "application/x-tgsticker"),
		Animation: domain.StarGiftAnimation{JSON: []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`), SHA256: make([]byte, 32), SourceFormat: domain.StarGiftAnimationTGS, Width: 512, Height: 512},
		Actor:     "test", CommandID: "test-star-gift-v3-" + suffix,
	})
	if err != nil {
		t.Fatalf("create support-only revision: %v", err)
	}
	supportOnly, found, err := st.CatalogRevision(ctx, supportOnlyRev.Gift.RevisionID)
	if err != nil || !found || !supportOnly.SupportOnly {
		t.Fatalf("support-only revision = %+v found %v err %v", supportOnly, found, err)
	}
}

// Regression: the profile page is ordered by received time (gift_date) descending. A gift instance row
// is reused on ownership transfer, gift_date is reset to the transfer moment while the id stays the
// same, so ordering by id would leave a transferred gift in its old position. The keyset cursor must
// carry the date, with same-second gifts broken by id.
func TestStarGiftProfileDateOrderPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	st := NewStarGiftStore(pool)

	users := NewUserStore(pool)
	suffix := randomSuffix(t)
	owner, err := users.Create(ctx, domain.User{AccessHash: 83, Phone: "+1779" + suffix + "54", FirstName: "DateOrderOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	from, err := users.Create(ctx, domain.User{AccessHash: 84, Phone: "+1779" + suffix + "55", FirstName: "DateOrderSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	ownerPeer := domain.Peer{Type: domain.PeerTypeUser, ID: owner.ID}
	docID := time.Now().UnixNano() & 0x7fffffffffffffff
	documentIDs := []int64{docID}
	locationKeys := []string{"doc:" + fmt.Sprint(docID)}
	entry, err := st.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		Stars: 50, ConvertStars: 50, Enabled: true, Document: domain.Document{
			ID: docID, AccessHash: docID + 1, MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
		Blob:      postgresTestBlob("doc:"+fmt.Sprint(docID), "test-star-gift-date-order", 4, "application/x-tgsticker"),
		Animation: domain.StarGiftAnimation{JSON: []byte(`{"v":"5.7","w":512,"h":512,"fr":30,"ip":0,"op":30,"layers":[{}]}`), SHA256: make([]byte, 32), SourceFormat: domain.StarGiftAnimationTGS, Width: 512, Height: 512},
		Actor:     "test", CommandID: "test-star-gift-date-order-" + suffix,
	})
	if err != nil {
		t.Fatalf("create catalog gift: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM peer_star_gifts WHERE owner_peer_id = $1", owner.ID)
		tx, _ := pool.Begin(ctx)
		if tx != nil {
			_, _ = tx.Exec(ctx, "DELETE FROM star_gift_catalog WHERE gift_id=$1", entry.Gift.ID)
			_, _ = tx.Exec(ctx, "DELETE FROM star_gift_catalog_revisions WHERE gift_id=$1", entry.Gift.ID)
			_, _ = tx.Exec(ctx, "DELETE FROM file_blobs WHERE location_key = ANY($1::text[])", locationKeys)
			_, _ = tx.Exec(ctx, "DELETE FROM documents WHERE id = ANY($1::bigint[])", documentIDs)
			_ = tx.Commit(ctx)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{owner.ID, from.ID})
	})

	newest, err := st.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		MsgID: 300, Date: 1700000900, ConvertStars: 50,
	})
	if err != nil {
		t.Fatalf("create newest gift: %v", err)
	}
	// Received in the same second as newest: the profile page breaks ties by id, and the cursor must cross the same-second boundary too.
	sameSecond, err := st.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		MsgID: 301, Date: 1700000900, ConvertStars: 50,
	})
	if err != nil {
		t.Fatalf("create same-second gift: %v", err)
	}
	// Largest id but oldest received time (the control group equivalent to reusing an old row after transfer without refreshing the date).
	oldest, err := st.Create(ctx, domain.SavedStarGift{
		Owner: ownerPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID,
		MsgID: 302, Date: 1700000100, ConvertStars: 50,
	})
	if err != nil {
		t.Fatalf("create oldest gift: %v", err)
	}

	wantMsgIDs := []int{301, 300, 302}
	wantSavedIDs := []int64{sameSecond, newest, oldest}
	assertOrder := func(stage string, got []int64) {
		if !slices.Equal(got, wantSavedIDs) {
			t.Fatalf("%s order = %v, want %v (newest received first)", stage, got, wantSavedIDs)
		}
	}
	walk := func(stage string, limit int) []int64 {
		t.Helper()
		got := make([]int64, 0, len(wantSavedIDs))
		offset := ""
		for pageNumber := 0; ; pageNumber++ {
			page, err := st.ListByOwner(ctx, ownerPeer, false, offset, limit)
			if err != nil {
				t.Fatalf("%s page %d: %v", stage, pageNumber, err)
			}
			for _, gift := range page.Gifts {
				got = append(got, gift.ID)
			}
			if page.NextOffset == "" {
				break
			}
			offset = page.NextOffset
		}
		assertOrder(stage, got)
		return got
	}

	full, err := st.ListByOwner(ctx, ownerPeer, false, "", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	gotMsgIDs := make([]int, 0, len(full.Gifts))
	for _, gift := range full.Gifts {
		gotMsgIDs = append(gotMsgIDs, gift.MsgID)
	}
	if !slices.Equal(gotMsgIDs, wantMsgIDs) {
		t.Fatalf("profile msg ids = %v, want %v (gift_date DESC, id DESC)", gotMsgIDs, wantMsgIDs)
	}
	walk("single page", 100)
	walk("page size 1", 1)

	// The pinned block stays first, and within it the order is likewise by received time descending.
	if err := st.SetPinned(ctx, ownerPeer, []int64{oldest}); err != nil {
		t.Fatalf("set pinned: %v", err)
	}
	wantSavedIDs = []int64{oldest, sameSecond, newest}
	walk("pinned page size 1", 1)
}
