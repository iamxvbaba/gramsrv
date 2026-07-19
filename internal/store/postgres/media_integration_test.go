package postgres

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

// TestMediaStoreRoundTrip 验证 MediaStore 各表的写读往返（含 nil bytea 归一、JSONB attributes/sizes、
// 头像历史 current/list/delete、上传分片）。直接证明媒体元数据落 PG 后可原样读回。
func TestMediaStoreRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewMediaStore(pool)

	const docID = int64(9100000000000000001)
	const photoID = int64(9100000000000000002)
	const setID = int64(9100000000000000003)
	const ownerID = int64(9100000000000000099)
	const reactionEmoji = "\U0001f9ea"

	cleanupMediaStoreRoundTripRows(t, ctx, pool)
	t.Cleanup(func() {
		cleanupMediaStoreRoundTripRows(t, context.Background(), pool)
	})

	// ---- file blob（nil sha256 应被归一为空，不报 NOT NULL）----
	if err := s.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:9100000000000000001",
		ObjectKey:   "ab/cd/abcdef",
		Size:        1234,
		MimeType:    "application/x-tgsticker",
	}); err != nil {
		t.Fatalf("put file blob (nil sha256): %v", err)
	}
	blob, ok, err := s.GetFileBlob(ctx, "doc:9100000000000000001")
	if err != nil || !ok {
		t.Fatalf("get file blob: ok=%v err=%v", ok, err)
	}
	if blob.ObjectKey != "ab/cd/abcdef" || blob.Size != 1234 || blob.Backend != domain.MediaBackendLocalFS {
		t.Fatalf("file blob mismatch: %+v", blob)
	}

	// ---- document（含 sticker 属性 + thumbs JSONB；nil file_reference 路径）----
	doc := domain.Document{
		ID:         docID,
		AccessHash: 77,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
		Size:       2048,
		Attributes: []domain.DocumentAttribute{
			{Kind: domain.DocAttrImageSize, W: 512, H: 512},
			{Kind: domain.DocAttrSticker, Alt: "\U0001f600", StickerSetID: setID, StickerSetAccessHash: 5},
		},
		Thumbs: []domain.PhotoSize{{Kind: domain.PhotoSizeKindPath, Type: "j", Bytes: []byte{1, 2, 3}}},
	}
	if err := s.PutDocument(ctx, doc); err != nil {
		t.Fatalf("put document: %v", err)
	}
	got, ok, err := s.GetDocument(ctx, docID)
	if err != nil || !ok {
		t.Fatalf("get document: ok=%v err=%v", ok, err)
	}
	if got.DCID != 2 || len(got.Attributes) != 2 || len(got.Thumbs) != 1 {
		t.Fatalf("document mismatch: %+v", got)
	}
	if id, hash, ok := got.StickerSetRef(); !ok || id != setID || hash != 5 {
		t.Fatalf("document sticker set ref = (%d,%d,%v)", id, hash, ok)
	}
	docs, err := s.GetDocuments(ctx, []int64{docID})
	if err != nil || len(docs) != 1 {
		t.Fatalf("get documents: n=%d err=%v", len(docs), err)
	}

	// ---- photo（sizes JSONB）----
	photo := domain.Photo{
		ID:         photoID,
		AccessHash: 88,
		DCID:       2,
		Sizes:      []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 800, H: 600, Size: 4096}},
	}
	if err := s.PutPhoto(ctx, photo); err != nil {
		t.Fatalf("put photo: %v", err)
	}
	gotPhoto, ok, err := s.GetPhoto(ctx, photoID)
	if err != nil || !ok || len(gotPhoto.Sizes) != 1 || gotPhoto.Sizes[0].Type != "x" {
		t.Fatalf("get photo mismatch: ok=%v err=%v photo=%+v", ok, err, gotPhoto)
	}
	photos, err := s.GetPhotos(ctx, []int64{photoID, 0, photoID, photoID + 99})
	if err != nil || len(photos) != 1 || photos[0].ID != photoID || len(photos[0].Sizes) != 1 {
		t.Fatalf("get photos mismatch: photos=%+v err=%v", photos, err)
	}

	// ---- sticker set ----
	set := domain.StickerSet{
		ID:          setID,
		AccessHash:  5,
		ShortName:   "telesrv_test_set_9100000000000000003",
		Title:       "Test Set",
		Count:       1,
		Kind:        domain.StickerSetKindStickers,
		Animated:    true,
		Installed:   true,
		DocumentIDs: []int64{docID},
		Packs:       []domain.StickerPack{{Emoticon: "\U0001f600", DocumentIDs: []int64{docID}}},
		SystemKey:   "test_system_9100000000000000003",
	}
	if err := s.PutStickerSet(ctx, set); err != nil {
		t.Fatalf("put sticker set: %v", err)
	}
	byID, ok, err := s.GetStickerSetByID(ctx, setID)
	if err != nil || !ok || len(byID.DocumentIDs) != 1 || len(byID.Packs) != 1 {
		t.Fatalf("get sticker set by id: ok=%v err=%v set=%+v", ok, err, byID)
	}
	if byShort, ok, _ := s.GetStickerSetByShortName(ctx, set.ShortName); !ok || byShort.ID != setID {
		t.Fatalf("get sticker set by short name failed: ok=%v", ok)
	}
	if bySys, ok, _ := s.GetStickerSetBySystemKey(ctx, set.SystemKey); !ok || bySys.ID != setID {
		t.Fatalf("get sticker set by system key failed: ok=%v", ok)
	}

	// ---- available reaction ----
	if err := s.PutAvailableReaction(ctx, domain.AvailableReaction{
		Reaction: reactionEmoji, Title: "Test", StaticIconID: docID, SelectAnimationID: docID, Order: 9999,
	}); err != nil {
		t.Fatalf("put available reaction: %v", err)
	}
	reactions, err := s.ListAvailableReactions(ctx)
	if err != nil {
		t.Fatalf("list available reactions: %v", err)
	}
	foundReaction := false
	for _, r := range reactions {
		if r.Reaction == reactionEmoji {
			foundReaction = true
			if r.StaticIconID != docID {
				t.Fatalf("reaction static icon id = %d", r.StaticIconID)
			}
		}
	}
	if !foundReaction {
		t.Fatal("inserted reaction not found in list")
	}

	// ---- profile photo 历史 ----
	if err := s.AddProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, photoID, 1700000000); err != nil {
		t.Fatalf("add profile photo: %v", err)
	}
	cur, ok, err := s.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile)
	if err != nil || !ok || cur != photoID {
		t.Fatalf("current profile photo = (%d,%v,%v)", cur, ok, err)
	}
	refs, err := s.CurrentProfilePhotos(ctx, domain.PeerTypeUser, []int64{ownerID})
	if err != nil || refs[ownerID].PhotoID != photoID || refs[ownerID].DCID != 2 {
		t.Fatalf("current profile photos batch = %+v err=%v", refs, err)
	}
	ids, total, err := s.ListProfilePhotosKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, 0, 10, 0)
	if err != nil || total < 1 || len(ids) < 1 {
		t.Fatalf("list profile photos: ids=%v total=%d err=%v", ids, total, err)
	}
	deleted, err := s.DeleteProfilePhotos(ctx, domain.PeerTypeUser, ownerID, []int64{photoID})
	if err != nil || len(deleted) != 1 {
		t.Fatalf("delete profile photos: deleted=%v err=%v", deleted, err)
	}
	if _, ok, _ := s.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile); ok {
		t.Fatal("profile photo still current after delete")
	}

	// ---- upload parts ----
	if err := s.SaveFilePart(ctx, domain.UploadPart{OwnerUserID: ownerID, FileID: 555, Part: 0, Backend: domain.MediaBackendLocalFS, ObjectKey: "upload_parts/test/555/0-a.part", Size: 5, SHA256: []byte("hello")}); err != nil {
		t.Fatalf("save file part: %v", err)
	}
	if err := s.SaveFilePart(ctx, domain.UploadPart{OwnerUserID: ownerID, FileID: 555, Part: 0, Backend: domain.MediaBackendLocalFS, ObjectKey: "upload_parts/test/555/0-b.part", Size: 6, SHA256: []byte("hello!")}); err != nil {
		t.Fatalf("retry file part: %v", err)
	}
	parts, err := s.LoadFileParts(ctx, ownerID, 555)
	if err != nil || len(parts) != 1 || parts[0].ObjectKey != "upload_parts/test/555/0-b.part" || parts[0].Size != 6 {
		t.Fatalf("load file parts: parts=%+v err=%v", parts, err)
	}
	usage, err := s.UploadPartUsage(ctx, ownerID)
	if err != nil {
		t.Fatalf("upload part usage: %v", err)
	}
	if usage.Bytes != 6 || usage.Parts != 1 || usage.Files != 1 {
		t.Fatalf("upload part usage = %+v", usage)
	}
	slot, err := s.UploadPartSlot(ctx, ownerID, 555, 0)
	if err != nil {
		t.Fatalf("upload part slot: %v", err)
	}
	if !slot.Found || slot.ExistingBytes != 6 || slot.ObjectKey != "upload_parts/test/555/0-b.part" || slot.FileParts != 1 {
		t.Fatalf("upload part slot = %+v", slot)
	}
	if err := s.SaveFilePart(ctx, domain.UploadPart{OwnerUserID: ownerID, FileID: 556, Part: 0, Backend: domain.MediaBackendLocalFS, ObjectKey: "upload_parts/test/556/0.part", Size: 3, SHA256: []byte("old")}); err != nil {
		t.Fatalf("save old file part: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE upload_parts SET created_at = now() - interval '48 hours' WHERE owner_user_id = $1 AND file_id = 556", ownerID); err != nil {
		t.Fatalf("age upload part: %v", err)
	}
	uploadDeleted, err := s.DeleteExpiredUploadParts(ctx, time.Now().Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatalf("delete expired upload parts: %v", err)
	}
	if len(uploadDeleted) != 1 || uploadDeleted[0] != "upload_parts/test/556/0.part" {
		t.Fatalf("delete expired upload parts deleted = %+v, want object key", uploadDeleted)
	}
	if stale, _ := s.LoadFileParts(ctx, ownerID, 556); len(stale) != 0 {
		t.Fatalf("expired file parts still present: %+v", stale)
	}
	deletedPartKeys, err := s.DeleteFileParts(ctx, ownerID, 555)
	if err != nil {
		t.Fatalf("delete file parts: %v", err)
	}
	if len(deletedPartKeys) != 1 || deletedPartKeys[0] != "upload_parts/test/555/0-b.part" {
		t.Fatalf("delete file parts keys = %+v", deletedPartKeys)
	}
	if parts, _ := s.LoadFileParts(ctx, ownerID, 555); len(parts) != 0 {
		t.Fatal("file parts not cleared")
	}
}

