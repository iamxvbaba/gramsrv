package geoip

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// 本文件覆盖 failover 链的行为。响应体用 2026-09 的实测样本,所以这些断言同时也在
// 钉住各家的真实字段形状——它们的文档和实际返回并不总是一致。

// chainResolver 用一组 httptest 端点构造解析器,跳过 New 的配置校验。
func chainResolver(t *testing.T, servers []*lookupServer, mutate func(*Config)) *cachedResolver {
	t.Helper()
	endpoints := make([]string, 0, len(servers))
	for _, srv := range servers {
		endpoints = append(endpoints, srv.URL+"/json/{ip}")
	}
	cfg := testConfig(endpoints...)
	if mutate != nil {
		mutate(&cfg)
	}
	resolver, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New(%v): %v", endpoints, err)
	}
	t.Cleanup(func() { _ = resolver.Close() })
	return resolver.(*cachedResolver)
}

const (
	// geoJSBody 是 get.geojs.io 的实测响应:只有 ASN 级覆盖,没有 city/region。
	geoJSBody = `{"accuracy":1000,"area_code":"0","asn":15169,"continent_code":"NA",` +
		`"country":"United States","country_code":"US","country_code3":"USA","ip":"8.8.8.8",` +
		`"latitude":"37.751","longitude":"-97.822","organization":"AS15169 Google LLC",` +
		`"organization_name":"Google LLC","timezone":"America/Chicago"}`
	// geoJSBodyNoCountry 是 geojs 对 1.1.1.1 的实测响应:连 country 字段都没有。
	geoJSBodyNoCountry = `{"area_code":"0","asn":13335,"ip":"1.1.1.1","latitude":"nil",` +
		`"longitude":"nil","organization":"AS13335 Cloudflare, Inc.",` +
		`"organization_name":"Cloudflare, Inc."}`
	// rfgiBody 是 reallyfreegeoip.org 的实测响应。
	rfgiBody = `{"ip":"8.8.8.8","country_code":"US","country_name":"United States","region_code":"",` +
		`"region_name":"","city":"","zip_code":"","time_zone":"America/Chicago","latitude":37.751,` +
		`"longitude":-97.822,"metro_code":0}`
	// rfgiBodyUnknown 是它对查不到的地址的实测响应:200 + 全空字符串。
	rfgiBodyUnknown = `{"ip":"203.0.113.7","country_code":"","country_name":"","region_code":"",` +
		`"region_name":"","city":"","zip_code":"","time_zone":"","latitude":0,"longitude":0,"metro_code":0}`
	// hackMyIPBody 是 hackmyip.com /api/lookup 的实测响应。注意 location.country 是
	// 两位国家码,国名在 country_name。
	hackMyIPBody = `{"success":true,"data":{"ip":"1.1.1.1","location":{"city":"South Brisbane",` +
		`"region":"Queensland","country":"AU","country_name":"Australia","latitude":-27.4766,` +
		`"longitude":153.0166,"timezone":"Australia/Brisbane","postal_code":"4101"}}}`
	// hackMyIPDownBody 是它服务故障时的实测响应:HTTP 400 + success=false。
	hackMyIPDownBody = `{"success":false,"error":"IP lookup service temporarily unavailable. Please try again."}`
	// ipapiISBody 是 api.ipapi.is 的实测响应。
	ipapiISBody = `{"ip":"1.1.1.1","is_bogon":false,"company":"APNIC Research and Development",` +
		`"asn":"AS13335 Cloudflare, Inc.","city":"Brisbane","region":"Queensland",` +
		`"country":"Australia","lat":-27.46754,"lon":153.02809,"timezone":"Australia/Brisbane"}`
	// ipapiISBogonBody 是它对保留网段的实测响应:全字段 null。
	ipapiISBogonBody = `{"ip":"203.0.113.7","is_bogon":true,"company":null,"asn":null,"city":null,` +
		`"region":null,"country":null,"lat":null,"lon":null,"timezone":null}`
)

// okServer 让所有地址都返回给定响应。
func okServer(t *testing.T, body string) *lookupServer {
	t.Helper()
	return newLookupServer(t, func(string) (int, string) { return http.StatusOK, body })
}

