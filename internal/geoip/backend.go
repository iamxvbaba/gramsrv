package geoip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"go.uber.org/zap"
)

// maxResponseBytes caps the response body size read. Normal responses are under
// 1KB; this limit only exists to stop a hijacked or rewritten endpoint from
// exhausting memory.
const maxResponseBytes = 64 << 10

// lookupStatus classifies the result of one backend lookup. The classification
// serves two purposes: telling the breaker what is worth stopping for, and
// telling the caller whether to continue to the next backend.
type lookupStatus int

const (
	// statusOK means "a usable country/region was obtained successfully". An empty result is not OK.
	statusOK lookupStatus = iota
	// statusNotFound means "the backend explicitly has no usable data for this
	// IP". Retrying the same backend is pointless, but asking the next backend is
	// worthwhile -- free libraries' coverage ranges do not overlap.
	statusNotFound
	// statusRateLimited is HTTP 429. Only it advances the rate-limit window.
	statusRateLimited
	// statusTimeout is a local context/request timeout.
	statusTimeout
	// statusFailed is any other network/protocol error, including a 5xx from the backend.
	statusFailed
)

// parseOutcome is the result of parsing one response body.
type parseOutcome int

const (
	// parseOK means the response contains a usable country name.
	parseOK parseOutcome = iota
	// parseNotFound means the response is complete but has no usable country name (missing data, is_bogon, 404).
	parseNotFound
	// parseFailed means the response itself is unusable (bad JSON, backend self-reported error), which counts toward the unreachable window.
	parseFailed
)

// responseParser translates one backend's response body into display text.
type responseParser func(body []byte) (Location, parseOutcome)

// backend is one geographic backend. All implementations are HTTP + JSON, differing only in URL shape and response fields.
type backend interface {
	// Name is the short identifier used in logs (usually the hostname).
	Name() string
	// Lookup queries a single normalized address already confirmed publicly routable.
	Lookup(ctx context.Context, addr netip.Addr) (Location, lookupStatus)
	// Breaker returns this backend's own health state machine. Each backend has its own, independent of the others.
	Breaker() *breaker
}

// httpBackend is the HTTP implementation shared by all backends.
//
// The endpoint is configurable rather than hardcoded so that a changed path, a
// different mirror or a new provider needs only an environment-variable change;
// the request shape (GET) and response fields are fixed by the parser.
type httpBackend struct {
	name     string
	endpoint string
	client   *http.Client
	parse    responseParser
	brk      *breaker
	log      *zap.Logger
}

type httpBackendConfig struct {
	endpoint string
	timeout  time.Duration
	parser   responseParser
	breaker  breakerConfig
}

// newHTTPBackend infers the provider from the endpoint URL and builds a backend.
//
// Inferring from the hostname rather than an explicit kind lets operators supply
// just a list of URLs: the same TELESRV_GEOIP_ENDPOINTS works for all four free
// services. Unrecognized hosts (self-hosted MaxMind proxies, etc.) go through
// generic parsing, which tries common field names.
func newHTTPBackend(cfg httpBackendConfig, log *zap.Logger) (backend, error) {
	endpoint := strings.TrimSpace(cfg.endpoint)
	if !strings.Contains(endpoint, "{ip}") {
		return nil, errors.New("endpoint must contain the {ip} placeholder")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("endpoint is not a valid URL")
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return nil, errors.New("endpoint must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("endpoint must include a host")
	}
	parser := parserForHost(parsed.Hostname())
	return &httpBackend{
		name:     parsed.Hostname(),
		endpoint: endpoint,
		client:   newGeoIPHTTPClient(cfg.timeout),
		parse:    parser,
		brk:      newBreaker(cfg.breaker),
		log:      log.Named("backend").Named(parsed.Hostname()),
	}, nil
}

func newGeoIPHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		// Per-request timeout. Not wrapping an extra context beyond
		// http.Client.Timeout keeps "our timeout" and "caller ctx cancellation"
		// distinguishable in logs.
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: timeout,
			ExpectContinueTimeout: time.Second,
		},
	}
}

func (b *httpBackend) Name() string      { return b.name }
func (b *httpBackend) Breaker() *breaker { return b.brk }

// EndpointNames is a diagnostics-only projection of an already validated chain.
// Never return userinfo, path, query or fragments, which may contain credentials.
func EndpointNames(endpoints []string) []string {
	names := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		parsed, err := url.Parse(strings.TrimSpace(endpoint))
		if err == nil && parsed.Hostname() != "" {
			names = append(names, parsed.Hostname())
		}
	}
	return names
}

