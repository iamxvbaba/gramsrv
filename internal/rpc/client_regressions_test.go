package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	"telesrv/internal/branding"
	"telesrv/internal/domain"
)

func TestMessagesSearchPhoneCallsDoesNotFallThroughToOrdinaryHistory(t *testing.T) {
	r := New(Config{}, Deps{}, zaptest.NewLogger(t), clock.System)
	filter, err := r.messageFilterFromSearchRequest(context.Background(), 1001, &tg.MessagesSearchRequest{
		Peer:   &tg.InputPeerSelf{},
		Filter: &tg.InputMessagesFilterPhoneCalls{Missed: true},
		Limit:  50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filter.PhoneCallsOnly || !filter.MissedPhoneCallsOnly {
		t.Fatalf("phone filter = %+v", filter)
	}
	if searchFilterNeedsMediaStore(&tg.InputMessagesFilterPhoneCalls{}) {
		t.Fatal("phone-call filter must use message service-action search, not media search")
	}
}

func TestCrossDialogReplyKeepsExplicitSourcePeer(t *testing.T) {
	const senderID, destinationID, sourceID = int64(1001), int64(1002), int64(1003)
	r := New(Config{}, Deps{Users: mapUsersService{users: map[int64]domain.User{
		senderID:      {ID: senderID, AccessHash: 11},
		destinationID: {ID: destinationID, AccessHash: 22},
		sourceID:      {ID: sourceID, AccessHash: 33},
	}}}, zaptest.NewLogger(t), clock.System)
	input := &tg.InputReplyToMessage{ReplyToMsgID: 77}
	input.SetReplyToPeerID(&tg.InputPeerUser{UserID: sourceID, AccessHash: 33})
	input.SetQuoteText("source quote")
	reply, err := r.messageReplyFromInput(context.Background(), senderID,
		domain.Peer{Type: domain.PeerTypeUser, ID: destinationID}, input)
	if err != nil {
		t.Fatal(err)
	}
	if reply == nil || reply.MessageID != 77 || reply.Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: sourceID}) {
		t.Fatalf("cross-dialog reply = %+v", reply)
	}
}

func TestStarGiftCustomEmojiEntitiesSurviveAllProjections(t *testing.T) {
	entity := domain.MessageEntity{Type: domain.MessageEntityCustomEmoji, Offset: 0, Length: 2, DocumentID: 987654321}
	action, ok := tgMessageActionStarGift(&domain.MessageStarGiftAction{
		GiftID: 1, Stars: 15, Message: "🎁", MessageEntities: []domain.MessageEntity{entity},
	}).(*tg.MessageActionStarGift)
	if !ok {
		t.Fatalf("action = %T", action)
	}
	message, ok := action.GetMessage()
	if !ok || len(message.Entities) != 1 {
		t.Fatalf("action message = %+v", message)
	}
	custom, ok := message.Entities[0].(*tg.MessageEntityCustomEmoji)
	if !ok || custom.DocumentID != entity.DocumentID {
		t.Fatalf("action custom emoji = %#v", message.Entities[0])
	}

	saved := tgSavedStarGifts(1001, []domain.SavedStarGift{{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Message: "🎁",
		MessageEntities: []domain.MessageEntity{entity},
	}}, nil, nil, 0)[0]
	savedMessage, ok := saved.GetMessage()
	if !ok || len(savedMessage.Entities) != 1 {
		t.Fatalf("saved message = %+v", savedMessage)
	}

	unique := tgUniqueStarGift(domain.UniqueStarGift{
		KeepOriginalDetails: true,
		OriginalOwner:       domain.Peer{Type: domain.PeerTypeUser, ID: 1001},
		OriginalDate:        1, OriginalMessage: "🎁", OriginalMessageEntities: []domain.MessageEntity{entity},
	})
	original, ok := unique.Attributes[len(unique.Attributes)-1].(*tg.StarGiftAttributeOriginalDetails)
	if !ok {
		t.Fatalf("original details = %#v", unique.Attributes)
	}
	originalMessage, ok := original.GetMessage()
	if !ok || len(originalMessage.Entities) != 1 {
		t.Fatalf("unique original message = %+v", originalMessage)
	}
}

func TestOfficialSystemUserProjectionUsesConfiguredBrand(t *testing.T) {
	previous := branding.Current()
	t.Cleanup(func() { _ = branding.Configure(previous) })
	cfg := previous
	cfg.ProductName = "NexGram"
	cfg.ProductUsername = "nexgram"
	if err := branding.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	projected := tgUser(domain.User{ID: domain.OfficialSystemUserID, FirstName: "Telesrv", Username: "telesrv"})
	if projected.FirstName != "NexGram" || projected.Username != "nexgram" {
		t.Fatalf("official projection = %+v", projected)
	}
}
