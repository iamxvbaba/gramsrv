package rpc

import (
	"testing"

	"github.com/gotd/td/tg"
)

// TestScheduledGroupCallLifecycle 覆盖定时通话闭环：create(schedule_date) →
// scheduled 服务消息 + groupCall.schedule_date → 订阅开播提醒（per-viewer flag）→
// startScheduledGroupCall 清 schedule_date + started 服务消息 → join 正常入会。
func TestScheduledGroupCallLifecycle(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)
	scheduleDate := int(f.clk.Now().Unix()) + 3600

	// --- 过去时间拒绝 ---
	pastReq := &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 1,
	}
	pastReq.SetScheduleDate(int(f.clk.Now().Unix()) - 10)
	if _, err := f.router.onPhoneCreateGroupCall(ownerCtx, pastReq); err == nil {
		t.Fatalf("past schedule_date accepted")
	} else {
		assertPhoneRPCErr(t, err, "SCHEDULE_DATE_INVALID")
	}

	// --- create scheduled ---
	createReq := &tg.PhoneCreateGroupCallRequest{
		Peer:     &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		RandomID: 2,
	}
	createReq.SetScheduleDate(scheduleDate)
	createRes, err := f.router.onPhoneCreateGroupCall(ownerCtx, createReq)
	if err != nil {
		t.Fatalf("create scheduled group call: %v", err)
	}
	call := findUpdate[*tg.UpdateGroupCall](t, createRes).Call.(*tg.GroupCall)
	if got, ok := call.GetScheduleDate(); !ok || got != scheduleDate {
		t.Fatalf("groupCall.schedule_date = %d ok=%v, want %d", got, ok, scheduleDate)
	}
	// scheduled 服务消息。
	msgUpdate := findUpdate[*tg.UpdateNewChannelMessage](t, createRes)
	svc, ok := msgUpdate.Message.(*tg.MessageService)
	if !ok {
		t.Fatalf("create response message = %T, want MessageService", msgUpdate.Message)
	}
	scheduledAction, ok := svc.Action.(*tg.MessageActionGroupCallScheduled)
	if !ok {
		t.Fatalf("service action = %T, want MessageActionGroupCallScheduled", svc.Action)
	}
	if scheduledAction.ScheduleDate != scheduleDate {
		t.Fatalf("service action schedule_date = %d, want %d", scheduledAction.ScheduleDate, scheduleDate)
	}
	input := &tg.InputGroupCall{ID: call.ID, AccessHash: call.AccessHash}

	// --- member 订阅开播提醒 ---
	subRes, err := f.router.onPhoneToggleGroupCallStartSubscription(memberCtx, &tg.PhoneToggleGroupCallStartSubscriptionRequest{
		Call: input, Subscribed: true,
	})
	if err != nil {
		t.Fatalf("toggle start subscription: %v", err)
	}
	subCall := findUpdate[*tg.UpdateGroupCall](t, subRes).Call.(*tg.GroupCall)
	if !subCall.ScheduleStartSubscribed {
		t.Fatalf("subscription response missing schedule_start_subscribed")
	}
	// getGroupCall 回填 per-viewer flag：member 已订阅、owner 未订阅。
	memberView, err := f.router.onPhoneGetGroupCall(memberCtx, &tg.PhoneGetGroupCallRequest{Call: input, Limit: 10})
	if err != nil {
		t.Fatalf("member getGroupCall: %v", err)
	}
	if !memberView.Call.(*tg.GroupCall).ScheduleStartSubscribed {
		t.Fatalf("member getGroupCall missing subscribed flag")
	}
	ownerView, err := f.router.onPhoneGetGroupCall(ownerCtx, &tg.PhoneGetGroupCallRequest{Call: input, Limit: 10})
	if err != nil {
		t.Fatalf("owner getGroupCall: %v", err)
	}
	if ownerView.Call.(*tg.GroupCall).ScheduleStartSubscribed {
		t.Fatalf("owner getGroupCall unexpectedly subscribed")
	}

	// --- 非管理员不能开播 ---
	if _, err := f.router.onPhoneStartScheduledGroupCall(memberCtx, input); err == nil {
		t.Fatalf("non-admin started scheduled call")
	} else {
		assertPhoneRPCErr(t, err, "CHAT_ADMIN_REQUIRED")
	}

	// --- start ---
	f.sessions.reset()
	startRes, err := f.router.onPhoneStartScheduledGroupCall(ownerCtx, input)
	if err != nil {
		t.Fatalf("startScheduledGroupCall: %v", err)
	}
	started := findUpdate[*tg.UpdateGroupCall](t, startRes).Call.(*tg.GroupCall)
	if _, ok := started.GetScheduleDate(); ok {
		t.Fatalf("started call still has schedule_date")
	}
	// started 服务消息（messageActionGroupCall 无 duration）。
	startMsg := findUpdate[*tg.UpdateNewChannelMessage](t, startRes)
	startSvc := startMsg.Message.(*tg.MessageService)
	if _, ok := startSvc.Action.(*tg.MessageActionGroupCall); !ok {
		t.Fatalf("start service action = %T, want MessageActionGroupCall", startSvc.Action)
	}
	// 在线成员收到 schedule_date 已清的 updateGroupCall（客户端据此自动入会）。
	memberGotStart := false
	for _, rec := range f.sessions.records() {
		if rec.userID != f.member.ID {
			continue
		}
		if box, ok := rec.msg.(*tg.Updates); ok {
			for _, u := range box.Updates {
				if gc, ok := u.(*tg.UpdateGroupCall); ok {
					if call, ok := gc.Call.(*tg.GroupCall); ok {
						if _, has := call.GetScheduleDate(); !has {
							memberGotStart = true
						}
					}
				}
			}
		}
	}
	if !memberGotStart {
		t.Fatalf("member did not receive started updateGroupCall: %+v", f.sessions.records())
	}

	// --- 幂等重复 start ---
	if _, err := f.router.onPhoneStartScheduledGroupCall(ownerCtx, input); err != nil {
		t.Fatalf("idempotent re-start: %v", err)
	}

	// --- 已开始后订阅提醒非法 ---
	if _, err := f.router.onPhoneToggleGroupCallStartSubscription(memberCtx, &tg.PhoneToggleGroupCallStartSubscriptionRequest{
		Call: input, Subscribed: true,
	}); err == nil {
		t.Fatalf("subscription toggle allowed after start")
	}

	// --- start 后正常 join ---
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 7001),
	}); err != nil {
		t.Fatalf("join after start: %v", err)
	}
}
