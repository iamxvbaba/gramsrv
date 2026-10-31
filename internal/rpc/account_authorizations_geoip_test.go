package rpc

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/domain"
	"telesrv/internal/geoip"
)

// stubGeoIPResolver records resolve requests, used to verify "must be batched" and fallback behavior.
type stubGeoIPResolver struct {
	mu        sync.Mutex
	locations map[string]geoip.Location
	calls     int
	batches   [][]string
	// waitBeforeResolve lets the test verify the list is still returned when resolution times out.
	waitBeforeResolve time.Duration
}

func (s *stubGeoIPResolver) Resolve(ctx context.Context, ips []string) map[string]geoip.Location {
	s.mu.Lock()
	s.calls++
	s.batches = append(s.batches, append([]string(nil), ips...))
	wait := s.waitBeforeResolve
	s.mu.Unlock()

	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			// The upstream-capped context expired: the caller must already have fallen back, so return an empty result directly.
			return nil
		}
	}

	out := make(map[string]geoip.Location, len(ips))
	for _, ip := range ips {
		if loc, ok := s.locations[ip]; ok {
			out[ip] = loc
		}
	}
	return out
}

func (s *stubGeoIPResolver) Close() error { return nil }

func (s *stubGeoIPResolver) requestedIPs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, batch := range s.batches {
		out = append(out, batch...)
	}
	sort.Strings(out)
	return out
}

func newGeoIPAuthorizationsRouter(t *testing.T, resolver geoip.Resolver, items ...domain.Authorization) *Router {
	t.Helper()
	auth := &captureAuthService{authorizations: items}
	return New(Config{}, Deps{Auth: auth, GeoIP: resolver}, zaptest.NewLogger(t), clock.System)
}

// With no geo backend configured the list must be byte-identical to before it was enabled: the
// placeholder text must not disappear or become an empty string.
func TestAccountGetAuthorizationsWithoutGeoIPKeepsUnknownPlaceholder(t *testing.T) {
	router := newGeoIPAuthorizationsRouter(t, nil, domain.Authorization{
		AuthKeyID:   [8]byte{1},
		IP:          "203.0.113.7",
		DeviceModel: "iPhone",
	})

	out, err := router.onAccountGetAuthorizations(WithUserID(context.Background(), 42))
	if err != nil {
		t.Fatalf("getAuthorizations: %v", err)
	}
	if len(out.Authorizations) != 1 {
		t.Fatalf("authorizations = %d, want 1", len(out.Authorizations))
	}
	got := out.Authorizations[0]
	if got.Country != authorizationLocationUnknown || got.Region != authorizationLocationUnknown {
		t.Fatalf("country/region = %q/%q, want %q placeholder", got.Country, got.Region, authorizationLocationUnknown)
	}
	if got.IP != "203.0.113.7" {
		t.Fatalf("ip = %q, want the stored address", got.IP)
	}
}

func TestAccountGetAuthorizationsFillsResolvedLocations(t *testing.T) {
	resolver := &stubGeoIPResolver{locations: map[string]geoip.Location{
		"203.0.113.7":  {Country: "Finland", Region: "Helsinki"},
		"198.51.100.9": {Country: "Germany", Region: "Berlin"},
	}}
	router := newGeoIPAuthorizationsRouter(t, resolver,
		domain.Authorization{AuthKeyID: [8]byte{1}, IP: "203.0.113.7"},
		domain.Authorization{AuthKeyID: [8]byte{2}, IP: "198.51.100.9"},
		// The backend cannot find this device: must fall back, not drop the whole record.
		domain.Authorization{AuthKeyID: [8]byte{3}, IP: "192.0.2.44"},
	)

	out, err := router.onAccountGetAuthorizations(WithUserID(context.Background(), 42))
	if err != nil {
		t.Fatalf("getAuthorizations: %v", err)
	}
	if len(out.Authorizations) != 3 {
		t.Fatalf("authorizations = %d, want 3", len(out.Authorizations))
	}
	want := []struct {
		country, region string
	}{
		{"Finland", "Helsinki"},
		{"Germany", "Berlin"},
		{authorizationLocationUnknown, authorizationLocationUnknown},
	}
	for i, w := range want {
		got := out.Authorizations[i]
		if got.Country != w.country || got.Region != w.region {
			t.Fatalf("authorization[%d] country/region = %q/%q, want %q/%q", i, got.Country, got.Region, w.country, w.region)
		}
	}
	// Resolving one by one multiplies the upstream cost by the device count, so it must be submitted in one batch.
	if calls := resolver.calls; calls != 1 {
		t.Fatalf("resolver calls = %d, want 1 batched call", calls)
	}
}

func TestAccountGetAuthorizationsSkipsLookupWithoutAddresses(t *testing.T) {
	resolver := &stubGeoIPResolver{}
	router := newGeoIPAuthorizationsRouter(t, resolver,
		domain.Authorization{AuthKeyID: [8]byte{1}},
		domain.Authorization{AuthKeyID: [8]byte{2}, IP: "  "},
	)

	out, err := router.onAccountGetAuthorizations(WithUserID(context.Background(), 42))
	if err != nil {
		t.Fatalf("getAuthorizations: %v", err)
	}
	if len(out.Authorizations) != 2 {
		t.Fatalf("authorizations = %d, want 2", len(out.Authorizations))
	}
	if calls := resolver.calls; calls != 0 {
		t.Fatalf("resolver calls = %d, want 0 when no address is routable", calls)
	}
	for i, got := range out.Authorizations {
		if got.Country != authorizationLocationUnknown {
			t.Fatalf("authorization[%d] country = %q, want placeholder", i, got.Country)
		}
	}
}

