package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestMessageStoreResolveWebPageSwapsMediaOnly verifies that WebPageResolve mode swaps only
// media and does not touch body/entities/edit_date (it does not mark "edited"), that both boxes
// stay consistent, that the event is web_page, and that it is idempotent.
func TestMessageStoreResolveWebPageSwapsMediaOnly(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000201)
	recipientID := int64(1000000202)
	const url = "https://example.com/x"
	urlHash := domain.WebPageURLHash(url)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipientID}

	pending := &domain.MessageMedia{
		Kind:    domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStatePending, ID: urlHash, URL: url},
	}
	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: senderID, RecipientUserID: recipientID, RandomID: 201,
		Message: "see " + url, Date: 1700000100, Media: pending,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}

	done := &domain.MessageMedia{
		Kind:    domain.MessageMediaKindWebPage,
		WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: urlHash, URL: url, Title: "Example"},
	}
	resolveReq := domain.EditMessageRequest{
		OwnerUserID:       senderID,
		Peer:              peer,
		ID:                sent.SenderMessage.ID,
		Media:             done,
		WebPageResolve:    true,
		ExpectedWebPageID: urlHash,
	}
	res, err := messages.EditMessage(ctx, resolveReq)
	if err != nil {
		t.Fatalf("EditMessage(WebPageResolve): %v", err)
	}
	if len(res.Edited) != 2 {
		t.Fatalf("edited boxes = %d, want 2", len(res.Edited))
	}
	self := res.Self()
	if self.Event.Type != domain.UpdateEventWebPage {
		t.Fatalf("event type = %q, want web_page", self.Event.Type)
	}
	if self.Message.Media == nil || self.Message.Media.WebPage == nil || self.Message.Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("self media not resolved: %+v", self.Message.Media)
	}
	if self.Message.Body != "see "+url {
		t.Fatalf("body changed to %q, want unchanged", self.Message.Body)
	}
	if self.Message.EditDate != 0 {
		t.Fatalf("edit_date = %d, want 0 (no 'edited' marker)", self.Message.EditDate)
	}

	// The recipient's box is also replaced with done.
	rh, err := messages.ListByUser(ctx, recipientID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: senderID}, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(rh.Messages) != 1 || rh.Messages[0].Media == nil || rh.Messages[0].Media.WebPage == nil || rh.Messages[0].Media.WebPage.State != domain.MessageWebPageStateDone {
		t.Fatalf("recipient media not resolved: %+v", rh.Messages)
	}

	// Idempotent: resolving again after it is already resolved → ErrMessageNotModified.
	if _, err := messages.EditMessage(ctx, resolveReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("re-resolve err = %v, want ErrMessageNotModified", err)
	}
}

// TestMessageStoreResolveWebPageWrongIDNoop verifies that nothing is replaced when expectedID does not match.
func TestMessageStoreResolveWebPageWrongIDNoop(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000211)
	recipientID := int64(1000000212)
	const url = "https://example.com/y"
	urlHash := domain.WebPageURLHash(url)

	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: senderID, RecipientUserID: recipientID, RandomID: 211, Message: url, Date: 1700000100,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStatePending, ID: urlHash, URL: url}},
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	_, err = messages.EditMessage(ctx, domain.EditMessageRequest{
		OwnerUserID:       senderID,
		Peer:              domain.Peer{Type: domain.PeerTypeUser, ID: recipientID},
		ID:                sent.SenderMessage.ID,
		Media:             &domain.MessageMedia{Kind: domain.MessageMediaKindWebPage, WebPage: &domain.MessageWebPage{State: domain.MessageWebPageStateDone, ID: 999, URL: url}},
		WebPageResolve:    true,
		ExpectedWebPageID: 999, // does not match the placeholder id
	})
	if !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("wrong-id resolve err = %v, want ErrMessageNotModified", err)
	}
}
