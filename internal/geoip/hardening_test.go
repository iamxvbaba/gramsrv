package geoip

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestResolveCancellationWaitsForInflightHTTPParser(t *testing.T) {
	primary := okServer(t, `{"country":"Australia","city":"Brisbane"}`)
	fallback := okServer(t, rfgiBody)
	r := chainResolver(t, []*lookupServer{primary, fallback}, func(c *Config) { c.Concurrency = 1 })
	b := r.backends[0].(*httpBackend)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(finish)
	parse := b.parse
	b.parse = func(body []byte) (Location, parseOutcome) {
		close(entered)
		<-release
		return parse(body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan map[string]Location, 1)
	go func() { done <- r.Resolve(ctx, []string{"8.8.8.8", "1.1.1.1"}) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight parser did not start")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("Resolve returned before its successful in-flight worker finished")
	case <-time.After(25 * time.Millisecond):
	}
	finish()
	select {
	case got := <-done:
		queried := primary.seen()
		if len(queried) != 1 || len(got) != 1 || got[queried[0]].Country != "Australia" {
			t.Fatalf("queried = %v, partial result = %+v; want the completed lookup only", queried, got)
		}
		for _, ip := range []string{"8.8.8.8", "1.1.1.1"} {
			if entry, found := r.cache.get(netip.MustParseAddr(ip)); found && !entry.resolved {
				t.Fatalf("cancelled chain wrote a negative cache entry for %s", ip)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve did not finish after the parser was released")
	}
	if calls := fallback.calls(); calls != 0 {
		t.Fatalf("cancelled batch queried fallback %d times", calls)
	}
}

func TestResolveCallerCancellationDoesNotPoisonHealthOrCache(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			started := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				once.Do(func() {
					close(started)
					<-req.Context().Done()
				})
				_, _ = io.WriteString(w, rfgiBody)
			}))
			t.Cleanup(server.Close)
			cfg := testConfig(server.URL + "/json/{ip}")
			cfg.DownThreshold = 1
			r := newTestResolver(t, cfg).(*cachedResolver)
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 200*time.Millisecond)
			}
			defer cancel()
			done := make(chan struct{})
			go func() {
				r.Resolve(ctx, []string{"8.8.8.8"})
				close(done)
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("HTTP lookup did not start")
			}
			if !deadline {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled HTTP lookup did not finish")
			}
			if r.backends[0].Breaker().state().Open {
				t.Fatal("caller cancellation opened the provider circuit")
			}
			if _, found := r.cache.get(netip.MustParseAddr("8.8.8.8")); found {
				t.Fatal("caller cancellation was cached as a provider miss")
			}
			if got := mustResolve(t, r, "8.8.8.8"); got["8.8.8.8"].Country != "United States" {
				t.Fatalf("fresh retry after cancellation = %+v, want provider result", got)
			}
		})
	}
}

