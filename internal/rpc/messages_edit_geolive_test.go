package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func sendTestLiveLocation(t *testing.T, r *Router, fromID int64, peer tg.InputPeerClass, randomID int64, period int) *tg.Message {
	t.Helper()
	point := &tg.InputGeoPoint{Lat: 39.9, Long: 116.4}
	media := &tg.InputMediaGeoLive{GeoPoint: point}
	media.SetPeriod(period)
	media.SetHeading(90)
	updates, err := r.onMessagesSendMedia(WithUserID(context.Background(), fromID), &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    media,
		RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("sendMedia geolive: %v", err)
	}
	return newMessageFromUpdates(t, updates)
}

func TestSendMediaGeoLive(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	msg := sendTestLiveLocation(t, r, owner.ID, &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, 6001, 900)
	media, ok := msg.Media.(*tg.MessageMediaGeoLive)
	if !ok {
		t.Fatalf("expected MessageMediaGeoLive, got %T", msg.Media)
	}
	if media.Period != 900 {
		t.Errorf("period = %d, want 900", media.Period)
	}
	if heading, ok := media.GetHeading(); !ok || heading != 90 {
		t.Errorf("heading = %d (%v), want 90", heading, ok)
	}
	geo, ok := media.Geo.(*tg.GeoPoint)
	if !ok || geo.Lat != 39.9 || geo.Long != 116.4 {
		t.Fatalf("geo = %#v, want (39.9, 116.4)", media.Geo)
	}
}

func TestEditMessageLiveLocationUpdateAndStop(t *testing.T) {
	ctx := context.Background()
	r, owner, friend := newMediaTestRouter(t)
	ownerPeer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	ownerCtx := WithUserID(ctx, owner.ID)

	msg := sendTestLiveLocation(t, r, owner.ID, ownerPeer, 6002, 3600)

	// 续报新坐标。
	editReq := &tg.MessagesEditMessageRequest{Peer: ownerPeer, ID: msg.ID}
	next := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 40.0, Long: 116.5}}
	next.SetHeading(180)
	editReq.SetMedia(next)
	updates, err := r.onMessagesEditMessage(ownerCtx, editReq)
	if err != nil {
		t.Fatalf("editMessage live update: %v", err)
	}
	edited := editedMessageFromUpdates(t, updates)
	live, ok := edited.Media.(*tg.MessageMediaGeoLive)
	if !ok {
		t.Fatalf("expected MessageMediaGeoLive after edit, got %T", edited.Media)
	}
	geo := live.Geo.(*tg.GeoPoint)
	if geo.Lat != 40.0 || geo.Long != 116.5 {
		t.Fatalf("edited geo = (%v, %v), want (40.0, 116.5)", geo.Lat, geo.Long)
	}
	if heading, ok := live.GetHeading(); !ok || heading != 180 {
		t.Fatalf("edited heading = %d (%v), want 180", heading, ok)
	}
	if live.Period != 3600 {
		t.Fatalf("edited period = %d, want preserved 3600", live.Period)
	}

	// 停止共享：period 收口为已逝时长（≤ 原 period 且 ≥1），客户端立即判定过期。
	stopReq := &tg.MessagesEditMessageRequest{Peer: ownerPeer, ID: msg.ID}
	stopped := &tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPointEmpty{}, Stopped: true}
	stopReq.SetMedia(stopped)
	updates, err = r.onMessagesEditMessage(ownerCtx, stopReq)
	if err != nil {
		t.Fatalf("editMessage live stop: %v", err)
	}
	edited = editedMessageFromUpdates(t, updates)
	live, ok = edited.Media.(*tg.MessageMediaGeoLive)
	if !ok {
		t.Fatalf("expected MessageMediaGeoLive after stop, got %T", edited.Media)
	}
	if live.Period < 1 || live.Period > 60 {
		t.Fatalf("stopped period = %d, want small elapsed value", live.Period)
	}

	// 非 geolive 消息不可走 live 编辑。
	plain, err := r.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{
		Peer: ownerPeer, Message: "hello", RandomID: 6003,
	})
	if err != nil {
		t.Fatalf("send plain: %v", err)
	}
	plainMsg := newMessageFromUpdates(t, plain)
	badReq := &tg.MessagesEditMessageRequest{Peer: ownerPeer, ID: plainMsg.ID}
	badReq.SetMedia(&tg.InputMediaGeoLive{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}})
	if _, err := r.onMessagesEditMessage(ownerCtx, badReq); err == nil || !tgerr.Is(err, "MESSAGE_ID_INVALID") {
		t.Fatalf("live edit on text message err = %v, want MESSAGE_ID_INVALID", err)
	}
}

func editedMessageFromUpdates(t *testing.T, updates tg.UpdatesClass) *tg.Message {
	t.Helper()
	upd, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("expected *tg.Updates, got %T", updates)
	}
	for _, u := range upd.Updates {
		if em, ok := u.(*tg.UpdateEditMessage); ok {
			msg, ok := em.Message.(*tg.Message)
			if !ok {
				t.Fatalf("expected *tg.Message, got %T", em.Message)
			}
			return msg
		}
	}
	t.Fatal("no updateEditMessage found")
	return nil
}
