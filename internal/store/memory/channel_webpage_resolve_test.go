package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestChannelStoreResolveWebPage verifies the channel WebPageResolve mode: only media is
// swapped, body/edit_date are untouched, the event is channel_web_page, and it is idempotent.
func TestChannelStoreResolveWebPage(t *testing.T) {
	st := NewChannelStore()
	ctx := context.Background()
	const creator = int64(1000000301)
	const url = "https://example.com/c"
	urlHash := domain.WebPageURLHash(url)

	created, err := st.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: creator, Title: "WP", Megagroup: true, Date: 1700000000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID

	sent, err := st.SendChannelMessage(ctx, domain.SendChannelMessageRequest{
		UserID: creator, ChannelID: channelID, Message: "see " + url, Date: 1700000000,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStatePending, ID: urlHash, URL: url}},
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}

	done := &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: urlHash, URL: url, Title: "Example"}}
	resolveReq := domain.EditChannelMessageRequest{
		UserID: creator, ChannelID: channelID, ID: sent.Message.ID,
		Media: done, WebPageResolve: true, ExpectedWebPageID: urlHash,
	}
	res, err := st.EditChannelMessage(ctx, resolveReq)
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

	if _, err := st.EditChannelMessage(ctx, resolveReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("re-resolve err = %v, want ErrMessageNotModified", err)
	}

	// A wrong expectedID -> no replacement.
	_, err = st.EditChannelMessage(ctx, domain.EditChannelMessageRequest{
		UserID: creator, ChannelID: channelID, ID: sent.Message.ID,
		Media:          &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: 999, URL: url}},
		WebPageResolve: true, ExpectedWebPageID: 999,
	})
	if !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("wrong-id resolve err = %v, want ErrMessageNotModified (already resolved, not pending)", err)
	}
}
