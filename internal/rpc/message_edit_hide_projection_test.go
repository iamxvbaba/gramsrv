package rpc

import (
	"testing"

	"github.com/gotd/td/tg"
	"telesrv/internal/domain"
)

func TestMessageProjectionSetsEditHide(t *testing.T) {
	projected, ok := tgMessage(domain.Message{
		ID:         5,
		Peer:       domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		Body:       "streamed",
		EditDate:   1700000400,
		HideEdited: true,
	}).(*tg.Message)
	if !ok {
		t.Fatalf("tgMessage = %T, want *tg.Message", projected)
	}
	if projected.EditDate != 1700000400 || !projected.EditHide {
		t.Fatalf("projected edit fields = edit_date %d edit_hide %v", projected.EditDate, projected.EditHide)
	}

	plain := tgMessage(domain.Message{
		ID:       6,
		Peer:     domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		Body:     "edited",
		EditDate: 1700000401,
	}).(*tg.Message)
	if plain.EditHide {
		t.Fatal("plain edited message should not hide edited badge")
	}
}
