package files

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"telesrv/internal/domain"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestBlobMetaCacheGetPutEvict(t *testing.T) {
	c := newBlobMetaCache(2)
	c.put("a", domain.FileBlob{LocationKey: "a", ObjectKey: "oa"})
	c.put("b", domain.FileBlob{LocationKey: "b", ObjectKey: "ob"})
	if b, ok := c.get("a"); !ok || b.ObjectKey != "oa" {
		t.Fatalf("get a = %+v ok=%v", b, ok)
	}
	// Capacity 2: a was just accessed, so putting c should evict the least recently used b.
	c.put("c", domain.FileBlob{LocationKey: "c", ObjectKey: "oc"})
	if _, ok := c.get("b"); ok {
		t.Error("b should be evicted (least recently used)")
	}
	if _, ok := c.get("a"); !ok {
		t.Error("a should remain (recently used)")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("c should be present")
	}
}

// countingMediaStore counts GetFileBlob calls, verifying that a metadata cache hit no longer queries PG.
type countingMediaStore struct {
	*fakeMediaStore
	getBlobCalls    int
	getSetByIDCalls int
}

func (c *countingMediaStore) GetFileBlob(ctx context.Context, key string) (domain.FileBlob, bool, error) {
	c.getBlobCalls++
	return c.fakeMediaStore.GetFileBlob(ctx, key)
}

func (c *countingMediaStore) GetStickerSetByID(ctx context.Context, id int64) (domain.StickerSet, bool, error) {
	c.getSetByIDCalls++
	return c.fakeMediaStore.GetStickerSetByID(ctx, id)
}

type countingBlobBackend struct {
	BlobBackend
	// WarmCaches reads blobs concurrently, so the count must be atomic.
	getRangeCalls atomic.Int64
}

func (c *countingBlobBackend) GetRange(ctx context.Context, objectKey string, offset, limit int64) ([]byte, int64, error) {
	c.getRangeCalls.Add(1)
	return c.BlobBackend.GetRange(ctx, objectKey, offset, limit)
}

