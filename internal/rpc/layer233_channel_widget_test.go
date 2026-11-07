package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	"telesrv/internal/domain"
)

// messages.editMessageWidgetData on a channel message patches only the widget's
// public/chat data in place and projects the schema-native updateMessageWidgetData
// (carrying the channel peer) instead of updateEditChannelMessage.
func TestLayer233EditMessageWidgetDataChannelPatch(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(23311, "15550023311", "Owner")
	channel := createLayer233Megagroup(t, f, owner, "Widget channel")

	sent, err := f.channels.SendChannelMessage(f.ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channel.ID, Message: "widget", Date: 1700000000,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
			WidgetID: 5, PublicData: "a", PublicDataHash: widgetDataHash("a"), ChatData: "chat-a",
		}},
	})
	if err != nil {
		t.Fatalf("send channel widget message: %v", err)
	}

	req := &tg.MessagesEditMessageWidgetDataRequest{Peer: inputPeerChannel(channel), ID: sent.Message.ID}
	req.SetPublicData("b")
	req.SetChatData("chat-b")
	updates, err := f.router.onMessagesEditMessageWidgetData(f.userCtx(owner), req)
	if err != nil {
		t.Fatalf("editMessageWidgetData(channel): %v", err)
	}
	container, ok := updates.(*tg.Updates)
	if !ok || len(container.Updates) != 1 {
		t.Fatalf("channel patch updates = %#v, want one update", updates)
	}
	update, ok := container.Updates[0].(*tg.UpdateMessageWidgetData)
	if !ok {
		t.Fatalf("channel patch update = %T, want *tg.UpdateMessageWidgetData", container.Updates[0])
	}
	peerChannel, ok := update.Peer.(*tg.PeerChannel)
	if !ok || peerChannel.ChannelID != channel.ID {
		t.Fatalf("update peer = %#v, want peerChannel{%d}", update.Peer, channel.ID)
	}
	if update.MsgID != sent.Message.ID {
		t.Fatalf("update msg_id = %d, want %d", update.MsgID, sent.Message.ID)
	}
	if data, ok := update.GetPublicData(); !ok || data != "b" {
		t.Fatalf("update public_data = %q, %v", data, ok)
	}
	if data, ok := update.GetChatData(); !ok || data != "chat-b" {
		t.Fatalf("update chat_data = %q, %v", data, ok)
	}

	// Reapplying the same data is a no-op reported as WIDGET_DATA_NOT_MODIFIED.
	if _, err := f.router.onMessagesEditMessageWidgetData(f.userCtx(owner), req); !tgerr.Is(err, "WIDGET_DATA_NOT_MODIFIED") {
		t.Fatalf("reapply err = %v, want WIDGET_DATA_NOT_MODIFIED", err)
	}
}