// Resolution is a pure display enhancement: when the backend is slow the list must still be returned,
// just with the text falling back.
func TestAccountGetAuthorizationsFallsBackWhenLookupTimesOut(t *testing.T) {
	resolver := &stubGeoIPResolver{
		locations:         map[string]geoip.Location{"203.0.113.7": {Country: "Finland", Region: "Helsinki"}},
		waitBeforeResolve: authorizationLocationLookupTimeout + 5*time.Second,
	}
	router := newGeoIPAuthorizationsRouter(t, resolver, domain.Authorization{AuthKeyID: [8]byte{1}, IP: "203.0.113.7"})

	start := time.Now()
	out, err := router.onAccountGetAuthorizations(WithUserID(context.Background(), 42))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("getAuthorizations: %v", err)
	}
	if elapsed > authorizationLocationLookupTimeout+2*time.Second {
		t.Fatalf("getAuthorizations took %v, want the lookup bounded at %v", elapsed, authorizationLocationLookupTimeout)
	}
	if len(out.Authorizations) != 1 {
		t.Fatalf("authorizations = %d, want 1", len(out.Authorizations))
	}
	if got := out.Authorizations[0]; got.Country != authorizationLocationUnknown || got.Region != authorizationLocationUnknown {
		t.Fatalf("country/region = %q/%q, want placeholder after lookup timeout", got.Country, got.Region)
	}
}

// The single authorization returned by QR login goes through the same resolution: only this one address
// is looked up, and it should not fall back to the placeholder text outside the list.
func TestAcceptLoginTokenResolvesAuthorizationLocation(t *testing.T) {
	const scannerUserID = int64(1000000001)
	targetRawAuthKeyID := [8]byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	targetAuthKeyID := [8]byte{0x81, 0x71, 0x61, 0x51, 0x41, 0x31, 0x21, 0x11}
	scannerRawAuthKeyID := [8]byte{0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99}
	scannerAuthKeyID := [8]byte{0x99, 0x88, 0x77, 0x66, 0x55, 0x44, 0x33, 0x22}

	resolver := &stubGeoIPResolver{locations: map[string]geoip.Location{
		"203.0.113.7": {Country: "Japan", Region: "Tokyo"},
	}}
	users := mapUsersService{users: map[int64]domain.User{
		scannerUserID: {ID: scannerUserID, FirstName: "Alice", Phone: "15550001001"},
	}}
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Auth:     &captureAuthService{},
		Sessions: &captureScopedSessions{captureSessions: &captureSessions{}},
		Users:    users,
		GeoIP:    resolver,
	}, zaptest.NewLogger(t), clock.System)

	targetCtx := WithClientInfo(
		WithLayer(
			WithSessionID(
				WithAuthKeyID(
					WithRawAuthKeyID(WithClientIP(context.Background(), "203.0.113.7"), targetRawAuthKeyID),
					targetAuthKeyID,
				),
				101,
			),
			currentClientLayer,
		),
		ClientInfo{APIID: 2040, DeviceModel: "WebA", SystemVersion: "Chrome", AppVersion: "1.0", LangPack: "tdesktop"},
	)
	exported, err := router.onAuthExportLoginToken(targetCtx, &tg.AuthExportLoginTokenRequest{APIID: 2040, APIHash: "hash"})
	if err != nil {
		t.Fatalf("export login token: %v", err)
	}
	loginToken, ok := exported.(*tg.AuthLoginToken)
	if !ok {
		t.Fatalf("export type = %T, want *tg.AuthLoginToken", exported)
	}

	scannerCtx := WithUserID(
		WithSessionID(
			WithAuthKeyID(
				WithRawAuthKeyID(context.Background(), scannerRawAuthKeyID),
				scannerAuthKeyID,
			),
			202,
		),
		scannerUserID,
	)
	authorization, err := router.onAuthAcceptLoginToken(scannerCtx, loginToken.Token)
	if err != nil {
		t.Fatalf("accept login token: %v", err)
	}
	if authorization == nil {
		t.Fatal("accept login token returned nil authorization")
	}
	if authorization.Country != "Japan" || authorization.Region != "Tokyo" {
		t.Fatalf("country/region = %q/%q, want Japan/Tokyo", authorization.Country, authorization.Region)
	}
	if got := resolver.requestedIPs(); len(got) != 1 || got[0] != "203.0.113.7" {
		t.Fatalf("resolved IPs = %v, want exactly the accepted session address", got)
	}
}

func TestAuthorizationLocationTextFallsBackOnBlankValues(t *testing.T) {
	cases := map[string]string{
		"Finland":   "Finland",
		" Finland ": "Finland",
		"":          authorizationLocationUnknown,
		"   ":       authorizationLocationUnknown,
	}
	for input, want := range cases {
		if got := authorizationLocationText(input); got != want {
			t.Fatalf("authorizationLocationText(%q) = %q, want %q", input, got, want)
		}
	}
}
