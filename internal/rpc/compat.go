package rpc

import "context"

// withClientDriftMetadata only fills in the minimal client type when the caller
// has already proven the client origin via constructor drift. It is not an
// unknown fallback; it must not be called on a plain bare RPC. DrKLO Android's
// client-private constructor only proves the Android compatibility path and
// cannot stand in for invokeWithLayer or the real Layer evidence already frozen
// for the same logical session.
func (r *Router) withClientDriftMetadata(ctx context.Context, typ ClientType) context.Context {
	if typ == ClientTypeUnknown || ClientTypeFrom(ctx) != ClientTypeUnknown {
		return ctx
	}
	return WithClientInfo(ctx, ClientInfo{Type: typ})
}
