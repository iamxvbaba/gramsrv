package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStorePatchWidgetDataIntegration verifies the channel WidgetData patch branch against
// live Postgres: only the widget's public/chat data changes in place, body/edit_date are unchanged,
// the durable channel_widget_data event is accepted by the whitelist (migration 20261106030000),
// history reads back the patched data, and reapplying is a no-op. Requires TELESRV_TEST_POSTGRES_DSN.
func TestChannelStorePatchWidgetDataIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 52, Phone: "+1669" + suffix + "11", FirstName: "ChWDOwner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	var channelID int64
	t.Cleanup(func() {
		if channelID != 0 {
			_, _ = pool.Exec(ctx, "DELETE FROM channels WHERE id = $1", channelID)
		}
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", owner.ID)
	})

	channels := NewChannelStore(pool)
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "ChWD " + suffix, Megagroup: true, Date: 1700000400})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 9912, Message: "widget", Date: 1700000401,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
			WidgetID: 77, PublicData: "a", PublicDataHash: domain.WidgetDataHash("a"), ChatData: "chat-a",
		}},
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}

	public := "b"
	chat := "chat-b"
	patchReq := domain.EditChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, ID: sent.Message.ID,
		WidgetData: &domain.MessageWidgetDataPatch{PublicData: &public, ChatData: &chat},
	}
	res, err := channels.EditChannelMessage(ctx, patchReq)
	if err != nil {
		t.Fatalf("EditChannelMessage(WidgetData): %v", err)
	}
	if res.Event.Type != domain.ChannelUpdateWidgetData {
		t.Fatalf("event type = %q, want channel_widget_data", res.Event.Type)
	}
	widget := res.Message.Media.Widget
	if widget == nil || widget.PublicData != "b" || widget.PublicDataHash != domain.WidgetDataHash("b") || widget.ChatData != "chat-b" {
		t.Fatalf("widget data not patched: %+v", widget)
	}
	if res.Message.Body != "widget" || res.Message.EditDate != 0 {
		t.Fatalf("body/edit_date changed: body=%q edit_date=%d", res.Message.Body, res.Message.EditDate)
	}

	// The durable event is accepted by the whitelist (migration 20261106030000).
	var eventType string
	if err := pool.QueryRow(ctx, `SELECT event_type FROM channel_update_events WHERE channel_id = $1 ORDER BY pts DESC LIMIT 1`, channelID).Scan(&eventType); err != nil {
		t.Fatalf("read latest channel event: %v", err)
	}
	if eventType != string(domain.ChannelUpdateWidgetData) {
		t.Fatalf("latest channel event type = %q, want channel_widget_data", eventType)
	}

	// History reads back the patched widget data with body/edit_date unchanged.
	hist, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 10})
	if err != nil {
		t.Fatalf("list channel history: %v", err)
	}
	var found bool
	for _, m := range hist.Messages {
		if m.ID == sent.Message.ID {
			found = true
			if m.Media == nil || m.Media.Widget == nil || m.Media.Widget.PublicData != "b" || m.Media.Widget.ChatData != "chat-b" {
				t.Fatalf("history widget data not patched: %+v", m.Media)
			}
			if m.Body != "widget" || m.EditDate != 0 {
				t.Fatalf("history body/edit_date changed: body=%q edit_date=%d", m.Body, m.EditDate)
			}
		}
	}
	if !found {
		t.Fatalf("message %d not in history", sent.Message.ID)
	}

	// Idempotent.
	if _, err := channels.EditChannelMessage(ctx, patchReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("reapply err = %v, want ErrMessageNotModified", err)
	}
}
