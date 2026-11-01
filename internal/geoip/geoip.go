// Package geoip resolves session IPs into the country/region text shown by
// account.getAuthorizations.
//
// The protocol layer has no equivalent of help.requestIpAddress, so
// authorization.country / .region could only ever return the literal "Unknown".
// This package is an optional enhancement: with no backend configured, New
// returns a nil Resolver and the caller falls back to the original placeholder,
// so the default behavior is identical to before the feature was enabled.
//
// Operators may configure several free backends in order (see Config.Endpoints);
// resolution fails over in order: when one backend cannot find the address or is
// momentarily unavailable, the next one is tried automatically. As measured in
// 2026-09, every provider intermittently fails to return complete data --
// reallyfreegeoip.org returns 429 after a few consecutive requests; geojs.io
// only covers ASN data and an address like 1.1.1.1 has no country field at all;
// hackmyip.com intermittently returns 400; ipapi.is returns is_bogon for
// reserved ranges. A single backend inevitably leaves part of the session list
// showing Unknown; multiple backends are the workable answer.
//
// Each backend has two independent health states (see breaker): a long
// rate-limit window and a short unreachable window. The caller tries backends in
// order and skips any with an open window outright, sending not a single request.
//
// Privacy constraint: only publicly routable addresses are resolved.
// Private/loopback/link-local addresses have no geographic identity, and must not
// be sent to third-party services.
package geoip

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

// Location is the display text for an IP's geographic identity.
//
// A non-empty Country means the lookup succeeded. Region is a finer location
// (city -> state/province -> timezone); it is left empty when unavailable, and
// the caller decides the placeholder text -- this package never fabricates a
// place name.
type Location struct {
	Country string
	Region  string
}

// Resolver resolves IPs in bulk into display text.
//
// The returned map is keyed by the exact strings the caller passed in; IPs with
// no hit do not appear in the map. The caller can simply fall back on a zero
// value and does not need to know the internal IP normalization or failover rules.
type Resolver interface {
	Resolve(ctx context.Context, ips []string) map[string]Location
	// Close releases resources held by the backends. Callers must not call it on a nil Resolver.
	Close() error
}

// Config describes the geoip backend chain. The zero value is invalid; when the
// feature is disabled, simply do not call New.
type Config struct {
	// Endpoints is the ordered list of backend URL templates to try, the first
	// being the primary. Every element must contain the {ip} placeholder, for
	// example "https://api.ipapi.is/?q={ip}". An empty list disables geolocation.
	//
	// Order matters: unresolved addresses are handed to the later backends in
	// turn, so the more accurate the primary the less often the others are
	// queried and the lower the quota consumption.
	Endpoints []string
	// Timeout is the cap for a single HTTP request. Free backends normally take
	// 0.6-1.3s, so 2s leaves ample jitter margin; a timeout is treated as a
	// failure and counts toward that backend's unreachable window.
	Timeout time.Duration
	// Concurrency is the cap on in-flight requests across all backends within a
	// single Resolve. Bulk concurrency is required: querying N devices serially
	// costs N x latency, and the client would perceive the stall directly.
	Concurrency int
	// CacheTTL is the lifetime of a positive cache hit. A single IP's geographic
	// identity barely changes, so the default grants a full day.
	CacheTTL time.Duration
	// NegativeTTL is the negative-cache lifetime for "no backend produced a
	// result" (rate limit / timeout / network error / IP not found). It must be
	// far shorter than CacheTTL, otherwise a single rate limit locks the IP out
	// for a long time.
	NegativeTTL time.Duration
	// CacheSize is the maximum number of cache entries.
	CacheSize int
	// RateLimitThreshold is how many consecutive rate limits open that backend's rate-limit window.
	RateLimitThreshold int
	// RateLimitCooldown is the length of the rate-limit window; during it not a
	// single request is sent to that backend.
	RateLimitCooldown time.Duration
	// DownThreshold is how many consecutive timeouts/network errors mark the backend unreachable.
	DownThreshold int
	// DownCooldown is the length of the unreachable window. It is much shorter
	// than the rate-limit window: the backend may have only hiccuped once.
	DownCooldown time.Duration
}

