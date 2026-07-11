package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestMessageStorePrivateRandomIDConflictAndReplayFacts(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	base := domain.SendPrivateTextRequest{
		SenderUserID: 1001, RecipientUserID: 1002, RandomID: 501,
		Message: "immutable", Date: 1700000000,
	}
	first, err := messages.SendPrivateText(ctx, base)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	replay := base
	replay.Date++
	replay.OriginSessionID = 77
	replay.RecipientBlocked = true
	duplicate, err := messages.SendPrivateText(ctx, replay)
	if err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	if !duplicate.Duplicate || duplicate.SenderMessage.ID != first.SenderMessage.ID || duplicate.RecipientMessage.ID != first.RecipientMessage.ID {
		t.Fatalf("exact replay = %+v, want original delivered boxes", duplicate)
	}

	tests := []struct {
		name   string
		mutate func(*domain.SendPrivateTextRequest)
	}{
		{name: "peer", mutate: func(req *domain.SendPrivateTextRequest) { req.RecipientUserID = 1003 }},
		{name: "body", mutate: func(req *domain.SendPrivateTextRequest) { req.Message = "changed" }},
		{name: "media", mutate: func(req *domain.SendPrivateTextRequest) {
			req.Media = &domain.MessageMedia{
				Kind: domain.MessageMediaKindContact,
				Contact: &domain.MessageContact{
					PhoneNumber: "+10000000000",
					FirstName:   "Changed",
				},
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			if _, err := messages.SendPrivateText(ctx, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
				t.Fatalf("conflicting replay err = %v, want ErrMessageRandomIDDuplicate", err)
			}
		})
	}
}

func TestMessageStorePrivateRandomIDSelfAndBlockedReplay(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	selfReq := domain.SendPrivateTextRequest{
		SenderUserID: 2001, RecipientUserID: 2001, RandomID: 601,
		Message: "saved note", Date: 1700000100,
	}
	self, err := messages.SendPrivateText(ctx, selfReq)
	if err != nil {
		t.Fatalf("self first: %v", err)
	}
	selfReq.Date++
	selfReplay, err := messages.SendPrivateText(ctx, selfReq)
	if err != nil {
		t.Fatalf("self replay: %v", err)
	}
	if !selfReplay.Duplicate || selfReplay.SenderMessage.ID != self.SenderMessage.ID || selfReplay.RecipientMessage.ID != self.SenderMessage.ID {
		t.Fatalf("self replay = %+v, want original single box", selfReplay)
	}

	blockedReq := domain.SendPrivateTextRequest{
		SenderUserID: 2002, RecipientUserID: 2003, RandomID: 602,
		Message: "blocked", Date: 1700000110, RecipientBlocked: true,
	}
	blocked, err := messages.SendPrivateText(ctx, blockedReq)
	if err != nil {
		t.Fatalf("blocked first: %v", err)
	}
	if blocked.RecipientMessage.ID != 0 {
		t.Fatalf("blocked recipient = %+v, want empty", blocked.RecipientMessage)
	}
	blockedReq.Date++
	blockedReq.RecipientBlocked = false
	blockedReplay, err := messages.SendPrivateText(ctx, blockedReq)
	if err != nil {
		t.Fatalf("blocked replay: %v", err)
	}
	if !blockedReplay.Duplicate || blockedReplay.SenderMessage.ID != blocked.SenderMessage.ID || blockedReplay.RecipientMessage.ID != 0 {
		t.Fatalf("blocked replay = %+v, want original sender-only result", blockedReplay)
	}
}

func TestMessageStorePrivateRandomIDReplayUsesCurrentSnapshotAndDurableDeleteMemory(t *testing.T) {
	ctx := context.Background()
	messages := NewMessageStore()
	req := domain.SendPrivateTextRequest{
		SenderUserID: 3001, RecipientUserID: 3002, RandomID: 701,
		Message: "original", Date: 1700000200,
	}
	first, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	edited, err := messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID: req.SenderUserID,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: req.RecipientUserID},
		ID:          first.SenderMessage.ID,
		Message:     "edited projection",
		EditDate:    1700000201,
	})
	if err != nil {
		t.Fatalf("edit message: %v", err)
	}
	senderPtsBeforeReplay := messages.nextPts[req.SenderUserID]
	recipientPtsBeforeReplay := messages.nextPts[req.RecipientUserID]
	replay, err := messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("replay after edit: %v", err)
	}
	if replay.SenderMessage.ID != first.SenderMessage.ID || replay.SenderMessage.Pts != edited.Self().Message.Pts || replay.SenderMessage.Body != "edited projection" {
		t.Fatalf("replay after edit = %+v, want current visible snapshot", replay.SenderMessage)
	}
	if replay.SenderEvent.Pts != first.SenderEvent.Pts || replay.ReplayDeleteEvent != nil {
		t.Fatalf("replay after edit event = %+v delete=%+v, want original send pts and no delete", replay.SenderEvent, replay.ReplayDeleteEvent)
	}
	if messages.nextPts[req.SenderUserID] != senderPtsBeforeReplay || messages.nextPts[req.RecipientUserID] != recipientPtsBeforeReplay {
		t.Fatalf("edit replay advanced pts sender/recipient = %d/%d, want %d/%d", messages.nextPts[req.SenderUserID], messages.nextPts[req.RecipientUserID], senderPtsBeforeReplay, recipientPtsBeforeReplay)
	}
	deleted, err := messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
		OwnerUserID: req.SenderUserID,
		IDs:         []int{first.SenderMessage.ID},
		Revoke:      true,
		Date:        1700000202,
	})
	if err != nil {
		t.Fatalf("delete message: %v", err)
	}
	senderPtsBeforeReplay = messages.nextPts[req.SenderUserID]
	recipientPtsBeforeReplay = messages.nextPts[req.RecipientUserID]
	replay, err = messages.SendPrivateText(ctx, req)
	if err != nil {
		t.Fatalf("replay after delete: %v", err)
	}
	if replay.SenderMessage.ID != first.SenderMessage.ID || replay.SenderMessage.Pts != first.SenderMessage.Pts || replay.SenderMessage.Body != "original" {
		t.Fatalf("replay after delete = %+v, want immutable first snapshot", replay.SenderMessage)
	}
	if replay.ReplayDeleteEvent == nil || replay.ReplayDeleteEvent.Pts != deleted.Self().Event.Pts ||
		len(replay.ReplayDeleteEvent.MessageIDs) != 1 || replay.ReplayDeleteEvent.MessageIDs[0] != first.SenderMessage.ID {
		t.Fatalf("replay delete event = %+v, want durable delete %+v", replay.ReplayDeleteEvent, deleted.Self().Event)
	}
	if messages.nextPts[req.SenderUserID] != senderPtsBeforeReplay || messages.nextPts[req.RecipientUserID] != recipientPtsBeforeReplay {
		t.Fatalf("delete replay advanced pts sender/recipient = %d/%d, want %d/%d", messages.nextPts[req.SenderUserID], messages.nextPts[req.RecipientUserID], senderPtsBeforeReplay, recipientPtsBeforeReplay)
	}
}
