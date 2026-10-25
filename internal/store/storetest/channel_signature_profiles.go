package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelSignatureProfiles checks both implementations against the same durable
// visibility contract. Reopen must discard process-local caches for PostgreSQL.
func ChannelSignatureProfiles(t *testing.T, s store.ChannelStore, reopen func() store.ChannelStore, owner, member int64) {
	t.Helper()
	ctx := context.Background()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Signature contract", Broadcast: true, MemberUserIDs: []int64{member}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.SetSignatures(ctx, owner, id, false, true); !errors.Is(err, domain.ErrChannelInvalid) {
		t.Fatalf("invalid profiles = %v", err)
	}
	if _, err := s.SetSignatures(ctx, member, id, true, true); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("member toggle = %v", err)
	}
	requests := make(map[int64]domain.SendChannelMessageRequest)
	send := func(random int64, peer *domain.Peer) domain.ChannelMessage {
		t.Helper()
		req := domain.SendChannelMessageRequest{UserID: owner, ChannelID: id, Message: "post", RandomID: random, Date: baseNow(), PostAuthor: "Owner", SendAs: peer}
		requests[random] = req
		res, err := s.SendChannelMessage(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return res.Message
	}
	old := send(1, nil)
	before, err := s.GetChannel(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.SetSignatures(ctx, owner, id, true, true); err != nil {
			t.Fatal(err)
		}
	}
	s = reopen()
	view, err := s.GetChannel(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Channel.SignatureProfiles || !view.Channel.Signatures || view.Channel.Pts != before.Channel.Pts {
		t.Fatal("settings lost or consumed PTS")
	}
	logs, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: id, Limit: 100, Filter: domain.ChannelAdminLogFilter{Settings: true}})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[domain.ChannelAdminLogEventType]int{}
	for _, event := range logs.Events {
		counts[event.Type]++
	}
	if counts[domain.ChannelAdminLogToggleSignatures] != 1 || counts[domain.ChannelAdminLogToggleSignatureProfiles] != 1 {
		t.Fatalf("toggle event counts = %v", counts)
	}
	self := domain.Peer{Type: domain.PeerTypeUser, ID: owner}
	personal := send(2, &self)
	current := domain.Peer{Type: domain.PeerTypeChannel, ID: id}
	anonymous := send(3, &current)
	if _, err := s.SetSignatures(ctx, owner, id, true, false); err != nil {
		t.Fatal(err)
	}
	signed := send(4, &current)
	if old.ShowMessageSender || !personal.ShowMessageSender || anonymous.ShowMessageSender || signed.ShowMessageSender || signed.PostAuthor != "Owner" || personal.PostAuthor != "" {
		t.Fatal("incorrect author snapshots")
	}
	if _, err := s.EditChannelMessage(ctx, domain.EditChannelMessageRequest{UserID: owner, ChannelID: id, ID: personal.ID, Message: "edited"}); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	history, err := s.GetChannelMessages(ctx, member, id, []int{old.ID, personal.ID, anonymous.ID, signed.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 4 {
		t.Fatalf("history count %d", len(history.Messages))
	}
	for _, msg := range history.Messages {
		if msg.ShowMessageSender != (msg.ID == personal.ID) {
			t.Fatalf("history visibility changed: %d", msg.ID)
		}
	}
	diff, err := s.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: member, ChannelID: id, Pts: created.Channel.Pts, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, event := range diff.Events {
		if event.Message.ID == personal.ID {
			seen = true
			if !event.Message.ShowMessageSender {
				t.Fatal("difference lost author visibility")
			}
		}
	}
	if !seen {
		t.Fatal("difference missing profile message")
	}
	replay, err := s.SendChannelMessage(ctx, requests[2])
	if err != nil {
		t.Fatal(err)
	}
	if replay.Message.ID != personal.ID || !replay.Message.ShowMessageSender || replay.Message.Body != "edited" || replay.Event.Pts != personal.Pts {
		t.Fatal("retry changed author visibility or original PTS")
	}
}
