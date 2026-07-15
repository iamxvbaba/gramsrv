package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"reflect"
	"telesrv/internal/domain"
	"testing"
)

func TestUpdatesDifferenceIncludesReadHistoryInbox(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 6, Date: 1700000200, Seq: 5},
		Events: []domain.UpdateEvent{{
			Type:             domain.UpdateEventReadHistoryInbox,
			Pts:              6,
			PtsCount:         1,
			Date:             1700000200,
			Peer:             peer,
			MaxID:            12,
			StillUnreadCount: 0,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 6 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference = %+v, want one read history update and pts=6", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateReadHistoryInbox)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateReadHistoryInbox", got.OtherUpdates[0])
	}
	if update.MaxID != 12 || update.Pts != 6 || update.PtsCount != 1 {
		t.Fatalf("read update = %+v, want max_id=12 pts=6 pts_count=1", update)
	}
}

func TestUpdatesDifferenceIncludesReadHistoryOutbox(t *testing.T) {
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 7, Date: 1700000210, Seq: 0},
		Events: []domain.UpdateEvent{{
			Type:     domain.UpdateEventReadHistoryOutbox,
			Pts:      7,
			PtsCount: 1,
			Date:     1700000210,
			Peer:     peer,
			MaxID:    9,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.OtherUpdates) != 1 {
		t.Fatalf("other updates = %+v, want one read outbox update", got.OtherUpdates)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateReadHistoryOutbox)
	if !ok || update.MaxID != 9 || update.Pts != 7 || update.PtsCount != 1 {
		t.Fatalf("read outbox = %T %+v, want max_id=9 pts=7", got.OtherUpdates[0], got.OtherUpdates[0])
	}
}

func TestMessagesGetOutboxReadDateReturnsDate(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	messages := &captureMessages{outboxReadDate: 1700000300}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesGetOutboxReadDateRequest{
		Peer:  &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MsgID: 3,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), [8]byte{}, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.OutboxReadDate)
	if !ok || got.Date != 1700000300 {
		t.Fatalf("response = %T %#v, want outboxReadDate date", enc, enc)
	}
	if messages.outboxReadDateReq.OwnerUserID != userID || messages.outboxReadDateReq.Peer.ID != peerID || messages.outboxReadDateReq.ID != 3 {
		t.Fatalf("read date request = %+v, want owner peer message id", messages.outboxReadDateReq)
	}
}

func TestMessagesReadMessageContentsPushesUpdateToOtherSessions(t *testing.T) {
	authKeyID := [8]byte{9, 9, 9}
	messages := &captureMessages{
		readContentsRes: domain.ReadMessageContentsResult{
			OwnerUserID: 1000000001,
			MessageIDs:  []int{7, 8},
		},
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Messages: messages,
		Updates:  &captureUpdates{state: domain.UpdateState{Pts: 42, Date: 1700000200}},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	ctx := WithSessionID(WithAuthKeyID(WithUserID(context.Background(), 1000000001), authKeyID), 55)

	affected, err := r.onMessagesReadMessageContents(ctx, []int{7, 8})
	if err != nil {
		t.Fatalf("messages.readMessageContents: %v", err)
	}
	if affected.Pts != 42 || affected.PtsCount != 0 {
		t.Fatalf("affected = %+v, want pts=42 pts_count=0", affected)
	}
	if messages.readContentsReq.OwnerUserID != 1000000001 || !reflect.DeepEqual(messages.readContentsReq.IDs, []int{7, 8}) {
		t.Fatalf("read contents req = %+v", messages.readContentsReq)
	}
	snap := sessions.snapshot()
	if snap.userID != 1000000001 || snap.sessionID != 55 || snap.messageType != proto.MessageFromServer {
		t.Fatalf("push target = %+v", snap)
	}
	updates, ok := snap.message.(*tg.Updates)
	if !ok {
		t.Fatalf("pushed message = %T, want *tg.Updates", snap.message)
	}
	if len(updates.Updates) != 1 {
		t.Fatalf("updates = %+v", updates.Updates)
	}
	read, ok := updates.Updates[0].(*tg.UpdateReadMessagesContents)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateReadMessagesContents", updates.Updates[0])
	}
	if !reflect.DeepEqual(read.Messages, []int{7, 8}) || read.Pts != 42 || read.PtsCount != 0 {
		t.Fatalf("read update = %+v", read)
	}
}

