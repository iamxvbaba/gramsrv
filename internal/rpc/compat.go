package rpc

import "context"

// withAndroidCompatMetadata 为「客户端构造器漂移」请求仅兜底 client 类型。
// DrKLO/OwpenGram Android 可能在不同版本使用不同 TL layer，client-private 构造器
// 只能证明这是 Android 兼容路径，不能替代 invokeWithLayer 里的真实 layer。
func (r *Router) withAndroidCompatMetadata(ctx context.Context) context.Context {
	if ClientTypeFrom(ctx) == ClientTypeUnknown {
		ctx = WithClientInfo(ctx, ClientInfo{LangPack: string(ClientTypeAndroid), Type: ClientTypeAndroid})
	}
	return ctx
}
