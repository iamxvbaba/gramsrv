package geoip

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// This file covers the behavior of the failover chain. The response bodies use
// measured 2026-09 samples, so these assertions also pin down each provider's
// real field shapes -- their docs and actual responses do not always agree.

// chainResolver builds a resolver from a set of httptest endpoints, skipping New's config validation.
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
	// geoJSBody is the measured get.geojs.io response: only ASN-level coverage, no city/region.
	geoJSBody = `{"accuracy":1000,"area_code":"0","asn":15169,"continent_code":"NA",` +
		`"country":"United States","country_code":"US","country_code3":"USA","ip":"8.8.8.8",` +
		`"latitude":"37.751","longitude":"-97.822","organization":"AS15169 Google LLC",` +
		`"organization_name":"Google LLC","timezone":"America/Chicago"}`
	// geoJSBodyNoCountry is geojs's measured response for 1.1.1.1: not even a country field.
	geoJSBodyNoCountry = `{"area_code":"0","asn":13335,"ip":"1.1.1.1","latitude":"nil",` +
		`"longitude":"nil","organization":"AS13335 Cloudflare, Inc.",` +
		`"organization_name":"Cloudflare, Inc."}`
	// rfgiBody is the measured reallyfreegeoip.org response.
	rfgiBody = `{"ip":"8.8.8.8","country_code":"US","country_name":"United States","region_code":"",` +
		`"region_name":"","city":"","zip_code":"","time_zone":"America/Chicago","latitude":37.751,` +
		`"longitude":-97.822,"metro_code":0}`
	// rfgiBodyUnknown is its measured response for an address it cannot find: 200 + all-empty strings.
	rfgiBodyUnknown = `{"ip":"203.0.113.7","country_code":"","country_name":"","region_code":"",` +
		`"region_name":"","city":"","zip_code":"","time_zone":"","latitude":0,"longitude":0,"metro_code":0}`
	// hackMyIPBody is the measured hackmyip.com /api/lookup response. Note that
	// location.country is a two-letter country code; the country name is in country_name.
	hackMyIPBody = `{"success":true,"data":{"ip":"1.1.1.1","location":{"city":"South Brisbane",` +
		`"region":"Queensland","country":"AU","country_name":"Australia","latitude":-27.4766,` +
		`"longitude":153.0166,"timezone":"Australia/Brisbane","postal_code":"4101"}}}`
	// hackMyIPDownBody is its measured response when the service is failing: HTTP 400 + success=false.
	hackMyIPDownBody = `{"success":false,"error":"IP lookup service temporarily unavailable. Please try again."}`
	// ipapiISBody is the measured api.ipapi.is response.
	ipapiISBody = `{"ip":"1.1.1.1","is_bogon":false,"company":"APNIC Research and Development",` +
		`"asn":"AS13335 Cloudflare, Inc.","city":"Brisbane","region":"Queensland",` +
		`"country":"Australia","lat":-27.46754,"lon":153.02809,"timezone":"Australia/Brisbane"}`
	// ipapiISBogonBody is its measured response for a reserved range: every field null.
	ipapiISBogonBody = `{"ip":"203.0.113.7","is_bogon":true,"company":null,"asn":null,"city":null,` +
		`"region":null,"country":null,"lat":null,"lon":null,"timezone":null}`
)

// okServer makes every address return the given response.
func okServer(t *testing.T, body string) *lookupServer {
	t.Helper()
	return newLookupServer(t, func(string) (int, string) { return http.StatusOK, body })
}

// notFoundServer makes every address return 404.
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

	// For some addresses geojs has only ASN data and no country field at all.
	if _, outcome := parseGeoJS([]byte(geoJSBodyNoCountry)); outcome != parseNotFound {
		t.Fatalf("outcome = %v, want parseNotFound when the ASN-only response has no country", outcome)
	}
}

func TestHackMyIPParserPrefersCountryNameOverCode(t *testing.T) {
	got, outcome := parseHackMyIP([]byte(hackMyIPBody))
	if outcome != parseOK {
		t.Fatalf("outcome = %v, want parseOK", outcome)
	}
	// location.country is "AU"; using the wrong field would show a two-letter country code in the UI.
	if got.Country != "Australia" {
		t.Fatalf("country = %q, want Australia (country_name, not the AU code)", got.Country)
	}
	if got.Region != "South Brisbane" {
		t.Fatalf("region = %q, want South Brisbane", got.Region)
	}

	// A self-reported service failure must count as unreachable, otherwise it is
	// taken as "no such IP" and pointlessly queries every later backend.
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

	// is_bogon plus all-null: "null" must not be shown as a place name.
	if _, outcome := parseIPAPIS([]byte(ipapiISBogonBody)); outcome != parseNotFound {
		t.Fatalf("outcome = %v, want parseNotFound for is_bogon", outcome)
	}
}

