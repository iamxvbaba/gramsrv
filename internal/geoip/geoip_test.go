package geoip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testConfig is a usable config. The TTLs are shorter than the defaults so tests
// can assert cache behavior without depending on the real clock.
func testConfig(endpoints ...string) Config {
	return Config{
		Endpoints:         endpoints,
		Timeout:           2 * time.Second,
		Concurrency:       4,
		CacheTTL:          time.Hour,
		NegativeTTL:       time.Minute,
		CacheSize:         64,
		RateLimitCooldown: time.Minute,
		DownCooldown:      30 * time.Second,
	}
}

// lookupServer is a controllable fake backend. handler returns a preset response per IP; anything without a preset returns 404.
type lookupServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	handler  func(ip string) (int, string)
}

func newLookupServer(t *testing.T, handler func(ip string) (int, string)) *lookupServer {
	t.Helper()
	s := &lookupServer{handler: handler}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The endpoint looks like /json/{ip}; taking the last segment is enough.
		ip := r.URL.Path
		if idx := strings.LastIndexByte(ip, '/'); idx >= 0 {
			ip = ip[idx+1:]
		}
		s.mu.Lock()
		s.requests = append(s.requests, ip)
		s.mu.Unlock()
		status, body := s.handler(ip)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *lookupServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *lookupServer) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func newTestResolver(t *testing.T, cfg Config) Resolver {
	t.Helper()
	resolver, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New(%q): %v", cfg.Endpoints, err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	return resolver
}

func mustResolve(t *testing.T, resolver Resolver, ips ...string) map[string]Location {
	t.Helper()
	if resolver == nil {
		t.Fatal("resolver is nil; New must return a non-nil Resolver for a configured endpoint")
	}
	return resolver.Resolve(context.Background(), ips)
}

// With no endpoint configured it returns a nil Resolver, which is not an error
// -- the caller relies on that nil to fall back to the "Unknown" placeholder.
func TestNewWithoutEndpointDisablesResolver(t *testing.T) {
	resolver, err := New(Config{}, nil)
	if err != nil {
		t.Fatalf("New with empty endpoint: %v", err)
	}
	if resolver != nil {
		t.Fatalf("New with empty endpoint = %v, want nil Resolver", resolver)
	}
	// The caller must check for nil before calling (see
	// rpc.resolveAuthorizationLocations); here we only confirm the return is a
	// true nil interface rather than an interface holding a nil pointer, or the
	// deps validation would reject startup as a typed nil.
	if resolver != nil && reflect.ValueOf(resolver).Kind() == reflect.Ptr && reflect.ValueOf(resolver).IsNil() {
		t.Fatal("New with empty endpoint returned a typed-nil Resolver, want untyped nil")
	}
}

func TestNewRejectsInvalidEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
	}{
		{name: "missing placeholder", endpoint: "https://geo.example/json"},
		{name: "unsupported scheme", endpoint: "ftp://geo.example/json/{ip}"},
		{name: "no host", endpoint: "https:///json/{ip}"},
		{name: "unparsable", endpoint: "https://geo.example/%zz/{ip}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(testConfig(tc.endpoint), nil); err == nil {
				t.Fatalf("New(%q) = nil error, want validation failure", tc.endpoint)
			}
		})
	}
}

