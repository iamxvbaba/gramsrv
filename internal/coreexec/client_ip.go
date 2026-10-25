package coreexec

import (
	"context"
	"errors"
	"net/netip"

	"telesrv/internal/transport"
)

var errBadClientIP = errors.New("coreexec: bad client ip")

// clientIPToPB copies the Edge-observed peer IP, carried as neutral transport
// metadata, into a CoreExec request field. Core never sees the connection, so
// this is the only path by which the address reaches authorization metadata.
func clientIPToPB(ctx context.Context) string {
	ip, _ := transport.ClientIPFrom(ctx)
	return ip
}

// applyClientIPPB re-attaches the Edge-observed peer IP on the Core side. Edge
// only forwards canonical IP literals without port or zone; any other value is
// a protocol error rather than metadata to be stored as-is.
func applyClientIPPB(ctx context.Context, ip string) (context.Context, error) {
	if ip == "" {
		return ctx, nil
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" || addr.Is4In6() || addr.String() != ip {
		return ctx, errBadClientIP
	}
	return transport.WithClientIP(ctx, ip), nil
}
