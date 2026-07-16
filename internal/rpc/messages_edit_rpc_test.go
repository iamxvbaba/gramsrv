package rpc

import (
	"context"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"
	"strings"
	"telesrv/internal/domain"
	"testing"
)

func TestUpdatesDifferenceIncludesEditMessage(t *testing.T) {
	msg := domain.Message{
		ID:          4,
		OwnerUserID: 1000000001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000002},
		From:        domain.Peer{Type: domain.PeerTypeUser, ID: 1000000001},
		Date:        1700000200,
		EditDate:    1700000300,
		Out:         true,
		Body:        "edited",
		Pts:         8,
	}
	got, ok := tgUpdatesDifference(0, domain.UpdateDifference{
		State: domain.UpdateState{Pts: 8, Date: 1700000300, Seq: 0},
		Events: []domain.UpdateEvent{{
			Type:     domain.UpdateEventEditMessage,
			Pts:      8,
			PtsCount: 1,
			Date:     1700000300,
			Message:  msg,
		}},
	}).(*tg.UpdatesDifference)
	if !ok {
		t.Fatalf("difference = %T, want *tg.UpdatesDifference", got)
	}
	if len(got.OtherUpdates) != 1 {
		t.Fatalf("other updates = %+v, want one edit update", got.OtherUpdates)
	}
	update, ok := got.OtherUpdates[0].(*tg.UpdateEditMessage)
	if !ok || update.Pts != 8 || update.PtsCount != 1 {
		t.Fatalf("edit update = %T %+v, want pts=8", got.OtherUpdates[0], got.OtherUpdates[0])
	}
	edited, ok := update.Message.(*tg.Message)
	if !ok || edited.Message != "edited" || edited.EditDate != 1700000300 {
		t.Fatalf("edited message = %#v, want text and edit_date", update.Message)
	}
}

func TestMessagesEditMessageReturnsUpdateAndRecordsOwnerContext(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	var authKeyID [8]byte
	authKeyID[0] = 9
	messages := &captureMessages{}
	r := New(Config{}, Deps{Messages: messages}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	}
	req.SetMessage("edited")
	req.SetEntities([]tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 6}})
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	enc, err := r.Dispatch(WithUserID(context.Background(), userID), authKeyID, 77, &in)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, ok := enc.(*tg.Updates)
	if !ok {
		t.Fatalf("response = %T, want *tg.Updates", enc)
	}
	if len(got.Updates) != 1 {
		t.Fatalf("updates = %+v, want one update", got)
	}
	edit, ok := got.Updates[0].(*tg.UpdateEditMessage)
	if !ok || edit.Pts != 7 || edit.PtsCount != 1 {
		t.Fatalf("edit update = %#v, want pts=7 count=1", got.Updates[0])
	}
	msg, ok := edit.Message.(*tg.Message)
	if !ok || msg.ID != 3 || msg.Message != "edited" {
		t.Fatalf("edited message = %#v, want id=3 text edited", edit.Message)
	}
	if messages.editReq.OwnerUserID != userID || messages.editReq.Peer.ID != peerID || messages.editReq.ID != 3 || messages.editReq.OriginAuthKeyID != authKeyID || messages.editReq.OriginSessionID != 77 {
		t.Fatalf("edit request = %+v, want owner peer message id and origin", messages.editReq)
	}
	if len(messages.editReq.Entities) != 1 || messages.editReq.Entities[0].Type != domain.MessageEntityBold {
		t.Fatalf("edit entities = %+v, want bold", messages.editReq.Entities)
	}
}

func TestMessagesEditMessageOptionBoundaries(t *testing.T) {
	const (
		userID = int64(1000000001)
		peerID = int64(1000000002)
	)
	ctx := WithUserID(context.Background(), userID)
	r := New(Config{}, Deps{Messages: &captureMessages{}}, zaptest.NewLogger(t), clock.System)

	webPreviewReq := &tg.MessagesEditMessageRequest{
		Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22},
		ID:   3,
	}
	webPreviewReq.SetMessage("edited with link")
	webPreviewReq.SetMedia(&tg.InputMediaWebPage{URL: "https://example.test/"})
	if _, err := r.onMessagesEditMessage(ctx, webPreviewReq); err != nil {
		t.Fatalf("edit with webpage media err = %v, want text-only downgrade", err)
	}

	cases := []struct {
		name string
		req  *tg.MessagesEditMessageRequest
		want string
	}{
		{
			name: "quick reply shortcut",
			req: func() *tg.MessagesEditMessageRequest {
				req := &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3}
				req.SetMessage("edited")
				req.SetQuickReplyShortcutID(11)
				return req
			}(),
			want: "MESSAGE_ID_INVALID",
		},
		{
			name: "unsupported media",
			req: func() *tg.MessagesEditMessageRequest {
				req := &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3}
				req.SetMessage("edited")
				req.SetMedia(&tg.InputMediaUploadedPhoto{})
				return req
			}(),
			want: "MEDIA_INVALID",
		},
		{
			name: "message flag missing",
			req:  &tg.MessagesEditMessageRequest{Peer: &tg.InputPeerUser{UserID: peerID, AccessHash: 22}, ID: 3},
			want: "MESSAGE_EMPTY",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := r.onMessagesEditMessage(ctx, tc.req); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("edit err = %v, want %s", err, tc.want)
			}
		})
	}
}