func TestMessagesReadHistoryMarksDialogRead(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 7
	messages := &captureMessages{readResult: domain.ReadHistoryResult{
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		MaxID:       12,
		Changed:     true,
		InboxEvent: domain.UpdateEvent{
			Type:     domain.UpdateEventReadHistoryInbox,
			Pts:      5,
			PtsCount: 1,
			Date:     1700000100,
			Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			MaxID:    12,
		},
	}}
	updates := &captureUpdates{state: domain.UpdateState{Pts: 5, Date: 1700000100, Seq: 3}}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Updates: updates, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesReadHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		MaxID: 12,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), authKeyID, 0, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedMessages)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedMessages", enc)
	}
	if messages.readPeer.ID != domain.OfficialSystemUserID || messages.readMaxID != 12 {
		t.Fatalf("read = peer %+v max %d, want official/12", messages.readPeer, messages.readMaxID)
	}
	if got.Pts != 5 || got.PtsCount != 1 {
		t.Fatalf("affected = %+v, want recorded read-history pts", got)
	}
	gotSession := sessions.snapshot()
	if gotSession.userID != 1000000001 || gotSession.messageType != proto.MessageFromServer {
		t.Fatalf("push target = user %d type %v, want read update push to other sessions", gotSession.userID, gotSession.messageType)
	}
}

func TestMessagesReadHistoryWithReliableDispatchPushesCurrentSessionReadUpdate(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 8
	messages := &captureMessages{readResult: domain.ReadHistoryResult{
		OwnerUserID:      1000000001,
		Peer:             domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		MaxID:            12,
		StillUnreadCount: 2,
		Changed:          true,
		InboxEvent: domain.UpdateEvent{
			Type:             domain.UpdateEventReadHistoryInbox,
			Pts:              5,
			PtsCount:         1,
			Date:             1700000100,
			Peer:             domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
			MaxID:            12,
			StillUnreadCount: 2,
		},
	}}
	updates := &captureUpdates{
		state:            domain.UpdateState{Pts: 5, Date: 1700000100, Seq: 3},
		reliableDispatch: true,
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Updates: updates, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesReadHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		MaxID: 12,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), authKeyID, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedMessages)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedMessages", enc)
	}
	if got.Pts != 5 || got.PtsCount != 1 {
		t.Fatalf("affected = %+v, want recorded read-history pts", got)
	}
	if messages.readReq.OriginSessionID != 77 || messages.readReq.OriginAuthKeyID != authKeyID {
		t.Fatalf("read origin = auth %v session %d, want request auth/session", messages.readReq.OriginAuthKeyID, messages.readReq.OriginSessionID)
	}
	snap := sessions.snapshot()
	if snap.sessionID != 77 || snap.messageType != proto.MessageFromServer {
		t.Fatalf("current-session push target = session %d type %v, want session 77 server message", snap.sessionID, snap.messageType)
	}
	updatesMsg, ok := snap.message.(*tg.Updates)
	if !ok || len(updatesMsg.Updates) != 1 {
		t.Fatalf("current-session push = %T %+v, want one updates container", snap.message, snap.message)
	}
	update, ok := updatesMsg.Updates[0].(*tg.UpdateReadHistoryInbox)
	if !ok {
		t.Fatalf("current-session update = %T, want *tg.UpdateReadHistoryInbox", updatesMsg.Updates[0])
	}
	if update.Pts != 5 || update.PtsCount != 1 || update.MaxID != 12 || update.StillUnreadCount != 2 {
		t.Fatalf("current-session update = %+v, want pts=5 count=1 max=12 still=2", update)
	}
}

func TestMessagesReadHistoryAlreadyReadReturnsCurrentStateWithoutEcho(t *testing.T) {
	var authKeyID [8]byte
	authKeyID[0] = 9
	messages := &captureMessages{readResult: domain.ReadHistoryResult{
		OwnerUserID:      1000000001,
		Peer:             domain.Peer{Type: domain.PeerTypeUser, ID: domain.OfficialSystemUserID},
		MaxID:            12,
		StillUnreadCount: 0,
		Changed:          false,
	}}
	updates := &captureUpdates{
		state:            domain.UpdateState{Pts: 7, Date: 1700000101, Seq: 3},
		reliableDispatch: true,
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{Messages: messages, Updates: updates, Sessions: sessions}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesReadHistoryRequest{
		Peer:  &tg.InputPeerUser{UserID: domain.OfficialSystemUserID, AccessHash: domain.OfficialSystemUser().AccessHash},
		MaxID: 12,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), 1000000001), authKeyID, 88, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedMessages)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedMessages", enc)
	}
	if got.Pts != 7 || got.PtsCount != 0 {
		t.Fatalf("affected = %+v, want current pts without advancing", got)
	}
	snap := sessions.snapshot()
	if snap.message != nil {
		t.Fatalf("current-session push = %T, want no direct echo for already-read request", snap.message)
	}
}
