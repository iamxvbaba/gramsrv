package files

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestManageStickerSetMutationsKeepSetAndDocumentsConsistent(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 1001, DCID: 2, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			102: {ID: 102, AccessHash: 1002, DCID: 2, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		sets: map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	set, docs, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Fresh Pack",
		ShortName:     "fresh_pack",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         101,
			DocumentAccessHash: 1001,
			Emoji:              "🙂",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	originalHash := set.Hash
	if len(docs) != 1 {
		t.Fatalf("created docs = %d, want 1", len(docs))
	}

	set, docs, err = svc.AddStickerToSet(ctx, 1000000001, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: "fresh_pack"}, domain.StickerSetItemInput{
		DocumentID:         102,
		DocumentAccessHash: 1002,
		Emoji:              "😄",
		Keywords:           "smile, fresh",
	})
	if err != nil {
		t.Fatalf("add sticker: %v", err)
	}
	if set.Count != 2 || len(set.DocumentIDs) != 2 || len(docs) != 2 || set.Hash == originalHash {
		t.Fatalf("after add set=%+v docs=%d originalHash=%d, want two docs and bumped hash", set, len(docs), originalHash)
	}
	if id, hash, ok := docs[1].StickerSetRef(); !ok || id != set.ID || hash != set.AccessHash {
		t.Fatalf("added doc sticker ref = %d/%d/%v, want %d/%d/true", id, hash, ok, set.ID, set.AccessHash)
	}

	set, docs, err = svc.ChangeStickerPosition(ctx, 1000000001, 102, 1002, 0)
	if err != nil {
		t.Fatalf("change sticker position: %v", err)
	}
	if got := set.DocumentIDs; len(got) != 2 || got[0] != 102 || got[1] != 101 {
		t.Fatalf("document order after move = %v, want [102 101]", got)
	}
	if len(docs) != 2 || docs[0].ID != 102 || docs[1].ID != 101 {
		t.Fatalf("returned docs after move = %+v, want 102 then 101", docs)
	}

	set, docs, err = svc.RenameStickerSet(ctx, 1000000001, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID, AccessHash: set.AccessHash}, "Renamed Pack")
	if err != nil {
		t.Fatalf("rename sticker set: %v", err)
	}
	if set.Title != "Renamed Pack" || len(docs) != 2 {
		t.Fatalf("renamed set=%+v docs=%d, want renamed with docs intact", set, len(docs))
	}

	set, docs, err = svc.RemoveStickerFromSet(ctx, 1000000001, 102, 1002)
	if err != nil {
		t.Fatalf("remove sticker: %v", err)
	}
	if set.Count != 1 || len(set.DocumentIDs) != 1 || set.DocumentIDs[0] != 101 || len(docs) != 1 || docs[0].ID != 101 {
		t.Fatalf("after remove set=%+v docs=%+v, want only doc 101", set, docs)
	}
	detached, ok := media.docs[102]
	if !ok {
		t.Fatalf("detached doc missing from fake store")
	}
	if id, _, ok := detached.StickerSetRef(); ok || id != 0 {
		t.Fatalf("removed doc sticker ref = %d/%v, want detached", id, ok)
	}

	_, _, err = svc.RemoveStickerFromSet(ctx, 1000000001, 101, 1001)
	if !errors.Is(err, domain.ErrStickerSetEmpty) {
		t.Fatalf("remove last sticker err = %v, want ErrStickerSetEmpty", err)
	}

	kind, err := svc.DeleteStickerSet(ctx, 1000000001, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID, AccessHash: set.AccessHash})
	if err != nil {
		t.Fatalf("delete sticker set: %v", err)
	}
	if kind != domain.StickerSetKindStickers {
		t.Fatalf("delete kind = %q, want stickers", kind)
	}
	if _, _, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByShortName, ShortName: "fresh_pack"}); err != nil || found {
		t.Fatalf("resolve deleted set = found %v err %v, want miss", found, err)
	}
}

func TestManageStickerSetRejectsNonCreator(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 1001, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			102: {ID: 102, AccessHash: 1002, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		sets: map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	set, _, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Fresh Pack",
		ShortName:     "fresh_pack",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         101,
			DocumentAccessHash: 1001,
			Emoji:              "🙂",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	_, _, err = svc.AddStickerToSet(ctx, 1000000002, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID, AccessHash: set.AccessHash}, domain.StickerSetItemInput{
		DocumentID:         102,
		DocumentAccessHash: 1002,
		Emoji:              "😄",
	})
	if !errors.Is(err, domain.ErrStickerSetNotOwned) {
		t.Fatalf("non-creator add err = %v, want ErrStickerSetNotOwned", err)
	}
}

func TestAddStickerToSetAcceptsUploadedMaterial(t *testing.T) {
	ctx := context.Background()
	media := &fakeMediaStore{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 1001, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			202: {ID: 202, AccessHash: 2002, MimeType: "video/mp4", Size: 4096, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrVideo, W: 512, H: 512, Duration: 1}}},
		},
		sets: map[int64]domain.StickerSet{},
	}
	svc := NewService(media, nil, 2)

	set, _, err := svc.CreateStickerSet(ctx, domain.CreateStickerSetRequest{
		CreatorUserID: 1000000001,
		Title:         "Fresh Pack",
		ShortName:     "fresh_pack",
		Items: []domain.StickerSetItemInput{{
			DocumentID:         101,
			DocumentAccessHash: 1001,
			Emoji:              "🙂",
		}},
	})
	if err != nil {
		t.Fatalf("create sticker set: %v", err)
	}
	set, docs, err := svc.AddStickerToSet(ctx, 1000000001, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID, AccessHash: set.AccessHash}, domain.StickerSetItemInput{
		DocumentID:         202,
		DocumentAccessHash: 2002,
		Emoji:              "🎬",
	})
	if err != nil {
		t.Fatalf("add uploaded material: %v", err)
	}
	if set.Count != 2 || len(docs) != 2 {
		t.Fatalf("after add set=%+v docs=%d, want two items", set, len(docs))
	}
	added := docs[1]
	if added.ID != 202 || !added.IsSticker() || !documentHasAttr(added, domain.DocAttrVideo) {
		t.Fatalf("added doc = %+v, want sticker-tagged video material", added)
	}
}