// Pick the parser by hostname rather than making operators write an explicit
// kind: the same TELESRV_GEOIP_ENDPOINTS should work for all four free services.
// Unrecognized hosts (self-hosted MaxMind proxies, etc.) use generic parsing.
func TestParserForHostSelectsProviderByHostname(t *testing.T) {
	// Each provider gets a response body only it recognizes: generic parsing
	// returns parseNotFound here, so probeOK=true is equivalent to "the
	// dedicated parser really was selected".
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
		// Both case and port should be tolerated.
		{host: "API.IpApi.Is", body: ipapiISBody, probeOK: true, wantName: "Australia"},
		{host: "geo.example.test:8443", body: `{"country_name":"Iceland","city":"Reykjavik"}`, probeOK: true, wantName: "Iceland"},
		// Generic parsing recognizes nested structures and common aliases.
		{host: "proxy.internal.lan:8080", body: `{"data":{"location":{"country_name":"Peru","city":"Lima"}}}`, probeOK: true, wantName: "Peru"},
		// Generic parsing cannot read ipapi.is's null fields; the dedicated parser can.
		{host: "proxy.internal.lan", body: ipapiISBody, probeOK: true, wantName: "Australia"},
		// A response without a country name must be "not found" under every parser.
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
			// Field naming of a GeoIP2-style proxy.
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

// When the primary cannot find the address, fall to the next one: that is the
// very reason multiple backends exist. geojs has no country field for 1.1.1.1
// (measured); ipapi.is does.
func TestResolveFallsOverToNextBackendOnMissingData(t *testing.T) {
	primary := okServer(t, geoJSBodyNoCountry) // ASN-only, no country
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

// A failing backend (HTTP 400 / success=false) must also yield, rather than making the whole chain return empty.
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

// Rate limiting is also a reason to yield, and once the primary is tripped it must not keep consuming quota in the next batch.
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

	// A new batch during the breaker window: the primary sends no requests at
	// all, and the fallback backend answers directly.
	before := primary.calls()
	got := mustResolve(t, resolver, "8.8.8.9")
	if got["8.8.8.9"].Country != "Australia" {
		t.Fatalf("Resolve during cooldown = %+v, want the fallback result", got)
	}
	if primary.calls() != before {
		t.Fatalf("primary received %d extra requests during cooldown, want 0", primary.calls()-before)
	}
}

// A hard failure must immediately stop sending the rest of the batch: when a
// backend is failing, grinding its remaining addresses through the full timeout
// budget one by one only slows the batch -- exactly the stall failover exists to
// eliminate.
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
		c.Concurrency = 1 // serial, so the "stop sending" behavior is deterministically observable
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
	if served != 1 {
		t.Fatalf("failing backend served %d of %d addresses, want exactly 1 before handoff", served, len(ips))
	}
}

// A tripped primary and a healthy fallback must stay independent: one backend's rate limiting must not implicate another.
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

// A failover result still goes into the positive cache: once the primary
// recovers, the same batch of addresses must not be re-queried through the chain.
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

// A negative cache entry is written only after the whole chain misses: one backend's coverage gap must not freeze an address for long.
func TestNegativeCacheOnlyAfterWholeChainExhausted(t *testing.T) {
	first := notFoundServer(t)
	second := notFoundServer(t)
	resolver := chainResolver(t, []*lookupServer{first, second}, nil)

	if got := mustResolve(t, resolver, "8.8.8.7"); len(got) != 0 {
		t.Fatalf("Resolve = %+v, want no location when the whole chain misses", got)
	}
	if first.calls() != 1 || second.calls() != 1 {
		t.Fatalf("calls = %d/%d, want the whole chain tried once", first.calls(), second.calls())
	}

	// Within the negative-cache window the chain is not queried; only after it expires is it retried.
	mustResolve(t, resolver, "8.8.8.7")
	if first.calls() != 1 || second.calls() != 1 {
		t.Fatalf("negative cache missed: calls = %d/%d", first.calls(), second.calls())
	}

	resolver.cache.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	mustResolve(t, resolver, "8.8.8.7")
	if first.calls() != 2 {
		t.Fatalf("calls = %d, want the chain retried after the negative TTL", first.calls())
	}
}

// Configuring the same backend twice does not improve the success rate; it only doubles the requests for one session list.
func TestDuplicateEndpointsAreCollapsed(t *testing.T) {
	srv := okServer(t, rfgiBody)
	endpoint := srv.URL + "/json/{ip}"
	resolver, err := New(testConfig(endpoint, "  "+endpoint+"  ", "  ", endpoint), nil)
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

// On a cache hit the whole chain must not be queried -- free backends' quotas cannot afford pointless network traffic.
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

// When the whole chain is unavailable it must not hold up the caller beyond
// their budget: each backend costs one timeout, so a longer chain must not scale
// linearly.
func TestResolveRespectsCallerContextAcrossChain(t *testing.T) {
	// Use unresponsive servers: every backend burns its full timeout, and the
	// longer the chain the easier it is to blow the caller's budget.
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