func TestGetFileCachesMetadataAndSmallBlobBytes(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	objectKey, err := local.Put(ctx, []byte("0123456789"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{LocationKey: "doc:42", Backend: domain.MediaBackendLocalFS, ObjectKey: objectKey, Size: 10, MimeType: "application/octet-stream"}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	counting := &countingMediaStore{fakeMediaStore: media}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(counting, blobs, 2)

	// First time: query PG once and fill the metadata cache; the small blob is read whole into the byte cache and [0,5) is returned.
	c1, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:42", Offset: 0, Limit: 5})
	if err != nil || !ok {
		t.Fatalf("getfile1 ok=%v err=%v", ok, err)
	}
	if string(c1.Bytes) != "01234" {
		t.Errorf("chunk1 = %q, want 01234", c1.Bytes)
	}
	if c1.Total != 10 {
		t.Errorf("total = %d, want 10", c1.Total)
	}
	if c1.ImmutableRange == nil || c1.ImmutableRange.ObjectKey != objectKey || c1.ImmutableRange.Offset != 0 || c1.ImmutableRange.Length != 5 {
		t.Fatalf("immutable range = %+v, want exact first chunk descriptor", c1.ImmutableRange)
	}
	replaySvc := NewService(media, local, 2)
	replayed, err := replaySvc.ReadImmutableFileRange(ctx, *c1.ImmutableRange)
	if err != nil || string(replayed) != "01234" {
		t.Fatalf("immutable replay = %q err=%v, want 01234", replayed, err)
	}
	corrupt := *c1.ImmutableRange
	corrupt.RangeSHA256[0] ^= 0xff
	if _, err := replaySvc.ReadImmutableFileRange(ctx, corrupt); err == nil {
		t.Fatal("corrupt immutable range digest was accepted")
	}
	if counting.getBlobCalls != 1 {
		t.Errorf("getBlobCalls = %d, want 1", counting.getBlobCalls)
	}
	if got := blobs.getRangeCalls.Load(); got != 1 {
		t.Errorf("getRangeCalls = %d, want 1", got)
	}

	// Second time: the same location hits the metadata and byte caches; [5,10) is sliced directly from memory.
	c2, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:42", Offset: 5, Limit: 5})
	if err != nil || !ok {
		t.Fatalf("getfile2 ok=%v err=%v", ok, err)
	}
	if string(c2.Bytes) != "56789" {
		t.Errorf("chunk2 = %q, want 56789", c2.Bytes)
	}
	if counting.getBlobCalls != 1 {
		t.Errorf("getBlobCalls = %d, want 1 (cache hit)", counting.getBlobCalls)
	}
	if got := blobs.getRangeCalls.Load(); got != 1 {
		t.Errorf("getRangeCalls = %d, want 1 (byte cache hit)", got)
	}
}

func TestGetFileByteCacheReturnsSharedReadOnlyViews(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	objectKey, err := local.Put(ctx, []byte("0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:shared-small", Backend: domain.MediaBackendLocalFS,
		ObjectKey: objectKey, Size: 10, MimeType: "application/octet-stream",
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(media, local, 2)
	if _, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:shared-small", Offset: 2, Limit: 4}); err != nil || !ok {
		t.Fatalf("warm cache ok=%v err=%v", ok, err)
	}
	first, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:shared-small", Offset: 2, Limit: 4})
	if err != nil || !ok {
		t.Fatalf("first cache hit ok=%v err=%v", ok, err)
	}
	second, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:shared-small", Offset: 2, Limit: 4})
	if err != nil || !ok {
		t.Fatalf("second cache hit ok=%v err=%v", ok, err)
	}
	if len(first.Bytes) == 0 || &first.Bytes[0] != &second.Bytes[0] {
		t.Fatal("identical byte-cache ranges did not share immutable backing")
	}
	if cap(first.Bytes) != len(first.Bytes) || cap(second.Bytes) != len(second.Bytes) {
		t.Fatalf("range capacity was not clipped: first=%d/%d second=%d/%d", len(first.Bytes), cap(first.Bytes), len(second.Bytes), cap(second.Bytes))
	}
}

type blockingRangeBackend struct {
	BlobBackend
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	calls   int
}

func (b *blockingRangeBackend) GetRange(ctx context.Context, objectKey string, offset, limit int64) ([]byte, int64, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.once.Do(func() { close(b.entered) })
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	}
	return b.BlobBackend.GetRange(ctx, objectKey, offset, limit)
}

func TestGetFileSingleflightSharesImmutableRangeBacking(t *testing.T) {
	// A caller can be descheduled between signalling arrival and reaching the
	// singleflight group, so a loaded CI runner can occasionally produce a
	// second singleflight generation and a false negative. With a correct
	// implementation the sharing property holds on every attempt, while a
	// genuine regression fails deterministically every attempt, so retrying
	// separates scheduling jitter from a real bug.
	for attempt := 1; attempt <= 10; attempt++ {
		if err := exerciseGetFileSingleflightSharing(); err != nil {
			if attempt == 10 {
				t.Fatal(err)
			}
			continue
		}
		return
	}
}

func exerciseGetFileSingleflightSharing() error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "blobcache-singleflight")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	local, err := NewLocalFS(dir)
	if err != nil {
		return err
	}
	payload := bytes.Repeat([]byte("r"), blobBytesCacheMaxEntryBytes+1024)
	objectKey, err := local.Put(ctx, payload)
	if err != nil {
		return err
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:shared-range", Backend: domain.MediaBackendLocalFS,
		ObjectKey: objectKey, Size: int64(len(payload)), MimeType: "application/octet-stream",
	}); err != nil {
		return err
	}
	backend := &blockingRangeBackend{BlobBackend: local, entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewService(media, backend, 2)

	const callers = 16
	results := make(chan domain.FileChunk, callers)
	errs := make(chan error, callers)
	start := make(chan struct{})
	arrived := make(chan struct{}, callers)
	for range callers {
		go func() {
			<-start
			arrived <- struct{}{}
			chunk, found, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:shared-range", Offset: 17, Limit: 128 << 10})
			if err == nil && !found {
				err = context.Canceled
			}
			if err != nil {
				errs <- err
				return
			}
			results <- chunk
		}()
	}
	// Launch all callers, then wait for every caller to have been scheduled
	// into GetFile. The leader is confirmed blocked inside its singleflight
	// range read; releasing before every caller reaches the shared flight could
	// let a straggler start a second singleflight generation and observe its
	// own backing (a false negative), so we settle before unblocking the leader.
	close(start)
	for i := 0; i < callers; i++ {
		<-arrived
	}
	<-backend.entered
	for i := 0; i < 1024; i++ {
		runtime.Gosched()
	}
	time.Sleep(10 * time.Millisecond)
	close(backend.release)

	var first domain.FileChunk
	for i := 0; i < callers; i++ {
		select {
		case err := <-errs:
			return err
		case chunk := <-results:
			if i == 0 {
				first = chunk
				continue
			}
			if len(chunk.Bytes) == 0 || &chunk.Bytes[0] != &first.Bytes[0] {
				return fmt.Errorf("singleflight callers did not share immutable range backing")
			}
			if chunk.ImmutableRange == nil || chunk.ImmutableRange.RangeSHA256 != first.ImmutableRange.RangeSHA256 {
				return fmt.Errorf("singleflight callers did not share exact range digest")
			}
		}
	}
	backend.mu.Lock()
	calls := backend.calls
	backend.mu.Unlock()
	if calls != 1 {
		return fmt.Errorf("backend range calls = %d, want 1", calls)
	}
	return nil
}