// Lookup queries a single address and returns display text plus a result classification.
func (b *httpBackend) Lookup(ctx context.Context, addr netip.Addr) (Location, lookupStatus) {
	ip := addr.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.requestURL(ip), nil)
	if err != nil {
		b.log.Error("geoip request construction failed", zap.String("ip", ip), zap.String("error_type", fmt.Sprintf("%T", err)))
		return Location{}, statusFailed
	}
	req.Header.Set("accept", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		// A caller ctx timeout and a local network hiccup converge here.
		// Operators most want to see the former first (a slow backend holding up
		// the RPC), so it is classified separately and logged at warn.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			b.log.Warn("geoip request timed out",
				zap.String("ip", ip),
				zap.Duration("timeout", b.client.Timeout),
				zap.String("error_type", fmt.Sprintf("%T", err)))
			return Location{}, statusTimeout
		}
		b.log.Warn("geoip request failed",
			zap.String("ip", ip),
			zap.Bool("caller_cancelled", errors.Is(err, context.Canceled)),
			zap.String("error_type", fmt.Sprintf("%T", err)))
		return Location{}, statusFailed
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		// Log only structured status; never log response headers or a body that
		// might echo URL credentials.
		b.log.Warn("geoip hit upstream rate limit",
			zap.String("ip", ip),
			zap.Int("status", resp.StatusCode),
			zap.Bool("retry_after_present", resp.Header.Get("Retry-After") != ""),
			zap.String("backend", b.name))
		return Location{}, statusRateLimited
	case resp.StatusCode == http.StatusNotFound:
		// A definite "no such record"; keep asking the next backend.
		return Location{}, statusNotFound
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		if err != nil {
			b.log.Warn("geoip response read failed", zap.String("ip", ip), zap.String("error_type", fmt.Sprintf("%T", err)))
			return Location{}, statusFailed
		}
		return b.parseBody(ip, body)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// A 4xx (other than 404/429) means the request itself was not accepted:
		// when HackMyIP is failing it returns exactly 400 + {"success":false}.
		// That is not "no such IP", but nor should it slow the next round; just yield.
		b.log.Warn("geoip request rejected by upstream",
			zap.String("ip", ip),
			zap.Int("status", resp.StatusCode),
			zap.String("backend", b.name))
		return Location{}, statusNotFound
	default:
		b.log.Warn("geoip request returned an unexpected status code",
			zap.String("ip", ip),
			zap.Int("status", resp.StatusCode),
			zap.String("backend", b.name))
		return Location{}, statusFailed
	}
}

// parseBody parses a response body and translates it into a result classification.
func (b *httpBackend) parseBody(ip string, body []byte) (Location, lookupStatus) {
	loc, outcome := b.parse(body)
	switch outcome {
	case parseOK:
		return loc, statusOK
	case parseNotFound:
		// The upstream is alive but has no data for this (measured: 1.1.1.1 has
		// no country on geojs). This does not count toward any breaker counter;
		// it just gives the next backend a chance.
		return Location{}, statusNotFound
	default:
		b.log.Warn("geoip response parse failed",
			zap.String("ip", ip),
			zap.String("backend", b.name),
			zap.Int("bytes", len(body)))
		return Location{}, statusFailed
	}
}

// requestURL substitutes {ip} with the normalized address. The address has been
// validated by publicAddr and is certainly a valid IP, but it is still run
// through url.PathEscape -- the placeholder may sit in a path segment, and plain
// string concatenation would let unexpected input break the URL structure.
func (b *httpBackend) requestURL(ip string) string {
	return strings.ReplaceAll(b.endpoint, "{ip}", url.PathEscape(ip))
}

// parserForHost picks a parser by hostname. Unrecognized hosts use generic parsing.
func parserForHost(host string) responseParser {
	host = strings.ToLower(strings.TrimSpace(host))
	switch {
	case host == "reallyfreegeoip.org" || strings.HasSuffix(host, ".reallyfreegeoip.org"):
		return parseReallyFreeGeoIP
	case host == "geojs.io" || strings.HasSuffix(host, ".geojs.io"):
		return parseGeoJS
	case host == "hackmyip.com" || strings.HasSuffix(host, ".hackmyip.com"):
		return parseHackMyIP
	case host == "ipapi.is" || strings.HasSuffix(host, ".ipapi.is"):
		return parseIPAPIS
	default:
		return parseGenericJSON
	}
}
