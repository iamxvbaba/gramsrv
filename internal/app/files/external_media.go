package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"telesrv/internal/domain"
)

// External media: inputMediaPhotoExternal / inputMediaDocumentExternal — the client provides a
// URL and the server fetches it and mints a Photo/Document. Fetching an arbitrary user-controlled
// URL makes security the core concern:
//   - SSRF protection: a custom Dialer.Control checks the **resolved target IP** before connecting,
//     blocking loopback/private/link-local/CGNAT/multicast/unspecified. Because it checks on every
//     actual dial, it also stops DNS rebinding (a public domain resolving to an internal IP) and
//     redirects (every hop re-dials → re-checks).
//   - http/https only; a redirect limit; a response size limit (LimitReader); a request timeout; a
//     global fetch rate limit (to prevent an amplification attack where one message triggers many
//     server-side external fetches).

var (
	// ErrExternalMediaDisabled means external media fetching is not enabled (the rpc layer maps it to MEDIA_INVALID).
	ErrExternalMediaDisabled = errors.New("external media disabled")
	// ErrExternalMediaInvalid means the URL is invalid / blocked by SSRF protection / upstream failed / over a limit.
	ErrExternalMediaInvalid = errors.New("external media invalid")
)

const (
	externalMediaTimeout      = 15 * time.Second
	externalMediaMaxRedirects = 5
	// DefaultExternalMediaMaxBytes is the fetch response body limit.
	DefaultExternalMediaMaxBytes = int64(10 << 20)
	// DefaultExternalMediaRatePerMin is the global per-minute fetch limit (to prevent amplification attacks).
	DefaultExternalMediaRatePerMin = 60
	externalMediaRateWindow        = time.Minute
)

type externalMediaFetcher struct {
	client    *http.Client
	maxBytes  int64
	rateLimit int

	mu         sync.Mutex
	fetchTimes []time.Time
}

// WithExternalMedia enables external media fetching (inputMediaPhoto/DocumentExternal).
// maxBytes<=0 uses the default; ratePerMin<=0 uses the default. SSRF protection is always on.
func WithExternalMedia(maxBytes int64, ratePerMin int) Option {
	return func(s *Service) {
		if maxBytes <= 0 {
			maxBytes = DefaultExternalMediaMaxBytes
		}
		if ratePerMin <= 0 {
			ratePerMin = DefaultExternalMediaRatePerMin
		}
		s.externalMedia = newExternalMediaFetcher(maxBytes, ratePerMin, false)
	}
}

// newExternalMediaFetcher constructs the fetcher. allowPrivate is for tests only (pointing at
// httptest loopback); in production it is always false.
func newExternalMediaFetcher(maxBytes int64, ratePerMin int, allowPrivate bool) *externalMediaFetcher {
	dialer := &net.Dialer{Timeout: externalMediaTimeout}
	dialer.Control = func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return ErrExternalMediaInvalid
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return ErrExternalMediaInvalid
		}
		if !allowPrivate && isBlockedExternalIP(ip) {
			return fmt.Errorf("%w: blocked address %s (SSRF guard)", ErrExternalMediaInvalid, host)
		}
		return nil
	}
	client := &http.Client{
		Timeout:   externalMediaTimeout,
		Transport: &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= externalMediaMaxRedirects {
				return fmt.Errorf("%w: too many redirects", ErrExternalMediaInvalid)
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return fmt.Errorf("%w: blocked redirect scheme %q", ErrExternalMediaInvalid, req.URL.Scheme)
			}
			return nil
		},
	}
	return &externalMediaFetcher{client: client, maxBytes: maxBytes, rateLimit: ratePerMin}
}

// isBlockedExternalIP reports whether an address is an internal/special one that must not be fetched externally (SSRF protection).
func isBlockedExternalIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	// CGNAT 100.64.0.0/10 (carrier-grade NAT, often used for internal infrastructure).
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return true
	}
	return false
}

func (f *externalMediaFetcher) allowFetch() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	kept := f.fetchTimes[:0]
	for _, at := range f.fetchTimes {
		if now.Sub(at) <= externalMediaRateWindow {
			kept = append(kept, at)
		}
	}
	f.fetchTimes = kept
	if len(f.fetchTimes) >= f.rateLimit {
		return false
	}
	f.fetchTimes = append(f.fetchTimes, now)
	return true
}

// fetch fetches a URL and returns (bytes, content-type). The SSRF check happens at dial time.
func (f *externalMediaFetcher) fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", ErrExternalMediaInvalid
	}
	if !f.allowFetch() {
		return nil, "", fmt.Errorf("%w: rate limited", ErrExternalMediaInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, externalMediaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", ErrExternalMediaInvalid
	}
	req.Header.Set("User-Agent", "telesrv-media-fetch")
	resp, err := f.client.Do(req)
	if err != nil {
		// Includes SSRF blocks, timeouts and transport errors.
		return nil, "", fmt.Errorf("%w: %v", ErrExternalMediaInvalid, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("%w: upstream status %d", ErrExternalMediaInvalid, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: read body: %v", ErrExternalMediaInvalid, err)
	}
	if len(data) == 0 || int64(len(data)) > f.maxBytes {
		return nil, "", fmt.Errorf("%w: body size %d", ErrExternalMediaInvalid, len(data))
	}
	contentType := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = contentType[:i]
	}
	return data, strings.TrimSpace(contentType), nil
}

// CreatePhotoFromURL fetches a URL and mints a Photo (CreatePhotoFromBytes decodes to verify it is an image).
func (s *Service) CreatePhotoFromURL(ctx context.Context, rawURL string) (domain.Photo, error) {
	if s == nil || s.externalMedia == nil {
		return domain.Photo{}, ErrExternalMediaDisabled
	}
	data, _, err := s.externalMedia.fetch(ctx, rawURL)
	if err != nil {
		return domain.Photo{}, err
	}
	photo, err := s.CreatePhotoFromBytes(ctx, data)
	if err != nil {
		// Non-image bytes → ErrPhotoInvalid, uniformly reported externally as external invalid.
		return domain.Photo{}, fmt.Errorf("%w: %v", ErrExternalMediaInvalid, err)
	}
	return photo, nil
}

// CreateDocumentFromURL fetches a URL and mints a Document: the mime comes from Content-Type and the filename from the URL basename.
func (s *Service) CreateDocumentFromURL(ctx context.Context, rawURL string) (domain.Document, error) {
	if s == nil || s.externalMedia == nil {
		return domain.Document{}, ErrExternalMediaDisabled
	}
	data, contentType, err := s.externalMedia.fetch(ctx, rawURL)
	if err != nil {
		return domain.Document{}, err
	}
	mime := contentType
	if mime == "" {
		mime = "application/octet-stream"
	}
	spec := domain.DocumentSpec{
		MimeType:   mime,
		Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrFilename, FileName: externalMediaFilename(rawURL)}},
	}
	doc, err := s.CreateDocumentFromBytes(ctx, data, spec)
	if err != nil {
		return domain.Document{}, fmt.Errorf("%w: %v", ErrExternalMediaInvalid, err)
	}
	return doc, nil
}

// externalMediaFilename takes the basename from the URL path; falls back to a generic name when missing.
func externalMediaFilename(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err == nil {
		if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return "file"
}