func BenchmarkViewBlobBytes(b *testing.B) {
	data := bytes.Repeat([]byte("x"), 128<<10)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		view := viewBlobBytes(data, 0, int64(len(data)))
		if len(view) != len(data) {
			b.Fatal(len(view))
		}
	}
}

func TestGetFileLogsCacheHitMiss(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	objectKey, err := local.Put(ctx, []byte("0123456789"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{LocationKey: "doc:log", Backend: domain.MediaBackendLocalFS, ObjectKey: objectKey, Size: 10, MimeType: "application/octet-stream"}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	blobs := &countingBlobBackend{BlobBackend: local}
	core, logs := observer.New(zap.DebugLevel)
	svc := NewService(media, blobs, 2, WithLogger(zap.New(core)))

	if _, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:log", Offset: 0, Limit: 5}); err != nil || !ok {
		t.Fatalf("first getfile ok=%v err=%v", ok, err)
	}
	if _, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:log", Offset: 5, Limit: 5}); err != nil || !ok {
		t.Fatalf("second getfile ok=%v err=%v", ok, err)
	}

	entries := logs.FilterMessage("upload.getFile cache").All()
	if len(entries) != 2 {
		t.Fatalf("cache log entries = %d, want 2", len(entries))
	}
	first := entries[0].ContextMap()
	if first["source"] != "backend_fill_byte_cache" ||
		first["meta_cache_hit"] != false ||
		first["meta_cache_filled"] != true ||
		first["byte_cache_hit"] != false ||
		first["byte_cache_filled"] != true ||
		first["backend_read"] != true ||
		first["returned_bytes"] != int64(5) {
		t.Fatalf("first cache log = %#v, want backend fill miss", first)
	}
	second := entries[1].ContextMap()
	if second["source"] != "byte_cache" ||
		second["meta_cache_hit"] != true ||
		second["byte_cache_hit"] != true ||
		second["backend_read"] != false ||
		second["returned_bytes"] != int64(5) {
		t.Fatalf("second cache log = %#v, want byte cache hit", second)
	}
	if got := blobs.getRangeCalls.Load(); got != 1 {
		t.Fatalf("GetRange calls = %d, want only first miss to read backend", got)
	}
}