func TestResolveDeadlineDrainsFullBatchBeforeReadingResults(t *testing.T) {
	primary := okServer(t, rfgiBody)
	fallback := okServer(t, rfgiBody)
	r := chainResolver(t, []*lookupServer{primary, fallback}, nil)
	b := r.backends[0].(*httpBackend)
	started := make(chan struct{})
	release := make(chan struct{})
	var count atomic.Int32
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	t.Cleanup(finish)
	parse := b.parse
	b.parse = func(body []byte) (Location, parseOutcome) {
		if count.Add(1) == int32(r.concurrency) {
			close(started)
		}
		<-release
		return parse(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ips := []string{"8.8.8.8", "1.1.1.1", "9.9.9.9", "208.67.222.222", "8.8.4.4"}
	done := make(chan map[string]Location, 1)
	go func() { done <- r.Resolve(ctx, ips) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the full in-flight batch did not start")
	}
	<-ctx.Done()
	select {
	case <-done:
		t.Fatal("deadline returned before the full batch drained")
	case <-time.After(25 * time.Millisecond):
	}
	finish()
	select {
	case got := <-done:
		if len(got) != r.concurrency || primary.calls() != r.concurrency || fallback.calls() != 0 {
			t.Fatalf("partial result = %+v, primary/fallback calls = %d/%d", got, primary.calls(), fallback.calls())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("deadline batch did not finish after its parsers drained")
	}
}

func TestResolveAlreadyCancelledDoesNotQueryOrCache(t *testing.T) {
	srv := okServer(t, rfgiBody)
	r := chainResolver(t, []*lookupServer{srv}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := r.Resolve(ctx, []string{"8.8.8.8", "1.1.1.1"}); len(got) != 0 {
		t.Fatalf("already cancelled Resolve = %+v", got)
	}
	if srv.calls() != 0 || len(r.cache.entries) != 0 || r.backends[0].Breaker().state().Open {
		t.Fatal("already cancelled Resolve mutated cache, provider health or sent HTTP")
	}
}

type geoIPRoundTripFunc func(*http.Request) (*http.Response, error)

func (f geoIPRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type geoIPErrorReader struct{ err error }

func (r geoIPErrorReader) Read([]byte) (int, error) { return 0, r.err }

func TestBackendDiagnosticsNeverExposeEndpointCredentials(t *testing.T) {
	const marker = "geoip-test-credential-marker"
	endpoint := "https://test-user:" + marker + "@geo.example.test/" + marker + "/{ip}?key=" + marker + "#" + marker
	for _, mode := range []string{"rate_limit", "rejected", "server_error", "parse_error", "transport_error", "body_error"} {
		t.Run(mode, func(t *testing.T) {
			core, observed := observer.New(zap.DebugLevel)
			b, err := newHTTPBackend(httpBackendConfig{endpoint: endpoint, timeout: time.Second}, zap.New(core))
			if err != nil {
				t.Fatal(err)
			}
			h := b.(*httpBackend)
			h.client.Transport = geoIPRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("key") != marker || !strings.Contains(req.URL.Path, marker) {
					t.Error("credentials/path were removed from the actual provider request")
				}
				if mode == "transport_error" {
					return nil, errors.New("transport echoed " + req.URL.String())
				}
				status := http.StatusOK
				switch mode {
				case "rate_limit":
					status = http.StatusTooManyRequests
				case "rejected":
					status = http.StatusForbidden
				case "server_error":
					status = http.StatusInternalServerError
				}
				body := io.NopCloser(strings.NewReader("upstream echoed " + marker))
				if mode == "body_error" {
					body = io.NopCloser(geoIPErrorReader{errors.New("body echoed " + marker)})
				}
				return &http.Response{StatusCode: status, Body: body, Header: http.Header{"Retry-After": []string{marker}}}, nil
			})
			h.Lookup(context.Background(), netip.MustParseAddr("8.8.8.8"))
			if len(observed.All()) == 0 {
				t.Fatal("expected diagnostic log for the failure")
			}
			for _, entry := range observed.All() {
				fields, err := json.Marshal(entry.ContextMap())
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(entry.Message+entry.LoggerName+string(fields), marker) {
					t.Fatal("GeoIP diagnostics exposed an endpoint credential or echoed response")
				}
			}
		})
	}
	if got := EndpointNames([]string{endpoint, "https://api.ipapi.is/?q={ip}&key=" + marker}); !reflect.DeepEqual(got, []string{"geo.example.test", "api.ipapi.is"}) {
		t.Fatalf("startup diagnostics = %v, want hostname-only ordered list", got)
	}
}

func TestNewValidationNeverExposesEndpointCredentials(t *testing.T) {
	const marker = "geoip-test-credential-marker"
	for _, endpoint := range []string{
		"https://geo.example.test/?key=" + marker,
		"https://geo.example.test/%zz/{ip}?key=" + marker,
		"ftp://geo.example.test/{ip}?key=" + marker,
		"https:///{ip}?key=" + marker,
	} {
		_, err := New(testConfig(endpoint), nil)
		if err == nil || strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "geo.example.test") {
			t.Fatalf("validation did not fail with a credential-safe error: %v", err)
		}
	}
}

func TestIPAPISParserSupportsDocumentedKeyedResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		loc  Location
		out  parseOutcome
	}{
		{"keyed city", `{"is_bogon":false,"company":{"name":"VPS ACE"},"asn":{"asn":36352,"country":"us"},"location":{"country":"United States","state":"New York","city":"Buffalo","timezone":"America/New_York"}}`, Location{"United States", "Buffalo"}, parseOK},
		{"keyed state", `{"location":{"country":"United States","state":"New York","city":null}}`, Location{"United States", "New York"}, parseOK},
		{"keyed timezone", `{"location":{"country":"United States","timezone":"America/New_York"}}`, Location{"United States", "America/New_York"}, parseOK},
		{"keyed bogon", `{"is_bogon":true,"company":null,"asn":null,"location":null}`, Location{}, parseNotFound},
		{"no geographic country", `{"asn":{"country":"us"},"location":{"country":null,"city":"Buffalo"}}`, Location{}, parseNotFound},
		{"service error", `{"error":"Invalid API key"}`, Location{}, parseFailed},
		{"invalid JSON", `{`, Location{}, parseFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loc, out := parserForHost("api.ipapi.is")([]byte(tc.body))
			if loc != tc.loc || out != tc.out {
				t.Fatalf("parsed = %+v/%v, want %+v/%v", loc, out, tc.loc, tc.out)
			}
		})
	}
}

func TestIPAPISKeyedLookupIsHealthyAndCached(t *testing.T) {
	r := newTestResolver(t, testConfig("https://api.ipapi.is/?q={ip}&key=test-only-key")).(*cachedResolver)
	b := r.backends[0].(*httpBackend)
	var calls int
	b.client.Transport = geoIPRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Query().Get("q") != "8.8.8.8" || req.URL.Query().Get("key") != "test-only-key" {
			t.Error("keyed provider request did not retain query parameters")
		}
		body := `{"company":{"name":"Google"},"asn":{"asn":15169},"location":{"country":"United States","city":"Mountain View","state":"California"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	for i := 0; i < 2; i++ {
		got := mustResolve(t, r, "8.8.8.8")
		if got["8.8.8.8"] != (Location{"United States", "Mountain View"}) {
			t.Fatalf("keyed Resolve = %+v", got)
		}
	}
	if calls != 1 || b.Breaker().state().Open {
		t.Fatalf("keyed provider requests = %d, health = %+v; want one cached, healthy result", calls, b.Breaker().state())
	}
}
