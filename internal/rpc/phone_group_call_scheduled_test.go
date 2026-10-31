package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
)

// TestScheduledGroupCallLifecycle covers the scheduled-call loop: create(schedule_date) ->
// scheduled service message + groupCall.schedule_date -> subscribe to the start reminder
// (per-viewer flag) -> startScheduledGroupCall clears schedule_date + started service message ->
// join normally.
func TestScheduledGroupCallLifecycle(t *testing.T) {
	f := newGroupCallFixture(t)
	ownerCtx := f.userCtx(f.owner, 11)
	memberCtx := f.userCtx(f.member, 22)
	scheduleDate := int(f.clk.Now().Unix()) + 3600

	// --- past time rejected ---
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
	// scheduled service message.
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

	// --- member subscribes to the start reminder ---
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
	// getGroupCall backfills the per-viewer flag: member subscribed, owner not.
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

	// --- a non-admin cannot start ---
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
	// started service message (messageActionGroupCall has no duration).
	startMsg := findUpdate[*tg.UpdateNewChannelMessage](t, startRes)
	startSvc := startMsg.Message.(*tg.MessageService)
	if _, ok := startSvc.Action.(*tg.MessageActionGroupCall); !ok {
		t.Fatalf("start service action = %T, want MessageActionGroupCall", startSvc.Action)
	}
	// Online members receive an updateGroupCall with schedule_date cleared (so the client joins
	// automatically).
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

	// --- idempotent duplicate start ---
	if _, err := f.router.onPhoneStartScheduledGroupCall(ownerCtx, input); err != nil {
		t.Fatalf("idempotent re-start: %v", err)
	}

	// --- the start reminder is invalid after start ---
	if _, err := f.router.onPhoneToggleGroupCallStartSubscription(memberCtx, &tg.PhoneToggleGroupCallStartSubscriptionRequest{
		Call: input, Subscribed: true,
	}); err == nil {
		t.Fatalf("subscription toggle allowed after start")
	}

	// --- normal join after start ---
	if _, err := f.router.onPhoneJoinGroupCall(memberCtx, &tg.PhoneJoinGroupCallRequest{
		Call:   input,
		JoinAs: &tg.InputPeerSelf{},
		Params: groupCallJoinParams(t, 7001),
	}); err != nil {
		t.Fatalf("join after start: %v", err)
	}
}