const (
	defaultTimeout            = 2 * time.Second
	defaultConcurrency        = 4
	defaultCacheTTL           = 24 * time.Hour
	defaultNegativeTTL        = 5 * time.Minute
	defaultCacheSize          = 4096
	defaultRateLimitThreshold = 3
	// Measured 2026-09: 429 begins after about 5 consecutive requests and clears
	// after about 60s of silence. The rate-limit cooldown is 2min -- twice the
	// measured window, yet not so long that users stare at "Unknown" waiting.
	defaultRateLimitCooldown = 2 * time.Minute
	// Unreachable is given only 30s: one dead backend must not take the whole
	// chain offline for minutes, but burning through the full timeout budget on
	// every request is equally unacceptable -- that is exactly the stall failover
	// exists to eliminate.
	defaultDownThreshold = 2
	defaultDownCooldown  = 30 * time.Second
)

// New builds a resolver from cfg.
//
// With empty Endpoints it returns (nil, nil) -- that means "geolocation is off",
// not an error. Callers must tolerate a nil Resolver; that is the expected path
// when the feature is unconfigured.
func New(cfg Config, log *zap.Logger) (Resolver, error) {
	if log == nil {
		log = zap.NewNop()
	}
	backends, err := newBackendChain(cfg, log)
	if err != nil {
		return nil, err
	}
	if len(backends) == 0 {
		return nil, nil
	}
	return &cachedResolver{
		backends:    backends,
		concurrency: positiveInt(cfg.Concurrency, defaultConcurrency),
		cache: newCache(cacheConfig{
			ttl:     positiveDuration(cfg.CacheTTL, defaultCacheTTL),
			negTTL:  positiveDuration(cfg.NegativeTTL, defaultNegativeTTL),
			maxSize: positiveInt(cfg.CacheSize, defaultCacheSize),
		}),
		log: log,
	}, nil
}

// newBackendChain builds the backend chain in the configured order and drops
// duplicate endpoints.
//
// Dedup matters: configuring the same endpoint twice does not improve the
// success rate, it only doubles the requests for one session list, and free
// backends' quotas cannot afford that waste.
func newBackendChain(cfg Config, log *zap.Logger) ([]backend, error) {
	timeout := positiveDuration(cfg.Timeout, defaultTimeout)
	breakerCfg := breakerConfig{
		rateLimitThreshold: positiveInt(cfg.RateLimitThreshold, defaultRateLimitThreshold),
		rateLimitCooldown:  positiveDuration(cfg.RateLimitCooldown, defaultRateLimitCooldown),
		downThreshold:      positiveInt(cfg.DownThreshold, defaultDownThreshold),
		downCooldown:       positiveDuration(cfg.DownCooldown, defaultDownCooldown),
	}
	seen := make(map[string]struct{}, len(cfg.Endpoints))
	backends := make([]backend, 0, len(cfg.Endpoints))
	for index, endpoint := range cfg.Endpoints {
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			continue
		}
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}
		b, err := newHTTPBackend(httpBackendConfig{
			endpoint: endpoint,
			timeout:  timeout,
			breaker:  breakerCfg,
		}, log)
		if err != nil {
			return nil, fmt.Errorf("geoip endpoint %d: %w", index+1, err)
		}
		backends = append(backends, b)
	}
	return backends, nil
}

// cachedResolver is the only exported Resolver implementation. It owns IP
// normalization, caching, backend failover and circuit breaking, reducing each
// backend to "give me a normalized public IP, give me its country/region".
type cachedResolver struct {
	backends    []backend
	concurrency int
	cache       *cache
	log         *zap.Logger
}

// Resolve resolves a batch of IPs.
//
// A nil receiver returns nil outright: when geolocation is off, callers pass a
// nil Resolver down here, and this must degrade safely rather than panic.
func (r *cachedResolver) Resolve(ctx context.Context, ips []string) map[string]Location {
	if r == nil || len(ips) == 0 {
		return nil
	}
	// Deduplicate by unique address after normalization: multiple devices in one
	// session list often share the same egress IP.
	want := make(map[netip.Addr]struct{}, len(ips))
	for _, ip := range ips {
		if addr, ok := publicAddr(ip); ok {
			want[addr] = struct{}{}
		}
	}
	if len(want) == 0 {
		return nil
	}

	// Check the cache first and send only the genuinely missing ones to the
	// backends. Rate limits are aggressive, and even one network round-trip that
	// could have been a cache hit is enough to slow the batch and possibly earn a 429.
	pending := make([]netip.Addr, 0, len(want))
	hit := make(map[netip.Addr]Location, len(want))
	for addr := range want {
		entry, found := r.cache.get(addr)
		switch {
		case !found:
			pending = append(pending, addr)
		case entry.resolved:
			hit[addr] = entry.location
		}
	}

	if len(pending) > 0 {
		r.resolvePending(ctx, pending, hit)
	}
	return expandByInput(hit, ips)
}

