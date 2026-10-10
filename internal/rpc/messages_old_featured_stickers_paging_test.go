package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
)

func featuredStickerRouter(t *testing.T) *Router {
	t.Helper()
	files := &fakeFiles{
		docs: map[int64]domain.Document{
			101: {ID: 101, AccessHash: 11, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			102: {ID: 102, AccessHash: 12, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
			103: {ID: 103, AccessHash: 13, Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrSticker}}},
		},
		sets: map[domain.StickerSetKind][]domain.StickerSet{
			domain.StickerSetKindStickers: {
				{ID: 10, AccessHash: 100, ShortName: "one", Title: "One", Kind: domain.StickerSetKindStickers, Count: 1, Hash: 7, DocumentIDs: []int64{101}},
				{ID: 20, AccessHash: 200, ShortName: "two", Title: "Two", Kind: domain.StickerSetKindStickers, Count: 1, Hash: 8, DocumentIDs: []int64{102}},
				{ID: 30, AccessHash: 300, ShortName: "three", Title: "Three", Kind: domain.StickerSetKindStickers, Count: 1, Hash: 9, DocumentIDs: []int64{103}},
			},
		},
	}
	return New(Config{}, Deps{Files: files}, zaptest.NewLogger(t), clock.System)
}

func TestMessagesGetOldFeaturedStickersPagesByOffsetLimit(t *testing.T) {
	r := featuredStickerRouter(t)
	ctx := WithUserID(context.Background(), 1000000001)

	full, err := r.onMessagesGetOldFeaturedStickers(ctx, &tg.MessagesGetOldFeaturedStickersRequest{})
	if err != nil {
		t.Fatalf("full list: %v", err)
	}
	all, ok := full.(*tg.MessagesFeaturedStickers)
	if !ok {
		t.Fatalf("full list = %T, want *tg.MessagesFeaturedStickers", full)
	}
	if all.Count != 3 || len(all.Sets) != 3 {
		t.Fatalf("full list count = %d sets = %d, want 3", all.Count, len(all.Sets))
	}

	paged, err := r.onMessagesGetOldFeaturedStickers(ctx, &tg.MessagesGetOldFeaturedStickersRequest{Offset: 0, Limit: 2})
	if err != nil {
		t.Fatalf("paged list: %v", err)
	}
	page, ok := paged.(*tg.MessagesFeaturedStickers)
	if !ok {
		t.Fatalf("paged list = %T, want *tg.MessagesFeaturedStickers", paged)
	}
	if page.Count != 3 {
		t.Fatalf("paged count = %d, want total 3", page.Count)
	}
	if len(page.Sets) != 2 {
		t.Fatalf("paged sets = %d, want 2", len(page.Sets))
	}

	tail, err := r.onMessagesGetOldFeaturedStickers(ctx, &tg.MessagesGetOldFeaturedStickersRequest{Offset: 2, Limit: 10})
	if err != nil {
		t.Fatalf("tail page: %v", err)
	}
	tailPage, ok := tail.(*tg.MessagesFeaturedStickers)
	if !ok {
		t.Fatalf("tail page = %T, want *tg.MessagesFeaturedStickers", tail)
	}
	if len(tailPage.Sets) != 1 {
		t.Fatalf("tail sets = %d, want 1", len(tailPage.Sets))
	}

	beyond, err := r.onMessagesGetOldFeaturedStickers(ctx, &tg.MessagesGetOldFeaturedStickersRequest{Offset: 99, Limit: 10})
	if err != nil {
		t.Fatalf("beyond page: %v", err)
	}
	beyondPage, ok := beyond.(*tg.MessagesFeaturedStickers)
	if !ok {
		t.Fatalf("beyond page = %T, want *tg.MessagesFeaturedStickers", beyond)
	}
	if len(beyondPage.Sets) != 0 {
		t.Fatalf("beyond sets = %d, want 0", len(beyondPage.Sets))
	}
}
