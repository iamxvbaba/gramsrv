package rpc

import "context"

// withClientDriftMetadata 只在调用方已经用 constructor drift 证明客户端来源时
// 补最小 client 类型。它不是 unknown fallback；不能在普通裸 RPC 上调用。
// DrKLO Android 的 client-private constructor 只能证明 Android 兼容路径，
// 不能替代 invokeWithLayer/auth_keys/authorizations 里的真实 layer。
func (r *Router) withClientDriftMetadata(ctx context.Context, typ ClientType) context.Context {
	if typ == ClientTypeUnknown || ClientTypeFrom(ctx) != ClientTypeUnknown {
		return ctx
	}
	return WithClientInfo(ctx, ClientInfo{Type: typ})
}
