package files

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

type gifCatalogMemoryStore struct {
	mu      sync.Mutex
	entries []domain.GifCatalogEntry
}

func (s *gifCatalogMemoryStore) CreateGifCatalogEntry(_ context.Context, entry domain.GifCatalogEntry) (domain.GifCatalogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.entries) >= domain.MaxGifCatalogEntries {
		return domain.GifCatalogEntry{}, domain.ErrGifCatalogFull
	}
	s.entries = append(s.entries, entry)
	return entry, nil
}
func (s *gifCatalogMemoryStore) GifCatalogSeedMatches(_ context.Context, name, digest string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var byName, byDigest bool
	for _, entry := range s.entries {
		byName = byName || entry.SourceFilename == name
		byDigest = byDigest || entry.SourceSHA256 == digest
	}
	return byName, byDigest, nil
}
func (s *gifCatalogMemoryStore) ListGifCatalog(_ context.Context, enabled bool) ([]domain.GifCatalogEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.GifCatalogEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if !enabled || entry.Enabled {
			out = append(out, entry)
		}
	}
	return out, nil
}
func (s *gifCatalogMemoryStore) SetGifCatalogEnabled(context.Context, int64, bool) (bool, error) {
	return false, nil
}
func (s *gifCatalogMemoryStore) SetGifCatalogSortOrder(context.Context, int64, int) (bool, error) {
	return false, nil
}
func (s *gifCatalogMemoryStore) SetGifCatalogCategory(context.Context, int64, string) (bool, error) {
	return false, nil
}
func (s *gifCatalogMemoryStore) DeleteGifCatalogEntry(context.Context, int64) (bool, error) {
	return false, nil
}

func TestSeedGifsIsContentIdempotentAndRejectsChangedSource(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "cat.gif")
	if err := os.WriteFile(source, []byte("GIF89a-first"), 0o600); err != nil {
		t.Fatal(err)
	}
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := &gifCatalogMemoryStore{}
	transcoder := &fakeGIFTranscoder{result: GIFVideo{Data: []byte("normalized-mp4"), Width: 16, Height: 16, Duration: 1}}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil), WithGIFTranscoder(transcoder), WithGifCatalog(catalog))
	stats, err := svc.SeedGifs(ctx, root)
	if err != nil || stats.Imported != 1 || transcoder.calls != 1 {
		t.Fatalf("first seed = %+v err=%v calls=%d", stats, err, transcoder.calls)
	}
	stats, err = svc.SeedGifs(ctx, root)
	if err != nil || stats.Skipped != 1 || transcoder.calls != 1 {
		t.Fatalf("repeat seed = %+v err=%v calls=%d", stats, err, transcoder.calls)
	}
	if err := os.WriteFile(source, []byte("GIF89a-changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SeedGifs(ctx, root); !errors.Is(err, domain.ErrGifCatalogSourceChanged) {
		t.Fatalf("changed source err=%v", err)
	}
}

func TestSeedGifsSkipsRenamedIdenticalContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := []byte("GIF89a-identical")
	if err := os.WriteFile(filepath.Join(root, "first.gif"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := &gifCatalogMemoryStore{}
	transcoder := &fakeGIFTranscoder{result: GIFVideo{Data: []byte("mp4"), Width: 8, Height: 8, Duration: 1}}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil), WithGIFTranscoder(transcoder), WithGifCatalog(catalog))
	if _, err := svc.SeedGifs(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "first.gif"), filepath.Join(root, "renamed.gif")); err != nil {
		t.Fatal(err)
	}
	stats, err := svc.SeedGifs(ctx, root)
	if err != nil || stats.Skipped != 1 || len(catalog.entries) != 1 || transcoder.calls != 1 {
		t.Fatalf("rename seed = %+v err=%v entries=%d calls=%d", stats, err, len(catalog.entries), transcoder.calls)
	}
}

func TestValidateGifCatalogRejectsBackendMismatchAndMissingObject(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "wave.gif"), []byte("GIF89a-wave"), 0o600); err != nil {
		t.Fatal(err)
	}
	media := newFakeMediaStore()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	catalog := &gifCatalogMemoryStore{}
	transcoder := &fakeGIFTranscoder{result: GIFVideo{Data: []byte("normalized-mp4"), Width: 16, Height: 16, Duration: 1}}
	svc := NewService(media, blobs, 2, WithVideoThumbnailer(nil), WithGIFTranscoder(transcoder), WithGifCatalog(catalog))
	if _, err := svc.SeedGifs(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateGifCatalog(ctx); err != nil {
		t.Fatalf("valid catalog: %v", err)
	}

	entry := catalog.entries[0]
	locationKey := fmt.Sprintf("doc:%d", entry.DocumentID)
	blob, found, err := media.GetFileBlob(ctx, locationKey)
	if err != nil || !found {
		t.Fatalf("load catalog blob: found=%v err=%v", found, err)
	}
	blob.Backend = domain.MediaBackendS3
	if err := media.PutFileBlob(ctx, blob); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateGifCatalog(ctx); err == nil {
		t.Fatal("backend mismatch was accepted")
	}

	blob.Backend = domain.MediaBackendLocalFS
	if err := media.PutFileBlob(ctx, blob); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blobs.pathFor(blob.ObjectKey)); err != nil {
		t.Fatal(err)
	}
	if err := svc.ValidateGifCatalog(ctx); err == nil {
		t.Fatal("missing configured-backend object was accepted")
	}
}
