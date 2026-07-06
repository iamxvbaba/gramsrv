package rpc

import (
	"context"

	"github.com/gotd/td/tg"
)

// 群通话范围外入口的被动 stub（防点崩）。未列出的 phone.*（conference 族 /
// 通话内消息族 / scheduled / RTMP）走 router fallback：400/500 NOT_IMPLEMENTED +
// 兼容矩阵日志，客户端不断连。
func (r *Router) registerPhoneStubs(d *tg.ServerDispatcher) {
	// 入会身份候选：真实实现见 phone_group_call.go（self + admin 的频道身份）。
	d.OnPhoneGetGroupCallJoinAs(r.onPhoneGetGroupCallJoinAs)
	// default join-as 偏好持久化仍是 stub（chatFull.groupcall_default_join_as 不回填）。
	d.OnPhoneSaveDefaultGroupCallJoinAs(func(ctx context.Context, req *tg.PhoneSaveDefaultGroupCallJoinAsRequest) (bool, error) {
		return true, nil
	})
	// 录制范围外：客户端只看 record_start_date（恒不下发），打发掉即可。
	d.OnPhoneToggleGroupCallRecord(func(ctx context.Context, req *tg.PhoneToggleGroupCallRecordRequest) (tg.UpdatesClass, error) {
		return tgEmptyUpdates(int(r.clock.Now().Unix())), nil
	})
	// RTMP 直播（Live Stream）：真实 handler 见 phone_group_call_rtmp.go。
	d.OnPhoneGetGroupCallStreamChannels(r.onPhoneGetGroupCallStreamChannels)
	d.OnPhoneGetGroupCallStreamRtmpURL(r.onPhoneGetGroupCallStreamRtmpURL)
}
