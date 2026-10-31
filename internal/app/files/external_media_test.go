package files

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsBlockedExternalIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},      // loopback
		{"::1", true},            // loopback v6
		{"10.0.0.5", true},       // private
		{"172.16.3.4", true},     // private
		{"192.168.1.1", true},    // private
		{"169.254.1.1", true},    // link-local
		{"fe80::1", true},        // link-local v6
		{"0.0.0.0", true},        // unspecified
		{"100.64.0.1", true},     // CGNAT
		{"100.127.255.1", true},  // CGNAT upper bound
		{"224.0.0.1", true},      // multicast
		{"8.8.8.8", false},       // public
		{"1.1.1.1", false},       // public
		{"100.63.255.1", false},  // outside the CGNAT lower bound (public)
		{"100.128.0.1", false},   // outside the CGNAT upper bound (public)
		{"2606:4700:4700::1111", false}, // public v6
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("parse %s failed", c.ip)
		}
		if got := isBlockedExternalIP(ip); got != c.blocked {
			t.Errorf("isBlockedExternalIP(%s) = %v, want %v", c.ip, got, c.blocked)
		}
	}
}

// TestExternalMediaFetcherSSRFGuard verifies SSRF protection: httptest is on loopback, so
// allowPrivate=false must block (not connect to the internal network) and allowPrivate=true must
// allow fetching the bytes through.
func TestExternalMediaFetcherSSRFGuard(t *testing.T) {
	body := []byte("hello-external-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	// Production config (allowPrivate=false): a loopback target is blocked by SSRF protection.
	guarded := newExternalMediaFetcher(DefaultExternalMediaMaxBytes, DefaultExternalMediaRatePerMin, false)
	if _, _, err := guarded.fetch(context.Background(), srv.URL); !errors.Is(err, ErrExternalMediaInvalid) {
		t.Fatalf("SSRF guard fetch err = %v, want ErrExternalMediaInvalid (loopback should be blocked)", err)
	}

	// Test bypass (allowPrivate=true): the fetch succeeds.
	open := newExternalMediaFetcher(DefaultExternalMediaMaxBytes, DefaultExternalMediaRatePerMin, true)
	data, ct, err := open.fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("open fetch err = %v", err)
	}
	if string(data) != string(body) {
		t.Fatalf("fetched %q, want %q", data, body)
	}
	if ct != "application/octet-stream" {
		t.Fatalf("content-type = %q, want application/octet-stream", ct)
	}
}

// TestExternalMediaFetcherRejectsBadURL directly rejects non-http(s)/empty host.
func TestExternalMediaFetcherRejectsBadURL(t *testing.T) {
	f := newExternalMediaFetcher(DefaultExternalMediaMaxBytes, DefaultExternalMediaRatePerMin, true)
	for _, bad := range []string{"", "ftp://x/y", "file:///etc/passwd", "javascript:alert(1)", "http://", "not a url"} {
		if _, _, err := f.fetch(context.Background(), bad); !errors.Is(err, ErrExternalMediaInvalid) {
			t.Errorf("fetch(%q) err = %v, want ErrExternalMediaInvalid", bad, err)
		}
	}
}

// TestExternalMediaFetcherSizeLimit rejects over the size limit.
func TestExternalMediaFetcherSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 2048))
	}))
	defer srv.Close()
	f := newExternalMediaFetcher(1024, DefaultExternalMediaRatePerMin, true)
	if _, _, err := f.fetch(context.Background(), srv.URL); !errors.Is(err, ErrExternalMediaInvalid) {
		t.Fatalf("oversize fetch err = %v, want ErrExternalMediaInvalid", err)
	}
}

// TestExternalMediaDisabled: Create*FromURL returns ErrExternalMediaDisabled when not enabled.
func TestExternalMediaDisabled(t *testing.T) {
	s := &Service{}
	if _, err := s.CreatePhotoFromURL(context.Background(), "http://x/y.png"); !errors.Is(err, ErrExternalMediaDisabled) {
		t.Fatalf("disabled photo err = %v, want ErrExternalMediaDisabled", err)
	}
	if _, err := s.CreateDocumentFromURL(context.Background(), "http://x/y.bin"); !errors.Is(err, ErrExternalMediaDisabled) {
		t.Fatalf("disabled doc err = %v, want ErrExternalMediaDisabled", err)
	}
}