func TestGetFileDoesNotByteCacheLargeBlob(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	content := bytes.Repeat([]byte("x"), blobBytesCacheMaxEntryBytes+2)
	objectKey, err := local.Put(ctx, content)
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:large",
		Backend:     domain.MediaBackendLocalFS,
		ObjectKey:   objectKey,
		Size:        int64(len(content)),
		MimeType:    "application/octet-stream",
	}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(media, blobs, 2)

	for i := 0; i < 2; i++ {
		chunk, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:large", Offset: 1, Limit: 7})
		if err != nil || !ok {
			t.Fatalf("getfile %d ok=%v err=%v", i, ok, err)
		}
		if string(chunk.Bytes) != "xxxxxxx" {
			t.Fatalf("chunk %d = %q, want seven x bytes", i, chunk.Bytes)
		}
	}
	if got := blobs.getRangeCalls.Load(); got != 2 {
		t.Errorf("getRangeCalls = %d, want 2 (large blob is not byte cached)", got)
	}
}

func TestGetFileRejectsStoredBackendMismatch(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	objectKey, err := local.Put(ctx, []byte("must-not-fallback"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:mismatch",
		Backend:     domain.MediaBackendS3,
		ObjectKey:   objectKey,
		Size:        int64(len("must-not-fallback")),
	}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	svc := NewService(media, local, 2)
	if _, found, err := svc.GetFile(ctx, domain.FileDownloadRequest{
		LocationKey: "doc:mismatch", Limit: 128 << 10,
	}); err == nil || found {
		t.Fatalf("mismatched backend found=%v err=%v", found, err)
	}
}

func TestWarmCachesPreloadsStickerSetAndSmallBlobs(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	mainKey, err := local.Put(ctx, []byte("sticker"))
	if err != nil {
		t.Fatalf("put main: %v", err)
	}
	thumbKey, err := local.Put(ctx, []byte("thumb"))
	if err != nil {
		t.Fatalf("put thumb: %v", err)
	}
	media := newFakeMediaStore()
	doc := domain.Document{
		ID:         100,
		AccessHash: 1,
		DCID:       2,
		MimeType:   "application/x-tgsticker",
		Size:       7,
		Thumbs: []domain.PhotoSize{
			{Kind: domain.PhotoSizeKindDefault, Type: "m", W: 128, H: 128, Size: 5},
		},
	}
	if err := media.PutDocument(ctx, doc); err != nil {
		t.Fatalf("put doc: %v", err)
	}
	if err := media.PutFileBlob(ctx, domain.FileBlob{LocationKey: "doc:100", Backend: domain.MediaBackendLocalFS, ObjectKey: mainKey, Size: 7, MimeType: doc.MimeType}); err != nil {
		t.Fatalf("put main blob: %v", err)
	}
	if err := media.PutFileBlob(ctx, domain.FileBlob{LocationKey: "doc:100:m", Backend: domain.MediaBackendLocalFS, ObjectKey: thumbKey, Size: 5, MimeType: "image/jpeg"}); err != nil {
		t.Fatalf("put thumb blob: %v", err)
	}
	set := domain.StickerSet{
		ID:         200,
		AccessHash: 2,
		ShortName:  "pack",
		Title:      "Pack",
		Kind:       domain.StickerSetKindStickers,
		Count:      1,
		DocumentIDs: []int64{
			doc.ID,
		},
	}
	if err := media.PutStickerSet(ctx, set); err != nil {
		t.Fatalf("put set: %v", err)
	}
	counting := &countingMediaStore{fakeMediaStore: media}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(counting, blobs, 2)

	stats, err := svc.WarmCaches(ctx)
	if err != nil {
		t.Fatalf("warm caches: %v", err)
	}
	if stats.StickerSets != 1 || stats.Documents != 1 || stats.Blobs != 2 {
		t.Fatalf("warm stats = %+v, want 1 set, 1 doc, 2 blobs", stats)
	}
	blobs.getRangeCalls.Store(0)
	chunk, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{LocationKey: "doc:100", Offset: 0, Limit: 7})
	if err != nil || !ok {
		t.Fatalf("getfile ok=%v err=%v", ok, err)
	}
	if string(chunk.Bytes) != "sticker" {
		t.Fatalf("chunk = %q, want sticker", chunk.Bytes)
	}
	if got := blobs.getRangeCalls.Load(); got != 0 {
		t.Fatalf("prewarmed blob should be served from byte cache, GetRange calls = %d", got)
	}

	gotSet, docs, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil || !found {
		t.Fatalf("resolve found=%v err=%v", found, err)
	}
	if gotSet.ID != set.ID || len(docs) != 1 || docs[0].ID != doc.ID {
		t.Fatalf("resolve = set %+v docs %+v", gotSet, docs)
	}
	if counting.getSetByIDCalls != 0 {
		t.Fatalf("ResolveStickerSet should hit full-set cache, GetStickerSetByID calls = %d", counting.getSetByIDCalls)
	}
	docs[0].ID = 999
	_, docsAgain, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil || !found || docsAgain[0].ID != doc.ID {
		t.Fatalf("cached docs were mutated: found=%v err=%v docs=%+v", found, err, docsAgain)
	}
}