func TestUploadedMediaReceiptFirstWriterWinsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	media := NewMediaStore(pool)
	userID := createRevokeTestUser(t, ctx, pool, "upload-receipt")
	const fileID = int64(880055501)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM uploaded_media_receipts WHERE owner_user_id = $1 AND file_id = $2", userID, fileID)
	})
	first := domain.UploadedMediaReceipt{
		OwnerUserID: userID,
		FileID:      fileID,
		IntentHash:  bytes.Repeat([]byte{1}, 32),
		Kind:        domain.UploadedMediaPhoto,
		MediaID:     7001,
	}
	stored, created, err := media.PutUploadedMediaReceipt(ctx, first)
	if err != nil || !created || stored.MediaID != first.MediaID || !bytes.Equal(stored.IntentHash, first.IntentHash) {
		t.Fatalf("first receipt = %+v created=%v err=%v", stored, created, err)
	}
	second := first
	second.IntentHash = bytes.Repeat([]byte{2}, 32)
	second.Kind = domain.UploadedMediaDocument
	second.MediaID = 7002
	stored, created, err = media.PutUploadedMediaReceipt(ctx, second)
	if err != nil || created {
		t.Fatalf("conflicting receipt created=%v err=%v", created, err)
	}
	if stored.Kind != first.Kind || stored.MediaID != first.MediaID || !bytes.Equal(stored.IntentHash, first.IntentHash) {
		t.Fatalf("conflicting receipt replaced first writer: %+v", stored)
	}
	got, found, err := media.GetUploadedMediaReceipt(ctx, userID, fileID)
	if err != nil || !found || got.MediaID != first.MediaID {
		t.Fatalf("get receipt = %+v found=%v err=%v", got, found, err)
	}
}

