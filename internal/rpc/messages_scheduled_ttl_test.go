package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestScheduledMessagesLifecycleUsesScheduledStore(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700001000)
	)
	alice := domain.User{ID: aliceID, AccessHash: 11, FirstName: "Alice"}
	bob := domain.User{ID: bobID, AccessHash: 22, FirstName: "Bob"}
	messages := &scheduledCaptureMessages{captureMessages: &captureMessages{}}
	r := New(Config{}, Deps{
		Messages: messages,
		Users:    mapUsersService{users: map[int64]domain.User{aliceID: alice, bobID: bob}},
	}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithUserID(context.Background(), aliceID)

	sendReq := &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
		Message:  "later",
		RandomID: 123456,
	}
	sendReq.SetScheduleDate(int(now + 3600))
	updates, err := r.onMessagesSendMessage(ctx, sendReq)
	if err != nil {
		t.Fatalf("send scheduled message: %v", err)
	}
	if messages.scheduleReq.OwnerUserID != aliceID || messages.scheduleReq.Peer.ID != bobID || messages.scheduleReq.ScheduleDate != int(now+3600) {
		t.Fatalf("schedule req = %+v, want alice->bob at requested date", messages.scheduleReq)
	}
	gotUpdates := updates.(*tg.Updates).Updates
	if len(gotUpdates) != 2 {
		t.Fatalf("scheduled send updates = %+v, want id + new scheduled", gotUpdates)
	}
	newScheduled, ok := gotUpdates[1].(*tg.UpdateNewScheduledMessage)
	if !ok {
		t.Fatalf("scheduled update = %T, want UpdateNewScheduledMessage", gotUpdates[1])
	}
	scheduledMsg, ok := newScheduled.Message.(*tg.Message)
	if !ok || !scheduledMsg.FromScheduled || scheduledMsg.Message != "later" || scheduledMsg.Date != int(now+3600) {
		t.Fatalf("scheduled message = %#v, want from_scheduled later at schedule date", newScheduled.Message)
	}

	history, err := r.onMessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
	})
	if err != nil {
		t.Fatalf("get scheduled history: %v", err)
	}
	if list := history.(*tg.MessagesMessages).Messages; len(list) != 1 {
		t.Fatalf("scheduled history = %+v, want one message", list)
	}

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
		ID:   scheduledMsg.ID,
	}
	editReq.SetMessage("edited")
	editReq.SetScheduleDate(int(now + 7200))
	edited, err := r.onMessagesEditMessage(ctx, editReq)
	if err != nil {
		t.Fatalf("edit scheduled message: %v", err)
	}
	if messages.editScheduledReq.ID != scheduledMsg.ID || !messages.editScheduledReq.SetMessage || messages.editScheduledReq.Message != "edited" || messages.editScheduledReq.ScheduleDate != int(now+7200) {
		t.Fatalf("edit scheduled req = %+v, want updated scheduled text/date", messages.editScheduledReq)
	}
	editedMessage := edited.(*tg.Updates).Updates[0].(*tg.UpdateNewScheduledMessage).Message.(*tg.Message)
	if !editedMessage.FromScheduled || editedMessage.Message != "edited" || editedMessage.Date != int(now+7200) {
		t.Fatalf("edited scheduled message = %#v, want edited future scheduled item", editedMessage)
	}

	sent, err := r.onMessagesSendScheduledMessages(ctx, &tg.MessagesSendScheduledMessagesRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: bob.AccessHash},
		ID:   []int{scheduledMsg.ID},
	})
	if err != nil {
		t.Fatalf("send scheduled now: %v", err)
	}
	if messages.sendReq.Message != "edited" || messages.markedScheduledID != scheduledMsg.ID || messages.markedSentID == 0 {
		t.Fatalf("sent scheduled state send=%+v marked scheduled=%d sent=%d", messages.sendReq, messages.markedScheduledID, messages.markedSentID)
	}
	var deleteUpdate *tg.UpdateDeleteScheduledMessages
	for _, update := range sent.(*tg.Updates).Updates {
		if v, ok := update.(*tg.UpdateDeleteScheduledMessages); ok {
			deleteUpdate = v
		}
	}
	if deleteUpdate == nil || len(deleteUpdate.Messages) != 1 || deleteUpdate.Messages[0] != scheduledMsg.ID {
		t.Fatalf("send scheduled delete update = %#v, want deleted scheduled id", deleteUpdate)
	}
	if sentIDs, ok := deleteUpdate.GetSentMessages(); !ok || len(sentIDs) != 1 || sentIDs[0] != messages.markedSentID {
		t.Fatalf("delete scheduled sent ids = %+v ok %v, want sent message id %d", sentIDs, ok, messages.markedSentID)
	}
}