// publicAddr decides which addresses leave the machine; each class is pinned down here.
func TestPublicAddrNormalizesAndRejects(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "8.8.8.7", want: "8.8.8.7", ok: true},
		{raw: "  8.8.8.7  ", want: "8.8.8.7", ok: true},
		{raw: "2606:4700:4700::1", want: "2606:4700:4700::1", ok: true},
		{raw: "2606:4700:4700::1%eth0", want: "2606:4700:4700::1", ok: true},
		// The most common form on a dual-stack listener: without unmapping, the backend can never find it.
		{raw: "::ffff:8.8.8.7", want: "8.8.8.7", ok: true},
		// The following have no geographic identity and must not be sent to third parties.
		{raw: ""},
		{raw: "   "},
		{raw: "not-an-ip"},
		{raw: "8.8.8.7:443"},
		{raw: "127.0.0.1"},
		{raw: "::1"},
		{raw: "10.0.0.5"},
		{raw: "192.168.1.10"},
		{raw: "172.16.4.4"},
		{raw: "fe80::1"},
		{raw: "169.254.10.1"},
		{raw: "0.0.0.0"},
		{raw: "224.0.0.1"},
		{raw: "ff02::1"},
		{raw: "8.8.8.8%invalid"},
		{raw: "0.1.2.3"},
		{raw: "100.64.0.0"},
		{raw: "100.127.255.255"},
		{raw: "::ffff:100.64.0.12"},
		{raw: "192.0.0.8"},
		{raw: "192.0.0.170"},
		{raw: "192.0.2.1"},
		{raw: "192.88.99.2"},
		{raw: "198.18.0.1"},
		{raw: "198.19.255.255"},
		{raw: "198.51.100.1"},
		{raw: "203.0.113.1"},
		{raw: "240.0.0.1"},
		{raw: "255.255.255.255"},
		{raw: "::192.168.1.1"},
		{raw: "64:ff9b::c0a8:101"},
		{raw: "64:ff9b:1::1"},
		{raw: "100::1"},
		{raw: "100:0:0:1::1"},
		{raw: "2001::1"},
		{raw: "2001:2::1"},
		{raw: "2001:db8::1"},
		{raw: "2002:a00:1::1"},
		{raw: "3fff::1"},
		{raw: "5f00::1"},
		{raw: "fc00::1"},
		{raw: "4000::1"},
		// Adjacent public boundaries and more-specific IANA exceptions stay usable.
		{raw: "100.63.255.255", want: "100.63.255.255", ok: true},
		{raw: "100.128.0.0", want: "100.128.0.0", ok: true},
		{raw: "192.0.0.9", want: "192.0.0.9", ok: true},
		{raw: "192.0.0.10", want: "192.0.0.10", ok: true},
		{raw: "192.31.196.1", want: "192.31.196.1", ok: true},
		{raw: "2001:1::1", want: "2001:1::1", ok: true},
		{raw: "2001:1::3", want: "2001:1::3", ok: true},
		{raw: "2001:3::1", want: "2001:3::1", ok: true},
		{raw: "2001:4:112::1", want: "2001:4:112::1", ok: true},
		{raw: "2001:20::1", want: "2001:20::1", ok: true},
		{raw: "2001:30::1", want: "2001:30::1", ok: true},
		{raw: "2620:4f:8000::1", want: "2620:4f:8000::1", ok: true},
	}
	for _, tc := range cases {
		addr, ok := publicAddr(tc.raw)
		if ok != tc.ok {
			t.Fatalf("publicAddr(%q) ok = %v, want %v", tc.raw, ok, tc.ok)
		}
		if !ok {
			continue
		}
		if addr.String() != tc.want {
			t.Fatalf("publicAddr(%q) = %q, want %q", tc.raw, addr, tc.want)
		}
	}
}

// city -> state/province -> timezone is a deliberate degradation chain: Russian IPs often have lat/long but no city.
func TestReallyFreeGeoIPFallsBackThroughCityRegionTimezone(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantCountry string
		wantRegion  string
		wantOutcome parseOutcome
	}{
		{
			name:        "city preferred",
			body:        `{"country_name":"Germany","city":"Berlin","region_name":"Berlin","time_zone":"Europe/Berlin"}`,
			wantCountry: "Germany",
			wantRegion:  "Berlin",
			wantOutcome: parseOK,
		},
		{
			name:        "region when no city",
			body:        `{"country_name":"United States","region_name":"California","time_zone":"America/Los_Angeles"}`,
			wantCountry: "United States",
			wantRegion:  "California",
			wantOutcome: parseOK,
		},
		{
			name:        "timezone as last resort",
			body:        `{"country_name":"Russia","time_zone":"Europe/Moscow"}`,
			wantCountry: "Russia",
			wantRegion:  "Europe/Moscow",
			wantOutcome: parseOK,
		},
		{
			// No country name means no trustworthy identity: better to fall back
			// to the placeholder than to piece a country together from a timezone.
			name:        "timezone alone is not a country",
			body:        `{"time_zone":"Europe/Berlin","region_name":"Berlin","city":"Berlin"}`,
			wantOutcome: parseNotFound,
		},
		{
			name:        "empty response",
			body:        `{}`,
			wantOutcome: parseNotFound,
		},
		{
			// Measured: this is exactly how reallyfreegeoip.org responds for an IP it cannot find: 200 + all empty.
			name:        "all blank fields",
			body:        `{"ip":"8.8.8.7","country_code":"","country_name":"","region_name":"","city":"","time_zone":""}`,
			wantOutcome: parseNotFound,
		},
		{
			name:        "malformed body",
			body:        `<html>502 Bad Gateway</html>`,
			wantOutcome: parseFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := parseReallyFreeGeoIP([]byte(tc.body))
			if outcome != tc.wantOutcome {
				t.Fatalf("outcome = %v, want %v", outcome, tc.wantOutcome)
			}
			if got.Country != tc.wantCountry || got.Region != tc.wantRegion {
				t.Fatalf("location = %+v, want country %q region %q", got, tc.wantCountry, tc.wantRegion)
			}
		})
	}
}