// notFoundServer 让所有地址都回 404。
func notFoundServer(t *testing.T) *lookupServer {
	t.Helper()
	return newLookupServer(t, func(string) (int, string) {
		return http.StatusNotFound, `{"error":"not found"}`
	})
}

func TestGeoJSParserReadsCountryAndTimezone(t *testing.T) {
	got, outcome := parseGeoJS([]byte(geoJSBody))
	if outcome != parseOK {
		t.Fatalf("outcome = %v, want parseOK", outcome)
	}
	if got.Country != "United States" || got.Region != "America/Chicago" {
		t.Fatalf("location = %+v, want United States/America/Chicago", got)
	}

	// geojs 对部分地址只有 ASN 数据,没有任何国家字段。
	if _, outcome := parseGeoJS([]byte(geoJSBodyNoCountry)); outcome != parseNotFound {
		t.Fatalf("outcome = %v, want parseNotFound when the ASN-only response has no country", outcome)
	}
}

func TestHackMyIPParserPrefersCountryNameOverCode(t *testing.T) {
	got, outcome := parseHackMyIP([]byte(hackMyIPBody))
	if outcome != parseOK {
		t.Fatalf("outcome = %v, want parseOK", outcome)
	}
	// location.country 是 "AU";用错字段的话界面上会出现两位国家码。
	if got.Country != "Australia" {
		t.Fatalf("country = %q, want Australia (country_name, not the AU code)", got.Country)
	}
	if got.Region != "South Brisbane" {
		t.Fatalf("region = %q, want South Brisbane", got.Region)
	}

	// 服务自报故障必须算不可达,否则会当成"没这条 IP"去白问后面所有后端。
	if _, outcome := parseHackMyIP([]byte(hackMyIPDownBody)); outcome != parseFailed {
		t.Fatalf("outcome = %v, want parseFailed for success=false", outcome)
	}
}

func TestIPAPISParserHandlesBogonAndNullFields(t *testing.T) {
	got, outcome := parseIPAPIS([]byte(ipapiISBody))
	if outcome != parseOK {
		t.Fatalf("outcome = %v, want parseOK", outcome)
	}
	if got.Country != "Australia" || got.Region != "Brisbane" {
		t.Fatalf("location = %+v, want Australia/Brisbane", got)
	}

	// is_bogon 加上全 null:不能把 "null" 当地名显示出去。
	if _, outcome := parseIPAPIS([]byte(ipapiISBogonBody)); outcome != parseNotFound {
		t.Fatalf("outcome = %v, want parseNotFound for is_bogon", outcome)
	}
}

// 按主机名选解析器,而不是让运维显式写 kind:同一串 TELESRV_GEOIP_ENDPOINTS 对四个
// 免费服务都该成立。识别不出的主机(自建 MaxMind 代理等)走通用解析。
func TestParserForHostSelectsProviderByHostname(t *testing.T) {
	// 每家都用一个只有自己认得的响应体:通用解析会在这里返回 parseNotFound,所以
	// probeOK=true 就等价于"确实选中了专属解析器"。
	cases := []struct {
		host     string
		body     string
		probeOK  bool
		wantName string
	}{
		{host: "reallyfreegeoip.org", body: rfgiBody, probeOK: true, wantName: "United States"},
		{host: "my-reallyfreegeoip.org", body: rfgiBody, probeOK: true, wantName: "United States"},
		{host: "get.geojs.io", body: geoJSBody, probeOK: true, wantName: "United States"},
		{host: "geojs.io", body: geoJSBody, probeOK: true, wantName: "United States"},
		{host: "hackmyip.com", body: hackMyIPBody, probeOK: true, wantName: "Australia"},
		{host: "api.hackmyip.com", body: hackMyIPBody, probeOK: true, wantName: "Australia"},
		{host: "api.ipapi.is", body: ipapiISBody, probeOK: true, wantName: "Australia"},
		{host: "ipapi.is", body: ipapiISBody, probeOK: true, wantName: "Australia"},
		// 大小写与端口都该被容忍。
		{host: "API.IpApi.Is", body: ipapiISBody, probeOK: true, wantName: "Australia"},
		{host: "geo.example.test:8443", body: `{"country_name":"Iceland","city":"Reykjavik"}`, probeOK: true, wantName: "Iceland"},
		// 通用解析认得嵌套结构和常见别名。
		{host: "proxy.internal.lan:8080", body: `{"data":{"location":{"country_name":"Peru","city":"Lima"}}}`, probeOK: true, wantName: "Peru"},
		// 通用解析读不出 ipapi.is 的 null 字段,专属解析能读。
		{host: "proxy.internal.lan", body: ipapiISBody, probeOK: true, wantName: "Australia"},
		// 没有国家名的响应在所有解析器下都必须是"没查到"。
		{host: "api.ipapi.is", body: ipapiISBogonBody},
		{host: "get.geojs.io", body: geoJSBodyNoCountry},
		{host: "proxy.internal.lan", body: `{"city":"Nowhere"}`},
	}
	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			loc, outcome := parserForHost(tc.host)([]byte(tc.body))
			if !tc.probeOK {
				if outcome != parseNotFound {
					t.Fatalf("outcome = %v, want parseNotFound", outcome)
				}
				return
			}
			if outcome != parseOK {
				t.Fatalf("outcome = %v, want parseOK for %s", outcome, tc.host)
			}
			if loc.Country != tc.wantName {
				t.Fatalf("country = %q, want %q", loc.Country, tc.wantName)
			}
		})
	}
}