func TestScheduledMessageEditDateOnlyPreservesContent(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700003000)
	)
	media := &domain.MessageMedia{
		Kind:     domain.MessageMediaKindDocument,
		Document: &domain.Document{ID: 910000000000000001, AccessHash: 91, DCID: 2},
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: bobID}
	messages := &scheduledCaptureMessages{
		captureMessages: &captureMessages{},
		scheduled: []domain.ScheduledMessage{{
			OwnerUserID:  aliceID,
			ID:           77,
			Peer:         peer,
			Message:      "",
			Media:        media,
			ScheduleDate: int(now + 3600),
			State:        "pending",
		}},
	}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithUserID(context.Background(), aliceID)

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: 22},
		ID:   77,
	}
	editReq.SetScheduleDate(int(now + 7200))
	updates, err := r.onMessagesEditMessage(ctx, editReq)
	if err != nil {
		t.Fatalf("date-only scheduled edit: %v", err)
	}
	if messages.editScheduledReq.SetMessage {
		t.Fatalf("edit scheduled req = %+v, want date-only edit without content overwrite", messages.editScheduledReq)
	}
	if messages.scheduled[0].Message != "" || messages.scheduled[0].Media != media || messages.scheduled[0].ScheduleDate != int(now+7200) {
		t.Fatalf("scheduled item after date-only edit = %+v, want original content and new date", messages.scheduled[0])
	}
	editedMessage := updates.(*tg.Updates).Updates[0].(*tg.UpdateNewScheduledMessage).Message.(*tg.Message)
	if !editedMessage.FromScheduled || editedMessage.Date != int(now+7200) {
		t.Fatalf("date-only edit update = %#v, want scheduled message at new date", editedMessage)
	}
}

func TestScheduledMessageEditAllowsEmptyCaptionForMedia(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700004000)
	)
	media := &domain.MessageMedia{
		Kind:     domain.MessageMediaKindDocument,
		Document: &domain.Document{ID: 910000000000000002, AccessHash: 92, DCID: 2},
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: bobID}
	messages := &scheduledCaptureMessages{
		captureMessages: &captureMessages{},
		scheduled: []domain.ScheduledMessage{{
			OwnerUserID:  aliceID,
			ID:           78,
			Peer:         peer,
			Message:      "caption",
			Media:        media,
			ScheduleDate: int(now + 3600),
			State:        "pending",
		}},
	}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithUserID(context.Background(), aliceID)

	editReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: bobID, AccessHash: 22},
		ID:   78,
	}
	editReq.SetMessage("")
	editReq.SetScheduleDate(int(now + 7200))
	if _, err := r.onMessagesEditMessage(ctx, editReq); err != nil {
		t.Fatalf("empty-caption scheduled edit: %v", err)
	}
	if !messages.editScheduledReq.SetMessage || messages.editScheduledReq.Message != "" {
		t.Fatalf("edit scheduled req = %+v, want explicit empty caption update", messages.editScheduledReq)
	}
	if messages.scheduled[0].Message != "" || messages.scheduled[0].Media != media || messages.scheduled[0].ScheduleDate != int(now+7200) {
		t.Fatalf("scheduled item after empty-caption edit = %+v, want media kept and caption cleared", messages.scheduled[0])
	}
}

func TestMessagesSetHistoryTTLPushesPrivatePeerBothSides(t *testing.T) {
	const (
		aliceID = int64(1000000001)
		bobID   = int64(1000000002)
		now     = int64(1700002000)
		period  = 86400
	)
	messages := &ttlCaptureMessages{captureMessages: &captureMessages{}}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Sessions: sessions}, zaptest.NewLogger(t), fixedClock{now: time.Unix(now, 0)})
	ctx := WithSessionID(WithUserID(context.Background(), aliceID), 77)

	out, err := r.onMessagesSetHistoryTTL(ctx, &tg.MessagesSetHistoryTTLRequest{
		Peer:   &tg.InputPeerUser{UserID: bobID, AccessHash: 22},
		Period: period,
	})
	if err != nil {
		t.Fatalf("set private history ttl: %v", err)
	}
	if messages.setTTLUserID != aliceID || messages.setTTLPeer != (domain.Peer{Type: domain.PeerTypeUser, ID: bobID}) || messages.setTTLPeriod != period {
		t.Fatalf("set ttl context user=%d peer=%+v period=%d", messages.setTTLUserID, messages.setTTLPeer, messages.setTTLPeriod)
	}
	selfUpdate := out.(*tg.Updates).Updates[0].(*tg.UpdatePeerHistoryTTL)
	if peer, ok := selfUpdate.Peer.(*tg.PeerUser); !ok || peer.UserID != bobID {
		t.Fatalf("self ttl peer = %#v, want bob", selfUpdate.Peer)
	}
	if ttl, ok := selfUpdate.GetTTLPeriod(); !ok || ttl != period {
		t.Fatalf("self ttl period = %d ok %v, want %d", ttl, ok, period)
	}
	if got := sessions.pushedUserIDs(); len(got) != 2 || got[0] != aliceID || got[1] != bobID {
		t.Fatalf("pushed users = %+v, want alice then bob", got)
	}
	peerUpdates := sessions.snapshot().message.(*tg.Updates)
	peerUpdate := peerUpdates.Updates[0].(*tg.UpdatePeerHistoryTTL)
	if peer, ok := peerUpdate.Peer.(*tg.PeerUser); !ok || peer.UserID != aliceID {
		t.Fatalf("peer ttl update peer = %#v, want alice", peerUpdate.Peer)
	}
}
