// Package transport holds neutral transport metadata shared across the
// protocol/edge boundary and the RPC business layer. It must not import any
// business package (internal/rpc, internal/app, ...), so that mtprotoedge can
// produce transport facts without depending on the RPC implementation.
package transport

import "context"

type ctxKey int

const (
	clientIPKey ctxKey = iota
)

// WithClientIP records the client remote IP as neutral transport metadata.
// An empty ip is ignored.
func WithClientIP(ctx context.Context, ip string) context.Context {
	if ip == "" {
		return ctx
	}
	return context.WithValue(ctx, clientIPKey, ip)
}

// ClientIPFrom returns the client remote IP carried in ctx, if set.
func ClientIPFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(clientIPKey).(string)
	return v, ok && v != ""
}