// blockedStickerDocumentsStore suspends the first query's read snapshot, letting an RPC complete a write before the warm-up publishes.
type blockedStickerDocumentsStore struct {
	*fakeMediaStore
	loaded  chan struct{}
	release chan struct{}
	blocked atomic.Bool
}

func (s *blockedStickerDocumentsStore) GetDocuments(ctx context.Context, ids []int64) ([]domain.Document, error) {
	docs, err := s.fakeMediaStore.GetDocuments(ctx, ids)
	if s.blocked.CompareAndSwap(false, true) {
		close(s.loaded)
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return docs, err
}

func TestStickerSetCacheRejectsConcurrentStaleLoads(t *testing.T) {
	for _, loader := range []string{"warmup", "lookup"} {
		for _, mutation := range []string{"delete", "rename"} {
			t.Run(loader+"/"+mutation, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				media := &blockedStickerDocumentsStore{
					fakeMediaStore: newFakeMediaStore(),
					loaded:         make(chan struct{}),
					release:        make(chan struct{}),
				}
				if err := media.PutDocument(ctx, domain.Document{ID: 1000001}); err != nil {
					t.Fatal(err)
				}
				if err := media.PutStickerSet(ctx, domain.StickerSet{
					ID: 1, AccessHash: 2, CreatorUserID: 42, ShortName: "TestSet", Title: "Original",
					Kind: domain.StickerSetKindStickers, DocumentIDs: []int64{1000001},
				}); err != nil {
					t.Fatal(err)
				}
				svc := NewService(media, nil, 2)
				ref := domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: 1, AccessHash: 2}
				done := make(chan error, 1)
				go func() {
					if loader == "warmup" {
						_, err := svc.WarmCaches(ctx)
						done <- err
					} else {
						_, _, _, err := svc.ResolveStickerSet(ctx, ref)
						done <- err
					}
				}()
				select {
				case <-media.loaded:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}

				if mutation == "delete" {
					// An invalidation while the cache is still empty must also block an in-flight query.
					if _, err := svc.AdminDeleteStickerSet(ctx, 1); err != nil {
						t.Fatal(err)
					}
				} else if _, _, err := svc.RenameStickerSet(ctx, 42, ref, "Renamed"); err != nil {
					t.Fatal(err)
				}
				close(media.release)
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}

				for _, lookup := range []domain.StickerSetRef{ref, {Kind: domain.StickerSetRefByShortName, ShortName: "TestSet"}} {
					set, _, found, err := svc.ResolveStickerSet(ctx, lookup)
					if err != nil {
						t.Fatal(err)
					}
					if mutation == "delete" && found {
						t.Fatalf("stale load resurrected deleted set: %+v", set)
					}
					if mutation == "rename" && (!found || set.Title != "Renamed") {
						t.Fatalf("stale load overwrote renamed set: set=%+v found=%v", set, found)
					}
				}
			})
		}
	}
}
