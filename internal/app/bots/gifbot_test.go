package bots

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

type gifCatalogTestSource struct {
	entries []domain.GifCatalogEntry
	docs    []domain.Document
}

func (s gifCatalogTestSource) ListGifCatalog(context.Context, bool) ([]domain.GifCatalogEntry, error) {
	return s.entries, nil
}
func (s gifCatalogTestSource) GetDocuments(context.Context, []int64) ([]domain.Document, error) {
	return s.docs, nil
}

func TestGifBotRanksMatchesAndReturnsPlayableDocuments(t *testing.T) {
	doc := domain.Document{ID: 9, MimeType: "video/mp4", Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAnimated}, {Kind: domain.DocAttrVideo, W: 320, H: 240, Duration: 1}}}
	svc := NewService(nil, nil, nil, WithGifCatalog(gifCatalogTestSource{
		entries: []domain.GifCatalogEntry{{ID: 1, Title: "Dog", DocumentID: 9}, {ID: 2, Title: "Cat wave", DocumentID: 9}}, docs: []domain.Document{doc},
	}))
	got, handled, err := svc.OnInlineQuery(context.Background(), domain.GifBotUserID, 42, "cat", "")
	if err != nil || !handled || got.QueryID != 0 || len(got.Results) != 2 {
		t.Fatalf("OnInlineQuery = %+v,%v,%v", got, handled, err)
	}
	if got.Results[0].ID != "2" || got.Results[0].Media == nil || got.Results[0].Media.Document.ID != 9 {
		t.Fatalf("ranked results = %+v", got.Results)
	}
}

func TestGifBotFailsFastOnMissingDocument(t *testing.T) {
	svc := NewService(nil, nil, nil, WithGifCatalog(gifCatalogTestSource{entries: []domain.GifCatalogEntry{{ID: 1, Title: "Missing", DocumentID: 99}}}))
	if _, handled, err := svc.OnInlineQuery(context.Background(), domain.GifBotUserID, 42, "", ""); !handled || err == nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}

func TestGifBotRanksManualCategoryAfterTitleMatches(t *testing.T) {
	entries := []domain.GifCatalogEntry{
		{ID: 1, Title: "Unrelated", Category: domain.GifCategoryAnimals, CategoryManual: true},
		{ID: 2, Title: "Cat wave", Category: domain.GifCategoryReaction},
		{ID: 3, Title: "Other", Category: domain.GifCategoryOther},
	}
	got := rankGifCatalogEntries(entries, "cat")
	if got[0].ID != 2 || got[1].ID != 1 || got[2].ID != 3 {
		t.Fatalf("ranked IDs = %d,%d,%d", got[0].ID, got[1].ID, got[2].ID)
	}
}
