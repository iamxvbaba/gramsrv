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

// testConfig 是一份可用配置。TTL 都给得比默认短,好让测试能断言缓存行为而不是
// 依赖真实时钟。
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

// lookupServer 是可控的假后端。handler 按 IP 返回预设响应,没预设的一律 404。
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
		// 端点形如 /json/{ip},取最后一段即可。
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

// 未配置 endpoint 时返回 nil Resolver,这不是错误——上层正是靠 nil 回落到
// "Unknown" 占位文案。
func TestNewWithoutEndpointDisablesResolver(t *testing.T) {
	resolver, err := New(Config{}, nil)
	if err != nil {
		t.Fatalf("New with empty endpoint: %v", err)
	}
	if resolver != nil {
		t.Fatalf("New with empty endpoint = %v, want nil Resolver", resolver)
	}
	// 上层必须先判 nil 再调用(见 rpc.resolveAuthorizationLocations);这里只确认
	// 返回的是真正的 nil 接口而不是装着 nil 指针的接口,否则 deps 校验会当成
	// typed nil 拒绝启动。
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

// publicAddr 决定哪些地址会离开本机,这里逐类钉死。
func TestPublicAddrNormalizesAndRejects(t *testing.T) {
	cases := []struct {
		raw  string
		want string
		ok   bool
	}{
		{raw: "203.0.113.7", want: "203.0.113.7", ok: true},
		{raw: "  203.0.113.7  ", want: "203.0.113.7", ok: true},
		{raw: "2001:db8::1", want: "2001:db8::1", ok: true},
		{raw: "2001:db8::1%eth0", want: "2001:db8::1", ok: true},
		// 双栈 listener 上最常见的形态:不 unmap 就一定在后端查不到。
		{raw: "::ffff:203.0.113.7", want: "203.0.113.7", ok: true},
		// 下面这些既没有地理归属,也不该被送到第三方。
		{raw: ""},
		{raw: "   "},
		{raw: "not-an-ip"},
		{raw: "203.0.113.7:443"},
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

// 城市 -> 州/省 -> 时区 是有意设计的降级链:俄罗斯 IP 常有经纬度却没有 city。
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
			// 没有国家名就没有可信归属:宁可回落到占位文案,也不拿时区拼一个国家。
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
			// 实测 reallyfreegeoip.org 对查不到的 IP 就是这么回的:200 + 全空。
			name:        "all blank fields",
			body:        `{"ip":"203.0.113.7","country_code":"","country_name":"","region_name":"","city":"","time_zone":""}`,
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
		if ip == "203.0.113.7" {
			return http.StatusOK, `{"country_name":"Finland","city":"Helsinki"}`
		}
		return http.StatusNotFound, `{"error":"not found"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	got := mustResolve(t, resolver, "203.0.113.7")
	want := map[string]Location{"203.0.113.7": {Country: "Finland", Region: "Helsinki"}}
	if len(got) != len(want) {
		t.Fatalf("Resolve = %+v, want %+v", got, want)
	}
	if got["203.0.113.7"] != want["203.0.113.7"] {
		t.Fatalf("Resolve[203.0.113.7] = %+v, want %+v", got["203.0.113.7"], want["203.0.113.7"])
	}
}

// 一次会话列表里多台设备常共享出口 IP,归一后必须去重,否则凭空多打上游。
func TestResolveDeduplicatesEquivalentAddressesAndCachesResults(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"country_name":"Iceland","city":"Reykjavik"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	first := mustResolve(t, resolver, "203.0.113.7", "::ffff:203.0.113.7", " 203.0.113.7 ")
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("first Resolve made %d requests, want 1 (deduplicated)", calls)
	}
	// 结果必须映射回调用方传入的原始字符串,上层是直接拿 authorization.ip 索引的。
	for _, raw := range []string{"203.0.113.7", "::ffff:203.0.113.7", " 203.0.113.7 "} {
		if loc, ok := first[raw]; !ok || loc.Country != "Iceland" {
			t.Fatalf("Resolve[%q] = %+v, %v; want Iceland", raw, loc, ok)
		}
	}

	second := mustResolve(t, resolver, "203.0.113.7")
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("cached Resolve made %d requests, want 0 additional", calls)
	}
	if second["203.0.113.7"].Region != "Reykjavik" {
		t.Fatalf("cached Resolve = %+v, want Reykjavik", second)
	}
}

// 后端对查不到的 IP 会反复返回同样的空结果,不记负缓存就等于每次打开会话列表都
// 重付一遍往返延迟,而这些请求又会把限流窗口续得更久。
func TestResolveNegativeCachesMissingAddresses(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusNotFound, `{"error":"not found"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	if got := mustResolve(t, resolver, "203.0.113.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location for unknown IP", got)
	}
	if got := mustResolve(t, resolver, "203.0.113.7"); len(got) != 0 {
		t.Fatalf("second Resolve = %+v, want no location", got)
	}
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("Resolve made %d requests, want 1 (negative cache)", calls)
	}
}

// 后端返回 200 但 country_name 为空是常态(实测 1.1.1.1 连国家名都没有),
// 那属于"没查到",必须回落到占位文案,不能拿时区之类硬凑一个地名。
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

// 私网/回环地址没有地理归属,更重要的是不该被送到第三方。
func TestResolveNeverQueriesNonRoutablePeers(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"country_name":"Never"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	got := mustResolve(t, resolver, "127.0.0.1", "10.0.0.5", "fe80::1", "", "garbage")
	if got != nil {
		t.Fatalf("Resolve = %+v, want nil for non-routable peers only", got)
	}
	if calls := srv.calls(); calls != 0 {
		t.Fatalf("Resolve made %d requests, want 0 for non-routable peers", calls)
	}
}

// 熔断的全部意义在于冷却期内一个请求都不发:继续打只会把 429 窗口一直续着。
func TestBreakerOpensOnConsecutiveRateLimitsAndSuppressesRequests(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limited"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 2
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	mustResolve(t, resolver, "203.0.113.1", "203.0.113.2", "203.0.113.3")
	limited := srv.calls()
	if limited != 3 {
		t.Fatalf("rate limited Resolve made %d requests, want 3", limited)
	}
	if state := resolver.backends[0].Breaker().state(); !state.Open || state.Reason != "rate_limit" {
		t.Fatalf("breaker = %+v, want an open rate_limit window", state)
	}

	// 冷却期内的新 IP 同样不该出网,并应立刻进负缓存。
	if got := mustResolve(t, resolver, "203.0.113.4"); len(got) != 0 {
		t.Fatalf("Resolve during cooldown = %+v, want nil", got)
	}
	if calls := srv.calls(); calls != limited {
		t.Fatalf("cooldown Resolve made %d extra requests, want 0", calls-limited)
	}
	entry, found := resolver.cache.get(netip.MustParseAddr("203.0.113.4"))
	if !found || entry.resolved {
		t.Fatalf("cache entry for skipped IP = %+v, %v; want negative entry", entry, found)
	}
}

// 冷却结束后自动闭合,并给下一轮限流留出完整阈值。
func TestBreakerClosesAfterCooldown(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limited"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 1
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	var now = time.Now()
	resolver.backends[0].Breaker().now = func() time.Time { return now }

	mustResolve(t, resolver, "203.0.113.1")
	if state := resolver.backends[0].Breaker().state(); !state.Open {
		t.Fatalf("breaker = %+v, want open after a single 429", state)
	}

	now = now.Add(2 * time.Minute)
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker after cooldown = %+v, want closed", state)
	}
}

