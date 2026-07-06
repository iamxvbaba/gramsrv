package rpc

import (
	"context"

	"github.com/gotd/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Scheduled video chat（定时通话）RPC。客户端流程（TDesktop calls_group_call.cpp）：
// createGroupCall(schedule_date) → State::Waiting 倒计时（不 join）→ 管理员
// startScheduledGroupCall 清 schedule_date → updateGroupCall（无 schedule_date）→
// 客户端 setScheduledDate(0) 触发 initialJoin 正式入会。开播提醒是 per-viewer 的
// schedule_start_subscribed flag（toggleGroupCallStartSubscription）。

// applyScheduleSubscription 为单个 viewer 回填 ScheduleStartSubscribed 投影字段。
func (r *Router) applyScheduleSubscription(ctx context.Context, call domain.GroupCall, viewerUserID int64) domain.GroupCall {
	if call.ScheduleDate == 0 || viewerUserID == 0 {
		return call
	}
	subs, err := r.deps.GroupCalls.ScheduleSubscriberIDs(ctx, call.ID)
	if err != nil {
		return call
	}
	for _, id := range subs {
		if id == viewerUserID {
			call.ScheduleStartSubscribed = true
			break
		}
	}
	return call
}

func (r *Router) onPhoneStartScheduledGroupCall(ctx context.Context, in tg.InputGroupCallClass) (tg.UpdatesClass, error) {
	scope, err := r.groupCallScopeFrom(ctx, in)
	if err != nil {
		return nil, err
	}
	if scope.call.Conference() || scope.channel.ID == 0 {
		return nil, groupCallInvalidErr()
	}
	if !scope.canManage() {
		return nil, tgerr400("CHAT_ADMIN_REQUIRED")
	}
	call, changed, err := r.deps.GroupCalls.StartScheduled(ctx, scope.call.ID)
	if err != nil {
		return nil, groupCallErr(err)
	}
	channel := scope.channel
	if !changed {
		// 幂等：已开始，只回快照，不重复扇出/服务消息。
		return r.groupCallUpdateContainer(ctx, scope.userID, channel,
			groupCallUpdateFor(channel, call, scope.userID, true), nil), nil
	}
	now := int(r.clock.Now().Unix())
	// started 服务消息（与即时创建的 started 同构）。
	var serviceRes domain.SendChannelMessageResult
	if res, err := r.deps.Channels.AppendCallServiceMessage(ctx, channel.ID, scope.userID, now, domain.ChannelMessageAction{
		Type:           domain.ChannelActionGroupCall,
		CallID:         call.ID,
		CallAccessHash: call.AccessHash,
	}); err == nil {
		serviceRes = res
		_ = r.deps.GroupCalls.SetStartedMessageID(ctx, call.ID, res.Message.ID)
	} else {
		r.log.Warn("scheduled group call started service message", zap.Int64("channel_id", channel.ID), zap.Error(err))
	}
	// 扇出：updateGroupCall（schedule_date 已清，客户端据此自动入会）+ 服务消息。
	// 订阅者与普通在线成员走同一在线扇出；离线订阅者的推送提醒（push notification）
	// 属通知系统范围，当前不实现（记矩阵 todo）。
	r.pushGroupCallUpdate(ctx, channel, call)
	if serviceRes.Event.Pts != 0 {
		r.pushGroupCallServiceMessage(ctx, scope.userID, serviceRes)
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		groupCallUpdateFor(channel, call, scope.userID, true), nil)
	if serviceRes.Event.Pts != 0 {
		if msgUpdate := tgChannelUpdate(scope.userID, serviceRes.Event); msgUpdate != nil {
			out.Updates = append(out.Updates, msgUpdate)
		}
	}
	return out, nil
}

func (r *Router) onPhoneToggleGroupCallStartSubscription(ctx context.Context, req *tg.PhoneToggleGroupCallStartSubscriptionRequest) (tg.UpdatesClass, error) {
	if req == nil {
		return nil, inputRequestInvalidErr()
	}
	scope, err := r.groupCallScopeFrom(ctx, req.Call)
	if err != nil {
		return nil, err
	}
	if !scope.call.Active() {
		return nil, groupCallAlreadyDiscardedErr()
	}
	if scope.call.ScheduleDate == 0 {
		// 只有未开始的定时通话才有开播提醒可订。
		return nil, groupCallInvalidErr()
	}
	if err := r.deps.GroupCalls.SetScheduleSubscription(ctx, scope.call.ID, scope.userID, req.Subscribed); err != nil {
		return nil, groupCallErr(err)
	}
	call := scope.call
	call.ScheduleStartSubscribed = req.Subscribed
	// 订阅是 per-viewer 私有状态：响应给本设备，推送同步本人其它在线设备即可。
	update := groupCallUpdateFor(scope.channel, call, scope.userID, scope.canManage())
	r.pushUserMessage(ctx, scope.userID, "schedule subscription update",
		r.groupCallUpdateContainer(ctx, scope.userID, scope.channel, update, nil))
	return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel, update, nil), nil
}
