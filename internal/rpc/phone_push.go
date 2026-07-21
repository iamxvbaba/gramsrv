package rpc

import (
	"context"

	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 通话信令推送全部走无 pts 直推（pushUserMessage）：信令有效期秒级，离线设备
// 重连后经 getDifference 补收一条早已失效的来电毫无意义甚至有害。唯一带 pts 的
// 产物是结束后的 messageActionPhoneCall 服务消息（P2，走 outbox 补偿离线设备）。

// phoneCallUpdates 把 viewer 视角的 phoneCall 状态包成可直推的 Updates 容器。
func (r *Router) phoneCallUpdates(ctx context.Context, call domain.PhoneCall, viewerID int64) *tg.Updates {
	return r.phoneCallUpdatesWith(ctx, tgPhoneCallForViewer(call, viewerID), call, viewerID)
}

func (r *Router) phoneCallUpdatesWith(ctx context.Context, view tg.PhoneCallClass, call domain.PhoneCall, viewerID int64) *tg.Updates {
	return &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePhoneCall{PhoneCall: view}},
		Users:   r.tgUsersForIDs(ctx, viewerID, []int64{call.AdminID, call.ParticipantID}),
		Chats:   []tg.ChatClass{},
		Date:    int(r.clock.Now().Unix()),
		Seq:     0,
	}
}

// pushPhoneCall 把 call 当前状态按 targetUserID 视角推给其全部在线设备
// （ctx 携带的发起设备会被 pushUserMessage 的 except 语义排除）。
func (r *Router) pushPhoneCall(ctx context.Context, targetUserID int64, call domain.PhoneCall, logMessage string) int {
	return r.pushUserMessage(ctx, targetUserID, logMessage, r.phoneCallUpdates(ctx, call, targetUserID))
}

// pushPhoneCallToDevice 只把 phoneCall 状态推给一台精确的物理 session。
//
// 这是 fail-closed 路径：目标锚点缺失、session 已断开或编码/发送失败时都不得
// 回退为 user 级广播。呼出通话的 ringing 更新只属于 requestCall 来源设备；
// 扩大投递范围会在 DrKLO 多账号同机时污染另一账号的全局 pending 来电对象。
func (r *Router) pushPhoneCallToDevice(ctx context.Context, targetUserID int64, device domain.SessionRef, call domain.PhoneCall, logMessage string) {
	if targetUserID == 0 || device.RawAuthKeyID == ([8]byte{}) || device.SessionID == 0 || r.deps.Sessions == nil {
		if r.log != nil {
			r.log.Debug(logMessage,
				zap.Int64("target_user_id", targetUserID),
				zap.Int64("call_id", call.ID),
				zap.Int64("target_session_id", device.SessionID),
				zap.String("delivery", "skipped_invalid_device_anchor"),
			)
		}
		return
	}

	updates := r.phoneCallUpdates(ctx, call, targetUserID)
	if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, device.RawAuthKeyID, device.SessionID, proto.MessageFromServer, updates); err != nil && r.log != nil {
		r.log.Debug(logMessage,
			zap.Int64("target_user_id", targetUserID),
			zap.Int64("call_id", call.ID),
			zap.Int64("target_session_id", device.SessionID),
			zap.Error(err),
		)
	}
}

// pushPhoneCallStopRinging 向被叫其它设备推合成 phoneCallDiscarded 停振铃（P0-1 修正）。
// ctx 必须是接听设备的请求上下文：except 语义恰好把赢家排除在外。
func (r *Router) pushPhoneCallStopRinging(ctx context.Context, call domain.PhoneCall) int {
	upd := r.phoneCallUpdatesWith(ctx, tgPhoneCallStopRinging(call), call, call.ParticipantID)
	return r.pushUserMessage(ctx, call.ParticipantID, "phone call stop ringing", upd)
}

// pushPhoneCallDiscardedBoth 把终态推给双方全部设备（发起设备由 ctx except 排除，
// 其结果从 RPC 响应获得）。
func (r *Router) pushPhoneCallDiscardedBoth(ctx context.Context, call domain.PhoneCall) {
	r.pushPhoneCall(ctx, call.AdminID, call, "phone call discarded")
	r.pushPhoneCall(ctx, call.ParticipantID, call, "phone call discarded")
}

// pushPhoneSignalingData 把信令字节透传给对端。优先走设备锚点定向推送
// （requestCall/acceptCall 受理设备），锚点失效（设备重连换 session）则回退
// user 级扇出——非参与设备按 phone_call_id 不匹配静默丢弃（TDesktop
// handleSignalingData 行为），扇出无害。
func (r *Router) pushPhoneSignalingData(ctx context.Context, targetUserID int64, device domain.SessionRef, callID int64, data []byte) {
	upd := &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdatePhoneCallSignalingData{
			PhoneCallID: callID,
			Data:        data,
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
		Seq:   0,
	}
	if !device.Zero() && r.deps.Sessions != nil {
		if err := r.deps.Sessions.PushToSessionForAuthKey(ctx, device.RawAuthKeyID, device.SessionID, proto.MessageFromServer, upd); err == nil {
			return
		}
	}
	r.pushUserMessage(ctx, targetUserID, "phone call signaling", upd)
}
