package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestMessageStoreResolveWebPageIntegration verifies the WebPageResolve branch against a live Postgres:
// both boxes swap media to done in place, body/edit_date stay unchanged (no "edited" marker), the durable
// web_page event rebuilds done media via a box JOIN (reused by difference), and it is idempotent. Requires TELESRV_TEST_POSTGRES_DSN.
func TestMessageStoreResolveWebPageIntegration(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender, err := users.Create(ctx, domain.User{AccessHash: 41, Phone: "+1667" + suffix + "11", FirstName: "WPSender"})
	if err != nil {
		t.Fatalf("create sender: %v", err)
	}
	recipient, err := users.Create(ctx, domain.User{AccessHash: 42, Phone: "+1667" + suffix + "12", FirstName: "WPRecipient"})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{sender.ID, recipient.ID})
	})

	const url = "https://example.com/x"
	urlHash := domain.WebPageURLHash(url)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipient.ID}

	messages := NewMessageStore(pool)
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: sender.ID, RecipientUserID: recipient.ID, RandomID: 778811,
		Message: "see " + url, Date: 1700000300,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStatePending, ID: urlHash, URL: url}},
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}

	done := &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: urlHash, URL: url, DisplayURL: "example.com", Title: "Example"}}
	resolveReq := domain.EditMessageRequest{
		OwnerUserID: sender.ID, Peer: peer, ID: sent.SenderMessage.ID,
		Media: done, WebPageResolve: true, ExpectedWebPageID: urlHash,
	}
	res, err := messages.EditMessage(ctx, resolveReq)
	if err != nil {
		t.Fatalf("EditMessage(WebPageResolve): %v", err)
	}
	self := res.Self()
	if self.Event.Type != domain.UpdateEventWebPage {
		t.Fatalf("self event type = %q, want web_page", self.Event.Type)
	}
	if self.Message.Media == nil || self.Message.Media.WebPage == nil || self.Message.Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("self media not resolved: %+v", self.Message.Media)
	}
	if self.Message.Body != "see "+url || self.Message.EditDate != 0 {
		t.Fatalf("body/edit_date changed: body=%q edit_date=%d", self.Message.Body, self.Message.EditDate)
	}

	// Recipient box: media=done, body/edit_date unchanged.
	rh, err := messages.ListByUser(ctx, recipient.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID}, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(rh.Messages) != 1 || rh.Messages[0].Media == nil || rh.Messages[0].Media.WebPage == nil || rh.Messages[0].Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("recipient media not resolved: %+v", rh.Messages)
	}
	if rh.Messages[0].Body != "see "+url || rh.Messages[0].EditDate != 0 {
		t.Fatalf("recipient body/edit_date changed: %+v", rh.Messages[0])
	}

	// Durable event: difference reuse rebuilds done media via a box JOIN.
	recipientEvents, err := NewUpdateEventStore(pool).ListAfter(ctx, recipient.ID, 0, 10)
	if err != nil {
		t.Fatalf("recipient events: %v", err)
	}
	last := recipientEvents[len(recipientEvents)-1]
	if last.Type != domain.UpdateEventWebPage || last.Message.Media == nil || last.Message.Media.WebPage == nil || last.Message.Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("recipient durable web_page event = %+v, want done media", last)
	}

	// Idempotent: resolving again -> ErrMessageNotModified.
	if _, err := messages.EditMessage(ctx, resolveReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("re-resolve err = %v, want ErrMessageNotModified", err)
	}
}