func TestMediaStoreDocumentCacheCopiesAndRefreshes(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewMediaStore(pool)

	const docID = int64(9100000000000000101)
	if _, err := pool.Exec(ctx, `DELETE FROM documents WHERE id = $1`, docID); err != nil {
		t.Fatalf("cleanup document: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM documents WHERE id = $1`, docID)
	})

	doc := domain.Document{
		ID:            docID,
		AccessHash:    101,
		FileReference: []byte{1, 2, 3},
		Date:          123,
		MimeType:      "application/octet-stream",
		Size:          10,
		DCID:          2,
		Attributes: []domain.DocumentAttribute{{
			Kind:     domain.DocAttrAudio,
			Waveform: []byte{4, 5, 6},
		}},
		Thumbs: []domain.PhotoSize{{
			Kind:  domain.PhotoSizeKindCached,
			Type:  "m",
			Bytes: []byte{7, 8, 9},
			Sizes: []int{1, 2, 3},
		}},
	}
	if err := s.PutDocument(ctx, doc); err != nil {
		t.Fatalf("put document: %v", err)
	}

	doc.FileReference[0] = 99
	doc.Attributes[0].Waveform[0] = 99
	doc.Thumbs[0].Bytes[0] = 99
	doc.Thumbs[0].Sizes[0] = 99

	got, ok, err := s.GetDocument(ctx, docID)
	if err != nil || !ok {
		t.Fatalf("get document: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(got.FileReference, []byte{1, 2, 3}) ||
		!bytes.Equal(got.Attributes[0].Waveform, []byte{4, 5, 6}) ||
		!bytes.Equal(got.Thumbs[0].Bytes, []byte{7, 8, 9}) ||
		got.Thumbs[0].Sizes[0] != 1 {
		t.Fatalf("cached document was mutated: %+v", got)
	}

	got.FileReference[0] = 42
	got.Attributes[0].Waveform[0] = 42
	got.Thumbs[0].Bytes[0] = 42
	got.Thumbs[0].Sizes[0] = 42
	again, ok, err := s.GetDocument(ctx, docID)
	if err != nil || !ok {
		t.Fatalf("get document again: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(again.FileReference, []byte{1, 2, 3}) ||
		!bytes.Equal(again.Attributes[0].Waveform, []byte{4, 5, 6}) ||
		!bytes.Equal(again.Thumbs[0].Bytes, []byte{7, 8, 9}) ||
		again.Thumbs[0].Sizes[0] != 1 {
		t.Fatalf("cache returned shared slices: %+v", again)
	}

	docs, err := s.GetDocuments(ctx, []int64{0, docID, docID, docID + 1})
	if err != nil {
		t.Fatalf("get documents: %v", err)
	}
	if len(docs) != 1 || docs[0].ID != docID {
		t.Fatalf("get documents = %+v, want one cached document", docs)
	}

	updated := again
	updated.AccessHash = 202
	updated.FileReference = []byte{10, 11, 12}
	updated.Attributes[0].Waveform = []byte{13, 14, 15}
	if err := s.PutDocument(ctx, updated); err != nil {
		t.Fatalf("put updated document: %v", err)
	}
	refreshed, ok, err := s.GetDocument(ctx, docID)
	if err != nil || !ok {
		t.Fatalf("get refreshed document: ok=%v err=%v", ok, err)
	}
	if refreshed.AccessHash != 202 ||
		!bytes.Equal(refreshed.FileReference, []byte{10, 11, 12}) ||
		!bytes.Equal(refreshed.Attributes[0].Waveform, []byte{13, 14, 15}) {
		t.Fatalf("document cache did not refresh after PutDocument: %+v", refreshed)
	}
}

func TestMediaStoreListProfilePhotoDetailsBatchesPhotosAndRefreshesMaxID(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewMediaStore(pool)

	const ownerID = int64(9100000000000000199)
	photoIDs := []int64{9100000000000000201, 9100000000000000202, 9100000000000000203, 9100000000000000204}
	cleanupProfilePhotoDetailsRows(t, ctx, pool, ownerID, photoIDs)
	t.Cleanup(func() {
		cleanupProfilePhotoDetailsRows(t, context.Background(), pool, ownerID, photoIDs)
	})

	for i, id := range photoIDs {
		photo := domain.Photo{
			ID:            id,
			AccessHash:    int64(700 + i),
			FileReference: []byte{byte(i + 1)},
			Date:          1700000100 + i,
			DCID:          2,
			Sizes:         []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 100 + i, H: 100 + i, Size: 1000 + i}},
		}
		if err := s.PutPhoto(ctx, photo); err != nil {
			t.Fatalf("put photo %d: %v", id, err)
		}
	}
	if err := s.AddProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, photoIDs[0], 1700000101); err != nil {
		t.Fatalf("add first profile photo: %v", err)
	}
	if err := s.AddProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, photoIDs[1], 1700000102); err != nil {
		t.Fatalf("add second profile photo: %v", err)
	}
	if err := s.AddProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, photoIDs[2], 1700000103); err != nil {
		t.Fatalf("add third profile photo: %v", err)
	}
	if err := s.AddProfilePhotoKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindFallback, photoIDs[3], 1700000104); err != nil {
		t.Fatalf("add fallback photo: %v", err)
	}

	photos, total, err := s.ListProfilePhotoDetailsKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, 0, 2, 0)
	if err != nil {
		t.Fatalf("list profile photo details: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3 profile photos only", total)
	}
	if gotIDs := photoIDsFromDomain(photos); len(gotIDs) != 2 || gotIDs[0] != photoIDs[2] || gotIDs[1] != photoIDs[1] {
		t.Fatalf("first page ids = %v, want newest profile photos [%d %d]", gotIDs, photoIDs[2], photoIDs[1])
	}
	if len(photos[0].Sizes) != 1 || photos[0].Sizes[0].W != 102 {
		t.Fatalf("joined photo sizes not decoded: %+v", photos[0])
	}

	refreshed, total, err := s.ListProfilePhotoDetailsKind(ctx, domain.PeerTypeUser, ownerID, domain.ProfilePhotoKindProfile, -1, 1, photoIDs[1])
	if err != nil {
		t.Fatalf("refresh profile photo by max_id: %v", err)
	}
	if total != 3 {
		t.Fatalf("refresh total = %d, want 3", total)
	}
	if gotIDs := photoIDsFromDomain(refreshed); len(gotIDs) != 1 || gotIDs[0] != photoIDs[1] {
		t.Fatalf("refresh ids = %v, want exact max_id %d", gotIDs, photoIDs[1])
	}
}

