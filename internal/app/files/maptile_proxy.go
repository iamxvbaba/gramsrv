package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// This file implements the real data source for map thumbnails: when upload.getWebFile hits a geo
// coordinate, the server proxies the Mapbox Static Images API to fetch a static map and cache it to
// disk. The client downloads the same file in offset/limit chunks and the bytes must be consistent
// throughout, therefore:
//   - a successful fetch is atomically written to disk first (temp+rename), and all chunks are read
//     from the cache file;
//   - if the disk write fails the bytes go into a short-TTL in-process fallback cache — chunks are
//     sequential requests that singleflight cannot merge, so without this layer it would degrade to
//     one external fetch per chunk with no byte-for-byte consistency guarantee across two fetches;
//   - a failed fetch records a short-TTL negative cache, during which the key goes straight to the
//     deterministic placeholder, avoiding a flip between "real map/placeholder" across chunks of the
//     same download and avoiding one external request per chunk while the upstream is down.
//
// Resource bounds (the key space is made of client-controllable coordinate×size combinations and
// must be defended):
//   - the fetch size is quantized to 32px steps, narrowing the key space and cache cardinality;
//   - the disk cache has a total limit, evicting oldest-first by mtime when over (also reclaiming
//     .tmp files left by crashes);
//   - upstream fetches have a global rate limit, falling back to the placeholder for that request
//     when over, preventing a malicious enumeration from burning the Mapbox quota.
//
// The map does not embed a location pin: TDesktop (historyMapPoint icon) and DrKLO both overlay the
// marker client-side, consistent with the official static image behavior.

const (
	mapTileFetchTimeout   = 15 * time.Second
	mapTileMaxFetchBytes  = 8 << 20 // defensive limit; a 640x640@2x PNG is far smaller
	mapTileFailureTTL     = time.Minute
	mapboxStaticStyleBase = "/styles/v1/mapbox/streets-v12/static"

	// mapTileEdgeStep is the quantization step for the fetch size (rounded up); the client receives a slightly larger image and scales it adaptively.
	mapTileEdgeStep = 32
	// mapTileMemTTL/mapTileMemMaxBytes are the retention period and total limit of the fallback byte cache used when the disk write fails.
	mapTileMemTTL      = 10 * time.Minute
	mapTileMemMaxBytes = 32 << 20
	// mapTileDiskMaxBytes is the total disk cache limit; over the limit it evicts by mtime down to 90%.
	mapTileDiskMaxBytes = int64(256 << 20)
	// mapTileFetchRateLimit is the global per-minute upstream fetch limit (prevents a malicious coordinate enumeration from burning the quota).
	mapTileFetchRateLimit  = 120
	mapTileFetchRateWindow = time.Minute
	// mapTileTmpMaxAge is the reclamation threshold for .tmp files left by crashes.
	mapTileTmpMaxAge = time.Hour
)

type memTileEntry struct {
	data []byte
	at   time.Time
}

type mapTileProxy struct {
	token        string
	baseURL      string // defaults to https://api.mapbox.com; tests inject an httptest address
	dir          string
	client       *http.Client
	log          *zap.Logger
	maxDiskBytes int64

	group   singleflight.Group
	sweepMu sync.Mutex

	mu         sync.Mutex
	failures   map[string]time.Time // key → failure time (negative cache)
	memTiles   map[string]memTileEntry
	memBytes   int
	fetchTimes []time.Time // upstream fetch sliding window
}

// WithMapboxMapTiles enables the Mapbox static map proxy; it is not enabled when token is empty
// (keeping the placeholder). The logger is injected uniformly by NewService after all Options are
// applied.
func WithMapboxMapTiles(token, cacheDir string) Option {
	return func(s *Service) {
		if token == "" || cacheDir == "" {
			return
		}
		s.mapTiles = &mapTileProxy{
			token:        token,
			baseURL:      "https://api.mapbox.com",
			dir:          cacheDir,
			client:       &http.Client{Timeout: mapTileFetchTimeout},
			maxDiskBytes: mapTileDiskMaxBytes,
			failures:     make(map[string]time.Time),
			memTiles:     make(map[string]memTileEntry),
		}
	}
}

