package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStoreResolveWebPageIntegration verifies the channel WebPageResolve branch against live Postgres:
// media replaced in place with done, body/edit_date unchanged, the durable channel_web_page event accepted by the whitelist (migration 0016),
// history read back as done, and idempotency. Requires TELESRV_TEST_POSTGRES_DSN.
func TestChannelStoreResolveWebPageIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 51, Phone: "+1668" + suffix + "11", FirstName: "ChWPOwner"})
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
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "ChWP " + suffix, Megagroup: true, Date: 1700000300})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID = created.Channel.ID

	const url = "https://example.com/c"
	urlHash := domain.WebPageURLHash(url)
	sent, err := channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: owner.ID, ChannelID: channelID, RandomID: 9911, Message: "see " + url, Date: 1700000301,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStatePending, ID: urlHash, URL: url}},
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}

	done := &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: urlHash, URL: url, DisplayURL: "example.com", Title: "Example"}}
	resolveReq := domain.EditChannelMessageRequest{UserID: owner.ID, ChannelID: channelID, ID: sent.Message.ID, Media: done, WebPageResolve: true, ExpectedWebPageID: urlHash}
	res, err := channels.EditChannelMessage(ctx, resolveReq)
	if err != nil {
		t.Fatalf("EditChannelMessage(WebPageResolve): %v", err)
	}
	if res.Event.Type != domain.ChannelUpdateWebPage {
		t.Fatalf("event type = %q, want channel_web_page", res.Event.Type)
	}
	if res.Message.Media == nil || res.Message.Media.WebPage == nil || res.Message.Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("message media not resolved: %+v", res.Message.Media)
	}
	if res.Message.Body != "see "+url || res.Message.EditDate != 0 {
		t.Fatalf("body/edit_date changed: body=%q edit_date=%d", res.Message.Body, res.Message.EditDate)
	}

	// The durable event is accepted by the whitelist (migration 0016) + the type is correct.
	var eventType string
	if err := pool.QueryRow(ctx, `SELECT event_type FROM channel_update_events WHERE channel_id = $1 ORDER BY pts DESC LIMIT 1`, channelID).Scan(&eventType); err != nil {
		t.Fatalf("read latest channel event: %v", err)
	}
	if eventType != string(domain.ChannelUpdateWebPage) {
		t.Fatalf("latest channel event type = %q, want channel_web_page", eventType)
	}

	// History reads back media=done, body/edit_date unchanged.
	hist, err := channels.ListChannelHistory(ctx, owner.ID, domain.ChannelHistoryFilter{ChannelID: channelID, Limit: 10})
	if err != nil {
		t.Fatalf("list channel history: %v", err)
	}
	var found bool
	for _, m := range hist.Messages {
		if m.ID == sent.Message.ID {
			found = true
			if m.Media == nil || m.Media.WebPage == nil || m.Media.WebPage.State != domain.MessageWebPageStateDone {
				t.Fatalf("history media not resolved: %+v", m.Media)
			}
			if m.Body != "see "+url || m.EditDate != 0 {
				t.Fatalf("history body/edit_date changed: body=%q edit_date=%d", m.Body, m.EditDate)
			}
		}
	}
	if !found {
		t.Fatalf("message %d not in history", sent.Message.ID)
	}

	// Idempotent.
	if _, err := channels.EditChannelMessage(ctx, resolveReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("re-resolve err = %v, want ErrMessageNotModified", err)
	}
}