func photoIDsFromDomain(photos []domain.Photo) []int64 {
	ids := make([]int64, 0, len(photos))
	for _, photo := range photos {
		ids = append(ids, photo.ID)
	}
	return ids
}

func cleanupMediaStoreRoundTripRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	const docID = int64(9100000000000000001)
	const photoID = int64(9100000000000000002)
	const setID = int64(9100000000000000003)
	const ownerID = int64(9100000000000000099)
	const reactionEmoji = "\U0001f9ea"

	statements := []struct {
		sql  string
		args []any
	}{
		{sql: "DELETE FROM upload_parts WHERE owner_user_id = $1 AND file_id IN (555, 556)", args: []any{ownerID}},
		{sql: "DELETE FROM profile_photos WHERE owner_peer_type = 'user' AND owner_peer_id = $1 AND photo_id = $2", args: []any{ownerID, photoID}},
		{sql: "DELETE FROM available_reactions WHERE reaction IN ($1, 'telesrv-test-😀')", args: []any{reactionEmoji}},
		{sql: "DELETE FROM sticker_sets WHERE id = $1 OR short_name = 'telesrv_test_set_9100000000000000003' OR system_key = 'test_system_9100000000000000003'", args: []any{setID}},
		{sql: "DELETE FROM file_blobs WHERE location_key = 'doc:9100000000000000001'"},
		{sql: "DELETE FROM documents WHERE id = $1", args: []any{docID}},
		{sql: "DELETE FROM photos WHERE id = $1", args: []any{photoID}},
	}
	for _, stmt := range statements {
		if _, err := pool.Exec(ctx, stmt.sql, stmt.args...); err != nil {
			t.Fatalf("cleanup media store round trip rows: %v", err)
		}
	}
}

func cleanupProfilePhotoDetailsRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool, ownerID int64, photoIDs []int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, "DELETE FROM profile_photos WHERE owner_peer_type = 'user' AND owner_peer_id = $1 AND photo_id = ANY($2::bigint[])", ownerID, photoIDs); err != nil {
		t.Fatalf("cleanup profile photo details profile_photos: %v", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM photos WHERE id = ANY($1::bigint[])", photoIDs); err != nil {
		t.Fatalf("cleanup profile photo details photos: %v", err)
	}
}