// tile returns the static map bytes for (lat,long,zoom,w,h,scale); the inputs must already be clamped.
func (p *mapTileProxy) tile(lat, long float64, w, h, zoom, scale int) ([]byte, string, error) {
	// Mapbox static images only support @2x; scale 3 is also fetched at 2x (the client scales adaptively to the target size).
	retina := ""
	if scale >= 2 {
		retina = "@2x"
	}
	// Size quantization narrows the key space the client can mint; the client scales the slightly larger image adaptively to the target rectangle.
	w = quantizeTileEdge(w)
	h = quantizeTileEdge(h)
	key := fmt.Sprintf("v1-%.5f-%.5f-%d-%dx%d%s", lat, long, zoom, w, h, retina)
	path := p.cachePath(key)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, mapTileMime(data), nil
	}
	if data, ok := p.cachedMemTile(key); ok {
		return data, mapTileMime(data), nil
	}
	if p.recentlyFailed(key) {
		return nil, "", errors.New("map tile fetch in failure backoff")
	}
	v, err, _ := p.group.Do(key, func() (any, error) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return data, nil
		}
		if data, ok := p.cachedMemTile(key); ok {
			return data, nil
		}
		if !p.allowFetch() {
			// Global fetch rate limit: the negative cache keeps this key stably on the placeholder for a short time (keeping chunk bytes consistent) without hitting upstream.
			p.markFailed(key)
			return nil, errors.New("map tile fetch rate limited")
		}
		data, err := p.fetch(lat, long, w, h, zoom, retina)
		if err != nil {
			p.markFailed(key)
			return nil, err
		}
		if err := p.store(path, data); err != nil {
			// Chunks are sequential requests that singleflight cannot merge for later chunks; the
			// bytes must go into the memory cache as a fallback, otherwise a persistent disk failure
			// would degrade to one external fetch per chunk with no consistency guarantee.
			p.rememberMemTile(key, data)
			p.log.Warn("map tile cache write failed, serving from memory", zap.Error(err), zap.String("key", key))
		} else {
			p.sweepDisk()
		}
		return data, nil
	})
	if err != nil {
		return nil, "", err
	}
	data := v.([]byte)
	return data, mapTileMime(data), nil
}

// quantizeTileEdge rounds the edge length up to a multiple of mapTileEdgeStep (the caller has already clamped to 16..1024).
func quantizeTileEdge(v int) int {
	q := ((v + mapTileEdgeStep - 1) / mapTileEdgeStep) * mapTileEdgeStep
	if q > mapTileMaxEdge {
		return mapTileMaxEdge
	}
	return q
}

// fetch requests the Mapbox Static Images API. Note the URL coordinate order is {long},{lat}.
func (p *mapTileProxy) fetch(lat, long float64, w, h, zoom int, retina string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s%s/%.5f,%.5f,%d/%dx%d%s?access_token=%s&attribution=false&logo=false",
		p.baseURL, mapboxStaticStyleBase, long, lat, zoom, w, h, retina, url.QueryEscape(p.token))
	// The RPC ctx is not passed through: the singleflight result is shared by concurrent chunks, and a single caller's cancellation should not take down the whole fetch.
	ctx, cancel := context.WithTimeout(context.Background(), mapTileFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build map tile request: %w", err)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		// Transport-layer errors embed the full URL (including access_token), so redact before propagating/logging.
		return nil, fmt.Errorf("fetch map tile: %s", p.redactToken(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch map tile: upstream status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, mapTileMaxFetchBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read map tile body: %w", err)
	}
	if len(data) == 0 || len(data) > mapTileMaxFetchBytes {
		return nil, fmt.Errorf("map tile body size invalid: %d", len(data))
	}
	if mime := mapTileMime(data); mime == "" {
		return nil, errors.New("map tile body is not an image")
	}
	return data, nil
}

func (p *mapTileProxy) cachePath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(p.dir, hex.EncodeToString(sum[:])+".img")
}

