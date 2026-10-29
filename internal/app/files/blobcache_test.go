package files

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	// 容量 2：刚 access 过 a，再 put c 应淘汰最久未用的 b。
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

func TestBlobBytesCacheConcurrentSharedReadOnlyRanges(t *testing.T) {
	cache := newBlobBytesCache(2 << 20)
	cache.putOwned("blob", bytes.Repeat([]byte{'a'}, 64<<10))
	var wg sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				shared, ok := cache.getShared("blob")
				if !ok || len(shared) != 64<<10 {
					t.Errorf("shared cache read: ok=%v size=%d", ok, len(shared))
					return
				}
				view := viewBlobBytes(shared, 0, int64(len(shared)))
				if len(view) == 0 || &view[0] != &shared[0] {
					t.Error("range view copied immutable cache backing")
					return
				}
				if cap(view) != len(view) {
					t.Error("range view permits append into immutable cache backing")
					return
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		cache.putOwned("blob", bytes.Repeat([]byte{byte('a' + i%2)}, 64<<10))
	}
	wg.Wait()
}

// countingMediaStore 统计 GetFileBlob 次数，验证元数据缓存命中后不再查 PG。
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
	// WarmCaches 并发读 blob，计数必须是原子的。
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

	// 第一次：查 PG 一次并填充元数据缓存；小 blob 读整块进字节缓存后返回 [0,5)。
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
	// 后续请求：同 location 命中元数据与字节缓存；[5,10) 直接从内存切片。
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
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("r"), blobBytesCacheMaxEntryBytes+1024)
	objectKey, err := local.Put(ctx, payload)
	if err != nil {
		t.Fatal(err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:shared-range", Backend: domain.MediaBackendLocalFS,
		ObjectKey: objectKey, Size: int64(len(payload)), MimeType: "application/octet-stream",
	}); err != nil {
		t.Fatal(err)
	}
	backend := &blockingRangeBackend{BlobBackend: local, entered: make(chan struct{}), release: make(chan struct{})}
	svc := NewService(media, backend, 2)

	const callers = 16
	results := make(chan domain.FileChunk, callers)
	errs := make(chan error, callers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
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
	ready.Wait()
	close(start)
	<-backend.entered
	// Keep the leader blocked long enough for every simultaneously released caller
	// to join the same flight. Releasing as soon as the leader enters makes this a
	// scheduler test: late callers correctly start a new flight and own new backing.
	time.Sleep(25 * time.Millisecond)
	close(backend.release)

	var first domain.FileChunk
	for i := 0; i < callers; i++ {
		select {
		case err := <-errs:
			t.Fatal(err)
		case chunk := <-results:
			if i == 0 {
				first = chunk
				continue
			}
			if len(chunk.Bytes) == 0 || &chunk.Bytes[0] != &first.Bytes[0] {
				t.Fatal("singleflight callers did not share immutable range backing")
			}
			if chunk.ImmutableRange == nil || chunk.ImmutableRange.RangeSHA256 != first.ImmutableRange.RangeSHA256 {
				t.Fatal("singleflight callers did not share exact range digest")
			}
		}
	}
	backend.mu.Lock()
	calls := backend.calls
	backend.mu.Unlock()
	if calls != 1 {
		t.Fatalf("backend range calls = %d, want 1", calls)
	}
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

func TestGetFileHashesUsesBatchedSHA256Ranges(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	content := make([]byte, DefaultFileHashPartSize*11+5)
	for i := range content {
		content[i] = byte(i % 251)
	}
	objectKey, size, sum, err := local.PutReader(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:hashes",
		Backend:     domain.MediaBackendLocalFS,
		ObjectKey:   objectKey,
		Size:        size,
		SHA256:      sum,
		MimeType:    "application/octet-stream",
	}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(media, blobs, 2)

	first, found, err := svc.GetFileHashes(ctx, domain.FileHashRequest{LocationKey: "doc:hashes", Offset: 0})
	if err != nil || !found {
		t.Fatalf("first hashes found=%v err=%v", found, err)
	}
	if len(first) != DefaultFileHashRangeSize {
		t.Fatalf("first hashes = %d, want %d", len(first), DefaultFileHashRangeSize)
	}
	wantFirst := sha256.Sum256(content[:DefaultFileHashPartSize])
	if first[0].Offset != 0 || first[0].Limit != DefaultFileHashPartSize || !bytes.Equal(first[0].Hash, wantFirst[:]) {
		t.Fatalf("first hash = %+v, want offset 0 limit %d sha256(first part)", first[0], DefaultFileHashPartSize)
	}
	if first[len(first)-1].Offset != int64((DefaultFileHashRangeSize-1)*DefaultFileHashPartSize) {
		t.Fatalf("last first-batch offset = %d", first[len(first)-1].Offset)
	}

	second, found, err := svc.GetFileHashes(ctx, domain.FileHashRequest{
		LocationKey: "doc:hashes",
		Offset:      int64(DefaultFileHashRangeSize * DefaultFileHashPartSize),
	})
	if err != nil || !found {
		t.Fatalf("second hashes found=%v err=%v", found, err)
	}
	if len(second) != 2 {
		t.Fatalf("second hashes = %d, want 2", len(second))
	}
	if second[0].Offset != int64(DefaultFileHashRangeSize*DefaultFileHashPartSize) || second[0].Limit != DefaultFileHashPartSize {
		t.Fatalf("second[0] = %+v", second[0])
	}
	tail := content[DefaultFileHashPartSize*11:]
	wantTail := sha256.Sum256(tail)
	if second[1].Limit != len(tail) || !bytes.Equal(second[1].Hash, wantTail[:]) {
		t.Fatalf("tail hash = %+v, want len %d sha256(tail)", second[1], len(tail))
	}

	empty, found, err := svc.GetFileHashes(ctx, domain.FileHashRequest{
		LocationKey: "doc:hashes",
		Offset:      int64(len(content)),
	})
	if err != nil || !found || len(empty) != 0 {
		t.Fatalf("eof hashes len=%d found=%v err=%v", len(empty), found, err)
	}
	if got := blobs.getRangeCalls.Load(); got != 2 {
		t.Fatalf("GetRange calls = %d, want 2", got)
	}
}

func TestGetFileHashesUsesStoredWholeSHAForSinglePartBlob(t *testing.T) {
	ctx := context.Background()
	local, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatalf("local fs: %v", err)
	}
	content := []byte("tiny file")
	objectKey, _, sum, err := local.PutReader(ctx, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	media := newFakeMediaStore()
	if err := media.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: "doc:tiny",
		Backend:     domain.MediaBackendLocalFS,
		ObjectKey:   objectKey,
		Size:        int64(len(content)),
		SHA256:      sum,
	}); err != nil {
		t.Fatalf("put blob: %v", err)
	}
	blobs := &countingBlobBackend{BlobBackend: local}
	svc := NewService(media, blobs, 2)

	hashes, found, err := svc.GetFileHashes(ctx, domain.FileHashRequest{LocationKey: "doc:tiny"})
	if err != nil || !found {
		t.Fatalf("hashes found=%v err=%v", found, err)
	}
	if len(hashes) != 1 || hashes[0].Limit != len(content) || !bytes.Equal(hashes[0].Hash, sum) {
		t.Fatalf("hashes = %+v, want stored whole sha", hashes)
	}
	if got := blobs.getRangeCalls.Load(); got != 0 {
		t.Fatalf("GetRange calls = %d, want 0", got)
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

func TestResolveStickerSetCachesOnFirstUse(t *testing.T) {
	ctx := context.Background()
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
	svc := NewService(counting, nil, 2)

	gotSet, docs, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil || !found {
		t.Fatalf("resolve found=%v err=%v", found, err)
	}
	if gotSet.ID != set.ID || len(docs) != 1 || docs[0].ID != doc.ID {
		t.Fatalf("resolve = set %+v docs %+v", gotSet, docs)
	}
	if counting.getSetByIDCalls != 1 {
		t.Fatalf("first ResolveStickerSet calls = %d, want one lazy store load", counting.getSetByIDCalls)
	}
	docs[0].ID = 999
	_, docsAgain, found, err := svc.ResolveStickerSet(ctx, domain.StickerSetRef{Kind: domain.StickerSetRefByID, ID: set.ID})
	if err != nil || !found || docsAgain[0].ID != doc.ID {
		t.Fatalf("cached docs were mutated: found=%v err=%v docs=%+v", found, err, docsAgain)
	}
	if counting.getSetByIDCalls != 1 {
		t.Fatalf("second ResolveStickerSet calls = %d, want cache hit", counting.getSetByIDCalls)
	}
}

// blockedStickerDocumentsStore 挂起第一次查询的已读快照，允许 RPC 在该查询发布缓存前完成写入。
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
	for _, mutation := range []string{"delete", "rename"} {
		t.Run(mutation, func(t *testing.T) {
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
				_, _, _, err := svc.ResolveStickerSet(ctx, ref)
				done <- err
			}()
			select {
			case <-media.loaded:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}

			if mutation == "delete" {
				// 缓存尚为空时的失效也必须挡住在途查询。
				if _, err := svc.AdminDeleteStickerSet(ctx, 1); err != nil {
					t.Fatal(err)
				}
			} else if _, _, err := svc.RenameStickerSet(ctx, 42, ref, "Renamed", testStickerSetDeliveryEffects); err != nil {
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
