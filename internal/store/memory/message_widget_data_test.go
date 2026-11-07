package memory

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

// TestMessageStorePatchWidgetDataSwapsDataOnly verifies the private WidgetData
// patch mode: only the widget's public/chat data changes, body/edit_date are
// untouched, the event is message_widget_data, both boxes stay consistent and
// reapplying the same data is a no-op.
func TestMessageStorePatchWidgetDataSwapsDataOnly(t *testing.T) {
	ctx := context.Background()
	dialogs := NewDialogStore()
	messages := NewMessageStore(dialogs)
	senderID := int64(1000000221)
	recipientID := int64(1000000222)
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: recipientID}

	sent, err := messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
		SenderUserID: senderID, RecipientUserID: recipientID, RandomID: 221,
		Message: "widget", Date: 1700000100,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
			WidgetID: 9, PublicData: "a", PublicDataHash: domain.WidgetDataHash("a"), ChatData: "chat-a",
		}},
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}

	public := "b"
	chat := "chat-b"
	patchReq := domain.EditMessageRequest{
		OwnerUserID: senderID, Peer: peer, ID: sent.SenderMessage.ID,
		WidgetData: &domain.MessageWidgetDataPatch{PublicData: &public, ChatData: &chat},
	}
	res, err := messages.EditMessage(ctx, patchReq)
	if err != nil {
		t.Fatalf("EditMessage(WidgetData): %v", err)
	}
	if len(res.Edited) != 2 {
		t.Fatalf("edited boxes = %d, want 2", len(res.Edited))
	}
	self := res.Self()
	if self.Event.Type != domain.UpdateEventMessageWidgetData {
		t.Fatalf("event type = %q, want message_widget_data", self.Event.Type)
	}
	if self.Event.Pts == 0 {
		t.Fatal("event pts = 0, want a reserved account pts")
	}
	widget := self.Message.Media.Widget
	if widget == nil || widget.PublicData != "b" || widget.PublicDataHash != domain.WidgetDataHash("b") || widget.ChatData != "chat-b" {
		t.Fatalf("widget data not patched: %+v", widget)
	}
	if self.Message.Body != "widget" || self.Message.EditDate != 0 {
		t.Fatalf("body/edit_date changed: body=%q edit_date=%d", self.Message.Body, self.Message.EditDate)
	}

	// The recipient's box is patched too.
	rh, err := messages.ListByUser(ctx, recipientID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: senderID}, Limit: 10})
	if err != nil {
		t.Fatalf("recipient history: %v", err)
	}
	if len(rh.Messages) != 1 || rh.Messages[0].Media.Widget == nil || rh.Messages[0].Media.Widget.PublicData != "b" {
		t.Fatalf("recipient widget data not patched: %+v", rh.Messages)
	}

	if _, err := messages.EditMessage(ctx, patchReq); !errors.Is(err, domain.ErrMessageNotModified) {
		t.Fatalf("reapply err = %v, want ErrMessageNotModified", err)
	}
}