func TestGenericParserHandlesNestedAndAliasFields(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantCtry   string
		wantRegion string
	}{
		{
			name:       "flat aliases",
			body:       `{"country":"Japan","city":"Tokyo"}`,
			wantCtry:   "Japan",
			wantRegion: "Tokyo",
		},
		{
			name:       "camel case",
			body:       `{"countryName":"Kenya","cityName":"Nairobi"}`,
			wantCtry:   "Kenya",
			wantRegion: "Nairobi",
		},
		{
			name:       "nested under data.location",
			body:       `{"success":true,"data":{"location":{"country_name":"Australia","city":"Brisbane"}}}`,
			wantCtry:   "Australia",
			wantRegion: "Brisbane",
		},
		{
			// GeoIP2 风格代理的字段命名。
			name:       "geoip2 style",
			body:       `{"country":{"names":{"en":"Germany"}},"city":{"names":{"en":"Berlin"}}}`,
			wantCtry:   "",
			wantRegion: "",
		},
		{
			name:     "no country",
			body:     `{"city":"Nowhere","timezone":"UTC"}`,
			wantCtry: "",
		},
		{
			name:     "null values must not leak into the UI",
			body:     `{"country":null,"city":null}`,
			wantCtry: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, outcome := parseGenericJSON([]byte(tc.body))
			if tc.wantCtry == "" {
				if outcome != parseNotFound {
					t.Fatalf("outcome = %v, want parseNotFound", outcome)
				}
				return
			}
			if outcome != parseOK {
				t.Fatalf("outcome = %v, want parseOK", outcome)
			}
			if got.Country != tc.wantCtry || got.Region != tc.wantRegion {
				t.Fatalf("location = %+v, want %q/%q", got, tc.wantCtry, tc.wantRegion)
			}
		})
	}
}

// 主力查不到就落到下一个:这正是多后端存在的理由。geojs 对 1.1.1.1 没有国家字段
// (实测),ipapi.is 有。
func TestResolveFallsOverToNextBackendOnMissingData(t *testing.T) {
	primary := okServer(t, geoJSBodyNoCountry) // ASN-only,没有 country
	fallback := okServer(t, ipapiISBody)
	resolver := chainResolver(t, []*lookupServer{primary, fallback}, nil)

	got := mustResolve(t, resolver, "1.1.1.1")
	want := Location{Country: "Australia", Region: "Brisbane"}
	if got["1.1.1.1"] != want {
		t.Fatalf("Resolve = %+v, want %+v from the fallback backend", got, want)
	}
	if primary.calls() != 1 || fallback.calls() != 1 {
		t.Fatalf("primary calls = %d, fallback calls = %d; want 1 each", primary.calls(), fallback.calls())
	}
}

