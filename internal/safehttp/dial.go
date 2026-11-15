// Package safehttp provides outbound connections for user-controlled HTTP URLs.
package safehttp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

var blockedPrefixes = [...]netip.Prefix{
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
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}

var ipv6GlobalUnicast = netip.MustParsePrefix("2000::/3")

// PublicIP rejects private, local, translation and reserved destinations.
func PublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLinkLocalUnicast() ||
		(addr.Is6() && !ipv6GlobalUnicast.Contains(addr)) {
		return false
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

// DialContext resolves once and connects only to the validated numeric address.
// Transports using this dialer must disable proxies to preserve this boundary.
func DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return dialContext(ctx, network, address, net.DefaultResolver.LookupIPAddr, dialer.DialContext)
}

func dialContext(ctx context.Context, network, address string,
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := lookup(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		return nil, errors.New("outbound host has no addresses")
	}
	// Reject mixed public/private answers before opening any connection.
	for _, address := range addresses {
		if address.Zone != "" || !PublicIP(address.IP) {
			return nil, errors.New("outbound host resolves to a non-public address")
		}
	}
	for _, address := range addresses {
		var conn net.Conn
		conn, err = dial(ctx, network, net.JoinHostPort(address.IP.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}
