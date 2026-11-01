package loadtest

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	filesapp "telesrv/internal/app/files"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
)

// TestMediaDownloadRangeBaseline load-tests the files.Service.GetFile range read
// path behind upload.getFile using real PostgreSQL metadata + a localfs blob
// backend. Skipped by default; run after setting TELESRV_MEDIA_DOWNLOAD_LOAD=1 and
// TELESRV_TEST_POSTGRES_DSN.
func TestMediaDownloadRangeBaseline(t *testing.T) {
	if os.Getenv("TELESRV_MEDIA_DOWNLOAD_LOAD") != "1" {
		t.Skip("set TELESRV_MEDIA_DOWNLOAD_LOAD=1 to run media download baseline")
	}
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run media download baseline")
	}

	ctx := context.Background()
	if err := postgres.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := postgres.Open(ctx, dsn, postgres.WithMaxConns(envInt("TELESRV_MEDIA_LOAD_POOL_CONNS", 16)))
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(pool.Close)

	blobDir := os.Getenv("TELESRV_MEDIA_LOAD_BLOB_DIR")
	if blobDir == "" {
		blobDir = t.TempDir()
	}
	blobs, err := filesapp.NewLocalFS(blobDir)
	if err != nil {
		t.Fatalf("new localfs: %v", err)
	}
	mediaStore := postgres.NewMediaStore(pool)
	svc := filesapp.NewService(mediaStore, blobs, 2, filesapp.WithVideoThumbnailer(nil))

	runID := time.Now().UnixNano()
	bodyKey := fmt.Sprintf("loadtest:doc:%d", runID)
	thumbKey := fmt.Sprintf("loadtest:doc:%d:m", runID)
	bodyBytes := int64(envInt("TELESRV_MEDIA_LOAD_BLOB_BYTES", 64<<20))
	if bodyBytes < 1<<20 {
		bodyBytes = 1 << 20
	}
	chunkSize := envInt("TELESRV_MEDIA_LOAD_CHUNK_BYTES", 512<<10)
	if chunkSize < 1 {
		chunkSize = 512 << 10
	}
	thumbBytes := envInt("TELESRV_MEDIA_LOAD_THUMB_BYTES", 96<<10)
	if thumbBytes < 1 {
		thumbBytes = 96 << 10
	}

	bodyObject, bodySize, bodySHA, err := blobs.PutReader(ctx, newPatternReader(bodyBytes))
	if err != nil {
		t.Fatalf("put body blob: %v", err)
	}
	thumbObject, thumbSize, thumbSHA, err := blobs.PutReader(ctx, newPatternReader(int64(thumbBytes)))
	if err != nil {
		t.Fatalf("put thumb blob: %v", err)
	}
	if err := mediaStore.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: bodyKey,
		Backend:     domain.MediaBackend(blobs.Name()),
		ObjectKey:   bodyObject,
		Size:        bodySize,
		SHA256:      bodySHA,
		MimeType:    "video/mp4",
	}); err != nil {
		t.Fatalf("put body file blob: %v", err)
	}
	if err := mediaStore.PutFileBlob(ctx, domain.FileBlob{
		LocationKey: thumbKey,
		Backend:     domain.MediaBackend(blobs.Name()),
		ObjectKey:   thumbObject,
		Size:        thumbSize,
		SHA256:      thumbSHA,
		MimeType:    "image/jpeg",
	}); err != nil {
		t.Fatalf("put thumb file blob: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM file_blobs WHERE location_key IN ($1, $2)", bodyKey, thumbKey)
	})

	concurrency := envInt("TELESRV_MEDIA_LOAD_CONCURRENCY", 16)
	if concurrency < 1 {
		concurrency = 1
	}
	requests := envInt("TELESRV_MEDIA_LOAD_REQUESTS", 2000)
	if requests < 1 {
		requests = 1
	}
	thumbRequests := envInt("TELESRV_MEDIA_LOAD_THUMB_REQUESTS", 500)
	if thumbRequests < 0 {
		thumbRequests = 0
	}

	bodyResult := runGetFileLoad(ctx, t, svc, bodyKey, bodyBytes, chunkSize, concurrency, requests)
	t.Logf("media body range load: requests=%d concurrency=%d chunk=%d blob=%d wall=%s throughput=%.1f req/s %.1f MiB/s p50=%s p95=%s p99=%s errors=%d",
		requests,
		concurrency,
		chunkSize,
		bodyBytes,
		bodyResult.wall,
		float64(requests)/bodyResult.wall.Seconds(),
		float64(bodyResult.bytes)/(1024*1024)/bodyResult.wall.Seconds(),
		bodyResult.p50,
		bodyResult.p95,
		bodyResult.p99,
		bodyResult.errors,
	)
	if bodyResult.errors > 0 {
		t.Fatalf("body range load errors=%d", bodyResult.errors)
	}

	if thumbRequests > 0 {
		thumbResult := runGetFileLoad(ctx, t, svc, thumbKey, thumbSize, int(thumbSize), max(1, concurrency/2), thumbRequests)
		t.Logf("media thumb load: requests=%d concurrency=%d bytes=%d wall=%s throughput=%.1f req/s p50=%s p95=%s p99=%s errors=%d",
			thumbRequests,
			max(1, concurrency/2),
			thumbSize,
			thumbResult.wall,
			float64(thumbRequests)/thumbResult.wall.Seconds(),
			thumbResult.p50,
			thumbResult.p95,
			thumbResult.p99,
			thumbResult.errors,
		)
		if thumbResult.errors > 0 {
			t.Fatalf("thumb load errors=%d", thumbResult.errors)
		}
	}
}