// 后端坏掉(HTTP 400 / success=false)也必须让位,而不是让整条链返回空。
func TestResolveFallsOverWhenBackendIsDown(t *testing.T) {
	primary := newLookupServer(t, func(string) (int, string) {
		return http.StatusBadRequest, hackMyIPDownBody
	})
	fallback := okServer(t, rfgiBody)
	resolver := chainResolver(t, []*lookupServer{primary, fallback}, nil)

	got := mustResolve(t, resolver, "8.8.8.8")
	want := Location{Country: "United States", Region: "America/Chicago"}
	if got["8.8.8.8"] != want {
		t.Fatalf("Resolve = %+v, want %+v from the healthy backend", got, want)
	}
}

// 限流也是让位理由之一,而且主力被熔断后不能在下一批里继续消耗配额。
func TestResolveFallsOverOnRateLimitAndOpensBreaker(t *testing.T) {
	primary := newLookupServer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limited"}`
	})
	fallback := okServer(t, ipapiISBody)
	resolver := chainResolver(t, []*lookupServer{primary, fallback}, func(c *Config) {
		c.RateLimitThreshold = 1
	})

	for _, ip := range []string{"1.1.1.1", "8.8.8.8"} {
		if got := mustResolve(t, resolver, ip); got[ip].Country != "Australia" {
			t.Fatalf("Resolve(%s) = %+v, want the fallback result", ip, got)
		}
	}
	if state := resolver.backends[0].Breaker().state(); !state.Open || state.Reason != "rate_limit" {
		t.Fatalf("primary breaker = %+v, want an open rate_limit window", state)
	}

	// 熔断期内新的一批:主力一个请求都不发,直接由兜底后端回答。
	before := primary.calls()
	got := mustResolve(t, resolver, "203.0.113.9")
	if got["203.0.113.9"].Country != "Australia" {
		t.Fatalf("Resolve during cooldown = %+v, want the fallback result", got)
	}
	if primary.calls() != before {
		t.Fatalf("primary received %d extra requests during cooldown, want 0", primary.calls()-before)
	}
}

// 硬失败要立刻停发本批剩余请求:后端正在坏掉时,把它剩下的地址一个一个撞满超时预算
// 只会拖慢整批,那正是 failover 要消灭的卡顿。
func TestResolveStopsSendingToFailingBackendMidBatch(t *testing.T) {
	var served int
	var mu sync.Mutex
	primary := newLookupServer(t, func(string) (int, string) {
		mu.Lock()
		served++
		mu.Unlock()
		return http.StatusInternalServerError, `{"error":"boom"}`
	})
	fallback := okServer(t, ipapiISBody)
	resolver := chainResolver(t, []*lookupServer{primary, fallback}, func(c *Config) {
		c.Concurrency = 1 // 串行,让"停发"这件事可确定地观测
	})

	ips := []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "208.67.222.222"}
	got := mustResolve(t, resolver, ips...)
	for _, ip := range ips {
		if got[ip].Country != "Australia" {
			t.Fatalf("Resolve[%s] = %+v, want the fallback result", ip, got[ip])
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if served >= len(ips) {
		t.Fatalf("failing backend served %d of %d addresses, want it to stop early", served, len(ips))
	}
}

// 熔断打开的主力和健康的兜底必须各自独立:一个后端限流不该连累另一个。
func TestBreakersAreIsolatedPerBackend(t *testing.T) {
	limited := newLookupServer(t, func(string) (int, string) {
		return http.StatusTooManyRequests, `{"error":"rate limited"}`
	})
	healthy := okServer(t, ipapiISBody)
	resolver := chainResolver(t, []*lookupServer{limited, healthy}, func(c *Config) {
		c.RateLimitThreshold = 1
	})

	mustResolve(t, resolver, "1.1.1.1")
	if !resolver.backends[0].Breaker().state().Open {
		t.Fatal("limited backend breaker closed, want open")
	}
	if state := resolver.backends[1].Breaker().state(); state.Open {
		t.Fatalf("healthy backend breaker = %+v, want closed: breakers are per backend", state)
	}
}

// failover 的结果照样进正缓存:主力恢复后不该把同一批地址重新问一遍链。
func TestFailingOverResultIsCachedAcrossBackends(t *testing.T) {
	primary := notFoundServer(t)
	fallback := okServer(t, rfgiBody)
	resolver := chainResolver(t, []*lookupServer{primary, fallback}, nil)

	mustResolve(t, resolver, "8.8.8.8")
	primaryCalls, fallbackCalls := primary.calls(), fallback.calls()

	mustResolve(t, resolver, "8.8.8.8")
	if primary.calls() != primaryCalls || fallback.calls() != fallbackCalls {
		t.Fatalf("cached Resolve re-queried the chain: primary %d->%d, fallback %d->%d",
			primaryCalls, primary.calls(), fallbackCalls, fallback.calls())
	}
}

// 负缓存只在整条链都没结果之后才写:某个后端的覆盖缺失不能把地址长期冻住。
func TestNegativeCacheOnlyAfterWholeChainExhausted(t *testing.T) {
	first := notFoundServer(t)
	second := notFoundServer(t)
	resolver := chainResolver(t, []*lookupServer{first, second}, nil)

	if got := mustResolve(t, resolver, "203.0.113.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location when the whole chain misses", got)
	}
	if first.calls() != 1 || second.calls() != 1 {
		t.Fatalf("calls = %d/%d, want the whole chain tried once", first.calls(), second.calls())
	}

	// 负缓存期内不再问链;过期后才重试。
	mustResolve(t, resolver, "203.0.113.7")
	if first.calls() != 1 || second.calls() != 1 {
		t.Fatalf("negative cache missed: calls = %d/%d", first.calls(), second.calls())
	}

	resolver.cache.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	mustResolve(t, resolver, "203.0.113.7")
	if first.calls() != 2 {
		t.Fatalf("calls = %d, want the chain retried after the negative TTL", first.calls())
	}
}