func TestResolveReturnsCountryAndRegion(t *testing.T) {
	srv := newLookupServer(t, func(ip string) (int, string) {
		if ip == "8.8.8.7" {
			return http.StatusOK, `{"country_name":"Finland","city":"Helsinki"}`
		}
		return http.StatusNotFound, `{"error":"not found"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	got := mustResolve(t, resolver, "8.8.8.7")
	want := map[string]Location{"8.8.8.7": {Country: "Finland", Region: "Helsinki"}}
	if len(got) != len(want) {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
	if got["8.8.8.7"] != want["8.8.8.7"] {
		t.Fatalf("Resolve[8.8.8.7] = %+v, want %+v", got["8.8.8.7"], want["8.8.8.7"])
	}
}

// Multiple devices in one session list often share an egress IP; after normalization they must be deduplicated, otherwise the upstream is hit needlessly.
func TestResolveDeduplicatesEquivalentAddressesAndCachesResults(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"country_name":"Iceland","city":"Reykjavik"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	first := mustResolve(t, resolver, "8.8.8.7", "::ffff:8.8.8.7", " 8.8.8.7 ")
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("first Resolve made %d requests, want 1 (deduplicated)", calls)
	}
	// Results must map back to the original strings the caller passed in; the
	// caller indexes directly by authorization.ip.
	for _, raw := range []string{"8.8.8.7", "::ffff:8.8.8.7", " 8.8.8.7 "} {
		if loc, ok := first[raw]; !ok || loc.Country != "Iceland" {
			t.Fatalf("Resolve[%q] = %+v, %v; want Iceland", raw, loc, ok)
		}
	}

	second := mustResolve(t, resolver, "8.8.8.7")
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("cached Resolve made %d requests, want 0 additional", calls)
	}
	if second["8.8.8.7"].Region != "Reykjavik" {
		t.Fatalf("cached Resolve = %+v, want Reykjavik", second)
	}
}

// A backend repeatedly returns the same empty result for an IP it cannot find;
// without a negative cache, every session-list open re-pays the round-trip
// latency, and those requests extend the rate-limit window even further.
func TestResolveNegativeCachesMissingAddresses(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusNotFound, `{"error":"not found"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	if got := mustResolve(t, resolver, "8.8.8.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location for unknown IP", got)
	}
	if got := mustResolve(t, resolver, "8.8.8.7"); len(got) != 0 {
		t.Fatalf("second Resolve = %+v, want no location", got)
	}
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("Resolve made %d requests, want 1 (negative cache)", calls)
	}
}

// A backend returning 200 with an empty country_name is the norm (measured:
// 1.1.1.1 has not even a country name); that counts as "not found" and must fall
// back to the placeholder rather than piecing a place name from a timezone.
func TestResolveTreatsEmptyCountryAsUnresolved(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"ip":"1.1.1.1","time_zone":"Australia/Sydney","region_name":"New South Wales"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	if got := mustResolve(t, resolver, "1.1.1.1"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location when country is empty", got)
	}
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("Resolve made %d requests, want 1 (miss cached)", calls)
	}
}

// Private/loopback addresses have no geographic identity and, more importantly, must not be sent to third parties.
func TestResolveNeverQueriesNonRoutablePeers(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"country_name":"Never"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	got := mustResolve(t, resolver,
		"127.0.0.1", "10.0.0.5", "fe80::1", "", "garbage",
		"100.64.0.12", "::ffff:100.64.0.12", "192.0.2.1", "198.18.0.1",
		"198.51.100.1", "203.0.113.1", "240.0.0.1", "2001:db8::1", "3fff::1",
	)
	if got != nil {
		t.Fatalf("Resolve = %+v, want nil for non-routable peers only", got)
	}
	if calls := srv.calls(); calls != 0 {
		t.Fatalf("Resolve made %d requests, want 0 for non-routable peers", calls)
	}
}