func (p *mapTileProxy) store(path string, data []byte) error {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(p.dir, "tile-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}

// redactToken replaces the access token in error text (raw or URL-escaped form) with a placeholder.
func (p *mapTileProxy) redactToken(s string) string {
	if p.token == "" {
		return s
	}
	s = strings.ReplaceAll(s, p.token, "***")
	if escaped := url.QueryEscape(p.token); escaped != p.token {
		s = strings.ReplaceAll(s, escaped, "***")
	}
	return s
}

// cachedMemTile returns bytes from the fallback cache used when the disk write fails (within the TTL).
func (p *mapTileProxy) cachedMemTile(key string) ([]byte, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.memTiles[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.at) > mapTileMemTTL {
		p.memBytes -= len(entry.data)
		delete(p.memTiles, key)
		return nil, false
	}
	return entry.data, true
}

// rememberMemTile stashes bytes when the disk write fails; over the total limit it evicts oldest-first.
func (p *mapTileProxy) rememberMemTile(key string, data []byte) {
	if len(data) > mapTileMemMaxBytes {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for k, entry := range p.memTiles {
		if now.Sub(entry.at) > mapTileMemTTL {
			p.memBytes -= len(entry.data)
			delete(p.memTiles, k)
		}
	}
	if old, ok := p.memTiles[key]; ok {
		p.memBytes -= len(old.data)
	}
	for p.memBytes+len(data) > mapTileMemMaxBytes && len(p.memTiles) > 0 {
		oldestKey := ""
		var oldestAt time.Time
		for k, entry := range p.memTiles {
			if oldestKey == "" || entry.at.Before(oldestAt) {
				oldestKey, oldestAt = k, entry.at
			}
		}
		p.memBytes -= len(p.memTiles[oldestKey].data)
		delete(p.memTiles, oldestKey)
	}
	p.memTiles[key] = memTileEntry{data: data, at: now}
	p.memBytes += len(data)
}

// allowFetch is the global upstream fetch rate limit (sliding window); returns false when over the limit.
func (p *mapTileProxy) allowFetch() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	kept := p.fetchTimes[:0]
	for _, at := range p.fetchTimes {
		if now.Sub(at) <= mapTileFetchRateWindow {
			kept = append(kept, at)
		}
	}
	p.fetchTimes = kept
	if len(p.fetchTimes) >= mapTileFetchRateLimit {
		return false
	}
	p.fetchTimes = append(p.fetchTimes, now)
	return true
}

// sweepDisk measures the total cache directory size after a new write and, when over the limit,
// evicts oldest-first by mtime down to 90%, also reclaiming expired .tmp files left by crashes.
// store only happens after an upstream fetch (low frequency), so a synchronous scan is acceptable.
func (p *mapTileProxy) sweepDisk() {
	p.sweepMu.Lock()
	defer p.sweepMu.Unlock()
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return
	}
	type tileFile struct {
		name string
		size int64
		mod  time.Time
	}
	var files []tileFile
	var total int64
	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".tmp") {
			if now.Sub(info.ModTime()) > mapTileTmpMaxAge {
				_ = os.Remove(filepath.Join(p.dir, entry.Name()))
			}
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".img") {
			continue
		}
		files = append(files, tileFile{name: entry.Name(), size: info.Size(), mod: info.ModTime()})
		total += info.Size()
	}
	if total <= p.maxDiskBytes {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	target := p.maxDiskBytes * 9 / 10
	removed := 0
	for _, f := range files {
		if total <= target {
			break
		}
		if err := os.Remove(filepath.Join(p.dir, f.name)); err == nil {
			total -= f.size
			removed++
		}
	}
	if removed > 0 && p.log != nil {
		p.log.Info("map tile cache swept", zap.Int("removed", removed), zap.Int64("remaining_bytes", total))
	}
}

func (p *mapTileProxy) recentlyFailed(key string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	at, ok := p.failures[key]
	if !ok {
		return false
	}
	if time.Since(at) > mapTileFailureTTL {
		delete(p.failures, key)
		return false
	}
	return true
}

func (p *mapTileProxy) markFailed(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Also clean up expired entries to prevent unbounded map growth (the key space itself is bounded: coordinate×size enumeration).
	now := time.Now()
	for k, at := range p.failures {
		if now.Sub(at) > mapTileFailureTTL {
			delete(p.failures, k)
		}
	}
	p.failures[key] = now
}

// mapTileMime identifies the image type by magic number; returns an empty string for non-images.
func mapTileMime(data []byte) string {
	switch {
	case len(data) > 8 && data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G':
		return "image/png"
	case len(data) > 3 && data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	case len(data) > 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	default:
		return ""
	}
}