// resolvePending tries the backend chain in order, writing results and negative-cache entries together.
func (r *cachedResolver) resolvePending(ctx context.Context, pending []netip.Addr, hit map[netip.Addr]Location) {
	for _, b := range r.backends {
		if ctx.Err() != nil {
			return
		}
		remaining := unresolved(pending, hit)
		if len(remaining) == 0 {
			return
		}
		if state := b.Breaker().state(); state.Open {
			// Skip the whole batch while the breaker is open, issuing no network
			// requests at all. That is the entire point of the breaker: hammering
			// on only extends the 429/failure window and burns every request's latency budget.
			r.log.Debug("geoip resolution skipped: backend temporarily unavailable",
				zap.String("backend", b.Name()),
				zap.String("reason", state.Reason),
				zap.Duration("remaining", state.Remaining),
				zap.Int("skipped_ips", len(remaining)))
			continue
		}
		r.resolveWithBackend(ctx, b, remaining, hit)
	}
	// Cancellation does not mean the backend lacks this IP; an incomplete chain
	// must not pollute the negative cache.
	if ctx.Err() != nil {
		return
	}
	// Record a negative cache entry only after every backend was tried and still
	// found nothing: one backend's gap must not condemn the whole chain, or free
	// libraries' coverage differences would keep a batch of addresses showing Unknown.
	for _, addr := range pending {
		if _, ok := hit[addr]; !ok {
			r.cache.putMiss(addr)
		}
	}
}

// resolveWithBackend resolves a batch of addresses concurrently with one backend.
//
// A hard failure mid-batch (timeout / transport error / 5xx) stops new requests
// and yields to the next backend: when a backend is failing, grinding through
// its remaining addresses one timeout budget at a time only slows the batch --
// exactly the stall failover exists to eliminate. A definite "no such IP" answer
// does not trip the breaker; it is just an opportunity for the next backend to try.
func (r *cachedResolver) resolveWithBackend(ctx context.Context, b backend, addrs []netip.Addr, hit map[netip.Addr]Location) {
	sem := make(chan struct{}, r.concurrency)
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		healthy atomic.Bool
	)
	// Every return path must wait for in-flight tasks; the caller reads hit or
	// switches backends right after.
	defer wg.Wait()
	healthy.Store(true)
	for _, addr := range addrs {
		if ctx.Err() != nil || !healthy.Load() {
			break
		}
		// Acquire the semaphore on the sender side rather than inside the
		// goroutine: this loop then blocks on its own, forming a natural
		// concurrency cap without spawning a goroutine per IP to compete for it.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		// Cancellation or a hard failure may occur while waiting for the
		// semaphore; send no new requests to a failed backend.
		if ctx.Err() != nil || !healthy.Load() {
			<-sem
			return
		}
		wg.Add(1)
		go func(addr netip.Addr) {
			defer func() {
				<-sem
				wg.Done()
			}()
			loc, status := b.Lookup(ctx, addr)
			// A caller cancellation or an expired overall budget must not be
			// miscounted as a backend fault.
			if ctx.Err() == nil {
				b.Breaker().record(status)
			}
			switch status {
			case statusOK:
				mu.Lock()
				hit[addr] = loc
				mu.Unlock()
				r.cache.put(addr, loc)
			case statusNotFound:
				// The backend is alive but lacks this data; keep asking the next backend.
			case statusRateLimited:
				// The breaker maintains the window; hammering the rest of the batch
				// is pointless, so yield immediately.
				healthy.Store(false)
			default:
				healthy.Store(false)
			}
		}(addr)
	}
}

// Close releases backend resources. HTTP backends hold no long-lived resources;
// this exists only so the interface stays symmetric.
func (r *cachedResolver) Close() error { return nil }

// unresolved returns the addresses that have no result yet.
func unresolved(pending []netip.Addr, hit map[netip.Addr]Location) []netip.Addr {
	out := make([]netip.Addr, 0, len(pending))
	for _, addr := range pending {
		if _, ok := hit[addr]; !ok {
			out = append(out, addr)
		}
	}
	return out
}

// expandByInput remaps results keyed by normalized address back to the original
// strings the caller passed in. The caller can index directly by
// authorization.ip without knowing the normalization rules.
func expandByInput(hit map[netip.Addr]Location, ips []string) map[string]Location {
	if len(hit) == 0 {
		return nil
	}
	out := make(map[string]Location, len(hit))
	for _, ip := range ips {
		addr, ok := publicAddr(ip)
		if !ok {
			continue
		}
		if loc, ok := hit[addr]; ok {
			out[ip] = loc
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func positiveDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func positiveInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}