// The entire point of the breaker is to send no requests during the cooldown: hammering on only extends the 429 window.
func TestBreakerOpensOnConsecutiveRateLimitsAndSuppressesRequests(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limited"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 2
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	mustResolve(t, resolver, "8.8.8.1", "8.8.8.2", "8.8.8.3")
	limited := srv.calls()
	if limited != 3 {
		t.Fatalf("rate limited Resolve made %d requests, want 3", limited)
	}
	if state := resolver.backends[0].Breaker().state(); !state.Open || state.Reason != "rate_limit" {
		t.Fatalf("breaker = %+v, want an open rate_limit window", state)
	}

	// A new IP during the cooldown must also not go to the network, and should immediately enter the negative cache.
	if got := mustResolve(t, resolver, "8.8.8.4"); len(got) != 0 {
		t.Fatalf("Resolve during cooldown = %+v, want nil", got)
	}
	if calls := srv.calls(); calls != limited {
		t.Fatalf("cooldown Resolve made %d extra requests, want 0", calls-limited)
	}
	entry, found := resolver.cache.get(netip.MustParseAddr("8.8.8.4"))
	if !found || entry.resolved {
		t.Fatalf("cache entry for skipped IP = %+v, %v; want negative entry", entry, found)
	}
}

// It closes automatically once the cooldown ends, and gives the next rate-limit round the full threshold.
func TestBreakerClosesAfterCooldown(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limited"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 1
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	var now = time.Now()
	resolver.backends[0].Breaker().now = func() time.Time { return now }

	mustResolve(t, resolver, "8.8.8.1")
	if state := resolver.backends[0].Breaker().state(); !state.Open {
		t.Fatalf("breaker = %+v, want open after a single 429", state)
	}

	now = now.Add(2 * time.Minute)
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker after cooldown = %+v, want closed", state)
	}
}

// Timeouts and 5xx go through a separate, distinctly shorter unreachable window:
// the backend may have only hiccuped, and one network failure should not take it
// offline longer than the quota window.
func TestBreakerTracksDownFailuresSeparatelyFromRateLimits(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusInternalServerError, `{"error":"boom"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 1
	cfg.DownThreshold = 2
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	var now = time.Now()
	resolver.backends[0].Breaker().now = func() time.Time { return now }

	mustResolve(t, resolver, "8.8.8.1")
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker = %+v, want closed after a single 5xx", state)
	}

	mustResolve(t, resolver, "8.8.8.2")
	state := resolver.backends[0].Breaker().state()
	if !state.Open || state.Reason != "down" {
		t.Fatalf("breaker = %+v, want an open down window", state)
	}
	// The unreachable window must be shorter than the rate-limit window: a brief hiccup must not keep a backend offline for two minutes.
	if state.Remaining >= cfg.RateLimitCooldown {
		t.Fatalf("down window = %v, want shorter than rate limit cooldown %v", state.Remaining, cfg.RateLimitCooldown)
	}

	now = now.Add(45 * time.Second)
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker after down cooldown = %+v, want closed", state)
	}
}

// "No such IP" is a definite answer from the backend, not a fault: it must not
// trip the breaker, or a batch of obscure addresses could take a healthy backend offline.
func TestBreakerIgnoresNotFound(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusNotFound, `{"error":"not found"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 1
	cfg.DownThreshold = 1
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	mustResolve(t, resolver, "8.8.8.1", "8.8.8.2", "8.8.8.3")
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker = %+v, want closed: not-found is an answer, not a fault", state)
	}
	if calls := srv.calls(); calls != 3 {
		t.Fatalf("Resolve made %d requests, want 3 (no suppression)", calls)
	}
}

// A single-request timeout must converge to "not found" rather than holding up the whole RPC batch.
func TestResolveHonorsPerRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := newLookupServer(t, func(string) (int, string) {
		<-release
		return http.StatusOK, `{"country_name":"Never"}`
	})
	t.Cleanup(func() { close(release) })

	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.Timeout = 50 * time.Millisecond
	resolver := newTestResolver(t, cfg)

	start := time.Now()
	if got := mustResolve(t, resolver, "8.8.8.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location after timeout", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Resolve took %v, want it bounded by the per-request timeout", elapsed)
	}
}

