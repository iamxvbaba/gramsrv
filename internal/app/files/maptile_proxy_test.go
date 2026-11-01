package files

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakePNG is a minimal valid PNG header + padding (it only needs to pass magic-number detection, not be decodable).
var fakePNG = append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, bytes.Repeat([]byte{0x42}, 64)...)

func newProxyTestService(t *testing.T, handler http.Handler) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	s := NewService(nil, nil, 2, WithMapboxMapTiles("test-token", t.TempDir()))
	if s.mapTiles == nil {
		t.Fatal("map tile proxy not configured")
	}
	s.mapTiles.baseURL = srv.URL
	return s, srv
}

func TestGeoMapTileProxyFetchesAndCaches(t *testing.T) {
	var calls atomic.Int32
	var lastPath, lastQuery string
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		lastPath = r.URL.Path
		lastQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))

	first, mime := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	if !bytes.Equal(first, fakePNG) {
		t.Fatal("first fetch did not return upstream bytes")
	}
	// Mapbox form: /styles/v1/mapbox/streets-v12/static/{long},{lat},{zoom}/{w}x{h}@2x
	if !strings.Contains(lastPath, "/static/116.40740,39.90420,15/256x128@2x") {
		t.Fatalf("unexpected upstream path: %s", lastPath)
	}
	if !strings.Contains(lastQuery, "access_token=test-token") {
		t.Fatalf("missing access token in query: %s", lastQuery)
	}

	// The second time (including repeated chunk reads) must go through the disk cache without triggering another upstream request.
	second, _ := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if !bytes.Equal(first, second) {
		t.Fatal("cached bytes differ from first fetch")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (second hit must be cached)", got)
	}
}

func TestGeoMapTileProxyScaleOneOmitsRetina(t *testing.T) {
	var lastPath string
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastPath = r.URL.Path
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	s.GeoMapTile(1.5, 2.5, 100, 100, 16, 1)
	if strings.Contains(lastPath, "@2x") {
		t.Fatalf("scale=1 must not request retina: %s", lastPath)
	}
}

func TestGeoMapTileProxyFallsBackToPlaceholder(t *testing.T) {
	var calls atomic.Int32
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))

	data, mime := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if len(data) == 0 || mime != "image/png" {
		t.Fatalf("fallback placeholder missing: len=%d mime=%q", len(data), mime)
	}
	// Placeholder determinism: the fallback path must be byte-identical to the pure placeholder service (chunk resend consistency).
	plain := NewService(nil, nil, 2)
	expected, _ := plain.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if !bytes.Equal(data, expected) {
		t.Fatal("fallback placeholder differs from deterministic rendering")
	}

	// Negative cache: subsequent chunk requests after a failure must not keep hitting upstream.
	again, _ := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if !bytes.Equal(again, expected) {
		t.Fatal("placeholder must stay byte-identical during failure backoff")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (failure must be negative-cached)", got)
	}

	// After the negative cache expires, retry is allowed and the real map is restored.
	s.mapTiles.mu.Lock()
	for k := range s.mapTiles.failures {
		s.mapTiles.failures[k] = time.Now().Add(-2 * mapTileFailureTTL)
	}
	s.mapTiles.mu.Unlock()
	// Upstream recovered.
	s.mapTiles.baseURL = newRecoveredUpstream(t)
	recovered, _ := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if !bytes.Equal(recovered, fakePNG) {
		t.Fatal("proxy must recover after failure TTL expires")
	}
}

func newRecoveredUpstream(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// When the disk write fails, bytes must go into the memory fallback cache: sequential chunks no longer hit upstream one by one and the bytes stay consistent throughout.
func TestGeoMapTileProxyStoreFailureServesFromMemory(t *testing.T) {
	var calls atomic.Int32
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	// Point the cache directory path at a regular file → MkdirAll/the disk write must fail.
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.mapTiles.dir = blocked

	first, _ := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	second, _ := s.GeoMapTile(39.9042, 116.4074, 256, 128, 15, 2)
	if !bytes.Equal(first, fakePNG) || !bytes.Equal(second, fakePNG) {
		t.Fatal("store-failure path must keep serving upstream bytes")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1 (memory cache must absorb later chunks)", got)
	}
}

// Fetch dimensions are quantized to 32px steps, narrowing the cache key space the client can mint.
func TestGeoMapTileProxyQuantizesFetchDimensions(t *testing.T) {
	var mu sync.Mutex
	var lastPath string
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastPath = r.URL.Path
		mu.Unlock()
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	s.GeoMapTile(1.5, 2.5, 100, 50, 15, 2)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(lastPath, "/128x64@2x") {
		t.Fatalf("dimensions not quantized to 32px steps: %s", lastPath)
	}
}

// The disk cache evicts oldest-first by mtime once over the total limit.
func TestGeoMapTileProxyDiskSweepEvictsOldest(t *testing.T) {
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	s.mapTiles.maxDiskBytes = int64(len(fakePNG)*2 + 8) // fits 2 tiles; the 3rd triggers eviction
	for i := 0; i < 3; i++ {
		s.GeoMapTile(10+float64(i), 20, 128, 128, 15, 1)
		time.Sleep(20 * time.Millisecond) // ensure the mtimes are distinguishable
	}
	entries, err := os.ReadDir(s.mapTiles.dir)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	count := 0
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
		count++
	}
	if count >= 3 || total > s.mapTiles.maxDiskBytes {
		t.Fatalf("sweep did not evict: files=%d total=%d max=%d", count, total, s.mapTiles.maxDiskBytes)
	}
}

// Global fetch rate limit: a new key over the limit falls back to the placeholder and does not hit upstream.
func TestGeoMapTileProxyFetchRateLimit(t *testing.T) {
	var calls atomic.Int32
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fakePNG)
	}))
	// Pre-fill the sliding window.
	s.mapTiles.mu.Lock()
	now := time.Now()
	for i := 0; i < mapTileFetchRateLimit; i++ {
		s.mapTiles.fetchTimes = append(s.mapTiles.fetchTimes, now)
	}
	s.mapTiles.mu.Unlock()

	data, mime := s.GeoMapTile(33.3, 44.4, 128, 128, 15, 1)
	plain := NewService(nil, nil, 2)
	expected, _ := plain.GeoMapTile(33.3, 44.4, 128, 128, 15, 1)
	if !bytes.Equal(data, expected) || mime != "image/png" {
		t.Fatal("rate-limited request must fall back to deterministic placeholder")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 when rate limited", got)
	}
}

func TestGeoMapTileProxyRejectsNonImageBody(t *testing.T) {
	s, _ := newProxyTestService(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html>not a map</html>"))
	}))
	data, mime := s.GeoMapTile(10, 20, 128, 128, 15, 1)
	plain := NewService(nil, nil, 2)
	expected, _ := plain.GeoMapTile(10, 20, 128, 128, 15, 1)
	if !bytes.Equal(data, expected) || mime != "image/png" {
		t.Fatal("non-image upstream body must fall back to placeholder")
	}
}
