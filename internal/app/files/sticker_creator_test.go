package files

import (
	"context"
	"errors"
	"strings"
	"testing"

	"telesrv/internal/domain"
)

func TestCreateStickerSetInvalidatesNegativeCacheAndLinksDocuments(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 1001, DCID: 2, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		photos: map[int64]domain.Photo{},
		sets:   map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	_, _, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: "fresh_pack"})
	if err != nil {
		t.Fatalf("resolve before create: %v", err)
	}
	if found {
		t.Fatalf("resolve before create found set, want miss")
	}

	set, docs, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Fresh Pack",
		ShortName:     "fresh_pack",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         101,
			DocumentAccessHash: 1001,
			Emoji:              "🙂",
			Keywords:           "fresh, happy, fresh",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	if set.ShortName != "fresh_pack" || !set.Creator || set.CreatorUserID != 1000000001 || set.Count != 1 {
		t.Fatalf("created set = %+v, want creator-owned fresh_pack with one item", set)
	}
	if len(set.Keywords) != 1 || len(set.Keywords[0].Keywords) != 2 {
		t.Fatalf("keywords = %+v, want deduped keyword list", set.Keywords)
	}
	if len(docs) != 1 {
		t.Fatalf("created docs = %d, want 1", len(docs))
	}
	id, hash, ok := docs[0].StickerSetRef()
	if !ok || id != set.ID || hash != set.AccessHash {
		t.Fatalf("document sticker set ref = %d/%d/%v, want %d/%d/true", id, hash, ok, set.ID, set.AccessHash)
	}

	resolved, resolvedDocs, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: "fresh_pack"})
	if err != nil {
		t.Fatalf("resolve after create: %v", err)
	}
	if !found || resolved.ID != set.ID || len(resolvedDocs) != 1 {
		t.Fatalf("resolve after create = found %v set %+v docs %d, want created set", found, resolved, len(resolvedDocs))
	}
}

func TestCreateStickerSetAcceptsUploadedStickerMaterial(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			201: {
				ID:         201,
				AccessHash: 2001,
				DCID:       2,
				MimeType:   "application/octet-stream",
				Size:       4096,
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "local.tgs"}},
			},
		},
		sets: map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	set, docs, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Uploads",
		ShortName:     "uploads_pack",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         201,
			DocumentAccessHash: 2001,
			Emoji:              "👋",
		}},
	})
	if err != nil {
		t.Fatalf("create with uploaded material: %v", err)
	}
	if len(docs) != 1 || !docs[0].IsSticker() {
		t.Fatalf("created docs = %+v, want sticker-tagged uploaded document", docs)
	}
	if id, hash, ok := docs[0].StickerSetRef(); !ok || id != set.ID || hash != set.AccessHash {
		t.Fatalf("uploaded doc sticker ref = %d/%d/%v, want %d/%d/true", id, hash, ok, set.ID, set.AccessHash)
	}
	if !documentHasAttr(docs[0], domain.DocAttrImageSize) || !documentHasAttr(docs[0], domain.DocAttrFilename) {
		t.Fatalf("uploaded doc attrs = %+v, want filename preserved and image size added", docs[0].Attributes)
	}
}

func TestCreateStickerSetAcceptsWebPMaterialWithClientImageSize(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			202: {
				ID:         202,
				AccessHash: 2002,
				DCID:       2,
				MimeType:   "image/webp",
				Size:       4096,
				Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrImageSize, W: 512, H: 512}},
			},
		},
		sets: map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	_, docs, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "WebP Uploads",
		ShortName:     "webp_uploads",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         202,
			DocumentAccessHash: 2002,
			Emoji:              "🙂",
		}},
	})
	if err != nil {
		t.Fatalf("create with client-sized webp: %v", err)
	}
	if len(docs) != 1 || !docs[0].IsSticker() || !documentHasAttr(docs[0], domain.DocAttrImageSize) {
		t.Fatalf("created docs = %+v, want sticker with image size preserved", docs)
	}
}

