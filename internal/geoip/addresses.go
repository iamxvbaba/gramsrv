package geoip

import (
	"net/netip"
	"strings"
)

// Non-global special-use ranges from the IANA registries. IsPrivate only covers
// RFC 1918 and IPv6 ULA; it does not reject CGNAT, documentation or benchmarking.
// https://www.iana.org/assignments/iana-ipv4-special-registry/
// https://www.iana.org/assignments/iana-ipv6-special-registry/
var nonPublicPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 reachability is conditional, not global.
	netip.MustParsePrefix("3fff::/20"),
}

// These more-specific IANA allocations are globally reachable, unlike the
// enclosing protocol-assignment prefixes above. Check them before the deny list.
var publicSpecialPrefixes = [...]netip.Prefix{
	netip.MustParsePrefix("192.0.0.9/32"),
	netip.MustParsePrefix("192.0.0.10/32"),
	netip.MustParsePrefix("2001:1::1/128"),
	netip.MustParsePrefix("2001:1::2/128"),
	netip.MustParsePrefix("2001:1::3/128"),
	netip.MustParsePrefix("2001:3::/32"),
	netip.MustParsePrefix("2001:4:112::/48"),
	netip.MustParsePrefix("2001:20::/28"),
	netip.MustParsePrefix("2001:30::/28"),
}

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

// publicAddr normalizes RemoteAddr's host before enforcing the outbound privacy
// boundary. Mapped IPv4 is checked as IPv4; zones are removed only after parsing.
// For IPv6, only native global unicast is geolocated: translation prefixes and
// unallocated space are not ordinary client locations and stay local.
func publicAddr(raw string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return netip.Addr{}, false
	}
	addr = addr.Unmap().WithZone("")
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return netip.Addr{}, false
	}
	if addr.Is6() && !ipv6GlobalUnicast.Contains(addr) {
		return netip.Addr{}, false
	}
	for _, prefix := range publicSpecialPrefixes {
		if prefix.Contains(addr) {
			return addr, true
		}
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return netip.Addr{}, false
		}
	}
	return addr, true
}