// 超时和 5xx 走的是另一条独立的、明显更短的不可达窗口:后端可能只是抖了一下,
// 不该因为一次网络故障就离线比配额窗口还久。
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

	mustResolve(t, resolver, "203.0.113.1")
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker = %+v, want closed after a single 5xx", state)
	}

	mustResolve(t, resolver, "203.0.113.2")
	state := resolver.backends[0].Breaker().state()
	if !state.Open || state.Reason != "down" {
		t.Fatalf("breaker = %+v, want an open down window", state)
	}
	// 不可达窗口必须比限流窗口短:短暂抖动不该让后端离线两分钟。
	if state.Remaining >= cfg.RateLimitCooldown {
		t.Fatalf("down window = %v, want shorter than rate limit cooldown %v", state.Remaining, cfg.RateLimitCooldown)
	}

	now = now.Add(45 * time.Second)
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker after down cooldown = %+v, want closed", state)
	}
}

// "没这条 IP"是后端给出的确定答复,不是故障:不能因此熔断,否则一批冷门地址能把
// 一个健康的后端下线。
func TestBreakerIgnoresNotFound(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusNotFound, `{"error":"not found"}`
	})
	cfg := testConfig(srv.URL + "/json/{ip}")
	cfg.RateLimitThreshold = 1
	cfg.DownThreshold = 1
	resolver := newTestResolver(t, cfg).(*cachedResolver)

	mustResolve(t, resolver, "203.0.113.1", "203.0.113.2", "203.0.113.3")
	if state := resolver.backends[0].Breaker().state(); state.Open {
		t.Fatalf("breaker = %+v, want closed: not-found is an answer, not a fault", state)
	}
	if calls := srv.calls(); calls != 3 {
		t.Fatalf("Resolve made %d requests, want 3 (no suppression)", calls)
	}
}

