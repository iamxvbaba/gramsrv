package rpc

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func TestStickerSearchPagesKeywordsEmojiAndHash(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	files := r.deps.Files.(*fakeFiles)
	files.sets[domain.StickerSetKindStickers] = []domain.StickerSet{
		{ID: 20, Title: "Cats", ShortName: "cat_pack", Kind: domain.StickerSetKindStickers, DocumentIDs: []int64{102}, Hash: 2},
		{ID: 10, Title: "Animals", ShortName: "animals", Kind: domain.StickerSetKindStickers, DocumentIDs: []int64{101}, Hash: 1,
			Keywords: []domain.StickerKeyword{{DocumentID: 101, Keywords: []string{"cat", "happy"}}}, Packs: []domain.StickerPack{{Emoticon: "\U0001f431", DocumentIDs: []int64{101}}}},
		{ID: 30, Title: "Cats archived", Kind: domain.StickerSetKindStickers, Archived: true, DocumentIDs: []int64{999}},
		{ID: 40, Title: "Cats deleted", Kind: domain.StickerSetKindStickers, Deleted: true, DocumentIDs: []int64{998}},
	}
	ctx := WithUserID(context.Background(), 1000000001)
	for offset, want := range []int64{101, 102} {
		req := &tg.MessagesSearchStickersRequest{Q: "CAT", Offset: offset, Limit: 1, LangCode: []string{"en"}}
		result, err := r.onMessagesSearchStickers(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		full := result.(*tg.MessagesFoundStickers)
		if len(full.Stickers) != 1 || full.Stickers[0].GetID() != want {
			t.Fatalf("page %d: %+v", offset, full)
		}
		calls := files.getDocumentsCalls
		req.Hash = full.Hash
		cached, err := r.onMessagesSearchStickers(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cached.(*tg.MessagesFoundStickersNotModified); !ok || files.getDocumentsCalls != calls {
			t.Fatalf("cache materialized documents: %T", cached)
		}
	}
	result, err := r.onMessagesSearchStickers(ctx, &tg.MessagesSearchStickersRequest{Emoticon: "\U0001f431\ufe0f", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	full := result.(*tg.MessagesFoundStickers)
	if len(full.Stickers) != 1 || full.Stickers[0].GetID() != 101 {
		t.Fatalf("emoji: %+v", full)
	}
	empty, err := r.onMessagesSearchStickers(ctx, &tg.MessagesSearchStickersRequest{Q: "cat", Offset: 2, Limit: 1, Hash: 12345})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := empty.(*tg.MessagesFoundStickers); !ok || len(got.Stickers) != 0 {
		t.Fatalf("stale hash must return empty full page: %T", empty)
	}
	files.sets[domain.StickerSetKindStickers][1].Keywords[0].Keywords = []string{"dog"}
	r.invalidateStickerCatalog(domain.StickerSetKindStickers)
	changed, err := r.onMessagesSearchStickers(ctx, &tg.MessagesSearchStickersRequest{Q: "cat", Limit: 1, Hash: int64(tdesktopCountHash([]int64{101}))})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := changed.(*tg.MessagesFoundStickers); !ok || got.Stickers[0].GetID() != 102 {
		t.Fatalf("metadata invalidation: %T", changed)
	}
}

func TestStickerSetSearchHasCoversAndViewerInstallState(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)
	if _, err := r.onMessagesInstallStickerSet(ctx, &tg.MessagesInstallStickerSetRequest{Stickerset: &tg.InputStickerSetShortName{ShortName: "funny"}}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []int64{1000000001, 1000000002} {
		result, err := r.onMessagesSearchStickerSets(WithUserID(context.Background(), user), &tg.MessagesSearchStickerSetsRequest{Q: "FUN"})
		if err != nil {
			t.Fatal(err)
		}
		full := result.(*tg.MessagesFoundStickerSets)
		if len(full.Sets) != 1 {
			t.Fatalf("sets: %+v", full)
		}
		set := full.Sets[0].GetSet()
		if set.ID != 10 || (set.InstalledDate != 0) != (user == 1000000001) {
			t.Fatalf("viewer %d: %+v", user, set)
		}
	}
	excluded, err := r.onMessagesSearchStickerSets(ctx, &tg.MessagesSearchStickerSetsRequest{Q: "fun", ExcludeFeatured: true})
	if err != nil || len(excluded.(*tg.MessagesFoundStickerSets).Sets) != 0 {
		t.Fatalf("exclude featured: %+v %v", excluded, err)
	}
}

type failingStickerSearchFiles struct{ *fakeFiles }

func (f failingStickerSearchFiles) ListStickerSets(context.Context, domain.StickerSetKind) ([]domain.StickerSet, error) {
	return nil, errors.New("storage unavailable")
}
func TestStickerSearchDoesNotHideStorageFailure(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	r.deps.Files = failingStickerSearchFiles{r.deps.Files.(*fakeFiles)}
	ctx := WithUserID(context.Background(), 1000000001)
	if _, err := r.onMessagesSearchStickers(ctx, &tg.MessagesSearchStickersRequest{Limit: 20}); err == nil {
		t.Fatal("search hid storage failure")
	}
	if _, err := r.onMessagesSearchStickerSets(ctx, &tg.MessagesSearchStickerSetsRequest{}); err == nil {
		t.Fatal("set search hid storage failure")
	}
	if _, err := r.onMessagesGetFeaturedStickers(ctx, 0); err == nil {
		t.Fatal("discovery hid storage failure")
	}
}

func TestStickerSearchUnicodeAndPerItemKeywords(t *testing.T) {
	sets := []domain.StickerSet{{ID: 1, Kind: domain.StickerSetKindStickers, DocumentIDs: []int64{1, 2}, Keywords: []domain.StickerKeyword{
		{DocumentID: 1, Keywords: []string{"\u5f00\u5fc3", "happy"}}, {DocumentID: 2, Keywords: []string{"sad"}},
	}}}
	if got := domain.SearchStickerDocuments(sets, domain.StickerSetKindStickers, "\u5f00\u5fc3", "", 0, 100); !reflect.DeepEqual(got, []int64{1}) {
		t.Fatalf("unicode keywords: %v", got)
	}
	if got := domain.SearchStickerDocuments(sets, domain.StickerSetKindEmoji, "happy", "", 0, 100); len(got) != 0 {
		t.Fatalf("mixed sticker type: %v", got)
	}
}

// messages.searchEmojiStickerSets searches custom emoji packs and never
// returns regular sticker packs.
func TestEmojiStickerSetSearchFindsOnlyEmojiPacks(t *testing.T) {
	r, _, _ := userStickerSetRouter(t)
	files := r.deps.Files.(*fakeFiles)
	files.sets[domain.StickerSetKindEmoji] = []domain.StickerSet{
		{ID: 70, Title: "Cat emoji", ShortName: "cat_emoji", Kind: domain.StickerSetKindEmoji, Emojis: true, DocumentIDs: []int64{701}, Hash: 7},
		{ID: 71, Title: "Dog emoji", ShortName: "dog_emoji", Kind: domain.StickerSetKindEmoji, Emojis: true, DocumentIDs: []int64{711}, Hash: 8},
	}
	files.sets[domain.StickerSetKindStickers] = []domain.StickerSet{
		{ID: 72, Title: "Cat stickers", ShortName: "cat_stickers", Kind: domain.StickerSetKindStickers, DocumentIDs: []int64{721}, Hash: 9},
	}
	for _, id := range []int64{701, 711, 721} {
		files.docs[id] = domain.Document{ID: id, AccessHash: id + 1000, DCID: 2}
	}
	r.invalidateStickerCatalog(domain.StickerSetKindEmoji)
	r.invalidateStickerCatalog(domain.StickerSetKindStickers)
	ctx := WithUserID(context.Background(), 1000000001)
	result, err := r.onMessagesSearchEmojiStickerSets(ctx, &tg.MessagesSearchEmojiStickerSetsRequest{Q: "cat"})
	if err != nil {
		t.Fatalf("searchEmojiStickerSets: %v", err)
	}
	found, ok := result.(*tg.MessagesFoundStickerSets)
	if !ok || len(found.Sets) != 1 {
		t.Fatalf("searchEmojiStickerSets = %#v, want one emoji pack", result)
	}
	if set := found.Sets[0].GetSet(); set.ID != 70 || !set.Emojis {
		t.Fatalf("found set = %+v, want emoji pack 70", set)
	}
	cached, err := r.onMessagesSearchEmojiStickerSets(ctx, &tg.MessagesSearchEmojiStickerSetsRequest{Q: "cat", Hash: found.Hash})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cached.(*tg.MessagesFoundStickerSetsNotModified); !ok {
		t.Fatalf("same hash = %T, want notModified", cached)
	}
}