type getFileLoadResult struct {
	wall   time.Duration
	bytes  int64
	errors int64
	p50    time.Duration
	p95    time.Duration
	p99    time.Duration
}

func runGetFileLoad(ctx context.Context, t *testing.T, svc *filesapp.Service, locationKey string, totalSize int64, chunkSize, concurrency, requests int) getFileLoadResult {
	t.Helper()
	perWorkerLat := make([][]time.Duration, concurrency)
	var counter atomic.Int64
	var readBytes atomic.Int64
	var errs atomic.Int64
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			lat := make([]time.Duration, 0, requests/concurrency+1)
			for {
				n := int(counter.Add(1))
				if n > requests {
					break
				}
				offset := int64((n - 1) * chunkSize)
				if totalSize > 0 {
					offset %= totalSize
				}
				t0 := time.Now()
				chunk, ok, err := svc.GetFile(ctx, domain.FileDownloadRequest{
					LocationKey: locationKey,
					Offset:      offset,
					Limit:       chunkSize,
				})
				lat = append(lat, time.Since(t0))
				if err != nil || !ok {
					errs.Add(1)
					continue
				}
				readBytes.Add(int64(len(chunk.Bytes)))
			}
			perWorkerLat[worker] = lat
		}(w)
	}
	wg.Wait()
	wall := time.Since(start)
	latencies := flattenDurations(perWorkerLat)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return getFileLoadResult{
		wall:   wall,
		bytes:  readBytes.Load(),
		errors: errs.Load(),
		p50:    percentile(latencies, 50),
		p95:    percentile(latencies, 95),
		p99:    percentile(latencies, 99),
	}
}

func flattenDurations(values [][]time.Duration) []time.Duration {
	var total int
	for _, v := range values {
		total += len(v)
	}
	out := make([]time.Duration, 0, total)
	for _, v := range values {
		out = append(out, v...)
	}
	return out
}

type patternReader struct {
	remaining int64
	next      byte
}

func newPatternReader(n int64) *patternReader {
	return &patternReader{remaining: n, next: 17}
}

func (r *patternReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	for i := range p {
		p[i] = r.next
		r.next += 31
	}
	r.remaining -= int64(len(p))
	return len(p), nil
}
