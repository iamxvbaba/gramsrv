package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestMessagesToggleSuggestedPostApprovalRegisteredAndProjectsLifecycle(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	owner, err := users.Create(ctx, domain.User{AccessHash: 101, Phone: "15551110001", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	subscriber, err := users.Create(ctx, domain.User{AccessHash: 102, Phone: "15551110002", FirstName: "Subscriber"})
	if err != nil {
		t.Fatal(err)
	}
	channelsStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelsStore)
	created, err := channels.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "Suggested", Broadcast: true, Date: 1_700_000_000})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := channelsStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	mono, err := channelsStore.GetChannelByID(ctx, enabled.Channel.LinkedMonoforumID)
	if err != nil {
		t.Fatal(err)
	}
	saved := domain.Peer{Type: domain.PeerTypeUser, ID: subscriber.ID}
	suggestion, err := channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: subscriber.ID, SavedPeer: saved, RandomID: 91, Message: "RPC suggestion", SuggestedPost: &domain.SuggestedPost{}, Date: 1_700_000_100})
	if err != nil {
		t.Fatal(err)
	}
	router := New(Config{}, Deps{Users: appusers.NewService(users), Channels: channels}, zaptest.NewLogger(t), clock.System)
	req := &tg.MessagesToggleSuggestedPostApprovalRequest{Peer: &tg.InputPeerChannel{ChannelID: mono.ID, AccessHash: mono.AccessHash}, MsgID: suggestion.Message.ID}
	var raw bin.Buffer
	if err := req.Encode(&raw); err != nil {
		t.Fatal(err)
	}
	response, err := router.Dispatch(WithLayer(WithUserID(ctx, owner.ID), 228), [8]byte{}, 0, &raw)
	if err != nil {
		t.Fatalf("dispatch toggleSuggestedPostApproval: %v", err)
	}
	updates, ok := response.(*tg.Updates)
	if !ok {
		t.Fatalf("response=%T, want *tg.Updates", response)
	}
	var edited, approval, published bool
	for _, update := range updates.Updates {
		switch item := update.(type) {
		case *tg.UpdateEditChannelMessage:
			message, ok := item.Message.(*tg.Message)
			if ok && message.ID == suggestion.Message.ID {
				post, present := message.GetSuggestedPost()
				edited = present && post.GetAccepted()
			}
		case *tg.UpdateNewChannelMessage:
			switch message := item.Message.(type) {
			case *tg.MessageService:
				action, ok := message.Action.(*tg.MessageActionSuggestedPostApproval)
				if ok {
					scheduleDate, hasScheduleDate := action.GetScheduleDate()
					approval = !action.Rejected && !action.BalanceTooLow && hasScheduleDate && scheduleDate > 0
				}
				if savedPeer, present := message.GetSavedPeerID(); !present {
					t.Fatalf("approval service missing saved_peer_id")
				} else if peer, ok := savedPeer.(*tg.PeerUser); !ok || peer.UserID != subscriber.ID {
					t.Fatalf("approval saved_peer=%#v", savedPeer)
				}
			case *tg.Message:
				published = message.PeerID.(*tg.PeerChannel).ChannelID == created.Channel.ID && message.Post && message.Message == "RPC suggestion"
			}
		}
	}
	if !edited || !approval || !published {
		t.Fatalf("updates missing edit/approval/publish: %#v", updates.Updates)
	}

	var retryRaw bin.Buffer
	if err := req.Encode(&retryRaw); err != nil {
		t.Fatal(err)
	}
	retry, err := router.Dispatch(WithLayer(WithUserID(ctx, owner.ID), 227), [8]byte{}, 0, &retryRaw)
	if err != nil {
		t.Fatalf("layer 227 retry: %v", err)
	}
	got, ok := retry.(*tg.Updates)
	if !ok {
		t.Fatalf("layer 227 response=%T", retry)
	}
	// Duplicate replay returns the persisted approval + published update to
	// the caller but is never fanned out again.
	if len(got.Updates) != 3 {
		t.Fatalf("layer 227 duplicate updates=%d, want 3", len(got.Updates))
	}
}

func TestSuggestedPostTLProjectionSeparatesSuggestionAndPublishedPaymentFlags(t *testing.T) {
	original := domain.ChannelMessage{ChannelID: 10, ID: 1, SenderUserID: 20, From: domain.Peer{Type: domain.PeerTypeUser, ID: 20}, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: 20}, Date: 100, Body: "proposal", SuggestedPost: &domain.SuggestedPost{Accepted: true, Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}}}
	proposal := tgChannelMessage(20, original).(*tg.Message)
	if _, present := proposal.GetSuggestedPost(); !present || proposal.GetPaidSuggestedPostStars() {
		t.Fatalf("proposal flags=%+v", proposal)
	}
	published := original
	published.ChannelID, published.ID, published.Post, published.SavedPeer = 11, 2, true, domain.Peer{}
	post := tgChannelMessage(20, published).(*tg.Message)
	if !post.GetPaidSuggestedPostStars() {
		t.Fatalf("published Stars post missing paid flag")
	}
	if _, present := post.GetSuggestedPost(); present {
		t.Fatalf("published post leaked suggested_post")
	}
	published.SuggestedPost.Price = &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceTON, Amount: 10_000_000}
	ton := tgChannelMessage(20, published).(*tg.Message)
	if !ton.GetPaidSuggestedPostTon() || ton.GetPaidSuggestedPostStars() {
		t.Fatalf("published TON flags=%+v", ton)
	}
}

func TestSuggestedPostApprovalScheduleDateSurvivesExactProfiles(t *testing.T) {
	action := tgChannelMessageAction(domain.ChannelMessageAction{
		Type:                      domain.ChannelActionSuggestedPostApproval,
		SuggestedPostScheduleDate: 1_700_000_200,
	})
	for _, profile := range []tlprofile.Profile{tlprofile.Profile227, tlprofile.Profile228} {
		wire := &bin.Buffer{}
		if err := tlprofile.EncodeObject(profile, action, wire); err != nil {
			t.Fatalf("encode Layer %d approval action: %v", profile, err)
		}
		decodedObject, err := tlprofile.DecodeObject(profile, &bin.Buffer{Buf: wire.Copy()}, tlprofile.Limits{})
		if err != nil {
			t.Fatalf("decode Layer %d approval action: %v", profile, err)
		}
		decoded, ok := decodedObject.(*tg.MessageActionSuggestedPostApproval)
		if !ok {
			t.Fatalf("decode Layer %d approval action = %T", profile, decodedObject)
		}
		date, present := decoded.GetScheduleDate()
		if !present || date != 1_700_000_200 {
			t.Fatalf("Layer %d approval date=%d/%v, want 1700000200/true", profile, date, present)
		}
	}
}
