package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// Scheduled video chat RPCs. Client flow (TDesktop calls_group_call.cpp):
// createGroupCall(schedule_date) -> State::Waiting countdown (no join) -> an admin calls
// startScheduledGroupCall to clear schedule_date -> updateGroupCall (no schedule_date) -> the client
// setScheduledDate(0) triggers initialJoin to join for real. The start reminder is a per-viewer
// schedule_start_subscribed flag (toggleGroupCallStartSubscription).

// applyScheduleSubscription backfills the ScheduleStartSubscribed projection field for a single
// viewer.
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
		// Idempotent: already started, return only the snapshot without repeating the fan-out/service
		// message.
		return r.groupCallUpdateContainer(ctx, scope.userID, channel,
			groupCallUpdateFor(channel, call, scope.userID, true, r.cfg.PublicBaseURL), nil), nil
	}
	now := int(r.clock.Now().Unix())
	// started service message (isomorphic to the started one for an immediate create).
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
	// Fan-out: updateGroupCall (schedule_date cleared, so the client joins automatically) + service
	// message. Subscribers go through the same online fan-out as ordinary online members; a push
	// notification for offline subscribers is notification-system scope and is not implemented yet
	// (noted as a matrix todo).
	r.pushGroupCallUpdate(ctx, channel, call)
	if serviceRes.Event.Pts != 0 {
		r.pushGroupCallServiceMessage(ctx, scope.userID, serviceRes)
	}
	out := r.groupCallUpdateContainer(ctx, scope.userID, channel,
		groupCallUpdateFor(channel, call, scope.userID, true, r.cfg.PublicBaseURL), nil)
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
		// Only a not-yet-started scheduled call has a start reminder to subscribe to.
		return nil, groupCallInvalidErr()
	}
	if err := r.deps.GroupCalls.SetScheduleSubscription(ctx, scope.call.ID, scope.userID, req.Subscribed); err != nil {
		return nil, groupCallErr(err)
	}
	call := scope.call
	call.ScheduleStartSubscribed = req.Subscribed
	// The subscription is per-viewer private state: respond to this device and push to sync the
	// user's other online devices.
	update := groupCallUpdateFor(scope.channel, call, scope.userID, scope.canManage(), r.cfg.PublicBaseURL)
	r.pushUserMessage(ctx, scope.userID, "schedule subscription update",
		r.groupCallUpdateContainer(ctx, scope.userID, scope.channel, update, nil))
	return r.groupCallUpdateContainer(ctx, scope.userID, scope.channel, update, nil), nil
}
