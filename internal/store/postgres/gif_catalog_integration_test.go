package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/domain"
)

func TestGifCatalogCapacityIsAtomic(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	base := time.Now().UnixNano()
	const extraDocuments = 2
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gif_catalog WHERE id >= $1 AND id < $2`, base, base+domain.MaxGifCatalogEntries+extraDocuments)
		_, _ = pool.Exec(ctx, `DELETE FROM documents WHERE id >= $1 AND id < $2`, base, base+domain.MaxGifCatalogEntries+extraDocuments)
	})

	var existing, reserved int
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM gif_catalog), entry_count
FROM gif_catalog_capacity WHERE singleton`).Scan(&existing, &reserved); err != nil {
		t.Fatalf("load initial gif catalog capacity: %v", err)
	}
	if existing != 0 || reserved != 0 {
		t.Fatalf("dedicated test database has gif catalog rows: count=%d reserved=%d", existing, reserved)
	}

	media := NewMediaStore(pool)
	for i := 0; i < domain.MaxGifCatalogEntries+extraDocuments; i++ {
		if err := media.PutDocument(ctx, domain.Document{
			ID: base + int64(i), MimeType: "video/mp4", Size: 1, DCID: 2,
		}); err != nil {
			t.Fatalf("put document %d: %v", i, err)
		}
	}

	type result struct {
		entry domain.GifCatalogEntry
		err   error
	}
	results := make(chan result, domain.MaxGifCatalogEntries+1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < domain.MaxGifCatalogEntries+1; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			entry, err := NewGifCatalogStore(pool).CreateGifCatalogEntry(ctx, domain.GifCatalogEntry{
				ID: base + int64(i), Title: fmt.Sprintf("gif-%02d", i), DocumentID: base + int64(i),
			})
			results <- result{entry: entry, err: err}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	successes, full := make([]domain.GifCatalogEntry, 0, domain.MaxGifCatalogEntries), 0
	for got := range results {
		switch {
		case got.err == nil:
			successes = append(successes, got.entry)
		case errors.Is(got.err, domain.ErrGifCatalogFull):
			full++
		default:
			t.Fatalf("concurrent create: %v", got.err)
		}
	}
	if len(successes) != domain.MaxGifCatalogEntries || full != 1 {
		t.Fatalf("concurrent creates: success=%d full=%d", len(successes), full)
	}
	assertGifCatalogCapacity(t, ctx, pool, domain.MaxGifCatalogEntries)

	if changed, err := NewGifCatalogStore(pool).DeleteGifCatalogEntry(ctx, successes[0].ID); err != nil || !changed {
		t.Fatalf("delete catalog entry: changed=%v err=%v", changed, err)
	}
	assertGifCatalogCapacity(t, ctx, pool, domain.MaxGifCatalogEntries-1)

	last := base + domain.MaxGifCatalogEntries + 1
	if _, err := NewGifCatalogStore(pool).CreateGifCatalogEntry(ctx, domain.GifCatalogEntry{
		ID: last, Title: "replacement", DocumentID: last,
	}); err != nil {
		t.Fatalf("create after release: %v", err)
	}
	assertGifCatalogCapacity(t, ctx, pool, domain.MaxGifCatalogEntries)
}

func TestGifCatalogCategoryOverrideAndAutoRestore(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	id := time.Now().UnixNano()
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM gif_catalog WHERE id=$1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM documents WHERE id=$1`, id)
	})
	if err := NewMediaStore(pool).PutDocument(ctx, domain.Document{ID: id, MimeType: "video/mp4", Size: 1, DCID: 2}); err != nil {
		t.Fatal(err)
	}
	store := NewGifCatalogStore(pool)
	entry, err := store.CreateGifCatalogEntry(ctx, domain.GifCatalogEntry{ID: id, Title: "Untitled", FileName: "cat.gif", DocumentID: id})
	if err != nil || entry.Category != domain.GifCategoryAnimals || entry.CategoryManual {
		t.Fatalf("auto entry = %+v err=%v", entry, err)
	}
	if changed, err := store.SetGifCatalogCategory(ctx, id, domain.GifCategorySports); err != nil || !changed {
		t.Fatalf("override changed=%v err=%v", changed, err)
	}
	entries, err := store.ListGifCatalog(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	var found domain.GifCatalogEntry
	for _, candidate := range entries {
		if candidate.ID == id {
			found = candidate
		}
	}
	if found.Category != domain.GifCategorySports || !found.CategoryManual {
		t.Fatalf("manual entry = %+v", found)
	}
	if changed, err := store.SetGifCatalogCategory(ctx, id, ""); err != nil || !changed {
		t.Fatalf("restore changed=%v err=%v", changed, err)
	}
	entries, err = store.ListGifCatalog(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range entries {
		if candidate.ID == id && (candidate.Category != domain.GifCategoryAnimals || candidate.CategoryManual) {
			t.Fatalf("restored entry = %+v", candidate)
		}
	}
}

func assertGifCatalogCapacity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int) {
	t.Helper()
	var rows, reserved int
	if err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM gif_catalog), entry_count
FROM gif_catalog_capacity WHERE singleton`).Scan(&rows, &reserved); err != nil {
		t.Fatalf("load gif catalog capacity: %v", err)
	}
	if rows != want || reserved != want {
		t.Fatalf("gif catalog capacity: rows=%d reserved=%d want=%d", rows, reserved, want)
	}
}
