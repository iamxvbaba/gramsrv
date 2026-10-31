package postgres

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

// TestWebPageStoreRoundTrip verifies web_pages upsert/read-back (byte-level JSONB snapshot round-trip)
// and dedup overwrite by url_hash. Requires TELESRV_TEST_POSTGRES_DSN.
func TestWebPageStoreRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	s := NewMediaStore(pool)

	const urlHash = int64(9100000000000000777)
	_, _ = pool.Exec(ctx, `DELETE FROM web_pages WHERE url_hash = $1`, urlHash)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM web_pages WHERE url_hash = $1`, urlHash)
	})

	// miss
	if _, _, found, err := s.GetWebPageByURLHash(ctx, urlHash); err != nil || found {
		t.Fatalf("expected miss, got found=%v err=%v", found, err)
	}

	page := domain.MessageWebPage{
		State:       domain.MessageWebPageStateDone,
		ID:          urlHash,
		URL:         "https://example.com/a",
		DisplayURL:  "example.com",
		Title:       "Title",
		Description: "Desc",
		SiteName:    "Site",
		Type:        "article",
		Hash:        123,
		Photo:       &domain.Photo{ID: 5, AccessHash: 6, DCID: 2, Sizes: []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 320, H: 200, Size: 1024}}},
	}
	if err := s.PutWebPage(ctx, urlHash, page, 1700000000); err != nil {
		t.Fatalf("PutWebPage: %v", err)
	}
	got, refreshedAt, found, err := s.GetWebPageByURLHash(ctx, urlHash)
	if err != nil || !found {
		t.Fatalf("GetWebPageByURLHash: found=%v err=%v", found, err)
	}
	if got.State != domain.MessageWebPageStateDone || got.Title != "Title" || got.Photo == nil || got.Photo.ID != 5 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if refreshedAt != 1700000000 {
		t.Fatalf("refreshedAt = %d, want 1700000000", refreshedAt)
	}

	// upsert overwrites to the empty state.
	empty := domain.MessageWebPage{State: domain.MessageWebPageStateEmpty, ID: urlHash, URL: "https://example.com/a"}
	if err := s.PutWebPage(ctx, urlHash, empty, 1700000500); err != nil {
		t.Fatalf("PutWebPage(empty): %v", err)
	}
	got2, refreshed2, _, err := s.GetWebPageByURLHash(ctx, urlHash)
	if err != nil {
		t.Fatalf("GetWebPageByURLHash after upsert: %v", err)
	}
	if got2.State != domain.MessageWebPageStateEmpty || got2.Photo != nil {
		t.Fatalf("upsert did not overwrite: %+v", got2)
	}
	if refreshed2 != 1700000500 {
		t.Fatalf("refreshed_at not updated on upsert: %d", refreshed2)
	}
}
