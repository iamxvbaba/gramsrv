package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// updateMessageWidgetData is a durable account-pts event projected from the
// stored widget media.
func TestLayer233MessageWidgetDataProjects(t *testing.T) {
	msg := domain.Message{
		ID: 5, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 9},
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindWidget, Widget: &domain.MessageWidget{
			PublicData: "public", PublicDataHash: 77, ChatData: "chat",
		}},
	}
	event := domain.UpdateEvent{Type: domain.UpdateEventMessageWidgetData, Message: msg, Pts: 12, PtsCount: 1}
	update, ok := tgOtherUpdateFromEvent(event).(*tg.UpdateMessageWidgetData)
	if !ok {
		t.Fatalf("projection = %T, want *tg.UpdateMessageWidgetData", tgOtherUpdateFromEvent(event))
	}
	if update.MsgID != 5 || update.Pts != 12 || update.PtsCount != 1 {
		t.Fatalf("update = %#v", update)
	}
	if data, ok := update.GetPublicData(); !ok || data != "public" {
		t.Fatalf("public_data = %q, %v", data, ok)
	}
	if hash, ok := update.GetPublicDataHash(); !ok || hash != 77 {
		t.Fatalf("public_data_hash = %d, %v", hash, ok)
	}
	if data, ok := update.GetChatData(); !ok || data != "chat" {
		t.Fatalf("chat_data = %q, %v", data, ok)
	}
}