func TestCreateStickerSetConvertsLottieJSONMaterialToTGS(t *testing.T) {
	ctx := context.Background()
	raw := testLottieJSON()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	objectKey, err := blobs.Put(ctx, raw)
	if err != nil {
		t.Fatalf("put lottie json blob: %v", err)
	}
	media := newFakeMediaStore()
	media.docs[204] = domain.Document{
		ID:         204,
		AccessHash: 2004,
		DCID:       2,
		MimeType:   "application/json",
		Size:       int64(len(raw)),
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "wave.json"}},
	}
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:204",
		Backend:     domain.MediaBackend(blobs.Name()),
		ObjectKey:   objectKey,
		Size:        int64(len(raw)),
		MimeType:    "application/json",
	}); err != nil {
		t.Fatalf("put lottie file blob: %v", err)
	}
	svc := NewService(media, blobs, 2)

	_, docs, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Lottie Uploads",
		ShortName:     "lottie_uploads",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         204,
			DocumentAccessHash: 2004,
			Emoji:              "👋",
		}},
	})
	if err != nil {
		t.Fatalf("create with lottie json: %v", err)
	}
	if len(docs) != 1 || !docs[0].IsSticker() || docs[0].MimeType != "application/x-tgsticker" {
		t.Fatalf("created docs = %+v, want sticker-tagged tgs document", docs)
	}
	if !documentHasAttr(docs[0], domain.DocAttrImageSize) {
		t.Fatalf("converted doc attrs = %+v, want image size", docs[0].Attributes)
	}
	if got := documentFileName(docs[0]); got != "wave.tgs" {
		t.Fatalf("converted filename = %q, want wave.tgs", got)
	}
	blob, found, err := media.GetFileBlob(ctx, "doc:204")
	if err != nil || !found {
		t.Fatalf("converted file blob found=%v err=%v", found, err)
	}
	if blob.MimeType != "application/x-tgsticker" || blob.Size != docs[0].Size {
		t.Fatalf("converted file blob = %+v doc size %d, want tgs metadata", blob, docs[0].Size)
	}
	data, total, err := blobs.GetRange(ctx, blob.ObjectKey, 0, blob.Size)
	if err != nil {
		t.Fatalf("read converted tgs blob: %v", err)
	}
	if int64(len(data)) != total || !validTGSStickerData(data) {
		t.Fatalf("converted blob len=%d total=%d valid=%v, want valid tgs", len(data), total, validTGSStickerData(data))
	}
}

func TestCreateStickerSetRejectsInvalidLottieJSONMaterial(t *testing.T) {
	ctx := context.Background()
	raw := []byte(`{"v":"5.7.4","layers":[]}`)
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("new local fs: %v", err)
	}
	objectKey, err := blobs.Put(ctx, raw)
	if err != nil {
		t.Fatalf("put invalid lottie json blob: %v", err)
	}
	media := newFakeMediaStore()
	media.docs[205] = domain.Document{
		ID:         205,
		AccessHash: 2005,
		DCID:       2,
		MimeType:   "application/json",
		Size:       int64(len(raw)),
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: "bad.json"}},
	}
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:205",
		Backend:     domain.MediaBackend(blobs.Name()),
		ObjectKey:   objectKey,
		Size:        int64(len(raw)),
		MimeType:    "application/json",
	}); err != nil {
		t.Fatalf("put invalid lottie file blob: %v", err)
	}
	svc := NewService(media, blobs, 2)

	_, _, err = svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Bad Lottie Uploads",
		ShortName:     "bad_lottie_uploads",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         205,
			DocumentAccessHash: 2005,
			Emoji:              "👋",
		}},
	})
	if !errors.Is(err, domain.ErrStickerSetFileInvalid) {
		t.Fatalf("create with invalid lottie json err = %v, want ErrStickerSetFileInvalid", err)
	}
}

func TestCreateStickerSetRejectsWebPMaterialWithoutShape(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			203: {ID: 203, AccessHash: 2003, MimeType: "image/webp", Size: 4096},
		},
		sets: map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	_, _, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Bad WebP Uploads",
		ShortName:     "bad_webp_uploads",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         203,
			DocumentAccessHash: 2003,
			Emoji:              "🙂",
		}},
	})
	if !errors.Is(err, domain.ErrStickerSetFileInvalid) {
		t.Fatalf("create with unsized webp err = %v, want ErrStickerSetFileInvalid", err)
	}
}

func TestCreateStickerSetRejectsDuplicateShortNameCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 1001, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		sets: map[int64]domain.StickerSet{
			10: {ID: 10, ShortName: "Fresh_Pack", Kind: domain.StickerSetKindStickers},
		},
	}
	svc := NewService(media, nil, 2)
	_, _, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Other",
		ShortName:     "fresh_pack",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         101,
			DocumentAccessHash: 1001,
			Emoji:              "🙂",
		}},
	})
	if !errors.Is(err, domain.ErrStickerSetShortNameOccupied) {
		t.Fatalf("duplicate create err = %v, want ErrStickerSetShortNameOccupied", err)
	}
}

func documentHasAttr(doc domain.Document, kind domain.DocumentAttributeKind) bool {
	for _, attr := range doc.Attributes {
		if attr.Kind == kind {
			return true
		}
	}
	return false
}

func documentFileName(doc domain.Document) string {
	for _, attr := range doc.Attributes {
		if attr.Kind == domain.DocAttrFilename {
			return attr.FileName
		}
	}
	return ""
}

func testLottieJSON() []byte {
	return []byte(strings.TrimSpace(`{
		"v": "5.7.4",
		"fr": 30,
		"ip": 0,
		"op": 30,
		"w": 512,
		"h": 512,
		"layers": []
	}`))
}
