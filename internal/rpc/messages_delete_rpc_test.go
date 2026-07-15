package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestMessagesDeleteHistoryChannelReturnsOffsetForBoundedPage(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 45, Phone: "15550002145", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 46, Phone: "15550002146", FirstName: "Friend"})
	channelStore := memory.NewChannelStore()
	created, err := channelStore.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner.ID,
		Title:         "Delete History Offset",
		Megagroup:     true,
		MemberUserIDs: []int64{friend.ID},
		Date:          1_700_002_145,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	total := domain.MaxDeleteHistoryBatch + 2
	for i := 0; i < total; i++ {
		if _, err := channelStore.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
			UserID:    owner.ID,
			ChannelID: created.Channel.ID,
			RandomID:  int64(210_000 + i),
			Message:   "bulk delete",
			Date:      1_700_002_146 + i,
		}); err != nil {
			t.Fatalf("send channel message %d: %v", i, err)
		}
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Channels: appchannels.NewService(channelStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)

	req := &tg.MessagesDeleteHistoryRequest{
		Peer:  &tg.InputPeerChannel{ChannelID: created.Channel.ID, AccessHash: created.Channel.AccessHash},
		MaxID: 0,
	}
	req.SetRevoke(true)
	affected, err := r.onMessagesDeleteHistory(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("messages.deleteHistory channel: %v", err)
	}
	if affected.Offset != 1 || affected.PtsCount != domain.MaxDeleteHistoryBatch {
		t.Fatalf("affected history = %+v, want offset=1 pts_count=%d", affected, domain.MaxDeleteHistoryBatch)
	}
	pushed := sessions.snapshot()
	updates, ok := pushed.message.(*tg.Updates)
	if !ok || len(updates.Updates) != 1 {
		t.Fatalf("pushed update = %T %+v, want one bounded delete update", pushed.message, pushed.message)
	}
	deleted, ok := updates.Updates[0].(*tg.UpdateDeleteChannelMessages)
	if !ok {
		t.Fatalf("pushed update[0] = %T, want updateDeleteChannelMessages", updates.Updates[0])
	}
	if len(deleted.Messages) != domain.MaxDeleteHistoryBatch || deleted.PtsCount != domain.MaxDeleteHistoryBatch {
		t.Fatalf("delete update len=%d pts_count=%d, want bounded %d", len(deleted.Messages), deleted.PtsCount, domain.MaxDeleteHistoryBatch)
	}
}

func TestUpdatesDifferenceIncludesDeleteMessages(t *testing.T) {
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 8, Date: 1700000250, Seq: 0},
		Events: []domain.UpdateEvent{{
			Type:       domain.UpdateEventDeleteMessages,
			Pts:        8,
			PtsCount:   3,
			Date:       1700000250,
			MessageIDs: []int{3, 4, 5},
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if got.State.Pts != 8 || len(got.OtherUpdates) != 1 {
		t.Fatalf("difference = %+v, want one delete update and pts=8", got)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateDeleteMessages)
	if !ok {
		t.Fatalf("update = %T, want *tg.UpdateDeleteMessages", got.OtherUpdates[0])
	}
	if update.Pts != 8 || update.PtsCount != 3 || len(update.Messages) != 3 || update.Messages[0] != 3 || update.Messages[2] != 5 {
		t.Fatalf("delete update = %+v, want ids [3 4 5] pts=8 pts_count=3", update)
	}
}

func TestMessagesDeleteMessagesPassesOwnerContext(t *testing.T) {
	const userID = int64(1000000001)
	var authKeyID [8]byte
	authKeyID[0] = 10
	messages := &captureMessages{deleteMessagesRes: domain.DeleteMessagesResult{
		OwnerUserID: userID,
		Deleted: []domain.DeletedMessagesForUser{{
			UserID:     userID,
			MessageIDs: []int{2, 3},
			Event:      domain.UpdateEvent{Pts: 9, PtsCount: 2},
		}},
	}}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{3, 2}}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), authKeyID, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedMessages)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedMessages", enc)
	}
	if got.Pts != 9 || got.PtsCount != 2 {
		t.Fatalf("affected = %+v, want pts=9 pts_count=2", got)
	}
	if messages.deleteMessagesReq.OwnerUserID != userID || !messages.deleteMessagesReq.Revoke || messages.deleteMessagesReq.OriginSessionID != 77 || messages.deleteMessagesReq.OriginAuthKeyID != authKeyID {
		t.Fatalf("delete request = %+v, want owner/revoke/current session", messages.deleteMessagesReq)
	}
	if len(messages.deleteMessagesReq.IDs) != 2 || messages.deleteMessagesReq.IDs[0] != 3 || messages.deleteMessagesReq.IDs[1] != 2 {
		t.Fatalf("delete ids = %+v, want request order [3 2]", messages.deleteMessagesReq.IDs)
	}
}

func TestMessagesDeleteHistoryPassesJustClearContext(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	var authKeyID [8]byte
	authKeyID[0] = 11
	messages := &captureMessages{deleteHistoryRes: domain.DeleteMessagesResult{
		OwnerUserID: userID,
		Deleted: []domain.DeletedMessagesForUser{{
			UserID:     userID,
			MessageIDs: []int{1, 2, 3},
			Event:      domain.UpdateEvent{Pts: 12, PtsCount: 3},
		}},
	}}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesDeleteHistoryRequest{
		JustClear: true,
		Revoke:    true,
		Peer:      &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		MaxID:     15,
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), authKeyID, 88, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.MessagesAffectedHistory)
	if !ok {
		t.Fatalf("response = %T, want *tg.MessagesAffectedHistory", enc)
	}
	if got.Pts != 12 || got.PtsCount != 3 {
		t.Fatalf("affected = %+v, want pts=12 pts_count=3", got)
	}
	reqGot := messages.deleteHistoryReq
	if reqGot.OwnerUserID != userID || reqGot.Peer.ID != peerID || reqGot.MaxID != 15 || !reqGot.JustClear || !reqGot.Revoke || reqGot.OriginSessionID != 88 || reqGot.OriginAuthKeyID != authKeyID {
		t.Fatalf("delete history request = %+v, want owner peer max_id flags current session", reqGot)
	}
}
