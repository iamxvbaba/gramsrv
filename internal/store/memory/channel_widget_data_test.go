package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStorePatchWidgetData verifies the channel WidgetData patch mode: only the
// widget's public/chat data changes, body/edit_date are untouched, the event is
// channel_widget_data, and reapplying the same data is a no-op.
func TestChannelStorePatchWidgetData(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000311)

	created, err := st.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator, Title: "WD", Megagroup: true, Date: 1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	sent, err := st.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: creator, ChannelID: channelID, Message: "widget", Date: 1700000000,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
			WidgetID: 42, PublicData: "a", PublicDataHash: domain.WidgetDataHash("a"), ChatData: "chat-a",
		}},
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}

	public := "b"
	chat := "chat-b"
	patchReq := domain.EditChannelMessageRequest{
		UserID: creator, ChannelID: channelID, ID: sent.Message.ID,
		WidgetData: &domain.MessageWidgetDataPatch{PublicData: &public, ChatData: &chat},
	}
	res, err := st.EditChannelMessage(ctx, patchReq)
	if err != nil {
		t.Fatalf("EditChannelMessage(WidgetData): %v", err)
	}
	if res.Event.Type != domain.ChannelUpdateWidgetData {
		t.Fatalf("event type = %q, want channel_widget_data", res.Event.Type)
	}
	if res.Event.Pts == 0 {
		t.Fatalf("event pts = 0, want a reserved channel pts")
	}
	widget := res.Message.Media.Widget
	if widget == nil || widget.PublicData != "b" || widget.PublicDataHash != domain.WidgetDataHash("b") || widget.ChatData != "chat-b" {
		t.Fatalf("widget data not patched: %+v", widget)
	}
	if res.Message.Body != "widget" || res.Message.EditDate != 0 {
		t.Fatalf("body/edit_date changed: body=%q edit_date=%d", res.Message.Body, res.Message.EditDate)
	}

	if _, err := st.EditChannelMessage(ctx, patchReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("reapply err = %v, want ErrMessageNotModified", err)
	}
}
