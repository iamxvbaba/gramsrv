package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
)

func TestMessagesGetRecentLocationsReturnsOnlyGeoLive(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}

	if _, err := r.onMessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer: peer, Message: "not a location", RandomID: 73001,
	}); err != nil {
		t.Fatalf("send text: %v", err)
	}
	want := sendTestLiveLocation(t, r, owner.ID, peer, 73002, 900)

	result, err := r.onMessagesGetRecentLocations(ctx, &tg.MessagesGetRecentLocationsRequest{Peer: peer, Limit: 20})
	if err != nil {
		t.Fatalf("getRecentLocations: %v", err)
	}
	var messages []tg.MessageClass
	switch value := result.(type) {
	case *tg.MessagesMessages:
		messages = value.Messages
	case *tg.MessagesMessagesSlice:
		messages = value.Messages
	default:
		t.Fatalf("getRecentLocations = %T", result)
	}
	if len(messages) != 1 {
		t.Fatalf("recent location messages = %d, want 1", len(messages))
	}
	got, ok := messages[0].(*tg.Message)
	if !ok || got.ID != want.ID {
		t.Fatalf("recent location = %#v, want id %d", messages[0], want.ID)
	}
	if _, ok := got.Media.(*tg.MessageMediaGeoLive); !ok {
		t.Fatalf("recent location media = %T, want MessageMediaGeoLive", got.Media)
	}
}

func TestMessagesGetRecentLocationsValidatesLimitAndAccessHash(t *testing.T) {
	r, owner, friend := newMediaTestRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	if _, err := r.onMessagesGetRecentLocations(ctx, &tg.MessagesGetRecentLocationsRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}, Limit: maxSearchResultsLimit + 1,
	}); err == nil || !tgerr.Is(err, "LIMIT_INVALID") {
		t.Fatalf("oversized limit err = %v, want LIMIT_INVALID", err)
	}
	if _, err := r.onMessagesGetRecentLocations(ctx, &tg.MessagesGetRecentLocationsRequest{
		Peer: &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash + 1}, Limit: 20,
	}); err == nil || !tgerr.Is(err, "USER_ID_INVALID") {
		t.Fatalf("bad access hash err = %v, want USER_ID_INVALID", err)
	}
}