// 同一个后端配两次不会提高成功率,只会让一次会话列表多打一遍请求。
func TestDuplicateEndpointsAreCollapsed(t *testing.T) {
	srv := okServer(t, rfgiBody)
	endpoint := srv.URL + "/json/{ip}"
	resolver, err := New(testConfig(endpoint, endpoint, "  ", endpoint), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = resolver.Close() })

	if got := len(resolver.(*cachedResolver).backends); got != 1 {
		t.Fatalf("backends = %d, want 1 after dedup", got)
	}
	mustResolve(t, resolver, "8.8.8.8")
	if calls := srv.calls(); calls != 1 {
		t.Fatalf("requests = %d, want 1", calls)
	}
}

// 缓存命中时整条链都不该被问到——免费后端的配额经不起无谓的出网。
func TestCachedResolutionSkipsWholeChain(t *testing.T) {
	srv := okServer(t, rfgiBody)
	resolver := chainResolver(t, []*lookupServer{srv}, nil)

	mustResolve(t, resolver, "8.8.8.8", "::ffff:8.8.8.8")
	before := srv.calls()
	mustResolve(t, resolver, "8.8.8.8", "::ffff:8.8.8.8")
	if srv.calls() != before {
		t.Fatalf("cached Resolve made %d extra requests, want 0", srv.calls()-before)
	}
}

// 整条链都不可用时不能拖住超过调用方预算:每个后端各占一次超时,链再长也不能线性放大。
func TestResolveRespectsCallerContextAcrossChain(t *testing.T) {
	// 用不响应的服务器:每个后端都要耗满 timeout,链越长越容易把调用方预算撑爆。
	unresponsive := func() *lookupServer {
		release := make(chan struct{})
		s := newLookupServer(t, func(string) (int, string) {
			<-release
			return http.StatusOK, rfgiBody
		})
		t.Cleanup(func() { close(release) })
		return s
	}
	servers := []*lookupServer{unresponsive(), unresponsive(), unresponsive()}
	resolver := chainResolver(t, servers, func(c *Config) {
		c.Timeout = 300 * time.Millisecond
		c.Concurrency = 3
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := resolver.Resolve(ctx, []string{"8.8.8.8"}); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location when every backend hangs", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Resolve took %v, want the caller context to cut the chain short", elapsed)
	}
}
