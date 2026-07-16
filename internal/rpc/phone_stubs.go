package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// 群通话范围外入口的被动 stub（防点崩）。未列出的 phone.*（conference 族 /
// 通话内消息族 / scheduled / RTMP）走 router fallback：400/500 NOT_IMPLEMENTED +
// 兼容矩阵日志，客户端不断连。
func (r *Router) registerPhoneStubs(d *tlprofile.Dispatcher) {
	registerRPC[
	// 入会身份候选：真实实现见 phone_group_call.go（self + admin 的频道身份）。
	*tg.PhoneGetGroupCallJoinAsRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallJoinAs, func(ctx context.
		// default join-as 偏好持久化仍是 stub（chatFull.groupcall_default_join_as 不回填）。
		Context, layerRequest *tg.PhoneGetGroupCallJoinAsRequest) (any, error) {
		return r.onPhoneGetGroupCallJoinAs(ctx, layerRequest.
			Peer)
	})
	registerRPC[*tg.PhoneSaveDefaultGroupCallJoinAsRequest](d, tlprofile.SemanticMethodPhoneSaveDefaultGroupCallJoinAs, func(ctx context.Context, req *tg.PhoneSaveDefaultGroupCallJoinAsRequest) (any, error) {
		return true, nil
	})
	registerRPC[

	// 录制范围外：客户端只看 record_start_date（恒不下发），打发掉即可。
	*tg.PhoneToggleGroupCallRecordRequest](d, tlprofile.SemanticMethodPhoneToggleGroupCallRecord, func(ctx context.Context, req *tg.PhoneToggleGroupCallRecordRequest) (any, error) {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	})
	registerRPC[

	// RTMP 直播（Live Stream）：真实 handler 见 phone_group_call_rtmp.go。
	*tg.PhoneGetGroupCallStreamChannelsRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallStreamChannels, func(ctx context.Context, layerRequest *tg.PhoneGetGroupCallStreamChannelsRequest) (any, error) {
		return r.onPhoneGetGroupCallStreamChannels(ctx, layerRequest.
			Call)
	})
	registerRPC[*tg.PhoneGetGroupCallStreamRtmpURLRequest](d, tlprofile.SemanticMethodPhoneGetGroupCallStreamRtmpURL, func(ctx context.Context, layerRequest *tg.PhoneGetGroupCallStreamRtmpURLRequest) (any, error) {
		return r.onPhoneGetGroupCallStreamRtmpURL(ctx, layerRequest)
	})
}
