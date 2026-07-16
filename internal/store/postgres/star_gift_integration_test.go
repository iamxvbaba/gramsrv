package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// TestStarGiftStorePostgres 回归迁移 0089：目录不可变版本与用户收到礼物实例对真实 PG 的 CRUD
// （版本固定 / 创建 / keyset 分页 / excludeUnsaved / 隐藏切换 / 转换幂等）。
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
		Blob:      domain.FileBlob{LocationKey: "doc:" + fmt.Sprint(docID), Backend: domain.MediaBackendLocalFS, ObjectKey: "test-star-gift", Size: 4, SHA256: make([]byte, 32), MimeType: "application/x-tgsticker"},
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

	// 创建三份礼物（msg_id 递增）。
	for i := 0; i < 3; i++ {
		if _, err := st.Create(ctx, domain.SavedStarGift{
			Owner: ownerPeer, FromUserID: from.ID, GiftID: entry.Gift.ID, RevisionID: entry.Gift.RevisionID, MsgID: 100 + i,
			Date: 1700000000 + i, ConvertStars: 50,
		}); err != nil {
			t.Fatalf("create gift #%d: %v", i, err)
		}
	}

	// active revision 更新后，已收到的礼物必须继续固定到购买瞬间的 immutable revision。
	docID2 := docID + 1
	documentIDs = append(documentIDs, docID2)
	locationKeys = append(locationKeys, "doc:"+fmt.Sprint(docID2))
	updated, err := st.CreateCatalogRevision(ctx, domain.StarGiftCatalogWrite{
		GiftID: entry.Gift.ID, Title: "Revision 2", Stars: 75, ConvertStars: 25, Enabled: true,
		Document: domain.Document{
			ID: docID2, AccessHash: docID2 + 1, MimeType: "application/x-tgsticker", Size: 4, DCID: 2,
			Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}},
		},
		Blob:      domain.FileBlob{LocationKey: "doc:" + fmt.Sprint(docID2), Backend: domain.MediaBackendLocalFS, ObjectKey: "test-star-gift-v2", Size: 4, SHA256: make([]byte, 32), MimeType: "application/x-tgsticker"},
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

	// keyset 分页：每页 2，末页省略游标。
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

	// 隐藏 msg_id=101 → excludeUnsaved 列表少一份。
	if ok, err := st.SetUnsaved(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 101}, true); err != nil || !ok {
		t.Fatalf("set unsaved = %v err %v", ok, err)
	}
	shown, err := st.ListByOwner(ctx, ownerPeer, true, "", 100)
	if err != nil || len(shown.Gifts) != 2 || shown.Count != 2 {
		t.Fatalf("excludeUnsaved = %d count %d err %v, want 2/2", len(shown.Gifts), shown.Count, err)
	}

	// GetByRef(user msg_id)。
	g, found, err := st.GetByRef(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 100})
	if err != nil || !found || g.GiftID != entry.Gift.ID || g.RevisionID != entry.Gift.RevisionID || g.ConvertStars != 50 {
		t.Fatalf("get = %+v found %v err %v", g, found, err)
	}

	// 转换 msg_id=100 → converted，从列表消失；重复转换被拒。
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
	// 转换不存在的礼物。
	if _, err := st.MarkConverted(ctx, domain.SavedStarGiftRef{Owner: ownerPeer, MsgID: 999}); !errors.Is(err, domain.ErrStarGiftNotFound) {
		t.Fatalf("convert missing err = %v, want ErrStarGiftNotFound", err)
	}

	// CountByOwner（展示在资料 = 非转换、非隐藏）：100 已转换、101 已隐藏、102 仍展示 → 1。
	n, err := st.CountByOwner(ctx, ownerPeer)
	if err != nil || n != 1 {
		t.Fatalf("CountByOwner = %d err %v, want 1 (100 converted, 101 hidden, 102 shown)", n, err)
	}

	// 频道礼物用 saved_id 定位，和用户 msg_id 身份键隔离。
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
}