// 单请求超时必须收敛成"查不到",而不是把整批 RPC 拖住。
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
	if got := mustResolve(t, resolver, "203.0.113.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location after timeout", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Resolve took %v, want it bounded by the per-request timeout", elapsed)
	}
}

// 响应体被劫持或改写时不能让内存吃光。
func TestResolveRejectsOversizedResponse(t *testing.T) {
	srv := newLookupServer(t, func(string) (int, string) {
		return http.StatusOK, `{"country_name":"` + strings.Repeat("A", 4*maxResponseBytes) + `"}`
	})
	resolver := newTestResolver(t, testConfig(srv.URL+"/json/{ip}"))

	if got := mustResolve(t, resolver, "203.0.113.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location for undecodable oversized body", got)
	}
}

func TestResolveIsSafeOnNilReceiver(t *testing.T) {
	var resolver *cachedResolver
	if got := resolver.Resolve(context.Background(), []string{"203.0.113.7"}); got != nil {
		t.Fatalf("nil receiver Resolve = %v, want nil", got)
	}
	if got := resolver.Resolve(context.Background(), nil); got != nil {
		t.Fatalf("nil receiver Resolve = %v, want nil", got)
	}
}

// 缓存必须有确定的内存上界。会话 IP 基数随在线用户增长,超限后允许整体清空,
// 下一次请求重查一次即可。
func TestCacheEvictionKeepsBoundedSize(t *testing.T) {
	c := newCache(cacheConfig{ttl: time.Hour, negTTL: time.Minute, maxSize: 2})
	for i := 1; i <= 5; i++ {
		c.put(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), Location{Country: "X"})
	}
	if len(c.entries) > 2 {
		t.Fatalf("cache holds %d entries, want at most 2", len(c.entries))
	}
}

// 正文缓存用长 TTL、负缓存用短 TTL 是两件不同的事,分别钉死。
func TestCacheExpiresEntries(t *testing.T) {
	c := newCache(cacheConfig{ttl: time.Minute, negTTL: time.Second, maxSize: 8})
	now := time.Now()
	c.now = func() time.Time { return now }

	hit := netip.MustParseAddr("203.0.113.7")
	c.put(hit, Location{Country: "Finland"})
	if entry, found := c.get(hit); !found || !entry.resolved || entry.location.Country != "Finland" {
		t.Fatalf("get = %+v, %v; want live positive entry", entry, found)
	}
	// 单个 IP 的地理归属几乎不变,一个短 TTL 也不该让正缓存提前失效。
	now = now.Add(30 * time.Second)
	if entry, found := c.get(hit); !found || !entry.resolved {
		t.Fatalf("get = %+v, %v; want positive entry alive before CacheTTL", entry, found)
	}

	miss := netip.MustParseAddr("203.0.113.8")
	c.putMiss(miss)
	if entry, found := c.get(miss); !found || entry.resolved {
		t.Fatalf("get = %+v, %v; want live negative entry", entry, found)
	}
	// 负缓存必须很快过期,否则一次限流会把该 IP 锁到第二天。
	now = now.Add(2 * time.Second)
	if _, found := c.get(miss); found {
		t.Fatal("get found negative entry after NegativeTTL, want miss so the IP is retried")
	}

	now = now.Add(2 * time.Minute)
	if _, found := c.get(hit); found {
		t.Fatal("get found expired positive entry, want miss")
	}
}

// 续期侧:后端恢复后同一个 IP 能被重新解析,不会永久卡在负缓存上。
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

	if got := mustResolve(t, resolver, "203.0.113.7"); len(got) != 0 {
		t.Fatalf("Resolve while rate limited = %+v, want no location", got)
	}

	rateLimited.Store(false)
	now = now.Add(10 * time.Minute) // 越过负缓存与熔断冷却

	got := mustResolve(t, resolver, "203.0.113.7")
	if loc := got["203.0.113.7"]; loc.Country != "Kenya" || loc.Region != "Nairobi" {
		t.Fatalf("Resolve after cooldown = %+v, want Kenya/Nairobi", got)
	}
}