// A hijacked or rewritten response body must not exhaust memory.
func TestResolveRejectsOversizedResponse(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"country_name":"` + strings.Repeat("A", 4*maxResponseBytes) + `"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	if got := mustResolve(t, resolver, "8.8.8.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location for undecodable oversized body", got)
	}
}

func TestResolveIsSafeOnNilReceiver(t *testing.T) {
	var resolver *cachedResolver
	if got := resolver.Resolve(context.Background(), []string{"8.8.8.7"}); got != nil {
		t.Fatalf("nil receiver Resolve = %v, want nil", got)
	}
	if got := resolver.Resolve(context.Background(), nil); got != nil {
		t.Fatalf("nil receiver Resolve = %v, want nil", got)
	}
}

// The cache must have a deterministic memory bound. Session IP cardinality grows
// with online users, and clearing everything when the limit is exceeded is fine:
// the next request simply re-queries once.
func TestCacheEvictionKeepsBoundedSize(t *testing.T) {
	c := newCache(cacheConfig{ttl: time.Hour, negTTL: time.Minute, maxSize: 2})
	for i := 1; i <= 5; i++ {
		c.put(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), Location{Country: "X"})
	}
	if len(c.entries) > 2 {
		t.Fatalf("cache holds %d entries, want at most 2", len(c.entries))
	}
}

// Positive cache with a long TTL and negative cache with a short TTL are two
// different things, pinned down separately.
func TestCacheExpiresEntries(t *testing.T) {
	c := newCache(cacheConfig{ttl: time.Minute, negTTL: time.Second, maxSize: 8})
	now := time.Now()
	c.now = func() time.Time { return now }

	hit := netip.MustParseAddr("8.8.8.7")
	c.put(hit, Location{Country: "Finland"})
	if entry, found := c.get(hit); !found || !entry.resolved || entry.location.Country != "Finland" {
		t.Fatalf("get = %+v, %v; want live positive entry", entry, found)
	}
	// A single IP's geographic identity barely changes, so a short TTL must not let the positive entry expire early.
	now = now.Add(30 * time.Second)
	if entry, found := c.get(hit); !found || !entry.resolved {
		t.Fatalf("get = %+v, %v; want positive entry alive before CacheTTL", entry, found)
	}

	miss := netip.MustParseAddr("8.8.8.8")
	c.putMiss(miss)
	if entry, found := c.get(miss); !found || entry.resolved {
		t.Fatalf("get = %+v, %v; want live negative entry", entry, found)
	}
	// The negative cache must expire quickly, otherwise one rate limit locks the IP out until the next day.
	now = now.Add(2 * time.Second)
	if _, found := c.get(miss); found {
		t.Fatal("get found negative entry after NegativeTTL, want miss so the IP is retried")
	}

	now = now.Add(2 * time.Minute)
	if _, found := c.get(hit); found {
		t.Fatal("get found expired positive entry, want miss")
	}
}

// Renewal side: once the backend recovers, the same IP can be resolved again and is not stuck on the negative cache forever.
func TestResolveRetriesAfterNegativeCacheExpires(t *testing.T) {
	var rateLimited atomic.Bool
	rateLimited.Store(true)
	srv := newLookupServer(t, func(string) (int, string) {
		if rateLimited.Load() {
			return http.StatusTooManyRequests, `{"error":"rate limited"}`
		}
		return http.StatusOK, `{"country_name":"Kenya","city":"Nairobi"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	now := time.Now()
	resolver.cache.now = func() time.Time { return now }
	resolver.backends[0].Breaker().now = func() time.Time { return now }

	if got := mustResolve(t, resolver, "8.8.8.7"); len(got) != 0 {
		t.Fatalf("Resolve while rate limited = %+v, want no location", got)
	}

	rateLimited.Store(false)
	now = now.Add(10 * time.Minute) // past the negative cache and breaker cooldowns

	got := mustResolve(t, resolver, "8.8.8.7")
	if loc := got["8.8.8.7"]; loc.Country != "Kenya" || loc.Region != "Nairobi" {
		t.Fatalf("Resolve after cooldown = %+v, want Kenya/Nairobi", got)
	}
}
